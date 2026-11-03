package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tshirt2/internal/db"
)

// ---- エラーページ ---------------------------------------------------------------------

func TestErrorPagesUseFlask版Messages(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	r := c.get("/no-such-page") // 未ログインでも 404 (ログイン画面へは移動しない)
	if r.status != 404 {
		t.Errorf("anonymous unknown path: %d, want 404", r.status)
	}
	contains(t, r, "ページが見つかりません。")
	c.mustLogin("admin", false)
	r = c.get("/change-me-not-found")
	if r.status != 404 {
		t.Errorf("%d", r.status)
	}
	contains(t, r, "ページが見つかりません。")
	contains(t, r, `href="/admin/users"`) // エラーページにも、通常のナビが付く
}

func TestPanicBecomes500WithoutLeakingDetails(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	r := c.get("/test/panic")
	if r.status != 500 {
		t.Fatalf("%d", r.status)
	}
	contains(t, r, "エラーが起きました。管理者に連絡してください。")
	notContains(t, r, "secret-internal-detail")
	if !strings.Contains(h.logs.String(), "secret-internal-detail") {
		t.Error("the panic must be logged for the administrator")
	}
	if r := c.get("/login"); r.status != 200 { // その後も、サーバーは動き続ける
		t.Errorf("server unhealthy after panic: %d", r.status)
	}
}

func TestBusyDatabaseIs503AndOtherErrorsAre500(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	r := c.get("/test/busy")
	if r.status != 503 || r.header.Get("Retry-After") == "" {
		t.Errorf("busy: %d Retry-After=%q", r.status, r.header.Get("Retry-After"))
	}
	contains(t, r, "ただいま混み合っています。数秒おいて、もう一度お試しください。")
	r = c.get("/test/err")
	if r.status != 500 {
		t.Errorf("err: %d", r.status)
	}
	contains(t, r, "エラーが起きました。")
	notContains(t, r, "secret-detail")
}

// ---- no-store と静的ファイル -----------------------------------------------------------

func TestNoStoreForPagesAndRedirectsButNotStatic(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	for name, r := range map[string]resp{
		"login page": c.get("/login"), "redirect": c.get("/blank"), "404": c.get("/nope"),
	} {
		if got := r.header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, got)
		}
	}
	if got := c.get("/static/app.css").header.Get("Cache-Control"); got == "no-store" {
		t.Errorf("static must be cacheable with revalidation, got %q", got)
	}
}

func TestStaticFilesETagAndNotModified(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	r := c.get("/static/app.css")
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/css") {
		t.Fatalf("css: %d %q", r.status, r.header.Get("Content-Type"))
	}
	etag := r.header.Get("ETag")
	if etag == "" || r.header.Get("Cache-Control") != "no-cache" {
		t.Errorf("ETag=%q Cache-Control=%q", etag, r.header.Get("Cache-Control"))
	}
	if len(r.header.Values("Set-Cookie")) != 0 {
		t.Error("static files must not set cookies")
	}
	req, _ := http.NewRequest("GET", h.ts.URL+"/static/app.css", nil)
	req.Header.Set("If-None-Match", etag)
	if r2 := c.do(req); r2.status != 304 || r2.body != "" {
		t.Errorf("conditional GET: %d, body %d bytes", r2.status, len(r2.body))
	}
	if r := c.get("/static/vendor/bootstrap.bundle.min.js"); r.status != 200 || !strings.Contains(r.header.Get("Content-Type"), "javascript") {
		t.Errorf("js: %d %q", r.status, r.header.Get("Content-Type"))
	}
	// "." / ".." を含むパスは、ServeMux の正規化リダイレクト (Go の版で 301/307 が変わる) に
	// 頼らず、rejectDotSegments がその場で 404 を返す (Flask 版と同じ見え方)。
	for _, p := range []string{"/static/missing.css", "/static/../go.mod", "/static/%2e%2e/templates/base.html", "/static/", "/static/./app.css", "/static/%2e/app.css"} {
		if r := c.get(p); r.status != 404 {
			t.Errorf("%s: %d, want 404", p, r.status)
		}
	}
	contains(t, c.get("/static/missing.css"), "ページが見つかりません。")
}

