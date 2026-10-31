package engine

import (
	"log/slog"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
)

// NetworkConfig 是 /network/* 五个资源的内存快照。
//
// 语义（与 lead 约定）：**这份快照是唯一真相源**——GET 读它、PATCH 写它、
// Start 时才把它应用到 mihomo。不要用它去读 mihomo 的实时值来响应 GET：
// 那样 PATCH 完 GET 会看到旧值，用户会以为没保存成功，反复点。
type NetworkConfig struct {
	Interface NetworkInterface
	Resolver  NetworkResolver
	FakeIP    NetworkFakeIP
	Outbound  NetworkOutbound
	Hosts     map[string]string
}

// NetworkInterface 对应 Network.Interface。
type NetworkInterface struct {
	AutoDetect bool
	Name       string
}

// NetworkResolver 对应 Network.Resolver。
type NetworkResolver struct {
	BootstrapServers []string
	FallbackServers  []string
	PrimaryServers   []string
	FallbackFilter   NetworkFallbackFilter
	NameServerPolicy map[string]any
}

// NetworkFallbackFilter 对应 Network.Resolver.FallbackFilter。
type NetworkFallbackFilter struct {
	Domains   []string
	Geoip     bool
	GeoipCode string
	Ipcidr    []string
}

// NetworkFakeIP 对应 Network.FakeIP。
type NetworkFakeIP struct {
	Enabled bool
	Filter  []string
	Range   string
}

// NetworkOutbound 对应 Network.Outbound。
type NetworkOutbound struct {
	Ipv6 bool
}

func defaultNetworkConfig() NetworkConfig {
	return NetworkConfig{
		Resolver: NetworkResolver{
			BootstrapServers: []string{},
			FallbackServers:  []string{},
			PrimaryServers:   []string{},
			NameServerPolicy: map[string]any{},
		},
		FakeIP: NetworkFakeIP{Filter: []string{}},
		Hosts:  map[string]string{},
	}
}

// Network 返回网络配置快照（深拷贝，调用方可以随便改）。
func (e *Engine) Network() NetworkConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneNetworkConfig(e.network)
}

// PatchNetwork 原地修改网络配置快照，fn 拿到的是可改的副本。
//
// 只写快照，**不重建 tunnel**：重建期间用户会断网，而"改个 DNS 设置结果网断了"
// 是最糟的体验。改动在下一次 Start 时生效（日志里有 Info 提示）。
func (e *Engine) PatchNetwork(fn func(*NetworkConfig)) {
	e.mu.Lock()
	next := cloneNetworkConfig(e.network)
	fn(&next)
	next = normalizeNetworkConfig(next)
	e.network = next
	e.mu.Unlock()
	slog.Info("network config patched, takes effect on next tunnel start")
}

// seedNetworkFromConfig 在 Start 时用配置初始化快照里能确定的部分。
//
// 只填"从配置里能无歧义读到"的字段；mihomo 的 dns.NameServer 没有 String()，
// resolver 的服务器列表暂时留空（TODO），不做字符串猜测。
func (e *Engine) seedNetworkFromConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	e.PatchNetwork(func(n *NetworkConfig) {
		n.Interface = NetworkInterface{
			Name:       cfg.General.Interface,
			AutoDetect: cfg.General.Interface == "",
		}
		n.Outbound = NetworkOutbound{Ipv6: cfg.General.IPv6}
		// resolver 的三组服务器：正向解析逻辑反推出的还原规则见 nameserver.go。
		n.Resolver.PrimaryServers = nameServerStrings(cfg.DNS.NameServer)
		n.Resolver.FallbackServers = nameServerStrings(cfg.DNS.Fallback)
		n.Resolver.BootstrapServers = nameServerStrings(cfg.DNS.DefaultNameserver)
		// TODO(联调): FallbackFilter 里的 domains/ipcidr/geoip 在解析后只剩下
		// C.DomainMatcher / C.IpMatcher 匹配器，原始字符串已丢，暂时不还原。
		n.FakeIP.Enabled = cfg.DNS.EnhancedMode == C.DNSFakeIP
		if cfg.DNS.FakeIPRange.IsValid() {
			n.FakeIP.Range = cfg.DNS.FakeIPRange.String()
		}
	})
}

func cloneNetworkConfig(in NetworkConfig) NetworkConfig {
	out := in
	out.Interface = in.Interface
	out.Outbound = in.Outbound
	out.FakeIP = NetworkFakeIP{Enabled: in.FakeIP.Enabled, Filter: append([]string{}, in.FakeIP.Filter...), Range: in.FakeIP.Range}
	out.Resolver = NetworkResolver{
		BootstrapServers: append([]string{}, in.Resolver.BootstrapServers...),
		FallbackServers:  append([]string{}, in.Resolver.FallbackServers...),
		PrimaryServers:   append([]string{}, in.Resolver.PrimaryServers...),
		FallbackFilter: NetworkFallbackFilter{
			Domains:   append([]string{}, in.Resolver.FallbackFilter.Domains...),
			Geoip:     in.Resolver.FallbackFilter.Geoip,
			GeoipCode: in.Resolver.FallbackFilter.GeoipCode,
			Ipcidr:    append([]string{}, in.Resolver.FallbackFilter.Ipcidr...),
		},
		NameServerPolicy: map[string]any{},
	}
	for k, v := range in.Resolver.NameServerPolicy {
		out.Resolver.NameServerPolicy[k] = v
	}
	out.Hosts = make(map[string]string, len(in.Hosts))
	for k, v := range in.Hosts {
		out.Hosts[k] = v
	}
	return out
}

// normalizeNetworkConfig 保证所有集合非 nil —— 线上必须是 [] 而不是 null
// （kotlinx 的 List 不接受 null），这一步在写快照时就做掉。
func normalizeNetworkConfig(n NetworkConfig) NetworkConfig {
	if n.Resolver.BootstrapServers == nil {
		n.Resolver.BootstrapServers = []string{}
	}
	if n.Resolver.FallbackServers == nil {
		n.Resolver.FallbackServers = []string{}
	}
	if n.Resolver.PrimaryServers == nil {
		n.Resolver.PrimaryServers = []string{}
	}
	if n.Resolver.NameServerPolicy == nil {
		n.Resolver.NameServerPolicy = map[string]any{}
	}
	if n.Resolver.FallbackFilter.Domains == nil {
		n.Resolver.FallbackFilter.Domains = []string{}
	}
	if n.Resolver.FallbackFilter.Ipcidr == nil {
		n.Resolver.FallbackFilter.Ipcidr = []string{}
	}
	if n.FakeIP.Filter == nil {
		n.FakeIP.Filter = []string{}
	}
	if n.Hosts == nil {
		n.Hosts = map[string]string{}
	}
	return n
}
