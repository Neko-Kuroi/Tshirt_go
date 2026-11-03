// Package cli はコマンドラインの入口 (Flask 版の `flask` コマンドに相当)。
//
// サーバー起動 (serve) と管理コマンド (init-db / seed / create-user / list-users) を
// 同じバイナリのサブコマンドとして提供する。文言は Flask 版に合わせ、
// 案内の中のコマンド名だけ Go 版の名前 (inventory) に置き換えてある。
package cli

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"tshirt2/internal/auth"
	"tshirt2/internal/db"
	"tshirt2/internal/repo"
	"tshirt2/internal/util"
	"tshirt2/internal/web"
)

const usageText = `使い方: inventory <コマンド> [引数]

コマンド:
  serve         サーバーを起動する (省略時も同じ。--addr で待ち受けアドレスを指定)
  init-db       DBを作成/更新する (何度実行しても安全)
  seed          初期カテゴリーを登録する (既にあれば何もしない)
  create-user   ユーザーを作成する: create-user [--admin] [--password <パスワード>] <ユーザー名>
  list-users    登録済みのユーザー名・権限・有効/無効を表示する (パスワードは表示しない)
  help          この使い方を表示する
`

// Run はコマンドを実行し、終了コードを返す。
// baseDir は設定ファイル (instance フォルダー) の置き場所で、製品版では実行ファイルのあるフォルダー。
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, baseDir string) int {
	if len(args) == 0 {
		return cmdServe(nil, stdout, stderr, baseDir)
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:], stdout, stderr, baseDir)
	case "init-db":
		return cmdInitDB(stdout, stderr, baseDir)
	case "seed":
		return cmdSeed(stdout, stderr, baseDir)
	case "list-users":
		return cmdListUsers(stdout, stderr, baseDir)
	case "create-user":
		return cmdCreateUser(args[1:], stdin, stdout, stderr, baseDir)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprintf(stderr, "不明なコマンドです: %s\n", args[0])
		fmt.Fprint(stderr, usageText)
		return 2
	}
}

// settings は、この実行で使うパスの解決結果 (Flask 版 config.py と同じ規則)。
type settings struct {
	dbPath      string // INVENTORY_DATABASE、無ければ <baseDir>/instance/inventory.db
	instanceDir string // 鍵ファイルの置き場所。常に <baseDir>/instance (DB の場所には連動しない)
}

func resolve(baseDir string) settings {
	instanceDir := filepath.Join(baseDir, "instance")
	dbPath := os.Getenv("INVENTORY_DATABASE")
	if dbPath == "" {
		dbPath = filepath.Join(instanceDir, "inventory.db")
	}
	return settings{dbPath: dbPath, instanceDir: instanceDir}
}

// requireDB は、既存の DB を開く。ファイルが無ければ作らずに案内を出して ok=false を返す
// (存在しないファイルに接続すると、空の DB が勝手に作られてしまうため。Flask 版と同じ配慮)。
func requireDB(stderr io.Writer, path string) (d *db.DB, ok bool) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(stderr, "DBがありません(%s)。先に inventory init-db を実行してください。\n", path)
		} else {
			fmt.Fprintln(stderr, err)
		}
		return nil, false
	}
	d, err := db.Open(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, false
	}
	return d, true
}

// errUninitialized は、テーブルがまだ無い DB への案内を出すための目印。
var errUninitialized = errors.New("DBが初期化されていません")

// reportErr はコマンドの失敗を表示する。未初期化は案内に変える。
func reportErr(stderr io.Writer, err error) int {
	if errors.Is(err, errUninitialized) {
		fmt.Fprintln(stderr, "DBが初期化されていません。先に inventory init-db を実行してください。")
	} else {
		fmt.Fprintln(stderr, err)
	}
	return 1
}

func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// isUniqueViolation は、DB のエラーが UNIQUE 制約違反か (Flask 版 utils.is_unique_violation)。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

