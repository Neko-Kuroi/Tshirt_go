package util

import (
	"math"
	"strings"
	"unicode"
)

const (
	// Int64Max は SQLite の整数の上限。これを超える値を渡すと、ドライバーでエラーになる。
	Int64Max = math.MaxInt64
	// MaxQty は1回の入荷・出荷・調整の上限 (Flask 版 MAX_QTY)。SQLite は整数の足し算が溢れると、
	// エラーにせず REAL (小数) にしてしまうので、在庫列を守るためにも必要。
	MaxQty = 1_000_000
)

// decimalValue は Python の int() が受け付ける「10進数字」(Unicode の Nd) の値を返す。
// 全角数字 (１２) やアラビア・インド数字 (٣) も数字として受け付ける。
// 上付き数字 (²) は Nd ではないので受け付けない (Python の int("²") も ValueError)。
func decimalValue(r rune) (uint64, bool) {
	if r >= '0' && r <= '9' {
		return uint64(r - '0'), true
	}
	if r < 0x80 || !unicode.Is(unicode.Nd, r) {
		return 0, false
	}
	// Nd の範囲は、0〜9 の10文字ずつのブロックが連続したもの。範囲の先頭が 0。
	for _, rg := range unicode.Nd.R16 {
		if uint32(r) >= uint32(rg.Lo) && uint32(r) <= uint32(rg.Hi) {
			return uint64((uint32(r) - uint32(rg.Lo)) % 10), true
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if uint32(r) >= rg.Lo && uint32(r) <= rg.Hi {
			return uint64((uint32(r) - rg.Lo) % 10), true
		}
	}
	return 0, false
}

// ToInt はフォーム/クエリの文字列を整数にする (Flask 版 to_int)。
// 変換できない、または SQLite の整数範囲 (±Int64Max) の外なら ok=false。例外やパニックは出さない。
//
// Python の int() と同じ書式を受け付ける:
// 前後の空白 (PyStrip)、先頭の符号 (+ -)、数字の間の単独のアンダースコア (1_000)、
// 全角数字などの Unicode 10進数字。
func ToInt(raw string) (n int64, ok bool) {
	s := PyStrip(raw)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	var acc uint64
	prevUnderscore := true // 先頭の _ と、連続する _ を拒否するため
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				return 0, false
			}
			prevUnderscore = true
			continue
		}
		d, isDigit := decimalValue(r)
		if !isDigit {
			return 0, false
		}
		prevUnderscore = false
		if acc > (math.MaxUint64-d)/10 {
			return 0, false // 桁あふれ = 範囲外
		}
		acc = acc*10 + d
	}
	if prevUnderscore { // 末尾の _
		return 0, false
	}
	if acc > Int64Max {
		return 0, false
	}
	if neg {
		return -int64(acc), true
	}
	return int64(acc), true
}

// SafeNext は、ログイン後の移動先 (next) が、このサイト内の相対パスのときだけ返す
// (オープンリダイレクト対策。Flask 版 safe_next)。それ以外は空文字。
//
// Flask 版と同じ判定に加えて、次の点を厳しくしている (ブラウザは、タブ・改行を取り除き、
// \ を / として扱うため、Python 版では通ってしまう "/<TAB>\evil.example" が、外部への移動になりうる):
//   - 制御文字 (U+0000〜001F, U+007F) を含むものは拒否
//   - "/\" で始まるものは拒否
func SafeNext(target string) string {
	if target == "" || target[0] != '/' {
		return ""
	}
	if strings.HasPrefix(target, "//") || strings.HasPrefix(target, `/\`) {
		return ""
	}
	for i := 0; i < len(target); i++ {
		if target[i] < 0x20 || target[i] == 0x7f {
			return ""
		}
	}
	return target
}
