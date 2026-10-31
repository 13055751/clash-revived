package engine

// tun.go — TUN/VPN 开关与宿主 fd 的桥接。
//
// 分工：
//   - engine 不知道 platform 包的存在（不 import internal/platform）
//   - 宿主能力通过 SetTunProvider 注入一个函数，由 core 包在初始化时接上
//   - 这样 engine 可以单独测试（注入假 provider 即可）
//
// 时序（真机）：
//   用户打开 UI 的 TUN 开关
//     -> PUT /listeners/TUN/enabled {enabled:true}
//     -> engine.SetListenerEnabled("TUN", true)
//     -> tunProvider()  -> core 调 platform.CreateVpn -> 宿主弹 VPN 授权 -> 回传 tun fd
//     -> 用该 fd 重启 mihomo（applyTunFD 写入 tun.file-descriptor，sing-tun 直接接管）
//
// 为什么必须重启：mihomo 的 tun 监听器在 ApplyConfig 时创建，启动后无法热插入
// file-descriptor。所以先 Stop 再 Start（配置字节保留在 e.cfg，Stop 不清它）。

import (
	"fmt"
	"log/slog"
)

// tunProvider 由 core 注入：返回宿主建好的 tun fd，<0 表示拿不到。
// 允许返回 error 表示明确失败（例如用户拒绝授权）。
var tunProvider func() (int, error)

// SetTunProvider 注入宿主 tun 获取能力。传 nil 解除。
// 必须在任何 TUN/VPN 开关被打开之前调用。
func SetTunProvider(f func() (int, error)) { tunProvider = f }

// HasTunProvider 报告是否已接上宿主能力（测试与日志用）。
func HasTunProvider() bool { return tunProvider != nil }

// acquireTun 向宿主要 tun fd。没接 provider 时返回 -1（表示"不接管 tun"），
// 这不是错误：纯 HTTP 代理模式下本来就不需要。
func acquireTun() (int, error) {
	if tunProvider == nil {
		slog.Warn("tun requested but no provider wired; staying in proxy-only mode")
		return -1, nil
	}
	fd, err := tunProvider()
	if err != nil {
		return -1, err
	}
	if fd < 0 {
		return -1, fmt.Errorf("host returned no tun fd")
	}
	return fd, nil
}

// enableTun 打开 TUN/VPN：取 fd，若有配置则用新 fd 重启。
func (e *Engine) enableTun(typ string) error {
	fd, err := acquireTun()
	if err != nil {
		return fmt.Errorf("%s: acquire tun: %w", typ, err)
	}
	cfg := e.ConfigBytes()
	if len(cfg) == 0 {
		// 还没有激活的 profile：记住开关，等 Start 时再取一次 fd。
		slog.Info("tun enabled before any profile; will attach on next start", "listener", typ, "fd", fd)
		e.mu.Lock()
		e.pendingTunFD = fd
		e.mu.Unlock()
		return nil
	}
	if !e.Running() {
		// 有配置但没在跑：直接带着 fd 起
		slog.Info("starting engine with tun", "listener", typ, "fd", fd)
		return e.Start(cfg, fd)
	}
	slog.Info("restarting engine to attach tun", "listener", typ, "fd", fd)
	if err := e.Stop(); err != nil {
		return fmt.Errorf("%s: stop for tun restart: %w", typ, err)
	}
	if err := e.Start(cfg, fd); err != nil {
		return fmt.Errorf("%s: restart with tun: %w", typ, err)
	}
	return nil
}

// disableTun 关闭 TUN/VPN。mihomo 侧需要重建 tunnel 才能卸掉 tun。
func (e *Engine) disableTun(typ string) error {
	e.mu.Lock()
	e.pendingTunFD = -1
	e.mu.Unlock()

	cfg := e.ConfigBytes()
	if len(cfg) == 0 || !e.Running() {
		return nil
	}
	slog.Info("restarting engine without tun", "listener", typ)
	if err := e.Stop(); err != nil {
		return fmt.Errorf("%s: stop for tun detach: %w", typ, err)
	}
	if err := e.Start(cfg, -1); err != nil {
		return fmt.Errorf("%s: restart without tun: %w", typ, err)
	}
	return nil
}

// pendingTunFD 是「先开 TUN、后激活订阅」这一顺序下暂存的 fd。
// Start 时会消费它（见 engine.Start）。
func (e *Engine) takePendingTun() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	fd := e.pendingTunFD
	e.pendingTunFD = -1
	// fd 0/1/2 是标准流，绝不可能是宿主给的 tun。哪怕状态被意外置 0 也挡掉。
	if fd >= 0 && fd <= 2 {
		slog.Warn("refusing suspicious tun fd (std stream)", "fd", fd)
		return -1
	}
	return fd
}
