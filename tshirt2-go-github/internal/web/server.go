package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"tshirt2/internal/db"
	"tshirt2/internal/repo"
	"tshirt2/internal/service"
)

// HandlerFunc は、エラーを返せるハンドラー。error を返すと、エラーページになる
// (*HTTPError ならそのコードで、DB が混み合っているなら 503、それ以外は 500)。
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Server は HTTP サーバー。
type Server struct {
	cfg Config

	sessCodec, remCodec, flashCodec codec
	csrfKey                         []byte

	dbMu     sync.Mutex
	db       *db.DB
	schemaOK atomic.Bool
	warned   atomic.Bool

	pages  map[string]*template.Template
	mux    *http.ServeMux
	static http.Handler
}

const maxBody = 1 << 20 // フォームの最大サイズ (1 MiB)

// New は Server を作る。DB が未作成/古くても作れる (そのあいだは 503 の案内を返す)。
func New(cfg Config) (*Server, error) {
	cfg.applyDefaults()
	if len(cfg.SecretKey) == 0 {
		return nil, fmt.Errorf("web: SecretKey is empty")
	}
	pages, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:        cfg,
		sessCodec:  newCodec(cfg.SecretKey, "session"),
		remCodec:   newCodec(cfg.SecretKey, "remember"),
		flashCodec: newCodec(cfg.SecretKey, "flash"),
		csrfKey:    newCodec(cfg.SecretKey, "csrf").key,
		pages:      pages,
		mux:        http.NewServeMux(),
	}
	s.static = staticHandler(func(w http.ResponseWriter, r *http.Request) {
		s.errorPage(w, r, http.StatusNotFound, NotFound().(*HTTPError).Message)
	})
	s.routes()
	return s, nil
}

// rejectDotSegments は、パスに "." / ".." のセグメントを含むリクエストを、
// ルーティングの手前でアプリの 404 ページにする。
//
// ServeMux には「正規形でないパスを正規形へリダイレクトする」動作があるが、
// そのステータスは Go の版で変わる (1.22 では 301、現行では 307)。版に依存せず
// Flask 版と同じくその場で 404 を返すため、ここで受ける。守りの本体は別にあり
// (静的ファイルは埋め込みの一覧表からしか配らない)、これは見え方を揃える層。
// 判定はセグメント単位で、各セグメントを復号してから比べるため、
// %2e%2e のようなエンコードされた ".." もここで受けるし、
// "foo..bar" のような名前の一部は弾かない。
func (s *Server) rejectDotSegments(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, seg := range strings.Split(r.URL.EscapedPath(), "/") {
			if u, err := url.PathUnescape(seg); err == nil && (u == "." || u == "..") {
				s.errorPage(w, r, http.StatusNotFound, NotFound().(*HTTPError).Message)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// DB は、スキーマの確認が済んだあとの DB (確認前は nil)。ハンドラーは、確認後にだけ呼ばれる。
func (s *Server) DB() *db.DB {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	return s.db
}

// Close は DB を閉じる。
func (s *Server) Close() error {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Handler は、ミドルウェアを含む http.Handler。
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = s.rejectDotSegments(h)
	h = s.csrfProtect(h)
	h = s.requireSchema(h)
	h = s.noStore(h)
	h = s.recoverPanic(h)
	return s.withState(h)
}

// ---- ルート登録 (各画面が使う) ---------------------------------------------------------

func (s *Server) adapt(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			s.handleError(w, r, err)
		}
	}
}

// Public はログイン不要のルート。
func (s *Server) Public(pattern string, h HandlerFunc) { s.mux.HandleFunc(pattern, s.adapt(h)) }

// Page はログイン必須のルート。未ログインなら、ログイン画面へ移動する (移動先は next に入れる)。
func (s *Server) Page(pattern string, h HandlerFunc) {
	s.mux.HandleFunc(pattern, s.adapt(func(w http.ResponseWriter, r *http.Request) error {
		u, err := stateOf(r).currentUser()
		if err != nil {
			return err
		}
		if u == nil {
			s.redirectToLogin(w, r)
			return nil
		}
		return h(w, r)
	}))
}

// AdminPage は管理者だけのルート。未ログインはログイン画面へ、スタッフは 403。
func (s *Server) AdminPage(pattern string, h HandlerFunc) {
	s.Page(pattern, func(w http.ResponseWriter, r *http.Request) error {
		if u, _ := stateOf(r).currentUser(); u == nil || !u.IsAdmin() {
			return Forbidden()
		}
		return h(w, r)
	})
}

func (s *Server) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	Flash(r, "warning", service.MsgLoginRequired)
	http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
}

