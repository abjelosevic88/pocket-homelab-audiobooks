package library_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestNativeUploadPublishesValidatedBookWithoutReplacingFiles(t *testing.T) {
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
	execSQL := func(q string, args ...any) {
		t.Helper()
		if _, err := c.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true'),'$.upload',json('true')) WHERE pocket_user_id=2`)
	lib, err := c.CreateLibrary(ctx, 2, library.NewLibrary{Name: "Uploaded", MediaType: "book", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	var folder string
	if err = c.DB.QueryRow(`SELECT id FROM catalog_entities WHERE kind='libraryFolders' AND library_id=?`, lib).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.mp3")
	cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "1", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("audio fixture: %v %s", err, out)
	}
	opt := library.UploadOptions{LibraryID: lib, FolderID: folder, Title: "A new book", Author: "An Author", Series: "A Series", FFprobe: ffprobe, Files: []library.UploadFile{{Name: "Book.mp3", Path: source}}}
	id, err := c.Ingest(ctx, 2, opt)
	if err != nil {
		t.Fatal(err)
	}
	it, err := c.Item(ctx, 2, id)
	if err != nil || it.Title != opt.Title || it.Author != opt.Author {
		t.Fatalf("upload metadata %+v %v", it, err)
	}
	if _, err = c.OpenSession(ctx, 2, id, "", "test", "Test player"); err != nil {
		t.Fatal("uploaded book cannot play", err)
	}
	if _, err = c.Ingest(ctx, 2, opt); !errors.Is(err, library.ErrConflict) {
		t.Fatalf("existing title overwritten %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, opt.Title, "Book.mp3")); err != nil {
		t.Fatal(err)
	}
	opt.Title = "Broken book"
	bad := filepath.Join(t.TempDir(), "bad.mp3")
	if err = os.WriteFile(bad, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	opt.Files = append(opt.Files, library.UploadFile{Name: "bad.mp3", Path: bad})
	if _, err = c.Ingest(ctx, 2, opt); err == nil {
		t.Fatal("invalid upload accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed upload left files: %v %v", entries, err)
	}
	page, err := c.Items(ctx, 2, library.Query{LibraryID: lib})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("failed upload left catalog records")
	}
	opt.Files = []library.UploadFile{{Name: "../escape.mp3", Path: source}}
	if _, err = c.Ingest(ctx, 2, opt); !errors.Is(err, library.ErrInvalid) {
		t.Fatalf("path traversal accepted %v", err)
	}
}
