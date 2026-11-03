package service

import (
	"context"
	"database/sql"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"tshirt2/internal/db"
	"tshirt2/internal/repo"
)

// ---- Flask 版 tests/test_stock_list.py の、サービス層で成り立つ部分 -------------------------

func setStyle(t *testing.T, d *db.DB, brand, style string) {
	t.Helper()
	mustExec(t, d, "UPDATE item SET style = ? WHERE brand = ?", style, brand)
}

// stocked は、半袖 (Printstar)・長袖 (United Athle)・タイプ未設定のバッジを用意する (Flask 版の fixture)。
func stocked(t *testing.T) *db.DB {
	t.Helper()
	d := newDB(t)
	setStyle(t, d, "Printstar", "半袖")
	setStyle(t, d, "United Athle", "長袖")
	for _, s := range []struct {
		size string
		qty  int64
	}{{"XL", 4}, {"S", 10}, {"L", 1}, {"M", 7}} {
		receive(t, d, "Printstar", "黒", s.size, s.qty)
	}
	// 在庫0の行を作る (入荷0は拒否されるので、出荷で0にする)
	l, err := repo.FindVariant(ctx, d, itemID(t, d, "Printstar"), "黒", "L", "")
	if err != nil || l == nil {
		t.Fatal(err)
	}
	mustTx(t, d, func(c *sql.Conn) error { _, err := Ship(ctx, c, adminID, l.Target(), 1, ""); return err })
	receive(t, d, "Printstar", "白", "M", 2)
	receive(t, d, "United Athle", "黒", "M", 5)
	receive(t, d, "United Athle", "赤", "L", 1)
	receive(t, d, "", "", "", 9, "赤")
	return d
}

func query(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v
}

