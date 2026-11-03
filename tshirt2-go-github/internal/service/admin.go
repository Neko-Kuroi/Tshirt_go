package service

// 管理画面 (カテゴリー・品番・ユーザー) の書き込み操作 (Flask 版 app/admin/routes.py)。
//
// 検証に失敗した場合は *AdminError (画面に出す文言) を返す。DB の制約違反のうち、
// UNIQUE 違反だけを「重複」の文言に変える (Flask 版 is_unique_violation と同じ)。
// 呼び出し側が db.DB.Tx で包むこと (stock.go と同じ約束)。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"tshirt2/internal/auth"
	"tshirt2/internal/db"
	"tshirt2/internal/repo"
	"tshirt2/internal/util"
)

// AdminError は管理画面でユーザーに見せてよいエラー。
type AdminError struct{ Msg string }

func (e *AdminError) Error() string { return e.Msg }

func adminErr(format string, a ...any) error {
	return &AdminError{Msg: fmt.Sprintf(format, a...)}
}

// IsAdminError は err が AdminError か (errors.As の短縮形)。
func IsAdminError(err error) bool {
	var ae *AdminError
	return errors.As(err, &ae)
}

// isUniqueViolation は、DB のエラーが UNIQUE 制約違反か (Flask 版 utils.is_unique_violation)。
// NOT NULL など他の制約違反を「重複」と誤表示しないために使う。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ---- カテゴリー ------------------------------------------------------

// CategoryFlags はカテゴリーの入力欄の設定 (チェックボックス)。
type CategoryFlags struct {
	UsesColor, UsesSize, UsesVariantName, IsPrintable bool
}

// CreateCategory はカテゴリーを追加する。表示順は、追加前の件数 (Flask 版と同じ)。
func CreateCategory(ctx context.Context, q db.Querier, name string, f CategoryFlags) error {
	name = util.PyStrip(name)
	if name == "" {
		return adminErr("カテゴリー名を入力してください。")
	}
	var count int64
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM category").Scan(&count); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO category (name, sort_order, uses_color, uses_size, uses_variant_name, is_printable) "+
			"VALUES (?, ?, ?, ?, ?, ?)",
		name, count, b2i(f.UsesColor), b2i(f.UsesSize), b2i(f.UsesVariantName), b2i(f.IsPrintable))
	if isUniqueViolation(err) {
		return adminErr("同名のカテゴリーが既にあります。")
	}
	return err
}

// flagColumn は、入力欄の設定 → その設定で使う variant の列と表示名 (Flask 版 _FLAG_COLUMN)。
var flagColumns = []struct {
	column, label string
	on            func(*repo.Category) bool
	wanted        func(CategoryFlags) bool
}{
	{"color", "色", func(c *repo.Category) bool { return c.UsesColor }, func(f CategoryFlags) bool { return f.UsesColor }},
	{"size", "サイズ", func(c *repo.Category) bool { return c.UsesSize }, func(f CategoryFlags) bool { return f.UsesSize }},
	{"variant_name", "種類名", func(c *repo.Category) bool { return c.UsesVariantName }, func(f CategoryFlags) bool { return f.UsesVariantName }},
}

// FlagsInUse は、OFF にしようとしているのに、既に値が入っている設定の表示名を返す
// (Flask 版 _flags_in_use)。順序は色・サイズ・種類名。
func FlagsInUse(ctx context.Context, q db.Querier, cat *repo.Category, f CategoryFlags) ([]string, error) {
	var blocked []string
	for _, fc := range flagColumns {
		if !fc.on(cat) || fc.wanted(f) {
			continue
		}
		var n int64
		err := q.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM variant v JOIN item i ON i.id = v.item_id "+
				"WHERE i.category_id = ? AND v."+fc.column+" <> ''", cat.ID).Scan(&n)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			blocked = append(blocked, fc.label)
		}
	}
	return blocked, nil
}

// UpdateCategory はカテゴリーを更新する。名前が空なら、今の名前のまま (Flask 版と同じ)。
func UpdateCategory(ctx context.Context, q db.Querier, cat *repo.Category, name string, sortOrder int64, f CategoryFlags) error {
	name = util.PyStrip(name)
	if name == "" {
		name = cat.Name
	}
	_, err := q.ExecContext(ctx,
		"UPDATE category SET name=?, sort_order=?, uses_color=?, uses_size=?, "+
			"uses_variant_name=?, is_printable=? WHERE id=?",
		name, sortOrder, b2i(f.UsesColor), b2i(f.UsesSize), b2i(f.UsesVariantName), b2i(f.IsPrintable), cat.ID)
	if isUniqueViolation(err) {
		return adminErr("同名のカテゴリーが既にあります。")
	}
	return err
}

