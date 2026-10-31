package endpoint

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kr328/clash-revived/internal/engine"

	"github.com/coder/websocket"

	_ "modernc.org/sqlite"
)

// --- 测试替身 -------------------------------------------------------------

type listenerToggle struct {
	Type    string
	Enabled bool
}

type fakeBackend struct {
	state     string
	lastErr   string
	profileID string
	listeners []engine.Listener
	proxies   []engine.Proxy
	selectErr error

	providers      []engine.Provider
	hcCalled       []string
	hcErr          error
	profiles       []engine.Profile
	profilesErr    error
	deleteErr      error
	refreshErr     error
	selectionErr   error
	deletedIDs     []string
	refreshedIDs   []string
	savedSelection [][2]string
	profilesConfig engine.ProfilesConfig
	profilesCfgErr error
	execCfg        engine.ExecutorConfig
	activeErr      error
	activeSet      []string
	mode           string
	modeErr        error
	network        engine.NetworkConfig
	trafficUp      int64
	trafficDown    int64
	totalUp        int64
	totalDown      int64
	connNow        int64
	connHistory    int64

	sessions      map[string]*engine.Session
	sessionOrder  []string
	newSessionErr error

	selected map[string]string
	toggles  []listenerToggle
}

func newFake() *fakeBackend {
	return &fakeBackend{
		state:     engine.StatusIdle,
		profileID: "00000000-0000-0000-0000-000000000000",
		selected:  map[string]string{},
		sessions:  map[string]*engine.Session{},
	}
}

func (f *fakeBackend) State() string     { return f.state }
func (f *fakeBackend) LastError() string { return f.lastErr }
func (f *fakeBackend) ProfileID() string { return f.profileID }

func (f *fakeBackend) Listeners() []engine.Listener { return f.listeners }

func (f *fakeBackend) SetListenerEnabled(typ string, enabled bool) error {
	if !engine.ValidListenerType(typ) {
		return fmt.Errorf("unknown listener %q", typ)
	}
	f.toggles = append(f.toggles, listenerToggle{Type: typ, Enabled: enabled})
	return nil
}

func (f *fakeBackend) Proxies() []engine.Proxy { return f.proxies }

func (f *fakeBackend) Proxy(name string) (engine.Proxy, bool) {
	for _, p := range f.proxies {
		if p.Name == name {
			return p, true
		}
	}
	return engine.Proxy{}, false
}

func (f *fakeBackend) Providers(typ string) []engine.Provider {
	if typ == "" {
		return f.providers
	}
	var out []engine.Provider
	for _, p := range f.providers {
		if p.Type == typ {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeBackend) Provider(typ, name string) (engine.Provider, bool) {
	for _, p := range f.Providers(typ) {
		if p.Name == name {
			return p, true
		}
	}
	return engine.Provider{}, false
}

func (f *fakeBackend) HealthCheckProvider(name string) error {
	if f.hcErr != nil {
		return f.hcErr
	}
	f.hcCalled = append(f.hcCalled, name)
	return nil
}

func (f *fakeBackend) DeleteProfile(id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedIDs = append(f.deletedIDs, id)
	return nil
}

func (f *fakeBackend) RefreshProfile(id string) error {
	if f.refreshErr != nil {
		return f.refreshErr
	}
	f.refreshedIDs = append(f.refreshedIDs, id)
	return nil
}

func (f *fakeBackend) SaveProxySelection(group, proxyName string) error {
	if f.selectionErr != nil {
		return f.selectionErr
	}
	f.savedSelection = append(f.savedSelection, [2]string{group, proxyName})
	return nil
}

func (f *fakeBackend) ExecutorConfig() engine.ExecutorConfig { return f.execCfg }

func (f *fakeBackend) Mode() string {
	if f.mode == "" {
		return engine.ModeRule
	}
	return f.mode
}

func (f *fakeBackend) SetMode(mode string) error {
	if f.modeErr != nil {
		return f.modeErr
	}
	if mode == engine.ModeScript {
		return errors.New("engine: script mode is not supported by mihomo")
	}
	f.mode = mode
	return nil
}

func (f *fakeBackend) Traffic() (int64, int64) { return f.trafficUp, f.trafficDown }
func (f *fakeBackend) ConnectionCount() (int64, int64) {
	return f.connNow, f.connHistory
}
func (f *fakeBackend) Total() (int64, int64) { return f.totalUp, f.totalDown }

func (f *fakeBackend) Network() engine.NetworkConfig { return f.network }

func (f *fakeBackend) PatchNetwork(fn func(*engine.NetworkConfig)) { fn(&f.network) }

func (f *fakeBackend) SetActiveProfile(id string) error {
	if f.activeErr != nil {
		return f.activeErr
	}
	f.activeSet = append(f.activeSet, id)
	f.execCfg.ActiveProfile = id
	return nil
}

func (f *fakeBackend) LoadProfilesConfig() (engine.ProfilesConfig, error) {
	if f.profilesCfgErr != nil {
		return engine.ProfilesConfig{}, f.profilesCfgErr
	}
	return f.profilesConfig, nil
}

func (f *fakeBackend) SaveProfilesConfig(cfg engine.ProfilesConfig) error {
	if f.profilesCfgErr != nil {
		return f.profilesCfgErr
	}
	f.profilesConfig = cfg
	return nil
}

func (f *fakeBackend) Profiles() ([]engine.Profile, error) {
	if f.profilesErr != nil {
		return nil, f.profilesErr
	}
	return f.profiles, nil
}

func (f *fakeBackend) NewSession(spec engine.SessionSpec) (string, error) {
	if f.newSessionErr != nil {
		return "", f.newSessionErr
	}
	id := "11111111-2222-4333-8444-555555555555"
	sess := &engine.Session{
		ID:   id,
		Spec: spec,
		Attributes: engine.SessionAttributes{
			Name:            spec.FileName,
			RemoteURL:       spec.URL,
			UpdatedAtUnixMS: 1702000000000,
		},
		Providers: []engine.ProviderDescriptor{
			{Name: "prov-a", Type: engine.ProviderTypeProxy, VehicleType: engine.VehicleHTTP, Length: 3},
		},
		Status: engine.SessionStoreStatus{Stage: engine.StoreStageVerifying},
	}
	f.sessions[id] = sess
	f.sessionOrder = append(f.sessionOrder, id)
	return id, nil
}

func (f *fakeBackend) Session(id string) (engine.Session, bool) {
	s, ok := f.sessions[id]
	if !ok {
		return engine.Session{}, false
	}
	return *s, true
}

func (f *fakeBackend) DeleteSession(id string) bool {
	if _, ok := f.sessions[id]; !ok {
		return false
	}
	delete(f.sessions, id)
	return true
}

func (f *fakeBackend) SetSessionConfig(id string, cfg engine.ProfilesConfig) bool {
	s, ok := f.sessions[id]
	if !ok {
		return false
	}
	s.Config = cfg
	return true
}

// StoreSession 在假实现里模拟"内容已就绪就立即完成"。
// 会话存在就置 Completed，让依赖 /store 返回 Completed 的用例保持行为。
func (f *fakeBackend) StoreSession(id string) (string, error) {
	s, ok := f.sessions[id]
	if !ok {
		return "", engine.ErrSessionNotFound
	}
	if len(s.Payload) == 0 && s.Spec.Type == engine.SessionTypeEdit {
		return "", engine.ErrSessionNotStored
	}
	s.Status.Stage = engine.StoreStageCompleted
	return "11111111-1111-4111-8111-111111111111", nil
}

func (f *fakeBackend) SetSessionAttributes(id string, attrs engine.SessionAttributes) bool {
	s, ok := f.sessions[id]
	if !ok {
		return false
	}
	s.Attributes = attrs
	return true
}

func (f *fakeBackend) SelectProxy(group, target string) error {
	if f.selectErr != nil {
		return f.selectErr
	}
	f.selected[group] = target
	return nil
}

// --- 工具 -----------------------------------------------------------------

func newTestServer(t *testing.T, b Backend) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	register(mux, b)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// keysOf 取 JSON 对象的键集合（排序后），用于「不多发、不漏发字段」的严格断言。
func keysOf(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not a JSON object: %v (body=%s)", err, raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func assertKeys(t *testing.T, raw []byte, want ...string) {
	t.Helper()
	got := keysOf(t, raw)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %v, want %v (body=%s)", got, want, raw)
	}
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, raw)
	}
	return v
}

