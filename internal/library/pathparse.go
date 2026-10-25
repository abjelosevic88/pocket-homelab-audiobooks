package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// pathMetadata is what a book's folder layout says about it. It is the lowest
// precedence source: embedded tags and sidecars override every field.
type pathMetadata struct {
	Title, Author, Series, Sequence, Year, ASIN string
	Narrators                                   []string
}

var (
	pathASIN     = regexp.MustCompile(`(?:^|\s)\[?(B0[0-9A-Z]{8})\]?(?:\s|$)`)
	pathNarrator = regexp.MustCompile(`^(.*) \{(.*)\}$`)
	pathSequence = regexp.MustCompile(`(?i)^(vol\.? |volume |book |#)?(\d{1,3}(?:\.\d{1,2})?)(\.?)(?: (.*))?$`)
	pathYear     = regexp.MustCompile(`^ *\(?([0-9]{4})\)? * - *(.+)`)
	anyYear      = regexp.MustCompile(`(?:^|[^0-9])([0-9]{4})(?:[^0-9]|$)`)
)

// parseBookPath follows Audiobookshelf's folder convention: the last folder
// names the book, the one above it (when there are at least two more) names
// the series and the next one up names the author: Author/Series/Book.
func parseBookPath(rel string) pathMetadata {
	parts := []string{}
	for _, p := range strings.Split(filepath.ToSlash(rel), "/") {
		if p = strings.TrimSpace(p); p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	out := pathMetadata{}
	if len(parts) == 0 {
		return out
	}
	folder := parts[len(parts)-1]
	parts = parts[:len(parts)-1]
	if len(parts) > 1 {
		out.Series = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 0 {
		out.Author = parts[len(parts)-1]
	}
	if m := pathASIN.FindStringSubmatchIndex(folder); m != nil {
		out.ASIN = folder[m[2]:m[3]]
		folder = strings.Join(strings.Fields(folder[:m[0]]+" "+folder[m[1]:]), " ")
	}
	if m := pathNarrator.FindStringSubmatch(folder); m != nil {
		folder = strings.TrimSpace(m[1])
		out.Narrators = splitNames(m[2])
	}
	if out.Series != "" {
		folder, out.Sequence = pathSequenceOf(folder)
	}
	if m := pathYear.FindStringSubmatch(folder); m != nil {
		out.Year, folder = m[1], strings.TrimSpace(m[2])
	}
	out.Title = strings.TrimSpace(folder)
	return out
}

// pathSequenceOf removes a volume prefix ("Book 3", "Vol. 3", "#3", "3.")
// from one " - " separated part. Four-digit years never match.
func pathSequenceOf(folder string) (string, string) {
	parts := strings.Split(folder, " - ")
	for i, part := range parts {
		m := pathSequence.FindStringSubmatch(part)
		// A bare number followed by more words ("3 Body Problem") is a title.
		if m == nil || (m[4] != "" && m[1] == "" && m[3] == "") {
			continue
		}
		parts[i] = m[4]
		if parts[i] == "" {
			parts = append(parts[:i], parts[i+1:]...)
		}
		rest := strings.TrimSpace(strings.Join(parts, " - "))
		if rest == "" {
			return folder, ""
		}
		return rest, normalizeSequence(m[2])
	}
	return folder, ""
}

func normalizeSequence(s string) string {
	s, _, _ = strings.Cut(s, "/")
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseFloat(s, 64); err == nil && n >= 0 {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return s
}

// yearOf extracts the first four-digit year from a date tag such as
// "2019-05-01T00:00:00Z" or "(p) 2004".
func yearOf(s string) string {
	if m := anyYear.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// splitNames separates "A & B", "A; B", "A and B" and "A, B". A comma alone
// only separates names when every part looks like a full name or there are at
// least two commas, so a single "Last, First" author stays one person.
func splitNames(s string) []string {
	major := []string{s}
	for _, sep := range []string{"&", ";", " and "} {
		if strings.Contains(s, sep) {
			major = strings.Split(s, sep)
			break
		}
	}
	out := []string{}
	for _, m := range major {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		pieces := strings.Split(m, ",")
		split := len(pieces) > 2
		if len(pieces) == 2 {
			split = strings.Contains(strings.TrimSpace(pieces[0]), " ") && strings.Contains(strings.TrimSpace(pieces[1]), " ")
		}
		if !split {
			pieces = []string{m}
		}
		for _, p := range pieces {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// seriesFromGrouping reads "Series #3", "Series, Book 3" or "Series Book 3"
// style grouping tags. Grouping often holds a genre instead, so a value
// without a recognisable sequence is not treated as a series.
var groupingSequence = regexp.MustCompile(`(?i)^(.*?)(?:\s*#\s*|,?\s+(?:book|vol\.?|volume)\s+)(\d{1,3}(?:\.\d{1,2})?)$`)

func seriesFromGrouping(s string) (string, string) {
	s = strings.TrimSpace(s)
	if m := groupingSequence.FindStringSubmatch(s); m != nil && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1]), normalizeSequence(m[2])
	}
	return "", ""
}

// tagMetadata is everything the scanner reads from embedded audio tags.
type tagMetadata struct {
	Title, Subtitle, Description, Publisher, Year, Language, ASIN, ISBN, Series, Sequence, Genre string
	Authors, Narrators                                                                           []string
}

func firstTag(t map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(t[k]); v != "" {
			return v
		}
	}
	return ""
}

func readTags(t map[string]string, singleFile bool) tagMetadata {
	out := tagMetadata{}
	out.Title = firstTag(t, "album")
	if out.Title == "" && singleFile {
		out.Title = firstTag(t, "title")
	}
	if a := firstTag(t, "album_artist", "artist"); a != "" {
		out.Authors = splitNames(a)
	}
	if n := firstTag(t, "composer"); n != "" {
		out.Narrators = splitNames(n)
	}
	out.Subtitle = firstTag(t, "subtitle")
	out.Description = firstTag(t, "description", "comment")
	out.Publisher = firstTag(t, "publisher")
	out.Year = yearOf(firstTag(t, "date", "year", "originalyear", "originaldate"))
	out.Language = firstTag(t, "language", "lang")
	out.ASIN = firstTag(t, "asin", "audible_asin", "com.apple.itunes:asin", "----:com.apple.itunes:asin")
	out.ISBN = firstTag(t, "isbn")
	out.Genre = firstTag(t, "genre")
	out.Series = firstTag(t, "series", "mvnm", "movementname")
	if out.Series != "" {
		out.Sequence = normalizeSequence(firstTag(t, "series-part", "mvin", "movement"))
	} else if g := firstTag(t, "grouping"); g != "" {
		out.Series, out.Sequence = seriesFromGrouping(g)
	}
	return out
}
