package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"tshirt2/internal/auth"
	"tshirt2/internal/service"
)

func contains(t *testing.T, r resp, want string) {
	t.Helper()
	if !strings.Contains(r.body, want) {
		t.Errorf("body does not contain %q:\n%.400s", want, r.body)
	}
}

func notContains(t *testing.T, r resp, bad string) {
	t.Helper()
	if strings.Contains(r.body, bad) {
		t.Errorf("body must not contain %q:\n%.400s", bad, r.body)
	}
}

// ---- Flask 版 tests/test_web.py のうち、認証と権限の部分 ----------------------------------

func TestLoginRequiredRedirects(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	for _, p := range []string{"/", "/blank", "/printed", "/receive", "/convert", "/move", "/history", "/admin/users"} {
		r := c.get(p)
		if r.status != 302 || !strings.HasPrefix(r.location(), "/login") {
			t.Errorf("%s: %d %s, want 302 to /login", p, r.status, r.location())
		}
	}
}

// Flask 版の実測: /blank/list?category=1&q=a%20b → /login?next=%2Fblank%2Flist%3Fcategory%3D1%26q%3Da%2520b
func TestUnauthenticatedRedirectKeepsTheWholeURLInNext(t *testing.T) {
	h := newHarness(t)
	r := h.client().get("/blank?category=1&q=a%20b")
	if want := "/login?next=%2Fblank%3Fcategory%3D1%26q%3Da%2520b"; r.location() != want {
		t.Errorf("Location = %q, want %q", r.location(), want)
	}
}

func TestLoginRequiredMessageIsShownOnceOnTheLoginPage(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.get("/blank")
	first := c.get("/login")
	contains(t, first, "ログインしてください。")
	contains(t, first, "alert-warning")
	notContains(t, c.get("/login"), "ログインしてください。") // 表示したら消える
}

func TestLoginLogoutAndOpenRedirectBlocked(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	r := c.postCSRF("/login", "/login?next=https://evil.example/", url.Values{"username": {"admin"}, "password": {testPassword}})
	if r.status != 302 || r.location() != "/" {
		t.Fatalf("login with external next: %d %q, want 302 /", r.status, r.location())
	}
	if r := c.postCSRF("/", "/logout", nil); r.status != 302 || r.location() != "/login" {
		t.Errorf("logout: %d %q", r.status, r.location())
	}
	r = c.login("admin", "wrong", false)
	if r.status != 200 {
		t.Fatalf("wrong password: %d", r.status)
	}
	contains(t, r, "違います")
	contains(t, r, "alert-danger")
}

func TestSafeNextIsFollowedAfterLogin(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	r := c.postCSRF("/login", "/login?next=%2Fblank%3Fcategory%3D1", url.Values{"username": {"admin"}, "password": {testPassword}})
	if r.status != 302 || r.location() != "/blank?category=1" {
		t.Errorf("%d %q, want redirect to /blank?category=1", r.status, r.location())
	}
	for _, evil := range []string{"//evil.example", "https://evil.example", "/\t/evil", "/\n/evil", "javascript:alert(1)", "evil"} {
		c2 := h.client()
		r := c2.postCSRF("/login", "/login?next="+url.QueryEscape(evil), url.Values{"username": {"admin"}, "password": {testPassword}})
		if r.status != 302 || r.location() != "/" {
			t.Errorf("next=%q -> %d %q, want /", evil, r.status, r.location())
		}
	}
}

func TestDisabledUserCannotLogin(t *testing.T) {
	h := newHarness(t)
	h.d.ExecContext(ctx, `UPDATE "user" SET is_active = 0 WHERE username = 'staff'`)
	r := h.client().login("staff", testPassword, false)
	if r.status != 200 {
		t.Fatalf("status %d, want 200 (ログイン画面のまま)", r.status)
	}
	contains(t, r, "違います")
}

func TestUnknownUserAndWrongPasswordLookTheSame(t *testing.T) {
	h := newHarness(t)
	a := h.client().login("nobody", "x", false)
	b := h.client().login("admin", "x", false)
	if a.status != b.status || a.status != 200 {
		t.Fatalf("statuses %d / %d", a.status, b.status)
	}
	contains(t, a, "ユーザー名またはパスワードが違います。")
	contains(t, b, "ユーザー名またはパスワードが違います。")
}

