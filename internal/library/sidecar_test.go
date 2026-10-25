package library_test

import (
	"context"
	"encoding/json"
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
	// Invalid metadata quarantines only this book: it is not partially updated
	// and the scan itself still completes.
	write("metadata.json", `{"title":"Should roll back","chapters":[{"start":0,"end":999,"title":"Bad"}]}`)
	if r, err := c.Scan(ctx, 2, lib, library.ScanOptions{ImportMetadata: true}); err != nil || r.Quarantined != 1 || len(r.Failures) != 1 || r.Failures[0].Path != dir {
		t.Fatalf("invalid chapters: %+v %v", r, err)
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

func TestLegacyABMetadataAndJSONPriority(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	if _, err := c.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=2`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Legacy book")
	os.Mkdir(dir, 0700)
	os.WriteFile(filepath.Join(dir, "book.epub"), []byte("fixture"), 0600)
	legacy := `;ABMETADATA2
# exported book
media=book
title=Legacy = title
authors=One Author, Two Author
narrators=Voice One, Voice Two
series=A series #2.5
tags=["legacy", "tag"]
explicit=N
abridged=Y
[DESCRIPTION]
First = paragraph

Second paragraph
[CHAPTER]
start=0
end=0
title=Chapter = one
`
	os.WriteFile(filepath.Join(dir, "metadata.abs"), []byte(legacy), 0600)
	id, err := c.CreateLibrary(ctx, 2, library.NewLibrary{Name: "Legacy", Path: root, MediaType: "book"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Scan(ctx, 2, id, library.ScanOptions{}); err != nil {
		t.Fatal(err)
	}
	page, _ := c.Items(ctx, 2, library.Query{LibraryID: id})
	if len(page.Items) != 1 {
		t.Fatal(page)
	}
	book, _ := c.Item(ctx, 2, page.Items[0].ID)
	var doc map[string]any
	json.Unmarshal(book.Document, &doc)
	m := doc["media"].(map[string]any)
	if book.Title != "Legacy = title" || book.Author != "One Author, Two Author" || m["description"] != "First = paragraph\n\nSecond paragraph" || m["abridged"] != true || len(book.Tags) != 2 {
		t.Fatal(book, m)
	}
	os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"title":"JSON title"}`), 0600)
	if _, err = c.Scan(ctx, 2, id, library.ScanOptions{ImportMetadata: true}); err != nil {
		t.Fatal(err)
	}
	book, _ = c.Item(ctx, 2, book.ID)
	if book.Title != "JSON title" {
		t.Fatal(book.Title)
	}
	before := book.UpdatedAt
	os.WriteFile(filepath.Join(dir, "metadata.abs"), []byte(";ABMETADATA2\nmedia=book\n[CHAPTER]\nstart=NaN\nend=4\ntitle=invalid"), 0600)
	if r, err := c.Scan(ctx, 2, id, library.ScanOptions{ImportMetadata: true}); err != nil || r.Quarantined != 1 {
		t.Fatal("invalid chapter accepted", r, err)
	}
	book, _ = c.Item(ctx, 2, book.ID)
	if book.UpdatedAt != before {
		t.Fatal("malformed sidecar partially committed")
	}
}
