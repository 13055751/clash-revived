package engine

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

const writeTestDDL = `
CREATE TABLE profiles (id UUID PRIMARY KEY, version INTEGER, name TEXT, remoteURL TEXT,
  updateExpr TEXT, updatedAt TIMESTAMP, content BLOB, modeOverride VARCHAR(32));
CREATE TABLE user_infos (profileID UUID PRIMARY KEY, uploaded INTEGER, downloaded INTEGER,
  total INTEGER, expireAt TIMESTAMP,
  FOREIGN KEY(profileID) REFERENCES profiles(id) ON DELETE CASCADE ON UPDATE CASCADE);
CREATE TABLE providers (profileID UUID, type VARCHAR(16), name TEXT, updatedAt TIMESTAMP,
  content BLOB,
  FOREIGN KEY (profileID) REFERENCES profiles(id) ON DELETE CASCADE ON UPDATE CASCADE,
  PRIMARY KEY (profileID, type, name));
CREATE TABLE proxy_selections (profileID UUID, groupName TEXT, proxyName TEXT,
  FOREIGN KEY (profileID) REFERENCES profiles(id) ON DELETE CASCADE ON UPDATE CASCADE,
  PRIMARY KEY (profileID, groupName));
`

const (
	profileA = "11111111-1111-4111-8111-111111111111"
	profileB = "22222222-2222-4222-8222-222222222222"
)

// seedWritableStore 造一个带父子表数据的合成库。
func seedWritableStore(t *testing.T, remoteURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(writeTestDDL); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, version, name, remoteURL, updateExpr, updatedAt, content, modeOverride)
VALUES (?, 2, '本地', ?, '0 3 * * *', '2023-12-08 01:46:40+00:00', X'00', 'rule')`,
		profileA, nullable(remoteURL)); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO user_infos VALUES ('` + profileA + `', 1, 2, 3, NULL)`,
		`INSERT INTO providers VALUES ('` + profileA + `', 'Proxy', 'prov-a', NULL, X'00')`,
		`INSERT INTO proxy_selections VALUES ('` + profileA + `', 'GLOBAL', 'node-a')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed child row: %v", err)
		}
	}
	return path
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func countRows(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// 删除必须真的级联清掉三张子表——pragma 没生效时 SQLite 不报错，只留孤儿行。
func TestDeleteProfileCascade(t *testing.T) {
	path := seedWritableStore(t, "")
	// 另加一个"无关"profile，确认不会被误删。
	db, _ := sql.Open("sqlite", "file:"+path+"?mode=rwc&_pragma=foreign_keys(1)")
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES (?, '无关')`, profileB); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	e := New()
	e.SetDatabasePath(path)
	if err := e.DeleteProfile(profileA); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if n := countRows(t, path, "profiles"); n != 1 {
		t.Fatalf("profiles = %d, want 1 (other profile must survive)", n)
	}
	for _, table := range []string{"user_infos", "providers", "proxy_selections"} {
		if n := countRows(t, path, table); n != 0 {
			t.Fatalf("%s = %d after cascade, want 0", table, n)
		}
	}
}

func TestDeleteProfileNotFound(t *testing.T) {
	path := seedWritableStore(t, "")
	e := New()
	e.SetDatabasePath(path)

	if err := e.DeleteProfile("99999999-9999-4999-8999-999999999999"); err != ErrProfileNotFound {
		t.Fatalf("err = %v, want ErrProfileNotFound", err)
	}
	// 失败后数据必须原封不动。
	if n := countRows(t, path, "profiles"); n != 1 {
		t.Fatalf("profiles = %d after failed delete", n)
	}
	if n := countRows(t, path, "proxy_selections"); n != 1 {
		t.Fatalf("proxy_selections = %d after failed delete", n)
	}
}

func TestDeleteProfileNoStore(t *testing.T) {
	e := New()
	if err := e.DeleteProfile(profileA); err != ErrStorePathNotConfigured {
		t.Fatalf("err = %v", err)
	}
}

