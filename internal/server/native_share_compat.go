package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
)

type shareCapture struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (w *shareCapture) Header() http.Header { return w.header }
func (w *shareCapture) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}
func (w *shareCapture) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	return w.body.Write(b)
}
func (s *Server) nativeShareCompat(w http.ResponseWriter, r *http.Request, uid int64) {
	u, err := s.store.GetUser(r.Context(), uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	method, id := "", ""
	in := nativeObject{}
	mediaID, mediaType := "", ""
	if r.Method != "GET" && r.Method != "DELETE" {
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&in); e != nil && e != io.EOF {
			writeErr(w, 400, "invalid share request")
			return
		}
	}
	translated := nativeObject{}
	switch {
	case r.URL.Path == "/api/feeds" && r.Method == "GET":
		method = "GET"
	case len(parts) == 5 && parts[1] == "feeds" && parts[4] == "open" && r.Method == "POST":
		if parts[2] != "item" && parts[2] != "collection" && parts[2] != "series" {
			s.nativeError(w, library.ErrInvalid)
			return
		}
		method = "POST"
		translated = nativeObject{"kind": parts[2], "target_id": parts[3], "slug": in["slug"], "name": in["title"], "password": in["password"], "expires_at": in["expiresAt"]}
	case len(parts) == 4 && parts[1] == "feeds" && parts[3] == "close" && r.Method == "POST":
		method = "DELETE"
		id = parts[2]
	case r.URL.Path == "/api/share/mediaitem" && r.Method == "POST":
		mediaID = nativeString(in["mediaItemId"])
		mediaType = nativeString(in["mediaItemType"])
		item := ""
		episode := ""
		if mediaType == "book" {
			err = s.store.DB.QueryRowContext(r.Context(), `SELECT id FROM catalog_items WHERE media_id=? AND media_type='book'`, mediaID).Scan(&item)
		} else if mediaType == "podcastEpisode" {
			episode = mediaID
			err = s.store.DB.QueryRowContext(r.Context(), `SELECT i.id FROM catalog_items i WHERE EXISTS (SELECT 1 FROM json_each(i.document,'$.episodes') e WHERE json_extract(e.value,'$.id')=?)`, mediaID).Scan(&item)
		} else {
			s.nativeError(w, library.ErrInvalid)
			return
		}
		if err != nil {
			writeErr(w, 404, "media item not found")
			return
		}
		method = "POST"
		translated = nativeObject{"kind": "item", "target_id": item, "episode_id": episode, "slug": in["slug"], "password": in["password"], "downloadable": in["isDownloadable"], "expires_at": in["expiresAt"]}
	case len(parts) == 4 && parts[1] == "share" && parts[2] == "mediaitem" && r.Method == "DELETE":
		method = "DELETE"
		id = parts[3]
	default:
		writeErr(w, 501, "share operation is not implemented")
		return
	}
	clone := r.Clone(context.WithValue(r.Context(), userKey, u))
	clone.Method = method
	route := chi.NewRouteContext()
	route.URLParams.Add("share", id)
	clone = clone.WithContext(context.WithValue(clone.Context(), chi.RouteCtxKey, route))
	clone.Body = io.NopCloser(bytes.NewReader(nativeJSON(translated)))
	captured := &shareCapture{header: http.Header{}}
	s.nativeShares(captured, clone)
	if captured.code >= 400 {
		for k, vs := range captured.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(captured.code)
		w.Write(captured.body.Bytes())
		return
	}
	if method == "DELETE" {
		w.WriteHeader(200)
		return
	}
	if method == "GET" {
		var shares []nativeShare
		if err = json.Unmarshal(captured.body.Bytes(), &shares); err != nil {
			s.nativeError(w, err)
			return
		}
		feeds := []nativeObject{}
		for _, v := range shares {
			v.UserID = uid
			detail, e := s.nativeFeedDetail(r, v)
			if e != nil {
				detail = s.nativeFeedWire(v)
				detail["episodes"] = []any{}
				detail["unavailable"] = true
			}
			feeds = append(feeds, detail)
		}
		writeJSON(w, 200, nativeObject{"feeds": feeds, "minified": feeds})
		return
	}
	var v nativeShare
	if err = json.Unmarshal(captured.body.Bytes(), &v); err != nil {
		s.nativeError(w, err)
		return
	}
	if mediaID != "" {
		writeJSON(w, 201, nativeObject{"id": v.ID, "mediaItemId": mediaID, "mediaItemType": mediaType, "slug": v.Slug, "expiresAt": v.ExpiresAt, "createdAt": v.CreatedAt, "updatedAt": v.CreatedAt, "isDownloadable": v.Download})
		return
	}
	v.UserID = uid
	detail, err := s.nativeFeedDetail(r, v)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, nativeObject{"feed": detail})
}
func (s *Server) nativeFeedWire(v nativeShare) nativeObject {
	v = s.shareWire(v)
	kind := v.Kind
	if kind == "item" {
		kind = "libraryItem"
	}
	return nativeObject{"id": v.ID, "slug": v.Slug, "entityType": kind, "entityId": v.TargetID, "feedUrl": v.FeedURL, "serverAddress": strings.TrimRight(s.cfg.BaseURL, "/"), "createdAt": v.CreatedAt, "updatedAt": v.CreatedAt, "episodes": []any{}, "meta": nativeObject{"title": v.Name, "description": v.Name, "preventIndexing": true}}
}

func (s *Server) nativeFeedDetail(r *http.Request, v nativeShare) (nativeObject, error) {
	out := s.nativeFeedWire(v)
	items, err := s.shareItems(r.Context(), v)
	if err != nil {
		return nil, err
	}
	tracks, err := s.shareTracks(r, v, items)
	if err != nil {
		return nil, err
	}
	episodes := []any{}
	for _, tr := range tracks {
		episodes = append(episodes, nativeObject{"id": tr.Item + ":" + tr.File, "title": tr.Title, "description": tr.Title, "pubDate": tr.Published, "enclosure": nativeObject{"url": tr.URL, "size": tr.Size, "type": tr.MIME}, "duration": tr.Duration, "libraryItemId": tr.Item})
	}
	out["episodes"] = episodes
	return out, nil
}
