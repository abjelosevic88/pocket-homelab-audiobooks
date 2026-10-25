package server

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// authorHarness has two authors on the native book: "auth-1" (Brandon
// Sanderson, nothing yet) and "auth-2", whose bio and photo came from an
// Audiobookshelf import (photo under the read-only source mount).
func authorHarness(t *testing.T) (*Server, http.Handler, person, person, *atomic.Int32) {
	t.Helper()
	s, h, a, b, _ := matchHarness(t)
	var root string
	if err := s.store.DB.QueryRow(`SELECT local_path FROM catalog_roots WHERE id='root'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	var jpg bytes.Buffer
	jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 3, 3)), nil)
	os.MkdirAll(filepath.Join(root, "authors"), 0755)
	if err := os.WriteFile(filepath.Join(root, "authors", "auth-2.jpg"), jpg.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE catalog_items SET author='Brandon Sanderson, Imported Writer',document=json_set(document,'$.authors',json('[{"id":"auth-1","name":"Brandon Sanderson"},{"id":"auth-2","name":"Imported Writer"}]')) WHERE id='book'`,
		`INSERT INTO catalog_entities VALUES('authors','auth-1',NULL,'{"id":"auth-1","name":"Brandon Sanderson"}')`,
		`INSERT INTO catalog_entities VALUES('authors','auth-2','lib','{"id":"auth-2","name":"Imported Writer","description":"From ABS","imagePath":"/media/authors/auth-2.jpg","updatedAt":1700000000000}')`,
	} {
		if _, err := s.store.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var pic bytes.Buffer
	png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 5, 5)))
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "metadata", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	calls := &atomic.Int32{}
	s.nativeFetchHTTP = &http.Client{Transport: gatewayTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		ok := func(body string) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
		}
		switch {
		case r.URL.Host == "api.audnex.us" && r.URL.Path == "/authors":
			if r.URL.Query().Get("region") != "us" {
				t.Errorf("region %v", r.URL.Query())
			}
			if strings.Contains(r.URL.Query().Get("name"), "Sanderson") {
				return ok(read("audnexus_author_search.json"))
			}
			return ok(`[]`)
		case r.URL.Host == "api.audnex.us" && r.URL.Path == "/authors/B001IGFHW6":
			return ok(read("audnexus_author.json"))
		case r.URL.Host == "api.audnex.us" && strings.HasPrefix(r.URL.Path, "/authors/"):
			return ok(read("audnexus_author_empty.json"))
		case r.URL.Host == "images-na.ssl-images-amazon.com":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pic.Bytes())), Header: http.Header{"Content-Type": {"image/png"}}}, nil
		}
		return nil, fmt.Errorf("unexpected request %s", r.URL)
	})}
	return s, h, a, b, calls
}

func uploadPhoto(t *testing.T, h http.Handler, p person, path string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "photo")
	fw.Write(data)
	mw.Close()
	r := httptest.NewRequest("POST", path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(p.cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAuthorProfileMatchEditAndImage(t *testing.T) {
	s, h, a, b, _ := authorHarness(t)
	// Visibility follows the books: Bob cannot see the library.
	if w := call(t, h, b, "GET", "/api/v1/catalog/authors/auth-1", ""); w.Code != 404 {
		t.Fatal("hidden author", w.Code)
	}
	if w := call(t, h, b, "GET", "/api/v1/catalog/authors/auth-2/image", ""); w.Code != 404 {
		t.Fatal("hidden photo", w.Code)
	}
	if w := call(t, h, b, "POST", "/api/v1/catalog/authors/auth-1/match", `{}`); w.Code != 403 {
		t.Fatal("unprivileged match", w.Code)
	}
	got := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/authors/auth-1", ""))
	if got["name"] != "Brandon Sanderson" || got["image"] != "" || got["books"] != float64(1) || got["can_edit"] != true {
		t.Fatal(got)
	}

	// Imported Audiobookshelf profiles keep working, in the web and ABS-compatible APIs.
	imported := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/authors/auth-2", ""))
	if imported["description"] != "From ABS" || !strings.HasPrefix(nativeString(imported["image"]), "/api/v1/catalog/authors/auth-2/image?v=1700000000000") {
		t.Fatal(imported)
	}
	w := call(t, h, a, "GET", nativeString(imported["image"]), "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("imported photo", w.Code, w.Header())
	}
	web := jsonObject(t, call(t, h, a, "GET", "/api/v1/authors/auth-2", ""))
	if web["description"] != "From ABS" || !strings.Contains(nativeString(nativeMap(web["author"])["image"]), "v=1700000000000") {
		t.Fatal("web author", web)
	}

	// Match by name: closest Audnexus author, plain-text bio, photo stored as native art.
	res := jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/authors/auth-1/match", `{}`))
	r := nativeMap(res["result"])
	if r["status"] != "matched" || r["asin"] != "B001IGFHW6" || r["image"] != true {
		t.Fatal(res)
	}
	author := nativeMap(res["author"])
	desc := nativeString(author["description"])
	if author["asin"] != "B001IGFHW6" || !strings.HasPrefix(desc, "I’m Brandon Sanderson") || strings.Contains(desc, "<br") || author["last_matched"] == float64(0) {
		t.Fatal(author)
	}
	img := call(t, h, a, "GET", nativeString(author["image"]), "")
	if img.Code != 200 || img.Header().Get("Content-Type") != "image/png" || img.Header().Get("ETag") == "" {
		t.Fatal("matched photo", img.Code, img.Header())
	}
	req := httptest.NewRequest("GET", nativeString(author["image"]), nil)
	req.AddCookie(a.cookie)
	req.Header.Set("If-None-Match", img.Header().Get("ETag"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Fatal("conditional photo", rec.Code)
	}
	// The ABS-compatible API (iOS apps) shows the same profile.
	tok, _, _ := s.store.CreateAPIToken(context.Background(), a.u.ID, "abs app")
	abs := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/authors/auth-1", ""))
	if abs["asin"] != "B001IGFHW6" || !strings.HasPrefix(nativeString(abs["imagePath"]), "/pocket-native-art/") || nativeString(abs["description"]) != desc {
		t.Fatal("abs author", abs)
	}
	if w := nativeCall(t, h, tok, "GET", "/api/authors/auth-1/image", ""); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatal("abs photo", w.Code)
	}

	// Explicit ASIN: an unknown one is reported, nothing changes.
	if res = jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/authors/auth-1/match", `{"asin":"B000000000"}`)); nativeMap(res["result"])["status"] != "not_found" {
		t.Fatal(res)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/authors/auth-1/match", `{"asin":"nope"}`); w.Code != 400 {
		t.Fatal("bad asin", w.Code)
	}

	// Edit: rename propagates to the book; description and ASIN validated.
	if w := call(t, h, a, "PUT", "/api/v1/catalog/authors/auth-1", `{"name":"Imported Writer"}`); w.Code != 409 {
		t.Fatal("duplicate name", w.Code)
	}
	if w := call(t, h, a, "PUT", "/api/v1/catalog/authors/auth-1", `{"asin":"bad"}`); w.Code != 400 {
		t.Fatal("asin validation", w.Code)
	}
	edited := jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/authors/auth-1", `{"name":"B. Sanderson","description":"Short bio"}`))
	if edited["name"] != "B. Sanderson" || edited["description"] != "Short bio" || edited["asin"] != "B001IGFHW6" {
		t.Fatal(edited)
	}
	it, _ := s.catalog().Item(context.Background(), a.u.ID, "book")
	if it.Author != "B. Sanderson, Imported Writer" || !strings.Contains(string(it.Document), `"B. Sanderson"`) {
		t.Fatal("rename", it.Author, string(it.Document))
	}

	// Upload validates the image; remove clears it.
	if w := uploadPhoto(t, h, a, "/api/v1/catalog/authors/auth-1/image", []byte("not an image")); w.Code != 400 {
		t.Fatal("invalid upload", w.Code)
	}
	webp := []byte("RIFF\x24\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x07\x00\x00\x03\x00\x00")
	up := jsonObject(t, uploadPhoto(t, h, a, "/api/v1/catalog/authors/auth-1/image", webp))
	if up["has_image"] != true {
		t.Fatal(up)
	}
	if w := call(t, h, a, "GET", nativeString(up["image"]), ""); w.Header().Get("Content-Type") != "image/webp" {
		t.Fatal("webp", w.Header())
	}
	if removed := jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/authors/auth-1", `{"remove_image":true}`)); removed["image"] != "" {
		t.Fatal(removed)
	}
}

