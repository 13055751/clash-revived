package engine

import (
	"fmt"
	"net"
	"strconv"

	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/listener"
)

// CFA 侧 Listeners.Type 的 8 个取值（dex：Listeners$Type.<clinit>）。
const (
	ListenerDNS                = "DNS"
	ListenerExternalController = "ExternalController"
	ListenerHTTPSocks5         = "HTTPSocks5"
	ListenerRedirect           = "Redirect"
	ListenerSystemProxy        = "SystemProxy"
	ListenerTUN                = "TUN"
	ListenerTransProxy         = "TransProxy"
	ListenerVPN                = "VPN"
)

// listenerOrder 是 Listeners.Type 的枚举声明顺序，输出保持同样顺序。
var listenerOrder = []string{
	ListenerDNS,
	ListenerExternalController,
	ListenerHTTPSocks5,
	ListenerRedirect,
	ListenerSystemProxy,
	ListenerTUN,
	ListenerTransProxy,
	ListenerVPN,
}

// Listener 是一个监听器的运行时快照。
// Status 的键名直接就是线上 JSON 的键（已按 Listeners.Listener.Status$* 的字段名对齐），
// endpoint 层只负责加 "type" 判别符。
type Listener struct {
	Type    string
	Enabled bool
	Status  map[string]any
}

// Listeners 返回 8 个监听器的状态。未启动时全部 enabled=false 且字段为零值，
// 保证端点在任何时刻都能给出完整形状。
func (e *Engine) Listeners() []Listener {
	ports := listener.GetPorts()
	tun := listener.GetTunConf()
	allowLan := listener.AllowLan()
	bind := listener.BindAddress()

	authUsers := []any{}
	if g := executor.GetGeneral(); g != nil {
		for _, u := range g.Authentication {
			authUsers = append(authUsers, u)
		}
	}

	var dnsListen string
	var dnsIPv6, dnsUseHosts, dnsEnabled bool
	if cfg := e.ParsedConfig(); cfg != nil {
		dnsEnabled = cfg.DNS.Enable && cfg.DNS.Listen != ""
		dnsListen = listenAt(cfg.DNS.Listen, allowLan, bind)
		dnsIPv6 = cfg.DNS.IPv6
		dnsUseHosts = cfg.DNS.UseHosts
	}

	// HTTPSocks5 在 CFA 里是一个逻辑监听器：mixed 优先，其次 http，再次 socks。
	httpPort := ports.MixedPort
	if httpPort == 0 {
		httpPort = ports.Port
	}
	if httpPort == 0 {
		httpPort = ports.SocksPort
	}

	e.mu.RLock()
	platform := make(map[string]bool, len(e.listeners))
	for k, v := range e.listeners {
		platform[k] = v
	}
	ctrlAddr, ctrlSecret := e.ctrlAddr, e.ctrlSecret
	e.mu.RUnlock()

	// TUN 状态字段语义映射（TODO 待联调确认）：
	//   boosted       <- tun.GSO（CFA 的 "boost" 与 mihomo 的 gso 是同一个开关）
	//   routeInserted <- tun.AutoRoute
	//   dnsHijacked   <- len(tun.DNSHijack) > 0
	tunAddr := ""
	var tunRoute, tunHijack bool
	if tun.Enable {
		if len(tun.Inet4Address) > 0 {
			tunAddr = tun.Inet4Address[0].String()
		}
		tunRoute = tun.AutoRoute
		tunHijack = len(tun.DNSHijack) > 0
	}

	out := make([]Listener, 0, len(listenerOrder))
	for _, typ := range listenerOrder {
		var l Listener
		l.Type = typ
		switch typ {
		case ListenerDNS:
			l.Enabled = dnsEnabled
			l.Status = map[string]any{
				"ipv6":     dnsIPv6,
				"listenAt": dnsListen,
				"useHosts": dnsUseHosts,
			}
		case ListenerExternalController:
			l.Enabled = ctrlAddr != ""
			l.Status = map[string]any{
				"listenAt": ctrlAddr,
				"secret":   ctrlSecret,
			}
		case ListenerHTTPSocks5:
			addr := listenAtPort(httpPort, allowLan, bind)
			l.Enabled = httpPort != 0
			l.Status = map[string]any{
				"listenAt": addr,
				"users":    authUsers,
			}
		case ListenerRedirect:
			addr := listenAtPort(ports.RedirPort, allowLan, bind)
			l.Enabled = ports.RedirPort != 0
			l.Status = map[string]any{"listenAt": addr}
		case ListenerSystemProxy:
			l.Enabled = platform[ListenerSystemProxy]
			l.Status = map[string]any{
				"listenAt": "",
				"excludes": []any{},
			}
		case ListenerTUN:
			l.Enabled = tun.Enable
			l.Status = map[string]any{
				"address":       tunAddr,
				"boosted":       tun.GSO,
				"dnsHijacked":   tunHijack,
				"routeInserted": tunRoute,
			}
		case ListenerTransProxy:
			addr := listenAtPort(ports.TProxyPort, allowLan, bind)
			l.Enabled = ports.TProxyPort != 0
			l.Status = map[string]any{
				"listenAt": addr,
				// mihomo 的 tproxy 同时接管 UDP；CFA 的 udp 字段即此含义。
				"udp": ports.TProxyPort != 0,
			}
		case ListenerVPN:
			l.Enabled = platform[ListenerVPN]
			l.Status = map[string]any{
				"dnsHijacked":          false,
				"hasChanges":           false,
				"systemProxyListening": platform[ListenerSystemProxy],
			}
		}
		out = append(out, l)
	}
	return out
}

