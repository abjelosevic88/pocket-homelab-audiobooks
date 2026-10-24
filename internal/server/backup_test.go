package server

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/backup"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

func TestApplicationBundleRestoresTokensSecretsNativeProgressAndCovers(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	ctx := context.Background()
	if _, err := s.store.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	a.u.Role = "admin"
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=?`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	// The new key must still decrypt previously sealed provider/account settings.
	sealed, err := s.seal("private-test-value")
	if err != nil {
		t.Fatal(err)
	}
	image, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jHxoAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.nativeSaveCover(ctx, a.u.ID, "book", image); err != nil {
		t.Fatal(err)
	}
	info := jsonObject(t, call(t, h, a, "POST", "/api/v1/play/book", `{"client":"backup-test","mime_types":["audio/mpeg"]}`))
	sid := nativeString(info["session_id"])
	jsonObject(t, call(t, h, a, "POST", "/api/v1/sessions/"+sid+"/sync", `{"report_id":"backup-progress","current_time":42,"listened":12,"playing":false}`))
	if w := call(t, h, b, "GET", "/api/v1/backup?format=bundle", ""); w.Code != 403 {
		t.Fatal("member downloaded all credentials", w.Code)
	}
	w := call(t, h, a, "GET", "/api/v1/backup?format=bundle", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err = os.WriteFile(archive, w.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if _, err = backup.Restore(ctx, archive, dest); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(dest, "pocket-audiobooks.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := testConfig(dest)
	cfg.Backend = "native"
	restored := New(cfg, st, log, nil)
	if err = restored.loadSecretKey(); err != nil {
		t.Fatal(err)
	}
	plain, err := restored.open(sealed)
	if err != nil || plain != "private-test-value" {
		t.Fatal("lost encryption key", err)
	}
	rh := restored.Router()
	book := jsonObject(t, nativeCall(t, rh, tok, "GET", "/api/items/book", ""))
	if book["id"] != "book" {
		t.Fatal("lost native identity")
	}
	me := jsonObject(t, nativeCall(t, rh, tok, "GET", "/api/me", ""))
	found := false
	for _, v := range nativeArray(me["mediaProgress"]) {
		p := nativeMap(v)
		if p["libraryItemId"] == "book" && nativeNumber(p["currentTime"]) == 42 {
			found = true
		}
	}
	if !found {
		t.Fatal("lost playback progress", me)
	}
	cover := nativeCall(t, rh, tok, "GET", "/api/items/book/cover", "")
	if cover.Code != 200 || len(cover.Body.Bytes()) != len(image) {
		t.Fatal("managed cover missing", cover.Code)
	}
	// The raw database endpoint remains available for existing backup jobs.
	if w = call(t, h, a, "GET", "/api/v1/backup", ""); w.Code != 200 || w.Header().Get("Content-Type") != "application/vnd.sqlite3" {
		t.Fatal("database backup regression", w.Code)
	}
}
