package web

import (
	"net/http"

	"tshirt2/internal/service"
	"tshirt2/internal/util"
)

// routes は、この手順で用意する共通のルート (ログイン、ログアウト、パスワード変更、静的ファイル)。
// 各画面は、別の場所で Page / AdminPage / Public を使って追加する。
func (s *Server) routes() {
	s.mux.Handle("/static/", s.static)
	s.Public("GET /login", s.login)
	s.Public("POST /login", s.login)
	s.Page("POST /logout", s.logout)
	s.Page("GET /account/password", s.changePassword)
	s.Page("POST /account/password", s.changePassword)
	// 手順4の各画面 (読み取り系・書き込み系・管理画面)
	s.registerRead()
	s.registerOps()
	s.registerAdmin()
	// どのルートにも当たらないものは 404 (Flask 版と同じエラーページ)
	s.mux.HandleFunc("/", s.adapt(func(w http.ResponseWriter, r *http.Request) error { return NotFound() }))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	st := stateOf(r)
	u, err := st.currentUser()
	if err != nil {
		return err
	}
	if u != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return nil
	}
	if r.Method == http.MethodPost {
		user, err := service.Authenticate(r.Context(), s.DB(), r.PostFormValue("username"), r.PostFormValue("password"))
		if err != nil {
			return err
		}
		if user != nil {
			st.login(user, r.PostFormValue("remember") == "on")
			next := util.SafeNext(r.URL.Query().Get("next"))
			if next == "" {
				next = "/"
			}
			http.Redirect(w, r, next, http.StatusFound)
			return nil
		}
		Flash(r, "danger", service.MsgLoginFailed)
	}
	return s.Render(w, r, http.StatusOK, "auth/login.html", nil)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	stateOf(r).logout()
	http.Redirect(w, r, "/login", http.StatusFound)
	return nil
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) error {
	st := stateOf(r)
	u, err := st.currentUser()
	if err != nil {
		return err
	}
	if r.Method == http.MethodPost {
		updated, msg, err := service.ChangePassword(r.Context(), s.DB(), u.ID,
			r.PostFormValue("old"), r.PostFormValue("new"), r.PostFormValue("confirm"))
		if err != nil {
			return err
		}
		if msg != "" {
			Flash(r, "danger", msg)
		} else {
			// 他の端末のセッションはトークンが合わなくなって無効になる。本人だけ入り直させる
			st.refreshToken(updated)
			Flash(r, "success", service.MsgPasswordChanged)
			http.Redirect(w, r, "/", http.StatusFound)
			return nil
		}
	}
	return s.Render(w, r, http.StatusOK, "auth/password.html", nil)
}
