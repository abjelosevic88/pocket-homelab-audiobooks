package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// audibleFixtures serves recorded Audible and Audnexus responses.
func audibleFixtures(t *testing.T, calls *atomic.Int32) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("User-Agent") != "PocketAudiobooks/test" {
			t.Error("missing user agent")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/audible/1.0/catalog/products":
			q := r.URL.Query()
			if q.Get("num_results") != "10" || q.Get("products_sort_by") != "Relevance" || !strings.Contains(q.Get("response_groups"), "series") {
				t.Errorf("search parameters %v", q)
			}
			if q.Get("title") == "Nothing" || q.Get("keywords") == "Nothing" {
				w.Write([]byte(`{"products":[]}`))
				return
			}
			if q.Get("title") != "The Way of Kings" && q.Get("keywords") != "The Way of Kings" {
				t.Errorf("query %v", q)
			}
			w.Write(fixture(t, "audible_search.json"))
		case r.URL.Path == "/audnexus/books/B003ZWFO7E":
			if r.URL.Query().Get("region") != "uk" {
				t.Errorf("region %v", r.URL.Query())
			}
			w.Write(fixture(t, "audnexus_book.json"))
		case r.URL.Path == "/audnexus/books/B08G9PRS1K/chapters":
			w.Write(fixture(t, "audnexus_chapters.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return Client{HTTP: fetch.Client(5*time.Second, []string{"127.0.0.1"}), UserAgent: "PocketAudiobooks/test", AudibleURL: srv.URL + "/audible", AudnexusURL: srv.URL + "/audnexus", Limiter: NewLimiter(0)}
}

func TestAudibleSearchParsesRecordedCatalog(t *testing.T) {
	var calls atomic.Int32
	c := audibleFixtures(t, &calls)
	got, err := c.Search(context.Background(), Provider{ID: "audible", Kind: "audible", Region: "uk"}, "The Way of Kings", "Brandon Sanderson", "")
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	first := got[0]
	m := first.Metadata
	if first.ID != "B003ZWFO7E" || first.Provider != "audible" || first.Region != "uk" || first.Duration != 2730*60 {
		t.Fatalf("candidate %+v", first)
	}
	if first.Cover != "https://m.media-amazon.com/images/I/51hAwcG3oNL.jpg" {
		t.Fatal("cover not full size", first.Cover)
	}
	series := m["series"].([]any)
	if s := series[0].(map[string]any); s["name"] != "The Stormlight Archive" || s["sequence"] != "1" {
		t.Fatal("numbered series first", series)
	}
	if m["subtitle"] != "Book One of the Stormlight Archive" || m["publishedYear"] != "2010" || m["language"] != "English" || m["publisher"] != "Macmillan Audio" {
		t.Fatal("details", m)
	}
	if n := m["narrators"].([]string); len(n) != 2 || n[0] != "Michael Kramer" {
		t.Fatal("narrators", n)
	}
	if d := m["description"].(string); strings.Contains(d, "<") || !strings.Contains(d, "Brandon Sanderson") {
		t.Fatal("description html", d)
	}
	if g := m["genres"].([]string); g[0] != "Science Fiction & Fantasy" {
		t.Fatal("genres", g)
	}
	if m["duration"] != nil {
		t.Fatal("runtime must not become editable metadata")
	}
	// Empty title+author search retries with keywords only.
	calls.Store(0)
	if got, err = c.Search(context.Background(), Provider{ID: "audible", Kind: "audible"}, "Nothing", "Nobody", ""); err != nil || len(got) != 0 || calls.Load() != 2 {
		t.Fatalf("fallback %d %v", calls.Load(), err)
	}
}

func TestAudnexusDetailAndASINLookup(t *testing.T) {
	var calls atomic.Int32
	c := audibleFixtures(t, &calls)
	got, err := c.Search(context.Background(), Provider{ID: "audible", Kind: "audible", Region: "uk"}, "B003ZWFO7E", "", "")
	if err != nil || len(got) != 1 || calls.Load() != 1 {
		t.Fatalf("asin lookup %+v %v %d", got, err, calls.Load())
	}
	m := got[0].Metadata
	if m["isbn"] != "9781427209764" || m["asin"] != "B003ZWFO7E" || got[0].Cover != "https://m.media-amazon.com/images/I/91iPex6QO8L.jpg" {
		t.Fatal("detail", m, got[0].Cover)
	}
	series := m["series"].([]any)
	if len(series) != 2 || series[0].(map[string]any)["sequence"] != "1" || series[1].(map[string]any)["name"] != "The Cosmere" {
		t.Fatal("series", series)
	}
	if g, tags := m["genres"].([]string), m["tags"].([]string); len(g) != 1 || g[0] != "Science Fiction & Fantasy" || len(tags) != 4 || tags[0] != "Fantasy" {
		t.Fatal("genres/tags", g, tags)
	}
	if m["abridged"] != false || got[0].Duration != 2730*60 {
		t.Fatal("format/runtime", m["abridged"], got[0].Duration)
	}
	if _, err = c.AudibleBook(context.Background(), "audible", "us", "../admin"); err == nil {
		t.Fatal("arbitrary path")
	}
}

func TestAudnexusChapters(t *testing.T) {
	var calls atomic.Int32
	c := audibleFixtures(t, &calls)
	ch, err := c.AudibleChapterList(context.Background(), "us", "B08G9PRS1K")
	if err != nil || len(ch.Chapters) != 5 || !ch.Accurate {
		t.Fatalf("%+v %v", ch, err)
	}
	if ch.Runtime != 58244.201 || ch.Chapters[0].Title != "Opening Credits" || ch.Chapters[1].Start != 11.264 || ch.Chapters[2].End != 17.173+2203.838 {
		t.Fatalf("%+v", ch)
	}
}

func TestLimiterSpacesRequestsAndCancels(t *testing.T) {
	l := NewLimiter(40 * time.Millisecond)
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) < 80*time.Millisecond {
		t.Fatal("not spaced", time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	l = NewLimiter(time.Hour)
	l.Wait(ctx)
	cancel()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("cancelled wait succeeded")
	}
}

func TestHelpers(t *testing.T) {
	if !LooksLikeASIN("B0CTZVXP8H") || !LooksLikeASIN("1427209766") || LooksLikeASIN("FOUNDATION") || LooksLikeASIN("b0ctzvxp8h") {
		t.Fatal("asin detection")
	}
	if HTMLText("<p><b>One</b> &amp; two</p><p>Three<br/>four</p>") != "One & two\n\nThree\nfour" {
		t.Fatalf("%q", HTMLText("<p><b>One</b> &amp; two</p><p>Three<br/>four</p>"))
	}
	if CleanSequence("Book 2.5") != "2.5" || CleanSequence("") != "" || CleanSequence("Prequel") != "Prequel" {
		t.Fatal("sequence")
	}
	if DefaultAudibleRegion() != "us" {
		t.Setenv("AUDIBLE_REGION", "")
	}
	t.Setenv("AUDIBLE_REGION", "DE")
	if DefaultAudibleRegion() != "de" {
		t.Fatal("env region")
	}
	t.Setenv("AUDIBLE_REGION", "mars")
	if DefaultAudibleRegion() != "us" {
		t.Fatal("invalid env region")
	}
}

func TestAudibleLiveContract(t *testing.T) {
	if os.Getenv("POCKET_METADATA_LIVE") != "1" {
		t.Skip("optional public metadata contract check")
	}
	c := Client{HTTP: fetch.Client(20*time.Second, nil), UserAgent: "PocketAudiobooks/1.0 (+https://github.com/abjelosevic88/pocket-homelab-audiobooks)"}
	out, err := c.Search(context.Background(), Provider{ID: "audible", Kind: "audible"}, "Project Hail Mary", "Andy Weir", "")
	if err != nil || len(out) == 0 {
		t.Fatalf("search returned %d results: %v", len(out), err)
	}
	if _, err = c.AudibleBook(context.Background(), "audible", "us", out[0].ID); err != nil {
		t.Fatal(err)
	}
}
