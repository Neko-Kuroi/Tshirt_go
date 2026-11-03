package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"tshirt2/internal/repo"
	"tshirt2/internal/service"
	"tshirt2/internal/util"
)

// registerRead は読み取り系の画面 (手順4-1) を登録する。
// 統合時に routes() から呼ばれる想定 (いまはテストから直接呼ぶ)。
func (s *Server) registerRead() {
	s.Page("GET /blank", s.blankList)
	s.Page("GET /blank/list", s.stockListPage)
	s.Page("GET /printed", s.printedList)
	s.Page("GET /{$}", s.dashboard)
}

// NoteList は _notes.html の "note_list" 部品への入力。
type NoteList struct {
	Notes   []repo.Note
	Back    string
	IsAdmin bool
	CSRF    string
}

// NoteForm は _notes.html の "note_form" 部品への入力。
type NoteForm struct {
	ItemID  int64
	Targets []string
	Back    string
	CSRF    string
}

// ---- ダッシュボード --------------------------------------------------

type dashboardCard struct {
	Category repo.Category
	Blank    int64
	Printed  int64
}

type dashboardData struct {
	Cards     []dashboardCard
	Notes     []repo.Note
	NoteCount int64
	Remaining int64 // 未対応の備考のうち、表示しきれなかった件数
	Recent    []repo.Movement
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := s.DB()
	cats, err := repo.Categories(ctx, q)
	if err != nil {
		return err
	}
	blank, err := repo.BlankTotals(ctx, q)
	if err != nil {
		return err
	}
	printed, err := repo.PrintedTotals(ctx, q)
	if err != nil {
		return err
	}
	recent, err := repo.RecentMovements(ctx, q, 8)
	if err != nil {
		return err
	}
	notes, err := repo.Notes(ctx, q, repo.NotesFilter{Limit: 5})
	if err != nil {
		return err
	}
	count, err := repo.OpenNoteCount(ctx, q)
	if err != nil {
		return err
	}
	cards := make([]dashboardCard, len(cats))
	for i, c := range cats {
		cards[i] = dashboardCard{Category: c, Blank: blank[c.ID], Printed: printed[c.ID]}
	}
	remaining := count - int64(len(notes))
	if remaining < 0 {
		remaining = 0
	}
	return s.Render(w, r, http.StatusOK, "inventory/dashboard.html", dashboardData{
		Cards: cards, Notes: notes, NoteCount: count, Remaining: remaining, Recent: recent,
	})
}

// ---- 無地在庫 (マトリクス) -------------------------------------------

type blankItem struct {
	Matrix
	NoteList NoteList
	NoteForm NoteForm
}

type blankData struct {
	Cats  []repo.Category
	Cat   *repo.Category
	Items []blankItem
	Q     string
}

func (s *Server) blankList(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := s.DB()
	cats, err := repo.Categories(ctx, q)
	if err != nil {
		return err
	}
	// ?category= が無効なら、先頭のカテゴリーに寄せる (Flask 版と同じ)
	var cat *repo.Category
	if id, ok := util.ToInt(r.URL.Query().Get("category")); ok {
		for i := range cats {
			if cats[i].ID == id {
				cat = &cats[i]
				break
			}
		}
	}
	if cat == nil && len(cats) > 0 {
		cat = &cats[0]
	}
	text := util.PyStrip(r.URL.Query().Get("q"))

	var items []blankItem
	if cat != nil {
		variants, err := repo.VariantsInCategory(ctx, q, cat.ID)
		if err != nil {
			return err
		}
		byItem := map[int64][]repo.Variant{}
		for _, v := range variants {
			byItem[v.ItemID] = append(byItem[v.ItemID], v)
		}
		notes, err := repo.Notes(ctx, q, repo.NotesFilter{CategoryID: cat.ID})
		if err != nil {
			return err
		}
		notesByItem := map[int64][]repo.Note{}
		for _, n := range notes {
			notesByItem[n.ItemID] = append(notesByItem[n.ItemID], n)
		}
		found, err := repo.ItemsInCategory(ctx, q, cat.ID, text)
		if err != nil {
			return err
		}
		back := r.URL.RequestURI()
		csrf := stateOf(r).csrfToken()
		isAdmin := false
		if u := CurrentUser(r); u != nil {
			isAdmin = u.IsAdmin()
		}
		for _, it := range found {
			m := BuildMatrix(it, *cat, byItem[it.ID], notesByItem[it.ID])
			items = append(items, blankItem{
				Matrix:   m,
				NoteList: NoteList{Notes: m.Notes, Back: back, IsAdmin: isAdmin, CSRF: csrf},
				NoteForm: NoteForm{ItemID: it.ID, Targets: m.Targets, Back: back, CSRF: csrf},
			})
		}
	}
	return s.Render(w, r, http.StatusOK, "inventory/blank_list.html", blankData{
		Cats: cats, Cat: cat, Items: items, Q: text,
	})
}

