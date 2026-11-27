package server

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// The three routes behind `capabilities` (links.go): /api/v1/pocket/search,
// /day/{date} and /widget, for Pocket Search, Pocket Day and Pocket Hub. Each
// runs as the request's person and shows only what they may open, with their
// own place in each book; the SDK's handlers check and cap what goes in and
// out. Nothing is played, marked or recorded.
//
// The library is the app's own (native mode, the default): the search and
// the widget read it the way GET /search and GET /home do, through the same
// in-process adapter, with no network call. With MEDIA_BACKEND=audiobookshelf
// (kept only to roll back a migration) those two ask that server exactly as
// GET /search and GET /home do, with the person's own account and the same
// timeouts. The day is read from the listening history kept here in both
// modes, as GET /day/{date} reads it.

const (
	widgetRows = 5 // rows in the widget
	// widgetLookups caps how many books in progress the widget opens to find
	// its rows (a book that left the library or a hidden library is skipped).
	widgetLookups = 3 * widgetRows
)

func bookURL(id string) string { return "/book/" + id }

// dots joins the parts of a line that are there: "Andy Weir · 16 h 10 min".
func dots(parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// count is a number with its word: "1 book", "3 books"; nothing for none.
func count(n int, one, many string) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// spoken is a length of time in words: "1 h 10 min", "45 min", "2 h",
// "less than a minute".
func spoken(sec float64) string {
	m := int(math.Round(sec / 60))
	if m < 1 {
		return "less than a minute"
	}
	h, m := m/60, m%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	case h > 0:
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d min", m)
}

// timeLeft is what is left of a book the person is in the middle of.
func timeLeft(b Book) float64 {
	return math.Max(0, b.Duration-b.CurrentTime)
}

// millisDate is a Unix time in milliseconds as RFC 3339, or nothing.
func millisDate(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// hasWord reports whether there is a letter or a digit to look for.
func hasWord(q string) bool {
	return strings.IndexFunc(q, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

// ---------- search ----------

// listenedWords says where the person is in a book: "finished", "40%
// listened", nothing when they have not started it.
func listenedWords(b Book) string {
	switch {
	case b.Finished:
		return "finished"
	case b.CurrentTime > 0 && b.Duration > 0:
		pct := int(b.CurrentTime / b.Duration * 100)
		return strconv.Itoa(min(99, max(1, pct))) + "% listened"
	}
	return ""
}

// searchItem is one book as Pocket Search shows it.
func searchItem(b Book, score float64) core.SearchItem {
	narrator := ""
	if b.Narrator != "" {
		narrator = "read by " + b.Narrator
	}
	length := ""
	if b.Duration > 0 {
		length = spoken(b.Duration)
	}
	return core.SearchItem{ID: b.ID, Title: b.Title, Kind: "audiobook", URL: bookURL(b.ID),
		Snippet: dots(b.Author, narrator, length, listenedWords(b)), Date: millisDate(max(b.LastPlayed, b.AddedAt)), Score: score}
}

// titleScore says how well a title answers the words: it is the title (4),
// starts with them (3), has them (2), or the book was found by its author,
// narrator or subtitle (1).
func titleScore(title, q string) float64 {
	t, q := strings.ToLower(strings.TrimSpace(title)), strings.ToLower(strings.TrimSpace(q))
	switch {
	case t == q:
		return 4
	case strings.HasPrefix(t, q):
		return 3
	case strings.Contains(t, q):
		return 2
	}
	return 1
}

// pocketSearch answers Pocket Search with the books GET /search finds in
// every library the person sees (by title, author or narrator), then the
// books of the series it finds, each book once: the title that answers best
// first. Someone without an audiobook account finds nothing; a library that
// does not answer is left out, as on the search page.
func (s *Server) pocketSearch(r *http.Request, q string, limit int) ([]core.SearchItem, error) {
	ctx, u := r.Context(), userFrom(r.Context())
	if u == nil || !hasWord(q) {
		return nil, nil
	}
	ac, err := s.absFor(ctx, u)
	if isNoABS(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	libs, err := s.bookLibraries(ctx, ac, false)
	if abs.IsAuth(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	me, err := ac.c.Me(ctx)
	if abs.IsAuth(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	prog := progressMap(me)
	res := make([]*abs.SearchResult, len(libs))
	var wg sync.WaitGroup
	for i, l := range libs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			res[i], _ = ac.c.Search(ctx, id, q, limit)
		}(i, l.ID)
	}
	wg.Wait()
	out := []core.SearchItem{}
	seen := map[string]bool{}
	add := func(b Book, score float64) {
		if seen[b.ID] || b.Missing {
			return
		}
		seen[b.ID] = true
		out = append(out, searchItem(b, score))
	}
	for _, sr := range res {
		if sr == nil {
			continue
		}
		for _, x := range sr.Book {
			b := bookOf(&x.LibraryItem, prog, false)
			add(b, titleScore(b.Title, q))
		}
	}
	for _, sr := range res {
		if sr == nil {
			continue
		}
		for _, x := range sr.Series {
			bs := books(x.Books, prog)
			sortBySequence(bs)
			for _, b := range bs {
				add(b, 1.5) // its series has the words
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------- day ----------

// dayListen is one book on a day: every stretch of it together.
type dayListen struct {
	id, title, author string
	seconds           int
	first, last       string // started and last reported, RFC 3339
	finished          string // when it was finished that day
}

// pocketDay answers Pocket Day from the listening history kept here (what
// GET /day/{date} reads): each book listened to that day once, with when it
// started and stopped, the minutes it got and whether it was finished that
// day; a book finished that day without listening (marked finished) is
// there too. The day runs in the caller's time zone when it gives one, else
// in the server's, as the history page does.
func (s *Server) pocketDay(r *http.Request, date string, tz *time.Location) (*core.Day, error) {
	ctx, uid := r.Context(), actorID(r.Context())
	loc, from, to := time.Local, date, date
	if tz != nil {
		// the history keeps the server's day: read the days around it and keep the caller's
		loc, from, to = tz, store.AddDays(date, -1), store.AddDays(date, 1)
	}
	onDay := func(ts string) bool {
		t, err := time.Parse(time.RFC3339Nano, ts)
		return err == nil && t.In(loc).Format("2006-01-02") == date
	}
	clock := func(ts string) string {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil && t.In(loc).Format("2006-01-02") == date {
			return t.In(loc).Format("15:04")
		}
		return ""
	}
	ls, _, err := s.store.Listens(ctx, uid, store.ListenFilter{From: from, To: to, Limit: -1})
	if err != nil {
		return nil, err
	}
	fs, err := s.store.Finishes(ctx, uid, from, to)
	if err != nil {
		return nil, err
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].StartedAt < ls[j].StartedAt })
	var order []*dayListen
	by := map[string]*dayListen{}
	total := 0
	for _, l := range ls {
		if tz != nil && !onDay(l.StartedAt) {
			continue
		}
		x := by[l.ItemID]
		if x == nil {
			x = &dayListen{id: l.ItemID, title: l.Title, author: l.Author, first: l.StartedAt}
			by[l.ItemID] = x
			order = append(order, x)
		}
		x.seconds += l.Seconds
		if l.UpdatedAt > x.last {
			x.last = l.UpdatedAt
		}
		total += l.Seconds
	}
	finished := 0
	for _, f := range fs {
		if tz != nil && !onDay(f.FinishedAt) {
			continue
		}
		x := by[f.ItemID]
		if x == nil {
			x = &dayListen{id: f.ItemID, title: f.Title, author: f.Author}
			by[f.ItemID] = x
			order = append(order, x)
		}
		if x.finished == "" {
			x.finished = f.FinishedAt
			finished++
		}
	}
	if len(order) == 0 {
		return nil, nil
	}

	day := &core.Day{Date: date}
	var listened, done []string // titles, the longest listened first
	sorted := append([]*dayListen(nil), order...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].seconds > sorted[j].seconds })
	for _, x := range sorted {
		if x.seconds > 0 {
			listened = append(listened, x.title)
		}
		if x.finished != "" {
			done = append(done, x.title)
		}
	}
	for _, x := range order {
		it := core.DayItem{ID: x.id, Title: firstNonEmpty(x.title, "Untitled"), Sub: x.author, Done: x.finished != "",
			Minutes: math.Round(float64(x.seconds)/60*100) / 100, URL: bookURL(x.id), Image: "/api/v1/cover/" + x.id}
		if x.seconds > 0 {
			it.Time = clock(x.first)
			if end := clock(x.last); end != it.Time {
				it.End = end
			}
		} else {
			it.Time = clock(x.finished)
		}
		day.Items = append(day.Items, it)
	}

	// "Listened to Project Hail Mary for 1 h 10 min", "Listened to Dune and
	// Emma for 2 h", "Listened to 3 books for 2 h 5 min, finished Dune"
	switch len(listened) {
	case 0:
	case 1:
		day.Summary = "Listened to " + listened[0] + " for " + spoken(float64(total))
	case 2:
		day.Summary = "Listened to " + listened[0] + " and " + listened[1] + " for " + spoken(float64(total))
	default:
		day.Summary = "Listened to " + count(len(listened), "book", "books") + " for " + spoken(float64(total))
	}
	finishedWords := count(len(done), "book", "books")
	if len(done) == 1 {
		finishedWords = done[0]
	}
	switch {
	case len(done) > 0 && day.Summary != "":
		day.Summary += ", finished " + finishedWords
	case len(done) > 0:
		day.Summary = "Finished " + finishedWords
	}
	if total > 0 {
		day.Metrics = append(day.Metrics, core.Metric{Label: "Time", Value: spoken(float64(total))})
	}
	if finished > 0 {
		day.Metrics = append(day.Metrics, core.Metric{Label: "Finished", Value: count(finished, "book", "books")})
	}
	return day, nil
}

// ---------- widget ----------

// pocketWidget answers Pocket Hub with the books the person is in the middle
// of, the one listened to last first, with the time left in each (the home
// page's "Continue listening"), then the newest books of their libraries
// until there are five rows. Libraries they hid are left out, as on the home
// page. It reads their own progress, opens only the few books it shows, and
// finds the newest books without reading the whole library. Someone without
// an audiobook account, or with nothing in it, gets nothing to show.
func (s *Server) pocketWidget(r *http.Request) (*core.WidgetData, error) {
	ctx, u := r.Context(), userFrom(r.Context())
	if u == nil {
		return nil, nil
	}
	ac, err := s.absFor(ctx, u)
	if isNoABS(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	libs, err := s.bookLibraries(ctx, ac, false)
	if abs.IsAuth(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	visible := map[string]bool{}
	for _, l := range libs {
		visible[l.ID] = true
	}
	me, err := ac.c.Me(ctx)
	if abs.IsAuth(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	prog := progressMap(me)
	var going []abs.MediaProgress
	for _, p := range prog {
		if !p.IsFinished && !p.HideFromContinueListening && p.CurrentTime > 0 {
			going = append(going, p)
		}
	}
	sort.Slice(going, func(i, j int) bool {
		if going[i].LastUpdate != going[j].LastUpdate {
			return going[i].LastUpdate > going[j].LastUpdate
		}
		return going[i].LibraryItemID < going[j].LibraryItemID
	})

	w := &core.WidgetData{URL: "/"}
	shown := map[string]bool{}
	for i, p := range going {
		if len(w.Items) == widgetRows || i == widgetLookups {
			break
		}
		b, err := s.bookBrief(ctx, ac, p.LibraryItemID, prog)
		if err != nil || b.Missing || !visible[b.LibraryID] {
			continue // gone from the library, or in a library the person hid
		}
		shown[b.ID] = true
		left := spoken(timeLeft(*b)) + " left"
		if w.Line == "" {
			w.Line = b.Title + ", " + left
		}
		w.Items = append(w.Items, core.WidgetItem{ID: b.ID, Title: b.Title, Detail: dots(b.Author, left), URL: bookURL(b.ID)})
	}
	if len(w.Items) < widgetRows {
		for _, b := range s.newestBooks(r, ac, visible, prog, widgetRows+len(w.Items)) {
			if len(w.Items) == widgetRows {
				break
			}
			if shown[b.ID] {
				continue
			}
			shown[b.ID] = true
			length := ""
			if b.Duration > 0 {
				length = spoken(b.Duration)
			}
			w.Items = append(w.Items, core.WidgetItem{ID: b.ID, Title: b.Title, Detail: dots(b.Author, length), URL: bookURL(b.ID)})
		}
	}
	if len(w.Items) == 0 {
		return nil, nil
	}
	return w, nil
}

// newestBooks are the newest books of the libraries the person sees, newest
// first: from the catalog's newest rows here, or from each library's newest
// items on Audiobookshelf (as the home page falls back to).
func (s *Server) newestBooks(r *http.Request, ac *absConn, visible map[string]bool, prog map[string]abs.MediaProgress, n int) []Book {
	ctx, uid := r.Context(), actorID(r.Context())
	out := []Book{}
	if s.cfg.Backend == "native" {
		ids, _ := s.catalog().NewestBooks(ctx, uid, n, func(lib string) bool { return visible[lib] })
		for _, id := range ids {
			if b, err := s.bookBrief(ctx, ac, id, prog); err == nil && !b.Missing {
				out = append(out, *b)
			}
		}
		return out
	}
	for lib := range visible {
		if p, err := ac.c.Items(ctx, lib, abs.ItemsQuery{Sort: "addedAt", Desc: true, Limit: n}); err == nil {
			for _, b := range books(p.Results, prog) {
				if !b.Missing {
					out = append(out, b)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AddedAt != out[j].AddedAt {
			return out[i].AddedAt > out[j].AddedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}
