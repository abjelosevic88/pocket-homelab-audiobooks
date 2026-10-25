package metadata

// Audible search mirrors Audiobookshelf's provider: the public Audible catalog
// API finds products, and Audnexus (https://audnex.us) supplies the cleaned
// per-ASIN details and chapter lists. Both services are shared and unofficial,
// so every request goes through one process-wide limiter.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// AudibleRegions maps Audiobookshelf's region codes to Audible API domains.
var AudibleRegions = map[string]string{"us": ".com", "ca": ".ca", "uk": ".co.uk", "au": ".com.au", "fr": ".fr", "de": ".de", "jp": ".co.jp", "it": ".it", "in": ".in", "es": ".es"}

// RegionCodes lists the supported regions in a stable display order.
var RegionCodes = []string{"us", "ca", "uk", "au", "fr", "de", "jp", "it", "in", "es"}

// ValidRegion reports whether code is a supported Audible region.
func ValidRegion(code string) bool { _, ok := AudibleRegions[code]; return ok }

// DefaultAudibleRegion is AUDIBLE_REGION when valid, otherwise "us".
func DefaultAudibleRegion() string {
	if r := strings.ToLower(strings.TrimSpace(os.Getenv("AUDIBLE_REGION"))); ValidRegion(r) {
		return r
	}
	return "us"
}

// Limiter spaces requests to a shared service. Reservations are handed out in
// order, so an interactive search waits at most one interval behind a bulk job.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func NewLimiter(interval time.Duration) *Limiter { return &Limiter{interval: interval} }

func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil || l.interval <= 0 {
		return ctx.Err()
	}
	l.mu.Lock()
	now := time.Now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(l.interval)
	l.mu.Unlock()
	wait := time.Until(at)
	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// AudibleLimiter keeps Audible and Audnexus traffic near one request a second.
var AudibleLimiter = NewLimiter(time.Second)

const audibleGroups = "contributors,product_desc,product_attrs,product_extended_attrs,media,series,category_ladders"

var (
	asinPattern  = regexp.MustCompile(`^[0-9A-Z]{10}$`)
	sizedImage   = regexp.MustCompile(`\._[A-Za-z0-9,_]+_\.(jpg|jpeg|png)$`)
	seqNumber    = regexp.MustCompile(`\.\d+|\d+(?:\.\d+)?`)
	htmlBreak    = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlBlockEnd = regexp.MustCompile(`(?i)</(p|div|h[1-6]|ul|ol)>`)
	htmlItem     = regexp.MustCompile(`(?i)<li[^>]*>`)
	htmlTag      = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRun     = regexp.MustCompile(`[ \t\f\v\r]+`)
	blankLines   = regexp.MustCompile(`\n{3,}`)
)

// LooksLikeASIN accepts Audible/Amazon product IDs as typed (upper-case
// letters and digits, ten characters, at least one digit) so that a ten-letter
// title such as "FOUNDATION" is still searched as a title.
func LooksLikeASIN(s string) bool {
	return asinPattern.MatchString(s) && strings.ContainsAny(s, "0123456789")
}

