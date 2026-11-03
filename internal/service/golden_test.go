package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"tshirt2/internal/db"
)

// Flask 版が作った DB と出力 (tools/gen_golden.py) を正解にして、Go の出力と照合する。
// DB には、特殊文字・数式注入・LIKE のワイルドカード・特殊なサイズ・在庫0・停止中・
// 複数カテゴリー (sort_order が同じものを含む) など、紛らわしいデータが入っている。

func goldenDB(t *testing.T) *db.DB {
	t.Helper()
	src, err := os.Open("testdata/golden/inventory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), "golden.db")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()
	if st, err := db.Check(ctx, path); err != nil || st != db.StateCurrent {
		t.Fatalf("golden DB state = %v, %v; want current", st, err)
	}
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/golden/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func firstDiff(got, want []byte) string {
	n := len(got)
	if len(want) < n {
		n = len(want)
	}
	i := 0
	for i < n && got[i] == want[i] {
		i++
	}
	lo := i - 30
	if lo < 0 {
		lo = 0
	}
	hi := func(b []byte) int {
		if i+30 > len(b) {
			return len(b)
		}
		return i + 30
	}
	return "first difference at byte " + itoa(i) + ":\n got  " + string(got[lo:hi(got)]) +
		"\n want " + string(want[lo:hi(want)])
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestGoldenCSVMatchesPythonByteForByte(t *testing.T) {
	d := goldenDB(t)
	cases := []struct {
		file string
		got  []byte
	}{
		{"blank.csv", blankCSV(t, d, nil)},
		{"blank_cat1.csv", blankCSV(t, d, ptr(1))},
		{"blank_cat2.csv", blankCSV(t, d, ptr(2))},
		{"blank_cat3.csv", blankCSV(t, d, ptr(3))},
		{"blank_cat999.csv", blankCSV(t, d, ptr(999))},
		{"printed.csv", printedCSV(t, d)},
	}
	for _, c := range cases {
		want := golden(t, c.file)
		if !bytes.Equal(c.got, want) {
			t.Errorf("%s differs from Flask's output\n%s", c.file, firstDiff(c.got, want))
		}
	}
}

func ptr(n int64) *int64 { return &n }

type goldenFilter struct {
	Category *int64 `json:"category"`
	Style    string `json:"style"`
	Color    string `json:"color"`
	Size     string `json:"size"`
	Q        string `json:"q"`
	InStock  bool   `json:"in_stock"`
	Sort     string `json:"sort"`
	Dir      string `json:"dir"`
}

func TestGoldenStockListMatchesPython(t *testing.T) {
	d := goldenDB(t)
	var cases []struct {
		Query     [][2]string  `json:"query"`
		MaxRows   *int         `json:"max_rows"`
		Filter    goldenFilter `json:"filter"`
		IDs       []int64      `json:"ids"`
		Count     int          `json:"count"`
		TotalQty  int64        `json:"total_qty"`
		Truncated bool         `json:"truncated"`
	}
	if err := json.Unmarshal(golden(t, "stocklist.json"), &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 70 {
		t.Fatalf("too few golden cases: %d", len(cases))
	}
	saved := MaxRows
	defer func() { MaxRows = saved }()
	for _, c := range cases {
		args := url.Values{}
		for _, kv := range c.Query {
			args.Add(kv[0], kv[1])
		}
		label := args.Encode()
		if len(label) > 80 {
			label = label[:80] + "…"
		}
		MaxRows = saved
		if c.MaxRows != nil {
			MaxRows = *c.MaxRows
			label += " [max_rows=5]"
		}

		f := ParseFilter(args)
		gf := goldenFilter{f.Category, f.Style, f.Color, f.Size, f.Q, f.InStock, f.Sort, f.Dir}
		if !reflect.DeepEqual(gf, c.Filter) {
			t.Errorf("%s: ParseFilter\n got  %+v (category=%v)\n want %+v (category=%v)", label, gf, deref(gf.Category), c.Filter, deref(c.Filter.Category))
			continue
		}
		res, err := SearchStock(ctx, d, f)
		if err != nil {
			t.Errorf("%s: %v", label, err)
			continue
		}
		if got := ids(res.Rows); !reflect.DeepEqual(append([]int64{}, got...), append([]int64{}, c.IDs...)) {
			t.Errorf("%s: row order differs from Python\n got  %v\n want %v", label, got, c.IDs)
		}
		if res.Count != c.Count || res.TotalQty != c.TotalQty || res.Truncated != c.Truncated {
			t.Errorf("%s: count/total/truncated = %d/%d/%v, want %d/%d/%v", label,
				res.Count, res.TotalQty, res.Truncated, c.Count, c.TotalQty, c.Truncated)
		}
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestGoldenOptionsMatchPython(t *testing.T) {
	d := goldenDB(t)
	var want struct {
		Styles       []string `json:"styles"`
		Colors       []string `json:"colors"`
		Sizes        []string `json:"sizes"`
		StyleChoices []string `json:"style_choices"`
	}
	if err := json.Unmarshal(golden(t, "options.json"), &want); err != nil {
		t.Fatal(err)
	}
	got, err := ListOptions(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Styles, want.Styles) || !reflect.DeepEqual(got.Colors, want.Colors) ||
		!reflect.DeepEqual(got.Sizes, want.Sizes) {
		t.Errorf("options differ\n got  %+v\n want %+v", got, want)
	}
	choices, err := StyleChoices(ctx, d)
	if err != nil || !reflect.DeepEqual(choices, want.StyleChoices) {
		t.Errorf("style choices = %v, %v; want %v", choices, err, want.StyleChoices)
	}
}
