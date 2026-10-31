package engine

import (
	"strings"
	"testing"

	"github.com/metacubex/mihomo/hub/executor"
)

// 未启动的引擎必须能安全地回答所有查询（端点 httptest 依赖这个前提）。
func TestIdleEngineDefaults(t *testing.T) {
	e := New()
	if got := e.State(); got != StatusIdle {
		t.Fatalf("state = %q, want %q", got, StatusIdle)
	}
	if e.Running() {
		t.Fatal("Running() = true on fresh engine")
	}
	if got := e.ProfileID(); got != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("ProfileID = %q", got)
	}
	if got := e.LastError(); got != "" {
		t.Fatalf("LastError = %q", got)
	}
	if got := e.Uptime(); got != 0 {
		t.Fatalf("Uptime = %v", got)
	}
	if got := e.Mode(); got != ModeRule {
		// 未启动时 mihomo 的默认 mode 是 rule
		t.Fatalf("Mode = %q", got)
	}
	if pro := e.Proxies(); len(pro) != 0 {
		t.Fatalf("Proxies = %v", pro)
	}
	if ls := e.Listeners(); len(ls) == 0 {
		t.Fatal("Listeners() must always describe the 8 CFA listener types")
	}
	if now, history := e.ConnectionCount(); now != 0 || history != 0 {
		t.Fatalf("ConnectionCount = %d,%d", now, history)
	}
}

