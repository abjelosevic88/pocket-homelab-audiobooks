package library

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type TrackChoice struct {
	ID       string `json:"id"`
	Excluded bool   `json:"excluded"`
}
type TrackEdit struct {
	Expected      int64         `json:"revision"`
	Tracks        []TrackChoice `json:"tracks"`
	ResetChapters bool          `json:"reset_chapters"`
}

func (c *Catalog) EditTracks(ctx context.Context, uid int64, item string, in TrackEdit) error {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	if !a.Permissions.Update {
		return ErrNotFound
	}
	it, err := c.Item(ctx, uid, item)
	if err != nil {
		return err
	}
	if it.MediaType != "book" {
		return ErrInvalid
	}
	if it.UpdatedAt != in.Expected {
		return ErrConflict
	}
	if len(in.Tracks) == 0 || len(in.Tracks) > 10000 {
		return ErrInvalid
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM catalog_items WHERE id=?`, item).Scan(&revision); err != nil {
		return err
	}
	if revision != in.Expected {
		return ErrConflict
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_play_sessions WHERE item_id=? AND playing=1 AND closed=0 AND updated_at>?`, item, time.Now().Add(-2*time.Minute).UnixMilli()).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return fmt.Errorf("%w: pause playback before changing tracks", ErrConflict)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,duration,document FROM catalog_files WHERE item_id=? AND role='audio'`, item)
	if err != nil {
		return err
	}
	type audio struct {
		name     string
		duration float64
		doc      sourceRow
	}
	files := map[string]audio{}
	for rows.Next() {
		var id, name, doc string
		var duration float64
		if err = rows.Scan(&id, &name, &duration, &doc); err != nil {
			rows.Close()
			return err
		}
		files[id] = audio{name, duration, objectJSON(doc)}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(files) != len(in.Tracks) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	duration := 0.0
	chapters := []Chapter{}
	ordered := []any{}
	for i, v := range in.Tracks {
		f, ok := files[v.ID]
		if !ok || seen[v.ID] {
			return ErrInvalid
		}
		seen[v.ID] = true
		f.doc["ino"] = v.ID
		f.doc["exclude"] = v.Excluded
		f.doc["index"] = i + 1
		f.doc["manuallyVerified"] = true
		if !v.Excluded {
			if !validSeconds(f.duration) || f.duration <= 0 {
				return fmt.Errorf("%w: exclude invalid zero-duration audio", ErrInvalid)
			}
			chapters = append(chapters, Chapter{ID: len(chapters), Title: f.name, Start: duration, End: duration + f.duration})
			duration += f.duration
		}
		ordered = append(ordered, f.doc)
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_files SET track_index=?,excluded=?,document=? WHERE item_id=? AND id=?`, i+1, v.Excluded, raw(f.doc), item, v.ID); err != nil {
			return err
		}
	}
	if duration <= 0 {
		return fmt.Errorf("%w: keep at least one playable track", ErrInvalid)
	}
	doc := objectJSON(string(it.Document))
	media := object(doc["media"])
	if in.ResetChapters {
		media["chapters"] = chapters
	} else {
		var old []Chapter
		if err = json.Unmarshal([]byte(raw(media["chapters"])), &old); err != nil {
			return err
		}
		for _, ch := range old {
			if ch.Start > duration || ch.End > duration+1 {
				return fmt.Errorf("%w: chapters extend beyond the new duration; choose to rebuild chapters from tracks", ErrConflict)
			}
		}
	}
	media["audioFiles"] = ordered
	media["duration"] = duration
	doc["media"] = media
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_items SET duration=?,document=?,updated_at=MAX(updated_at+1,?) WHERE id=?`, duration, raw(doc), time.Now().UnixMilli(), item); err != nil {
		return err
	}
	// Old sessions keep their original timeline in history; their late reports
	// cannot overwrite a position after a new track layout has been selected.
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_play_heads SET generation=generation+1 WHERE item_id=?`, item); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_play_sessions SET playing=0,closed=1 WHERE item_id=?`, item); err != nil {
		return err
	}
	return tx.Commit()
}
