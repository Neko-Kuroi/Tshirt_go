package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"tshirt2/internal/repo"
)

const (
	cookieSession  = "session"
	cookieRemember = "remember"
	cookieFlash    = "flash"

	maxFlashes     = 5
	maxFlashLength = 300
)

// FlashMessage は、次に表示するメッセージ。Category は Bootstrap の alert の種類 (success / danger / warning / info)。
type FlashMessage struct {
	Category string `json:"c"`
	Message  string `json:"m"`
}

type sessionData struct {
	UID  int64  `json:"u,omitempty"` // ログイン中のユーザー (0 = 未ログイン)
	Tok  string `json:"t,omitempty"` // repo.User.SessionToken。パスワード変更で無効になる
	CSRF string `json:"c,omitempty"` // CSRF トークンの元になる、セッションごとの乱数
	IAT  int64  `json:"i"`           // 作成時刻 (Unix 秒)。SessionLifetime で失効
}

type rememberData struct {
	UID int64  `json:"u"`
	Tok string `json:"t"`
	IAT int64  `json:"i"`
}

type rememberAction int

const (
	rememberKeep rememberAction = iota
	rememberSet
	rememberClear
)

// reqState は 1 リクエストぶんの状態 (セッション、ログイン中のユーザー、flash)。
// 変更は、レスポンスのヘッダーを書く直前に、まとめて Cookie へ反映する (stateWriter)。
type reqState struct {
	s *Server
	r *http.Request

	sess       sessionData
	sessLoaded bool
	sessDirty  bool

	user       *repo.User
	userLoaded bool
	userErr    error

	remember    rememberAction
	rememberVal rememberData

	flashIn    []FlashMessage // Cookie で届いたもの
	flashOut   []FlashMessage // このリクエストで追加したもの
	flashLoad  bool
	flashShown bool // 画面に出した (= 消費した)
}

type ctxKey struct{}

func stateOf(r *http.Request) *reqState {
	st, _ := r.Context().Value(ctxKey{}).(*reqState)
	return st
}

func withState(r *http.Request, st *reqState) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, st))
}

func (st *reqState) now() time.Time { return st.s.cfg.Now() }

// ---- セッション ----------------------------------------------------------------

func (st *reqState) session() *sessionData {
	if st.sessLoaded {
		return &st.sess
	}
	st.sessLoaded = true
	if c, err := st.r.Cookie(cookieSession); err == nil {
		var d sessionData
		if st.s.sessCodec.decode(c.Value, &d) && st.now().Sub(time.Unix(d.IAT, 0)) <= st.s.cfg.SessionLifetime &&
			time.Unix(d.IAT, 0).Before(st.now().Add(time.Minute)) {
			st.sess = d
		}
	}
	return &st.sess
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// csrfSecret は、セッションの CSRF 用の乱数 (無ければ作る。= セッションが作られる)。
func (st *reqState) csrfSecret() string {
	sess := st.session()
	if sess.CSRF == "" {
		sess.CSRF = randomHex(32)
		if sess.IAT == 0 {
			sess.IAT = st.now().Unix()
		}
		st.sessDirty = true
	}
	return sess.CSRF
}

// ---- ログイン中のユーザー ---------------------------------------------------------

func (st *reqState) verifyUser(uid int64, tok string) (*repo.User, error) {
	d := st.s.DB()
	if d == nil || uid == 0 {
		return nil, nil
	}
	u, err := repo.GetUser(st.r.Context(), d, uid)
	if err != nil || u == nil || !u.IsActive { // 無効なユーザーは、ログイン中でも未ログインとして扱う
		return nil, err
	}
	if !hmac.Equal([]byte(tok), []byte(u.SessionToken(st.s.cfg.SecretKey))) {
		return nil, nil
	}
	return u, nil
}

// currentUser はログイン中のユーザー (未ログインなら nil)。
// セッションが無効でも、remember Cookie が有効ならログイン状態を復元する。
func (st *reqState) currentUser() (*repo.User, error) {
	if st.userLoaded {
		return st.user, st.userErr
	}
	st.userLoaded = true
	sess := st.session()
	if sess.UID != 0 {
		u, err := st.verifyUser(sess.UID, sess.Tok)
		if err != nil {
			st.userErr = err
			return nil, err
		}
		if u != nil {
			st.user = u
			return u, nil
		}
		sess.UID, sess.Tok = 0, "" // 無効になったログイン状態は捨てる (CSRF の乱数は残す)
		st.sessDirty = true
	}
	if c, err := st.r.Cookie(cookieRemember); err == nil {
		var d rememberData
		ok := st.s.remCodec.decode(c.Value, &d) && st.now().Sub(time.Unix(d.IAT, 0)) <= st.s.cfg.RememberDuration
		var u *repo.User
		if ok {
			if u, err = st.verifyUser(d.UID, d.Tok); err != nil {
				st.userErr = err
				return nil, err
			}
		}
		if u == nil {
			st.remember = rememberClear // 無効な remember Cookie は消す
			return nil, nil
		}
		st.establish(u)
		st.user = u
		return u, nil
	}
	return nil, nil
}

// establish は、このユーザーでログインしたセッションにする (CSRF の乱数は作り直す)。
func (st *reqState) establish(u *repo.User) {
	st.sess = sessionData{
		UID: u.ID, Tok: u.SessionToken(st.s.cfg.SecretKey), CSRF: randomHex(32), IAT: st.now().Unix(),
	}
	st.sessLoaded, st.sessDirty = true, true
	st.user, st.userLoaded, st.userErr = u, true, nil
}

// login はログイン成功の処理。remember なら、ログイン保持の Cookie も出す。
func (st *reqState) login(u *repo.User, remember bool) {
	st.establish(u)
	if remember {
		st.remember = rememberSet
		st.rememberVal = rememberData{UID: u.ID, Tok: st.sess.Tok, IAT: st.now().Unix()}
	} else {
		st.remember = rememberClear // 前のユーザーの remember Cookie を残さない
	}
}

// refreshToken はパスワード変更後に、このセッション (と remember Cookie) のトークンを更新する。
// 他の端末のセッションは、トークンが合わなくなって無効になる。
func (st *reqState) refreshToken(u *repo.User) {
	hadRemember := false
	if _, err := st.r.Cookie(cookieRemember); err == nil {
		hadRemember = true
	}
	sess := st.session()
	sess.UID, sess.Tok = u.ID, u.SessionToken(st.s.cfg.SecretKey)
	if sess.CSRF == "" {
		sess.CSRF = randomHex(32)
	}
	st.sessDirty = true
	st.user, st.userLoaded = u, true
	if hadRemember {
		st.remember = rememberSet
		st.rememberVal = rememberData{UID: u.ID, Tok: sess.Tok, IAT: st.now().Unix()}
	}
}

func (st *reqState) logout() {
	st.sess = sessionData{}
	st.sessLoaded, st.sessDirty = true, true
	st.user, st.userLoaded, st.userErr = nil, true, nil
	st.remember = rememberClear
}

// ---- flash ----------------------------------------------------------------------

func (st *reqState) loadFlash() {
	if st.flashLoad {
		return
	}
	st.flashLoad = true
	if c, err := st.r.Cookie(cookieFlash); err == nil {
		var fl []FlashMessage
		if st.s.flashCodec.decode(c.Value, &fl) && len(fl) <= maxFlashes {
			st.flashIn = fl
		}
	}
}

func (st *reqState) addFlash(category, message string) {
	st.loadFlash()
	if r := []rune(message); len(r) > maxFlashLength {
		message = string(r[:maxFlashLength])
	}
	st.flashOut = append(st.flashOut, FlashMessage{Category: category, Message: message})
}

// takeFlashes は、表示するメッセージを返す (表示したものは、次のリクエストでは出さない)。
func (st *reqState) takeFlashes() []FlashMessage {
	st.loadFlash()
	st.flashShown = true
	return append(append([]FlashMessage(nil), st.flashIn...), st.flashOut...)
}

// ---- CSRF トークン ---------------------------------------------------------------

func (st *reqState) csrfToken() string {
	return st.s.csrfMake(st.csrfSecret(), st.now())
}

func (st *reqState) csrfValid(token string) bool {
	secret := st.session().CSRF
	if secret == "" || token == "" {
		return false
	}
	return st.s.csrfCheck(secret, token, st.now())
}

// ---- Cookie への反映 -------------------------------------------------------------

func (st *reqState) cookie(name, value string) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: st.s.cfg.CookieSecure}
}

