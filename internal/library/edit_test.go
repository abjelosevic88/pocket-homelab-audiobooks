package library_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestNativeEditingPreservesProgressAndRejectsLostUpdates(t *testing.T) {
	f := setup(t)
	apply(t, f)
	c := f.catalog
	ctx := context.Background()
	if _, err := c.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=2`); err != nil {
		t.Fatal(err)
	}
	it, err := c.Item(ctx, 2, "visible")
	if err != nil {
		t.Fatal(err)
	}
	patch := library.EditOptions{Expected: it.UpdatedAt, Metadata: map[string]any{"title": "Edited title", "authors": []any{map[string]any{"name": "New author"}}, "series": []any{map[string]any{"name": "A series", "sequence": "2"}}, "genres": []any{"Mystery"}, "tags": []any{"allowed"}, "description": "My notes"}}
	if err = c.Edit(ctx, 2, "visible", patch); err != nil {
		t.Fatal(err)
	}
	current, err := c.Item(ctx, 2, "visible")
	if err != nil {
		t.Fatal(err)
	}
	if current.Title != "Edited title" || current.Author != "New author" {
		t.Fatalf("indexed metadata %+v", current)
	}
	if err = c.Edit(ctx, 2, "visible", patch); !errors.Is(err, library.ErrConflict) {
		t.Fatalf("lost update %v", err)
	}
	chapters := []library.Chapter{{ID: 0, Title: "Invalid", Start: 0, End: current.Duration + 10}}
	if err = c.Edit(ctx, 2, "visible", library.EditOptions{Expected: current.UpdatedAt, Chapters: &chapters}); !errors.Is(err, library.ErrInvalid) {
		t.Fatalf("invalid chapters %v", err)
	}
	next, _ := c.Item(ctx, 2, "visible")
	if string(next.Document) != string(current.Document) {
		t.Fatal("failed edit changed metadata")
	}
	var position float64
	if err = c.DB.QueryRow(`SELECT position FROM catalog_progress WHERE account_id='bob' AND item_id='visible'`).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if position != 99 {
		t.Fatal("metadata edit changed listening progress", position)
	}
	var doc map[string]any
	if err = json.Unmarshal(next.Document, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc["series"].([]any)) != 1 {
		t.Fatal("series lost")
	}
	if err = c.Edit(ctx, 1, "visible", library.EditOptions{Expected: next.UpdatedAt, Metadata: map[string]any{"title": "Forbidden"}}); !errors.Is(err, library.ErrNotFound) {
		t.Fatalf("unauthorized metadata edit %v", err)
	}
}
