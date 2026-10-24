// Package backup creates and validates application recovery bundles. External
// libraries stay separate: a database bundle is not a backup of the media disks.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

const maxTotal = int64(64 << 30)
const maxEntries = 100000
const maxManifest = 32 << 20

type Entry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Modified int64  `json:"modified_ns"`
}
type Root struct {
	ID     string `json:"id"`
	Source string `json:"source_path"`
	Local  string `json:"local_path"`
}
type Manifest struct {
	Format        int     `json:"format"`
	App           string  `json:"app"`
	Version       string  `json:"version"`
	Backend       string  `json:"backend"`
	Created       string  `json:"created_at"`
	Files         []Entry `json:"files"`
	ExternalRoots []Root  `json:"external_media_roots"`
	MediaIncluded bool    `json:"external_media_included"`
}
type Options struct{ DataDir, Destination, Backend, Version string }
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func readonly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=query_only(1)"}).String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
func checkDB(ctx context.Context, db *sql.DB) error {
	var ok string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&ok); err != nil {
		return err
	}
	if ok != "ok" {
		return errors.New("backup database integrity check failed")
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("backup database has broken references")
	}
	return rows.Err()
}
func validName(name string) bool {
	if strings.ContainsAny(name, "\\\x00") || path.Clean(name) != name || !filepath.IsLocal(name) {
		return false
	}
	switch name {
	case "pocket-audiobooks.db", "secret.key", "core.token", "core.token.oidc.json":
		return true
	}
	return strings.HasPrefix(name, "native-art/")
}
func inspect(ctx context.Context, db *sql.DB) ([]Root, []string, error) {
	if err := checkDB(ctx, db); err != nil {
		return nil, nil, err
	}
	var pending int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM catalog_trash WHERE state IN ('moving','restoring','purging','error')`).Scan(&pending); err != nil {
		return nil, nil, err
	}
	if pending > 0 {
		return nil, nil, errors.New("finish or recover pending Trash operations before making an application bundle; a database snapshot is still available")
	}
	roots := []Root{}
	rows, err := db.QueryContext(ctx, `SELECT id,source_path,local_path FROM catalog_roots WHERE id<>'native-art' ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var v Root
		if err = rows.Scan(&v.ID, &v.Source, &v.Local); err != nil {
			rows.Close()
			return nil, nil, err
		}
		roots = append(roots, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	needed := map[string]bool{}
	rows, err = db.QueryContext(ctx, `SELECT f.relative_path,COALESCE(t.state,''),COALESCE(t.move_files,0),COALESCE(t.files,'[]') FROM catalog_files f LEFT JOIN catalog_removed_files r ON r.item_id=f.item_id AND r.file_id=f.id LEFT JOIN catalog_trash t ON t.id=r.trash_id WHERE f.root_id='native-art'`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var name, state, document string
		var moved bool
		if err = rows.Scan(&name, &state, &moved, &document); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if moved && (state == "deleted" || state == "purged") {
			var files []struct {
				RootID, Source, Destination string
				Missing, Shared             bool
			}
			if err = json.Unmarshal([]byte(document), &files); err != nil {
				rows.Close()
				return nil, nil, err
			}
			for _, f := range files {
				if f.RootID == "native-art" && f.Source == name && !f.Shared {
					if f.Missing || state == "purged" {
						name = ""
					} else {
						name = f.Destination
					}
					break
				}
			}
		}
		if name != "" {
			if !validName("native-art/" + filepath.ToSlash(name)) {
				rows.Close()
				return nil, nil, errors.New("invalid artwork path in catalog")
			}
			needed["native-art/"+filepath.ToSlash(name)] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	names := []string{}
	for name := range needed {
		names = append(names, name)
	}
	sort.Strings(names)
	return roots, names, nil
}

// Create must run with the server's stable-files guard or an exclusive offline
// application lease. A manifest records exactly the hashed bytes in the archive.
func Create(ctx context.Context, st *store.Store, opt Options) (Manifest, error) {
	m := Manifest{Format: 1, App: "pocket-audiobooks", Version: opt.Version, Backend: opt.Backend, Created: time.Now().UTC().Format(time.RFC3339Nano), Files: []Entry{}, ExternalRoots: []Root{}}
	if opt.Backend != "native" && opt.Backend != "audiobookshelf" {
		return m, errors.New("invalid backup backend")
	}
	if err := os.MkdirAll(filepath.Dir(opt.Destination), 0700); err != nil {
		return m, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(opt.Destination), ".bundle-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(stage)
	source, err := os.OpenRoot(opt.DataDir)
	if err != nil {
		return m, err
	}
	defer source.Close()
	// Credentials must be stable across the snapshot/archive; never include paths
	// discovered from database metadata as arbitrary credential file names.
	secrets := map[string][]byte{}
	for _, name := range []string{"secret.key", "core.token", "core.token.oidc.json"} {
		info, e := source.Lstat(name)
		if errors.Is(e, os.ErrNotExist) && name != "secret.key" {
			continue
		}
		if e != nil {
			return m, e
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return m, errors.New("invalid credential file")
		}
		b, e := source.ReadFile(name)
		if e != nil {
			return m, e
		}
		if name == "secret.key" && len(b) != 32 {
			return m, errors.New("secret.key must hold 32 bytes")
		}
		secrets[name] = b
	}
	snapshot := filepath.Join(stage, "pocket-audiobooks.db")
	if err = st.Backup(ctx, snapshot); err != nil {
		return m, err
	}
	db, err := readonly(snapshot)
	if err != nil {
		return m, err
	}
	roots, art, err := inspect(ctx, db)
	db.Close()
	if err != nil {
		return m, err
	}
	m.ExternalRoots = roots
	output, err := os.OpenFile(filepath.Join(stage, "archive.tar.gz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return m, err
	}
	defer output.Close()
	gz := gzip.NewWriter(output)
	tw := tar.NewWriter(gz)
	var total int64
	add := func(name string, f *os.File) error {
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxTotal-total || len(m.Files) >= maxEntries {
			return errors.New("bundle exceeds supported limits or contains a nonregular file")
		}
		hdr := &tar.Header{Name: name, Mode: 0600, Size: info.Size(), ModTime: info.ModTime(), Format: tar.FormatPAX}
		if e = tw.WriteHeader(hdr); e != nil {
			return e
		}
		hash := sha256.New()
		n, e := io.CopyN(io.MultiWriter(tw, hash), contextReader{ctx, f}, info.Size())
		if e != nil {
			return e
		}
		after, e := f.Stat()
		if e != nil {
			return e
		}
		if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			return errors.New("file changed during backup; retry")
		}
		m.Files = append(m.Files, Entry{name, n, hex.EncodeToString(hash.Sum(nil)), info.ModTime().UnixNano()})
		total += n
		return nil
	}
	f, err := os.Open(snapshot)
	if err != nil {
		return m, err
	}
	if err = add("pocket-audiobooks.db", f); err != nil {
		return m, err
	}
	names := []string{}
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	names = append(names, art...)
	for _, name := range names {
		info, e := source.Lstat(name)
		if e != nil {
			return m, e
		}
		if !info.Mode().IsRegular() {
			return m, errors.New("backup source is not a regular file")
		}
		if secret, ok := secrets[name]; ok {
			private := filepath.Join(stage, name)
			if e = os.WriteFile(private, secret, 0600); e != nil {
				return m, e
			}
			f, e = os.Open(private)
		} else {
			f, e = source.Open(name)
		}
		if e != nil {
			return m, e
		}
		if err = add(name, f); err != nil {
			return m, err
		}
	}
	for name, want := range secrets {
		got, e := source.ReadFile(name)
		if e != nil || string(got) != string(want) {
			return m, errors.New("credentials changed during backup; retry")
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	if len(data) > maxManifest {
		return m, errors.New("backup manifest too large")
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(data)), Format: tar.FormatPAX}); err != nil {
		return m, err
	}
	if _, err = tw.Write(data); err != nil {
		return m, err
	}
	if err = tw.Close(); err != nil {
		return m, err
	}
	if err = gz.Close(); err != nil {
		return m, err
	}
	if err = output.Sync(); err != nil {
		return m, err
	}
	if err = output.Close(); err != nil {
		return m, err
	}
	if err = os.Link(filepath.Join(stage, "archive.tar.gz"), opt.Destination); err != nil {
		return m, err
	}
	parent, err := os.Open(filepath.Dir(opt.Destination))
	if err != nil {
		return m, err
	}
	defer parent.Close()
	return m, parent.Sync()
}
