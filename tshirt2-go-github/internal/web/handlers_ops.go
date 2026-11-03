package web

// 手順4-2: 書き込み系の画面 (入荷・プリント変換・出荷/調整) と、履歴・備考・CSV出力。
// Flask 版 app/inventory/routes.py の receive / convert / move / history / notes / export に対応。
//
// 検証に失敗したときは、Flask 版と同じく StockError の文言を danger で flash し、
// 送信値を保持したままフォームを再表示する (ステータス 200)。

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"unicode/utf8"

	"tshirt2/internal/repo"
	"tshirt2/internal/service"
	"tshirt2/internal/util"
)

// historyPerPage は履歴の1ページの件数 (Flask 版 config.py の HISTORY_PER_PAGE)。
const historyPerPage = 50

// QtyInput は _macros.html の "qty_input" 部品への入力 (Flask 版 qty_input マクロ)。
type QtyInput struct {
	Name, Value, Label string
	Negative           bool // 調整用: マイナス入力を許可する (data-allow-negative)
}

// opsForm は送信値の保持用 (Flask 版 form=request.form)。再表示のときに、そのまま出す。
type opsForm struct {
	ItemID, Color, Size, VariantName, Qty, Note string
	VariantID, Design, DesignNew, Used, Output  string
	Kind, Target                                string
}

// formInt はフォーム/クエリの文字列を整数にする。変換できなければ def
// (Flask 版 to_int(raw, def))。検証はサービス層が担うので、既定値は「検証に落ちる値」にしておく
// (数量なら 0、完成数なら -1)。
func formInt(raw string, def int64) int64 {
	if n, ok := util.ToInt(raw); ok {
		return n
	}
	return def
}

func stockErrorf(format string, a ...any) error {
	return &service.StockError{Msg: fmt.Sprintf(format, a...)}
}

// flashStock は、Tx の結果を画面へ伝える。StockError なら danger で flash して false、
// 成功なら true、それ以外のエラーはそのまま返す (エラーページになる)。
func flashStock(r *http.Request, err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if service.IsStockError(err) {
		Flash(r, "danger", err.Error())
		return false, nil
	}
	return false, err
}

// noteList は _notes.html の "note_list" 部品への入力を組み立てる (handlers_read.go の NoteList)。
func (s *Server) noteList(r *http.Request, notes []repo.Note) NoteList {
	isAdmin := false
	if u := CurrentUser(r); u != nil {
		isAdmin = u.IsAdmin()
	}
	return NoteList{Notes: notes, Back: r.URL.RequestURI(),
		IsAdmin: isAdmin, CSRF: stateOf(r).csrfToken()}
}

