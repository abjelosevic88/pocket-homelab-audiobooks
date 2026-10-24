package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	var doc sourceRow
	if err = json.Unmarshal(it.Document, &doc); err != nil {
		return err
	}
	media := object(doc["media"])
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	res, err := tx.ExecContext(ctx, `UPDATE catalog_items SET title=?,author=?,explicit=?,tags=?,document=?,updated_at=? WHERE id=? AND updated_at=?`, it.Title, it.Author, it.Explicit, raw(it.Tags), raw(doc), updated, id, in.Expected)
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
	return tx.Commit()
}

type NewLibrary struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Path      string `json:"path"`
}

func (c *Catalog) CreateLibrary(ctx context.Context, uid int64, in NewLibrary) (string, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return "", err
	}
	if !a.Permissions.Update || !a.Permissions.AllLibraries || !a.Permissions.AllTags || !a.Permissions.Explicit {
		return "", ErrNotFound
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 200 || !filepath.IsAbs(in.Path) || filepath.Clean(in.Path) != in.Path || (in.MediaType != "book" && in.MediaType != "podcast") {
		return "", ErrInvalid
	}
	root, err := os.OpenRoot(in.Path)
	if err != nil {
		return "", fmt.Errorf("library directory is unavailable: %w", err)
	}
	root.Close()
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, folder, rid := newID(), newID(), newID()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM catalog_roots WHERE source_path=? AND local_path=?`, in.Path, in.Path).Scan(&existing)
	if err == nil {
		rid = existing
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_roots VALUES(?,?,?)`, rid, in.Path, in.Path); err != nil {
			return "", err
		}
	} else {
		return "", err
	}
	doc := sourceRow{"id": id, "name": name, "mediaType": in.MediaType, "settings": sourceRow{}, "displayOrder": 0}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_libraries VALUES(?,?,?,0,?)`, id, name, in.MediaType, raw(doc)); err != nil {
		return "", err
	}
	fdoc := sourceRow{"id": folder, "path": in.Path, "libraryId": id}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_entities VALUES('libraryFolders',?,?,?)`, folder, id, raw(fdoc)); err != nil {
		return "", err
	}
	return id, tx.Commit()
}
