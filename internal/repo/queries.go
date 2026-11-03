package repo

// 画面 (手順4) が使う読み取りクエリ集。Flask 版 app/repo.py の残りに対応する。
// (get_item / get_variant / products など、既にあるものは repo.go 側)

import (
	"context"
	"database/sql"
	"strings"

	"tshirt2/internal/db"
	"tshirt2/internal/util"
)

// KindLabels は履歴の種別の表示名 (Flask 版 models.KINDS)。
var KindLabels = map[string]string{
	"receive":      "入荷",
	"print_use":    "プリント使用",
	"print_output": "プリント完成",
	"ship":         "出荷",
	"adjust":       "棚卸し調整",
}

// ---- カテゴリー ------------------------------------------------------

type Category struct {
	ID, SortOrder                                     int64
	Name                                              string
	UsesColor, UsesSize, UsesVariantName, IsPrintable bool
}

const categorySQL = `SELECT id, name, sort_order, uses_color, uses_size, uses_variant_name, is_printable FROM category`

func scanCategory(s scanner) (Category, error) {
	var c Category
	var uc, us, uv, pr int64
	err := s.Scan(&c.ID, &c.Name, &c.SortOrder, &uc, &us, &uv, &pr)
	c.UsesColor, c.UsesSize, c.UsesVariantName, c.IsPrintable = uc != 0, us != 0, uv != 0, pr != 0
	return c, err
}

