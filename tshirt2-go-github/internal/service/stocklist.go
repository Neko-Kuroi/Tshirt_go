package service

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"tshirt2/internal/db"
	"tshirt2/internal/repo"
	"tshirt2/internal/util"
)

// 無地在庫の一覧 (1行 = 1在庫) の、絞り込みと並べ替え。読み取り専用 (Flask 版 services/stocklist.py)。
//
//   - 条件は URL のクエリ文字列から受け取る。不正な値は無視して既定値にする (エラーにしない)。
//   - 並べ替えは、決まったキーだけ許可する (SQL に、ユーザーの入力を並べ替えの指定として入れない)。
//   - 空の値 (タイプ未設定、色なし、サイズなし) は、昇順でも降順でも、いちばん最後に並べる。

// MaxRows は画面に出す最大行数 (それ以上は、絞り込みを促す)。テストで差し替える。
var MaxRows = 2000

const (
	MaxText     = 100 // 絞り込み文字列の最大長 (文字数)
	DefaultSort = "category"
)

// DefaultStyles は管理画面の入力候補 (まだ使われていなくても出す)。
var DefaultStyles = []string{"半袖", "長袖"}

// SortKeys は並べ替えキーと画面の見出し (この順に列が並ぶ)。
var SortKeys = []struct{ Key, Label string }{
	{"category", "カテゴリー"}, {"style", "タイプ"}, {"item", "品番"},
	{"color", "色・種類"}, {"size", "サイズ"}, {"qty", "数量"},
}

func validSort(s string) bool {
	for _, k := range SortKeys {
		if k.Key == s {
			return true
		}
	}
	return false
}

// Filter は安全に正規化された絞り込み条件。
type Filter struct {
	Category              *int64 // nil = 指定なし (不正な値も nil)
	Style, Color, Size, Q string
	InStock               bool
	Sort, Dir             string
}

// ParseFilter はクエリ文字列を安全な絞り込み条件にする (Flask 版 parse)。
// 同じキーが複数あるときは、最初の値を使う (Flask の request.args.get と同じ)。
func ParseFilter(args url.Values) Filter {
	text := func(name string) string {
		return util.TruncateRunes(util.PyStrip(args.Get(name)), MaxText)
	}
	f := Filter{
		Style: text("style"), Color: text("color"), Size: text("size"), Q: text("q"),
		InStock: args.Get("in_stock") == "1",
		Sort:    DefaultSort, Dir: "asc",
	}
	if n, ok := util.ToInt(args.Get("category")); ok {
		f.Category = &n
	}
	if s := args.Get("sort"); validSort(s) {
		f.Sort = s
	}
	if args.Get("dir") == "desc" {
		f.Dir = "desc"
	}
	return f
}

// likePattern は LIKE のワイルドカード (% _) を文字として扱うためのパターン (ESCAPE '\')。
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// StockRow は一覧の1行。
type StockRow struct {
	ID, ItemID               int64
	Color, Size, VariantName string
	Quantity                 int64
	Brand, ItemNo, ItemName  string
	Style                    string
	ItemActive               bool
	CategoryID               int64
	CategoryName             string
	CategoryOrder            int64
}

// ItemLabel は「ブランド 品番 商品名」。
func (r StockRow) ItemLabel() string { return repo.ItemLabel(r.Brand, r.ItemNo, r.ItemName, r.ItemID) }

// Spec は「種類名 / 色」(一覧では、サイズは別の列)。
func (r StockRow) Spec() string {
	var parts []string
	for _, p := range []string{r.VariantName, r.Color} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " / ")
}

const stockListSQL = `
SELECT v.id, v.item_id, v.color, v.size, v.variant_name, v.quantity,
       i.brand, i.item_no, i.name AS item_name, i.style, i.is_active AS item_active,
       c.id AS category_id, c.name AS category_name, c.sort_order AS category_order
FROM variant v
JOIN item i ON i.id = v.item_id
JOIN category c ON c.id = i.category_id
`

