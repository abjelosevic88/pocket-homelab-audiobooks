package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ---- connections ----

// GetConnection returns how a person reaches Audiobookshelf.
func (s *Store) GetConnection(ctx context.Context, uid int64) (*Connection, error) {
	var c Connection
	err := s.DB.QueryRowContext(ctx, `SELECT user_id, kind, server_url, username, abs_user_id, secret, access, server_version, updated_at FROM connections WHERE user_id = ?`, uid).
		Scan(&c.UserID, &c.Kind, &c.ServerURL, &c.Username, &c.ABSUserID, &c.Secret, &c.Access, &c.ServerVersion, &c.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

// SaveConnection stores how a person reaches Audiobookshelf.
func (s *Store) SaveConnection(ctx context.Context, c *Connection) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO connections (user_id, kind, server_url, username, abs_user_id, secret, access, server_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET kind = excluded.kind, server_url = excluded.server_url, username = excluded.username, abs_user_id = excluded.abs_user_id,
		secret = excluded.secret, access = excluded.access, server_version = excluded.server_version, updated_at = excluded.updated_at`,
		c.UserID, c.Kind, c.ServerURL, c.Username, c.ABSUserID, c.Secret, c.Access, c.ServerVersion, Now())
	return err
}

// SaveTokens replaces the sealed tokens of a password sign-in (they rotate).
func (s *Store) SaveTokens(ctx context.Context, uid int64, secret, access string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE connections SET secret = ?, access = ?, updated_at = ? WHERE user_id = ? AND kind = 'direct'`, secret, access, Now(), uid)
	return err
}

// ConnectedUsers lists everyone with a connection that isn't switched off,
// plus (with Core) everyone without one.
func (s *Store) ConnectedUsers(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id FROM users u LEFT JOIN connections c ON c.user_id = u.id WHERE c.kind IS NULL OR c.kind <> 'off' ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- listens ----

func joinGenres(g []string) string {
	if len(g) == 0 {
		return ""
	}
	return "|" + strings.Join(g, "|") + "|"
}

func splitGenres(s string) []string {
	s = strings.Trim(s, "|")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "|")
}

// LocalDay is the local date of a UTC timestamp.
func LocalDay(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return Today()
	}
	return t.Local().Format("2006-01-02")
}

