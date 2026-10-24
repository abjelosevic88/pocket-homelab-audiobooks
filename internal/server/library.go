package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

// ---- home ----

// SeriesCard is a series with a few covers.
type SeriesCard struct {
	ID        string   `json:"id"`
	LibraryID string   `json:"library_id"`
	Name      string   `json:"name"`
	Books     int      `json:"books"`
	Covers    []string `json:"covers"`
	Finished  int      `json:"finished"`
	Next      *Book    `json:"next,omitempty"` // the first book not finished
	AddedAt   int64    `json:"added_at"`
}

// AuthorCard is an author with a picture.
type AuthorCard struct {
	ID        string `json:"id"`
	LibraryID string `json:"library_id,omitempty"`
	Name      string `json:"name"`
	Books     int    `json:"books"`
	Image     string `json:"image,omitempty"`
}

func authorCard(a abs.Author, lib string) AuthorCard {
	c := AuthorCard{ID: a.ID, LibraryID: lib, Name: a.Name, Books: a.NumBooks}
	if a.ImagePath != "" {
		c.Image = "/api/v1/author-image/" + a.ID + "?v=" + strconv.FormatInt(a.UpdatedAt, 10)
	}
	return c
}

func seriesCard(sr abs.Series, lib string, prog map[string]abs.MediaProgress) SeriesCard {
	c := SeriesCard{ID: sr.ID, LibraryID: firstNonEmpty(lib), Name: sr.Name, Books: len(sr.Books), Covers: []string{}, AddedAt: sr.AddedAt}
	bs := books(sr.Books, prog)
	sortBySequence(bs)
	for i := range bs {
		if bs[i].Cover != "" && len(c.Covers) < 4 {
			c.Covers = append(c.Covers, bs[i].Cover)
		}
		if bs[i].Finished {
			c.Finished++
		} else if c.Next == nil {
			b := bs[i]
			c.Next = &b
		}
	}
	return c
}

func seqNum(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 1e9
	}
	return f
}

func sortBySequence(bs []Book) {
	sort.SliceStable(bs, func(i, j int) bool {
		a, b := seqNum(bs[i].Sequence), seqNum(bs[j].Sequence)
		if a != b {
			return a < b
		}
		return bs[i].Year < bs[j].Year
	})
}

// handleHome gathers the person's home: what they're in the middle of, the
// next book of series they started, what's new, up next and today's listening.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	me, err := ac.c.Me(ctx)
	if err != nil {
		s.absErr(w, err)
		return
	}
	prog := progressMap(me)
	libs, err := s.bookLibraries(ctx, ac, false)
	if err != nil {
		s.absErr(w, err)
		return
	}
	type shelfSet struct {
		lib     string
		shelves []abs.Shelf
	}
	sets := make([]shelfSet, len(libs))
	var wg sync.WaitGroup
	for i, l := range libs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sh, err := ac.c.Personalized(ctx, id, 12)
			if err == nil {
				sets[i] = shelfSet{lib: id, shelves: sh}
			}
		}(i, l.ID)
	}
	wg.Wait()

	seen := map[string]bool{}
	var continueL, recent, discover, again []Book
	var seriesNext []Book
	var authors []AuthorCard
	addBooks := func(dst *[]Book, raw []json.RawMessage) {
		for _, e := range raw {
			var it abs.Item
			if json.Unmarshal(e, &it) != nil || it.ID == "" {
				continue
			}
			*dst = append(*dst, bookOf(&it, prog, false))
		}
	}
	for _, set := range sets {
		for _, sh := range set.shelves {
			switch sh.ID {
			case "continue-listening":
				addBooks(&continueL, sh.Entities)
			case "continue-series":
				addBooks(&seriesNext, sh.Entities)
			case "recently-added":
				addBooks(&recent, sh.Entities)
			case "discover":
				addBooks(&discover, sh.Entities)
			case "listen-again":
				addBooks(&again, sh.Entities)
			case "newest-authors":
				for _, e := range sh.Entities {
					var a abs.Author
					if json.Unmarshal(e, &a) == nil && a.ID != "" {
						authors = append(authors, authorCard(a, set.lib))
					}
				}
			}
		}
	}
	// in progress: Audiobookshelf's own list, plus anything with progress it left out
	for _, b := range continueL {
		seen[b.ID] = true
	}
	sort.SliceStable(continueL, func(i, j int) bool { return continueL[i].LastPlayed > continueL[j].LastPlayed })
	if len(recent) == 0 {
		// Audiobookshelf only shelves recent additions: otherwise the newest books of each library
		for _, l := range libs {
			if p, err := ac.c.Items(ctx, l.ID, abs.ItemsQuery{Sort: "addedAt", Desc: true, Limit: 12}); err == nil {
				recent = append(recent, books(p.Results, prog)...)
			}
		}
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].AddedAt > recent[j].AddedAt })
	dedupe := func(bs []Book) []Book {
		out := bs[:0]
		for _, b := range bs {
			if !seen[b.ID] {
				seen[b.ID] = true
				out = append(out, b)
			}
		}
		return out
	}
	seriesNext = dedupe(seriesNext)

	up, _ := s.store.UpNextList(ctx, u.ID)
	upBooks := []Book{}
	for _, x := range up {
		if b, err := s.bookBrief(ctx, ac, x.ItemID, prog); err == nil {
			upBooks = append(upBooks, *b)
		} else {
			upBooks = append(upBooks, Book{ID: x.ItemID, Title: x.Title, Author: x.Author, Genres: []string{}, Missing: true})
		}
	}

	today := store.Today()
	ls, _, _ := s.store.Listens(ctx, u.ID, store.ListenFilter{From: store.AddDays(today, -6), To: today, Limit: -1})
	todaySec, weekSec := 0, 0
	for _, l := range ls {
		weekSec += l.Seconds
		if l.Day == today {
			todaySec += l.Seconds
		}
	}
	st, _ := s.store.GetSettings(ctx, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"continue": nz(continueL), "series_next": nz(seriesNext), "up_next": upBooks,
		"recent": nz(capBooks(recent, 18)), "discover": nz(capBooks(dedupe(discover), 12)), "listen_again": nz(capBooks(again, 12)),
		"authors": nz(authors), "libraries": libs,
		"today": map[string]any{"seconds": todaySec, "week_seconds": weekSec, "goal_minutes": st.DailyGoal, "streak": s.streak(ctx, u.ID, st.DailyGoal)},
	})
}

