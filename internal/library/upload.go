package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type UploadFile struct{ Name, Path string }
type UploadOptions struct {
	LibraryID, FolderID, Title, Author, Series, FFprobe string
	Files                                               []UploadFile
}

// Ingest publishes a new directory only after every uploaded audio file probes
// successfully. It never replaces existing books or follows paths outside the
// chosen root. Callers spool multipart bodies with their configured byte limit.
func (c *Catalog) Ingest(ctx context.Context, uid int64, opt UploadOptions) (id string, err error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return "", err
	}
	if !a.Permissions.Upload || !a.allowsLibrary(opt.LibraryID) {
		return "", ErrNotFound
	}
	title := strings.TrimSpace(opt.Title)
	if title == "" || title == "." || title == ".." || filepath.Base(title) != title || strings.ContainsAny(title, "\\\x00") || len(title) > 240 || len(opt.Files) == 0 || len(opt.Files) > 1024 {
		return "", ErrInvalid
	}
	folders, err := c.scanFolders(ctx, opt.LibraryID)
	if err != nil {
		return "", err
	}
	var folder *scanFolder
	for i := range folders {
		if folders[i].ID == opt.FolderID {
			folder = &folders[i]
			break
		}
	}
	if folder == nil {
		return "", ErrNotFound
	}
	var typ string
	if err = c.DB.QueryRowContext(ctx, `SELECT media_type FROM catalog_libraries WHERE id=?`, opt.LibraryID).Scan(&typ); err != nil {
		return "", err
	}
	if typ != "book" {
		return "", ErrInvalid
	}
	root, err := os.OpenRoot(folder.Root.local)
	if err != nil {
		return "", err
	}
	defer root.Close()
	target := filepath.Join(folder.Relative, title)
	if _, err = root.Stat(target); err == nil {
		return "", ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	temp := filepath.Join(folder.Relative, ".pocket-upload-"+newID())
	if err = root.Mkdir(temp, 0755); err != nil {
		return "", err
	}
	published, committed := false, false
	defer func() {
		if !committed {
			cleanup := temp
			if published {
				cleanup = target
			}
			_ = root.RemoveAll(cleanup)
		}
	}()
	now := time.Now().UnixMilli()
	book := &scanBook{Item: Item{ID: newID(), LibraryID: opt.LibraryID, MediaID: newID(), MediaType: "book", AddedAt: now, UpdatedAt: now, Tags: []string{}}, Path: filepath.Join(folder.Source, title), FolderID: folder.ID, New: true, Doc: sourceRow{"item": sourceRow{}, "media": sourceRow{"title": title, "authorName": opt.Author, "seriesName": opt.Series}, "authors": []any{}, "series": []any{}, "episodes": []any{}}}
	if !a.Permissions.AllTags && !a.Permissions.DenySelectedTags {
		if len(a.Permissions.Tags) == 0 {
			return "", ErrNotFound
		}
		book.Item.Tags = append([]string{}, a.Permissions.Tags...)
	}
	seen := map[string]bool{}
	hasMedia := false
	for _, input := range opt.Files {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		name := input.Name
		key := strings.ToLower(name)
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "\\\x00") || name == "." || name == ".." || seen[key] {
			return "", ErrInvalid
		}
		seen[key] = true
		role := scanRole(name)
		if role == "" {
			return "", fmt.Errorf("unsupported upload file type")
		}
		hasMedia = hasMedia || role == "audio" || role == "ebook"
		src, e := os.Open(input.Path)
		if e != nil {
			return "", e
		}
		dst, e := root.OpenFile(filepath.Join(temp, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if e != nil {
			src.Close()
			return "", e
		}
		_, copyErr := io.Copy(dst, src)
		src.Close()
		syncErr := dst.Sync()
		closeErr := dst.Close()
		if e = errors.Join(copyErr, syncErr, closeErr); e != nil {
			return "", e
		}
		staged, e := root.Open(filepath.Join(temp, name))
		if e != nil {
			return "", e
		}
		stat, e := staged.Stat()
		if e != nil {
			staged.Close()
			return "", e
		}
		file := &scannedFile{ID: newID(), Role: role, RootID: folder.Root.id, Relative: filepath.Join(target, name), Source: filepath.Join(book.Path, name), Name: name, MIME: mimeFor(name), Size: stat.Size(), Modified: stat.ModTime().UnixNano(), Identity: fileIdentity(stat), Doc: sourceRow{}}
		if role == "audio" {
			probe, e := Probe(ctx, opt.FFprobe, staged)
			if e != nil {
				staged.Close()
				return "", e
			}
			file.Duration = probe.Duration
			file.Probe = &probe
		}
		staged.Close()
		file.Doc = sourceRow{"ino": file.ID, "diskInode": fileInode(stat), "duration": file.Duration, "mimeType": file.MIME, "exclude": false, "metadata": sourceRow{"path": file.Source, "filename": name, "relPath": file.Relative, "size": stat.Size(), "mtimeMs": stat.ModTime().UnixMilli()}}
		book.Files = append(book.Files, file)
	}
	if !hasMedia {
		return "", ErrNoAudio
	}
	sort.SliceStable(book.Files, func(i, j int) bool { return naturalLess(book.Files[i].Name, book.Files[j].Name) })
	// Recheck access immediately before publishing; revocation during a long
	// transfer cannot grant a new book to an account that no longer has access.
	current, err := c.Account(ctx, uid)
	if err != nil || !current.Permissions.Upload || !current.allows(book.Item) {
		return "", ErrNotFound
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	report := ScanReport{}
	if err = c.commitScannedBook(ctx, tx, book, &report); err != nil {
		return "", err
	}
	// Publishing a directory is atomic. Go's directory Rename refuses an
	// existing destination, so a concurrent upload cannot replace another book.
	if err = root.Rename(temp, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", ErrConflict
		}
		return "", err
	}
	published = true
	if err = tx.Commit(); err != nil {
		return "", err
	}
	committed = true
	return book.Item.ID, nil
}
