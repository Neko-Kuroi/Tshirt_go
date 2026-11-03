package web

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// newAdminClient は、管理者でログイン済みのクライアントを返す (管理画面のルート登録込み)。
func newAdminClient(t *testing.T, h *harness) *client {
	t.Helper()
	h.srv.registerAdmin()
	c := h.client()
	c.mustLogin("admin", false)
	return c
}

func scalarEq(t *testing.T, h *harness, want int64, query string, args ...any) {
	t.Helper()
	var n int64
	if err := h.d.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Errorf("%s = %d, want %d", query, n, want)
	}
}

// ---- カテゴリー ------------------------------------------------------

func TestAdminCategoryCreateAndEdit(t *testing.T) {
	h := newHarness(t)
	c := newAdminClient(t, h)

	// 追加 → 一覧へ戻り、成功メッセージと行が出る
	r := c.postCSRF("/admin/categories", "/admin/categories", url.Values{
		"name": {"ステッカー"}, "uses_color": {"on"}, "is_printable": {"on"}})
	if r.status != 302 || r.location() != "/admin/categories" {
		t.Fatalf("create: %d %q", r.status, r.location())
	}
	r = c.get("/admin/categories")
	contains(t, r, "カテゴリーを追加しました: ステッカー")
	contains(t, r, "ステッカー")

	// 名前が空 → エラー表示で出し直し (302 しない)
	r = c.postCSRF("/admin/categories", "/admin/categories", url.Values{"name": {"  "}})
	if r.status != 200 {
		t.Fatalf("empty name: %d", r.status)
	}
	contains(t, r, "カテゴリー名を入力してください。")

	// 同名 → 重複エラー
	r = c.postCSRF("/admin/categories", "/admin/categories", url.Values{"name": {"ステッカー"}})
	contains(t, r, "同名のカテゴリーが既にあります。")

	// 編集: 表示順が整数でない → エラー。正常なら保存される
	var cid int64
	if err := h.d.QueryRowContext(ctx, "SELECT id FROM category WHERE name = 'ステッカー'").Scan(&cid); err != nil {
		t.Fatal(err)
	}
	r = c.get(fmt.Sprintf("/admin/categories/%d", cid))
	if r.status != 200 {
		t.Fatalf("edit page: %d", r.status)
	}
	contains(t, r, "カテゴリー編集: ステッカー")
	r = c.postCSRF(fmt.Sprintf("/admin/categories/%d", cid), fmt.Sprintf("/admin/categories/%d", cid), url.Values{
		"name": {"ステッカー"}, "sort_order": {"abc"}})
	if r.status != 200 {
		t.Fatalf("bad sort_order: %d", r.status)
	}
	contains(t, r, "表示順は整数で入力してください。")
	r = c.postCSRF(fmt.Sprintf("/admin/categories/%d", cid), fmt.Sprintf("/admin/categories/%d", cid), url.Values{
		"name": {"シール"}, "sort_order": {"7"}, "uses_color": {"on"}})
	if r.status != 302 || r.location() != "/admin/categories" {
		t.Fatalf("edit: %d %q", r.status, r.location())
	}
	scalarEq(t, h, 7, "SELECT sort_order FROM category WHERE id = ?", cid)
	r = c.get("/admin/categories")
	contains(t, r, "カテゴリーを更新しました。")
	contains(t, r, "シール")

	// 無いカテゴリーは 404 で、Flask 版の文言
	r = c.get("/admin/categories/999")
	if r.status != 404 {
		t.Fatalf("missing category: %d", r.status)
	}
	contains(t, r, "カテゴリーが見つかりません。")
}

