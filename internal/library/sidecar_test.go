package library_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestSidecarsImportPreserveEditsAndRollback(t *testing.T) {
	f := setup(t)
	apply(t, f)
	c := f.catalog
	ctx := context.Background()
	if _, err := c.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=2`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Sidecar book")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lib, err := c.CreateLibrary(ctx, 2, library.NewLibrary{Name: "Sidecars", MediaType: "book", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("book.epub", "ebook fixture")
	write("desc.txt", "Description from a file")
	write("reader.txt", "A Narrator")
	write("metadata.json", `{"metadata":{"title":"From sidecar","authors":["First Author","Second Author"]},"series":["Series #3"],"tags":["custom"],"explicit":true,"chapters":[]}`)
	run := func(importMetadata bool) library.ScanReport {
		t.Helper()
		r, e := c.Scan(ctx, 2, lib, library.ScanOptions{ImportMetadata: importMetadata})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	if r := run(false); r.Added != 1 {
		t.Fatalf("added %+v", r)
	}
	page, err := c.Items(ctx, 2, library.Query{LibraryID: lib})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	it, err := c.Item(ctx, 2, page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	id := it.ID
	if it.Title != "From sidecar" || it.Author != "First Author, Second Author" || !it.Explicit {
		t.Fatalf("metadata %+v", it)
	}
	var d map[string]any
	json.Unmarshal(it.Document, &d)
	m := d["media"].(map[string]any)
	if m["description"] != "Description from a file" || len(d["authors"].([]any)) != 2 || d["series"].([]any)[0].(map[string]any)["sequence"] != "3" {
		t.Fatal("metadata missing", string(it.Document))
	}
	if err = c.Edit(ctx, 2, id, library.EditOptions{Expected: it.UpdatedAt, Metadata: map[string]any{"title": "Manual edit"}}); err != nil {
		t.Fatal(err)
	}
	run(false)
	it, _ = c.Item(ctx, 2, id)
	if it.Title != "Manual edit" {
		t.Fatal("regular scan overwrote edit")
	}
	run(true)
	it, _ = c.Item(ctx, 2, id)
	if it.Title != "From sidecar" {
		t.Fatal("explicit sidecar import ignored")
	}
	// Invalid metadata does not partially update either this book or new books.
	write("metadata.json", `{"title":"Should roll back","chapters":[{"start":0,"end":999,"title":"Bad"}]}`)
	if _, err = c.Scan(ctx, 2, lib, library.ScanOptions{ImportMetadata: true}); !errors.Is(err, library.ErrInvalid) {
		t.Fatalf("invalid chapters: %v", err)
	}
	current, _ := c.Item(ctx, 2, id)
	if string(it.Document) != string(current.Document) {
		t.Fatal("bad sidecar partly applied")
	}
	// A sidecar symlink is ignored; it cannot read a file outside the root.
	if err = os.Remove(filepath.Join(dir, "metadata.json")); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "secret.json")
	os.WriteFile(external, []byte(`{"title":"Escaped"}`), 0600)
	if err = os.Symlink(external, filepath.Join(dir, "metadata.json")); err != nil {
		t.Fatal(err)
	}
	run(true)
	current, _ = c.Item(ctx, 2, id)
	if current.Title == "Escaped" {
		t.Fatal("symlink escape")
	}
}
