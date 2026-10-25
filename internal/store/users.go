package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// CountUsers returns the number of users.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&n)
	return n, err
}

// CreateUser inserts a user.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO users (email, name, password_hash, role) VALUES (?, ?, ?, ?)`, u.Email, u.Name, u.PasswordHash, u.Role)
	if err != nil {
		return conflict(err)
	}
	u.ID, _ = res.LastInsertId()
	return nil
}

// UpdateUser updates name/email/password/role.
func (s *Store) UpdateUser(ctx context.Context, u *User) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET email = ?, name = ?, password_hash = ?, role = ?, updated_at = ? WHERE id = ?`, u.Email, u.Name, u.PasswordHash, u.Role, Now(), u.ID)
	return conflict(err)
}

const userCols = `id, email, name, password_hash, role, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

// GetUserByEmail finds a user by email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

// ListUsers returns all users.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// GetUser returns a user by id.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// DeleteUser removes a user together with everything they own (their
// listening history, finished books, up next, settings, sessions, tokens):
// the foreign keys cascade. Content is personal, so nothing is left behind.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OwnerID returns the box owner: the first admin, or the first account.
func (s *Store) OwnerID(ctx context.Context) int64 {
	var id int64
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE role = 'admin' ORDER BY id LIMIT 1`).Scan(&id); err == nil {
		return id
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MIN(id), 0) FROM users`).Scan(&id)
	return id
}

// CountAdmins returns how many admin accounts exist.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM users WHERE role = 'admin'`).Scan(&n)
	return n, err
}

// RandomToken returns n random bytes hex encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HashToken returns the sha256 hex digest of a token.
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// CreateSession creates a session for a user and returns its id.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	id := RandomToken(32)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`, HashToken(id), userID, time.Now().Add(ttl).UTC().Format(time.RFC3339))
	return id, err
}

// GetSessionUser resolves a session id to a user.
func (s *Store) GetSessionUser(ctx context.Context, sessionID string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT u.id, u.email, u.name, u.password_hash, u.role, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id = ? AND s.expires_at > ?`, HashToken(sessionID), time.Now().UTC().Format(time.RFC3339)))
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, HashToken(sessionID))
	return err
}

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339))
	return err
}

// CreateAPIToken creates a token and returns the plaintext (shown once).
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name string) (string, *APIToken, error) {
	plain := "pab_" + RandomToken(24)
	prefix := plain[:10]
	res, err := s.DB.ExecContext(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, prefix) VALUES (?, ?, ?, ?)`, userID, name, HashToken(plain), prefix)
	if err != nil {
		return "", nil, err
	}
	id, _ := res.LastInsertId()
	return plain, &APIToken{ID: id, UserID: userID, Name: name, Prefix: prefix, CreatedAt: Now()}, nil
}

// ReplaceAPIToken revokes the person's tokens with exactly this name and
// creates a fresh one: one token per signed-in device instead of a new one at
// every sign-in.
func (s *Store) ReplaceAPIToken(ctx context.Context, userID int64, name string) (string, *APIToken, error) {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ? AND name = ?`, userID, name); err != nil {
		return "", nil, err
	}
	return s.CreateAPIToken(ctx, userID, name)
}

// DeleteAPITokensNamed revokes the person's tokens whose name starts with
// prefix (tokens the app minted itself, never personal ones by other names).
func (s *Store) DeleteAPITokensNamed(ctx context.Context, userID int64, prefix string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ? AND substr(name, 1, ?) = ?`, userID, len(prefix), prefix)
	return err
}

// DeleteUserSessions signs the person out everywhere except the session keep
// (empty: everywhere).
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keep string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, HashToken(keep))
	return err
}

// tokenTouchEvery limits how often a token's last-used time is written: a
// player authenticates every HLS segment and the pool holds one connection.
const tokenTouchEvery = time.Minute

// GetUserByAPIToken resolves a bearer token.
func (s *Store) GetUserByAPIToken(ctx context.Context, token string) (*User, error) {
	h := HashToken(token)
	var u User
	var last *string
	err := s.DB.QueryRowContext(ctx, `SELECT u.id, u.email, u.name, u.password_hash, u.role, u.created_at, t.last_used_at
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`, h).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.CreatedAt, &last)
	if err != nil {
		return nil, notFound(err)
	}
	if last == nil || tokenStale(*last, time.Now()) {
		_, _ = s.DB.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?`, Now(), h)
	}
	return &u, nil
}

func tokenStale(last string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, last)
	return err != nil || now.Sub(t) >= tokenTouchEvery || t.After(now)
}

// ListAPITokens lists tokens for a user.
func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, name, prefix, last_used_at, created_at FROM api_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.LastUsedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken removes a token.
func (s *Store) DeleteAPIToken(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	return err
}
