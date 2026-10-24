package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/feed"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
)

func (s *Server) readPodcastFeed(ctx context.Context, url string) (feed.Feed, error) {
	b, _, err := fetch.Get(ctx, s.nativeFetchHTTP, url, s.cfg.UserAgent, "", 10<<20)
	if err != nil {
		return feed.Feed{}, err
	}
	return feed.Parse(b, url)
}
func (s *Server) checkPodcast(ctx context.Context, uid int64, id string) error {
	if err := s.catalog().CanManagePodcast(ctx, uid, id); err != nil {
		return err
	}
	it, err := s.catalog().Item(ctx, uid, id)
	if err != nil {
		return err
	}
	var doc nativeObject
	if err = json.Unmarshal(it.Document, &doc); err != nil {
		return err
	}
	f, err := s.readPodcastFeed(ctx, nativeString(nativeMap(doc["media"])["feedURL"]))
	if err != nil {
		return err
	}
	return s.catalog().UpdateFeed(ctx, uid, id, f)
}
func (s *Server) nativePodcasts(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native podcasts are not active")
		return
	}
	uid := actorID(r.Context())
	ctx := r.Context()
	c := s.catalog()
	tail := strings.Split(strings.Trim(chi.URLParam(r, "*"), "/"), "/")
	path := strings.Join(tail, "/")
	if path == "" && r.Method == "GET" {
		page, err := c.Items(ctx, uid, library.Query{MediaType: "podcast", Limit: 1000})
		if err != nil {
			s.nativeError(w, err)
			return
		}
		libraries, err := s.nativeLibraries(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		libs := []any{}
		for _, v := range libraries {
			if nativeMap(v)["mediaType"] == "podcast" {
				libs = append(libs, v)
			}
		}
		a, err := c.Account(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"items": page.Items, "total": page.Total, "libraries": libs, "can_subscribe": a.Permissions.Upload && a.Permissions.Update && a.Permissions.AllTags})
		return
	}
	if path == "search" && r.Method == "GET" {
		s.nativePodcastSearch(w, r, uid)
		return
	}
	if path == "subscribe" && r.Method == "POST" {
		var in library.PodcastOptions
		if err := decode(r, &in); err != nil {
			writeErr(w, 400, "invalid subscription")
			return
		}
		a, err := c.Account(ctx, uid)
		if err != nil || !a.Permissions.Upload || !a.Permissions.Update {
			writeErr(w, 403, "podcast upload and editing permission required")
			return
		}
		f, err := s.readPodcastFeed(ctx, in.URL)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		id, err := c.Subscribe(ctx, uid, in, f)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 201, nativeObject{"id": id})
		return
	}
	if path == "opml" {
		if r.Method == "POST" {
			var in struct {
				OPML string `json:"opml"`
			}
			if err := decode(r, &in); err != nil {
				writeErr(w, 400, "invalid OPML")
				return
			}
			subs, err := feed.OPML([]byte(in.OPML))
			if err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			writeJSON(w, 200, nativeObject{"subscriptions": subs})
			return
		}
		page, err := c.Items(ctx, uid, library.Query{MediaType: "podcast", Limit: 10000})
		if err != nil {
			s.nativeError(w, err)
			return
		}
		subs := []feed.Subscription{}
		for _, it := range page.Items {
			item, err := c.Item(ctx, uid, it.ID)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			var doc nativeObject
			json.Unmarshal(item.Document, &doc)
			u := nativeString(nativeMap(doc["media"])["feedURL"])
			if u != "" {
				subs = append(subs, feed.Subscription{Title: item.Title, URL: u})
			}
		}
		b, err := feed.ExportOPML(subs)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="podcasts.opml"`)
		w.Write(b)
		return
	}
	if len(tail) == 2 && tail[0] == "jobs" && r.Method == "DELETE" {
		var item string
		if err := s.store.DB.QueryRowContext(ctx, `SELECT item_id FROM catalog_podcast_jobs WHERE id=?`, tail[1]).Scan(&item); err != nil {
			writeErr(w, 404, "job not found")
			return
		}
		if err := c.CanManagePodcast(ctx, uid, item); err != nil {
			s.nativeError(w, err)
			return
		}
		_, err := s.store.DB.ExecContext(ctx, `UPDATE catalog_podcast_jobs SET status='cancelled',updated_at=? WHERE id=? AND status IN ('queued','running')`, time.Now().UnixMilli(), tail[1])
		if err != nil {
			s.nativeError(w, err)
			return
		}
		s.nativePodcastMu.Lock()
		if cancel := s.nativePodcastCancel[tail[1]]; cancel != nil {
			cancel()
		}
		s.nativePodcastMu.Unlock()
		writeJSON(w, 200, nativeObject{"ok": true})
		return
	}
	id := tail[0]
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if it.MediaType != "podcast" {
		writeErr(w, 404, "podcast not found")
		return
	}
	if len(tail) == 1 && r.Method == "GET" {
		eps, err := c.PodcastEpisodes(ctx, uid, id)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		config, err := c.PodcastSubscription(ctx, uid, id)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"item": it, "episodes": eps, "settings": config.PodcastSettings, "last_check": config.LastCheck, "next_check": config.NextCheck, "error": config.Error, "has_feed": nativeString(nativeMap(nativeMapJSON(json.RawMessage(it.Document))["media"])["feedURL"]) != "", "can_manage": c.CanManagePodcast(ctx, uid, id) == nil, "can_delete": c.CanDelete(ctx, uid, id) == nil})
		return
	}
	if len(tail) == 2 && tail[1] == "check" && r.Method == "POST" {
		if err = s.checkPodcast(ctx, uid, id); err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"ok": true})
		return
	}
	if len(tail) == 2 && tail[1] == "settings" && r.Method == "PUT" {
		var in library.PodcastSettings
		if err = decode(r, &in); err != nil {
			writeErr(w, 400, "invalid podcast settings")
			return
		}
		if err = c.SetPodcastSettings(ctx, uid, id, in); err != nil {
			s.nativeError(w, err)
			return
		}
		if err = c.RetainPodcast(ctx, id); err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"ok": true})
		return
	}
	if len(tail) == 4 && tail[1] == "episodes" && tail[3] == "editor" {
		if r.Method == "GET" {
			ep, rev, e := c.EpisodeDocument(ctx, uid, id, tail[2])
			if e != nil {
				s.nativeError(w, e)
				return
			}
			writeJSON(w, 200, nativeObject{"episode": ep, "revision": rev})
			return
		}
		if r.Method == "PUT" {
			var in library.EpisodeEdit
			if decode(r, &in) != nil {
				writeErr(w, 400, "invalid episode edit")
				return
			}
			if e := c.EditEpisode(ctx, uid, id, tail[2], in); e != nil {
				s.nativeError(w, e)
				return
			}
			writeJSON(w, 200, nativeObject{"ok": true})
			return
		}
	}
	if len(tail) == 3 && tail[1] == "episodes" && r.Method == "GET" {
		b, err := s.nativeEpisodeBook(ctx, uid, id, tail[2])
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"book": b})
		return
	}
	if len(tail) == 4 && tail[1] == "episodes" && tail[3] == "download" && r.Method == "POST" {
		job, err := c.QueueEpisode(ctx, uid, id, tail[2])
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 202, nativeObject{"job_id": job})
		return
	}
	writeErr(w, 404, "podcast operation not found")
}

func (s *Server) nativePodcastLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.runPodcastJob(ctx) {
			continue
		}
		s.checkOnePodcast(ctx)
	}
}

func (s *Server) checkOnePodcast(ctx context.Context) bool {
	// Check one due subscription per tick so one slow feed cannot monopolize
	// the download queue. Automatic subscriptions always run as their owner.
	var id string
	var uid, last int64
	var maxNew int
	err := s.store.DB.QueryRowContext(ctx, `SELECT item_id,actor_id,download_watermark,max_new FROM catalog_podcast_subscriptions WHERE auto_download=1 AND actor_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM catalog_library_state s JOIN catalog_items i ON i.library_id=s.library_id WHERE i.id=catalog_podcast_subscriptions.item_id AND s.archived=1) AND NOT EXISTS(SELECT 1 FROM catalog_removed_items r WHERE r.item_id=catalog_podcast_subscriptions.item_id) AND next_check<=? ORDER BY next_check LIMIT 1`, time.Now().UnixMilli()).Scan(&id, &uid, &last, &maxNew)
	if err != nil {
		return false
	}
	err = s.checkPodcast(ctx, uid, id)
	if err != nil {
		_ = s.catalog().PodcastCheckFailed(ctx, id)
		return true
	}
	// Settings may have changed while the network request was in flight.
	current, err := s.catalog().PodcastSubscription(ctx, uid, id)
	if err != nil || !current.Auto || current.Actor == nil || *current.Actor != uid {
		return true
	}
	maxNew = current.MaxNew
	episodes, err := s.catalog().PodcastEpisodes(ctx, uid, id)
	if err != nil {
		return true
	}
	if e := s.catalog().RetainPodcast(ctx, id); e != nil {
		s.log.Warn("podcast retention", "error", e)
	}
	added := 0
	queuedAll := true
	for _, ep := range episodes {
		if ep.Downloaded || ep.InTrash || ep.PublishedAt <= last || added >= maxNew {
			continue
		}
		if ep.Status == "queued" || ep.Status == "running" {
			added++
			continue
		}
		if _, err = s.catalog().QueueEpisode(ctx, uid, id, ep.ID); err == nil {
			added++
		} else {
			queuedAll = false
		}
	}
	// Advance only after durable queue inserts. A restart or enqueue failure
	// retries the same window; active-job uniqueness makes retries safe.
	if queuedAll {
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE catalog_podcast_subscriptions SET download_watermark=max(download_watermark,?) WHERE item_id=? AND actor_id=? AND auto_download=1`, current.LastCheck, id, uid)
	}
	return true
}