func TestAuthorMatchJob(t *testing.T) {
	_, h, a, b, calls := authorHarness(t)
	if w := call(t, h, b, "POST", "/api/v1/catalog/author-match-jobs", `{"library_id":"lib"}`); w.Code != 403 {
		t.Fatal("unprivileged job", w.Code)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/author-match-jobs", `{"library_id":"missing"}`); w.Code != 404 {
		t.Fatal("missing library", w.Code)
	}
	// Only auth-1 lacks a photo or bio.
	start := accepted(t, call(t, h, a, "POST", "/api/v1/catalog/author-match-jobs", `{"library_id":"lib","only_missing":true}`))
	if start["total"] != float64(1) {
		t.Fatal(start)
	}
	id := nativeString(start["id"])
	job := nativeObject{}
	waitFor(t, "author job", func() bool {
		job = jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/author-match-jobs/"+id, ""))
		return nativeMap(job["job"])["status"] == "complete"
	})
	if counts := nativeMap(nativeMap(job["job"])["counts"]); counts["matched"] != float64(1) {
		t.Fatal(job)
	}
	if res := nativeMap(nativeArray(job["results"])[0]); res["author_id"] != "auth-1" || res["name"] != "Brandon Sanderson" {
		t.Fatal(res)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/author-match-jobs", `{"library_id":"lib"}`); w.Code != 400 {
		t.Fatal("nothing missing", w.Code)
	}
	// All authors: the imported one is not found and keeps its bio and photo.
	before := calls.Load()
	id = nativeString(accepted(t, call(t, h, a, "POST", "/api/v1/catalog/author-match-jobs", `{"library_id":"lib","only_missing":false}`))["id"])
	waitFor(t, "author job", func() bool {
		job = jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/author-match-jobs/"+id, ""))
		return nativeMap(job["job"])["status"] == "complete"
	})
	if counts := nativeMap(nativeMap(job["job"])["counts"]); counts["not_found"] != float64(1) || nativeMap(nativeMap(job["job"])["options"])["only_missing"] != false || calls.Load() == before {
		t.Fatal(job)
	}
	if got := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/authors/auth-2", "")); got["description"] != "From ABS" {
		t.Fatal(got)
	}
	// Author jobs have their own list; book quick-match lists leave them out.
	if list := call(t, h, a, "GET", "/api/v1/catalog/author-match-jobs?library=lib", ""); !strings.Contains(list.Body.String(), id) {
		t.Fatal(list.Body.String())
	}
	if list := call(t, h, a, "GET", "/api/v1/catalog/match-jobs", ""); strings.Contains(list.Body.String(), id) {
		t.Fatal("author job in book list")
	}
	if w := call(t, h, a, "GET", "/api/v1/catalog/match-jobs/"+id, ""); w.Code != 404 {
		t.Fatal("author job via book endpoint", w.Code)
	}
	if w := call(t, h, a, "DELETE", "/api/v1/catalog/author-match-jobs/"+id, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
