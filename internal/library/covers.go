package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Cover sources, tried in this order for a book without a cover.
const (
	CoverEmbedded = "embedded" // picture attached to an audio file
	CoverFolder   = "folder"   // a loose JPEG/PNG in the book's folder
	CoverAudible  = "audible"  // Audible/Audnexus artwork for the book's ASIN
)

// CoverCandidate is a book without a cover and where one could come from.
// Source is empty when nothing local was found; ASIN is set when the server
// may still look the cover up on Audible.
type CoverCandidate struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Path     string `json:"path"`
	Expected int64  `json:"expected_updated_at"`
	Source   string `json:"source"`
	Detail   string `json:"detail"`
	ASIN     string `json:"asin,omitempty"`
}

// MissingCovers lists the editable books of a library that have no cover,
// each with the best local source: an embedded picture (probed with ffprobe
// when the import did not record one), then a loose image in the folder.
func (c *Catalog) MissingCovers(ctx context.Context, uid int64, lib, ffprobe string) ([]CoverCandidate, error) {
	books, err := c.tidyBooks(ctx, uid, lib)
	if err != nil {
		return nil, err
	}
	out := []CoverCandidate{}
	for _, b := range books {
		var doc sourceRow
		if json.Unmarshal(b.it.Document, &doc) != nil {
			continue
		}
		media := object(doc["media"])
		if str(media["coverPath"]) != "" {
			continue
		}
		var covers int
		if err = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_files WHERE item_id=? AND role='cover'`, b.it.ID).Scan(&covers); err != nil {
			return nil, err
		}
		if covers > 0 {
			continue
		}
		cand := CoverCandidate{ID: b.it.ID, Title: b.it.Title, Author: b.it.Author, Path: b.in.Rel, Expected: b.it.UpdatedAt, ASIN: strings.TrimSpace(str(media["asin"]))}
		if f, ok := c.embeddedCoverFile(ctx, b.it.ID, ffprobe); ok {
			cand.Source, cand.Detail = CoverEmbedded, f.name
		} else if name, ok := c.folderCoverImage(ctx, str(object(doc["item"])["path"])); ok {
			cand.Source, cand.Detail = CoverFolder, name
		}
		out = append(out, cand)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path) })
	return out, nil
}

type coverAudio struct{ root, rel, name string }

// embeddedCoverFile finds the first audio file with an attached picture.
// Imported books say so in embeddedCoverArt; scanned ones are probed.
func (c *Catalog) embeddedCoverFile(ctx context.Context, id, ffprobe string) (coverAudio, bool) {
	rows, err := c.DB.QueryContext(ctx, `SELECT root_id,relative_path,name,COALESCE(json_extract(document,'$.embeddedCoverArt'),'') FROM catalog_files WHERE item_id=? AND role='audio' AND excluded=0 ORDER BY track_index,name LIMIT 3`, id)
	if err != nil {
		return coverAudio{}, false
	}
	files := []coverAudio{}
	for rows.Next() {
		var f coverAudio
		var art string
		if rows.Scan(&f.root, &f.rel, &f.name, &art) != nil {
			continue
		}
		if art != "" {
			rows.Close()
			return f, true
		}
		files = append(files, f)
	}
	rows.Close()
	if ffprobe == "" {
		return coverAudio{}, false
	}
	for _, f := range files {
		file, err := c.openCatalogFile(ctx, f.root, f.rel)
		if err != nil {
			continue
		}
		probe, err := Probe(ctx, ffprobe, file)
		file.Close()
		if err == nil && probe.Cover {
			return f, true
		}
	}
	return coverAudio{}, false
}

// folderCoverImage picks a JPEG/PNG in the book folder or one level below
// ("CD 01/"): cover/folder/front by name first, otherwise the largest.
func (c *Catalog) folderCoverImage(ctx context.Context, bookPath string) (string, bool) {
	root, rel, ok := c.bookFolder(ctx, bookPath)
	if !ok {
		return "", false
	}
	defer root.Close()
	type image struct {
		rel  string
		size int64
		rank int
	}
	found := []image{}
	look := func(dir string) []fs.DirEntry {
		f, err := root.Open(dir)
		if err != nil {
			return nil
		}
		defer f.Close()
		entries, _ := f.ReadDir(500)
		return entries
	}
	for _, e := range look(rel) {
		dirs := []string{}
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(rel, e.Name()))
		} else if img, ok := coverImage(filepath.Join(rel, e.Name()), e); ok {
			found = append(found, image{img, size(e), coverRank(e.Name())})
		}
		for _, d := range dirs {
			for _, sub := range look(d) {
				if img, ok := coverImage(filepath.Join(d, sub.Name()), sub); ok {
					found = append(found, image{img, size(sub), coverRank(sub.Name()) + 1})
				}
			}
		}
	}
	if len(found) == 0 {
		return "", false
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].rank != found[j].rank {
			return found[i].rank < found[j].rank
		}
		return found[i].size > found[j].size
	})
	best, _ := filepath.Rel(rel, found[0].rel)
	return filepath.ToSlash(best), true
}

func size(e fs.DirEntry) int64 {
	if info, err := e.Info(); err == nil {
		return info.Size()
	}
	return 0
}

func coverImage(path string, e fs.DirEntry) (string, bool) {
	if !e.Type().IsRegular() {
		return "", false
	}
	switch strings.ToLower(filepath.Ext(e.Name())) {
	case ".jpg", ".jpeg", ".png":
		return path, true
	}
	return "", false
}

func coverRank(name string) int {
	n := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	for _, w := range []string{"cover", "folder", "front"} {
		if strings.Contains(n, w) {
			return 0
		}
	}
	return 2
}

// bookFolder opens the catalog root holding a book folder.
func (c *Catalog) bookFolder(ctx context.Context, bookPath string) (*os.Root, string, bool) {
	if bookPath == "" || !filepath.IsAbs(bookPath) {
		return nil, "", false
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT id,source_path,local_path FROM catalog_roots ORDER BY length(source_path) DESC`)
	if err != nil {
		return nil, "", false
	}
	roots := []importRoot{}
	for rows.Next() {
		var r importRoot
		if rows.Scan(&r.id, &r.source, &r.local) == nil {
			roots = append(roots, r)
		}
	}
	rows.Close()
	r, rel, err := mapPath(filepath.Clean(bookPath), roots)
	if err != nil {
		return nil, "", false
	}
	root, err := os.OpenRoot(r.local)
	if err != nil {
		return nil, "", false
	}
	return root, rel, true
}

