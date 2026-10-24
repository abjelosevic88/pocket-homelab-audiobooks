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
	Force          bool
}
type ScanReport struct {
	Invalid   int    `json:"invalid"`
	Error     string `json:"error,omitempty"`
	ID        string `json:"id"`
	LibraryID string `json:"library_id"`
	Added     int    `json:"added"`
	Updated   int    `json:"updated"`
	Missing   int    `json:"missing"`
	Files     int    `json:"files"`
	Probed    int    `json:"probed"`
	Unchanged int    `json:"unchanged"`
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
	New            bool
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
	if err = c.CanScan(ctx, uid, lib); err != nil {
		return
	}
	var libraryRevision int64
	if err = c.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM catalog_library_state WHERE library_id=?),0)`, lib).Scan(&libraryRevision); err != nil {
		return report, err
	}
	folders, err := c.scanFolders(ctx, lib)
	if err != nil {
		return report, err
	}
	_, err = c.DB.ExecContext(ctx, `INSERT INTO catalog_scan_runs VALUES(?,?,?,NULL,'running','{}')`, report.ID, lib, time.Now().UnixMilli())
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
	seen := map[string]bool{}
	newGroups := map[string]*scanBook{}
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
			return fs.WalkDir(root.FS(), filepath.ToSlash(folder.Relative), func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if e := ctx.Err(); e != nil {
					return e
				}
				if entry.Type()&fs.ModeSymlink != 0 {
					return nil
				}
				if entry.IsDir() {
					if path != filepath.ToSlash(folder.Relative) && strings.HasPrefix(entry.Name(), ".") {
						return fs.SkipDir
					}
					return nil
				}
				role := scanRole(entry.Name())
				if role == "" {
					return nil
				}
				path = filepath.FromSlash(path)
				key := folder.Root.id + "/" + path
				sourcePath := filepath.Join(folder.Root.source, path)
				longest, owner := -1, ""
				for id, book := range books {
					if book.Path != "" && under(sourcePath, book.Path) && len(book.Path) > longest {
						longest, owner = len(book.Path), id
					}
				}
				if removed[owner] {
					return nil
				}
				if old := oldByPath[key]; old != nil && removed[old.OldItem] {
					return nil
				}

				if seen[key] {
					return nil
				}
				seen[key] = true
				info, e := entry.Info()
				if e != nil {
					return e
				}
				if !info.Mode().IsRegular() {
					return nil
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
					return nil
				}
				f := &scannedFile{ID: scanStableID(folder.Root.id, path), RootID: folder.Root.id, Relative: path, Source: filepath.Join(folder.Root.source, path), Name: entry.Name(), Role: role, MIME: mimeFor(path), Size: info.Size(), Modified: info.ModTime().UnixNano(), Identity: fileIdentity(info), Doc: sourceRow{}}
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
				if role == "audio" && unchanged && old != nil && f.Duration <= 0 && !opt.Force {
					// Imported zero-duration audio was already unusable at the
					// source. Preserve it and report it without blocking healthy
					// books or pretending the file was repaired by migration.
					report.Invalid++
					report.Unchanged++
				} else if role == "audio" && (!unchanged || opt.Force || f.Duration <= 0) {
					input, e := root.Open(path)
					if e != nil {
						return e
					}
					probe, e := Probe(ctx, opt.FFprobe, input)
					input.Close()
					report.Probed++
					if e != nil {
						if old == nil || old.Duration > 0 {
							return fmt.Errorf("audio metadata could not be read for %s: %w", path, e)
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
				var book *scanBook
				if f.OldItem != "" {
					book = books[f.OldItem]
				}
				if book == nil {
					for _, b := range books {
						if b.Path != "" && under(f.Source, b.Path) && (book == nil || len(b.Path) > len(book.Path)) {
							book = b
						}
					}
				}
				if book == nil {
					group := filepath.Dir(f.Source)
					if group == folder.Source {
						if role == "cover" || role == "supplement" {
							return nil
						}
						group = strings.TrimSuffix(f.Source, filepath.Ext(f.Source))
					}
					book = newGroups[group]
					if book == nil {
						now := time.Now().UnixMilli()
						book = &scanBook{Item: Item{ID: newID(), LibraryID: lib, MediaID: newID(), MediaType: "book", AddedAt: now, UpdatedAt: now, Tags: []string{}}, Doc: sourceRow{"item": sourceRow{}, "media": sourceRow{}, "authors": []any{}, "series": []any{}, "episodes": []any{}}, Path: group, FolderID: folder.ID, New: true}
						newGroups[group] = book
						books[book.Item.ID] = book
					}
				}
				book.Files = append(book.Files, f)
				report.Files++
				return nil
			})
		}()
		if e != nil {
			return report, e
		}
	}
	// Sidecars are bounded and read before taking the database write lock.
	for _, book := range books {
		if book.New || opt.ImportMetadata {
			book.Sidecar, err = c.readSidecars(ctx, book.Files)
			if err != nil {
				return report, err
			}
		}
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
	for _, book := range books {
		if removed[book.Item.ID] {
			continue
		}
		if err = ctx.Err(); err != nil {
			return report, err
		}
		if !book.New {
			var version int64
			if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM catalog_items WHERE id=?`, book.Item.ID).Scan(&version); err != nil {
				return report, err
			}
			if version != book.Item.UpdatedAt {
				return report, ErrConflict
			}
		}
		if book.New {
			audioOrEbook := false
			for _, f := range book.Files {
				audioOrEbook = audioOrEbook || f.Role == "audio" || f.Role == "ebook"
			}
			if !audioOrEbook {
				continue
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
				if _, err = tx.ExecContext(ctx, `UPDATE catalog_items SET missing=1,updated_at=MAX(updated_at+1,?) WHERE id=?`, time.Now().UnixMilli(), book.Item.ID); err != nil {
					return report, err
				}
				report.Missing++
			}
			continue
		}
		// Preserve missing tracks and their IDs so a partial disappearance cannot
		// silently shorten a book or shift every subsequent progress position.
		ids := map[string]bool{}
		book.Item.Missing = false
		moved := ""
		consistent := true
		for _, f := range book.Files {
			ids[f.ID] = true
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
		sort.SliceStable(book.Files, func(i, j int) bool {
			a, b := book.Files[i], book.Files[j]
			ai, bi := a.Index, b.Index
			if ai == 0 {
				ai = 1 << 30
			}
			if bi == 0 {
				bi = 1 << 30
			}
			if ai != bi {
				return ai < bi
			}
			return naturalLess(a.Name, b.Name)
		})
		if err = c.commitScannedBook(ctx, tx, book, &report); err != nil {
			return report, err
		}
		if book.Sidecar != nil {
			current, e := scanItem(tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE id=?`, book.Item.ID))
			if e != nil {
				return report, e
			}
			book.Sidecar.Expected = current.UpdatedAt
			if e = editItem(ctx, tx, account, current, *book.Sidecar); e != nil {
				return report, fmt.Errorf("sidecar for %s: %w", book.Path, e)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
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
	if b.New {
		b.Item.Title = filepath.Base(b.Path)
		for _, f := range b.Files {
			if f.Probe != nil {
				t := f.Probe.Tags
				if t["album"] != "" {
					b.Item.Title = t["album"]
				} else if len(b.Files) == 1 && t["title"] != "" {
					b.Item.Title = t["title"]
				}
				b.Item.Author = t["album_artist"]
				if b.Item.Author == "" {
					b.Item.Author = t["artist"]
				}
				media["description"] = t["description"]
				media["publishedYear"] = t["date"]
				if t["genre"] != "" {
					media["genres"] = []string{t["genre"]}
				}
				if b.Item.Author != "" {
					break
				}
			}
		}
		if suppliedTitle != "" {
			b.Item.Title = suppliedTitle
		}
		if suppliedAuthor != "" {
			b.Item.Author = suppliedAuthor
		}
		media["title"] = b.Item.Title
	}
	audio := []any{}
	chapters := []Chapter{}
	duration := 0.0
	audioIndex := 0
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
			media["ebookFile"] = f.Doc
		}
		if f.Role == "cover" && (b.New || str(media["coverPath"]) == "") {
			media["coverPath"] = f.Source
		}
	}
	// Imported/editable chapters and descriptive metadata win over auto-extraction.
	if b.New || len(array(media["chapters"])) == 0 {
		media["chapters"] = chapters
	}
	media["audioFiles"], media["duration"] = audio, duration
	b.Item.Duration = duration
	item["id"], item["path"], item["libraryFolderId"] = b.Item.ID, b.Path, b.FolderID
	b.Doc["media"], b.Doc["item"] = media, item
	if b.New {
		if b.Item.Author != "" {
			id := newID()
			var old string
			e := tx.QueryRowContext(ctx, `SELECT id FROM catalog_entities WHERE kind='authors' AND lower(json_extract(document,'$.name'))=lower(?) LIMIT 1`, b.Item.Author).Scan(&old)
			if e == nil {
				id = old
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			author := sourceRow{"id": id, "name": b.Item.Author}
			b.Doc["authors"] = []any{author}
			if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO catalog_entities VALUES('authors',?,NULL,?)`, id, raw(author)); e != nil {
				return e
			}
		}
		if name := str(media["seriesName"]); name != "" {
			id := newID()
			var old string
			e := tx.QueryRowContext(ctx, `SELECT id FROM catalog_entities WHERE kind='series' AND library_id=? AND lower(json_extract(document,'$.name'))=lower(?) LIMIT 1`, b.Item.LibraryID, name).Scan(&old)
			if e == nil {
				id = old
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			series := sourceRow{"id": id, "name": name}
			b.Doc["series"] = []any{series}
			if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO catalog_entities VALUES('series',?,?,?)`, id, b.Item.LibraryID, raw(series)); e != nil {
				return e
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_items VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.Item.ID, b.Item.LibraryID, b.Item.MediaID, "book", b.Item.Title, b.Item.Author, duration, false, false, b.Item.AddedAt, b.Item.UpdatedAt, raw(b.Item.Tags), raw(b.Doc)); err != nil {
			return err
		}
		report.Added++
	} else if !bytes.Equal([]byte(raw(b.Doc)), b.Item.Document) || b.Item.Missing {
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_items SET duration=?,missing=?,updated_at=MAX(updated_at+1,?),document=? WHERE id=?`, duration, b.Item.Missing, time.Now().UnixMilli(), raw(b.Doc), b.Item.ID); err != nil {
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
