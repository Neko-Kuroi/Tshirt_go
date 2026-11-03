package service

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"tshirt2/internal/repo"
	"tshirt2/internal/util"
)

// ---- Flask 版 tests/test_stock.py の移植 ----------------------------------------------

func TestReceiveCreatesVariantAndMovement(t *testing.T) {
	d := newDB(t)
	var v *repo.Variant
	mustTx(t, d, func(c *sql.Conn) error {
		var err error
		if v, err = GetOrCreateVariant(ctx, c, tee(t, c, "Printstar"), "黒", "M", ""); err != nil {
			return err
		}
		_, err = Receive(ctx, c, adminID, v, 30, "納品書A")
		return err
	})
	if q := qtyOf(t, d, v.ID); q != 30 {
		t.Fatalf("quantity = %d", q)
	}
	if n := count(t, d, "stock_movement"); n != 1 {
		t.Fatalf("movements = %d", n)
	}
	var kind, note string
	var delta, variantID, userID int64
	var productID sql.NullInt64
	err := d.QueryRowContext(ctx,
		"SELECT kind, delta, variant_id, printed_product_id, note, user_id FROM stock_movement").
		Scan(&kind, &delta, &variantID, &productID, &note, &userID)
	if err != nil || kind != "receive" || delta != 30 || variantID != v.ID || productID.Valid ||
		note != "納品書A" || userID != adminID {
		t.Fatalf("movement = %q %d %d %v %q user=%d (%v)", kind, delta, variantID, productID, note, userID, err)
	}
}

func TestSameSpecReusesVariantAndBrandsAreSeparate(t *testing.T) {
	d := newDB(t)
	var a1, a2, b *repo.Variant
	mustTx(t, d, func(c *sql.Conn) (err error) {
		if a1, err = GetOrCreateVariant(ctx, c, tee(t, c, "Printstar"), "黒", "M", ""); err != nil {
			return
		}
		if a2, err = GetOrCreateVariant(ctx, c, tee(t, c, "Printstar"), " 黒 ", "M", ""); err != nil {
			return
		}
		b, err = GetOrCreateVariant(ctx, c, tee(t, c, "United Athle"), "黒", "M", "")
		return
	})
	if a1.ID != a2.ID {
		t.Error("same spec created two variants")
	}
	if a1.ID == b.ID {
		t.Error("different brands share a variant")
	}
}

func TestUnusedColumnsAreBlankedAndRequiredOnesEnforced(t *testing.T) {
	d := newDB(t)
	var v *repo.Variant
	mustTx(t, d, func(c *sql.Conn) (err error) {
		v, err = GetOrCreateVariant(ctx, c, badge(t, c), "赤", "M", "デザインA")
		return
	})
	if v.Color != "" || v.Size != "" || v.VariantName != "デザインA" {
		t.Errorf("got %q %q %q; want blank color/size (NULL ではなく空文字)", v.Color, v.Size, v.VariantName)
	}
	for _, c := range []struct {
		name        string
		brand       string
		color, size string
		variantName string
		msg         string
	}{
		{"種類名", "", "", "", "", "種類名を入力してください。"},
		{"サイズ", "Printstar", "黒", "", "", "サイズを入力してください。"},
		{"色", "Printstar", "", "M", "", "色を入力してください。"},
		{"空白だけの色", "Printstar", "  ", "M", "", "色を入力してください。"},
	} {
		err := tx(d, func(cn *sql.Conn) error {
			_, err := GetOrCreateVariant(ctx, cn, tee0(t, cn, c.brand), c.color, c.size, c.variantName)
			return err
		})
		if !IsStockError(err) || err.Error() != c.msg {
			t.Errorf("%s: err = %v; want %q", c.name, err, c.msg)
		}
	}
}

func TestInactiveItemCannotBeReceived(t *testing.T) {
	d := newDB(t)
	mustExec(t, d, "UPDATE item SET is_active = 0 WHERE brand = 'Printstar'")
	err := tx(d, func(c *sql.Conn) error {
		_, err := GetOrCreateVariant(ctx, c, tee(t, c, "Printstar"), "黒", "M", "")
		return err
	})
	wantStockError(t, err, "この品番は使用停止中のため、入荷できません。")
}

