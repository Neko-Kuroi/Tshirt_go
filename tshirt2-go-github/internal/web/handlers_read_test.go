package web

import (
	"strings"
	"testing"
)

// newReadHarness は、読み取り系の画面を登録したハーネス。
// registerRead の routes() への配線は統合時に行われるので、ここで直接登録する。
// /{$} だけは routes() の仮ダッシュボードと重なって panic になるため受け止め、
// ダッシュボード自体は /test/dashboard からも開けるようにして検証する。
func newReadHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	func() {
		defer func() { _ = recover() }()
		h.srv.registerRead()
	}()
	h.srv.Page("GET /test/dashboard", h.srv.dashboard)
	return h
}

func seedExec(t *testing.T, h *harness, sql string, args ...any) {
	t.Helper()
	if _, err := h.d.ExecContext(ctx, sql, args...); err != nil {
		t.Fatalf("seed: %v (%s)", err, sql)
	}
}

// seedReadData は、読み取り系の画面の確認用データ。
// カテゴリー1「Tシャツ」(色・サイズ・プリント可)、カテゴリー2「バッジ」(種類名のみ)。
// Tシャツ: 白 M=10 / L=2 (low)、黒 M=0 (zero) / L は行なし (– になる)。バッジ: 丸型=5。
func seedReadData(t *testing.T, h *harness) {
	t.Helper()
	seedExec(t, h, `INSERT INTO category (id, name, sort_order, uses_color, uses_size, uses_variant_name, is_printable)
		VALUES (1, 'Tシャツ', 1, 1, 1, 0, 1), (2, 'バッジ', 2, 0, 0, 1, 0)`)
	seedExec(t, h, `INSERT INTO item (id, category_id, brand, item_no, name, style)
		VALUES (1, 1, 'Fruit', '001', 'ヘビーウェイトT', '半袖'), (2, 2, '', 'B1', '缶バッジ', '')`)
	seedExec(t, h, `INSERT INTO variant (item_id, color, size, variant_name, quantity) VALUES
		(1, '白', 'M', '', 10), (1, '白', 'L', '', 2), (1, '黒', 'M', '', 0), (2, '', '', '丸型', 5)`)
	seedExec(t, h, `INSERT INTO design (id, name) VALUES (1, 'ロゴA'), (2, 'ロゴB')`)
	seedExec(t, h, `INSERT INTO printed_product (design_id, variant_id, quantity)
		SELECT 1, id, 4 FROM variant WHERE item_id = 1 AND color = '白' AND size = 'M'`)
	seedExec(t, h, `INSERT INTO printed_product (design_id, variant_id, quantity)
		SELECT 2, id, 3 FROM variant WHERE item_id = 1 AND color = '白' AND size = 'L'`)
	seedExec(t, h, `INSERT INTO item_note (item_id, target, body, is_resolved, user_id)
		VALUES (1, '白', '白は補充注意', 0, 1)`)
	seedExec(t, h, `INSERT INTO stock_movement (batch_id, kind, variant_id, delta, user_id, created_at)
		SELECT 'b1', 'receive', id, 10, 1, '2026-11-01 00:00:00'
		FROM variant WHERE item_id = 1 AND color = '白' AND size = 'M'`)
}

func TestDashboardCardsNotesAndRecent(t *testing.T) {
	h := newReadHarness(t)
	seedReadData(t, h)
	c := h.client()
	c.mustLogin("admin", false)
	r := c.get("/test/dashboard")
	if r.status != 200 {
		t.Fatalf("dashboard: %d %.200s", r.status, r.body)
	}
	contains(t, r, "Tシャツ")
	contains(t, r, `<div class="fs-3 num text-start">12</div>`) // 無地の合計 (10+2+0)
	contains(t, r, `<div class="fs-5 num text-start">7</div>`)  // プリント済みの合計 (4+3)
	contains(t, r, "バッジ")
	contains(t, r, `<div class="fs-3 num text-start">5</div>`)
	contains(t, r, `<span class="badge text-bg-warning">1</span>`) // 未対応の備考の件数
	contains(t, r, "白は補充注意")
	contains(t, r, "11/01 09:00") // UTC 00:00 → JST
	contains(t, r, "入荷")
	contains(t, r, "Fruit 001 ヘビーウェイトT | 白 / M")
	contains(t, r, `<span class="num delta-pos">&#43;10</span>`) // html/template が '+' を &#43; にする (表示は +10)
	contains(t, r, "admin")
}