func TestUsernameWhitespaceIsStrippedAtLogin(t *testing.T) {
	h := newHarness(t)
	if r := h.client().login(" admin\u3000", testPassword, false); r.status != 302 {
		t.Errorf("status %d, want 302", r.status)
	}
}

func TestLoggedInUserVisitingLoginGoesHome(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.mustLogin("admin", false)
	if r := c.get("/login"); r.status != 302 || r.location() != "/" {
		t.Errorf("%d %q", r.status, r.location())
	}
}

func TestStaffCannotAccessAdminButAdminCan(t *testing.T) {
	h := newHarness(t)
	staff := h.client()
	staff.mustLogin("staff", false)
	for _, p := range []string{"/admin/users", "/admin/items"} {
		r := staff.get(p)
		if r.status != 403 {
			t.Errorf("staff %s: %d, want 403", p, r.status)
		}
		contains(t, r, "この操作をする権限がありません。")
	}
	if r := staff.get("/blank"); r.status != 200 {
		t.Errorf("staff /blank: %d", r.status)
	}
	admin := h.client()
	admin.mustLogin("admin", false)
	for _, p := range []string{"/admin/users", "/admin/items", "/blank"} {
		if r := admin.get(p); r.status != 200 {
			t.Errorf("admin %s: %d", p, r.status)
		}
	}
	if r := h.client().get("/admin/users"); r.status != 302 { // 未ログインは 403 ではなくログイン画面へ
		t.Errorf("anonymous /admin/users: %d, want 302", r.status)
	}
}

func TestNavigationShowsAdminLinksOnlyToAdmins(t *testing.T) {
	h := newHarness(t)
	a, s := h.client(), h.client()
	a.mustLogin("admin", false)
	s.mustLogin("staff", false)
	ra, rs := a.get("/blank"), s.get("/blank")
	contains(t, ra, `href="/admin/users"`)
	notContains(t, rs, `href="/admin/users"`)
	contains(t, rs, `href="/receive"`)
	contains(t, rs, ">staff</a>") // ユーザー名 (パスワード変更へのリンク)
	if h.client().get("/login").body == "" {
		t.Error("empty login page")
	}
	notContains(t, h.client().get("/login"), `href="/receive"`) // 未ログインにナビは出ない
}

func TestUsernameIsHTMLEscapedInNavigation(t *testing.T) {
	h := newHarness(t)
	h.d.ExecContext(ctx, `INSERT INTO "user" (username, password_hash, role) VALUES (?, ?, 'staff')`, "<i>x</i>", passwordHash(t))
	c := h.client()
	c.mustLogin("<i>x</i>", false)
	r := c.get("/blank")
	contains(t, r, "&lt;i&gt;x&lt;/i&gt;")
	notContains(t, r, "<i>x</i>")
}

// ---- セッション Cookie と「ログインを保持する」 ---------------------------------------------

func TestSessionCookieAttributes(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	tok := c.token("/login")
	r := c.post("/login", url.Values{"username": {"admin"}, "password": {testPassword}, "csrf_token": {tok}})
	ck := setCookies(r)["session"]
	if ck == nil {
		t.Fatal("no session cookie")
	}
	if !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" || ck.Secure {
		t.Errorf("attributes: %+v", ck)
	}
	if ck.MaxAge != 0 || !ck.Expires.IsZero() { // ブラウザを閉じるまで (Flask 版と同じ)
		t.Errorf("session cookie must not be persistent: MaxAge=%d Expires=%v", ck.MaxAge, ck.Expires)
	}
	if _, ok := setCookies(r)["remember"]; ok {
		t.Error("remember cookie set without the checkbox")
	}
	if strings.Contains(ck.Value, "password") || strings.Contains(ck.Value, testPassword) {
		t.Error("session cookie leaks data")
	}
}

