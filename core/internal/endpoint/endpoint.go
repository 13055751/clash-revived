// Package endpoint 把 CFA 3.0.3 的 35 组 HTTP 端点挂到 net/http 的 ServeMux 上。
//
// 唯一导出入口：Register(mux *http.ServeMux, e *engine.Engine)。
// 所有路由都在本包内注册，api.go 只负责构造 mux 并调用 Register。
//
// 端点按可用性分档：
//
//	P0（已实现）：/status /listeners /proxies /config /attributes（+ /healthcheck /stage）
//	P1-P3：先注册成 501 桩，桩里写明缺什么，避免宿主拿到 404 而误判为路由错误。
package endpoint

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/kr328/clash-revived/internal/engine"
)

// Backend 是端点层需要的引擎能力。生产实现是 *engine.Engine；
// 测试用假实现，从而能在不启动 mihomo 的前提下校验 JSON 形状。
type Backend interface {
	State() string
	LastError() string
	ProfileID() string

	Listeners() []engine.Listener
	SetListenerEnabled(typ string, enabled bool) error

	Proxies() []engine.Proxy
	Proxy(name string) (engine.Proxy, bool)
	SelectProxy(group, target string) error

	Providers(typ string) []engine.Provider
	Provider(typ, name string) (engine.Provider, bool)
	HealthCheckProvider(name string) error

	Profiles() ([]engine.Profile, error)
	DeleteProfile(id string) error
	RefreshProfile(id string) error
	SaveProxySelection(group, proxyName string) error

	LoadProfilesConfig() (engine.ProfilesConfig, error)
	SaveProfilesConfig(cfg engine.ProfilesConfig) error

	ExecutorConfig() engine.ExecutorConfig
	SetActiveProfile(id string) error

	Mode() string
	SetMode(mode string) error
	Traffic() (up, down int64)
	Total() (up, down int64)
	ConnectionCount() (now, history int64)
	Network() engine.NetworkConfig
	PatchNetwork(fn func(*engine.NetworkConfig))

	NewSession(spec engine.SessionSpec) (string, error)
	Session(id string) (engine.Session, bool)
	DeleteSession(id string) bool
	SetSessionConfig(id string, cfg engine.ProfilesConfig) bool
	SetSessionAttributes(id string, attrs engine.SessionAttributes) bool
	// StoreSession 把会话落成持久 profile，返回其 UUID。
	StoreSession(id string) (string, error)
}

// Register 注册全部端点。
func Register(mux *http.ServeMux, e *engine.Engine) {
	register(mux, e)
}

