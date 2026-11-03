package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A link token only reaches the routes its scopes name: library:read finds
// books (and their covers), library:scan starts scans, neither opens anything else.
func TestLinkScopesGateFindAndScan(t *testing.T) {
	for _, c := range []struct {
		method, path string
		scopes       []string
		ok           bool
	}{
		{"GET", "/api/v1/link/find", []string{"library:read"}, true},
		{"GET", "/api/v1/link/find", []string{"listening:read"}, false},
		{"GET", "/api/v1/link/find", []string{"library:scan"}, false},
		{"POST", "/api/v1/link/scan", []string{"library:scan"}, true},
		{"POST", "/api/v1/link/scan", []string{"library:read"}, false},
		{"GET", "/api/v1/link/scan", []string{"library:scan"}, false},
		{"GET", "/api/v1/cover/abc-1", []string{"library:read"}, true},
		{"GET", "/api/v1/catalog/items", []string{"library:read", "library:scan"}, false},
		{"POST", "/api/v1/catalog/libraries/lib/scan", []string{"library:scan"}, false},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := linkAllows(c.scopes, r); got != c.ok {
			t.Errorf("%s %s with %v: %v, want %v", c.method, c.path, c.scopes, got, c.ok)
		}
	}
}

func TestLinkFindMatchesTitleAndAuthor(t *testing.T) {
	s, h, a, b, _ := nativeHarness(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE catalog_items SET title='Project Hail Mary',author='Andy Weir',document=json_set(document,'$.media.title','Project Hail Mary','$.media.narrators',json('["Ray Porter"]'),'$.authors',json('[{"id":"w","name":"Andy Weir"}]'),'$.series',json('[{"id":"s","name":"Standalone","sequence":"1"}]')) WHERE id='book'`)
	exec(`INSERT INTO catalog_items SELECT 'zafon',library_id,'zafon-media',media_type,'Senka vetra','Carlos Ruiz Zafón',50,0,0,2,2,'[]',json_set(document,'$.media.title','Senka vetra','$.authors',json('[{"id":"z","name":"Carlos Ruiz Zafón"}]')) FROM catalog_items WHERE id='book'`)
	find := func(p person, q string) []any {
		t.Helper()
		w := call(t, h, p, "GET", "/api/v1/link/find?"+q, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body.String())
		}
		return nativeArray(jsonObject(t, w)["items"])
	}
	got := find(a, "title=project+hail+mary+(unabridged)&author=Weir+Andy")
	if len(got) != 1 {
		t.Fatalf("title+author: %v", got)
	}
	it := nativeMap(got[0])
	if it["id"] != "book" || it["url_path"] != "/book/book" || it["cover"] != "/api/v1/cover/book" || it["series"] != "Standalone" || nativeArray(it["narrators"])[0] != "Ray Porter" {
		t.Fatal(it)
	}
	if got := find(a, "title=Senka+vetra&author=Carlos+Ruiz+Zafon"); len(got) != 1 || nativeMap(got[0])["id"] != "zafon" {
		t.Fatal("accents should fold", got)
	}
	if got := find(a, "title=Project+Hail+Mary&author=Brandon+Sanderson"); len(got) != 0 {
		t.Fatal("wrong author matched", got)
	}
	if got := find(a, "title=Mary&author=Andy+Weir"); len(got) != 0 {
		t.Fatal("one title word is not the book", got)
	}
	if got := find(a, "q=hail+weir"); len(got) != 1 {
		t.Fatal("free text", got)
	}
	// bob may not open the library: nothing, not an error
	if got := find(b, "q=hail"); len(got) != 0 {
		t.Fatal("bob sees alice's library", got)
	}
	if w := call(t, h, a, "GET", "/api/v1/link/find", ""); w.Code != 400 {
		t.Fatal("empty query", w.Code)
	}
}

// The folder that holds a path wins on a folder boundary: a book under
// /x/audiobooks-archive is not in the library of /x/audiobooks.
func TestLinkScanFindsLibraryByFolder(t *testing.T) {
	s, h, a, b, _ := nativeHarness(t)
	base := t.TempDir()
	books, archive := filepath.Join(base, "audiobooks"), filepath.Join(base, "audiobooks-archive")
	for _, d := range []string{filepath.Join(books, "Andy Weir", "Project Hail Mary"), filepath.Join(archive, "Old")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO catalog_roots VALUES('r-books','/audiobooks',?)`, books)
	exec(`INSERT INTO catalog_roots VALUES('r-archive','/audiobooks-archive',?)`, archive)
	exec(`INSERT INTO catalog_libraries VALUES('main','Audiobooks','book',1,'{}')`)
	exec(`INSERT INTO catalog_libraries VALUES('ga','Archive','book',2,'{}')`)
	exec(`INSERT INTO catalog_libraries VALUES('pods','Podcasts','podcast',3,'{}')`)
	exec(`INSERT INTO catalog_entities VALUES('libraryFolders','f-main','main','{"id":"f-main","path":"/audiobooks"}')`)
	exec(`INSERT INTO catalog_entities VALUES('libraryFolders','f-ga','ga','{"id":"f-ga","path":"/audiobooks-archive"}')`)
	exec(`UPDATE catalog_accounts SET permissions='{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"update":true}' WHERE id='alice'`)
	ctx := context.Background()
	for path, want := range map[string]string{
		filepath.Join(books, "Andy Weir", "Project Hail Mary"): "main",
		books:                         "main",
		filepath.Join(archive, "Old"): "ga",
		"/audiobooks/Andy Weir/Project Hail Mary": "main", // as the folder is written
		"/audiobooks-archive/Old":                 "ga",
		filepath.Join(base, "audiobooks-other"):   "",
		filepath.Join(books, "..", "elsewhere"):   "",
	} {
		got, _ := s.catalog().LibraryForPath(ctx, a.u.ID, path)
		if got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
	scan := func(p person, path string) (int, map[string]any) {
		t.Helper()
		w := call(t, h, p, "POST", "/api/v1/link/scan", `{"path":"`+path+`"}`)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := scan(a, "relative/path"); code != 400 {
		t.Fatal("relative path", code)
	}
	if code, _ := scan(a, filepath.Join(base, "nowhere")); code != 404 {
		t.Fatal("outside every library", code)
	}
	if code, _ := scan(b, filepath.Join(books, "Andy Weir")); code != 404 {
		t.Fatal("bob may not open the library", code)
	}
	code, out := scan(a, filepath.Join(books, "Andy Weir", "Project Hail Mary"))
	if code != 202 || out["library_id"] != "main" || out["started"] != true {
		t.Fatal(code, out)
	}
	waitScans(t, s)
	// while a scan runs the ask waits and the scheduler starts it afterwards
	exec(`INSERT INTO catalog_scan_runs(id,library_id,started_at,finished_at,status,report) VALUES('busy','ga',1,NULL,'running','{}')`)
	if code, out = scan(a, filepath.Join(books, "Andy Weir")); code != 202 || out["started"] != false {
		t.Fatal("busy", code, out)
	}
	if got := s.runLinkScans(ctx); len(got) != 0 {
		t.Fatal("started while another scan runs", got)
	}
	exec(`UPDATE catalog_scan_runs SET status='complete',finished_at=2 WHERE id='busy'`)
	if got := s.runLinkScans(ctx); len(got) != 1 {
		t.Fatal("queued scan did not start", got)
	}
	waitScans(t, s)
	if len(s.linkScans) != 0 {
		t.Fatal("queue not emptied", s.linkScans)
	}
}

func waitScans(t *testing.T, s *Server) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if !s.anyScanRunning(context.Background()) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("scan did not finish")
}
