package util

import "time"

// JST は日本時間。DB は UTC で保存し、表示だけ JST にする。
var JST = time.FixedZone("JST", 9*60*60)

var utcLayouts = []string{
	"2006-01-02 15:04:05",       // datetime('now') の形式。小数秒付き (SQLAlchemy 版) も読める
	"2006-01-02T15:04:05",       //
	"2006-01-02 15:04:05Z07:00", // オフセット付き
	"2006-01-02T15:04:05Z07:00",
}

// FormatJST は UTC の日時文字列を、JST の "MM/dd HH:mm" にする (Flask 版 jst フィルタ)。
// 空文字は空文字。解釈できない値は、そのまま返す (Python 版はここで例外になる)。
func FormatJST(v string) string {
	if v == "" {
		return ""
	}
	for _, layout := range utcLayouts {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t.In(JST).Format("01/02 15:04")
		}
	}
	return v
}

// DateStampJST はファイル名用の JST の日付 (YYYYMMDD)。
func DateStampJST(t time.Time) string {
	return t.In(JST).Format("20060102")
}
