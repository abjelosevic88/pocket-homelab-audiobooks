package metadata

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// MatchQuery describes the library book a candidate is compared with.
type MatchQuery struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	Authors  []string `json:"authors"`
	Duration float64  `json:"duration,omitempty"` // seconds
	ASIN     string   `json:"asin,omitempty"`
	ISBN     string   `json:"isbn,omitempty"`
}

// Score explains how well a candidate fits. Parts are 0–1; -1 means unknown.
type Score struct {
	Total     float64 `json:"total"`
	Title     float64 `json:"title"`
	Author    float64 `json:"author"`
	Duration  float64 `json:"duration"`
	Exact     bool    `json:"exact"`
	Confident bool    `json:"confident"`
}

type Ranked struct {
	Candidate
	Score Score `json:"score"`
}

// Confidence thresholds used by quick match.
const (
	MinTitle         = 0.9
	MinAuthor        = 0.8
	MinTitleNoAuthor = 0.97
)

var (
	bracketed   = regexp.MustCompile(`\s*\[[^\]]*\]\s*`)
	parenthesis = regexp.MustCompile(`\s*\([^)]*\)\s*`)
	editionWord = regexp.MustCompile(`(?i)\b(unabridged|abridged|audiobook|audio book|audio edition|dramati[sz]ed|full[- ]cast)\b`)
	trackPrefix = regexp.MustCompile(`^\d{1,3}\s+-\s+`)
	leadArticle = regexp.MustCompile(`^(the|a|an) `)
	isbnChars   = regexp.MustCompile(`[^0-9Xx]`)
)

// CleanTitle prepares a library title for a provider query: it drops
// "(Unabridged)", bracketed release notes, series parentheticals and track
// prefixes, like Audiobookshelf does before searching.
func CleanTitle(s string) string {
	s = strings.TrimSpace(s)
	orig := s
	s = bracketed.ReplaceAllString(s, " ")
	s = parenthesis.ReplaceAllString(s, " ")
	s = editionWord.ReplaceAllString(s, " ")
	s = trackPrefix.ReplaceAllString(strings.TrimSpace(s), "")
	s = strings.Trim(strings.Join(strings.Fields(s), " "), " -:;,.")
	if s == "" {
		return orig
	}
	return s
}