func capBooks(bs []Book, n int) []Book {
	if len(bs) > n {
		return bs[:n]
	}
	return bs
}

// bookBrief reads one book (expanded) and turns it into a Book without the long fields.
func (s *Server) bookBrief(ctx context.Context, ac *absConn, id string, prog map[string]abs.MediaProgress) (*Book, error) {
	it, err := ac.c.Item(ctx, id)
	if err != nil {
		return nil, err
	}
	b := bookOf(it, prog, false)
	return &b, nil
}

// ---- browse ----

// handleLibraries lists the person's book libraries (with the hidden ones when asked).
func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	libs, err := s.bookLibraries(r.Context(), ac, queryBool(r, "all"))
	if err != nil {
		s.absErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, libs)
}

var sorts = map[string]string{
	"added": "addedAt", "title": "media.metadata.title", "author": "media.metadata.authorNameLF", "length": "media.duration",
	"year": "media.metadata.publishedYear", "progress": "progress", "random": "random",
}

// handleBooks lists one library's books: sort, a progress filter, genre/narrator, pages.
func (s *Server) handleBooks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	q := r.URL.Query()
	lib := q.Get("library")
	if lib == "" {
		libs, err := s.bookLibraries(ctx, ac, false)
		if err != nil {
			s.absErr(w, err)
			return
		}
		if len(libs) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"items": []Book{}, "total": 0})
			return
		}
		lib = libs[0].ID
	}
	iq := abs.ItemsQuery{Sort: sorts[q.Get("sort")], Desc: q.Get("desc") == "1", Limit: queryInt(r, "limit"), Page: queryInt(r, "page")}
	if iq.Sort == "" {
		iq.Sort, iq.Desc = "addedAt", true
	}
	if iq.Limit <= 0 || iq.Limit > 200 {
		iq.Limit = 60
	}
	switch show := q.Get("show"); show {
	case "in-progress", "finished", "not-started", "not-finished":
		iq.Filter = abs.Filter("progress", show)
	}
	if g := q.Get("genre"); g != "" {
		iq.Filter = abs.Filter("genres", g)
	} else if n := q.Get("narrator"); n != "" {
		iq.Filter = abs.Filter("narrators", n)
	}
	me, err := ac.c.Me(ctx)
	if err != nil {
		s.absErr(w, err)
		return
	}
	p, err := ac.c.Items(ctx, lib, iq)
	if err != nil {
		s.absErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": books(p.Results, progressMap(me)), "total": p.Total, "page": iq.Page, "limit": iq.Limit, "library": lib})
}

