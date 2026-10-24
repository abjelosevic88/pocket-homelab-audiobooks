package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeLibraryManagementArchiveRestoreAndIsolation(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	ctx := context.Background()
	if w := call(t, h, a, "GET", "/api/v1/catalog/library-settings", ""); w.Code != 403 {
		t.Fatal("non-admin management", w.Code)
	}
	if _, err := s.store.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true'),'$.delete',json('true')) WHERE id='alice'`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	second := t.TempDir()
	id, err := s.catalog().CreateLibrary(ctx, a.u.ID, library.NewLibrary{Name: "Manage me", MediaType: "book", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := s.catalog().LibraryConfig(ctx, a.u.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Name = "Two folders"
	cfg.Order = 7
	cfg.Folders = append(cfg.Folders, library.LibraryFolder{Path: second})
	jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/libraries/"+id+"/settings", string(nativeJSON(cfg))))
	if w := call(t, h, a, "PUT", "/api/v1/catalog/libraries/"+id+"/settings", string(nativeJSON(cfg))); w.Code != 409 {
		t.Fatal("stale settings", w.Code)
	}
	cfg, err = s.catalog().LibraryConfig(ctx, a.u.ID, id)
	if err != nil || len(cfg.Folders) != 2 || cfg.Name != "Two folders" || cfg.Order != 7 {
		t.Fatal(cfg, err)
	}
	invalid := cfg
	invalid.Folders = append(append([]library.LibraryFolder{}, cfg.Folders...), library.LibraryFolder{Path: filepath.Join(root, "missing")})
	if err = s.catalog().SaveLibrary(ctx, a.u.ID, invalid); err == nil {
		t.Fatal("missing directory accepted")
	}
	check, _ := s.catalog().LibraryConfig(ctx, a.u.ID, id)
	if check.Revision != cfg.Revision || len(check.Folders) != 2 {
		t.Fatal("partial settings commit", check)
	}
	// Retain source IDs and bytes while hiding all routes into an archived library.
	if _, err = s.store.DB.Exec(`UPDATE catalog_items SET library_id=?,document=json_set(document,'$.item.libraryFolderId',?,'$.item.path',?) WHERE id='book'`, id, cfg.Folders[0].ID, filepath.Join(cfg.Folders[0].Path, "book")); err != nil {
		t.Fatal(err)
	}
	invalid = cfg
	invalid.Folders = append([]library.LibraryFolder{}, cfg.Folders[1:]...)
	if err = s.catalog().SaveLibrary(ctx, a.u.ID, invalid); !errors.Is(err, library.ErrConflict) {
		t.Fatal("nonempty folder removed", err)
	}
	if w := call(t, h, b, "PUT", "/api/v1/catalog/libraries/"+id+"/settings", string(nativeJSON(cfg))); w.Code != 403 {
		t.Fatal("member changed libraries", w.Code)
	}
	session := jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"forceDirectPlay":true}`))
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+nativeString(session["id"])+"/sync", `{"currentTime":2,"timeListened":2,"playing":true,"reportId":"archive-playing"}`))
	cfg.Archived = true
	if err = s.catalog().SaveLibrary(ctx, a.u.ID, cfg); !errors.Is(err, library.ErrConflict) {
		t.Fatal("archived active playback", err)
	}
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+nativeString(session["id"])+"/close", `{}`))
	jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/libraries/"+id+"/settings", string(nativeJSON(cfg))))
	for _, path := range []string{"/api/items/book", "/api/items/book/file/11", "/api/libraries/" + id} {
		if w := nativeCall(t, h, tok, "GET", path, ""); w.Code != 404 {
			t.Fatal("archived library still visible", path, w.Code)
		}
	}
	if err = s.catalog().CanScan(ctx, a.u.ID, id); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("archived scanning", err)
	}
	cfg, _ = s.catalog().LibraryConfig(ctx, a.u.ID, id)
	cfg.Archived = false
	jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/libraries/"+id+"/settings", string(nativeJSON(cfg))))
	if w := nativeCall(t, h, tok, "GET", "/api/items/book/file/11", ""); w.Code != 206 || w.Body.String() != "2345" {
		t.Fatal("restore bytes", w.Code, w.Body.String())
	}
	var pos float64
	if err = s.store.DB.QueryRow(`SELECT position FROM catalog_progress WHERE item_id='book' AND account_id='alice'`).Scan(&pos); err != nil || pos != 2 {
		t.Fatal("archive lost progress", pos, err)
	}
	jsonObject(t, nativeCall(t, h, tok, "PATCH", "/api/libraries/"+id, `{"name":"Client library"}`))
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/libraries/order", string(nativeJSON([]library.LibraryOrder{{ID: id, Order: 1}, {ID: "lib", Order: 2}}))))
	if w := nativeCall(t, h, tok, "POST", "/api/libraries/order", string(nativeJSON([]library.LibraryOrder{{ID: id, Order: 99}, {ID: "unknown", Order: 1}}))); w.Code != 404 {
		t.Fatal(w.Code)
	}
	cfg, _ = s.catalog().LibraryConfig(ctx, a.u.ID, id)
	if cfg.Order != 1 || cfg.Name != "Client library" {
		t.Fatal("partial reorder", cfg)
	}
	w := call(t, h, a, "GET", "/api/v1/catalog/library-settings", "")
	var list []library.LibraryConfig
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestNativeScanRejectsConcurrentLibrarySettingsChange(t *testing.T) {
	s, _, a, _, _ := nativeHarness(t)
	ctx := context.Background()
	s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE id='alice'`)
	root := t.TempDir()
	id, err := s.catalog().CreateLibrary(ctx, a.u.ID, library.NewLibrary{Name: "Scan", MediaType: "book", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	// Empty directories need no FFprobe. Change the config after the scanner's snapshot.
	_, err = s.catalog().Scan(ctx, a.u.ID, id, library.ScanOptions{Started: func(string) {
		if _, e := s.store.DB.Exec(`INSERT INTO catalog_library_state VALUES(?,1,0)`, id); e != nil {
			t.Fatal(e)
		}
	}})
	if !errors.Is(err, library.ErrConflict) {
		t.Fatal("stale scan committed", err)
	}
	if _, err = os.Stat(root); err != nil {
		t.Fatal(err)
	}
}
