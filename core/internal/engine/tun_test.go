package engine

import (
	"errors"
	"testing"
)

// 没有 provider 时开 TUN：应降级为 -1（纯代理模式），不报错。
func TestTunWithoutProviderDegrades(t *testing.T) {
	SetTunProvider(nil)
	e := New()
	if err := e.SetListenerEnabled(ListenerTUN, true); err != nil {
		t.Fatalf("无 provider 时应降级而不是报错，got %v", err)
	}
	// 没激活配置，会暂存 pending（这里是 -1，等于没存）
	if fd := e.takePendingTun(); fd >= 0 {
		t.Fatalf("不该暂存到有效 fd，got %d", fd)
	}
}

// provider 明确失败（例如用户拒绝 VPN 授权）：错误必须冒泡，且状态不能变。
func TestTunProviderErrorPropagates(t *testing.T) {
	SetTunProvider(func() (int, error) { return -1, errors.New("user denied") })
	defer SetTunProvider(nil)
	e := New()
	err := e.SetListenerEnabled(ListenerTUN, true)
	if err == nil {
		t.Fatal("provider 报错时必须返回错误")
	}
	// 开关不应被标记为 enabled
	for _, l := range e.Listeners() {
		if l.Type == ListenerTUN && l.Enabled {
			t.Fatal("失败时不应把 TUN 标记为 enabled")
		}
	}
}

// provider 返回 fd 但没有配置：暂存，等 Start 消费。
func TestTunPendingFDStoredWhenNoProfile(t *testing.T) {
	SetTunProvider(func() (int, error) { return 42, nil })
	defer SetTunProvider(nil)
	e := New()
	if err := e.SetListenerEnabled(ListenerVPN, true); err != nil {
		t.Fatalf("应有 fd 且无配置 -> 暂存并返回 nil，got %v", err)
	}
	if fd := e.takePendingTun(); fd != 42 {
		t.Fatalf("pending fd 应为 42，got %d", fd)
	}
	// 取过一次就清空
	if fd := e.takePendingTun(); fd != -1 {
		t.Fatalf("第二次取应为 -1，got %d", fd)
	}
}

// 关闭 TUN 时应清掉 pending。
func TestTunDisableClearsPending(t *testing.T) {
	SetTunProvider(func() (int, error) { return 7, nil })
	defer SetTunProvider(nil)
	e := New()
	_ = e.SetListenerEnabled(ListenerTUN, true) // 暂存 7
	if err := e.SetListenerEnabled(ListenerTUN, false); err != nil {
		t.Fatalf("关闭应成功，got %v", err)
	}
	if fd := e.takePendingTun(); fd != -1 {
		t.Fatalf("关闭后 pending 应清空，got %d", fd)
	}
}

// provider 返回 -1 无错误：视为"宿主拿不到"，是错误而非静默降级。
func TestTunProviderReturnsMinusOne(t *testing.T) {
	SetTunProvider(func() (int, error) { return -1, nil })
	defer SetTunProvider(nil)
	e := New()
	if err := e.SetListenerEnabled(ListenerTUN, true); err == nil {
		t.Fatal("provider 返回 -1 时应报错，否则 UI 会以为 TUN 开了")
	}
}

// 回归：New() 必须把 pendingTunFD 初始化为 -1。
// Go 零值是 0，而 fd 0 是 stdin——漏初始化会让内核把 stdin 当 tun 传给 sing-tun，
// 真机上表现为界面白屏（2026-10-31 实测踩到）。
func TestPendingTunDefaultsToMinusOne(t *testing.T) {
	e := New()
	if fd := e.takePendingTun(); fd >= 0 {
		t.Fatalf("新实例不该有 pending tun fd，got %d", fd)
	}
}

// 回归：即使 pendingTunFD 被意外置 0，也不能当有效 fd 交出去。
func TestTakePendingTunRejectsStdStreams(t *testing.T) {
	e := New()
	for _, bad := range []int{0, 1, 2} {
		e.mu.Lock()
		e.pendingTunFD = bad
		e.mu.Unlock()
		if fd := e.takePendingTun(); fd >= 0 {
			t.Fatalf("fd %d 是标准流，不该被接受", bad)
		}
	}
}
