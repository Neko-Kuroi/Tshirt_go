package web

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// 手順4-2 (入荷・プリント変換・出荷/調整・履歴・備考・CSV) のテスト。
// registerOps の routes() への配線は統合時に行われるので、ここで直接登録する
// (ハーネス側でも登録済みだが、二重登録は registerOps 側で防いでいる)。

func seedOps(t *testing.T, h *harness) {
	t.Helper()
	seedExec(t, h, `INSERT INTO category (id, name, sort_order, uses_color, uses_size, uses_variant_name, is_printable)
		VALUES (1, 'Tシャツ', 1, 1, 1, 0, 1), (2, 'バッジ', 2, 0, 0, 1, 0)`)
	seedExec(t, h, `INSERT INTO item (id, category_id, brand, item_no, name, style)
		VALUES (1, 1, 'Printstar', '085-CVT', 'Tシャツ', ''), (2, 2, '', '44mm', '缶バッジ', '')`)
}

// seedVariant は、品番1 (Printstar) の「ホワイト / M」を quantity 付きで作る (id=1)。
func seedVariant(t *testing.T, h *harness, qty int64) {
	t.Helper()
	seedExec(t, h, `INSERT INTO variant (id, item_id, color, size, variant_name, quantity)
		VALUES (1, 1, 'ホワイト', 'M', '', ?)`, qty)
}

