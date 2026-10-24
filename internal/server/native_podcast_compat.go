package server

import (
	"encoding/json"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/feed"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"net/http"
	"strconv"
	"strings"
)

func episodeCompat(ep map[string]any) nativeObject {
	out := nativeObject{}
	for k, v := range ep {
		out[k] = v
	}
	out["publishedAt"] = nativeMillis(ep["publishedAt"])
	out["addedAt"] = nativeMillis(ep["createdAt"])
	out["updatedAt"] = nativeMillis(ep["updatedAt"])
	out["enclosure"] = nativeObject{"url": ep["enclosureURL"], "type": ep["enclosureType"], "length": strconv.FormatInt(int64(nativeNumber(ep["enclosureSize"])), 10)}
	return out
}
func (s *Server) nativePodcastCompat(w http.ResponseWriter, r *http.Request, uid int64) {
	ctx := r.Context()
	c := s.catalog()
	path := strings.TrimPrefix(r.URL.Path, "/api/podcasts")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	a, err := c.Account(ctx, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	respond := func(v any) { writeJSON(w, 200, v) }
	if path == "/feed" && r.Method == "POST" {
		if !a.Permissions.Update {
			writeErr(w, 403, "podcast editing permission required")
			return
		}
		var in struct {
			URL string `json:"rssFeed"`
		}
		if decode(r, &in) != nil {
			writeErr(w, 400, "invalid feed URL")
			return
		}
		f, e := s.readPodcastFeed(ctx, in.URL)
		if e != nil {
			writeErr(w, 400, e.Error())
			return
		}
		if f.Explicit && !a.Permissions.Explicit {
			writeErr(w, 404, "feed not available")
			return
		}
		eps := []any{}
		for _, ep := range f.Episodes {
			eps = append(eps, episodeCompat(nativeMapJSON(ep)))
		}
		respond(nativeObject{"podcast": nativeObject{"metadata": nativeObject{"title": f.Title, "author": f.Author, "description": f.Description, "imageUrl": f.Image, "language": f.Language, "explicit": f.Explicit}, "episodes": eps}})
		return
	}
	if path == "/opml/parse" && r.Method == "POST" {
		if !a.Permissions.Update {
			writeErr(w, 403, "podcast editing permission required")
			return
		}
		var in struct {
			Text string `json:"opmlText"`
		}
		if decode(r, &in) != nil {
			writeErr(w, 400, "invalid OPML")
			return
		}
		subs, e := feed.OPML([]byte(in.Text))
		if e != nil {
			writeErr(w, 400, e.Error())
			return
		}
		out := []any{}
		for _, v := range subs {
			out = append(out, nativeObject{"title": v.Title, "xmlUrl": v.URL})
		}
		respond(nativeObject{"feeds": out})
		return
	}
	if path == "" && r.Method == "POST" {
		if !a.Permissions.Update || !a.Permissions.Upload {
			writeErr(w, 403, "podcast editing and upload permission required")
			return
		}
		var in struct {
			Library string       `json:"libraryId"`
			Folder  string       `json:"folderId"`
			Media   nativeObject `json:"media"`
		}
		if decode(r, &in) != nil {
			writeErr(w, 400, "invalid podcast")
			return
		}
		url := nativeString(in.Media["feedUrl"])
		if url == "" {
			url = nativeString(in.Media["feedURL"])
		}
		f, e := s.readPodcastFeed(ctx, url)
		if e != nil {
			writeErr(w, 400, e.Error())
			return
		}
		id, e := c.Subscribe(ctx, uid, library.PodcastOptions{LibraryID: in.Library, FolderID: in.Folder, URL: url}, f)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		item, e := s.nativeItem(ctx, uid, id)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		respond(item)
		return
	}
	if len(parts) < 2 {
		writeErr(w, 501, "podcast operation is not implemented")
		return
	}
	id := parts[0]
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if it.MediaType != "podcast" {
		writeErr(w, 404, "podcast not found")
		return
	}
	if len(parts) == 3 && parts[1] == "episode" {
		ep, rev, e := c.EpisodeDocument(ctx, uid, id, parts[2])
		if e != nil {
			s.nativeError(w, e)
			return
		}
		switch r.Method {
		case "GET":
			respond(episodeCompat(ep))
			return
		case "PATCH":
			var fields map[string]any
			if decode(r, &fields) != nil {
				writeErr(w, 400, "invalid episode")
				return
			}
			err = c.EditEpisode(ctx, uid, id, parts[2], library.EpisodeEdit{Expected: rev, Fields: fields})
		case "DELETE":
			_, err = c.Trash(ctx, uid, id, parts[2], r.URL.Query().Get("hard") == "1", rev)
		default:
			writeErr(w, 405, "method not allowed")
			return
		}
		if err != nil {
			s.nativeError(w, err)
			return
		}
		item, e := s.nativeItem(ctx, uid, id)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		respond(item)
		return
	}
	if len(parts) != 2 {
		writeErr(w, 501, "podcast operation is not implemented")
		return
	}
	operation := parts[1]
	if operation == "downloads" && r.Method == "GET" {
		eps, e := c.PodcastEpisodes(ctx, uid, id)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		out := []any{}
		for _, ep := range eps {
			if ep.JobID != "" {
				out = append(out, nativeObject{"id": ep.JobID, "libraryItemId": id, "libraryId": it.LibraryID, "episodeDisplayTitle": ep.Title, "podcastTitle": it.Title, "isFinished": ep.Status == "complete", "failed": ep.Status == "failed", "status": ep.Status, "url": ep.URL})
			}
		}
		respond(nativeObject{"downloads": out})
		return
	}
	if err = c.CanManagePodcast(ctx, uid, id); err != nil {
		s.nativeError(w, err)
		return
	}
	if operation == "clear-queue" && r.Method == "GET" {
		_, err = s.store.DB.ExecContext(ctx, `UPDATE catalog_podcast_jobs SET status='cancelled',error='Download cancelled' WHERE item_id=? AND status IN ('queued','running')`, id)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		respond(nativeObject{"ok": true})
		return
	}
	if (operation == "checknew" || operation == "search-episode") && r.Method == "GET" {
		if err = s.checkPodcast(ctx, uid, id); err != nil {
			s.nativeError(w, err)
			return
		}
		eps, e := c.PodcastEpisodes(ctx, uid, id)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		out := []any{}
		limit := queryInt(r, "limit")
		if limit <= 0 {
			limit = 3
		}
		if limit > 100 {
			limit = 100
		}
		term := strings.ToLower(r.URL.Query().Get("title"))
		for _, ep := range eps {
			if operation == "checknew" {
				if ep.Downloaded || ep.InTrash || len(out) >= limit {
					continue
				}
				if _, e = c.QueueEpisode(ctx, uid, id, ep.ID); e != nil {
					continue
				}
			} else if !strings.Contains(strings.ToLower(ep.Title), term) {
				continue
			}
			out = append(out, episodeCompat(nativeMapJSON(ep.Episode)))
		}
		respond(nativeObject{"episodes": out})
		return
	}
	if operation == "download-episodes" && r.Method == "POST" {
		var requested []nativeObject
		if decode(r, &requested) != nil || len(requested) == 0 || len(requested) > 100 {
			writeErr(w, 400, "select 1 to 100 episodes")
			return
		}
		eps, e := c.PodcastEpisodes(ctx, uid, id)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		results := []any{}
		for _, want := range requested {
			found := ""
			for _, ep := range eps {
				if ep.ID == nativeString(want["id"]) || ep.URL != "" && ep.URL == nativeString(nativeMap(want["enclosure"])["url"]) {
					found = ep.ID
					break
				}
			}
			status := "not_found"
			job := ""
			if found != "" {
				job, e = c.QueueEpisode(ctx, uid, id, found)
				status = "queued"
				if e != nil {
					status = "failed"
				}
			}
			results = append(results, nativeObject{"id": found, "jobId": job, "status": status})
		}
		respond(nativeObject{"results": results})
		return
	}
	writeErr(w, 501, "podcast operation is not implemented")
}

func nativeMapJSON(v any) nativeObject {
	out := nativeObject{}
	_ = json.Unmarshal(nativeJSON(v), &out)
	return out
}