func TestBlankMatrix(t *testing.T) {
	h := newReadHarness(t)
	seedReadData(t, h)
	c := h.client()
	c.mustLogin("admin", false)

	r := c.get("/blank") // カテゴリー指定なし → 先頭のカテゴリー
	if r.status != 200 {
		t.Fatalf("blank: %d %.200s", r.status, r.body)
	}
	contains(t, r, "色 / 種類")
	contains(t, r, "<th>M</th>")
	contains(t, r, "<th>L</th>")
	contains(t, r, "<th>計</th>")
	contains(t, r, `白 <span class="badge text-bg-warning">備考</span>`) // 「白」対象の備考がある行
	contains(t, r, `<td class="num low">2</td>`)                      // 白 L (3以下は low)
	contains(t, r, `<td class="num zero">0</td>`)                     // 黒 M
	contains(t, r, `<td class="num zero">–</td>`)                     // 黒 L は行がない
	contains(t, r, `<td class="num fw-bold">12</td>`)                 // 白の行合計
	contains(t, r, "白は補充注意")                                          // 備考の一覧
	contains(t, r, `action="/notes/1/resolve"`)
	contains(t, r, `action="/notes/1/delete"`) // 管理者には削除フォーム
	contains(t, r, "この備考を削除しますか?")
	contains(t, r, `<option value="白">`) // 備考フォームの対象候補
	contains(t, r, `<option value="黒">`)
	contains(t, r, `href="/receive?item_id=1"`)
	contains(t, r, `href="/export/blank.csv?category=1"`)
	contains(t, r, `href="/blank/list?category=1"`)

	// スタッフには削除フォームを出さない
	s := h.client()
	s.mustLogin("staff", false)
	rs := s.get("/blank")
	contains(t, rs, `action="/notes/1/resolve"`)
	notContains(t, rs, `action="/notes/1/delete"`)

	// 存在しないカテゴリーは、先頭のカテゴリーに寄せる
	r = c.get("/blank?category=999")
	if r.status != 200 {
		t.Fatalf("blank fallback: %d", r.status)
	}
	contains(t, r, "ヘビーウェイトT")

	// 種類名だけのカテゴリー (サイズ無し → 計の列なし、見出しは「種類名」、列は「数量」)
	r = c.get("/blank?category=2")
	contains(t, r, "種類名")
	contains(t, r, "<th>数量</th>")
	contains(t, r, "丸型")
	notContains(t, r, "<th>計</th>")
}

func TestStockListFilterAndSortLinks(t *testing.T) {
	h := newReadHarness(t)
	seedReadData(t, h)
	c := h.client()
	c.mustLogin("staff", false)

	r := c.get("/blank/list?color=白")
	if r.status != 200 {
		t.Fatalf("stock list: %d %.200s", r.status, r.body)
	}
	contains(t, r, `<span class="num">2</span> 件`)   // 白の2行
	contains(t, r, `合計 <span class="num">12</span>`) // 10+2
	// 並べ替えリンクは、いまの絞り込み (正規化済み) を引き継ぐ
	contains(t, r, `href="/blank/list?color=%E7%99%BD&amp;sort=qty&amp;dir=asc"`)
	contains(t, r, `<option value="白" selected>白</option>`)

	// 並べ替え中の列は、リンクの向きが反転し、向きの印が付く
	r = c.get("/blank/list?sort=qty")
	contains(t, r, `<a class="text-decoration-none" href="/blank/list?sort=qty&amp;dir=desc">数量 ▲</a>`)
	r = c.get("/blank/list?sort=qty&dir=desc")
	contains(t, r, `<a class="text-decoration-none" href="/blank/list?sort=qty&amp;dir=asc">数量 ▼</a>`)

	// カテゴリーで絞ると、そのカテゴリーの行だけになる
	r = c.get("/blank/list?category=2")
	contains(t, r, `<span class="num">1</span> 件`)
	contains(t, r, `<option value="2" selected>バッジ</option>`)
	contains(t, r, "丸型")
	notContains(t, r, "ヘビーウェイトT")
}

func TestPrintedGroups(t *testing.T) {
	h := newReadHarness(t)
	seedReadData(t, h)
	c := h.client()
	c.mustLogin("staff", false)
	r := c.get("/printed")
	if r.status != 200 {
		t.Fatalf("printed: %d %.200s", r.status, r.body)
	}
	contains(t, r, "ロゴA")
	contains(t, r, `<span class="num text-muted">4</span>`) // ロゴA の合計
	contains(t, r, "ロゴB")
	contains(t, r, `<span class="num text-muted">3</span>`) // ロゴB の合計
	contains(t, r, "Fruit 001 ヘビーウェイトT")
	contains(t, r, "白 / M")
	if strings.Index(r.body, "ロゴA") > strings.Index(r.body, "ロゴB") {
		t.Error("groups are not in first-seen (design name) order")
	}
}

func TestReadPagesRequireLogin(t *testing.T) {
	h := newReadHarness(t)
	for _, p := range []string{"/blank", "/blank/list", "/printed", "/test/dashboard"} {
		if r := h.client().get(p); r.status != 302 {
			t.Errorf("%s: %d, want 302 to login", p, r.status)
		}
	}
}
