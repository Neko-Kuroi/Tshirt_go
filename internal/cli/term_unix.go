//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// hideEcho は端末のエコーを止め、元に戻す関数を返す。端末でない場合は ok=false。
func hideEcho(f *os.File) (restore func(), ok bool) {
	old, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		return nil, false
	}
	noEcho := *old
	noEcho.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, &noEcho); err != nil {
		return nil, false
	}
	return func() { unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, old) }, true
}
