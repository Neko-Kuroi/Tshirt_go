// Command inventory は在庫管理アプリの実行ファイル。
// サーバー起動と管理コマンド (init-db / seed / create-user / list-users) を兼ねる。
package main

import (
	"os"
	"path/filepath"

	"tshirt2/internal/cli"
)

func main() {
	// 設定 (instance フォルダー) の置き場所は、実行ファイルのあるフォルダー (Flask 版の BASE_DIR 相当)。
	baseDir := "."
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		baseDir = filepath.Dir(exe)
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, baseDir))
}
