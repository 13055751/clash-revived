package engine

import (
	"fmt"
	"sort"
	"time"

	"github.com/metacubex/mihomo/adapter/provider"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/tunnel"
)

// CFA 侧 Providers.Type 的两个取值（dex：Providers$Type.<clinit> => Proxy, Rule）。
const (
	ProviderTypeProxy = "Proxy"
	ProviderTypeRule  = "Rule"
)

// CFA 侧 Vehicle.Type 只有 File / HTTP 两个取值（dex：Vehicle$Type => File, HTTP）。
const (
	VehicleFile = "File"
	VehicleHTTP = "HTTP"
)

// Provider 是一个 provider 的快照。
type Provider struct {
	Name string
	Type string // ProviderTypeProxy / ProviderTypeRule
	// VehicleType 是 CFA 语义的 File/HTTP；mihomo 的 Inline/Compatible 归到 File（见 vehicleName）。
	VehicleType string
	// Proxies 是 proxy provider 里的节点名；rule provider 为空。
	Proxies []string
	// RuleCount 是 rule provider 的规则条数（CFA 模型里没有这个字段，留作内部/后续用）。
	RuleCount int
	// UpdatedUnixMS 是「最后更新时刻」的近似值（mihomo 不暴露更新时间，见 touched）。
	UpdatedUnixMS int64
}

// Providers 返回指定类型的 provider 快照；typ 传空串表示两类都要。按名字排序。
func (e *Engine) Providers(typ string) []Provider {
	if !e.Running() {
		return nil
	}
	out := make([]Provider, 0, 8)
	now := time.Now()

	for name, prov := range tunnel.Providers() {
		if name == provider.ReservedName {
			continue // mihomo 给内置出站建的内部 provider，不对 UI 暴露
		}
		if typ != "" && typ != ProviderTypeProxy {
			continue
		}
		names := make([]string, 0, len(prov.Proxies()))
		for _, p := range prov.Proxies() {
			names = append(names, p.Name())
		}
		sort.Strings(names)
		out = append(out, Provider{
			Name:          name,
			Type:          ProviderTypeProxy,
			VehicleType:   vehicleName(prov.VehicleType()),
			Proxies:       names,
			UpdatedUnixMS: e.touched("proxy/"+name, prov.Version(), now),
		})
	}

	for name, prov := range tunnel.RuleProviders() {
		if typ != "" && typ != ProviderTypeRule {
			continue
		}
		out = append(out, Provider{
			Name:          name,
			Type:          ProviderTypeRule,
			VehicleType:   vehicleName(prov.VehicleType()),
			Proxies:       []string{},
			RuleCount:     prov.Count(),
			UpdatedUnixMS: e.touched("rule/"+name, uint32(prov.Count()), now),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Provider 查单个 provider。
func (e *Engine) Provider(typ, name string) (Provider, bool) {
	for _, p := range e.Providers(typ) {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// HealthCheckProvider 触发 proxy provider 的健康检查（异步执行，立即返回）。
func (e *Engine) HealthCheckProvider(name string) error {
	if !e.Running() {
		return fmt.Errorf("engine: not running")
	}
	prov, ok := tunnel.Providers()[name]
	if !ok || name == provider.ReservedName {
		return fmt.Errorf("engine: proxy provider %q not found", name)
	}
	prov.HealthCheck()
	return nil
}

// touched 维护 provider 的「最后观察到的变化时刻」。
//
// mihomo 的 Provider 接口没有更新时间，只有 Version()（proxy provider，内容哈希）；
// rule provider 连 Version 都没有，用规则条数顶替。
//
// TODO(P1c): store 的 `providers` 表里有 `updatedAt TIMESTAMP` 列（见 STORE_SCHEMA.md），
// 当 profile 已经落库时应当直接读那一列，不要再走这里的推断。本函数届时退化为
// 「provider 来自运行时（inline/临时）」时的兜底。
//
// 语义刻意保守：**首次见到时不知道它是什么时候更新的，就报 0（未知）**，
// 不拿"引擎启动时刻"冒充——从磁盘缓存加载的 provider 可能是三天前的数据，
// 报成"刚刚更新"会让用户误判节点有效性。只有真的观察到版本发生变化，才记 now。
func (e *Engine) touched(key string, version uint32, now time.Time) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.provMeta == nil {
		e.provMeta = make(map[string]*provMeta, 8)
	}
	meta, ok := e.provMeta[key]
	if !ok {
		e.provMeta[key] = &provMeta{version: version}
		return 0
	}
	if meta.version != version {
		meta.version = version
		meta.updatedAt = now
	}
	if meta.updatedAt.IsZero() {
		// 仍然未知：必须继续报 0，不能把零值 time.Time 的 UnixMilli 漏出去
		// （那会变成 0001-01-01，与本函数的契约"0 表示未知"不一致）。
		return 0
	}
	return meta.updatedAt.UnixMilli()
}

type provMeta struct {
	version   uint32
	updatedAt time.Time
}

// vehicleName 把 mihomo 的 vehicle 归一到 CFA 的两值模型。
//
// TODO(联调): mihomo 有 File/HTTP/Compatible/Inline 四种，CFA 只认 File/HTTP。
// Inline（配置内联）与 Compatible（兼容模式）暂时都报 File——它们本质都是本地内容，
// 若 UI 对这两者需要区别显示，得先在模型里加值。
func vehicleName(v P.VehicleType) string {
	if v == P.HTTP {
		return VehicleHTTP
	}
	return VehicleFile
}
