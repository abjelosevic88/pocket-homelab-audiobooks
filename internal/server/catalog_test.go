package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCatalogHTTPWithoutUpstream(t *testing.T) {
	s, h, _ := testServer(t)
	// Any accidental upstream API/stream request makes this test fail.
	never := gatewayTransport(func(*http.Request) (*http.Response, error) {
		t.Error("native catalog attempted an upstream request")
		return nil, io.ErrUnexpectedEOF
	})
	s.absHTTP = &http.Client{Transport: never}
	s.streamHTTP = &http.Client{Transport: never}
	a := addPerson(t, s, "alice@example.com", "keyA")
	b := addPerson(t, s, "bob@example.com", "keyB")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "book.mp3"), []byte("0123456789audio"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO catalog_roots VALUES('root','/media',?)`, root)
	exec(`INSERT INTO catalog_libraries VALUES('lib','Books','book',0,'{}')`)
	exec(`INSERT INTO catalog_accounts VALUES('alice',?,'Alice',1,0,'{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"download":true}')`, a.u.ID)
	exec(`INSERT INTO catalog_accounts VALUES('bob',?,'Bob',1,0,'{"accessAllLibraries":false,"librariesAccessible":[],"accessAllTags":true}')`, b.u.ID)
	exec(`INSERT INTO catalog_items VALUES('book','lib','media','book','A book','An author',100,0,0,1,1,'[]','{"chapters":[]}')`)
	exec(`INSERT INTO catalog_files VALUES('book','file','audio','root','book.mp3','book.mp3','audio/mpeg',14,100,1,0,'{}')`)
	for _, path := range []string{"/api/v1/catalog/libraries", "/api/v1/catalog/items", "/api/v1/catalog/items/book", "/api/v1/catalog/me/progress"} {
		w := call(t, h, a, "GET", path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("private response cache policy missing")
		}
	}
	for _, path := range []string{"/api/v1/catalog/items/book", "/api/v1/catalog/items/book/files/file"} {
		if w := call(t, h, b, "GET", path, ""); w.Code != 404 {
			t.Fatalf("restricted path %s: %d", path, w.Code)
		}
	}
	request := func(method, path, rangeValue string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if authenticated {
			r.AddCookie(a.cookie)
		}
		r.Header.Set("Range", rangeValue)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/catalog/items/book/files/file"
	w := request("GET", path, "bytes=2-5", true)
	if w.Code != 206 || w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/15" {
		t.Fatalf("range: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if w = request("HEAD", path, "", true); w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "15" {
		t.Fatalf("HEAD: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if w = request("GET", path, "bytes=100-200", true); w.Code != 416 {
		t.Fatalf("invalid range %d", w.Code)
	}
	if w = request("GET", path, "", false); w.Code != 401 {
		t.Fatalf("anonymous %d", w.Code)
	}
	if w = request("GET", path+"?download=1", "", true); !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("download not attached")
	}
	// Tokens accepted by Pocket's regular auth also work here; upstream keys don't.
	tok, _, err := s.store.CreateAPIToken(ctx, a.u.ID, "native catalog test")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("token stream: %d", w.Code)
	}
	r = httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer upstream-key")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("upstream key accepted: %d", w.Code)
	}
}
