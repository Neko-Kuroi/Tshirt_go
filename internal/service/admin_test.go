package service

import (
	"database/sql"
	"strings"
	"testing"

	"tshirt2/internal/auth"
	"tshirt2/internal/repo"
)

// wantAdminError は、err が *AdminError で、文言が期待どおりかを確認する。
func wantAdminError(t *testing.T, err error, msg string) {
	t.Helper()
	if !IsAdminError(err) {
		t.Fatalf("err = %v; want *AdminError", err)
	}
	if err.Error() != msg {
		t.Fatalf("err = %q; want %q", err.Error(), msg)
	}
}

// ---- ユーザー作成 ----------------------------------------------------

func TestCreateUserValidations(t *testing.T) {
	d := newDB(t)

	// ユーザー名が空 (空白だけも) → エラー
	for _, name := range []string{"", "  　"} {
		err := tx(d, func(c *sql.Conn) error { return CreateUser(ctx, c, name, "password123", "staff") })
		wantAdminError(t, err, "ユーザー名と、8文字以上のパスワードを入力してください。")
	}
	// パスワードが短い → エラー。文字数 (バイト数ではない) で数える: 7文字の日本語は不可、8文字は可
	err := tx(d, func(c *sql.Conn) error { return CreateUser(ctx, c, "new1", "あいうえおかき", "staff") })
	wantAdminError(t, err, "ユーザー名と、8文字以上のパスワードを入力してください。")
	if n := count(t, d, `"user"`); n != 2 {
		t.Fatalf("users = %d; want 2 (失敗した作成が残っていない)", n)
	}

	// 正常系: 前後の空白は除かれ、role の指定なし相当 ("x") はスタッフになる
	mustTx(t, d, func(c *sql.Conn) error { return CreateUser(ctx, c, " new1 ", "あいうえおかきく", "x") })
	u, err := repo.GetUserByName(ctx, d, "new1")
	if err != nil || u == nil {
		t.Fatalf("GetUserByName(new1) = %v, %v", u, err)
	}
	if u.Role != "staff" || !u.IsActive {
		t.Errorf("role=%q active=%v; want staff / active", u.Role, u.IsActive)
	}
	// ハッシュは Werkzeug 互換の scrypt 形式で、作ったパスワードで照合できる
	if !strings.HasPrefix(u.PasswordHash, "scrypt:") {
		t.Errorf("hash = %q; want scrypt:...", u.PasswordHash)
	}
	if ok, err := auth.Verify(u.PasswordHash, "あいうえおかきく"); err != nil || !ok {
		t.Errorf("Verify = %v, %v; want true", ok, err)
	}

	// role = "admin" は管理者になる
	mustTx(t, d, func(c *sql.Conn) error { return CreateUser(ctx, c, "boss", "password123", "admin") })
	boss, _ := repo.GetUserByName(ctx, d, "boss")
	if boss == nil || !boss.IsAdmin() {
		t.Errorf("boss = %+v; want admin", boss)
	}

	// 重複するユーザー名 → エラー (UNIQUE 違反を「重複」の文言にする)
	err = tx(d, func(c *sql.Conn) error { return CreateUser(ctx, c, "new1", "password123", "staff") })
	wantAdminError(t, err, "そのユーザー名は既に使われています。")
	err = tx(d, func(c *sql.Conn) error { return CreateUser(ctx, c, "admin", "password123", "staff") })
	wantAdminError(t, err, "そのユーザー名は既に使われています。")
}

func TestSetUserActiveAndResetPassword(t *testing.T) {
	d := newDB(t)

	mustTx(t, d, func(c *sql.Conn) error { return SetUserActive(ctx, c, staffID, false) })
	u, _ := repo.GetUser(ctx, d, staffID)
	if u.IsActive {
		t.Error("staff is still active after SetUserActive(false)")
	}
	mustTx(t, d, func(c *sql.Conn) error { return SetUserActive(ctx, c, staffID, true) })
	u, _ = repo.GetUser(ctx, d, staffID)
	if !u.IsActive {
		t.Error("staff is not active after SetUserActive(true)")
	}

	// 短いパスワードは不可
	err := tx(d, func(c *sql.Conn) error { return ResetUserPassword(ctx, c, staffID, "short") })
	wantAdminError(t, err, "パスワードは8文字以上にしてください。")

	// 再設定すると、新しいパスワードで照合できる
	mustTx(t, d, func(c *sql.Conn) error { return ResetUserPassword(ctx, c, staffID, "new-password") })
	u, _ = repo.GetUser(ctx, d, staffID)
	if ok, err := auth.Verify(u.PasswordHash, "new-password"); err != nil || !ok {
		t.Errorf("Verify(new) = %v, %v; want true", ok, err)
	}
}

// ---- カテゴリー ------------------------------------------------------