func TestCookiesAreSecureWhenConfigured(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.CookieSecure = true })
	c := h.client()
	r := c.post("/login", url.Values{"username": {"admin"}, "password": {testPassword}, "remember": {"on"},
		"csrf_token": {c.token("/login")}})
	for name, ck := range setCookies(r) {
		if !ck.Secure || !ck.HttpOnly {
			t.Errorf("%s: Secure=%v HttpOnly=%v, want both", name, ck.Secure, ck.HttpOnly)
		}
	}
}

func TestRememberCookie(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	r := c.post("/login", url.Values{"username": {"admin"}, "password": {testPassword}, "remember": {"on"},
		"csrf_token": {c.token("/login")}})
	rem := setCookies(r)["remember"]
	if rem == nil {
		t.Fatal("no remember cookie")
	}
	if want := 365 * 24 * 3600; rem.MaxAge != want { // Flask-Login の既定
		t.Errorf("MaxAge = %d, want %d", rem.MaxAge, want)
	}
	if !rem.HttpOnly || rem.Path != "/" {
		t.Errorf("attributes: %+v", rem)
	}
}

func TestRememberRestoresTheSessionAfterTheSessionCookieIsLost(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.mustLogin("admin", true)
	c.dropCookie("session") // ブラウザを閉じた状態
	r := c.get("/test/whoami")
	if r.status != 200 || r.body != "admin" {
		t.Fatalf("%d %q, want restored admin session", r.status, r.body)
	}
	if setCookies(r)["session"] == nil {
		t.Error("session cookie was not re-issued after restoring from remember")
	}
	// ログイン保持なしだと、セッション Cookie を失うとログアウト状態
	c2 := h.client()
	c2.mustLogin("admin", false)
	c2.dropCookie("session")
	if r := c2.get("/blank"); r.status != 302 {
		t.Errorf("without remember: %d, want 302", r.status)
	}
}

func TestSessionExpiresAfterFourteenDaysButRememberOutlivesIt(t *testing.T) {
	h := newHarness(t)
	plain, kept := h.client(), h.client()
	plain.mustLogin("admin", false)
	kept.mustLogin("admin", true)
	h.advance(13*24*time.Hour + 23*time.Hour)
	if r := plain.get("/blank"); r.status != 200 {
		t.Errorf("before 14 days: %d, want 200", r.status)
	}
	h.advance(2 * time.Hour) // 14日を超えた
	if r := plain.get("/blank"); r.status != 302 {
		t.Errorf("after 14 days (no remember): %d, want 302", r.status)
	}
	if r := kept.get("/blank"); r.status != 200 {
		t.Errorf("after 14 days (remember): %d, want 200 (remember から復元)", r.status)
	}
	h.advance(352 * 24 * time.Hour) // 合計で365日を超えた
	if r := kept.get("/blank"); r.status != 302 {
		t.Errorf("after 365 days: %d, want 302", r.status)
	}
}

func TestLogoutClearsTheCookies(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.mustLogin("admin", true)
	r := c.postCSRF("/blank", "/logout", nil)
	cs := setCookies(r)
	for _, name := range []string{"session", "remember"} {
		if ck := cs[name]; ck == nil || ck.MaxAge >= 0 {
			t.Errorf("%s cookie not cleared: %+v", name, ck)
		}
	}
	if r := c.get("/blank"); r.status != 302 {
		t.Errorf("after logout: %d, want 302", r.status)
	}
}

func TestDisabledUsersExistingSessionIsRejected(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.mustLogin("staff", true)
	if r := c.get("/blank"); r.status != 200 {
		t.Fatal(r.status)
	}
	h.d.ExecContext(ctx, `UPDATE "user" SET is_active = 0 WHERE username = 'staff'`)
	r := c.get("/blank")
	if r.status != 302 || !strings.HasPrefix(r.location(), "/login") { // Flask 版の実測と同じ
		t.Errorf("%d %q, want redirect to login", r.status, r.location())
	}
	if ck := setCookies(r)["remember"]; ck == nil || ck.MaxAge >= 0 {
		t.Errorf("stale remember cookie not cleared: %+v", ck)
	}
	h.d.ExecContext(ctx, `UPDATE "user" SET is_active = 1 WHERE username = 'staff'`)
	c.mustLogin("staff", false) // 有効に戻せば、またログインできる
}

