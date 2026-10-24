package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

// ---- playing ----
//
// The browser asks POST /play/{book} with the audio types it can decode.
// Pocket Audiobooks opens an Audiobookshelf playback session as the person,
// so Audiobookshelf knows where they are: the files play as they are when the
// browser can (byte ranges, nothing for the server to do), otherwise
// Audiobookshelf transcodes to HLS. The browser reports every 15 seconds and
// on pause; each report moves the person's progress in Audiobookshelf, which
// is what the phone apps pick up ("continue on the phone"), and becomes a
// listening session in their history. Files, playlists and segments go
// through /api/v1/media/…, where the person's key is added on the way: the
// browser never holds an Audiobookshelf token.

// playState is one open session in this app.
type playState struct {
	id       string // Audiobookshelf session id
	uid      int64
	key      string // the Audiobookshelf account it belongs to
	itemID   string
	title    string
	author   string
	cover    string
	duration float64
	position float64
	playing  bool
	client   string
	device   string
	method   string // direct | hls
	updated  time.Time
}

// PlayTrack is one file the browser plays, in book time.
type PlayTrack struct {
	URL      string  `json:"url"`
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
	Mime     string  `json:"mime"`
}

func deviceID(client string) string {
	if client == "" {
		return "pocket-audiobooks"
	}
	return "pocket-audiobooks-" + store.HashToken(client)[:12]
}

var clientRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// openSession starts an Audiobookshelf playback session for a book.
func (s *Server) openSession(ctx context.Context, ac *absConn, u *store.User, itemID, client, device string, mimes []string, transcode bool, episodes ...string) (*abs.Session, *playState, error) {
	episode := ""
	if len(episodes) > 0 {
		episode = episodes[0]
	}
	sess, err := ac.c.Play(ctx, itemID, abs.PlayOptions{EpisodeID: episode,
		DeviceInfo:         abs.DeviceInfo{DeviceID: deviceID(client), ClientName: "Pocket Audiobooks", ClientVersion: Version, DeviceName: trim(device, 60)},
		SupportedMimeTypes: mimes, ForceTranscode: transcode, MediaPlayer: "pocket-audiobooks",
	})
	if err != nil {
		return nil, nil, err
	}
	ps := &playState{id: sess.ID, uid: u.ID, key: ac.key, itemID: itemID, title: sess.DisplayTitle, author: sess.DisplayAuthor, duration: sess.Duration,
		position: sess.CurrentTime, client: client, device: device, method: "direct", updated: time.Now()}
	if sess.PlayMethod == 2 {
		ps.method = "hls"
	}
	s.playMu.Lock()
	// one session per person and browser: Audiobookshelf closed the previous one too
	for id, p := range s.plays {
		if p.uid == u.ID && p.client == client {
			delete(s.plays, id)
		}
	}
	s.plays[sess.ID] = ps
	s.playMu.Unlock()
	return sess, ps, nil
}

func tracksOf(sess *abs.Session) []PlayTrack {
	out := make([]PlayTrack, 0, len(sess.AudioTracks))
	for _, t := range sess.AudioTracks {
		out = append(out, PlayTrack{URL: "/api/v1/media" + t.ContentURL, Start: t.StartOffset, Duration: t.Duration, Mime: t.MimeType})
	}
	return out
}

