package web

import (
	"sort"
	"strings"

	"tshirt2/internal/repo"
	"tshirt2/internal/util"
)

// NoteTargets は、備考の対象に選べる値 (その品番にある色・種類名)。
// Flask 版 routes.py の note_targets と同じ (重複なし、空でないもの、文字列順)。
func NoteTargets(variants []repo.Variant) []string {
	set := map[string]bool{}
	for _, v := range variants {
		if v.Color != "" {
			set[v.Color] = true
		}
		if v.VariantName != "" {
			set[v.VariantName] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out) // バイト順 = コードポイント順 (Python の sorted と同じ)
	return out
}

// MatrixRow はマトリクスの1行 (色/種類名ごと)。
type MatrixRow struct {
	Label   string                   // 色と種類名のうち空でないものを " / " でつないだもの (両方空なら "-")
	Cells   map[string]*repo.Variant // キーはサイズ (サイズ無しカテゴリーでは "")
	Total   int64
	HasNote bool // この行の色か種類名を対象にした備考がある
}

// Matrix は、1品番分の色×サイズ表 (Flask 版 build_matrix の返り値)。
type Matrix struct {
	Item    repo.Item
	Sizes   []string // 列の見出し (サイズ順。サイズ無しカテゴリーでは [""])
	Rows    []MatrixRow
	Notes   []repo.Note
	Targets []string
	Total   int64
}

// BuildMatrix は Flask 版 build_matrix の移植。
// variants は repo の並び (variant_name, color 順) のまま渡すこと。行はその順で作る。
func BuildMatrix(item repo.Item, cat repo.Category, variants []repo.Variant, notes []repo.Note) Matrix {
	noted := map[string]bool{}
	for _, n := range notes {
		if n.Target != "" {
			noted[n.Target] = true
		}
	}

	var sizes []string
	if cat.UsesSize {
		seen := map[string]bool{}
		for _, v := range variants {
			if !seen[v.Size] {
				seen[v.Size] = true
				sizes = append(sizes, v.Size)
			}
		}
		sort.SliceStable(sizes, func(i, j int) bool {
			return util.MakeSizeKey(sizes[i]).Compare(util.MakeSizeKey(sizes[j])) < 0
		})
	} else {
		sizes = []string{""}
	}

	type rowKey struct{ variantName, color string }
	var rows []MatrixRow
	rowIndex := map[rowKey]int{}
	for i := range variants {
		v := variants[i]
		key := rowKey{v.VariantName, v.Color}
		ri, ok := rowIndex[key]
		if !ok {
			var parts []string
			if key.variantName != "" {
				parts = append(parts, key.variantName)
			}
			if key.color != "" {
				parts = append(parts, key.color)
			}
			label := strings.Join(parts, " / ")
			if label == "" {
				label = "-"
			}
			hasNote := (key.variantName != "" && noted[key.variantName]) ||
				(key.color != "" && noted[key.color])
			rows = append(rows, MatrixRow{Label: label, Cells: map[string]*repo.Variant{}, HasNote: hasNote})
			ri = len(rows) - 1
			rowIndex[key] = ri
		}
		cellKey := ""
		if cat.UsesSize {
			cellKey = v.Size
		}
		vv := v
		rows[ri].Cells[cellKey] = &vv
		rows[ri].Total += v.Quantity
	}

	total := int64(0)
	for _, r := range rows {
		total += r.Total
	}
	return Matrix{Item: item, Sizes: sizes, Rows: rows, Notes: notes,
		Targets: NoteTargets(variants), Total: total}
}