// noteTargets は備考の対象に選べる値 (その品番にある色・種類名。Flask 版 note_targets)。
func noteTargets(vs []repo.Variant) []string {
	set := map[string]bool{}
	for _, v := range vs {
		for _, t := range []string{v.Color, v.VariantName} {
			if t != "" {
				set[t] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// registerOps は書き込み系・履歴・備考・CSV のルートを登録する。
// ハーネス (テスト) と routes() の両方から呼ばれても壊れないよう、二重登録を防ぐ。
var opsRegistered sync.Map // *Server -> bool

func (s *Server) registerOps() {
	if _, loaded := opsRegistered.LoadOrStore(s, true); loaded {
		return
	}
	s.Page("GET /receive", s.receive)
	s.Page("POST /receive", s.receive)
	s.Page("GET /convert", s.convert)
	s.Page("POST /convert", s.convert)
	s.Page("GET /move", s.move)
	s.Page("POST /move", s.move)
	s.Page("GET /history", s.history)
	s.Page("GET /notes", s.notesList)
	s.Page("POST /notes", s.noteAdd)
	s.Page("POST /notes/{id}/resolve", s.noteResolve)
	s.Page("POST /notes/{id}/delete", s.noteDelete)
	s.Page("GET /export/blank.csv", s.exportBlank)
	s.Page("GET /export/printed.csv", s.exportPrinted)
}

// ---- 入荷 ------------------------------------------------------------

type receivePage struct {
	Items     []repo.Item
	Selected  int64
	Colors    []string
	Form      opsForm
	Qty       QtyInput
	ItemNotes []repo.Note
	NoteList  NoteList
	Targets   []string
}

func (s *Server) receive(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := s.DB()
	user := CurrentUser(r)
	form := opsForm{
		ItemID: r.PostFormValue("item_id"), Color: r.PostFormValue("color"),
		Size: r.PostFormValue("size"), VariantName: r.PostFormValue("variant_name"),
		Qty: r.PostFormValue("qty"), Note: r.PostFormValue("note"),
	}
	// Flask 版: selected = to_int(request.values.get("item_id")) (values はクエリが優先)
	rawSel := r.URL.Query().Get("item_id")
	if rawSel == "" {
		rawSel = form.ItemID
	}
	selected := formInt(rawSel, 0)

	if r.Method == http.MethodPost {
		var item *repo.Item
		var err error
		if selected != 0 {
			if item, err = repo.GetItem(ctx, q, selected); err != nil {
				return err
			}
		}
		qty := formInt(form.Qty, 0)
		var v *repo.Variant
		var opErr error
		if item == nil {
			opErr = stockErrorf("品番を選んでください。")
		} else {
			opErr = s.DB().Tx(ctx, func(conn *sql.Conn) error {
				var err error
				if v, err = service.GetOrCreateVariant(ctx, conn, item, form.Color, form.Size, form.VariantName); err != nil {
					return err
				}
				_, err = service.Receive(ctx, conn, user.ID, v, qty, form.Note)
				return err
			})
		}
		if ok, err := flashStock(r, opErr); err != nil {
			return err
		} else if ok {
			Flash(r, "success", fmt.Sprintf("入荷を登録しました: %s +%d", v.Label(), qty))
			http.Redirect(w, r, fmt.Sprintf("/receive?item_id=%d", item.ID), http.StatusFound)
			return nil
		}
	}

	items, err := repo.ItemsForSelect(ctx, q)
	if err != nil {
		return err
	}
	colors, err := repo.UsedColors(ctx, q)
	if err != nil {
		return err
	}
	var itemNotes []repo.Note
	var targets []string
	if selected != 0 {
		if itemNotes, err = repo.Notes(ctx, q, repo.NotesFilter{ItemID: selected}); err != nil {
			return err
		}
		vs, err := repo.VariantsOfItem(ctx, q, selected)
		if err != nil {
			return err
		}
		targets = noteTargets(vs)
	}
	return s.Render(w, r, http.StatusOK, "inventory/receive.html", receivePage{
		Items: items, Selected: selected, Colors: colors, Form: form,
		Qty:       QtyInput{Name: "qty", Value: form.Qty, Label: "数量"},
		ItemNotes: itemNotes, NoteList: s.noteList(r, itemNotes), Targets: targets,
	})
}

// ---- プリント変換 ----------------------------------------------------

type convertPage struct {
	Variants []repo.Variant
	Designs  []repo.Design
	Form     opsForm
	Used     QtyInput
	Output   QtyInput
}

func (s *Server) convert(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := s.DB()
	user := CurrentUser(r)
	form := opsForm{
		VariantID: r.PostFormValue("variant_id"), Design: r.PostFormValue("design"),
		DesignNew: r.PostFormValue("design_new"), Used: r.PostFormValue("used"),
		Output: r.PostFormValue("output"), Note: r.PostFormValue("note"),
	}
	if r.Method == http.MethodPost {
		v, err := repo.GetVariant(ctx, q, formInt(form.VariantID, 0))
		if err != nil {
			return err
		}
		used := formInt(form.Used, 0)
		output := formInt(form.Output, -1) // 不正な完成数は「0以上で入力」を出すため -1 にする
		designName := util.PyStrip(form.DesignNew)
		if designName == "" {
			designName = form.Design
		}
		var opErr error
		if v == nil {
			opErr = stockErrorf("無地在庫を選んでください。")
		} else {
			opErr = s.DB().Tx(ctx, func(conn *sql.Conn) error {
				_, err := service.ConvertToPrinted(ctx, conn, user.ID, v, designName, used, output, form.Note)
				return err
			})
		}
		if ok, err := flashStock(r, opErr); err != nil {
			return err
		} else if ok {
			Flash(r, "success", fmt.Sprintf("プリント変換しました: %s / %s (使用 %d → 完成 %d)",
				designName, v.Label(), used, output))
			http.Redirect(w, r, "/convert", http.StatusFound)
			return nil
		}
	}
	variants, err := repo.PrintableVariantsInStock(ctx, q)
	if err != nil {
		return err
	}
	designs, err := repo.Designs(ctx, q)
	if err != nil {
		return err
	}
	return s.Render(w, r, http.StatusOK, "inventory/convert.html", convertPage{
		Variants: variants, Designs: designs, Form: form,
		Used:   QtyInput{Name: "used", Value: form.Used, Label: "使用数(無地から減る数)"},
		Output: QtyInput{Name: "output", Value: form.Output, Label: "完成数(プリント済みに増える数)"},
	})
}

// ---- 出荷・棚卸し調整 ------------------------------------------------

type movePage struct {
	Variants []repo.Variant
	Products []repo.Product
	Form     opsForm
	Qty      QtyInput
}

func (s *Server) move(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := s.DB()
	user := CurrentUser(r)
	form := opsForm{
		Kind: r.PostFormValue("kind"), Target: r.PostFormValue("target"),
		Qty: r.PostFormValue("qty"), Note: r.PostFormValue("note"),
	}
	if r.Method == http.MethodPost {
		target, err := repo.GetTarget(ctx, q, form.Target)
		if err != nil {
			return err
		}
		qty := formInt(form.Qty, 0)
		var opErr error
		if target == nil {
			opErr = stockErrorf("対象の在庫を選んでください。")
		} else {
			opErr = s.DB().Tx(ctx, func(conn *sql.Conn) error {
				switch form.Kind {
				case "ship":
					_, err := service.Ship(ctx, conn, user.ID, target.Target, qty, form.Note)
					return err
				case "adjust":
					_, err := service.Adjust(ctx, conn, user.ID, target.Target, qty, form.Note)
					return err
				}
				return stockErrorf("操作の種類を選んでください。")
			})
		}
		if ok, err := flashStock(r, opErr); err != nil {
			return err
		} else if ok {
			shown := fmt.Sprintf("-%d", qty)
			if form.Kind == "adjust" {
				shown = fmt.Sprintf("%+d", qty)
			}
			Flash(r, "success", fmt.Sprintf("%sを登録しました: %s (%s)",
				repo.KindLabels[form.Kind], target.Label(), shown))
			http.Redirect(w, r, "/move", http.StatusFound)
			return nil
		}
	}
	variants, err := repo.AllVariants(ctx, q)
	if err != nil {
		return err
	}
	products, err := repo.Products(ctx, q, "")
	if err != nil {
		return err
	}
	return s.Render(w, r, http.StatusOK, "inventory/move.html", movePage{
		Variants: variants, Products: products, Form: form,
		Qty: QtyInput{Name: "qty", Value: form.Qty, Label: "数量(調整のときは ± 可)", Negative: true},
	})
}

// ---- 履歴 ------------------------------------------------------------

type kindOption struct{ Key, Label string }

// kindOptions は履歴の種別の選択肢 (Flask 版 models.KINDS の並び順)。
var kindOptions = []kindOption{
	{"receive", "入荷"}, {"print_use", "プリント使用"}, {"print_output", "プリント完成"},
	{"ship", "出荷"}, {"adjust", "棚卸し調整"},
}

type historyPage struct {
	Pagination repo.MovementPage
	Kind       string
	Kinds      []kindOption
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) error {
	page := formInt(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	if page > 100000 { // OFFSET の計算が溢れないように (Flask 版と同じ上限)
		page = 100000
	}
	kind := r.URL.Query().Get("kind")
	pg, err := repo.MovementsPage(r.Context(), s.DB(), kind, page, historyPerPage)
	if err != nil {
		return err
	}
	return s.Render(w, r, http.StatusOK, "inventory/history.html",
		historyPage{Pagination: pg, Kind: kind, Kinds: kindOptions})
}

// ---- 備考 ------------------------------------------------------------

type notePanel struct {
	Note repo.Note
	List NoteList
}

// Q は備考の見出しリンクの検索語 (Flask 版: item_no が無ければ brand)。
func (p notePanel) Q() string {
	if p.Note.ItemNo != "" {
		return p.Note.ItemNo
	}
	return p.Note.Brand
}

type notesPage struct {
	Panels  []notePanel
	ShowAll bool
}

func (s *Server) notesList(w http.ResponseWriter, r *http.Request) error {
	showAll := r.URL.Query().Get("all") == "1"
	notes, err := repo.Notes(r.Context(), s.DB(), repo.NotesFilter{IncludeResolved: showAll})
	if err != nil {
		return err
	}
	panels := make([]notePanel, 0, len(notes))
	for _, n := range notes {
		panels = append(panels, notePanel{Note: n, List: s.noteList(r, []repo.Note{n})})
	}
	return s.Render(w, r, http.StatusOK, "inventory/notes.html",
		notesPage{Panels: panels, ShowAll: showAll})
}

func (s *Server) noteBack(r *http.Request) string {
	if back := util.SafeNext(r.PostFormValue("back")); back != "" {
		return back
	}
	return "/notes"
}

func (s *Server) noteAdd(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	itemID := formInt(r.PostFormValue("item_id"), 0)
	body := util.PyStrip(r.PostFormValue("body"))
	target := util.PyStrip(r.PostFormValue("target"))
	item, err := repo.GetItem(ctx, s.DB(), itemID)
	if err != nil {
		return err
	}
	switch {
	case item == nil:
		Flash(r, "danger", "品番が見つかりません。")
	case body == "":
		Flash(r, "danger", "備考の内容を入力してください。")
	case utf8.RuneCountInString(body) > 1000 || utf8.RuneCountInString(target) > 64:
		Flash(r, "danger", "備考は1000文字まで、対象は64文字までです。")
	default:
		user := CurrentUser(r)
		err := s.DB().Tx(ctx, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx,
				"INSERT INTO item_note (item_id, target, body, is_resolved, user_id, created_at) "+
					"VALUES (?, ?, ?, 0, ?, datetime('now'))",
				itemID, target, body, user.ID)
			return err
		})
		if err != nil {
			return err
		}
		Flash(r, "success", "備考を追加しました。")
	}
	http.Redirect(w, r, s.noteBack(r), http.StatusFound)
	return nil
}

func (s *Server) noteResolve(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		return NotFound()
	}
	if _, found, err := repo.GetNote(ctx, s.DB(), id); err != nil {
		return err
	} else if !found {
		return NotFound()
	}
	err := s.DB().Tx(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx,
			"UPDATE item_note SET is_resolved = 1 - is_resolved WHERE id = ?", id)
		return err
	})
	if err != nil {
		return err
	}
	http.Redirect(w, r, s.noteBack(r), http.StatusFound)
	return nil
}

