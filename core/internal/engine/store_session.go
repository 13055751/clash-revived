package engine

// store_session.go — 把内存会话落成持久 profile。
//
// 背景（2026-10-31 真机实证）：
// UI 的「保存」动作序列是
//   PUT /profiles/sessions/{id}/attributes   （写入名称/URL）
//   GET /profiles/sessions/{id}/store        （轮询，等 Completed）
// 而原实现里：
//   1) /store 端点根本不存在 → 404 → UI 无限重试（表现为"点保存没反应"）
//   2) 即使有 /store，会话也从未写进 profiles 表 → 配置永远不会持久化
//
// 本文件补上第二半：把会话的内容写进 store.db 的 profiles 表。

import (
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrSessionNotStored 表示会话内容还没准备好（或已失败），无法落库。
var ErrSessionNotStored = errors.New("engine: session has no content to store")

// ErrSessionNotFound 表示会话不存在或已过期（会话是内存对象，进程重启即失效）。
var ErrSessionNotFound = errors.New("engine: session not found")

// StoreSession 把会话落成 profile 并返回其 UUID。
//
// 语义：
//   - SessionTypeEdit 且有 ProfileID：更新既有 profile 的 content/version/updatedAt
//   - 其它类型：新建一行 profile
//   - 名称缺省时用 remoteURL 的 host 或文件名兜底（UI 那边常给空名）
//
// 成功后把会话 stage 置 Completed，供 UI 轮询 /store 拿到结果。
func (e *Engine) StoreSession(id string) (string, error) {
	rec, ok := e.sessionStore.Get(id)
	if !ok {
		return "", ErrSessionNotFound
	}
	snap := rec.snapshot()

	// 内容：新 URL 会话此时应已由 background 下载并缓存到 Payload。
	payload := snap.Payload
	if len(payload) == 0 {
		// 后台还没跑完或失败了——让 UI 继续等，不要伪造成功。
		return "", ErrSessionNotStored
	}

	name := snap.Attributes.Name
	if name == "" {
		name = fallbackProfileName(snap.Attributes.RemoteURL, snap.Spec.FileName)
	}

	storeMu.Lock()
	defer storeMu.Unlock()

	db, err := e.openStoreRW()
	if err != nil {
		return "", err
	}
	defer db.Close()

	now := time.Now().UTC()

	// Edit 会话：更新既有行。
	if snap.Spec.Type == SessionTypeEdit && snap.Spec.ProfileID != "" {
		v1, v2 := uuidVariants(snap.Spec.ProfileID)[0], uuidVariants(snap.Spec.ProfileID)[1]
		res, err := db.Exec(
			`UPDATE profiles SET content = ?, version = version + 1, updatedAt = ? WHERE id = ? OR id = ?`,
			payload, now.Format(time.RFC3339), v1, v2)
		if err != nil {
			return "", fmt.Errorf("update profile: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return "", ErrProfileNotFound
		}
		e.markSessionCompleted(id)
		slog.Info("session stored as profile (edit)", "session", id, "profile", snap.Spec.ProfileID)
		return snap.Spec.ProfileID, nil
	}

	// 新建：生成 UUID 并插入。
	newID := newSessionID() // 同格式 UUIDv4，crypto/rand 生成
	// modeOverride 由 UI 在会话配置里给，SessionSpec 没有这个字段，先留空。
	var modeOverride any
	_, err = db.Exec(
		`INSERT INTO profiles (id, version, name, remoteURL, updateExpr, updatedAt, content, modeOverride)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		newID, 1, name, snap.Attributes.RemoteURL, snap.Attributes.UpdateExpr,
		now.Format(time.RFC3339), payload, modeOverride)
	if err != nil {
		return "", fmt.Errorf("insert profile: %w", err)
	}

	e.markSessionCompleted(id)
	slog.Info("session stored as profile (new)", "session", id, "profile", newID, "name", name, "bytes", len(payload))
	return newID, nil
}

// markSessionCompleted 把会话 stage 推到 Completed，供 UI 轮询。
func (e *Engine) markSessionCompleted(id string) {
	if rec, ok := e.sessionStore.Get(id); ok {
		rec.mutate(func(s *Session) {
			s.Status.Stage = StoreStageCompleted
			s.Status.Error = ""
		})
	}
}

// fallbackProfileName 在 UI 没给名称时兜底。
//
// 真机观察到「名称」字段是 32 位十六进制（URL 的 hash），说明 UI 有时会把
// 派生的 hash 当名字传下来。这种名字对用户没有意义，但也不该覆盖——
// 只有空串才兜底。
func fallbackProfileName(remoteURL, fileName string) string {
	if fileName != "" {
		return fileName
	}
	if remoteURL != "" {
		if host := hostOf(remoteURL); host != "" {
			return host
		}
		return "订阅" + remoteURL[len(remoteURL)-minInt(8, len(remoteURL)):]
	}
	return "未命名配置"
}

// hostOf 从 URL 里取 host，失败返回空串（不引 net/url 只为这一处）。
func hostOf(raw string) string {
	rest := raw
	for _, p := range []string{"https://", "http://"} {
		if len(rest) > len(p) && rest[:len(p)] == p {
			rest = rest[len(p):]
			break
		}
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == ':' || rest[i] == '?' {
			return rest[:i]
		}
	}
	return rest
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