func Categories(ctx context.Context, q db.Querier) ([]Category, error) {
	rows, err := q.QueryContext(ctx, categorySQL+" ORDER BY sort_order, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetCategory(ctx context.Context, q db.Querier, id int64) (*Category, error) {
	c, err := scanCategory(q.QueryRowContext(ctx, categorySQL+" WHERE id = ?", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ---- 品番リスト ------------------------------------------------------

func scanItem(s scanner) (Item, error) {
	var it Item
	var id2 int64
	var active, uc, us, uv, pr int64
	err := s.Scan(&it.ID, &id2, &it.CategoryID, &it.Brand, &it.ItemNo, &it.Name, &it.Style, &active,
		&it.CategoryName, &uc, &us, &uv, &pr)
	it.IsActive, it.UsesColor, it.UsesSize = active != 0, uc != 0, us != 0
	it.UsesVariantName, it.IsPrintable = uv != 0, pr != 0
	return it, err
}

func listItems(ctx context.Context, q db.Querier, sqlTail string, args ...any) ([]Item, error) {
	rows, err := q.QueryContext(ctx, itemSQL+sqlTail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ItemsForSelect は入荷画面などの選択肢 (有効な品番のみ)。
func ItemsForSelect(ctx context.Context, q db.Querier) ([]Item, error) {
	return listItems(ctx, q, " WHERE i.is_active = 1 ORDER BY c.sort_order, i.brand, i.item_no")
}

// ItemsInCategory はカテゴリー内の有効な品番。q は brand / item_no / name の部分一致。
func ItemsInCategory(ctx context.Context, q db.Querier, categoryID int64, text string) ([]Item, error) {
	sqlTail := " WHERE i.is_active = 1 AND i.category_id = ?"
	args := []any{categoryID}
	if text != "" {
		like := "%" + text + "%"
		sqlTail += " AND (i.brand LIKE ? OR i.item_no LIKE ? OR i.name LIKE ?)"
		args = append(args, like, like, like)
	}
	return listItems(ctx, q, sqlTail+" ORDER BY i.brand, i.item_no", args...)
}

// ItemsAdmin は管理画面用 (無効な品番も含む)。categoryID = 0 で全カテゴリー。
func ItemsAdmin(ctx context.Context, q db.Querier, categoryID int64) ([]Item, error) {
	if categoryID != 0 {
		return listItems(ctx, q, " WHERE i.category_id = ? ORDER BY c.sort_order, i.brand, i.item_no", categoryID)
	}
	return listItems(ctx, q, " ORDER BY c.sort_order, i.brand, i.item_no")
}

// ---- 無地在庫リスト --------------------------------------------------

func listVariants(ctx context.Context, q db.Querier, sqlTail string, args ...any) ([]Variant, error) {
	rows, err := q.QueryContext(ctx, variantSQL+sqlTail, args...)
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

func VariantsOfItem(ctx context.Context, q db.Querier, itemID int64) ([]Variant, error) {
	return listVariants(ctx, q, " WHERE v.item_id = ? ORDER BY v.variant_name, v.color", itemID)
}

func VariantsInCategory(ctx context.Context, q db.Querier, categoryID int64) ([]Variant, error) {
	return listVariants(ctx, q, " WHERE i.category_id = ? ORDER BY v.variant_name, v.color", categoryID)
}

const variantOrder = " ORDER BY c.sort_order, i.brand, i.item_no, v.color, v.size"

func PrintableVariantsInStock(ctx context.Context, q db.Querier) ([]Variant, error) {
	return listVariants(ctx, q, " WHERE c.is_printable = 1 AND v.quantity > 0 AND i.is_active = 1"+variantOrder)
}

func UsedColors(ctx context.Context, q db.Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT color FROM variant WHERE color <> '' ORDER BY color")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- プリント済み ----------------------------------------------------

type Design struct {
	ID   int64
	Name string
	Note string
}

func Designs(ctx context.Context, q db.Querier) ([]Design, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, name, note FROM design ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Design
	for rows.Next() {
		var d Design
		if err := rows.Scan(&d.ID, &d.Name, &d.Note); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TargetRow はフォームの 'v:12' / 'p:34' から引いた在庫の行。
type TargetRow struct {
	Target  Target
	Variant *Variant // Target.Kind == KindVariant のとき
	Product *Product // Target.Kind == KindProduct のとき
}

func (t *TargetRow) Label() string {
	if t.Variant != nil {
		return t.Variant.Label()
	}
	return t.Product.Label()
}

func GetTarget(ctx context.Context, q db.Querier, raw string) (*TargetRow, error) {
	kind, rid, found := strings.Cut(raw, ":")
	if !found {
		return nil, nil
	}
	id, ok := util.ToInt(rid)
	if !ok {
		return nil, nil
	}
	switch kind {
	case "v":
		v, err := GetVariant(ctx, q, id)
		if v == nil || err != nil {
			return nil, err
		}
		return &TargetRow{Target: v.Target(), Variant: v}, nil
	case "p":
		p, err := GetProduct(ctx, q, id)
		if p == nil || err != nil {
			return nil, err
		}
		return &TargetRow{Target: p.Target(), Product: p}, nil
	}
	return nil, nil
}

// ---- ダッシュボード --------------------------------------------------

func totals(ctx context.Context, q db.Querier, sqlText string) (map[int64]int64, error) {
	rows, err := q.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var cid, total int64
		if err := rows.Scan(&cid, &total); err != nil {
			return nil, err
		}
		out[cid] = total
	}
	return out, rows.Err()
}

func BlankTotals(ctx context.Context, q db.Querier) (map[int64]int64, error) {
	return totals(ctx, q, "SELECT i.category_id, COALESCE(SUM(v.quantity), 0) AS total "+
		"FROM variant v JOIN item i ON i.id = v.item_id GROUP BY i.category_id")
}

func PrintedTotals(ctx context.Context, q db.Querier) (map[int64]int64, error) {
	return totals(ctx, q, "SELECT i.category_id, COALESCE(SUM(p.quantity), 0) AS total "+
		"FROM printed_product p JOIN variant v ON v.id = p.variant_id "+
		"JOIN item i ON i.id = v.item_id GROUP BY i.category_id")
}

// ---- 履歴 ------------------------------------------------------------

type Movement struct {
	ID, ItemID    int64
	BatchID       string
	Kind          string
	Delta         int64
	Note          string
	CreatedAt     string
	Username      string
	DesignName    string
	Brand, ItemNo string
	ItemName      string
	Color, Size   string
	VariantName   string
}

func (m Movement) KindLabel() string {
	if l, ok := KindLabels[m.Kind]; ok {
		return l
	}
	return m.Kind
}

func (m Movement) TargetLabel() string {
	base := ItemLabel(m.Brand, m.ItemNo, m.ItemName, m.ItemID) + " | " +
		SpecOf(m.VariantName, m.Color, m.Size)
	if m.DesignName != "" {
		return m.DesignName + " | " + base
	}
	return base
}

const movementSQL = `
SELECT m.id, m.batch_id, m.kind, m.delta, m.note, m.created_at, u.username,
       d.name AS design_name,
       i.id AS item_id, i.brand, i.item_no, i.name AS item_name,
       COALESCE(v.color, pv.color) AS color,
       COALESCE(v.size, pv.size) AS size,
       COALESCE(v.variant_name, pv.variant_name) AS variant_name
FROM stock_movement m
JOIN "user" u ON u.id = m.user_id
LEFT JOIN variant v ON v.id = m.variant_id
LEFT JOIN printed_product p ON p.id = m.printed_product_id
LEFT JOIN design d ON d.id = p.design_id
LEFT JOIN variant pv ON pv.id = p.variant_id
JOIN item i ON i.id = COALESCE(v.item_id, pv.item_id)
`

func scanMovement(s scanner) (Movement, error) {
	var m Movement
	var design sql.NullString
	err := s.Scan(&m.ID, &m.BatchID, &m.Kind, &m.Delta, &m.Note, &m.CreatedAt, &m.Username,
		&design, &m.ItemID, &m.Brand, &m.ItemNo, &m.ItemName, &m.Color, &m.Size, &m.VariantName)
	m.DesignName = design.String
	return m, err
}

func RecentMovements(ctx context.Context, q db.Querier, limit int64) ([]Movement, error) {
	rows, err := q.QueryContext(ctx, movementSQL+" ORDER BY m.created_at DESC, m.id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movement
	for rows.Next() {
		m, err := scanMovement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type MovementPage struct {
	Items   []Movement
	Page    int64
	HasPrev bool
	HasNext bool
	PrevNum int64
	NextNum int64
}

func MovementsPage(ctx context.Context, q db.Querier, kind string, page, perPage int64) (MovementPage, error) {
	sqlText := movementSQL
	var args []any
	if _, ok := KindLabels[kind]; ok {
		sqlText += " WHERE m.kind = ?"
		args = append(args, kind)
	}
	sqlText += " ORDER BY m.created_at DESC, m.id DESC LIMIT ? OFFSET ?"
	args = append(args, perPage+1, (page-1)*perPage)
	rows, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return MovementPage{}, err
	}
	defer rows.Close()
	var all []Movement
	for rows.Next() {
		m, err := scanMovement(rows)
		if err != nil {
			return MovementPage{}, err
		}
		all = append(all, m)
	}
	if err := rows.Err(); err != nil {
		return MovementPage{}, err
	}
	hasNext := int64(len(all)) > perPage
	items := all
	if hasNext {
		items = all[:perPage]
	}
	return MovementPage{Items: items, Page: page, HasPrev: page > 1, HasNext: hasNext,
		PrevNum: page - 1, NextNum: page + 1}, nil
}

// ---- 備考 (item_note) -------------------------------------------------

type Note struct {
	ID, ItemID   int64
	Target       string
	Body         string
	IsResolved   bool
	CreatedAt    string
	Username     string
	Brand        string
	ItemNo       string
	ItemName     string
	CategoryID   int64
	CategoryName string
}

func (n Note) ItemLabel() string { return ItemLabel(n.Brand, n.ItemNo, n.ItemName, n.ItemID) }

const noteSQL = `
SELECT n.id, n.item_id, n.target, n.body, n.is_resolved, n.created_at, u.username,
       i.brand, i.item_no, i.name AS item_name, i.category_id, c.name AS category_name
FROM item_note n
JOIN "user" u ON u.id = n.user_id
JOIN item i ON i.id = n.item_id
JOIN category c ON c.id = i.category_id
`

func scanNote(s scanner) (Note, error) {
	var n Note
	var res int64
	err := s.Scan(&n.ID, &n.ItemID, &n.Target, &n.Body, &res, &n.CreatedAt, &n.Username,
		&n.Brand, &n.ItemNo, &n.ItemName, &n.CategoryID, &n.CategoryName)
	n.IsResolved = res != 0
	return n, err
}

type NotesFilter struct {
	IncludeResolved bool
	ItemID          int64 // 0 = 指定なし
	CategoryID      int64 // 0 = 指定なし
	Limit           int64 // 0 = 無制限
}

func Notes(ctx context.Context, q db.Querier, f NotesFilter) ([]Note, error) {
	sqlText := noteSQL + " WHERE 1=1"
	var args []any
	if !f.IncludeResolved {
		sqlText += " AND n.is_resolved = 0"
	}
	if f.ItemID != 0 {
		sqlText += " AND n.item_id = ?"
		args = append(args, f.ItemID)
	}
	if f.CategoryID != 0 {
		sqlText += " AND i.category_id = ?"
		args = append(args, f.CategoryID)
	}
	sqlText += " ORDER BY n.is_resolved, n.created_at DESC, n.id DESC"
	if f.Limit != 0 {
		sqlText += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNote は備考の id と item_id だけを返す (操作前の存在確認用)。無ければ nil。
func GetNote(ctx context.Context, q db.Querier, noteID int64) (itemID int64, found bool, err error) {
	err = q.QueryRowContext(ctx, "SELECT item_id FROM item_note WHERE id = ?", noteID).Scan(&itemID)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return itemID, true, nil
}

func OpenNoteCount(ctx context.Context, q db.Querier) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM item_note WHERE is_resolved = 0").Scan(&n)
	return n, err
}

// ---- ユーザー一覧 ------------------------------------------------------

func ListUsers(ctx context.Context, q db.Querier) ([]User, error) {
	rows, err := q.QueryContext(ctx, userSQL+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var active int64
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &active); err != nil {
			return nil, err
		}
		u.IsActive = active != 0
		out = append(out, u)
	}
	return out, rows.Err()
}
