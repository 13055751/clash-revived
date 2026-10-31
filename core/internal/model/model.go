// Code generated from api_dataclasses.json by gen_model.py. DO NOT EDIT MANUALLY.
//
// 这些结构体是 Kotlin 侧 kotlinx.serialization 的镜像。
// 字段名即 JSON key（kotlinx 默认取属性名），改动会导致宿主反序列化失败。
package model

import "encoding/json"

// --- 枚举 ---
// 常量名 = Kotlin 枚举名；常量值 = 线上 JSON 字面量。
// 默认两者相同（kotlinx 用 Enum.name()）。
// 例外：General$Config$Mode 带 @SerialName 烘焙成的小写名数组，值因此不同。
// 证据见 gen_model.py 顶部 SERIALIZED_OVERRIDE 的注释。

type EventsEventExecutorType string

const (
	EventsEventExecutorTypeConfigChanged  EventsEventExecutorType = "ConfigChanged"
	EventsEventExecutorTypeGeneralChanged EventsEventExecutorType = "GeneralChanged"
	EventsEventExecutorTypeStatusChanged  EventsEventExecutorType = "StatusChanged"
)

type EventsEventListenerType string

const (
	EventsEventListenerTypeConfigChanged EventsEventListenerType = "ConfigChanged"
	EventsEventListenerTypeStatusChanged EventsEventListenerType = "StatusChanged"
)

type EventsEventProfileType string

const (
	EventsEventProfileTypeAdded        EventsEventProfileType = "Added"
	EventsEventProfileTypeModified     EventsEventProfileType = "Modified"
	EventsEventProfileTypeReconfigured EventsEventProfileType = "Reconfigured"
	EventsEventProfileTypeRemoved      EventsEventProfileType = "Removed"
)

type GeneralConfigMode string

const (
	GeneralConfigModeDirect GeneralConfigMode = "direct"
	GeneralConfigModeGlobal GeneralConfigMode = "global"
	GeneralConfigModeRule   GeneralConfigMode = "rule"
	GeneralConfigModeScript GeneralConfigMode = "script"
)

type ListenersListenerConfigVPNAccessControlConfigMode string

const (
	ListenersListenerConfigVPNAccessControlConfigModeAllowAll         ListenersListenerConfigVPNAccessControlConfigMode = "AllowAll"
	ListenersListenerConfigVPNAccessControlConfigModeAllowPackages    ListenersListenerConfigVPNAccessControlConfigMode = "AllowPackages"
	ListenersListenerConfigVPNAccessControlConfigModeDisallowPackages ListenersListenerConfigVPNAccessControlConfigMode = "DisallowPackages"
)

type ListenersListenerConfigVPNRouteMode string

const (
	ListenersListenerConfigVPNRouteModeAsDefault       ListenersListenerConfigVPNRouteMode = "AsDefault"
	ListenersListenerConfigVPNRouteModeExcludesPrivate ListenersListenerConfigVPNRouteMode = "ExcludesPrivate"
	ListenersListenerConfigVPNRouteModeNone            ListenersListenerConfigVPNRouteMode = "None"
)

type ListenersType string

const (
	ListenersTypeDNS                ListenersType = "DNS"
	ListenersTypeExternalController ListenersType = "ExternalController"
	ListenersTypeHTTPSocks5         ListenersType = "HTTPSocks5"
	ListenersTypeRedirect           ListenersType = "Redirect"
	ListenersTypeSystemProxy        ListenersType = "SystemProxy"
	ListenersTypeTUN                ListenersType = "TUN"
	ListenersTypeTransProxy         ListenersType = "TransProxy"
	ListenersTypeVPN                ListenersType = "VPN"
)

type ProxiesType string

const (
	ProxiesTypeDirect   ProxiesType = "Direct"
	ProxiesTypeSelector ProxiesType = "Selector"
)

type RequestBuilderMethod string