func register(mux *http.ServeMux, b Backend) {
	s := &server{backend: b}

	// --- P0：数据面 ---
	mux.HandleFunc("GET /status", s.getStatus)
	mux.HandleFunc("GET /listeners", s.getListeners)
	mux.HandleFunc("PUT /listeners/{name}/enabled", s.putListenerEnabled)
	mux.HandleFunc("PATCH /listeners/{name}/config", s.patchListenerConfig)
	mux.HandleFunc("GET /proxies", s.getProxies)
	mux.HandleFunc("GET /proxies/{name}", s.getProxy)
	mux.HandleFunc("PUT /proxies/{name}", s.putProxy)
	mux.HandleFunc("GET /config", s.getConfig)
	mux.HandleFunc("GET /attributes", s.getAttributes)

	// --- 探活 / 生命周期 ---
	mux.HandleFunc("GET /healthcheck", s.getHealthcheck)
	mux.HandleFunc("GET /stage", s.getStage)

	// --- P1：profiles / providers ---
	mux.HandleFunc("GET /profiles", s.getProfiles)
	mux.HandleFunc("PUT /profiles/{id}", s.putProfile)
	mux.HandleFunc("DELETE /profiles/{id}", s.deleteProfile)
	mux.HandleFunc("GET /profiles/config", s.getProfilesConfig)
	mux.HandleFunc("PUT /profiles/config", s.putProfilesConfig)
	mux.HandleFunc("POST /profiles/sessions", s.postSession)
	mux.HandleFunc("GET /profiles/sessions/{id}", s.getSessionStatus)
	// UI 实际轮询的是 .../store（真机日志实证：保存配置后它会反复 GET 这个路径，
	// 拿到 404 就无限重试，表现为「点保存没反应」）。
	// 返回体与 /{id} 相同，都是 Sessions.StoreStatus。
	mux.HandleFunc("GET /profiles/sessions/{id}/store", s.getSessionStatus)
	mux.HandleFunc("DELETE /profiles/sessions/{id}", s.deleteSession)
	mux.HandleFunc("GET /profiles/sessions/{id}/attributes", s.getSessionAttributes)
	mux.HandleFunc("PUT /profiles/sessions/{id}/attributes", s.putSessionAttributes)
	mux.HandleFunc("GET /profiles/sessions/{id}/config", s.getSessionConfig)
	mux.HandleFunc("PUT /profiles/sessions/{id}/config", s.putSessionConfig)
	// 真机日志实证：保存时会 POST 这个路径触发下载/校验，之前 404 导致保存流程中断。
	mux.HandleFunc("POST /profiles/sessions/{id}/config/update", s.postSessionConfigUpdate)
	mux.HandleFunc("GET /profiles/sessions/{id}/providers", s.getSessionProviders)
	mux.HandleFunc("GET /providers/proxies", s.getProxyProviders)
	mux.HandleFunc("GET /providers/proxies/{name}", s.getProxyProvider)
	mux.HandleFunc("GET /providers/proxies/{name}/healthcheck", s.getProxyProviderHealthcheck)
	mux.HandleFunc("GET /providers/rules", s.getRuleProviders)
	mux.HandleFunc("GET /providers/rules/{name}", s.getRuleProvider)
	// 这两个端点在升了 lambda 类（ProxyProviders$update / RuleProviders$update）里存在，
	// 但路径与语义（是「触发更新」还是「写配置」）dex 里没读全，先留桩。
	stub(mux, "PUT /providers/proxies/{name}", "provider 触发更新语义未确认（P1 后续）")
	stub(mux, "PUT /providers/rules/{name}", "rule provider 触发更新语义未确认（P1 后续）")

	// --- P2：network / executor / general ---
	mux.HandleFunc("GET /general", s.getGeneral)
	mux.HandleFunc("PATCH /general", s.patchGeneral)
	mux.HandleFunc("GET /executor/config", s.getExecutorConfig)
	mux.HandleFunc("PATCH /executor/config", s.patchExecutorConfig)
	mux.HandleFunc("GET /network/interface", s.getNetworkInterface)
	mux.HandleFunc("PATCH /network/interface", s.patchNetworkInterface)
	mux.HandleFunc("GET /network/resolver", s.getNetworkResolver)
	mux.HandleFunc("PATCH /network/resolver", s.patchNetworkResolver)
	mux.HandleFunc("GET /network/fakeip", s.getNetworkFakeIP)
	mux.HandleFunc("PATCH /network/fakeip", s.patchNetworkFakeIP)
	mux.HandleFunc("GET /network/hosts", s.getNetworkHosts)
	mux.HandleFunc("PATCH /network/hosts", s.patchNetworkHosts)
	mux.HandleFunc("GET /network/outbound", s.getNetworkOutbound)
	mux.HandleFunc("PATCH /network/outbound", s.patchNetworkOutbound)

	// --- P3：实时数据 / WebSocket ---
	mux.HandleFunc("GET /connections/count", s.getConnectionsCount)
	mux.HandleFunc("GET /traffic", s.getTraffic)
	mux.HandleFunc("GET /bandwidth", s.getBandwidth)
	stub(mux, "GET /events", "事件 WebSocket 待 P3")

	// --- 其余（语义未确认，见 API_CONTRACT 的「推断」标记）---
	mux.HandleFunc("POST /profiles/{id}/config/update", s.postProfileConfigUpdate)
	// 注：不要在这里加 "GET /profiles/{id}/store" 之类的通配兜底——
	// ServeMux 里 {id} 会与 "GET /profiles/sessions/{id}" 冲突并 panic。
	// 缺端点只能按具体路径补。
	stub(mux, "POST /config/update", "全局配置更新语义未确认")
	stub(mux, "GET /store", "store 视图语义未确认")
	stub(mux, "POST /api/tokens", "AppToken 机制未还原")
	stub(mux, "POST /shizuku", "Shizuku 提权由 platform 层负责")
	stub(mux, "POST /shizuku/request", "Shizuku 提权由 platform 层负责")
}

type server struct {
	backend Backend
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("endpoint: write json failed", "err", err)
	}
}

// readJSON 解请求体。空体（Ktor 的 PUT/PATCH 可能不带 body）等价于零值，不算错。
func readJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// writeError 用统一的 {"error": "..."} 结构报错，方便宿主打印。
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// stub 注册一个「已登记但未实现」的端点。
func stub(mux *http.ServeMux, pattern, todo string) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("unimplemented endpoint", "method", r.Method, "path", r.URL.Path, "todo", todo)
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "not implemented yet",
			"path":  r.URL.Path,
			"todo":  todo,
		})
	})
}
