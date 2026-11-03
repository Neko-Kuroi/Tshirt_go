package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()

func newDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inventory.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

func mustExec(t *testing.T, q Querier, query string, args ...any) {
	t.Helper()
	if _, err := q.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
}

func scalarInt(t *testing.T, q Querier, query string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	return n
}

func names(t *testing.T, q Querier, typ string) map[string]bool {
	t.Helper()
	rows, err := q.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type=?", typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out[n] = true
	}
	return out
}

func hasColumn(t *testing.T, q Querier, table, col string) bool {
	t.Helper()
	rows, err := q.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		rows.Scan(&n)
		if n == col {
			return true
		}
	}
	return false
}

// ---- マイグレーション ------------------------------------------------------------

func TestMigrationsMatchFlaskSchema(t *testing.T) {
	b, err := os.ReadFile("testdata/migrations.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(Migrations) {
		t.Fatalf("migrations: got %d, want %d", len(Migrations), len(want))
	}
	for i := range want {
		if Migrations[i] != want[i] {
			t.Errorf("migration %d differs from Flask schema.py", i+1)
		}
	}
}

func TestMigrateFreshDB(t *testing.T) {
	d, _ := newDB(t)
	v, err := Migrate(ctx, d)
	if err != nil || v != 3 {
		t.Fatalf("Migrate = %d, %v; want 3, nil", v, err)
	}
	tables := names(t, d, "table")
	for _, n := range []string{"user", "category", "item", "variant", "design",
		"printed_product", "stock_movement", "item_note"} {
		if !tables[n] {
			t.Errorf("table %q missing", n)
		}
	}
	indexes := names(t, d, "index")
	for _, n := range []string{"ix_stock_movement_batch_id", "ix_stock_movement_created_at", "ix_item_note_item_id"} {
		if !indexes[n] {
			t.Errorf("index %q missing", n)
		}
	}
	if !hasColumn(t, d, "item", "style") {
		t.Error("item.style missing (migration 3)")
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	d, _ := newDB(t)
	for i := 0; i < 3; i++ {
		if v, err := Migrate(ctx, d); err != nil || v != 3 {
			t.Fatalf("run %d: %d, %v", i, v, err)
		}
	}
}

func atV1(t *testing.T, d *DB) {
	t.Helper()
	mustExec(t, d, Migrations[0])
	mustExec(t, d, "PRAGMA user_version=1")
}

func TestMigrateFromV1CopiesOldItemNotes(t *testing.T) {
	d, _ := newDB(t)
	atV1(t, d)
	mustExec(t, d, `INSERT INTO "user"(id, username, password_hash) VALUES (5, 'a', 'x'), (7, 'b', 'x')`)
	mustExec(t, d, `INSERT INTO category(id, name) VALUES (1, 'Tシャツ')`)
	mustExec(t, d, `INSERT INTO item(id, category_id, brand, item_no, note) VALUES (1, 1, 'P', '1', '旧メモ'), (2, 1, 'P', '2', '')`)
	if v, err := Migrate(ctx, d); err != nil || v != 3 {
		t.Fatalf("Migrate = %d, %v", v, err)
	}
	if n := scalarInt(t, d, "SELECT COUNT(*) FROM item_note"); n != 1 {
		t.Fatalf("item_note rows = %d, want 1 (空の備考は引き継がない)", n)
	}
	var body string
	var uid int
	if err := d.QueryRowContext(ctx, "SELECT body, user_id FROM item_note").Scan(&body, &uid); err != nil {
		t.Fatal(err)
	}
	if body != "旧メモ" || uid != 5 {
		t.Errorf("got body=%q user_id=%d; want 旧メモ / 5 (最小のuser id)", body, uid)
	}
	var style string
	if err := d.QueryRowContext(ctx, "SELECT style FROM item WHERE id=1").Scan(&style); err != nil || style != "" {
		t.Errorf("style = %q, %v; want empty default", style, err)
	}
}

func TestMigrateFromV1WithoutUsersSkipsNoteCopy(t *testing.T) {
	d, _ := newDB(t)
	atV1(t, d)
	mustExec(t, d, `INSERT INTO category(id, name) VALUES (1, 'Tシャツ')`)
	mustExec(t, d, `INSERT INTO item(id, category_id, brand, item_no, note) VALUES (1, 1, 'P', '1', 'メモ')`)
	if _, err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, d, "SELECT COUNT(*) FROM item_note"); n != 0 {
		t.Errorf("item_note rows = %d, want 0 (user が居ないので user_id を決められない)", n)
	}
}

func TestFailedMigrationIsRolledBackCompletely(t *testing.T) {
	d, _ := newDB(t)
	if _, err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	migs := append(append([]string{}, Migrations...),
		"CREATE TABLE half_done (x INTEGER);\nINSERT INTO no_such_table VALUES (1);")
	v, err := migrateWith(ctx, d, migs)
	if err == nil {
		t.Fatal("expected error")
	}
	_ = v
	if got, _ := CurrentVersion(ctx, d); got != 3 {
		t.Errorf("version = %d after failed migration, want 3", got)
	}
	if names(t, d, "table")["half_done"] {
		t.Error("half_done table survived a failed migration (rollback incomplete)")
	}
	// 失敗のあとも接続は健全で、通常の書き込みができる
	if err := d.Tx(ctx, func(c *sql.Conn) error {
		_, err := c.ExecContext(ctx, `INSERT INTO category(name) VALUES ('after')`)
		return err
	}); err != nil {
		t.Errorf("Tx after failed migration: %v", err)
	}
}

