package service

import (
	"bytes"
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"tshirt2/internal/db"
	"tshirt2/internal/repo"
	"tshirt2/internal/util"
)

// CSV出力 (Excel で開いてレポートにする用途。Flask 版 services/export.py)。
//
//   - 文字コードは UTF-8 の BOM 付き (Excel で日本語が文字化けしない)。改行は CRLF。
//   - 在庫 0 の行も出す (「無い」ことも報告したいため)。
//   - Excel の数式として実行されないよう、= + - @ などで始まる文字列には ' を付ける。
//
// 出力は Flask 版とバイト単位で一致する (tools/gen_golden.py が作る正解データと照合)。

const bom = "\ufeff"

var (
	BlankHeaders   = []string{"カテゴリー", "ブランド", "品番", "商品名", "色", "サイズ", "種類名", "数量", "品番の状態"}
	PrintedHeaders = []string{"デザイン", "カテゴリー", "ブランド", "品番", "商品名", "色", "サイズ", "種類名", "数量"}
)

// SafeCell は文字列だけを無害化する。数量などの整数 (int64) はそのまま
// (Excel で数値として扱える)。
func SafeCell(v any) any {
	if s, ok := v.(string); ok && strings.ContainsRune("=+-@\t\r", firstRune(s)) && s != "" {
		return "'" + s
	}
	return v
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// csvField は Python の csv.writer (excel 方言、QUOTE_MINIMAL) と同じ規則で1つの値を書く。
// 区切り文字・引用符・改行 (CR/LF) を含むときだけ "..." で囲み、" は "" にする。
// (Go の encoding/csv は、先頭に空白があるときも囲むので、Python と出力が分かれる)
func csvField(buf *bytes.Buffer, v any) {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case int64:
		s = strconv.FormatInt(x, 10)
	case int:
		s = strconv.Itoa(x)
	default:
		panic("csv: unsupported cell type")
	}
	if strings.ContainsAny(s, ",\"\r\n") {
		buf.WriteByte('"')
		buf.WriteString(strings.ReplaceAll(s, `"`, `""`))
		buf.WriteByte('"')
		return
	}
	buf.WriteString(s)
}

// ToCSVBytes は見出しと行から、BOM 付き UTF-8・CRLF の CSV を作る。
func ToCSVBytes(headers []string, rows [][]any) []byte {
	var buf bytes.Buffer
	buf.WriteString(bom)
	writeRow := func(cells []any) {
		for i, c := range cells {
			if i > 0 {
				buf.WriteByte(',')
			}
			csvField(&buf, c)
		}
		buf.WriteString("\r\n")
	}
	h := make([]any, len(headers))
	for i, s := range headers {
		h[i] = s
	}
	writeRow(h)
	for _, r := range rows {
		cells := make([]any, len(r))
		for i, c := range r {
			cells[i] = SafeCell(c)
		}
		writeRow(cells)
	}
	return buf.Bytes()
}

func sizeLess(a, b string) int {
	return util.MakeSizeKey(a).Compare(util.MakeSizeKey(b))
}

// BlankRows は無地在庫の行 (CSV 用)。categoryID が nil なら全カテゴリー。
// 存在しないカテゴリー ID は、行なし (見出しだけの CSV)。
func BlankRows(ctx context.Context, q db.Querier, categoryID *int64) ([][]any, error) {
	all, err := repo.AllVariants(ctx, q)
	if err != nil {
		return nil, err
	}
	vs := all[:0:0]
	for _, v := range all {
		if categoryID == nil || v.CategoryID == *categoryID {
			vs = append(vs, v)
		}
	}
	sort.SliceStable(vs, func(i, j int) bool {
		a, b := &vs[i], &vs[j]
		return firstNonZero(
			cmpI64(a.CategoryOrder, b.CategoryOrder), cmpStr(a.Brand, b.Brand), cmpStr(a.ItemNo, b.ItemNo),
			cmpI64(a.ItemID, b.ItemID), cmpStr(a.VariantName, b.VariantName), cmpStr(a.Color, b.Color),
			sizeLess(a.Size, b.Size)) < 0
	})
	out := make([][]any, 0, len(vs))
	for _, v := range vs {
		state := "停止中"
		if v.ItemActive {
			state = "使用中"
		}
		out = append(out, []any{v.CategoryName, v.Brand, v.ItemNo, v.ItemName, v.Color, v.Size,
			v.VariantName, v.Quantity, state})
	}
	return out, nil
}

// PrintedRows はプリント済み在庫の行 (CSV 用)。
func PrintedRows(ctx context.Context, q db.Querier) ([][]any, error) {
	ps, err := repo.Products(ctx, q, "")
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := &ps[i], &ps[j]
		return firstNonZero(
			cmpStr(a.DesignName, b.DesignName), cmpI64(a.CategoryOrder, b.CategoryOrder),
			cmpStr(a.Brand, b.Brand), cmpStr(a.ItemNo, b.ItemNo), cmpI64(a.ItemID, b.ItemID),
			cmpStr(a.VariantName, b.VariantName), cmpStr(a.Color, b.Color),
			sizeLess(a.Size, b.Size)) < 0
	})
	out := make([][]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, []any{p.DesignName, p.CategoryName, p.Brand, p.ItemNo, p.ItemName,
			p.Color, p.Size, p.VariantName, p.Quantity})
	}
	return out, nil
}

// CSVFilename はダウンロード時のファイル名 (日付は JST)。name は inventory_blank / inventory_printed。
func CSVFilename(name string, now time.Time) string {
	return name + "_" + util.DateStampJST(now) + ".csv"
}
