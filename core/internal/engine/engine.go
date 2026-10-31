// Package engine 封装 mihomo 内核的生命周期与状态查询。
//
// 设计约束（见 DEV_SPEC.md）：
//   - 本包是 mihomo 的唯一入口；endpoint 包不直接 import mihomo。
//   - 所有查询在「未启动」状态下必须安全返回零值，端点因此可以无 mihomo 环境下做 httptest。
//   - 不引入「以后可能用到」的抽象：这里只做 CFA 数据面真正要读的东西。
package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/tunnel"
)

// CFA 侧 Executor.Status.Type 的取值（dex 证据：Executor$Status$Type.<clinit> 的
// const-string 顺序为 Idle, Loading, Loaded, Error；R8 已删掉静态常量字段）。
const (
	StatusIdle    = "Idle"
	StatusLoading = "Loading"
	StatusLoaded  = "Loaded"
	StatusError   = "Error"
)

// CFA 侧 General.Config.Mode 的枚举常量。
//
// 注意：CFA 的 Mode 是 dex 里唯一一个带「枚举项序列化名」的枚举
// （General$Config.<clinit> code_item 0x18050c 里 const-string 'direct'/'global'/'rule'/'script'
//
//	作为 names 数组喂给 createAnnotatedEnumSerializer），所以线上 JSON 值是小写；
//
// 此处保持大写的规范名，lowercase 转换只发生在 endpoint 的 /general 处理器里。
const (
	ModeDirect = "Direct"
	ModeGlobal = "Global"
	ModeRule   = "Rule"
	ModeScript = "Script"
)

// Engine 是内核实例。进程内只有一个（Default）。
type Engine struct {
	mu sync.RWMutex

	cfg       []byte         // 最近一次应用的配置原文（/config 端点用）
	parsed    *config.Config // 最近一次解析成功的配置
	state     string         // StatusIdle / StatusLoading / StatusLoaded / StatusError
	lastErr   string
	profileID string // 当前生效 profile 的 UUID；由 Session/Profile 层写入
	startedAt time.Time

	// 监听器运行时状态：mihomo 只管端口，VPN/SystemProxy 由 platform 层（teammate-B）回报。
	listeners map[string]bool

	// 连接计数历史（mihomo 只有活跃连接表，见 stats.go）。
	seenConns   map[string]struct{}
	connHistory int64

	// provider 最后更新时刻的近似值（mihomo 不暴露更新时间，见 providers.go）。
	provMeta map[string]*provMeta

	// 会话存储（内存 map，见 sessions.go）。
	sessionStore sessionStore

	// store.db 的完整路径（含文件名），由宿主 initialize 下发，见 store.go。
	dbPath string

	// /network/* 的内存快照（唯一真相源），见 network.go。
	network NetworkConfig

	// external-controller 由宿主配置注入（mihomo 自己不开这个口子，我们只回报状态）。
	ctrlAddr   string
	ctrlSecret string
	// pendingTunFD：先开 TUN、后激活订阅时暂存的宿主 fd（-1 表示无）。
	pendingTunFD int
}

var (
	defaultEngine = New()
	defaultOnce   sync.Once
)

// Default 返回进程内单例。api.go 与 core.go 通过它共享同一个引擎实例，
// 因此宿主侧不需要任何额外的实例传递。
func Default() *Engine { return defaultEngine }

// New 造一个未启动的引擎。测试用；生产用 Default。
func New() *Engine {
	return &Engine{
		state:        StatusIdle,
		listeners:    map[string]bool{},
		sessionStore: newMemorySessionStore(),
		network:      defaultNetworkConfig(),
		// 必须显式置 -1：Go 零值是 0，而 fd 0 是 stdin。
		// 漏了这行会让 takePendingTun() 返回 0 并被当成"宿主给的 tun fd"，
		// 结果把 stdin 交给 sing-tun —— 真机上表现为界面白屏、引擎崩塌。
		pendingTunFD: -1,
	}
}

// homeDir 只设置一次，供 mihomo 定位 geo 数据与缓存。
var homeDirSet atomic.Bool

// SetHomeDir 告知 mihomo 工作目录（Bridge initialize 的 filesPath）。
// 必须在 Start 之前调用；重复调用忽略后者。
func SetHomeDir(dir string) {
	if dir == "" || homeDirSet.Load() {
		return
	}
	C.SetHomeDir(dir)
	homeDirSet.Store(true)
	slog.Info("engine home dir set", "dir", dir)
}

