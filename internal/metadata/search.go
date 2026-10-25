// Package metadata searches independent metadata services. Results are proposals:
// choosing one fills the editor; it never silently changes the library.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
)

// Provider is one configured metadata source. Region applies to Audible.
type Provider struct{ ID, Kind, URL, Authorization, Region string }
type Candidate struct {
	ID       string         `json:"id"`
	Provider string         `json:"provider"`
	Title    string         `json:"title"`
	Authors  []string       `json:"authors"`
	Cover    string         `json:"cover"`
	Duration float64        `json:"duration,omitempty"` // seconds, when the provider knows the runtime
	Region   string         `json:"region,omitempty"`
	Metadata map[string]any `json:"metadata"`
}
type Client struct {
	HTTP      *http.Client
	UserAgent string
	// AudibleURL and AudnexusURL replace the public API bases (tests).
	AudibleURL, AudnexusURL string
	// Limiter spaces Audible/Audnexus requests; nil uses AudibleLimiter.
	Limiter *Limiter
}

func (c Client) json(ctx context.Context, raw, auth string, out any) error {
	b, _, err := fetch.Get(ctx, c.HTTP, raw, c.UserAgent, auth, 4<<20)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, out); err != nil {
		return errors.New("metadata provider returned invalid JSON")
	}
	return nil
}
func names(v []string) []any {
	out := []any{}
	for _, n := range v {
		out = append(out, map[string]any{"name": n})
	}
	return out
}
func first(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}
func (c Client) Search(ctx context.Context, p Provider, q, author, isbn string) ([]Candidate, error) {
	if len(q) > 500 || len(author) > 500 || len(isbn) > 100 || strings.TrimSpace(q+isbn) == "" {
		return nil, errors.New("enter a title or ISBN")
	}
	out := []Candidate{}
	switch p.Kind {
	case "audible":
		return c.searchAudible(ctx, p, q, author, isbn)
	case "openlibrary":
		params := url.Values{"q": {q}, "limit": {"20"}, "fields": {"key,title,author_name,first_publish_year,isbn,cover_i,language,publisher"}}
		if author != "" {
			params.Set("author", author)
		}
		if isbn != "" {
			params.Set("isbn", isbn)
		}
		var response struct {
			Docs []struct {
				Key       string
				Title     string
				Authors   []string `json:"author_name"`
				Year      int      `json:"first_publish_year"`
				ISBN      []string `json:"isbn"`
				Cover     int      `json:"cover_i"`
				Language  []string
				Publisher []string
			}
		}
		if err := c.json(ctx, "https://openlibrary.org/search.json?"+params.Encode(), "", &response); err != nil {
			return nil, err
		}
		for _, d := range response.Docs {
			m := map[string]any{"title": d.Title, "authors": names(d.Authors)}
			if d.Year > 0 {
				m["publishedYear"] = fmt.Sprint(d.Year)
			}
			if len(d.ISBN) > 0 {
				m["isbn"] = d.ISBN[0]
			}
			if len(d.Publisher) > 0 {
				m["publisher"] = d.Publisher[0]
			}
			if len(d.Language) > 0 {
				m["language"] = d.Language[0]
			}
			cover := ""
			if d.Cover > 0 {
				cover = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", d.Cover)
			}
			out = append(out, Candidate{ID: strings.TrimPrefix(d.Key, "/works/"), Provider: p.ID, Title: d.Title, Authors: d.Authors, Cover: cover, Metadata: m})
		}
	case "google":
		query := q
		if author != "" {
			query += " inauthor:" + author
		}
		if isbn != "" {
			query = "isbn:" + isbn
		}
		params := url.Values{"q": {query}, "maxResults": {"20"}, "key": {p.Authorization}}
		var response struct {
			Items []struct {
				ID   string
				Info struct {
					Title, Subtitle, Publisher, PublishedDate, Description, Language string
					Authors, Categories                                              []string
					ImageLinks                                                       map[string]string
					IndustryIdentifiers                                              []struct{ Type, Identifier string }
				} `json:"volumeInfo"`
			}
		}
		if err := c.json(ctx, "https://www.googleapis.com/books/v1/volumes?"+params.Encode(), "", &response); err != nil {
			return nil, err
		}
		for _, item := range response.Items {
			d := item.Info
			m := map[string]any{"title": d.Title, "authors": names(d.Authors)}
			for k, v := range map[string]string{"subtitle": d.Subtitle, "publisher": d.Publisher, "publishedDate": d.PublishedDate, "description": d.Description, "language": d.Language} {
				if v != "" {
					m[k] = v
				}
			}
			if len(d.PublishedDate) >= 4 {
				m["publishedYear"] = d.PublishedDate[:4]
			}
			if len(d.Categories) > 0 {
				m["genres"] = d.Categories
			}
			for _, id := range d.IndustryIdentifiers {
				if id.Type == "ISBN_13" {
					m["isbn"] = id.Identifier
					break
				}
				if id.Type == "ISBN_10" {
					m["isbn"] = id.Identifier
				}
			}
			cover := strings.Replace(d.ImageLinks["thumbnail"], "http://", "https://", 1)
			out = append(out, Candidate{ID: item.ID, Provider: p.ID, Title: d.Title, Authors: d.Authors, Cover: cover, Metadata: m})
		}
	case "custom":
		params := url.Values{"query": {q}, "mediaType": {"book"}}
		if author != "" {
			params.Set("author", author)
		}
		if isbn != "" {
			params.Set("isbn", isbn)
		}
		var response struct {
			Matches []map[string]any `json:"matches"`
		}
		if err := c.json(ctx, strings.TrimRight(p.URL, "/")+"/search?"+params.Encode(), p.Authorization, &response); err != nil {
			return nil, err
		}
		if response.Matches == nil {
			return nil, errors.New("metadata provider returned no matches array")
		}
		for i, d := range response.Matches {
			if i >= 20 {
				break
			}
			title, _ := d["title"].(string)
			if title == "" {
				continue
			}
			m := map[string]any{"title": title}
			for _, k := range []string{"subtitle", "description", "publisher", "publishedYear", "publishedDate", "isbn", "asin", "language"} {
				if v, ok := d[k].(string); ok && v != "" {
					m[k] = v
				}
			}
			for _, k := range []string{"explicit", "abridged"} {
				if v, ok := d[k].(bool); ok {
					m[k] = v
				}
			}
			for _, k := range []string{"genres", "tags"} {
				if v := stringList(d[k]); len(v) > 0 {
					m[k] = v
				}
			}
			authors := stringList(d["authors"])
			if len(authors) == 0 {
				authors = stringList(d["author"])
			}
			if len(authors) > 0 {
				m["authors"] = names(authors)
			}
			narrators := stringList(d["narrators"])
			if len(narrators) == 0 {
				narrators = stringList(d["narrator"])
			}
			if len(narrators) > 0 {
				m["narrators"] = narrators
			}
			if series, ok := d["series"].([]any); ok {
				refs := []any{}
				for _, v := range series {
					if s, ok := v.(map[string]any); ok {
						n, _ := s["series"].(string)
						if n != "" {
							seq := ""
							if s["sequence"] != nil {
								seq = fmt.Sprint(s["sequence"])
							}
							refs = append(refs, map[string]any{"name": n, "sequence": seq})
						}
					}
				}
				if len(refs) > 0 {
					m["series"] = refs
				}
			}
			cover, _ := d["cover"].(string)
			if _, err := fetch.URL(cover); err != nil {
				cover = ""
			}
			out = append(out, Candidate{ID: fmt.Sprint(i), Provider: p.ID, Title: title, Authors: authors, Cover: cover, Metadata: m})
		}
	default:
		return nil, errors.New("unknown metadata provider")
	}
	return out, nil
}
func stringList(v any) []string {
	out := []string{}
	switch a := v.(type) {
	case string:
		for _, s := range strings.Split(a, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case []any:
		for _, s := range a {
			if v, ok := s.(string); ok && v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

var workID = regexp.MustCompile(`^OL[0-9]+W$`)

func (c Client) Work(ctx context.Context, id string) (map[string]any, error) {
	if !workID.MatchString(id) {
		return nil, errors.New("invalid Open Library work")
	}
	var d struct {
		Description any
		Subjects    []string
	}
	if err := c.json(ctx, "https://openlibrary.org/works/"+id+".json", "", &d); err != nil {
		return nil, err
	}
	m := map[string]any{}
	if s, ok := d.Description.(string); ok {
		m["description"] = s
	} else if v, ok := d.Description.(map[string]any); ok {
		if s, ok := v["value"].(string); ok {
			m["description"] = s
		}
	}
	if len(d.Subjects) > 0 {
		m["genres"] = d.Subjects[:min(10, len(d.Subjects))]
	}
	return m, nil
}
