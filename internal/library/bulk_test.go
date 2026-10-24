package library_test

import (
	"context"
	"errors"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"testing"
)

func TestBulkEditAtomicityAppendAndPermission(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	if _, err := c.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=2`); err != nil {
		t.Fatal(err)
	}
	page, err := c.Items(ctx, 2, library.Query{Limit: 100})
	if err != nil || len(page.Items) < 2 {
		t.Fatal(page, err)
	}
	first, _ := c.Item(ctx, 2, page.Items[0].ID)
	second, _ := c.Item(ctx, 2, page.Items[1].ID)
	edits := []library.ItemEdit{{ID: first.ID, EditOptions: library.EditOptions{Expected: first.UpdatedAt, Metadata: map[string]any{"authors": []any{map[string]any{"name": "New bulk author"}}, "tags": []any{"first", "SHARED"}}}}, {ID: second.ID, EditOptions: library.EditOptions{Expected: second.UpdatedAt - 1, Metadata: map[string]any{"title": "Second changed"}}}}
	if err = c.EditMany(ctx, 2, edits); !errors.Is(err, library.ErrConflict) {
		t.Fatal(err)
	}
	var count int
	if err = c.DB.QueryRow(`SELECT count(*) FROM catalog_entities WHERE kind='authors' AND json_extract(document,'$.name')='New bulk author'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial author commit", count, err)
	}
	check, _ := c.Item(ctx, 2, first.ID)
	if check.UpdatedAt != first.UpdatedAt {
		t.Fatal("partial book update")
	}
	edits[1].Expected = second.UpdatedAt
	if err = c.EditMany(ctx, 2, edits); err != nil {
		t.Fatal(err)
	}
	first, _ = c.Item(ctx, 2, first.ID)
	edits = []library.ItemEdit{{ID: first.ID, Append: true, EditOptions: library.EditOptions{Expected: first.UpdatedAt, Metadata: map[string]any{"tags": []any{"shared", "new"}}}}}
	if err = c.EditMany(ctx, 2, edits); err != nil {
		t.Fatal(err)
	}
	first, _ = c.Item(ctx, 2, first.ID)
	if len(first.Tags) != 3 || first.Tags[0] != "first" || first.Tags[1] != "SHARED" || first.Tags[2] != "new" {
		t.Fatal("append replaced or duplicated tags", first.Tags)
	}
	if err = c.EditMany(ctx, 1, edits); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("unauthorized bulk edit", err)
	}
}