// --- /status --------------------------------------------------------------

func TestGetStatusShape(t *testing.T) {
	mux := newTestServer(t, newFake())
	w := do(t, mux, "GET", "/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	raw := w.Body.Bytes()
	assertKeys(t, raw, "error", "profileId", "type")
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["error"] != nil {
		t.Fatalf("error = %v, want null", got["error"])
	}
	if got["type"] != "Idle" {
		t.Fatalf("type = %v", got["type"])
	}
	if got["profileId"] != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("profileId = %v", got["profileId"])
	}
}

func TestGetStatusErrorField(t *testing.T) {
	f := newFake()
	f.state = engine.StatusError
	f.lastErr = "boom"
	mux := newTestServer(t, f)
	got := decode[executorStatus](t, do(t, mux, "GET", "/status", "").Body.Bytes())
	if got.Type != "Error" {
		t.Fatalf("type = %q", got.Type)
	}
	if got.Error == nil || *got.Error != "boom" {
		t.Fatalf("error = %v", got.Error)
	}
}

// 枚举取值必须与 dex 里 Executor$Status$Type 的常量一致（首字母大写）。
func TestStatusEnumValues(t *testing.T) {
	for _, want := range []string{"Idle", "Loading", "Loaded", "Error"} {
		f := newFake()
		f.state = want
		mux := newTestServer(t, f)
		got := decode[executorStatus](t, do(t, mux, "GET", "/status", "").Body.Bytes())
		if got.Type != want {
			t.Fatalf("type = %q, want %q", got.Type, want)
		}
	}
}

// --- /listeners -----------------------------------------------------------

func fullListenerSnapshot(t *testing.T) []engine.Listener {
	t.Helper()
	return []engine.Listener{
		{Type: engine.ListenerDNS, Enabled: true, Status: map[string]any{"ipv6": true, "listenAt": "127.0.0.1:1053", "useHosts": true}},
		{Type: engine.ListenerExternalController, Enabled: true, Status: map[string]any{"listenAt": "127.0.0.1:9090", "secret": "s3cr3t"}},
		{Type: engine.ListenerHTTPSocks5, Enabled: true, Status: map[string]any{"listenAt": "127.0.0.1:7890", "users": []any{"a", "b"}}},
		{Type: engine.ListenerRedirect, Enabled: false, Status: map[string]any{"listenAt": ""}},
		{Type: engine.ListenerSystemProxy, Enabled: false, Status: map[string]any{"listenAt": "", "excludes": []any{}}},
		{Type: engine.ListenerTUN, Enabled: true, Status: map[string]any{"address": "172.19.0.1/30", "boosted": true, "dnsHijacked": true, "routeInserted": true}},
		{Type: engine.ListenerTransProxy, Enabled: true, Status: map[string]any{"listenAt": ":7893", "udp": true}},
		{Type: engine.ListenerVPN, Enabled: true, Status: map[string]any{"dnsHijacked": false, "hasChanges": false, "systemProxyListening": true}},
	}
}

func TestGetListenersShape(t *testing.T) {
	f := newFake()
	f.listeners = fullListenerSnapshot(t)
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/listeners", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	raw := w.Body.Bytes()
	// 顶层是包装体，只有一个键。
	assertKeys(t, raw, "listeners")

	all := decode[listenersAll](t, raw)
	if len(all.Listeners) != 8 {
		t.Fatalf("listeners count = %d, want 8", len(all.Listeners))
	}
	for _, typ := range engine.ListenerTypes() {
		if _, ok := all.Listeners[typ]; !ok {
			t.Fatalf("missing listener %q", typ)
		}
	}
	// 每个 Listener 只有 enabled / status。
	for typ, l := range all.Listeners {
		b, _ := json.Marshal(l)
		assertKeys(t, b, "enabled", "status")
		if typ == engine.ListenerHTTPSocks5 && !l.Enabled {
			t.Fatalf("HTTPSocks5 should be enabled")
		}
	}
}

// 每个 sealed 子类型的字段集合必须与模型一致（多一个键 Kotlin 就崩）。
func TestListenersStatusFieldSets(t *testing.T) {
	f := newFake()
	f.listeners = fullListenerSnapshot(t)
	mux := newTestServer(t, f)

	var wrapped struct {
		Listeners map[string]json.RawMessage `json:"listeners"`
	}
	if err := json.Unmarshal(do(t, mux, "GET", "/listeners", "").Body.Bytes(), &wrapped); err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		engine.ListenerDNS:                {"type", "ipv6", "listenAt", "useHosts"},
		engine.ListenerExternalController: {"type", "listenAt", "secret"},
		engine.ListenerHTTPSocks5:         {"type", "listenAt", "users"},
		engine.ListenerRedirect:           {"type", "listenAt"},
		engine.ListenerSystemProxy:        {"type", "excludes", "listenAt"},
		engine.ListenerTUN:                {"type", "address", "boosted", "dnsHijacked", "routeInserted"},
		engine.ListenerTransProxy:         {"type", "listenAt", "udp"},
		engine.ListenerVPN:                {"type", "dnsHijacked", "hasChanges", "systemProxyListening"},
	}
	for typ, wantKeys := range want {
		var entry struct {
			Status json.RawMessage `json:"status"`
		}
		if err := json.Unmarshal(wrapped.Listeners[typ], &entry); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		assertKeys(t, entry.Status, wantKeys...)
		// 判别符必须等于监听器名。
		var st struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(entry.Status, &st); err != nil {
			t.Fatal(err)
		}
		if st.Type != typ {
			t.Fatalf("%s: status.type = %q", typ, st.Type)
		}
	}
}

// 空集合必须是 []，不能是 null（kotlinx 的 List 不接受 null）。
func TestListenersEmptyCollections(t *testing.T) {
	f := newFake()
	f.listeners = []engine.Listener{
		{Type: engine.ListenerHTTPSocks5, Enabled: false, Status: map[string]any{"listenAt": ""}},
		{Type: engine.ListenerSystemProxy, Enabled: false, Status: map[string]any{"listenAt": ""}},
	}
	mux := newTestServer(t, f)
	body := do(t, mux, "GET", "/listeners", "").Body.String()
	if strings.Contains(body, `"users":null`) || strings.Contains(body, `"excludes":null`) {
		t.Fatalf("nil slice leaked into JSON: %s", body)
	}
	if !strings.Contains(body, `"users":[]`) || !strings.Contains(body, `"excludes":[]`) {
		t.Fatalf("empty slices must serialize as []: %s", body)
	}
}