func TestIsCurrent(t *testing.T) {
	for v, want := range map[int]bool{0: false, 1: false, 2: false, 3: true, 4: true} {
		if IsCurrent(v) != want {
			t.Errorf("IsCurrent(%d) = %v, want %v", v, !want, want)
		}
	}
}

// ---- SQLAlchemy 版のDBは引き継がず、書き込まずに止める -----------------------------

func sqlalchemyMadeDB(t *testing.T) (*DB, string) {
	t.Helper()
	d, path := newDB(t)
	ddl, err := os.ReadFile("testdata/sqlalchemy_version_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, string(ddl))
	mustExec(t, d, "INSERT INTO alembic_version VALUES ('afd0c4cd0c7c')")
	return d, path
}

func TestSQLAlchemyMadeDBIsRefusedWithoutWriting(t *testing.T) {
	d, path := sqlalchemyMadeDB(t)
	tablesBefore := len(names(t, d, "table"))

	_, err := Migrate(ctx, d)
	var fe *ForeignSchemaError
	if !errors.As(err, &fe) || !fe.Alembic {
		t.Fatalf("err = %v; want *ForeignSchemaError{Alembic:true}", err)
	}
	if !strings.Contains(err.Error(), "SQLAlchemy") {
		t.Errorf("message should mention SQLAlchemy: %q", err)
	}
	if v, _ := CurrentVersion(ctx, d); v != 0 {
		t.Errorf("user_version = %d, want it untouched (0)", v)
	}
	if n := len(names(t, d, "table")); n != tablesBefore {
		t.Errorf("tables changed: %d -> %d", tablesBefore, n)
	}
	if hasColumn(t, d, "item", "style") {
		t.Error("item.style was added to a foreign DB")
	}
	if st, err := Check(ctx, path); err != nil || st != StateForeign {
		t.Errorf("Check = %v, %v; want foreign", st, err)
	}
}

func TestExistingDBWithoutUserVersionIsRefusedToo(t *testing.T) {
	d, _ := newDB(t)
	mustExec(t, d, "CREATE TABLE category (id INTEGER PRIMARY KEY, name TEXT)")
	_, err := Migrate(ctx, d)
	var fe *ForeignSchemaError
	if !errors.As(err, &fe) || fe.Alembic {
		t.Fatalf("err = %v; want *ForeignSchemaError{Alembic:false}", err)
	}
}

// ---- 起動時の状態 (旧503画面の判定) ---------------------------------------------------

func TestCheckMissingDoesNotCreateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.db")
	st, err := Check(ctx, path)
	if err != nil || st != StateMissing {
		t.Fatalf("Check = %v, %v; want missing", st, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Check created the file (stat err = %v)", err)
	}
	for _, ext := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + ext); err == nil {
			t.Errorf("Check created %s", path+ext)
		}
	}
}

func TestCheckOutdatedThenCurrent(t *testing.T) {
	d, path := newDB(t)
	atV1(t, d)
	if st, err := Check(ctx, path); err != nil || st != StateOutdated {
		t.Fatalf("v1: Check = %v, %v; want outdated", st, err)
	}
	if _, err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	if st, err := Check(ctx, path); err != nil || st != StateCurrent {
		t.Fatalf("v3: Check = %v, %v; want current", st, err)
	}
}

func TestCheckEmptyFileIsOutdatedNotForeign(t *testing.T) {
	_, path := newDB(t) // 開いただけの空のDB
	if st, err := Check(ctx, path); err != nil || st != StateOutdated {
		t.Fatalf("Check = %v, %v; want outdated (新規DBはそのまま migrate できる)", st, err)
	}
}

func TestCheckGarbageFileIsErrorAndLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	content := []byte(strings.Repeat("this is not a sqlite database\n", 200))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(ctx, path); err == nil {
		t.Fatal("expected an error for a non-SQLite file")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(content) {
		t.Error("garbage file was modified")
	}
}

// ---- 接続設定とトランザクション -------------------------------------------------------

func TestForeignKeysAndWALOnEveryConnection(t *testing.T) {
	d, _ := newDB(t)
	if _, err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	var conns []*sql.Conn
	for i := 0; i < 3; i++ { // 別々の接続を同時に確保する (プールが新しい接続を作る)
		c, err := d.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		defer c.Close()
	}
	for i, c := range conns {
		if n := scalarInt(t, c, "PRAGMA foreign_keys"); n != 1 {
			t.Errorf("conn %d: foreign_keys = %d, want 1", i, n)
		}
		var mode string
		c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
		if strings.ToLower(mode) != "wal" {
			t.Errorf("conn %d: journal_mode = %q, want wal", i, mode)
		}
		_, err := c.ExecContext(ctx, `INSERT INTO item(category_id, brand) VALUES (999, 'x')`)
		if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY") {
			t.Errorf("conn %d: insert with bad FK: err = %v, want FOREIGN KEY failure", i, err)
		}
	}
}