func fetchStockRows(ctx context.Context, q db.Querier, f Filter) ([]StockRow, error) {
	var where []string
	var args []any
	if f.Category != nil {
		where = append(where, "c.id = ?")
		args = append(args, *f.Category)
	}
	if f.Style != "" {
		where = append(where, "i.style = ?")
		args = append(args, f.Style)
	}
	if f.Color != "" { // 色のないカテゴリー (バッジなど) は、種類名で探せるようにする
		where = append(where, "(v.color = ? OR v.variant_name = ?)")
		args = append(args, f.Color, f.Color)
	}
	if f.Size != "" {
		where = append(where, "v.size = ?")
		args = append(args, f.Size)
	}
	if f.InStock {
		where = append(where, "v.quantity > 0")
	}
	if f.Q != "" {
		where = append(where, `(i.brand LIKE ? ESCAPE '\' OR i.item_no LIKE ? ESCAPE '\' OR i.name LIKE ? ESCAPE '\')`)
		p := likePattern(f.Q)
		args = append(args, p, p, p)
	}
	query := stockListSQL
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StockRow
	for rows.Next() {
		var r StockRow
		var active int64
		if err := rows.Scan(&r.ID, &r.ItemID, &r.Color, &r.Size, &r.VariantName, &r.Quantity,
			&r.Brand, &r.ItemNo, &r.ItemName, &r.Style, &active,
			&r.CategoryID, &r.CategoryName, &r.CategoryOrder); err != nil {
			return nil, err
		}
		r.ItemActive = active != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func cmpStr(a, b string) int { return strings.Compare(a, b) }

func cmpI64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// firstNonZero は、比較結果を順に見て、最初の非0を返す (Python のタプル比較)。
func firstNonZero(cs ...int) int {
	for _, c := range cs {
		if c != 0 {
			return c
		}
	}
	return 0
}

// defaultCmp は標準の順 (カテゴリー → ブランド → 品番 → 色 → サイズ)。Flask 版 _default_key。
func defaultCmp(a, b *StockRow) int {
	return firstNonZero(
		cmpI64(a.CategoryOrder, b.CategoryOrder), cmpI64(a.CategoryID, b.CategoryID),
		cmpStr(a.Brand, b.Brand), cmpStr(a.ItemNo, b.ItemNo), cmpI64(a.ItemID, b.ItemID),
		cmpStr(a.Color, b.Color), cmpStr(a.VariantName, b.VariantName),
		util.MakeSizeKey(a.Size).Compare(util.MakeSizeKey(b.Size)),
	)
}

// isBlank は、そのキーで「空の値」か (空なら、昇順でも降順でも最後)。Flask 版 _PRIMARY の None。
func isBlank(key string, r *StockRow) bool {
	switch key {
	case "style":
		return r.Style == ""
	case "item":
		return r.Brand == "" && r.ItemNo == ""
	case "color":
		return r.Color == "" && r.VariantName == ""
	case "size":
		return r.Size == ""
	}
	return false // category と qty は常に値がある (qty の 0 も値)
}

func primaryCmp(key string, a, b *StockRow) int {
	switch key {
	case "category":
		return firstNonZero(cmpI64(a.CategoryOrder, b.CategoryOrder), cmpI64(a.CategoryID, b.CategoryID))
	case "style":
		return cmpStr(a.Style, b.Style)
	case "item":
		return firstNonZero(cmpStr(a.Brand, b.Brand), cmpStr(a.ItemNo, b.ItemNo))
	case "color":
		return cmpStr(colorOrName(a), colorOrName(b))
	case "size":
		return util.MakeSizeKey(a.Size).Compare(util.MakeSizeKey(b.Size))
	case "qty":
		return cmpI64(a.Quantity, b.Quantity)
	}
	return 0
}

func colorOrName(r *StockRow) string {
	if r.Color != "" {
		return r.Color
	}
	return r.VariantName
}

// SortRows は、まず標準の順に整え、指定キーで安定ソートする。
// 同じ値の行は標準の順のまま残る。空の値は、昇順でも降順でも最後。
func SortRows(rows []StockRow, key, direction string) []StockRow {
	rows = append([]StockRow(nil), rows...)
	sort.SliceStable(rows, func(i, j int) bool { return defaultCmp(&rows[i], &rows[j]) < 0 })
	var present, blank []StockRow
	for i := range rows {
		if isBlank(key, &rows[i]) {
			blank = append(blank, rows[i])
		} else {
			present = append(present, rows[i])
		}
	}
	desc := direction == "desc"
	sort.SliceStable(present, func(i, j int) bool {
		c := primaryCmp(key, &present[i], &present[j])
		if desc {
			return c > 0
		}
		return c < 0
	})
	return append(present, blank...)
}

// StockListResult は一覧の結果。Count と TotalQty は、表示行数の上限で切り捨てる前の全件分。
type StockListResult struct {
	Rows      []StockRow
	Count     int
	TotalQty  int64
	Truncated bool
}

// SearchStock は絞り込みと並べ替えを行う (Flask 版 search)。
func SearchStock(ctx context.Context, q db.Querier, f Filter) (StockListResult, error) {
	rows, err := fetchStockRows(ctx, q, f)
	if err != nil {
		return StockListResult{}, err
	}
	res := StockListResult{Count: len(rows)}
	for _, r := range rows {
		res.TotalQty += r.Quantity
	}
	rows = SortRows(rows, f.Sort, f.Dir)
	if len(rows) > MaxRows {
		rows = rows[:MaxRows]
	}
	res.Rows, res.Truncated = rows, res.Count > MaxRows
	return res, nil
}

// StockListOptions は絞り込みの選択肢 (いま登録されている値)。
type StockListOptions struct {
	Styles, Colors, Sizes []string
}

func queryStrings(ctx context.Context, q db.Querier, query string) ([]string, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListOptions は絞り込みの選択肢を返す (Flask 版 options)。
func ListOptions(ctx context.Context, q db.Querier) (StockListOptions, error) {
	var o StockListOptions
	var err error
	if o.Styles, err = queryStrings(ctx, q,
		"SELECT DISTINCT style FROM item WHERE style <> '' ORDER BY style"); err != nil {
		return o, err
	}
	if o.Colors, err = queryStrings(ctx, q,
		"SELECT color AS v FROM variant WHERE color <> '' "+
			"UNION SELECT variant_name FROM variant WHERE variant_name <> '' ORDER BY v"); err != nil {
		return o, err
	}
	if o.Sizes, err = queryStrings(ctx, q, "SELECT DISTINCT size FROM variant WHERE size <> ''"); err != nil {
		return o, err
	}
	sort.SliceStable(o.Sizes, func(i, j int) bool {
		return util.MakeSizeKey(o.Sizes[i]).Compare(util.MakeSizeKey(o.Sizes[j])) < 0
	})
	return o, nil
}

// StyleChoices は管理画面で、タイプを入力するときの候補 (既に使われているもの + 半袖・長袖)。
func StyleChoices(ctx context.Context, q db.Querier) ([]string, error) {
	o, err := ListOptions(ctx, q)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, s := range append(append([]string{}, o.Styles...), DefaultStyles...) {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}