func (s *Server) noteDelete(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if !CurrentUser(r).IsAdmin() { // Flask 版: 管理者でなければ、存在確認より先に 403
		return Forbidden()
	}
	id, ok := pathID(r)
	if !ok {
		return NotFound()
	}
	if _, found, err := repo.GetNote(ctx, s.DB(), id); err != nil {
		return err
	} else if !found {
		return NotFound()
	}
	err := s.DB().Tx(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM item_note WHERE id = ?", id)
		return err
	})
	if err != nil {
		return err
	}
	Flash(r, "success", "備考を削除しました。")
	http.Redirect(w, r, s.noteBack(r), http.StatusFound)
	return nil
}

// ---- CSV出力 ----------------------------------------------------------

func (s *Server) writeCSV(w http.ResponseWriter, name string, headers []string, rows [][]any) error {
	body := service.ToCSVBytes(headers, rows)
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, service.CSVFilename(name, s.cfg.Now())))
	h.Set("Cache-Control", "no-store") // 在庫データなので、共有キャッシュに残さない
	_, err := w.Write(body)
	return err
}

func (s *Server) exportBlank(w http.ResponseWriter, r *http.Request) error {
	var cat *int64
	if n, ok := util.ToInt(r.URL.Query().Get("category")); ok {
		cat = &n // 存在しない ID は、見出しだけの CSV になる (Flask 版と同じ)
	}
	rows, err := service.BlankRows(r.Context(), s.DB(), cat)
	if err != nil {
		return err
	}
	return s.writeCSV(w, "inventory_blank", service.BlankHeaders, rows)
}

func (s *Server) exportPrinted(w http.ResponseWriter, r *http.Request) error {
	rows, err := service.PrintedRows(r.Context(), s.DB())
	if err != nil {
		return err
	}
	return s.writeCSV(w, "inventory_printed", service.PrintedHeaders, rows)
}
