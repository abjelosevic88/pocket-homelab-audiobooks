package server

import (
	"encoding/xml"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
)

type sharedTrack struct {
	Item, File, Title, MIME, URL string
	Size                         int64
	Duration                     float64
	Published                    int64
}

func (s *Server) shareTracks(r *http.Request, v nativeShare, items []library.Item) ([]sharedTrack, error) {
	tracks := []sharedTrack{}
	base := strings.TrimRight(s.cfg.BaseURL, "/") + "/share/" + url.PathEscape(v.Slug)
	for _, item := range items {
		files, err := s.catalog().Files(r.Context(), v.UserID, item.ID)
		if err != nil {
			return nil, err
		}
		selected := map[string]bool{}
		if v.EpisodeID != "" {
			timeline, _, _, e := s.catalog().Timeline(r.Context(), v.UserID, item.ID, v.EpisodeID)
			if e != nil {
				return nil, e
			}
			for _, t := range timeline {
				selected[t.ID] = true
			}
		}
		audio := []library.File{}
		for _, f := range files {
			if f.Role == "audio" && !f.Excluded && f.Duration > 0 && (v.EpisodeID == "" || selected[f.ID]) {
				audio = append(audio, f)
			}
		}
		episodes := map[string]nativeObject{}
		if item.MediaType == "podcast" {
			wire, e := s.nativeItem(r.Context(), v.UserID, item.ID)
			if e != nil {
				return nil, e
			}
			for _, raw := range nativeArray(nativeMap(wire["media"])["episodes"]) {
				ep := nativeMap(raw)
				episodes[nativeString(nativeMap(ep["audioFile"])["ino"])] = ep
			}
		}
		for i, f := range audio {
			title := item.Title
			date := item.AddedAt
			if ep := episodes[f.ID]; ep != nil {
				title = nativeString(ep["title"])
				date = nativeMillis(ep["publishedAt"])
			} else if len(audio) > 1 {
				title = fmt.Sprintf("%s · Part %d of %d", title, i+1, len(audio))
			}
			tracks = append(tracks, sharedTrack{item.ID, f.ID, title, f.MIME, base + "/file/" + url.PathEscape(item.ID) + "/" + url.PathEscape(f.ID), f.Size, f.Duration, date})
		}
	}
	return tracks, nil
}
func (s *Server) nativePublicShare(w http.ResponseWriter, r *http.Request) {
	v, ok := s.shareAccess(w, r)
	if !ok {
		return
	}
	items, err := s.shareItems(r.Context(), v)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	path := chi.URLParam(r, "*")
	if strings.HasPrefix(r.URL.Path, "/feed/") || path == "feed.xml" {
		s.nativeShareFeed(w, r, v, items)
		return
	}
	if path != "" {
		parts := strings.Split(path, "/")
		allowed := false
		if len(parts) >= 2 {
			for _, item := range items {
				if item.ID == parts[1] {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			writeErr(w, 404, "share item not found")
			return
		}
		switch parts[0] {
		case "hls":
			if len(parts) != 4 {
				writeErr(w, 404, "stream not found")
				return
			}
			tracks, e := s.shareTracks(r, v, items)
			if e != nil {
				s.nativeError(w, e)
				return
			}
			for _, tr := range tracks {
				if tr.Item == parts[1] && tr.File == parts[2] {
					s.nativeHLSTracks(w, r, v.UserID, tr.Item, []library.Track{{ID: tr.File, Duration: tr.Duration, MIME: tr.MIME}}, parts[3], "p:"+v.ID+":"+r.RemoteAddr+":"+tr.Item+":"+tr.File, hlsPoolShare)
					return
				}
			}
		case "file":
			if len(parts) != 3 {
				writeErr(w, 404, "file not found")
				return
			}
			files, e := s.catalog().Files(r.Context(), v.UserID, parts[1])
			if e != nil {
				s.nativeError(w, e)
				return
			}
			for _, f := range files {
				if f.ID == parts[2] {
					audio := f.Role == "audio" && !f.Excluded && f.Duration > 0
					if v.EpisodeID != "" {
						selected, _, _, e := s.catalog().Timeline(r.Context(), v.UserID, v.TargetID, v.EpisodeID)
						if e != nil {
							s.nativeError(w, e)
							return
						}
						audio = false
						for _, track := range selected {
							if track.ID == f.ID {
								audio = true
							}
						}
						// An episode share must never expose files from another episode.
						if !audio {
							writeErr(w, 404, "file not in shared episode")
							return
						}
					}
					if queryBool(r, "download") && !v.Download {
						writeErr(w, 403, "downloads are disabled for this share")
						return
					}
					if !audio && !v.Download {
						writeErr(w, 403, "downloads are disabled for this share")
						return
					}
					s.nativeFile(w, r, v.UserID, parts[1], f.ID, !audio || queryBool(r, "download"))
					return
				}
			}
		case "cover":
			files, e := s.catalog().Files(r.Context(), v.UserID, parts[1])
			if e != nil {
				s.nativeError(w, e)
				return
			}
			for _, f := range files {
				if f.ID == "native-cover" {
					s.nativeFile(w, r, v.UserID, parts[1], f.ID, false)
					return
				}
			}
			for _, f := range files {
				if f.Role == "cover" {
					s.nativeFile(w, r, v.UserID, parts[1], f.ID, false)
					return
				}
			}
		case "download":
			if !v.Download || v.EpisodeID != "" {
				writeErr(w, 403, "book archive downloads are disabled for this share")
				return
			}
			s.nativeDownload(w, r, v.UserID, []string{parts[1]})
			return
		}
		writeErr(w, 404, "share resource not found")
		return
	}
	tracks, err := s.shareTracks(r, v, items)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	type displayFile struct{ ItemID, ID, Name string }
	type displayItem struct {
		Title, ID string
		Files     []displayFile
	}
	display := []displayItem{}
	for _, item := range items {
		entry := displayItem{Title: item.Title, ID: item.ID}
		if v.Download && v.EpisodeID == "" {
			files, e := s.catalog().Files(r.Context(), v.UserID, item.ID)
			if e != nil {
				s.nativeError(w, e)
				return
			}
			for _, f := range files {
				if f.Role == "ebook" || f.Role == "supplement" {
					entry.Files = append(entry.Files, displayFile{item.ID, f.ID, f.Name})
				}
			}
		}
		display = append(display, entry)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'unsafe-inline'; media-src 'self' blob:; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == "HEAD" {
		return
	}
	_ = nativeShareTemplate.Execute(w, map[string]any{"Name": v.Name, "Slug": v.Slug, "ID": v.ID, "Tracks": tracks, "Items": display, "Download": v.Download && v.EpisodeID == "", "FileDownload": v.Download, "Expires": v.ExpiresAt, "Feed": s.shareWire(v).FeedURL})
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}
type rssItem struct {
	Title     string       `xml:"title"`
	GUID      string       `xml:"guid"`
	Date      string       `xml:"pubDate"`
	Duration  int          `xml:"itunes:duration"`
	Number    int          `xml:"itunes:episode"`
	Enclosure rssEnclosure `xml:"enclosure"`
}
type rssImage struct {
	URL   string `xml:"url"`
	Title string `xml:"title"`
	Link  string `xml:"link"`
}
type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Type        string    `xml:"itunes:type"`
	Block       string    `xml:"itunes:block"`
	Image       *rssImage `xml:"image,omitempty"`
	Author      string    `xml:"itunes:author,omitempty"`
	Items       []rssItem `xml:"item"`
}

func (s *Server) nativeShareFeed(w http.ResponseWriter, r *http.Request, v nativeShare, items []library.Item) {
	tracks, err := s.shareTracks(r, v, items)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if len(tracks) == 0 {
		writeErr(w, 400, "this share has no audio tracks")
		return
	}
	channel := rssChannel{Title: v.Name, Link: s.shareWire(v).URL, Description: v.Name, Type: "serial", Block: "Yes", Items: []rssItem{}}
	channel.Author = items[0].Author
	if items[0].MediaType == "podcast" {
		channel.Type = "episodic"
	}
	if files, e := s.catalog().Files(r.Context(), v.UserID, items[0].ID); e == nil {
		for _, f := range files {
			if f.Role == "cover" {
				channel.Image = &rssImage{strings.TrimRight(s.cfg.BaseURL, "/") + "/share/" + url.PathEscape(v.Slug) + "/cover/" + url.PathEscape(items[0].ID), v.Name, channel.Link}
				break
			}
		}
	}
	for i, tr := range tracks {
		date := tr.Published
		if date <= 0 {
			date = v.CreatedAt
		}
		channel.Items = append(channel.Items, rssItem{tr.Title, v.ID + ":" + tr.Item + ":" + tr.File, time.UnixMilli(date).UTC().Format(time.RFC1123Z), int(tr.Duration), i + 1, rssEnclosure{tr.URL, tr.Size, tr.MIME}})
	}
	doc := struct {
		XMLName xml.Name   `xml:"rss"`
		Version string     `xml:"version,attr"`
		ITunes  string     `xml:"xmlns:itunes,attr"`
		Channel rssChannel `xml:"channel"`
	}{Version: "2.0", ITunes: "http://www.itunes.com/dtds/podcast-1.0.dtd", Channel: channel}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == "HEAD" {
		return
	}
	fmt.Fprint(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(doc)
}

var nativeShareTemplate = template.Must(template.New("share").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Name}} · Pocket Audiobooks</title><style>body{font:16px/1.6 system-ui;margin:auto;padding:28px;max-width:800px;background:#f8faf9;color:#172622}a{color:#08796e}button{font:inherit;cursor:pointer;padding:8px 12px;border:1px solid #ccc;border-radius:8px;background:white;color:#172622}audio{width:100%;margin:12px 0}li{margin:10px 0}.selected{border-color:#08796e;background:#d6f5ed}header{position:sticky;top:0;background:#f8faf9;padding:8px 0}small{color:#52655e}input{max-width:100%}</style><script type="module" src="/share-player.js"></script></head><body data-share="{{.ID}}"><h1>{{.Name}}</h1><p>Shared with you through Pocket Audiobooks.</p>{{if .Tracks}}<header><strong id="track-title">Choose a part to listen</strong><audio id="share-audio" controls preload="none"></audio><label>Speed <select id="share-speed"><option>0.75</option><option selected>1</option><option>1.25</option><option>1.5</option><option>1.75</option><option>2</option></select>×</label><p><small id="share-status">Your place is saved in this browser.</small></p></header><ol id="share-tracks">{{range .Tracks}}<li><button type="button" data-src="{{.URL}}" data-id="{{.Item}}:{{.File}}">{{.Title}}</button>{{if $.FileDownload}} <a href="{{.URL}}?download=1">Download audio</a>{{end}}</li>{{end}}</ol><p><a href="{{.Feed}}">Subscribe to the RSS feed</a></p>{{end}}{{if .Download}}{{range .Items}}<section><h2>{{.Title}}</h2><p><a href="/share/{{$.Slug}}/download/{{.ID}}">Download book files</a></p><ul>{{range .Files}}<li><a href="/share/{{$.Slug}}/file/{{.ItemID}}/{{.ID}}">{{.Name}}</a></li>{{end}}</ul></section>{{end}}{{end}}<p><small>Listening here does not change the publisher’s listening history. Links may expire or be revoked.</small></p></body></html>`))

func (s *Server) nativeShareScript(w http.ResponseWriter, r *http.Request) {
	if s.webFS == nil {
		writeErr(w, 503, "web build is unavailable")
		return
	}
	f, err := s.webFS.Open("share-player.js")
	if err != nil {
		writeErr(w, 503, "share player build is unavailable")
		return
	}
	f.Close()
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.FileServerFS(s.webFS).ServeHTTP(w, r)
}
