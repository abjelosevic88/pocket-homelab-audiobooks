package library_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestTidyPreviewApplyAndHandEdits(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := c.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=2`)
	exec(`INSERT INTO catalog_entities VALUES('libraryFolders','folder-public','public','{"id":"folder-public","path":"/media","libraryId":"public"}')`)
	book := func(id, title, path string) {
		exec(`UPDATE catalog_items SET title=?,document=json_set(document,'$.media.title',?,'$.item.path',?) WHERE id=?`, title, title, path, id)
	}
	book("visible", "01a.Originally Human", "/media/Eileen Wilks/World of the Lupi/01a.Originally Human")
	book("explicit", "Vol 2 - The Atlantis Plague", "/media/A.G. Riddle/The Origin Mystery/Vol 2 - The Atlantis Plague")
	book("tagged", "Countdown", "/media/DC Comics/Countdown (2009)")

	// A person sets a placeholder-looking title on purpose: tidy-up keeps it.
	tagged, err := c.Item(ctx, 2, "tagged")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Edit(ctx, 2, "tagged", library.EditOptions{Expected: tagged.UpdatedAt, Metadata: map[string]any{"title": "[01]"}}); err != nil {
		t.Fatal(err)
	}
	// Saving the editor without changing the title is not a title edit.
	visible, _ := c.Item(ctx, 2, "visible")
	if err = c.Edit(ctx, 2, "visible", library.EditOptions{Expected: visible.UpdatedAt, Metadata: map[string]any{"title": visible.Title, "description": "New blurb"}}); err != nil {
		t.Fatal(err)
	}
	var edits int
	if err = c.DB.QueryRow(`SELECT count(*) FROM catalog_item_edits`).Scan(&edits); err != nil || edits != 1 {
		t.Fatal("hand edits recorded", edits, err)
	}

	report, err := c.TidyPreview(ctx, 2, "public")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]library.TidyChange{}
	for _, ch := range report.Changes {
		byID[ch.ID] = ch
	}
	if _, ok := byID["tagged"]; ok {
		t.Fatal("hand-edited title proposed", byID["tagged"])
	}
	if ch := byID["visible"]; len(ch.Changes) != 2 || ch.Changes[0].To != "Originally Human" || ch.Changes[1].To != "World of the Lupi" {
		t.Fatalf("visible: %+v", ch)
	}
	if ch := byID["explicit"]; len(ch.Changes) != 2 || ch.Changes[0].To != "The Atlantis Plague" || ch.Changes[1].To != "The Origin Mystery #2" {
		t.Fatalf("explicit: %+v", ch)
	}
	if _, err = c.TidyPreview(ctx, 1, "public"); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("tidy-up without edit permission", err)
	}

	// A stale revision fails the whole apply.
	picked := []library.TidySelection{{ID: "visible", Expected: byID["visible"].Expected}, {ID: "explicit", Expected: byID["explicit"].Expected - 1}}
	if _, err = c.TidyApply(ctx, 2, "public", picked); !errors.Is(err, library.ErrConflict) {
		t.Fatal(err)
	}
	if it, _ := c.Item(ctx, 2, "visible"); it.Title != "01a.Originally Human" {
		t.Fatal("partial apply", it.Title)
	}
	picked[1].Expected++
	n, err := c.TidyApply(ctx, 2, "public", picked)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if it, _ := c.Item(ctx, 2, "explicit"); it.Title != "The Atlantis Plague" {
		t.Fatal(it.Title)
	}
	// Tidy-up's own changes are not hand edits, and nothing is left to do.
	if err = c.DB.QueryRow(`SELECT count(*) FROM catalog_item_edits`).Scan(&edits); err != nil || edits != 1 {
		t.Fatal("tidy-up recorded hand edits", edits, err)
	}
	report, err = c.TidyPreview(ctx, 2, "public")
	if err != nil || len(report.Changes) != 0 {
		t.Fatalf("second preview: %+v %v", report.Changes, err)
	}
}

func TestMissingCoversSources(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := c.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=2`)
	exec(`INSERT INTO catalog_entities VALUES('libraryFolders','folder-public','public','{"id":"folder-public","path":"/media"}')`)
	// "visible" has a loose image (in a disc folder), "explicit" embedded art,
	// "tagged" nothing at all.
	if err := os.MkdirAll(filepath.Join(f.root, "Book", "CD 01"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"Book/CD 01/scan.jpg": "small", "Book/CD 01/Front Cover.png": "png-bytes", "Book/notes.txt": "x"} {
		if err := os.WriteFile(filepath.Join(f.root, filepath.FromSlash(name)), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE catalog_items SET document=json_set(document,'$.item.path','/media/Book','$.media.coverPath','') WHERE id='visible'`)
	exec(`UPDATE catalog_items SET document=json_set(document,'$.item.path','/media/Other/'||id,'$.media.coverPath','') WHERE id IN ('explicit','tagged')`)
	exec(`UPDATE catalog_files SET document=json_set(document,'$.embeddedCoverArt','mjpeg') WHERE item_id='explicit' AND role='audio'`)
	found, err := c.MissingCovers(ctx, 2, "public", "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]library.CoverCandidate{}
	for _, v := range found {
		got[v.ID] = v
	}
	if v := got["visible"]; v.Source != library.CoverFolder || v.Detail != "CD 01/Front Cover.png" {
		t.Fatalf("folder image: %+v", v)
	}
	if v := got["explicit"]; v.Source != library.CoverEmbedded {
		t.Fatalf("embedded: %+v", v)
	}
	if v, ok := got["tagged"]; !ok || v.Source != "" {
		t.Fatalf("no source: %+v", v)
	}
	data, err := c.LocalCover(ctx, 2, "visible", library.CoverFolder, "", "")
	if err != nil || string(data) != "png-bytes" {
		t.Fatal(string(data), err)
	}
	if _, err = c.LocalCover(ctx, 2, "tagged", library.CoverFolder, "", ""); !errors.Is(err, library.ErrNotFound) {
		t.Fatal(err)
	}
}