func TestCreateAndUpdateCategory(t *testing.T) {
	d := newDB(t)
	all := CategoryFlags{UsesColor: true, UsesSize: true, UsesVariantName: true, IsPrintable: true}

	err := tx(d, func(c *sql.Conn) error { return CreateCategory(ctx, c, " ", all) })
	wantAdminError(t, err, "カテゴリー名を入力してください。")

	// 表示順は追加前の件数 (初期データが2件あるので 2)
	mustTx(t, d, func(c *sql.Conn) error { return CreateCategory(ctx, c, "ステッカー", all) })
	cats, err := repo.Categories(ctx, d)
	if err != nil || len(cats) != 3 {
		t.Fatalf("Categories = %v, %v", cats, err)
	}
	if cats[2].Name != "ステッカー" || cats[2].SortOrder != 2 {
		t.Errorf("new category = %+v; want name=ステッカー sort_order=2", cats[2])
	}

	// 同名は重複エラー
	err = tx(d, func(c *sql.Conn) error { return CreateCategory(ctx, c, "ステッカー", all) })
	wantAdminError(t, err, "同名のカテゴリーが既にあります。")

	// 更新: 名前が空なら据え置き。同名 (他のカテゴリー) は重複エラー
	cat := cats[2]
	mustTx(t, d, func(c *sql.Conn) error { return UpdateCategory(ctx, c, &cat, "", 5, CategoryFlags{}) })
	got, _ := repo.GetCategory(ctx, d, cat.ID)
	if got.Name != "ステッカー" || got.SortOrder != 5 || got.UsesColor {
		t.Errorf("updated = %+v; want name 据え置き / sort_order=5 / uses_color=0", got)
	}
	err = tx(d, func(c *sql.Conn) error { return UpdateCategory(ctx, c, &cat, "Tシャツ", 0, CategoryFlags{}) })
	wantAdminError(t, err, "同名のカテゴリーが既にあります。")
}

func TestFlagsInUse(t *testing.T) {
	d := newDB(t)
	// カテゴリー1 (Tシャツ) は色・サイズ使用。色だけ値が入った variant を作る
	mustExec(t, d, "INSERT INTO variant (item_id, color, size, variant_name, quantity) VALUES (1, '白', '', '', 3)")
	cat, err := repo.GetCategory(ctx, d, 1)
	if err != nil || cat == nil {
		t.Fatalf("GetCategory(1) = %v, %v", cat, err)
	}

	// 色を OFF → ブロックされる。サイズは値が無いので OFF にできる
	blocked, err := FlagsInUse(ctx, d, cat, CategoryFlags{UsesSize: true, IsPrintable: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0] != "色" {
		t.Errorf("blocked = %v; want [色]", blocked)
	}

	// サイズにも値が入れば、両方ブロックされる (順序は色・サイズ)
	mustExec(t, d, "UPDATE variant SET size = 'M' WHERE item_id = 1")
	blocked, err = FlagsInUse(ctx, d, cat, CategoryFlags{IsPrintable: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 2 || blocked[0] != "色" || blocked[1] != "サイズ" {
		t.Errorf("blocked = %v; want [色 サイズ]", blocked)
	}

	// どちらも ON のままならブロックなし
	blocked, err = FlagsInUse(ctx, d, cat, CategoryFlags{UsesColor: true, UsesSize: true})
	if err != nil || len(blocked) != 0 {
		t.Errorf("blocked = %v, %v; want empty", blocked, err)
	}

	// 元々 OFF の設定 (種類名) は、値があっても対象外
	mustExec(t, d, "INSERT INTO variant (item_id, color, size, variant_name, quantity) VALUES (3, '', '', '星', 1)")
	badge, _ := repo.GetCategory(ctx, d, 2) // バッジ: 種類名 ON
	blocked, err = FlagsInUse(ctx, d, badge, CategoryFlags{})
	if err != nil || len(blocked) != 1 || blocked[0] != "種類名" {
		t.Errorf("badge blocked = %v, %v; want [種類名]", blocked, err)
	}
}

// ---- 品番 ------------------------------------------------------------

func TestCreateAndUpdateItem(t *testing.T) {
	d := newDB(t)

	// ブランド・品番が両方空 → エラー
	err := tx(d, func(c *sql.Conn) error { return CreateItem(ctx, c, 1, " ", "", "名前", "") })
	wantAdminError(t, err, "ブランドか品番のどちらかは入力してください。")

	// タイプが31文字 (文字数) → エラー。30文字は可
	long := strings.Repeat("あ", MaxStyle+1)
	err = tx(d, func(c *sql.Conn) error { return CreateItem(ctx, c, 1, "B", "1", "", long) })
	wantAdminError(t, err, "タイプは30文字以内で入力してください。")
	mustTx(t, d, func(c *sql.Conn) error {
		return CreateItem(ctx, c, 1, "B", "1", "", strings.Repeat("あ", MaxStyle))
	})

	// 同じカテゴリーに同じブランド・品番 → 重複エラー (初期データと、新規分の両方)
	err = tx(d, func(c *sql.Conn) error { return CreateItem(ctx, c, 1, "Printstar", "085-CVT", "", "") })
	wantAdminError(t, err, "同じカテゴリーに同じブランド・品番が既にあります。")
	err = tx(d, func(c *sql.Conn) error { return CreateItem(ctx, c, 1, "B", "1", "", "") })
	wantAdminError(t, err, "同じカテゴリーに同じブランド・品番が既にあります。")
	// 別のカテゴリーなら同じブランド・品番でもよい
	mustTx(t, d, func(c *sql.Conn) error { return CreateItem(ctx, c, 2, "Printstar", "085-CVT", "", "") })

	// 更新: 使用停止にできる。空にするとエラー
	id := itemID(t, d, "B")
	it := item(t, d, id)
	mustTx(t, d, func(c *sql.Conn) error { return UpdateItem(ctx, c, id, "B2", "1", it.Name, "", false) })
	got := item(t, d, id)
	if got.Brand != "B2" || got.IsActive {
		t.Errorf("updated = %+v; want brand=B2 / inactive", got)
	}
	err = tx(d, func(c *sql.Conn) error { return UpdateItem(ctx, c, id, "", "", "", "", true) })
	wantAdminError(t, err, "ブランドか品番のどちらかは入力してください。")
}
