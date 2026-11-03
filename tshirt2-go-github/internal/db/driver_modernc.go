//go:build !mattn

package db

import (
	"errors"

	"modernc.org/sqlite" // 純Go (cgo不要)。既定のドライバー
)

const driverName = "sqlite"

// dsn は接続ごとに foreign_keys=ON / WAL / busy_timeout を設定する DSN を返す。
// (foreign_keys は接続ごとの設定なので、接続プールを使う以上、DSN で指定する必要がある)
func dsn(path string) string {
	return "file:" + escapePath(path) +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
}

// isBusyError は SQLITE_BUSY / SQLITE_LOCKED かを判定する (拡張コードは主コードに直す)。
func isBusyError(err error) bool {
	var e *sqlite.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code() & 0xff {
	case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
		return true
	}
	return false
}
