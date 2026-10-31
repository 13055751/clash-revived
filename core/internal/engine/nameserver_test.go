package engine

import (
	"testing"

	"github.com/metacubex/mihomo/dns"
)

// 还原规则对照表（由 config.parseNameServer 正向逻辑反推）。
func TestNameServerString(t *testing.T) {
	cases := []struct {
		name string
		in   dns.NameServer
		want string
	}{
		{"udp 裸地址", dns.NameServer{Addr: "223.5.5.5:53"}, "223.5.5.5:53"},
		{"tcp", dns.NameServer{Net: "tcp", Addr: "8.8.8.8:53"}, "tcp://8.8.8.8:53"},
		{"tls", dns.NameServer{Net: "tls", Addr: "dns.google:853"}, "tls://dns.google:853"},
		// https 的 Addr 解析时已用 url.URL.String() 重建，是完整 URL，不能再加前缀。
		{"https 完整 URL", dns.NameServer{Net: "https", Addr: "https://doh.example.com:443/dns-query"},
			"https://doh.example.com:443/dns-query"},
		{"quic", dns.NameServer{Net: "quic", Addr: "dns.example:853"}, "quic://dns.example:853"},
		{"h3", dns.NameServer{Net: "h3", Addr: "dns.example:853"}, "h3://dns.example:853"},
		{"带 proxyName", dns.NameServer{Addr: "8.8.8.8:53", ProxyName: "HK"},
			"8.8.8.8:53#HK"},
		{"带 params", dns.NameServer{Addr: "8.8.8.8:53", Params: map[string]string{"skip-cert-verify": "true"}},
			"8.8.8.8:53#skip-cert-verify=true"},
		{"proxyName + 多个 params（排序稳定）", dns.NameServer{
			Addr: "8.8.8.8:53", ProxyName: "HK",
			Params: map[string]string{"b": "2", "a": "1"},
		}, "8.8.8.8:53#HK&a=1&b=2"},
	}
	for _, c := range cases {
		if got := nameServerString(c.in); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// Start 时把配置里的三组 DNS 服务器填进 resolver 快照。
func TestSeedResolverFromConfig(t *testing.T) {
	e := New()
	cfg := []byte(`mode: rule
log-level: silent
dns:
  enable: true
  nameserver: [223.5.5.5, tls://dns.google:853]
  default-nameserver: [114.114.114.114]
`)
	if err := e.Start(cfg, -1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = e.Stop() }()

	n := e.Network()
	if len(n.Resolver.PrimaryServers) != 2 {
		t.Fatalf("primaryServers = %v", n.Resolver.PrimaryServers)
	}
	if n.Resolver.PrimaryServers[0] != "223.5.5.5:53" && n.Resolver.PrimaryServers[1] != "223.5.5.5:53" {
		t.Fatalf("primaryServers = %v", n.Resolver.PrimaryServers)
	}
	foundTLS := false
	for _, s := range n.Resolver.PrimaryServers {
		if s == "tls://dns.google:853" {
			foundTLS = true
		}
	}
	if !foundTLS {
		t.Fatalf("tls server not restored: %v", n.Resolver.PrimaryServers)
	}
	if len(n.Resolver.BootstrapServers) != 1 {
		t.Fatalf("bootstrapServers = %v", n.Resolver.BootstrapServers)
	}
	// 注意：配置里带 dns.fallback 会让 mihomo 去下载 GeoIP MMDB（需要网络），
	// 所以这个测试故意不写 fallback —— 那不是我们的代码路径，别让单测去联网。
	if len(n.Resolver.FallbackServers) != 0 {
		t.Fatalf("fallbackServers = %v", n.Resolver.FallbackServers)
	}
}