func TestAdminCategoryFlagInUse(t *testing.T) {
	h := newHarness(t)
	c := newAdminClient(t, h)

	r := c.postCSRF("/admin/categories", "/admin/categories", url.Values{
		"name": {"テスト"}, "uses_color": {"on"}})
	if r.status != 302 {
		t.Fatalf("create: %d", r.status)
	}
	var cid, iid int64
	if err := h.d.QueryRowContext(ctx, "SELECT id FROM category WHERE name = 'テスト'").Scan(&cid); err != nil {
		t.Fatal(err)
	}
	// そのカテゴリーの品番に、色つきの在庫を登録する
	if _, err := h.d.ExecContext(ctx,
		"INSERT INTO item (category_id, brand, item_no, name) VALUES (?, 'B社', '1', '')", cid); err != nil {
		t.Fatal(err)
	}
	if err := h.d.QueryRowContext(ctx, "SELECT id FROM item WHERE category_id = ?", cid).Scan(&iid); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.ExecContext(ctx,
		"INSERT INTO variant (item_id, color, size, variant_name, quantity) VALUES (?, '赤', '', '', 1)", iid); err != nil {
		t.Fatal(err)
	}

	// 色を OFF にして保存 → 在庫があるので拒否され、設定は変わらない
	r = c.postCSRF(fmt.Sprintf("/admin/categories/%d", cid), fmt.Sprintf("/admin/categories/%d", cid), url.Values{
		"name": {"テスト"}, "sort_order": {"0"}})
	if r.status != 200 {
		t.Fatalf("flag off: %d", r.status)
	}
	contains(t, r, "「色」は、既に在庫登録があるためOFFにできません。")
	scalarEq(t, h, 1, "SELECT uses_color FROM category WHERE id = ?", cid)
}

// ---- 品番 ------------------------------------------------------------

func TestAdminItemCreateAndEdit(t *testing.T) {
	h := newHarness(t)
	c := newAdminClient(t, h)
	if _, err := h.d.ExecContext(ctx,
		"INSERT INTO category (name, sort_order, uses_color, uses_size, uses_variant_name, is_printable) "+
			"VALUES ('Tシャツ', 0, 1, 1, 0, 1)"); err != nil {
		t.Fatal(err)
	}

	// カテゴリー未選択 → エラー
	r := c.postCSRF("/admin/items", "/admin/items", url.Values{"brand": {"B社"}})
	if r.status != 200 {
		t.Fatalf("no category: %d", r.status)
	}
	contains(t, r, "カテゴリーを選んでください。")

	// ブランド・品番が両方空 → エラー
	r = c.postCSRF("/admin/items", "/admin/items", url.Values{"category_id": {"1"}, "name": {"x"}})
	contains(t, r, "ブランドか品番のどちらかは入力してください。")

	// 追加 → そのカテゴリーの一覧へ
	r = c.postCSRF("/admin/items", "/admin/items", url.Values{
		"category_id": {"1"}, "brand": {"Printstar"}, "item_no": {"085-CVT"}, "name": {"ヘビーウェイト"}, "style": {"半袖"}})
	if r.status != 302 || r.location() != "/admin/items?category=1" {
		t.Fatalf("create: %d %q", r.status, r.location())
	}
	r = c.get("/admin/items?category=1")
	contains(t, r, "品番を追加しました。")
	contains(t, r, "Printstar")
	contains(t, r, "085-CVT")

	// 重複 → エラー
	r = c.postCSRF("/admin/items", "/admin/items", url.Values{
		"category_id": {"1"}, "brand": {"Printstar"}, "item_no": {"085-CVT"}})
	if r.status != 200 {
		t.Fatalf("duplicate: %d", r.status)
	}
	contains(t, r, "同じカテゴリーに同じブランド・品番が既にあります。")

	// 編集: 使用停止にして保存 → 一覧で (停止中) と出る
	var iid int64
	if err := h.d.QueryRowContext(ctx, "SELECT id FROM item WHERE brand = 'Printstar'").Scan(&iid); err != nil {
		t.Fatal(err)
	}
	r = c.postCSRF(fmt.Sprintf("/admin/items/%d", iid), fmt.Sprintf("/admin/items/%d", iid), url.Values{
		"brand": {"Printstar"}, "item_no": {"085-CVT"}, "name": {"ヘビーウェイト"}, "style": {"長袖"}})
	if r.status != 302 || r.location() != "/admin/items?category=1" {
		t.Fatalf("edit: %d %q", r.status, r.location())
	}
	r = c.get("/admin/items?category=1")
	contains(t, r, "品番を更新しました。")
	contains(t, r, "(停止中)")
	scalarEq(t, h, 0, "SELECT is_active FROM item WHERE id = ?", iid)

	// 無い品番は 404 で、Flask 版の文言
	r = c.get("/admin/items/999")
	if r.status != 404 {
		t.Fatalf("missing item: %d", r.status)
	}
	contains(t, r, "品番が見つかりません。")
}

