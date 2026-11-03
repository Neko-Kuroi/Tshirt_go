package util

import (
	"strings"
)

// SizeOrder は決まったサイズ表記の並び (Flask 版 SIZE_ORDER)。
var SizeOrder = []string{"XXS", "XS", "S", "M", "L", "XL", "2XL", "XXL", "3XL", "XXXL", "4XL", "5XL",
	"FREE", "F"}

var sizeIndex = func() map[string]int {
	m := make(map[string]int, len(SizeOrder))
	for i, s := range SizeOrder {
		m[s] = i
	}
	return m
}()

// SizeKey は Flask 版 size_key(s) の返すタプルに対応する並べ替えキー。
//
//	グループ0: SizeOrder にある表記 (大文字化して照合。表の順)
//	グループ1: ASCII の 0-9 だけの文字列 (数値の大小)
//	グループ2: それ以外 (空文字、全角数字、上付き数字、ふつうの文字列)
//
// 数値は整数に変換しない (桁数無制限の Python の int と一致させるため)。
// 先頭の 0 を除いた文字列を「長さ → 辞書順」で比べ、数値として等しければ元の文字列で決める。
// 文字列の比較は全てバイト順。UTF-8 ではコードポイント順と一致するので、Python と同じになる。
type SizeKey struct {
	group  int
	index  int    // グループ0: SizeOrder の位置
	digits string // グループ1: 先頭の 0 を除いた数字列 (全て 0 なら空)
	orig   string // 元の文字列 (タイブレーク)
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// MakeSizeKey は size_key(s) に相当するキーを作る。
func MakeSizeKey(s string) SizeKey {
	if i, ok := sizeIndex[strings.ToUpper(s)]; ok {
		return SizeKey{group: 0, index: i, orig: s}
	}
	if isASCIIDigits(s) {
		return SizeKey{group: 1, digits: strings.TrimLeft(s, "0"), orig: s}
	}
	return SizeKey{group: 2, orig: s}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Compare は a < b なら負、等しければ 0、a > b なら正。
func (a SizeKey) Compare(b SizeKey) int {
	if c := cmpInt(a.group, b.group); c != 0 {
		return c
	}
	switch a.group {
	case 0:
		if c := cmpInt(a.index, b.index); c != 0 {
			return c
		}
	case 1:
		if c := cmpInt(len(a.digits), len(b.digits)); c != 0 {
			return c
		}
		if c := strings.Compare(a.digits, b.digits); c != 0 {
			return c
		}
	}
	return strings.Compare(a.orig, b.orig)
}
