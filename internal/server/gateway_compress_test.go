package server

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// Library stats name the top ten items compactly, like Audiobookshelf. Full
// items (podcasts carry every episode) made this answer 27 MB.
func TestNativeLibraryStatsAreCompact(t *testing.T) {
	s, h, _, _, tok := nativeHarness(t)
	big := strings.Repeat("A long description. ", 50000) // ~1 MB inside the item
	if _, err := s.store.DB.Exec(`UPDATE catalog_items SET document=json_set(document,'$.media.description',?,'$.authors',json('[{"id":"writer","name":"Writer"}]')) WHERE id='book'`, big); err != nil {
		t.Fatal(err)
	}
	w := nativeCall(t, h, tok, "GET", "/api/libraries/lib/stats", "")
	if w.Body.Len() > 4096 {
		t.Fatalf("stats answer is %d bytes", w.Body.Len())
	}
	stats := jsonObject(t, w)
	keys := func(v any) string {
		out := []string{}
		for k := range nativeMap(v) {
			out = append(out, k)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	largest, longest := nativeArray(stats["largestItems"]), nativeArray(stats["longestItems"])
	if len(largest) != 1 || keys(largest[0]) != "id,size,title" || nativeMap(largest[0])["id"] != "book" || nativeNumber(nativeMap(largest[0])["size"]) != 30 {
		t.Fatalf("largestItems: %v", largest)
	}
	if len(longest) != 1 || keys(longest[0]) != "duration,id,title" || nativeNumber(nativeMap(longest[0])["duration"]) != 100 {
		t.Fatalf("longestItems: %v", longest)
	}
	if nativeString(nativeMap(largest[0])["title"]) == "" {
		t.Fatalf("largestItems has no title: %v", largest)
	}
	authors := nativeArray(stats["authorsWithCount"])
	if len(authors) != 1 || keys(authors[0]) != "count,id,name,numBooks" || nativeNumber(nativeMap(authors[0])["count"]) != 1 {
		t.Fatalf("authorsWithCount: %v", authors)
	}
}

// The Audiobookshelf-compatible API gzips JSON, but audio and ranges keep their bytes.
func TestGatewayCompressesJSONButNotMedia(t *testing.T) {
	_, h, _, _, tok := nativeHarness(t)
	get := func(path, rng string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Accept-Encoding", "gzip")
		if rng != "" {
			r.Header.Set("Range", rng)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/api/libraries/lib/stats", "")
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("stats: %d %q", w.Code, w.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	var stats map[string]any
	if err = json.NewDecoder(zr).Decode(&stats); err != nil || stats["totalItems"] != float64(1) {
		t.Fatalf("gzipped stats: %v %v", stats, err)
	}
	// The same route under /audiobookshelf is compressed too.
	if w = get("/audiobookshelf/api/libraries/lib/stats", ""); w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("prefixed stats: %d %q", w.Code, w.Header().Get("Content-Encoding"))
	}
	w = get("/api/items/book/file/12", "bytes=2-5")
	if w.Code != 206 || w.Header().Get("Content-Encoding") != "" || w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/15" {
		t.Fatalf("range: %d %q %q %q", w.Code, w.Header().Get("Content-Encoding"), w.Header().Get("Content-Range"), w.Body.String())
	}
	w = get("/api/items/book/file/12", "")
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "" || w.Body.String() != "0123456789audio" {
		t.Fatalf("whole file: %d %q %q", w.Code, w.Header().Get("Content-Encoding"), w.Body.String())
	}
	// Unauthenticated callers still get a plain 401.
	r := httptest.NewRequest("GET", "/api/libraries/lib/stats", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("no token:", w.Code)
	}
}

func TestCompressJSONLeavesMediaTypesAlone(t *testing.T) {
	body := strings.Repeat("compressible ", 200)
	for _, tc := range []struct {
		ctype, rng string
		gzip       bool
	}{
		{"application/json; charset=utf-8", "", true},
		{"application/json", "bytes=0-9", false},
		{"application/vnd.apple.mpegurl", "", false},
		{"video/MP2T", "", false},
		{"audio/mpeg", "", false},
		{"audio/mp4", "", false},
		{"image/jpeg", "", false},
	} {
		h := compressJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", tc.ctype)
			_, _ = io.WriteString(w, body)
		}))
		r := httptest.NewRequest("GET", "/api/x", nil)
		r.Header.Set("Accept-Encoding", "gzip, deflate, br")
		if tc.rng != "" {
			r.Header.Set("Range", tc.rng)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		got := w.Header().Get("Content-Encoding") == "gzip"
		if got != tc.gzip {
			t.Fatalf("%s range=%q: gzip=%v", tc.ctype, tc.rng, got)
		}
		if !got && w.Body.String() != body {
			t.Fatalf("%s: body changed", tc.ctype)
		}
	}
}