func TestCannotGoNegativeAndFailureRollsEverythingBack(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "黒", "M", 5)
	err := tx(d, func(c *sql.Conn) error {
		_, err := Ship(ctx, c, adminID, v.Target(), 6, "")
		return err
	})
	wantStockError(t, err, "在庫が足りません。")
	if q := qtyOf(t, d, v.ID); q != 5 {
		t.Errorf("quantity = %d, want 5", q)
	}
	if n := count(t, d, "stock_movement"); n != 1 {
		t.Errorf("movements = %d, want 1", n)
	}
}

func TestConvertIsAtomicAndLogsBothSides(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "黒", "M", 20)
	var batch string
	mustTx(t, d, func(c *sql.Conn) (err error) {
		batch, err = ConvertToPrinted(ctx, c, adminID, v, "ロゴA", 10, 9, "1枚ミスプリント")
		return
	})
	if q := qtyOf(t, d, v.ID); q != 10 {
		t.Errorf("blank quantity = %d, want 10", q)
	}
	ps, err := repo.Products(ctx, d, "")
	if err != nil || len(ps) != 1 || ps[0].Quantity != 9 || ps[0].DesignName != "ロゴA" {
		t.Fatalf("products = %+v, %v", ps, err)
	}
	rows, err := d.QueryContext(ctx, "SELECT kind, delta FROM stock_movement WHERE batch_id = ? ORDER BY id", batch)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var kind string
		var delta int64
		rows.Scan(&kind, &delta)
		got = append(got, fmt.Sprintf("%s:%d", kind, delta))
	}
	if strings.Join(got, ",") != "print_use:-10,print_output:9" {
		t.Errorf("movements in batch = %v", got)
	}
}

func TestConvertFailureLeavesNothingBehind(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "黒", "M", 3)
	err := tx(d, func(c *sql.Conn) error {
		_, err := ConvertToPrinted(ctx, c, adminID, v, "ロゴA", 5, 5, "")
		return err
	})
	wantStockError(t, err, "在庫が足りません。")
	if q := qtyOf(t, d, v.ID); q != 3 {
		t.Errorf("quantity = %d, want 3", q)
	}
	if n := count(t, d, "printed_product"); n != 0 {
		t.Errorf("printed_product rows = %d", n)
	}
	if n := count(t, d, "design"); n != 0 {
		t.Errorf("design rows = %d (デザインの新規登録も巻き戻ること)", n)
	}
	if n := count(t, d, "stock_movement"); n != 1 {
		t.Errorf("movements = %d, want 1", n)
	}
}

func TestConvertValidations(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "黒", "M", 10)
	bv := receive(t, d, "", "", "", 10, "デザインA")
	cases := []struct {
		name   string
		v      *repo.Variant
		design string
		used   int64
		output int64
		msg    string
	}{
		{"完成>使用", v, "A", 5, 6, "完成数が使用数を超えています。"},
		{"デザイン名なし", v, "", 5, 5, "デザイン名を入力してください。"},
		{"プリント対象外", bv, "A", 1, 1, "このカテゴリーはプリント対象ではありません。"},
		{"使用数0", v, "A", 0, 0, "使用数は1以上の整数で入力してください。"},
		{"完成数マイナス", v, "A", 1, -1, "完成数は0以上の整数で入力してください。"},
		{"使用数が上限超え", v, "A", util.MaxQty + 1, 1, "使用数は1,000,000以下で入力してください。"},
	}
	for _, tc := range cases {
		err := tx(d, func(cn *sql.Conn) error {
			_, err := ConvertToPrinted(ctx, cn, adminID, tc.v, tc.design, tc.used, tc.output, "")
			return err
		})
		if !IsStockError(err) || err.Error() != tc.msg {
			t.Errorf("%s: err = %v; want %q", tc.name, err, tc.msg)
		}
	}
	// 使用停止中の品番 (variant を取り直して item_active を反映)
	mustExec(t, d, "UPDATE item SET is_active = 0 WHERE brand = 'Printstar'")
	stopped, _ := repo.GetVariant(ctx, d, v.ID)
	err := tx(d, func(cn *sql.Conn) error {
		_, err := ConvertToPrinted(ctx, cn, adminID, stopped, "A", 1, 1, "")
		return err
	})
	wantStockError(t, err, "この品番は使用停止中のため、プリント変換できません。")
	if q := qtyOf(t, d, v.ID); q != 10 {
		t.Errorf("quantity changed by rejected conversions: %d", q)
	}
}

