package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ScanOptions struct {
	ImportMetadata bool // Explicitly replace existing fields from sidecar files.
	Started        func(string)
	FFprobe        string
	// FFmpeg and ArtDir enable copying embedded cover art into managed artwork
	// storage for books without a cover image; either empty disables it.
	FFmpeg, ArtDir string
	Force          bool
}

// ScanFailure names a book (or unreadable folder) skipped by one scan.
type ScanFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}
type ScanReport struct {
	Invalid     int           `json:"invalid"`
	Error       string        `json:"error,omitempty"`
	ID          string        `json:"id"`
	LibraryID   string        `json:"library_id"`
	Added       int           `json:"added"`
	Updated     int           `json:"updated"`
	Missing     int           `json:"missing"`
	Files       int           `json:"files"`
	Probed      int           `json:"probed"`
	Unchanged   int           `json:"unchanged"`
	Quarantined int           `json:"quarantined"`
	Failures    []ScanFailure `json:"failures,omitempty"`
}

const maxScanFailures = 200

// quarantine records a book the scan skipped. Its existing record (if any) is
// left exactly as it was and the rest of the library still commits.
func (r *ScanReport) quarantine(path string, err error) {
	r.Quarantined++
	if len(r.Failures) < maxScanFailures {
		r.Failures = append(r.Failures, ScanFailure{Path: path, Error: err.Error()})
	}
	slog.Warn("library scan skipped book", "library", r.LibraryID, "path", path, "error", err)
}

type scanFolder struct {
	ID, Source, Relative string
	Root                 importRoot
}
type scannedFile struct {
	Missing                                                  bool
	PreviousSource                                           string
	ID, RootID, Relative, Source, Name, Role, MIME, Identity string
	Size, Modified                                           int64
	Duration                                                 float64
	Index                                                    int
	Excluded                                                 bool
	Doc                                                      sourceRow
	Probe                                                    *ProbeResult
	OldItem                                                  string
}
type scanBook struct {
	Sidecar        *EditOptions
	Item           Item
	Doc            sourceRow
	Files          []*scannedFile
	Path, FolderID string
	// Rel is a new book's path below its library folder, for folder parsing.
	Rel             string
	New             bool
	Quarantined     bool
	RebuildChapters bool
}

