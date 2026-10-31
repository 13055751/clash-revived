package engine

import "testing"

func TestNetworkSnapshotDefaults(t *testing.T) {
	e := New()
	n := e.Network()
	// 所有集合非 nil（线上必须是 [] 不是 null）。
	if n.Resolver.BootstrapServers == nil || n.Resolver.PrimaryServers == nil ||
		n.Resolver.FallbackFilter.Domains == nil || n.FakeIP.Filter == nil || n.Hosts == nil {
		t.Fatalf("defaults must use empty slices: %+v", n)
	}
}

func TestPatchNetworkRoundTrip(t *testing.T) {
	e := New()
	e.PatchNetwork(func(n *NetworkConfig) {
		n.Interface = NetworkInterface{Name: "wlan0"}
		n.Outbound = NetworkOutbound{Ipv6: true}
	})
	n := e.Network()
	if n.Interface.Name != "wlan0" || !n.Outbound.Ipv6 {
		t.Fatalf("after patch: %+v", n)
	}

	// 返回的是深拷贝：外部改它不能影响内部状态。
	n.Hosts["evil"] = "1.2.3.4"
	n.Resolver.PrimaryServers = append(n.Resolver.PrimaryServers, "x")
	if _, ok := e.Network().Hosts["evil"]; ok {
		t.Fatal("Network() must return a deep copy")
	}
	if len(e.Network().Resolver.PrimaryServers) != 0 {
		t.Fatal("slice not copied")
	}
}

// Start 时用配置初始化快照里能确定的部分。
func TestSeedNetworkFromConfig(t *testing.T) {
	e := New()
	if err := e.Start([]byte("interface-name: rmnet_data0\nipv6: true\nmode: rule\nlog-level: silent\n"), -1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = e.Stop() }()

	n := e.Network()
	if n.Interface.Name != "rmnet_data0" || n.Interface.AutoDetect {
		t.Fatalf("interface = %+v", n.Interface)
	}
	if !n.Outbound.Ipv6 {
		t.Fatalf("outbound = %+v", n.Outbound)
	}
}

// AutoDetect 的语义：配置里没写 interface-name 就是自动探测。
func TestSeedNetworkAutoDetect(t *testing.T) {
	e := New()
	if err := e.Start(nil, -1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = e.Stop() }()
	if n := e.Network(); !n.Interface.AutoDetect || n.Interface.Name != "" {
		t.Fatalf("interface = %+v", n.Interface)
	}
}