// HTMLText converts provider description HTML to plain text with paragraphs.
func HTMLText(s string) string {
	s = htmlBreak.ReplaceAllString(s, "\n")
	s = htmlBlockEnd.ReplaceAllString(s, "\n\n")
	s = htmlItem.ReplaceAllString(s, "\n- ")
	s = html.UnescapeString(htmlTag.ReplaceAllString(s, ""))
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaceRun.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// CleanSequence keeps the number from a series position ("Book 2" → "2").
func CleanSequence(s string) string {
	s = strings.TrimSpace(s)
	if n := seqNumber.FindString(s); n != "" {
		return n
	}
	return s
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (c Client) audibleBase(region string) string {
	if c.AudibleURL != "" {
		return strings.TrimRight(c.AudibleURL, "/")
	}
	return "https://api.audible" + AudibleRegions[region]
}
func (c Client) audnexusBase() string {
	if c.AudnexusURL != "" {
		return strings.TrimRight(c.AudnexusURL, "/")
	}
	return "https://api.audnex.us"
}
func (c Client) limited(ctx context.Context, raw string, out any) error {
	l := c.Limiter
	if l == nil {
		l = AudibleLimiter
	}
	if err := l.Wait(ctx); err != nil {
		return err
	}
	return c.json(ctx, raw, "", out)
}

func region(p Provider) string {
	if ValidRegion(p.Region) {
		return p.Region
	}
	return DefaultAudibleRegion()
}

type audibleProduct struct {
	ASIN      string                  `json:"asin"`
	Title     string                  `json:"title"`
	Subtitle  string                  `json:"subtitle"`
	Authors   []struct{ Name string } `json:"authors"`
	Narrators []struct{ Name string } `json:"narrators"`
	Series    []struct {
		Title    string `json:"title"`
		Sequence string `json:"sequence"`
	} `json:"series"`
	Runtime         float64           `json:"runtime_length_min"`
	ReleaseDate     string            `json:"release_date"`
	IssueDate       string            `json:"issue_date"`
	Publisher       string            `json:"publisher_name"`
	Summary         string            `json:"publisher_summary"`
	Merchandising   string            `json:"merchandising_summary"`
	Language        string            `json:"language"`
	FormatType      string            `json:"format_type"`
	Adult           bool              `json:"is_adult_product"`
	Images          map[string]string `json:"product_images"`
	CategoryLadders []struct {
		Root   string `json:"root"`
		Ladder []struct {
			Name string `json:"name"`
		} `json:"ladder"`
	} `json:"category_ladders"`
}

func addUnique(list []string, seen map[string]bool, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || seen[strings.ToLower(v)] {
		return list
	}
	seen[strings.ToLower(v)] = true
	return append(list, v)
}

func fullImage(u string) string { return sizedImage.ReplaceAllString(u, ".$1") }

func (p audibleProduct) candidate(provider, region string) Candidate {
	m := map[string]any{"title": p.Title, "asin": p.ASIN}
	authors, narrators := []string{}, []string{}
	for _, a := range p.Authors {
		if a.Name != "" {
			authors = append(authors, a.Name)
		}
	}
	for _, n := range p.Narrators {
		if n.Name != "" {
			narrators = append(narrators, n.Name)
		}
	}
	if len(authors) > 0 {
		m["authors"] = names(authors)
	}
	if len(narrators) > 0 {
		m["narrators"] = narrators
	}
	// Numbered series first: Audible lists universes ("The Cosmere") unnumbered.
	series := []any{}
	for _, numbered := range []bool{true, false} {
		for _, s := range p.Series {
			if s.Title != "" && (s.Sequence != "") == numbered {
				series = append(series, map[string]any{"name": s.Title, "sequence": CleanSequence(s.Sequence)})
			}
		}
	}
	if len(series) > 0 {
		m["series"] = series
	}
	desc := HTMLText(p.Summary)
	if desc == "" {
		desc = HTMLText(p.Merchandising)
	}
	date := p.ReleaseDate
	if date == "" {
		date = p.IssueDate
	}
	for k, v := range map[string]string{"subtitle": p.Subtitle, "publisher": p.Publisher, "description": desc, "language": capitalize(p.Language)} {
		if v != "" {
			m[k] = v
		}
	}
	if len(date) >= 4 {
		m["publishedYear"] = date[:4]
		if len(date) >= 10 {
			m["publishedDate"] = date[:10]
		}
	}
	genres, tags, seen := []string{}, []string{}, map[string]bool{}
	for _, l := range p.CategoryLadders {
		for i, e := range l.Ladder {
			if i == 0 {
				genres = addUnique(genres, seen, e.Name)
			}
		}
	}
	for _, l := range p.CategoryLadders {
		for i, e := range l.Ladder {
			if i > 0 {
				tags = addUnique(tags, seen, e.Name)
			}
		}
	}
	if len(genres) > 0 {
		m["genres"] = genres
	}
	if len(tags) > 0 {
		m["tags"] = tags
	}
	m["abridged"] = strings.EqualFold(p.FormatType, "abridged")
	if p.Adult {
		m["explicit"] = true
	}
	cover := ""
	for _, size := range []string{"2400", "1024", "500"} {
		if u := p.Images[size]; u != "" {
			cover = fullImage(u)
			break
		}
	}
	return Candidate{ID: p.ASIN, Provider: provider, Title: p.Title, Authors: authors, Cover: cover, Duration: p.Runtime * 60, Region: region, Metadata: m}
}

func (c Client) searchAudible(ctx context.Context, p Provider, q, author, isbn string) ([]Candidate, error) {
	reg := region(p)
	q = strings.TrimSpace(q)
	out := []Candidate{}
	if LooksLikeASIN(q) {
		if cand, err := c.AudibleBook(ctx, p.ID, reg, q); err == nil {
			return []Candidate{cand}, nil
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	search := func(params url.Values) ([]audibleProduct, error) {
		params.Set("num_results", "10")
		params.Set("products_sort_by", "Relevance")
		params.Set("response_groups", audibleGroups)
		var response struct {
			Products []audibleProduct `json:"products"`
		}
		if err := c.limited(ctx, c.audibleBase(reg)+"/1.0/catalog/products?"+params.Encode(), &response); err != nil {
			return nil, err
		}
		return response.Products, nil
	}
	params := url.Values{}
	switch {
	case q != "" && author != "":
		params.Set("title", q)
		params.Set("author", author)
	case q != "":
		params.Set("keywords", q)
	default:
		params.Set("keywords", isbn)
	}
	products, err := search(params)
	if err != nil {
		return nil, err
	}
	if len(products) == 0 && q != "" && author != "" {
		// Author spellings differ ("J.R.R." vs "J. R. R."); retry by title alone.
		if products, err = search(url.Values{"keywords": {q}}); err != nil {
			return nil, err
		}
	}
	for _, prod := range products {
		if prod.ASIN == "" || prod.Title == "" {
			continue
		}
		out = append(out, prod.candidate(p.ID, reg))
	}
	return out, nil
}

type audnexusBook struct {
	ASIN            string                  `json:"asin"`
	Title           string                  `json:"title"`
	Subtitle        string                  `json:"subtitle"`
	Authors         []struct{ Name string } `json:"authors"`
	Narrators       []struct{ Name string } `json:"narrators"`
	SeriesPrimary   *audnexusSeries         `json:"seriesPrimary"`
	SeriesSecondary *audnexusSeries         `json:"seriesSecondary"`
	Summary         string                  `json:"summary"`
	Description     string                  `json:"description"`
	Publisher       string                  `json:"publisherName"`
	ReleaseDate     string                  `json:"releaseDate"`
	Language        string                  `json:"language"`
	Genres          []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"genres"`
	Image      string  `json:"image"`
	ISBN       string  `json:"isbn"`
	Runtime    float64 `json:"runtimeLengthMin"`
	FormatType string  `json:"formatType"`
	Adult      bool    `json:"isAdult"`
	Region     string  `json:"region"`
}
type audnexusSeries struct {
	Name     string `json:"name"`
	Position string `json:"position"`
}

// AudibleBook reads one ASIN's details from Audnexus.
func (c Client) AudibleBook(ctx context.Context, provider, region, asin string) (Candidate, error) {
	asin = strings.ToUpper(strings.TrimSpace(asin))
	if !asinPattern.MatchString(asin) {
		return Candidate{}, errors.New("invalid ASIN")
	}
	if !ValidRegion(region) {
		region = DefaultAudibleRegion()
	}
	var b audnexusBook
	if err := c.limited(ctx, c.audnexusBase()+"/books/"+asin+"?"+url.Values{"region": {region}}.Encode(), &b); err != nil {
		return Candidate{}, err
	}
	if b.ASIN == "" || b.Title == "" {
		return Candidate{}, errors.New("Audnexus has no details for this ASIN")
	}
	m := map[string]any{"title": b.Title, "asin": b.ASIN}
	authors, narrators := []string{}, []string{}
	for _, a := range b.Authors {
		if a.Name != "" {
			authors = append(authors, a.Name)
		}
	}
	for _, n := range b.Narrators {
		if n.Name != "" {
			narrators = append(narrators, n.Name)
		}
	}
	if len(authors) > 0 {
		m["authors"] = names(authors)
	}
	if len(narrators) > 0 {
		m["narrators"] = narrators
	}
	series := []any{}
	for _, s := range []*audnexusSeries{b.SeriesPrimary, b.SeriesSecondary} {
		if s != nil && s.Name != "" {
			series = append(series, map[string]any{"name": s.Name, "sequence": CleanSequence(s.Position)})
		}
	}
	if len(series) > 0 {
		m["series"] = series
	}
	desc := HTMLText(b.Summary)
	if desc == "" {
		desc = HTMLText(b.Description)
	}
	for k, v := range map[string]string{"subtitle": b.Subtitle, "publisher": b.Publisher, "description": desc, "language": capitalize(b.Language), "isbn": b.ISBN} {
		if v != "" {
			m[k] = v
		}
	}
	if len(b.ReleaseDate) >= 4 {
		m["publishedYear"] = b.ReleaseDate[:4]
		if len(b.ReleaseDate) >= 10 {
			m["publishedDate"] = b.ReleaseDate[:10]
		}
	}
	genres, tags := []string{}, []string{}
	seen := map[string]bool{}
	for _, g := range b.Genres {
		if g.Type == "genre" {
			genres = addUnique(genres, seen, g.Name)
		}
	}
	for _, g := range b.Genres {
		if g.Type == "tag" {
			tags = addUnique(tags, seen, g.Name)
		}
	}
	if len(genres) > 0 {
		m["genres"] = genres
	}
	if len(tags) > 0 {
		m["tags"] = tags
	}
	m["abridged"] = strings.EqualFold(b.FormatType, "abridged")
	if b.Adult {
		m["explicit"] = true
	}
	return Candidate{ID: b.ASIN, Provider: provider, Title: b.Title, Authors: authors, Cover: b.Image, Duration: b.Runtime * 60, Region: region, Metadata: m}, nil
}

// AudibleChapter is one chapter in seconds from the beginning of the book.
type AudibleChapter struct {
	Title string  `json:"title"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}
type AudibleChapters struct {
	ASIN     string           `json:"asin"`
	Region   string           `json:"region"`
	Accurate bool             `json:"accurate"`
	Runtime  float64          `json:"runtime"`
	Chapters []AudibleChapter `json:"chapters"`
}

// AudibleChapterList reads an ASIN's chapter markers from Audnexus.
func (c Client) AudibleChapterList(ctx context.Context, region, asin string) (AudibleChapters, error) {
	asin = strings.ToUpper(strings.TrimSpace(asin))
	if !asinPattern.MatchString(asin) {
		return AudibleChapters{}, errors.New("invalid ASIN")
	}
	if !ValidRegion(region) {
		region = DefaultAudibleRegion()
	}
	var raw struct {
		ASIN       string  `json:"asin"`
		Accurate   bool    `json:"isAccurate"`
		RuntimeMs  float64 `json:"runtimeLengthMs"`
		RuntimeSec float64 `json:"runtimeLengthSec"`
		Chapters   []struct {
			Title   string  `json:"title"`
			StartMs float64 `json:"startOffsetMs"`
			LenMs   float64 `json:"lengthMs"`
		} `json:"chapters"`
	}
	if err := c.limited(ctx, c.audnexusBase()+"/books/"+asin+"/chapters?"+url.Values{"region": {region}}.Encode(), &raw); err != nil {
		return AudibleChapters{}, err
	}
	if len(raw.Chapters) == 0 {
		return AudibleChapters{}, errors.New("Audnexus has no chapters for this ASIN")
	}
	out := AudibleChapters{ASIN: asin, Region: region, Accurate: raw.Accurate, Runtime: raw.RuntimeMs / 1000, Chapters: []AudibleChapter{}}
	if out.Runtime == 0 {
		out.Runtime = raw.RuntimeSec
	}
	for i, ch := range raw.Chapters {
		title := strings.TrimSpace(ch.Title)
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		out.Chapters = append(out.Chapters, AudibleChapter{Title: title, Start: ch.StartMs / 1000, End: (ch.StartMs + ch.LenMs) / 1000})
	}
	sort.SliceStable(out.Chapters, func(i, j int) bool { return out.Chapters[i].Start < out.Chapters[j].Start })
	return out, nil
}

// Merge overlays detail fields onto a search candidate; detail wins.
func Merge(base, detail Candidate) Candidate {
	out := base
	m := map[string]any{}
	for k, v := range base.Metadata {
		m[k] = v
	}
	for k, v := range detail.Metadata {
		if !Empty(v) {
			m[k] = v
		}
	}
	out.Metadata = m
	if detail.Title != "" {
		out.Title = detail.Title
	}
	if len(detail.Authors) > 0 {
		out.Authors = detail.Authors
	}
	if detail.Cover != "" {
		out.Cover = detail.Cover
	}
	if detail.Duration > 0 {
		out.Duration = detail.Duration
	}
	return out
}

// Empty reports whether a metadata value carries nothing.
func Empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []string:
		return len(x) == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