func TestPagesReferenceExistingStaticFiles(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	body := c.get("/login").body
	for _, p := range []string{"/static/vendor/bootstrap.min.css", "/static/app.css", "/static/vendor/bootstrap.bundle.min.js", "/static/app.js"} {
		if !strings.Contains(body, p) {
			t.Errorf("page does not reference %s", p)
		}
		if r := c.get(p); r.status != 200 {
			t.Errorf("%s: %d", p, r.status)
		}
	}
}

// ---- DB が未作成・古い・他の版のとき (Flask 版 tests/test_robustness.py の 503 の部分) ------------

func TestMissingDatabaseGives503AndCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.db")
	h := newHarnessAt(t, path, nil)
	c := h.client()
	r := c.get("/login")
	if r.status != 503 || r.header.Get("Retry-After") != "60" {
		t.Fatalf("%d Retry-After=%q", r.status, r.header.Get("Retry-After"))
	}
	contains(t, r, "データベースの更新が必要です")
	contains(t, r, "管理者に連絡してください。")
	if got := r.header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	// POST も、CSRF エラーではなく 503 の案内になる
	if r := c.post("/login", url.Values{"username": {"a"}}); r.status != 503 {
		t.Errorf("POST: %d, want 503", r.status)
	}
	if r := c.get("/static/app.css"); r.status != 200 { // 静的ファイルは配れる
		t.Errorf("static while DB missing: %d", r.status)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a missing database was created by a request (stat err = %v)", err)
	}
	if n := strings.Count(h.logs.String(), "DBが未作成か"); n != 1 {
		t.Errorf("warning logged %d times, want once", n)
	}
}

func TestOutdatedDatabaseRecoversWithoutRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.ExecContext(ctx, db.Migrations[0])
	d.ExecContext(ctx, "PRAGMA user_version=1")
	h := newHarnessAt(t, path, d)
	c := h.client()
	if r := c.get("/login"); r.status != 503 {
		t.Fatalf("outdated: %d, want 503", r.status)
	}
	if _, err := db.Migrate(ctx, d); err != nil { // 別のプロセスで init-db を実行した状態
		t.Fatal(err)
	}
	if r := c.get("/login"); r.status != 200 {
		t.Errorf("after migration: %d, want 200 (再起動なしで復旧)", r.status)
	}
}

func TestSQLAlchemyMadeDatabaseGives503WithAnExplanation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sa.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ddl, _ := os.ReadFile("../db/testdata/sqlalchemy_version_schema.sql")
	if _, err := d.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	d.ExecContext(ctx, "INSERT INTO alembic_version VALUES ('afd0c4cd0c7c')")
	h := newHarnessAt(t, path, d)
	r := h.client().get("/login")
	if r.status != 503 {
		t.Fatalf("%d", r.status)
	}
	contains(t, r, "SQLAlchemy")
	var v int
	d.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	if v != 0 {
		t.Errorf("user_version = %d: a foreign database was modified", v)
	}
}

func TestUnreadableDatabaseFileGives503AndIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	content := []byte(strings.Repeat("not a database ", 500))
	os.WriteFile(path, content, 0o644)
	h := newHarnessAt(t, path, nil)
	r := h.client().get("/login")
	if r.status != 503 {
		t.Fatalf("%d", r.status)
	}
	contains(t, r, "DBファイルを開けません")
	if after, _ := os.ReadFile(path); string(after) != string(content) {
		t.Error("garbage file was modified")
	}
}

func TestNewRequiresASecretKey(t *testing.T) {
	if _, err := New(Config{DatabasePath: "x"}); err == nil {
		t.Error("New without SecretKey must fail")
	}
}
