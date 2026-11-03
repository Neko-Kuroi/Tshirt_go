//go:build mattn

package db

import (
	"errors"

	"github.com/mattn/go-sqlite3" // cgo。ドライバー差の切り分けと、cgo環境でのテスト用 (-tags mattn)
)

const driverName = "sqlite3"

func dsn(path string) string {
	return "file:" + escapePath(path) +
		"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"
}

// isBusyError は SQLITE_BUSY / SQLITE_LOCKED かを判定する。
func isBusyError(err error) bool {
	var e sqlite3.Error
	if errors.As(err, &e) {
		return e.Code == sqlite3.ErrBusy || e.Code == sqlite3.ErrLocked
	}
	var pe *sqlite3.Error
	if errors.As(err, &pe) {
		return pe.Code == sqlite3.ErrBusy || pe.Code == sqlite3.ErrLocked
	}
	return false
}
