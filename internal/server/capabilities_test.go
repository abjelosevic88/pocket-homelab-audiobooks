package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// capScopes are the link tokens of the fake Core: pcx_<who>_<what> acts for
// <who>@example.com with one scope.
var capScopes = map[string]string{
	"search": core.ScopeSearch, "day": core.ScopeDay, "widget": core.ScopeWidget, "capture": core.ScopeCapture,
	"read": "listening:read", "library": "library:read", "scan": "library:scan",
}

// capHarness is the app on its own library (native mode) behind a fake Core
// that knows the capScopes tokens and keeps the manifest the app registers
// with. Alice may open every library, Bob only Kids; Carol has no
// audiobook account. Kids is shared: both have their own place in its book.
type capHarness struct {
	s                  *Server
	h                  http.Handler
	alice, bob, carol  person
	manifest           func() map[string]any
	setProgress        func(p person, item string, at int64, changes map[string]any)
	listen, finishBook func(p person, item, title, author string, start time.Time, seconds int)
}

func newCapHarness(t *testing.T) *capHarness {
	t.Helper()
	var mu sync.Mutex
	var manifest map[string]any
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/apps/register":
			mu.Lock()
			manifest = map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&manifest)
			mu.Unlock()
			io.WriteString(w, `{"token":"pca_test"}`)
		case "/api/v1/auth/introspect":
			var in struct {
				Token string `json:"token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			parts := strings.SplitN(strings.TrimPrefix(in.Token, "pcx_"), "_", 2)
			if len(parts) != 2 || capScopes[parts[1]] == "" {
				io.WriteString(w, `{"active":false}`)
				return
			}
			email := parts[0] + "@example.com"
			fmt.Fprintf(w, `{"active":true,"kind":"delegated","scope":%q,"audience":"audiobooks","user":{"id":1,"sub":%q,"email":%q,"name":%q,"role":"member"},"app":{"id":7,"slug":"hub","name":"Pocket Hub"}}`,
				capScopes[parts[1]], email, email, parts[0])
		default:
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(coreSrv.Close)
	cfg := testConfig(t.TempDir())
	cfg.CoreURL, cfg.CoreRegistrationKey = coreSrv.URL, "test-key"
	cfg.Backend = "native"
	st := openTestStore(t, cfg)
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err := s.loadSecretKey(); err != nil {
		t.Fatal(err)
	}
	blocked := gatewayTransport(func(*http.Request) (*http.Response, error) {
		t.Error("native backend called an upstream service")
		return nil, io.ErrUnexpectedEOF
	})
	s.absHTTP = &http.Client{Transport: blocked}
	s.streamHTTP = &http.Client{Transport: blocked}

	c := &capHarness{s: s, h: s.Router(), manifest: func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return manifest
	}}
	c.alice = addPerson(t, s, "alice@example.com", "unused-a")
	c.bob = addPerson(t, s, "bob@example.com", "unused-b")
	c.carol = addPerson(t, s, "carol@example.com", "unused-c")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO catalog_libraries VALUES('lib','Books','book',0,'{}')`)
	exec(`INSERT INTO catalog_libraries VALUES('kids','Kids','book',1,'{}')`)
	exec(`INSERT INTO catalog_accounts VALUES('alice',?,'Alice',1,0,'{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}')`, c.alice.u.ID)
	exec(`INSERT INTO catalog_accounts VALUES('bob',?,'Bob',1,0,'{"accessAllLibraries":false,"librariesAccessible":["kids"],"accessAllTags":true}')`, c.bob.u.ID)
	book := func(id, lib, title, author string, seconds float64, added int64, narrator string, series ...any) {
		t.Helper()
		doc := nativeObject{"media": nativeObject{"title": title, "narrators": []any{narrator}}, "authors": []any{nativeObject{"id": "a-" + id, "name": author}}}
		if len(series) == 2 {
			doc["series"] = []any{nativeObject{"id": series[0], "name": series[0], "sequence": series[1]}}
		}
		exec(`INSERT INTO catalog_items VALUES(?,?,?,'book',?,?,?,0,0,?,?,'[]',?)`, id, lib, "m-"+id, title, author, seconds, added, added, string(nativeJSON(doc)))
	}
	book("phm", "lib", "Project Hail Mary", "Andy Weir", 58200, 1000, "Ray Porter")
	book("dune", "lib", "Dune", "Frank Herbert", 75600, 2000, "Scott Brick", "Dune Chronicles", "1")
	book("messiah", "lib", "Dune Messiah", "Frank Herbert", 30000, 3000, "Scott Brick", "Dune Chronicles", "2")
	book("gruffalo", "kids", "The Gruffalo", "Julia Donaldson", 600, 4000, "Imelda Staunton")
	book("martian", "lib", "The Martian", "Andy Weir", 39000, 5000, "R. C. Bray")

	c.setProgress = func(p person, item string, at int64, changes map[string]any) {
		t.Helper()
		if err := s.catalog().SetProgress(context.Background(), p.u.ID, item, "", changes); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE catalog_progress SET updated_at=?1, document=json_set(document,'$.lastUpdate',?1) WHERE item_id=?2 AND account_id=(SELECT id FROM catalog_accounts WHERE pocket_user_id=?3)`, at, item, p.u.ID)
	}
	c.listen = func(p person, item, title, author string, start time.Time, seconds int) {
		t.Helper()
		l := store.Listen{ItemID: item, Title: title, Author: author, StartedAt: start.UTC().Format(time.RFC3339Nano), UpdatedAt: start.Add(time.Duration(seconds) * time.Second).UTC().Format(time.RFC3339Nano), Seconds: seconds}
		if _, err := s.store.UpsertListen(context.Background(), p.u.ID, &l); err != nil {
			t.Fatal(err)
		}
	}
	c.finishBook = func(p person, item, title, author string, at time.Time, _ int) {
		t.Helper()
		if _, err := s.store.AddFinish(context.Background(), p.u.ID, &store.Finish{ItemID: item, Title: title, Author: author, FinishedAt: at.UTC().Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func capGet(t *testing.T, c *capHarness, p person, path string, out any) {
	t.Helper()
	w := call(t, c.h, p, "GET", "/api/v1/pocket/"+path, "")
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("%s: %v (%s)", path, err, w.Body)
	}
}

func capFind(t *testing.T, c *capHarness, p person, q string) []core.SearchItem {
	t.Helper()
	var res core.SearchResults
	capGet(t, c, p, "search?"+q, &res)
	if res.Items == nil {
		t.Fatalf("search %s: no items list", q)
	}
	return res.Items
}

func titles(items []core.SearchItem) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Title)
	}
	return strings.Join(out, " | ")
}

// Pocket Search finds books by title, author, narrator and series in the
// libraries the person may open, each with their own place in it.
func TestPocketSearch(t *testing.T) {
	c := newCapHarness(t)
	c.setProgress(c.alice, "phm", 1_700_000_000_000, map[string]any{"currentTime": 23280.0})
	c.setProgress(c.alice, "gruffalo", 1_700_000_100_000, map[string]any{"isFinished": true})
	c.setProgress(c.bob, "gruffalo", 1_700_000_200_000, map[string]any{"currentTime": 300.0})

	got := capFind(t, c, c.alice, "q=project+hail")
	if len(got) != 1 {
		t.Fatalf("project hail = %s", titles(got))
	}
	if it := got[0]; it.ID != "phm" || it.Kind != "audiobook" || it.URL != "/book/phm" || it.Snippet != "Andy Weir · read by Ray Porter · 16 h 10 min · 40% listened" || it.Date != "2023-11-14T22:13:20Z" {
		t.Errorf("book = %+v", it)
	}
	// the title that answers best first; the series brings its books in order
	if got := capFind(t, c, c.alice, "q=dune"); titles(got) != "Dune | Dune Messiah" {
		t.Errorf("dune = %s", titles(got))
	}
	if got := capFind(t, c, c.alice, "q=chronicles"); titles(got) != "Dune | Dune Messiah" || got[0].Snippet != "Frank Herbert · read by Scott Brick · 21 h" || got[0].Date != "1970-01-01T00:00:02Z" {
		t.Errorf("series = %+v", got)
	}
	if got := capFind(t, c, c.alice, "q=weir"); titles(got) != "The Martian | Project Hail Mary" && titles(got) != "Project Hail Mary | The Martian" {
		t.Errorf("author = %s", titles(got))
	}
	if got := capFind(t, c, c.alice, "q=ray+porter"); titles(got) != "Project Hail Mary" {
		t.Errorf("narrator = %s", titles(got))
	}
	if got := capFind(t, c, c.alice, "q=a&limit=2"); len(got) != 2 {
		t.Errorf("limit = %s", titles(got))
	}
	// a shared library: each person with their own place
	if got := capFind(t, c, c.alice, "q=gruffalo"); len(got) != 1 || got[0].Snippet != "Julia Donaldson · read by Imelda Staunton · 10 min · finished" {
		t.Errorf("alice gruffalo = %+v", got)
	}
	if got := capFind(t, c, c.bob, "q=gruffalo"); len(got) != 1 || got[0].Snippet != "Julia Donaldson · read by Imelda Staunton · 10 min · 50% listened" {
		t.Errorf("bob gruffalo = %+v", got)
	}
	// nothing from a library the person may not open, nothing without an account
	for _, q := range []string{"q=dune", "q=weir", "q=project", "q=a"} {
		for _, it := range capFind(t, c, c.bob, q) {
			if it.ID != "gruffalo" {
				t.Errorf("bob found %s with %s", it.Title, q)
			}
		}
		if got := capFind(t, c, c.carol, q); len(got) != 0 {
			t.Errorf("carol found %s with %s", titles(got), q)
		}
	}
	// words without a letter or a digit find nothing
	for _, q := range []string{"q=", "q=+", "q=%3F%3F", "q=*", "q=%22"} {
		if got := capFind(t, c, c.alice, q); len(got) != 0 {
			t.Errorf("%s = %s", q, titles(got))
		}
	}
	// a library the person hid is left out, as on the search page
	st, _ := c.s.store.GetSettings(context.Background(), c.alice.u.ID)
	st.HiddenLibraries = "kids"
	if err := c.s.store.SaveUserSettings(context.Background(), c.alice.u.ID, st); err != nil {
		t.Fatal(err)
	}
	if got := capFind(t, c, c.alice, "q=gruffalo"); len(got) != 0 {
		t.Errorf("hidden library = %s", titles(got))
	}
	// GET /search is as it was
	if w := call(t, c.h, c.alice, "GET", "/api/v1/search?q=dune", ""); w.Code != 200 || len(nativeArray(jsonObject(t, w)["books"])) != 2 {
		t.Errorf("/search = %d %s", w.Code, w.Body)
	}
}

// Pocket Day: one item per book listened to that day, with its minutes, and
// the books finished that day ticked off.
func TestPocketDay(t *testing.T) {
	c := newCapHarness(t)
	at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.Local) }
	c.listen(c.alice, "phm", "Project Hail Mary", "Andy Weir", at(20, 9, 30), 2400)
	c.listen(c.alice, "phm", "Project Hail Mary", "Andy Weir", at(20, 20, 0), 1800)
	c.listen(c.alice, "dune", "Dune", "Frank Herbert", at(20, 21, 0), 1200)
	c.finishBook(c.alice, "dune", "Dune", "Frank Herbert", at(20, 21, 20), 0)
	c.finishBook(c.alice, "gruffalo", "The Gruffalo", "Julia Donaldson", at(20, 8, 0), 0)
	c.listen(c.alice, "phm", "Project Hail Mary", "Andy Weir", at(21, 7, 0), 4200)
	c.listen(c.bob, "gruffalo", "The Gruffalo", "Julia Donaldson", at(20, 19, 0), 300)

	var day core.Day
	capGet(t, c, c.alice, "day/2026-11-21", &day)
	if day.Summary != "Listened to Project Hail Mary for 1 h 10 min" || len(day.Items) != 1 {
		t.Fatalf("one book = %+v", day)
	}
	if it := day.Items[0]; it.ID != "phm" || it.Title != "Project Hail Mary" || it.Sub != "Andy Weir" || it.Minutes != 70 || it.Time != "07:00" || it.End != "08:10" || it.Done || it.URL != "/book/phm" || it.Image != "/api/v1/cover/phm" {
		t.Errorf("item = %+v", it)
	}

	day = core.Day{}
	capGet(t, c, c.alice, "day/2026-11-20", &day)
	if day.Summary != "Listened to Project Hail Mary and Dune for 1 h 30 min, finished 2 books" {
		t.Errorf("summary = %q", day.Summary)
	}
	byID := map[string]core.DayItem{}
	for _, it := range day.Items {
		byID[it.ID] = it
	}
	if len(day.Items) != 3 || byID["phm"].Minutes != 70 || byID["phm"].Time != "09:30" || byID["phm"].End != "20:30" || byID["phm"].Done ||
		byID["dune"].Minutes != 20 || !byID["dune"].Done || byID["gruffalo"].Minutes != 0 || !byID["gruffalo"].Done || byID["gruffalo"].Time != "08:00" {
		t.Errorf("items = %+v", day.Items)
	}
	if len(day.Metrics) != 2 || day.Metrics[0] != (core.Metric{Label: "Time", Value: "1 h 30 min"}) || day.Metrics[1] != (core.Metric{Label: "Finished", Value: "2 books"}) {
		t.Errorf("metrics = %+v", day.Metrics)
	}

	// another person's day is theirs; an empty day is nothing
	day = core.Day{}
	capGet(t, c, c.bob, "day/2026-11-20", &day)
	if day.Summary != "Listened to The Gruffalo for 5 min" || len(day.Items) != 1 || day.Items[0].ID != "gruffalo" {
		t.Errorf("bob = %+v", day)
	}
	for _, p := range []person{c.bob, c.carol} {
		day = core.Day{}
		capGet(t, c, p, "day/2026-11-21", &day)
		if day.Summary != "" || len(day.Items) != 0 {
			t.Errorf("empty day = %+v", day)
		}
	}
	// a finished book alone
	c.finishBook(c.bob, "gruffalo", "The Gruffalo", "Julia Donaldson", at(22, 12, 0), 0)
	day = core.Day{}
	capGet(t, c, c.bob, "day/2026-11-22", &day)
	if day.Summary != "Finished The Gruffalo" || len(day.Items) != 1 || !day.Items[0].Done || len(day.Metrics) != 1 {
		t.Errorf("finished only = %+v", day)
	}

	// the caller's time zone decides the day
	late := time.Date(2026, 11, 24, 23, 30, 0, 0, time.UTC)
	c.listen(c.carol, "x", "Late Book", "Night Owl", late, 600)
	day = core.Day{}
	capGet(t, c, c.carol, "day/2026-11-25?tz=Pacific/Kiritimati", &day)
	if len(day.Items) != 1 || day.Items[0].Time != "13:30" || day.Items[0].End != "13:40" {
		t.Errorf("in +14 = %+v", day)
	}
	day = core.Day{}
	capGet(t, c, c.carol, "day/2026-11-24?tz=Pacific/Kiritimati", &day)
	if len(day.Items) != 0 {
		t.Errorf("day before in +14 = %+v", day)
	}
	day = core.Day{}
	capGet(t, c, c.carol, "day/2026-11-24?tz=America/Los_Angeles", &day)
	if len(day.Items) != 1 || day.Items[0].Time != "15:30" {
		t.Errorf("in Los Angeles = %+v", day)
	}

	// GET /day/{date} is as it was
	if w := call(t, c.h, c.alice, "GET", "/api/v1/day/2026-11-20", ""); w.Code != 200 || len(nativeArray(jsonObject(t, w)["books"])) != 2 {
		t.Errorf("/day = %d %s", w.Code, w.Body)
	}
}

// Pocket Hub: the books in progress, the latest first, with the time left;
// then the newest books, from the libraries the person sees.
func TestPocketWidget(t *testing.T) {
	c := newCapHarness(t)
	c.setProgress(c.alice, "phm", 300, map[string]any{"currentTime": 46200.0})
	c.setProgress(c.alice, "dune", 200, map[string]any{"currentTime": 100.0})
	c.setProgress(c.alice, "gruffalo", 400, map[string]any{"isFinished": true})
	c.setProgress(c.alice, "messiah", 500, map[string]any{"currentTime": 60.0, "hideFromContinueListening": true})
	c.setProgress(c.bob, "gruffalo", 100, map[string]any{"currentTime": 300.0})

	rows := func(w core.WidgetData) string {
		var out []string
		for _, it := range w.Items {
			out = append(out, it.Title+" ("+it.Detail+")")
		}
		return strings.Join(out, " | ")
	}
	var w core.WidgetData
	capGet(t, c, c.alice, "widget", &w)
	if w.Line != "Project Hail Mary, 3 h 20 min left" || w.URL != "/" {
		t.Errorf("line = %q %q", w.Line, w.URL)
	}
	if got := rows(w); got != "Project Hail Mary (Andy Weir · 3 h 20 min left) | Dune (Frank Herbert · 20 h 58 min left) | The Martian (Andy Weir · 10 h 50 min) | The Gruffalo (Julia Donaldson · 10 min) | Dune Messiah (Frank Herbert · 8 h 20 min)" {
		t.Errorf("rows = %s", got)
	}
	if w.Items[0].ID != "phm" || w.Items[0].URL != "/book/phm" {
		t.Errorf("first = %+v", w.Items[0])
	}

	// a shared book with Bob's own place; nothing of Alice's library
	w = core.WidgetData{}
	capGet(t, c, c.bob, "widget", &w)
	if w.Line != "The Gruffalo, 5 min left" || rows(w) != "The Gruffalo (Julia Donaldson · 5 min left)" {
		t.Errorf("bob = %q %s", w.Line, rows(w))
	}
	// no account: nothing to show
	w = core.WidgetData{}
	capGet(t, c, c.carol, "widget", &w)
	if w.Line != "" || len(w.Items) != 0 {
		t.Errorf("carol = %+v", w)
	}
	// nothing in progress: no line, the newest books
	c.setProgress(c.bob, "gruffalo", 600, map[string]any{"isFinished": true})
	w = core.WidgetData{}
	capGet(t, c, c.bob, "widget", &w)
	if w.Line != "" || rows(w) != "The Gruffalo (Julia Donaldson · 10 min)" {
		t.Errorf("bob finished = %q %s", w.Line, rows(w))
	}
	// a hidden library is left out
	st, _ := c.s.store.GetSettings(context.Background(), c.alice.u.ID)
	st.HiddenLibraries = "kids"
	if err := c.s.store.SaveUserSettings(context.Background(), c.alice.u.ID, st); err != nil {
		t.Fatal(err)
	}
	w = core.WidgetData{}
	capGet(t, c, c.alice, "widget", &w)
	if got := rows(w); got != "Project Hail Mary (Andy Weir · 3 h 20 min left) | Dune (Frank Herbert · 20 h 58 min left) | The Martian (Andy Weir · 10 h 50 min) | Dune Messiah (Frank Herbert · 8 h 20 min)" {
		t.Errorf("hidden = %s", got)
	}
}

// Each pocket:* scope opens its one route (and /auth/me), nothing else; the
// app's older scopes do not open the new routes.
func TestPocketLinkTokens(t *testing.T) {
	c := newCapHarness(t)
	day := "/api/v1/pocket/day/2026-11-20"
	for _, x := range []struct {
		tok, method, path string
		want              int
	}{
		{"pcx_alice_search", "GET", "/api/v1/pocket/search?q=dune", 200},
		{"pcx_alice_day", "GET", day, 200},
		{"pcx_alice_widget", "GET", "/api/v1/pocket/widget", 200},
		{"pcx_alice_search", "GET", "/api/v1/auth/me", 200},
		{"pcx_alice_widget", "GET", "/api/v1/auth/me", 200},

		{"pcx_alice_search", "GET", day, 403},
		{"pcx_alice_search", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_alice_day", "GET", "/api/v1/pocket/search?q=dune", 403},
		{"pcx_alice_day", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_alice_widget", "GET", "/api/v1/pocket/search?q=dune", 403},
		{"pcx_alice_widget", "GET", day, 403},
		{"pcx_alice_capture", "POST", "/api/v1/pocket/capture", 404}, // no capture here
		{"pcx_alice_capture", "GET", "/api/v1/pocket/widget", 403},

		{"pcx_alice_search", "GET", "/api/v1/search?q=dune", 403},
		{"pcx_alice_search", "GET", "/api/v1/link/find?q=dune", 403},
		{"pcx_alice_search", "GET", "/api/v1/books", 403},
		{"pcx_alice_day", "GET", "/api/v1/day/2026-11-20", 403},
		{"pcx_alice_day", "GET", "/api/v1/history", 403},
		{"pcx_alice_widget", "GET", "/api/v1/home", 403},
		{"pcx_alice_widget", "GET", "/api/v1/dashboard", 403},
		{"pcx_alice_widget", "GET", "/api/v1/cover/phm", 403},
		{"pcx_alice_widget", "POST", "/api/v1/books/phm/progress", 403},
		{"pcx_alice_widget", "POST", "/api/v1/up-next", 403},

		{"pcx_alice_read", "GET", "/api/v1/pocket/search?q=dune", 403},
		{"pcx_alice_read", "GET", day, 403},
		{"pcx_alice_read", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_alice_library", "GET", "/api/v1/pocket/search?q=dune", 403},
		{"pcx_alice_library", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_alice_scan", "GET", day, 403},
		{"pcx_alice_read", "GET", "/api/v1/day/2026-11-20", 200},
	} {
		r := httptest.NewRequest(x.method, x.path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+x.tok)
		w := httptest.NewRecorder()
		c.h.ServeHTTP(w, r)
		if w.Code != x.want {
			t.Errorf("%s %s %s = %d, want %d (%s)", x.tok, x.method, x.path, w.Code, x.want, w.Body)
		}
	}
	// through a link, the person's own: Alice's search, Bob's widget
	get := func(tok, path string, out any) {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		c.h.ServeHTTP(w, r)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), out) != nil {
			t.Fatalf("%s %s = %d %s", tok, path, w.Code, w.Body)
		}
	}
	var found core.SearchResults
	get("pcx_alice_search", "/api/v1/pocket/search?q=dune", &found)
	if titles(found.Items) != "Dune | Dune Messiah" {
		t.Errorf("alice through a link = %s", titles(found.Items))
	}
	get("pcx_bob_search", "/api/v1/pocket/search?q=dune", &found)
	if len(found.Items) != 0 {
		t.Errorf("bob through a link = %s", titles(found.Items))
	}
	c.setProgress(c.bob, "gruffalo", 100, map[string]any{"currentTime": 300.0})
	var w core.WidgetData
	get("pcx_bob_widget", "/api/v1/pocket/widget", &w)
	if w.Line != "The Gruffalo, 5 min left" || len(w.Items) != 1 {
		t.Errorf("bob's widget through a link = %+v", w)
	}
}

// The manifest carries the category, the capabilities and their offers.
func TestPocketManifest(t *testing.T) {
	c := newCapHarness(t)
	if err := c.s.registerWithCore(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := c.manifest()
	if m["category"] != "Media" {
		t.Errorf("category = %v", m["category"])
	}
	caps := m // the capabilities sit at the top of the manifest
	search, _ := caps["search"].(map[string]any)
	day, _ := caps["day"].(map[string]any)
	widget, _ := caps["widget"].(map[string]any)
	if search == nil || fmt.Sprint(search["bangs"]) != "[audiobooks audio]" || fmt.Sprint(search["kinds"]) != "[audiobook]" {
		t.Errorf("search = %v", caps["search"])
	}
	if day == nil || day["label"] != "Audiobooks" || day["one"] != "book" || day["many"] != "books" || day["unit"] != "min" {
		t.Errorf("day = %v", caps["day"])
	}
	if widget == nil || widget["title"] != "Continue listening" || caps["capture"] != nil {
		t.Errorf("widget/capture = %v %v", caps["widget"], caps["capture"])
	}
	offers := map[string]bool{}
	for _, o := range m["offers"].([]any) {
		offers[o.(map[string]any)["scope"].(string)] = true
	}
	for _, sc := range []string{core.ScopeSearch, core.ScopeDay, core.ScopeWidget, "listening:read", "library:read", "library:scan"} {
		if !offers[sc] {
			t.Errorf("offer %s missing: %v", sc, offers)
		}
	}
	if offers[core.ScopeCapture] {
		t.Error("capture is offered")
	}
}

// With MEDIA_BACKEND=audiobookshelf the widget asks that server as the home
// page does, with the person's own account.
func TestPocketWidgetOnAudiobookshelf(t *testing.T) {
	s, h, _ := testServer(t)
	ana := addPerson(t, s, "ana@example.com", "keyA")
	c := &capHarness{s: s, h: h}
	var w core.WidgetData
	capGet(t, c, ana, "widget", &w)
	if w.Line != "The Hobbit, 50 min left" || len(w.Items) != 1 || w.Items[0].ID != bookID || w.Items[0].Detail != "J.R.R. Tolkien · 50 min left" {
		t.Errorf("widget = %+v", w)
	}
}