// SetListenerEnabled 记录监听器开关。mihomo 侧真正生效的部分（端口类监听器）
// 需要重建 listener，P0 只做状态记录，端口重建留到 P2 与 platform 层一起做。
func (e *Engine) SetListenerEnabled(typ string, enabled bool) error {
	if !ValidListenerType(typ) {
		return fmt.Errorf("engine: unknown listener %q", typ)
	}
	// TUN / VPN 需要宿主提供 tun fd，必须真的动引擎（见 tun.go）。
	if typ == ListenerTUN || typ == ListenerVPN {
		if enabled {
			if err := e.enableTun(typ); err != nil {
				return err
			}
		} else {
			if err := e.disableTun(typ); err != nil {
				return err
			}
		}
		e.mu.Lock()
		e.listeners[typ] = enabled
		e.mu.Unlock()
		return nil
	}
	if typ == ListenerHTTPSocks5 || typ == ListenerRedirect ||
		typ == ListenerTransProxy || typ == ListenerDNS {
		// TODO(P2): 这几个需要重建 mihomo 的 listener，暂只记录状态。
	}
	e.mu.Lock()
	e.listeners[typ] = enabled
	e.mu.Unlock()
	return nil
}

// ListenerTypes 返回 CFA 定义的 8 个监听器名，按枚举声明顺序。
func ListenerTypes() []string {
	out := make([]string, len(listenerOrder))
	copy(out, listenerOrder)
	return out
}

// ValidListenerType 判断名字是否是 Listeners.Type 的合法取值。
func ValidListenerType(typ string) bool { return validListenerType(typ) }

func validListenerType(typ string) bool {
	for _, t := range listenerOrder {
		if t == typ {
			return true
		}
	}
	return false
}

// listenAtPort 复刻 mihomo listener.genAddr 的语义，返回空串表示该端口没开。
func listenAtPort(port int, allowLan bool, bind string) string {
	if port == 0 {
		return ""
	}
	if !allowLan {
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	if bind == "" || bind == "*" {
		return ":" + strconv.Itoa(port)
	}
	return net.JoinHostPort(bind, strconv.Itoa(port))
}

// listenAt 处理配置里已是 "host:port" 形式的监听地址（DNS 用）。
func listenAt(configured string, allowLan bool, bind string) string {
	if configured == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(configured)
	if err != nil || port == "" {
		return configured
	}
	return listenAtPort(atoiOr(port, 0), allowLan, bind)
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}
