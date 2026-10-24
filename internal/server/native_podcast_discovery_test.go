package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNativePodcastDirectoryPermissionFilteringAndContract(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true'),'$.accessExplicitContent',json('false')) WHERE id='alice'`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.nativeFetchHTTP = &http.Client{Transport: gatewayTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "itunes.apple.com" || r.URL.Query().Get("term") != "science & space" || r.URL.Query().Get("entity") != "podcast" {
			t.Error("incorrect search request", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"results":[{"collectionId":1,"collectionName":"Science","feedUrl":"https://example.com/feed.xml","collectionViewUrl":"https://podcasts.apple.com/show/1"},{"collectionId":2,"collectionName":"Explicit","feedUrl":"https://example.com/adult.xml","collectionExplicitness":"explicit"},{"collectionId":3,"feedUrl":"file:///etc/passwd"}]}`))}, nil
	})}
	if w := call(t, h, b, "GET", "/api/v1/podcasts/search?term=science", ""); w.Code != 403 || calls != 0 {
		t.Fatal("unauthorized directory", w.Code, calls)
	}
	w := call(t, h, a, "GET", "/api/v1/podcasts/search?term=science+%26+space", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Science") || strings.Contains(w.Body.String(), "Explicit") || strings.Contains(w.Body.String(), "passwd") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = nativeCall(t, h, tok, "GET", "/api/search/podcast?term=science+%26+space", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "feedUrl") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call(t, h, a, "GET", "/api/v1/podcasts/search?term=ab&country=bad", ""); w.Code != 400 || calls != 2 {
		t.Fatal("invalid country", w.Code, calls)
	}
}