// handleBook is one book with chapters, bookmarks, series and what you listened.
func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	id := chi.URLParam(r, "id")
	var it *abs.Item
	var me *abs.Me
	var errI, errM error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); it, errI = ac.c.Item(ctx, id) }()
	go func() { defer wg.Done(); me, errM = ac.c.Me(ctx) }()
	wg.Wait()
	if errI != nil {
		s.absErr(w, errI)
		return
	}
	if errM != nil {
		s.absErr(w, errM)
		return
	}
	if it.MediaType != "book" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	prog := progressMap(me)
	b := bookOf(it, prog, true)
	marks := []abs.Bookmark{}
	for _, m := range me.Bookmarks {
		if m.LibraryItemID == id {
			marks = append(marks, m)
		}
	}
	sort.Slice(marks, func(i, j int) bool { return marks[i].Time < marks[j].Time })
	// the rest of its series
	var series []map[string]any
	for _, sr := range b.SeriesList {
		p, err := ac.c.Items(ctx, it.LibraryID, abs.ItemsQuery{Filter: abs.Filter("series", sr.ID), Sort: "sequence", Limit: 100})
		if err != nil {
			continue
		}
		bs := books(p.Results, prog)
		sortBySequence(bs)
		series = append(series, map[string]any{"id": sr.ID, "name": sr.Name, "sequence": sr.Sequence, "books": bs})
	}
	ls, _, _ := s.store.Listens(ctx, u.ID, store.ListenFilter{ItemID: id, Limit: -1})
	secs := 0
	first := ""
	for _, l := range ls {
		secs += l.Seconds
		if first == "" || l.StartedAt < first {
			first = l.StartedAt
		}
	}
	up, _ := s.store.UpNextList(ctx, u.ID)
	inUp := false
	for _, x := range up {
		if x.ItemID == id {
			inUp = true
		}
	}
	p := prog[id]
	openURL := strings.TrimRight(ac.public, "/") + "/item/" + id
	canEdit, canDownload := false, false
	if s.cfg.Backend == "native" {
		openURL = ""
		if a, e := s.catalog().Account(ctx, u.ID); e == nil {
			canEdit, canDownload = a.Permissions.Update, a.Permissions.Download
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"book": b, "bookmarks": marks, "series": nz(series), "up_next": inUp, "progress_id": p.ID, "started_at": p.StartedAt,
		"listened": map[string]any{"seconds": secs, "sessions": len(ls), "first": first},
		"open_url": openURL, "can_edit": canEdit, "can_download": canDownload, "backend": s.cfg.Backend,
	})
}

