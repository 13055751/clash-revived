package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
)

// preferenceKeyProfilesConfig 是 /profiles/config 在 preferences 表里的 key。
//
// TODO(联调): key 名没能从二进制里确认（原版 .so 里 preferences 的 key 常量没以可读
// 字符串留下）。联调时用 `SELECT key FROM preferences` 核对真库；若原版用的是别的
// key，这里改名即可——因为我们替换了内核，读写只要自洽就不影响正确性，
// 唯一的损失是"原版里改过的更新设置不会继承过来"（迁移问题，不是正确性问题）。
const preferenceKeyProfilesConfig = "profiles_config"

// defaultProfilesConfig 是读不到配置时的返回值。
//
// 注意必须返回**完整对象**而不是 0 值结构体被省略：Profiles$Config 的字段是必填的，
// kotlinx 拿到 `{}` 会抛 MissingFieldException。
func defaultProfilesConfig() ProfilesConfig {
	return ProfilesConfig{}
}

// LoadProfilesConfig 读 /profiles/config。读不到（无表 / 无 key / 值损坏）一律回默认值。
//
// 这条路径**不返回错误**：设置页不该因为库还没接好就打不开。
// 但每种退化都会打一条 warn，避免"默认值"被误当成"用户真的这么配的"。
func (e *Engine) LoadProfilesConfig() (ProfilesConfig, error) {
	if e.DatabasePath() == "" {
		slog.Warn("profiles config: store path not configured, fallback to defaults")
		return defaultProfilesConfig(), nil
	}

	storeMu.Lock()
	defer storeMu.Unlock()

	db, err := sql.Open("sqlite", "file:"+e.DatabasePath()+"?mode=ro&_pragma=foreign_keys(1)")
	if err != nil {
		slog.Warn("profiles config: open store failed, fallback to defaults", "err", err)
		return defaultProfilesConfig(), nil
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	ok, err := storeHasTable(db, "preferences")
	if err != nil || !ok {
		slog.Warn("profiles config: no preferences table, fallback to defaults", "err", err)
		return defaultProfilesConfig(), nil
	}

	var raw []byte
	err = db.QueryRow(`SELECT value FROM preferences WHERE key = ?`, preferenceKeyProfilesConfig).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		// 首次运行：没有这条记录是正常的，不算异常。
		return defaultProfilesConfig(), nil
	}
	if err != nil {
		slog.Warn("profiles config: read failed, fallback to defaults", "err", err)
		return defaultProfilesConfig(), nil
	}

	var cfg ProfilesConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		slog.Warn("profiles config: stored value is corrupt, fallback to defaults", "err", err)
		return defaultProfilesConfig(), nil
	}
	return cfg, nil
}

// SaveProfilesConfig 写 /profiles/config（upsert 到 preferences）。
func (e *Engine) SaveProfilesConfig(cfg ProfilesConfig) error {
	if e.DatabasePath() == "" {
		return ErrStorePathNotConfigured
	}

	storeMu.Lock()
	defer storeMu.Unlock()

	db, err := e.openStoreRW()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// 老库可能连 preferences 表都没有：建表是幂等的，先确保存在再写。
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS preferences (key VARCHAR(128) PRIMARY KEY, value BLOB)`); err != nil {
		return fmt.Errorf("ensure preferences table: %w", err)
	}

	payload, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal profiles config: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO preferences (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, preferenceKeyProfilesConfig, payload); err != nil {
		return fmt.Errorf("save profiles config: %w", err)
	}
	return nil
}