// PUT /profiles/{id} = 重新下载。成功时只动 content/version/updatedAt。
func TestRefreshProfile(t *testing.T) {
	body := "mode: rule\nlog-level: silent\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	path := seedWritableStore(t, srv.URL)
	e := New()
	e.SetDatabasePath(path)

	if err := e.RefreshProfile(profileA); err != nil {
		t.Fatalf("RefreshProfile: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var (
		content    string
		version    int64
		name       string
		updateExpr string
		mode       string
	)
	if err := db.QueryRow(`SELECT CAST(content AS TEXT), version, name, updateExpr, modeOverride
FROM profiles WHERE id = ?`, profileA).Scan(&content, &version, &name, &updateExpr, &mode); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if content != body {
		t.Fatalf("content = %q", content)
	}
	if version != 3 {
		t.Fatalf("version = %d, want 3 (2+1)", version)
	}
	// 用户元数据不能被下载流程改动。
	if name != "本地" || updateExpr != "0 3 * * *" || mode != "rule" {
		t.Fatalf("metadata changed: name=%q updateExpr=%q mode=%q", name, updateExpr, mode)
	}
}

// 本地 profile（无 remoteURL）不能去 fetch：返回明确错误，而不是把内容清空。
func TestRefreshProfileNoRemoteURL(t *testing.T) {
	path := seedWritableStore(t, "")
	e := New()
	e.SetDatabasePath(path)

	if err := e.RefreshProfile(profileA); err != ErrProfileNoRemoteURL {
		t.Fatalf("err = %v, want ErrProfileNoRemoteURL", err)
	}
	if n := countRows(t, path, "profiles"); n != 1 {
		t.Fatalf("profiles = %d", n)
	}
}

// 下载回来的内容不合法时，不能覆盖旧内容。
func TestRefreshProfileInvalidContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("mode: [unclosed\n"))
	}))
	defer srv.Close()

	path := seedWritableStore(t, srv.URL)
	e := New()
	e.SetDatabasePath(path)

	if err := e.RefreshProfile(profileA); err == nil {
		t.Fatal("invalid downloaded config must be rejected")
	}
	db, _ := sql.Open("sqlite", "file:"+path+"?mode=ro")
	defer func() { _ = db.Close() }()
	var version int64
	var content []byte
	if err := db.QueryRow(`SELECT version, content FROM profiles WHERE id = ?`, profileA).Scan(&version, &content); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("version = %d, must stay 2 on failure", version)
	}
	if string(content) == "mode: [unclosed\n" {
		t.Fatal("invalid content must not be persisted")
	}
}

func TestRefreshProfileNotFound(t *testing.T) {
	path := seedWritableStore(t, "https://example.com/x")
	e := New()
	e.SetDatabasePath(path)
	if err := e.RefreshProfile("99999999-9999-4999-8999-999999999999"); err != ErrProfileNotFound {
		t.Fatalf("err = %v", err)
	}
}

// 没有激活 profile 时，选择不落库（只 warn）。
func TestSaveProxySelectionSkippedWithoutProfile(t *testing.T) {
	path := seedWritableStore(t, "")
	e := New()
	e.SetDatabasePath(path)

	if err := e.SaveProxySelection("GLOBAL", "node-b"); err != nil {
		t.Fatalf("SaveProxySelection: %v", err)
	}
	if n := countRows(t, path, "proxy_selections"); n != 1 {
		t.Fatalf("proxy_selections = %d, want untouched (1)", n)
	}
}

// 有激活 profile 时写入并覆盖同一 group 的旧选择。
func TestSaveProxySelection(t *testing.T) {
	path := seedWritableStore(t, "")
	e := New()
	e.SetDatabasePath(path)
	e.SetProfileID(profileA)

	if err := e.SaveProxySelection("GLOBAL", "node-b"); err != nil {
		t.Fatalf("SaveProxySelection: %v", err)
	}
	got, err := e.LoadProxySelections()
	if err != nil {
		t.Fatalf("LoadProxySelections: %v", err)
	}
	if len(got) != 1 || got[0].GroupName != "GLOBAL" || got[0].ProxyName != "node-b" {
		t.Fatalf("selections = %+v", got)
	}
	// 同一个 group 再选一次是 upsert，不是插入第二行。
	if err := e.SaveProxySelection("GLOBAL", "node-c"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, path, "proxy_selections"); n != 1 {
		t.Fatalf("proxy_selections = %d, want 1 (upsert)", n)
	}
	got, _ = e.LoadProxySelections()
	if got[0].ProxyName != "node-c" {
		t.Fatalf("selection = %+v", got)
	}
}

// profile 不在库里时，写选择应静默跳过（外键会拒绝，不该冒泡成 500）。
func TestSaveProxySelectionProfileMissing(t *testing.T) {
	path := seedWritableStore(t, "")
	e := New()
	e.SetDatabasePath(path)
	e.SetProfileID("99999999-9999-4999-8999-999999999999")

	if err := e.SaveProxySelection("GLOBAL", "node-b"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := countRows(t, path, "proxy_selections"); n != 1 {
		t.Fatalf("proxy_selections = %d", n)
	}
}
