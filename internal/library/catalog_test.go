package library_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

type fixture struct {
	catalog      *library.Catalog
	opt          library.ImportOptions
	root, source string
}

func setup(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	media := filepath.Join(root, "media")
	if err := os.Mkdir(media, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "chapter.mp3"), []byte("0123456789audio"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "pocket.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, u := range []store.User{{Email: "a@example.com", Role: "admin"}, {Email: "b@example.com", Role: "member"}, {Email: "unmapped@example.com", Role: "member"}} {
		if err = st.CreateUser(context.Background(), &u); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "source.sqlite")
	src, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	schema := `
 CREATE TABLE libraries(id TEXT PRIMARY KEY,name TEXT,mediaType TEXT,displayOrder INTEGER,settings TEXT,extraData TEXT);
 CREATE TABLE libraryFolders(id TEXT PRIMARY KEY,libraryId TEXT,path TEXT);
 CREATE TABLE libraryItems(id TEXT PRIMARY KEY,libraryId TEXT,mediaId TEXT,mediaType TEXT,authorNamesFirstLast TEXT,isMissing INTEGER,createdAt TEXT,updatedAt TEXT,libraryFiles TEXT,extraData TEXT);
 CREATE TABLE books(id TEXT PRIMARY KEY,title TEXT,duration REAL,explicit INTEGER,narrators TEXT,audioFiles TEXT,ebookFile TEXT,chapters TEXT,tags TEXT,genres TEXT,coverPath TEXT);
 CREATE TABLE authors(id TEXT PRIMARY KEY,name TEXT,libraryId TEXT);
 CREATE TABLE series(id TEXT PRIMARY KEY,name TEXT,libraryId TEXT);
 CREATE TABLE bookAuthors(id TEXT PRIMARY KEY,bookId TEXT,authorId TEXT);
 CREATE TABLE bookSeries(id TEXT PRIMARY KEY,bookId TEXT,seriesId TEXT,sequence TEXT);
 CREATE TABLE podcasts(id TEXT PRIMARY KEY,title TEXT,tags TEXT,genres TEXT);
 CREATE TABLE podcastEpisodes(id TEXT PRIMARY KEY,podcastId TEXT,audioFile TEXT,chapters TEXT,extraData TEXT);
 CREATE TABLE users(id TEXT PRIMARY KEY,username TEXT,isActive INTEGER,isLocked INTEGER,permissions TEXT,bookmarks TEXT,token TEXT,pash TEXT,email TEXT);
 CREATE TABLE mediaProgresses(id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,currentTime REAL,isFinished INTEGER,updatedAt TEXT,extraData TEXT);
 CREATE TABLE playbackSessions(id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,mediaMetadata TEXT,extraData TEXT);
 CREATE TABLE collections(id TEXT PRIMARY KEY,libraryId TEXT);
 CREATE TABLE collectionBooks(id TEXT PRIMARY KEY);
 CREATE TABLE playlists(id TEXT PRIMARY KEY,libraryId TEXT);
 CREATE TABLE playlistMediaItems(id TEXT PRIMARY KEY);
 INSERT INTO libraries VALUES('public','Books','book',0,'{}','{}'),('private','Private','book',1,'{}','{}');
 INSERT INTO users VALUES('alice','Alice',1,0,'{"accessAllLibraries":false,"librariesAccessible":["public"],"accessAllTags":false,"itemTagsSelected":["allowed"],"accessExplicitContent":false,"download":false}','[]','do-not-copy-token','do-not-copy-password','do-not-copy-email');
 INSERT INTO users VALUES('bob','Bob',1,0,'{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"download":true}','[]','','','');
 INSERT INTO mediaProgresses VALUES('progress-a','alice','media-visible',30,0,'2026-01-02T00:00:00Z','{}'),('progress-b','bob','media-visible',99,0,'2026-01-02T00:00:00Z','{}');
 INSERT INTO playbackSessions VALUES('history-a','alice','media-visible','{"title":"Visible"}','{}');
 `
	if _, err = src.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, lib, tags string
		explicit      int
	}{{"visible", "public", `["allowed"]`, 0}, {"explicit", "public", `["allowed"]`, 1}, {"tagged", "public", `["blocked"]`, 0}, {"hidden", "private", `["allowed"]`, 0}} {
		f := `{"index":1,"ino":"file-1","duration":100,"mimeType":"audio/mpeg","metadata":{"path":"/media/chapter.mp3","filename":"chapter.mp3","size":14}}`
		if _, err = src.Exec(`INSERT INTO books VALUES(?,?,100,?,'[]',?,NULL,'[]',?,'[]','')`, "media-"+row.id, row.id, row.explicit, "["+f+"]", row.tags); err != nil {
			t.Fatal(err)
		}
		if _, err = src.Exec(`INSERT INTO libraryItems VALUES(?,?,?,'book','Author',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',?,'{}')`, row.id, row.lib, "media-"+row.id, "["+f+"]"); err != nil {
			t.Fatal(err)
		}
	}
	return fixture{catalog: &library.Catalog{DB: st.DB}, root: media, source: source, opt: library.ImportOptions{Source: source, Mounts: map[string]string{"/media": media}, Users: map[string]int64{"alice": 1, "bob": 2}, Apply: true}}
}
func apply(t *testing.T, f fixture) library.ImportReport {
	t.Helper()
	report, err := f.catalog.Import(context.Background(), f.opt)
	if err != nil {
		t.Fatal(err)
	}
	return report
}
func sourceExec(t *testing.T, f fixture, q string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", f.source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func assertEmpty(t *testing.T, f fixture) {
	t.Helper()
	for _, table := range []string{"catalog_imports", "catalog_roots", "catalog_accounts", "catalog_items", "catalog_files", "catalog_progress"} {
		var n int
		if err := f.catalog.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s retained %d rows", table, n)
		}
	}
}

