package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Engine.IO v4 framing with the Socket.IO default namespace. Native sessions
// support direct WebSocket or polling (no transport upgrade is advertised).
// Authentication is Pocket authentication; no upstream key or socket is used.
type nativeSocketHub struct {
	mu      sync.Mutex
	clients map[string]*nativeSocket
}
type nativeSocket struct {
	id      string
	mu      sync.Mutex
	token   string
	uid     int64
	seen    time.Time
	out     chan string
	closed  chan struct{}
	once    sync.Once
	polling sync.Mutex
}

func (c *nativeSocket) stop() { c.once.Do(func() { close(c.closed) }) }
func (c *nativeSocket) push(v string) {
	select {
	case <-c.closed:
		return
	default:
	}
	select {
	case c.out <- v:
	default:
		c.stop()
	}
}
func (h *nativeSocketHub) open() *nativeSocket {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients == nil {
		h.clients = map[string]*nativeSocket{}
	}
	for id, c := range h.clients {
		c.mu.Lock()
		stale := time.Since(c.seen) > 90*time.Second
		c.mu.Unlock()
		if stale {
			c.stop()
			delete(h.clients, id)
		}
	}
	if len(h.clients) >= 512 {
		return nil
	}
	c := &nativeSocket{id: rand.Text(), seen: time.Now(), out: make(chan string, 32), closed: make(chan struct{})}
	h.clients[c.id] = c
	return c
}
func (h *nativeSocketHub) remove(c *nativeSocket) {
	c.stop()
	h.mu.Lock()
	delete(h.clients, c.id)
	h.mu.Unlock()
}
func socketEvent(name string, v any) string { return "42" + string(nativeJSON([]any{name, v})) }
func (s *Server) nativeSocketValid(ctx context.Context, c *nativeSocket) bool {
	c.mu.Lock()
	tok, uid := c.token, c.uid
	c.mu.Unlock()
	if uid == 0 {
		return true
	}
	u, err := s.gatewayUser(ctx, tok)
	if err != nil || u == nil || u.ID != uid {
		c.stop()
		return false
	}
	if _, err = s.catalog().Account(ctx, uid); err != nil {
		c.stop()
		return false
	}
	return true
}
func (s *Server) nativeSocketMessage(ctx context.Context, c *nativeSocket, msg string) {
	c.mu.Lock()
	c.seen = time.Now()
	c.mu.Unlock()
	switch msg {
	case "1", "41":
		c.stop()
		return
	case "3":
		return
	case "40":
		c.push("40" + string(nativeJSON(nativeObject{"sid": c.id})))
		return
	}
	if !strings.HasPrefix(msg, "42") {
		return
	}
	// Optional Socket.IO acknowledgement number precedes the JSON array.
	start := strings.IndexByte(msg, '[')
	if start < 0 {
		return
	}
	var event []json.RawMessage
	if json.Unmarshal([]byte(msg[start:]), &event) != nil || len(event) < 2 {
		return
	}
	var name string
	_ = json.Unmarshal(event[0], &name)
	if name != "auth" {
		if !s.nativeSocketValid(ctx, c) {
			return
		}
		return
	}
	var tok string
	if json.Unmarshal(event[1], &tok) != nil {
		c.push(socketEvent("auth_failed", nativeObject{"message": "Invalid credentials"}))
		return
	}
	u, err := s.gatewayUser(ctx, tok)
	if err != nil || u == nil {
		c.push(socketEvent("auth_failed", nativeObject{"message": "Invalid credentials"}))
		return
	}
	a, err := s.catalog().Account(ctx, u.ID)
	if err != nil {
		c.push(socketEvent("auth_failed", nativeObject{"message": "No library access"}))
		return
	}
	c.mu.Lock()
	// Do not mix queued events from two identities on a single connection.
	if c.uid != 0 && c.uid != u.ID {
		c.mu.Unlock()
		c.stop()
		return
	}
	c.uid, c.token = u.ID, tok
	c.mu.Unlock()
	c.push(socketEvent("init", nativeObject{"userId": a.ID, "username": u.Name}))
}
func (s *Server) nativeSocketProgress(ctx context.Context, uid int64, sid string) {
	session, err := s.catalog().Session(ctx, uid, sid)
	if err != nil {
		return
	}
	s.nativeSocketItemProgress(ctx, uid, session.ItemID, session.EpisodeID, sid, session.Device)
}
func (s *Server) nativeSocketItemProgress(ctx context.Context, uid int64, item, episode, sid, device string) {
	progress, err := s.nativeProgress(ctx, uid)
	if err != nil {
		return
	}
	var current any
	for _, v := range progress {
		p := nativeMap(v)
		if nativeString(p["libraryItemId"]) == item && nativeString(p["episodeId"]) == episode {
			current = p
			break
		}
	}
	if current == nil {
		return
	}
	event := socketEvent("user_item_progress_updated", nativeObject{"id": nativeMap(current)["id"], "sessionId": sid, "deviceDescription": device, "data": current})
	h := s.nativeSockets
	h.mu.Lock()
	clients := []*nativeSocket{}
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.mu.Lock()
		matches := c.uid == uid
		c.mu.Unlock()
		if matches && s.nativeSocketValid(ctx, c) {
			c.push(event)
		}
	}
}
func (s *Server) nativeSocketHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("EIO") != "4" {
		writeErr(w, 400, "Engine.IO v4 required")
		return
	}
	transport := r.URL.Query().Get("transport")
	if transport != "websocket" && transport != "polling" {
		writeErr(w, 400, "invalid transport")
		return
	}
	h := s.nativeSockets
	sid := r.URL.Query().Get("sid")
	var c *nativeSocket
	fresh := sid == ""
	if fresh {
		if r.Method != "GET" {
			writeErr(w, 400, "open a session first")
			return
		}
		c = h.open()
	} else {
		h.mu.Lock()
		c = h.clients[sid]
		h.mu.Unlock()
	}
	if c == nil {
		writeErr(w, 400, "unknown or unavailable session")
		return
	}
	select {
	case <-c.closed:
		h.remove(c)
		writeErr(w, 400, "closed session")
		return
	default:
	}
	handshake := "0" + string(nativeJSON(nativeObject{"sid": c.id, "upgrades": []string{}, "pingInterval": 25000, "pingTimeout": 20000, "maxPayload": 65536}))
	if transport == "websocket" {
		if !fresh {
			writeErr(w, 400, "transport upgrades are not supported")
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			h.remove(c)
			return
		}
		defer conn.CloseNow()
		defer h.remove(c)
		conn.SetReadLimit(65536)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if err = conn.Write(ctx, websocket.MessageText, []byte(handshake)); err != nil {
			return
		}
		go func() {
			defer cancel()
			for {
				_, msg, err := conn.Read(ctx)
				if err != nil {
					return
				}
				s.nativeSocketMessage(ctx, c, string(msg))
			}
		}()
		tick := time.NewTicker(25 * time.Second)
		defer tick.Stop()
		for {
			var msg string
			select {
			case <-ctx.Done():
				return
			case <-c.closed:
				return
			case msg = <-c.out:
			case <-tick.C:
				c.mu.Lock()
				stale := time.Since(c.seen) > 60*time.Second
				c.mu.Unlock()
				if stale {
					return
				}
				msg = "2"
			}
			if !s.nativeSocketValid(ctx, c) {
				return
			}
			writeCtx, done := context.WithTimeout(ctx, 10*time.Second)
			err = conn.Write(writeCtx, websocket.MessageText, []byte(msg))
			done()
			if err != nil {
				return
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	if fresh {
		_, _ = io.WriteString(w, handshake)
		return
	}
	if !s.nativeSocketValid(r.Context(), c) {
		writeErr(w, 401, "authentication expired")
		return
	}
	if r.Method == "POST" {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
		if err != nil {
			writeErr(w, 400, "invalid packet")
			return
		}
		for _, msg := range strings.Split(string(raw), "\x1e") {
			s.nativeSocketMessage(r.Context(), c, msg)
		}
		_, _ = io.WriteString(w, "ok")
		return
	}
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	if !c.polling.TryLock() {
		writeErr(w, 400, "another poll is pending")
		return
	}
	defer c.polling.Unlock()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	var msg string
	select {
	case <-r.Context().Done():
		return
	case <-c.closed:
		writeErr(w, 400, "closed session")
		return
	case msg = <-c.out:
	case <-timer.C:
		msg = "2"
	}
	if !s.nativeSocketValid(r.Context(), c) {
		writeErr(w, 401, "authentication expired")
		return
	}
	_, _ = io.WriteString(w, msg)
}
