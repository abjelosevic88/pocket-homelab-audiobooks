package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
)

func FileSignature(info fs.FileInfo) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d", fileIdentity(info), info.Size(), info.ModTime().UnixNano())))
	return hex.EncodeToString(sum[:])
}
func (c *Catalog) CanUseTools(ctx context.Context, uid int64, id string) (Item, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return Item{}, err
	}
	if !a.Permissions.Update || !a.Permissions.Download {
		return Item{}, ErrNotFound
	}
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		return it, err
	}
	if it.MediaType != "book" {
		return it, ErrInvalid
	}
	return it, nil
}