// handlePlay opens a session and answers with the tracks to play and where to start.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	var in struct {
		Episode   string   `json:"episode_id"`
		Mimes     []string `json:"mime_types"`
		Client    string   `json:"client"`
		Device    string   `json:"device"`
		Transcode bool     `json:"force_transcode"` // the browser failed to decode the file as it is
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !clientRE.MatchString(in.Client) {
		in.Client = ""
	}
	if len(in.Mimes) == 0 {
		in.Mimes = []string{"audio/mpeg", "audio/mp4", "audio/aac", "audio/x-m4a"}
	}
	id := chi.URLParam(r, "id")
	it, err := ac.c.Item(ctx, id)
	if err != nil {
		s.absErr(w, err)
		return
	}
	if !(s.cfg.Backend == "native" && it.MediaType == "podcast" && in.Episode != "") && (it.MediaType != "book" || it.Media.NumAudioFiles == 0 && len(it.Media.AudioFiles) == 0) {
		writeErr(w, http.StatusBadRequest, "this book has no audio")
		return
	}
	sess, ps, err := s.openSession(ctx, ac, u, id, in.Client, in.Device, in.Mimes, in.Transcode, in.Episode)
	if err != nil {
		s.absErr(w, err)
		return
	}
	me, _ := ac.c.Me(ctx)
	b := bookOf(it, progressMap(me), true)
	if in.Episode != "" && s.cfg.Backend == "native" {
		b, err = s.nativeEpisodeBook(ctx, u.ID, id, in.Episode)
		if err != nil {
			s.nativeError(w, err)
			return
		}
	}
	ps.cover = b.Cover
	st, _ := s.store.GetSettings(ctx, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sess.ID, "method": ps.method, "tracks": tracksOf(sess), "current_time": sess.CurrentTime, "duration": sess.Duration,
		"chapters": nz(sess.Chapters), "book": b, "speed": st.Speed, "backend": s.cfg.Backend, "server_time": time.Now().UnixMilli(),
	})
}

