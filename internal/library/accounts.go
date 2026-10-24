package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

type ManagedAccount struct {
	ID          string      `json:"id"`
	UserID      *int64      `json:"user_id"`
	Username    string      `json:"username"`
	Active      bool        `json:"active"`
	Locked      bool        `json:"locked"`
	Permissions Permissions `json:"permissions"`
	Revision    string      `json:"revision"`
}

func requireCatalogAdmin(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, uid int64) error {
	var role string
	if err := q.QueryRowContext(ctx, `SELECT role FROM users WHERE id=?`, uid).Scan(&role); err != nil {
		return ErrNotFound
	}
	if role != "admin" {
		return ErrNotFound
	}
	return nil
}
func readManagedAccount(row scanner) (ManagedAccount, error) {
	var a ManagedAccount
	var permissions string
	if err := row.Scan(&a.ID, &a.UserID, &a.Username, &a.Active, &a.Locked, &permissions); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return a, ErrNotFound
		}
		return a, err
	}
	if err := json.Unmarshal([]byte(permissions), &a.Permissions); err != nil {
		return a, err
	}
	hash := sha256.Sum256([]byte(raw(a) + permissions))
	a.Revision = hex.EncodeToString(hash[:])
	return a, nil
}

const managedAccountCols = `id,pocket_user_id,username,active,locked,permissions`

func (c *Catalog) ManagedAccounts(ctx context.Context, uid int64) ([]ManagedAccount, error) {
	if err := requireCatalogAdmin(ctx, c.DB, uid); err != nil {
		return nil, err
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT `+managedAccountCols+` FROM catalog_accounts ORDER BY username,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManagedAccount{}
	for rows.Next() {
		a, err := readManagedAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveAccount explicitly binds an unbound source identity or creates a new one.
// An established mapping cannot be reassigned: that would transfer private
// history, bookmarks and playlists to a different person.
func (c *Catalog) SaveAccount(ctx context.Context, actor int64, in ManagedAccount) (ManagedAccount, error) {
	if err := requireCatalogAdmin(ctx, c.DB, actor); err != nil {
		return in, err
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" || len(in.Username) > 256 || len(in.Permissions.Tags) > 100 || len(in.Permissions.Libraries) > 1000 {
		return in, ErrInvalid
	}
	for _, tag := range in.Permissions.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 256 {
			return in, ErrInvalid
		}
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	if err = requireCatalogAdmin(ctx, tx, actor); err != nil {
		return in, err
	}
	if in.UserID != nil {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id=?`, *in.UserID).Scan(&exists); err != nil {
			return in, err
		}
		if exists != 1 {
			return in, ErrInvalid
		}
	}
	for _, lib := range in.Permissions.Libraries {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_libraries WHERE id=?`, lib).Scan(&exists); err != nil {
			return in, err
		}
		if exists != 1 {
			return in, ErrInvalid
		}
	}
	if in.ID == "" {
		if in.UserID == nil {
			return in, ErrInvalid
		}
		in.ID = newID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_accounts VALUES(?,?,?,?,?,?)`, in.ID, in.UserID, in.Username, in.Active, in.Locked, raw(in.Permissions)); err != nil {
			return in, ErrConflict
		}
	} else {
		old, e := readManagedAccount(tx.QueryRowContext(ctx, `SELECT `+managedAccountCols+` FROM catalog_accounts WHERE id=?`, in.ID))
		if e != nil {
			return in, e
		}
		if old.Revision != in.Revision {
			return in, ErrConflict
		}
		if old.UserID != nil && (in.UserID == nil || *old.UserID != *in.UserID) {
			return in, ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_accounts SET pocket_user_id=?,username=?,active=?,locked=?,permissions=? WHERE id=?`, in.UserID, in.Username, in.Active, in.Locked, raw(in.Permissions), in.ID); err != nil {
			return in, ErrConflict
		}
	}
	out, err := readManagedAccount(tx.QueryRowContext(ctx, `SELECT `+managedAccountCols+` FROM catalog_accounts WHERE id=?`, in.ID))
	if err != nil {
		return in, err
	}
	return out, tx.Commit()
}