func TestPutListenerEnabled(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)

	w := do(t, mux, "PUT", "/listeners/TUN/enabled", `{"enabled":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if len(f.toggles) != 1 || f.toggles[0].Type != "TUN" || !f.toggles[0].Enabled {
		t.Fatalf("toggles = %+v", f.toggles)
	}
	// 空 body 等价于 enabled=false，不应报错。
	if w := do(t, mux, "PUT", "/listeners/VPN/enabled", ""); w.Code != http.StatusOK {
		t.Fatalf("empty body: code = %d", w.Code)
	}
	// 未知监听器名 -> 400
	if w := do(t, mux, "PUT", "/listeners/Nope/enabled", `{"enabled":true}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown listener: code = %d", w.Code)
	}
	// 坏 JSON -> 400
	if w := do(t, mux, "PUT", "/listeners/TUN/enabled", `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: code = %d", w.Code)
	}
}

func TestPatchListenerConfig(t *testing.T) {
	mux := newTestServer(t, newFake())
	body := `{"type":"TUN","address":"172.19.0.1/30","autoRoute":true,"boost":true,"dnsHijack":true}`
	if w := do(t, mux, "PATCH", "/listeners/TUN/config", body); w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if w := do(t, mux, "PATCH", "/listeners/Nope/config", body); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown listener: code = %d", w.Code)
	}
}

// --- /proxies -------------------------------------------------------------

func sampleProxy() engine.Proxy {
	return engine.Proxy{
		Name:      "GLOBAL",
		Type:      engine.ProxyTypeSelector,
		UDP:       true,
		Now:       "node-a",
		Providers: []string{"prov"},
		Proxies:   []string{"node-a", "node-b"},
		History:   []engine.Delay{{DelayMS: 120, MeanDelayMS: 130, TimeUnixMS: 1702000000000}},
	}
}

func TestGetProxiesShape(t *testing.T) {
	f := newFake()
	f.proxies = []engine.Proxy{sampleProxy(), {Name: "DIRECT", Type: engine.ProxyTypeDirect, UDP: true}}
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/proxies", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("proxies = %d, want 2", len(all))
	}
	assertKeys(t, all["GLOBAL"], "history", "name", "now", "providers", "proxies", "type", "udp")
	assertKeys(t, all["DIRECT"], "history", "name", "now", "providers", "proxies", "type", "udp")

	var got proxiesProxy
	if err := json.Unmarshal(all["GLOBAL"], &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "Selector" || got.Now != "node-a" || !got.UDP {
		t.Fatalf("proxy = %+v", got)
	}
	if len(got.History) != 1 || got.History[0].Delay != 120 || got.History[0].MeanDelay != 130 {
		t.Fatalf("history = %+v", got.History)
	}
	if got.History[0].Time != "2023-12-08T01:46:40.000Z" {
		t.Fatalf("history time = %q", got.History[0].Time)
	}
	// 单节点的 history 必须是 [] 不是 null
	if !strings.Contains(string(all["DIRECT"]), `"history":[]`) {
		t.Fatalf("DIRECT history must be []: %s", all["DIRECT"])
	}
}

func TestGetProxyByName(t *testing.T) {
	f := newFake()
	f.proxies = []engine.Proxy{sampleProxy()}
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/proxies/GLOBAL", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	assertKeys(t, w.Body.Bytes(), "history", "name", "now", "providers", "proxies", "type", "udp")

	if w := do(t, mux, "GET", "/proxies/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing proxy: code = %d", w.Code)
	}
}

func TestPutProxySelects(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)

	if w := do(t, mux, "PUT", "/proxies/GLOBAL", `{"name":"node-b"}`); w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if f.selected["GLOBAL"] != "node-b" {
		t.Fatalf("selected = %+v", f.selected)
	}
	if w := do(t, mux, "PUT", "/proxies/GLOBAL", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("missing name: code = %d", w.Code)
	}
	f.selectErr = errors.New("not a selector")
	if w := do(t, mux, "PUT", "/proxies/DIRECT", `{"name":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("engine error: code = %d", w.Code)
	}
}

// --- /config /attributes --------------------------------------------------

// TODO(P0 未决): 这两个端点的响应体类型尚无二进制证据，先钉住「返回空对象」这个
// 保守约定，等联调抓包后替换成真实模型。
func TestConfigAndAttributesAreEmptyObjects(t *testing.T) {
	mux := newTestServer(t, newFake())
	for _, path := range []string{"/config", "/attributes"} {
		w := do(t, mux, "GET", path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", path, w.Code)
		}
		assertKeys(t, w.Body.Bytes())
	}
}

// --- /healthcheck /stage --------------------------------------------------

func TestHealthcheck(t *testing.T) {
	mux := newTestServer(t, newFake())
	got := decode[map[string]any](t, do(t, mux, "GET", "/healthcheck", "").Body.Bytes())
	if got["ok"] != true {
		t.Fatalf("healthcheck = %v", got)
	}
}

func TestStageMapping(t *testing.T) {
	cases := []struct{ state, want string }{
		{engine.StatusIdle, "Dead"},
		{engine.StatusLoading, "Initializing"},
		{engine.StatusLoaded, "Running"},
		{engine.StatusError, "Failed"},
	}
	for _, c := range cases {
		f := newFake()
		f.state = c.state
		f.lastErr = "x"
		mux := newTestServer(t, f)
		got := decode[map[string]any](t, do(t, mux, "GET", "/stage", "").Body.Bytes())
		if got["type"] != c.want {
			t.Fatalf("state %q -> %v, want %q", c.state, got["type"], c.want)
		}
	}
}

// --- 路由表本身 -----------------------------------------------------------

// 桩端点必须返回 501 而不是 404：宿主拿到 404 会当成路由写错。
func TestUnimplementedEndpointsReturn501(t *testing.T) {
	mux := newTestServer(t, newFake())
	for _, path := range []string{
		"/events", "/store",
	} {
		w := do(t, mux, "GET", path, "")
		if w.Code != http.StatusNotImplemented {
			t.Fatalf("%s: code = %d, want 501", path, w.Code)
		}
	}
}

// 用真实（未启动）engine 走一遍 Register，确保路由表无重复模式 panic，
// 且未启动状态下各端点都能给出完整形状。
func TestRegisterWithRealEngine(t *testing.T) {
	e := engine.New()
	mux := http.NewServeMux()
	Register(mux, e) // 重复模式会在这里 panic

	w := do(t, mux, "GET", "/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d", w.Code)
	}
	got := decode[executorStatus](t, w.Body.Bytes())
	if got.Type != engine.StatusIdle {
		t.Fatalf("type = %q", got.Type)
	}

	w = do(t, mux, "GET", "/listeners", "")
	if w.Code != http.StatusOK {
		t.Fatalf("listeners code = %d", w.Code)
	}
	all := decode[listenersAll](t, w.Body.Bytes())
	if len(all.Listeners) != len(engine.ListenerTypes()) {
		t.Fatalf("listeners = %d", len(all.Listeners))
	}

	// 未启动时 /proxies 是空对象，不是 null。
	if body := do(t, mux, "GET", "/proxies", "").Body.String(); strings.TrimSpace(body) != "{}" {
		t.Fatalf("proxies body = %q, want {}", body)
	}
}

