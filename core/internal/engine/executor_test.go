package engine

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// seedProfileWithContent 在合成库里放一个有 content 的 profile。
func seedProfileWithContent(t *testing.T, id, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(writeTestDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, version, name, content) VALUES (?, 1, 'x', ?)`, id, []byte(content)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	return path
}

// 切换 profile = 立刻重载 tunnel，而不是只记一个字段。
func TestSetActiveProfileReloads(t *testing.T) {
	path := seedProfileWithContent(t, profileA, "mode: global\nlog-level: silent\n")
	e := New()
	e.SetDatabasePath(path)

	if err := e.SetActiveProfile(profileA); err != nil {
		t.Fatalf("SetActiveProfile: %v", err)
	}
	if !e.Running() {
		t.Fatal("switch must bring the tunnel up")
	}
	if got := e.ProfileID(); got != profileA {
		t.Fatalf("profileID = %q", got)
	}
	// 配置真的被应用了：content 里是 global。
	if got := e.Mode(); got != ModeGlobal {
		t.Fatalf("mode = %q, want Global (config not applied)", got)
	}
	_ = e.Stop()
}

// 解析失败时：tunnel 不动、ProfileID 不变（状态不能脏）。
func TestSetActiveProfileBadConfig(t *testing.T) {
	path := seedProfileWithContent(t, profileA, "mode: [unclosed\n")
	e := New()
	e.SetDatabasePath(path)

	if err := e.Start(nil, -1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = e.Stop() }()
	before := e.ProfileID()

	if err := e.SetActiveProfile(profileA); err == nil {
		t.Fatal("invalid profile content must be rejected")
	}
	if !e.Running() {
		t.Fatal("existing tunnel must keep running after a failed switch")
	}
	if got := e.ProfileID(); got != before {
		t.Fatalf("profileID changed to %q after failure", got)
	}
}

func TestSetActiveProfileNoContentOrMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, _ := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if _, err := db.Exec(writeTestDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES (?, 'no content')`, profileA); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	e := New()
	e.SetDatabasePath(path)
	if err := e.SetActiveProfile(profileA); err != ErrProfileNoContent {
		t.Fatalf("err = %v, want ErrProfileNoContent", err)
	}
	if err := e.SetActiveProfile("99999999-9999-4999-8999-999999999999"); err != ErrProfileNotFound {
		t.Fatalf("err = %v, want ErrProfileNotFound", err)
	}
	if e.Running() {
		t.Fatal("failed switch must not start the tunnel")
	}
}

func TestExecutorConfigDTO(t *testing.T) {
	e := New()
	e.SetProfileID(profileA)
	e.SetExternalController("127.0.0.1:9090", "s3cr3t")
	cfg := e.ExecutorConfig()
	if cfg.ActiveProfile != profileA || cfg.ExternalController != "127.0.0.1:9090" || cfg.ExternalControllerSecret != "s3cr3t" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

// tun 要 fd 而宿主没给：必须在停旧 tunnel 之前拦下来。
func TestSetActiveProfileTunPrecheck(t *testing.T) {
	path := seedProfileWithContent(t, profileA, "tun:\n  enable: true\nmode: rule\nlog-level: silent\n")
	e := New()
	e.SetDatabasePath(path)
	if err := e.Start(nil, -1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = e.Stop() }()

	err := e.SetActiveProfile(profileA)
	if err == nil {
		t.Fatal("tun without fd must be rejected before stopping the tunnel")
	}
	if !e.Running() {
		t.Fatal("old tunnel must stay up when the precondition fails")
	}
	if got := e.ProfileID(); got != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("profileID = %q, must not change", got)
	}
}
