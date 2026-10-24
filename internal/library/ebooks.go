package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

type ReadingPosition struct {
	Location string  `json:"location"`
	Progress float64 `json:"progress"`
	Revision string  `json:"revision"`
}

func (p *ReadingPosition) revision() {
	sum := sha256.Sum256([]byte(raw([]any{p.Location, p.Progress})))
	p.Revision = hex.EncodeToString(sum[:])
}

func (c *Catalog) EbookFiles(ctx context.Context, uid int64, item string) ([]File, string, error) {
	it, err := c.Item(ctx, uid, item)
	if err != nil {
		return nil, "", err
	}
	a, err := c.Account(ctx, uid)
	if err != nil || !a.Permissions.Download {
		return nil, "", ErrNotFound
	}
	files, err := c.Files(ctx, uid, item)
	if err != nil {
		return nil, "", err
	}
	doc := sourceRow{}
	if err = json.Unmarshal(it.Document, &doc); err != nil {
		return nil, "", err
	}
	primary := str(object(object(doc["media"])["ebookFile"])["ino"])
	out := []File{}
	found := false
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.Name))
		if (f.Role == "ebook" || f.Role == "supplement") && (ext == ".epub" || ext == ".pdf") {
			out = append(out, f)
			if f.ID == primary {
				found = true
			}
		}
	}
	if !found {
		primary = ""
		for _, f := range out {
			if f.Role == "ebook" {
				primary = f.ID
				break
			}
		}
	}
	if primary == "" && len(out) > 0 {
		primary = out[0].ID
	}
	return out, primary, nil
}
func (c *Catalog) readingAccess(ctx context.Context, uid int64, item, file string) (Account, bool, error) {
	files, primary, err := c.EbookFiles(ctx, uid, item)
	if err != nil {
		return Account{}, false, err
	}
	found := false
	for _, f := range files {
		if f.ID == file {
			found = true
		}
	}
	if !found {
		return Account{}, false, ErrNotFound
	}
	a, err := c.Account(ctx, uid)
	return a, primary == file, err
}
func readingQuery(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, account, item, file string, primary bool) (ReadingPosition, error) {
	p := ReadingPosition{}
	var err error
	if primary {
		var rawDoc string
		err = db.QueryRowContext(ctx, `SELECT document FROM catalog_progress WHERE account_id=? AND item_id=? AND episode_id=''`, account, item).Scan(&rawDoc)
		if err == nil {
			d := sourceRow{}
			if err = json.Unmarshal([]byte(rawDoc), &d); err != nil {
				return p, err
			}
			p.Location = str(d["ebookLocation"])
			p.Progress = number(d["ebookProgress"])
		}
	} else {
		err = db.QueryRowContext(ctx, `SELECT location,progress FROM catalog_ebook_positions WHERE account_id=? AND item_id=? AND file_id=?`, account, item, file).Scan(&p.Location, &p.Progress)
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	p.revision()
	return p, err
}
func (c *Catalog) ReadingPosition(ctx context.Context, uid int64, item, file string) (ReadingPosition, error) {
	a, primary, err := c.readingAccess(ctx, uid, item, file)
	if err != nil {
		return ReadingPosition{}, err
	}
	return readingQuery(ctx, c.DB, a.ID, item, file, primary)
}
func (c *Catalog) SaveReadingPosition(ctx context.Context, uid int64, item, file string, in ReadingPosition) (ReadingPosition, error) {
	if len(in.Location) > 8192 || in.Location == "" || !validSeconds(in.Progress) || in.Progress > 1 {
		return ReadingPosition{}, ErrInvalid
	}
	a, primary, err := c.readingAccess(ctx, uid, item, file)
	if err != nil {
		return ReadingPosition{}, err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return ReadingPosition{}, err
	}
	defer tx.Rollback()
	prev, err := readingQuery(ctx, tx, a.ID, item, file, primary)
	if err != nil {
		return prev, err
	}
	if prev.Location == in.Location && prev.Progress == in.Progress {
		return prev, nil
	} // response-lost retry
	if prev.Revision != in.Revision {
		return prev, ErrConflict
	}
	now := time.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_ebook_positions VALUES(?,?,?,?,?,?) ON CONFLICT(account_id,item_id,file_id) DO UPDATE SET location=excluded.location,progress=excluded.progress,updated_at=excluded.updated_at`, a.ID, item, file, in.Location, in.Progress, now)
	if err != nil {
		return prev, err
	}
	if primary {
		d := sourceRow{}
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT document FROM catalog_progress WHERE account_id=? AND item_id=? AND episode_id=''`, a.ID, item).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return prev, err
		}
		if existing != "" {
			if err = json.Unmarshal([]byte(existing), &d); err != nil {
				return prev, err
			}
		}
		if str(d["id"]) == "" {
			d["id"] = newID()
			d["startedAt"] = now
		}
		d["libraryItemId"], d["episodeId"], d["ebookLocation"], d["ebookProgress"], d["lastUpdate"] = item, "", in.Location, in.Progress, now
		d["updatedAt"] = time.UnixMilli(now).UTC().Format(time.RFC3339Nano)
		_, err = tx.ExecContext(ctx, `INSERT INTO catalog_progress(id,account_id,item_id,episode_id,position,finished,updated_at,document) VALUES(?,?,?,'',?,?,?,?) ON CONFLICT(account_id,item_id,episode_id) DO UPDATE SET updated_at=excluded.updated_at,document=excluded.document`, d["id"], a.ID, item, number(d["currentTime"]), truth(d["isFinished"]), now, raw(d))
		if err != nil {
			return prev, err
		}
	}
	in.revision()
	return in, tx.Commit()
}
