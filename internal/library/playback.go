package library

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid playback request")
var ErrConflict = errors.New("report identifier already used with different contents")
var ErrNoAudio = errors.New("this item has no playable audio")

func newID() string               { return "native-" + rand.Text() }
func validSeconds(n float64) bool { return n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) }

type Chapter struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}
type Track struct {
	ID       string  `json:"id"`
	Index    int     `json:"index"`
	Start    float64 `json:"startOffset"`
	Duration float64 `json:"duration"`
	Title    string  `json:"title"`
	MIME     string  `json:"mimeType"`
	URL      string  `json:"contentUrl"`
}
type Session struct {
	ID            string    `json:"id"`
	AccountID     string    `json:"userId"`
	ItemID        string    `json:"libraryItemId"`
	EpisodeID     string    `json:"episodeId"`
	Generation    int64     `json:"-"`
	Device        string    `json:"device"`
	Client        string    `json:"client"`
	StartedAt     int64     `json:"startedAt"`
	UpdatedAt     int64     `json:"updatedAt"`
	Position      float64   `json:"currentTime"`
	StartPosition float64   `json:"startTime"`
	Duration      float64   `json:"duration"`
	Listened      float64   `json:"timeListening"`
	Playing       bool      `json:"playing"`
	Closed        bool      `json:"closed"`
	ObservedAt    int64     `json:"-"`
	Tracks        []Track   `json:"audioTracks"`
	Chapters      []Chapter `json:"chapters"`
}

const sessionColumns = `id,account_id,item_id,episode_id,generation,device,client,started_at,updated_at,position,start_position,duration,listened,playing,closed,last_observed_at`

func scanSession(row scanner) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.AccountID, &s.ItemID, &s.EpisodeID, &s.Generation, &s.Device, &s.Client, &s.StartedAt, &s.UpdatedAt, &s.Position, &s.StartPosition, &s.Duration, &s.Listened, &s.Playing, &s.Closed, &s.ObservedAt)
	return s, err
}

