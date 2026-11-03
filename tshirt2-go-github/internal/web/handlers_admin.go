package web

// 管理画面 (カテゴリー・品番・ユーザー) のハンドラー (Flask 版 app/admin/routes.py)。
// すべて管理者専用 (AdminPage)。検証に失敗したら、flash を出して同じ画面を出し直す
// (Flask 版と同じで、入力値は保持しない。編集画面は DB の値で出し直す)。

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"tshirt2/internal/repo"
	"tshirt2/internal/service"
	"tshirt2/internal/util"
)

// registerAdmin は管理画面のルートを登録する。
// ハーネス (テスト) と routes() の両方から呼ばれても壊れないよう、二重登録を防ぐ。
var adminRegistered sync.Map // *Server -> bool

func (s *Server) registerAdmin() {
	if _, loaded := adminRegistered.LoadOrStore(s, true); loaded {
		return
	}
	s.AdminPage("GET /admin/categories", s.adminCategories)
	s.AdminPage("POST /admin/categories", s.adminCategories)
	s.AdminPage("GET /admin/categories/{id}", s.adminCategoryEdit)
	s.AdminPage("POST /admin/categories/{id}", s.adminCategoryEdit)
	s.AdminPage("GET /admin/items", s.adminItems)
	s.AdminPage("POST /admin/items", s.adminItems)
	s.AdminPage("GET /admin/items/{id}", s.adminItemEdit)
	s.AdminPage("POST /admin/items/{id}", s.adminItemEdit)
	s.AdminPage("GET /admin/users", s.adminUsers)
	s.AdminPage("POST /admin/users", s.adminUsers)
	s.AdminPage("POST /admin/users/{id}/toggle", s.adminUserToggle)
	s.AdminPage("POST /admin/users/{id}/password", s.adminUserPassword)
}