// cmdInitDB は DB を作成/更新する (Flask 版 init-db)。
func cmdInitDB(stdout, stderr io.Writer, baseDir string) int {
	st := resolve(baseDir)
	if err := os.MkdirAll(filepath.Dir(st.dbPath), 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	d, err := db.Open(st.dbPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer d.Close()
	version, err := db.Migrate(context.Background(), d)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "スキーマのバージョン: %d\n", version)
	return 0
}

// seedDefaults は初期カテゴリー (名前, 色, サイズ, 種類名, プリント可)。並びが sort_order になる。
var seedDefaults = []struct {
	name                            string
	color, size, variant, printable int
}{
	{"Tシャツ", 1, 1, 0, 1},
	{"キャップ", 1, 0, 0, 1},
	{"カバン", 1, 0, 0, 1},
	{"バッジ", 0, 0, 1, 0},
}

// cmdSeed は初期カテゴリーを登録する (Flask 版 seed)。
func cmdSeed(stdout, stderr io.Writer, baseDir string) int {
	st := resolve(baseDir)
	d, ok := requireDB(stderr, st.dbPath)
	if !ok {
		return 1
	}
	defer d.Close()
	ctx := context.Background()
	registered := false
	err := d.Tx(ctx, func(conn *sql.Conn) error {
		var one int
		err := conn.QueryRowContext(ctx, "SELECT 1 FROM category LIMIT 1").Scan(&one)
		if err == nil {
			registered = true
			return nil
		}
		if err != sql.ErrNoRows {
			if isNoSuchTable(err) {
				return errUninitialized
			}
			return err
		}
		for i, c := range seedDefaults {
			if _, err := conn.ExecContext(ctx,
				"INSERT INTO category (name, sort_order, uses_color, uses_size, "+
					"uses_variant_name, is_printable) VALUES (?, ?, ?, ?, ?, ?)",
				c.name, i, c.color, c.size, c.variant, c.printable); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return reportErr(stderr, err)
	}
	if registered {
		fmt.Fprintln(stdout, "カテゴリーは登録済みです。")
	} else {
		fmt.Fprintln(stdout, "初期カテゴリーを登録しました。")
	}
	return 0
}

// cmdListUsers はユーザー一覧を表示する (Flask 版 list-users)。パスワードハッシュは出さない。
func cmdListUsers(stdout, stderr io.Writer, baseDir string) int {
	st := resolve(baseDir)
	d, ok := requireDB(stderr, st.dbPath)
	if !ok {
		return 1
	}
	defer d.Close()
	users, err := repo.ListUsers(context.Background(), d)
	if err != nil {
		if isNoSuchTable(err) {
			err = errUninitialized
		}
		return reportErr(stderr, err)
	}
	if len(users) == 0 {
		fmt.Fprintln(stdout, "ユーザーはまだ登録されていません。(inventory create-user --admin 名前 で作成できます)")
		return 0
	}
	for _, u := range users {
		role := "スタッフ"
		if u.Role == "admin" {
			role = "管理者"
		}
		status := "無効"
		if u.IsActive {
			status = "有効"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", u.Username, role, status)
	}
	return 0
}

// cmdCreateUser はユーザーを作成する (Flask 版 create-user)。
// パスワードの長さの規則は管理画面専用なので、ここでは Flask 版 CLI と同じく課さない。
func cmdCreateUser(args []string, stdin io.Reader, stdout, stderr io.Writer, baseDir string) int {
	const usage = "使い方: inventory create-user [--admin] [--password <パスワード>] <ユーザー名>"
	admin := false
	var password *string
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--admin":
			admin = true
		case a == "--password":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, usage)
				return 2
			}
			password = &args[i]
		case strings.HasPrefix(a, "--password="):
			v := strings.TrimPrefix(a, "--password=")
			password = &v
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "不明なオプションです: %s\n", a)
			fmt.Fprintln(stderr, usage)
			return 2
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	// ログイン画面・管理画面と同じく前後の空白を取り除く (空白付きだと二度とログインできない)
	username := util.PyStrip(positional[0])
	if username == "" {
		fmt.Fprintln(stderr, "ユーザー名を入力してください。")
		return 1
	}

	st := resolve(baseDir)
	d, ok := requireDB(stderr, st.dbPath)
	if !ok {
		return 1
	}
	defer d.Close()

	pw := ""
	if password != nil {
		pw = *password
	} else {
		p := &prompter{r: bufio.NewReader(stdin), stdin: stdin, out: stdout}
		first, err := p.password("Password: ")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		second, err := p.password("Repeat for confirmation: ")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if first != second {
			fmt.Fprintln(stderr, "パスワードが一致しません。")
			return 1
		}
		pw = first
	}

	hash, err := auth.Hash(pw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	role := "staff"
	if admin {
		role = "admin"
	}
	ctx := context.Background()
	err = d.Tx(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx,
			`INSERT INTO "user" (username, password_hash, role, is_active, created_at) `+
				"VALUES (?, ?, ?, 1, datetime('now'))",
			username, hash, role)
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			fmt.Fprintln(stderr, "そのユーザー名は既に使われています。")
			return 1
		}
		if isNoSuchTable(err) {
			err = errUninitialized
		}
		return reportErr(stderr, err)
	}
	fmt.Fprintf(stdout, "作成しました: %s (%s)\n", username, role)
	return 0
}

// prompter はパスワードを端末から読む。端末のときはエコーを止める (getpass と同じ)。
type prompter struct {
	r     *bufio.Reader // 2回の読みで入力を使い切らないよう、読み手は1つにまとめる
	stdin io.Reader
	out   io.Writer
}

func (p *prompter) password(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	hidden := false
	if f, ok := p.stdin.(*os.File); ok {
		if old, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS); err == nil {
			noEcho := *old
			noEcho.Lflag &^= unix.ECHO
			if err := unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, &noEcho); err == nil {
				hidden = true
				defer unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, old)
			}
		}
	}
	line, err := p.r.ReadString('\n')
	if hidden {
		fmt.Fprintln(p.out) // エコーが無いので、Enter の改行はこちらで出す
	}
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// cmdServe は HTTP サーバーを起動する。DB が未作成/古くても起動し、
// web の層が案内 (503) を出す。init-db が済めば再起動なしで使えるようになる。
func cmdServe(args []string, stdout, stderr io.Writer, baseDir string) int {
	const usage = "使い方: inventory serve [--addr <アドレス>]"
	addr := os.Getenv("INVENTORY_ADDR")
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--addr":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, usage)
				return 2
			}
			addr = args[i]
		case strings.HasPrefix(a, "--addr="):
			addr = strings.TrimPrefix(a, "--addr=")
		default:
			fmt.Fprintf(stderr, "不明な引数です: %s\n", a)
			fmt.Fprintln(stderr, usage)
			return 2
		}
	}
	if addr == "" {
		addr = "127.0.0.1:5000"
	}

	st := resolve(baseDir)
	secret, err := auth.LoadSecretKey(st.instanceDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	srv, err := web.New(web.Config{
		DatabasePath: st.dbPath,
		SecretKey:    secret,
		CookieSecure: os.Getenv("INVENTORY_COOKIE_SECURE") == "1",
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer srv.Close()

	hs := &http.Server{Addr: addr, Handler: srv.Handler()}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	fmt.Fprintf(stderr, "在庫管理サーバー起動: http://%s (DB: %s)\n", addr, st.dbPath)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
