// Package library owns Pocket Audiobooks' native media catalog. It uses local
// SQLite and files only; it has no dependency on the Audiobookshelf client/server.
package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
)

var ErrNotFound = errors.New("catalog entry not found")
var ErrImported = errors.New("native catalog already contains an import; use a fresh staging database")

type Catalog struct{ DB *sql.DB }

type Permissions struct {
	Download         bool     `json:"download"`
	Upload           bool     `json:"upload"`
	Update           bool     `json:"update"`
	Delete           bool     `json:"delete"`
	AllLibraries     bool     `json:"accessAllLibraries"`
	AllTags          bool     `json:"accessAllTags"`
	Explicit         bool     `json:"accessExplicitContent"`
	DenySelectedTags bool     `json:"selectedTagsNotAccessible"`
	Libraries        []string `json:"librariesAccessible"`
	Tags             []string `json:"itemTagsSelected"`
}

type Account struct {
	ID          string
	Permissions Permissions
}

// Account never falls back to the owner's identity or grants library access on
// the basis of Pocket's admin role. Mapping is an explicit migration operation.
func (c *Catalog) Account(ctx context.Context, userID int64) (Account, error) {
	var a Account
	var raw string
	err := c.DB.QueryRowContext(ctx, `SELECT id,permissions FROM catalog_accounts WHERE pocket_user_id=? AND active=1 AND locked=0`, userID).Scan(&a.ID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal([]byte(raw), &a.Permissions); err != nil {
		return a, err
	}
	return a, nil
}
func (a Account) allowsLibrary(id string) bool {
	return a.Permissions.AllLibraries || slices.Contains(a.Permissions.Libraries, id)
}
func (a Account) allows(it Item) bool {
	p := a.Permissions
	if !a.allowsLibrary(it.LibraryID) || (it.Explicit && !p.Explicit) {
		return false
	}
	if p.AllTags {
		return true
	}
	matches := false
	for _, tag := range it.Tags {
		if slices.Contains(p.Tags, tag) {
			matches = true
			break
		}
	}
	if p.DenySelectedTags {
		return !matches
	}
	return matches
}

type Library struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Count     int    `json:"count"`
}
type Item struct {
	ID        string          `json:"id"`
	LibraryID string          `json:"library_id"`
	MediaID   string          `json:"media_id"`
	MediaType string          `json:"media_type"`
	Title     string          `json:"title"`
	Author    string          `json:"author"`
	Duration  float64         `json:"duration"`
	Explicit  bool            `json:"explicit"`
	Missing   bool            `json:"missing"`
	AddedAt   int64           `json:"added_at"`
	UpdatedAt int64           `json:"updated_at"`
	Tags      []string        `json:"tags"`
	Document  json.RawMessage `json:"document,omitempty"`
}

const itemColumns = `id,library_id,media_id,media_type,title,author,duration,explicit,missing,added_at,updated_at,tags,document`

type scanner interface{ Scan(...any) error }

