// Package service は業務ロジック (Flask 版 app/services/)。
//
// 在庫の増減は必ずここの関数を使う (画面は quantity を直接書き換えない)。
// 呼び出し側が db.DB.Tx で包むこと (1リクエスト = 1トランザクション)。
// 失敗時は *StockError (ユーザーに見せてよい文言) か、DB のエラーを返す。
// Tx が全体をロールバックするので、途中まで成功した状態は残らない。
package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"tshirt2/internal/db"
	"tshirt2/internal/repo"
	"tshirt2/internal/util"
)

// StockError はユーザーに見せてよいエラー (Flask 版 StockError)。
type StockError struct{ Msg string }

func (e *StockError) Error() string { return e.Msg }

func stockErr(format string, a ...any) error { return &StockError{Msg: fmt.Sprintf(format, a...)} }

// IsStockError は err が StockError か (errors.As の短縮形)。
func IsStockError(err error) bool {
	var se *StockError
	return errors.As(err, &se)
}

// 履歴の種類 (stock_movement.kind)。
const (
	KindReceive     = "receive"
	KindPrintUse    = "print_use"
	KindPrintOutput = "print_output"
	KindShip        = "ship"
	KindAdjust      = "adjust"
)

var tableOf = map[repo.TargetKind]string{
	repo.KindVariant: "variant",
	repo.KindProduct: "printed_product",
}

// formatQty は Python の f"{n:,}" (3桁区切り)。
func formatQty(n int64) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// newUUID は UUID v4 (Python の str(uuid.uuid4()) と同じ形式)。
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // 乱数が取れない環境では続行できない
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// apply は在庫を原子的に増減する。マイナスになる場合は更新せず StockError。
func apply(ctx context.Context, q db.Querier, kind repo.TargetKind, rowID, delta int64) error {
	res, err := q.ExecContext(ctx,
		fmt.Sprintf("UPDATE %s SET quantity = quantity + ?, updated_at = datetime('now') "+
			"WHERE id = ? AND quantity + ? >= 0", tableOf[kind]),
		delta, rowID, delta)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return stockErr("在庫が足りません。")
	}
	return nil
}

