package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Scanning and adoption must not observe each other's partially published layout.
var mediaLayoutMu sync.Mutex

// ApplyGenerated preserves every original file and personal record. A durable
// journal makes publication recoverable after a crash between rename and commit.
// Generated output is never adopted by an ordinary scan before this commit.
func (c *Catalog) ApplyGenerated(ctx context.Context, uid int64, job, output string) (err error) {
	if !mediaLayoutMu.TryLock() {
		return fmt.Errorf("%w: a library scan or file change is active", ErrConflict)
	}
	defer mediaLayoutMu.Unlock()
	trashMu.Lock()
	defer trashMu.Unlock()
	var item, status, source, options, hash, rootID, path, fileID string
	var revision, size int64
	var duration float64
	err = c.DB.QueryRowContext(ctx, `SELECT item_id,status,source_revision,source,options,output_sha256,output_size,output_duration,apply_root,apply_path,apply_file_id FROM catalog_tool_jobs WHERE id=? AND user_id=?`, job, uid).Scan(&item, &status, &revision, &source, &options, &hash, &size, &duration, &rootID, &path, &fileID)
	if err != nil {
		return err
	}
	it, err := c.CanUseTools(ctx, uid, item)
	if err != nil {
		return err
	}
	if status == "applied" {
		return nil
	}
	if (status != "complete" && status != "applying") || it.UpdatedAt != revision {
		return ErrConflict
	}
	if size <= 0 || len(hash) != 64 || !validSeconds(duration) || duration <= 0 {
		return ErrInvalid
	}
	var inputs []struct {
		ID        string `json:"id"`
		Signature string `json:"signature"`
	}
	if err = json.Unmarshal([]byte(source), &inputs); err != nil || len(inputs) == 0 {
		return ErrInvalid
	}
	validateSources := func() error {
		for _, v := range inputs {
			f, _, e := c.OpenFile(ctx, uid, item, v.ID, true)
			if e != nil {
				return e
			}
			info, e := f.Stat()
			f.Close()
			if e != nil {
				return e
			}
			if FileSignature(info) != v.Signature {
				return fmt.Errorf("%w: original audio changed; rebuild the job", ErrConflict)
			}
		}
		return nil
	}
	if err = validateSources(); err != nil {
		return err
	}
	var active int
	if err = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_play_sessions WHERE item_id=? AND playing=1 AND closed=0 AND updated_at>?`, item, time.Now().Add(-2*time.Minute).UnixMilli()).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return fmt.Errorf("%w: pause playback before using the generated file", ErrConflict)
	}
	var opt struct {
		Extension string `json:"extension"`
	}
	if json.Unmarshal([]byte(options), &opt) != nil || (opt.Extension != ".m4b" && opt.Extension != ".mp3" && opt.Extension != ".flac") {
		return ErrInvalid
	}
	var local, sourceRoot string
	if rootID == "" {
		var relative string
		if err = c.DB.QueryRowContext(ctx, `SELECT f.root_id,f.relative_path,r.local_path,r.source_path FROM catalog_files f JOIN catalog_roots r ON r.id=f.root_id WHERE f.item_id=? AND f.id=?`, item, inputs[0].ID).Scan(&rootID, &relative, &local, &sourceRoot); err != nil {
			return err
		}
		parent := filepath.Dir(relative)
		// Use the book directory (above CD subdirectories) when it is available.
		bookPath := str(object(objectJSON(string(it.Document))["item"])["path"])
		if under(bookPath, sourceRoot) {
			rel, e := filepath.Rel(sourceRoot, bookPath)
			if e == nil {
				root, e := os.OpenRoot(local)
				if e != nil {
					return e
				}
				info, e := root.Stat(rel)
				root.Close()
				if e == nil && info.IsDir() {
					parent = rel
				}
			}
		}
		path = filepath.Join(parent, "Pocket-"+job+opt.Extension)
		fileID = scanStableID(rootID, path)
	} else if err = c.DB.QueryRowContext(ctx, `SELECT local_path,source_path FROM catalog_roots WHERE id=?`, rootID).Scan(&local, &sourceRoot); err != nil {
		return err
	}
	if !filepath.IsLocal(path) || strings.ContainsAny(job, "/\\") {
		return ErrInvalid
	}
	root, err := os.OpenRoot(local)
	if err != nil {
		return err
	}
	defer root.Close()
	if status == "complete" {
		// Even a matching pre-existing file is a collision until this job has a journal.
		if _, e := root.Lstat(path); e == nil {
			return fmt.Errorf("%w: generated filename already exists", ErrConflict)
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		result, e := c.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET status='applying',apply_root=?,apply_path=?,apply_file_id=?,error='',updated_at=? WHERE id=? AND status='complete'`, rootID, path, fileID, time.Now().UnixMilli(), job)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return ErrConflict
		}
	}
	defer func() {
		if err != nil {
			_, _ = c.DB.ExecContext(context.Background(), `UPDATE catalog_tool_jobs SET error=?,updated_at=? WHERE id=? AND status='applying'`, err.Error(), time.Now().UnixMilli(), job)
		}
	}()
	verify := func(f *os.File) error {
		info, e := f.Stat()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() != size {
			return errors.New("generated file size changed")
		}
		if _, e = f.Seek(0, 0); e != nil {
			return e
		}
		sum := sha256.New()
		if _, e = io.Copy(sum, f); e != nil {
			return e
		}
		if hex.EncodeToString(sum.Sum(nil)) != hash {
			return errors.New("generated file checksum changed")
		}
		return nil
	}
	existing, e := root.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		input, e := os.Open(output)
		if e != nil {
			return e
		}
		defer input.Close()
		if e = verify(input); e != nil {
			return e
		}
		if _, e = input.Seek(0, 0); e != nil {
			return e
		}
		temp := filepath.Join(filepath.Dir(path), ".pocket-media-"+job+".part")
		// An interrupted copy is private to this journal and has never been indexed.
		if e = root.Remove(temp); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		f, e := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer root.Remove(temp)
		_, e = io.Copy(f, input)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if e = renameNoReplace(root, temp, path); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else if !existing.Mode().IsRegular() {
		return errors.New("generated destination is not a regular file")
	}
	generated, err := root.Open(path)
	if err != nil {
		return err
	}
	defer generated.Close()
	if err = verify(generated); err != nil {
		return err
	}
	info, err := generated.Stat()
	if err != nil {
		return err
	}
	if err = validateSources(); err != nil {
		return err
	}
	current, err := c.CanUseTools(ctx, uid, item)
	if err != nil {
		return err
	}
	if current.UpdatedAt != revision {
		return ErrConflict
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var nowRevision int64
	if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM catalog_items WHERE id=? AND NOT EXISTS(SELECT 1 FROM catalog_removed_items r WHERE r.item_id=?)`, item, item).Scan(&nowRevision); err != nil {
		return err
	}
	if nowRevision != revision {
		return ErrConflict
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_play_sessions WHERE item_id=? AND playing=1 AND closed=0 AND updated_at>?`, item, time.Now().Add(-2*time.Minute).UnixMilli()).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return fmt.Errorf("%w: pause playback before changing tracks", ErrConflict)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,document FROM catalog_files WHERE item_id=? AND role='audio' ORDER BY track_index,id`, item)
	if err != nil {
		return err
	}
	oldIDs := []string{}
	oldDocs := []sourceRow{}
	for rows.Next() {
		var id, doc string
		if err = rows.Scan(&id, &doc); err != nil {
			rows.Close()
			return err
		}
		oldIDs = append(oldIDs, id)
		oldDocs = append(oldDocs, objectJSON(doc))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	audio := []any{}
	metadata := sourceRow{"filename": filepath.Base(path), "path": filepath.Join(sourceRoot, path), "relPath": path, "size": size, "mtimeMs": info.ModTime().UnixMilli(), "ext": opt.Extension}
	newDoc := sourceRow{"ino": fileID, "index": 1, "duration": duration, "exclude": false, "manuallyVerified": true, "metadata": metadata, "mimeType": mimeFor(path)}
	audio = append(audio, newDoc)
	for i, id := range oldIDs {
		d := oldDocs[i]
		d["exclude"] = true
		d["manuallyVerified"] = true
		d["index"] = i + 2
		audio = append(audio, d)
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_files SET excluded=1,track_index=?,document=? WHERE item_id=? AND id=?`, i+2, raw(d), item, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_files VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, item, fileID, "audio", rootID, path, filepath.Base(path), mimeFor(path), size, duration, 1, false, raw(newDoc)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_file_observations VALUES(?,?,?,?,?)`, item, fileID, size, info.ModTime().UnixNano(), fileIdentity(info)); err != nil {
		return err
	}
	doc := objectJSON(string(it.Document))
	m := object(doc["media"])
	m["audioFiles"] = audio
	m["duration"] = duration
	doc["media"] = m
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_items SET duration=?,document=?,updated_at=MAX(updated_at+1,?) WHERE id=?`, duration, raw(doc), time.Now().UnixMilli(), item); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_play_heads SET generation=generation+1 WHERE item_id=?`, item); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_play_sessions SET playing=0,closed=1 WHERE item_id=?`, item); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_tool_jobs SET status='applied',error='',updated_at=? WHERE id=?`, time.Now().UnixMilli(), job); err != nil {
		return err
	}
	return tx.Commit()
}

// CancelGenerated removes only the unindexed file recorded by an unfinished
// adoption. The checksum and inode checks prevent deleting a replaced file.
func (c *Catalog) CancelGenerated(ctx context.Context, uid int64, job string) error {
	if !mediaLayoutMu.TryLock() {
		return ErrConflict
	}
	defer mediaLayoutMu.Unlock()
	trashMu.Lock()
	defer trashMu.Unlock()
	var item, status, rootID, path, hash string
	var size int64
	if err := c.DB.QueryRowContext(ctx, `SELECT item_id,status,apply_root,apply_path,output_sha256,output_size FROM catalog_tool_jobs WHERE id=? AND user_id=?`, job, uid).Scan(&item, &status, &rootID, &path, &hash, &size); err != nil {
		return err
	}
	if _, err := c.CanUseTools(ctx, uid, item); err != nil {
		return err
	}
	if status != "applying" {
		return ErrConflict
	}
	var local string
	if err := c.DB.QueryRowContext(ctx, `SELECT local_path FROM catalog_roots WHERE id=?`, rootID).Scan(&local); err != nil {
		return err
	}
	root, err := os.OpenRoot(local)
	if err != nil {
		return err
	}
	defer root.Close()
	var indexed int
	if err = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_files WHERE root_id=? AND relative_path=?`, rootID, path).Scan(&indexed); err != nil {
		return err
	}
	if indexed > 0 {
		return ErrConflict
	}
	if info, e := root.Lstat(path); e == nil {
		if !info.Mode().IsRegular() || info.Size() != size {
			return ErrConflict
		}
		f, e := root.Open(path)
		if e != nil {
			return e
		}
		sum := sha256.New()
		_, e = io.Copy(sum, f)
		f.Close()
		if e != nil {
			return e
		}
		current, e := root.Lstat(path)
		if e != nil {
			return e
		}
		if FileSignature(current) != FileSignature(info) || hex.EncodeToString(sum.Sum(nil)) != hash {
			return ErrConflict
		}
		if e = root.Remove(path); e != nil {
			return e
		}
		dir, e := root.Open(filepath.Dir(path))
		if e != nil {
			return e
		}
		e = dir.Sync()
		dir.Close()
		if e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	temp := filepath.Join(filepath.Dir(path), ".pocket-media-"+job+".part")
	if err = root.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err = c.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET status='cancelled',apply_root='',apply_path='',apply_file_id='',error='',updated_at=? WHERE id=? AND status='applying'`, time.Now().UnixMilli(), job)
	return err
}