func TestTxCommitRollbackAndPanic(t *testing.T) {
	d, _ := newDB(t)
	mustExec(t, d, "CREATE TABLE t (n INTEGER)")
	insert := func(c *sql.Conn) error {
		_, err := c.ExecContext(ctx, "INSERT INTO t VALUES (1)")
		return err
	}
	count := func() int { return scalarInt(t, d, "SELECT COUNT(*) FROM t") }

	if err := d.Tx(ctx, insert); err != nil || count() != 1 {
		t.Fatalf("commit: err=%v count=%d", err, count())
	}
	boom := errors.New("boom")
	err := d.Tx(ctx, func(c *sql.Conn) error {
		if err := insert(c); err != nil {
			return err
		}
		return boom
	})
	if err != boom || count() != 1 {
		t.Fatalf("rollback on error: err=%v count=%d, want boom / 1", err, count())
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		d.Tx(ctx, func(c *sql.Conn) error {
			insert(c)
			panic("kaboom")
		})
	}()
	if count() != 1 {
		t.Fatalf("rollback on panic: count=%d, want 1", count())
	}
	if err := d.Tx(ctx, insert); err != nil || count() != 2 { // パニックのあとも使える
		t.Fatalf("Tx after panic: err=%v count=%d", err, count())
	}
}

func TestTxSerializesConcurrentWriters(t *testing.T) {
	d, _ := newDB(t)
	mustExec(t, d, "CREATE TABLE counter (n INTEGER)")
	mustExec(t, d, "INSERT INTO counter VALUES (0)")
	const workers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				errs <- d.Tx(ctx, func(c *sql.Conn) error {
					var n int
					if err := c.QueryRowContext(ctx, "SELECT n FROM counter").Scan(&n); err != nil {
						return err
					}
					time.Sleep(time.Millisecond) // 読んでから書くまでの間に、他の書き込みが割り込もうとする状況を作る
					_, err := c.ExecContext(ctx, "UPDATE counter SET n = ?", n+1)
					return err
				})
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Tx failed (database is locked?): %v", err)
		}
	}
	if n := scalarInt(t, d, "SELECT n FROM counter"); n != workers*each {
		t.Errorf("counter = %d, want %d (lost update)", n, workers*each)
	}
}

// ---- Flask 版 test_stock.py / test_stock_list.py のうち、DB層に属する部分 -----------------

func TestCheckConstraintsAreEnforced(t *testing.T) {
	d, _ := newDB(t)
	if _, err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO category(id, name) VALUES (1, 'Tシャツ')`)
	mustExec(t, d, `INSERT INTO item(id, category_id, brand) VALUES (1, 1, 'x')`)
	_, err := d.ExecContext(ctx, `INSERT INTO variant (item_id, quantity) VALUES (1, -1)`) // マイナス在庫
	if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "CHECK") {
		t.Errorf("negative quantity: err = %v, want a CHECK constraint failure", err)
	}
	// 同じ規格の二重登録は、UNIQUE で拒否される
	mustExec(t, d, `INSERT INTO variant (item_id, color, size, variant_name, quantity) VALUES (1, '黒', 'M', '', 0)`)
	_, err = d.ExecContext(ctx, `INSERT INTO variant (item_id, color, size, variant_name, quantity) VALUES (1, '黒', 'M', '', 0)`)
	if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
		t.Errorf("duplicate variant: err = %v, want a UNIQUE constraint failure", err)
	}
}

func TestMigrateFromV2AddsStyleColumnAndKeepsData(t *testing.T) {
	d, _ := newDB(t)
	for n := 0; n < 2; n++ { // タイプ列が無い、旧バージョンのDBを作る
		mustExec(t, d, Migrations[n])
		mustExec(t, d, "PRAGMA user_version="+string(rune('1'+n)))
	}
	mustExec(t, d, `INSERT INTO category (name, uses_size) VALUES ('Tシャツ', 1)`)
	mustExec(t, d, `INSERT INTO item (category_id, brand, item_no, name) VALUES (1, 'Printstar', '085', 'T')`)
	if hasColumn(t, d, "item", "style") {
		t.Fatal("test setup: style column already exists")
	}
	if v, err := Migrate(ctx, d); err != nil || v != 3 {
		t.Fatalf("Migrate = %d, %v", v, err)
	}
	var brand, style string
	if err := d.QueryRowContext(ctx, "SELECT brand, style FROM item").Scan(&brand, &style); err != nil {
		t.Fatal(err)
	}
	if brand != "Printstar" || style != "" { // 既存の品番は、タイプ未設定
		t.Errorf("brand=%q style=%q", brand, style)
	}
	if v, err := Migrate(ctx, d); err != nil || v != 3 { // 2回目は何も起きない
		t.Errorf("second Migrate = %d, %v", v, err)
	}
}
