package service

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"reflect"
	"strings"
	"testing"
	"time"

	"tshirt2/internal/db"
	"tshirt2/internal/repo"
)

// csvRows は、Excel と同じように BOM 付き UTF-8 として読み、行のリストにする。
func csvRows(t *testing.T, b []byte) [][]string {
	t.Helper()
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	rows, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	return rows
}

func blankCSV(t *testing.T, q db.Querier, cat *int64) []byte {
	t.Helper()
	rows, err := BlankRows(ctx, q, cat)
	if err != nil {
		t.Fatal(err)
	}
	return ToCSVBytes(BlankHeaders, rows)
}

func printedCSV(t *testing.T, q db.Querier) []byte {
	t.Helper()
	rows, err := PrintedRows(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	return ToCSVBytes(PrintedHeaders, rows)
}

func contains(rows [][]string, want []string) bool {
	for _, r := range rows {
		if reflect.DeepEqual(r, want) {
			return true
		}
	}
	return false
}

// ---- Flask 版 tests/test_export.py の、サービス層で成り立つ部分 ---------------------------

func TestBlankCSVFormatForExcel(t *testing.T) {
	d := newDB(t)
	receive(t, d, "Printstar", "黒", "M", 10)
	out := blankCSV(t, d, nil)
	if !bytes.HasPrefix(out, []byte("\xef\xbb\xbf")) { // BOM (Excel の文字化け防止)
		t.Error("no BOM")
	}
	if !bytes.Contains(out, []byte("\r\n")) || bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
		t.Error("line endings must be CRLF only")
	}
	rows := csvRows(t, out)
	if !reflect.DeepEqual(rows[0], BlankHeaders) {
		t.Errorf("header = %v", rows[0])
	}
	if !contains(rows, []string{"Tシャツ", "Printstar", "085-CVT", "Tシャツ", "黒", "M", "", "10", "使用中"}) {
		t.Errorf("row missing in %v", rows)
	}
}

func TestQuantityIsPlainNumberAndZeroRowsAreIncluded(t *testing.T) {
	d := newDB(t)
	receive(t, d, "Printstar", "黒", "M", 5)
	w := receive(t, d, "Printstar", "白", "M", 3)
	mustTx(t, d, func(c *sql.Conn) error { _, err := Ship(ctx, c, adminID, w.Target(), 3, ""); return err })
	out := blankCSV(t, d, nil)
	raw := strings.TrimPrefix(string(out), "\ufeff")
	if strings.Contains(raw, `"5"`) || strings.Contains(raw, `"0"`) { // 数量は引用符なしの数値
		t.Error("quantity is quoted")
	}
	rows := csvRows(t, out)
	qty := map[string]string{}
	for _, r := range rows[1:] {
		qty[r[4]] = r[7]
	}
	if qty["黒"] != "5" || qty["白"] != "0" {
		t.Errorf("quantities = %v, want 黒=5 白=0 (在庫0の行も出る)", qty)
	}
}

func TestStoppedItemIsMarked(t *testing.T) {
	d := newDB(t)
	receive(t, d, "United Athle", "赤", "L", 2)
	mustExec(t, d, "UPDATE item SET is_active = 0 WHERE brand = 'United Athle'")
	for _, r := range csvRows(t, blankCSV(t, d, nil))[1:] {
		if r[1] == "United Athle" && r[8] != "停止中" {
			t.Errorf("state = %q, want 停止中", r[8])
		}
	}
}

func TestSizesAreInNaturalOrder(t *testing.T) {
	d := newDB(t)
	for _, s := range []string{"XL", "S", "L", "M"} {
		receive(t, d, "Printstar", "黒", s, 1)
	}
	var sizes []string
	for _, r := range csvRows(t, blankCSV(t, d, nil))[1:] {
		if r[1] == "Printstar" {
			sizes = append(sizes, r[5])
		}
	}
	if want := []string{"S", "M", "L", "XL"}; !reflect.DeepEqual(sizes, want) {
		t.Errorf("sizes = %v, want %v", sizes, want)
	}
}

func TestCategoryFilter(t *testing.T) {
	d := newDB(t)
	receive(t, d, "Printstar", "黒", "M", 1)
	receive(t, d, "", "", "", 4, "赤")
	cats := func(rows [][]string) map[string]bool {
		m := map[string]bool{}
		for _, r := range rows[1:] {
			m[r[0]] = true
		}
		return m
	}
	both := csvRows(t, blankCSV(t, d, nil))
	if c := cats(both); !c["Tシャツ"] || !c["バッジ"] || len(c) != 2 {
		t.Errorf("all = %v", c)
	}
	two, nope := int64(2), int64(999)
	if c := cats(csvRows(t, blankCSV(t, d, &two))); !c["バッジ"] || len(c) != 1 {
		t.Errorf("category=2 -> %v", c)
	}
	if rows := csvRows(t, blankCSV(t, d, &nope)); len(rows) != 1 { // 存在しないIDは見出しだけ
		t.Errorf("category=999 -> %d rows, want header only", len(rows))
	}
}

