package engine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/hub/executor"
)

// CFA 侧 Sessions.CreateSessionSpec.Type 的三个取值
// （dex：Sessions$CreateSessionSpec$Type.<clinit> => Edit, NewFile, NewURL）。
const (
	SessionTypeEdit    = "Edit"
	SessionTypeNewFile = "NewFile"
	SessionTypeNewURL  = "NewURL"
)

// CFA 侧 Sessions.StoreStatus.Stage 的四个取值
// （dex：Sessions$StoreStatus$Stage.<clinit> => Verifying, CompletingProviders, Failed, Completed）。
const (
	StoreStageVerifying           = "Verifying"
	StoreStageCompletingProviders = "CompletingProviders"
	StoreStageFailed              = "Failed"
	StoreStageCompleted           = "Completed"
)

// sessionStageFloor 是 Verifying 阶段的最短停留时间。
//
// 真实内核里下载/解析配置是异步的，UI 靠轮询 StoreStatus 画进度条。
// 本实现的解析是毫秒级的，不设下限的话 UI 从 0 直接跳到 100（看起来像卡了一次）。
// 这是产品体验上的取舍，不是协议要求。
var sessionStageFloor = 500 * time.Millisecond

// sessionProviderInitTimeout 限制单个 provider 初始化（下载/读文件）的时间。
var sessionProviderInitTimeout = 30 * time.Second

// SessionSpec 是 POST /profiles/sessions 的请求体（Sessions.CreateSessionSpec）。
type SessionSpec struct {
	Type        string
	URL         string
	FileName    string
	FileContent string
	ProfileID   string
}

// SessionAttributes 对应 Profiles.Profile.Attributes。
type SessionAttributes struct {
	Name       string
	RemoteURL  string
	UpdateExpr string
	// UpdatedAtUnixMS 为 0 表示未知（输出零值 Instant，不要伪装成"刚刚更新"）。
	UpdatedAtUnixMS int64
}

// ProviderDescriptor 对应 Sessions.ProviderDescriptor。
type ProviderDescriptor struct {
	Name        string
	Type        string // ProviderTypeProxy / ProviderTypeRule
	VehicleType string
	// Length 对 proxy provider 是节点数，对 rule provider 是规则条数。
	Length int
}

// SessionStoreStatus 对应 Sessions.StoreStatus。
type SessionStoreStatus struct {
	Stage     string
	Error     string
	Completed int
	Total     int
}

// ProfilesConfig 对应 Profiles.Config（会话的 profile 级配置）。
//
// 用途 1：`/profiles/sessions/{id}/config`（TODO(联调)：方法体被 R8 内联，按模型实现）
// 用途 2：`/profiles/config`（全局，持久化在 preferences 表，见 store_config.go）
type ProfilesConfig struct {
	MaxRetryCount   int
	NotifyOnSuccess bool
	NotifyOnFailure bool
	WithTunnel      bool
}

// Session 是一个会话的完整快照（值类型，外部拿到的是副本）。
type Session struct {
	ID         string
	Spec       SessionSpec
	Attributes SessionAttributes
	Config     ProfilesConfig
	Providers  []ProviderDescriptor
	Status     SessionStoreStatus
	CreatedAt  time.Time
	// Payload 是后台阶段下载/读到的配置原文。
	// 保存会话时直接复用，避免二次下载（订阅通常要翻墙，重下代价高且可能失败）。
	Payload []byte
}

// sessionStore 是会话的存储抽象。
//
// 宿主侧的类型表里有 `map[uuid.UUID]*sessions.Session`，且 store 包（SQLite）只有
// ProfileTable/ProvidersTable/ProxySelection/UserInfo 四张表——**没有 SessionTable**。
// 所以会话就是内存 map，这里保留一层接口只是为了让"内存 vs 落盘"这个真实可变点
// 将来可以单点替换，不牵连 handler。
type sessionStore interface {
	Put(s *sessionRecord)
	Get(id string) (*sessionRecord, bool)
	Delete(id string) bool
	Range(f func(*sessionRecord) bool)
}

type sessionRecord struct {
	mu   sync.RWMutex
	data Session
}

func (r *sessionRecord) snapshot() Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := r.data
	out.Providers = append([]ProviderDescriptor{}, r.data.Providers...)
	out.Payload = append([]byte(nil), r.data.Payload...)
	return out
}

