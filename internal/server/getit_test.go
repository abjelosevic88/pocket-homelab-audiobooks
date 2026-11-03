package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// "Get it": the audiobook page asks Pocket Requests (through Core, as the
// person) about the book's ebook, and asks for it; the author page lists more
// of their books. Without the link it stays visible, with the reason.
func TestGetItThroughRequests(t *testing.T) {
	s, h, a, b, _ := nativeHarness(t)
	if _, err := s.store.DB.Exec(`UPDATE catalog_items SET title='Project Hail Mary (Unabridged)',author='Andy Weir',document=json_set(document,'$.media.title','Project Hail Mary (Unabridged)','$.media.isbn','9780593135228','$.authors',json('[{"id":"w","name":"Andy Weir"}]')) WHERE id='book'`); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var posted []map[string]any
	asked := false
	rq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pcx_rq" {
			http.Error(w, `{"error":"no"}`, http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		j := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		title := func(key, name string) map[string]any {
			var list []any
			if asked && key == "ol:OL1W" {
				list = append(list, map[string]any{"id": 7, "kind": "ebook", "language": "sr", "status": "approved", "state": "downloading", "progress": 0.5})
			}
			return map[string]any{"book_key": key, "title": name, "subtitle": "Andy Weir", "year": 2021,
				"book": map[string]any{"have": map[string]any{"ebook": nil, "audiobook": map[string]any{"url": "https://audio.example.com/book/book"}}, "asked": list}}
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/books/lookup":
			q := r.URL.Query()
			if q.Get("title") != "Project Hail Mary" || q.Get("author") != "Andy Weir" || q.Get("isbn") != "9780593135228" {
				j(map[string]any{"title": nil})
				return
			}
			j(map[string]any{"title": title("ol:OL1W", "Project Hail Mary"), "languages_wanted": []string{"en", "sr"}, "can_get": map[string]any{"ok": true}})
		case r.Method == "GET" && r.URL.Path == "/api/v1/books/author":
			if r.URL.Query().Get("name") != "Andy Weir" {
				j(map[string]any{"items": []any{}})
				return
			}
			j(map[string]any{"items": []any{title("ol:OL1W", "Project Hail Mary"), title("ol:OL2W", "The Martian")}, "languages_wanted": []string{"en"}, "can_get": map[string]any{"ok": true}})
		case r.Method == "POST" && r.URL.Path == "/api/v1/requests":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			posted = append(posted, in)
			if asked {
				w.WriteHeader(http.StatusConflict)
				j(map[string]any{"error": "You already asked for Project Hail Mary.", "request_id": 7})
				return
			}
			asked = true
			w.WriteHeader(http.StatusCreated)
			j(map[string]any{"id": 7, "kind": in["kind"], "status": "approved", "state": "sent"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rq.Close)
	s.rqLink = func(_ context.Context, email string) (*core.LinkToken, error) {
		if email != a.u.Email {
			return nil, &core.LinkError{Reason: "off_person", Message: "switched off"}
		}
		return &core.LinkToken{AccessToken: "pcx_rq", URL: rq.URL, PublicURL: "https://get.example.com"}, nil
	}
	get := func(p person, path string) getIt {
		t.Helper()
		w := call(t, h, p, "GET", path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var v getIt
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return v
	}
	v := get(a, "/api/v1/get-it/items/book")
	if !v.Available || !v.Found || v.Book == nil || v.Book.BookKey != "ol:OL1W" || v.Book.Audio == nil || v.Book.Ebook != nil || len(v.Book.Asked) != 0 || len(v.Languages) != 2 {
		t.Fatalf("first look: %+v", v)
	}
	w := call(t, h, a, "POST", "/api/v1/get-it", `{"kind":"ebook","item_id":"book","book_key":"ol:OL1W","language":"sr"}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), "https://get.example.com/requests/7") {
		t.Fatalf("ask: %d %s", w.Code, w.Body.String())
	}
	if p := posted[0]; p["book_key"] != "ol:OL1W" || p["title"] != "Project Hail Mary" || p["author"] != "Andy Weir" || p["isbn"] != "9780593135228" || p["language"] != "sr" {
		t.Fatalf("posted %v", p)
	}
	// the cache was dropped: the request shows, with its page
	v = get(a, "/api/v1/get-it/items/book")
	if len(v.Book.Asked) != 1 || v.Book.Asked[0].URL != "https://get.example.com/requests/7" || v.Book.Asked[0].Language != "sr" {
		t.Fatalf("after: %+v", v.Book)
	}
	// asking twice: Requests' 409 comes through with the request's page
	if w := call(t, h, a, "POST", "/api/v1/get-it", `{"kind":"ebook","item_id":"book"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "/requests/7") {
		t.Fatalf("twice: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, h, a, "POST", "/api/v1/get-it", `{"kind":"film","title":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad kind: %d", w.Code)
	}
	// more by the author (by name when the author has no profile here)
	v = get(a, "/api/v1/get-it/authors/none?name=Andy+Weir")
	if !v.Available || len(v.Items) != 2 || v.Items[1].Title != "The Martian" {
		t.Fatalf("author: %+v", v)
	}
	// bob: no access to the book, and his link is off (greyed out, not an error)
	if w := call(t, h, b, "GET", "/api/v1/get-it/items/book", ""); w.Code != http.StatusNotFound {
		t.Fatalf("bob on alice's book: %d", w.Code)
	}
	v = get(b, "/api/v1/get-it/authors/none?name=Andy+Weir")
	if v.Available || !strings.Contains(v.Reason, "Apps acting for me") {
		t.Fatalf("bob: %+v", v)
	}
}

func TestFactsDropUnabridged(t *testing.T) {
	cases := map[string]string{"Dune (Unabridged)": "Dune", "Dune (Abridged Edition)": "Dune", "Dune (Book 1)": "Dune (Book 1)"}
	for in, want := range cases {
		f := facts(libraryItem(in))
		if f.Title != want {
			t.Errorf("%q: %q", in, f.Title)
		}
	}
}

func libraryItem(title string) (it library.Item) {
	it.Title, it.Document = title, []byte(`{}`)
	return
}
