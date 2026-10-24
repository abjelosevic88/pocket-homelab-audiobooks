package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type ListEntry struct {
	ItemID    string `json:"libraryItemId"`
	EpisodeID string `json:"episodeId,omitempty"`
}
type MediaList struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"`
	LibraryID   string      `json:"libraryId"`
	AccountID   string      `json:"userId,omitempty"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	CreatedAt   int64       `json:"createdAt"`
	UpdatedAt   int64       `json:"updatedAt"`
	Revision    int64       `json:"revision"`
	Entries     []ListEntry `json:"entries"`
}
type ListEdit struct {
	Name        *string
	Description *string
	Entries     *[]ListEntry
	Expected    *int64
}

func listKind(kind string) bool { return kind == "collections" || kind == "playlists" }
func (c *Catalog) mediaList(ctx context.Context, a Account, kind, id string) (MediaList, sourceRow, error) {
	out := MediaList{Kind: kind, Entries: []ListEntry{}}
	if !listKind(kind) {
		return out, nil, ErrNotFound
	}
	var rawDoc string
	err := c.DB.QueryRowContext(ctx, `SELECT document FROM catalog_entities WHERE kind=? AND id=?`, kind, id).Scan(&rawDoc)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil, ErrNotFound
	}
	if err != nil {
		return out, nil, err
	}
	var d sourceRow
	if err = json.Unmarshal([]byte(rawDoc), &d); err != nil {
		return out, nil, err
	}
	out.ID, out.LibraryID, out.AccountID, out.Name, out.Description = id, str(d["libraryId"]), str(d["userId"]), str(d["name"]), str(d["description"])
	if !a.allowsLibrary(out.LibraryID) || (kind == "playlists" && out.AccountID != a.ID) {
		return out, nil, ErrNotFound
	}
	out.CreatedAt, _ = milliseconds(d["createdAt"])
	out.UpdatedAt, _ = milliseconds(d["updatedAt"])
	out.Revision = int64(number(d["nativeRevision"]))
	if entries, ok := d["nativeEntries"]; ok {
		if err = json.Unmarshal([]byte(raw(entries)), &out.Entries); err != nil {
			return out, nil, err
		}
		return out, d, nil
	}
	// Old associations remain lossless in catalog_entities. On first edit, write
	// an explicit native ordered list while keeping the source rows for audit.
	relation, fk := "collectionBooks", "collectionId"
	if kind == "playlists" {
		relation, fk = "playlistMediaItems", "playlistId"
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT document FROM catalog_entities WHERE kind=? AND json_extract(document,?)=? ORDER BY CAST(json_extract(document,'$.order') AS INTEGER),id`, relation, "$."+fk, id)
	if err != nil {
		return out, nil, err
	}
	refs := []sourceRow{}
	for rows.Next() {
		var doc string
		if err = rows.Scan(&doc); err != nil {
			rows.Close()
			return out, nil, err
		}
		var r sourceRow
		if err = json.Unmarshal([]byte(doc), &r); err != nil {
			rows.Close()
			return out, nil, err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, nil, err
	}
	for _, r := range refs {
		mediaID := str(r["bookId"])
		episode := ""
		if kind == "playlists" {
			mediaID = str(r["mediaItemId"])
			if str(r["mediaItemType"]) == "podcastEpisode" {
				episode = mediaID
				if err = c.DB.QueryRowContext(ctx, `SELECT json_extract(document,'$.podcastId') FROM catalog_entities WHERE kind='podcastEpisodes' AND id=?`, episode).Scan(&mediaID); err != nil {
					return out, nil, err
				}
			}
		}
		var item string
		if err = c.DB.QueryRowContext(ctx, `SELECT id FROM catalog_items WHERE media_id=?`, mediaID).Scan(&item); err != nil {
			return out, nil, err
		}
		out.Entries = append(out.Entries, ListEntry{ItemID: item, EpisodeID: episode})
	}
	return out, d, nil
}
func (c *Catalog) GetList(ctx context.Context, uid int64, kind, id string) (MediaList, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return MediaList{}, err
	}
	out, _, err := c.mediaList(ctx, a, kind, id)
	if err != nil {
		return out, err
	}
	visible := []ListEntry{}
	for _, e := range out.Entries {
		if _, err = c.Item(ctx, uid, e.ItemID); err == nil {
			visible = append(visible, e)
		} else if !errors.Is(err, ErrNotFound) {
			return out, err
		}
	}
	out.Entries = visible
	return out, nil
}
func (c *Catalog) Lists(ctx context.Context, uid int64, kind, lib string) ([]MediaList, error) {
	if !listKind(kind) {
		return nil, ErrNotFound
	}
	a, err := c.Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	if lib != "" && !a.allowsLibrary(lib) {
		return nil, ErrNotFound
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT id FROM catalog_entities WHERE kind=? AND (?='' OR library_id=?) ORDER BY lower(json_extract(document,'$.name')),id`, kind, lib, lib)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []MediaList{}
	for _, id := range ids {
		l, e := c.GetList(ctx, uid, kind, id)
		if errors.Is(e, ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, l)
	}
	return out, nil
}
func (c *Catalog) validateEntries(ctx context.Context, uid int64, kind, lib string, entries []ListEntry) error {
	if len(entries) > 10000 {
		return ErrInvalid
	}
	seen := map[ListEntry]bool{}
	for _, entry := range entries {
		if seen[entry] {
			return ErrInvalid
		}
		seen[entry] = true
		it, err := c.Item(ctx, uid, entry.ItemID)
		if err != nil {
			return err
		}
		if it.LibraryID != lib {
			return ErrInvalid
		}
		if kind == "collections" && it.MediaType != "book" {
			return ErrInvalid
		}
		if entry.EpisodeID != "" {
			var doc sourceRow
			if err = json.Unmarshal(it.Document, &doc); err != nil {
				return err
			}
			found := false
			for _, e := range array(doc["episodes"]) {
				if str(object(e)["id"]) == entry.EpisodeID {
					found = true
					break
				}
			}
			if !found {
				return ErrNotFound
			}
		} else if it.MediaType == "podcast" {
			return ErrInvalid
		}
	}
	return nil
}
func (c *Catalog) CreateList(ctx context.Context, uid int64, kind, lib, name, description string, entries []ListEntry) (MediaList, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return MediaList{}, err
	}
	if !listKind(kind) || !a.allowsLibrary(lib) || (kind == "collections" && !a.Permissions.Update) {
		return MediaList{}, ErrNotFound
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 512 || len(description) > 100000 {
		return MediaList{}, ErrInvalid
	}
	var exists int
	if err = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_libraries WHERE id=?`, lib).Scan(&exists); err != nil {
		return MediaList{}, err
	}
	if exists != 1 {
		return MediaList{}, ErrNotFound
	}
	if err = c.validateEntries(ctx, uid, kind, lib, entries); err != nil {
		return MediaList{}, err
	}
	now := time.Now().UnixMilli()
	id := newID()
	owner := ""
	if kind == "playlists" {
		owner = a.ID
	}
	if entries == nil {
		entries = []ListEntry{}
	}
	doc := sourceRow{"id": id, "libraryId": lib, "name": name, "description": description, "userId": owner, "createdAt": now, "updatedAt": now, "nativeRevision": 1, "nativeEntries": entries}
	if _, err = c.DB.ExecContext(ctx, `INSERT INTO catalog_entities VALUES(?,?,?,?)`, kind, id, lib, raw(doc)); err != nil {
		return MediaList{}, err
	}
	return c.GetList(ctx, uid, kind, id)
}
func (c *Catalog) EditList(ctx context.Context, uid int64, kind, id string, in ListEdit) (MediaList, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return MediaList{}, err
	}
	old, doc, err := c.mediaList(ctx, a, kind, id)
	if err != nil {
		return old, err
	}
	if kind == "collections" && !a.Permissions.Update {
		return old, ErrNotFound
	}
	if in.Expected != nil && *in.Expected != old.Revision {
		return old, ErrConflict
	}
	previous := raw(doc)
	// Do not accidentally remove hidden entries while replacing a filtered list.
	if err = c.validateEntries(ctx, uid, kind, old.LibraryID, old.Entries); err != nil {
		return old, err
	}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" || len(name) > 512 {
			return old, ErrInvalid
		}
		doc["name"] = name
	}
	if in.Description != nil {
		if len(*in.Description) > 100000 {
			return old, ErrInvalid
		}
		doc["description"] = *in.Description
	}
	entries := old.Entries
	if in.Entries != nil {
		entries = *in.Entries
	}
	if err = c.validateEntries(ctx, uid, kind, old.LibraryID, entries); err != nil {
		return old, err
	}
	doc["nativeEntries"], doc["nativeRevision"], doc["updatedAt"] = entries, old.Revision+1, time.Now().UnixMilli()
	// JSON canonicalization is not used in the predicate: keep the source byte
	// representation to detect concurrent changes, including imported documents.
	var actual string
	if err = c.DB.QueryRowContext(ctx, `SELECT document FROM catalog_entities WHERE kind=? AND id=?`, kind, id).Scan(&actual); err != nil {
		return old, err
	}
	var current sourceRow
	if err = json.Unmarshal([]byte(actual), &current); err != nil {
		return old, err
	}
	if raw(current) != previous {
		return old, ErrConflict
	}
	res, err := c.DB.ExecContext(ctx, `UPDATE catalog_entities SET document=? WHERE kind=? AND id=? AND document=?`, raw(doc), kind, id, actual)
	if err != nil {
		return old, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return old, err
	}
	if n != 1 {
		return old, ErrConflict
	}
	return c.GetList(ctx, uid, kind, id)
}
func (c *Catalog) DeleteList(ctx context.Context, uid int64, kind, id string) error {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	list, _, err := c.mediaList(ctx, a, kind, id)
	if err != nil {
		return err
	}
	if kind == "collections" && !a.Permissions.Update {
		return ErrNotFound
	}
	if err = c.validateEntries(ctx, uid, kind, list.LibraryID, list.Entries); err != nil {
		return err
	}
	_, err = c.DB.ExecContext(ctx, `DELETE FROM catalog_entities WHERE kind=? AND id=?`, kind, id)
	return err
}