func (s *Server) runPodcastJob(ctx context.Context) bool {
	var job, item, episode string
	var uid int64
	err := s.store.DB.QueryRowContext(ctx, `SELECT id,item_id,episode_id,actor_id FROM catalog_podcast_jobs WHERE status='queued' ORDER BY created_at LIMIT 1`).Scan(&job, &item, &episode, &uid)
	if err != nil {
		return false
	}
	result, err := s.store.DB.ExecContext(ctx, `UPDATE catalog_podcast_jobs SET status='running',updated_at=? WHERE id=? AND status='queued'`, time.Now().UnixMilli(), job)
	if err != nil {
		return false
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return false
	}
	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	s.nativePodcastMu.Lock()
	s.nativePodcastCancel[job] = cancel
	s.nativePodcastMu.Unlock()
	err = s.downloadPodcastEpisode(jobCtx, uid, item, episode, job)
	cancel()
	s.nativePodcastMu.Lock()
	delete(s.nativePodcastCancel, job)
	s.nativePodcastMu.Unlock()
	state, message := "complete", ""
	if err != nil {
		state = "failed"
		message = "Download failed; retry this episode"
		if errors.Is(err, context.Canceled) {
			state = "cancelled"
			message = "Download cancelled"
		}
		s.log.Warn("native podcast download failed", "job", job, "error", err)
	}
	done, cancelDone := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDone()
	_, _ = s.store.DB.ExecContext(done, `UPDATE catalog_podcast_jobs SET status=?,updated_at=?,error=? WHERE id=? AND status='running'`, state, time.Now().UnixMilli(), message, job)
	if err == nil {
		if e := s.catalog().RetainPodcast(done, item); e != nil {
			s.log.Warn("podcast retention", "error", e)
		}
	}
	return true
}
func (s *Server) downloadPodcastEpisode(ctx context.Context, uid int64, item, episode, job string) error {
	if err := s.catalog().CanManagePodcast(ctx, uid, item); err != nil {
		return err
	}
	doc, _, err := s.catalog().EpisodeDocument(ctx, uid, item, episode)
	if err != nil {
		return err
	}
	u, err := fetch.URL(nativeString(doc["enclosureURL"]))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	// Clone only the client timeout; share its vetted transport and redirects.
	client := *s.nativeFetchHTTP
	client.Timeout = 30 * time.Minute
	response, err := client.Do(req)
	if err != nil {
		return errors.New("episode request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("episode server returned an error")
	}
	max := s.audiobookUploadLimit()
	if response.ContentLength > max {
		return errors.New("episode exceeds configured media size limit")
	}
	temp, err := os.CreateTemp(s.cfg.DataDir, ".podcast-download-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	n, copyErr := io.Copy(temp, io.LimitReader(response.Body, max+1))
	closeErr := temp.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if n > max {
		return errors.New("episode exceeds configured media size limit")
	}
	var status string
	if err = s.store.DB.QueryRowContext(ctx, `SELECT status FROM catalog_podcast_jobs WHERE id=?`, job).Scan(&status); err != nil {
		return err
	}
	if status != "running" {
		return context.Canceled
	}
	return s.catalog().PublishEpisode(ctx, uid, item, episode, temp.Name(), s.cfg.FFprobe, job)
}

func (s *Server) nativeEpisodeBook(ctx context.Context, uid int64, id, episode string) (Book, error) {
	it, err := s.catalog().Item(ctx, uid, id)
	if err != nil {
		return Book{}, err
	}
	tracks, chapters, duration, err := s.catalog().Timeline(ctx, uid, id, episode)
	if err != nil {
		return Book{}, err
	}
	var doc nativeObject
	if err = json.Unmarshal(it.Document, &doc); err != nil {
		return Book{}, err
	}
	b := Book{ID: id, EpisodeID: episode, LibraryID: it.LibraryID, Author: it.Title, Title: it.Title, Duration: duration, Genres: []string{}, AddedAt: it.AddedAt, Chapters: len(chapters), Files: len(tracks), ChapterList: []abs.Chapter{}}
	if nativeString(nativeMap(doc["media"])["coverPath"]) != "" {
		b.Cover = "/api/v1/cover/" + id
	}
	for _, v := range nativeArray(doc["episodes"]) {
		e := nativeMap(v)
		if nativeString(e["id"]) == episode {
			b.Title = nativeString(e["title"])
			b.Description = nativeString(e["description"])
			break
		}
	}
	for _, ch := range chapters {
		b.ChapterList = append(b.ChapterList, abs.Chapter{ID: ch.ID, Start: ch.Start, End: ch.End, Title: ch.Title})
	}
	progress, err := s.nativeProgress(ctx, uid)
	if err != nil {
		return b, err
	}
	for _, v := range progress {
		p := nativeMap(v)
		if nativeString(p["libraryItemId"]) == id && nativeString(p["episodeId"]) == episode {
			b.CurrentTime = nativeNumber(p["currentTime"])
			b.Progress = nativeNumber(p["progress"])
			b.Finished = nativeBool(p["isFinished"])
			b.LastPlayed = int64(nativeNumber(p["lastUpdate"]))
		}
	}
	return b, nil
}