const (
	RequestBuilderMethodDELETE RequestBuilderMethod = "DELETE"
	RequestBuilderMethodGET    RequestBuilderMethod = "GET"
	RequestBuilderMethodPATCH  RequestBuilderMethod = "PATCH"
	RequestBuilderMethodPOST   RequestBuilderMethod = "POST"
	RequestBuilderMethodPUT    RequestBuilderMethod = "PUT"
)

type SessionsCreateSessionSpecType string

const (
	SessionsCreateSessionSpecTypeEdit    SessionsCreateSessionSpecType = "Edit"
	SessionsCreateSessionSpecTypeNewFile SessionsCreateSessionSpecType = "NewFile"
	SessionsCreateSessionSpecTypeNewURL  SessionsCreateSessionSpecType = "NewURL"
)

type VehicleType string

const (
	VehicleTypeFile VehicleType = "File"
	VehicleTypeHTTP VehicleType = "HTTP"
)

// --- 数据结构 ---

// ConnectionsCount mirrors Connections$Count
type ConnectionsCount struct {
	History int64 `json:"history,omitempty"`
	Now     int64 `json:"now,omitempty"`
}

// EventsEventExecutor mirrors Events$Event$Executor
type EventsEventExecutor struct {
	Type EventsEventExecutorType `json:"type,omitempty"`
}

// EventsEventListener mirrors Events$Event$Listener
type EventsEventListener struct {
	ListenerType ListenersType           `json:"listenerType,omitempty"`
	Type         EventsEventListenerType `json:"type,omitempty"`
}

// EventsEventPayload mirrors Events$Event$Payload
type EventsEventPayload struct {
	Event  json.RawMessage `json:"event,omitempty"`
	Source any             `json:"source,omitempty"`
}

// EventsEventProfile mirrors Events$Event$Profile
type EventsEventProfile struct {
	Type EventsEventProfileType `json:"type,omitempty"`
	Uuid string                 `json:"uuid,omitempty"`
}

// ExecutorConfig mirrors Executor$Config
type ExecutorConfig struct {
	ActiveProfile            string `json:"activeProfile,omitempty"`
	ExternalController       string `json:"externalController,omitempty"`
	ExternalControllerSecret string `json:"externalControllerSecret,omitempty"`
}

// ExecutorStatus mirrors Executor$Status
type ExecutorStatus struct {
	Error     string `json:"error,omitempty"`
	ProfileId string `json:"profileId,omitempty"`
	Type      any    `json:"type,omitempty"`
}

// GeneralConfig mirrors General$Config
type GeneralConfig struct {
	Mode GeneralConfigMode `json:"mode,omitempty"`
}

// ListenersListener mirrors Listeners$Listener
type ListenersListener struct {
	Enabled bool `json:"enabled,omitempty"`
	Status  any  `json:"status,omitempty"`
}

// ListenersListenerAll mirrors Listeners$Listener$All
type ListenersListenerAll struct {
	Listeners map[string]any `json:"listeners,omitempty"`
}

// ListenersListenerConfigDNS mirrors Listeners$Listener$Config$DNS
type ListenersListenerConfigDNS struct {
	Ipv6     bool   `json:"ipv6,omitempty"`
	ListenAt string `json:"listenAt,omitempty"`
	UseHosts bool   `json:"useHosts,omitempty"`
}

// ListenersListenerConfigExternalController mirrors Listeners$Listener$Config$ExternalController
type ListenersListenerConfigExternalController struct {
	ListenAt string `json:"listenAt,omitempty"`
	Secret   string `json:"secret,omitempty"`
}

// ListenersListenerConfigHTTPSocks5 mirrors Listeners$Listener$Config$HTTPSocks5
type ListenersListenerConfigHTTPSocks5 struct {
	ListenAt string         `json:"listenAt,omitempty"`
	Users    map[string]any `json:"users,omitempty"`
}

// ListenersListenerConfigRedirect mirrors Listeners$Listener$Config$Redirect
type ListenersListenerConfigRedirect struct {
	ListenAt string `json:"listenAt,omitempty"`
}

