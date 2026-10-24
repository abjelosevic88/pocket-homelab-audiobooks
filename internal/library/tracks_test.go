package library_test

import (
	"context"
	"errors"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscGroupingTrackOrderAndPersistentManualLayout(t *testing.T) {
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
	ctx := context.Background()
	c := f.catalog
	c.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=2`)
	root := t.TempDir()
	for _, v := range []struct{ disc, name, track string }{{"CD 01", "z.mp3", "1/2"}, {"CD 01", "a.mp3", "2/2"}, {"Disc 02", "b.mp3", "1/1"}} {
		dir := filepath.Join(root, "Disc story", v.disc)
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if out, e := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "2", "-metadata", "album=Disc story", "-metadata", "track="+v.track, filepath.Join(dir, v.name)).CombinedOutput(); e != nil {
			t.Fatal(e, string(out))
		}
	}
	id, err := c.CreateLibrary(ctx, 2, library.NewLibrary{Name: "Discs", Path: root, MediaType: "book"})
	if err != nil {
		t.Fatal(err)
	}
	if report, e := c.Scan(ctx, 2, id, library.ScanOptions{FFprobe: ffprobe}); e != nil || report.Added != 1 {
		t.Fatal("disc grouping", report, e)
	}
	page, _ := c.Items(ctx, 2, library.Query{LibraryID: id})
	if len(page.Items) != 1 {
		t.Fatal(page)
	}
	bookID := page.Items[0].ID
	tracks, _, _, err := c.Timeline(ctx, 2, bookID, "")
	if err != nil || len(tracks) != 3 || tracks[0].Title != "z.mp3" || tracks[1].Title != "a.mp3" || tracks[2].Title != "b.mp3" {
		t.Fatal("disc/tag ordering", tracks, err)
	}
	c.SetProgress(ctx, 2, bookID, "", map[string]any{"currentTime": 0.5})
	c.Bookmark(ctx, 2, bookID, 0.5, "Keep this bookmark", false)
	oldSession, err := c.OpenSession(ctx, 2, bookID, "", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	book, _ := c.Item(ctx, 2, bookID)
	in := library.TrackEdit{Expected: book.UpdatedAt, Tracks: []library.TrackChoice{{ID: tracks[2].ID}, {ID: tracks[0].ID}, {ID: tracks[1].ID, Excluded: true}}}
	if err = c.EditTracks(ctx, 2, bookID, in); !errors.Is(err, library.ErrConflict) {
		t.Fatal("did not protect existing chapter boundaries", err)
	}
	in.ResetChapters = true
	if err = c.EditTracks(ctx, 2, bookID, in); err != nil {
		t.Fatal(err)
	}
	if err = c.EditTracks(ctx, 2, bookID, in); !errors.Is(err, library.ErrConflict) {
		t.Fatal("stale track edit", err)
	}
	oldSession, err = c.Session(ctx, 2, oldSession.ID)
	if err != nil || !oldSession.Closed {
		t.Fatal("stale session active", oldSession, err)
	}
	now, chapters, duration, err := c.Timeline(ctx, 2, bookID, "")
	if err != nil || len(now) != 2 || now[0].ID != tracks[2].ID || len(chapters) != 2 || duration < 4 || duration > 5 {
		t.Fatal(now, chapters, duration, err)
	}
	if _, err = c.Scan(ctx, 2, id, library.ScanOptions{FFprobe: ffprobe, Force: true}); err != nil {
		t.Fatal(err)
	}
	after, _, _, err := c.Timeline(ctx, 2, bookID, "")
	if err != nil || len(after) != 2 || after[0].ID != now[0].ID {
		t.Fatal("rescan undid manual layout", after, err)
	}
	var position float64
	c.DB.QueryRow(`SELECT position FROM catalog_progress WHERE item_id=?`, bookID).Scan(&position)
	if position != 0.5 {
		t.Fatal("progress changed", position)
	}
	var count int
	c.DB.QueryRow(`SELECT count(*) FROM catalog_bookmarks WHERE item_id=? AND position=0.5`, bookID).Scan(&count)
	if count != 1 {
		t.Fatal("bookmark lost")
	}
}
