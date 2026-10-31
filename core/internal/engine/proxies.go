package engine

import (
	"errors"
	"fmt"
	"sort"

	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/adapter/provider"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
)

// CFA 侧 Proxies.Type 只有两个取值（dex：Proxies$Type.<clinit> => Direct, Selector）。
const (
	ProxyTypeDirect   = "Direct"
	ProxyTypeSelector = "Selector"
)

// Delay 是一次延迟测试结果，对应 Proxies.Proxy.Delay。
type Delay struct {
	DelayMS     int
	MeanDelayMS int
	TimeUnixMS  int64
}

// Proxy 是 /proxies 的领域快照。字段语义与 mihomo 无关的部分在注释里说明。
type Proxy struct {
	Name string
	Type string // ProxyTypeDirect / ProxyTypeSelector
	UDP  bool
	// Now 是 selector 组当前选中的节点名；单节点为空串。
	Now string
	// Providers 是提供这个节点的 provider 名（静态节点为空）。
	Providers []string
	// Proxies 是 selector 组成员名（单节点为空）。
	Proxies []string
	// History 按时间顺序给出最近 10 次延迟测试。
	History []Delay
}

// Proxies 返回全部出站代理（含 DIRECT/REJECT/GLOBAL 与各 selector 组），按名字排序。
// 未启动时返回空切片。
func (e *Engine) Proxies() []Proxy {
	if !e.Running() {
		return nil
	}
	all := tunnel.Proxies()
	providers := providerIndex()

	out := make([]Proxy, 0, len(all))
	for name, p := range all {
		out = append(out, snapshotProxy(name, p, providers[name]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Proxy 查一个代理。
func (e *Engine) Proxy(name string) (Proxy, bool) {
	if !e.Running() {
		return Proxy{}, false
	}
	p, ok := tunnel.Proxies()[name]
	if !ok {
		return Proxy{}, false
	}
	return snapshotProxy(name, p, providerIndex()[name]), true
}

// SelectProxy 切换 selector 组的选择（对应 PUT /proxies/{name}）。
func (e *Engine) SelectProxy(group, target string) error {
	if !e.Running() {
		return errors.New("engine: not running")
	}
	p, ok := tunnel.Proxies()[group]
	if !ok {
		return fmt.Errorf("engine: proxy %q not found", group)
	}
	sel, ok := p.Adapter().(outboundgroup.SelectAble)
	if !ok {
		return fmt.Errorf("engine: proxy %q is not a selector", group)
	}
	if err := sel.Set(target); err != nil {
		return fmt.Errorf("engine: select %q -> %q: %w", group, target, err)
	}
	return nil
}

// providerIndex 反查「代理名 -> provider 名」。mihomo 的 proxy 对象不带 provider，
// 只能从 provider 列表反推；provider 数量很小，每次现算足够。
//
// 会跳过 mihomo 的保留 provider（provider.ReservedName == "default"）：
// 它是内核给内置代理建的内部门，不是用户配置的 provider，不该出现在 UI 里。
func providerIndex() map[string][]string {
	provs := tunnel.Providers()
	if len(provs) == 0 {
		return nil
	}
	idx := make(map[string][]string, len(provs))
	for pname, prov := range provs {
		if pname == provider.ReservedName {
			continue
		}
		for _, p := range prov.Proxies() {
			idx[p.Name()] = append(idx[p.Name()], pname)
		}
	}
	for k := range idx {
		sort.Strings(idx[k])
	}
	return idx
}

// snapshotProxy 把 mihomo 的 C.Proxy 转成领域快照。
func snapshotProxy(name string, p C.Proxy, providers []string) Proxy {
	out := Proxy{
		Name:      name,
		Type:      ProxyTypeDirect,
		UDP:       p.SupportUDP(),
		Providers: providers,
	}
	if group, ok := p.Adapter().(outboundgroup.ProxyGroup); ok {
		out.Type = ProxyTypeSelector
		out.Now = group.Now()
		members := group.Proxies()
		names := make([]string, 0, len(members))
		for _, m := range members {
			names = append(names, m.Name())
		}
		out.Proxies = names
	}
	out.History = historySnapshot(p.DelayHistory())
	return out
}

// historySnapshot 转换延迟历史；meanDelay 取历史延迟的算术平均（CFA 的字段，
// 原内核也是按历史均值算，见 Proxies$Proxy$Delay.meanDelay）。
func historySnapshot(hist []C.DelayHistory) []Delay {
	if len(hist) == 0 {
		return nil
	}
	out := make([]Delay, 0, len(hist))
	var sum int
	for _, h := range hist {
		out = append(out, Delay{
			DelayMS:    int(h.Delay),
			TimeUnixMS: h.Time.UnixMilli(),
		})
		sum += int(h.Delay)
	}
	mean := sum / len(hist)
	for i := range out {
		out[i].MeanDelayMS = mean
	}
	return out
}
