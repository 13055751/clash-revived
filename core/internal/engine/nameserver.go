package engine

import (
	"sort"
	"strings"

	"github.com/metacubex/mihomo/dns"
)

// nameServerString 把 mihomo 解析后的 dns.NameServer 还原成配置里的原始写法。
//
// 还原规则由正向解析逻辑反推（config.parseNameServer）：
//
//	Net=""      Addr 是 host:port         -> 原样输出
//	Net="tcp"   Addr 是 host:port         -> "tcp://" + Addr
//	Net="tls"   Addr 是 host:port         -> "tls://" + Addr
//	Net="quic"  Addr 是 host:port         -> "quic://" + Addr
//	Net="h3"    Addr 是 host:port         -> "h3://" + Addr
//	Net="https" Addr **已是完整 URL**      -> 原样输出（解析时用 url.URL.String() 重建过）
//
// proxyName 与 params 都挂在 "#" 之后、用 "&" 连接（解析侧是 Split(u.Fragment, "&")，
// 每段 SplitN("=", 2)：一段 = proxyName，两段 = param）。
//
// 已知的字面差异：原配置写 `223.5.5.5`（不写端口）会被解析成 `223.5.5.5:53`，
// 还原回去也是带端口的。信息在解析阶段就丢了，带端口比留空诚实（留空 UI 会显示
// "没有 DNS 服务器"，用户以为配置坏了）。
func nameServerString(ns dns.NameServer) string {
	addr := ns.Addr
	var b strings.Builder
	switch ns.Net {
	case "", "udp":
		b.WriteString(addr)
	case "https":
		b.WriteString(addr) // 已经是完整 URL
	default:
		b.WriteString(ns.Net)
		b.WriteString("://")
		b.WriteString(addr)
	}

	// fragment 部分：proxyName 与 params 用 & 连接，顺序稳定（proxyName 在前）。
	parts := make([]string, 0, len(ns.Params)+1)
	if ns.ProxyName != "" {
		parts = append(parts, ns.ProxyName)
	}
	keys := make([]string, 0, len(ns.Params))
	for k := range ns.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys) // map 迭代无序，排序保证同一份配置每次输出一致
	for _, k := range keys {
		parts = append(parts, k+"="+ns.Params[k])
	}
	if len(parts) > 0 {
		b.WriteString("#")
		b.WriteString(strings.Join(parts, "&"))
	}
	return b.String()
}

// nameServerStrings 转换一组，跳过空项。
func nameServerStrings(list []dns.NameServer) []string {
	out := make([]string, 0, len(list))
	for _, ns := range list {
		if s := nameServerString(ns); s != "" {
			out = append(out, s)
		}
	}
	return out
}
