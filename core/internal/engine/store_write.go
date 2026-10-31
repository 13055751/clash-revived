package engine

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/metacubex/mihomo/hub/executor"
)

// ErrProfileNotFound 表示库里没有这个 profile。
var ErrProfileNotFound = errors.New("engine: profile not found")

// ErrProfileNoRemoteURL 表示该 profile 没有 remoteURL，无法"重新下载"。
var ErrProfileNoRemoteURL = errors.New("engine: profile has no remoteURL")

// openStoreRW 以读写模式打开（写路径专用）。每次开新连接，避免长事务。
func (e *Engine) openStoreRW() (*sql.DB, error) {
	path := e.DatabasePath()
	if path == "" {
		return nil, ErrStorePathNotConfigured
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// DeleteProfile 删除一个 profile（依赖 ON DELETE CASCADE 清理子表）。
//
// 注意：必须确保连接上 foreign_keys=1，否则 CASCADE 不生效、会留孤儿行。
func (e *Engine) DeleteProfile(id string) error {
	if id == "" {
		return ErrProfileNotFound
	}
	storeMu.Lock()
	defer storeMu.Unlock()

	db, err := e.openStoreRW()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// id 在库里可能是 TEXT 也可能是 16 字节 BLOB，两种形态都试。
	variants := uuidVariants(id)
	res, err := db.Exec(`DELETE FROM profiles WHERE id = ? OR id = ?`, variants[0], variants[1])
	if err != nil {
		return fmt.Errorf("delete profile: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete profile: %w", err)
	}
	if n == 0 {
		return ErrProfileNotFound
	}
	return nil
}

// RefreshProfile 对应 PUT /profiles/{id}：**触发重新下载**，不是写入客户端内容。
//
// 证据：`Profiles.update(UUID)` → PUT '/profiles/' + id，响应类型 Unit，
// 且 `Profiles$update$$inlined$request$default$1` 没有 $body 字段（对比 patchConfig 有）。
//
// 只有 content / version / updatedAt 三列会被改；name / updateExpr / modeOverride
// 等用户元数据一律不动。下载或解析失败时**不写库**（失败不覆盖旧内容）。
func (e *Engine) RefreshProfile(id string) error {
	if id == "" {
		return ErrProfileNotFound
	}
	storeMu.Lock()
	defer storeMu.Unlock()

	db, err := e.openStoreRW()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	var (
		remoteURL sql.NullString
		version   sql.NullInt64
	)
	variants := uuidVariants(id)
	row := db.QueryRow(`SELECT remoteURL, version FROM profiles WHERE id = ? OR id = ?`,
		variants[0], variants[1])
	if err := row.Scan(&remoteURL, &version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProfileNotFound
		}
		return fmt.Errorf("load profile: %w", err)
	}
	if strings.TrimSpace(remoteURL.String) == "" {
		return ErrProfileNoRemoteURL
	}

	body, err := fetchURL(remoteURL.String)
	if err != nil {
		return err
	}
	// 先验证是合法配置再落库：坏内容不该覆盖用户当前可用的配置。
	if _, err := executor.ParseWithBytes(body); err != nil {
		return fmt.Errorf("downloaded config is invalid: %w", err)
	}

	next := version.Int64 + 1
	_, err = db.Exec(`UPDATE profiles SET content = ?, version = ?, updatedAt = ? WHERE id = ? OR id = ?`,
		body, next, time.Now().UTC().Format("2006-01-02 15:04:05.999999999-07:00"),
		variants[0], variants[1])
	if err != nil {
		return fmt.Errorf("update profile: %w", err)
	}
	return nil
}

// SaveProxySelection 持久化 selector 选择（proxy_selections 表）。
//
// 没有激活 profile（ProfileID 为空或全零）时**直接跳过**：写一行零 UUID 的垃圾数据
// 比丢一次选择更糟。等"激活 profile"流程接入后，这里自然会拿到真 ID。
func (e *Engine) SaveProxySelection(group, proxyName string) error {
	id := e.ProfileID()
	if id == "" || strings.Trim(id, "0-") == "" {
		// 必须留痕：否则用户重启后发现"选择没了"，却没有任何线索指向
		// "当时根本没有激活 profile"。等 /executor/config 的 activeProfile 接上就不会再走到这里。
		slog.Warn("skip persisting proxy selection: no active profile", "group", group, "proxy", proxyName, "profileId", id)
		return nil
	}
	if group == "" || proxyName == "" {
		return nil
	}
	storeMu.Lock()
	defer storeMu.Unlock()

	db, err := e.openStoreRW()
	if err != nil {
		// 写选择失败不该让端点报错（用户的选择已经在内存里生效了）。
		slog.Warn("persist proxy selection skipped", "err", err, "group", group, "proxy", proxyName)
		return nil
	}
	defer func() { _ = db.Close() }()

	// 用库里 profiles 行的真实形态（TEXT 或 BLOB）作为外键值，避免类型不匹配
	// 导致外键约束静默失败。
	var rawID any
	if err := db.QueryRow(`SELECT id FROM profiles WHERE id = ? OR id = ?`,
		uuidVariants(id)[0], uuidVariants(id)[1]).Scan(&rawID); err != nil {
		slog.Debug("skip persisting proxy selection: profile not in store", "id", id, "err", err)
		return nil
	}

	_, err = db.Exec(`INSERT INTO proxy_selections (profileID, groupName, proxyName) VALUES (?, ?, ?)
ON CONFLICT(profileID, groupName) DO UPDATE SET proxyName = excluded.proxyName`, rawID, group, proxyName)
	if err != nil {
		slog.Warn("persist proxy selection failed", "err", err, "group", group)
	}
	return nil
}

// ProxySelection 是一条持久化的选择记录（读出来供调试/将来 apply 时用）。
type ProxySelection struct {
	GroupName string
	ProxyName string
}

// LoadProxySelections 读取当前激活 profile 的选择记录。
func (e *Engine) LoadProxySelections() ([]ProxySelection, error) {
	id := e.ProfileID()
	if id == "" || strings.Trim(id, "0-") == "" {
		return nil, nil
	}
	storeMu.Lock()
	defer storeMu.Unlock()

	db, err := e.openStoreRW()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`SELECT groupName, proxyName FROM proxy_selections
WHERE profileID = ? OR profileID = ?`, uuidVariants(id)[0], uuidVariants(id)[1])
	if err != nil {
		return nil, fmt.Errorf("query proxy_selections: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ProxySelection, 0, 8)
	for rows.Next() {
		var s ProxySelection
		if err := rows.Scan(&s.GroupName, &s.ProxyName); err != nil {
			return nil, fmt.Errorf("scan proxy_selection: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// uuidVariants 给出 UUID 在 SQLite 里的两种可能形态（TEXT / 16 字节 BLOB）。
// 解析失败时两种都用原文，至少不会崩。
func uuidVariants(id string) [2]any {
	hexOnly := strings.ReplaceAll(id, "-", "")
	if len(hexOnly) == 32 {
		if b, err := hex.DecodeString(hexOnly); err == nil {
			return [2]any{id, b}
		}
	}
	return [2]any{id, id}
}

// fetchURL 下载订阅内容（给 session 与 profile 刷新共用）。
func fetchURL(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "clash.meta")
	client := &http.Client{Timeout: sessionProviderInitTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: http %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}