func TestImportDryRunAtomicAndReadOnly(t *testing.T) {
	f := setup(t)
	before, err := os.ReadFile(f.source)
	if err != nil {
		t.Fatal(err)
	}
	f.opt.Apply = false
	r := apply(t, f)
	if r.Applied || r.FilesAccessible != 4 {
		t.Fatalf("bad report: %+v", r)
	}
	assertEmpty(t, f)
	after, _ := os.ReadFile(f.source)
	if string(before) != string(after) {
		t.Fatal("source changed")
	}
	f.opt.Apply = true
	applied := apply(t, f)
	if !applied.Applied || r.Fingerprint != applied.Fingerprint {
		t.Fatal("unstable fingerprint")
	}
	if _, err = f.catalog.Import(context.Background(), f.opt); !errors.Is(err, library.ErrImported) {
		t.Fatalf("repeat import: %v", err)
	}
}
func TestImportFailureRollsBackEveryTable(t *testing.T) {
	f := setup(t)
	sourceExec(t, f, `UPDATE mediaProgresses SET userId='missing' WHERE id='progress-b'`)
	if _, err := f.catalog.Import(context.Background(), f.opt); err == nil {
		t.Fatal("orphan accepted")
	}
	assertEmpty(t, f)
}
func TestPermissionsApplyToEveryReadIncludingPocketAdmin(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	libs, err := f.catalog.Libraries(ctx, 1)
	if err != nil || len(libs) != 1 || libs[0].Count != 1 {
		t.Fatalf("libraries: %+v %v", libs, err)
	}
	page, err := f.catalog.Items(ctx, 1, library.Query{})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "visible" {
		t.Fatalf("page: %+v %v", page, err)
	}
	for _, id := range []string{"explicit", "tagged", "hidden"} {
		if _, err = f.catalog.Item(ctx, 1, id); !errors.Is(err, library.ErrNotFound) {
			t.Fatalf("item %s: %v", id, err)
		}
		if _, err = f.catalog.Files(ctx, 1, id); !errors.Is(err, library.ErrNotFound) {
			t.Fatalf("files %s: %v", id, err)
		}
		if file, _, err := f.catalog.OpenFile(ctx, 1, id, "file-1", false); !errors.Is(err, library.ErrNotFound) {
			if file != nil {
				file.Close()
			}
			t.Fatalf("stream %s: %v", id, err)
		}
	}
	page, err = f.catalog.Items(ctx, 2, library.Query{})
	if err != nil || page.Total != 4 {
		t.Fatalf("bob: %+v %v", page, err)
	}
	if _, err = f.catalog.Libraries(ctx, 3); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("unmapped account granted access")
	}
}
func TestProgressIsolationAndCredentialsOmitted(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	for uid, want := range map[int64]string{1: "progress-a", 2: "progress-b"} {
		p, err := f.catalog.Progress(ctx, uid)
		if err != nil || len(p) != 1 {
			t.Fatalf("progress: %v %v", p, err)
		}
		var row map[string]any
		if err = json.Unmarshal(p[0], &row); err != nil {
			t.Fatal(err)
		}
		if row["id"] != want {
			t.Fatalf("wrong progress: %v", row)
		}
	}
	var permissions string
	if err := f.catalog.DB.QueryRow(`SELECT permissions FROM catalog_accounts WHERE id='alice'`).Scan(&permissions); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(permissions, "do-not-copy") {
		t.Fatal("upstream credentials copied")
	}
	rows, err := f.catalog.DB.Query(`PRAGMA table_info(catalog_accounts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n, notnull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&n, &name, &typ, &notnull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "token" || name == "pash" || name == "email" {
			t.Fatalf("unexpected credential column %s", name)
		}
	}
}
func TestFilesAreLocalAndRespectDownloadAndSymlinkBoundary(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	file, _, err := f.catalog.OpenFile(ctx, 1, "visible", "file-1", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(b) != "0123456789audio" {
		t.Fatalf("bytes: %s %v", b, err)
	}
	if file, _, err = f.catalog.OpenFile(ctx, 1, "visible", "file-1", true); !errors.Is(err, library.ErrNotFound) {
		if file != nil {
			file.Close()
		}
		t.Fatal("download permission ignored")
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err = os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, "chapter.mp3")
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(secret, path); err != nil {
		t.Fatal(err)
	}
	if file, _, err = f.catalog.OpenFile(ctx, 2, "visible", "file-1", false); err == nil {
		file.Close()
		t.Fatal("symlink escape opened")
	}
}
func TestInvalidMappingsAndPathsCannotImport(t *testing.T) {
	for _, kind := range []string{"duplicate-user", "unknown-user", "unknown-source", "traversal", "unmapped-root", "malformed-json", "wrong-json-shape", "orphan-media", "invalid-duration"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			switch kind {
			case "duplicate-user":
				f.opt.Users["bob"] = 1
			case "unknown-user":
				f.opt.Users["alice"] = 999
			case "unknown-source":
				f.opt.Users["absent"] = 3
			case "traversal":
				sourceExec(t, f, `UPDATE books SET audioFiles=replace(audioFiles,'/media/chapter.mp3','/media/../secret')`)
			case "unmapped-root":
				f.opt.Mounts = map[string]string{}
			case "wrong-json-shape":
				sourceExec(t, f, `UPDATE books SET chapters='{}'`)
			case "orphan-media":
				sourceExec(t, f, `DELETE FROM libraryItems WHERE id='hidden'`)
			case "invalid-duration":
				sourceExec(t, f, `UPDATE books SET duration='not-a-number'`)
			case "malformed-json":
				sourceExec(t, f, `UPDATE users SET permissions='not json' WHERE id='alice'`)
			}
			if _, err := f.catalog.Import(context.Background(), f.opt); err == nil {
				t.Fatal("invalid import accepted")
			}
			assertEmpty(t, f)
		})
	}
}
func TestMissingFilesRemainInCatalogAndReport(t *testing.T) {
	f := setup(t)
	if err := os.Remove(filepath.Join(f.root, "chapter.mp3")); err != nil {
		t.Fatal(err)
	}
	r := apply(t, f)
	if r.MissingFiles != 4 || len(r.MissingFileItems) != 4 {
		t.Fatalf("report %+v", r)
	}
	files, err := f.catalog.Files(context.Background(), 2, "visible")
	if err != nil || len(files) != 1 {
		t.Fatalf("missing records lost: %+v %v", files, err)
	}
}
func TestLockedAccountAndDenyTags(t *testing.T) {
	f := setup(t)
	sourceExec(t, f, `UPDATE users SET isLocked=1 WHERE id='bob'`)
	sourceExec(t, f, `UPDATE users SET permissions='{"accessAllLibraries":true,"accessAllTags":false,"itemTagsSelected":["blocked"],"selectedTagsNotAccessible":true,"accessExplicitContent":true}' WHERE id='alice'`)
	apply(t, f)
	ctx := context.Background()
	if _, err := f.catalog.Account(ctx, 2); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("locked account admitted")
	}
	p, err := f.catalog.Items(ctx, 1, library.Query{})
	if err != nil || p.Total != 3 {
		t.Fatalf("tag exclusion: %+v %v", p, err)
	}
}

func TestImportExplicitAccountExclusion(t *testing.T) {
	f := setup(t)
	sourceExec(t, f, `ALTER TABLE playlists ADD COLUMN userId TEXT; ALTER TABLE playlistMediaItems ADD COLUMN playlistId TEXT;
 INSERT INTO playlists VALUES('discard-list','public','bob'); INSERT INTO playlistMediaItems VALUES('discard-member','discard-list');
 INSERT INTO playbackSessions VALUES('history-b','bob','media-visible','{}','{}');
 UPDATE users SET bookmarks='[{"libraryItemId":"visible","time":12}]' WHERE id='bob';`)
	f.opt.ExcludeAccounts = []string{"bob"}
	if _, err := f.catalog.Import(context.Background(), f.opt); err == nil {
		t.Fatal("mapped exclusion accepted")
	}
	assertEmpty(t, f)
	delete(f.opt.Users, "bob")
	report := apply(t, f)
	if report.ExcludedRows["users"] != 1 || report.ExcludedRows["mediaProgresses"] != 1 || report.ExcludedRows["playbackSessions"] != 1 || report.ExcludedRows["playlistMediaItems"] != 1 {
		t.Fatalf("exclusion report: %+v", report)
	}
	for table, want := range map[string]int{"catalog_accounts": 1, "catalog_progress": 1, "catalog_history": 1, "catalog_bookmarks": 0, "catalog_items": 4} {
		var count int
		if err := f.catalog.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var lists int
	f.catalog.DB.QueryRow(`SELECT count(*) FROM catalog_entities WHERE kind IN ('playlists','playlistMediaItems')`).Scan(&lists)
	if lists != 0 {
		t.Fatal("excluded private list survived")
	}
	sourceExec(t, f, `UPDATE users SET username='Still present in source' WHERE id='bob'`)
}
