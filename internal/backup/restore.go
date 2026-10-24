package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

func extract(ctx context.Context, archive, stage string) (Manifest, error) {
	var m Manifest
	input, err := os.Open(archive)
	if err != nil {
		return m, err
	}
	defer input.Close()
	buffered := bufio.NewReader(contextReader{ctx, input})
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return m, err
	}
	defer gz.Close()
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	seen := map[string]Entry{}
	var total int64
	var manifest []byte
	for {
		hdr, e := tr.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return m, e
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Size < 0 || hdr.Size > maxTotal-total || len(seen) >= maxEntries {
			return m, errors.New("invalid archive entry or backup too large")
		}
		total += hdr.Size
		if hdr.Name == "manifest.json" {
			if manifest != nil || hdr.Size > maxManifest {
				return m, errors.New("invalid backup manifest")
			}
			manifest, e = io.ReadAll(tr)
			if e != nil {
				return m, e
			}
			continue
		}
		if !validName(hdr.Name) {
			return m, errors.New("unexpected or unsafe archive path")
		}
		if _, ok := seen[hdr.Name]; ok {
			return m, errors.New("duplicate archive path")
		}
		dest := filepath.Join(stage, filepath.FromSlash(hdr.Name))
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return m, err
		}
		f, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return m, e
		}
		hash := sha256.New()
		n, e := io.Copy(io.MultiWriter(f, hash), contextReader{ctx, tr})
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return m, e
		}
		if closeErr != nil {
			return m, closeErr
		}
		if n != hdr.Size {
			return m, errors.New("truncated backup entry")
		}
		if err = os.Chtimes(dest, hdr.ModTime, hdr.ModTime); err != nil {
			return m, err
		}
		seen[hdr.Name] = Entry{hdr.Name, n, hex.EncodeToString(hash.Sum(nil)), hdr.ModTime.UnixNano()}
	}
	// Consume the gzip checksum. Reject appended members/data and nonzero tar tail.
	tail, err := io.ReadAll(io.LimitReader(gz, 1<<20))
	if err != nil {
		return m, err
	}
	if len(tail) == 1<<20 || len(bytes.Trim(tail, "\x00")) != 0 {
		return m, errors.New("unexpected archive tail")
	}
	if _, err = buffered.Peek(1); !errors.Is(err, io.EOF) {
		return m, errors.New("unexpected trailing compressed data")
	}
	if manifest == nil {
		return m, errors.New("backup manifest missing")
	}
	dec := json.NewDecoder(bytes.NewReader(manifest))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&m); err != nil {
		return m, err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return m, errors.New("trailing manifest data")
	}
	if m.Format != 1 || m.App != "pocket-audiobooks" || (m.Backend != "native" && m.Backend != "audiobookshelf") || m.MediaIncluded {
		return m, errors.New("unsupported backup format")
	}
	if len(m.Files) != len(seen) {
		return m, errors.New("backup inventory mismatch")
	}
	verified := map[string]bool{}
	for _, v := range m.Files {
		actual, ok := seen[v.Name]
		if !ok || verified[v.Name] || v != actual {
			return m, fmt.Errorf("backup checksum/inventory mismatch for %q", v.Name)
		}
		verified[v.Name] = true
	}
	if !verified["pocket-audiobooks.db"] || !verified["secret.key"] || seen["secret.key"].Size != 32 {
		return m, errors.New("backup is missing database or encryption key")
	}
	for _, name := range []string{"core.token", "core.token.oidc.json"} {
		if seen[name].Size > 1<<20 {
			return m, errors.New("invalid credential file size")
		}
	}
	db, err := readonly(filepath.Join(stage, "pocket-audiobooks.db"))
	if err != nil {
		return m, err
	}
	defer db.Close()
	if err = store.CheckCompatibleSchema(ctx, db); err != nil {
		return m, err
	}
	roots, needed, err := inspect(ctx, db)
	if err != nil {
		return m, err
	}
	if !reflect.DeepEqual(roots, m.ExternalRoots) {
		return m, errors.New("external media inventory differs from database")
	}
	for _, name := range needed {
		if !verified[name] {
			return m, fmt.Errorf("managed artwork %q is absent", name)
		}
	}
	return m, nil
}

// Verify extracts into disposable private storage, checks every checksum, SQLite
// integrity/references and the artwork inventory. It never opens live media.
func Verify(ctx context.Context, archive string) (Manifest, error) {
	stage, err := os.MkdirTemp("", "pocket-verify-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(stage)
	return extract(ctx, archive, stage)
}

// Restore publishes a complete validated directory in one no-replace rename.
// The destination must not exist; the current installation is never modified.
func Restore(ctx context.Context, archive, destination string) (Manifest, error) {
	var m Manifest
	dest, err := filepath.Abs(destination)
	if err != nil {
		return m, err
	}
	if _, err = os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return m, errors.New("restore destination must not exist")
	}
	stage, err := os.MkdirTemp(filepath.Dir(dest), ".pocket-restore-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(stage)
	m, err = extract(ctx, archive, stage)
	if err != nil {
		return m, err
	}
	art := filepath.Join(stage, "native-art")
	if err = os.MkdirAll(art, 0700); err != nil {
		return m, err
	}
	db, err := sql.Open("sqlite", filepath.Join(stage, "pocket-audiobooks.db"))
	if err != nil {
		return m, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `PRAGMA foreign_keys=ON; PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL`); err == nil {
		err = (&library.Catalog{DB: db}).RebaseRestoredArtwork(ctx, art, filepath.Join(dest, "native-art"))
	}
	if err == nil {
		err = checkDB(ctx, db)
	}
	closeErr := db.Close()
	if err != nil {
		return m, err
	}
	if closeErr != nil {
		return m, closeErr
	}
	// Keep provenance without credential values; this manifest describes the input,
	// before rebasing the managed-art root to the new data directory.
	receipt, _ := json.MarshalIndent(struct {
		Restored string   `json:"restored_at"`
		Bundle   Manifest `json:"bundle"`
	}{time.Now().UTC().Format(time.RFC3339Nano), m}, "", "  ")
	if err = os.WriteFile(filepath.Join(stage, "restore-manifest.json"), receipt, 0600); err != nil {
		return m, err
	}
	if err = syncTree(stage); err != nil {
		return m, err
	}
	if err = publishDirectory(stage, dest); err != nil {
		return m, err
	}
	return m, nil
}
func syncTree(dir string) error {
	return filepath.WalkDir(dir, func(name string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	})
}
