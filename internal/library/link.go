package library

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// What other Pocket apps may ask of the catalog through a Pocket Core link:
// "do you have this book?" (FindBooks) and "which library holds this folder?"
// (LibraryForPath, so a new book can be scanned in). Both answer only for the
// person the link acts for.

// FoundBook is an audiobook as a linked app sees it.
type FoundBook struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle"`
	Authors     []string `json:"authors"`
	Narrators   []string `json:"narrators"`
	Series      string   `json:"series"`
	SeriesIndex string   `json:"series_index"`
	LibraryID   string   `json:"library_id"`
	Cover       string   `json:"cover"`
	URLPath     string   `json:"url_path"`
	AddedAt     int64    `json:"added_at"`
}

// FindQuery is a free-text search, or a title with its author.
type FindQuery struct {
	Q, Title, Author string
	Limit            int
}

var foldRunes = map[rune]string{'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'æ': "ae", 'ç': "c", 'č': "c", 'ć': "c", 'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ñ': "n", 'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ý': "y", 'ÿ': "y", 'š': "s", 'ž': "z", 'đ': "dj", 'ß': "ss", 'ł': "l", 'œ': "oe"}

// foldText lower-cases, folds accents and turns punctuation into spaces, so
// "Zafón" finds "Zafon" and "Hail Mary: A Novel" contains "hail mary".
func foldText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '\'' || r == '’':
		case r == '&':
			b.WriteString(" and ")
		case foldRunes[r] != "":
			b.WriteString(foldRunes[r])
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// containsWords reports whether a holds b as whole words ("" never matches).
func containsWords(a, b string) bool {
	return b != "" && strings.Contains(" "+a+" ", " "+b+" ")
}

// startsWords reports whether a begins with the words of b, a leading
// article aside ("" never matches).
func startsWords(a, b string) bool {
	a, b = dropArticle(a), dropArticle(b)
	return b != "" && strings.HasPrefix(a+" ", b+" ")
}

func dropArticle(s string) string {
	for _, art := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, art) {
			return s[len(art):]
		}
	}
	return s
}

