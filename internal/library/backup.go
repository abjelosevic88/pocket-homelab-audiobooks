package library

import (
	"context"
	"encoding/json"
	"errors"
	"os"
)

// WithStableFiles coordinates a live bundle with recoverable media moves.
// New cover uploads publish immutable bytes before their database transaction.
func WithStableFiles(fn func() error) error {
	trashMu.Lock()
	defer trashMu.Unlock()
	return fn()
}

// RebaseRestoredArtwork runs only on an extracted, validated, private restore.
// Original external media roots and their file identities are never rewritten.
func (c *Catalog) RebaseRestoredArtwork(ctx context.Context, dir, finalDir string) error {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_roots SET local_path=? WHERE id='native-art'`, finalDir); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,files FROM catalog_trash WHERE move_files=1 AND state='deleted'`)
	if err != nil {
		return err
	}
	type entry struct{ id, document string }
	all := []entry{}
	for rows.Next() {
		var v entry
		if err = rows.Scan(&v.id, &v.document); err != nil {
			rows.Close()
			return err
		}
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range all {
		var files []trashFile
		if err = json.Unmarshal([]byte(v.document), &files); err != nil {
			return err
		}
		changed := false
		for i := range files {
			f := &files[i]
			if f.RootID != "native-art" || f.Shared || f.Missing {
				continue
			}
			info, e := root.Lstat(f.Destination)
			if e != nil {
				return e
			}
			if !info.Mode().IsRegular() || info.Size() != f.Size || info.ModTime().UnixNano() != f.Modified {
				return errors.New("restored artwork does not match its trash journal")
			}
			f.Identity = fileIdentity(info)
			changed = true
		}
		if changed {
			if _, err = tx.ExecContext(ctx, `UPDATE catalog_trash SET files=? WHERE id=?`, raw(files), v.id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
