package server

import (
	"encoding/json"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) nativePodcastSearch(w http.ResponseWriter, r *http.Request, uid int64) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native podcasts are not active")
		return
	}
	a, err := s.catalog().Account(r.Context(), uid)
	if err != nil || !a.Permissions.Update {
		writeErr(w, 403, "podcast editing permission required")
		return
	}
	term := strings.TrimSpace(r.URL.Query().Get("term"))
	country := strings.ToLower(r.URL.Query().Get("country"))
	if country == "" {
		country = "us"
	}
	if len(term) < 2 || len(term) > 256 || len(country) != 2 || country[0] < 'a' || country[0] > 'z' || country[1] < 'a' || country[1] > 'z' {
		writeErr(w, 400, "enter a search term and a two-letter country code")
		return
	}
	query := url.Values{"term": {term}, "country": {country}, "media": {"podcast"}, "entity": {"podcast"}, "limit": {"30"}}
	b, _, err := fetch.Get(r.Context(), s.nativeFetchHTTP, "https://itunes.apple.com/search?"+query.Encode(), s.cfg.UserAgent, "", 4<<20)
	if err != nil {
		writeErr(w, 502, "podcast directory is unavailable; you can still subscribe with an RSS URL")
		return
	}
	var result struct {
		Results []struct {
			ID       int64  `json:"collectionId"`
			Title    string `json:"collectionName"`
			Author   string `json:"artistName"`
			Feed     string `json:"feedUrl"`
			Link     string `json:"collectionViewUrl"`
			Explicit string `json:"collectionExplicitness"`
		}
	}
	if json.Unmarshal(b, &result) != nil {
		writeErr(w, 502, "invalid directory response")
		return
	}
	out := []any{}
	for _, v := range result.Results {
		if v.Explicit == "explicit" && !a.Permissions.Explicit {
			continue
		}
		if _, err = fetch.URL(v.Feed); err != nil {
			continue
		}
		link := ""
		if u, e := url.Parse(v.Link); e == nil && u.Scheme == "https" && (u.Hostname() == "podcasts.apple.com" || u.Hostname() == "itunes.apple.com") {
			link = u.String()
		}
		out = append(out, nativeObject{"id": v.ID, "title": v.Title, "artistName": v.Author, "feedUrl": v.Feed, "itunesPageUrl": link, "pageUrl": link, "explicit": v.Explicit == "explicit"})
	}
	writeJSON(w, 200, out)
}
