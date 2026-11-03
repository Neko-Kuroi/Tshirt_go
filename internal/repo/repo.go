// Package repo は読み取り用のクエリ集 (Flask 版 app/repo.py)。
//
// 手順2では、サービス層 (在庫操作・一覧・CSV) が使う分だけを移植している。
// ダッシュボード・履歴・備考・管理画面用のクエリは、画面と一緒に追加する。
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"tshirt2/internal/db"
	"tshirt2/internal/util"
)

type TargetKind string

const (
	KindVariant TargetKind = "variant" // 無地在庫 (variant)
	KindProduct TargetKind = "product" // プリント済み在庫 (printed_product)
)

// Target は在庫操作 (出荷・調整) の対象。Flask 版では、kind と id を持つ行がこれに当たる。
type Target struct {
	Kind TargetKind
	ID   int64
}

// ItemLabel は Flask 版 item_label。「ブランド 品番 商品名」。全部空なら Item#ID。
func ItemLabel(brand, itemNo, itemName string, itemID int64) string {
	var head []string
	for _, p := range []string{brand, itemNo} {
		if p != "" {
			head = append(head, p)
		}
	}
	s := strings.TrimFunc(strings.Join(head, " ")+" "+itemName, util.IsPySpace)
	if s == "" {
		return fmt.Sprintf("Item#%d", itemID)
	}
	return s
}

// SpecOf は Flask 版 spec_of。「種類名 / 色 / サイズ」。全部空なら "-"。
func SpecOf(variantName, color, size string) string {
	var parts []string
	for _, p := range []string{variantName, color, size} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " / ")
}

// ---- 品番 ------------------------------------------------------------

type Item struct {
	ID, CategoryID                                    int64
	Brand, ItemNo, Name, Style                        string
	IsActive                                          bool
	CategoryName                                      string
	UsesColor, UsesSize, UsesVariantName, IsPrintable bool
}

func (i Item) Label() string { return ItemLabel(i.Brand, i.ItemNo, i.Name, i.ID) }

const itemSQL = `
SELECT i.id, i.id AS item_id, i.category_id, i.brand, i.item_no, i.name AS item_name, i.style, i.is_active,
       c.name AS category_name, c.uses_color, c.uses_size, c.uses_variant_name, c.is_printable
FROM item i JOIN category c ON c.id = i.category_id
`

