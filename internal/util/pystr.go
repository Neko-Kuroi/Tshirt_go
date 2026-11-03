// Package util は、Flask 版 app/utils.py と、Python の標準動作に依存していた小さな処理の移植。
//
// Python と Go で挙動が分かれやすい箇所 (空白の定義、int() の受け付ける書式、文字列の並び) を、
// Python 側の出力を正解として揃える。tools/gen_golden.py が正解データを作る。
package util

import (
	"strings"
	"unicode"
)

// IsPySpace は Python の str.isspace() が真になる文字か。
// Go の unicode.IsSpace に加えて、Python は U+001C〜U+001F も空白として扱う。
func IsPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// PyStrip は Python の str.strip() (引数なし) と同じ。
func PyStrip(s string) string {
	return strings.TrimFunc(s, IsPySpace)
}

// TruncateRunes は Python の s[:n] (コードポイント単位の切り詰め)。
func TruncateRunes(s string, n int) string {
	if n < 0 {
		n = 0
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
