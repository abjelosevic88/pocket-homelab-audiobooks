package library_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestNativeScannerIncrementalMovesAndAtomicFailure(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	apply(t, f)
	c := f.catalog
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "New book")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	execSQL := func(q string, args ...any) {
		t.Helper()
		if _, err := c.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL(`INSERT INTO catalog_roots VALUES('scanroot','/scan',?)`, root)
	execSQL(`INSERT INTO catalog_libraries VALUES('scanlib','Scanned','book',1,'{}')`)
	execSQL(`INSERT INTO catalog_entities VALUES('libraryFolders','scanfolder','scanlib','{"id":"scanfolder","path":"/scan"}')`)
	execSQL(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=2`)
	for _, name := range []string{"track10.mp3", "track2.mp3"} {
		cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "1", "-metadata", "album=Scanned title", "-metadata", "artist=Test Author", filepath.Join(dir, name))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("audio fixture: %v %s", err, out)
		}
	}
	scan := func() library.ScanReport {
		t.Helper()
		r, err := c.Scan(ctx, 2, "scanlib", library.ScanOptions{FFprobe: ffprobe})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := scan()
	if r.Added != 1 || r.Probed != 2 {
		t.Fatalf("initial: %+v", r)
	}
	page, err := c.Items(ctx, 2, library.Query{LibraryID: "scanlib"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("catalog %+v %v", page, err)
	}
	id := page.Items[0].ID
	if page.Items[0].Title != "Scanned title" || page.Items[0].Author != "Test Author" {
		t.Fatalf("tags %+v", page.Items[0])
	}
	tracks, _, duration, err := c.Timeline(ctx, 2, id, "")
	if err != nil || len(tracks) != 2 || tracks[0].Title != "track2.mp3" || duration < 2 {
		t.Fatalf("timeline %+v %v %f", tracks, err, duration)
	}
	originalID := tracks[0].ID
	r = scan()
	if r.Added != 0 || r.Probed != 0 {
		t.Fatalf("incremental scan reprobed: %+v", r)
	}
	// Moving a folder on the same filesystem retains item and file IDs.
	moved := filepath.Join(root, "Moved book")
	if err = os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	r = scan()
	if r.Added != 0 || r.Probed != 0 {
		t.Fatalf("move %+v", r)
	}
	it, err := c.Item(ctx, 2, id)
	if err != nil || it.Missing {
		t.Fatalf("move lost item %v", err)
	}
	var doc map[string]any
	if err = json.Unmarshal(it.Document, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["item"].(map[string]any)["path"] != "/scan/Moved book" {
		t.Fatal("move left stale directory", string(it.Document))
	}
	tracks, _, _, err = c.Timeline(ctx, 2, id, "")
	if err != nil || tracks[0].ID != originalID {
		t.Fatalf("move changed identity %+v %v", tracks, err)
	}
	// A broken newly arrived file quarantines only its book: that book's record
	// is untouched, a broken new book is not created, and healthy books commit.
	if err = os.WriteFile(filepath.Join(moved, "bad.mp3"), []byte("not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	brokenNew := filepath.Join(root, "Broken new book")
	healthy := filepath.Join(root, "Healthy new book")
	for _, d := range []string{brokenNew, healthy} {
		if err = os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(brokenNew, "bad.mp3"), []byte("not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "1", filepath.Join(healthy, "one.mp3")).CombinedOutput(); err != nil {
		t.Fatalf("audio fixture: %v %s", err, out)
	}
	r = scan()
	if r.Added != 1 || r.Quarantined != 2 || len(r.Failures) != 2 {
		t.Fatalf("quarantine: %+v", r)
	}
	again, _ := c.Item(ctx, 2, id)
	if string(again.Document) != string(it.Document) {
		t.Fatal("quarantined book mutated")
	}
	var runReport string
	if err = c.DB.QueryRow(`SELECT report FROM catalog_scan_runs WHERE id=? AND status='complete'`, r.ID).Scan(&runReport); err != nil || !strings.Contains(runReport, `"quarantined":2`) || !strings.Contains(runReport, "Broken new book") {
		t.Fatalf("scan run report %s %v", runReport, err)
	}
	if err = os.RemoveAll(brokenNew); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(moved, "bad.mp3")); err != nil {
		t.Fatal(err)
	}
	// A missing track is retained, not silently removed from the timeline.
	if err = os.Remove(filepath.Join(moved, "track2.mp3")); err != nil {
		t.Fatal(err)
	}
	r = scan()
	if r.Missing != 1 {
		t.Fatalf("missing %+v", r)
	}
	it, err = c.Item(ctx, 2, id)
	if err != nil || !it.Missing || it.Duration != duration {
		t.Fatalf("partial disappearance changed book %+v %v", it, err)
	}
	if _, err = c.Scan(ctx, 1, "scanlib", library.ScanOptions{FFprobe: ffprobe}); !errors.Is(err, library.ErrNotFound) {
		t.Fatalf("restricted scanner %v", err)
	}
}
