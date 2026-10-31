package endpoint

// 本文件是「线上 JSON 形状」的唯一真相：字段名、类型、是否可空都照
// api_dataclasses.json 与 classes.dex 的反序列化器对齐。
//
// 三条硬规则（Kotlin 侧是 kotlinx.serialization，默认 ignoreUnknownKeys=false）：
//  1. **不多发字段**：模型里没有的键一律不加，否则整条响应会被判为未知键。
//  2. **不漏发字段**：模型里的字段（除 nullable）全部显式输出，false/""/0 也要发。
//  3. **空集合发 [] 而不是 null**：kotlinx 的 List 反序列化不接受 null。

import "time"

// executorStatus 对应 Executor.Status。
// error 在 Kotlin 里是 String?（可空），所以用指针：无错时输出 null。
type executorStatus struct {
	Error     *string `json:"error"`
	ProfileID string  `json:"profileId"`
	Type      string  `json:"type"`
}

// listenersAll 对应 Listeners.Listener.All：响应是包装体，不是裸 Map。
// 键是 Listeners.Type 的枚举名（DNS / ExternalController / …）。
type listenersAll struct {
	Listeners map[string]listenerDTO `json:"listeners"`
}

// listenerDTO 对应 Listeners.Listener。
type listenerDTO struct {
	Enabled bool `json:"enabled"`
	// Status 是 sealed 子类型之一（见下方各 listenerStatus*）。
	Status any `json:"status"`
}

// 以下是 Listeners.Listener.Status 的 8 个 sealed 子类型。
// kotlinx 的 sealed 多态序列化用 "type" 判别符（Json 默认 classDiscriminator）。
// TODO(联调): 判别符名与字段集合需用真机抓包复核；默认判别符就是 "type"。

type listenerStatusDNS struct {
	Type     string `json:"type"`
	IPv6     bool   `json:"ipv6"`
	ListenAt string `json:"listenAt"`
	UseHosts bool   `json:"useHosts"`
}

type listenerStatusExternalController struct {
	Type     string `json:"type"`
	ListenAt string `json:"listenAt"`
	Secret   string `json:"secret"`
}

type listenerStatusHTTPSocks5 struct {
	Type     string   `json:"type"`
	ListenAt string   `json:"listenAt"`
	Users    []string `json:"users"`
}

type listenerStatusRedirect struct {
	Type     string `json:"type"`
	ListenAt string `json:"listenAt"`
}

type listenerStatusSystemProxy struct {
	Type     string   `json:"type"`
	Excludes []string `json:"excludes"`
	ListenAt string   `json:"listenAt"`
}

type listenerStatusTUN struct {
	Type          string `json:"type"`
	Address       string `json:"address"`
	Boosted       bool   `json:"boosted"`
	DNSHijacked   bool   `json:"dnsHijacked"`
	RouteInserted bool   `json:"routeInserted"`
}

type listenerStatusTransProxy struct {
	Type     string `json:"type"`
	ListenAt string `json:"listenAt"`
	UDP      bool   `json:"udp"`
}

type listenerStatusVPN struct {
	Type                 string `json:"type"`
	DNSHijacked          bool   `json:"dnsHijacked"`
	HasChanges           bool   `json:"hasChanges"`
	SystemProxyListening bool   `json:"systemProxyListening"`
}

// proxiesProxy 对应 Proxies.Proxy。
//
// 【2026-10-31 真机崩溃实证】
// 原先 Proxies 与 Providers 都是 []string（节点名列表），UI 启动即崩：
//
//	JsonDecodingException: Expected start of the object '{', but had '[' instead
//	at path: $.proxies[0]
//
// 栈里 CollectionLikeSerializer.readElement -> Proxies$Proxy$$serializer.deserialize
// 是**递归**调用，证明 proxies 是 List<Proxies.Proxy>，每个元素是完整对象。
// 悬停注释里原有的「泛型擦除、无法确认」到此结案：是对象列表，不是名字列表。
type proxiesProxy struct {
	History []proxiesProxyDelay `json:"history"`
	Name    string              `json:"name"`
	Now     string              `json:"now"`
	// Providers：selector 组所属的 provider 名（原版这里是名字，仍按名字）。
	Providers []string `json:"providers"`
	// Proxies：组内成员，每个都是完整的 Proxy 对象（递归）。
	Proxies []proxiesProxy `json:"proxies"`
	Type    string         `json:"type"`
	UDP     bool           `json:"udp"`
}