func search(t *testing.T, q db.Querier, kv ...string) StockListResult {
	t.Helper()
	res, err := SearchStock(ctx, q, ParseFilter(query(kv...)))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func ids(rows []StockRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func colorSize(rows []StockRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Color + "/" + r.Size
	}
	return out
}

func TestAllRowsAreListedIncludingZeroStock(t *testing.T) {
	d := stocked(t)
	res := search(t, d)
	if len(res.Rows) != 8 || res.Count != 8 {
		t.Fatalf("rows = %d, count = %d; want 8 (在庫0の行も含む)", len(res.Rows), res.Count)
	}
	zero := 0
	for _, r := range res.Rows {
		if r.Quantity == 0 {
			zero++
		}
	}
	if zero != 1 {
		t.Errorf("zero-stock rows = %d, want 1", zero)
	}
}

func TestDefaultOrderIsCategoryBrandItemColorSize(t *testing.T) {
	d := stocked(t)
	res := search(t, d)
	var printstar []StockRow
	for _, r := range res.Rows {
		if r.Brand == "Printstar" {
			printstar = append(printstar, r)
		}
	}
	want := []string{"白/M", "黒/S", "黒/M", "黒/L", "黒/XL"}
	if got := colorSize(printstar); !reflect.DeepEqual(got, want) {
		t.Errorf("Printstar rows = %v, want %v", got, want)
	}
	for i, r := range res.Rows {
		wantCat := "Tシャツ"
		if i == 7 {
			wantCat = "バッジ"
		}
		if r.CategoryName != wantCat {
			t.Errorf("row %d category = %q, want %q", i, r.CategoryName, wantCat)
		}
	}
}

func TestFilterByStyleIsWhatMakesSleeveLengthWork(t *testing.T) {
	d := stocked(t)
	short := search(t, d, "style", "半袖")
	if len(short.Rows) != 5 {
		t.Fatalf("半袖 rows = %d, want 5", len(short.Rows))
	}
	for _, r := range short.Rows {
		if r.Brand != "Printstar" || r.Style != "半袖" {
			t.Errorf("unexpected row %+v", r)
		}
	}
	long := search(t, d, "style", "長袖")
	if len(long.Rows) != 2 {
		t.Fatalf("長袖 rows = %d, want 2", len(long.Rows))
	}
	for _, r := range long.Rows {
		if r.Brand != "United Athle" {
			t.Errorf("unexpected row %+v", r)
		}
	}
	if res := search(t, d, "style", "存在しない"); len(res.Rows) != 0 || res.Count != 0 {
		t.Errorf("unknown style returned %d rows", len(res.Rows))
	}
}

func TestFilterByCategoryColorSizeAndCombination(t *testing.T) {
	d := stocked(t)
	for _, r := range search(t, d, "category", "2").Rows {
		if r.CategoryName != "バッジ" {
			t.Errorf("category=2 returned %q", r.CategoryName)
		}
	}
	if n := len(search(t, d, "category", "2").Rows); n != 1 {
		t.Errorf("category=2 rows = %d, want 1", n)
	}
	for _, r := range search(t, d, "color", "白").Rows {
		if r.Color != "白" {
			t.Errorf("color=白 returned %q", r.Color)
		}
	}
	m := search(t, d, "size", "M")
	if len(m.Rows) != 3 {
		t.Errorf("size=M rows = %d, want 3", len(m.Rows))
	}
	both := search(t, d, "style", "半袖", "color", "黒", "size", "M")
	if len(both.Rows) != 1 || both.Rows[0].Quantity != 7 {
		t.Errorf("combination = %+v, want 1 row with quantity 7", both.Rows)
	}
}

func TestColorFilterAlsoFindsBadgeVariantName(t *testing.T) {
	d := stocked(t)
	cats := map[string]bool{}
	for _, r := range search(t, d, "color", "赤").Rows { // Tシャツの赤 + バッジの種類名「赤」
		cats[r.CategoryName] = true
	}
	if !cats["Tシャツ"] || !cats["バッジ"] || len(cats) != 2 {
		t.Errorf("categories = %v, want Tシャツ and バッジ", cats)
	}
}

func TestInStockOnlyAndTextSearch(t *testing.T) {
	d := stocked(t)
	in := search(t, d, "in_stock", "1")
	if len(in.Rows) != 7 { // 在庫0の1行が消える
		t.Fatalf("in_stock rows = %d, want 7", len(in.Rows))
	}
	for _, r := range in.Rows {
		if r.Quantity == 0 {
			t.Error("in_stock returned a zero-stock row")
		}
	}
	if n := len(search(t, d, "q", "United").Rows); n != 2 {
		t.Errorf("q=United rows = %d, want 2", n)
	}
	if n := len(search(t, d, "q", "085").Rows); n != 5 {
		t.Errorf("q=085 rows = %d, want 5", n)
	}
}

func TestLikeWildcardsInSearchAreLiteral(t *testing.T) {
	d := stocked(t)
	for _, q := range []string{"%", "_"} {
		if n := len(search(t, d, "q", q).Rows); n != 0 {
			t.Errorf("q=%q matched %d rows; wildcards must be literal", q, n)
		}
	}
	// 文字として含む品番は、そのまま見つかる
	mustExec(t, d, "INSERT INTO item (category_id, brand, item_no, name) VALUES (1, '100%_cotton', 'A\\B', 'x')")
	receive(t, d, "100%_cotton", "黒", "M", 1)
	for _, q := range []string{"%", "_", "0%_c", `A\B`, `\`} {
		if n := len(search(t, d, "q", q).Rows); n != 1 {
			t.Errorf("q=%q matched %d rows, want 1 (リテラルとして)", q, n)
		}
	}
}

func TestSortBySizeIsNaturalBothDirections(t *testing.T) {
	d := stocked(t)
	base := []string{"category", "1", "style", "半袖", "color", "黒", "sort", "size"}
	asc := colorSize(search(t, d, append(base, "dir", "asc")...).Rows)
	if want := []string{"黒/S", "黒/M", "黒/L", "黒/XL"}; !reflect.DeepEqual(asc, want) {
		t.Errorf("asc = %v, want %v", asc, want)
	}
	desc := colorSize(search(t, d, append(base, "dir", "desc")...).Rows)
	if want := []string{"黒/XL", "黒/L", "黒/M", "黒/S"}; !reflect.DeepEqual(desc, want) {
		t.Errorf("desc = %v, want %v", desc, want)
	}
}

func TestSortByQuantity(t *testing.T) {
	d := stocked(t)
	qtys := func(res StockListResult) []int64 {
		out := make([]int64, len(res.Rows))
		for i, r := range res.Rows {
			out[i] = r.Quantity
		}
		return out
	}
	asc := qtys(search(t, d, "sort", "qty", "dir", "asc"))
	if !sort.SliceIsSorted(asc, func(i, j int) bool { return asc[i] < asc[j] }) || asc[0] != 0 {
		t.Errorf("asc = %v", asc)
	}
	desc := qtys(search(t, d, "sort", "qty", "dir", "desc"))
	if !sort.SliceIsSorted(desc, func(i, j int) bool { return desc[i] > desc[j] }) || desc[0] != 10 {
		t.Errorf("desc = %v", desc)
	}
}

func TestBlankValuesAlwaysSortLast(t *testing.T) {
	d := stocked(t)
	for _, dir := range []string{"asc", "desc"} {
		rows := search(t, d, "sort", "style", "dir", dir).Rows
		if rows[len(rows)-1].Style != "" {
			t.Errorf("%s: last style = %q, want blank (タイプ未設定のバッジ)", dir, rows[len(rows)-1].Style)
		}
		for _, r := range rows[:len(rows)-1] {
			if r.Style == "" {
				t.Errorf("%s: a blank style sorted before a set one", dir)
			}
		}
		rows = search(t, d, "sort", "size", "dir", dir).Rows
		if rows[len(rows)-1].Size != "" {
			t.Errorf("%s: last size = %q, want blank (サイズなしのバッジ)", dir, rows[len(rows)-1].Size)
		}
	}
}

func TestSortByStyleGroupsAndKeepsDefaultOrderInside(t *testing.T) {
	d := stocked(t)
	rows := search(t, d, "sort", "style", "dir", "asc").Rows
	for i, r := range rows[:5] {
		if r.Style != "半袖" {
			t.Errorf("row %d style = %q, want 半袖", i, r.Style)
		}
	}
	for i, r := range rows[5:7] {
		if r.Style != "長袖" {
			t.Errorf("row %d style = %q, want 長袖", i+5, r.Style)
		}
	}
	want := []string{"白/M", "黒/S", "黒/M", "黒/L", "黒/XL"}
	if got := colorSize(rows[:5]); !reflect.DeepEqual(got, want) {
		t.Errorf("inside 半袖 = %v, want %v (標準の順のまま)", got, want)
	}
}

func TestSortByCategoryDesc(t *testing.T) {
	d := stocked(t)
	if first := search(t, d, "sort", "category", "dir", "desc").Rows[0]; first.CategoryName != "バッジ" {
		t.Errorf("first = %q, want バッジ", first.CategoryName)
	}
}

func TestBadInputNeverBreaksAndFallsBack(t *testing.T) {
	d := stocked(t)
	big := "1" + strings.Repeat("0", 30)
	bad := []url.Values{
		{"sort": {"evil;DROP TABLE item"}}, {"sort": {""}}, {"dir": {"sideways"}}, {"category": {"abc"}},
		{"category": {big}}, {"category": {"-1"}}, {"style": {strings.Repeat("あ", 5000)}},
		{"q": {strings.Repeat("x", 5000)}}, {"color": {"\x00"}}, {"in_stock": {"yes"}},
		{"sort": {"size", "qty"}}, {"_external": {"1"}, "endpoint": {"x"}},
	}
	for _, args := range bad {
		if _, err := SearchStock(ctx, d, ParseFilter(args)); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	// 不正な並べ替え指定は、既定の順になる
	def := ids(search(t, d).Rows)
	if got := ids(search(t, d, "sort", "nonsense", "dir", "sideways").Rows); !reflect.DeepEqual(got, def) {
		t.Errorf("bad sort order %v != default %v", got, def)
	}
	// 同じキーが複数あるときは、最初の値 (Flask の args.get と同じ)
	if got := ParseFilter(url.Values{"sort": {"size", "qty"}}).Sort; got != "size" {
		t.Errorf("repeated sort = %q, want size", got)
	}
}

func TestParseFilter(t *testing.T) {
	f := ParseFilter(url.Values{})
	if f.Category != nil || f.Sort != "category" || f.Dir != "asc" || f.InStock || f.Style != "" {
		t.Errorf("defaults wrong: %+v", f)
	}
	f = ParseFilter(query("style", "  半袖 ", "in_stock", "1", "dir", "desc", "sort", "qty", "category", "１"))
	if f.Style != "半袖" || !f.InStock || f.Dir != "desc" || f.Sort != "qty" || f.Category == nil || *f.Category != 1 {
		t.Errorf("parsed wrong: %+v (category=%v)", f, f.Category)
	}
	if n := len([]rune(ParseFilter(query("style", strings.Repeat("あ", 5000))).Style)); n != MaxText {
		t.Errorf("style length = %d, want %d (文字数で切り詰め)", n, MaxText)
	}
	if ParseFilter(query("in_stock", "yes")).InStock {
		t.Error("in_stock=yes must not enable the filter")
	}
}

func TestLikePattern(t *testing.T) {
	for in, want := range map[string]string{"abc": "%abc%", "50%": `%50\%%`, "a_b": `%a\_b%`, `a\b`: `%a\\b%`, "": "%%"} {
		if got := likePattern(in); got != want {
			t.Errorf("likePattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStoppedItemsAreListedWithFlag(t *testing.T) {
	d := stocked(t)
	mustExec(t, d, "UPDATE item SET is_active = 0 WHERE brand = 'United Athle'")
	rows := search(t, d, "style", "長袖").Rows
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.ItemActive {
			t.Error("stopped item not flagged")
		}
	}
}

func TestTotalCountsAllMatches(t *testing.T) {
	d := stocked(t)
	res := search(t, d, "style", "半袖")
	if res.Count != 5 || res.TotalQty != 23 {
		t.Errorf("count=%d total=%d, want 5 / 23", res.Count, res.TotalQty)
	}
}

func TestTruncationKeepsTheTotalCorrect(t *testing.T) {
	d := stocked(t)
	saved := MaxRows
	MaxRows = 3
	defer func() { MaxRows = saved }()
	res := search(t, d)
	if len(res.Rows) != 3 || !res.Truncated || res.Count != 8 || res.TotalQty != 38 {
		t.Errorf("rows=%d truncated=%v count=%d total=%d; want 3 / true / 8 / 38 (合計は全件分)",
			len(res.Rows), res.Truncated, res.Count, res.TotalQty)
	}
	MaxRows = 8
	if res := search(t, d); res.Truncated {
		t.Error("exactly MaxRows rows must not be truncated")
	}
}

func TestEmptyDatabase(t *testing.T) {
	d := newDB(t)
	res := search(t, d)
	if len(res.Rows) != 0 || res.Count != 0 || res.TotalQty != 0 || res.Truncated {
		t.Errorf("empty DB result = %+v", res)
	}
	opts, err := ListOptions(ctx, d)
	if err != nil || len(opts.Styles)+len(opts.Colors)+len(opts.Sizes) != 0 {
		t.Errorf("options = %+v, %v", opts, err)
	}
}

// countingQ は、発行されたクエリの数を数える。
type countingQ struct {
	db.Querier
	n *int
}

func (c countingQ) QueryContext(x context.Context, q string, a ...any) (*sql.Rows, error) {
	*c.n++
	return c.Querier.QueryContext(x, q, a...)
}
func (c countingQ) QueryRowContext(x context.Context, q string, a ...any) *sql.Row {
	*c.n++
	return c.Querier.QueryRowContext(x, q, a...)
}

func queryCount(t *testing.T, d *db.DB, fn func(q db.Querier)) int {
	n := 0
	fn(countingQ{d, &n})
	return n
}

func TestQueryCountDoesNotGrowWithRows(t *testing.T) {
	d := stocked(t)
	run := func(q db.Querier) { SearchStock(ctx, q, ParseFilter(query("sort", "size"))) }
	small := queryCount(t, d, run)
	for i := 0; i < 15; i++ {
		res, err := d.ExecContext(ctx, "INSERT INTO item (category_id, brand, item_no, name, style) VALUES (1, ?, ?, 'T', '半袖')",
			"B"+string(rune('a'+i)), "N")
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		mustTx(t, d, func(c *sql.Conn) error {
			it, _ := repo.GetItem(ctx, c, id)
			v, err := GetOrCreateVariant(ctx, c, it, "黒", "M", "")
			if err != nil {
				return err
			}
			_, err = Receive(ctx, c, adminID, v, 3, "")
			return err
		})
	}
	if got := queryCount(t, d, run); got != small {
		t.Errorf("query count grew from %d to %d with more rows (N+1)", small, got)
	}
}

func TestOptionsAndStyleChoices(t *testing.T) {
	d := stocked(t)
	setStyle(t, d, "Printstar", "ドライ")
	opts, err := ListOptions(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ドライ", "長袖"}; !reflect.DeepEqual(opts.Styles, want) {
		t.Errorf("styles = %v, want %v", opts.Styles, want)
	}
	if want := []string{"S", "M", "L", "XL"}; !reflect.DeepEqual(opts.Sizes, want) {
		t.Errorf("sizes = %v, want %v (自然順)", opts.Sizes, want)
	}
	choices, err := StyleChoices(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, s := range choices {
		set[s] = true
	}
	for _, s := range []string{"半袖", "長袖", "ドライ"} { // 既定の候補 + 使用中の値
		if !set[s] {
			t.Errorf("style choice %q missing from %v", s, choices)
		}
	}
	if !sort.StringsAreSorted(choices) {
		t.Errorf("choices not sorted: %v", choices)
	}
}