// pathID は URL の {id} を整数にする。Flask 版の <int:...> と同じく、
// ASCII の数字だけ (符号・空白なし) を受け付け、それ以外はルートに無いものとして扱う。
func pathID(r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	if raw == "" {
		return 0, false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	return util.ToInt(raw)
}

// flagOn は Flask 版 _flag (チェックボックスは "on" のときだけ 1)。
func flagOn(r *http.Request, name string) bool { return r.PostFormValue(name) == "on" }

func formFlags(r *http.Request) service.CategoryFlags {
	return service.CategoryFlags{
		UsesColor:       flagOn(r, "uses_color"),
		UsesSize:        flagOn(r, "uses_size"),
		UsesVariantName: flagOn(r, "uses_variant_name"),
		IsPrintable:     flagOn(r, "is_printable"),
	}
}

// flashServiceErr は、サービス層の *AdminError / *StockError を flash にする。
// どちらでもなければ false (呼び出し側が本物のエラーとして扱う)。
func flashServiceErr(r *http.Request, err error) bool {
	var ae *service.AdminError
	if errors.As(err, &ae) {
		Flash(r, "danger", ae.Msg)
		return true
	}
	var se *service.StockError
	if errors.As(err, &se) {
		Flash(r, "danger", se.Msg)
		return true
	}
	return false
}

// ---- カテゴリー ------------------------------------------------------

type categoriesPage struct {
	Cats []repo.Category
}

type categoryEditPage struct {
	Cat *repo.Category
}

func (s *Server) adminCategories(w http.ResponseWriter, r *http.Request) error {
	ctx, d := r.Context(), s.DB()
	if r.Method == http.MethodPost {
		name := r.PostFormValue("name")
		flags := formFlags(r)
		err := d.Tx(ctx, func(c *sql.Conn) error {
			return service.CreateCategory(ctx, c, name, flags)
		})
		if err == nil {
			Flash(r, "success", fmt.Sprintf("カテゴリーを追加しました: %s", util.PyStrip(name)))
			http.Redirect(w, r, "/admin/categories", http.StatusFound)
			return nil
		}
		if !flashServiceErr(r, err) {
			return err
		}
	}
	cats, err := repo.Categories(ctx, d)
	if err != nil {
		return err
	}
	return s.Render(w, r, http.StatusOK, "admin/categories.html", categoriesPage{Cats: cats})
}

func (s *Server) adminCategoryEdit(w http.ResponseWriter, r *http.Request) error {
	ctx, d := r.Context(), s.DB()
	id, ok := pathID(r)
	if !ok {
		return NotFound()
	}
	cat, err := repo.GetCategory(ctx, d, id)
	if err != nil {
		return err
	}
	if cat == nil {
		return &HTTPError{404, "カテゴリーが見つかりません。"}
	}
	if r.Method == http.MethodPost {
		rawOrder := util.PyStrip(r.PostFormValue("sort_order"))
		sortOrder, valid := int64(0), true // 空欄は 0 (Flask 版と同じ)
		if rawOrder != "" {
			n, ok := util.ToInt(rawOrder)
			if !ok {
				valid = false
			} else {
				sortOrder = n
			}
		}
		flags := formFlags(r)
		switch {
		case !valid:
			Flash(r, "danger", "表示順は整数で入力してください。")
		default:
			blocked, err := service.FlagsInUse(ctx, d, cat, flags)
			if err != nil {
				return err
			}
			if len(blocked) > 0 {
				Flash(r, "danger", "「"+strings.Join(blocked, "」「")+"」は、既に在庫登録があるためOFFにできません。"+
					"(OFFにすると、同じ行・列の在庫が1つに見えてしまいます)")
			} else {
				name := r.PostFormValue("name")
				err := d.Tx(ctx, func(c *sql.Conn) error {
					return service.UpdateCategory(ctx, c, cat, name, sortOrder, flags)
				})
				if err == nil {
					Flash(r, "success", "カテゴリーを更新しました。")
					http.Redirect(w, r, "/admin/categories", http.StatusFound)
					return nil
				}
				if !flashServiceErr(r, err) {
					return err
				}
			}
		}
	}
	return s.Render(w, r, http.StatusOK, "admin/category_edit.html", categoryEditPage{Cat: cat})
}

// ---- 品番 ------------------------------------------------------------

type itemsPage struct {
	Cats   []repo.Category
	Styles []string
	Items  []repo.Item
	CatID  int64 // 0 = すべて
}

type itemEditPage struct {
	Item   *repo.Item
	Styles []string
}

// queryCatID は一覧の絞り込み (Flask 版 to_int(request.args.get("category")))。
// 変換できない・指定なしは 0 (= すべて)。
func queryCatID(r *http.Request) int64 {
	n, ok := util.ToInt(r.URL.Query().Get("category"))
	if !ok {
		return 0
	}
	return n
}

func (s *Server) renderItems(w http.ResponseWriter, r *http.Request) error {
	ctx, d := r.Context(), s.DB()
	cats, err := repo.Categories(ctx, d)
	if err != nil {
		return err
	}
	styles, err := service.StyleChoices(ctx, d)
	if err != nil {
		return err
	}
	catID := queryCatID(r)
	items, err := repo.ItemsAdmin(ctx, d, catID)
	if err != nil {
		return err
	}
	return s.Render(w, r, http.StatusOK, "admin/items.html",
		itemsPage{Cats: cats, Styles: styles, Items: items, CatID: catID})
}

func (s *Server) adminItems(w http.ResponseWriter, r *http.Request) error {
	ctx, d := r.Context(), s.DB()
	if r.Method == http.MethodPost {
		catID, _ := util.ToInt(r.PostFormValue("category_id")) // Flask 版 to_int(..., 0)
		cat, err := repo.GetCategory(ctx, d, catID)
		if err != nil {
			return err
		}
		if cat == nil {
			Flash(r, "danger", "カテゴリーを選んでください。")
		} else {
			err := d.Tx(ctx, func(c *sql.Conn) error {
				return service.CreateItem(ctx, c, cat.ID,
					r.PostFormValue("brand"), r.PostFormValue("item_no"),
					r.PostFormValue("name"), r.PostFormValue("style"))
			})
			if err == nil {
				Flash(r, "success", "品番を追加しました。")
				http.Redirect(w, r, fmt.Sprintf("/admin/items?category=%d", cat.ID), http.StatusFound)
				return nil
			}
			if !flashServiceErr(r, err) {
				return err
			}
		}
	}
	return s.renderItems(w, r)
}

func (s *Server) adminItemEdit(w http.ResponseWriter, r *http.Request) error {
	ctx, d := r.Context(), s.DB()
	id, ok := pathID(r)
	if !ok {
		return NotFound()
	}
	item, err := repo.GetItem(ctx, d, id)
	if err != nil {
		return err
	}
	if item == nil {
		return &HTTPError{404, "品番が見つかりません。"}
	}
	if r.Method == http.MethodPost {
		err := d.Tx(ctx, func(c *sql.Conn) error {
			return service.UpdateItem(ctx, c, item.ID,
				r.PostFormValue("brand"), r.PostFormValue("item_no"),
				r.PostFormValue("name"), r.PostFormValue("style"), flagOn(r, "is_active"))
		})
		if err == nil {
			Flash(r, "success", "品番を更新しました。")
			http.Redirect(w, r, fmt.Sprintf("/admin/items?category=%d", item.CategoryID), http.StatusFound)
			return nil
		}
		if !flashServiceErr(r, err) {
			return err
		}
	}
	styles, err := service.StyleChoices(ctx, d)
	if err != nil {
		return err
	}
	return s.Render(w, r, http.StatusOK, "admin/item_edit.html", itemEditPage{Item: item, Styles: styles})
}

// ---- ユーザー --------------------------------------------------------

type usersPage struct {
	Users []repo.User
}

func (s *Server) renderUsers(w http.ResponseWriter, r *http.Request) error {
	users, err := repo.ListUsers(r.Context(), s.DB())
	if err != nil {
		return err
	}
	return s.Render(w, r, http.StatusOK, "admin/users.html", usersPage{Users: users})
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) error {
	ctx, d := r.Context(), s.DB()
	if r.Method == http.MethodPost {
		username := r.PostFormValue("username")
		err := d.Tx(ctx, func(c *sql.Conn) error {
			return service.CreateUser(ctx, c, username,
				r.PostFormValue("password"), r.PostFormValue("role"))
		})
		if err == nil {
			Flash(r, "success", fmt.Sprintf("ユーザーを追加しました: %s", util.PyStrip(username)))
			http.Redirect(w, r, "/admin/users", http.StatusFound)
			return nil
		}
		if !flashServiceErr(r, err) {
			return err
		}
	}
	return s.renderUsers(w, r)
}

// adminTargetUser は toggle / password の対象ユーザーを返す。無ければ、その旨のエラーを返す。
func (s *Server) adminTargetUser(r *http.Request) (*repo.User, error) {
	id, ok := pathID(r)
	if !ok {
		return nil, NotFound()
	}
	u, err := repo.GetUser(r.Context(), s.DB(), id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, &HTTPError{404, "ユーザーが見つかりません。"}
	}
	return u, nil
}

func (s *Server) adminUserToggle(w http.ResponseWriter, r *http.Request) error {
	ctx, d := r.Context(), s.DB()
	u, err := s.adminTargetUser(r)
	if err != nil {
		return err
	}
	if u.ID == CurrentUser(r).ID {
		Flash(r, "danger", "自分自身は無効化できません。")
	} else {
		newState := !u.IsActive
		err := d.Tx(ctx, func(c *sql.Conn) error {
			return service.SetUserActive(ctx, c, u.ID, newState)
		})
		if err != nil {
			return err
		}
		state := "無効"
		if newState {
			state = "有効"
		}
		Flash(r, "success", fmt.Sprintf("%s を%sにしました。", u.Username, state))
	}
	http.Redirect(w, r, "/admin/users", http.StatusFound)
	return nil
}

func (s *Server) adminUserPassword(w http.ResponseWriter, r *http.Request) error {
	ctx, d := r.Context(), s.DB()
	u, err := s.adminTargetUser(r)
	if err != nil {
		return err
	}
	err = d.Tx(ctx, func(c *sql.Conn) error {
		return service.ResetUserPassword(ctx, c, u.ID, r.PostFormValue("password"))
	})
	if err != nil {
		if !flashServiceErr(r, err) {
			return err
		}
	} else {
		if u.ID == CurrentUser(r).ID {
			// 自分自身を再設定した場合は入り直し (Flask 版 login_user の呼び直しと同じ)
			updated, err := repo.GetUser(ctx, d, u.ID)
			if err != nil {
				return err
			}
			stateOf(r).refreshToken(updated)
		}
		Flash(r, "success", fmt.Sprintf("%s のパスワードを再設定しました。(その人は再ログインが必要になります)", u.Username))
	}
	http.Redirect(w, r, "/admin/users", http.StatusFound)
	return nil
}
