package library

// Author profiles: biography, ASIN and photo live on the author entity
// (catalog_entities kind 'authors', an Audiobookshelf-shaped document with
// name, asin, description and imagePath). An author is visible to a person
// when at least one book they can open credits it.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Author is an author's profile as the API presents it.
type Author struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ASIN        string `json:"asin"`
	Description string `json:"description"`
	ImagePath   string `json:"-"`
	LastMatched int64  `json:"last_matched"`
	UpdatedAt   int64  `json:"updated_at"`
	Books       int    `json:"books"`
}

// AuthorEdit changes an author's profile; nil fields are left alone.
type AuthorEdit struct {
	Name, Description, ASIN, ImagePath *string
	// Matched records the time of a successful provider match.
	Matched bool
}

var authorASIN = regexp.MustCompile(`^[0-9A-Z]{10}$`)

// authorItems lists the books crediting an author, visible or not.
func authorItems(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT i.id FROM catalog_items i, json_each(i.document,'$.authors') a WHERE json_extract(a.value,'$.id')=? ORDER BY i.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func authorEntity(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (sourceRow, bool, error) {
	var doc string
	err := q.QueryRowContext(ctx, `SELECT document FROM catalog_entities WHERE kind='authors' AND id=?`, id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return sourceRow{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var d sourceRow
	if err = json.Unmarshal([]byte(doc), &d); err != nil {
		return nil, false, err
	}
	return d, true, nil
}

// Author returns a profile the person can see, counting their visible books.
func (c *Catalog) Author(ctx context.Context, uid int64, id string) (Author, error) {
	out := Author{ID: id}
	if id == "" || len(id) > 200 {
		return out, ErrNotFound
	}
	items, err := authorItems(ctx, c.DB, id)
	if err != nil {
		return out, err
	}
	name := ""
	for _, item := range items {
		it, err := c.Item(ctx, uid, item)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		out.Books++
		if name == "" {
			var doc sourceRow
			_ = json.Unmarshal(it.Document, &doc)
			for _, a := range array(doc["authors"]) {
				if o := object(a); str(o["id"]) == id {
					name = str(o["name"])
				}
			}
		}
	}
	if out.Books == 0 {
		return out, ErrNotFound
	}
	d, _, err := authorEntity(ctx, c.DB, id)
	if err != nil {
		return out, err
	}
	out.Name = firstText(str(d["name"]), name)
	out.ASIN, out.Description, out.ImagePath = str(d["asin"]), str(d["description"]), str(d["imagePath"])
	out.LastMatched, out.UpdatedAt = entityMillis(d["lastMatched"]), entityMillis(d["updatedAt"])
	return out, nil
}

// entityMillis reads a JSON number or Audiobookshelf timestamp as milliseconds.
func entityMillis(v any) int64 {
	if f, ok := v.(float64); ok && f > 0 {
		return int64(f)
	}
	n, _ := milliseconds(v)
	return n
}

func firstText(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// EditAuthor saves profile changes. A new name is written to every book that
// credits the author, so lists and search stay consistent.
func (c *Catalog) EditAuthor(ctx context.Context, uid int64, id string, in AuthorEdit) (Author, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return Author{}, err
	}
	if !a.Permissions.Update {
		return Author{}, ErrNotFound
	}
	cur, err := c.Author(ctx, uid, id)
	if err != nil {
		return cur, err
	}
	if in.Name != nil {
		v := strings.TrimSpace(*in.Name)
		if v == "" || len(v) > 512 {
			return cur, fmt.Errorf("%w: enter a name up to 512 characters", ErrInvalid)
		}
		in.Name = &v
	}
	if in.Description != nil {
		v := strings.TrimSpace(*in.Description)
		if len(v) > 100000 {
			return cur, fmt.Errorf("%w: the biography is too long", ErrInvalid)
		}
		in.Description = &v
	}
	if in.ASIN != nil {
		v := strings.ToUpper(strings.TrimSpace(*in.ASIN))
		if v != "" && !authorASIN.MatchString(v) {
			return cur, fmt.Errorf("%w: an ASIN has ten letters and digits", ErrInvalid)
		}
		in.ASIN = &v
	}
	if in.ImagePath != nil && len(*in.ImagePath) > 4096 {
		return cur, ErrInvalid
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return cur, err
	}
	defer tx.Rollback()
	doc, exists, err := authorEntity(ctx, tx, id)
	if err != nil {
		return cur, err
	}
	if !exists {
		doc = sourceRow{"id": id, "name": cur.Name}
	}
	now := time.Now().UnixMilli()
	if in.Name != nil && *in.Name != cur.Name {
		var other string
		e := tx.QueryRowContext(ctx, `SELECT id FROM catalog_entities WHERE kind='authors' AND id<>? AND lower(json_extract(document,'$.name'))=lower(?) LIMIT 1`, id, *in.Name).Scan(&other)
		if e == nil {
			return cur, fmt.Errorf("%w: another author is already called %s", ErrConflict, *in.Name)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return cur, e
		}
		if err = renameAuthorInItems(ctx, tx, id, *in.Name, now); err != nil {
			return cur, err
		}
		doc["name"] = *in.Name
	}
	if in.Description != nil {
		doc["description"] = *in.Description
	}
	if in.ASIN != nil {
		doc["asin"] = *in.ASIN
	}
	if in.ImagePath != nil {
		if *in.ImagePath == "" {
			doc["imagePath"] = nil
		} else {
			doc["imagePath"] = *in.ImagePath
		}
	}
	if in.Matched {
		doc["lastMatched"] = now
	}
	doc["updatedAt"] = max(now, cur.UpdatedAt+1)
	if exists {
		_, err = tx.ExecContext(ctx, `UPDATE catalog_entities SET document=? WHERE kind='authors' AND id=?`, raw(doc), id)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO catalog_entities VALUES('authors',?,NULL,?)`, id, raw(doc))
	}
	if err != nil {
		return cur, err
	}
	if err = tx.Commit(); err != nil {
		return cur, err
	}
	return c.Author(ctx, uid, id)
}

func renameAuthorInItems(ctx context.Context, tx *sql.Tx, id, name string, now int64) error {
	items, err := authorItems(ctx, tx, id)
	if err != nil {
		return err
	}
	for _, item := range items {
		var rawDoc string
		var updated int64
		if err = tx.QueryRowContext(ctx, `SELECT document,updated_at FROM catalog_items WHERE id=?`, item).Scan(&rawDoc, &updated); err != nil {
			return err
		}
		var doc sourceRow
		if err = json.Unmarshal([]byte(rawDoc), &doc); err != nil {
			return err
		}
		refs, names := []any{}, []string{}
		for _, v := range array(doc["authors"]) {
			o := object(v)
			if str(o["id"]) == id {
				o["name"] = name
			}
			refs = append(refs, o)
			if n := str(o["name"]); n != "" {
				names = append(names, n)
			}
		}
		doc["authors"] = refs
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_items SET author=?,document=?,updated_at=? WHERE id=?`, strings.Join(names, ", "), raw(doc), max(updated+1, now), item); err != nil {
			return err
		}
	}
	return nil
}

// AuthorTargets lists the authors of a library's accessible books for a bulk
// author match. onlyMissing keeps authors without a photo or bio.
func (c *Catalog) AuthorTargets(ctx context.Context, uid int64, libraryID string, onlyMissing bool) ([]string, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	if !a.Permissions.Update {
		return nil, ErrNotFound
	}
	if libraryID == "" {
		return nil, ErrInvalid
	}
	var exists int
	if err = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_libraries WHERE id=?`, libraryID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 || !a.allowsLibrary(libraryID) {
		return nil, ErrNotFound
	}
	found, seen := []string{}, map[string]bool{}
	for offset := 0; ; offset += 200 {
		page, err := c.Items(ctx, uid, Query{LibraryID: libraryID, MediaType: "book", Offset: offset, Limit: 200})
		if err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			rows, err := c.DB.QueryContext(ctx, `SELECT json_extract(a.value,'$.id') FROM catalog_items i, json_each(i.document,'$.authors') a WHERE i.id=? AND json_type(a.value,'$.id')='text'`, it.ID)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				if id != "" && !seen[id] {
					seen[id] = true
					found = append(found, id)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		if len(page.Items) < 200 || len(found) >= 20000 {
			break
		}
	}
	out := []string{}
	for _, id := range found {
		if onlyMissing {
			d, _, err := authorEntity(ctx, c.DB, id)
			if err != nil {
				return nil, err
			}
			if str(d["imagePath"]) != "" && strings.TrimSpace(str(d["description"])) != "" {
				continue
			}
		}
		out = append(out, id)
	}
	return out, nil
}
