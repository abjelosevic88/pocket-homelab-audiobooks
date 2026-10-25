package library

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxSidecar = 2 << 20

// JSON sidecars use the portable Audiobookshelf metadata format, including
// legacy nested metadata. Unsupported fields remain in the original file;
// malformed supported fields quarantine the book instead of partially applying it.
func parseSidecar(data []byte) (EditOptions, error) {
	in := sourceRow{}
	if err := json.Unmarshal(data, &in); err != nil {
		return EditOptions{}, fmt.Errorf("invalid metadata.json: %w", err)
	}
	if in == nil {
		return EditOptions{}, ErrInvalid
	}
	for k, v := range object(in["metadata"]) {
		if _, ok := in[k]; !ok {
			in[k] = v
		}
	}
	// A blank title cannot name a book; keep the title derived from tags or the folder.
	if title, ok := in["title"].(string); ok && strings.TrimSpace(title) == "" {
		delete(in, "title")
	}
	out := EditOptions{Metadata: map[string]any{}}
	for _, k := range []string{"title", "subtitle", "description", "publisher", "publishedYear", "publishedDate", "isbn", "asin", "language", "explicit", "abridged", "genres", "narrators", "tags"} {
		if v, ok := in[k]; ok && v != nil {
			out.Metadata[k] = v
		}
	}
	for _, k := range []string{"authors", "series"} {
		if v, ok := in[k]; ok && v != nil {
			entries, ok := v.([]any)
			if !ok {
				return out, ErrInvalid
			}
			list := []any{}
			for _, entry := range entries {
				switch v := entry.(type) {
				case string:
					name, seq := v, ""
					if k == "series" {
						if prefix, suffix, found := strings.Cut(v, " #"); found {
							name, seq = prefix, suffix
						}
					}
					list = append(list, map[string]any{"name": name, "sequence": seq})
				case map[string]any:
					list = append(list, v)
				default:
					return out, ErrInvalid
				}
			}
			out.Metadata[k] = list
		}
	}
	if v, ok := in["chapters"]; ok {
		chapters := []Chapter{}
		if v != nil {
			b, _ := json.Marshal(v)
			if err := json.Unmarshal(b, &chapters); err != nil {
				return out, ErrInvalid
			}
		}
		out.Chapters = &chapters
	}
	return out, nil
}

// sidecarKind maps a supplement file name to the sidecar format it carries.
func sidecarKind(name string) string {
	name = strings.ToLower(name)
	switch name {
	case "metadata.json", "metadata.abs", "desc.txt", "reader.txt":
		return name
	}
	if strings.HasSuffix(name, ".opf") {
		return "opf"
	}
	return ""
}

// readSidecars reads the metadata files of one book. Precedence, lowest first:
// desc.txt/reader.txt, OPF, metadata.abs, metadata.json. When a book contains
// the same sidecar more than once (for example in a disc folder), the one at
// the book's root wins, then the shallowest one.
func (c *Catalog) readSidecars(ctx context.Context, bookPath string, files []*scannedFile) (*EditOptions, error) {
	chosen := map[string]*scannedFile{}
	depth := func(f *scannedFile) int {
		if filepath.Dir(f.Source) == bookPath {
			return 0
		}
		return 1 + strings.Count(f.Source, string(filepath.Separator))
	}
	for _, f := range files {
		if f.Role != "supplement" || f.Missing {
			continue
		}
		kind := sidecarKind(f.Name)
		if kind == "" {
			continue
		}
		if prev := chosen[kind]; prev == nil || depth(f) < depth(prev) || (depth(f) == depth(prev) && f.Source < prev.Source) {
			chosen[kind] = f
		}
	}
	contents := map[string][]byte{}
	for kind, f := range chosen {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := c.readCatalogFile(ctx, f.RootID, f.Relative, maxSidecar)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		contents[kind] = data
	}
	if len(contents) == 0 {
		return nil, nil
	}
	out := EditOptions{Metadata: map[string]any{}}
	if b, ok := contents["desc.txt"]; ok {
		out.Metadata["description"] = string(b)
	}
	if b, ok := contents["reader.txt"]; ok {
		out.Metadata["narrators"] = []any{strings.TrimSpace(string(b))}
	}
	if b, ok := contents["opf"]; ok {
		v, err := parseOPF(b)
		if err != nil {
			return nil, err
		}
		for k, val := range v.Metadata {
			out.Metadata[k] = val
		}
	}
	if b, ok := contents["metadata.abs"]; ok {
		v, err := parseABSidecar(b)
		if err != nil {
			return nil, err
		}
		for k, val := range v.Metadata {
			out.Metadata[k] = val
		}
		out.Chapters = v.Chapters
	}
	if b, ok := contents["metadata.json"]; ok {
		v, err := parseSidecar(b)
		if err != nil {
			return nil, err
		}
		for k, val := range v.Metadata {
			out.Metadata[k] = val
		}
		if v.Chapters != nil {
			out.Chapters = v.Chapters
		}
	}
	if len(out.Metadata) == 0 && out.Chapters == nil {
		return nil, nil
	}
	return &out, nil
}

// readCatalogFile reads a bounded regular file below a catalog root without
// following paths out of it.
func (c *Catalog) readCatalogFile(ctx context.Context, rootID, relative string, limit int64) ([]byte, error) {
	input, err := c.openCatalogFile(ctx, rootID, relative)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	stat, err := input.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > limit {
		return nil, fmt.Errorf("%w: sidecar exceeds %d bytes or is not a regular file", ErrInvalid, limit)
	}
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrInvalid
	}
	return data, nil
}

func (c *Catalog) openCatalogFile(ctx context.Context, rootID, relative string) (*os.File, error) {
	var path string
	if err := c.DB.QueryRowContext(ctx, `SELECT local_path FROM catalog_roots WHERE id=?`, rootID).Scan(&path); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(relative)
}
