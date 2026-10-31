package endpoint

import (
	"log/slog"
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// getProxies 对应 GET /proxies。
//
// TODO(联调): 响应体按「裸 Map<名字, Proxies.Proxy>」实现——dex 里不存在
// Proxies.Proxy.All 这样的包装体数据类，而 /listeners、/providers/* 都有各自的
// All 包装体，这个缺失是「裸 Map」判定的主要依据。若真机抓包显示有包装体，
// 只改本函数一行。
func (s *server) getProxies(w http.ResponseWriter, r *http.Request) {
	list := s.backend.Proxies()
	// 先建名字索引：组的成员要展开成完整对象，需要能按名字回查。
	index := make(map[string]engine.Proxy, len(list))
	for _, p := range list {
		index[p.Name] = p
	}
	out := make(map[string]proxiesProxy, len(list))
	for _, p := range list {
		if builtinProxy(p.Name) {
			continue
		}
		out[p.Name] = proxyDTO(p, index, 0)
	}
	writeJSON(w, http.StatusOK, out)
}

// getProxy 对应 GET /proxies/{name}（单个代理）。
func (s *server) getProxy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if builtinProxy(name) {
		// 内置出站对宿主不可见，查询也按不存在处理，避免两种入口行为不一致。
		writeError(w, http.StatusNotFound, "proxy not found: "+name)
		return
	}
	p, ok := s.backend.Proxy(name)
	if !ok {
		writeError(w, http.StatusNotFound, "proxy not found: "+name)
		return
	}
	index := map[string]engine.Proxy{}
	for _, q := range s.backend.Proxies() {
		index[q.Name] = q
	}
	writeJSON(w, http.StatusOK, proxyDTO(p, index, 0))
}

// putProxy 对应 PUT /proxies/{name}，body = Proxies.put.PutProxy{name}：
// 把名为 {name} 的 selector 组切换到 body.name 指定的节点。
func (s *server) putProxy(w http.ResponseWriter, r *http.Request) {
	group := r.PathValue("name")
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "body.name is required")
		return
	}
	if err := s.backend.SelectProxy(group, body.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 选择要落库（proxy_selections），否则重启丢选择。
	// 没有激活 profile 时实现侧会自行跳过，失败也不影响本次切换。
	if err := s.backend.SaveProxySelection(group, body.Name); err != nil {
		slog.Warn("persist proxy selection failed", "group", group, "proxy", body.Name, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// builtinProxy 判断是否是 mihomo 的内置出站。
//
// 原内核（CFA 3.0.3）只吐 DIRECT / REJECT / GLOBAL，而 mihomo 额外建了这四个。
// 直接吐给 UI 会凭空多出 4 个「节点」，用户会以为配置里多了东西。
// **只在这一层过滤**：engine 内部保留它们，规则里引用 PASS / REJECT-DROP 照常工作。
var builtinProxyNames = map[string]bool{
	"COMPATIBLE":  true,
	"PASS":        true,
	"PASS-RULE":   true,
	"REJECT-DROP": true,
}

func builtinProxy(name string) bool { return builtinProxyNames[name] }

// proxyDTO 把领域快照转成线上模型。
//
// proxies 字段的元素是**完整 Proxy 对象**（见 dto.go 里 2026-10-31 的崩溃实证），
// 所以要按名字把成员递归展开。depth 用于防止环形引用把栈打爆——
// 正常配置里 selector 嵌套最深也就几层，超过 maxProxyDepth 就截断成空列表。
const maxProxyDepth = 4

func proxyDTO(p engine.Proxy, index map[string]engine.Proxy, depth int) proxiesProxy {
	out := proxiesProxy{
		Name:      p.Name,
		Type:      p.Type,
		UDP:       p.UDP,
		Now:       p.Now,
		Providers: append([]string{}, p.Providers...),
		Proxies:   make([]proxiesProxy, 0, len(p.Proxies)),
		History:   make([]proxiesProxyDelay, 0, len(p.History)),
	}
	if depth < maxProxyDepth {
		for _, member := range p.Proxies {
			m, ok := index[member]
			if !ok {
				// 名字索引里没有（例如内置出站被过滤），给一个只有名字的最小对象，
				// 不要塞字符串——那正是崩溃的原因。
				out.Proxies = append(out.Proxies, proxiesProxy{
					Name:      member,
					History:   []proxiesProxyDelay{},
					Proxies:   []proxiesProxy{},
					Providers: []string{},
				})
				continue
			}
			out.Proxies = append(out.Proxies, proxyDTO(m, index, depth+1))
		}
	}
	for _, h := range p.History {
		out.History = append(out.History, proxiesProxyDelay{
			Delay:     h.DelayMS,
			MeanDelay: h.MeanDelayMS,
			Time:      instant(h.TimeUnixMS),
		})
	}
	return out
}