const sessionLiveConfig = `mode: rule
log-level: silent
proxy-providers:
  inline-prov:
    type: inline
    payload:
      - {name: n1, type: ss, server: 127.0.0.1, port: 1, cipher: aes-128-gcm, password: x}
rule-providers:
  rules-a:
    type: inline
    behavior: domain
    format: yaml
    payload:
      - "+.example.com"
`

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 真实引擎（真 mihomo）跑一遍 P0，验证端点层的接线与线上形状在真实状态下也成立。
// 用 -v 运行可以看到各端点实际吐出的 JSON。
func TestP0WithLiveEngine(t *testing.T) {
	// 用 inline provider：既有真实 provider 可查，又不碰磁盘/网络。
	cfg := []byte(`mode: rule
log-level: silent
proxy-providers:
  inline-prov:
    type: inline
    payload:
      - {name: n1, type: ss, server: 127.0.0.1, port: 1, cipher: aes-128-gcm, password: x}
rule-providers:
  rules-a:
    type: inline
    behavior: domain
    format: yaml
    payload:
      - "+.example.com"
`)
	e := engine.New()
	if err := e.Start(cfg, -1); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	defer func() { _ = e.Stop() }()

	mux := http.NewServeMux()
	Register(mux, e)

	for _, path := range []string{
		"/status", "/listeners", "/proxies", "/config", "/attributes",
		"/providers/proxies", "/providers/rules",
		"/providers/proxies/inline-prov", "/providers/rules/rules-a",
		"/providers/proxies/inline-prov/healthcheck",
	} {
		w := do(t, mux, "GET", path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", path, w.Code)
		}
		t.Logf("%s -> %s", path, strings.TrimSpace(w.Body.String()))
	}

	// 会话全流程：POST -> 轮询 StoreStatus -> attributes / providers。
	post := do(t, mux, "POST", "/profiles/sessions", `{"type":"NewFile","fileName":"live.yaml","fileContent":`+
		jsonString(t, sessionLiveConfig)+`}`)
	if post.Code != http.StatusOK {
		t.Fatalf("POST session: code = %d body=%s", post.Code, post.Body.String())
	}
	newID := decode[sessionNewResult](t, post.Body.Bytes()).SessionID
	t.Logf("POST /profiles/sessions -> %s", strings.TrimSpace(post.Body.String()))

	var status sessionStoreStatus
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body := do(t, mux, "GET", "/profiles/sessions/"+newID, "").Body.Bytes()
		status = decode[sessionStoreStatus](t, body)
		if status.Stage == "Completed" || status.Stage == "Failed" {
			t.Logf("GET /profiles/sessions/{id} -> %s", strings.TrimSpace(string(body)))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.Stage != "Completed" {
		t.Fatalf("session stage = %q err=%q", status.Stage, status.Error)
	}
	if status.ProviderProgress.Total != 2 || status.ProviderProgress.Completed != 2 {
		t.Fatalf("progress = %+v", status.ProviderProgress)
	}
	for _, path := range []string{
		"/profiles/sessions/" + newID + "/attributes",
		"/profiles/sessions/" + newID + "/providers",
		"/profiles/sessions/" + newID + "/config",
	} {
		w := do(t, mux, "GET", path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", path, w.Code)
		}
		t.Logf("%s -> %s", path, strings.TrimSpace(w.Body.String()))
	}

	st := decode[executorStatus](t, do(t, mux, "GET", "/status", "").Body.Bytes())
	if st.Type != engine.StatusLoaded {
		t.Fatalf("status.type = %q, want Loaded", st.Type)
	}
	if st.Error != nil {
		t.Fatalf("status.error = %v, want null", *st.Error)
	}
}

// --- /providers/* ---------------------------------------------------------

func providerFixtures() []engine.Provider {
	return []engine.Provider{
		{
			Name:          "机场A",
			Type:          engine.ProviderTypeProxy,
			VehicleType:   engine.VehicleHTTP,
			Proxies:       []string{"n1", "n2"},
			UpdatedUnixMS: 1702000000000,
		},
		{
			Name:          "local-inline",
			Type:          engine.ProviderTypeProxy,
			VehicleType:   engine.VehicleFile,
			Proxies:       []string{},
			UpdatedUnixMS: 1702000000000,
		},
		{
			Name:          "rules-a",
			Type:          engine.ProviderTypeRule,
			VehicleType:   engine.VehicleFile,
			Proxies:       []string{},
			RuleCount:     42,
			UpdatedUnixMS: 1702000000000,
		},
	}
}

func TestGetProxyProvidersShape(t *testing.T) {
	f := newFake()
	f.providers = providerFixtures()
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/providers/proxies", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	// 顶层包装体只有 providers 一个键。
	assertKeys(t, w.Body.Bytes(), "providers")

	all := decode[providersAll](t, w.Body.Bytes())
	if len(all.Providers) != 2 {
		t.Fatalf("providers = %d, want 2 (rule provider must not leak in)", len(all.Providers))
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	var inner struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &inner); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, inner.Providers["机场A"], "name", "proxies", "type", "updatedAt", "vehicleType")

	got := all.Providers["机场A"]
	if got.Type != "Proxy" || got.VehicleType != "HTTP" {
		t.Fatalf("provider = %+v", got)
	}
	// proxies 的元素必须是对象（不是字符串）——2026-10-31 真机崩溃实证。
	if len(got.Proxies) != 2 || got.Proxies[0].Name != "n1" {
		t.Fatalf("proxies = %v", got.Proxies)
	}
	if got.UpdatedAt != "2023-12-08T01:46:40.000Z" {
		t.Fatalf("updatedAt = %q", got.UpdatedAt)
	}
	// 空 proxies 必须是 []，不能是 null。
	if !strings.Contains(string(inner.Providers["local-inline"]), `"proxies":[]`) {
		t.Fatalf("empty proxies leaked: %s", inner.Providers["local-inline"])
	}
}

func TestGetRuleProvidersShape(t *testing.T) {
	f := newFake()
	f.providers = providerFixtures()
	mux := newTestServer(t, f)

	all := decode[providersAll](t, do(t, mux, "GET", "/providers/rules", "").Body.Bytes())
	if len(all.Providers) != 1 {
		t.Fatalf("rule providers = %d, want 1", len(all.Providers))
	}
	got := all.Providers["rules-a"]
	if got.Type != "Rule" || got.VehicleType != "File" {
		t.Fatalf("rule provider = %+v", got)
	}
	if got.Proxies == nil || len(got.Proxies) != 0 {
		t.Fatalf("rule provider proxies = %v, want []", got.Proxies)
	}
}

func TestGetProviderByName(t *testing.T) {
	f := newFake()
	f.providers = providerFixtures()
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/providers/proxies/机场A", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	assertKeys(t, w.Body.Bytes(), "name", "proxies", "type", "updatedAt", "vehicleType")

	if w := do(t, mux, "GET", "/providers/proxies/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing provider: code = %d", w.Code)
	}
	if w := do(t, mux, "GET", "/providers/rules/rules-a", ""); w.Code != http.StatusOK {
		t.Fatalf("rule provider by name: code = %d", w.Code)
	}
	if w := do(t, mux, "GET", "/providers/rules/机场A", ""); w.Code != http.StatusNotFound {
		t.Fatalf("cross-type lookup must 404: code = %d", w.Code)
	}
}

func TestProviderHealthcheck(t *testing.T) {
	f := newFake()
	f.providers = providerFixtures()
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/providers/proxies/机场A/healthcheck", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	assertKeys(t, w.Body.Bytes()) // Kotlin 侧返回类型是 Unit -> {}
	if len(f.hcCalled) != 1 || f.hcCalled[0] != "机场A" {
		t.Fatalf("hcCalled = %v", f.hcCalled)
	}
	f.hcErr = errors.New("engine: not running")
	if w := do(t, mux, "GET", "/providers/proxies/机场A/healthcheck", ""); w.Code != http.StatusNotFound {
		t.Fatalf("hc error: code = %d", w.Code)
	}
}

// mihomo 的 4 个内置出站不能出现在 /proxies 里（原内核没有它们）。
func TestProxiesFilterBuiltinOutbounds(t *testing.T) {
	f := newFake()
	f.proxies = []engine.Proxy{
		{Name: "GLOBAL", Type: engine.ProxyTypeSelector},
		{Name: "DIRECT", Type: engine.ProxyTypeDirect},
		{Name: "REJECT", Type: engine.ProxyTypeDirect},
		{Name: "COMPATIBLE", Type: engine.ProxyTypeDirect},
		{Name: "PASS", Type: engine.ProxyTypeDirect},
		{Name: "PASS-RULE", Type: engine.ProxyTypeDirect},
		{Name: "REJECT-DROP", Type: engine.ProxyTypeDirect},
	}
	mux := newTestServer(t, f)

	var all map[string]json.RawMessage
	if err := json.Unmarshal(do(t, mux, "GET", "/proxies", "").Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"COMPATIBLE", "PASS", "PASS-RULE", "REJECT-DROP"} {
		if _, ok := all[hidden]; ok {
			t.Fatalf("%s must be filtered out of /proxies", hidden)
		}
		if w := do(t, mux, "GET", "/proxies/"+hidden, ""); w.Code != http.StatusNotFound {
			t.Fatalf("GET /proxies/%s: code = %d, want 404", hidden, w.Code)
		}
	}
	for _, want := range []string{"GLOBAL", "DIRECT", "REJECT"} {
		if _, ok := all[want]; !ok {
			t.Fatalf("%s must stay in /proxies", want)
		}
	}
}

// --- /profiles/sessions/* -------------------------------------------------