// 最小配置的启动 / 停止往返，验证 mihomo 生命周期被正确接管。
func TestStartStopRoundTrip(t *testing.T) {
	e := New()
	if err := e.Start(nil, -1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := e.State(); got != StatusLoaded {
		t.Fatalf("state = %q, want %q", got, StatusLoaded)
	}
	if !e.Running() {
		t.Fatal("Running() = false after Start")
	}
	if got := e.Mode(); got != ModeRule {
		t.Fatalf("Mode = %q, want %q", got, ModeRule)
	}
	if err := e.Start(nil, -1); err == nil {
		t.Fatal("second Start must fail")
	}
	if err := e.SetMode(ModeGlobal); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if got := e.Mode(); got != ModeGlobal {
		t.Fatalf("Mode = %q after SetMode", got)
	}
	if err := e.SetMode(ModeScript); err == nil {
		t.Fatal("Script mode must be rejected (mihomo has no script mode)")
	}
	if err := e.SetMode("nonsense"); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := e.State(); got != StatusIdle {
		t.Fatalf("state = %q after Stop", got)
	}
}

// 坏配置必须进入 Error 并把原因暴露出去，而不是崩。
func TestStartBadConfig(t *testing.T) {
	e := New()
	if err := e.Start([]byte("mode: [unclosed\n"), -1); err == nil {
		t.Fatal("expected parse error")
	}
	if got := e.State(); got != StatusError {
		t.Fatalf("state = %q, want %q", got, StatusError)
	}
	if e.LastError() == "" {
		t.Fatal("LastError must be non-empty after failed start")
	}
}

// 最小配置里指定端口后，listeners 快照要能反映出来。
func TestListenersReflectPorts(t *testing.T) {
	e := New()
	cfg := []byte("mixed-port: 17890\nmode: rule\nlog-level: silent\n")
	if err := e.Start(cfg, -1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = e.Stop() }()

	got := map[string]Listener{}
	for _, l := range e.Listeners() {
		got[l.Type] = l
	}
	if !got["HTTPSocks5"].Enabled {
		t.Fatalf("HTTPSocks5 should be enabled after mixed-port: %+v", got["HTTPSocks5"])
	}
	if addr, _ := got["HTTPSocks5"].Status["listenAt"].(string); addr == "" {
		t.Fatalf("HTTPSocks5.listenAt empty: %+v", got["HTTPSocks5"])
	}
	for name, l := range got {
		if l.Status == nil {
			t.Fatalf("listener %s has nil status", name)
		}
	}
}

// 停止后再次启动：宿主的「重载配置」路径依赖这个行为。
func TestRestartAfterStop(t *testing.T) {
	e := New()
	if err := e.Start(nil, -1); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := e.Start([]byte("mixed-port: 17891\nmode: global\nlog-level: silent\n"), -1); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	defer func() { _ = e.Stop() }()
	if got := e.State(); got != StatusLoaded {
		t.Fatalf("state = %q after restart", got)
	}
	if got := e.Mode(); got != ModeGlobal {
		t.Fatalf("Mode = %q after restart", got)
	}
}

// 宿主给的 tun fd 必须原样落到 tun.file-descriptor，且不覆盖配置里的路由语义。
func TestApplyTunFD(t *testing.T) {
	cfg, err := executor.ParseWithBytes([]byte("tun:\n  enable: false\n  auto-route: false\n  dns-hijack: [any:53]\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyTunFD(cfg, -1)
	if cfg.General.Tun.Enable {
		t.Fatal("tunFD < 0 must not enable tun")
	}
	applyTunFD(cfg, 42)
	if !cfg.General.Tun.Enable {
		t.Fatal("tunFD >= 0 must enable tun")
	}
	if cfg.General.Tun.FileDescriptor != 42 {
		t.Fatalf("file-descriptor = %d, want 42", cfg.General.Tun.FileDescriptor)
	}
	if cfg.General.Tun.AutoRoute {
		t.Fatal("applyTunFD must not touch auto-route")
	}
	if len(cfg.General.Tun.DNSHijack) != 1 {
		t.Fatalf("applyTunFD must not touch dns-hijack: %v", cfg.General.Tun.DNSHijack)
	}
}

// provider 快照：config 里用 inline 类型，不碰磁盘也不需要网络。
func TestProvidersSnapshot(t *testing.T) {
	e := New()
	cfg := []byte(`mode: rule
log-level: silent
proxy-providers:
  inline-prov:
    type: inline
    payload:
      - {name: n2, type: ss, server: 127.0.0.1, port: 1, cipher: aes-128-gcm, password: x}
      - {name: n1, type: ss, server: 127.0.0.1, port: 2, cipher: aes-128-gcm, password: x}
rule-providers:
  rules-a:
    type: inline
    behavior: domain
    format: yaml
    payload:
      - "+.example.com"
      - "+.example.org"
`)
	if err := e.Start(cfg, -1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = e.Stop() }()

	proxyProvs := e.Providers(ProviderTypeProxy)
	if len(proxyProvs) != 1 {
		t.Fatalf("proxy providers = %d (%+v)", len(proxyProvs), proxyProvs)
	}
	got := proxyProvs[0]
	if got.Name != "inline-prov" || got.Type != ProviderTypeProxy {
		t.Fatalf("provider = %+v", got)
	}
	// inline 在 mihomo 里是 Inline，CFA 模型只有 File/HTTP，归一为 File。
	if got.VehicleType != VehicleFile {
		t.Fatalf("vehicleType = %q", got.VehicleType)
	}
	// 节点名按名字排序，便于 UI 稳定展示。
	if strings.Join(got.Proxies, ",") != "n1,n2" {
		t.Fatalf("proxies = %v", got.Proxies)
	}
	// 首次见到时不知道真实更新时间，必须报 0（未知），不能拿启动时刻冒充。
	if got.UpdatedUnixMS != 0 {
		t.Fatalf("updatedAt on first sight = %d, want 0 (unknown)", got.UpdatedUnixMS)
	}
	// 再看一次必须还是 0（不能因为走了"版本未变"的分支就漏出零值 time 的毫秒数）。
	if again, ok := e.Provider(ProviderTypeProxy, "inline-prov"); !ok || again.UpdatedUnixMS != 0 {
		t.Fatalf("second read updatedAt = %d, want 0", again.UpdatedUnixMS)
	}

	ruleProvs := e.Providers(ProviderTypeRule)
	if len(ruleProvs) != 1 || ruleProvs[0].Name != "rules-a" {
		t.Fatalf("rule providers = %+v", ruleProvs)
	}
	if ruleProvs[0].RuleCount != 2 {
		t.Fatalf("rule count = %d, want 2", ruleProvs[0].RuleCount)
	}
	if len(ruleProvs[0].Proxies) != 0 {
		t.Fatalf("rule provider proxies must be empty: %v", ruleProvs[0].Proxies)
	}

	if _, ok := e.Provider(ProviderTypeProxy, "inline-prov"); !ok {
		t.Fatal("Provider() lookup failed")
	}
	if _, ok := e.Provider(ProviderTypeRule, "inline-prov"); ok {
		t.Fatal("cross-type lookup must fail")
	}
	if _, ok := e.Provider(ProviderTypeProxy, "nope"); ok {
		t.Fatal("unknown provider must not be found")
	}
}

// 未启动时 provider 查询必须安全返回空值。
func TestProvidersIdle(t *testing.T) {
	e := New()
	if got := e.Providers(""); len(got) != 0 {
		t.Fatalf("Providers = %+v", got)
	}
	if _, ok := e.Provider(ProviderTypeProxy, "x"); ok {
		t.Fatal("idle engine must not resolve providers")
	}
	if err := e.HealthCheckProvider("x"); err == nil {
		t.Fatal("idle engine must reject healthcheck")
	}
}