// MillisUTC formats Unix milliseconds for storage.
func MillisUTC(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

const listenCols = `id, abs_id, item_id, library_id, title, author, narrator, series, genres, duration, day, started_at, updated_at, seconds, start_pos, end_pos, device, source`

func scanListen(row interface{ Scan(...any) error }) (Listen, error) {
	var l Listen
	var g string
	err := row.Scan(&l.ID, &l.ABSID, &l.ItemID, &l.LibraryID, &l.Title, &l.Author, &l.Narrator, &l.Series, &g, &l.Duration, &l.Day, &l.StartedAt, &l.UpdatedAt, &l.Seconds, &l.StartPos, &l.EndPos, &l.Device, &l.Source)
	l.Genres = splitGenres(g)
	return l, err
}

// UpsertListen records a listening session: by its Audiobookshelf id when it
// has one (a session that grows is updated, never shrunk), else as a new row.
// added reports a new row.
func (s *Store) UpsertListen(ctx context.Context, uid int64, l *Listen) (added bool, err error) {
	if l.Day == "" {
		l.Day = LocalDay(l.StartedAt)
	}
	if l.Source == "" {
		l.Source = "audiobookshelf"
	}
	if l.ABSID != "" {
		var id int64
		var oldSec int
		err = s.DB.QueryRowContext(ctx, `SELECT id, seconds FROM listens WHERE user_id = ? AND abs_id = ?`, uid, l.ABSID).Scan(&id, &oldSec)
		if err == nil {
			l.ID = id
			if l.Seconds < oldSec {
				l.Seconds = oldSec
			}
			_, err = s.DB.ExecContext(ctx, `UPDATE listens SET title = ?, author = ?, narrator = ?, series = ?, genres = CASE WHEN ? = '' THEN genres ELSE ? END,
				duration = CASE WHEN ? > 0 THEN ? ELSE duration END, updated_at = ?, seconds = ?, end_pos = ?, device = CASE WHEN ? = '' THEN device ELSE ? END
				WHERE id = ?`,
				l.Title, l.Author, l.Narrator, l.Series, joinGenres(l.Genres), joinGenres(l.Genres), l.Duration, l.Duration, l.UpdatedAt, l.Seconds, l.EndPos, l.Device, l.Device, id)
			return false, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO listens (user_id, abs_id, item_id, library_id, title, author, narrator, series, genres, duration, day, started_at, updated_at, seconds, start_pos, end_pos, device, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, l.ABSID, l.ItemID, l.LibraryID, l.Title, l.Author, l.Narrator, l.Series, joinGenres(l.Genres), l.Duration, l.Day, l.StartedAt, l.UpdatedAt, l.Seconds, l.StartPos, l.EndPos, l.Device, l.Source)
	if err != nil {
		return false, conflict(err)
	}
	l.ID, _ = res.LastInsertId()
	return true, nil
}

// ListenFilter narrows a history query.
type ListenFilter struct {
	From, To string // local days, inclusive ("" = open)
	ItemID   string
	Q        string
	Limit    int // -1 = all
	Offset   int
}

// Listens returns a person's listening sessions, newest first, and how many match.
func (s *Store) Listens(ctx context.Context, uid int64, f ListenFilter) ([]Listen, int, error) {
	where := []string{"user_id = ?"}
	args := []any{uid}
	if f.From != "" {
		where = append(where, "day >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "day <= ?")
		args = append(args, f.To)
	}
	if f.ItemID != "" {
		where = append(where, "item_id = ?")
		args = append(args, f.ItemID)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, "(title LIKE ? OR author LIKE ? OR narrator LIKE ? OR series LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like, like)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM listens WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	lim := f.Limit
	if lim == 0 {
		lim = 50
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+listenCols+` FROM listens WHERE `+w+` ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, lim, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Listen{}
	for rows.Next() {
		l, err := scanListen(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// GetListen returns one of a person's listening sessions.
func (s *Store) GetListen(ctx context.Context, uid, id int64) (*Listen, error) {
	l, err := scanListen(s.DB.QueryRowContext(ctx, `SELECT `+listenCols+` FROM listens WHERE user_id = ? AND id = ?`, uid, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &l, nil
}

// DeleteListen removes one of a person's listening sessions from the history here.
func (s *Store) DeleteListen(ctx context.Context, uid, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM listens WHERE user_id = ? AND id = ?`, uid, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LastListens returns the newest session of each book a person heard, newest first.
func (s *Store) LastListens(ctx context.Context, uid int64, limit int) ([]Listen, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+listenCols+` FROM listens l WHERE user_id = ? AND id = (SELECT id FROM listens x WHERE x.user_id = l.user_id AND x.item_id = l.item_id ORDER BY updated_at DESC LIMIT 1)
		ORDER BY updated_at DESC LIMIT ?`, uid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Listen{}
	for rows.Next() {
		l, err := scanListen(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---- finished books ----

// AddFinish records a finished book; added is false when that day's finish is known.
func (s *Store) AddFinish(ctx context.Context, uid int64, f *Finish) (added bool, err error) {
	if f.Day == "" {
		f.Day = LocalDay(f.FinishedAt)
	}
	// the same finish read again (or moved by seconds) is not a new one
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM finishes WHERE user_id = ? AND item_id = ? AND (day = ? OR finished_at = ?)`, uid, f.ItemID, f.Day, f.FinishedAt).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO finishes (user_id, item_id, title, author, duration, finished_at, day) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uid, f.ItemID, f.Title, f.Author, f.Duration, f.FinishedAt, f.Day)
	if err != nil {
		return false, conflict(err)
	}
	f.ID, _ = res.LastInsertId()
	return true, nil
}

// Finishes returns a person's finished books between two local days (inclusive, "" = open), newest first.
func (s *Store) Finishes(ctx context.Context, uid int64, from, to string) ([]Finish, error) {
	q := `SELECT id, item_id, title, author, duration, finished_at, day FROM finishes WHERE user_id = ?`
	args := []any{uid}
	if from != "" {
		q += ` AND day >= ?`
		args = append(args, from)
	}
	if to != "" {
		q += ` AND day <= ?`
		args = append(args, to)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY finished_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Finish{}
	for rows.Next() {
		var f Finish
		if err := rows.Scan(&f.ID, &f.ItemID, &f.Title, &f.Author, &f.Duration, &f.FinishedAt, &f.Day); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- sync state ----

// GetSync returns where reading a person's history stands.
func (s *Store) GetSync(ctx context.Context, uid int64) SyncState {
	var st SyncState
	var b int
	if err := s.DB.QueryRowContext(ctx, `SELECT server_key, watermark, backfilled, synced_at, error FROM listen_sync WHERE user_id = ?`, uid).Scan(&st.ServerKey, &st.Watermark, &b, &st.SyncedAt, &st.Error); err != nil {
		return SyncState{}
	}
	st.Backfilled = b == 1
	return st
}

// SaveSync stores where reading a person's history stands.
func (s *Store) SaveSync(ctx context.Context, uid int64, st SyncState) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO listen_sync (user_id, server_key, watermark, backfilled, synced_at, error) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET server_key = excluded.server_key, watermark = excluded.watermark, backfilled = excluded.backfilled, synced_at = excluded.synced_at, error = excluded.error`,
		uid, st.ServerKey, st.Watermark, boolInt(st.Backfilled), st.SyncedAt, st.Error)
	return err
}

// ---- up next ----

// UpNextList returns a person's up next, in order.
func (s *Store) UpNextList(ctx context.Context, uid int64) ([]UpNext, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, item_id, title, author, position, added_at FROM up_next WHERE user_id = ? ORDER BY position, id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UpNext{}
	for rows.Next() {
		var u UpNext
		if err := rows.Scan(&u.ID, &u.ItemID, &u.Title, &u.Author, &u.Position, &u.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AddUpNext puts a book at the end of a person's up next (or first).
func (s *Store) AddUpNext(ctx context.Context, uid int64, u *UpNext, first bool) error {
	var pos int
	q := `SELECT COALESCE(MAX(position), 0) + 1 FROM up_next WHERE user_id = ?`
	if first {
		q = `SELECT COALESCE(MIN(position), 0) - 1 FROM up_next WHERE user_id = ?`
	}
	if err := s.DB.QueryRowContext(ctx, q, uid).Scan(&pos); err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO up_next (user_id, item_id, title, author, position) VALUES (?, ?, ?, ?, ?)`, uid, u.ItemID, u.Title, u.Author, pos)
	if err != nil {
		return conflict(err)
	}
	u.ID, _ = res.LastInsertId()
	u.Position = pos
	return nil
}

// RemoveUpNext takes a book off a person's up next.
func (s *Store) RemoveUpNext(ctx context.Context, uid int64, itemID string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM up_next WHERE user_id = ? AND item_id = ?`, uid, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReorderUpNext sets the order of a person's up next (ids not listed keep their place after).
func (s *Store) ReorderUpNext(ctx context.Context, uid int64, itemIDs []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for i, id := range itemIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE up_next SET position = ? WHERE user_id = ? AND item_id = ?`, i+1, uid, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