func (r *sessionRecord) mutate(f func(*Session)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(&r.data)
}

type memorySessionStore struct {
	mu sync.RWMutex
	m  map[string]*sessionRecord
}

func newMemorySessionStore() *memorySessionStore {
	return &memorySessionStore{m: make(map[string]*sessionRecord, 4)}
}

func (s *memorySessionStore) Put(r *sessionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[r.data.ID] = r
}

func (s *memorySessionStore) Get(id string) (*sessionRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.m[id]
	return r, ok
}

func (s *memorySessionStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; !ok {
		return false
	}
	delete(s.m, id)
	return true
}

func (s *memorySessionStore) Range(f func(*sessionRecord) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.m {
		if !f(r) {
			return
		}
	}
}

// NewSession 建一个会话并异步开始 store（解析配置 + 初始化 provider）。
//
// 返回值是 sessionId；真正的结果要靠轮询 StoreStatus（GET /profiles/sessions/{id}）。
// 语义上等价于宿主侧的 Sessions.newSession → sessions.storeSession。
func (e *Engine) NewSession(spec SessionSpec) (string, error) {
	switch spec.Type {
	case SessionTypeNewURL:
		if spec.URL == "" {
			return "", fmt.Errorf("engine: NewURL session requires url")
		}
	case SessionTypeNewFile, SessionTypeEdit:
		if spec.FileContent == "" {
			return "", fmt.Errorf("engine: %s session requires fileContent", spec.Type)
		}
	default:
		return "", fmt.Errorf("engine: unknown session type %q", spec.Type)
	}

	id := newSessionID()
	rec := &sessionRecord{data: Session{
		ID:        id,
		Spec:      spec,
		CreatedAt: time.Now(),
		Status:    SessionStoreStatus{Stage: StoreStageVerifying},
		Attributes: SessionAttributes{
			Name:      sessionName(spec),
			RemoteURL: spec.URL,
		},
		Providers: []ProviderDescriptor{},
	}}
	e.sessionStore.Put(rec)
	go e.storeSession(rec)
	return id, nil
}

// Session 取会话快照。
func (e *Engine) Session(id string) (Session, bool) {
	rec, ok := e.sessionStore.Get(id)
	if !ok {
		return Session{}, false
	}
	return rec.snapshot(), true
}

// DeleteSession 删会话。
func (e *Engine) DeleteSession(id string) bool { return e.sessionStore.Delete(id) }

// SetSessionConfig 写会话的 profile 级配置。
func (e *Engine) SetSessionConfig(id string, cfg ProfilesConfig) bool {
	rec, ok := e.sessionStore.Get(id)
	if !ok {
		return false
	}
	rec.mutate(func(s *Session) { s.Config = cfg })
	return true
}

// SetSessionAttributes 写会话属性（用户改名等）。
func (e *Engine) SetSessionAttributes(id string, attrs SessionAttributes) bool {
	rec, ok := e.sessionStore.Get(id)
	if !ok {
		return false
	}
	rec.mutate(func(s *Session) { s.Attributes = attrs })
	return true
}

// storeSession 是会话的后台推进：Verifying（取内容 + 解析）→ CompletingProviders → Completed/Failed。
func (e *Engine) storeSession(rec *sessionRecord) {
	// 让 Verifying 至少可观测一次（见 sessionStageFloor 注释）。
	if floor := sessionStageFloor; floor > 0 {
		time.Sleep(floor)
	}

	rec.mutate(func(s *Session) { s.Status.Stage = StoreStageVerifying })

	payload, err := e.sessionPayload(rec)
	if err != nil {
		rec.mutate(func(s *Session) {
			s.Status.Stage = StoreStageFailed
			s.Status.Error = err.Error()
		})
		return
	}
	// 缓存原文：后续「保存」直接用它，不再重新下载。
	rec.mutate(func(s *Session) { s.Payload = payload })

	cfg, err := executor.ParseWithBytes(payload)
	if err != nil {
		rec.mutate(func(s *Session) {
			s.Status.Stage = StoreStageFailed
			s.Status.Error = fmt.Sprintf("parse config: %v", err)
		})
		return
	}

	descriptors := sessionProviders(cfg)

	rec.mutate(func(s *Session) {
		s.Status.Stage = StoreStageCompletingProviders
		s.Status.Total = len(descriptors)
		s.Status.Completed = 0
		s.Attributes.UpdatedAtUnixMS = time.Now().UnixMilli()
		s.Providers = []ProviderDescriptor{}
	})

	failed := ""
	for i := range descriptors {
		d := &descriptors[i]
		length, err := initSessionProvider(cfg, *d)
		if err != nil && failed == "" {
			// 原内核的行为无法确认，这里取"记错误但继续"：一个 provider 拉不下来
			// 不应该让整个会话失败，UI 仍然能看到其它 provider。
			failed = fmt.Sprintf("provider %s: %v", d.Name, err)
			slog.Warn("session provider init failed", "session", rec.data.ID, "provider", d.Name, "err", err)
		}
		if err == nil {
			d.Length = length
		}
		rec.mutate(func(s *Session) {
			s.Providers = append(s.Providers, *d)
			s.Status.Completed = len(s.Providers)
		})
	}

	rec.mutate(func(s *Session) {
		if failed != "" {
			s.Status.Stage = StoreStageFailed
			s.Status.Error = failed
			return
		}
		s.Status.Stage = StoreStageCompleted
	})
}

