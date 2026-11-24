package util

import (
	"regexp"
	"time"
)

// JST は日本時間。DB は UTC で保存し、表示だけ JST にする。
var JST = time.FixedZone("JST", 9*60*60)

var utcLayouts = []string{
	"2006-01-02 15:04:05",       // datetime('now') の形式。小数秒付き (SQLAlchemy 版) も読める
	"2006-01-02T15:04:05",       //
	"2006-01-02 15:04:05Z07:00", // オフセット付き
	"2006-01-02T15:04:05Z07:00",
}

// dbDatetimeShape は、変換してよい DB 日時の形 (Flask 版 _DB_DATETIME_SHAPE と同じ)。
// Go の time.Parse は小数秒のカンマ区切りなど Python 側で変換しない形も通して
// しまうので、先にこの形で縛る。
var dbDatetimeShape = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})?$`)

// FormatJST は UTC の日時文字列を、JST の "YYYY/MM/DD HH:mm" にする (Flask 版 jst フィルタ)。
// 空文字は空文字。変換できるのは dbDatetimeShape の形だけ (年 0000 は Python の
// datetime の範囲外なので変換しない)。変換すると年が 1〜9999 の範囲を超える値も、
// 解釈できない値と同じく、そのまま返す。
func FormatJST(v string) string {
	if v == "" {
		return ""
	}
	if !dbDatetimeShape.MatchString(v) || v[:4] == "0000" {
		return v
	}
	for _, layout := range utcLayouts {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			j := t.In(JST)
			if j.Year() < 1 || j.Year() > 9999 {
				return v
			}
			return j.Format("2006/01/02 15:04")
		}
	}
	return v
}

// DateStampJST はファイル名用の JST の日付 (YYYYMMDD)。
func DateStampJST(t time.Time) string {
	return t.In(JST).Format("20060102")
}
