package engine

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	// 纯 Go 的 SQLite 实现（CGO_ENABLED=0 是硬约束，不能用 mattn/go-sqlite3）。
	_ "modernc.org/sqlite"
)

// ErrStorePathNotConfigured 表示宿主没把 store.db 路径交过来（未接桥 / 老宿主）。
//
// 这种状态下**不能返回空列表**：那会伪装成"用户还没有任何配置"，比报错更难排查。
var ErrStorePathNotConfigured = errors.New("engine: store database path is not configured")

// Profile 是 Profiles.Profile 的领域快照。
//
// 列名映射见 STORE_SCHEMA.md，注意两个坑：
//   - API 里叫 upload/download，库里叫 uploaded/downloaded（多一个 ed）
//   - 这两个是整数（int64），不是字符串
type Profile struct {
	ID              string
	Name            string
	RemoteURL       string
	UpdateExpr      string
	UpdatedAtUnixMS int64 // 0 = 未知
	Upload          int64
	Download        int64
	Total           int64
	ExpireAtUnixMS  int64 // 0 = 未知
}

// SetDatabasePath 是给 engine.Default() 用的包级入口（与 SetHomeDir 对称）。
func SetDatabasePath(path string) { defaultEngine.SetDatabasePath(path) }

// SetDatabasePath 告知 store.db 的**完整文件路径**（含文件名，来自 Bridge initialize）。
// 传空串表示不可用。
func (e *Engine) SetDatabasePath(path string) {
	e.mu.Lock()
	e.dbPath = path
	e.mu.Unlock()
}

// DatabasePath 返回当前的 store.db 路径（空串表示不可用）。
func (e *Engine) DatabasePath() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.dbPath
}

// storeMu 串行化只读查询：SQLite 只读场景也没必要开连接池。
var storeMu sync.Mutex

// Profiles 读取 profiles + user_infos，按名字排序。
//
// 只读：以 mode=ro 打开，物理上禁止误写。写路径（PUT/DELETE）留到 P1c 第二阶段。
func (e *Engine) Profiles() ([]Profile, error) {
	path := e.DatabasePath()
	if path == "" {
		return nil, ErrStorePathNotConfigured
	}

	storeMu.Lock()
	defer storeMu.Unlock()

	db, err := e.openStoreRO()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	ok, err := storeHasTable(db, "profiles")
	if err != nil {
		return nil, err
	}
	if !ok {
		// 老库或空库：没有 profiles 表就当"没有配置"，但要在日志里留痕。
		slog.Warn("store has no profiles table", "path", path)
		return []Profile{}, nil
	}

	rows, err := db.Query(`
SELECT p.id, p.name, p.remoteURL, p.updateExpr, p.updatedAt,
       u.uploaded, u.downloaded, u.total, u.expireAt
FROM profiles p
LEFT JOIN user_infos u ON u.profileID = p.id`)
	if err != nil {
		return nil, fmt.Errorf("query profiles: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Profile, 0, 8)
	for rows.Next() {
		var (
			rawID                       any
			name, remoteURL, updateExpr sql.NullString
			rawUpdatedAt                any
			uploaded, downloaded, total sql.NullInt64
			rawExpireAt                 any
		)
		if err := rows.Scan(&rawID, &name, &remoteURL, &updateExpr, &rawUpdatedAt,
			&uploaded, &downloaded, &total, &rawExpireAt); err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		out = append(out, Profile{
			ID:              decodeUUID(rawID),
			Name:            name.String,
			RemoteURL:       remoteURL.String,
			UpdateExpr:      updateExpr.String,
			UpdatedAtUnixMS: decodeTime(rawUpdatedAt),
			Upload:          uploaded.Int64,
			Download:        downloaded.Int64,
			Total:           total.Int64,
			ExpireAtUnixMS:  decodeTime(rawExpireAt),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profiles: %w", err)
	}
	return out, nil
}

// storeHasTable 用 sqlite_master 判断表是否存在（老库可能只有 imported 表）。
func storeHasTable(db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check table %s: %w", table, err)
	}
	return n > 0, nil
}

// decodeUUID 处理 UUID 的两种落库形态。
//
// DDL 写的是 `id UUID`，但 SQLite 没有 UUID 类型：TEXT（36 字符带横线）与
// BLOB（16 字节）都可能，所以用 any 接再分支，不做假设。
func decodeUUID(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		if len(x) == 16 {
			s := hex.EncodeToString(x)
			return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
		}
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

// decodeTime 把 SQLite 里的时间值归一成 epoch 毫秒。
//
// TIMESTAMP 列在不同 driver/写法下落成 TEXT / INTEGER / REAL 都有，
// 这里按类型分支解析；无法识别时返回 0（未知），不编造。
func decodeTime(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case time.Time:
		// modernc 的 driver 对 TIMESTAMP 列会直接给 time.Time（实测），
		// mattn/go-sqlite3 在开 parseTime 时也是。零值代表"未知"。
		if x.IsZero() {
			return 0
		}
		return x.UnixMilli()
	case int64:
		return normalizeEpoch(x)
	case int:
		return normalizeEpoch(int64(x))
	case float64:
		return normalizeEpoch(int64(x))
	case string:
		return decodeTimeString(x)
	case []byte:
		return decodeTimeString(string(x))
	default:
		return 0
	}
}

// normalizeEpoch 区分秒 / 毫秒：小于 1e12 的当秒（1e12 ms ≈ 2001 年，更小的一定是秒）。
func normalizeEpoch(v int64) int64 {
	if v == 0 {
		return 0
	}
	if v > -1e12 && v < 1e12 {
		return v * 1000
	}
	return v
}

// timeLayouts 覆盖 SQLite 里见过的时间文本格式（go-sqlite3 写的是第一种）。
var timeLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

func decodeTimeString(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// 纯数字文本：按秒/毫秒处理。
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return normalizeEpoch(n)
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	// 解析不出来就是未知，不要拿"现在"顶替。
	return 0
}
