package web

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"tshirt2/internal/auth"
	"tshirt2/internal/db"
)

var ctx = context.Background()

const testPassword = "password123"

var (
	hashOnce sync.Once
	testHash string
)

// scrypt は意図的に重いので、テスト全体で1回だけ作って使い回す。
func passwordHash(t *testing.T) string {
	hashOnce.Do(func() {
		var err error
		if testHash, err = auth.Hash(testPassword); err != nil {
			panic(err)
		}
	})
	return testHash
}

type harness struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	path   string
	d      *db.DB // テストが直接 DB を操作する (サーバーとは別の接続)
	mu     sync.Mutex
	offset time.Duration
	logs   *strings.Builder
}

func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Now().Add(h.offset)
}

// advance は、サーバーの時計を進める (有効期限のテスト用)。
func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.offset += d
	h.mu.Unlock()
}

type lockedWriter struct {
	mu sync.Mutex
	b  *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// newHarness は、マイグレーション済みの DB (admin / staff の2人、パスワードは testPassword) と、
// テスト用のルートを登録した Server を作る。
func newHarness(t *testing.T, tweak ...func(*Config)) *harness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inventory.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ name, role string }{{"admin", "admin"}, {"staff", "staff"}} {
		if _, err := d.ExecContext(ctx, `INSERT INTO "user" (username, password_hash, role) VALUES (?, ?, ?)`,
			u.name, passwordHash(t), u.role); err != nil {
			t.Fatal(err)
		}
	}
	return newHarnessAt(t, path, d, tweak...)
}

func newHarnessAt(t *testing.T, path string, d *db.DB, tweak ...func(*Config)) *harness {
	t.Helper()
	h := &harness{t: t, path: path, d: d, logs: &strings.Builder{}}
	cfg := Config{
		DatabasePath: path, SecretKey: []byte("test-secret-key"),
		Logger: log.New(&lockedWriter{b: h.logs}, "", 0), Now: h.now,
	}
	for _, f := range tweak {
		f(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.srv = srv
	h.registerTestRoutes()
	h.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(func() { h.ts.Close(); srv.Close() })
	return h
}

// registerTestRoutes は、テスト専用の補助ルート (/test/*)。各画面の実ルートは routes() が登録する。
func (h *harness) registerTestRoutes() {
	s := h.srv
	s.Page("POST /test/post", func(w http.ResponseWriter, r *http.Request) error {
		io.WriteString(w, "posted:"+r.PostFormValue("x"))
		return nil
	})
	s.Page("GET /test/whoami", func(w http.ResponseWriter, r *http.Request) error {
		io.WriteString(w, CurrentUser(r).Username)
		return nil
	})
	s.Public("GET /test/flash", func(w http.ResponseWriter, r *http.Request) error {
		Flash(r, "info", r.URL.Query().Get("m"))
		Flash(r, "success", "二つ目")
		return s.Render(w, r, 200, "dashboard_stub.html", nil)
	})
	s.Public("GET /test/flash-redirect", func(w http.ResponseWriter, r *http.Request) error {
		Flash(r, "success", "保存しました。")
		http.Redirect(w, r, "/login", http.StatusFound)
		return nil
	})
	s.Public("GET /test/panic", func(w http.ResponseWriter, r *http.Request) error { panic("secret-internal-detail") })
	s.Public("GET /test/busy", func(w http.ResponseWriter, r *http.Request) error { return errors.New("database is locked") })
	s.Public("GET /test/err", func(w http.ResponseWriter, r *http.Request) error { return errors.New("boom: secret-detail") })
}

// ---- クライアント ----------------------------------------------------------------

type client struct {
	h   *harness
	c   *http.Client
	jar *cookiejar.Jar
}

type resp struct {
	status int
	header http.Header
	body   string
}

func (r resp) location() string { return r.header.Get("Location") }

func (h *harness) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{h: h, jar: jar, c: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       30 * time.Second,
	}}
}

func (c *client) do(req *http.Request) resp {
	c.h.t.Helper()
	res, err := c.c.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, string(b)}
}

func (c *client) get(path string) resp {
	c.h.t.Helper()
	req, _ := http.NewRequest("GET", c.h.ts.URL+path, nil)
	return c.do(req)
}

func (c *client) post(path string, form url.Values, headers ...string) resp {
	c.h.t.Helper()
	req, _ := http.NewRequest("POST", c.h.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return c.do(req)
}

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// token は、ログイン画面を開いて、CSRF トークンを取り出す (未ログインでも、セッションが作られる)。
func (c *client) token(path string) string {
	c.h.t.Helper()
	r := c.get(path)
	m := csrfRe.FindStringSubmatch(r.body)
	if m == nil {
		c.h.t.Fatalf("no csrf token in %s (status %d): %.200s", path, r.status, r.body)
	}
	return m[1]
}

// postCSRF は、トークンを取ってから POST する。
func (c *client) postCSRF(tokenPage, path string, form url.Values) resp {
	c.h.t.Helper()
	f := url.Values{}
	for k, v := range form {
		f[k] = v
	}
	f.Set("csrf_token", c.token(tokenPage))
	return c.post(path, f)
}

func (c *client) login(user, pw string, remember bool) resp {
	c.h.t.Helper()
	f := url.Values{"username": {user}, "password": {pw}}
	if remember {
		f.Set("remember", "on")
	}
	return c.postCSRF("/login", "/login", f)
}

func (c *client) mustLogin(user string, remember bool) {
	c.h.t.Helper()
	if r := c.login(user, testPassword, remember); r.status != 302 || r.location() != "/" {
		c.h.t.Fatalf("login %s failed: %d %s %.200s", user, r.status, r.location(), r.body)
	}
}

func (c *client) cookie(name string) *http.Cookie {
	u, _ := url.Parse(c.h.ts.URL)
	for _, ck := range c.jar.Cookies(u) {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func (c *client) setCookie(name, value string) {
	u, _ := url.Parse(c.h.ts.URL)
	c.jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Path: "/"}})
}

func (c *client) dropCookie(name string) {
	u, _ := url.Parse(c.h.ts.URL)
	c.jar.SetCookies(u, []*http.Cookie{{Name: name, Value: "", Path: "/", MaxAge: -1}})
}

// setCookies は、レスポンスの Set-Cookie を、名前で引ける形にする。
func setCookies(r resp) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, ck := range (&http.Response{Header: r.header}).Cookies() {
		out[ck.Name] = ck
	}
	return out
}