const sessionFakeID = "11111111-2222-4333-8444-555555555555"

func TestPostSession(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)

	body := `{"type":"NewFile","fileName":"我的订阅.yaml","fileContent":"mode: rule\n"}`
	w := do(t, mux, "POST", "/profiles/sessions", body)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	assertKeys(t, w.Body.Bytes(), "sessionId")
	got := decode[sessionNewResult](t, w.Body.Bytes())
	if got.SessionID != sessionFakeID {
		t.Fatalf("sessionId = %q", got.SessionID)
	}
	// 请求体字段要原样传给 engine。
	if sess := f.sessions[sessionFakeID]; sess.Spec.FileName != "我的订阅.yaml" || sess.Spec.Type != "NewFile" {
		t.Fatalf("spec = %+v", sess.Spec)
	}

	// engine 拒绝（缺 url / 未知类型）-> 400
	f.newSessionErr = errors.New(`engine: NewURL session requires url`)
	if w := do(t, mux, "POST", "/profiles/sessions", `{"type":"NewURL"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid spec: code = %d", w.Code)
	}
	// 坏 JSON -> 400
	if w := do(t, mux, "POST", "/profiles/sessions", `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: code = %d", w.Code)
	}
}

func TestGetSessionStatus(t *testing.T) {
	f := newFake()
	if _, err := f.NewSession(engine.SessionSpec{Type: engine.SessionTypeNewFile, FileContent: "x"}); err != nil {
		t.Fatal(err)
	}
	f.sessions[sessionFakeID].Status = engine.SessionStoreStatus{
		Stage: engine.StoreStageCompletingProviders, Completed: 1, Total: 3,
	}
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/profiles/sessions/"+sessionFakeID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	assertKeys(t, w.Body.Bytes(), "error", "providerProgress", "stage")
	got := decode[sessionStoreStatus](t, w.Body.Bytes())
	if got.Stage != "CompletingProviders" || got.ProviderProgress.Completed != 1 || got.ProviderProgress.Total != 3 {
		t.Fatalf("status = %+v", got)
	}
	var progress map[string]json.RawMessage
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &outer); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(outer["providerProgress"], &progress); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, outer["providerProgress"], "completed", "total")

	if w := do(t, mux, "GET", "/profiles/sessions/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing session: code = %d", w.Code)
	}
}

func TestSessionAttributes(t *testing.T) {
	f := newFake()
	if _, err := f.NewSession(engine.SessionSpec{Type: engine.SessionTypeNewURL, URL: "https://example.com/sub.yaml"}); err != nil {
		t.Fatal(err)
	}
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/profiles/sessions/"+sessionFakeID+"/attributes", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	assertKeys(t, w.Body.Bytes(), "name", "remoteURL", "updateExpr", "updatedAt")
	got := decode[sessionAttributes](t, w.Body.Bytes())
	if got.RemoteURL != "https://example.com/sub.yaml" {
		t.Fatalf("remoteURL = %q", got.RemoteURL)
	}
	if got.UpdatedAt != "2023-12-08T01:46:40.000Z" {
		t.Fatalf("updatedAt = %q", got.UpdatedAt)
	}

	// PUT 只认这三个可写字段；updatedAt 是服务端字段，客户端传了也要被忽略。
	body := `{"name":"改名","remoteURL":"https://x","updateExpr":"0 3 * * *","updatedAt":"1999-01-01T00:00:00Z"}`
	if w := do(t, mux, "PUT", "/profiles/sessions/"+sessionFakeID+"/attributes", body); w.Code != http.StatusOK {
		t.Fatalf("put attributes: code = %d", w.Code)
	}
	sess := f.sessions[sessionFakeID]
	if sess.Attributes.Name != "改名" || sess.Attributes.UpdateExpr != "0 3 * * *" {
		t.Fatalf("attributes = %+v", sess.Attributes)
	}
	if sess.Attributes.UpdatedAtUnixMS != 1702000000000 {
		t.Fatalf("updatedAt must be preserved by server, got %d", sess.Attributes.UpdatedAtUnixMS)
	}
	if w := do(t, mux, "PUT", "/profiles/sessions/nope/attributes", body); w.Code != http.StatusNotFound {
		t.Fatalf("put missing: code = %d", w.Code)
	}
}

func TestSessionProviders(t *testing.T) {
	f := newFake()
	if _, err := f.NewSession(engine.SessionSpec{Type: engine.SessionTypeNewFile, FileContent: "x"}); err != nil {
		t.Fatal(err)
	}
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/profiles/sessions/"+sessionFakeID+"/providers", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	assertKeys(t, w.Body.Bytes(), "providers")
	all := decode[sessionProviderDescriptorAll](t, w.Body.Bytes())
	if len(all.Providers) != 1 {
		t.Fatalf("providers = %+v", all.Providers)
	}
	if all.Providers[0].Name != "prov-a" || all.Providers[0].Length != 3 || all.Providers[0].Type != "Proxy" {
		t.Fatalf("descriptor = %+v", all.Providers[0])
	}

	// 空列表必须是 []（kotlinx 的 List 不接受 null）。
	f.sessions[sessionFakeID].Providers = nil
	body := do(t, mux, "GET", "/profiles/sessions/"+sessionFakeID+"/providers", "").Body.String()
	if !strings.Contains(body, `"providers":[]`) {
		t.Fatalf("empty providers must be []: %s", body)
	}
}

func TestSessionConfig(t *testing.T) {
	f := newFake()
	if _, err := f.NewSession(engine.SessionSpec{Type: engine.SessionTypeNewFile, FileContent: "x"}); err != nil {
		t.Fatal(err)
	}
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/profiles/sessions/"+sessionFakeID+"/config", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	assertKeys(t, w.Body.Bytes(), "updater")
	got := decode[profilesConfig](t, w.Body.Bytes())
	if got.Updater.MaxRetryCount != 0 || got.Updater.Notify.OnSuccess {
		t.Fatalf("default config = %+v", got)
	}

	body := `{"updater":{"maxRetryCount":7,"notify":{"onFailure":true,"onSuccess":true},"withTunnel":true}}`
	if w := do(t, mux, "PUT", "/profiles/sessions/"+sessionFakeID+"/config", body); w.Code != http.StatusOK {
		t.Fatalf("put config: code = %d", w.Code)
	}
	if cfg := f.sessions[sessionFakeID].Config; cfg.MaxRetryCount != 7 || !cfg.WithTunnel || !cfg.NotifyOnFailure {
		t.Fatalf("stored config = %+v", cfg)
	}
	if w := do(t, mux, "PUT", "/profiles/sessions/nope/config", body); w.Code != http.StatusNotFound {
		t.Fatalf("put missing config: code = %d", w.Code)
	}
}

func TestDeleteSession(t *testing.T) {
	f := newFake()
	if _, err := f.NewSession(engine.SessionSpec{Type: engine.SessionTypeNewFile, FileContent: "x"}); err != nil {
		t.Fatal(err)
	}
	mux := newTestServer(t, f)

	if w := do(t, mux, "DELETE", "/profiles/sessions/"+sessionFakeID, ""); w.Code != http.StatusOK {
		t.Fatalf("delete: code = %d", w.Code)
	}
	if w := do(t, mux, "DELETE", "/profiles/sessions/"+sessionFakeID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("double delete: code = %d", w.Code)
	}
}

// --- /profiles ------------------------------------------------------------