// ListenersListenerConfigSystemProxy mirrors Listeners$Listener$Config$SystemProxy
type ListenersListenerConfigSystemProxy struct {
	Excludes   []any  `json:"excludes,omitempty"`
	ListenAddr string `json:"listenAddr,omitempty"`
}

// ListenersListenerConfigTUN mirrors Listeners$Listener$Config$TUN
type ListenersListenerConfigTUN struct {
	Address   string `json:"address,omitempty"`
	AutoRoute bool   `json:"autoRoute,omitempty"`
	Boost     bool   `json:"boost,omitempty"`
	DnsHijack bool   `json:"dnsHijack,omitempty"`
}

// ListenersListenerConfigTransProxy mirrors Listeners$Listener$Config$TransProxy
type ListenersListenerConfigTransProxy struct {
	ListenAt string `json:"listenAt,omitempty"`
}

// ListenersListenerConfigVPN mirrors Listeners$Listener$Config$VPN
type ListenersListenerConfigVPN struct {
	AccessControl ListenersListenerConfigVPNAccessControlConfig `json:"accessControl,omitempty"`
	DnsHijack     bool                                          `json:"dnsHijack,omitempty"`
	RouteMode     ListenersListenerConfigVPNRouteMode           `json:"routeMode,omitempty"`
	SystemProxy   ListenersListenerConfigVPNSystemProxyConfig   `json:"systemProxy,omitempty"`
}

// ListenersListenerConfigVPNAccessControlConfig mirrors Listeners$Listener$Config$VPN$AccessControlConfig
type ListenersListenerConfigVPNAccessControlConfig struct {
	Mode     ListenersListenerConfigVPNAccessControlConfigMode `json:"mode,omitempty"`
	Packages []any                                             `json:"packages,omitempty"`
}

// ListenersListenerConfigVPNSystemProxyConfig mirrors Listeners$Listener$Config$VPN$SystemProxyConfig
type ListenersListenerConfigVPNSystemProxyConfig struct {
	Enabled  bool  `json:"enabled,omitempty"`
	Excludes []any `json:"excludes,omitempty"`
}

// ListenersListenerStatusDNS mirrors Listeners$Listener$Status$DNS
type ListenersListenerStatusDNS struct {
	Ipv6     bool   `json:"ipv6,omitempty"`
	ListenAt string `json:"listenAt,omitempty"`
	UseHosts bool   `json:"useHosts,omitempty"`
}

// ListenersListenerStatusExternalController mirrors Listeners$Listener$Status$ExternalController
type ListenersListenerStatusExternalController struct {
	ListenAt string `json:"listenAt,omitempty"`
	Secret   string `json:"secret,omitempty"`
}

// ListenersListenerStatusHTTPSocks5 mirrors Listeners$Listener$Status$HTTPSocks5
type ListenersListenerStatusHTTPSocks5 struct {
	ListenAt string `json:"listenAt,omitempty"`
	Users    []any  `json:"users,omitempty"`
}

// ListenersListenerStatusRedirect mirrors Listeners$Listener$Status$Redirect
type ListenersListenerStatusRedirect struct {
	ListenAt string `json:"listenAt,omitempty"`
}

// ListenersListenerStatusSystemProxy mirrors Listeners$Listener$Status$SystemProxy
type ListenersListenerStatusSystemProxy struct {
	Excludes []any  `json:"excludes,omitempty"`
	ListenAt string `json:"listenAt,omitempty"`
}

// ListenersListenerStatusTUN mirrors Listeners$Listener$Status$TUN
type ListenersListenerStatusTUN struct {
	Address       string `json:"address,omitempty"`
	Boosted       bool   `json:"boosted,omitempty"`
	DnsHijacked   bool   `json:"dnsHijacked,omitempty"`
	RouteInserted bool   `json:"routeInserted,omitempty"`
}

// ListenersListenerStatusTransProxy mirrors Listeners$Listener$Status$TransProxy
type ListenersListenerStatusTransProxy struct {
	ListenAt string `json:"listenAt,omitempty"`
	Udp      bool   `json:"udp,omitempty"`
}