func (c *Catalog) CanScan(ctx context.Context, uid int64, lib string) error {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	if !a.Permissions.Update || !a.allowsLibrary(lib) || !a.Permissions.AllTags || !a.Permissions.Explicit {
		return ErrNotFound
	}
	var typ string
	if err = c.DB.QueryRowContext(ctx, `SELECT media_type FROM catalog_libraries WHERE id=? AND NOT EXISTS(SELECT 1 FROM catalog_library_state s WHERE s.library_id=catalog_libraries.id AND s.archived=1)`, lib).Scan(&typ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if typ != "book" {
		return fmt.Errorf("podcast libraries use episode downloads, not book scanning")
	}
	return nil
}
func (c *Catalog) scanFolders(ctx context.Context, lib string) ([]scanFolder, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT id,source_path,local_path FROM catalog_roots ORDER BY length(source_path) DESC`)
	if err != nil {
		return nil, err
	}
	roots := []importRoot{}
	for rows.Next() {
		var r importRoot
		if err = rows.Scan(&r.id, &r.source, &r.local); err != nil {
			rows.Close()
			return nil, err
		}
		roots = append(roots, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = c.DB.QueryContext(ctx, `SELECT id,document FROM catalog_entities WHERE kind='libraryFolders' AND library_id=? ORDER BY id`, lib)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	folders := []scanFolder{}
	for rows.Next() {
		var id, doc string
		if err = rows.Scan(&id, &doc); err != nil {
			return nil, err
		}
		var d sourceRow
		if err = json.Unmarshal([]byte(doc), &d); err != nil {
			return nil, err
		}
		path := str(d["path"])
		root, rel, err := mapPath(path, roots)
		if err != nil {
			return nil, err
		}
		folders = append(folders, scanFolder{ID: id, Source: path, Relative: rel, Root: root})
	}
	if len(folders) == 0 {
		return nil, fmt.Errorf("library has no folders")
	}
	return folders, rows.Err()
}
func scanRole(name string) string {
	switch strings.ToLower(filepath.Base(name)) {
	case "metadata.json", "metadata.abs", "desc.txt", "reader.txt":
		return "supplement"
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".opf":
		return "supplement"
	case ".mp3", ".m4b", ".m4a", ".mp4", ".flac", ".ogg", ".opus", ".wav", ".aac", ".wma", ".aiff":
		return "audio"
	case ".epub", ".pdf", ".mobi", ".azw3", ".cbz", ".cbr":
		return "ebook"
	case ".jpg", ".jpeg", ".png", ".webp":
		if b := strings.ToLower(strings.TrimSuffix(filepath.Base(name), ext)); b == "cover" || b == "folder" {
			return "cover"
		}
	}
	return ""
}

// looseImage is an image without a cover/folder name. It becomes the cover
// only when it is the single image directly in a book folder without one.
func looseImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return scanRole(name) == ""
	}
	return false
}
func fileIdentity(info fs.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return ""
}
func fileInode(info fs.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(st.Ino, 10)
	}
	return ""
}
func scanStableID(root, path string) string {
	sum := sha256.Sum256([]byte(root + "\x00" + path))
	return "file-" + hex.EncodeToString(sum[:12])
}
func under(path, parent string) bool {
	return path == parent || strings.HasPrefix(path, parent+string(filepath.Separator))
}

// Scan probes outside the write transaction and commits one consistent catalog
// change only after every folder has been walked successfully. Missing source
// folders, unreadable files, cancellation and invalid media never erase records.
// Existing IDs, edits, chapters and personal state survive incremental rescans.
func (c *Catalog) Scan(ctx context.Context, uid int64, lib string, opt ScanOptions) (report ScanReport, err error) {
	report = ScanReport{ID: newID(), LibraryID: lib}
	if !mediaLayoutMu.TryLock() {
		return report, fmt.Errorf("%w: a library scan or file change is active", ErrConflict)
	}
	defer mediaLayoutMu.Unlock()
	if err = c.CanScan(ctx, uid, lib); err != nil {
		return
	}
	var adopting int
	if err = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_tool_jobs j JOIN catalog_items i ON i.id=j.item_id WHERE i.library_id=? AND j.status='applying'`, lib).Scan(&adopting); err != nil {
		return report, err
	}
	if adopting > 0 {
		return report, fmt.Errorf("%w: finish or cancel pending audio file adoption first", ErrConflict)
	}
	var libraryRevision int64
	if err = c.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM catalog_library_state WHERE library_id=?),0)`, lib).Scan(&libraryRevision); err != nil {
		return report, err
	}
	folders, err := c.scanFolders(ctx, lib)
	if err != nil {
		return report, err
	}
	_, err = c.DB.ExecContext(ctx, `INSERT INTO catalog_scan_runs(id,library_id,started_at,finished_at,status,report) VALUES(?,?,?,NULL,'running','{}')`, report.ID, lib, time.Now().UnixMilli())
	if err != nil {
		return report, fmt.Errorf("start scan (another scan may be running): %w", err)
	}
	if opt.Started != nil {
		opt.Started(report.ID)
	}
	defer func() {
		status := "complete"
		if err != nil {
			status = "failed"
			report.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				status = "cancelled"
			}
		}
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, finishErr := c.DB.ExecContext(finishCtx, `UPDATE catalog_scan_runs SET finished_at=?,status=?,report=? WHERE id=?`, time.Now().UnixMilli(), status, raw(report), report.ID)
		if err == nil {
			err = finishErr
		}
	}()
	books := map[string]*scanBook{}
	oldByPath := map[string]*scannedFile{}
	oldByInode := map[string]*scannedFile{}
	rows, err := c.DB.QueryContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE library_id=?`, lib)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		it, e := scanItem(rows)
		if e != nil {
			rows.Close()
			return report, e
		}
		var doc sourceRow
		if e = json.Unmarshal(it.Document, &doc); e != nil {
			rows.Close()
			return report, e
		}
		books[it.ID] = &scanBook{Item: it, Doc: doc, Path: str(object(doc["item"])["path"]), FolderID: str(object(doc["item"])["libraryFolderId"])}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	rows, err = c.DB.QueryContext(ctx, `SELECT f.item_id,f.id,f.root_id,f.relative_path,f.name,f.role,f.mime,f.size,f.duration,f.track_index,f.excluded,f.document,COALESCE(o.modified_ns,0),COALESCE(o.identity,'') FROM catalog_files f JOIN catalog_items i ON i.id=f.item_id LEFT JOIN catalog_file_observations o ON o.item_id=f.item_id AND o.file_id=f.id WHERE i.library_id=?`, lib)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		f := &scannedFile{}
		var doc string
		if err = rows.Scan(&f.OldItem, &f.ID, &f.RootID, &f.Relative, &f.Name, &f.Role, &f.MIME, &f.Size, &f.Duration, &f.Index, &f.Excluded, &doc, &f.Modified, &f.Identity); err != nil {
			rows.Close()
			return report, err
		}
		if err = json.Unmarshal([]byte(doc), &f.Doc); err != nil {
			rows.Close()
			return report, err
		}
		oldByPath[f.RootID+"/"+f.Relative] = f
		ino := str(f.Doc["diskInode"])
		if ino == "" {
			ino = str(f.Doc["ino"])
		}
		if ino != "" {
			oldByInode[f.RootID+"/"+ino] = f
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	removed := map[string]bool{}
	rows, err = c.DB.QueryContext(ctx, `SELECT item_id FROM catalog_removed_items`)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return report, err
		}
		removed[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	// A generated file is invisible to scans until its recovery journal commits.
	pending := map[string]bool{}
	pendingRows, e := c.DB.QueryContext(ctx, `SELECT apply_root,apply_path FROM catalog_tool_jobs WHERE apply_path<>'' AND status<>'applied'`)
	if e != nil {
		return report, e
	}
	for pendingRows.Next() {
		var root, path string
		if e = pendingRows.Scan(&root, &path); e != nil {
			pendingRows.Close()
			return report, e
		}
		pending[root+"/"+path] = true
	}
	e = pendingRows.Err()
	pendingRows.Close()
	if e != nil {
		return report, e
	}
	seen := map[string]bool{}
	newGroups := map[string]*scanBook{}
	// Folders whose listing failed: books in or around them are skipped so an
	// unreadable directory never reads as deleted files.
	failedDirs := []string{}
	existingByPath := map[string]*scanBook{}
	for _, b := range books {
		if prev := existingByPath[b.Path]; b.Path != "" && (prev == nil || b.Item.ID < prev.Item.ID) {
			existingByPath[b.Path] = b
		}
	}
	for _, folder := range folders {
		root, e := os.OpenRoot(folder.Root.local)
		if e != nil {
			return report, fmt.Errorf("open library root: %w", e)
		}
		e = func() error {
			defer root.Close()
			info, e := root.Stat(folder.Relative)
			if e != nil || !info.IsDir() {
				return fmt.Errorf("library folder is unavailable; scan left catalog unchanged")
			}
			type candidate struct {
				path  string
				entry fs.DirEntry
				role  string
			}
			candidates := []candidate{}
			top := filepath.ToSlash(folder.Relative)
			e = fs.WalkDir(root.FS(), top, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					if path == top {
						return walkErr
					}
					source := filepath.Join(folder.Root.source, filepath.FromSlash(path))
					failedDirs = append(failedDirs, source)
					report.quarantine(source, walkErr)
					if entry != nil && entry.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				if e := ctx.Err(); e != nil {
					return e
				}
				if entry.Type()&fs.ModeSymlink != 0 {
					return nil
				}
				if entry.IsDir() {
					if path != top {
						if strings.HasPrefix(entry.Name(), ".") {
							return fs.SkipDir
						}
						// Audiobookshelf semantics: a folder holding a file named
						// .ignore is skipped together with everything below it.
						if _, e := root.Lstat(filepath.Join(filepath.FromSlash(path), ".ignore")); e == nil {
							return fs.SkipDir
						}
					}
					return nil
				}
				role := scanRole(entry.Name())
				if role == "" && looseImage(entry.Name()) {
					role = "image"
				}
				if role == "" {
					return nil
				}
				path = filepath.FromSlash(path)
				if pending[folder.Root.id+"/"+path] {
					return nil
				}
				candidates = append(candidates, candidate{path, entry, role})
				return nil
			})
			if e != nil {
				return e
			}
			// A folder that holds another book's audio below it is a container
			// (an author or series folder), never a book of its own. Files that
			// already belong to a book count at that book's path, so existing
			// layouts keep their grouping.
			bookDirs := map[string]bool{}
			for _, cand := range candidates {
				if cand.role != "audio" {
					continue
				}
				source := filepath.Join(folder.Root.source, cand.path)
				dir := discGroup(source, folder.Source)
				if old := oldByPath[folder.Root.id+"/"+cand.path]; old != nil && books[old.OldItem] != nil && books[old.OldItem].Path != "" {
					dir = books[old.OldItem].Path
				}
				if dir != folder.Source && under(dir, folder.Source) {
					bookDirs[dir] = true
				}
			}
			containers := map[string]bool{}
			for dir := range bookDirs {
				for p := filepath.Dir(dir); p != folder.Source && under(p, folder.Source) && !containers[p]; p = filepath.Dir(p) {
					containers[p] = true
				}
			}
			for _, cand := range candidates {
				if e := ctx.Err(); e != nil {
					return e
				}
				path, entry, role := cand.path, cand.entry, cand.role
				key := folder.Root.id + "/" + path
				sourcePath := filepath.Join(folder.Root.source, path)
				longest, owner := -1, ""
				for id, book := range books {
					if book.Path != "" && under(sourcePath, book.Path) && len(book.Path) > longest {
						longest, owner = len(book.Path), id
					}
				}
				if removed[owner] {
					continue
				}
				if old := oldByPath[key]; old != nil && removed[old.OldItem] {
					continue
				}
				if seen[key] {
					continue
				}
				seen[key] = true
				info, e := entry.Info()
				if e != nil {
					// The file vanished or became unreadable after listing.
					if old := oldByPath[key]; old != nil && books[old.OldItem] != nil {
						books[old.OldItem].Quarantined = true
					}
					report.quarantine(sourcePath, e)
					continue
				}
				if !info.Mode().IsRegular() {
					continue
				}
				old := oldByPath[key]
				if old == nil {
					if prev := oldByInode[folder.Root.id+"/"+fileInode(info)]; prev != nil && prev.Size == info.Size() {
						if _, e = root.Stat(prev.Relative); errors.Is(e, fs.ErrNotExist) {
							old = prev
						}
					}
				}
				if old != nil && removed[old.OldItem] {
					continue
				}
				f := &scannedFile{ID: scanStableID(folder.Root.id, path), RootID: folder.Root.id, Relative: path, Source: sourcePath, Name: entry.Name(), Role: role, MIME: mimeFor(path), Size: info.Size(), Modified: info.ModTime().UnixNano(), Identity: fileIdentity(info), Doc: sourceRow{}}
				unchanged := false
				if old != nil {
					f.PreviousSource = str(object(old.Doc["metadata"])["path"])
					f.ID, f.Duration, f.Index, f.Excluded, f.Doc, f.OldItem = old.ID, old.Duration, old.Index, old.Excluded, old.Doc, old.OldItem
					if old.Size == f.Size {
						unchanged = old.Modified == f.Modified
						if old.Modified == 0 {
							unchanged = number(object(old.Doc["metadata"])["mtimeMs"]) == float64(info.ModTime().UnixMilli())
						}
					}
				}
				var book *scanBook
				if f.OldItem != "" {
					book = books[f.OldItem]
				}
				if book == nil {
					group := discGroup(f.Source, folder.Source)
					if group == folder.Source || containers[group] {
						// Loose files beside other books are each their own book;
						// artwork and sidecars at that level describe no book.
						if role != "audio" && role != "ebook" {
							continue
						}
						group = strings.TrimSuffix(f.Source, filepath.Ext(f.Source))
						book = existingByPath[group]
					} else {
						for dir := group; dir != folder.Source && under(dir, folder.Source) && !containers[dir]; dir = filepath.Dir(dir) {
							if book = existingByPath[dir]; book != nil {
								break
							}
							if bookDirs[dir] {
								group = dir
								break
							}
						}
					}
					if book == nil {
						book = newGroups[group]
					}
					if book == nil {
						now := time.Now().UnixMilli()
						rel, _ := filepath.Rel(folder.Source, group)
						book = &scanBook{Item: Item{ID: newID(), LibraryID: lib, MediaID: newID(), MediaType: "book", AddedAt: now, UpdatedAt: now, Tags: []string{}}, Doc: sourceRow{"item": sourceRow{}, "media": sourceRow{}, "authors": []any{}, "series": []any{}, "episodes": []any{}}, Path: group, Rel: rel, FolderID: folder.ID, New: true}
						newGroups[group] = book
						books[book.Item.ID] = book
					}
				}
				if book.Quarantined {
					continue
				}
				if role == "audio" && unchanged && old != nil && f.Duration <= 0 && !opt.Force {
					// Imported zero-duration audio was already unusable at the
					// source. Preserve it and report it without blocking healthy
					// books or pretending the file was repaired by migration.
					report.Invalid++
					report.Unchanged++
				} else if role == "audio" && (!unchanged || opt.Force || f.Duration <= 0) {
					input, e := root.Open(path)
					if e != nil {
						book.Quarantined = true
						report.quarantine(book.Path, fmt.Errorf("open %s: %w", path, e))
						continue
					}
					probe, e := Probe(ctx, opt.FFprobe, input)
					input.Close()
					report.Probed++
					if e != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						if old == nil || old.Duration > 0 {
							book.Quarantined = true
							report.quarantine(book.Path, fmt.Errorf("audio metadata could not be read for %s: %w", path, e))
							continue
						}
						report.Invalid++
					} else {
						f.Probe = &probe
						f.Duration = probe.Duration
					}
				} else if unchanged {
					report.Unchanged++
				}

				f.Doc["diskInode"], f.Doc["isMissing"] = fileInode(info), false
				f.Doc["ino"], f.Doc["duration"], f.Doc["mimeType"], f.Doc["exclude"] = f.ID, f.Duration, f.MIME, f.Excluded
				f.Doc["metadata"] = sourceRow{"path": f.Source, "relPath": path, "filename": f.Name, "ext": filepath.Ext(path), "size": f.Size, "mtimeMs": info.ModTime().UnixMilli()}
				if f.Probe != nil {
					f.Doc["chapters"], f.Doc["codec"], f.Doc["metaTags"] = f.Probe.Chapters, f.Probe.Codec, f.Probe.Tags
				}
				book.Files = append(book.Files, f)
				if role != "image" {
					report.Files++
				}
			}
			return nil
		}()
		if e != nil {
			return report, e
		}
	}
	for _, book := range books {
		for _, dir := range failedDirs {
			if book.Path != "" && (under(book.Path, dir) || under(dir, book.Path)) {
				book.Quarantined = true
			}
		}
		// Like Audiobookshelf, a single image directly in a book folder is its
		// cover when there is no cover.* or folder.* image.
		files := []*scannedFile{}
		images := []*scannedFile{}
		hasCover := false
		for _, f := range book.Files {
			if f.Role == "image" {
				if filepath.Dir(f.Source) == book.Path {
					images = append(images, f)
				}
				continue
			}
			hasCover = hasCover || f.Role == "cover"
			files = append(files, f)
		}
		current := str(object(book.Doc["media"])["coverPath"])
		if !hasCover && len(images) == 1 && (current == "" || current == images[0].Source) {
			images[0].Role = "cover"
			files = append(files, images[0])
			report.Files++
		}
		book.Files = files
	}
	// Sidecars and embedded artwork are bounded and read before taking the
	// database write lock. A book whose sidecar cannot be read is skipped.
	art := false
	for _, book := range books {
		if book.Quarantined || removed[book.Item.ID] || len(book.Files) == 0 {
			continue
		}
		if book.New || opt.ImportMetadata {
			book.Sidecar, err = c.readSidecars(ctx, book.Path, book.Files)
			if err != nil {
				if ctx.Err() != nil {
					return report, ctx.Err()
				}
				book.Quarantined = true
				report.quarantine(book.Path, err)
				continue
			}
		}
		if opt.FFmpeg == "" || opt.ArtDir == "" || str(object(book.Doc["media"])["coverPath"]) != "" {
			continue
		}
		hasCover := false
		var embedded *scannedFile
		for _, f := range book.Files {
			hasCover = hasCover || f.Role == "cover"
			if embedded == nil && f.Role == "audio" && f.Probe != nil && f.Probe.Cover {
				embedded = f
			}
		}
		if hasCover || embedded == nil {
			continue
		}
		cover, e := c.extractEmbeddedCover(ctx, opt.FFmpeg, opt.ArtDir, embedded)
		if e != nil {
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			// Artwork is optional; the book still commits without it.
			slog.Warn("library scan could not extract embedded cover", "path", embedded.Source, "error", e)
			continue
		}
		book.Files = append(book.Files, cover)
		art = true
	}
	if err = c.CanScan(ctx, uid, lib); err != nil {
		return report, err
	}
	account, err := c.Account(ctx, uid)
	if err != nil {
		return report, err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	var currentRevision int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM catalog_library_state WHERE library_id=?),0)`, lib).Scan(&currentRevision); err != nil {
		return report, err
	}
	if currentRevision != libraryRevision {
		return report, fmt.Errorf("%w: library settings changed during scan", ErrConflict)
	}
	if art {
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_roots VALUES('native-art','/pocket-native-art',?) ON CONFLICT(id) DO UPDATE SET local_path=excluded.local_path`, opt.ArtDir); err != nil {
			return report, err
		}
	}
	ordered := make([]*scanBook, 0, len(books))
	for _, book := range books {
		ordered = append(ordered, book)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	for _, book := range ordered {
		if removed[book.Item.ID] || book.Quarantined {
			continue
		}
		if err = ctx.Err(); err != nil {
			return report, err
		}
		// Each book commits inside a savepoint: a failure rolls back only
		// that book and the rest of the library still commits.
		if _, err = tx.ExecContext(ctx, `SAVEPOINT scan_book`); err != nil {
			return report, err
		}
		before := report
		e := c.commitScanBook(ctx, tx, account, book, folders, oldByPath, &report)
		if e != nil {
			if err = ctx.Err(); err != nil {
				return report, err
			}
			if _, err = tx.ExecContext(ctx, `ROLLBACK TO scan_book`); err != nil {
				return report, err
			}
			report = before
			report.quarantine(book.Path, e)
		}
		if _, err = tx.ExecContext(ctx, `RELEASE scan_book`); err != nil {
			return report, err
		}
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

// commitScanBook writes one walked book. Any error leaves the book's stored
// record untouched (the caller rolls back to a savepoint).
func (c *Catalog) commitScanBook(ctx context.Context, tx *sql.Tx, account Account, book *scanBook, folders []scanFolder, oldByPath map[string]*scannedFile, report *ScanReport) error {
	if !book.New {
		var version int64
		if err := tx.QueryRowContext(ctx, `SELECT updated_at FROM catalog_items WHERE id=?`, book.Item.ID).Scan(&version); err != nil {
			return err
		}
		if version != book.Item.UpdatedAt {
			return fmt.Errorf("%w: book changed during scan", ErrConflict)
		}
	}
	if book.New {
		audioOrEbook := false
		for _, f := range book.Files {
			audioOrEbook = audioOrEbook || f.Role == "audio" || f.Role == "ebook"
		}
		if !audioOrEbook {
			return nil
		}
		// An upload or another writer may have published this folder since
		// the catalog was read; never create a second item for one path.
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_items WHERE library_id=? AND json_extract(document,'$.item.path')=?`, book.Item.LibraryID, book.Path).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return nil
		}
	}
	if len(book.Files) == 0 {
		// Never infer a deletion from absence; mark only items in scanned folders.
		covered := false
		for _, folder := range folders {
			if under(book.Path, folder.Source) {
				covered = true
			}
		}
		if covered {
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_items SET missing=1,updated_at=MAX(updated_at+1,?) WHERE id=?`, time.Now().UnixMilli(), book.Item.ID); err != nil {
				return err
			}
			report.Missing++
		}
		return nil
	}
	// Preserve missing tracks and their IDs so a partial disappearance cannot
	// silently shorten a book or shift every subsequent progress position.
	ids := map[string]bool{}
	book.Item.Missing = false
	moved := ""
	consistent := true
	added, manual := false, false
	for _, f := range book.Files {
		ids[f.ID] = true
		if f.Role == "audio" {
			added = added || (!book.New && f.OldItem != book.Item.ID)
			manual = manual || f.Doc["manuallyVerified"] == true
		}
		if f.PreviousSource != "" && under(f.PreviousSource, book.Path) {
			suffix := strings.TrimPrefix(f.PreviousSource, book.Path)
			candidate := strings.TrimSuffix(f.Source, suffix)
			if moved == "" {
				moved = candidate
			} else if moved != candidate {
				consistent = false
			}
		}
	}
	if consistent && moved != "" {
		book.Path = moved
	}
	for _, old := range oldByPath {
		if old.OldItem == book.Item.ID && !ids[old.ID] && (old.Role == "audio" || old.Role == "ebook") {
			old.Missing = true
			old.Doc["isMissing"] = true
			book.Item.Missing = true
			book.Files = append(book.Files, old)
		}
	}
	if book.Item.Missing {
		report.Missing++
	}
	// A file added to an existing book takes its natural place (disc, track
	// tag, file name) instead of sorting last, unless someone arranged the
	// tracks by hand. Chapters generated one-per-file follow the new order.
	natural := added && !manual
	book.RebuildChapters = natural && fileDerivedChapters(object(book.Doc["media"]))
	sort.SliceStable(book.Files, func(i, j int) bool {
		a, b := book.Files[i], book.Files[j]
		ai, bi := a.Index, b.Index
		if ai == 0 || natural {
			ai = 1 << 30
		}
		if bi == 0 || natural {
			bi = 1 << 30
		}
		if ai != bi {
			return ai < bi
		}
		if ai == 1<<30 && bi == 1<<30 {
			ad, at := discOrder(a)
			bd, bt := discOrder(b)
			if ad != bd {
				return ad < bd
			}
			if at == 0 {
				at = 1 << 30
			}
			if bt == 0 {
				bt = 1 << 30
			}
			if at != bt {
				return at < bt
			}
		}
		if a.Name == b.Name {
			return naturalLess(a.Source, b.Source)
		}
		return naturalLess(a.Name, b.Name)
	})
	if err := c.commitScannedBook(ctx, tx, book, report); err != nil {
		return err
	}
	if book.Sidecar != nil {
		current, err := scanItem(tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE id=?`, book.Item.ID))
		if err != nil {
			return err
		}
		book.Sidecar.Expected = current.UpdatedAt
		if err = editItem(ctx, tx, account, current, *book.Sidecar); err != nil {
			return fmt.Errorf("sidecar: %w", err)
		}
	}
	return nil
}

// fileDerivedChapters reports whether a book's chapters are the scanner's
// one-chapter-per-file defaults rather than embedded or edited chapters.
func fileDerivedChapters(media sourceRow) bool {
	chapters := array(media["chapters"])
	names := []string{}
	for _, v := range array(media["audioFiles"]) {
		d := object(v)
		if d["exclude"] == true {
			continue
		}
		names = append(names, str(object(d["metadata"])["filename"]))
	}
	if len(chapters) == 0 || len(chapters) != len(names) {
		return false
	}
	for i, ch := range chapters {
		title := str(object(ch)["title"])
		if title != names[i] && title != strings.TrimSuffix(names[i], filepath.Ext(names[i])) {
			return false
		}
	}
	return true
}

// extractEmbeddedCover copies an audio file's attached picture into the
// content-addressed artwork store used for uploaded covers.
func (c *Catalog) extractEmbeddedCover(ctx context.Context, ffmpeg, artDir string, f *scannedFile) (*scannedFile, error) {
	input, err := c.openCatalogFile(ctx, f.RootID, f.Relative)
	if err != nil {
		return nil, err
	}
	data, format, err := ExtractCover(ctx, ffmpeg, input)
	input.Close()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + "." + format
	if err = os.MkdirAll(artDir, 0755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(artDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	output, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err == nil {
		_, err = output.Write(data)
		if err == nil {
			err = output.Sync()
		}
		if closeErr := output.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = root.Remove(name)
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	path := "/pocket-native-art/" + name
	return &scannedFile{ID: "native-cover", RootID: "native-art", Relative: name, Source: path, Name: name, Role: "cover", MIME: "image/" + format, Size: int64(len(data)), Doc: sourceRow{"role": "cover", "metadata": sourceRow{"path": path, "filename": name}}}, nil
}

// Natural ordering makes track 2 precede track 10 without relying on directory order.
func naturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for len(a) > 0 && len(b) > 0 {
		if a[0] >= '0' && a[0] <= '9' && b[0] >= '0' && b[0] <= '9' {
			i, j := 0, 0
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			x, y := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if len(x) != len(y) {
				return len(x) < len(y)
			}
			if x != y {
				return x < y
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func (c *Catalog) commitScannedBook(ctx context.Context, tx *sql.Tx, b *scanBook, report *ScanReport) error {
	media := object(b.Doc["media"])
	suppliedTitle, suppliedAuthor := str(media["title"]), str(media["authorName"])
	item := object(b.Doc["item"])
	var authors []string
	seriesName, sequence := str(media["seriesName"]), ""
	if b.New {
		// Precedence, lowest first: folder layout, embedded tags, then values
		// supplied by an upload; sidecars are applied afterwards by editItem.
		path := parseBookPath(b.Rel)
		title := path.Title
		if title == "" {
			title = filepath.Base(b.Path)
		}
		if path.Author != "" {
			authors = splitNames(path.Author)
		}
		narrators, year := path.Narrators, path.Year
		if seriesName == "" {
			seriesName, sequence = path.Series, path.Sequence
		}
		if path.ASIN != "" {
			media["asin"] = path.ASIN
		}
		audioFiles := 0
		for _, f := range b.Files {
			if f.Role == "audio" {
				audioFiles++
			}
		}
		var tags *tagMetadata
		for _, f := range b.Files {
			if f.Probe == nil {
				continue
			}
			t := readTags(f.Probe.Tags, audioFiles == 1)
			if tags == nil || (len(tags.Authors) == 0 && len(t.Authors) > 0) {
				tags = &t
			}
			if len(tags.Authors) > 0 {
				break
			}
		}
		if tags != nil {
			if tags.Title != "" {
				title = tags.Title
			}
			if len(tags.Authors) > 0 {
				authors = tags.Authors
			}
			if len(tags.Narrators) > 0 {
				narrators = tags.Narrators
			}
			if tags.Series != "" && str(media["seriesName"]) == "" {
				seriesName, sequence = tags.Series, tags.Sequence
			}
			if tags.Year != "" {
				year = tags.Year
			}
			media["description"] = tags.Description
			for k, v := range map[string]string{"subtitle": tags.Subtitle, "publisher": tags.Publisher, "language": tags.Language, "asin": tags.ASIN, "isbn": tags.ISBN} {
				if v != "" {
					media[k] = v
				}
			}
			if tags.Genre != "" {
				media["genres"] = []string{tags.Genre}
			}
		}
		if year != "" {
			media["publishedYear"] = year
		}
		if len(narrators) > 0 {
			media["narrators"] = narrators
		}
		if suppliedTitle != "" {
			title = suppliedTitle
		}
		if suppliedAuthor != "" {
			authors = []string{suppliedAuthor}
		}
		b.Item.Title = title
		b.Item.Author = strings.Join(authors, ", ")
		media["title"] = b.Item.Title
	}
	audio := []any{}
	chapters := []Chapter{}
	duration := 0.0
	audioIndex := 0
	ebooks := []sourceRow{}
	for _, f := range b.Files {
		if f.Role == "audio" {
			audioIndex++
			f.Index = audioIndex
			f.Doc["index"] = audioIndex
			audio = append(audio, f.Doc)
			if !f.Excluded {
				if f.Probe != nil && len(f.Probe.Chapters) > 0 {
					for _, ch := range f.Probe.Chapters {
						ch.ID = len(chapters)
						ch.Start += duration
						ch.End += duration
						chapters = append(chapters, ch)
					}
				} else {
					chapters = append(chapters, Chapter{ID: len(chapters), Start: duration, End: duration + f.Duration, Title: strings.TrimSuffix(f.Name, filepath.Ext(f.Name))})
				}
				duration += f.Duration
			}
		}
		if f.Role == "ebook" {
			ebooks = append(ebooks, f.Doc)
		}
		if f.Role == "cover" && (b.New || str(media["coverPath"]) == "") {
			media["coverPath"] = f.Source
		}
	}
	if len(ebooks) > 0 {
		media["ebookFile"] = pickEbook(ebooks, object(media["ebookFile"]))
	}
	// Imported/editable chapters and descriptive metadata win over auto-extraction.
	if b.New || b.RebuildChapters || len(array(media["chapters"])) == 0 {
		media["chapters"] = chapters
	}
	media["audioFiles"], media["duration"] = audio, duration
	b.Item.Duration = duration
	item["id"], item["path"], item["libraryFolderId"] = b.Item.ID, b.Path, b.FolderID
	b.Doc["media"], b.Doc["item"] = media, item
	if b.New {
		// Entities a sidecar is about to replace are not created, so tag or
		// folder guesses never leave orphaned authors or series behind.
		sidecar := func(k string) bool { return b.Sidecar != nil && b.Sidecar.Metadata[k] != nil }
		if !sidecar("authors") {
			refs := []any{}
			for _, name := range authors {
				id := newID()
				var old string
				e := tx.QueryRowContext(ctx, `SELECT id FROM catalog_entities WHERE kind='authors' AND lower(json_extract(document,'$.name'))=lower(?) LIMIT 1`, name).Scan(&old)
				if e == nil {
					id = old
				} else if !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				author := sourceRow{"id": id, "name": name}
				refs = append(refs, author)
				if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO catalog_entities VALUES('authors',?,NULL,?)`, id, raw(author)); e != nil {
					return e
				}
			}
			b.Doc["authors"] = refs
		}
		if seriesName != "" && !sidecar("series") {
			id := newID()
			var old string
			e := tx.QueryRowContext(ctx, `SELECT id FROM catalog_entities WHERE kind='series' AND library_id=? AND lower(json_extract(document,'$.name'))=lower(?) LIMIT 1`, b.Item.LibraryID, seriesName).Scan(&old)
			if e == nil {
				id = old
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			series := sourceRow{"id": id, "name": seriesName}
			if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO catalog_entities VALUES('series',?,?,?)`, id, b.Item.LibraryID, raw(series)); e != nil {
				return e
			}
			ref := sourceRow{"id": id, "name": seriesName}
			if sequence != "" {
				ref["sequence"] = sequence
			}
			b.Doc["series"] = []any{ref}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_items VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.Item.ID, b.Item.LibraryID, b.Item.MediaID, "book", b.Item.Title, b.Item.Author, duration, false, false, b.Item.AddedAt, b.Item.UpdatedAt, raw(b.Item.Tags), canonicalDoc(b.Doc)); err != nil {
			return err
		}
		report.Added++
	} else if !bytes.Equal([]byte(canonicalDoc(b.Doc)), b.Item.Document) || b.Item.Missing {
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_items SET duration=?,missing=?,updated_at=MAX(updated_at+1,?),document=? WHERE id=?`, duration, b.Item.Missing, time.Now().UnixMilli(), canonicalDoc(b.Doc), b.Item.ID); err != nil {
			return err
		}
		report.Updated++
	}
	for _, f := range b.Files {
		_, err := tx.ExecContext(ctx, `INSERT INTO catalog_files VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(item_id,id) DO UPDATE SET root_id=excluded.root_id,relative_path=excluded.relative_path,name=excluded.name,mime=excluded.mime,size=excluded.size,duration=excluded.duration,track_index=excluded.track_index,document=excluded.document`, b.Item.ID, f.ID, f.Role, f.RootID, f.Relative, f.Name, f.MIME, f.Size, f.Duration, f.Index, f.Excluded, raw(f.Doc))
		if err != nil {
			return err
		}
		if f.Missing {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_file_observations VALUES(?,?,?,?,?) ON CONFLICT(item_id,file_id) DO UPDATE SET size=excluded.size,modified_ns=excluded.modified_ns,identity=excluded.identity`, b.Item.ID, f.ID, f.Size, f.Modified, f.Identity); err != nil {
			return err
		}
	}
	return nil
}

// canonicalDoc serializes a document the way it reads back from the
// database (object keys sorted), so typed values such as []Chapter do not make
// an unchanged book look modified on the next scan.
func canonicalDoc(doc sourceRow) string {
	return raw(objectJSON(raw(doc)))
}

// ebookRank orders formats the reader can open first; lower is better.
var ebookRank = map[string]int{".epub": 0, ".pdf": 1, ".azw3": 2, ".mobi": 3, ".cbz": 4, ".cbr": 5}

// pickEbook keeps the book's current ebook while it is still present, so a
// rescan never swaps an EPUB for a MOBI twin or a companion booklet; otherwise
// it prefers the most readable format, first file winning ties.
func pickEbook(files []sourceRow, current sourceRow) sourceRow {
	if current != nil {
		rel := str(object(current["metadata"])["relPath"])
		for _, f := range files {
			// Imported records may hold the path relative to the book folder.
			if r := str(object(f["metadata"])["relPath"]); rel != "" && (r == rel || strings.HasSuffix(r, "/"+rel)) {
				return f
			}
		}
	}
	best, bestRank := files[0], 99
	for _, f := range files {
		r, ok := ebookRank[strings.ToLower(filepath.Ext(str(object(f["metadata"])["filename"])))]
		if !ok {
			r = 50
		}
		if r < bestRank {
			best, bestRank = f, r
		}
	}
	return best
}