func TestConvertWithZeroOutputCreatesNoProduct(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "黒", "M", 5)
	mustTx(t, d, func(c *sql.Conn) error {
		_, err := ConvertToPrinted(ctx, c, adminID, v, "全滅", 2, 0, "")
		return err
	})
	if n := count(t, d, "printed_product"); n != 0 {
		t.Errorf("printed_product rows = %d, want 0", n)
	}
	if n := count(t, d, "stock_movement WHERE kind = 'print_output'"); n != 0 {
		t.Errorf("print_output movements = %d, want 0", n)
	}
	if q := qtyOf(t, d, v.ID); q != 3 {
		t.Errorf("quantity = %d, want 3", q)
	}
}

func TestAdjustRequiresReasonAndNonzero(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "黒", "M", 10)
	for _, tc := range []struct {
		delta int64
		note  string
		msg   string
	}{
		{-1, "", "棚卸し調整には理由を入力してください。"},
		{-1, "  　", "棚卸し調整には理由を入力してください。"}, // 空白 (全角含む) だけの理由も不可
		{0, "理由", "増減数は0以外の整数で入力してください。"},
	} {
		err := tx(d, func(c *sql.Conn) error {
			_, err := Adjust(ctx, c, adminID, v.Target(), tc.delta, tc.note)
			return err
		})
		if !IsStockError(err) || err.Error() != tc.msg {
			t.Errorf("delta=%d note=%q: err = %v; want %q", tc.delta, tc.note, err, tc.msg)
		}
	}
	mustTx(t, d, func(c *sql.Conn) error {
		_, err := Adjust(ctx, c, adminID, v.Target(), -2, "棚卸し")
		return err
	})
	if q := qtyOf(t, d, v.ID); q != 8 {
		t.Errorf("quantity = %d, want 8", q)
	}
}

func TestShipPrintedProduct(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "黒", "M", 10)
	mustTx(t, d, func(c *sql.Conn) error {
		_, err := ConvertToPrinted(ctx, c, adminID, v, "ロゴA", 10, 10, "")
		return err
	})
	ps, _ := repo.Products(ctx, d, "")
	mustTx(t, d, func(c *sql.Conn) error {
		_, err := Ship(ctx, c, adminID, ps[0].Target(), 4, "")
		return err
	})
	p, _ := repo.GetProduct(ctx, d, ps[0].ID)
	if p.Quantity != 6 {
		t.Errorf("printed quantity = %d, want 6", p.Quantity)
	}
	var pid sql.NullInt64
	d.QueryRowContext(ctx, "SELECT printed_product_id FROM stock_movement WHERE kind='ship'").Scan(&pid)
	if !pid.Valid || pid.Int64 != ps[0].ID {
		t.Errorf("ship movement should point at the printed product, got %v", pid)
	}
}

// ---- 同時リクエスト (別接続・別 goroutine) でも壊れない ------------------------------------

func TestConcurrentReceivesOnSameNewVariant(t *testing.T) {
	d := newDB(t)
	id := itemID(t, d, "Printstar")
	var wg sync.WaitGroup
	errs := make(chan error, 8*5)
	start := make(chan struct{})
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 5; i++ {
				errs <- tx(d, func(c *sql.Conn) error {
					it, err := repo.GetItem(ctx, c, id)
					if err != nil {
						return err
					}
					v, err := GetOrCreateVariant(ctx, c, it, "黒", "M", "")
					if err != nil {
						return err
					}
					_, err = Receive(ctx, c, adminID, v, 1, "")
					return err
				})
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("worker error: %v", err)
		}
	}
	if n := count(t, d, "variant"); n != 1 {
		t.Fatalf("variants = %d, want 1 (UNIQUE 衝突も、二重登録もなし)", n)
	}
	if q := scalar(t, d, "SELECT quantity FROM variant"); q != 40 {
		t.Errorf("quantity = %d, want 40", q)
	}
	if n := count(t, d, "stock_movement"); n != 40 {
		t.Errorf("movements = %d, want 40", n)
	}
}