func scanItem(row scanner) (Item, error) {
	var it Item
	var tags, document string
	err := row.Scan(&it.ID, &it.LibraryID, &it.MediaID, &it.MediaType, &it.Title, &it.Author, &it.Duration, &it.Explicit, &it.Missing, &it.AddedAt, &it.UpdatedAt, &tags, &document)
	if err != nil {
		return it, err
	}
	if err = json.Unmarshal([]byte(tags), &it.Tags); err != nil {
		return it, err
	}
	it.Document = json.RawMessage(document)
	return it, nil
}
func (c *Catalog) Item(ctx context.Context, userID int64, id string) (Item, error) {
	a, err := c.Account(ctx, userID)
	if err != nil {
		return Item{}, err
	}
	it, err := scanItem(c.DB.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	if !a.allows(it) {
		return Item{}, ErrNotFound
	}
	return it, nil
}

type Query struct {
	LibraryID, Search, MediaType string
	Offset, Limit                int
}
type Page struct {
	Items  []Item `json:"items"`
	Total  int    `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

func (c *Catalog) Items(ctx context.Context, userID int64, q Query) (Page, error) {
	a, err := c.Account(ctx, userID)
	if err != nil {
		return Page{}, err
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	out := Page{Items: []Item{}, Limit: q.Limit, Offset: q.Offset}
	if q.LibraryID != "" && !a.allowsLibrary(q.LibraryID) {
		return out, ErrNotFound
	}
	// User text is a literal substring, not a SQL LIKE expression. Filtering and
	// counting both happen after permissions, including explicit/tag restrictions.
	rows, err := c.DB.QueryContext(ctx, `SELECT `+strings.TrimSuffix(itemColumns, ",document")+`,'' AS document FROM catalog_items WHERE (?='' OR library_id=?) AND (?='' OR media_type=?) ORDER BY title COLLATE NOCASE,id`, q.LibraryID, q.LibraryID, q.MediaType, q.MediaType)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	search := strings.ToLower(strings.TrimSpace(q.Search))
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return out, err
		}
		if !a.allows(it) || (search != "" && !strings.Contains(strings.ToLower(it.Title+" "+it.Author), search)) {
			continue
		}
		if out.Total >= q.Offset && len(out.Items) < q.Limit {
			it.Document = nil
			out.Items = append(out.Items, it)
		}
		out.Total++
	}
	return out, rows.Err()
}
func (c *Catalog) Libraries(ctx context.Context, userID int64) ([]Library, error) {
	a, err := c.Account(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT id,name,media_type FROM catalog_libraries ORDER BY display_order,id`)
	if err != nil {
		return nil, err
	}
	result := []Library{}
	for rows.Next() {
		var l Library
		if err = rows.Scan(&l.ID, &l.Name, &l.MediaType); err != nil {
			rows.Close()
			return nil, err
		}
		if a.allowsLibrary(l.ID) {
			result = append(result, l)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		page, err := c.Items(ctx, userID, Query{LibraryID: result[i].ID, Limit: 1})
		if err != nil {
			return nil, err
		}
		result[i].Count = page.Total
	}
	return result, nil
}

type File struct {
	ID       string  `json:"id"`
	Role     string  `json:"role"`
	Name     string  `json:"name"`
	MIME     string  `json:"mime"`
	Size     int64   `json:"size"`
	Duration float64 `json:"duration"`
	Index    int     `json:"index"`
	Excluded bool    `json:"excluded"`
}

func (c *Catalog) Files(ctx context.Context, userID int64, itemID string) ([]File, error) {
	if _, err := c.Item(ctx, userID, itemID); err != nil {
		return nil, err
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT id,role,name,mime,size,duration,track_index,excluded FROM catalog_files WHERE item_id=? ORDER BY track_index,id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		var f File
		if err = rows.Scan(&f.ID, &f.Role, &f.Name, &f.MIME, &f.Size, &f.Duration, &f.Index, &f.Excluded); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// OpenFile uses os.Root to prevent symlink/path escapes even if filesystem
// entries change between request validation and open. Only indexed files open.
func (c *Catalog) OpenFile(ctx context.Context, userID int64, itemID, fileID string, download bool) (*os.File, File, error) {
	var f File
	if _, err := c.Item(ctx, userID, itemID); err != nil {
		return nil, f, err
	}
	var root, relative string
	err := c.DB.QueryRowContext(ctx, `SELECT f.id,f.role,f.name,f.mime,f.size,f.duration,f.track_index,f.excluded,r.local_path,f.relative_path FROM catalog_files f JOIN catalog_roots r ON r.id=f.root_id WHERE f.item_id=? AND f.id=?`, itemID, fileID).Scan(&f.ID, &f.Role, &f.Name, &f.MIME, &f.Size, &f.Duration, &f.Index, &f.Excluded, &root, &relative)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, f, ErrNotFound
	}
	if err != nil {
		return nil, f, err
	}
	a, err := c.Account(ctx, userID)
	if err != nil {
		return nil, f, err
	}
	if (download || f.Role == "supplement" || f.Role == "ebook") && !a.Permissions.Download {
		return nil, f, ErrNotFound
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, f, err
	}
	defer dir.Close()
	file, err := dir.Open(relative)
	if err != nil {
		return nil, f, fmt.Errorf("open catalog file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, f, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, f, fs.ErrPermission
	}
	return file, f, nil
}

// Progress includes only the person's own records for currently accessible items.
func (c *Catalog) Progress(ctx context.Context, userID int64) ([]json.RawMessage, error) {
	a, err := c.Account(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT p.document,i.library_id,i.explicit,i.tags FROM catalog_progress p JOIN catalog_items i ON i.id=p.item_id WHERE p.account_id=? ORDER BY p.updated_at DESC,p.id`, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var doc, tags string
		var it Item
		if err = rows.Scan(&doc, &it.LibraryID, &it.Explicit, &tags); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(tags), &it.Tags); err != nil {
			return nil, err
		}
		if a.allows(it) {
			out = append(out, json.RawMessage(doc))
		}
	}
	return out, rows.Err()
}