func TestGetProfilesShape(t *testing.T) {
	f := newFake()
	f.profiles = []engine.Profile{
		{
			ID:   "11111111-1111-4111-8111-111111111111",
			Name: "订阅A", RemoteURL: "https://example.com/a", UpdateExpr: "0 3 * * *",
			UpdatedAtUnixMS: 1702000000000,
			Upload:          1024, Download: 2048, Total: 4096,
			ExpireAtUnixMS: 1733622400000,
		},
		{
			ID:   "22222222-2222-4222-8222-222222222222",
			Name: "订阅B",
			// 未知的时间必须是零值，不能是 epoch
			UpdatedAtUnixMS: 0, ExpireAtUnixMS: 0,
		},
	}
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/profiles", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	// 包装体，唯一键 profiles。
	assertKeys(t, w.Body.Bytes(), "profiles")

	var raw struct {
		Profiles []json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Profiles) != 2 {
		t.Fatalf("profiles = %d", len(raw.Profiles))
	}
	assertKeys(t, raw.Profiles[0], "id", "attributes", "userInfo")

	var entry struct {
		ID         string          `json:"id"`
		Attributes json.RawMessage `json:"attributes"`
		UserInfo   json.RawMessage `json:"userInfo"`
	}
	if err := json.Unmarshal(raw.Profiles[0], &entry); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, entry.Attributes, "name", "remoteURL", "updateExpr", "updatedAt")
	assertKeys(t, entry.UserInfo, "upload", "download", "total", "expireAt")

	got := decode[profilesProfile](t, raw.Profiles[0])
	if got.Attributes.UpdatedAt != "2023-12-08T01:46:40.000Z" {
		t.Fatalf("updatedAt = %q", got.Attributes.UpdatedAt)
	}
	// uploaded/downloaded -> upload/download：取错列名就会是 0。
	if got.UserInfo.Upload != 1024 || got.UserInfo.Download != 2048 || got.UserInfo.Total != 4096 {
		t.Fatalf("userInfo = %+v", got.UserInfo)
	}
	// 未知时间 -> 零值 Instant（不是 1970）
	second := decode[profilesProfile](t, raw.Profiles[1])
	if second.Attributes.UpdatedAt != "0001-01-01T00:00:00.000Z" || second.UserInfo.ExpireAt != "0001-01-01T00:00:00.000Z" {
		t.Fatalf("unknown times = %q / %q", second.Attributes.UpdatedAt, second.UserInfo.ExpireAt)
	}
}

// 空库必须是 "profiles":[]，不能是 null。
func TestGetProfilesEmpty(t *testing.T) {
	mux := newTestServer(t, newFake())
	body := do(t, mux, "GET", "/profiles", "").Body.String()
	if !strings.Contains(body, `"profiles":[]`) {
		t.Fatalf("empty profiles must be []: %s", body)
	}
}

// store 路径没配 -> 503（不是空列表，避免伪装成"没有配置"）。
func TestGetProfilesStoreNotConfigured(t *testing.T) {
	f := newFake()
	f.profilesErr = engine.ErrStorePathNotConfigured
	mux := newTestServer(t, f)
	w := do(t, mux, "GET", "/profiles", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "store database path") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

// 其它读库错误 -> 500。
func TestGetProfilesStoreError(t *testing.T) {
	f := newFake()
	f.profilesErr = errors.New("query profiles: boom")
	mux := newTestServer(t, f)
	if w := do(t, mux, "GET", "/profiles", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
}

// /profiles 走真实 SQLite：造一个合成库（无真实订阅信息），验证端到端。
func TestProfilesAgainstRealSQLite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE profiles (id UUID PRIMARY KEY, version INTEGER, name TEXT, remoteURL TEXT,
  updateExpr TEXT, updatedAt TIMESTAMP, content BLOB, modeOverride VARCHAR(32));
CREATE TABLE user_infos (profileID UUID PRIMARY KEY, uploaded INTEGER, downloaded INTEGER,
  total INTEGER, expireAt TIMESTAMP);
INSERT INTO profiles VALUES ('11111111-1111-4111-8111-111111111111', 3, '订阅A',
  'https://example.com/a', '0 3 * * *', '2023-12-08 01:46:40+00:00', X'00', 'rule');
INSERT INTO user_infos VALUES ('11111111-1111-4111-8111-111111111111', 1024, 2048, 4096,
  '2024-12-08 01:46:40+00:00');`); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	_ = db.Close()

	e := engine.New()
	e.SetDatabasePath(path)
	mux := http.NewServeMux()
	Register(mux, e)

	w := do(t, mux, "GET", "/profiles", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	t.Logf("/profiles -> %s", strings.TrimSpace(w.Body.String()))

	all := decode[profilesProfileAll](t, w.Body.Bytes())
	if len(all.Profiles) != 1 {
		t.Fatalf("profiles = %+v", all.Profiles)
	}
	got := all.Profiles[0]
	if got.ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("id = %q", got.ID)
	}
	if got.Attributes.Name != "订阅A" || got.Attributes.UpdateExpr != "0 3 * * *" {
		t.Fatalf("attributes = %+v", got.Attributes)
	}
	if got.Attributes.UpdatedAt != "2023-12-08T01:46:40.000Z" {
		t.Fatalf("updatedAt = %q", got.Attributes.UpdatedAt)
	}
	if got.UserInfo.Upload != 1024 || got.UserInfo.Download != 2048 || got.UserInfo.Total != 4096 {
		t.Fatalf("userInfo = %+v", got.UserInfo)
	}
	if got.UserInfo.ExpireAt != "2024-12-08T01:46:40.000Z" {
		t.Fatalf("expireAt = %q", got.UserInfo.ExpireAt)
	}

	// 没配 store 路径的引擎必须 503，而不是空列表。
	e2 := engine.New()
	mux2 := http.NewServeMux()
	Register(mux2, e2)
	if w := do(t, mux2, "GET", "/profiles", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store path: code = %d, want 503", w.Code)
	}
}

// --- PUT / DELETE /profiles/{id} ------------------------------------------

func TestDeleteProfile(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)
	id := "11111111-1111-4111-8111-111111111111"

	if w := do(t, mux, "DELETE", "/profiles/"+id, ""); w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if len(f.deletedIDs) != 1 || f.deletedIDs[0] != id {
		t.Fatalf("deleted = %v", f.deletedIDs)
	}

	f.deleteErr = engine.ErrProfileNotFound
	if w := do(t, mux, "DELETE", "/profiles/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing: code = %d", w.Code)
	}
	f.deleteErr = engine.ErrStorePathNotConfigured
	if w := do(t, mux, "DELETE", "/profiles/"+id, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store: code = %d", w.Code)
	}
	f.deleteErr = errors.New("delete profile: disk I/O error")
	if w := do(t, mux, "DELETE", "/profiles/"+id, ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("io error: code = %d", w.Code)
	}
}

// PUT 是"触发重新下载"，无请求体。
func TestPutProfile(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)
	id := "11111111-1111-4111-8111-111111111111"

	if w := do(t, mux, "PUT", "/profiles/"+id, ""); w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if len(f.refreshedIDs) != 1 || f.refreshedIDs[0] != id {
		t.Fatalf("refreshed = %v", f.refreshedIDs)
	}
	assertKeys(t, do(t, mux, "PUT", "/profiles/"+id, "").Body.Bytes())

	f.refreshErr = engine.ErrProfileNoRemoteURL
	if w := do(t, mux, "PUT", "/profiles/"+id, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("no remoteURL: code = %d", w.Code)
	}
	f.refreshErr = engine.ErrProfileNotFound
	if w := do(t, mux, "PUT", "/profiles/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing: code = %d", w.Code)
	}
}

// PUT /proxies/{name} 成功后要把选择落库；落库失败不影响切换结果。
func TestPutProxyPersistsSelection(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)

	if w := do(t, mux, "PUT", "/proxies/GLOBAL", `{"name":"node-b"}`); w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if len(f.savedSelection) != 1 || f.savedSelection[0] != [2]string{"GLOBAL", "node-b"} {
		t.Fatalf("saved = %v", f.savedSelection)
	}

	// 落库失败：切换本身仍然成功（内存里已生效），只是日志里留痕。
	f.savedSelection = nil
	f.selectionErr = errors.New("persist failed")
	if w := do(t, mux, "PUT", "/proxies/GLOBAL", `{"name":"node-c"}`); w.Code != http.StatusOK {
		t.Fatalf("selection failure must not fail the request: code = %d", w.Code)
	}
	if f.selected["GLOBAL"] != "node-c" {
		t.Fatalf("selected = %v", f.selected)
	}
}

// --- /profiles/config -----------------------------------------------------

// 读不到配置时必须返回完整默认对象（不是 {}，不是 500）。
func TestGetProfilesConfigDefaults(t *testing.T) {
	mux := newTestServer(t, newFake())
	w := do(t, mux, "GET", "/profiles/config", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	assertKeys(t, w.Body.Bytes(), "updater")
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &outer); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, outer["updater"], "maxRetryCount", "notify", "withTunnel")
	got := decode[profilesConfig](t, w.Body.Bytes())
	if got.Updater.MaxRetryCount != 0 || got.Updater.Notify.OnSuccess || got.Updater.WithTunnel {
		t.Fatalf("defaults = %+v", got)
	}
	// 后端报错也要给默认值（设置页不能打不开）。
	f := newFake()
	f.profilesCfgErr = errors.New("boom")
	if w := do(t, newTestServer(t, f), "GET", "/profiles/config", ""); w.Code != http.StatusOK {
		t.Fatalf("read error must still return defaults: code = %d", w.Code)
	}
}

func TestPutProfilesConfig(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)

	body := `{"updater":{"maxRetryCount":5,"notify":{"onFailure":true,"onSuccess":true},"withTunnel":true}}`
	if w := do(t, mux, "PUT", "/profiles/config", body); w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if f.profilesConfig.MaxRetryCount != 5 || !f.profilesConfig.NotifyOnSuccess || !f.profilesConfig.WithTunnel {
		t.Fatalf("saved = %+v", f.profilesConfig)
	}
	// 写失败要可见（用户必须知道设置没存上）。
	f.profilesCfgErr = engine.ErrStorePathNotConfigured
	if w := do(t, mux, "PUT", "/profiles/config", body); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store: code = %d", w.Code)
	}
	f.profilesCfgErr = errors.New("disk I/O error")
	if w := do(t, mux, "PUT", "/profiles/config", body); w.Code != http.StatusInternalServerError {
		t.Fatalf("io error: code = %d", w.Code)
	}
	if w := do(t, mux, "PUT", "/profiles/config", `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: code = %d", w.Code)
	}
}