// ---- 無地在庫 (一覧) -------------------------------------------------

type stockFilterView struct {
	Category int64 // 0 = 指定なし
	Style    string
	Color    string
	Size     string
	Q        string
	InStock  bool
	Sort     string
	Dir      string
}

type sortLink struct {
	URL  string
	Mark string
}

type stockListData struct {
	F      stockFilterView
	Result service.StockListResult
	Links  map[string]sortLink
	Sorts  []struct{ Key, Label string }
	Cats   []repo.Category
	Opts   service.StockListOptions
}

// buildSortLinks は、見出しの並べ替えリンクを作る (Flask 版 stock_list.py と同じ)。
// いまの絞り込みのうち、正規化済みで空でないものだけを引き継ぐ。
// パラメーターの並びは Python の url_for と同じ (category, style, color, size, q, in_stock, sort, dir)。
func buildSortLinks(f service.Filter) map[string]sortLink {
	var keep [][2]string
	add := func(k, v string) { keep = append(keep, [2]string{k, v}) }
	if f.Category != nil {
		add("category", strconv.FormatInt(*f.Category, 10))
	}
	if f.Style != "" {
		add("style", f.Style)
	}
	if f.Color != "" {
		add("color", f.Color)
	}
	if f.Size != "" {
		add("size", f.Size)
	}
	if f.Q != "" {
		add("q", f.Q)
	}
	if f.InStock {
		add("in_stock", "1")
	}
	links := make(map[string]sortLink, len(service.SortKeys))
	for _, sk := range service.SortKeys {
		active := f.Sort == sk.Key
		next := "asc"
		if active && f.Dir == "asc" {
			next = "desc"
		}
		mark := ""
		if active {
			if f.Dir == "asc" {
				mark = "▲"
			} else {
				mark = "▼"
			}
		}
		var sb strings.Builder
		sb.WriteString("/blank/list?")
		parts := make([]string, 0, len(keep)+2)
		for _, kv := range keep {
			parts = append(parts, url.QueryEscape(kv[0])+"="+url.QueryEscape(kv[1]))
		}
		parts = append(parts, "sort="+url.QueryEscape(sk.Key), "dir="+next)
		sb.WriteString(strings.Join(parts, "&"))
		links[sk.Key] = sortLink{URL: sb.String(), Mark: mark}
	}
	return links
}

func (s *Server) stockListPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := s.DB()
	f := service.ParseFilter(r.URL.Query())
	result, err := service.SearchStock(ctx, q, f)
	if err != nil {
		return err
	}
	opts, err := service.ListOptions(ctx, q)
	if err != nil {
		return err
	}
	cats, err := repo.Categories(ctx, q)
	if err != nil {
		return err
	}
	view := stockFilterView{
		Style: f.Style, Color: f.Color, Size: f.Size, Q: f.Q,
		InStock: f.InStock, Sort: f.Sort, Dir: f.Dir,
	}
	if f.Category != nil {
		view.Category = *f.Category
	}
	return s.Render(w, r, http.StatusOK, "inventory/stock_list.html", stockListData{
		F: view, Result: result, Links: buildSortLinks(f),
		Sorts: service.SortKeys, Cats: cats, Opts: opts,
	})
}

// ---- プリント済み ----------------------------------------------------

type printedGroup struct {
	Name  string
	Rows  []repo.Product
	Total int64
}

type printedData struct {
	Groups []printedGroup
	Q      string
}

func (s *Server) printedList(w http.ResponseWriter, r *http.Request) error {
	text := util.PyStrip(r.URL.Query().Get("q"))
	products, err := repo.Products(r.Context(), s.DB(), text)
	if err != nil {
		return err
	}
	// デザイン名でまとめる (最初に現れた順。repo 側でデザイン名順に並んでいる)
	var groups []printedGroup
	index := map[string]int{}
	for _, p := range products {
		i, ok := index[p.DesignName]
		if !ok {
			groups = append(groups, printedGroup{Name: p.DesignName})
			i = len(groups) - 1
			index[p.DesignName] = i
		}
		groups[i].Rows = append(groups[i].Rows, p)
		groups[i].Total += p.Quantity
	}
	return s.Render(w, r, http.StatusOK, "inventory/printed_list.html", printedData{
		Groups: groups, Q: text,
	})
}
