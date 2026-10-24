package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxSidecar = 2 << 20

// JSON sidecars use the portable Audiobookshelf metadata format, including
// legacy nested metadata. Unsupported fields remain in the original file;
// malformed supported fields abort the scan instead of partially applying it.
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

func (c *Catalog) readSidecars(ctx context.Context, files []*scannedFile) (*EditOptions, error) {
	contents := map[string][]byte{}
	for _, f := range files {
		if f.Role != "supplement" || f.Missing {
			continue
		}
		name := strings.ToLower(f.Name)
		if name != "metadata.json" && name != "metadata.abs" && name != "desc.txt" && name != "reader.txt" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var path string
		if err := c.DB.QueryRowContext(ctx, `SELECT local_path FROM catalog_roots WHERE id=?`, f.RootID).Scan(&path); err != nil {
			return nil, err
		}
		root, err := os.OpenRoot(path)
		if err != nil {
			return nil, err
		}
		input, err := root.Open(f.Relative)
		if err != nil {
			root.Close()
			return nil, err
		}
		stat, err := input.Stat()
		if err == nil && (!stat.Mode().IsRegular() || stat.Size() > maxSidecar) {
			err = errors.New("metadata sidecar exceeds 2 MiB or is not a regular file")
		}
		var data []byte
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(input, maxSidecar+1))
			if len(data) > maxSidecar {
				err = ErrInvalid
			}
		}
		input.Close()
		root.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		key := strings.ToLower(f.Name)
		// Multiple nested sidecars are ambiguous; preserve them rather than pick one.
		if _, ok := contents[key]; ok {
			return nil, fmt.Errorf("multiple %s files in one book", key)
		}
		contents[key] = data
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
	return &out, nil
}