// handleProgress marks a book finished or not, starts it over, or moves the position.
func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	id := chi.URLParam(r, "id")
	var in struct {
		Action string  `json:"action"` // finished | unfinished | reset | position | hide
		Time   float64 `json:"time"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	it, err := ac.c.Item(ctx, id)
	if err != nil {
		s.absErr(w, err)
		return
	}
	dur := it.Media.Duration
	switch in.Action {
	case "finished":
		err = ac.c.UpdateProgress(ctx, id, map[string]any{"isFinished": true, "duration": dur, "currentTime": dur, "progress": 1})
		if err == nil {
			s.recordFinish(ctx, u, it, time.Now().UnixMilli())
		}
	case "unfinished":
		err = ac.c.UpdateProgress(ctx, id, map[string]any{"isFinished": false})
	case "reset":
		err = ac.c.UpdateProgress(ctx, id, map[string]any{"isFinished": false, "currentTime": 0, "progress": 0, "duration": dur})
	case "position":
		if in.Time < 0 || (dur > 0 && in.Time > dur) {
			writeErr(w, http.StatusBadRequest, "time is outside the book")
			return
		}
		err = ac.c.UpdateProgress(ctx, id, map[string]any{"currentTime": in.Time, "duration": dur, "progress": in.Time / max(dur, 1)})
	case "hide":
		me, merr := ac.c.Me(ctx)
		if merr != nil {
			s.absErr(w, merr)
			return
		}
		p, ok := progressMap(me)[id]
		if !ok {
			writeErr(w, http.StatusNotFound, "not in progress")
			return
		}
		err = ac.c.HideFromContinue(ctx, p.ID)
	default:
		writeErr(w, http.StatusBadRequest, "action is finished, unfinished, reset, position or hide")
		return
	}
	if err != nil {
		s.absErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- bookmarks (kept in Audiobookshelf, so the phone apps see them) ----

func (s *Server) handleAddBookmark(w http.ResponseWriter, r *http.Request) {
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	var in struct {
		Time  float64 `json:"time"`
		Title string  `json:"title"`
	}
	if err := decode(r, &in); err != nil || in.Time < 0 {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		in.Title = "Bookmark at " + clock(int(in.Time))
	}
	b, err := ac.c.AddBookmark(r.Context(), chi.URLParam(r, "id"), in.Time, trim(in.Title, 200))
	if err != nil {
		s.absErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleEditBookmark(w http.ResponseWriter, r *http.Request) {
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	var in struct {
		Time  float64 `json:"time"`
		Title string  `json:"title"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Title) == "" {
		writeErr(w, http.StatusBadRequest, "a bookmark needs a title")
		return
	}
	if err := ac.c.RenameBookmark(r.Context(), chi.URLParam(r, "id"), in.Time, trim(strings.TrimSpace(in.Title), 200)); err != nil {
		s.absErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	t, err := strconv.ParseFloat(r.URL.Query().Get("time"), 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "time is required")
		return
	}
	if err := ac.c.RemoveBookmark(r.Context(), chi.URLParam(r, "id"), t); err != nil {
		s.absErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- series and authors ----

func (s *Server) handleSeriesList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	lib := r.URL.Query().Get("library")
	if lib == "" {
		writeErr(w, http.StatusBadRequest, "library is required")
		return
	}
	sortBy := map[string]string{"name": "name", "added": "addedAt", "books": "numBooks", "length": "totalDuration"}[r.URL.Query().Get("sort")]
	if sortBy == "" {
		sortBy = "name"
	}
	limit := queryInt(r, "limit")
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	me, err := ac.c.Me(ctx)
	if err != nil {
		s.absErr(w, err)
		return
	}
	p, err := ac.c.SeriesList(ctx, lib, sortBy, r.URL.Query().Get("desc") == "1", limit, queryInt(r, "page"))
	if err != nil {
		s.absErr(w, err)
		return
	}
	prog := progressMap(me)
	out := make([]SeriesCard, 0, len(p.Results))
	for _, sr := range p.Results {
		out = append(out, seriesCard(sr, lib, prog))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": p.Total})
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	lib, id := r.URL.Query().Get("library"), chi.URLParam(r, "id")
	if lib == "" {
		writeErr(w, http.StatusBadRequest, "library is required")
		return
	}
	info, err := ac.c.SeriesInfo(ctx, lib, id)
	if err != nil {
		s.absErr(w, err)
		return
	}
	me, err := ac.c.Me(ctx)
	if err != nil {
		s.absErr(w, err)
		return
	}
	p, err := ac.c.Items(ctx, lib, abs.ItemsQuery{Filter: abs.Filter("series", id), Sort: "sequence", Limit: 200})
	if err != nil {
		s.absErr(w, err)
		return
	}
	bs := books(p.Results, progressMap(me))
	sortBySequence(bs)
	total, left := 0.0, 0.0
	done := 0
	for _, b := range bs {
		total += b.Duration
		if b.Finished {
			done++
		} else {
			left += b.Duration - b.CurrentTime
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "library_id": lib, "name": info.Name, "books": bs, "duration": total, "left": left, "finished": done})
}

func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	lib := r.URL.Query().Get("library")
	if lib == "" {
		writeErr(w, http.StatusBadRequest, "library is required")
		return
	}
	as, err := ac.c.Authors(ctx, lib)
	if err != nil {
		s.absErr(w, err)
		return
	}
	out := make([]AuthorCard, 0, len(as))
	for _, a := range as {
		out = append(out, authorCard(a, lib))
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

func (s *Server) handleAuthor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	a, err := ac.c.Author(ctx, chi.URLParam(r, "id"))
	if err != nil {
		s.absErr(w, err)
		return
	}
	me, err := ac.c.Me(ctx)
	if err != nil {
		s.absErr(w, err)
		return
	}
	bs := books(a.LibraryItems, progressMap(me))
	sort.SliceStable(bs, func(i, j int) bool {
		if bs[i].Series != bs[j].Series {
			return bs[i].Series < bs[j].Series
		}
		return seqNum(bs[i].Sequence) < seqNum(bs[j].Sequence)
	})
	writeJSON(w, http.StatusOK, map[string]any{"author": authorCard(a.Author, ""), "description": cleanDescription(a.Description), "books": bs})
}

// handleSearch searches every library the person sees.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"books": []Book{}, "authors": []AuthorCard{}, "series": []SeriesCard{}, "narrators": []any{}})
		return
	}
	libs, err := s.bookLibraries(ctx, ac, false)
	if err != nil {
		s.absErr(w, err)
		return
	}
	me, err := ac.c.Me(ctx)
	if err != nil {
		s.absErr(w, err)
		return
	}
	prog := progressMap(me)
	res := make([]*abs.SearchResult, len(libs))
	var wg sync.WaitGroup
	for i, l := range libs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			res[i], _ = ac.c.Search(ctx, id, q, 12)
		}(i, l.ID)
	}
	wg.Wait()
	bs, as, ss := []Book{}, []AuthorCard{}, []SeriesCard{}
	type narr struct {
		Name  string `json:"name"`
		Books int    `json:"books"`
		Lib   string `json:"library_id"`
	}
	ns := []narr{}
	for i, sr := range res {
		if sr == nil {
			continue
		}
		for _, b := range sr.Book {
			bs = append(bs, bookOf(&b.LibraryItem, prog, false))
		}
		for _, a := range sr.Authors {
			as = append(as, authorCard(a, libs[i].ID))
		}
		for _, x := range sr.Series {
			x.Series.Books = x.Books
			ss = append(ss, seriesCard(x.Series, libs[i].ID, prog))
		}
		for _, n := range sr.Narrators {
			ns = append(ns, narr{n.Name, n.NumBooks, libs[i].ID})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"books": bs, "authors": as, "series": ss, "narrators": ns})
}