func TestConcurrentShipNeverGoesNegative(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "黒", "M", 10)
	var ok, failed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 3; i++ { // 8×3=24回 ×1枚 を出荷しようとする (在庫は10)
				err := tx(d, func(c *sql.Conn) error {
					_, err := Ship(ctx, c, adminID, v.Target(), 1, "")
					return err
				})
				switch {
				case err == nil:
					ok.Add(1)
				case IsStockError(err):
					failed.Add(1)
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 10 || failed.Load() != 14 {
		t.Errorf("ok=%d failed=%d, want 10 / 14", ok.Load(), failed.Load())
	}
	if q := qtyOf(t, d, v.ID); q != 0 {
		t.Errorf("quantity = %d, want 0", q)
	}
}

// ---- Flask 版 tests/test_robustness.py の、サービス層で成り立つ部分 ------------------------

func TestQuantityStaysIntegerEvenWhenAHugeQtyIsSent(t *testing.T) {
	d := newDB(t)
	v := receive(t, d, "Printstar", "赤", "M", 5)
	err := tx(d, func(c *sql.Conn) error {
		_, err := Receive(ctx, c, adminID, v, util.Int64Max, "")
		return err
	})
	wantStockError(t, err, "数量は1,000,000以下で入力してください。")
	var q int64
	var typ string
	if err := d.QueryRowContext(ctx, "SELECT quantity, typeof(quantity) FROM variant WHERE id = ?", v.ID).Scan(&q, &typ); err != nil {
		t.Fatal(err)
	}
	if q != 5 || typ != "integer" {
		t.Errorf("quantity = %d (%s), want 5 (integer)", q, typ)
	}
}

func TestPerOperationLimitIsEnforcedEverywhere(t *testing.T) {
	d := newDB(t)
	const over = util.MaxQty + 1
	const msg = "1,000,000以下"
	// 上限ちょうどは通る。超えると、登録されない
	err := tx(d, func(c *sql.Conn) error {
		v, err := GetOrCreateVariant(ctx, c, tee(t, c, "Printstar"), "赤", "M", "")
		if err != nil {
			return err
		}
		_, err = Receive(ctx, c, adminID, v, over, "")
		return err
	})
	if !IsStockError(err) || !strings.Contains(err.Error(), msg) {
		t.Fatalf("receive over limit: err = %v", err)
	}
	if n := count(t, d, "variant"); n != 0 {
		t.Errorf("variants = %d: 上限超えの入荷で variant が残っている", n)
	}
	v := receive(t, d, "Printstar", "赤", "M", util.MaxQty)
	if q := qtyOf(t, d, v.ID); q != util.MaxQty {
		t.Fatalf("quantity = %d", q)
	}
	for _, tc := range []struct {
		name string
		fn   func(c *sql.Conn) error
	}{
		{"ship", func(c *sql.Conn) error { _, err := Ship(ctx, c, adminID, v.Target(), over, ""); return err }},
		{"adjust+", func(c *sql.Conn) error { _, err := Adjust(ctx, c, adminID, v.Target(), over, "x"); return err }},
		{"adjust-", func(c *sql.Conn) error { _, err := Adjust(ctx, c, adminID, v.Target(), -over, "x"); return err }},
		{"convert", func(c *sql.Conn) error {
			_, err := ConvertToPrinted(ctx, c, adminID, v, "x", over, 1, "")
			return err
		}},
	} {
		err := tx(d, tc.fn)
		if !IsStockError(err) || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: err = %v; want 〜%s", tc.name, err, msg)
		}
	}
	if q := qtyOf(t, d, v.ID); q != util.MaxQty {
		t.Errorf("quantity changed by rejected operations: %d", q)
	}
}

func TestStockErrorIsDistinguishableFromDatabaseErrors(t *testing.T) {
	if IsStockError(sql.ErrNoRows) || IsStockError(nil) {
		t.Error("non-StockError reported as StockError")
	}
	if !IsStockError(fmt.Errorf("wrapped: %w", &StockError{Msg: "x"})) {
		t.Error("wrapped StockError not recognized")
	}
}

func TestNewUUIDFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		u := newUUID()
		if len(u) != 36 || u[8] != '-' || u[13] != '-' || u[18] != '-' || u[23] != '-' || u[14] != '4' ||
			!strings.ContainsRune("89ab", rune(u[19])) || u != strings.ToLower(u) {
			t.Fatalf("not a UUID v4: %q", u)
		}
		if seen[u] {
			t.Fatalf("duplicate UUID %q", u)
		}
		seen[u] = true
	}
}

func TestFormatQty(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1000000: "1,000,000", 1234567: "1,234,567"} {
		if got := formatQty(n); got != want {
			t.Errorf("formatQty(%d) = %q, want %q", n, got, want)
		}
	}
}
