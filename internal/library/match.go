package library

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
)

// MatchOptions control what quick match may change. By default (all false)
// it only fills fields that are empty, like Audiobookshelf's quick match.
type MatchOptions struct {
	OverrideDetails bool `json:"override_details"` // replace non-empty text and list details
	OverrideCover   bool `json:"override_cover"`   // replace an existing cover
	UpdateAuthors   bool `json:"update_authors"`   // replace non-empty authors
	UpdateSeries    bool `json:"update_series"`    // replace non-empty series
}

// MatchFields are the metadata keys quick match may write, in display order.
// Tags are left out: they can control library access for restricted accounts.
var MatchFields = []string{"title", "subtitle", "authors", "narrators", "series", "description", "publisher", "publishedYear", "language", "genres", "isbn", "asin"}

// CurrentMetadata returns the editable metadata of a book in the editor's
// shape and whether it has a cover.
func CurrentMetadata(it Item) (map[string]any, bool) {
	doc := objectJSON(string(it.Document))
	media := object(doc["media"])
	out := map[string]any{}
	for _, k := range []string{"subtitle", "description", "publisher", "publishedYear", "publishedDate", "isbn", "asin", "language"} {
		out[k] = str(media[k])
	}
	out["title"] = it.Title
	for _, k := range []string{"genres", "narrators"} {
		list := []any{}
		for _, v := range array(media[k]) {
			if s := str(v); s != "" {
				list = append(list, s)
			}
		}
		out[k] = list
	}
	tags := []any{}
	for _, t := range it.Tags {
		tags = append(tags, t)
	}
	out["tags"] = tags
	for _, k := range []string{"authors", "series"} {
		refs := []any{}
		for _, v := range array(doc[k]) {
			o := object(v)
			if name := str(o["name"]); name != "" {
				ref := map[string]any{"name": name}
				if k == "series" {
					ref["sequence"] = str(o["sequence"])
				}
				refs = append(refs, ref)
			}
		}
		out[k] = refs
	}
	out["explicit"], _ = media["explicit"].(bool)
	out["abridged"], _ = media["abridged"].(bool)
	return out, str(media["coverPath"]) != ""
}

func emptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	}
	return false
}

// limits mirror editItem so a provider value never makes the edit invalid.
func fitValue(k string, v any) (any, bool) {
	switch k {
	case "authors", "series":
		list, ok := v.([]any)
		if !ok {
			return nil, false
		}
		out := []any{}
		for _, e := range list {
			o, ok := e.(map[string]any)
			if !ok {
				continue
			}
			name, _ := o["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" || len(name) > 512 {
				continue
			}
			ref := map[string]any{"name": name}
			if k == "series" {
				seq, _ := o["sequence"].(string)
				if len(seq) > 64 {
					seq = ""
				}
				ref["sequence"] = seq
			}
			out = append(out, ref)
			if len(out) == 100 {
				break
			}
		}
		return out, len(out) > 0
	case "genres", "narrators", "tags":
		list, ok := v.([]any)
		if !ok {
			return nil, false
		}
		out := []any{}
		for _, e := range list {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" && len(s) <= 256 {
				out = append(out, strings.TrimSpace(s))
			}
			if len(out) == 100 {
				break
			}
		}
		return out, len(out) > 0
	default:
		s, ok := v.(string)
		s = strings.TrimSpace(s)
		if !ok || s == "" {
			return nil, false
		}
		limit := 2048
		if k == "description" {
			limit = 100000
		}
		if len(s) > limit {
			return nil, false
		}
		return s, true
	}
}

// NormalizeMetadata converts provider values (which may use []string) to the
// JSON shapes the editor accepts, dropping values the editor would reject.
func NormalizeMetadata(in map[string]any) map[string]any {
	b, _ := json.Marshal(in)
	var generic map[string]any
	_ = json.Unmarshal(b, &generic)
	out := map[string]any{}
	for _, k := range MatchFields {
		if v, ok := fitValue(k, generic[k]); ok {
			out[k] = v
		}
	}
	return out
}

func sameValue(a, b any) bool {
	if sa, ok := a.(string); ok {
		sb, _ := b.(string)
		return strings.TrimSpace(sa) == strings.TrimSpace(sb)
	}
	return reflect.DeepEqual(a, b)
}

// QuickMatchFields chooses which proposed fields quick match writes: empty
// fields are filled; non-empty ones only change when the options allow it.
// It returns the edit and the keys that actually change.
func QuickMatchFields(current, proposed map[string]any, o MatchOptions) (map[string]any, []string) {
	proposed = NormalizeMetadata(proposed)
	edit := map[string]any{}
	keys := []string{}
	for _, k := range MatchFields {
		v, ok := proposed[k]
		if !ok || sameValue(current[k], v) {
			continue
		}
		allowed := emptyValue(current[k])
		switch k {
		case "authors":
			allowed = allowed || o.UpdateAuthors
		case "series":
			allowed = allowed || o.UpdateSeries
		default:
			allowed = allowed || o.OverrideDetails
		}
		if allowed {
			edit[k] = v
			keys = append(keys, k)
		}
	}
	return edit, keys
}

// MatchTargets lists the books a bulk quick match will visit: the given IDs,
// or every accessible book of a library, in title order.
func (c *Catalog) MatchTargets(ctx context.Context, uid int64, libraryID string, ids []string) ([]string, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	if !a.Permissions.Update {
		return nil, ErrNotFound
	}
	out := []string{}
	if len(ids) > 0 {
		if len(ids) > 1000 {
			return nil, ErrInvalid
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			it, err := c.Item(ctx, uid, id)
			if err != nil {
				return nil, err
			}
			if it.MediaType != "book" {
				return nil, ErrInvalid
			}
			out = append(out, id)
		}
		return out, nil
	}
	if libraryID == "" {
		return nil, ErrInvalid
	}
	var exists int
	if err := c.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_libraries WHERE id=?`, libraryID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	for offset := 0; ; offset += 200 {
		page, err := c.Items(ctx, uid, Query{LibraryID: libraryID, MediaType: "book", Offset: offset, Limit: 200})
		if err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			out = append(out, it.ID)
		}
		if len(page.Items) < 200 || len(out) >= 20000 {
			break
		}
	}
	return out, nil
}