// handleSync is the player's report: where it is and how much it played since the last one.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend == "native" {
		s.nativeWebSync(w, r)
		return
	}
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		CurrentTime float64 `json:"current_time"`
		Listened    float64 `json:"listened"`
		Duration    float64 `json:"duration"`
		Playing     bool    `json:"playing"`
		Close       bool    `json:"close"`
		Ended       bool    `json:"ended"`
		ItemID      string  `json:"item_id"`
		Client      string  `json:"client"`
		Device      string  `json:"device"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Listened < 0 || in.Listened > 3600 {
		in.Listened = 0
	}
	sid := chi.URLParam(r, "sid")
	s.playMu.Lock()
	ps := s.plays[sid]
	if ps != nil && ps.uid != u.ID {
		ps = nil
	}
	if ps != nil {
		ps.position, ps.playing, ps.updated = in.CurrentTime, in.Playing && !in.Close, time.Now()
	}
	s.playMu.Unlock()
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	d := abs.SyncData{CurrentTime: in.CurrentTime, TimeListened: in.Listened, Duration: in.Duration}
	var err error
	if in.Close || in.Ended {
		err = ac.c.Close(ctx, sid, &d)
		s.playMu.Lock()
		delete(s.plays, sid)
		s.playMu.Unlock()
	} else {
		err = ac.c.Sync(ctx, sid, d)
	}
	out := map[string]any{"ok": true}
	if abs.IsNotFound(err) && in.ItemID != "" {
		// Audiobookshelf restarted or closed the session (36 h idle): keep the
		// position, and open a new session unless the player is stopping
		err = ac.c.UpdateProgress(ctx, in.ItemID, map[string]any{"currentTime": in.CurrentTime, "duration": in.Duration, "progress": in.CurrentTime / max(in.Duration, 1)})
		if err == nil && !in.Close && !in.Ended && clientRE.MatchString(in.Client) {
			mimes := []string{"audio/mpeg", "audio/mp4", "audio/aac", "audio/x-m4a", "audio/flac", "audio/ogg", "audio/webm"}
			if sess, ps2, err2 := s.openSession(ctx, ac, u, in.ItemID, in.Client, in.Device, mimes, false); err2 == nil {
				ps2.position, ps2.playing = in.CurrentTime, in.Playing
				out["session_id"], out["method"], out["tracks"] = sess.ID, ps2.method, tracksOf(sess)
			}
		}
	}
	if err != nil {
		s.absErr(w, err)
		return
	}
	if in.Ended && in.ItemID != "" {
		if it, err := ac.c.Item(ctx, in.ItemID); err == nil {
			s.recordFinish(ctx, u, it, time.Now().UnixMilli())
		}
	}
	if in.Listened > 0 || in.Close || in.Ended {
		s.historySoon(u.ID)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleNext is what plays after a finished book: up next first, then the next book of its series.
func (s *Server) handleNext(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	after := r.URL.Query().Get("after")
	me, err := ac.c.Me(ctx)
	if err != nil {
		s.absErr(w, err)
		return
	}
	prog := progressMap(me)
	up, _ := s.store.UpNextList(ctx, u.ID)
	for _, x := range up {
		if x.ItemID == after || prog[x.ItemID].IsFinished {
			continue
		}
		if b, err := s.bookBrief(ctx, ac, x.ItemID, prog); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"book": b, "why": "up next"})
			return
		}
	}
	if after != "" {
		if it, err := ac.c.Item(ctx, after); err == nil {
			for _, sr := range it.Media.Metadata.Series {
				p, err := ac.c.Items(ctx, it.LibraryID, abs.ItemsQuery{Filter: abs.Filter("series", sr.ID), Sort: "sequence", Limit: 200})
				if err != nil {
					continue
				}
				bs := books(p.Results, prog)
				sortBySequence(bs)
				cur := seqNum(sr.Sequence)
				for i := range bs {
					if bs[i].ID != after && !bs[i].Finished && seqNum(bs[i].Sequence) > cur {
						writeJSON(w, http.StatusOK, map[string]any{"book": bs[i], "why": "next in " + sr.Name})
						return
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"book": nil})
}

// recordFinish keeps a finished book, takes it off up next, and tells Core and the person.
func (s *Server) recordFinish(ctx context.Context, u *store.User, it *abs.Item, ms int64) {
	m := it.Media.Metadata
	f := &store.Finish{ItemID: it.ID, Title: m.Title, Author: m.AuthorName, Duration: int(it.Media.Duration), FinishedAt: store.MillisUTC(ms)}
	added, err := s.store.AddFinish(ctx, u.ID, f)
	if err != nil || !added {
		return
	}
	_ = s.store.RemoveUpNext(ctx, u.ID, it.ID)
	s.finished(ctx, u, f)
}

// ---- now playing ----

// handleNowPlaying says what the person plays elsewhere: in another browser
// with this app, or in any Audiobookshelf app (from their newest listening
// session), so the web can offer "Continue here".
func (s *Server) handleNowPlaying(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend == "native" {
		s.nativeNowPlaying(w, r)
		return
	}
	ctx := r.Context()
	u := userFrom(ctx)
	mine := r.URL.Query().Get("session")
	client := r.URL.Query().Get("client")
	now := time.Now()
	out := map[string]any{"active": false, "server_time": now.UnixMilli()}
	// this app in another browser
	var best *playState
	s.playMu.Lock()
	for _, p := range s.plays {
		if p.uid != u.ID || p.id == mine || (client != "" && p.client == client) || now.Sub(p.updated) > 2*time.Minute {
			continue
		}
		if best == nil || p.updated.After(best.updated) {
			c := *p
			best = &c
		}
	}
	s.playMu.Unlock()
	if best != nil {
		out = map[string]any{"active": true, "playing": best.playing && now.Sub(best.updated) < 40*time.Second, "item_id": best.itemID, "title": best.title, "author": best.author,
			"cover": best.cover, "position": best.position, "duration": best.duration, "device": firstNonEmpty(best.device, "another browser"), "app": "Pocket Audiobooks",
			"updated_at": best.updated.UnixMilli(), "server_time": now.UnixMilli()}
		writeJSON(w, http.StatusOK, out)
		return
	}
	ac, err := s.absFor(ctx, u)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	p, err := ac.c.ListeningSessions(ctx, 0, 1)
	if err != nil || len(p.Sessions) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ls := p.Sessions[0]
	at := time.UnixMilli(ls.UpdatedAt)
	if ls.MediaType != "book" || ls.ID == mine || now.Sub(at) > 3*time.Minute {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if ls.LibraryID == "" { // some apps report podcast episodes as books without a library
		if it, err := ac.c.Item(ctx, ls.LibraryItemID); err != nil || it.MediaType != "book" {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	s.playMu.Lock()
	_, ours := s.plays[ls.ID]
	s.playMu.Unlock()
	if ours {
		writeJSON(w, http.StatusOK, out)
		return
	}
	// this browser's own earlier session (before a reload) is not "elsewhere"
	if ls.DeviceInfo != nil && client != "" && ls.DeviceInfo.DeviceID == deviceID(client) {
		writeJSON(w, http.StatusOK, out)
		return
	}
	app, dev := "Audiobookshelf", ""
	if ls.DeviceInfo != nil {
		app = firstNonEmpty(ls.DeviceInfo.ClientName, app)
		dev = firstNonEmpty(ls.DeviceInfo.DeviceName, strings.TrimSpace(ls.DeviceInfo.Manufacturer+" "+ls.DeviceInfo.Model), ls.DeviceInfo.OSName)
	}
	if app == "Pocket Audiobooks" && dev == "" {
		dev = "another browser"
	}
	cover := fmt.Sprintf("/api/v1/cover/%s?v=%d", ls.LibraryItemID, ls.StartedAt)
	// a Pocket Audiobooks session this server no longer follows has stopped (its browser went away)
	playing := now.Sub(at) < 75*time.Second && app != "Pocket Audiobooks"
	writeJSON(w, http.StatusOK, map[string]any{"active": true, "playing": playing, "item_id": ls.LibraryItemID, "title": ls.DisplayTitle, "author": ls.DisplayAuthor,
		"cover": cover, "position": ls.CurrentTime, "duration": ls.Duration, "device": firstNonEmpty(dev, app), "app": app, "updated_at": ls.UpdatedAt, "server_time": now.UnixMilli()})
}

// sweepPlays forgets sessions whose browser went quiet (Audiobookshelf closes its own after 36 h).
func (s *Server) sweepPlays(ctx context.Context) {
	s.playMu.Lock()
	var stale []*playState
	for id, p := range s.plays {
		if time.Since(p.updated) > 30*time.Minute {
			stale = append(stale, p)
			delete(s.plays, id)
		}
	}
	s.playMu.Unlock()
	for _, p := range stale {
		u, err := s.store.GetUser(ctx, p.uid)
		if err != nil {
			continue
		}
		if ac, err := s.absFor(ctx, u); err == nil {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = ac.c.Close(cctx, p.id, nil)
			cancel()
		}
	}
}

// ---- streaming ----

var mediaPathRE = regexp.MustCompile(`^/(api/items/[0-9A-Za-z-]+/file/[0-9A-Za-z_-]+|hls/[0-9A-Za-z-]+/[0-9A-Za-z_.-]+)$`)

// handleMedia streams a book's file, or an HLS playlist or segment, with the
// person's key added (byte ranges pass through, so seeking is instant).
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	path := "/" + chi.URLParam(r, "*")
	if !mediaPathRE.MatchString(path) || strings.Contains(path, "..") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	req, err := ac.c.Request(r.Context(), r.Method, path, nil)
	if err != nil {
		s.fail(w, err, "media")
		return
	}
	for _, h := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	res, err := ac.c.DoWith(s.streamHTTP, req)
	if err != nil {
		if r.Context().Err() == nil {
			writeErr(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	defer res.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "private, no-transform")
	w.WriteHeader(res.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	buf := make([]byte, 64<<10)
	_, _ = io.CopyBuffer(flushWriter{w}, res.Body, buf)
}

// flushWriter pushes each chunk out at once (HLS segments as they are made).
type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// clock formats seconds as h:mm:ss or m:ss.
func clock(sec int) string {
	if sec < 0 {
		sec = 0
	}
	h, m, s := sec/3600, sec/60%60, sec%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