func logMovement(ctx context.Context, q db.Querier, batchID, kind string, userID, delta int64, note string, t repo.Target) error {
	var variantID, productID any
	if t.Kind == repo.KindVariant {
		variantID = t.ID
	} else {
		productID = t.ID
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO stock_movement (batch_id, kind, variant_id, printed_product_id, delta, note, "+
			"user_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))",
		batchID, kind, variantID, productID, delta, util.PyStrip(note), userID)
	return err
}

func positive(n int64, label string) error {
	if n <= 0 {
		return stockErr("%sは1以上の整数で入力してください。", label)
	}
	if n > util.MaxQty {
		return stockErr("%sは%s以下で入力してください。", label, formatQty(util.MaxQty))
	}
	return nil
}

// GetOrCreateVariant は、品番・色・サイズ・種類名に対応する variant を返す (無ければ作る)。
// カテゴリー設定に合わない列は空文字に落として登録する。
func GetOrCreateVariant(ctx context.Context, q db.Querier, item *repo.Item, color, size, variantName string) (*repo.Variant, error) {
	if !item.IsActive {
		return nil, stockErr("この品番は使用停止中のため、入荷できません。")
	}
	norm := func(use bool, s string) string {
		if use {
			return util.PyStrip(s)
		}
		return ""
	}
	color = norm(item.UsesColor, color)
	size = norm(item.UsesSize, size)
	variantName = norm(item.UsesVariantName, variantName)
	if item.UsesColor && color == "" {
		return nil, stockErr("色を入力してください。")
	}
	if item.UsesSize && size == "" {
		return nil, stockErr("サイズを入力してください。")
	}
	if item.UsesVariantName && variantName == "" {
		return nil, stockErr("種類名を入力してください。")
	}
	v, err := repo.FindVariant(ctx, q, item.ID, color, size, variantName)
	if err != nil || v != nil {
		return v, err
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO variant (item_id, color, size, variant_name, quantity, updated_at) "+
			"VALUES (?, ?, ?, ?, 0, datetime('now'))", item.ID, color, size, variantName)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return repo.GetVariant(ctx, q, id)
}

// GetOrCreateDesign はデザイン名から design の id を返す (無ければ作る)。
func GetOrCreateDesign(ctx context.Context, q db.Querier, name string) (int64, error) {
	name = util.PyStrip(name)
	if name == "" {
		return 0, stockErr("デザイン名を入力してください。")
	}
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM design WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := q.ExecContext(ctx, "INSERT INTO design (name, note) VALUES (?, '')", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetOrCreateProduct は (デザイン, variant) に対応するプリント済み在庫を返す (無ければ作る)。
func GetOrCreateProduct(ctx context.Context, q db.Querier, designID, variantID int64) (*repo.Product, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM printed_product WHERE design_id = ? AND variant_id = ?", designID, variantID).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := q.ExecContext(ctx,
			"INSERT INTO printed_product (design_id, variant_id, quantity, updated_at) "+
				"VALUES (?, ?, 0, datetime('now'))", designID, variantID)
		if err != nil {
			return nil, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return repo.GetProduct(ctx, q, id)
}

// ---- 操作 -------------------------------------------------------------

// Receive は入荷。batch_id を返す。
func Receive(ctx context.Context, q db.Querier, userID int64, v *repo.Variant, qty int64, note string) (string, error) {
	if err := positive(qty, "数量"); err != nil {
		return "", err
	}
	batch := newUUID()
	if err := apply(ctx, q, repo.KindVariant, v.ID, qty); err != nil {
		return "", err
	}
	return batch, logMovement(ctx, q, batch, KindReceive, userID, qty, note, v.Target())
}

// Ship は出荷 (無地・プリント済みのどちらも対象にできる)。
func Ship(ctx context.Context, q db.Querier, userID int64, t repo.Target, qty int64, note string) (string, error) {
	if err := positive(qty, "数量"); err != nil {
		return "", err
	}
	batch := newUUID()
	if err := apply(ctx, q, t.Kind, t.ID, -qty); err != nil {
		return "", err
	}
	return batch, logMovement(ctx, q, batch, KindShip, userID, -qty, note, t)
}

// Adjust は棚卸し調整。増減どちらも可。理由 (note) は必須。
func Adjust(ctx context.Context, q db.Querier, userID int64, t repo.Target, delta int64, note string) (string, error) {
	if delta == 0 {
		return "", stockErr("増減数は0以外の整数で入力してください。")
	}
	if delta > util.MaxQty || delta < -util.MaxQty {
		return "", stockErr("増減数は%s以下で入力してください。", formatQty(util.MaxQty))
	}
	if util.PyStrip(note) == "" {
		return "", stockErr("棚卸し調整には理由を入力してください。")
	}
	batch := newUUID()
	if err := apply(ctx, q, t.Kind, t.ID, delta); err != nil {
		return "", err
	}
	return batch, logMovement(ctx, q, batch, KindAdjust, userID, delta, note, t)
}

// ConvertToPrinted は無地在庫 → プリント済み製品。used 枚消費して output 枚完成
// (ミスプリントで差が出てよい)。無地の減と製品の増は同じ batch_id で履歴に残る。
func ConvertToPrinted(ctx context.Context, q db.Querier, userID int64, v *repo.Variant, designName string, used, output int64, note string) (string, error) {
	if err := positive(used, "使用数"); err != nil {
		return "", err
	}
	if output < 0 {
		return "", stockErr("完成数は0以上の整数で入力してください。")
	}
	if output > used {
		return "", stockErr("完成数が使用数を超えています。")
	}
	if !v.IsPrintable {
		return "", stockErr("このカテゴリーはプリント対象ではありません。")
	}
	if !v.ItemActive {
		return "", stockErr("この品番は使用停止中のため、プリント変換できません。")
	}

	designID, err := GetOrCreateDesign(ctx, q, designName)
	if err != nil {
		return "", err
	}
	batch := newUUID()

	if err := apply(ctx, q, repo.KindVariant, v.ID, -used); err != nil {
		return "", err
	}
	if err := logMovement(ctx, q, batch, KindPrintUse, userID, -used, note, v.Target()); err != nil {
		return "", err
	}
	if output > 0 {
		p, err := GetOrCreateProduct(ctx, q, designID, v.ID)
		if err != nil {
			return "", err
		}
		if err := apply(ctx, q, repo.KindProduct, p.ID, output); err != nil {
			return "", err
		}
		if err := logMovement(ctx, q, batch, KindPrintOutput, userID, output, note, p.Target()); err != nil {
			return "", err
		}
	}
	return batch, nil
}