func TestSpecialCharactersRoundTrip(t *testing.T) {
	d := newDB(t)
	mustExec(t, d, "UPDATE item SET name = ? WHERE brand = 'Printstar'", "A,\"B\"\nC")
	receive(t, d, "Printstar", "黒", "M", 1)
	for _, r := range csvRows(t, blankCSV(t, d, nil))[1:] {
		if r[1] == "Printstar" && r[3] != "A,\"B\"\nC" {
			t.Errorf("name = %q", r[3])
		}
	}
}

func TestFormulaInjectionIsNeutralized(t *testing.T) {
	for _, evil := range []string{"=1+1", "+1", "-1", "@SUM(A1)", "\t=1"} {
		d := newDB(t)
		mustExec(t, d, "UPDATE item SET name = ? WHERE brand = 'Printstar'", evil)
		receive(t, d, "Printstar", "黒", "M", 1)
		for _, r := range csvRows(t, blankCSV(t, d, nil))[1:] {
			if r[1] == "Printstar" && r[3] != "'"+evil {
				t.Errorf("name %q -> %q, want %q", evil, r[3], "'"+evil)
			}
		}
	}
}

func TestSafeCellLeavesNormalValuesAlone(t *testing.T) {
	for _, v := range []any{"黒", "", int64(0), int64(12), "a=b", "x-1"} {
		if got := SafeCell(v); got != v {
			t.Errorf("SafeCell(%#v) = %#v", v, got)
		}
	}
	for in, want := range map[string]string{"=1": "'=1", "+1": "'+1", "-1": "'-1", "@a": "'@a", "\tx": "'\tx", "\rx": "'\rx"} {
		if got := SafeCell(in); got != want {
			t.Errorf("SafeCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintedCSV(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "黒", "M", 10)
	mustTx(t, d, func(c *sql.Conn) error {
		_, err := ConvertToPrinted(ctx, c, adminID, v, "ねこロゴ", 6, 5, "")
		return err
	})
	rows := csvRows(t, printedCSV(t, d))
	if !reflect.DeepEqual(rows[0], PrintedHeaders) {
		t.Errorf("header = %v", rows[0])
	}
	if !contains(rows, []string{"ねこロゴ", "Tシャツ", "Printstar", "085-CVT", "Tシャツ", "黒", "M", "", "5"}) {
		t.Errorf("row missing in %v", rows)
	}
}

func TestEmptyDatabaseGivesHeaderOnly(t *testing.T) {
	d := newDB(t)
	if rows := csvRows(t, blankCSV(t, d, nil)); len(rows) != 1 || !reflect.DeepEqual(rows[0], BlankHeaders) {
		t.Errorf("blank = %v", rows)
	}
	if rows := csvRows(t, printedCSV(t, d)); len(rows) != 1 || !reflect.DeepEqual(rows[0], PrintedHeaders) {
		t.Errorf("printed = %v", rows)
	}
}

func TestExportQueryCountDoesNotGrowWithRows(t *testing.T) {
	for name, run := range map[string]func(q db.Querier){
		"blank":   func(q db.Querier) { BlankRows(ctx, q, nil) },
		"printed": func(q db.Querier) { PrintedRows(ctx, q) },
	} {
		d := newDB(t)
		seed := func(from, n int) {
			for i := from; i < from+n; i++ {
				brand := "B" + string(rune('a'+i))
				res, err := d.ExecContext(ctx, "INSERT INTO item (category_id, brand, item_no, name) VALUES (1, ?, 'N', 'T')", brand)
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
					if _, err := Receive(ctx, c, adminID, v, 10, ""); err != nil {
						return err
					}
					_, err = ConvertToPrinted(ctx, c, adminID, v, "D"+brand, 5, 5, "")
					return err
				})
			}
		}
		seed(0, 3)
		small := queryCount(t, d, run)
		seed(3, 12)
		if got := queryCount(t, d, run); got != small {
			t.Errorf("%s: query count grew from %d to %d (N+1)", name, small, got)
		}
	}
}

func TestCSVQuotingMatchesPythonCSVWriter(t *testing.T) {
	// Python の csv.writer は、先頭の空白では引用符を付けない。Go の encoding/csv は付ける。
	got := string(ToCSVBytes([]string{"h"}, [][]any{{" lead"}, {"a,b"}, {`q"q`}, {"l1\nl2"}, {"c\rr"}, {""}, {int64(7)}}))
	want := "\ufeffh\r\n lead\r\n\"a,b\"\r\n\"q\"\"q\"\r\n\"l1\nl2\"\r\n\"c\rr\"\r\n\r\n7\r\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestCSVFilenameUsesJSTDate(t *testing.T) {
	// UTC の 10/31 15:30 は、JST では 11/01
	now := time.Date(2026, 10, 31, 15, 30, 0, 0, time.UTC)
	if got := CSVFilename("inventory_blank", now); got != "inventory_blank_20261101.csv" {
		t.Errorf("got %q", got)
	}
}
