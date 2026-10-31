package engine

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestProfilesConfigDefault(t *testing.T) {
	e := New()
	// 没配 store 路径：读回默认值，不报错。
	cfg, err := e.LoadProfilesConfig()
	if err != nil {
		t.Fatalf("LoadProfilesConfig: %v", err)
	}
	if cfg != (ProfilesConfig{}) {
		t.Fatalf("cfg = %+v", cfg)
	}
	// 但写必须报错（不能假装存上了）。
	if err := e.SaveProfilesConfig(ProfilesConfig{MaxRetryCount: 1}); err != ErrStorePathNotConfigured {
		t.Fatalf("save err = %v", err)
	}
}

func TestProfilesConfigRoundTrip(t *testing.T) {
	path := seedWritableStore(t, "")
	// seedWritableStore 没建 preferences 表：写路径应该自己建。
	e := New()
	e.SetDatabasePath(path)

	want := ProfilesConfig{MaxRetryCount: 4, NotifyOnSuccess: true, WithTunnel: true}
	if err := e.SaveProfilesConfig(want); err != nil {
		t.Fatalf("SaveProfilesConfig: %v", err)
	}
	got, err := e.LoadProfilesConfig()
	if err != nil {
		t.Fatalf("LoadProfilesConfig: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	// 再写一次是 upsert，不是插第二条。
	if err := e.SaveProfilesConfig(ProfilesConfig{MaxRetryCount: 9}); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", "file:"+path+"?mode=ro")
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("preferences rows = %d, want 1 (upsert)", n)
	}
	got, _ = e.LoadProfilesConfig()
	if got.MaxRetryCount != 9 {
		t.Fatalf("cfg = %+v", got)
	}
}

// 值损坏时回默认值，不能把整个设置页搞挂。
func TestProfilesConfigCorruptValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE preferences (key VARCHAR(128) PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO preferences (key, value) VALUES (?, ?)`, preferenceKeyProfilesConfig, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	e := New()
	e.SetDatabasePath(path)
	got, err := e.LoadProfilesConfig()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != (ProfilesConfig{}) {
		t.Fatalf("corrupt value must fall back to defaults: %+v", got)
	}
}
