package metadata

// Author profiles mirror Audiobookshelf's Audnexus author provider: a name
// search returns candidate ASINs, the closest name (edit distance, at most
// three changes) is chosen, and its detail record supplies the biography and
// photo. Requests share the Audible limiter.

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// AuthorRef is one Audnexus author search result.
type AuthorRef struct {
	ASIN string `json:"asin"`
	Name string `json:"name"`
}

// AuthorProfile is an author's Audnexus detail record with plain-text bio.
type AuthorProfile struct {
	ASIN        string   `json:"asin"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Image       string   `json:"image"`
	Genres      []string `json:"genres"`
	Region      string   `json:"region"`
	// Distance is the name edit distance when the profile was found by name.
	Distance int `json:"distance"`
}

// MaxAuthorDistance is Audiobookshelf's default author-name tolerance.
const MaxAuthorDistance = 3

// ErrNoAuthor reports that Audnexus has no matching author.
var ErrNoAuthor = errors.New("no matching author on Audible")

func authorRegion(region string) string {
	if ValidRegion(region) {
		return region
	}
	return DefaultAudibleRegion()
}

// AuthorDistance is the edit distance of two author names after folding case,
// accents and punctuation and reading "Last, First" as "First Last".
func AuthorDistance(a, b string) int {
	ra, rb := []rune(strings.Join(nameTokens(a), " ")), []rune(strings.Join(nameTokens(b), " "))
	return levenshtein(ra, rb)
}

// AudnexusAuthorSearch lists Audnexus authors for a name, without duplicates.
func (c Client) AudnexusAuthorSearch(ctx context.Context, region, name string) ([]AuthorRef, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 500 {
		return nil, errors.New("enter an author name")
	}
	var raw []AuthorRef
	if err := c.limited(ctx, c.audnexusBase()+"/authors?"+url.Values{"name": {name}, "region": {authorRegion(region)}}.Encode(), &raw); err != nil {
		return nil, err
	}
	out, seen := []AuthorRef{}, map[string]bool{}
	for _, a := range raw {
		a.ASIN, a.Name = strings.ToUpper(strings.TrimSpace(a.ASIN)), strings.TrimSpace(a.Name)
		if !asinPattern.MatchString(a.ASIN) || a.Name == "" || seen[a.ASIN] {
			continue
		}
		seen[a.ASIN] = true
		out = append(out, a)
	}
	return out, nil
}

// AudnexusAuthor reads one author ASIN's profile. Audnexus answers unknown
// ASINs with an empty record (sometimes a product's name), which is rejected.
func (c Client) AudnexusAuthor(ctx context.Context, region, asin string) (AuthorProfile, error) {
	asin = strings.ToUpper(strings.TrimSpace(asin))
	if !asinPattern.MatchString(asin) {
		return AuthorProfile{}, errors.New("invalid ASIN")
	}
	region = authorRegion(region)
	var raw struct {
		ASIN        string `json:"asin"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Image       string `json:"image"`
		Region      string `json:"region"`
		Genres      []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"genres"`
	}
	if err := c.limited(ctx, c.audnexusBase()+"/authors/"+asin+"?"+url.Values{"region": {region}}.Encode(), &raw); err != nil {
		return AuthorProfile{}, err
	}
	p := AuthorProfile{ASIN: strings.ToUpper(strings.TrimSpace(raw.ASIN)), Name: strings.TrimSpace(raw.Name), Description: HTMLText(raw.Description), Image: strings.TrimSpace(raw.Image), Genres: []string{}, Region: region}
	if p.ASIN == "" {
		p.ASIN = asin
	}
	seen := map[string]bool{}
	for _, g := range raw.Genres {
		if g.Type == "" || g.Type == "genre" {
			p.Genres = addUnique(p.Genres, seen, g.Name)
		}
	}
	if p.Image != "" {
		if u, err := url.Parse(p.Image); err != nil || u.Scheme != "https" || u.Host == "" {
			p.Image = ""
		}
	}
	if p.Name == "" || (p.Description == "" && p.Image == "" && len(p.Genres) == 0) {
		return AuthorProfile{}, ErrNoAuthor
	}
	return p, nil
}

// BestAuthor picks the search result closest to name (first wins on ties),
// or false when none is within MaxAuthorDistance edits.
func BestAuthor(name string, refs []AuthorRef) (AuthorRef, int, bool) {
	best, dist := AuthorRef{}, -1
	for _, r := range refs {
		d := AuthorDistance(name, r.Name)
		if dist < 0 || d < dist {
			best, dist = r, d
		}
	}
	limit := MaxAuthorDistance
	// Very short names would otherwise match almost anything.
	if n := len([]rune(strings.Join(nameTokens(name), " "))); n/3 < limit {
		limit = n / 3
	}
	return best, dist, dist >= 0 && dist <= limit
}

// FindAudnexusAuthor searches by name and reads the closest author's profile.
func (c Client) FindAudnexusAuthor(ctx context.Context, region, name string) (AuthorProfile, error) {
	refs, err := c.AudnexusAuthorSearch(ctx, region, name)
	if err != nil {
		return AuthorProfile{}, err
	}
	ref, dist, ok := BestAuthor(name, refs)
	if !ok {
		return AuthorProfile{}, ErrNoAuthor
	}
	p, err := c.AudnexusAuthor(ctx, region, ref.ASIN)
	if err != nil {
		return AuthorProfile{}, err
	}
	p.Distance = dist
	return p, nil
}
