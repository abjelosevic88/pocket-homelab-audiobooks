package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Live updates. The web app listens on GET /api/v1/events (Server-Sent Events:
// plain HTTP that passes Caddy's flush_interval -1 untouched); Audiobookshelf
// apps on the Socket.IO endpoint (native_socket.go). Both receive only events
// of their own person, and item events only for items that person may read.
//
// Web events (JSON in each "data:" line, "type" says which):
//
//	progress  {item_id, episode_id, session_id, device, progress}
//	session   {session_id, closed}         a session of this person opened/closed
//	items     {updated: [ids], added: [ids]}
//	scan_complete {library_id, added, updated, missing}
type realtimeHub struct {
	mu       sync.Mutex
	subs     map[*realtimeSub]struct{}
	watching bool
}

type realtimeSub struct {
	uid  int64
	out  chan []byte
	once sync.Once
	gone chan struct{}
}

func (c *realtimeSub) stop() { c.once.Do(func() { close(c.gone) }) }

const realtimeMaxPerUser = 16

var realtimeWatchEvery durationVar

func init() { realtimeWatchEvery.Set(2 * time.Second) }

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u == nil {
		writeErr(w, 401, "sign in first")
		return
	}
	rc := http.NewResponseController(w)
	sub := &realtimeSub{uid: u.ID, out: make(chan []byte, 64), gone: make(chan struct{})}
	h := &s.realtime
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[*realtimeSub]struct{}{}
	}
	mine := 0
	for other := range h.subs {
		if other.uid == u.ID {
			mine++
		}
	}
	if mine >= realtimeMaxPerUser || len(h.subs) >= 2048 {
		h.mu.Unlock()
		writeErr(w, 429, "too many live connections")
		return
	}
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, sub)
		h.mu.Unlock()
	}()
	s.realtimeKick()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	write := func(b string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := fmt.Fprint(w, b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write("retry: 3000\n" + "data: " + string(nativeJSON(nativeObject{"type": "hello", "server_time": time.Now().UnixMilli()})) + "\n\n") {
		return
	}
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.gone:
			// Too slow to keep up: the client reconnects and reloads what it shows.
			return
		case msg := <-sub.out:
			if !write("data: " + string(msg) + "\n\n") {
				return
			}
		case <-tick.C:
			// A revoked sign-in ends the stream, not only the next request.
			if again, _, err := s.resolveAuth(r); err != nil || again == nil || again.ID != u.ID {
				write("event: unauthorized\ndata: {}\n\n")
				return
			}
			if !write(": ping\n\n") {
				return
			}
		}
	}
}

// realtimeSend queues an event for the person's web listeners.
func (s *Server) realtimeSend(uid int64, v nativeObject) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h := &s.realtime
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if sub.uid != uid {
			continue
		}
		select {
		case sub.out <- b:
		default:
			sub.stop()
		}
	}
}

// realtimeUsers lists people with a live web or app connection.
func (s *Server) realtimeUsers() map[int64]bool {
	out := map[int64]bool{}
	h := &s.realtime
	h.mu.Lock()
	for sub := range h.subs {
		out[sub.uid] = true
	}
	h.mu.Unlock()
	sh := s.nativeSockets
	sh.mu.Lock()
	for _, c := range sh.clients {
		c.mu.Lock()
		if c.uid != 0 {
			out[c.uid] = true
		}
		c.mu.Unlock()
	}
	sh.mu.Unlock()
	return out
}

// socketEmit sends a Socket.IO event to the person's authenticated app sockets.
func (s *Server) socketEmit(ctx context.Context, uid int64, name string, v any) {
	event := socketEvent(name, v)
	h := s.nativeSockets
	h.mu.Lock()
	clients := []*nativeSocket{}
	for _, c := range h.clients {
		c.mu.Lock()
		if c.uid == uid {
			clients = append(clients, c)
		}
		c.mu.Unlock()
	}
	h.mu.Unlock()
	for _, c := range clients {
		if s.nativeSocketValid(ctx, c) {
			c.push(event)
		}
	}
}

func (s *Server) socketAnyone() bool {
	h := s.nativeSockets
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients {
		c.mu.Lock()
		authed := c.uid != 0
		c.mu.Unlock()
		if authed {
			return true
		}
	}
	return false
}

func (s *Server) socketListening(uid int64) bool {
	h := s.nativeSockets
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients {
		c.mu.Lock()
		match := c.uid == uid
		c.mu.Unlock()
		if match {
			return true
		}
	}
	return false
}

