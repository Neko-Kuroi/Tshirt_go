//go:build windows

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// hideEcho はコンソールのエコーを止め、元に戻す関数を返す。コンソールでない場合は ok=false。
func hideEcho(f *os.File) (restore func(), ok bool) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil, false
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return nil, false
	}
	return func() { windows.SetConsoleMode(h, mode) }, true
}
