// Package feed parses podcast syndication without running scripts or resolving
// XML external entities. IDs remain stable when publishers reorder episodes.
package feed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Episode struct {
	ID          string  `json:"id"`
	GUID        string  `json:"guid"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	URL         string  `json:"enclosureURL"`
	MIME        string  `json:"enclosureType"`
	Size        int64   `json:"enclosureSize"`
	PublishedAt int64   `json:"publishedAt"`
	Duration    float64 `json:"duration"`
	Season      string  `json:"season"`
	Number      string  `json:"episode"`
	Type        string  `json:"episodeType"`
}
type Feed struct {
	Title       string    `json:"title"`
	Author      string    `json:"author"`
	Description string    `json:"description"`
	Image       string    `json:"imageURL"`
	Language    string    `json:"language"`
	Explicit    bool      `json:"explicit"`
	Episodes    []Episode `json:"episodes"`
}
type enclosure struct {
	URL    string `xml:"url,attr"`
	Type   string `xml:"type,attr"`
	Length int64  `xml:"length,attr"`
}
type image struct {
	Href string `xml:"href,attr"`
	URL  string `xml:"url"`
}
type rssItem struct {
	Title       string    `xml:"title"`
	Description string    `xml:"description"`
	Summary     string    `xml:"summary"`
	Encoded     string    `xml:"encoded"`
	GUID        string    `xml:"guid"`
	Enclosure   enclosure `xml:"enclosure"`
	Date        string    `xml:"pubDate"`
	Duration    string    `xml:"duration"`
	Season      string    `xml:"season"`
	Number      string    `xml:"episode"`
	Type        string    `xml:"episodeType"`
}
type channel struct {
	Title       string    `xml:"title"`
	Description string    `xml:"description"`
	Author      string    `xml:"author"`
	Language    string    `xml:"language"`
	Explicit    string    `xml:"explicit"`
	Images      []image   `xml:"image"`
	Items       []rssItem `xml:"item"`
}
type atomLink struct {
	Href   string `xml:"href,attr"`
	Rel    string `xml:"rel,attr"`
	Type   string `xml:"type,attr"`
	Length int64  `xml:"length,attr"`
}

func stableID(base, guid, url string) string {
	if guid == "" {
		guid = url
	}
	hash := sha256.Sum256([]byte(base + "\x00" + guid))
	return "episode-" + hex.EncodeToString(hash[:16])
}
func resolve(base, raw string) string {
	u, e := url.Parse(raw)
	if e != nil || raw == "" {
		return ""
	}
	b, e := url.Parse(base)
	if e != nil {
		return ""
	}
	u = b.ResolveReference(u)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	return u.String()
}
func date(raw string) int64 {
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339Nano, "Mon, 2 Jan 2006 15:04:05 -0700", "2006-01-02"} {
		if t, e := time.Parse(layout, strings.TrimSpace(raw)); e == nil {
			return t.UnixMilli()
		}
	}
	return 0
}
func duration(raw string) float64 {
	n := 0.0
	for _, p := range strings.Split(strings.TrimSpace(raw), ":") {
		v, e := strconv.ParseFloat(p, 64)
		if e != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		n = n*60 + v
	}
	if n > 366*86400 {
		return 0
	}
	return n
}
func Parse(data []byte, base string) (Feed, error) {
	out := Feed{Episodes: []Episode{}}
	if len(data) > 10<<20 {
		return out, errors.New("feed exceeds 10 MiB")
	}
	var envelope struct {
		XMLName xml.Name
		Channel channel `xml:"channel"`
	}
	if err := xml.Unmarshal(data, &envelope); err != nil {
		return out, fmt.Errorf("invalid podcast XML: %w", err)
	}
	switch envelope.XMLName.Local {
	case "rss":
		c := envelope.Channel
		out.Title = c.Title
		out.Author = c.Author
		out.Description = c.Description
		out.Language = c.Language
		out.Explicit = strings.EqualFold(c.Explicit, "true") || strings.EqualFold(c.Explicit, "yes")
		for _, img := range c.Images {
			u := img.Href
			if u == "" {
				u = img.URL
			}
			if u = resolve(base, u); u != "" {
				out.Image = u
			}
		}
		for _, e := range c.Items {
			u := resolve(base, e.Enclosure.URL)
			if u == "" {
				continue
			}
			description := e.Description
			if description == "" {
				description = e.Summary
			}
			if description == "" {
				description = e.Encoded
			}
			out.Episodes = append(out.Episodes, Episode{ID: stableID(base, e.GUID, u), GUID: e.GUID, Title: e.Title, Description: description, URL: u, MIME: e.Enclosure.Type, Size: max(0, e.Enclosure.Length), PublishedAt: date(e.Date), Duration: duration(e.Duration), Season: e.Season, Number: e.Number, Type: e.Type})
		}
	case "feed":
		var a struct {
			Title    string `xml:"title"`
			Subtitle string `xml:"subtitle"`
			Logo     string `xml:"logo"`
			Author   struct {
				Name string `xml:"name"`
			} `xml:"author"`
			Entries []struct {
				ID        string     `xml:"id"`
				Title     string     `xml:"title"`
				Summary   string     `xml:"summary"`
				Published string     `xml:"published"`
				Updated   string     `xml:"updated"`
				Links     []atomLink `xml:"link"`
			} `xml:"entry"`
		}
		if err := xml.Unmarshal(data, &a); err != nil {
			return out, err
		}
		out.Title = a.Title
		out.Author = a.Author.Name
		out.Description = a.Subtitle
		out.Image = resolve(base, a.Logo)
		for _, e := range a.Entries {
			for _, l := range e.Links {
				if l.Rel != "enclosure" {
					continue
				}
				u := resolve(base, l.Href)
				if u == "" {
					continue
				}
				at := date(e.Published)
				if at == 0 {
					at = date(e.Updated)
				}
				out.Episodes = append(out.Episodes, Episode{ID: stableID(base, e.ID, u), GUID: e.ID, Title: e.Title, Description: e.Summary, URL: u, MIME: l.Type, Size: max(0, l.Length), PublishedAt: at})
			}
		}
	default:
		return out, errors.New("expected RSS or Atom podcast feed")
	}
	out.Title = strings.TrimSpace(out.Title)
	if out.Title == "" || len(out.Title) > 2048 || len(out.Episodes) > 10000 {
		return out, errors.New("feed title or episode count is invalid")
	}
	unique := []Episode{}
	seen := map[string]bool{}
	for _, e := range out.Episodes {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		unique = append(unique, e)
	}
	out.Episodes = unique
	sort.SliceStable(out.Episodes, func(i, j int) bool { return out.Episodes[i].PublishedAt > out.Episodes[j].PublishedAt })
	return out, nil
}

type Subscription struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func OPML(data []byte) ([]Subscription, error) {
	if len(data) > 2<<20 {
		return nil, errors.New("OPML exceeds 2 MiB")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	out := []Subscription{}
	seen := map[string]bool{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		s, ok := token.(xml.StartElement)
		if !ok || s.Name.Local != "outline" {
			continue
		}
		title, raw := "", ""
		for _, a := range s.Attr {
			switch strings.ToLower(a.Name.Local) {
			case "text", "title":
				title = a.Value
			case "xmlurl":
				raw = a.Value
			}
		}
		if u := resolve(raw, raw); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, Subscription{Title: title, URL: u})
			if len(out) > 1000 {
				return nil, errors.New("OPML exceeds 1000 subscriptions")
			}
		}
	}
	return out, nil
}
func ExportOPML(subs []Subscription) ([]byte, error) {
	type outline struct {
		Text string `xml:"text,attr"`
		Type string `xml:"type,attr"`
		URL  string `xml:"xmlUrl,attr"`
	}
	doc := struct {
		XMLName  xml.Name  `xml:"opml"`
		Version  string    `xml:"version,attr"`
		Title    string    `xml:"head>title"`
		Outlines []outline `xml:"body>outline"`
	}{Version: "2.0", Title: "Pocket Audiobooks subscriptions", Outlines: []outline{}}
	for _, s := range subs {
		doc.Outlines = append(doc.Outlines, outline{Text: s.Title, Type: "rss", URL: s.URL})
	}
	b, e := xml.MarshalIndent(doc, "", "  ")
	return append([]byte(xml.Header), b...), e
}
