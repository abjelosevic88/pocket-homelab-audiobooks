package library

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type ItemEdit struct {
	ID string `json:"id"`
	EditOptions
	Append bool `json:"append"`
}

// EditMany commits every selected book together. A stale/inaccessible item or
// invalid field rolls back metadata and any newly created author/series rows.
func (c *Catalog) EditMany(ctx context.Context, uid int64, edits []ItemEdit) error {
	if len(edits) == 0 || len(edits) > 100 {
		return ErrInvalid
	}
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	if !a.Permissions.Update {
		return ErrNotFound
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	for _, edit := range edits {
		if seen[edit.ID] {
			return ErrInvalid
		}
		seen[edit.ID] = true
		it, e := scanItem(tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE id=? AND NOT EXISTS(SELECT 1 FROM catalog_removed_items r WHERE r.item_id=catalog_items.id) AND NOT EXISTS(SELECT 1 FROM catalog_library_state l WHERE l.library_id=catalog_items.library_id AND l.archived=1)`, edit.ID))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if !a.allows(it) {
			return ErrNotFound
		}
		if it.MediaType != "book" {
			return ErrInvalid
		}
		if it.UpdatedAt != edit.Expected {
			return ErrConflict
		}
		fields := map[string]any{}
		for k, v := range edit.Metadata {
			fields[k] = v
		}
		if edit.Append {
			doc := objectJSON(string(it.Document))
			media := object(doc["media"])
			for _, key := range []string{"genres", "narrators", "tags", "authors", "series"} {
				value, exists := fields[key]
				if !exists {
					continue
				}
				incoming, ok := value.([]any)
				if !ok {
					return ErrInvalid
				}
				prior := array(media[key])
				if key == "tags" {
					prior = []any{}
					for _, v := range it.Tags {
						prior = append(prior, v)
					}
				}
				if key == "authors" || key == "series" {
					prior = array(doc[key])
				}
				merged := []any{}
				names := map[string]bool{}
				for _, v := range append(append([]any{}, prior...), incoming...) {
					name := str(v)
					if key == "authors" || key == "series" {
						name = str(object(v)["name"])
					}
					name = strings.ToLower(strings.TrimSpace(name))
					if !names[name] {
						names[name] = true
						merged = append(merged, v)
					}
				}
				fields[key] = merged
			}
		}
		edit.Metadata = fields
		if err = editItem(ctx, tx, a, it, edit.EditOptions); err != nil {
			return err
		}
	}
	return tx.Commit()
}