func TestPasswordChangeInvalidatesOtherSessionsButNotTheOwn(t *testing.T) {
	h := newHarness(t)
	a, b, bKept := h.client(), h.client(), h.client()
	a.mustLogin("staff", false)
	b.mustLogin("staff", false)
	bKept.mustLogin("staff", true)
	r := a.postCSRF("/account/password", "/account/password", url.Values{
		"old": {testPassword}, "new": {"newpassword1"}, "confirm": {"newpassword1"}})
	if r.status != 302 || r.location() != "/" {
		t.Fatalf("change: %d %q %.200s", r.status, r.location(), r.body)
	}
	if r := a.get("/blank"); r.status != 200 {
		t.Errorf("own session after change: %d, want 200", r.status)
	}
	if r := b.get("/blank"); r.status != 302 {
		t.Errorf("other session: %d, want 302 (無効)", r.status)
	}
	if r := bKept.get("/blank"); r.status != 302 { // remember Cookie も、古いトークンなので復元されない
		t.Errorf("other session with remember: %d, want 302", r.status)
	}
}

func TestOwnRememberCookieIsRefreshedAfterPasswordChange(t *testing.T) {
	h := newHarness(t)
	a := h.client()
	a.mustLogin("staff", true)
	a.postCSRF("/account/password", "/account/password", url.Values{
		"old": {testPassword}, "new": {"newpassword1"}, "confirm": {"newpassword1"}})
	a.dropCookie("session")
	if r := a.get("/test/whoami"); r.status != 200 || r.body != "staff" {
		t.Errorf("remember after password change: %d %q, want still logged in", r.status, r.body)
	}
}

func TestTamperedAndForeignCookiesAreIgnored(t *testing.T) {
	h := newHarness(t)
	good := h.client()
	good.mustLogin("admin", true)
	sess, rem := good.cookie("session").Value, good.cookie("remember").Value

	other := newHarness(t, func(c *Config) { c.SecretKey = []byte("a-different-secret") })
	foreign := other.client()
	foreign.mustLogin("admin", true)
	fSess, fRem := foreign.cookie("session").Value, foreign.cookie("remember").Value

	cases := map[string][2]string{ // 名前: {session, remember}
		"空":                {"", ""},
		"ゴミ":               {"garbage", "garbage"},
		"区切りだけ":            {".", "."},
		"末尾を欠く":            {sess[:len(sess)-3], rem[:len(rem)-3]},
		"署名を1文字変える":        {flip(sess), flip(rem)},
		"別の鍵で作った正しい形式":     {fSess, fRem},
		"remember をセッションに": {rem, sess},
		"flash の鍵では通らない":   {h.srv.flashCodec.encode(sessionData{UID: 1, IAT: time.Now().Unix()}), h.srv.flashCodec.encode(rememberData{UID: 1, IAT: time.Now().Unix()})},
		"ログイン中のIDを書き換え":    {tamperPayload(sess), tamperPayload(rem)},
	}
	for name, v := range cases {
		c := h.client()
		c.get("/login") // CSRF 用のセッションを作っておく
		c.setCookie("session", v[0])
		c.setCookie("remember", v[1])
		if r := c.get("/blank"); r.status != 302 {
			t.Errorf("%s: %d, want 302 (未ログイン扱い。500 にならないこと)", name, r.status)
		}
	}
	if strings.Contains(h.logs.String(), "panic") {
		t.Errorf("a bad cookie caused a panic:\n%s", h.logs.String())
	}
}

func flip(s string) string {
	b := []byte(s)
	i := len(b) - 2
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}

// tamperPayload は、署名はそのままで、中身 (JSON) を書き換える。
func tamperPayload(v string) string {
	i := strings.IndexByte(v, '.')
	return strings.Replace(v[:i], v[:3], "eyJ", 1) + "AA" + v[i:]
}

// ---- CSRF -------------------------------------------------------------------------------

const csrfMessage = "画面の有効期限が切れたか、不正な送信です。ページを開き直して、もう一度お試しください。"

func TestPostWithoutCSRFTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	for name, form := range map[string]url.Values{
		"トークンなし":  {"username": {"admin"}, "password": {testPassword}},
		"空のトークン":  {"username": {"admin"}, "password": {testPassword}, "csrf_token": {""}},
		"でたらめ":    {"username": {"admin"}, "password": {testPassword}, "csrf_token": {"abc"}},
		"形式だけ正しい": {"username": {"admin"}, "password": {testPassword}, "csrf_token": {"1700000000.AAAA"}},
	} {
		r := c.post("/login", form)
		if r.status != 400 {
			t.Errorf("%s: %d, want 400", name, r.status)
		}
		contains(t, r, csrfMessage) // Flask 版の実測と同じ文言
	}
	if r := c.get("/blank"); r.status != 302 {
		t.Errorf("a rejected login must not log in: %d", r.status)
	}
}

func TestCSRFFailureDoesNotChangeState(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.mustLogin("admin", false)
	if r := c.post("/logout", url.Values{}); r.status != 400 {
		t.Errorf("logout without token: %d, want 400", r.status)
	}
	if r := c.get("/blank"); r.status != 200 {
		t.Errorf("still logged in expected, got %d", r.status)
	}
	if r := c.post("/test/post", url.Values{"x": {"1"}}); r.status != 400 {
		t.Errorf("post without token: %d", r.status)
	}
}

func TestCSRFTokenIsBoundToTheSession(t *testing.T) {
	h := newHarness(t)
	a, b := h.client(), h.client()
	tokA := a.token("/login")
	b.token("/login")
	form := url.Values{"x": {"1"}, "csrf_token": {tokA}}
	a.mustLogin("admin", false)
	b.mustLogin("admin", false)
	tokA = a.token("/blank")
	if r := a.post("/test/post", url.Values{"x": {"1"}, "csrf_token": {tokA}}); r.status != 200 || r.body != "posted:1" {
		t.Errorf("own token: %d %q", r.status, r.body)
	}
	form.Set("csrf_token", tokA)
	if r := b.post("/test/post", form); r.status != 400 {
		t.Errorf("another session's token: %d, want 400", r.status)
	}
}

func TestCSRFHeaderIsAccepted(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.mustLogin("admin", false)
	tok := c.token("/blank")
	if r := c.post("/test/post", url.Values{"x": {"h"}}, "X-CSRFToken", tok); r.status != 200 || r.body != "posted:h" {
		t.Errorf("%d %q", r.status, r.body)
	}
	if r := c.post("/test/post", url.Values{"x": {"h"}}, "X-CSRFToken", "bad"); r.status != 400 {
		t.Errorf("bad header token: %d", r.status)
	}
}

func TestCSRFTokenExpiresAfterOneHour(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.mustLogin("admin", false)
	tok := c.token("/blank")
	h.advance(59 * time.Minute)
	if r := c.post("/test/post", url.Values{"x": {"1"}, "csrf_token": {tok}}); r.status != 200 {
		t.Errorf("59 min: %d, want 200", r.status)
	}
	h.advance(2 * time.Minute)
	r := c.post("/test/post", url.Values{"x": {"1"}, "csrf_token": {tok}})
	if r.status != 400 {
		t.Errorf("61 min: %d, want 400", r.status)
	}
	contains(t, r, csrfMessage)
	// ページを開き直せば、また送れる
	if r := c.post("/test/post", url.Values{"x": {"1"}, "csrf_token": {c.token("/blank")}}); r.status != 200 {
		t.Errorf("after reload: %d", r.status)
	}
}

func TestOversizedFormIsRejected(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.mustLogin("admin", false)
	big := url.Values{"x": {strings.Repeat("a", 2<<20)}, "csrf_token": {c.token("/blank")}}
	if r := c.post("/test/post", big); r.status != 400 {
		t.Errorf("2MiB form: %d, want 400", r.status)
	}
}

// ---- flash -------------------------------------------------------------------------------

