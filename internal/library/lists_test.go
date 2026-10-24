package library_test

import (
	"context"
	"errors"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestNativeListsOwnershipOrderingAndImport(t *testing.T) {
	f := setup(t)
	apply(t, f)
	c := f.catalog
	ctx := context.Background()
	if _, err := c.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=2`); err != nil {
		t.Fatal(err)
	}
	visible, err := c.Item(ctx, 2, "visible")
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.Item(ctx, 2, "explicit")
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.CreateList(ctx, 2, "playlists", visible.LibraryID, "Private list", "", []library.ListEntry{{ItemID: visible.ID}, {ItemID: other.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.GetList(ctx, 1, "playlists", list.ID); !errors.Is(err, library.ErrNotFound) {
		t.Fatalf("private list leaked %v", err)
	}
	if err = c.DeleteList(ctx, 1, "playlists", list.ID); !errors.Is(err, library.ErrNotFound) {
		t.Fatalf("private list deleted %v", err)
	}
	entries := []library.ListEntry{{ItemID: other.ID}, {ItemID: visible.ID}}
	reordered, err := c.EditList(ctx, 2, "playlists", list.ID, library.ListEdit{Entries: &entries, Expected: &list.Revision})
	if err != nil || reordered.Entries[0].ItemID != other.ID {
		t.Fatalf("reorder %+v %v", reordered, err)
	}
	if _, err = c.EditList(ctx, 2, "playlists", list.ID, library.ListEdit{Entries: &entries, Expected: &list.Revision}); !errors.Is(err, library.ErrConflict) {
		t.Fatalf("stale edit %v", err)
	}
	shared, err := c.CreateList(ctx, 2, "collections", visible.LibraryID, "Shared list", "", entries)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := c.GetList(ctx, 1, "collections", shared.ID)
	if err != nil || len(filtered.Entries) != 1 || filtered.Entries[0].ItemID != visible.ID {
		t.Fatalf("filtered collection %+v %v", filtered, err)
	}
	if _, err = c.EditList(ctx, 1, "collections", shared.ID, library.ListEdit{Entries: &filtered.Entries}); !errors.Is(err, library.ErrNotFound) {
		t.Fatalf("restricted collection edit %v", err)
	}
	// Source associations remain readable and are translated from media IDs to
	// stable library-item IDs before their first native edit.
	if _, err = c.DB.Exec(`INSERT INTO catalog_entities VALUES('collections','imported-list',?,json_object('id','imported-list','libraryId',?,'name','Imported'))`, visible.LibraryID, visible.LibraryID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`INSERT INTO catalog_entities VALUES('collectionBooks','imported-ref',NULL,json_object('id','imported-ref','collectionId','imported-list','bookId',?,'order',1))`, visible.MediaID); err != nil {
		t.Fatal(err)
	}
	imported, err := c.GetList(ctx, 2, "collections", "imported-list")
	if err != nil || len(imported.Entries) != 1 || imported.Entries[0].ItemID != visible.ID {
		t.Fatalf("imported list %+v %v", imported, err)
	}
	if err = c.DeleteList(ctx, 2, "playlists", list.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Item(ctx, 2, visible.ID); err != nil {
		t.Fatal("list deletion removed a book", err)
	}
}
