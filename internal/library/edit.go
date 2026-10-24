package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type EditOptions struct {
	Expected int64          `json:"expected_updated_at"`
	Metadata map[string]any `json:"metadata"`
	Chapters *[]Chapter     `json:"chapters,omitempty"`
}

func (c *Catalog) Edit(ctx context.Context, uid int64, id string, in EditOptions) error {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	if !a.Permissions.Update {
		return ErrNotFound
	}
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		return err
	}
	if in.Expected != it.UpdatedAt {
		return ErrConflict
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = editItem(ctx, tx, a, it, in); err != nil {
		return err
	}
	return tx.Commit()
}

// editItem shares validation and optimistic concurrency across single edits,
// atomic bulk changes and metadata discovered by the scanner.
func editItem(ctx context.Context, tx *sql.Tx, a Account, it Item, in EditOptions) error {
	var doc sourceRow
	if err := json.Unmarshal(it.Document, &doc); err != nil {
		return err
	}
	media := object(doc["media"])
	for k, v := range in.Metadata {
		switch k {
		case "title", "subtitle", "description", "publisher", "publishedYear", "publishedDate", "isbn", "asin", "language":
			value, ok := v.(string)
			if !ok || len(value) > 100000 || (k != "description" && len(value) > 2048) {
				return ErrInvalid
			}
			if k == "title" {
				if strings.TrimSpace(value) == "" {
					return ErrInvalid
				}
				it.Title = value
			}
			media[k] = value
		case "explicit", "abridged":
			value, ok := v.(bool)
			if !ok {
				return ErrInvalid
			}
			media[k] = value
			if k == "explicit" {
				it.Explicit = value
			}
		case "genres", "narrators", "tags":
			list, ok := v.([]any)
			if !ok || len(list) > 100 {
				return ErrInvalid
			}
			values := []string{}
			for _, entry := range list {
				value, ok := entry.(string)
				if !ok || len(value) > 256 {
					return ErrInvalid
				}
				value = strings.TrimSpace(value)
				if value != "" {
					values = append(values, value)
				}
			}
			media[k] = values
			if k == "tags" {
				it.Tags = values
			}
		case "authors", "series":
			list, ok := v.([]any)
			if !ok || len(list) > 100 {
				return ErrInvalid
			}
			refs := []any{}
			names := []string{}
			for _, entry := range list {
				obj, ok := entry.(map[string]any)
				if !ok {
					return ErrInvalid
				}
				name, ok := obj["name"].(string)
				name = strings.TrimSpace(name)
				if !ok || name == "" || len(name) > 512 {
					return ErrInvalid
				}
				entityID := newID()
				var existing string
				query := `SELECT id FROM catalog_entities WHERE kind=? AND lower(json_extract(document,'$.name'))=lower(?)`
				args := []any{k, name}
				var lib any
				if k == "series" {
					query += ` AND library_id=?`
					args = append(args, it.LibraryID)
					lib = it.LibraryID
				}
				e := tx.QueryRowContext(ctx, query+` LIMIT 1`, args...).Scan(&existing)
				if e == nil {
					entityID = existing
				} else if !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				ref := sourceRow{"id": entityID, "name": name}
				if k == "series" {
					if seq, ok := obj["sequence"].(string); ok {
						if len(seq) > 64 {
							return ErrInvalid
						}
						ref["sequence"] = seq
					}
				}
				if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO catalog_entities VALUES(?,?,?,?)`, k, entityID, lib, raw(ref)); e != nil {
					return e
				}
				refs = append(refs, ref)
				names = append(names, name)
			}
			doc[k] = refs
			if k == "authors" {
				it.Author = strings.Join(names, ", ")
			}
		default:
			return fmt.Errorf("%w: unsupported metadata field %s", ErrInvalid, k)
		}
	}
	if !a.allows(it) {
		return ErrNotFound
	}
	if in.Chapters != nil {
		if len(*in.Chapters) > 10000 {
			return ErrInvalid
		}
		last := 0.0
		for i := range *in.Chapters {
			ch := &(*in.Chapters)[i]
			if !validSeconds(ch.Start) || !validSeconds(ch.End) || ch.Start < last || ch.End < ch.Start || ch.End > it.Duration+1 || len(ch.Title) > 4096 {
				return ErrInvalid
			}
			ch.ID = i
			last = ch.End
		}
		media["chapters"] = *in.Chapters
	}
	doc["media"] = media
	updated := max(it.UpdatedAt+1, time.Now().UnixMilli())
	res, err := tx.ExecContext(ctx, `UPDATE catalog_items SET title=?,author=?,explicit=?,tags=?,document=?,updated_at=? WHERE id=? AND updated_at=?`, it.Title, it.Author, it.Explicit, raw(it.Tags), raw(doc), updated, it.ID, in.Expected)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

type NewLibrary struct {
	Name      string   `json:"name"`
	MediaType string   `json:"media_type"`
	Path      string   `json:"path"`
	Paths     []string `json:"paths,omitempty"`
}

func (c *Catalog) CreateLibrary(ctx context.Context, uid int64, in NewLibrary) (string, error) {
	if err := c.CanManageLibraries(ctx, uid); err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	paths := in.Paths
	if len(paths) == 0 && in.Path != "" {
		paths = []string{in.Path}
	}
	if name == "" || len(name) > 200 || len(paths) == 0 || len(paths) > 100 || (in.MediaType != "book" && in.MediaType != "podcast") {
		return "", ErrInvalid
	}
	for i, path := range paths {
		if err := validateLibraryFolder(path); err != nil {
			return "", err
		}
		for j := 0; j < i; j++ {
			if under(path, paths[j]) || under(paths[j], path) {
				return "", fmt.Errorf("%w: library folders must not overlap", ErrInvalid)
			}
		}
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id := newID()
	var order int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(display_order),-1)+1 FROM catalog_libraries`).Scan(&order); err != nil {
		return "", err
	}
	doc := sourceRow{"id": id, "name": name, "mediaType": in.MediaType, "settings": sourceRow{}, "displayOrder": order}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_libraries VALUES(?,?,?,?,?)`, id, name, in.MediaType, order, raw(doc)); err != nil {
		return "", err
	}
	for _, path := range paths {
		if _, err = ensureLibraryRoot(ctx, tx, path); err != nil {
			return "", err
		}
		folder := newID()
		fdoc := sourceRow{"id": folder, "path": path, "libraryId": id}
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_entities VALUES('libraryFolders',?,?,?)`, folder, id, raw(fdoc)); err != nil {
			return "", err
		}
	}
	return id, tx.Commit()
}
