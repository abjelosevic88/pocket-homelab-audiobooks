package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudiobooksOnlyRejectsPodcastAccessAndCreation(t *testing.T) {
	s, h, a, _, tok := nativeHarness(t)
	s.store.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, a.u.ID)
	s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.accessAllLibraries',json('true'),'$.update',json('true')) WHERE pocket_user_id=?`, a.u.ID)
	// Old/restored data must stay inaccessible even if it bypassed retirement.
	_, err := s.store.DB.Exec(`INSERT INTO catalog_libraries VALUES('pods','Podcasts','podcast',1,'{}');INSERT INTO catalog_items SELECT 'pod','pods','pm','podcast','Hidden podcast',author,duration,explicit,missing,added_at,updated_at,tags,document FROM catalog_items WHERE id='book';INSERT INTO catalog_files SELECT 'pod',id,role,root_id,relative_path,name,mime,size,duration,track_index,excluded,document FROM catalog_files WHERE item_id='book'`)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/podcasts", "/api/podcasts", "/api/podcasts/feed", "/api/search/podcast?q=test", "/api/items/pod", "/api/items/pod/file/11", "/api/libraries/pods/items", "/api/v1/catalog/items/pod", "/api/v1/catalog/items/pod/files/11"} {
		w := nativeCall(t, h, tok, "GET", path, "")
		if w.Code != 404 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := nativeCall(t, h, tok, "POST", "/api/items/pod/play", "{}"); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/libraries", "/api/v1/catalog/manage", "/api/v1/catalog/library-settings", "/api/v1/catalog/items"} {
		w := nativeCall(t, h, tok, "GET", path, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "Hidden podcast") || strings.Contains(w.Body.String(), `"id":"pods"`) {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	root := t.TempDir()
	body := string(nativeJSON(nativeObject{"name": "Podcasts", "path": root, "media_type": "podcast"}))
	if w := call(t, h, a, "POST", "/api/v1/catalog/libraries", body); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := s.catalog().Item(context.Background(), a.u.ID, "book"); err != nil {
		t.Fatal(err)
	}
	var mediaRoot string
	s.store.DB.QueryRow(`SELECT local_path FROM catalog_roots WHERE id='root'`).Scan(&mediaRoot)
	if _, err := os.Stat(filepath.Join(mediaRoot, "audio.mp3")); err != nil {
		t.Fatal("retirement touched media", err)
	}
}
