// Package store wraps the SQLite database, migrations and all queries.
//
// The pool holds ONE connection: never run a second query while a rows
// cursor is still open — collect first, then query.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique constraint is violated.
var ErrConflict = errors.New("already exists")

// Store is the database handle.
type Store struct {
	DB  *sql.DB
	log *slog.Logger
}

// Open opens (and creates if needed) the SQLite database and runs migrations.
func Open(path string, log *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{DB: db, log: log}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var exists int
		if err := s.DB.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE name = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, Now()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.log.Info("applied migration", "name", name)
	}
	return nil
}

// Backup writes a consistent copy of the database to dst using VACUUM INTO.
func (s *Store) Backup(ctx context.Context, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dst), ".sqlite-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	name := filepath.Join(stage, "snapshot.db")
	if _, err = s.DB.ExecContext(ctx, `VACUUM INTO ?`, name); err != nil {
		return err
	}
	if err = os.Chmod(name, 0600); err != nil {
		return err
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Publishing a completed snapshot never overwrites a previous backup or the
	// live database, including when concurrent callers choose the same name.
	if err = os.Link(name, dst); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

// Tables lists the tables counted for metrics / the About page.
var Tables = []string{"listens", "finishes", "up_next", "users", "api_tokens"}

// userTables are the content tables counted per user.
var userTables = []string{"listens", "finishes", "up_next", "api_tokens"}

// CountsFor returns one user's row counts per content table (plus the number of accounts).
func (s *Store) CountsFor(ctx context.Context, uid int64) map[string]int {
	out := map[string]int{}
	for _, t := range userTables {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+t+` WHERE user_id = ?`, uid).Scan(&n); err == nil {
			out[t] = n
		}
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&n); err == nil {
		out["users"] = n
	}
	return out
}

// Counts returns box-wide row counts per table (metrics only — never shown as content).
func (s *Store) Counts(ctx context.Context) map[string]int {
	out := map[string]int{}
	for _, t := range Tables {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+t).Scan(&n); err == nil {
			out[t] = n
		}
	}
	return out
}

// Now returns the current UTC time formatted for storage.
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Today returns today's date (local) as YYYY-MM-DD.
func Today() string { return time.Now().Format("2006-01-02") }

// ParseDate parses YYYY-MM-DD.
func ParseDate(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }

// AddDays shifts a YYYY-MM-DD date.
func AddDays(d string, n int) string {
	t, err := ParseDate(d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func conflict(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- generic cache ----

// CacheGet returns a cached value and when it was fetched.
func (s *Store) CacheGet(ctx context.Context, key string) (string, string, bool) {
	var v, at string
	if err := s.DB.QueryRowContext(ctx, `SELECT value, fetched_at FROM cache WHERE key = ?`, key).Scan(&v, &at); err != nil {
		return "", "", false
	}
	return v, at, true
}

// CachePut stores a value.
func (s *Store) CachePut(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO cache (key, value, fetched_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, fetched_at = excluded.fetched_at`, key, value, Now())
	return err
}

// CacheDelete removes every key with the prefix.
func (s *Store) CacheDelete(ctx context.Context, prefix string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM cache WHERE key LIKE ? || '%'`, prefix)
	return err
}

// CheckCompatibleSchema refuses future/foreign migrations before restoring a
// database with this binary. It performs no migrations or writes.
func CheckCompatibleSchema(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return err
		}
		if strings.ContainsAny(name, "/\\") {
			return errors.New("invalid database migration")
		}
		if _, err = fs.Stat(migrationFS, "migrations/"+name); err != nil {
			return fmt.Errorf("backup needs a different/newer application version: %s", name)
		}
		count++
	}
	if count == 0 {
		return errors.New("not a Pocket Audiobooks database")
	}
	return rows.Err()
}
