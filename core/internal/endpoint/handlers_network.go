package endpoint

import (
	"log/slog"
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// /network/* 的五个资源：GET 读 engine 的内存快照，PATCH 写快照。
//
// 约定（与 engine.PatchNetwork 一致）：GET 必须立刻反映刚 PATCH 的值，
// 否则用户会"点了保存、界面回显旧值、以为自己没保存成功、反复点"。
// PATCH 不重建 tunnel；待生效的事实用日志表达，**不发明模型里没有的字段**。

func (s *server) getNetworkInterface(w http.ResponseWriter, r *http.Request) {
	n := s.backend.Network()
	writeJSON(w, http.StatusOK, networkInterfaceDTO(n.Interface))
}

func (s *server) patchNetworkInterface(w http.ResponseWriter, r *http.Request) {
	var body networkInterface
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	s.backend.PatchNetwork(func(n *engine.NetworkConfig) {
		n.Interface = engine.NetworkInterface{AutoDetect: body.AutoDetect, Name: body.Name}
	})
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *server) getNetworkResolver(w http.ResponseWriter, r *http.Request) {
	n := s.backend.Network()
	writeJSON(w, http.StatusOK, networkResolverDTO(n.Resolver))
}

func (s *server) patchNetworkResolver(w http.ResponseWriter, r *http.Request) {
	var body networkResolver
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	s.backend.PatchNetwork(func(n *engine.NetworkConfig) {
		n.Resolver = engine.NetworkResolver{
			BootstrapServers: body.BootstrapServers,
			FallbackServers:  body.FallbackServers,
			PrimaryServers:   body.PrimaryServers,
			FallbackFilter: engine.NetworkFallbackFilter{
				Domains:   body.FallbackFilter.Domains,
				Geoip:     body.FallbackFilter.Geoip,
				GeoipCode: body.FallbackFilter.GeoipCode,
				Ipcidr:    body.FallbackFilter.Ipcidr,
			},
			NameServerPolicy: body.NameServerPolicy,
		}
	})
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *server) getNetworkFakeIP(w http.ResponseWriter, r *http.Request) {
	n := s.backend.Network()
	writeJSON(w, http.StatusOK, networkFakeIPDTO(n.FakeIP))
}

func (s *server) patchNetworkFakeIP(w http.ResponseWriter, r *http.Request) {
	var body networkFakeIP
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	s.backend.PatchNetwork(func(n *engine.NetworkConfig) {
		n.FakeIP = engine.NetworkFakeIP{
			Enabled: body.Enabled,
			Filter:  body.Filter,
			Range:   body.Range,
		}
	})
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *server) getNetworkOutbound(w http.ResponseWriter, r *http.Request) {
	n := s.backend.Network()
	writeJSON(w, http.StatusOK, networkOutboundDTO(n.Outbound))
}

func (s *server) patchNetworkOutbound(w http.ResponseWriter, r *http.Request) {
	var body networkOutbound
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	s.backend.PatchNetwork(func(n *engine.NetworkConfig) {
		n.Outbound = engine.NetworkOutbound{Ipv6: body.Ipv6}
	})
	writeJSON(w, http.StatusOK, map[string]any{})
}

// getNetworkHosts 对应 GET /network/hosts。
//
// TODO(联调): dex 里没有 Network.Hosts 这样的数据类，响应形状无法从二进制确认。
// 按 hosts 的语义实现成扁平的 Map<String,String>（域名 -> 地址）。
func (s *server) getNetworkHosts(w http.ResponseWriter, r *http.Request) {
	n := s.backend.Network()
	out := make(map[string]string, len(n.Hosts))
	for k, v := range n.Hosts {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) patchNetworkHosts(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	s.backend.PatchNetwork(func(n *engine.NetworkConfig) {
		n.Hosts = body
	})
	slog.Info("hosts patched", "entries", len(body))
	writeJSON(w, http.StatusOK, map[string]any{})
}

// --- DTO 转换 ---

func networkInterfaceDTO(v engine.NetworkInterface) networkInterface {
	return networkInterface{AutoDetect: v.AutoDetect, Name: v.Name}
}

func networkResolverDTO(v engine.NetworkResolver) networkResolver {
	out := networkResolver{
		BootstrapServers: append([]string{}, v.BootstrapServers...),
		FallbackServers:  append([]string{}, v.FallbackServers...),
		PrimaryServers:   append([]string{}, v.PrimaryServers...),
		NameServerPolicy: map[string]any{},
		FallbackFilter: networkFallbackFilter{
			Domains:   append([]string{}, v.FallbackFilter.Domains...),
			Geoip:     v.FallbackFilter.Geoip,
			GeoipCode: v.FallbackFilter.GeoipCode,
			Ipcidr:    append([]string{}, v.FallbackFilter.Ipcidr...),
		},
	}
	for k, val := range v.NameServerPolicy {
		out.NameServerPolicy[k] = val
	}
	return out
}

func networkFakeIPDTO(v engine.NetworkFakeIP) networkFakeIP {
	return networkFakeIP{Enabled: v.Enabled, Filter: append([]string{}, v.Filter...), Range: v.Range}
}

func networkOutboundDTO(v engine.NetworkOutbound) networkOutbound {
	return networkOutbound{Ipv6: v.Ipv6}
}