// --- /executor/config -----------------------------------------------------

const activeProfileID = "33333333-3333-4333-8333-333333333333"

func TestGetExecutorConfig(t *testing.T) {
	f := newFake()
	f.execCfg = engine.ExecutorConfig{
		ActiveProfile:            activeProfileID,
		ExternalController:       "127.0.0.1:9090",
		ExternalControllerSecret: "s3cr3t",
	}
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/executor/config", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	assertKeys(t, w.Body.Bytes(), "activeProfile", "externalController", "externalControllerSecret")
	got := decode[executorConfig](t, w.Body.Bytes())
	if got.ActiveProfile != activeProfileID || got.ExternalController != "127.0.0.1:9090" {
		t.Fatalf("cfg = %+v", got)
	}
}

// UI 切换订阅只发这一条：必须真的触发重载，且失败要可见。
func TestPatchExecutorConfigSwitchProfile(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)
	body := `{"activeProfile":"` + activeProfileID + `"}`

	if w := do(t, mux, "PATCH", "/executor/config", body); w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if len(f.activeSet) != 1 || f.activeSet[0] != activeProfileID {
		t.Fatalf("activeSet = %v", f.activeSet)
	}
	assertKeys(t, do(t, mux, "PATCH", "/executor/config", body).Body.Bytes())

	// 失败必须返回非 2xx，让 select 协程能捕获。
	for _, c := range []struct {
		err  error
		code int
	}{
		{engine.ErrProfileNotFound, http.StatusNotFound},
		{engine.ErrProfileNoContent, http.StatusNotFound},
		{engine.ErrStorePathNotConfigured, http.StatusServiceUnavailable},
		{errors.New("parse config: boom"), http.StatusBadRequest},
	} {
		f.activeErr = c.err
		if w := do(t, mux, "PATCH", "/executor/config", body); w.Code != c.code {
			t.Fatalf("%v: code = %d, want %d", c.err, w.Code, c.code)
		}
	}
}

// externalController 由 /listeners/ExternalController 管：这里必须忽略，不能存第二份。
func TestPatchExecutorConfigIgnoresController(t *testing.T) {
	f := newFake()
	f.execCfg = engine.ExecutorConfig{ActiveProfile: activeProfileID, ExternalController: "127.0.0.1:9090", ExternalControllerSecret: "old"}
	mux := newTestServer(t, f)

	// 只带 controller：空操作 -> 400，且不改任何状态。
	w := do(t, mux, "PATCH", "/executor/config", `{"externalControllerSecret":"new"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("controller-only patch: code = %d, want 400", w.Code)
	}
	if f.execCfg.ExternalControllerSecret != "old" || f.execCfg.ExternalController != "127.0.0.1:9090" {
		t.Fatalf("controller must be untouched: %+v", f.execCfg)
	}

	// 同时带 activeProfile：切换生效，controller 仍被忽略。
	if w := do(t, mux, "PATCH", "/executor/config",
		`{"activeProfile":"`+activeProfileID+`","externalController":"0.0.0.0:9"}`); w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if f.execCfg.ExternalController != "127.0.0.1:9090" {
		t.Fatalf("controller must be ignored: %+v", f.execCfg)
	}
	if len(f.activeSet) != 1 {
		t.Fatalf("activeSet = %v", f.activeSet)
	}

	// 空 patch -> 400（避免"什么都没做但返回成功"）
	if w := do(t, mux, "PATCH", "/executor/config", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: code = %d", w.Code)
	}
	if w := do(t, mux, "PATCH", "/executor/config", `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: code = %d", w.Code)
	}
}

// --- /general -------------------------------------------------------------

func TestGetGeneralModeLowercase(t *testing.T) {
	for canonical, wire := range map[string]string{
		engine.ModeRule: "rule", engine.ModeGlobal: "global",
		engine.ModeDirect: "direct", engine.ModeScript: "script",
	} {
		f := newFake()
		f.mode = canonical
		got := decode[generalConfig](t, do(t, newTestServer(t, f), "GET", "/general", "").Body.Bytes())
		if got.Mode != wire {
			t.Fatalf("%s -> %q, want %q", canonical, got.Mode, wire)
		}
	}
	// 字段集合只有一个 mode。
	mux := newTestServer(t, newFake())
	assertKeys(t, do(t, mux, "GET", "/general", "").Body.Bytes(), "mode")
}

// mode 切换必须真生效（不是写快照等重启）。
func TestPatchGeneralModeTakesEffect(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)

	if w := do(t, mux, "PATCH", "/general", `{"mode":"global"}`); w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if f.mode != engine.ModeGlobal {
		t.Fatalf("mode = %q", f.mode)
	}
	// 大写也接受（容错），但线上输出永远小写。
	if w := do(t, mux, "PATCH", "/general", `{"mode":"Direct"}`); w.Code != http.StatusOK {
		t.Fatalf("uppercase mode: code = %d", w.Code)
	}
	if f.mode != engine.ModeDirect {
		t.Fatalf("mode = %q", f.mode)
	}
	// 未知 mode / 空 patch / script 不支持
	if w := do(t, mux, "PATCH", "/general", `{"mode":"nonsense"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown mode: code = %d", w.Code)
	}
	if w := do(t, mux, "PATCH", "/general", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: code = %d", w.Code)
	}
	if w := do(t, mux, "PATCH", "/general", `{"mode":"script"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("script: code = %d", w.Code)
	}
}

// --- /network/* -----------------------------------------------------------

func TestNetworkInterfaceRoundTrip(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)

	assertKeys(t, do(t, mux, "GET", "/network/interface", "").Body.Bytes(), "autoDetect", "name")

	if w := do(t, mux, "PATCH", "/network/interface", `{"autoDetect":false,"name":"rmnet_data0"}`); w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	// 关键约束：PATCH 之后 GET 必须立刻反映新值。
	got := decode[networkInterface](t, do(t, mux, "GET", "/network/interface", "").Body.Bytes())
	if got.Name != "rmnet_data0" || got.AutoDetect {
		t.Fatalf("after patch: %+v", got)
	}
}

