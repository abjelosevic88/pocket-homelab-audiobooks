package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

// ---- reading history from Audiobookshelf ----
//
// Audiobookshelf saves a listening session for every stretch someone
// listens, in any app (this one included, since its player reports through
// playback sessions). Every SYNC_EVERY, and soon after this app's player
// reports, the sessions that changed since the last look are copied here;
// the first look copies them all. Finished books come from the person's
// finished marks. The copy is what history, stats, the day view and the
// weekly recap read, so they survive a book leaving the library.

func (s *Server) isSyncing(uid int64) bool {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	return s.syncing[uid]
}

// historySoon reads one person's history in the background (at most every 20 s).
func (s *Server) historySoon(uid int64) {
	s.syncMu.Lock()
	last := s.lastSync[uid]
	if time.Since(last) < 20*time.Second {
		s.syncMu.Unlock()
		return
	}
	s.lastSync[uid] = time.Now()
	s.syncMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := s.syncUser(ctx, uid, false); err != nil && !isNoABS(err) {
			s.log.Warn("history", "user", uid, "err", err)
		}
	}()
}

func (s *Server) syncAll(ctx context.Context) {
	ids, err := s.store.ConnectedUsers(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		if err := s.syncUser(cctx, id, false); err != nil && !isNoABS(err) {
			s.log.Debug("history", "user", id, "err", err)
		}
		cancel()
	}
}

// syncUser copies what changed in one person's listening sessions since the last look.
func (s *Server) syncUser(ctx context.Context, uid int64, full bool) error {
	if s.cfg.Backend == "native" {
		return s.nativeHistory(ctx, uid)
	}
	s.syncMu.Lock()
	if s.syncing[uid] {
		s.syncMu.Unlock()
		return nil
	}
	s.syncing[uid] = true
	s.syncMu.Unlock()
	defer func() {
		s.syncMu.Lock()
		delete(s.syncing, uid)
		s.syncMu.Unlock()
	}()
	u, err := s.store.GetUser(ctx, uid)
	if err != nil {
		return err
	}
	ac, err := s.absFor(ctx, u)
	if err != nil {
		return err
	}
	st := s.store.GetSync(ctx, uid)
	if st.ServerKey != ac.key || full {
		// another account (or "read everything again"): start over; rows already here stay
		st = store.SyncState{ServerKey: ac.key}
	}
	first := !st.Backfilled
	newest := st.Watermark
	var added int
	// only books: some apps (Audra) report podcast episodes as "book" sessions
	// without a library, so the item's library decides
	bookLib := map[string]bool{}
	if libs, err := ac.c.Libraries(ctx); err == nil {
		for _, l := range libs {
			if l.MediaType == "book" {
				bookLib[l.ID] = true
			}
		}
	}
	itemLib := map[string]string{}
	isBook := func(ls *abs.Session) bool {
		if ls.MediaType != "book" || ls.EpisodeID != "" || ls.LibraryItemID == "" {
			return false
		}
		lib := ls.LibraryID
		if lib == "" {
			l, ok := itemLib[ls.LibraryItemID]
			if !ok {
				if it, err := ac.c.Item(ctx, ls.LibraryItemID); err == nil && it.MediaType == "book" {
					l = it.LibraryID
				}
				itemLib[ls.LibraryItemID] = l
			}
			lib = l
		}
		return len(bookLib) == 0 || bookLib[lib]
	}
	const per = 100
	for page := 0; ; page++ {
		p, err := ac.c.ListeningSessions(ctx, page, per)
		if err != nil {
			st.Error = err.Error()
			st.SyncedAt = store.Now()
			_ = s.store.SaveSync(ctx, uid, st)
			return err
		}
		done := false
		for _, ls := range p.Sessions {
			if ls.UpdatedAt <= st.Watermark && st.Backfilled {
				done = true
				break
			}
			if ls.UpdatedAt > newest {
				newest = ls.UpdatedAt
			}
			if !isBook(&ls) {
				_ = s.store.DeleteListenABS(ctx, uid, ls.ID) // let in by an earlier version
				continue
			}
			if ls.TimeListening < 1 {
				continue
			}
			l := listenOf(&ls)
			if l.LibraryID == "" {
				l.LibraryID = itemLib[ls.LibraryItemID]
			}
			if ok, err := s.store.UpsertListen(ctx, uid, &l); err == nil && ok {
				added++
			}
		}
		if done || len(p.Sessions) < per || page+1 >= p.NumPages {
			break
		}
	}
	// finished books
	if me, err := ac.c.Me(ctx); err == nil {
		titles := map[string]store.Listen{}
		for _, p := range me.MediaProgress {
			if p.EpisodeID != "" || !p.IsFinished || p.FinishedAt == 0 {
				continue
			}
			f := &store.Finish{ItemID: p.LibraryItemID, Duration: int(p.Duration), FinishedAt: store.MillisUTC(p.FinishedAt)}
			l, ok := titles[p.LibraryItemID]
			if !ok {
				if ls, _, _ := s.store.Listens(ctx, uid, store.ListenFilter{ItemID: p.LibraryItemID, Limit: 1}); len(ls) > 0 {
					l = ls[0]
				} else if it, err := ac.c.Item(ctx, p.LibraryItemID); err == nil {
					l = store.Listen{Title: it.Media.Metadata.Title, Author: it.Media.Metadata.AuthorName}
				}
				titles[p.LibraryItemID] = l
			}
			f.Title, f.Author = l.Title, l.Author
			if ok, _ := s.store.AddFinish(ctx, uid, f); ok && !first {
				_ = s.store.RemoveUpNext(ctx, uid, f.ItemID)
				s.finished(ctx, u, f)
			}
		}
	}
	st.Watermark, st.Backfilled, st.SyncedAt, st.Error = newest, true, store.Now(), ""
	if err := s.store.SaveSync(ctx, uid, st); err != nil {
		return err
	}
	if added > 0 {
		s.log.Debug("history: sessions copied", "user", uid, "new", added)
	}
	return nil
}

