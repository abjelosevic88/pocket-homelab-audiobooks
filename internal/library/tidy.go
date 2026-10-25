package library

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// TidyChange is one book's proposed title/series/subtitle repair.
type TidyChange struct {
	ID         string      `json:"id"`
	Expected   int64       `json:"expected_updated_at"`
	Author     string      `json:"author"`
	Path       string      `json:"path"`
	Changes    []TidyField `json:"changes"`
	Reasons    []string    `json:"reasons"`
	Confidence string      `json:"confidence"`
	edit       map[string]any
}

// TidyField is one field before and after.
type TidyField struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// TidyUnresolved is a book whose title looks broken but nothing in its
// folder, file names or tags says what it should be.
type TidyUnresolved struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type TidyReport struct {
	LibraryID  string           `json:"library_id"`
	Books      int              `json:"books"`
	Changes    []TidyChange     `json:"changes"`
	Unresolved []TidyUnresolved `json:"unresolved"`
	Reasons    map[string]int   `json:"reasons"`
}

// TidySelection is a change the person ticked in the preview.
type TidySelection struct {
	ID       string `json:"id"`
	Expected int64  `json:"expected_updated_at"`
}

var confidenceName = map[int]string{TidyLow: "low", TidyMedium: "medium", TidyHigh: "high"}

// TidyPreview proposes title, series and subtitle repairs for every book of a
// library the person may edit. Fields set by a person are never proposed.
func (c *Catalog) TidyPreview(ctx context.Context, uid int64, lib string) (TidyReport, error) {
	report := TidyReport{LibraryID: lib, Changes: []TidyChange{}, Unresolved: []TidyUnresolved{}, Reasons: map[string]int{}}
	books, err := c.tidyBooks(ctx, uid, lib)
	if err != nil {
		return report, err
	}
	report.Books = len(books)
	twins := map[string]int{}
	for _, b := range books {
		if len(b.in.Series) > 0 {
			twins[strings.ToLower(b.in.Series[0].Name)+"\x00"+strings.ToLower(b.in.Title)]++
		}
	}
	for _, b := range books {
		if len(b.in.Series) > 0 {
			b.in.SeriesTwins = twins[strings.ToLower(b.in.Series[0].Name)+"\x00"+strings.ToLower(b.in.Title)] > 1
		}
		p := proposeTidy(b.in)
		if p.Unresolved != "" && !p.SetTitle {
			report.Unresolved = append(report.Unresolved, TidyUnresolved{ID: b.it.ID, Title: b.it.Title, Path: b.in.Rel, Reason: p.Unresolved})
		}
		if p.empty() {
			continue
		}
		change := TidyChange{ID: b.it.ID, Expected: b.it.UpdatedAt, Author: b.it.Author, Path: b.in.Rel, Reasons: p.Reasons, Confidence: confidenceName[p.Confidence], Changes: []TidyField{}, edit: map[string]any{}}
		if p.SetTitle {
			change.Changes = append(change.Changes, TidyField{"title", b.in.Title, p.Title})
			change.edit["title"] = p.Title
		}
		if p.SetSub {
			change.Changes = append(change.Changes, TidyField{"subtitle", b.in.Subtitle, p.Subtitle})
			change.edit["subtitle"] = p.Subtitle
		}
		if p.Series != nil {
			from := ""
			if len(b.in.Series) > 0 {
				from = seriesLabel(b.in.Series[0])
			}
			change.Changes = append(change.Changes, TidyField{"series", from, seriesLabel(*p.Series)})
			list := []any{map[string]any{"name": p.Series.Name, "sequence": p.Series.Sequence}}
			for i, s := range b.in.Series {
				if i > 0 {
					list = append(list, map[string]any{"name": s.Name, "sequence": s.Sequence})
				}
			}
			change.edit["series"] = list
		}
		for _, r := range p.Reasons {
			report.Reasons[r]++
		}
		report.Changes = append(report.Changes, change)
	}
	// Two books of one series ending up with the same repaired title (parts
	// of one novel, "Vol 1 - [01]" and "Vol 1 - [02]") need a person's eye.
	final := map[string]int{}
	key := func(series, title string) string { return strings.ToLower(series) + "\x00" + strings.ToLower(title) }
	changed := map[string]TidyChange{}
	for _, ch := range report.Changes {
		changed[ch.ID] = ch
	}
	finalKey := map[string]string{}
	for _, b := range books {
		title, series := b.in.Title, ""
		if len(b.in.Series) > 0 {
			series = b.in.Series[0].Name
		}
		if ch, ok := changed[b.it.ID]; ok {
			if t, ok := ch.edit["title"].(string); ok {
				title = t
			}
		}
		finalKey[b.it.ID] = key(series, title)
		final[finalKey[b.it.ID]]++
	}
	for i, ch := range report.Changes {
		if _, ok := ch.edit["title"]; ok && slices.Contains(ch.Reasons, TidyPlaceholder) && final[finalKey[ch.ID]] > 1 {
			report.Changes[i].Confidence = confidenceName[TidyLow]
		}
	}
	sort.SliceStable(report.Changes, func(i, j int) bool {
		return strings.ToLower(report.Changes[i].Path) < strings.ToLower(report.Changes[j].Path)
	})
	return report, nil
}

