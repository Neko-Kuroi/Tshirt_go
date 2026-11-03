package service

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"tshirt2/internal/db"
	"tshirt2/internal/repo"
)

var ctx = context.Background()

const adminID, staffID = 1, 2

// newDB は、Flask 版 tests/conftest.py と同じ初期データを持つ、マイグレーション済みの DB を作る。
func newDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, "INSERT INTO category (name, sort_order, uses_color, uses_size, uses_variant_name, is_printable) "+
		"VALUES ('Tシャツ', 0, 1, 1, 0, 1), ('バッジ', 1, 0, 0, 1, 0)")
	mustExec(t, d, "INSERT INTO item (category_id, brand, item_no, name) VALUES "+
		"(1, 'Printstar', '085-CVT', 'Tシャツ'), (1, 'United Athle', '5001', 'Tシャツ'), (2, '', '44mm', '缶バッジ')")
	mustExec(t, d, `INSERT INTO "user" (username, password_hash, role) VALUES ('admin', 'x', 'admin'), ('staff', 'x', 'staff')`)
	return d
}

func mustExec(t *testing.T, q db.Querier, query string, args ...any) {
	t.Helper()
	if _, err := q.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
}

func scalar(t *testing.T, q db.Querier, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := q.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	return n
}

func itemID(t *testing.T, q db.Querier, brand string) int64 {
	t.Helper()
	return scalar(t, q, "SELECT id FROM item WHERE brand = ?", brand)
}

func item(t *testing.T, q db.Querier, id int64) *repo.Item {
	t.Helper()
	it, err := repo.GetItem(ctx, q, id)
	if err != nil || it == nil {
		t.Fatalf("GetItem(%d) = %v, %v", id, it, err)
	}
	return it
}

func tee(t *testing.T, q db.Querier, brand string) *repo.Item { return item(t, q, itemID(t, q, brand)) }
func badge(t *testing.T, q db.Querier) *repo.Item {
	return item(t, q, scalar(t, q, "SELECT id FROM item WHERE item_no = '44mm'"))
}

// tx は d.Tx の短縮形 (エラーはそのまま返す)。
func tx(d *db.DB, fn func(c *sql.Conn) error) error { return d.Tx(ctx, fn) }

func mustTx(t *testing.T, d *db.DB, fn func(c *sql.Conn) error) {
	t.Helper()
	if err := tx(d, fn); err != nil {
		t.Fatal(err)
	}
}

// receive は1回の入荷 (variant を必要なら作る)。variant を返す。
func receive(t *testing.T, d *db.DB, brand, color, size string, qty int64, variantName ...string) *repo.Variant {
	t.Helper()
	vn := ""
	if len(variantName) > 0 {
		vn = variantName[0]
	}
	var v *repo.Variant
	mustTx(t, d, func(c *sql.Conn) error {
		var err error
		if v, err = GetOrCreateVariant(ctx, c, tee0(t, c, brand), color, size, vn); err != nil {
			return err
		}
		_, err = Receive(ctx, c, adminID, v, qty, "")
		return err
	})
	return v
}

// tee0 は brand が空なら、バッジの品番を返す (Flask 版テストの _receive("", ...) と同じ)。
func tee0(t *testing.T, q db.Querier, brand string) *repo.Item {
	if brand == "" {
		return badge(t, q)
	}
	return tee(t, q, brand)
}

func qtyOf(t *testing.T, q db.Querier, variantID int64) int64 {
	t.Helper()
	return scalar(t, q, "SELECT quantity FROM variant WHERE id = ?", variantID)
}

func count(t *testing.T, q db.Querier, table string) int64 {
	t.Helper()
	return scalar(t, q, "SELECT COUNT(*) FROM "+table)
}

func wantStockError(t *testing.T, err error, msg string) {
	t.Helper()
	if !IsStockError(err) {
		t.Fatalf("err = %v; want *StockError", err)
	}
	if msg != "" && err.Error() != msg {
		t.Fatalf("err = %q; want %q", err.Error(), msg)
	}
}