// Start 解析并应用配置。cfgBytes 为 nil 时以最小配置启动（P0：还没有 profile 数据）。
//
// tunFD 是宿主 VpnService/Shizuku 建好的 tun 文件描述符：
//   - tunFD < 0：不接管 tun（纯 HTTP 代理模式，或 P0 尚无 profile 的阶段）
//   - tunFD >= 0：写进 mihomo 的 tun.file-descriptor，由 sing-tun 直接接管该 fd
//
// fd 所有权：engine **不 close 它**，关闭是宿主 ReleaseVpn/CloseAll 的职责。
// 注意（TODO 联调确认）：sing-tun 在 engine.Stop() 时可能自己关闭该 fd，
// 若 platform 侧发现 fd 悬空，需改为「engine 停止时不销毁 tun listener」。
func (e *Engine) Start(cfgBytes []byte, tunFD int) error {
	// 若之前先开了 TUN 但那时还没有配置，fd 被暂存了；这里消费它。
	// 显式传入 >=0 的 fd 优先（例如切换 profile 时刚从宿主拿到的新 fd）。
	if tunFD < 0 {
		if p := e.takePendingTun(); p >= 0 {
			tunFD = p
			slog.Info("using pending tun fd", "fd", tunFD)
		}
	}
	e.mu.Lock()
	if e.state == StatusLoaded {
		e.mu.Unlock()
		return errors.New("engine: already started")
	}
	e.state = StatusLoading
	e.lastErr = ""
	e.mu.Unlock()

	if len(cfgBytes) == 0 {
		cfgBytes = []byte("mode: rule\nlog-level: silent\n")
	}
	cfg, err := executor.ParseWithBytes(cfgBytes)
	if err != nil {
		e.fail(fmt.Errorf("parse config: %w", err))
		return err
	}
	applyTunFD(cfg, tunFD)
	executor.ApplyConfig(cfg, true)

	e.seedNetworkFromConfig(cfg)

	now := time.Now()
	e.mu.Lock()
	e.cfg = cfgBytes
	e.parsed = cfg
	e.state = StatusLoaded
	e.startedAt = now
	e.mu.Unlock()
	slog.Info("engine started", "mode", tunnel.Mode().String())
	return nil
}

// applyTunFD 把宿主给的 tun fd 塞进配置。
//
// 只填 fd 与开关；auto-route / dns-hijack / 路由子网一律听配置的——
// Android 上的路由由宿主 VpnService 负责，platform 的 RouteSubnets 默认 false
// 表示"全局路由"，engine 不替它决定。
func applyTunFD(cfg *config.Config, tunFD int) {
	if tunFD < 0 {
		return
	}
	cfg.General.Tun.Enable = true
	cfg.General.Tun.FileDescriptor = tunFD
}

// Stop 停掉 tunnel；之后 engine 回到 Idle，可再次 Start。
func (e *Engine) Stop() error {
	e.mu.Lock()
	wasRunning := e.state == StatusLoaded
	e.state = StatusIdle
	e.parsed = nil
	e.mu.Unlock()
	if !wasRunning {
		return nil
	}
	executor.Shutdown()
	slog.Info("engine stopped")
	return nil
}

// fail 记录启动失败。
func (e *Engine) fail(err error) {
	e.mu.Lock()
	e.state = StatusError
	e.lastErr = err.Error()
	e.mu.Unlock()
	slog.Error("engine start failed", "err", err)
}

// State 返回 mihomo 生命周期状态。
func (e *Engine) State() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

// Running 表示 tunnel 已经跑起来。
func (e *Engine) Running() bool { return e.State() == StatusLoaded }

// LastError 返回最近一次启动失败的原因（无错为空串）。
func (e *Engine) LastError() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastErr
}

// ProfileID 返回当前生效 profile 的 UUID 字符串（无则全零 UUID）。
func (e *Engine) ProfileID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.profileID == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return e.profileID
}

// SetProfileID 由 profile/session 层写入当前生效 profile。
func (e *Engine) SetProfileID(id string) {
	e.mu.Lock()
	e.profileID = id
	e.mu.Unlock()
}

// Uptime 从启动到现在的秒数（未启动为 0）。
func (e *Engine) Uptime() time.Duration {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.state != StatusLoaded {
		return 0
	}
	return time.Since(e.startedAt)
}

// Mode 返回 CFA 语义的 mode（Direct/Global/Rule/Script）。
func (e *Engine) Mode() string {
	switch tunnel.Mode() {
	case tunnel.Direct:
		return ModeDirect
	case tunnel.Global:
		return ModeGlobal
	default:
		return ModeRule
	}
}

// SetMode 切换 mode。Script 在 mihomo 里没有对应实现，返回错误而不是静默降级。
func (e *Engine) SetMode(mode string) error {
	switch mode {
	case ModeDirect:
		tunnel.SetMode(tunnel.Direct)
	case ModeGlobal:
		tunnel.SetMode(tunnel.Global)
	case ModeRule:
		tunnel.SetMode(tunnel.Rule)
	case ModeScript:
		return errors.New("engine: script mode is not supported by mihomo")
	default:
		return fmt.Errorf("engine: unknown mode %q", mode)
	}
	return nil
}

// ConfigBytes 返回最近一次应用的配置原文（/config 端点用）。
func (e *Engine) ConfigBytes() []byte {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg
}

// ParsedConfig 返回解析后的配置快照，可能为 nil。
func (e *Engine) ParsedConfig() *config.Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.parsed
}

// SetExternalController 记录宿主希望暴露的 external-controller（只会出现在状态里，
// 真正监听由 endpoint 层决定，P0 不主动监听公网地址）。
func (e *Engine) SetExternalController(addr, secret string) {
	e.mu.Lock()
	e.ctrlAddr = addr
	e.ctrlSecret = secret
	e.mu.Unlock()
}

// ExternalController 返回记录下来的 external-controller 配置。
func (e *Engine) ExternalController() (addr, secret string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ctrlAddr, e.ctrlSecret
}