func (st *reqState) deleteCookie(w http.ResponseWriter, name string) {
	c := st.cookie(name, "")
	c.MaxAge = -1
	http.SetCookie(w, c)
}

func (st *reqState) hasCookie(name string) bool {
	_, err := st.r.Cookie(name)
	return err == nil
}

// writeCookies は、このリクエストで起きた変更を Set-Cookie にする (レスポンスのヘッダーを書く直前に1回)。
func (st *reqState) writeCookies(w http.ResponseWriter) {
	if st.sessDirty {
		if st.sess.UID == 0 && st.sess.CSRF == "" {
			if st.hasCookie(cookieSession) {
				st.deleteCookie(w, cookieSession)
			}
		} else {
			if st.sess.IAT == 0 {
				st.sess.IAT = st.now().Unix()
			}
			http.SetCookie(w, st.cookie(cookieSession, st.s.sessCodec.encode(st.sess))) // 有効期限なし = ブラウザを閉じるまで
		}
	}
	switch st.remember {
	case rememberSet:
		c := st.cookie(cookieRemember, st.s.remCodec.encode(st.rememberVal))
		c.MaxAge = int(st.s.cfg.RememberDuration / time.Second)
		c.Expires = st.now().Add(st.s.cfg.RememberDuration)
		http.SetCookie(w, c)
	case rememberClear:
		if st.hasCookie(cookieRemember) {
			st.deleteCookie(w, cookieRemember)
		}
	}
	if st.flashLoad {
		var pending []FlashMessage
		if !st.flashShown {
			pending = append(append(pending, st.flashIn...), st.flashOut...)
		}
		switch {
		case len(pending) > 0:
			if len(pending) > maxFlashes {
				pending = pending[len(pending)-maxFlashes:]
			}
			http.SetCookie(w, st.cookie(cookieFlash, st.s.flashCodec.encode(pending)))
		case st.hasCookie(cookieFlash):
			st.deleteCookie(w, cookieFlash)
		}
	}
}

// stateWriter は、レスポンスのヘッダーを書く直前に、Cookie の変更を反映する。
type stateWriter struct {
	http.ResponseWriter
	st        *reqState
	committed bool
}

func (w *stateWriter) commit() {
	if !w.committed {
		w.committed = true
		w.st.writeCookies(w.ResponseWriter)
	}
}

func (w *stateWriter) WriteHeader(code int) { w.commit(); w.ResponseWriter.WriteHeader(code) }
func (w *stateWriter) Write(b []byte) (int, error) {
	w.commit()
	return w.ResponseWriter.Write(b)
}
func (w *stateWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