// proxiesProxyDelay 对应 Proxies.Proxy.Delay。time 是 Kotlin 的 Instant，
// kotlinx-datetime 默认用 ISO-8601（Instant.parse 接受带 Z 的形式）。
type proxiesProxyDelay struct {
	Delay     int    `json:"delay"`
	MeanDelay int    `json:"meanDelay"`
	Time      string `json:"time"`
}

// instant 把毫秒时间戳转成 kotlinx-datetime 的 Instant 文本形式。
func instant(unixMS int64) string {
	return time.UnixMilli(unixMS).UTC().Format("2006-01-02T15:04:05.000Z")
}

// instantOrZero 用于「时间未知」的场合：字段是非空 Instant，必须给值，
// 所以用零值 0001-01-01T00:00:00Z 表达"不知道"，而不是伪造一个"刚刚"。
func instantOrZero(unixMS int64) string {
	if unixMS == 0 {
		return "0001-01-01T00:00:00.000Z"
	}
	return instant(unixMS)
}

// stageDTO 对应 app/core 的 CoreManager.Stage（sealed：Dead/Initializing/Running/Failed）。
// TODO(联调): /stage 不在 dex 的 const-string 里，路径来自 API_CONTRACT 的推断。
type stageDTO struct {
	Type   string  `json:"type"`
	Reason *string `json:"reason,omitempty"`
}

// providersAll 对应 Providers.Provider.All：{providers: {名字: Provider}}。
// 依据：该类只有一个字段 providers: Map；且 Providers.Type 的取值是 Proxy/Rule，
// 说明 /providers/proxies 与 /providers/rules 复用同一个包装体，靠 type 区分。
type providersAll struct {
	Providers map[string]providersProvider `json:"providers"`
}

// providersProvider 对应 Providers.Provider。
//
// TODO(联调): proxies 里到底是「节点名列表」还是「Proxies.Proxy 对象列表」没有直接证据
// （泛型擦除）。这里按节点名实现——若真机报类型不符，改成对象列表即可。
// rule provider 的 proxies 恒为 []。
type providersProvider struct {
	Name string `json:"name"`
	// Proxies：同为完整 Proxy 对象列表（与 Proxies.Proxy.Proxies 一致）。
	// rule provider 恒为 []。
	Proxies     []proxiesProxy `json:"proxies"`
	Type        string         `json:"type"`
	UpdatedAt   string         `json:"updatedAt"`
	VehicleType string         `json:"vehicleType"`
}

// --- 会话（/profiles/sessions/*）---

// sessionNewResult 对应 Sessions.newSession.Result。
type sessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// sessionAttributes 对应 Profiles.Profile.Attributes（会话的属性视图）。
type sessionAttributes struct {
	Name       string `json:"name"`
	RemoteURL  string `json:"remoteURL"`
	UpdateExpr string `json:"updateExpr"`
	UpdatedAt  string `json:"updatedAt"`
}

// sessionProviderDescriptor 对应 Sessions.ProviderDescriptor。
type sessionProviderDescriptor struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	VehicleType string `json:"vehicleType"`
	Length      int    `json:"length"`
}

// sessionProviderDescriptorAll 对应 Sessions.ProviderDescriptor.All。
type sessionProviderDescriptorAll struct {
	Providers []sessionProviderDescriptor `json:"providers"`
}

// sessionStoreStatus 对应 Sessions.StoreStatus。
type sessionStoreStatus struct {
	Error            string          `json:"error"`
	ProviderProgress sessionProgress `json:"providerProgress"`
	Stage            string          `json:"stage"`
}

type sessionProgress struct {
	Completed int `json:"completed"`
	Total     int `json:"total"`
}