// ---- ユーザー --------------------------------------------------------

func TestAdminUserCreate(t *testing.T) {
	h := newHarness(t)
	c := newAdminClient(t, h)

	// 追加 → 一覧へ。作ったユーザーでログインできる
	r := c.postCSRF("/admin/users", "/admin/users", url.Values{
		"username": {"newbie"}, "password": {"password999"}, "role": {"staff"}})
	if r.status != 302 || r.location() != "/admin/users" {
		t.Fatalf("create: %d %q", r.status, r.location())
	}
	r = c.get("/admin/users")
	contains(t, r, "ユーザーを追加しました: newbie")
	contains(t, r, "newbie")
	if r := h.client().login("newbie", "password999", false); r.status != 302 {
		t.Errorf("new user login: %d", r.status)
	}

	// 重複 → エラー
	r = c.postCSRF("/admin/users", "/admin/users", url.Values{
		"username": {"newbie"}, "password": {"password999"}})
	if r.status != 200 {
		t.Fatalf("duplicate: %d", r.status)
	}
	contains(t, r, "そのユーザー名は既に使われています。")

	// 短いパスワード / 空のユーザー名 → エラー
	r = c.postCSRF("/admin/users", "/admin/users", url.Values{
		"username": {"shorty"}, "password": {"short"}})
	contains(t, r, "ユーザー名と、8文字以上のパスワードを入力してください。")
	r = c.postCSRF("/admin/users", "/admin/users", url.Values{
		"username": {" "}, "password": {"password999"}})
	contains(t, r, "ユーザー名と、8文字以上のパスワードを入力してください。")
	scalarEq(t, h, 3, `SELECT COUNT(*) FROM "user"`) // admin, staff, newbie だけ
}