// GetItem は品番を返す。無ければ nil。
func GetItem(ctx context.Context, q db.Querier, itemID int64) (*Item, error) {
	var it Item
	var id2 int64
	var active, uc, us, uv, pr int64
	err := q.QueryRowContext(ctx, itemSQL+" WHERE i.id = ?", itemID).Scan(
		&it.ID, &id2, &it.CategoryID, &it.Brand, &it.ItemNo, &it.Name, &it.Style, &active,
		&it.CategoryName, &uc, &us, &uv, &pr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	it.IsActive, it.UsesColor, it.UsesSize = active != 0, uc != 0, us != 0
	it.UsesVariantName, it.IsPrintable = uv != 0, pr != 0
	return &it, nil
}

// ---- 無地在庫 (variant) -----------------------------------------------

type Variant struct {
	ID, ItemID               int64
	Color, Size, VariantName string
	Quantity                 int64
	Brand, ItemNo, ItemName  string
	ItemActive               bool
	CategoryID               int64
	CategoryName             string
	CategoryOrder            int64
	IsPrintable              bool
}

func (v Variant) ItemLabel() string { return ItemLabel(v.Brand, v.ItemNo, v.ItemName, v.ItemID) }
func (v Variant) Spec() string      { return SpecOf(v.VariantName, v.Color, v.Size) }
func (v Variant) Label() string     { return v.ItemLabel() + " | " + v.Spec() }
func (v Variant) Target() Target    { return Target{KindVariant, v.ID} }

const variantSQL = `
SELECT v.id, v.item_id, v.color, v.size, v.variant_name, v.quantity,
       i.brand, i.item_no, i.name AS item_name, i.is_active AS item_active,
       i.category_id, c.name AS category_name, c.sort_order AS category_order, c.is_printable
FROM variant v
JOIN item i ON i.id = v.item_id
JOIN category c ON c.id = i.category_id
`

type scanner interface{ Scan(dest ...any) error }

func scanVariant(s scanner) (Variant, error) {
	var v Variant
	var active, printable int64
	err := s.Scan(&v.ID, &v.ItemID, &v.Color, &v.Size, &v.VariantName, &v.Quantity,
		&v.Brand, &v.ItemNo, &v.ItemName, &active,
		&v.CategoryID, &v.CategoryName, &v.CategoryOrder, &printable)
	v.ItemActive, v.IsPrintable = active != 0, printable != 0
	return v, err
}

func oneVariant(ctx context.Context, q db.Querier, where string, args ...any) (*Variant, error) {
	v, err := scanVariant(q.QueryRowContext(ctx, variantSQL+where, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetVariant は variant を返す。無ければ nil。
func GetVariant(ctx context.Context, q db.Querier, id int64) (*Variant, error) {
	return oneVariant(ctx, q, " WHERE v.id = ?", id)
}

// FindVariant は (品番, 色, サイズ, 種類名) が一致する variant を返す。無ければ nil。
func FindVariant(ctx context.Context, q db.Querier, itemID int64, color, size, variantName string) (*Variant, error) {
	return oneVariant(ctx, q, " WHERE v.item_id=? AND v.color=? AND v.size=? AND v.variant_name=?",
		itemID, color, size, variantName)
}

// AllVariants は全ての variant (Flask 版 all_variants)。
func AllVariants(ctx context.Context, q db.Querier) ([]Variant, error) {
	rows, err := q.QueryContext(ctx,
		variantSQL+" ORDER BY c.sort_order, i.brand, i.item_no, v.color, v.size")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Variant
	for rows.Next() {
		v, err := scanVariant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- プリント済み在庫 (printed_product) -----------------------------------

type Product struct {
	ID, DesignID, VariantID  int64
	Quantity                 int64
	DesignName               string
	Color, Size, VariantName string
	ItemID                   int64
	Brand, ItemNo, ItemName  string
	CategoryName             string
	CategoryOrder            int64
}

func (p Product) ItemLabel() string { return ItemLabel(p.Brand, p.ItemNo, p.ItemName, p.ItemID) }
func (p Product) Spec() string      { return SpecOf(p.VariantName, p.Color, p.Size) }
func (p Product) Label() string     { return p.DesignName + " | " + p.ItemLabel() + " | " + p.Spec() }
func (p Product) Target() Target    { return Target{KindProduct, p.ID} }

const productSQL = `
SELECT p.id, p.design_id, p.variant_id, p.quantity, d.name AS design_name,
       v.color, v.size, v.variant_name,
       i.id AS item_id, i.brand, i.item_no, i.name AS item_name,
       c.name AS category_name, c.sort_order AS category_order
FROM printed_product p
JOIN design d ON d.id = p.design_id
JOIN variant v ON v.id = p.variant_id
JOIN item i ON i.id = v.item_id
JOIN category c ON c.id = i.category_id
`

func scanProduct(s scanner) (Product, error) {
	var p Product
	err := s.Scan(&p.ID, &p.DesignID, &p.VariantID, &p.Quantity, &p.DesignName,
		&p.Color, &p.Size, &p.VariantName,
		&p.ItemID, &p.Brand, &p.ItemNo, &p.ItemName,
		&p.CategoryName, &p.CategoryOrder)
	return p, err
}

// GetProduct はプリント済み在庫を返す。無ければ nil。
func GetProduct(ctx context.Context, q db.Querier, id int64) (*Product, error) {
	p, err := scanProduct(q.QueryRowContext(ctx, productSQL+" WHERE p.id = ?", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Products はプリント済み在庫の一覧 (Flask 版 products)。q が空でなければ、LIKE で絞る。
func Products(ctx context.Context, q db.Querier, text string) ([]Product, error) {
	query, args := productSQL, []any(nil)
	if text != "" {
		like := "%" + text + "%"
		query += " WHERE d.name LIKE ? OR i.brand LIKE ? OR i.item_no LIKE ? OR i.name LIKE ? OR v.color LIKE ?"
		args = []any{like, like, like, like, like}
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY d.name, i.brand, i.item_no", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
