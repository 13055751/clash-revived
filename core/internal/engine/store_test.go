package engine

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

const storeTestDDL = `
CREATE TABLE profiles (
    id UUID PRIMARY KEY,
    version INTEGER,
    name TEXT,
    remoteURL TEXT,
    updateExpr TEXT,
    updatedAt TIMESTAMP,
    content BLOB,
    modeOverride VARCHAR(32)
);
CREATE TABLE user_infos (
    profileID UUID PRIMARY KEY,
    uploaded INTEGER,
    downloaded INTEGER,
    total INTEGER,
    expireAt TIMESTAMP
);
`

// seedStore 造一个合成库（不含任何真实订阅信息）。
func seedStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(storeTestDDL); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	_, err = db.Exec(`INSERT INTO profiles (id, version, name, remoteURL, updateExpr, updatedAt, content, modeOverride)
VALUES
 ('11111111-1111-4111-8111-111111111111', 3, '订阅A', 'https://example.com/a', '0 3 * * *', '2023-12-08 01:46:40+00:00', X'00', 'rule'),
 ('22222222-2222-4222-8222-222222222222', 1, '订阅B', NULL, NULL, NULL, X'00', NULL)`)
	if err != nil {
		t.Fatalf("insert profiles: %v", err)
	}
	_, err = db.Exec(`INSERT INTO user_infos (profileID, uploaded, downloaded, total, expireAt)
VALUES ('11111111-1111-4111-8111-111111111111', 1024, 2048, 4096, '2024-12-08 01:46:40+00:00')`)
	if err != nil {
		t.Fatalf("insert user_infos: %v", err)
	}
	return path
}

func TestProfilesReadStore(t *testing.T) {
	path := seedStore(t)
	e := New()
	e.SetDatabasePath(path)

	got, err := e.Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("profiles = %d (%+v)", len(got), got)
	}
	byID := map[string]Profile{}
	for _, p := range got {
		byID[p.ID] = p
	}
	a := byID["11111111-1111-4111-8111-111111111111"]
	if a.Name != "订阅A" || a.RemoteURL != "https://example.com/a" || a.UpdateExpr != "0 3 * * *" {
		t.Fatalf("profile A = %+v", a)
	}
	// uploaded/downloaded -> Upload/Download（名字差一个 ed，取错就是 0）
	if a.Upload != 1024 || a.Download != 2048 || a.Total != 4096 {
		t.Fatalf("user info = %+v", a)
	}
	// 2023-12-08T01:46:40Z
	if a.UpdatedAtUnixMS != 1702000000000 {
		t.Fatalf("updatedAt = %d", a.UpdatedAtUnixMS)
	}
	if a.ExpireAtUnixMS != 1733622400000 {
		t.Fatalf("expireAt = %d", a.ExpireAtUnixMS)
	}

	b := byID["22222222-2222-4222-8222-222222222222"]
	if b.Name != "订阅B" || b.RemoteURL != "" || b.UpdateExpr != "" {
		t.Fatalf("profile B = %+v", b)
	}
	// 没有 user_infos 行 + updatedAt 为 NULL：必须都是 0（未知），不能编造。
	if b.Upload != 0 || b.Download != 0 || b.Total != 0 || b.UpdatedAtUnixMS != 0 || b.ExpireAtUnixMS != 0 {
		t.Fatalf("profile B should be all-zero where unknown: %+v", b)
	}
}

func TestProfilesPathNotConfigured(t *testing.T) {
	e := New()
	if _, err := e.Profiles(); !errors.Is(err, ErrStorePathNotConfigured) {
		t.Fatalf("err = %v, want ErrStorePathNotConfigured", err)
	}
}

// 老库/空库里没有 profiles 表：当作"没有配置"，返回空列表而不是报错。
func TestProfilesMissingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE preferences (key VARCHAR(128) PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	e := New()
	e.SetDatabasePath(path)
	got, err := e.Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("profiles = %+v, want empty", got)
	}
}

// UUID 可能落成 TEXT 也可能落成 16 字节 BLOB，两种都要能读。
func TestProfilesUUIDAsBlob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(storeTestDDL); err != nil {
		t.Fatal(err)
	}
	blob := []byte{
		0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x41, 0x11,
		0x81, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11,
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES (?, 'blob-id')`, blob); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	e := New()
	e.SetDatabasePath(path)
	got, err := e.Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(got) != 1 || got[0].ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("id = %q", got[0].ID)
	}
}

func TestDecodeTime(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int64
	}{
		{"nil", nil, 0},
		{"epoch seconds", int64(1702000000), 1702000000000},
		{"epoch millis", int64(1702000000000), 1702000000000},
		{"float seconds", float64(1702000000), 1702000000000},
		{"sqlite text", "2023-12-08 01:46:40+00:00", 1702000000000},
		{"rfc3339", "2023-12-08T01:46:40Z", 1702000000000},
		{"date only", "2023-12-08", 1701993600000},
		{"numeric text", "1702000000", 1702000000000},
		{"time.Time", time.Date(2023, 12, 8, 1, 46, 40, 0, time.UTC), 1702000000000},
		{"zero time.Time", time.Time{}, 0},
		{"garbage", "not a time", 0},
		{"empty", "", 0},
	}
	for _, c := range cases {
		if got := decodeTime(c.in); got != c.want {
			t.Fatalf("%s: decodeTime(%v) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

func TestDecodeUUID(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"text", "11111111-1111-4111-8111-111111111111", "11111111-1111-4111-8111-111111111111"},
		{"raw 16-byte blob", []byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x41, 0x11,
			0x81, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}, "11111111-1111-4111-8111-111111111111"},
		{"ascii blob", []byte("22222222-2222-4222-8222-222222222222"), "22222222-2222-4222-8222-222222222222"},
	}
	for _, c := range cases {
		if got := decodeUUID(c.in); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
