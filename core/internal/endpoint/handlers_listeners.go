package endpoint

import (
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// getListeners 对应 GET /listeners -> Listeners.Listener.All。
//
// 响应是包装体 {"listeners": {...}}，键是 Listeners.Type 的枚举名。
func (s *server) getListeners(w http.ResponseWriter, r *http.Request) {
	snap := s.backend.Listeners()
	out := listenersAll{Listeners: make(map[string]listenerDTO, len(snap))}
	for _, l := range snap {
		out.Listeners[l.Type] = listenerDTO{
			Enabled: l.Enabled,
			Status:  listenerStatus(l.Type, l.Status),
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// putListenerEnabled 对应 PUT /listeners/{name}/enabled，body = {enabled: Bool}。
func (s *server) putListenerEnabled(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !engine.ValidListenerType(name) {
		writeError(w, http.StatusBadRequest, "unknown listener type: "+name)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := s.backend.SetListenerEnabled(name, body.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// patchListenerConfig 对应 PATCH /listeners/{name}/config，body = Listeners.Listener.Config（sealed）。
//
// TODO(P2): 这里只做形状校验并记录日志，真正把配置下发到 mihomo / platform 层的
// 逻辑要等 platform-dev 的 TUN/VPN 通道就绪（Config.TUN / Config.VPN 的字段语义
// 还依赖 CreateVpn 的回流）。
func (s *server) patchListenerConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !engine.ValidListenerType(name) {
		writeError(w, http.StatusBadRequest, "unknown listener type: "+name)
		return
	}
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// listenerStatus 把 engine 的中性快照转成 sealed 子类型，保证字段集合与模型一致
// （不发多余键、不漏必填键、空集合发 [] 而不是 null）。
func listenerStatus(typ string, st map[string]any) any {
	switch typ {
	case engine.ListenerDNS:
		return listenerStatusDNS{
			Type:     typ,
			IPv6:     boolOf(st["ipv6"]),
			ListenAt: strOf(st["listenAt"]),
			UseHosts: boolOf(st["useHosts"]),
		}
	case engine.ListenerExternalController:
		return listenerStatusExternalController{
			Type:     typ,
			ListenAt: strOf(st["listenAt"]),
			Secret:   strOf(st["secret"]),
		}
	case engine.ListenerHTTPSocks5:
		return listenerStatusHTTPSocks5{
			Type:     typ,
			ListenAt: strOf(st["listenAt"]),
			Users:    strsOf(st["users"]),
		}
	case engine.ListenerRedirect:
		return listenerStatusRedirect{
			Type:     typ,
			ListenAt: strOf(st["listenAt"]),
		}
	case engine.ListenerSystemProxy:
		return listenerStatusSystemProxy{
			Type:     typ,
			Excludes: strsOf(st["excludes"]),
			ListenAt: strOf(st["listenAt"]),
		}
	case engine.ListenerTUN:
		return listenerStatusTUN{
			Type:          typ,
			Address:       strOf(st["address"]),
			Boosted:       boolOf(st["boosted"]),
			DNSHijacked:   boolOf(st["dnsHijacked"]),
			RouteInserted: boolOf(st["routeInserted"]),
		}
	case engine.ListenerTransProxy:
		return listenerStatusTransProxy{
			Type:     typ,
			ListenAt: strOf(st["listenAt"]),
			UDP:      boolOf(st["udp"]),
		}
	case engine.ListenerVPN:
		return listenerStatusVPN{
			Type:                 typ,
			DNSHijacked:          boolOf(st["dnsHijacked"]),
			HasChanges:           boolOf(st["hasChanges"]),
			SystemProxyListening: boolOf(st["systemProxyListening"]),
		}
	default:
		// 理论上不可达：engine 只吐 8 个已知类型。
		return map[string]any{"type": typ}
	}
}

func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolOf(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func strsOf(v any) []string {
	out := []string{}
	switch vals := v.(type) {
	case []string:
		return append(out, vals...)
	case []any:
		for _, item := range vals {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