func seriesLabel(s tidySeries) string {
	if s.Sequence == "" {
		return s.Name
	}
	return s.Name + " #" + s.Sequence
}

// TidyApply recomputes the proposals for the selected books and saves them
// in one transaction. Any book changed since the preview fails the whole
// apply with ErrConflict, like the bulk editor.
func (c *Catalog) TidyApply(ctx context.Context, uid int64, lib string, picked []TidySelection) (int, error) {
	if len(picked) == 0 || len(picked) > 5000 {
		return 0, ErrInvalid
	}
	report, err := c.TidyPreview(ctx, uid, lib)
	if err != nil {
		return 0, err
	}
	byID := map[string]TidyChange{}
	for _, ch := range report.Changes {
		byID[ch.ID] = ch
	}
	edits := []ItemEdit{}
	for _, p := range picked {
		ch, ok := byID[p.ID]
		if !ok || ch.Expected != p.Expected {
			// Changed, hand-edited or already tidy since the preview.
			return 0, fmt.Errorf("%w: %s changed since the preview", ErrConflict, p.ID)
		}
		edits = append(edits, ItemEdit{ID: ch.ID, EditOptions: EditOptions{Expected: ch.Expected, Metadata: ch.edit}})
	}
	if err = c.editMany(ctx, uid, edits, 5000, false); err != nil {
		return 0, err
	}
	return len(edits), nil
}

type tidyBook struct {
	it Item
	in tidyInput
}

// tidyBooks loads every editable book of a library with its folder, audio
// file names, album tag and hand-edited fields.
func (c *Catalog) tidyBooks(ctx context.Context, uid int64, lib string) ([]*tidyBook, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	if !a.Permissions.Update || !a.allowsLibrary(lib) {
		return nil, ErrNotFound
	}
	var mediaType string
	if err = c.DB.QueryRowContext(ctx, `SELECT media_type FROM catalog_libraries WHERE id=? AND NOT EXISTS(SELECT 1 FROM catalog_library_state s WHERE s.library_id=catalog_libraries.id AND s.archived=1)`, lib).Scan(&mediaType); err != nil {
		return nil, ErrNotFound
	}
	if mediaType != "book" {
		return nil, fmt.Errorf("%w: tidy-up works on book libraries", ErrInvalid)
	}
	folders := []string{}
	rows, err := c.DB.QueryContext(ctx, `SELECT json_extract(document,'$.path') FROM catalog_entities WHERE kind='libraryFolders' AND library_id=?`, lib)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p *string
		if err = rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		if p != nil && *p != "" {
			folders = append(folders, filepath.Clean(*p))
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows, err = c.DB.QueryContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE library_id=? AND media_type='book' AND NOT EXISTS(SELECT 1 FROM catalog_removed_items x WHERE x.item_id=catalog_items.id) ORDER BY id`, lib)
	if err != nil {
		return nil, err
	}
	books := []*tidyBook{}
	byID := map[string]*tidyBook{}
	for rows.Next() {
		it, e := scanItem(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		if !a.allows(it) {
			continue
		}
		var doc sourceRow
		if e = json.Unmarshal(it.Document, &doc); e != nil {
			continue
		}
		media := object(doc["media"])
		in := tidyInput{Title: it.Title, Subtitle: str(media["subtitle"]), Author: it.Author, Edited: map[string]bool{}}
		for _, s := range array(doc["series"]) {
			ref := object(s)
			if name := strings.TrimSpace(str(ref["name"])); name != "" {
				in.Series = append(in.Series, tidySeries{Name: name, Sequence: strings.TrimSpace(str(ref["sequence"]))})
			}
		}
		path := filepath.Clean(str(object(doc["item"])["path"]))
		best := ""
		for _, f := range folders {
			if under(path, f) && len(f) > len(best) {
				best = f
			}
		}
		if best != "" {
			if rel, e := filepath.Rel(best, path); e == nil && filepath.IsLocal(rel) {
				in.Rel = filepath.ToSlash(rel)
			}
		}
		b := &tidyBook{it: it, in: in}
		books = append(books, b)
		byID[it.ID] = b
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows, err = c.DB.QueryContext(ctx, `SELECT f.item_id,f.name,COALESCE(json_extract(f.document,'$.metaTags.tagAlbum'),json_extract(f.document,'$.metaTags.album'),'') FROM catalog_files f JOIN catalog_items i ON i.id=f.item_id WHERE i.library_id=? AND f.role='audio' AND f.excluded=0 ORDER BY f.item_id,f.track_index,f.name`, lib)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, name, album string
		if err = rows.Scan(&id, &name, &album); err != nil {
			rows.Close()
			return nil, err
		}
		if b := byID[id]; b != nil {
			b.in.Files = append(b.in.Files, name)
			if b.in.Album == "" {
				b.in.Album = album
			}
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows, err = c.DB.QueryContext(ctx, `SELECT e.item_id,e.field FROM catalog_item_edits e JOIN catalog_items i ON i.id=e.item_id WHERE i.library_id=?`, lib)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, field string
		if err = rows.Scan(&id, &field); err != nil {
			return nil, err
		}
		if b := byID[id]; b != nil {
			b.in.Edited[field] = true
		}
	}
	return books, rows.Err()
}