// Flash は、次に表示するメッセージを追加する (リダイレクトを挟んでも届く)。
func Flash(r *http.Request, category, message string) { stateOf(r).addFlash(category, message) }

// CurrentUser はログイン中のユーザー (未ログインなら nil)。Page / AdminPage の中では、必ず非 nil。
func CurrentUser(r *http.Request) *repo.User {
	u, _ := stateOf(r).currentUser()
	return u
}

// ---- ミドルウェア ----------------------------------------------------------------------

func isStatic(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/static/") }

func (s *Server) withState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &reqState{s: s, r: r}
		sw := &stateWriter{ResponseWriter: w, st: st}
		next.ServeHTTP(sw, withState(r, st))
		sw.commit() // 何も書かなかった場合も、Cookie の変更は反映する
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.cfg.Logger.Printf("panic: %s %s: %v\n%s", r.Method, r.URL.Path, v, debug.Stack())
				s.errorPage(w, r, http.StatusInternalServerError, msgServer)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// noStore: 在庫は常に最新を見せたいので、HTML をキャッシュさせない (静的ファイル以外)。
func (s *Server) noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isStatic(r) {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

const schemaPage = `<!doctype html>
<html lang="ja"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>準備中</title></head>
<body style="font-family:sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem">
<h1 style="font-size:1.3rem">データベースの更新が必要です</h1>
<p>管理者に連絡してください。</p>
<p style="color:#666;font-size:.9rem">(管理者向け: %s)</p>
</body></html>`

// ensureSchema は、DB が使える状態か (最新のスキーマか) を確かめる。使える状態になったら、
// 以降は確認しない。更新が済めば、再起動なしで使えるようになる。
func (s *Server) ensureSchema(ctx context.Context) (ok bool, hint string) {
	if s.schemaOK.Load() {
		return true, ""
	}
	st, err := db.Check(ctx, s.cfg.DatabasePath) // 存在しないファイルは、作らずに missing を返す
	switch {
	case err != nil:
		hint = "DBファイルを開けません: " + err.Error()
	case st == db.StateCurrent:
		d, oerr := db.Open(s.cfg.DatabasePath)
		if oerr != nil {
			hint = "DBファイルを開けません: " + oerr.Error()
			break
		}
		s.dbMu.Lock()
		if s.db != nil {
			s.db.Close()
		}
		s.db = d
		s.dbMu.Unlock()
		s.schemaOK.Store(true)
		return true, ""
	case st == db.StateForeign:
		hint = "この版が作ったDBではありません"
		if fe, derr := db.DescribeForeign(ctx, s.cfg.DatabasePath); derr == nil && fe != nil {
			hint = fe.Error()
		}
	default:
		hint = s.cfg.InitHint
	}
	if !s.warned.Swap(true) {
		s.cfg.Logger.Printf("DBが未作成か、古いか、使えない状態です (%s): %s", st, s.cfg.DatabasePath)
	}
	return false, hint
}

// requireSchema: DB が未作成/古いままだと、画面を開いて初めて 500 になる。先に分かりやすく止める。
func (s *Server) requireSchema(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStatic(r) {
			next.ServeHTTP(w, r)
			return
		}
		ok, hint := s.ensureSchema(r.Context())
		if !ok {
			w.Header().Set("Retry-After", "60")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, schemaPage, template.HTMLEscapeString(hint))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrfProtect: 状態を変える要求 (POST など) には、フォームの csrf_token か X-CSRFToken ヘッダーを必須にする。
func (s *Server) csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		token := r.Header.Get("X-CSRFToken")
		if token == "" {
			token = r.Header.Get("X-CSRF-Token")
		}
		if token == "" {
			if err := r.ParseMultipartForm(maxBody); err != nil && err != http.ErrNotMultipart {
				s.errorPage(w, r, http.StatusBadRequest, "送信内容を読み取れませんでした。")
				return
			}
			token = r.PostFormValue("csrf_token")
		}
		if !stateOf(r).csrfValid(token) {
			s.errorPage(w, r, http.StatusBadRequest, msgCSRF)
			return
		}
		next.ServeHTTP(w, r)
	})
}