// listenOf turns an Audiobookshelf listening session into a history row.
func listenOf(ls *abs.Session) store.Listen {
	m := ls.MediaMetadata
	l := store.Listen{ABSID: ls.ID, ItemID: ls.LibraryItemID, LibraryID: ls.LibraryID, Title: firstNonEmpty(m.Title, ls.DisplayTitle),
		Author: firstNonEmpty(m.AuthorName, ls.DisplayAuthor), Narrator: m.NarratorName, Genres: m.Genres, Duration: int(ls.Duration),
		StartedAt: store.MillisUTC(ls.StartedAt), UpdatedAt: store.MillisUTC(ls.UpdatedAt), Seconds: int(ls.TimeListening + 0.5),
		StartPos: int(ls.StartTime), EndPos: int(ls.CurrentTime)}
	if l.Author == "" && len(m.Authors) > 0 {
		names := make([]string, len(m.Authors))
		for i, a := range m.Authors {
			names[i] = a.Name
		}
		l.Author = strings.Join(names, ", ")
	}
	if len(m.Series) > 0 {
		l.Series = m.Series[0].Name
		if m.Series[0].Sequence != "" {
			l.Series += " #" + m.Series[0].Sequence
		}
	} else {
		l.Series = m.SeriesName
	}
	if d := ls.DeviceInfo; d != nil {
		if d.ClientName == "Pocket Audiobooks" {
			l.Source = "pocket"
		}
		app := d.ClientName
		if app == "" && ls.MediaPlayer != "" {
			app = ls.MediaPlayer
		}
		where := firstNonEmpty(d.DeviceName, strings.TrimSpace(d.Manufacturer+" "+d.Model), d.OSName, d.BrowserName)
		l.Device = strings.TrimSpace(strings.Trim(app+" · "+where, " ·"))
	}
	if ls.StartedAt == 0 {
		l.StartedAt = l.UpdatedAt
	}
	return l
}

// finished tells Core and (if asked) the person that a book was finished.
func (s *Server) finished(ctx context.Context, u *store.User, f *store.Finish) {
	s.publish(ctx, u, "audiobooks.finished", f.ItemID, map[string]any{"title": f.Title, "author": f.Author, "duration": f.Duration, "finished_at": f.FinishedAt})
	st, _ := s.store.GetSettings(ctx, u.ID)
	if !st.NotifyFinished || st.NotifyURL == "" {
		return
	}
	n := notify.New(st.NotifyURL, st.NotifyFormat)
	body := fmt.Sprintf("%s by %s — %s of listening.", f.Title, firstNonEmpty(f.Author, "unknown"), longDuration(f.Duration))
	if err := n.Send(ctx, notify.Message{Title: "Finished a book", Body: body, Priority: "info", Tags: []string{"books"}, Link: s.cfg.BaseURL + "/book/" + f.ItemID}); err != nil {
		s.log.Warn("finished push", "user", u.ID, "err", err)
	}
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	uid := actorID(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"sync": s.store.GetSync(r.Context(), uid), "syncing": s.isSyncing(uid)})
}

