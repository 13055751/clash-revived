package endpoint

import (
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// getProxyProviders 对应 GET /providers/proxies。
func (s *server) getProxyProviders(w http.ResponseWriter, r *http.Request) {
	s.writeProviders(w, engine.ProviderTypeProxy)
}

// getRuleProviders 对应 GET /providers/rules。
func (s *server) getRuleProviders(w http.ResponseWriter, r *http.Request) {
	s.writeProviders(w, engine.ProviderTypeRule)
}

// getProxyProvider 对应 GET /providers/proxies/{name}。
//
// TODO(联调): 单个 provider 的响应是裸对象还是 All 包装体，dex 里没有直接证据
// （ProxyProviders.get 方法体被 R8 内联掉了，只剩 $inlined lambda 类）。
// 按「裸对象」实现——与 Map 里的 value 形状一致，最省事也最可能。
func (s *server) getProxyProvider(w http.ResponseWriter, r *http.Request) {
	s.writeProvider(w, engine.ProviderTypeProxy, r.PathValue("name"))
}

// getRuleProvider 对应 GET /providers/rules/{name}（同上，裸对象）。
func (s *server) getRuleProvider(w http.ResponseWriter, r *http.Request) {
	s.writeProvider(w, engine.ProviderTypeRule, r.PathValue("name"))
}

// getProxyProviderHealthcheck 对应 GET /providers/proxies/{name}/healthcheck。
//
// 触发的是一次异步健康检查，因此立即返回空对象（Kotlin 侧该请求的返回类型是 Unit）。
func (s *server) getProxyProviderHealthcheck(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.backend.HealthCheckProvider(name); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *server) writeProviders(w http.ResponseWriter, typ string) {
	list := s.backend.Providers(typ)
	out := providersAll{Providers: make(map[string]providersProvider, len(list))}
	for _, p := range list {
		out.Providers[p.Name] = providerDTO(p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) writeProvider(w http.ResponseWriter, typ, name string) {
	p, ok := s.backend.Provider(typ, name)
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found: "+name)
		return
	}
	writeJSON(w, http.StatusOK, providerDTO(p))
}

func providerDTO(p engine.Provider) providersProvider {
	return providersProvider{
		Name:        p.Name,
		Proxies:     providerProxyList(p.Proxies),
		Type:        p.Type,
		UpdatedAt:   instantOrZero(p.UpdatedUnixMS),
		VehicleType: p.VehicleType,
	}
}

// providerProxyList 把 provider 的节点名列表转成完整 Proxy 对象列表。
// 与 /proxies 一致：元素必须是对象，不能是字符串（真机崩溃实证见 dto.go）。
// 这里拿不到全局索引，只能给出只带名字的最小对象——UI 展示 provider 内容时
// 通常只列名字，需要完整信息时再按名字查 /proxies/{name}。
func providerProxyList(names []string) []proxiesProxy {
	out := make([]proxiesProxy, 0, len(names))
	for _, n := range names {
		out = append(out, proxiesProxy{
			Name: n,
			// type 必须是 Proxies.Type 的合法取值（只有 Direct / Selector 两个）。
			// 留空串会让 kotlinx 抛：
			//   Proxies.Type does not contain element with name ''
			// 这里给 Direct 作保守占位——真机 UI 若只按名字展示，不会察觉；
			// 若需要精确类型，应当按名字回查 /proxies（后续优化）。
			Type:      "Direct",
			History:   []proxiesProxyDelay{},
			Proxies:   []proxiesProxy{},
			Providers: []string{},
		})
	}
	return out
}
