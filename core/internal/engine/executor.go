package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/hub/executor"
)

// ExecutorConfig 对应 Executor.Config。
type ExecutorConfig struct {
	ActiveProfile            string
	ExternalController       string
	ExternalControllerSecret string
}

// ErrProfileNoContent 表示 profile 行存在但没有 content（还没下载过）。
var ErrProfileNoContent = errors.New("engine: profile has no content")

// ExecutorConfig 返回当前 executor 配置。
func (e *Engine) ExecutorConfig() ExecutorConfig {
	addr, secret := e.ExternalController()
	return ExecutorConfig{
		ActiveProfile:            e.ProfileID(),
		ExternalController:       addr,
		ExternalControllerSecret: secret,
	}
}

// SetExternalController 设置 external-controller（只记录，不真的监听）。
func (e *Engine) SetExternalControllerConfig(addr, secret string) {
	e.SetExternalController(addr, secret)
}

// 关于 providers 缓存（P1 阶段故意不做，风险记录在此）：
//
// 切换 profile 时我没有把 store 里 providers.content 喂给 mihomo，mihomo 会自己
// 去下载 provider。**这会形成一个依赖环**：如果某个 provider 是远程 URL，而访问它
// 本身需要代理，那么"tunnel 刚起来/还没起来 → provider 下载失败 → 没有节点可用"。
// 订阅本身要翻墙才能拿到，是这类客户端的经典坑。
//
// 症状：切换订阅后长时间无节点、provider 报下载失败。
// 到时候第一件事是**补喂缓存**，不要去调 mihomo 的超时参数。
// 注意：mihomo 的 provider 缓存是文件级的（home 目录下的 providers 缓存文件），
// 补喂大概率是写文件而不是调某个内存 API —— 实现前先确认，别假设。
//
// SetActiveProfile 切换激活 profile，并且**立刻重载 tunnel**。
//
// 证据：`ViewModel$select$1` 只发一条 `PATCH /executor/config {activeProfile}`，
// 没有第二步，所以「只记录、下次启动生效」在 UI 上就是「点了没反应」。
//
// 顺序刻意做成「先解析、后停旧 tunnel」：
//  1. 读 content；读不到 → 错误，tunnel 不动
//  2. ParseWithBytes 校验；失败 → 错误，tunnel 不动
//  3. 校验通过才 Stop → SetProfileID → Start
//  4. Start 失败 → 把 ProfileID 回滚，避免「Status 说已切到 B、实际还在跑 A」
func (e *Engine) SetActiveProfile(id string) error {
	if id == "" {
		return ErrProfileNotFound
	}
	slog.Info("switch[1/5] reading profile content", "id", id)
	content, err := e.profileContent(id)
	if err != nil {
		return err
	}

	// 先校验：解析失败时**绝不能**先停旧 tunnel（否则用户断网且退不回去）。
	slog.Info("switch[2/5] parsing config", "bytes", len(content))
	cfg, err := executor.ParseWithBytes(content)
	slog.Info("switch[2/5] parse done", "err", err)
	if err != nil {
		return fmt.Errorf("profile %s is not a valid config: %w", id, err)
	}
	// 再检查"起得来吗"：这类前置条件必须在 Stop 之前查出来，
	// 而不是停了旧 tunnel 才发现新的起不来。
	slog.Info("switch[3/5] precheck")
	if err := precheckStart(cfg); err != nil {
		return fmt.Errorf("profile %s cannot start: %w", id, err)
	}

	prev := e.ProfileID()
	wasRunning := e.Running()

	slog.Info("switch[4/5] stopping old tunnel", "wasRunning", wasRunning)
	if wasRunning {
		if err := e.Stop(); err != nil {
			return fmt.Errorf("stop tunnel: %w", err)
		}
	}
	e.SetProfileID(id)
	slog.Info("switch[5/5] starting new tunnel")
	if err := e.Start(content, -1); err != nil {
		slog.Info("switch[5/5] start failed", "err", err)
		// 回滚：状态不能脏。
		e.SetProfileID(prev)
		if wasRunning {
			slog.Error("switch profile failed, previous tunnel is down", "profile", id, "prev", prev, "err", err)
		}
		return fmt.Errorf("apply profile %s: %w", id, err)
	}
	slog.Info("active profile switched", "from", prev, "to", id)
	return nil
}

// profileContent 读 profiles.content。
func (e *Engine) profileContent(id string) ([]byte, error) {
	storeMu.Lock()
	defer storeMu.Unlock()

	db, err := e.openStoreRO()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	variants := uuidVariants(id)
	var content []byte
	err = db.QueryRow(`SELECT content FROM profiles WHERE id = ? OR id = ?`,
		variants[0], variants[1]).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProfileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load profile content: %w", err)
	}
	if len(content) == 0 {
		return nil, ErrProfileNoContent
	}
	return content, nil
}

// openStoreRO 只读打开（读路径专用）。
func (e *Engine) openStoreRO() (*sql.DB, error) {
	path := e.DatabasePath()
	if path == "" {
		return nil, ErrStorePathNotConfigured
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// precheckStart 检查"这份配置现在能不能起来"，用于在停止旧 tunnel 之前拦下必然失败的情况。
//
// 目前只查一条硬依赖：配置要求 tun，但 engine 手里没有宿主给的 tun fd。
// 其它（端口占用等）交给 Start 的真实错误——那些不会让系统失去网络可达性，
// 而停掉旧 tunnel 之后起不来会。
func precheckStart(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	if cfg.General.Tun.Enable && cfg.General.Tun.FileDescriptor <= 0 {
		return errors.New("tun is enabled in config but no tun fd was provided by the host")
	}
	return nil
}
