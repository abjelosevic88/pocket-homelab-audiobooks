package library_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

type layoutFixture struct {
	t               *testing.T
	c               *library.Catalog
	ctx             context.Context
	root, lib       string
	ffmpeg, ffprobe string
	artDir          string
}

func newLayoutFixture(t *testing.T) *layoutFixture {
	t.Helper()
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
	if _, err = f.catalog.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true'),'$.upload',json('true')) WHERE pocket_user_id=2`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	lib, err := f.catalog.CreateLibrary(ctx, 2, library.NewLibrary{Name: "Layout", MediaType: "book", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	return &layoutFixture{t: t, c: f.catalog, ctx: ctx, root: root, lib: lib, ffmpeg: ffmpeg, ffprobe: ffprobe, artDir: filepath.Join(t.TempDir(), "art")}
}

// audio writes a one-second MP3 with the given tags ("key=value").
func (l *layoutFixture) audio(rel string, tags ...string) {
	l.t.Helper()
	path := filepath.Join(l.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		l.t.Fatal(err)
	}
	args := []string{"-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "1"}
	for _, tag := range tags {
		args = append(args, "-metadata", tag)
	}
	if out, err := exec.Command(l.ffmpeg, append(args, path)...).CombinedOutput(); err != nil {
		l.t.Fatalf("audio fixture %s: %v %s", rel, err, out)
	}
}

func (l *layoutFixture) file(rel, data string) {
	l.t.Helper()
	path := filepath.Join(l.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		l.t.Fatal(err)
	}
}

func (l *layoutFixture) scan(opt library.ScanOptions) library.ScanReport {
	l.t.Helper()
	opt.FFprobe = l.ffprobe
	r, err := l.c.Scan(l.ctx, 2, l.lib, opt)
	if err != nil {
		l.t.Fatal(err)
	}
	return r
}

type layoutBook struct {
	library.Item
	Path  string
	Media map[string]any
	Doc   map[string]any
}

// books returns every item keyed by its path relative to the library root.
func (l *layoutFixture) books() map[string]layoutBook {
	l.t.Helper()
	rows, err := l.c.DB.Query(`SELECT id FROM catalog_items WHERE library_id=?`, l.lib)
	if err != nil {
		l.t.Fatal(err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := map[string]layoutBook{}
	for _, id := range ids {
		it, err := l.c.Item(l.ctx, 2, id)
		if err != nil {
			l.t.Fatal(err)
		}
		var doc map[string]any
		if err = json.Unmarshal(it.Document, &doc); err != nil {
			l.t.Fatal(err)
		}
		rel, _ := filepath.Rel(l.root, doc["item"].(map[string]any)["path"].(string))
		if _, dup := out[rel]; dup {
			l.t.Fatalf("two items for %s", rel)
		}
		out[rel] = layoutBook{Item: it, Path: rel, Media: doc["media"].(map[string]any), Doc: doc}
	}
	return out
}

func (l *layoutFixture) trackNames(id string) []string {
	l.t.Helper()
	tracks, _, _, err := l.c.Timeline(l.ctx, 2, id, "")
	if err != nil {
		l.t.Fatal(err)
	}
	names := []string{}
	for _, tr := range tracks {
		names = append(names, tr.Title)
	}
	return names
}

func keys(m map[string]layoutBook) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (l *layoutFixture) fileIDs() map[string]string {
	l.t.Helper()
	rows, err := l.c.DB.Query(`SELECT f.item_id,f.id,f.relative_path FROM catalog_files f JOIN catalog_items i ON i.id=f.item_id WHERE i.library_id=?`, l.lib)
	if err != nil {
		l.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var item, id, rel string
		rows.Scan(&item, &id, &rel)
		out[item+"/"+id] = rel
	}
	return out
}

func TestScanStrayFileInAuthorFolderDoesNotMergeBooks(t *testing.T) {
	l := newLayoutFixture(t)
	l.file("Author/folder.jpg", "not really an image")
	l.file("Author/desc.txt", "author biography")
	l.audio("Author/Book One/01.mp3")
	l.audio("Author/the second book/01.mp3")
	l.audio("Author/zebra/01.mp3")
	if r := l.scan(library.ScanOptions{}); r.Added != 3 {
		t.Fatalf("report %+v", r)
	}
	got := l.books()
	if k := keys(got); strings.Join(k, "|") != "Author/Book One|Author/the second book|Author/zebra" {
		t.Fatalf("items %v", k)
	}
	for _, b := range got {
		if b.Author != "Author" || b.Media["coverPath"] != nil {
			t.Fatalf("folder metadata %+v %v", b.Item, b.Media["coverPath"])
		}
	}
	// A loose audio file beside book folders is its own book, never a parent.
	l.audio("Author/Loose story.mp3")
	l.audio("Author/aaa new/01.mp3")
	if r := l.scan(library.ScanOptions{}); r.Added != 2 || r.Updated != 0 {
		t.Fatalf("second scan %+v", r)
	}
	got = l.books()
	if k := keys(got); len(k) != 5 || got["Author/Loose story"].Title != "Loose story" || len(l.trackNames(got["Author/aaa new"].ID)) != 1 {
		t.Fatalf("items %v", k)
	}
}

func TestScanLegacyContainerItemDoesNotAbsorbNewBooks(t *testing.T) {
	l := newLayoutFixture(t)
	l.audio("Author/Book One/01.mp3")
	l.audio("Author/Book Two/01.mp3")
	l.scan(library.ScanOptions{})
	got := l.books()
	one, two := got["Author/Book One"], got["Author/Book Two"]
	// Recreate what the old scanner produced for Author/folder.jpg: one item
	// rooted at the author folder that owns both books' files.
	tx, err := l.c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`PRAGMA defer_foreign_keys=ON`,
		`UPDATE catalog_files SET item_id=? WHERE item_id=?`,
		`UPDATE catalog_file_observations SET item_id=? WHERE item_id=?`,
		`DELETE FROM catalog_items WHERE id=?`,
	} {
		args := []any{one.ID, two.ID}
		if strings.HasPrefix(q, "PRAGMA") {
			args = nil
		} else if strings.HasPrefix(q, "DELETE") {
			args = []any{two.ID}
		}
		if _, err = tx.Exec(q, args...); err != nil {
			t.Fatal(q, err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.c.DB.Exec(`UPDATE catalog_items SET document=json_set(document,'$.item.path',?) WHERE id=?`, filepath.Join(l.root, "Author"), one.ID); err != nil {
		t.Fatal(err)
	}
	l.file("Author/folder.jpg", "x")
	before := l.fileIDs()
	if r := l.scan(library.ScanOptions{}); r.Added != 0 {
		t.Fatalf("legacy item split or duplicated %+v", r)
	}
	after := l.fileIDs()
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("file %s reassigned", k)
		}
	}
	l.audio("Author/Book Three/01.mp3")
	if r := l.scan(library.ScanOptions{}); r.Added != 1 {
		t.Fatalf("new book absorbed %+v", r)
	}
	got = l.books()
	if _, ok := got["Author/Book Three"]; !ok || len(l.trackNames(one.ID)) != 2 {
		t.Fatalf("items %v", keys(got))
	}
	if r := l.scan(library.ScanOptions{}); r.Added+r.Updated+r.Missing+r.Probed+r.Quarantined != 0 {
		t.Fatalf("rescan changed %+v", r)
	}
}

func TestScanRepresentativeLibraryIsStableAndParsesMetadata(t *testing.T) {
	l := newLayoutFixture(t)
	// Author/Series/Book with numbered folders, narrator braces and covers.
	l.audio("Jane Doe/The Saga/Book 1 - First Light/01.mp3")
	l.audio("Jane Doe/The Saga/Book 1 - First Light/02.mp3")
	l.file("Jane Doe/The Saga/Book 1 - First Light/cover.jpg", "cover")
	l.audio("Jane Doe/The Saga/2 - Second Dark {Sam Reader}/CD1/01.mp3")
	l.audio("Jane Doe/The Saga/2 - Second Dark {Sam Reader}/CD2/01.mp3")
	l.file("Jane Doe/The Saga/2 - Second Dark {Sam Reader}/CD1/metadata.json", `{"title":"Nested title"}`)
	l.file("Jane Doe/The Saga/2 - Second Dark {Sam Reader}/metadata.json", `{"title":"Root title"}`)
	// Tags beat folder names; multi-author tags split.
	l.audio("Jane Doe/2005 - Standalone/track.mp3", "album=Tagged Standalone", "artist=Jane Doe & John Roe", "composer=Pat Voice", "date=2005-06-07", "series=Tag Saga", "series-part=7", "asin=B0TAGASIN0")
	l.file("Jane Doe/2005 - Standalone/art.png", "single loose image")
	// metadata.json beats tags; an empty title falls back.
	l.audio("John Roe/Sidecar Book/01.mp3", "album=Tag Album", "artist=Tag Artist")
	l.file("John Roe/Sidecar Book/metadata.json", `{"title":"","authors":["Sidecar Author"]}`)
	// OPF sidecar.
	l.audio("John Roe/Calibre Book/01.mp3")
	l.file("John Roe/Calibre Book/metadata.opf", `<package><metadata><title>OPF Title</title><creator role="aut">Opf Author</creator><meta name="calibre:series" content="Opf Saga"/><meta name="calibre:series_index" content="3"/></metadata></package>`)
	// .ignore hides a folder and everything below it.
	l.audio("Ignored Author/Hidden Book/01.mp3")
	l.file("Ignored Author/.ignore", "")
	// Embedded cover art.
	cover := filepath.Join(l.root, "Art Author/Embedded/01.mp3")
	os.MkdirAll(filepath.Dir(cover), 0700)
	if out, err := exec.Command(l.ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-f", "lavfi", "-i", "color=red:s=32x32", "-map", "0:a", "-map", "1:v", "-frames:v", "1", "-c:v", "mjpeg", "-disposition:v", "attached_pic", "-id3v2_version", "3", "-t", "1", cover).CombinedOutput(); err != nil {
		t.Fatalf("cover fixture %v %s", err, out)
	}
	opt := library.ScanOptions{FFmpeg: l.ffmpeg, ArtDir: l.artDir}
	r := l.scan(opt)
	if r.Added != 6 || r.Quarantined != 0 {
		t.Fatalf("report %+v", r)
	}
	got := l.books()
	if k := keys(got); strings.Join(k, "|") != "Art Author/Embedded|Jane Doe/2005 - Standalone|Jane Doe/The Saga/2 - Second Dark {Sam Reader}|Jane Doe/The Saga/Book 1 - First Light|John Roe/Calibre Book|John Roe/Sidecar Book" {
		t.Fatalf("items %v", k)
	}
	series := func(b layoutBook) string {
		list, _ := b.Doc["series"].([]any)
		if len(list) == 0 {
			return ""
		}
		s := list[0].(map[string]any)
		seq, _ := s["sequence"].(string)
		return s["name"].(string) + " #" + seq
	}
	first := got["Jane Doe/The Saga/Book 1 - First Light"]
	if first.Title != "First Light" || first.Author != "Jane Doe" || series(first) != "The Saga #1" || !strings.HasSuffix(first.Media["coverPath"].(string), "cover.jpg") {
		t.Fatalf("first %+v %v", first.Item, first.Doc)
	}
	second := got["Jane Doe/The Saga/2 - Second Dark {Sam Reader}"]
	if second.Title != "Root title" || series(second) != "The Saga #2" || len(l.trackNames(second.ID)) != 2 {
		t.Fatalf("second %+v", second.Item)
	}
	if n := second.Media["narrators"].([]any); len(n) != 1 || n[0] != "Sam Reader" {
		t.Fatalf("narrator %v", n)
	}
	standalone := got["Jane Doe/2005 - Standalone"]
	authors := standalone.Doc["authors"].([]any)
	if standalone.Title != "Tagged Standalone" || standalone.Author != "Jane Doe, John Roe" || len(authors) != 2 || standalone.Media["publishedYear"] != "2005" || series(standalone) != "Tag Saga #7" || standalone.Media["asin"] != "B0TAGASIN0" || !strings.HasSuffix(standalone.Media["coverPath"].(string), "art.png") {
		t.Fatalf("standalone %+v %v", standalone.Item, standalone.Doc)
	}
	if n := standalone.Media["narrators"].([]any); len(n) != 1 || n[0] != "Pat Voice" {
		t.Fatalf("composer narrator %v", n)
	}
	side := got["John Roe/Sidecar Book"]
	if side.Title != "Tag Album" || side.Author != "Sidecar Author" {
		t.Fatalf("sidecar %+v", side.Item)
	}
	var orphan int
	l.c.DB.QueryRow(`SELECT count(*) FROM catalog_entities WHERE kind='authors' AND json_extract(document,'$.name')='Tag Artist'`).Scan(&orphan)
	if orphan != 0 {
		t.Fatal("tag author created although the sidecar replaced it")
	}
	opf := got["John Roe/Calibre Book"]
	if opf.Title != "OPF Title" || opf.Author != "Opf Author" || series(opf) != "Opf Saga #3" {
		t.Fatalf("opf %+v %v", opf.Item, opf.Doc)
	}
	art := got["Art Author/Embedded"]
	if path, _ := art.Media["coverPath"].(string); !strings.HasPrefix(path, "/pocket-native-art/") {
		t.Fatalf("embedded cover %v", art.Media["coverPath"])
	} else if _, err := os.Stat(filepath.Join(l.artDir, strings.TrimPrefix(path, "/pocket-native-art/"))); err != nil {
		t.Fatal(err)
	}
	files, err := l.c.Files(l.ctx, 2, art.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(func() string {
		ids := []string{}
		for _, f := range files {
			ids = append(ids, f.ID+":"+f.Role)
		}
		return strings.Join(ids, ",")
	}(), "native-cover:cover") {
		t.Fatalf("cover file %+v", files)
	}
	// Rescanning an unchanged tree changes nothing at all.
	docs := map[string]string{}
	for k, b := range got {
		docs[k] = string(b.Document)
	}
	ids := l.fileIDs()
	for _, force := range []bool{false, true} {
		opt.Force = force
		r = l.scan(opt)
		for k, b := range l.books() {
			if docs[k] != string(b.Document) {
				t.Fatalf("rescan changed %s:\n%s\n%s", k, docs[k], b.Document)
			}
			if r.Added+r.Updated+r.Missing+r.Quarantined != 0 || (!force && r.Probed != 0) {
				t.Fatalf("rescan force=%v changed %+v", force, r)
			}
		}
		now := l.fileIDs()
		if len(now) != len(ids) {
			t.Fatalf("file rows changed %d -> %d", len(ids), len(now))
		}
		for k, v := range ids {
			if now[k] != v {
				t.Fatalf("file %s reassigned", k)
			}
		}
	}
}

func TestScanAddedTrackTakesNaturalPlaceAndRebuildsFileChapters(t *testing.T) {
	l := newLayoutFixture(t)
	l.audio("Author/Book/01.mp3")
	l.audio("Author/Book/03.mp3")
	l.audio("Author/Edited/01.mp3")
	l.audio("Author/Edited/03.mp3")
	l.scan(library.ScanOptions{})
	got := l.books()
	edited := got["Author/Edited"]
	if err := l.c.Edit(l.ctx, 2, edited.ID, library.EditOptions{Expected: edited.UpdatedAt, Metadata: map[string]any{}, Chapters: &[]library.Chapter{{Start: 0, End: 2, Title: "Whole book"}}}); err != nil {
		t.Fatal(err)
	}
	l.audio("Author/Book/02.mp3")
	l.audio("Author/Edited/02.mp3")
	if r := l.scan(library.ScanOptions{}); r.Updated != 2 || r.Added != 0 {
		t.Fatalf("report %+v", r)
	}
	got = l.books()
	book := got["Author/Book"]
	if names := strings.Join(l.trackNames(book.ID), ","); names != "01.mp3,02.mp3,03.mp3" {
		t.Fatalf("order %s", names)
	}
	_, chapters, _, err := l.c.Timeline(l.ctx, 2, book.ID, "")
	if err != nil || len(chapters) != 3 || chapters[1].Title != "02" || chapters[2].Start < 1.9 {
		t.Fatalf("chapters %+v %v", chapters, err)
	}
	editedNow := got["Author/Edited"]
	if names := strings.Join(l.trackNames(editedNow.ID), ","); names != "01.mp3,02.mp3,03.mp3" {
		t.Fatalf("edited order %s", names)
	}
	_, chapters, _, err = l.c.Timeline(l.ctx, 2, editedNow.ID, "")
	if err != nil || len(chapters) != 1 || chapters[0].Title != "Whole book" {
		t.Fatalf("edited chapters replaced %+v %v", chapters, err)
	}
}

func TestUploadAndScanDoNotDuplicate(t *testing.T) {
	l := newLayoutFixture(t)
	var folder string
	if err := l.c.DB.QueryRow(`SELECT id FROM catalog_entities WHERE kind='libraryFolders' AND library_id=?`, l.lib).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.mp3")
	if out, err := exec.Command(l.ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "1", source).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	done := make(chan error, 8)
	for i := 0; i < 4; i++ {
		go func() {
			_, err := l.c.Scan(l.ctx, 2, l.lib, library.ScanOptions{FFprobe: l.ffprobe})
			done <- err
		}()
	}
	if _, err := l.c.Ingest(l.ctx, 2, library.UploadOptions{LibraryID: l.lib, FolderID: folder, Title: "Uploaded", FFprobe: l.ffprobe, Files: []library.UploadFile{{Name: "a.mp3", Path: source}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		<-done // concurrent scans may be refused with a conflict; that is fine
	}
	l.scan(library.ScanOptions{})
	if got := l.books(); len(got) != 1 {
		t.Fatalf("items %v", keys(got))
	}
}