func variantQty(t *testing.T, h *harness, id int64) int64 {
	t.Helper()
	var n int64
	if err := h.d.QueryRowContext(ctx, "SELECT quantity FROM variant WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func newOpsHarness(t *testing.T) (*harness, *client) {
	t.Helper()
	h := newHarness(t)
	h.srv.registerOps()
	seedOps(t, h)
	c := h.client()
	c.mustLogin("staff", false)
	return h, c
}

// ---- 入荷 ------------------------------------------------------------

func TestReceiveCreatesVariantAndStock(t *testing.T) {
	h, c := newOpsHarness(t)
	r := c.postCSRF("/receive", "/receive", url.Values{
		"item_id": {"1"}, "color": {"ホワイト"}, "size": {"M"}, "qty": {"10"}, "note": {"納品書1"}})
	if r.status != 302 || r.location() != "/receive?item_id=1" {
		t.Fatalf("receive: %d %q, want 302 /receive?item_id=1", r.status, r.location())
	}
	page := c.get(r.location())
	contains(t, page, "入荷を登録しました: Printstar 085-CVT Tシャツ | ホワイト / M &#43;10")
	contains(t, page, "alert-success")
	if n := variantQty(t, h, 1); n != 10 {
		t.Errorf("quantity = %d, want 10", n)
	}
	scalarEq(t, h, 1, "SELECT COUNT(*) FROM stock_movement WHERE kind = 'receive' AND delta = 10")

	// 同じ色・サイズでもう一度入荷すると、同じ variant に積み上がる
	r = c.postCSRF("/receive", "/receive", url.Values{
		"item_id": {"1"}, "color": {"ホワイト"}, "size": {"M"}, "qty": {"5"}})
	if r.status != 302 {
		t.Fatalf("receive again: %d", r.status)
	}
	if n := variantQty(t, h, 1); n != 15 {
		t.Errorf("quantity = %d, want 15", n)
	}
	scalarEq(t, h, 1, "SELECT COUNT(*) FROM variant")
}

func TestReceiveValidationKeepsFormAndRollsBack(t *testing.T) {
	h, c := newOpsHarness(t)

	r := c.postCSRF("/receive", "/receive", url.Values{"qty": {"5"}})
	if r.status != 200 {
		t.Fatalf("no item: %d, want 200 (再表示)", r.status)
	}
	contains(t, r, "品番を選んでください。")
	contains(t, r, "alert-danger")

	r = c.postCSRF("/receive", "/receive", url.Values{"item_id": {"1"}, "size": {"M"}, "qty": {"5"}})
	contains(t, r, "色を入力してください。")
	contains(t, r, `value="M"`) // 送信値が保持されている

	r = c.postCSRF("/receive", "/receive", url.Values{
		"item_id": {"1"}, "color": {"ホワイト"}, "size": {"M"}, "qty": {"abc"}})
	contains(t, r, "数量は1以上の整数で入力してください。")

	scalarEq(t, h, 0, "SELECT COUNT(*) FROM variant") // 失敗した入荷では variant も残らない
	scalarEq(t, h, 0, "SELECT COUNT(*) FROM stock_movement")
}

// ---- プリント変換 ----------------------------------------------------

func TestConvertFlow(t *testing.T) {
	h, c := newOpsHarness(t)
	seedVariant(t, h, 10)

	r := c.postCSRF("/convert", "/convert", url.Values{
		"variant_id": {"1"}, "design_new": {" ロゴ "}, "used": {"4"}, "output": {"3"}})
	if r.status != 302 || r.location() != "/convert" {
		t.Fatalf("convert: %d %q, want 302 /convert", r.status, r.location())
	}
	page := c.get("/convert")
	contains(t, page, "プリント変換しました: ロゴ / Printstar 085-CVT Tシャツ | ホワイト / M (使用 4 → 完成 3)")
	if n := variantQty(t, h, 1); n != 6 {
		t.Errorf("blank quantity = %d, want 6", n)
	}
	scalarEq(t, h, 3, "SELECT quantity FROM printed_product")
	scalarEq(t, h, 1, "SELECT COUNT(*) FROM stock_movement WHERE kind = 'print_use' AND delta = -4")
	scalarEq(t, h, 1, "SELECT COUNT(*) FROM stock_movement WHERE kind = 'print_output' AND delta = 3")

	// 既存デザインは select から選ぶ (design_new が空なら design が使われる)
	r = c.postCSRF("/convert", "/convert", url.Values{
		"variant_id": {"1"}, "design": {"ロゴ"}, "used": {"1"}, "output": {"1"}})
	if r.status != 302 {
		t.Fatalf("convert with existing design: %d", r.status)
	}
	scalarEq(t, h, 4, "SELECT quantity FROM printed_product")
	scalarEq(t, h, 1, "SELECT COUNT(*) FROM design")
}

func TestConvertValidation(t *testing.T) {
	h, c := newOpsHarness(t)
	seedVariant(t, h, 10)

	r := c.postCSRF("/convert", "/convert", url.Values{"used": {"1"}, "output": {"1"}, "design_new": {"x"}})
	contains(t, r, "無地在庫を選んでください。")

	r = c.postCSRF("/convert", "/convert", url.Values{
		"variant_id": {"1"}, "design_new": {"ロゴ"}, "used": {"4"}, "output": {"x"}})
	contains(t, r, "完成数は0以上の整数で入力してください。")

	r = c.postCSRF("/convert", "/convert", url.Values{
		"variant_id": {"1"}, "design_new": {"ロゴ"}, "used": {"99"}, "output": {"1"}})
	contains(t, r, "在庫が足りません。")
	if n := variantQty(t, h, 1); n != 10 {
		t.Errorf("quantity = %d, want 10 (失敗で減らない)", n)
	}
	scalarEq(t, h, 0, "SELECT COUNT(*) FROM printed_product")
}

// ---- 出荷・棚卸し調整 ------------------------------------------------

func TestMoveShipAndAdjust(t *testing.T) {
	h, c := newOpsHarness(t)
	seedVariant(t, h, 10)

	r := c.postCSRF("/move", "/move", url.Values{"kind": {"ship"}, "target": {"v:1"}, "qty": {"3"}})
	if r.status != 302 || r.location() != "/move" {
		t.Fatalf("ship: %d %q, want 302 /move", r.status, r.location())
	}
	contains(t, c.get("/move"), "出荷を登録しました: Printstar 085-CVT Tシャツ | ホワイト / M (-3)")
	if n := variantQty(t, h, 1); n != 7 {
		t.Errorf("quantity = %d, want 7", n)
	}

	r = c.postCSRF("/move", "/move", url.Values{
		"kind": {"adjust"}, "target": {"v:1"}, "qty": {"-2"}, "note": {"棚卸しで不足"}})
	if r.status != 302 {
		t.Fatalf("adjust: %d", r.status)
	}
	contains(t, c.get("/move"), "棚卸し調整を登録しました: Printstar 085-CVT Tシャツ | ホワイト / M (-2)")
	if n := variantQty(t, h, 1); n != 5 {
		t.Errorf("quantity = %d, want 5", n)
	}

	r = c.postCSRF("/move", "/move", url.Values{
		"kind": {"adjust"}, "target": {"v:1"}, "qty": {"4"}, "note": {"発見"}})
	contains(t, c.get("/move"), "(&#43;4)")
	if n := variantQty(t, h, 1); n != 9 {
		t.Errorf("quantity = %d, want 9", n)
	}
}

func TestMoveValidation(t *testing.T) {
	h, c := newOpsHarness(t)
	seedVariant(t, h, 10)

	r := c.postCSRF("/move", "/move", url.Values{"kind": {"ship"}, "qty": {"1"}})
	contains(t, r, "対象の在庫を選んでください。")

	r = c.postCSRF("/move", "/move", url.Values{"kind": {"x"}, "target": {"v:1"}, "qty": {"1"}})
	contains(t, r, "操作の種類を選んでください。")

	r = c.postCSRF("/move", "/move", url.Values{"kind": {"adjust"}, "target": {"v:1"}, "qty": {"1"}})
	contains(t, r, "棚卸し調整には理由を入力してください。")

	r = c.postCSRF("/move", "/move", url.Values{"kind": {"ship"}, "target": {"v:1"}, "qty": {"999"}})
	contains(t, r, "在庫が足りません。")

	if n := variantQty(t, h, 1); n != 10 {
		t.Errorf("quantity = %d, want 10 (失敗で変わらない)", n)
	}
	scalarEq(t, h, 0, "SELECT COUNT(*) FROM stock_movement")
}

// ---- 履歴 ------------------------------------------------------------

func TestHistoryPaginationAndFilter(t *testing.T) {
	h, c := newOpsHarness(t)
	seedVariant(t, h, 10)
	for i := 0; i < 55; i++ {
		seedExec(t, h, `INSERT INTO stock_movement (batch_id, kind, variant_id, delta, note, user_id, created_at)
			VALUES ('b', 'receive', 1, 1, '入荷メモ', 2, datetime('now'))`)
	}
	for i := 0; i < 2; i++ {
		seedExec(t, h, `INSERT INTO stock_movement (batch_id, kind, variant_id, delta, note, user_id, created_at)
			VALUES ('b', 'ship', 1, -1, '出荷メモ', 2, datetime('now'))`)
	}

	page := c.get("/history")
	if page.status != 200 {
		t.Fatalf("history: %d", page.status)
	}
	contains(t, page, "Printstar 085-CVT Tシャツ | ホワイト / M")
	contains(t, page, `<span class="num delta-pos">&#43;1</span>`) // html/template が '+' を &#43; にする (表示は +1)
	contains(t, page, `href="/history?page=2&kind="`)

	page2 := c.get("/history?page=2")
	contains(t, page2, "← 新しい方")
	if strings.Contains(page2.body, "page=3") {
		t.Errorf("page 2 must not link to page 3:\n%.300s", page2.body)
	}

	ships := c.get("/history?kind=ship")
	contains(t, ships, "出荷メモ")
	contains(t, ships, `<span class="num delta-neg">-1</span>`)
	notContains(t, ships, "入荷メモ")

	all := c.get("/history?kind=bogus") // 未知の kind は絞らない (Flask 版と同じ)
	contains(t, all, "入荷メモ")
	contains(t, all, "出荷メモ")
}

// ---- 備考 ------------------------------------------------------------

func TestNotesAddResolveDelete(t *testing.T) {
	h, c := newOpsHarness(t)

	r := c.postCSRF("/notes", "/notes", url.Values{
		"item_id": {"1"}, "target": {"ホワイト"}, "body": {"在庫わずか"}, "back": {"/notes"}})
	if r.status != 302 || r.location() != "/notes" {
		t.Fatalf("note add: %d %q, want 302 /notes", r.status, r.location())
	}
	page := c.get("/notes")
	contains(t, page, "備考を追加しました。")
	contains(t, page, "在庫わずか")
	contains(t, page, "ホワイト")
	var noteID int64
	if err := h.d.QueryRowContext(ctx, "SELECT id FROM item_note").Scan(&noteID); err != nil {
		t.Fatal(err)
	}

	// 対応済みにすると未対応の一覧から消え、all=1 で見える
	r = c.postCSRF("/notes", "/notes/"+strconv.FormatInt(noteID, 10)+"/resolve", url.Values{"back": {"/notes"}})
	if r.status != 302 {
		t.Fatalf("resolve: %d", r.status)
	}
	notContains(t, c.get("/notes"), "在庫わずか")
	done := c.get("/notes?all=1")
	contains(t, done, "在庫わずか")
	contains(t, done, "対応済み")

	// スタッフは削除できない (存在確認より先に 403)
	r = c.postCSRF("/notes", "/notes/"+strconv.FormatInt(noteID, 10)+"/delete", url.Values{"back": {"/notes"}})
	if r.status != 403 {
		t.Fatalf("staff delete: %d, want 403", r.status)
	}
	contains(t, r, "この操作をする権限がありません。")
	scalarEq(t, h, 1, "SELECT COUNT(*) FROM item_note")

	// 無い備考・整数でない ID は 404
	if r := c.postCSRF("/notes", "/notes/999/resolve", url.Values{}); r.status != 404 {
		t.Errorf("resolve missing: %d, want 404", r.status)
	}
	if r := c.postCSRF("/notes", "/notes/abc/resolve", url.Values{}); r.status != 404 {
		t.Errorf("resolve bad id: %d, want 404", r.status)
	}

	admin := h.client()
	admin.mustLogin("admin", false)
	if r := admin.postCSRF("/notes", "/notes/999/delete", url.Values{}); r.status != 404 {
		t.Errorf("admin delete missing: %d, want 404", r.status)
	}
	r = admin.postCSRF("/notes", "/notes/"+strconv.FormatInt(noteID, 10)+"/delete", url.Values{"back": {"/notes"}})
	if r.status != 302 {
		t.Fatalf("admin delete: %d", r.status)
	}
	contains(t, admin.get("/notes"), "備考を削除しました。")
	scalarEq(t, h, 0, "SELECT COUNT(*) FROM item_note")
}

func TestNoteAddValidation(t *testing.T) {
	h, c := newOpsHarness(t)

	r := c.postCSRF("/notes", "/notes", url.Values{"item_id": {"999"}, "body": {"x"}, "back": {"/notes"}})
	if r.status != 302 {
		t.Fatalf("add: %d", r.status)
	}
	contains(t, c.get("/notes"), "品番が見つかりません。")

	c.postCSRF("/notes", "/notes", url.Values{"item_id": {"1"}, "body": {""}, "back": {"/notes"}})
	contains(t, c.get("/notes"), "備考の内容を入力してください。")

	c.postCSRF("/notes", "/notes", url.Values{
		"item_id": {"1"}, "body": {strings.Repeat("あ", 1001)}, "back": {"/notes"}})
	contains(t, c.get("/notes"), "備考は1000文字まで、対象は64文字までです。")

	scalarEq(t, h, 0, "SELECT COUNT(*) FROM item_note")
}

// ---- CSV出力 ----------------------------------------------------------

func TestExportBlankCSV(t *testing.T) {
	h, c := newOpsHarness(t)
	seedVariant(t, h, 10)
	seedExec(t, h, `INSERT INTO variant (id, item_id, color, size, variant_name, quantity)
		VALUES (2, 2, '', '', '丸型', 5)`)
	seedExec(t, h, `INSERT INTO design (id, name, note) VALUES (1, 'ロゴ', '')`)
	seedExec(t, h, `INSERT INTO printed_product (id, design_id, variant_id, quantity) VALUES (1, 1, 1, 3)`)

	r := c.get("/export/blank.csv")
	if r.status != 200 {
		t.Fatalf("export blank: %d", r.status)
	}
	if ct := r.header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := r.header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	cd := r.header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="inventory_blank_`) || !strings.HasSuffix(cd, `.csv"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !strings.HasPrefix(r.body, "\ufeff") {
		t.Errorf("body must start with BOM: %q", r.body[:10])
	}
	contains(t, r, "カテゴリー,ブランド,品番,商品名,色,サイズ,種類名,数量,品番の状態\r\n")
	contains(t, r, "Tシャツ,Printstar,085-CVT,Tシャツ,ホワイト,M,,10,使用中")
	contains(t, r, "バッジ,,44mm,缶バッジ,,,丸型,5,使用中")

	one := c.get("/export/blank.csv?category=2")
	contains(t, one, "バッジ,,44mm,缶バッジ,,,丸型,5,使用中")
	notContains(t, one, "Printstar")

	none := c.get("/export/blank.csv?category=999")
	if want := "\ufeffカテゴリー,ブランド,品番,商品名,色,サイズ,種類名,数量,品番の状態\r\n"; none.body != want {
		t.Errorf("category=999 body = %q, want header only %q", none.body, want)
	}
}

func TestExportPrintedCSV(t *testing.T) {
	_, c := newOpsHarness(t)
	seedVariant(t, c.h, 10)
	seedExec(t, c.h, `INSERT INTO design (id, name, note) VALUES (1, 'ロゴ', '')`)
	seedExec(t, c.h, `INSERT INTO printed_product (id, design_id, variant_id, quantity) VALUES (1, 1, 1, 3)`)

	r := c.get("/export/printed.csv")
	if r.status != 200 {
		t.Fatalf("export printed: %d", r.status)
	}
	if ct := r.header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := r.header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="inventory_printed_`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	contains(t, r, "デザイン,カテゴリー,ブランド,品番,商品名,色,サイズ,種類名,数量\r\n")
	contains(t, r, "ロゴ,Tシャツ,Printstar,085-CVT,Tシャツ,ホワイト,M,,3")
}