// nativeSessionOpened/Closed tell the person's other devices at once, so "Playing
// on …" appears and disappears without waiting for a poll.
func (s *Server) nativeSessionOpened(uid int64, sid string) {
	s.realtimeSend(uid, nativeObject{"type": "session", "session_id": sid, "closed": false})
}
func (s *Server) nativeSessionClosed(ctx context.Context, uid int64, sid string) {
	s.hlsCloseSession(sid)
	s.socketEmit(ctx, uid, "user_session_closed", sid)
	s.realtimeSend(uid, nativeObject{"type": "session", "session_id": sid, "closed": true})
}

func (s *Server) nativeScanComplete(lib, id string, added, updated, missing int) {
	if s.cfg.Backend != "native" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for uid := range s.realtimeUsers() {
		libs, err := s.nativeLibraries(ctx, uid)
		if err != nil {
			continue
		}
		name := ""
		for _, v := range libs {
			if l := nativeMap(v); nativeString(l["id"]) == lib {
				name = nativeString(l["name"])
				break
			}
		}
		if name == "" {
			continue
		}
		s.socketEmit(ctx, uid, "scan_complete", nativeObject{"id": id, "libraryId": lib, "type": "scan", "name": name, "results": nativeObject{"added": added, "updated": updated, "missing": missing}})
		s.realtimeSend(uid, nativeObject{"type": "scan_complete", "library_id": lib, "added": added, "updated": updated, "missing": missing})
	}
}

// realtimeKick starts the item watcher while anyone listens. Every catalog
// change (edits, matches, scans, tools, track and cover changes, apps' own
// edits) bumps catalog_items.updated_at, so one indexed query covers them all.
func (s *Server) realtimeKick() {
	if s.cfg.Backend != "native" {
		return
	}
	h := &s.realtime
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watching {
		return
	}
	h.watching = true
	go s.realtimeWatch()
}

func (s *Server) realtimeWatch() {
	ctx := context.Background()
	var mark int64
	var markID string
	_ = s.store.DB.QueryRowContext(ctx, `SELECT updated_at,id FROM catalog_items ORDER BY updated_at DESC,id DESC LIMIT 1`).Scan(&mark, &markID)
	since := time.Now().UnixMilli()
	for {
		time.Sleep(realtimeWatchEvery.Get())
		users := s.realtimeUsers()
		if len(users) == 0 {
			// Stop unless someone arrived meanwhile; arrivals kick under h.mu.
			h := &s.realtime
			h.mu.Lock()
			if len(h.subs) == 0 && !s.socketAnyone() {
				h.watching = false
				h.mu.Unlock()
				return
			}
			h.mu.Unlock()
			continue
		}
		type change struct {
			id    string
			added bool
		}
		changes := []change{}
		rows, err := s.store.DB.QueryContext(ctx, `SELECT id,added_at,updated_at FROM catalog_items WHERE updated_at>? OR (updated_at=? AND id>?) ORDER BY updated_at,id LIMIT 200`, mark, mark, markID)
		if err != nil {
			continue
		}
		for rows.Next() {
			var id string
			var added, updated int64
			if rows.Scan(&id, &added, &updated) != nil {
				continue
			}
			changes = append(changes, change{id, added >= since-2000})
			mark, markID = updated, id
		}
		rows.Close()
		since = time.Now().UnixMilli()
		if len(changes) == 0 {
			continue
		}
		for uid := range users {
			var updatedIDs, addedIDs []string
			for _, ch := range changes {
				if _, err := s.catalog().Item(ctx, uid, ch.id); err != nil {
					continue
				}
				if ch.added {
					addedIDs = append(addedIDs, ch.id)
				} else {
					updatedIDs = append(updatedIDs, ch.id)
				}
			}
			if len(updatedIDs)+len(addedIDs) == 0 {
				continue
			}
			s.realtimeSend(uid, nativeObject{"type": "items", "updated": nz(updatedIDs), "added": nz(addedIDs)})
			if s.socketListening(uid) {
				s.socketItems(ctx, uid, "item_updated", "items_updated", updatedIDs)
				s.socketItems(ctx, uid, "item_added", "items_added", addedIDs)
			}
		}
	}
}

// socketItems sends Audiobookshelf's expanded library items, one event for a
// single item and batches of 25 otherwise.
func (s *Server) socketItems(ctx context.Context, uid int64, one, many string, ids []string) {
	items := []any{}
	for _, id := range ids {
		if it, err := s.nativeItem(ctx, uid, id); err == nil {
			items = append(items, it)
		}
	}
	if len(items) == 1 {
		s.socketEmit(ctx, uid, one, items[0])
		return
	}
	for len(items) > 0 {
		n := min(25, len(items))
		s.socketEmit(ctx, uid, many, items[:n])
		items = items[n:]
	}
}

func (s *Server) realtimeListening(uid int64) bool {
	h := &s.realtime
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if sub.uid == uid {
			return true
		}
	}
	return false
}