func (s *Server) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	uid := actorID(r.Context())
	full := queryBool(r, "full")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := s.syncUser(ctx, uid, full); err != nil && !isNoABS(err) {
			s.log.Warn("history", "user", uid, "err", err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

// ---- history, day, stats ----

// listenOut is a history row with its cover.
type listenOut struct {
	store.Listen
	Cover string `json:"cover"`
}

func decorate(ls []store.Listen) []listenOut {
	out := make([]listenOut, 0, len(ls))
	for _, x := range ls {
		out = append(out, listenOut{Listen: x, Cover: "/api/v1/cover/" + x.ItemID})
	}
	return out
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ListenFilter{From: q.Get("from"), To: q.Get("to"), ItemID: q.Get("book"), Q: q.Get("q"), Limit: queryInt(r, "limit"), Offset: queryInt(r, "offset")}
	if !validDate(f.From) || !validDate(f.To) {
		writeErr(w, http.StatusBadRequest, "dates are YYYY-MM-DD")
		return
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	ls, total, err := s.store.Listens(r.Context(), actorID(r.Context()), f)
	if err != nil {
		s.fail(w, err, "history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": decorate(ls), "total": total})
}

func (s *Server) handleDeleteListen(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteListen(r.Context(), actorID(r.Context()), idParam(r, "id")); err != nil {
		s.fail(w, err, "delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFinished(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !validDate(q.Get("from")) || !validDate(q.Get("to")) {
		writeErr(w, http.StatusBadRequest, "dates are YYYY-MM-DD")
		return
	}
	fs, err := s.store.Finishes(r.Context(), actorID(r.Context()), q.Get("from"), q.Get("to"))
	if err != nil {
		s.fail(w, err, "finished")
		return
	}
	type out struct {
		store.Finish
		Cover string `json:"cover"`
	}
	res := make([]out, 0, len(fs))
	for _, f := range fs {
		res = append(res, out{f, "/api/v1/cover/" + f.ItemID})
	}
	writeJSON(w, http.StatusOK, res)
}

// dayBook is one book listened to on a day.
type dayBook struct {
	ItemID   string `json:"item_id"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Cover    string `json:"cover"`
	Seconds  int    `json:"seconds"`
	Duration int    `json:"duration"` // the book's length
	From     int    `json:"from"`
	To       int    `json:"to"`
	First    string `json:"first"`
	Last     string `json:"last"`
	Devices  string `json:"devices"`
	Finished bool   `json:"finished"`
}

// handleDay is what the person listened to on one day (Pocket Day reads it through an app link).
func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	d := chi.URLParam(r, "date")
	if !validDate(d) || d == "" {
		writeErr(w, http.StatusBadRequest, "date is YYYY-MM-DD")
		return
	}
	ls, _, err := s.store.Listens(ctx, uid, store.ListenFilter{From: d, To: d, Limit: -1})
	if err != nil {
		s.fail(w, err, "day")
		return
	}
	fs, _ := s.store.Finishes(ctx, uid, d, d)
	done := map[string]bool{}
	for _, f := range fs {
		done[f.ItemID] = true
	}
	by := map[string]*dayBook{}
	var order []string
	sec := 0
	sort.Slice(ls, func(i, j int) bool { return ls[i].StartedAt < ls[j].StartedAt })
	for _, l := range ls {
		sec += l.Seconds
		b := by[l.ItemID]
		if b == nil {
			b = &dayBook{ItemID: l.ItemID, Title: l.Title, Author: l.Author, Cover: "/api/v1/cover/" + l.ItemID, Duration: l.Duration, From: l.StartPos, First: l.StartedAt, Finished: done[l.ItemID]}
			by[l.ItemID] = b
			order = append(order, l.ItemID)
		}
		b.Seconds += l.Seconds
		b.To, b.Last = l.EndPos, l.UpdatedAt
		if l.Device != "" && !strings.Contains(b.Devices, l.Device) {
			b.Devices = strings.Trim(b.Devices+", "+l.Device, ", ")
		}
	}
	items := make([]dayBook, 0, len(order))
	for _, id := range order {
		items = append(items, *by[id])
	}
	finished := make([]map[string]any, 0, len(fs))
	for _, f := range fs {
		finished = append(finished, map[string]any{"item_id": f.ItemID, "title": f.Title, "author": f.Author, "cover": "/api/v1/cover/" + f.ItemID, "finished_at": f.FinishedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": d, "seconds": sec, "books": items, "finished": finished, "sessions": len(ls), "summary": daySummary(items, len(fs))})
}

// daySummary says the day in one line: "1h 20m of Project Hail Mary, finished Dune".
func daySummary(items []dayBook, finished int) string {
	if len(items) == 0 {
		return ""
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Seconds > items[j].Seconds })
	parts := []string{longDuration(items[0].Seconds) + " of " + items[0].Title}
	if len(items) == 2 {
		parts = append(parts, longDuration(items[1].Seconds)+" of "+items[1].Title)
	} else if len(items) > 2 {
		parts = append(parts, fmt.Sprintf("%d other books", len(items)-1))
	}
	out := strings.Join(parts, " and ")
	if finished > 0 {
		for _, it := range items {
			if it.Finished {
				out += ", finished " + it.Title
				break
			}
		}
	}
	return out
}

type countRow struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Image   string `json:"image,omitempty"`
	Count   int    `json:"count"`
	Seconds int    `json:"seconds"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	rng := r.URL.Query().Get("range")
	today := store.Today()
	from := ""
	switch rng {
	case "7d":
		from = store.AddDays(today, -6)
	case "30d", "":
		rng, from = "30d", store.AddDays(today, -29)
	case "90d":
		from = store.AddDays(today, -89)
	case "365d":
		from = store.AddDays(today, -364)
	case "all":
	default:
		if y, err := strconv.Atoi(rng); err == nil && y > 1900 && y < 3000 {
			from = fmt.Sprintf("%d-01-01", y)
			today = fmt.Sprintf("%d-12-31", y)
		} else {
			writeErr(w, http.StatusBadRequest, "range is 7d, 30d, 90d, 365d, all or a year")
			return
		}
	}
	ls, _, err := s.store.Listens(ctx, uid, store.ListenFilter{From: from, To: today, Limit: -1})
	if err != nil {
		s.fail(w, err, "stats")
		return
	}
	fs, _ := s.store.Finishes(ctx, uid, from, today)
	st, _ := s.store.GetSettings(ctx, uid)
	out := statsOf(ls, rng, from, today)
	out["finished"] = len(fs)
	type fin struct {
		store.Finish
		Cover string `json:"cover"`
	}
	fl := make([]fin, 0, len(fs))
	for _, f := range fs {
		fl = append(fl, fin{f, "/api/v1/cover/" + f.ItemID})
	}
	out["finished_books"] = fl
	out["goal_minutes"] = st.DailyGoal
	out["streak"] = s.streaks(ctx, uid, st.DailyGoal)
	writeJSON(w, http.StatusOK, out)
}

func statsOf(ls []store.Listen, rng, from, to string) map[string]any {
	tot := map[string]int{"sessions": len(ls), "seconds": 0, "books": 0}
	booksM := map[string]*countRow{}
	authors := map[string]*countRow{}
	narrators := map[string]*countRow{}
	genres := map[string]*countRow{}
	devices := map[string]*countRow{}
	days := map[string]int{}
	weekday := make([]int, 7)
	hours := make([]int, 24)
	add := func(m map[string]*countRow, key, name, img string, sec int) {
		if key == "" {
			return
		}
		if m[key] == nil {
			m[key] = &countRow{Key: key, Name: name, Image: img}
		}
		m[key].Count++
		m[key].Seconds += sec
	}
	for _, x := range ls {
		tot["seconds"] += x.Seconds
		add(booksM, x.ItemID, x.Title, "/api/v1/cover/"+x.ItemID, x.Seconds)
		for _, a := range splitNames(x.Author) {
			add(authors, a, a, "", x.Seconds)
		}
		for _, n := range splitNames(x.Narrator) {
			add(narrators, n, n, "", x.Seconds)
		}
		for _, g := range x.Genres {
			add(genres, g, g, "", x.Seconds)
		}
		add(devices, firstNonEmpty(x.Device, "Unknown"), firstNonEmpty(x.Device, "Unknown"), "", x.Seconds)
		days[x.Day] += x.Seconds
		if t, err := time.Parse(time.RFC3339Nano, x.StartedAt); err == nil {
			lt := t.Local()
			weekday[(int(lt.Weekday())+6)%7] += x.Seconds
			hours[lt.Hour()] += x.Seconds
		}
	}
	tot["books"] = len(booksM)
	top := func(m map[string]*countRow, n int) []countRow {
		out := make([]countRow, 0, len(m))
		for _, v := range m {
			out = append(out, *v)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Seconds != out[j].Seconds {
				return out[i].Seconds > out[j].Seconds
			}
			return out[i].Name < out[j].Name
		})
		if len(out) > n {
			out = out[:n]
		}
		return out
	}
	type point struct {
		Key     string `json:"key"`
		Label   string `json:"label"`
		Seconds int    `json:"seconds"`
	}
	var series []point
	start := from
	if start == "" {
		start = to
		for d := range days {
			if d < start {
				start = d
			}
		}
	}
	t0, _ := store.ParseDate(start)
	t1, _ := store.ParseDate(to)
	span := int(t1.Sub(t0).Hours()/24) + 1
	switch {
	case span <= 31:
		for d := t0; !d.After(t1); d = d.AddDate(0, 0, 1) {
			k := d.Format("2006-01-02")
			series = append(series, point{Key: k, Label: d.Format("2"), Seconds: days[k]})
		}
	case span <= 120:
		for d := t0; !d.After(t1); d = d.AddDate(0, 0, 7) {
			sum := 0
			for i := 0; i < 7; i++ {
				sum += days[d.AddDate(0, 0, i).Format("2006-01-02")]
			}
			series = append(series, point{Key: d.Format("2006-01-02"), Label: d.Format("Jan 2"), Seconds: sum})
		}
	default:
		m := time.Date(t0.Year(), t0.Month(), 1, 0, 0, 0, 0, time.UTC)
		for !m.After(t1) {
			sum := 0
			for d, v := range days {
				if strings.HasPrefix(d, m.Format("2006-01")) {
					sum += v
				}
			}
			series = append(series, point{Key: m.Format("2006-01"), Label: m.Format("Jan"), Seconds: sum})
			m = m.AddDate(0, 1, 0)
		}
	}
	avg := 0
	if span > 0 {
		avg = tot["seconds"] / span
	}
	return map[string]any{"range": rng, "from": from, "to": to, "totals": tot, "days_active": len(days), "average_seconds": avg, "series": series,
		"top_books": top(booksM, 10), "top_authors": top(authors, 10), "top_narrators": top(narrators, 10), "top_genres": top(genres, 10),
		"devices": top(devices, 8), "weekday": weekday, "hours": hours}
}

// splitNames splits "A, B & C" credits into names.
func splitNames(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '&' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// dayTotals is seconds listened per local day over the whole history.
func (s *Server) dayTotals(ctx context.Context, uid int64) map[string]int {
	days := map[string]int{}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT day, SUM(seconds) FROM listens WHERE user_id = ? GROUP BY day`, uid)
	if err != nil {
		return days
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		var n int
		if rows.Scan(&d, &n) == nil {
			days[d] = n
		}
	}
	return days
}

// streaks are runs of days with listening (at least the goal, when there is one).
func (s *Server) streaks(ctx context.Context, uid int64, goalMin int) map[string]int {
	days := s.dayTotals(ctx, uid)
	need := 1
	if goalMin > 0 {
		need = goalMin * 60
	}
	var dl []string
	for d, v := range days {
		if v >= need {
			dl = append(dl, d)
		}
	}
	sort.Strings(dl)
	longest, run := 0, 0
	prev := ""
	for _, d := range dl {
		if prev != "" && store.AddDays(prev, 1) == d {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
		prev = d
	}
	current := 0
	d := store.Today()
	if days[d] < need {
		d = store.AddDays(d, -1) // nothing yet today doesn't break the streak
	}
	for days[d] >= need {
		current++
		d = store.AddDays(d, -1)
	}
	return map[string]int{"longest": longest, "current": current}
}

func (s *Server) streak(ctx context.Context, uid int64, goalMin int) int {
	return s.streaks(ctx, uid, goalMin)["current"]
}

// handleDashboard is a compact summary (Homepage widgets, Pocket Day, Pocket Hub).
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	today := store.Today()
	week, _, _ := s.store.Listens(ctx, uid, store.ListenFilter{From: store.AddDays(today, -6), To: today, Limit: -1})
	sec, todaySec := 0, 0
	booksW := map[string]bool{}
	for _, x := range week {
		sec += x.Seconds
		booksW[x.ItemID] = true
		if x.Day == today {
			todaySec += x.Seconds
		}
	}
	year := strconv.Itoa(time.Now().Year())
	fs, _ := s.store.Finishes(ctx, uid, year+"-01-01", year+"-12-31")
	st, _ := s.store.GetSettings(ctx, uid)
	out := map[string]any{"today_seconds": todaySec, "week_seconds": sec, "week_books": len(booksW), "finished_this_year": len(fs),
		"goal_minutes": st.DailyGoal, "streak": s.streak(ctx, uid, st.DailyGoal)}
	if last, _ := s.store.LastListens(ctx, uid, 1); len(last) > 0 {
		l := last[0]
		cur := map[string]any{"item_id": l.ItemID, "title": l.Title, "author": l.Author, "cover": "/api/v1/cover/" + l.ItemID, "position": l.EndPos, "duration": l.Duration, "last_played": l.UpdatedAt}
		if l.Duration > 0 {
			cur["progress"] = float64(l.EndPos) / float64(l.Duration)
		}
		out["current"] = cur
	}
	var now []map[string]any
	s.playMu.Lock()
	for _, ps := range s.plays {
		if ps.uid == uid && time.Since(ps.updated) < 2*time.Minute {
			now = append(now, map[string]any{"item_id": ps.itemID, "title": ps.title, "author": ps.author, "position": ps.position, "duration": ps.duration, "playing": ps.playing, "device": ps.device})
		}
	}
	s.playMu.Unlock()
	out["now"] = nz(now)
	writeJSON(w, http.StatusOK, out)
}

// ---- weekly recap ----

func (s *Server) weeklyRecaps(ctx context.Context) {
	now := time.Now()
	if now.Weekday() != time.Monday || now.Hour() < 9 || now.Hour() > 11 {
		return
	}
	y, wk := now.ISOWeek()
	ids, _ := s.store.SettingsUsers(ctx)
	for _, uid := range ids {
		st, _ := s.store.GetSettings(ctx, uid)
		if !st.NotifyWeekly || st.NotifyURL == "" {
			continue
		}
		key := fmt.Sprintf("recap:%d:%d-%d", uid, y, wk)
		if _, _, done := s.store.CacheGet(ctx, key); done {
			continue
		}
		_ = s.store.CachePut(ctx, key, "1")
		to := store.AddDays(store.Today(), -1)
		ls, _, _ := s.store.Listens(ctx, uid, store.ListenFilter{From: store.AddDays(to, -6), To: to, Limit: -1})
		if len(ls) == 0 {
			continue
		}
		stats := statsOf(ls, "7d", store.AddDays(to, -6), to)
		tot := stats["totals"].(map[string]int)
		body := fmt.Sprintf("%s of listening, %d days out of 7.", longDuration(tot["seconds"]), stats["days_active"])
		if bs := stats["top_books"].([]countRow); len(bs) > 0 {
			body += " Mostly " + bs[0].Name + "."
		}
		if fs, _ := s.store.Finishes(ctx, uid, store.AddDays(to, -6), to); len(fs) > 0 {
			body += fmt.Sprintf(" Finished %d.", len(fs))
		}
		n := notify.New(st.NotifyURL, st.NotifyFormat)
		if err := n.Send(ctx, notify.Message{Title: "Your week in Pocket Audiobooks", Body: body, Priority: "info", Tags: []string{"headphones"}, Link: s.cfg.BaseURL + "/stats"}); err != nil {
			s.log.Warn("weekly recap", "user", uid, "err", err)
		}
	}
}

func longDuration(sec int) string {
	h, m := sec/3600, (sec%3600)/60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", m)
}
