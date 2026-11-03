package util

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func loadJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// ---- Python の出力との照合 (tools/gen_util_vectors.py が作る) ----------------------------

func TestToIntMatchesPython(t *testing.T) {
	var cases []struct {
		In  string
		Out *int64
	}
	loadJSON(t, "toint.json", &cases)
	if len(cases) < 50 {
		t.Fatalf("too few vectors: %d", len(cases))
	}
	for _, c := range cases {
		n, ok := ToInt(c.In)
		switch {
		case c.Out == nil && ok:
			t.Errorf("ToInt(%q) = %d, want not ok (Python: None)", c.In, n)
		case c.Out != nil && (!ok || n != *c.Out):
			t.Errorf("ToInt(%q) = %d, %v; want %d", c.In, n, ok, *c.Out)
		}
	}
}

func TestPyStripMatchesPython(t *testing.T) {
	var cases []struct{ In, Out string }
	loadJSON(t, "strip.json", &cases)
	for _, c := range cases {
		if got := PyStrip(c.In); got != c.Out {
			t.Errorf("PyStrip(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
}

func TestSizeKeyOrderMatchesPython(t *testing.T) {
	var v struct{ Input, Sorted []string }
	loadJSON(t, "sizekey.json", &v)
	got := append([]string{}, v.Input...)
	sort.SliceStable(got, func(i, j int) bool {
		return MakeSizeKey(got[i]).Compare(MakeSizeKey(got[j])) < 0
	})
	if len(got) != len(v.Sorted) {
		t.Fatalf("length %d != %d", len(got), len(v.Sorted))
	}
	for i := range got {
		if got[i] != v.Sorted[i] {
			t.Fatalf("first difference at %d: got %q, Python %q\n got[%d:]=%q\nwant[%d:]=%q",
				i, got[i], v.Sorted[i], i, got[i:min(i+5, len(got))], i, v.Sorted[i:min(i+5, len(v.Sorted))])
		}
	}
}

// ---- Flask 版 tests/test_utils.py の単体テスト (size_key) を、そのまま移植 ----------------

func sortedSizes(in ...string) []string {
	out := append([]string{}, in...)
	sort.SliceStable(out, func(i, j int) bool {
		return MakeSizeKey(out[i]).Compare(MakeSizeKey(out[j])) < 0
	})
	return out
}

func eq(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

func TestSizeKeyKnownSizesComeFirst(t *testing.T) {
	if got := sortedSizes("XL", "10", "S", "2", "M"); !eq(got, []string{"S", "M", "XL", "2", "10"}) {
		t.Errorf("got %q", got)
	}
}

func TestSizeKeyASCIIDigitsAreNumeric(t *testing.T) {
	if got := sortedSizes("100", "9", "12"); !eq(got, []string{"9", "12", "100"}) {
		t.Errorf("got %q", got)
	}
}

func TestSizeKeySuperscriptDigitsAreText(t *testing.T) {
	if k := MakeSizeKey("²"); k.group != 2 {
		t.Errorf("² group = %d, want 2", k.group)
	}
	if k := MakeSizeKey("¹²³"); k.group != 2 {
		t.Errorf("¹²³ group = %d, want 2", k.group)
	}
	if got := sortedSizes("M", "²", "10", "2"); !eq(got, []string{"M", "2", "10", "²"}) {
		t.Errorf("got %q", got)
	}
}

func TestSizeKeyNonASCIIDigitsAreTextNotNumbers(t *testing.T) {
	for _, s := range []string{"１２", "٣"} {
		if k := MakeSizeKey(s); k.group != 2 {
			t.Errorf("%q group = %d, want 2", s, k.group)
		}
	}
	if got := sortedSizes("１２", "3"); !eq(got, []string{"3", "１２"}) {
		t.Errorf("got %q", got)
	}
}

func TestSizeKeyHugeASCIINumberStaysNumeric(t *testing.T) {
	big := "99999999999999999999" // 20桁。Int64 の最大 (約9.2e18) を超える
	if got := sortedSizes(big, "9"); !eq(got, []string{"9", big}) {
		t.Errorf("got %q", got)
	}
	if got := sortedSizes(big, "10000000000000000000"); !eq(got, []string{"10000000000000000000", big}) {
		t.Errorf("got %q", got)
	}
}

func TestSizeKeyLeadingZerosTieBreakOnOriginalString(t *testing.T) {
	if got := sortedSizes("7", "007"); !eq(got, []string{"007", "7"}) {
		t.Errorf("got %q", got)
	}
	if got := sortedSizes("00", "0"); !eq(got, []string{"0", "00"}) {
		t.Errorf("got %q", got)
	}
}

// Swift 版で問題になった、正準等価な文字列の並び。Go の文字列比較はバイト順なので Python と一致する。
func TestSizeKeyCombiningCharacterUsesCodePointOrder(t *testing.T) {
	if got := sortedSizes("\u00e9", "e\u0301"); !eq(got, []string{"e\u0301", "\u00e9"}) {
		t.Errorf("got %q", got)
	}
}

func TestSizeKeyCaseInsensitiveKnownSizesKeepOriginal(t *testing.T) {
	if got := sortedSizes("xl", "S", "m"); !eq(got, []string{"S", "m", "xl"}) {
		t.Errorf("got %q", got)
	}
}

// ---- その他 ------------------------------------------------------------------------

func TestToIntRangeEdges(t *testing.T) {
	if n, ok := ToInt("9223372036854775807"); !ok || n != Int64Max {
		t.Errorf("INT64_MAX: %d %v", n, ok)
	}
	if _, ok := ToInt("9223372036854775808"); ok {
		t.Error("INT64_MAX+1 accepted")
	}
	if n, ok := ToInt("-9223372036854775807"); !ok || n != -Int64Max {
		t.Errorf("-INT64_MAX: %d %v", n, ok)
	}
	if _, ok := ToInt("-9223372036854775808"); ok { // Flask 版 to_int は -INT64_MAX までしか受け付けない
		t.Error("INT64_MIN accepted")
	}
	if _, ok := ToInt(strings.Repeat("9", 100000)); ok {
		t.Error("huge string accepted")
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"あいうえお", 3, "あいう"}, {"abc", 5, "abc"}, {"", 3, ""}, {"abc", 0, ""},
		{strings.Repeat("あ", 5000), 100, strings.Repeat("あ", 100)},
	}
	for _, c := range cases {
		if got := TruncateRunes(c.in, c.n); got != c.want {
			t.Errorf("TruncateRunes(%q, %d) = %q", c.in, c.n, got)
		}
	}
}

func TestFormatJST(t *testing.T) {
	cases := map[string]string{
		"2026-10-31 15:00:00":        "11/01 00:00", // UTC 15:00 = JST 翌日 0:00
		"2026-10-04 00:20:10.887356": "10/04 09:20", // SQLAlchemy 版が書いていた小数秒付き
		"2026-01-01T00:00:00":        "01/01 09:00",
		"2026-01-01 00:00:00+00:00":  "01/01 09:00",
		"2026-01-01 09:00:00+09:00":  "01/01 09:00", // オフセット付きはその時刻を JST に
		"":                           "",
		"not a date":                 "not a date",
	}
	for in, want := range cases {
		if got := FormatJST(in); got != want {
			t.Errorf("FormatJST(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDateStampJST(t *testing.T) {
	// UTC の 15:30 は JST では翌日。ファイル名の日付は JST で決まる
	utc := time.Date(2026, 10, 31, 15, 30, 0, 0, time.UTC)
	if got := DateStampJST(utc); got != "20261101" {
		t.Errorf("got %q, want 20261101", got)
	}
}

// ---- SafeNext (オープンリダイレクト対策) ----------------------------------------------

func TestSafeNextMatchesPythonOnOrdinaryInputs(t *testing.T) {
	var cases []struct {
		In  string
		Out *string
	}
	loadJSON(t, "safe_next.json", &cases)
	if len(cases) < 20 {
		t.Fatalf("too few vectors: %d", len(cases))
	}
	for _, c := range cases {
		want := ""
		if c.Out != nil {
			want = *c.Out
		}
		if got := SafeNext(c.In); got != want {
			t.Errorf("SafeNext(%q) = %q, Python gives %q", c.In, got, want)
		}
	}
}

// Python 版より厳しくしている入力: ブラウザが、タブ・改行を取り除き、\ を / として扱うため、
// "/<TAB>\evil.example" などは外部サイトへの移動になりうる。
func TestSafeNextIsStricterThanPython(t *testing.T) {
	for _, in := range []string{
		"/\t\\evil.example", "/\n/evil.example", "/\r\n/evil.example", "/\t/evil.example",
		"/a\x00b", "/a\x7fb", "/\\evil.example", "//evil.example",
	} {
		if got := SafeNext(in); got != "" {
			t.Errorf("SafeNext(%q) = %q, want rejection", in, got)
		}
	}
	for _, in := range []string{"/blank", "/blank/list?category=1&q=a%20b", "/日本語"} {
		if got := SafeNext(in); got != in {
			t.Errorf("SafeNext(%q) = %q, want unchanged", in, got)
		}
	}
}