// profilesConfig 对应 Profiles.Config。
type profilesConfig struct {
	Updater profilesUpdater `json:"updater"`
}

type profilesUpdater struct {
	MaxRetryCount int                   `json:"maxRetryCount"`
	Notify        profilesUpdaterNotify `json:"notify"`
	WithTunnel    bool                  `json:"withTunnel"`
}

type profilesUpdaterNotify struct {
	OnFailure bool `json:"onFailure"`
	OnSuccess bool `json:"onSuccess"`
}

// --- /profiles ---

// profilesProfileAll 对应 Profiles.Profile.All：包装体，唯一字段 profiles: List。
type profilesProfileAll struct {
	Profiles []profilesProfile `json:"profiles"`
}

// profilesProfile 对应 Profiles.Profile。
type profilesProfile struct {
	ID         string                    `json:"id"`
	Attributes profilesProfileAttributes `json:"attributes"`
	UserInfo   profilesProfileUserInfo   `json:"userInfo"`
}

// profilesProfileAttributes 对应 Profiles.Profile.Attributes。
type profilesProfileAttributes struct {
	Name       string `json:"name"`
	RemoteURL  string `json:"remoteURL"`
	UpdateExpr string `json:"updateExpr"`
	UpdatedAt  string `json:"updatedAt"`
}

// profilesProfileUserInfo 对应 Profiles.Profile.UserInfo。
//
// 注意：库里的列叫 uploaded / downloaded（多一个 ed），API 里叫 upload / download。
type profilesProfileUserInfo struct {
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Total    int64  `json:"total"`
	ExpireAt string `json:"expireAt"`
}

// executorConfig 对应 Executor.Config。
type executorConfig struct {
	ActiveProfile            string `json:"activeProfile"`
	ExternalController       string `json:"externalController"`
	ExternalControllerSecret string `json:"externalControllerSecret"`
}

// --- /general /network/* ---

// generalConfig 对应 General.Config。mode 线上是小写。
type generalConfig struct {
	Mode string `json:"mode"`
}

// networkInterface 对应 Network.Interface。
type networkInterface struct {
	AutoDetect bool   `json:"autoDetect"`
	Name       string `json:"name"`
}

// networkResolver 对应 Network.Resolver。
type networkResolver struct {
	BootstrapServers []string              `json:"bootstrapServers"`
	FallbackFilter   networkFallbackFilter `json:"fallbackFilter"`
	FallbackServers  []string              `json:"fallbackServers"`
	NameServerPolicy map[string]any        `json:"nameServerPolicy"`
	PrimaryServers   []string              `json:"primaryServers"`
}

// networkFallbackFilter 对应 Network.Resolver.FallbackFilter。
type networkFallbackFilter struct {
	Domains   []string `json:"domains"`
	Geoip     bool     `json:"geoip"`
	GeoipCode string   `json:"geoipCode"`
	Ipcidr    []string `json:"ipcidr"`
}

// networkFakeIP 对应 Network.FakeIP。
type networkFakeIP struct {
	Enabled bool     `json:"enabled"`
	Filter  []string `json:"filter"`
	Range   string   `json:"range"`
}

// networkOutbound 对应 Network.Outbound。
type networkOutbound struct {
	Ipv6 bool `json:"ipv6"`
}

// transmitBytes 对应 Transmit.Bytes —— /traffic 与 /bandwidth 的 WebSocket 帧。
//
// TODO(联调): direct*/proxy* 四个字段是"直连/代理"的分流统计，本内核暂不区分
// （mihomo 只按连接算总量），一律给 0。up/down 是真实值。
type transmitBytes struct {
	DirectDown int64 `json:"directDown"`
	DirectUp   int64 `json:"directUp"`
	Down       int64 `json:"down"`
	ProxyDown  int64 `json:"proxyDown"`
	ProxyUp    int64 `json:"proxyUp"`
	Up         int64 `json:"up"`
}

// connectionsCount 对应 Connections.Count。
type connectionsCount struct {
	History int64 `json:"history"`
	Now     int64 `json:"now"`
}
