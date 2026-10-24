package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type LibraryFolder struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}
type LibraryConfig struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	MediaType string          `json:"media_type"`
	Order     int             `json:"display_order"`
	Archived  bool            `json:"archived"`
	Revision  int64           `json:"revision"`
	Folders   []LibraryFolder `json:"folders"`
}

func (c *Catalog) CanManageLibraries(ctx context.Context, uid int64) error {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	p := a.Permissions
	if !p.Update || !p.AllLibraries || !p.AllTags || !p.Explicit {
		return ErrNotFound
	}
	return nil
}
func (c *Catalog) LibraryConfig(ctx context.Context, uid int64, id string) (LibraryConfig, error) {
	if err := c.CanManageLibraries(ctx, uid); err != nil {
		return LibraryConfig{}, err
	}
	v, err := readLibraryConfig(ctx, c.DB, id)
	if err == nil && c.BooksOnly && v.MediaType != "book" {
		return LibraryConfig{}, ErrNotFound
	}
	return v, err
}

type libraryQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readLibraryConfig(ctx context.Context, q libraryQuery, id string) (LibraryConfig, error) {
	v := LibraryConfig{Folders: []LibraryFolder{}}
	err := q.QueryRowContext(ctx, `SELECT l.id,l.name,l.media_type,l.display_order,COALESCE(s.revision,0),COALESCE(s.archived,0) FROM catalog_libraries l LEFT JOIN catalog_library_state s ON s.library_id=l.id WHERE l.id=?`, id).Scan(&v.ID, &v.Name, &v.MediaType, &v.Order, &v.Revision, &v.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id,json_extract(document,'$.path') FROM catalog_entities WHERE kind='libraryFolders' AND library_id=? ORDER BY id`, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var f LibraryFolder
		if err = rows.Scan(&f.ID, &f.Path); err != nil {
			return v, err
		}
		v.Folders = append(v.Folders, f)
	}
	return v, rows.Err()
}
func (c *Catalog) SaveLibrary(ctx context.Context, uid int64, in LibraryConfig) error {
	if c.BooksOnly && in.MediaType != "book" {
		return ErrInvalid
	}
	if c.BooksOnly && in.ID != "" {
		if _, err := c.LibraryConfig(ctx, uid, in.ID); err != nil {
			return err
		}
	}
	if err := c.CanManageLibraries(ctx, uid); err != nil {
		return err
	}
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 || len(in.Folders) < 1 || len(in.Folders) > 100 || in.Order < 0 || in.Order > 10000 || (in.MediaType != "book" && in.MediaType != "podcast") {
		return ErrInvalid
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := readLibraryConfig(ctx, tx, in.ID)
	if err != nil {
		return err
	}
	if old.Revision != in.Revision {
		return ErrConflict
	}
	var running int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_scan_runs WHERE library_id=? AND status='running'`, in.ID).Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return fmt.Errorf("%w: finish or cancel the current scan first", ErrConflict)
	}
	if in.Archived != old.Archived && !a.Permissions.Delete {
		return ErrNotFound
	}
	if in.Archived && !old.Archived {
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_play_sessions p JOIN catalog_items i ON i.id=p.item_id WHERE i.library_id=? AND p.playing=1 AND p.closed=0 AND p.updated_at>?`, in.ID, time.Now().Add(-2*time.Minute).UnixMilli()).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("%w: pause playback in this library before archiving it", ErrConflict)
		}
	}
	if old.MediaType != in.MediaType {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_items WHERE library_id=?`, in.ID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("%w: only empty libraries can change media type", ErrConflict)
		}
	}
	known := map[string]LibraryFolder{}
	for _, f := range old.Folders {
		known[f.ID] = f
	}
	retained := map[string]bool{}
	for i := range in.Folders {
		f := &in.Folders[i]
		if f.ID != "" {
			if before, ok := known[f.ID]; !ok || before.Path != f.Path || retained[f.ID] {
				return ErrInvalid
			}
			retained[f.ID] = true
		} else {
			if err = validateLibraryFolder(f.Path); err != nil {
				return err
			}
			f.ID = newID()
		}
		for j := 0; j < i; j++ {
			if under(f.Path, in.Folders[j].Path) || under(in.Folders[j].Path, f.Path) {
				return fmt.Errorf("%w: library folders must not overlap", ErrInvalid)
			}
		}
	}
	for id, f := range known {
		if retained[id] {
			continue
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_items WHERE library_id=? AND (json_extract(document,'$.item.libraryFolderId')=? OR json_extract(document,'$.item.path')=? OR substr(json_extract(document,'$.item.path'),1,length(?)+1)=?||'/')`, in.ID, id, f.Path, f.Path, f.Path).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("%w: this folder still contains indexed items, including trash; move them before removing the folder", ErrConflict)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM catalog_entities WHERE kind='libraryFolders' AND id=? AND library_id=?`, id, in.ID); err != nil {
			return err
		}
	}
	for _, f := range in.Folders {
		if retained[f.ID] {
			continue
		}
		if _, err = ensureLibraryRoot(ctx, tx, f.Path); err != nil {
			return err
		}
		doc := sourceRow{"id": f.ID, "path": f.Path, "libraryId": in.ID}
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_entities VALUES('libraryFolders',?,?,?)`, f.ID, in.ID, raw(doc)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_libraries SET name=?,media_type=?,display_order=?,document=json_set(document,'$.name',?,'$.mediaType',?,'$.displayOrder',?) WHERE id=?`, in.Name, in.MediaType, in.Order, in.Name, in.MediaType, in.Order, in.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_library_state VALUES(?,?,?) ON CONFLICT(library_id) DO UPDATE SET revision=excluded.revision,archived=excluded.archived`, in.ID, in.Revision+1, in.Archived); err != nil {
		return err
	}
	if in.Archived {
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_podcast_jobs SET status='cancelled',error='Library archived' WHERE item_id IN (SELECT id FROM catalog_items WHERE library_id=?) AND status IN ('queued','running')`, in.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func validateLibraryFolder(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalid
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return fmt.Errorf("library directory is unavailable: %w", err)
	}
	return root.Close()
}
func ensureLibraryRoot(ctx context.Context, tx *sql.Tx, path string) (string, error) {
	var id, local string
	err := tx.QueryRowContext(ctx, `SELECT id,local_path FROM catalog_roots WHERE source_path=?`, path).Scan(&id, &local)
	if err == nil {
		if local != path {
			return "", fmt.Errorf("%w: this source path has a different configured mount", ErrConflict)
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = newID()
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_roots VALUES(?,?,?)`, id, path, path)
	return id, err
}

type LibraryOrder struct {
	ID    string `json:"id"`
	Order int    `json:"newOrder"`
}

func (c *Catalog) ReorderLibraries(ctx context.Context, uid int64, order []LibraryOrder) error {
	if err := c.CanManageLibraries(ctx, uid); err != nil {
		return err
	}
	if len(order) == 0 || len(order) > 1000 {
		return ErrInvalid
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	for _, o := range order {
		if seen[o.ID] || o.Order < 0 || o.Order > 10000 {
			return ErrInvalid
		}
		seen[o.ID] = true
		v, e := readLibraryConfig(ctx, tx, o.ID)
		if e != nil {
			return e
		}
		if v.Archived {
			return ErrNotFound
		}
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_libraries SET display_order=?,document=json_set(document,'$.displayOrder',?) WHERE id=?`, o.Order, o.Order, o.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_library_state VALUES(?,?,0) ON CONFLICT(library_id) DO UPDATE SET revision=excluded.revision`, o.ID, v.Revision+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}