// sessionPayload 取会话要解析的配置原文。
func (e *Engine) sessionPayload(rec *sessionRecord) ([]byte, error) {
	spec := rec.snapshot().Spec
	if spec.Type != SessionTypeNewURL {
		return []byte(spec.FileContent), nil
	}
	return fetchURL(spec.URL)
}

// sessionProviders 从解析后的配置里收集 provider 描述。
func sessionProviders(cfg *config.Config) []ProviderDescriptor {
	out := make([]ProviderDescriptor, 0, len(cfg.Providers)+len(cfg.RuleProviders))
	for name, prov := range cfg.Providers {
		if name == provider.ReservedName {
			continue
		}
		out = append(out, ProviderDescriptor{
			Name:        name,
			Type:        ProviderTypeProxy,
			VehicleType: vehicleName(prov.VehicleType()),
		})
	}
	for name, prov := range cfg.RuleProviders {
		out = append(out, ProviderDescriptor{
			Name:        name,
			Type:        ProviderTypeRule,
			VehicleType: vehicleName(prov.VehicleType()),
			Length:      prov.Count(),
		})
	}
	return out
}

// initSessionProvider 初始化一个 provider 并返回它的条目数；带超时保护。
//
// 长度必须等 Initial() 之后才有意义：未初始化的 provider 手里还没有节点/规则。
func initSessionProvider(cfg *config.Config, d ProviderDescriptor) (int, error) {
	if d.Type == ProviderTypeRule {
		prov, ok := cfg.RuleProviders[d.Name]
		if !ok {
			return 0, fmt.Errorf("missing from parsed config")
		}
		if err := withinTimeout(sessionProviderInitTimeout, func() error { return prov.Initial() }); err != nil {
			return 0, err
		}
		return prov.Count(), nil
	}
	prov, ok := cfg.Providers[d.Name]
	if !ok {
		return 0, fmt.Errorf("missing from parsed config")
	}
	if err := withinTimeout(sessionProviderInitTimeout, func() error { return prov.Initial() }); err != nil {
		return 0, err
	}
	return len(prov.Proxies()), nil
}

// withinTimeout 在 goroutine 里跑 fn，超时就放弃等待（goroutine 会自己跑完）。
func withinTimeout(d time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		return fmt.Errorf("timeout after %s", d)
	}
}

// sessionName 推断会话（未来的 profile）名字：优先文件名，其次 URL 的最后一段。
func sessionName(spec SessionSpec) string {
	if spec.FileName != "" {
		return spec.FileName
	}
	if spec.URL == "" {
		return ""
	}
	tail := spec.URL
	if i := strings.LastIndexByte(tail, '/'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	if i := strings.IndexAny(tail, "?#"); i >= 0 {
		tail = tail[:i]
	}
	if tail == "" {
		return spec.URL
	}
	return tail
}

// newSessionID 生成 UUIDv4 文本（不用第三方包，crypto/rand 够了）。
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败意味着系统熵源坏了，退化成时间戳也能保证进程内唯一。
		now := time.Now().UnixNano()
		return fmt.Sprintf("%08x-0000-4000-8000-%012x", uint32(now>>32), uint64(now)&0xffffffffffff)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}