// ---- 品番 ------------------------------------------------------------

// MaxStyle はタイプの最大の長さ (文字数。Flask 版 MAX_STYLE)。
const MaxStyle = 30

// styleTooLong は Python の len() (コードポイント数) で数える。
func styleTooLong(style string) bool { return utf8.RuneCountInString(style) > MaxStyle }

func checkItemFields(brand, itemNo, style string) error {
	if brand == "" && itemNo == "" {
		return adminErr("ブランドか品番のどちらかは入力してください。")
	}
	if styleTooLong(style) {
		return adminErr("タイプは%d文字以内で入力してください。", MaxStyle)
	}
	return nil
}

// CreateItem は品番を追加する (備考は空、使用中で登録。Flask 版 items の POST)。
func CreateItem(ctx context.Context, q db.Querier, categoryID int64, brand, itemNo, name, style string) error {
	brand, itemNo, name, style = util.PyStrip(brand), util.PyStrip(itemNo), util.PyStrip(name), util.PyStrip(style)
	if err := checkItemFields(brand, itemNo, style); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO item (category_id, brand, item_no, name, style, note, is_active) "+
			"VALUES (?, ?, ?, ?, ?, '', 1)",
		categoryID, brand, itemNo, name, style)
	if isUniqueViolation(err) {
		return adminErr("同じカテゴリーに同じブランド・品番が既にあります。")
	}
	return err
}

// UpdateItem は品番を更新する (カテゴリーは変えられない。Flask 版 item_edit の POST)。
func UpdateItem(ctx context.Context, q db.Querier, itemID int64, brand, itemNo, name, style string, isActive bool) error {
	brand, itemNo, name, style = util.PyStrip(brand), util.PyStrip(itemNo), util.PyStrip(name), util.PyStrip(style)
	if err := checkItemFields(brand, itemNo, style); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		"UPDATE item SET brand=?, item_no=?, name=?, style=?, is_active=? WHERE id=?",
		brand, itemNo, name, style, b2i(isActive), itemID)
	if isUniqueViolation(err) {
		return adminErr("同じカテゴリーに同じブランド・品番が既にあります。")
	}
	return err
}

// ---- ユーザー --------------------------------------------------------

// CreateUser はユーザーを追加する (Flask 版 users の POST)。
// role は "admin" のときだけ管理者、それ以外はスタッフ。パスワードの長さは文字数で数える。
func CreateUser(ctx context.Context, q db.Querier, username, password, role string) error {
	username = util.PyStrip(username)
	if username == "" || utf8.RuneCountInString(password) < MinPasswordLen {
		return adminErr("ユーザー名と、%d文字以上のパスワードを入力してください。", MinPasswordLen)
	}
	if role != "admin" {
		role = "staff"
	}
	hash, err := auth.Hash(password)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO "user" (username, password_hash, role, is_active, created_at) `+
			"VALUES (?, ?, ?, 1, datetime('now'))",
		username, hash, role)
	if isUniqueViolation(err) {
		return adminErr("そのユーザー名は既に使われています。")
	}
	return err
}

// SetUserActive はユーザーの有効/無効を切り替える (Flask 版 user_toggle の書き込み部分)。
func SetUserActive(ctx context.Context, q db.Querier, userID int64, active bool) error {
	_, err := q.ExecContext(ctx, `UPDATE "user" SET is_active = ? WHERE id = ?`, b2i(active), userID)
	return err
}

// ResetUserPassword はユーザーのパスワードを再設定する (Flask 版 user_password)。
// ハッシュは Werkzeug 互換の scrypt 形式なので、Flask 版でもそのままログインできる。
func ResetUserPassword(ctx context.Context, q db.Querier, userID int64, password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return adminErr("パスワードは%d文字以上にしてください。", MinPasswordLen)
	}
	hash, err := auth.Hash(password)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE "user" SET password_hash = ? WHERE id = ?`, hash, userID)
	return err
}
