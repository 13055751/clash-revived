package engine

import (
	"strings"
	"testing"
	"time"
)

const sessionTestConfig = `mode: rule
log-level: silent
proxy-providers:
  inline-prov:
    type: inline
    payload:
      - {name: n1, type: ss, server: 127.0.0.1, port: 1, cipher: aes-128-gcm, password: x}
      - {name: n2, type: ss, server: 127.0.0.1, port: 2, cipher: aes-128-gcm, password: x}
rule-providers:
  rules-a:
    type: inline
    behavior: domain
    format: yaml
    payload:
      - "+.example.com"
`

// waitSession 轮询到会话进入终态（Completed / Failed）。
func waitSession(t *testing.T, e *Engine, id string) Session {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sess, ok := e.Session(id)
		if !ok {
			t.Fatalf("session %s disappeared", id)
		}
		if sess.Status.Stage == StoreStageCompleted || sess.Status.Stage == StoreStageFailed {
			return sess
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s did not reach terminal stage", id)
	return Session{}
}

func TestSessionNewFile(t *testing.T) {
	old := sessionStageFloor
	sessionStageFloor = 0
	defer func() { sessionStageFloor = old }()

	e := New()
	id, err := e.NewSession(SessionSpec{
		Type:        SessionTypeNewFile,
		FileName:    "我的订阅.yaml",
		FileContent: sessionTestConfig,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatalf("session id = %q, want UUID text", id)
	}
	if _, ok := e.Session(id); !ok {
		t.Fatal("session must be retrievable right after creation")
	}

	sess := waitSession(t, e, id)
	if sess.Status.Stage != StoreStageCompleted {
		t.Fatalf("stage = %q, err = %q", sess.Status.Stage, sess.Status.Error)
	}
	if sess.Status.Total != 2 || sess.Status.Completed != 2 {
		t.Fatalf("progress = %d/%d, want 2/2", sess.Status.Completed, sess.Status.Total)
	}
	if len(sess.Providers) != 2 {
		t.Fatalf("providers = %+v", sess.Providers)
	}
	byName := map[string]ProviderDescriptor{}
	for _, p := range sess.Providers {
		byName[p.Name] = p
	}
	if p := byName["inline-prov"]; p.Type != ProviderTypeProxy || p.Length != 2 {
		t.Fatalf("proxy provider = %+v", p)
	}
	if p := byName["rules-a"]; p.Type != ProviderTypeRule || p.Length != 1 {
		t.Fatalf("rule provider = %+v", p)
	}
	// 属性：文件名当名字，更新时间是服务端写的。
	if sess.Attributes.Name != "我的订阅.yaml" {
		t.Fatalf("attributes.name = %q", sess.Attributes.Name)
	}
	if sess.Attributes.UpdatedAtUnixMS == 0 {
		t.Fatal("attributes.updatedAt must be set after successful store")
	}
	if !e.DeleteSession(id) {
		t.Fatal("DeleteSession failed")
	}
	if _, ok := e.Session(id); ok {
		t.Fatal("session must be gone after delete")
	}
}

// Verifying 必须能被观测到，不能一步跳到 Completed（UI 进度条要求）。
func TestSessionVerifyingObservable(t *testing.T) {
	old := sessionStageFloor
	sessionStageFloor = 120 * time.Millisecond
	defer func() { sessionStageFloor = old }()

	e := New()
	id, err := e.NewSession(SessionSpec{Type: SessionTypeNewFile, FileContent: sessionTestConfig})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess, _ := e.Session(id)
	if sess.Status.Stage != StoreStageVerifying {
		t.Fatalf("immediately after create: stage = %q, want Verifying", sess.Status.Stage)
	}
	// 在 floor 之内仍然应该是 Verifying。
	time.Sleep(30 * time.Millisecond)
	if sess, _ := e.Session(id); sess.Status.Stage != StoreStageVerifying {
		t.Fatalf("stage within floor = %q, want Verifying", sess.Status.Stage)
	}
	if got := waitSession(t, e, id); got.Status.Stage != StoreStageCompleted {
		t.Fatalf("final stage = %q", got.Status.Stage)
	}
}

func TestSessionParseFailure(t *testing.T) {
	old := sessionStageFloor
	sessionStageFloor = 0
	defer func() { sessionStageFloor = old }()

	e := New()
	id, err := e.NewSession(SessionSpec{Type: SessionTypeNewFile, FileContent: "mode: [unclosed\n"})
	if err != nil {
		t.Fatalf("NewSession must accept the payload, failure is async: %v", err)
	}
	sess := waitSession(t, e, id)
	if sess.Status.Stage != StoreStageFailed {
		t.Fatalf("stage = %q, want Failed", sess.Status.Stage)
	}
	if sess.Status.Error == "" {
		t.Fatal("failed session must carry an error message")
	}
}

func TestSessionValidation(t *testing.T) {
	e := New()
	if _, err := e.NewSession(SessionSpec{Type: SessionTypeNewURL}); err == nil {
		t.Fatal("NewURL without url must be rejected")
	}
	if _, err := e.NewSession(SessionSpec{Type: SessionTypeNewFile}); err == nil {
		t.Fatal("NewFile without fileContent must be rejected")
	}
	if _, err := e.NewSession(SessionSpec{Type: "Nonsense"}); err == nil {
		t.Fatal("unknown type must be rejected")
	}
	if _, ok := e.Session("missing"); ok {
		t.Fatal("missing session must not resolve")
	}
	if e.DeleteSession("missing") {
		t.Fatal("deleting missing session must fail")
	}
}

func TestSessionConfigAndAttributes(t *testing.T) {
	old := sessionStageFloor
	sessionStageFloor = 0
	defer func() { sessionStageFloor = old }()

	e := New()
	id, err := e.NewSession(SessionSpec{Type: SessionTypeNewFile, FileContent: sessionTestConfig})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = waitSession(t, e, id)

	if !e.SetSessionConfig(id, ProfilesConfig{MaxRetryCount: 5, NotifyOnSuccess: true, WithTunnel: true}) {
		t.Fatal("SetSessionConfig failed")
	}
	sess, _ := e.Session(id)
	if sess.Config.MaxRetryCount != 5 || !sess.Config.NotifyOnSuccess || !sess.Config.WithTunnel {
		t.Fatalf("config = %+v", sess.Config)
	}
	if !e.SetSessionAttributes(id, SessionAttributes{Name: "改名", UpdateExpr: "0 3 * * *", UpdatedAtUnixMS: 42}) {
		t.Fatal("SetSessionAttributes failed")
	}
	sess, _ = e.Session(id)
	if sess.Attributes.Name != "改名" || sess.Attributes.UpdateExpr != "0 3 * * *" || sess.Attributes.UpdatedAtUnixMS != 42 {
		t.Fatalf("attributes = %+v", sess.Attributes)
	}
	if e.SetSessionConfig("missing", ProfilesConfig{}) {
		t.Fatal("SetSessionConfig on missing session must fail")
	}
}
