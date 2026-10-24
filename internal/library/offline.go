package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"time"
)

type OfflineSession struct {
	ID            string  `json:"id"`
	ItemID        string  `json:"libraryItemId"`
	EpisodeID     string  `json:"episodeId"`
	Position      float64 `json:"currentTime"`
	StartPosition float64 `json:"startTime"`
	Listened      float64 `json:"timeListening"`
	StartedAt     int64   `json:"startedAt"`
	UpdatedAt     int64   `json:"updatedAt"`
	DeviceInfo    struct {
		DeviceID   string `json:"deviceId"`
		DeviceName string `json:"deviceName"`
		ClientName string `json:"clientName"`
	} `json:"deviceInfo"`
}

func offlineKey(account, id string) string {
	sum := sha256.Sum256([]byte(account + "\x00" + id))
	return "offline-" + hex.EncodeToString(sum[:])
}

// SyncOffline accepts a cumulative total, unlike online reports' deltas. The
// receipt, maximum total and progress all change in one transaction. Retries,
// reordered batches and restarts therefore cannot double-count listening.
func (c *Catalog) SyncOffline(ctx context.Context, uid int64, in OfflineSession) (ReportResult, error) {
	out := ReportResult{}
	if in.ID == "" || len(in.ID) > 200 || len(in.DeviceInfo.DeviceName) > 256 || len(in.DeviceInfo.ClientName) > 256 || len(in.DeviceInfo.DeviceID) > 256 || !validSeconds(in.Listened) || in.Listened > 366*86400 || !validSeconds(in.Position) || !validSeconds(in.StartPosition) || in.StartedAt < 946684800000 || in.UpdatedAt < in.StartedAt || in.UpdatedAt > time.Now().Add(5*time.Minute).UnixMilli() {
		return out, ErrInvalid
	}
	a, err := c.Account(ctx, uid)
	if err != nil {
		return out, err
	}
	it, err := c.Item(ctx, uid, in.ItemID)
	if err != nil {
		return out, err
	}
	_, _, duration, err := c.Timeline(ctx, uid, it.ID, in.EpisodeID)
	if err != nil {
		return out, err
	}
	if in.Position > duration+1 || in.StartPosition > duration+1 {
		return out, ErrInvalid
	}
	id := offlineKey(a.ID, in.ID)
	digest := sha256.Sum256([]byte(raw(in)))
	receipt := "offline-receipt-" + hex.EncodeToString(digest[:])
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_play_reports WHERE account_id=? AND id=?`, a.ID, receipt).Scan(&count); err != nil {
		return out, err
	}
	if count > 0 {
		out.Duplicate = true
		return out, nil
	}
	sess, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM catalog_play_sessions WHERE id=? AND account_id=?`, id, a.ID))
	var archived string
	if errors.Is(err, sql.ErrNoRows) {
		// A previously synced client may resend a session present in the import.
		// Fold that record into the cumulative stream, preserving a lossless audit
		// copy and removing the old projection only in this same transaction.
		var sourceItem sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT item_id,document FROM catalog_history WHERE id=? AND account_id=?`, in.ID, a.ID).Scan(&sourceItem, &archived)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if archived != "" {
			old := objectJSON(archived)
			if sourceItem.String != in.ItemID {
				return out, ErrConflict
			}
			in.Listened = math.Max(in.Listened, number(old["timeListening"]))
			oldAt, _ := milliseconds(old["updatedAt"])
			if oldAt > in.UpdatedAt {
				in.UpdatedAt = oldAt
				in.Position = number(old["currentTime"])
			}
		}
		sess = Session{ID: id, AccountID: a.ID, ItemID: in.ItemID, EpisodeID: in.EpisodeID, Generation: -1, Device: in.DeviceInfo.DeviceName, Client: in.DeviceInfo.ClientName, StartedAt: in.StartedAt, UpdatedAt: in.UpdatedAt, Position: in.StartPosition, StartPosition: in.StartPosition, Duration: duration, Closed: true}
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_play_sessions(`+sessionColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, a.ID, in.ItemID, in.EpisodeID, -1, sess.Device, sess.Client, in.StartedAt, in.UpdatedAt, in.StartPosition, in.StartPosition, duration, 0, 0, 1, 0); err != nil {
			return out, err
		}
	} else if err != nil {
		return out, err
	}
	if sess.ItemID != in.ItemID || sess.EpisodeID != in.EpisodeID || sess.StartedAt != in.StartedAt {
		return out, ErrConflict
	}
	delta := math.Max(0, in.Listened-sess.Listened)
	out, err = reportSessionTx(ctx, tx, uid, it, sess, Report{ID: receipt, Position: in.Position, Listened: delta, ObservedAt: in.UpdatedAt, Close: true})
	if err != nil {
		return out, err
	}
	if archived != "" {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO catalog_entities VALUES('offlineImportedHistory',?,NULL,?)`, in.ID, archived); err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM catalog_history WHERE id=? AND account_id=?`, in.ID, a.ID); err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM listens WHERE user_id=? AND abs_id=?`, uid, in.ID); err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}