func TestFlashIsEscapedShownOnceAndInOrder(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	r := c.get("/test/flash?m=" + url.QueryEscape("<script>alert(1)</script>"))
	contains(t, r, "&lt;script&gt;alert(1)&lt;/script&gt;")
	notContains(t, r, "<script>alert(1)")
	if i, j := strings.Index(r.body, "alert-info"), strings.Index(r.body, "alert-success"); i < 0 || j < 0 || i > j {
		t.Errorf("flash order wrong (info at %d, success at %d)", i, j)
	}
	notContains(t, c.get("/test/flash?m=x2"), "&lt;script")
}

func TestFlashSurvivesARedirect(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if r := c.get("/test/flash-redirect"); r.status != 302 {
		t.Fatal(r.status)
	}
	contains(t, c.get("/login"), "保存しました。")
	notContains(t, c.get("/login"), "保存しました。")
}

func TestTamperedFlashCookieIsIgnored(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.setCookie("flash", "eyJjIjoiZGFuZ2VyIn0.xxxx")
	if r := c.get("/login"); r.status != 200 {
		t.Errorf("status %d", r.status)
	} else {
		notContains(t, r, "alert-danger")
	}
}

// ---- パスワード変更 (Flask 版 auth.change_password) --------------------------------------

func TestChangePasswordRules(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.mustLogin("staff", false)
	try := func(old, nw, conf string) resp {
		return c.postCSRF("/account/password", "/account/password", url.Values{"old": {old}, "new": {nw}, "confirm": {conf}})
	}
	for _, tc := range []struct{ old, nw, conf, msg string }{
		{"wrong", "newpassword1", "newpassword1", service.MsgOldPasswordWrong},
		{testPassword, "short", "short", service.MsgPasswordTooShort},
		{testPassword, "1234567", "1234567", service.MsgPasswordTooShort},
		{testPassword, "あいうえおかき", "あいうえおかき", service.MsgPasswordTooShort}, // 7文字 (21バイト)
		{testPassword, "newpassword1", "newpassword2", service.MsgConfirmMismatch},
	} {
		r := try(tc.old, tc.nw, tc.conf)
		if r.status != 200 {
			t.Errorf("%q/%q: status %d, want 200", tc.old, tc.nw, r.status)
		}
		contains(t, r, tc.msg)
		contains(t, r, "alert-danger")
	}
	var hash string
	h.d.QueryRowContext(ctx, `SELECT password_hash FROM "user" WHERE username='staff'`).Scan(&hash)
	if hash != passwordHash(t) {
		t.Error("password changed by a rejected request")
	}
	// 8文字ちょうど (日本語は文字数で数える) は通る
	r := try(testPassword, "あいうえおかきく", "あいうえおかきく")
	if r.status != 302 || r.location() != "/" {
		t.Fatalf("%d %q %.200s", r.status, r.location(), r.body)
	}
	contains(t, c.get("/"), "パスワードを変更しました。")
	h.d.QueryRowContext(ctx, `SELECT password_hash FROM "user" WHERE username='staff'`).Scan(&hash)
	if !strings.HasPrefix(hash, "scrypt:32768:8:1$") { // Flask 版 (Werkzeug) でも読める形式
		t.Errorf("hash format = %.30s", hash)
	}
	if ok, err := auth.Verify(hash, "あいうえおかきく"); err != nil || !ok {
		t.Errorf("new password does not verify: %v %v", ok, err)
	}
	if r := h.client().login("staff", testPassword, false); r.status != 200 {
		t.Errorf("old password still works: %d", r.status)
	}
	if r := h.client().login("staff", "あいうえおかきく", false); r.status != 302 {
		t.Errorf("new password rejected: %d", r.status)
	}
}

func TestChangePasswordRequiresLogin(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if r := c.get("/account/password"); r.status != 302 {
		t.Errorf("GET: %d", r.status)
	}
	f := url.Values{"old": {testPassword}, "new": {"newpassword1"}, "confirm": {"newpassword1"}, "csrf_token": {c.token("/login")}}
	if r := c.post("/account/password", f); r.status != 302 || !strings.HasPrefix(r.location(), "/login") {
		t.Errorf("POST: %d %q", r.status, r.location())
	}
}