// foundFrom reads a book's names from its stored document.
func foundFrom(it Item) FoundBook {
	f := FoundBook{ID: it.ID, Title: it.Title, LibraryID: it.LibraryID, Cover: "/api/v1/cover/" + it.ID, URLPath: "/book/" + it.ID, AddedAt: it.AddedAt,
		Authors: []string{}, Narrators: []string{}}
	var doc struct {
		Authors []struct {
			Name string `json:"name"`
		} `json:"authors"`
		Series []struct {
			Name     string `json:"name"`
			Sequence any    `json:"sequence"`
		} `json:"series"`
		Media struct {
			Title     string   `json:"title"`
			Subtitle  *string  `json:"subtitle"`
			Narrators []string `json:"narrators"`
		} `json:"media"`
	}
	_ = json.Unmarshal(it.Document, &doc)
	if doc.Media.Title != "" {
		f.Title = doc.Media.Title
	}
	if doc.Media.Subtitle != nil {
		f.Subtitle = *doc.Media.Subtitle
	}
	for _, a := range doc.Authors {
		if a.Name != "" {
			f.Authors = append(f.Authors, a.Name)
		}
	}
	if len(f.Authors) == 0 && it.Author != "" {
		for _, a := range strings.Split(it.Author, ",") {
			if a = strings.TrimSpace(a); a != "" {
				f.Authors = append(f.Authors, a)
			}
		}
	}
	for _, n := range doc.Media.Narrators {
		if n != "" {
			f.Narrators = append(f.Narrators, n)
		}
	}
	if len(doc.Series) > 0 {
		f.Series = doc.Series[0].Name
		switch v := doc.Series[0].Sequence.(type) {
		case string:
			f.SeriesIndex = v
		case float64:
			f.SeriesIndex = strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return f
}

// matches says whether a book is the one asked for: with a title and an
// author, one folded title must begin with the other (either way, so a
// subtitle or "(Unabridged)" doesn't hide it, but "Mary" is not "Project
// Hail Mary") and one author must match;
// otherwise every word of q must be in its title, subtitle, authors or series.
func (q FindQuery) matches(f FoundBook) bool {
	title := foldText(f.Title)
	full := foldText(f.Title + " " + f.Subtitle)
	authors := foldText(strings.Join(f.Authors, " "))
	if wt := foldText(q.Title); wt != "" {
		if !startsWords(full, wt) && !startsWords(wt, title) {
			return false
		}
		wa := foldText(q.Author)
		if wa == "" {
			return true
		}
		for _, a := range f.Authors {
			fa := foldText(a)
			if containsWords(fa, wa) || containsWords(wa, fa) {
				return true
			}
		}
		// "Weir Andy" or a different spelling of the first name: the surname alone
		parts := strings.Fields(wa)
		return len(parts) > 1 && len(parts[len(parts)-1]) > 2 && containsWords(authors, parts[len(parts)-1])
	}
	words := strings.Fields(foldText(q.Q))
	if len(words) == 0 {
		return false
	}
	hay := " " + full + " " + authors + " " + foldText(f.Series) + " "
	for _, w := range words {
		if !strings.Contains(hay, " "+w+" ") {
			return false
		}
	}
	return true
}

// FindBooks searches the audiobooks the person may open (newest first, at
// most 25).
func (c *Catalog) FindBooks(ctx context.Context, userID int64, q FindQuery) ([]FoundBook, error) {
	out := []FoundBook{}
	a, err := c.Account(ctx, userID)
	if err != nil {
		return out, err
	}
	if q.Limit <= 0 || q.Limit > 25 {
		q.Limit = 25
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE media_type='book'
	  AND NOT EXISTS(SELECT 1 FROM catalog_library_state s WHERE s.library_id=catalog_items.library_id AND s.archived=1)
	  AND NOT EXISTS(SELECT 1 FROM catalog_removed_items x WHERE x.item_id=catalog_items.id)
	  AND EXISTS(SELECT 1 FROM catalog_libraries l WHERE l.id=catalog_items.library_id AND l.media_type='book')
	  ORDER BY added_at DESC, id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() && len(out) < q.Limit {
		it, err := scanItem(rows)
		if err != nil {
			return out, err
		}
		if it.Missing || !a.allows(it) {
			continue
		}
		if f := foundFrom(it); q.matches(f) {
			out = append(out, f)
		}
	}
	return out, rows.Err()
}

// LibraryForPath finds the book library one of whose folders holds path (an
// absolute path as this server sees it, or as the library's folder is
// written). The person must be able to open that library.
func (c *Catalog) LibraryForPath(ctx context.Context, userID int64, path string) (string, error) {
	if !filepath.IsAbs(path) || strings.Contains(path, "..") {
		return "", ErrInvalid
	}
	path = filepath.Clean(path)
	a, err := c.Account(ctx, userID)
	if err != nil {
		return "", err
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT l.id FROM catalog_libraries l WHERE l.media_type='book'
	  AND NOT EXISTS(SELECT 1 FROM catalog_library_state s WHERE s.library_id=l.id AND s.archived=1) ORDER BY l.display_order, l.id`)
	if err != nil {
		return "", err
	}
	var libs []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			libs = append(libs, id)
		}
	}
	rows.Close()
	best, bestLen := "", -1
	for _, lib := range libs {
		folders, err := c.scanFolders(ctx, lib)
		if err != nil {
			continue
		}
		for _, f := range folders {
			for _, dir := range []string{f.Source, filepath.Join(f.Root.local, f.Relative)} {
				rel, err := filepath.Rel(filepath.Clean(dir), path)
				if err == nil && (rel == "." || filepath.IsLocal(rel)) && len(dir) > bestLen {
					best, bestLen = lib, len(dir)
				}
			}
		}
	}
	if best == "" || !a.allowsLibrary(best) {
		return "", ErrNotFound
	}
	return best, nil
}