// ListenersListenerStatusVPN mirrors Listeners$Listener$Status$VPN
type ListenersListenerStatusVPN struct {
	DnsHijacked          bool `json:"dnsHijacked,omitempty"`
	HasChanges           bool `json:"hasChanges,omitempty"`
	SystemProxyListening bool `json:"systemProxyListening,omitempty"`
}

// NetworkFakeIP mirrors Network$FakeIP
type NetworkFakeIP struct {
	Enabled bool   `json:"enabled,omitempty"`
	Filter  []any  `json:"filter,omitempty"`
	Range   string `json:"range,omitempty"`
}

// NetworkInterface mirrors Network$Interface
type NetworkInterface struct {
	AutoDetect bool   `json:"autoDetect,omitempty"`
	Name       string `json:"name,omitempty"`
}

// NetworkOutbound mirrors Network$Outbound
type NetworkOutbound struct {
	Ipv6 bool `json:"ipv6,omitempty"`
}

// NetworkResolver mirrors Network$Resolver
type NetworkResolver struct {
	BootstrapServers []any                         `json:"bootstrapServers,omitempty"`
	FallbackFilter   NetworkResolverFallbackFilter `json:"fallbackFilter,omitempty"`
	FallbackServers  []any                         `json:"fallbackServers,omitempty"`
	NameServerPolicy map[string]any                `json:"nameServerPolicy,omitempty"`
	PrimaryServers   []any                         `json:"primaryServers,omitempty"`
}

// NetworkResolverFallbackFilter mirrors Network$Resolver$FallbackFilter
type NetworkResolverFallbackFilter struct {
	Domains   []any  `json:"domains,omitempty"`
	Geoip     bool   `json:"geoip,omitempty"`
	GeoipCode string `json:"geoipCode,omitempty"`
	Ipcidr    []any  `json:"ipcidr,omitempty"`
}

// ProfilesConfig mirrors Profiles$Config
type ProfilesConfig struct {
	Updater ProfilesConfigUpdater `json:"updater,omitempty"`
}

// ProfilesConfigUpdater mirrors Profiles$Config$Updater
type ProfilesConfigUpdater struct {
	MaxRetryCount int32                       `json:"maxRetryCount,omitempty"`
	Notify        ProfilesConfigUpdaterNotify `json:"notify,omitempty"`
	WithTunnel    bool                        `json:"withTunnel,omitempty"`
}

// ProfilesConfigUpdaterNotify mirrors Profiles$Config$Updater$Notify
type ProfilesConfigUpdaterNotify struct {
	OnFailure bool `json:"onFailure,omitempty"`
	OnSuccess bool `json:"onSuccess,omitempty"`
}

// ProfilesProfile mirrors Profiles$Profile
type ProfilesProfile struct {
	Attributes ProfilesProfileAttributes `json:"attributes,omitempty"`
	Id         string                    `json:"id,omitempty"`
	UserInfo   ProfilesProfileUserInfo   `json:"userInfo,omitempty"`
}

// ProfilesProfileAll mirrors Profiles$Profile$All
type ProfilesProfileAll struct {
	Profiles []any `json:"profiles,omitempty"`
}