// SplitAuthors splits a display author string ("A, B & C") into names.
func SplitAuthors(s string) []string {
	out := []string{}
	for _, part := range regexp.MustCompile(`\s*(?:;|&|,| and )\s*`).Split(s, -1) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

var fold = map[rune]string{'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'æ': "ae", 'ç': "c", 'č': "c", 'ć': "c", 'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ñ': "n", 'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ý': "y", 'ÿ': "y", 'š': "s", 'ž': "z", 'đ': "d", 'ß': "ss", 'ł': "l", 'œ': "oe"}

// normalize lower-cases, folds accents and punctuation, and drops a leading
// article so "The Hobbit" equals "Hobbit, The".
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '\'' || r == '’':
		case r == '&':
			b.WriteString(" and ")
		case fold[r] != "":
			b.WriteString(fold[r])
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	out = strings.TrimSuffix(out, " the")
	return leadArticle.ReplaceAllString(out, "")
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// Similarity is a 0–1 edit-distance ratio of two normalized strings.
func Similarity(a, b string) float64 {
	a, b = normalize(a), normalize(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ra, rb := []rune(a), []rune(b)
	return 1 - float64(levenshtein(ra, rb))/float64(max(len(ra), len(rb)))
}

func titleScore(q MatchQuery, c Candidate) float64 {
	sub, _ := c.Metadata["subtitle"].(string)
	mine := []string{q.Title, CleanTitle(q.Title)}
	if q.Subtitle != "" {
		mine = append(mine, q.Title+" "+q.Subtitle)
	}
	if i := strings.Index(q.Title, ":"); i > 0 {
		mine = append(mine, q.Title[:i])
	}
	theirs := []string{c.Title, CleanTitle(c.Title)}
	if sub != "" {
		theirs = append(theirs, c.Title+" "+sub)
	}
	best := 0.0
	for _, a := range mine {
		for _, b := range theirs {
			best = math.Max(best, Similarity(a, b))
		}
	}
	return best
}

func nameTokens(s string) []string {
	s = strings.TrimSpace(s)
	// "Weir, Andy" → "Andy Weir"
	if i := strings.Index(s, ","); i > 0 && !strings.Contains(s[i+1:], ",") {
		s = strings.TrimSpace(s[i+1:]) + " " + s[:i]
	}
	return strings.Fields(normalize(s))
}

func authorSimilarity(a, b string) float64 {
	ta, tb := nameTokens(a), nameTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	direct := Similarity(strings.Join(ta, " "), strings.Join(tb, " "))
	sa, sb := append([]string{}, ta...), append([]string{}, tb...)
	sort.Strings(sa)
	sort.Strings(sb)
	best := math.Max(direct, Similarity(strings.Join(sa, " "), strings.Join(sb, " ")))
	// Same surname and compatible initials ("J. R. R. Tolkien" / "John Ronald Reuel Tolkien").
	if ta[len(ta)-1] == tb[len(tb)-1] && ta[0][0] == tb[0][0] {
		best = math.Max(best, 0.9)
	}
	return best
}

func authorScore(q MatchQuery, c Candidate) float64 {
	if len(q.Authors) == 0 || len(c.Authors) == 0 {
		return -1
	}
	best := 0.0
	for _, a := range q.Authors {
		for _, b := range c.Authors {
			best = math.Max(best, authorSimilarity(a, b))
		}
	}
	return best
}

func durationScore(a, b float64) float64 {
	if a <= 0 || b <= 0 {
		return -1
	}
	diff := math.Abs(a - b)
	ratio := diff / math.Max(a, b)
	if diff <= 120 || ratio <= 0.02 {
		return 1
	}
	return math.Max(0, 1-(ratio-0.02)/0.23)
}

func isbnDigits(s string) string { return strings.ToUpper(isbnChars.ReplaceAllString(s, "")) }

// ScoreCandidate compares one candidate with the book.
func ScoreCandidate(q MatchQuery, c Candidate) Score {
	s := Score{Title: titleScore(q, c), Author: authorScore(q, c), Duration: durationScore(q.Duration, c.Duration)}
	asin, _ := c.Metadata["asin"].(string)
	isbn, _ := c.Metadata["isbn"].(string)
	s.Exact = (q.ASIN != "" && strings.EqualFold(strings.TrimSpace(q.ASIN), strings.TrimSpace(asin))) ||
		(len(isbnDigits(q.ISBN)) >= 10 && isbnDigits(q.ISBN) == isbnDigits(isbn))
	total, weight := 0.6*s.Title, 0.6
	if s.Author >= 0 {
		total, weight = total+0.3*s.Author, weight+0.3
	}
	if s.Duration >= 0 {
		total, weight = total+0.1*s.Duration, weight+0.1
	}
	s.Total = math.Round(total/weight*1000) / 1000
	durationOK := s.Duration < 0 || s.Duration > 0
	switch {
	case s.Exact:
		s.Total, s.Confident = 1, true
	case s.Author >= 0:
		s.Confident = s.Title >= MinTitle && s.Author >= MinAuthor && durationOK
	default:
		// Without an author on either side, insist on a near-identical title.
		s.Confident = s.Title >= MinTitleNoAuthor && durationOK && len(q.Authors) == 0
	}
	return s
}

// Rank scores candidates, best first; ties keep provider order.
func Rank(q MatchQuery, cands []Candidate) []Ranked {
	out := make([]Ranked, 0, len(cands))
	for _, c := range cands {
		out = append(out, Ranked{Candidate: c, Score: ScoreCandidate(q, c)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score.Exact != out[j].Score.Exact {
			return out[i].Score.Exact
		}
		return out[i].Score.Total > out[j].Score.Total
	})
	return out
}