// Timeline uses indexed file order, excluding disabled tracks. Podcast sessions
// use exactly the selected episode, never concatenate a show's episodes.
func (c *Catalog) Timeline(ctx context.Context, uid int64, itemID, episodeID string) ([]Track, []Chapter, float64, error) {
	it, err := c.Item(ctx, uid, itemID)
	if err != nil {
		return nil, nil, 0, err
	}
	files, err := c.Files(ctx, uid, itemID)
	if err != nil {
		return nil, nil, 0, err
	}
	var doc sourceRow
	if err = json.Unmarshal(it.Document, &doc); err != nil {
		return nil, nil, 0, err
	}
	media := object(doc["media"])
	chapters := media["chapters"]
	episodeFile := ""
	if it.MediaType == "podcast" {
		if episodeID == "" {
			return nil, nil, 0, ErrNoAudio
		}
		found := false
		for _, v := range array(doc["episodes"]) {
			e := object(v)
			if str(e["id"]) == episodeID {
				found = true
				episodeFile = str(object(e["audioFile"])["ino"])
				chapters = e["chapters"]
				break
			}
		}
		if !found {
			return nil, nil, 0, ErrNotFound
		}
	} else if episodeID != "" {
		return nil, nil, 0, ErrNotFound
	}
	tracks := []Track{}
	duration := 0.0
	for _, f := range files {
		if f.Role != "audio" || f.Excluded || f.Duration <= 0 || (it.MediaType == "podcast" && f.ID != episodeFile) {
			continue
		}
		if !validSeconds(f.Duration) {
			return nil, nil, 0, ErrInvalid
		}
		tracks = append(tracks, Track{ID: f.ID, Index: len(tracks) + 1, Start: duration, Duration: f.Duration, Title: f.Name, MIME: f.MIME, URL: "/api/items/" + itemID + "/file/" + f.ID})
		duration += f.Duration
	}
	if len(tracks) == 0 {
		return nil, nil, 0, ErrNoAudio
	}
	out := []Chapter{}
	if chapters != nil {
		if err = json.Unmarshal([]byte(raw(chapters)), &out); err != nil {
			return nil, nil, 0, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	for _, ch := range out {
		if !validSeconds(ch.Start) || !validSeconds(ch.End) || ch.End < ch.Start || ch.Start > duration+1 {
			return nil, nil, 0, ErrInvalid
		}
	}
	return tracks, out, duration, nil
}

func (c *Catalog) OpenSession(ctx context.Context, uid int64, item, episode, client, device string) (Session, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return Session{}, err
	}
	tracks, chapters, duration, err := c.Timeline(ctx, uid, item, episode)
	if err != nil {
		return Session{}, err
	}
	if len(client) > 200 || len(device) > 200 {
		return Session{}, ErrInvalid
	}
	// Verify every selected file before advertising a session; a missing later
	// chapter must not leave the player believing the complete book is available.
	for _, t := range tracks {
		f, _, err := c.OpenFile(ctx, uid, item, t.ID, false)
		if err != nil {
			return Session{}, err
		}
		f.Close()
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	s := Session{ID: newID(), AccountID: a.ID, ItemID: item, EpisodeID: episode, Device: device, Client: client, StartedAt: now, UpdatedAt: now, Duration: duration, Tracks: tracks, Chapters: chapters}
	var finished bool
	err = tx.QueryRowContext(ctx, `SELECT position,finished FROM catalog_progress WHERE account_id=? AND item_id=? AND episode_id=?`, a.ID, item, episode).Scan(&s.Position, &finished)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	if finished {
		s.Position = 0
	}
	s.Position = math.Min(s.Position, duration)
	s.StartPosition = s.Position
	err = tx.QueryRowContext(ctx, `INSERT INTO catalog_play_heads(account_id,item_id,episode_id,generation) VALUES(?,?,?,1) ON CONFLICT(account_id,item_id,episode_id) DO UPDATE SET generation=generation+1 RETURNING generation`, a.ID, item, episode).Scan(&s.Generation)
	if err != nil {
		return s, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_play_sessions SET playing=0,closed=1 WHERE account_id=? AND item_id=? AND episode_id=? AND closed=0`, a.ID, item, episode); err != nil {
		return s, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_play_sessions(`+sessionColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, s.ID, s.AccountID, s.ItemID, s.EpisodeID, s.Generation, s.Device, s.Client, s.StartedAt, s.UpdatedAt, s.Position, s.StartPosition, s.Duration, 0, 0, 0, 0)
	if err != nil {
		return s, err
	}
	return s, tx.Commit()
}
func (c *Catalog) Session(ctx context.Context, uid int64, id string) (Session, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return Session{}, err
	}
	s, err := scanSession(c.DB.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM catalog_play_sessions WHERE id=? AND account_id=?`, id, a.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	if _, err = c.Item(ctx, uid, s.ItemID); err != nil {
		return Session{}, err
	}
	return s, nil
}

type Report struct {
	ID         string  `json:"report_id"`
	Position   float64 `json:"current_time"`
	Listened   float64 `json:"listened"`
	ObservedAt int64   `json:"observed_at"`
	Playing    bool    `json:"playing"`
	Close      bool    `json:"close"`
	Ended      bool    `json:"ended"`
}
type ReportResult struct {
	Duplicate       bool `json:"duplicate"`
	ProgressApplied bool `json:"progress_applied"`
	FinishedNow     bool `json:"finished_now"`
}

// ReportSession commits its receipt, session, progress, daily history and finish
// in one transaction. Older reports still count listening but never rewind newer
// progress or reopen closed/superseded sessions. Receipts survive restarts.
func (c *Catalog) ReportSession(ctx context.Context, uid int64, id string, r Report) (ReportResult, error) {
	out := ReportResult{}
	if r.ID == "" || len(r.ID) > 200 || !validSeconds(r.Position) || !validSeconds(r.Listened) || r.Listened > 86400 || r.ObservedAt <= 0 || r.ObservedAt > time.Now().Add(5*time.Minute).UnixMilli() {
		return out, ErrInvalid
	}
	session, err := c.Session(ctx, uid, id)
	if err != nil {
		return out, err
	}
	it, err := c.Item(ctx, uid, session.ItemID)
	if err != nil {
		return out, err
	}
	if r.Position > session.Duration+1 {
		return out, ErrInvalid
	}
	fingerprint := sha256.Sum256([]byte(id + raw(r)))
	hash := hex.EncodeToString(fingerprint[:])
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	// Reread under the write transaction: two reports may have raced before it.
	session, err = scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM catalog_play_sessions WHERE id=? AND account_id=?`, id, session.AccountID))
	if err != nil {
		return out, err
	}
	var old string
	err = tx.QueryRowContext(ctx, `SELECT fingerprint FROM catalog_play_reports WHERE account_id=? AND id=?`, session.AccountID, r.ID).Scan(&old)
	if err == nil {
		if old != hash {
			return out, ErrConflict
		}
		out.Duplicate = true
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	now := time.Now()
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_play_reports VALUES(?,?,?,?,?)`, session.AccountID, r.ID, id, hash, now.UnixMilli()); err != nil {
		return out, err
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT generation FROM catalog_play_heads WHERE account_id=? AND item_id=? AND episode_id=?`, session.AccountID, session.ItemID, session.EpisodeID).Scan(&generation); err != nil {
		return out, err
	}
	newer := r.ObservedAt > session.ObservedAt
	latest := generation == session.Generation
	if newer {
		session.Position = math.Min(r.Position, session.Duration)
		session.ObservedAt = r.ObservedAt
	}
	session.Listened += r.Listened
	session.Closed = session.Closed || r.Close || r.Ended || !latest
	if newer {
		session.Playing = r.Playing && !session.Closed
	}
	session.UpdatedAt = now.UnixMilli()
	_, err = tx.ExecContext(ctx, `UPDATE catalog_play_sessions SET updated_at=?,position=?,listened=?,playing=?,closed=?,last_observed_at=? WHERE id=?`, session.UpdatedAt, session.Position, session.Listened, session.Playing, session.Closed, session.ObservedAt, id)
	if err != nil {
		return out, err
	}
	if newer && latest {
		// Explicit progress edits are also timestamped, so delayed offline reports
		// cannot undo a newer manual mark/reset.
		var previousAt int64
		var previous string
		err = tx.QueryRowContext(ctx, `SELECT updated_at,document FROM catalog_progress WHERE account_id=? AND item_id=? AND episode_id=?`, session.AccountID, session.ItemID, session.EpisodeID).Scan(&previousAt, &previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if r.ObservedAt >= previousAt {
			out.ProgressApplied = true
			progress := sourceRow{}
			if previous != "" {
				if err = json.Unmarshal([]byte(previous), &progress); err != nil {
					return out, err
				}
			}
			finished := r.Ended || (r.Position >= session.Duration-1 && r.Listened > 0)
			// A previously finished book starts a new read only once playback resumes.
			if !finished && truth(progress["isFinished"]) && r.Listened == 0 {
				finished = true
			}
			progress["duration"], progress["currentTime"], progress["isFinished"] = session.Duration, session.Position, finished
			progress["libraryItemId"], progress["episodeId"] = session.ItemID, session.EpisodeID
			progress["lastUpdate"], progress["updatedAt"] = r.ObservedAt, time.UnixMilli(r.ObservedAt).UTC().Format(time.RFC3339Nano)
			if progress["startedAt"] == nil {
				if at, e := milliseconds(progress["createdAt"]); e == nil && at > 0 {
					progress["startedAt"] = at
				} else {
					progress["startedAt"] = session.StartedAt
				}
			}
			if finished {
				if !truth(objectJSON(previous)["isFinished"]) {
					progress["finishedAt"] = r.ObservedAt
				}
			} else {
				progress["finishedAt"] = nil
			}
			if str(progress["id"]) == "" {
				progress["id"] = newID()
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO catalog_progress(id,account_id,item_id,episode_id,position,finished,updated_at,document) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(account_id,item_id,episode_id) DO UPDATE SET position=excluded.position,finished=excluded.finished,updated_at=excluded.updated_at,document=excluded.document`, progress["id"], session.AccountID, session.ItemID, session.EpisodeID, session.Position, finished, r.ObservedAt, raw(progress))
			if err != nil {
				return out, err
			}
			if finished && session.EpisodeID == "" && it.MediaType == "book" {
				out.FinishedNow, err = recordNativeFinish(ctx, tx, uid, it, r.ObservedAt)
				if err != nil {
					return out, err
				}
			}
		}
	}
	if r.Listened > 0 && session.EpisodeID == "" && it.MediaType == "book" {
		// Split wall-clock listening at local midnight, so delayed/offline reports
		// accrue on the day they happened. Seeking contributes no listened seconds.
		until := time.UnixMilli(r.ObservedAt)
		remaining := r.Listened
		for remaining > 0 {
			local := until.Add(-time.Nanosecond).In(time.Local)
			midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
			seconds := math.Min(remaining, until.Sub(midnight).Seconds())
			if seconds <= 0 {
				return out, ErrInvalid
			}
			begin := until.Add(-time.Duration(seconds * float64(time.Second)))
			day := local.Format("2006-01-02")
			listenID := id + ":" + day
			var totalSeconds float64
			err = tx.QueryRowContext(ctx, `INSERT INTO catalog_play_days VALUES(?,?,?) ON CONFLICT(session_id,day) DO UPDATE SET seconds=seconds+excluded.seconds RETURNING seconds`, id, day, seconds).Scan(&totalSeconds)
			if err != nil {
				return out, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO listens(user_id,abs_id,item_id,library_id,title,author,duration,day,started_at,updated_at,seconds,start_pos,end_pos,device,source)
    VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'pocket') ON CONFLICT(user_id,abs_id) WHERE abs_id<>'' DO UPDATE SET seconds=excluded.seconds,updated_at=MAX(listens.updated_at,excluded.updated_at),end_pos=CASE WHEN excluded.updated_at>=listens.updated_at THEN excluded.end_pos ELSE listens.end_pos END`, uid, listenID, it.ID, it.LibraryID, it.Title, it.Author, int(session.Duration), day, begin.UTC().Format(time.RFC3339Nano), until.UTC().Format(time.RFC3339Nano), int(math.Floor(totalSeconds)), int(session.StartPosition), int(r.Position), strings.TrimSpace(session.Client+" · "+session.Device))
			if err != nil {
				return out, err
			}
			remaining -= seconds
			until = begin
		}
	}
	return out, tx.Commit()
}
func objectJSON(s string) sourceRow {
	out := sourceRow{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
func recordNativeFinish(ctx context.Context, tx *sql.Tx, uid int64, it Item, at int64) (bool, error) {
	when := time.UnixMilli(at)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO finishes(user_id,item_id,title,author,duration,finished_at,day) VALUES(?,?,?,?,?,?,?)`, uid, it.ID, it.Title, it.Author, int(it.Duration), when.UTC().Format(time.RFC3339Nano), when.In(time.Local).Format("2006-01-02"))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM up_next WHERE user_id=? AND item_id=?`, uid, it.ID); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (c *Catalog) RecentSessions(ctx context.Context, uid int64, limit int) ([]Session, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 0 {
		limit = -1
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT `+sessionColumns+` FROM catalog_play_sessions WHERE account_id=? ORDER BY updated_at DESC,id LIMIT ?`, a.ID, limit)
	if err != nil {
		return nil, err
	}
	all := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Session{}
	for _, s := range all {
		if _, err = c.Item(ctx, uid, s.ItemID); err == nil {
			out = append(out, s)
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return out, nil
}

// SetProgress handles explicit edits independently of listening reports.
func (c *Catalog) SetProgress(ctx context.Context, uid int64, item, episode string, changes map[string]any) error {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	it, err := c.Item(ctx, uid, item)
	if err != nil {
		return err
	}
	duration := it.Duration
	if episode != "" {
		_, _, duration, err = c.Timeline(ctx, uid, item, episode)
		if err != nil {
			return err
		}
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	doc := sourceRow{}
	var prev string
	err = tx.QueryRowContext(ctx, `SELECT document FROM catalog_progress WHERE account_id=? AND item_id=? AND episode_id=?`, a.ID, item, episode).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if prev != "" {
		if err = json.Unmarshal([]byte(prev), &doc); err != nil {
			return err
		}
	}
	for key, v := range changes {
		switch key {
		case "currentTime", "ebookProgress":
			n := number(v)
			if !validSeconds(n) || (key == "currentTime" && duration > 0 && n > duration+1) || (key == "ebookProgress" && n > 1) {
				return ErrInvalid
			}
			doc[key] = n
		case "isFinished", "hideFromContinueListening":
			b, ok := v.(bool)
			if !ok {
				return ErrInvalid
			}
			doc[key] = b
		case "ebookLocation":
			s, ok := v.(string)
			if !ok || len(s) > 8192 {
				return ErrInvalid
			}
			doc[key] = s
		default:
			return fmt.Errorf("%w: unsupported progress field %s", ErrInvalid, key)
		}
	}
	now := time.Now().UnixMilli()
	if str(doc["id"]) == "" {
		doc["id"] = newID()
		doc["startedAt"] = now
	}
	doc["libraryItemId"], doc["episodeId"], doc["duration"], doc["lastUpdate"] = item, episode, duration, now
	doc["updatedAt"] = time.UnixMilli(now).UTC().Format(time.RFC3339Nano)
	if v, ok := changes["isFinished"]; ok {
		if truth(v) {
			doc["finishedAt"] = now
			if episode == "" && it.MediaType == "book" {
				if _, err = recordNativeFinish(ctx, tx, uid, it, now); err != nil {
					return err
				}
			}
		} else {
			doc["finishedAt"] = nil
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_progress(id,account_id,item_id,episode_id,position,finished,updated_at,document) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(account_id,item_id,episode_id) DO UPDATE SET position=excluded.position,finished=excluded.finished,updated_at=excluded.updated_at,document=excluded.document`, doc["id"], a.ID, item, episode, number(doc["currentTime"]), truth(doc["isFinished"]), now, raw(doc))
	if err != nil {
		return err
	}
	// Explicit edits revoke the old session's right to update this progress.
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_play_heads VALUES(?,?,?,1) ON CONFLICT(account_id,item_id,episode_id) DO UPDATE SET generation=generation+1`, a.ID, item, episode)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (c *Catalog) Bookmarks(ctx context.Context, uid int64) ([]json.RawMessage, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT item_id,document FROM catalog_bookmarks WHERE account_id=? ORDER BY item_id,position`, a.ID)
	if err != nil {
		return nil, err
	}
	type entry struct{ id, doc string }
	all := []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.doc); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []json.RawMessage{}
	for _, e := range all {
		if _, err = c.Item(ctx, uid, e.id); err == nil {
			out = append(out, json.RawMessage(e.doc))
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return out, nil
}
func (c *Catalog) Bookmark(ctx context.Context, uid int64, item string, position float64, title string, remove bool) error {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	it, err := c.Item(ctx, uid, item)
	if err != nil {
		return err
	}
	if !validSeconds(position) || position > it.Duration+1 || len(title) > 1000 {
		return ErrInvalid
	}
	if remove {
		_, err = c.DB.ExecContext(ctx, `DELETE FROM catalog_bookmarks WHERE account_id=? AND item_id=? AND position=?`, a.ID, item, position)
		return err
	}
	doc := sourceRow{"libraryItemId": item, "time": position, "title": title, "createdAt": time.Now().UnixMilli()}
	_, err = c.DB.ExecContext(ctx, `INSERT INTO catalog_bookmarks VALUES(?,?,?,?) ON CONFLICT(account_id,item_id,position) DO UPDATE SET document=json_set(catalog_bookmarks.document,'$.title',json_extract(excluded.document,'$.title'))`, a.ID, item, position, raw(doc))
	return err
}
