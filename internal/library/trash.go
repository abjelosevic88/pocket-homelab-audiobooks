package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var trashMu sync.Mutex

type TrashEntry struct {
	ID        string      `json:"id"`
	ItemID    string      `json:"item_id"`
	EpisodeID string      `json:"episode_id"`
	Title     string      `json:"title"`
	MoveFiles bool        `json:"move_files"`
	State     string      `json:"state"`
	Operation string      `json:"operation"`
	CreatedAt int64       `json:"created_at"`
	UpdatedAt int64       `json:"updated_at"`
	Error     string      `json:"error,omitempty"`
	Files     []trashFile `json:"-"`
}
type trashFile struct {
	ID, RootID, Source, Destination, Identity string
	Size, Modified                            int64
	Missing, Shared                           bool
}

const trashColumns = "id,item_id,episode_id,title,move_files,state,operation,created_at,updated_at,error,files"

func scanTrash(row scanner) (TrashEntry, error) {
	var v TrashEntry
	var files string
	err := row.Scan(&v.ID, &v.ItemID, &v.EpisodeID, &v.Title, &v.MoveFiles, &v.State, &v.Operation, &v.CreatedAt, &v.UpdatedAt, &v.Error, &files)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(files), &v.Files)
	}
	return v, err
}
func (c *Catalog) trashPermission(ctx context.Context, uid int64, item string) (Item, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return Item{}, err
	}
	if !a.Permissions.Delete {
		return Item{}, ErrNotFound
	}
	it, err := scanItem(c.DB.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE id=?`, item))
	if err != nil {
		return it, err
	}
	if !a.allows(it) {
		return it, ErrNotFound
	}
	return it, nil
}
func (c *Catalog) TrashList(ctx context.Context, uid int64) ([]TrashEntry, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	if !a.Permissions.Delete {
		return nil, ErrNotFound
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT `+trashColumns+` FROM catalog_trash WHERE state<>'restored' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	all := []TrashEntry{}
	for rows.Next() {
		v, e := scanTrash(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []TrashEntry{}
	for _, v := range all {
		if _, e := c.trashPermission(ctx, uid, v.ItemID); e == nil {
			out = append(out, v)
		} else if !errors.Is(e, ErrNotFound) {
			return nil, e
		}
	}
	return out, nil
}
func (c *Catalog) Trash(ctx context.Context, uid int64, item, episode string, move bool, expected int64) (TrashEntry, error) {
	trashMu.Lock()
	defer trashMu.Unlock()
	it, err := c.Item(ctx, uid, item)
	if err != nil {
		return TrashEntry{}, err
	}
	if _, err = c.trashPermission(ctx, uid, item); err != nil {
		return TrashEntry{}, err
	}
	if it.UpdatedAt != expected {
		return TrashEntry{}, ErrConflict
	}
	v := TrashEntry{ID: newID(), ItemID: item, EpisodeID: episode, Title: it.Title, MoveFiles: move, State: "moving", Operation: "delete", CreatedAt: time.Now().UnixMilli(), Files: []trashFile{}}
	v.UpdatedAt = v.CreatedAt
	selected := ""
	if episode != "" {
		if it.MediaType != "podcast" {
			return v, ErrInvalid
		}
		doc := objectJSON(string(it.Document))
		for _, entry := range array(doc["episodes"]) {
			ep := object(entry)
			if str(ep["id"]) == episode {
				selected = str(object(ep["audioFile"])["ino"])
				v.Title = str(ep["title"])
				break
			}
		}
		if selected == "" {
			return v, ErrNotFound
		}
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT id,root_id,relative_path FROM catalog_files f WHERE item_id=? AND (?='' OR id=?) AND NOT EXISTS(SELECT 1 FROM catalog_removed_files x WHERE x.item_id=f.item_id AND x.file_id=f.id)`, item, selected, selected)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var f trashFile
		if err = rows.Scan(&f.ID, &f.RootID, &f.Source); err != nil {
			rows.Close()
			return v, err
		}
		f.Destination = filepath.Join(".pocket-trash", v.ID, scanStableID(f.RootID, f.Source)+"-"+filepath.Base(f.Source))
		v.Files = append(v.Files, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	if episode != "" && len(v.Files) == 0 {
		return v, ErrNotFound
	}
	seenPaths := map[string]bool{}
	for i := range v.Files {
		f := &v.Files[i]
		var shared int
		if err = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_files WHERE root_id=? AND relative_path=? AND item_id<>?`, f.RootID, f.Source, item).Scan(&shared); err != nil {
			return v, err
		}
		key := f.RootID + "/" + f.Source
		f.Shared = shared > 0 || seenPaths[key]
		seenPaths[key] = true
		if !move || f.Shared {
			continue
		}
		root, e := c.trashRoot(ctx, f.RootID)
		if e != nil {
			return v, e
		}
		stat, e := root.Lstat(f.Source)
		root.Close()
		if errors.Is(e, fs.ErrNotExist) {
			f.Missing = true
			continue
		}
		if e != nil {
			return v, e
		}
		if !stat.Mode().IsRegular() {
			return v, fmt.Errorf("%w: only regular indexed files can move to trash", ErrInvalid)
		}
		f.Size, f.Modified, f.Identity = stat.Size(), stat.ModTime().UnixNano(), fileIdentity(stat)
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	var version int64
	if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM catalog_items WHERE id=?`, item).Scan(&version); err != nil {
		return v, err
	}
	if version != expected {
		return v, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_trash(id,item_id,episode_id,user_id,title,move_files,state,operation,created_at,updated_at,files,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, item, episode, uid, v.Title, move, v.State, v.Operation, v.CreatedAt, v.UpdatedAt, raw(v.Files), "")
	// Explicit columns keep the journal stable when later migrations add fields.
	if err != nil {
		return v, err
	}
	if episode == "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO catalog_removed_items VALUES(?,?)`, item, v.ID)
	}
	if err != nil {
		return v, ErrConflict
	}
	for _, f := range v.Files {
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_removed_files VALUES(?,?,?)`, item, f.ID, v.ID); err != nil {
			return v, ErrConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_items SET updated_at=MAX(updated_at+1,?) WHERE id=?`, v.CreatedAt, item); err != nil {
		return v, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_podcast_jobs SET status='cancelled',error='Moved to trash' WHERE item_id=? AND (?='' OR episode_id=?) AND status IN ('queued','running')`, item, episode, episode); err != nil {
		return v, err
	}
	if err = tx.Commit(); err != nil {
		return v, err
	}
	err = c.runTrash(ctx, &v)
	return v, err
}
func (c *Catalog) trashRoot(ctx context.Context, id string) (*os.Root, error) {
	var path string
	if err := c.DB.QueryRowContext(ctx, `SELECT local_path FROM catalog_roots WHERE id=?`, id).Scan(&path); err != nil {
		return nil, err
	}
	return os.OpenRoot(path)
}
func sameTrashFile(info fs.FileInfo, f trashFile) bool {
	return info.Mode().IsRegular() && info.Size() == f.Size && info.ModTime().UnixNano() == f.Modified && fileIdentity(info) == f.Identity
}
func (c *Catalog) moveTrashFile(ctx context.Context, f trashFile, restore, purge bool) error {
	if f.Missing || f.Shared {
		return nil
	}
	root, err := c.trashRoot(ctx, f.RootID)
	if err != nil {
		return err
	}
	defer root.Close()
	from, to := f.Source, f.Destination
	if restore {
		from, to = to, from
	}
	if !filepath.IsLocal(from) || !filepath.IsLocal(to) || !strings.HasPrefix(f.Destination, ".pocket-trash"+string(filepath.Separator)) {
		return ErrInvalid
	}
	if purge {
		stat, e := root.Lstat(f.Destination)
		if errors.Is(e, fs.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if !sameTrashFile(stat, f) {
			return fmt.Errorf("%w: trash file changed; refusing removal", ErrConflict)
		}
		return root.Remove(f.Destination)
	}
	stat, err := root.Lstat(from)
	if errors.Is(err, fs.ErrNotExist) {
		existing, e := root.Lstat(to)
		if e == nil && sameTrashFile(existing, f) {
			return nil
		}
		return fmt.Errorf("%w: source and recorded destination do not match", ErrConflict)
	}
	if err != nil {
		return err
	}
	if !sameTrashFile(stat, f) {
		return fmt.Errorf("%w: source file changed; no overwrite performed", ErrConflict)
	}
	if _, e := root.Lstat(to); e == nil {
		return fmt.Errorf("%w: destination already exists", ErrConflict)
	} else if !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	if err = root.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	if err = renameNoReplace(root, from, to); err != nil {
		return err
	}
	moved, err := root.Lstat(to)
	if err != nil {
		return err
	}
	if !sameTrashFile(moved, f) {
		return fmt.Errorf("%w: file changed during move", ErrConflict)
	}
	return nil
}
func (c *Catalog) runTrash(ctx context.Context, v *TrashEntry) error {
	var operationErr error
	if v.MoveFiles {
		for _, f := range v.Files {
			if err := ctx.Err(); err != nil {
				operationErr = err
				break
			}
			if err := c.moveTrashFile(ctx, f, v.Operation == "restore", v.Operation == "purge"); err != nil {
				operationErr = err
				break
			}
		}
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if operationErr != nil {
		v.State = "error"
		v.Error = operationErr.Error()
		_, err := c.DB.ExecContext(finishCtx, `UPDATE catalog_trash SET state='error',error=?,updated_at=? WHERE id=?`, v.Error, time.Now().UnixMilli(), v.ID)
		if err != nil {
			return err
		}
		return operationErr
	}
	tx, err := c.DB.BeginTx(finishCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state := "deleted"
	if v.Operation == "restore" {
		state = "restored"
		if _, err = tx.ExecContext(finishCtx, `DELETE FROM catalog_removed_items WHERE trash_id=?`, v.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(finishCtx, `DELETE FROM catalog_removed_files WHERE trash_id=?`, v.ID); err != nil {
			return err
		}
	}
	if v.Operation == "purge" {
		state = "purged"
	}
	_, err = tx.ExecContext(finishCtx, `UPDATE catalog_trash SET state=?,error='',updated_at=? WHERE id=?`, state, time.Now().UnixMilli(), v.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(finishCtx, `UPDATE catalog_items SET updated_at=MAX(updated_at+1,?) WHERE id=?`, time.Now().UnixMilli(), v.ItemID)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	v.State = state
	v.Error = ""
	return nil
}
func (c *Catalog) TrashAction(ctx context.Context, uid int64, id, action string) (TrashEntry, error) {
	trashMu.Lock()
	defer trashMu.Unlock()
	v, err := scanTrash(c.DB.QueryRowContext(ctx, `SELECT `+trashColumns+` FROM catalog_trash WHERE id=?`, id))
	if err != nil {
		return v, err
	}
	if _, err = c.trashPermission(ctx, uid, v.ItemID); err != nil {
		return v, err
	}
	if v.State == "restored" || v.State == "purged" {
		return v, ErrConflict
	}
	if action == "restore" && v.Operation == "purge" {
		return v, ErrConflict
	}
	if (action == "restore" || action == "retry" && v.Operation == "restore") && v.EpisodeID != "" {
		var hidden int
		if err = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_removed_items WHERE item_id=?`, v.ItemID).Scan(&hidden); err != nil {
			return v, err
		}
		if hidden > 0 {
			return v, fmt.Errorf("%w: restore the podcast before restoring its episode", ErrConflict)
		}
		it, e := c.trashPermission(ctx, uid, v.ItemID)
		if e != nil {
			return v, e
		}
		doc := objectJSON(string(it.Document))
		for _, entry := range array(doc["episodes"]) {
			ep := object(entry)
			if str(ep["id"]) == v.EpisodeID {
				fid := str(object(ep["audioFile"])["ino"])
				found := false
				for _, f := range v.Files {
					if f.ID == fid {
						found = true
					}
				}
				if !found {
					return v, fmt.Errorf("%w: episode was downloaded again; keep the newer copy or purge this trash entry", ErrConflict)
				}
			}
		}
	}
	switch action {
	case "restore":
		v.Operation = "restore"
		v.State = "restoring"
	case "purge":
		if v.State != "deleted" {
			return v, ErrConflict
		}
		v.Operation = "purge"
		v.State = "purging"
	case "retry":
		if v.State != "error" {
			return v, ErrConflict
		}
		switch v.Operation {
		case "delete":
			v.State = "moving"
		case "restore":
			v.State = "restoring"
		case "purge":
			v.State = "purging"
		}
	default:
		return v, ErrInvalid
	}
	if _, err = c.DB.ExecContext(ctx, `UPDATE catalog_trash SET operation=?,state=?,updated_at=? WHERE id=?`, v.Operation, v.State, time.Now().UnixMilli(), id); err != nil {
		return v, err
	}
	err = c.runTrash(ctx, &v)
	return v, err
}

// Called before background workers start. A durable journal makes both a move
// and its reversal repeatable after termination between two filesystem calls.
func (c *Catalog) RecoverTrash(ctx context.Context) error {
	trashMu.Lock()
	defer trashMu.Unlock()
	rows, err := c.DB.QueryContext(ctx, `SELECT `+trashColumns+` FROM catalog_trash WHERE state IN ('moving','restoring','purging')`)
	if err != nil {
		return err
	}
	all := []TrashEntry{}
	for rows.Next() {
		v, e := scanTrash(rows)
		if e != nil {
			rows.Close()
			return e
		}
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, v := range all {
		if e := c.runTrash(ctx, &v); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

func (c *Catalog) CanDelete(ctx context.Context, uid int64, item string) error {
	_, err := c.trashPermission(ctx, uid, item)
	return err
}