func TestAdminUserToggle(t *testing.T) {
	h := newHarness(t)
	c := newAdminClient(t, h)
	var staffID int64
	if err := h.d.QueryRowContext(ctx, `SELECT id FROM "user" WHERE username = 'staff'`).Scan(&staffID); err != nil {
		t.Fatal(err)
	}

	// 無効化 → メッセージが出て、staff はログインできなくなる
	r := c.postCSRF("/admin/users", fmt.Sprintf("/admin/users/%d/toggle", staffID), nil)
	if r.status != 302 || r.location() != "/admin/users" {
		t.Fatalf("toggle: %d %q", r.status, r.location())
	}
	r = c.get("/admin/users")
	contains(t, r, "staff を無効にしました。")
	scalarEq(t, h, 0, `SELECT is_active FROM "user" WHERE id = ?`, staffID)
	if r := h.client().login("staff", testPassword, false); r.status != 200 {
		t.Errorf("disabled staff login: %d, want 200 (ログイン画面のまま)", r.status)
	}

	// 有効化し直せる
	r = c.postCSRF("/admin/users", fmt.Sprintf("/admin/users/%d/toggle", staffID), nil)
	if r.status != 302 {
		t.Fatalf("toggle back: %d", r.status)
	}
	r = c.get("/admin/users")
	contains(t, r, "staff を有効にしました。")
	scalarEq(t, h, 1, `SELECT is_active FROM "user" WHERE id = ?`, staffID)

	// 自分自身は無効化できない
	var adminID int64
	if err := h.d.QueryRowContext(ctx, `SELECT id FROM "user" WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	r = c.postCSRF("/admin/users", fmt.Sprintf("/admin/users/%d/toggle", adminID), nil)
	if r.status != 302 {
		t.Fatalf("self toggle: %d", r.status)
	}
	r = c.get("/admin/users")
	contains(t, r, "自分自身は無効化できません。")
	scalarEq(t, h, 1, `SELECT is_active FROM "user" WHERE id = ?`, adminID)

	// 無いユーザーは 404 で、Flask 版の文言
	r = c.postCSRF("/admin/users", "/admin/users/999/toggle", nil)
	if r.status != 404 {
		t.Fatalf("missing user toggle: %d", r.status)
	}
	contains(t, r, "ユーザーが見つかりません。")
}

func TestAdminUserPasswordReset(t *testing.T) {
	h := newHarness(t)
	c := newAdminClient(t, h)
	var staffID int64
	if err := h.d.QueryRowContext(ctx, `SELECT id FROM "user" WHERE username = 'staff'`).Scan(&staffID); err != nil {
		t.Fatal(err)
	}

	// 短いパスワード → エラー (リダイレクト先の一覧で表示)
	r := c.postCSRF("/admin/users", fmt.Sprintf("/admin/users/%d/password", staffID), url.Values{"password": {"short"}})
	if r.status != 302 {
		t.Fatalf("short reset: %d", r.status)
	}
	r = c.get("/admin/users")
	contains(t, r, "パスワードは8文字以上にしてください。")

	// 再設定 → 新しいパスワードでログインでき、古いパスワードは通らない
	r = c.postCSRF("/admin/users", fmt.Sprintf("/admin/users/%d/password", staffID), url.Values{"password": {"brand-new-pw"}})
	if r.status != 302 {
		t.Fatalf("reset: %d", r.status)
	}
	r = c.get("/admin/users")
	contains(t, r, "staff のパスワードを再設定しました。(その人は再ログインが必要になります)")
	if r := h.client().login("staff", "brand-new-pw", false); r.status != 302 {
		t.Errorf("login with new password: %d", r.status)
	}
	if r := h.client().login("staff", testPassword, false); r.status != 200 {
		t.Errorf("login with old password: %d, want 200 (失敗)", r.status)
	}

	// 自分自身の再設定: セッションは入り直し扱いになり、そのまま使い続けられる
	var adminID int64
	if err := h.d.QueryRowContext(ctx, `SELECT id FROM "user" WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	r = c.postCSRF("/admin/users", fmt.Sprintf("/admin/users/%d/password", adminID), url.Values{"password": {"admin-new-pw"}})
	if r.status != 302 {
		t.Fatalf("self reset: %d", r.status)
	}
	if r := c.get("/admin/users"); r.status != 200 {
		t.Errorf("after self reset: %d, want 200 (セッション継続)", r.status)
	}
	if r := h.client().login("admin", "admin-new-pw", false); r.status != 302 {
		t.Errorf("login as admin with new password: %d", r.status)
	}
}

func TestAdminPagesDeniedForStaff(t *testing.T) {
	h := newHarness(t)
	h.srv.registerAdmin()
	staff := h.client()
	staff.mustLogin("staff", false)
	for _, p := range []string{"/admin/categories", "/admin/items", "/admin/users"} {
		if r := staff.get(p); r.status != 403 {
			t.Errorf("staff GET %s: %d, want 403", p, r.status)
		}
	}
	r := staff.postCSRF("/", "/admin/users", url.Values{"username": {"x"}, "password": {"password999"}})
	if r.status != 403 {
		t.Errorf("staff POST /admin/users: %d, want 403", r.status)
	}
	scalarEq(t, h, 2, `SELECT COUNT(*) FROM "user"`)
	if !strings.Contains(r.body, "この操作をする権限がありません。") {
		t.Errorf("403 body missing message: %.200s", r.body)
	}
}
