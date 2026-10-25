package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
)

// authorFixtures serves Audnexus author responses recorded from api.audnex.us.
func authorFixtures(t *testing.T, calls *atomic.Int32) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("region") != "uk" {
			t.Errorf("region %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/audnexus/authors":
			switch r.URL.Query().Get("name") {
			case "Brandon Sanderson", "Sanderson, Brandon", "Brandon Sandersen":
				w.Write(fixture(t, "audnexus_author_search.json"))
			default:
				w.Write([]byte(`[]`))
			}
		case "/audnexus/authors/B001IGFHW6":
			w.Write(fixture(t, "audnexus_author.json"))
		case "/audnexus/authors/B000000000":
			w.Write(fixture(t, "audnexus_author_empty.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return Client{HTTP: fetch.Client(5*time.Second, []string{"127.0.0.1"}), UserAgent: "PocketAudiobooks/test", AudnexusURL: srv.URL + "/audnexus", Limiter: NewLimiter(0)}
}

func TestAudnexusAuthorSearchAndDetail(t *testing.T) {
	var calls atomic.Int32
	c := authorFixtures(t, &calls)
	ctx := context.Background()
	refs, err := c.AudnexusAuthorSearch(ctx, "uk", "Brandon Sanderson")
	if err != nil {
		t.Fatal(err)
	}
	// Audnexus repeats the same ASIN; results are de-duplicated in order.
	if len(refs) < 3 || refs[0].ASIN != "B001IGFHW6" || refs[1].ASIN == "B001IGFHW6" {
		t.Fatalf("refs %v", refs)
	}
	for _, name := range []string{"Brandon Sanderson", "Sanderson, Brandon", "Brandon Sandersen"} {
		p, err := c.FindAudnexusAuthor(ctx, "uk", name)
		if err != nil {
			t.Fatal(name, err)
		}
		if p.ASIN != "B001IGFHW6" || p.Name != "Brandon Sanderson" || p.Region != "uk" || !strings.HasPrefix(p.Image, "https://") {
			t.Fatalf("%s: %+v", name, p)
		}
		if strings.Contains(p.Description, "<br") || strings.Contains(p.Description, "&amp;") || !strings.Contains(p.Description, "Sample chapters & more.") {
			t.Fatalf("description not plain text: %q", p.Description)
		}
		if len(p.Genres) == 0 || p.Genres[0] != "Science Fiction & Fantasy" {
			t.Fatal("genres", p.Genres)
		}
	}
	if _, err = c.FindAudnexusAuthor(ctx, "uk", "Nobody Known"); !errors.Is(err, ErrNoAuthor) {
		t.Fatal("unknown name", err)
	}
	// Unknown ASINs come back as an empty record with a product name.
	if _, err = c.AudnexusAuthor(ctx, "uk", "B000000000"); !errors.Is(err, ErrNoAuthor) {
		t.Fatal("empty record", err)
	}
	if _, err = c.AudnexusAuthor(ctx, "uk", "../books"); err == nil {
		t.Fatal("invalid ASIN accepted")
	}
}

func TestBestAuthorDistance(t *testing.T) {
	refs := []AuthorRef{{"B00AY6JF6U", "Brandon Turner"}, {"B001IGFHW6", "Brandon Sanderson"}}
	if r, d, ok := BestAuthor("brandon sanderson", refs); !ok || r.ASIN != "B001IGFHW6" || d != 0 {
		t.Fatal(r, d, ok)
	}
	if _, _, ok := BestAuthor("Brandon Smith", refs); ok {
		t.Fatal("distant name matched")
	}
	if _, _, ok := BestAuthor("Bo", []AuthorRef{{"B000000001", "Al"}}); ok {
		t.Fatal("short name matched loosely")
	}
	if d := AuthorDistance("J.R.R. Tolkien", "J. R. R. Tolkien"); d != 0 {
		t.Fatal("initials", d)
	}
	if d := AuthorDistance("Tolkien, J. R. R.", "J.R.R. Tolkien"); d != 0 {
		t.Fatal("last, first", d)
	}
}
