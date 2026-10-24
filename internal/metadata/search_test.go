package metadata

import (
	"context"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProviderContractsAndPartialMetadata(t *testing.T) {
	c := Client{HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Host {
		case "openlibrary.org":
			if strings.HasPrefix(r.URL.Path, "/works/") {
				body = `{"description":{"value":"A work description"},"subjects":["Fiction"]}`
			} else {
				if r.URL.Query().Get("q") != "A title" {
					t.Fatal("query")
				}
				body = `{"docs":[{"key":"/works/OL1W","title":"A title","author_name":["An author"],"first_publish_year":1900,"cover_i":42,"isbn":["123"]}]}`
			}
		case "www.googleapis.com":
			if r.URL.Query().Get("key") != "private-key" {
				t.Fatal("missing API key")
			}
			body = `{"items":[{"id":"g1","volumeInfo":{"title":"Google title","authors":["Writer"],"publishedDate":"2020-01-01","description":"Description","industryIdentifiers":[{"type":"ISBN_13","identifier":"9780000000000"}],"imageLinks":{"thumbnail":"http://books.google.com/cover"}}}]}`
		case "provider.example":
			if r.URL.Path != "/base/search" || r.Header.Get("Authorization") != "Bearer private-key" || r.URL.Query().Get("mediaType") != "book" {
				t.Fatal("custom protocol")
			}
			body = `{"matches":[{"title":"Custom title","author":["One","Two"],"narrator":"Narrator","series":[{"series":"Series","sequence":2}],"tags":"one, two","cover":"file:///private","duration":1}]}`
		default:
			t.Fatal("unexpected host")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}, UserAgent: "test"}
	for _, p := range []Provider{{ID: "ol", Kind: "openlibrary"}, {ID: "g", Kind: "google", Authorization: "private-key"}, {ID: "c", Kind: "custom", URL: "https://provider.example/base", Authorization: "Bearer private-key"}} {
		got, err := c.Search(context.Background(), p, "A title", "An author", "")
		if err != nil || len(got) != 1 {
			t.Fatalf("%s %+v %v", p.ID, got, err)
		}
		if got[0].Provider != p.ID || got[0].Metadata["duration"] != nil {
			t.Fatal("provider changed playback metadata", got)
		}
		if p.Kind == "custom" && got[0].Cover != "" {
			t.Fatal("unsafe cover retained")
		}
	}
	work, err := c.Work(context.Background(), "OL1W")
	if err != nil || work["description"] != "A work description" {
		t.Fatalf("detail %+v %v", work, err)
	}
	if _, err = c.Work(context.Background(), "../admin"); err == nil {
		t.Fatal("arbitrary detail path")
	}
}

func TestOpenLibraryLiveContract(t *testing.T) {
	if os.Getenv("POCKET_METADATA_LIVE") != "1" {
		t.Skip("optional public metadata contract check")
	}
	c := Client{HTTP: fetch.Client(20*time.Second, nil), UserAgent: "PocketAudiobooks/1.0 (+https://github.com/abjelosevic88/pocket-homelab-audiobooks)"}
	out, err := c.Search(context.Background(), Provider{ID: "openlibrary", Kind: "openlibrary"}, "Frankenstein", "Mary Shelley", "")
	if err != nil || len(out) == 0 {
		t.Fatalf("public search returned %d results: %v", len(out), err)
	}
	if _, err = c.Work(context.Background(), out[0].ID); err != nil {
		t.Fatalf("work detail: %v", err)
	}
	t.Logf("public metadata search and work detail verified (%d matches)", len(out))
}