// ProfilesProfileAttributes mirrors Profiles$Profile$Attributes
type ProfilesProfileAttributes struct {
	Name       string `json:"name,omitempty"`
	RemoteURL  string `json:"remoteURL,omitempty"`
	UpdateExpr string `json:"updateExpr,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

// ProfilesProfileUserInfo mirrors Profiles$Profile$UserInfo
type ProfilesProfileUserInfo struct {
	Download int64  `json:"download,omitempty"`
	ExpireAt string `json:"expireAt,omitempty"`
	Total    int64  `json:"total,omitempty"`
	Upload   int64  `json:"upload,omitempty"`
}

// ProvidersProvider mirrors Providers$Provider
type ProvidersProvider struct {
	Name        string      `json:"name,omitempty"`
	Proxies     []any       `json:"proxies,omitempty"`
	Type        any         `json:"type,omitempty"`
	UpdatedAt   string      `json:"updatedAt,omitempty"`
	VehicleType VehicleType `json:"vehicleType,omitempty"`
}

// ProvidersProviderAll mirrors Providers$Provider$All
type ProvidersProviderAll struct {
	Providers map[string]any `json:"providers,omitempty"`
}

// ProxiesProxy mirrors Proxies$Proxy
type ProxiesProxy struct {
	History   []any       `json:"history,omitempty"`
	Name      string      `json:"name,omitempty"`
	Now       string      `json:"now,omitempty"`
	Providers []any       `json:"providers,omitempty"`
	Proxies   []any       `json:"proxies,omitempty"`
	Type      ProxiesType `json:"type,omitempty"`
	Udp       bool        `json:"udp,omitempty"`
}

// ProxiesProxyDelay mirrors Proxies$Proxy$Delay
type ProxiesProxyDelay struct {
	Delay     int32  `json:"delay,omitempty"`
	MeanDelay int32  `json:"meanDelay,omitempty"`
	Time      string `json:"time,omitempty"`
}

// RemoteError mirrors RemoteError
type RemoteError struct {
	Message string `json:"message,omitempty"`
}

// ResponseException mirrors ResponseException
type ResponseException struct {
	Error RemoteError `json:"error,omitempty"`
}

// RuleProviders mirrors RuleProviders
type RuleProviders struct {
	Api any `json:"api,omitempty"`
}

// SessionsCreateSessionSpec mirrors Sessions$CreateSessionSpec
type SessionsCreateSessionSpec struct {
	FileContent string                        `json:"fileContent,omitempty"`
	FileName    string                        `json:"fileName,omitempty"`
	ProfileId   string                        `json:"profileId,omitempty"`
	Type        SessionsCreateSessionSpecType `json:"type,omitempty"`
	Url         string                        `json:"url,omitempty"`
}

// SessionsProviderDescriptor mirrors Sessions$ProviderDescriptor
type SessionsProviderDescriptor struct {
	Length      int32       `json:"length,omitempty"`
	Name        string      `json:"name,omitempty"`
	Type        any         `json:"type,omitempty"`
	VehicleType VehicleType `json:"vehicleType,omitempty"`
}

// SessionsProviderDescriptorAll mirrors Sessions$ProviderDescriptor$All
type SessionsProviderDescriptorAll struct {
	Providers []any `json:"providers,omitempty"`
}

// SessionsStoreStatus mirrors Sessions$StoreStatus
type SessionsStoreStatus struct {
	Error            string                                `json:"error,omitempty"`
	ProviderProgress SessionsStoreStatusCompletionProgress `json:"providerProgress,omitempty"`
	Stage            any                                   `json:"stage,omitempty"`
}

// SessionsStoreStatusCompletionProgress mirrors Sessions$StoreStatus$CompletionProgress
type SessionsStoreStatusCompletionProgress struct {
	Completed int32 `json:"completed,omitempty"`
	Total     int32 `json:"total,omitempty"`
}

// TransmitBytes mirrors Transmit$Bytes
type TransmitBytes struct {
	DirectDown int64 `json:"directDown,omitempty"`
	DirectUp   int64 `json:"directUp,omitempty"`
	Down       int64 `json:"down,omitempty"`
	ProxyDown  int64 `json:"proxyDown,omitempty"`
	ProxyUp    int64 `json:"proxyUp,omitempty"`
	Up         int64 `json:"up,omitempty"`
}

// WebSocketMessageBytes mirrors WebSocket$Message$Bytes
type WebSocketMessageBytes struct {
	Bytes []byte `json:"bytes,omitempty"`
}

// WebSocketMessageText mirrors WebSocket$Message$Text
type WebSocketMessageText struct {
	Text string `json:"text,omitempty"`
}

// 确保 json 被使用（RawMessage 字段依赖）
var _ = json.RawMessage(nil)