// LocalCover reads the cover a MissingCovers source points at, for a book
// that still has none. ffmpeg extracts embedded pictures.
func (c *Catalog) LocalCover(ctx context.Context, uid int64, id, source, ffprobe, ffmpeg string) ([]byte, error) {
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		return nil, err
	}
	var doc sourceRow
	if err = json.Unmarshal(it.Document, &doc); err != nil {
		return nil, err
	}
	if str(object(doc["media"])["coverPath"]) != "" {
		return nil, fmt.Errorf("%w: the book has a cover now", ErrConflict)
	}
	switch source {
	case CoverEmbedded:
		f, ok := c.embeddedCoverFile(ctx, id, ffprobe)
		if !ok || ffmpeg == "" {
			return nil, fmt.Errorf("%w: no embedded picture", ErrNotFound)
		}
		file, err := c.openCatalogFile(ctx, f.root, f.rel)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		data, _, err := ExtractCover(ctx, ffmpeg, file)
		return data, err
	case CoverFolder:
		bookPath := str(object(doc["item"])["path"])
		name, ok := c.folderCoverImage(ctx, bookPath)
		if !ok {
			return nil, fmt.Errorf("%w: no image in the folder", ErrNotFound)
		}
		root, rel, ok := c.bookFolder(ctx, bookPath)
		if !ok {
			return nil, ErrNotFound
		}
		defer root.Close()
		f, err := root.Open(filepath.Join(rel, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 16<<20+1))
		if err == nil && len(data) > 16<<20 {
			err = errors.New("image is larger than 16 MB")
		}
		return data, err
	}
	return nil, ErrInvalid
}
