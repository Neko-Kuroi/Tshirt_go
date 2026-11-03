package web

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"tshirt2/internal/repo"
	"tshirt2/internal/util"
)

//go:embed all:templates
var templateFS embed.FS

//go:embed all:static
var staticFS embed.FS

// PageData は全ページ共通のテンプレートの入力。ページ固有の値は Data に入れる。
type PageData struct {
	User    *repo.User
	Flashes []FlashMessage
	CSRF    string
	Data    any
}

var funcs = template.FuncMap{
	"jst": util.FormatJST, // DB は UTC 保存。表示は日本時間
}

// loadTemplates は、templates/ のページごとに「base.html + _*.html (部品) + そのページ」を組む。
// (Go のテンプレートには継承が無いので、ページごとに、title / content ブロックを差し替えた1式を作る)
func loadTemplates() (map[string]*template.Template, error) {
	var shared []string
	var pages []string
	err := fs.WalkDir(templateFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := path.Base(p)
		switch {
		case base == "base.html" || strings.HasPrefix(base, "_"):
			shared = append(shared, p)
		default:
			pages = append(pages, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		files := append(append([]string{}, shared...), p)
		t, err := template.New(path.Base(p)).Funcs(funcs).ParseFS(templateFS, files...)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", p, err)
		}
		out[strings.TrimPrefix(p, "templates/")] = t
	}
	return out, nil
}

// HTTPError は、ハンドラーが返す「このコードのエラーページを出してほしい」という指示。
type HTTPError struct {
	Code    int
	Message string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }

// よく使うエラー (文言は Flask 版と同じ)。
func NotFound() error  { return &HTTPError{404, "ページが見つかりません。"} }
func Forbidden() error { return &HTTPError{403, "この操作をする権限がありません。"} }

const (
	msgCSRF   = "画面の有効期限が切れたか、不正な送信です。ページを開き直して、もう一度お試しください。"
	msgBusy   = "ただいま混み合っています。数秒おいて、もう一度お試しください。"
	msgServer = "エラーが起きました。管理者に連絡してください。"
)

// Render はページをテンプレートから描画する。
func (s *Server) Render(w http.ResponseWriter, r *http.Request, status int, page string, data any) error {
	t, ok := s.pages[page]
	if !ok {
		return fmt.Errorf("unknown template %q", page)
	}
	st := stateOf(r)
	user, _ := st.currentUser() // 取得に失敗しても、ナビの表示を省くだけ (エラーページの描画を止めない)
	pd := PageData{User: user, Flashes: st.takeFlashes(), CSRF: st.csrfToken(), Data: data}
	var buf bytes.Buffer // 途中まで書いてから失敗する、を避けるため、先に全部描画する
	if err := t.ExecuteTemplate(&buf, "base", pd); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := w.Write(buf.Bytes())
	return err
}

func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, code int, message string) {
	err := s.Render(w, r, code, "error.html", struct {
		Code    int
		Message string
	}{code, message})
	if err != nil { // エラーページの描画自体が失敗したときの、最後の手段
		http.Error(w, message, code)
	}
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "database is locked") || strings.Contains(m, "busy")
}

// handleError は、ハンドラーが返した error をエラーページにする。
func (s *Server) handleError(w http.ResponseWriter, r *http.Request, err error) {
	var he *HTTPError
	if errors.As(err, &he) {
		s.errorPage(w, r, he.Code, he.Message)
		return
	}
	s.cfg.Logger.Printf("error: %s %s: %v", r.Method, r.URL.Path, err)
	if isBusy(err) {
		w.Header().Set("Retry-After", "5")
		s.errorPage(w, r, http.StatusServiceUnavailable, msgBusy)
		return
	}
	s.errorPage(w, r, http.StatusInternalServerError, msgServer)
}