// ---- images ----

// handleCover streams a book's cover through the app (with the person's key).
func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	s.proxyImage(w, r, "/api/items/"+chi.URLParam(r, "id")+"/cover")
}

// handleAuthorImage streams an author's picture.
func (s *Server) handleAuthorImage(w http.ResponseWriter, r *http.Request) {
	s.proxyImage(w, r, "/api/authors/"+chi.URLParam(r, "id")+"/image")
}

func (s *Server) proxyImage(w http.ResponseWriter, r *http.Request, path string) {
	if strings.Contains(path, "..") {
		writeErr(w, http.StatusBadRequest, "bad path")
		return
	}
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	q := r.URL.Query()
	v := map[string][]string{}
	if wd := q.Get("w"); wd != "" {
		if n, err := strconv.Atoi(wd); err == nil && n > 0 && n <= 2000 {
			v["width"] = []string{strconv.Itoa(n)}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	req, err := ac.c.Request(ctx, http.MethodGet, path, v)
	if err != nil {
		s.fail(w, err, "cover")
		return
	}
	if a := r.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	res, err := ac.c.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		w.WriteHeader(res.StatusCode)
		return
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Last-Modified"} {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	// covers are versioned in their URL: browsers may keep them
	w.Header().Set("Cache-Control", "private, max-age=604800")
	_, _ = io.Copy(w, res.Body)
}

// ---- up next ----

func (s *Server) handleUpNext(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	list, err := s.store.UpNextList(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "up next")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleAddUpNext(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	var in struct {
		ItemID string `json:"item_id"`
		First  bool   `json:"first"`
	}
	if err := decode(r, &in); err != nil || in.ItemID == "" {
		writeErr(w, http.StatusBadRequest, "item_id is required")
		return
	}
	it, err := ac.c.Item(ctx, in.ItemID)
	if err != nil {
		s.absErr(w, err)
		return
	}
	x := &store.UpNext{ItemID: it.ID, Title: it.Media.Metadata.Title, Author: it.Media.Metadata.AuthorName}
	if err := s.store.AddUpNext(ctx, u.ID, x, in.First); err != nil {
		s.fail(w, err, "up next")
		return
	}
	writeJSON(w, http.StatusCreated, x)
}

func (s *Server) handleRemoveUpNext(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.RemoveUpNext(ctx, userFrom(ctx).ID, chi.URLParam(r, "id")); err != nil {
		s.fail(w, err, "up next")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReorderUpNext(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		ItemIDs []string `json:"item_ids"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.store.ReorderUpNext(ctx, userFrom(ctx).ID, in.ItemIDs); err != nil {
		s.fail(w, err, "up next")
		return
	}
	s.handleUpNext(w, r)
}
