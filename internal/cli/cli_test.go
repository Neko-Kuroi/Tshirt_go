package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tshirt2/internal/auth"
	"tshirt2/internal/db"
)

// run はコマンドを1回実行し、終了コードと標準出力・標準エラー出力を返す。
func run(t *testing.T, baseDir, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &out, &errOut, baseDir)
	return code, out.String(), errOut.String()
}

// newBase はテスト用の baseDir を用意する。INVENTORY_DATABASE の影響を受けないよう空にする。
func newBase(t *testing.T) string {
	t.Helper()
	t.Setenv("INVENTORY_DATABASE", "")
	return t.TempDir()
}

func dbPath(baseDir string) string {
	return filepath.Join(baseDir, "instance", "inventory.db")
}

func initDB(t *testing.T, baseDir string) {
	t.Helper()
	code, _, errOut := run(t, baseDir, "", "init-db")
	if code != 0 {
		t.Fatalf("init-db: code=%d stderr=%q", code, errOut)
	}
}

func openDB(t *testing.T, baseDir string) *db.DB {
	t.Helper()
	d, err := db.Open(dbPath(baseDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func userNames(t *testing.T, baseDir string) []string {
	t.Helper()
	d := openDB(t, baseDir)
	rows, err := d.QueryContext(context.Background(), `SELECT username FROM "user" ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func createUser(t *testing.T, baseDir string, args ...string) (int, string, string) {
	t.Helper()
	return run(t, baseDir, "", append([]string{"create-user"}, args...)...)
}

func TestInitDBPrintsVersionAndIsIdempotent(t *testing.T) {
	base := newBase(t)
	for i := 0; i < 2; i++ { // 何度実行しても安全で、同じ表示
		code, out, _ := run(t, base, "", "init-db")
		if code != 0 || out != "スキーマのバージョン: 3\n" {
			t.Errorf("回目=%d: code=%d out=%q", i+1, code, out)
		}
	}
	if _, err := os.Stat(dbPath(base)); err != nil {
		t.Errorf("DBファイルが作られていない: %v", err)
	}
}

func TestSeedInsertsDefaultsOnce(t *testing.T) {
	base := newBase(t)
	initDB(t, base)
	code, out, _ := run(t, base, "", "seed")
	if code != 0 || out != "初期カテゴリーを登録しました。\n" {
		t.Fatalf("seed: code=%d out=%q", code, out)
	}
	d := openDB(t, base)
	rows, err := d.QueryContext(context.Background(),
		"SELECT name, uses_color, uses_size, uses_variant_name, is_printable FROM category ORDER BY sort_order")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type cat struct {
		name                            string
		color, size, variant, printable int
	}
	var got []cat
	for rows.Next() {
		var c cat
		if err := rows.Scan(&c.name, &c.color, &c.size, &c.variant, &c.printable); err != nil {
			t.Fatal(err)
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []cat{
		{"Tシャツ", 1, 1, 0, 1},
		{"キャップ", 1, 0, 0, 1},
		{"カバン", 1, 0, 0, 1},
		{"バッジ", 0, 0, 1, 0},
	}
	if len(got) != len(want) {
		t.Fatalf("カテゴリー数: got=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d番目: got=%v want=%v", i, got[i], want[i])
		}
	}
	// 2回目は登録済みの表示で、増えない
	code, out, _ = run(t, base, "", "seed")
	if code != 0 || out != "カテゴリーは登録済みです。\n" {
		t.Errorf("seed 2回目: code=%d out=%q", code, out)
	}
	var n int
	if err := d.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM category").Scan(&n); err != nil || n != 4 {
		t.Errorf("カテゴリー数=%d err=%v", n, err)
	}
}

func TestSeedOnMissingDBGuidesAndCreatesNothing(t *testing.T) {
	base := newBase(t)
	code, _, errOut := run(t, base, "", "seed")
	if code != 1 || !strings.Contains(errOut, "inventory init-db") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
	if _, err := os.Stat(dbPath(base)); !os.IsNotExist(err) {
		t.Errorf("DBファイルが作られてしまった: %v", err)
	}
}

// Flask 版 tests/test_cli.py::test_list_users_shows_name_role_and_status_but_not_the_hash
func TestListUsersShowsNameRoleAndStatusButNotHash(t *testing.T) {
	base := newBase(t)
	initDB(t, base)
	if code, _, errOut := createUser(t, base, "admin", "--admin", "--password", "password123"); code != 0 {
		t.Fatalf("create admin: code=%d stderr=%q", code, errOut)
	}
	if code, _, errOut := createUser(t, base, "staff", "--password", "password123"); code != 0 {
		t.Fatalf("create staff: code=%d stderr=%q", code, errOut)
	}
	d := openDB(t, base)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `UPDATE "user" SET is_active = 0 WHERE username = 'staff'`); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := d.QueryRowContext(ctx, `SELECT password_hash FROM "user" LIMIT 1`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(t, base, "", "list-users")
	if code != 0 {
		t.Fatalf("list-users: code=%d", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := []string{"admin\t管理者\t有効", "staff\tスタッフ\t無効"}
	if len(lines) != len(want) {
		t.Fatalf("行数: got=%q", out)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("%d行目: got=%q want=%q", i, lines[i], want[i])
		}
	}
	if strings.Contains(out, hash) {
		t.Error("パスワードハッシュが出力に含まれている")
	}
}

// Flask 版 tests/test_cli.py::test_list_users_with_no_users
func TestListUsersWithNoUsers(t *testing.T) {
	base := newBase(t)
	initDB(t, base)
	code, out, _ := run(t, base, "", "list-users")
	if code != 0 || !strings.Contains(out, "ユーザーはまだ登録されていません。(inventory create-user --admin 名前 で作成できます)") {
		t.Errorf("code=%d out=%q", code, out)
	}
}

// Flask 版 tests/test_cli.py::test_list_users_on_missing_db_does_not_create_it
func TestListUsersOnMissingDBDoesNotCreateIt(t *testing.T) {
	base := newBase(t)
	code, _, errOut := run(t, base, "", "list-users")
	if code == 0 || !strings.Contains(errOut, "DBがありません") || !strings.Contains(errOut, "inventory init-db") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
	if _, err := os.Stat(dbPath(base)); !os.IsNotExist(err) {
		t.Errorf("DBファイルが作られてしまった: %v", err)
	}
}

// Flask 版 tests/test_cli.py::test_list_users_on_uninitialized_db
func TestListUsersOnUninitializedDB(t *testing.T) {
	base := newBase(t)
	if err := os.MkdirAll(filepath.Dir(dbPath(base)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath(base), nil, 0o644); err != nil { // 空ファイル (テーブルなし)
		t.Fatal(err)
	}
	code, _, errOut := run(t, base, "", "list-users")
	if code == 0 || !strings.Contains(errOut, "DBが初期化されていません") || !strings.Contains(errOut, "inventory init-db") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
}

// Flask 版 tests/test_create_user.py::test_surrounding_whitespace_is_stripped_so_the_user_can_log_in
func TestCreateUserStripsWhitespace(t *testing.T) {
	base := newBase(t)
	initDB(t, base)
	for _, raw := range []string{" pad1", "pad2 ", "　pad3　"} { // 半角・全角 (U+3000) の空白
		code, _, errOut := createUser(t, base, raw, "--password", "password123")
		if code != 0 {
			t.Fatalf("create %q: code=%d stderr=%q", raw, code, errOut)
		}
	}
	names := userNames(t, base)
	if len(names) != 3 || names[0] != "pad1" || names[1] != "pad2" || names[2] != "pad3" {
		t.Fatalf("ユーザー名: %v", names)
	}
	// 作った資格情報で実際に検証できる
	d := openDB(t, base)
	var hash string
	if err := d.QueryRowContext(context.Background(),
		`SELECT password_hash FROM "user" WHERE username = 'pad3'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	ok, err := auth.Verify(hash, "password123")
	if err != nil || !ok {
		t.Errorf("Verify: ok=%v err=%v", ok, err)
	}
}

// Flask 版 tests/test_create_user.py::test_blank_username_is_rejected
func TestCreateUserBlankIsRejected(t *testing.T) {
	base := newBase(t)
	initDB(t, base)
	for _, raw := range []string{"", "   ", "　"} {
		code, _, errOut := createUser(t, base, raw, "--password", "password123")
		if code == 0 || !strings.Contains(errOut, "ユーザー名を入力してください。") {
			t.Errorf("%q: code=%d stderr=%q", raw, code, errOut)
		}
	}
	if names := userNames(t, base); len(names) != 0 {
		t.Errorf("ユーザーが作られてしまった: %v", names)
	}
}

// Flask 版 tests/test_create_user.py::test_duplicate_is_detected_after_stripping
func TestCreateUserDuplicateAfterStripping(t *testing.T) {
	base := newBase(t)
	initDB(t, base)
	if code, _, _ := createUser(t, base, "pad", "--password", "password123"); code != 0 {
		t.Fatalf("create pad: code=%d", code)
	}
	code, _, errOut := createUser(t, base, " pad ", "--password", "password123")
	if code == 0 || !strings.Contains(errOut, "そのユーザー名は既に使われています。") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
	count := 0
	for _, n := range userNames(t, base) {
		if n == "pad" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("pad の数=%d", count)
	}
}

func TestCreateUserAdminFlagAndMessages(t *testing.T) {
	base := newBase(t)
	initDB(t, base)
	code, out, _ := createUser(t, base, "boss", "--admin", "--password", "password123")
	if code != 0 || out != "作成しました: boss (admin)\n" {
		t.Errorf("admin: code=%d out=%q", code, out)
	}
	code, out, _ = createUser(t, base, "helper", "--password", "password123")
	if code != 0 || out != "作成しました: helper (staff)\n" {
		t.Errorf("staff: code=%d out=%q", code, out)
	}
	d := openDB(t, base)
	var role string
	if err := d.QueryRowContext(context.Background(),
		`SELECT role FROM "user" WHERE username = 'boss'`).Scan(&role); err != nil || role != "admin" {
		t.Errorf("boss の role=%q err=%v", role, err)
	}
}

func TestCreateUserPromptViaPipe(t *testing.T) {
	base := newBase(t)
	initDB(t, base)
	// 端末でない標準入力からは、2行をそのまま読む (1行目=パスワード、2行目=確認)
	code, out, errOut := run(t, base, "secretpw\nsecretpw\n", "create-user", "prompted")
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if !strings.Contains(out, "Password: ") || !strings.Contains(out, "Repeat for confirmation: ") {
		t.Errorf("プロンプトが出ていない: %q", out)
	}
	d := openDB(t, base)
	var hash string
	if err := d.QueryRowContext(context.Background(),
		`SELECT password_hash FROM "user" WHERE username = 'prompted'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	ok, err := auth.Verify(hash, "secretpw")
	if err != nil || !ok {
		t.Errorf("Verify: ok=%v err=%v", ok, err)
	}
}

func TestCreateUserPromptMismatch(t *testing.T) {
	base := newBase(t)
	initDB(t, base)
	code, _, errOut := run(t, base, "aaa\nbbb\n", "create-user", "mismatch")
	if code != 1 || !strings.Contains(errOut, "パスワードが一致しません。") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
	if names := userNames(t, base); len(names) != 0 {
		t.Errorf("ユーザーが作られてしまった: %v", names)
	}
}

func TestCreateUserOnMissingDBDoesNotCreateIt(t *testing.T) {
	base := newBase(t)
	code, _, errOut := createUser(t, base, "someone", "--password", "password123")
	if code != 1 || !strings.Contains(errOut, "inventory init-db") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
	if _, err := os.Stat(dbPath(base)); !os.IsNotExist(err) {
		t.Errorf("DBファイルが作られてしまった: %v", err)
	}
}

func TestCreateUserOnUninitializedDB(t *testing.T) {
	base := newBase(t)
	if err := os.MkdirAll(filepath.Dir(dbPath(base)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath(base), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := createUser(t, base, "someone", "--password", "password123")
	if code != 1 || !strings.Contains(errOut, "DBが初期化されていません") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
}

func TestSeedOnUninitializedDB(t *testing.T) {
	base := newBase(t)
	if err := os.MkdirAll(filepath.Dir(dbPath(base)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath(base), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := run(t, base, "", "seed")
	if code != 1 || !strings.Contains(errOut, "DBが初期化されていません") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
}

func TestHelpAndUnknownCommand(t *testing.T) {
	base := newBase(t)
	for _, arg := range []string{"help", "--help", "-h"} {
		code, out, _ := run(t, base, "", arg)
		if code != 0 || !strings.Contains(out, "使い方: inventory") {
			t.Errorf("%s: code=%d out=%q", arg, code, out)
		}
	}
	code, _, errOut := run(t, base, "", "bogus")
	if code != 2 || !strings.Contains(errOut, "使い方: inventory") {
		t.Errorf("不明なコマンド: code=%d stderr=%q", code, errOut)
	}
}

func TestServeRejectsBadArgs(t *testing.T) {
	base := newBase(t)
	if code, _, _ := run(t, base, "", "serve", "--bogus"); code != 2 {
		t.Errorf("--bogus: code=%d", code)
	}
	if code, _, _ := run(t, base, "", "serve", "--addr"); code != 2 {
		t.Errorf("--addr の値なし: code=%d", code)
	}
}