func TestNetworkResolverShape(t *testing.T) {
	f := newFake()
	f.network = engine.NetworkConfig{
		Resolver: engine.NetworkResolver{
			BootstrapServers: []string{"223.5.5.5"},
			FallbackServers:  []string{"8.8.8.8"},
			PrimaryServers:   []string{"https://doh.example/dns-query"},
			FallbackFilter: engine.NetworkFallbackFilter{
				Domains: []string{"+.example.com"}, Geoip: true, GeoipCode: "CN", Ipcidr: []string{"10.0.0.0/8"},
			},
			NameServerPolicy: map[string]any{"geosite:cn": "223.5.5.5"},
		},
	}
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/network/resolver", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	assertKeys(t, w.Body.Bytes(), "bootstrapServers", "fallbackFilter", "fallbackServers", "nameServerPolicy", "primaryServers")
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &outer); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, outer["fallbackFilter"], "domains", "geoip", "geoipCode", "ipcidr")

	got := decode[networkResolver](t, w.Body.Bytes())
	if len(got.PrimaryServers) != 1 || got.FallbackFilter.GeoipCode != "CN" {
		t.Fatalf("resolver = %+v", got)
	}

	// PATCH 后回读一致
	patch := `{"bootstrapServers":["1.1.1.1"],"fallbackFilter":{"domains":["+.a.com"],"geoip":false,"geoipCode":"US","ipcidr":[]},` +
		`"fallbackServers":[],"nameServerPolicy":{},"primaryServers":["8.8.4.4"]}`
	if w := do(t, mux, "PATCH", "/network/resolver", patch); w.Code != http.StatusOK {
		t.Fatalf("patch: code = %d body=%s", w.Code, w.Body.String())
	}
	got = decode[networkResolver](t, do(t, mux, "GET", "/network/resolver", "").Body.Bytes())
	if len(got.BootstrapServers) != 1 || got.BootstrapServers[0] != "1.1.1.1" || got.FallbackFilter.GeoipCode != "US" {
		t.Fatalf("after patch: %+v", got)
	}
	// 空集合必须是 []，不能是 null。
	body := do(t, mux, "GET", "/network/resolver", "").Body.String()
	if strings.Contains(body, ":null") {
		t.Fatalf("nil slice leaked: %s", body)
	}
}

func TestNetworkFakeIPAndOutbound(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)

	assertKeys(t, do(t, mux, "GET", "/network/fakeip", "").Body.Bytes(), "enabled", "filter", "range")
	if w := do(t, mux, "PATCH", "/network/fakeip", `{"enabled":true,"filter":["+.a.com"],"range":"198.18.0.1/16"}`); w.Code != http.StatusOK {
		t.Fatalf("fakeip patch: code = %d", w.Code)
	}
	got := decode[networkFakeIP](t, do(t, mux, "GET", "/network/fakeip", "").Body.Bytes())
	if !got.Enabled || got.Range != "198.18.0.1/16" || len(got.Filter) != 1 {
		t.Fatalf("fakeip = %+v", got)
	}

	assertKeys(t, do(t, mux, "GET", "/network/outbound", "").Body.Bytes(), "ipv6")
	if w := do(t, mux, "PATCH", "/network/outbound", `{"ipv6":true}`); w.Code != http.StatusOK {
		t.Fatalf("outbound patch: code = %d", w.Code)
	}
	if got := decode[networkOutbound](t, do(t, mux, "GET", "/network/outbound", "").Body.Bytes()); !got.Ipv6 {
		t.Fatal("ipv6 not applied")
	}
}

// hosts 的响应形状是推断（模型里没有 Network.Hosts 类）：扁平 Map<String,String>。
func TestNetworkHosts(t *testing.T) {
	f := newFake()
	mux := newTestServer(t, f)

	if w := do(t, mux, "PATCH", "/network/hosts", `{"example.com":"1.2.3.4"}`); w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(do(t, mux, "GET", "/network/hosts", "").Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["example.com"] != "1.2.3.4" {
		t.Fatalf("hosts = %v", got)
	}
}

// --- /traffic /bandwidth（WebSocket）-------------------------------------

// readStreamFrame 从 WebSocket 读一帧并解成 Transmit.Bytes。
func readStreamFrame(t *testing.T, url string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	defer func() { _ = conn.CloseNow() }()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("frame is not JSON: %v (%s)", err, data)
	}
	return frame
}

func TestTrafficWebSocket(t *testing.T) {
	f := newFake()
	f.trafficUp, f.trafficDown = 1234, 5678
	srv := httptest.NewServer(newTestServer(t, f))
	defer srv.Close()

	frame := readStreamFrame(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/traffic")
	// 字段集合必须与 Transmit.Bytes 一致（多一个键 Kotlin 就崩）。
	keys := make([]string, 0, len(frame))
	for k := range frame {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "directDown,directUp,down,proxyDown,proxyUp,up" {
		t.Fatalf("frame keys = %v", keys)
	}
	if frame["up"] != float64(1234) || frame["down"] != float64(5678) {
		t.Fatalf("frame = %v", frame)
	}
}

// /bandwidth 用累计值，不是瞬时值——别弄反。
func TestBandwidthWebSocket(t *testing.T) {
	f := newFake()
	f.totalUp, f.totalDown = 999000, 888000
	f.trafficUp, f.trafficDown = 1, 2 // 瞬时值必须不被用到
	srv := httptest.NewServer(newTestServer(t, f))
	defer srv.Close()

	frame := readStreamFrame(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/bandwidth")
	if frame["up"] != float64(999000) || frame["down"] != float64(888000) {
		t.Fatalf("bandwidth must use totals: %v", frame)
	}
}

// 客户端主动断开是常态：handler 必须安静退出，不能把错误刷进日志/panic。
func TestStreamSurvivesClientDisconnect(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(newTestServer(t, f))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/traffic", nil)
		if err != nil {
			cancel()
			t.Fatalf("dial: %v", err)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			cancel()
			t.Fatalf("read: %v", err)
		}
		_ = conn.Close(websocket.StatusNormalClosure, "bye")
		cancel()
	}
	// 服务器还活着：普通 HTTP 端点仍可用。
	if w := do(t, newTestServer(t, f), "GET", "/status", ""); w.Code != http.StatusOK {
		t.Fatalf("server broken after disconnects: code = %d", w.Code)
	}
}

// 普通 GET（非 WebSocket 升级）不该 panic，只回 4xx。
func TestStreamRejectsPlainGet(t *testing.T) {
	mux := newTestServer(t, newFake())
	w := do(t, mux, "GET", "/traffic", "")
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("plain GET /traffic: code = %d, want 4xx", w.Code)
	}
}

// --- /connections/count ---------------------------------------------------

func TestConnectionsCount(t *testing.T) {
	f := newFake()
	f.connNow, f.connHistory = 7, 42
	mux := newTestServer(t, f)

	w := do(t, mux, "GET", "/connections/count", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	// 字段集合必须是 {history, now}（多一个键 kotlinx 就崩）。
	assertKeys(t, w.Body.Bytes(), "history", "now")
	got := decode[connectionsCount](t, w.Body.Bytes())
	if got.Now != 7 || got.History != 42 {
		t.Fatalf("count = %+v", got)
	}
}

// 未启动的引擎也要给出合法零值（UI 会轮询它）。
func TestConnectionsCountIdleEngine(t *testing.T) {
	e := engine.New()
	mux := http.NewServeMux()
	Register(mux, e)

	w := do(t, mux, "GET", "/connections/count", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	got := decode[connectionsCount](t, w.Body.Bytes())
	if got.Now != 0 || got.History != 0 {
		t.Fatalf("idle count = %+v", got)
	}
}
