package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// ---- Audiobookshelf apps: sign-in, live updates, /audiobookshelf ----
//
// gateway.go answers the Audiobookshelf API (/api, /hls) for apps that hold a
// Pocket token (Pocket Homelab). This adds what third-party Audiobookshelf apps
// (the Audiobookshelf app, AudioBooth, Plappa, ShelfPlayer…) need on top:
//
//   - password sign-in: POST /login with the person's email and an app
//     password (a Pocket Audiobooks token, pab_, from Settings → Audiobookshelf
//     → Phone & car apps; or their Pocket Audiobooks password, which gets the
//     app a token of its own), POST /api/authorize, POST /auth/refresh, logout;
//     /status says "local" sign-in;
//   - live updates: socket.io, where the token in the app's "auth" event is
//     swapped for the person's Audiobookshelf one, both ways;
//   - the same routes under /audiobookshelf, Audiobookshelf's usual sub-path.
//
// The person's Audiobookshelf key never reaches the app.

// gwSocketAuth finds a socket.io "auth" event carrying the app's token.
var gwSocketAuth = regexp.MustCompile(`\["auth","([^"]+)"\]`)

// gwSubpaths are the routes /audiobookshelf/… passes on (never the web app's /api/v1).
var gwSubpaths = regexp.MustCompile(`^/(status|ping|healthcheck|login|logout|auth/refresh|socket\.io(/.*)?|hls/.+|api/(?:[^v].*|v[^1].*|v1[^/].*))$`)

// gwPair is an app token and the Audiobookshelf token it stands for.
type gwPair struct {
	client, abs, base string
	at                time.Time
}

var (
	gwSocketsMu sync.Mutex
	gwSockets   = map[string]gwPair{} // socket.io sid → tokens (polling)
)

func gwError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
}

// handleABSPrefixed answers /audiobookshelf/… like the same route at the root.
func (s *Server) handleABSPrefixed(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/audiobookshelf")
	if p == "" || p == "/" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if !gwSubpaths.MatchString(p) || s.mux == nil {
		http.NotFound(w, r)
		return
	}
	r2 := r.Clone(context.WithValue(r.Context(), chi.RouteCtxKey, nil))
	r2.URL.Path, r2.URL.RawPath = p, ""
	s.mux.ServeHTTP(w, r2)
}

// gwLogin signs an app in: the email (or name) and an app password (pab_…) or
// the Pocket Audiobooks password, which gets the app its own token.
func (s *Server) gwLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		_ = r.ParseForm()
		in.Username, in.Password = r.PostFormValue("username"), r.PostFormValue("password")
	} else if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		gwError(w, http.StatusBadRequest, "Bad request")
		return
	}
	in.Username, in.Password = strings.TrimSpace(in.Username), strings.TrimSpace(in.Password)
	fail := func() {
		time.Sleep(600 * time.Millisecond) // slow down guessing
		gwError(w, http.StatusUnauthorized, "Unauthorized")
	}
	var u *store.User
	tok := ""
	if strings.HasPrefix(in.Password, "pab_") || (s.core.Configured() && core.IsCoreToken(in.Password)) {
		var err error
		if u, err = s.gatewayUser(ctx, in.Password); err != nil || u == nil {
			fail()
			return
		}
		tok = in.Password
	} else {
		cand, err := s.store.GetUserByEmail(ctx, strings.ToLower(in.Username))
		if err != nil || cand.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(cand.PasswordHash), []byte(in.Password)) != nil {
			fail()
			return
		}
		u = cand
		plain, _, err := s.store.CreateAPIToken(ctx, u.ID, "Audiobookshelf app (signed in with password)")
		if err != nil {
			s.fail(w, err, "app token")
			return
		}
		tok = plain
	}
	s.gwAuthorize(w, r, u, tok)
}

// gwRefresh answers an app renewing its sign-in: its token stays valid, so it gets it back.
func (s *Server) gwRefresh(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(r.Header.Get("x-refresh-token"))
	if tok == "" {
		tok = gatewayToken(r)
	}
	u, err := s.gatewayUser(r.Context(), tok)
	if err != nil || u == nil {
		gwError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	s.gwAuthorize(w, r, u, tok)
}

// gwAuthorizeRoute is POST /api/authorize: an app checking its sign-in.
func (s *Server) gwAuthorizeRoute(w http.ResponseWriter, r *http.Request) {
	tok := gatewayToken(r)
	u, err := s.gatewayUser(r.Context(), tok)
	if err != nil || u == nil {
		gwError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	s.gwAuthorize(w, r, u, tok)
}

// gwLogout answers an app signing out (its app password stays until revoked in Settings).
func (s *Server) gwLogout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

// gwAuthorize answers like Audiobookshelf's login: the person's own account, with the app's token.
func (s *Server) gwAuthorize(w http.ResponseWriter, r *http.Request, u *store.User, tok string) {
	ac, err := s.absFor(r.Context(), u)
	if err != nil {
		gwError(w, http.StatusServiceUnavailable, "Pocket Audiobooks: "+err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, ac.c.Base+"/api/authorize", nil)
	if err != nil {
		s.fail(w, err, "authorize")
		return
	}
	req.Header.Set("Authorization", "Bearer "+ac.c.Token())
	res, err := ac.c.Do(req)
	if err != nil {
		gwError(w, http.StatusBadGateway, "Audiobookshelf unreachable")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		gwError(w, http.StatusServiceUnavailable, "Audiobookshelf did not accept your connection: check Settings → Audiobookshelf in Pocket Audiobooks")
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		gwError(w, http.StatusBadGateway, "Audiobookshelf answered oddly")
		return
	}
	if user, ok := payload["user"].(map[string]any); ok {
		user["token"], user["accessToken"], user["refreshToken"] = tok, tok, tok
	}
	if ss, ok := payload["serverSettings"].(map[string]any); ok {
		ss["authActiveAuthMethods"] = []string{"local"}
		ss["authOpenIDAutoLaunch"] = false
	}
	b, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(swapToken(b, ac.c.Token(), tok))
}

// swapToken replaces the person's Audiobookshelf token in an answer with the one the app used.
func swapToken(b []byte, abs, client string) []byte {
	if abs == "" || abs == client {
		return b
	}
	b = bytes.ReplaceAll(b, []byte(abs), []byte(client))
	if e := url.QueryEscape(abs); e != abs {
		b = bytes.ReplaceAll(b, []byte(e), []byte(url.QueryEscape(client)))
	}
	return b
}

// ---- live updates (socket.io) ----
//
// An app opens socket.io without a token and then sends ["auth", token]. The
// gateway resolves that token, swaps it for the person's Audiobookshelf one
// and relays the rest both ways, swapping tokens in what passes.

// gwSocketPair resolves the token in an "auth" event to the person's Audiobookshelf token.
func (s *Server) gwSocketPair(ctx context.Context, msg []byte) (gwPair, bool) {
	m := gwSocketAuth.FindSubmatch(msg)
	if m == nil {
		return gwPair{}, false
	}
	tok := string(m[1])
	u, err := s.gatewayUser(ctx, tok)
	if err != nil || u == nil {
		return gwPair{}, false
	}
	ac, err := s.absFor(ctx, u)
	if err != nil {
		return gwPair{}, false
	}
	return gwPair{client: tok, abs: ac.c.Token(), base: ac.c.Base, at: time.Now()}, true
}

func gwSid(r *http.Request) string { return r.URL.Query().Get("sid") }

// handleABSSocket is /socket.io/: a websocket, or HTTP long-polling.
func (s *Server) handleABSSocket(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend == "native" {
		s.nativeSocketHTTP(w, r)
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.gwWebSocket(w, r, r.URL.Path)
		return
	}
	s.gwSocketPolling(w, r, r.URL.Path)
}

// gwSocketPolling relays socket.io's HTTP long-polling.
func (s *Server) gwSocketPolling(w http.ResponseWriter, r *http.Request, p string) {
	ctx := r.Context()
	sid := gwSid(r)
	gwSocketsMu.Lock()
	pair, known := gwSockets[sid]
	gwSocketsMu.Unlock()
	var raw []byte
	if r.Method == http.MethodPost {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			gwError(w, http.StatusBadRequest, "bad request")
			return
		}
		if np, ok := s.gwSocketPair(ctx, b); ok {
			pair, known = np, true
			if sid != "" {
				gwSocketsMu.Lock()
				gwSockets[sid] = np
				for k, v := range gwSockets {
					if time.Since(v.at) > 24*time.Hour {
						delete(gwSockets, k)
					}
				}
				gwSocketsMu.Unlock()
			}
		}
		if known {
			b = bytes.ReplaceAll(b, []byte(pair.client), []byte(pair.abs))
		}
		raw = b
	}
	base := s.cfg.ABSURL
	if known {
		base = pair.base
	}
	if base == "" {
		gwError(w, http.StatusServiceUnavailable, "no Audiobookshelf")
		return
	}
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, base+p+"?"+r.URL.RawQuery, body)
	if err != nil {
		s.fail(w, err, "socket")
		return
	}
	for _, h := range []string{"Content-Type", "Accept", "User-Agent"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	res, err := s.streamHTTP.Do(req)
	if err != nil {
		gwError(w, http.StatusBadGateway, "Audiobookshelf unreachable")
		return
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if known {
		b = bytes.ReplaceAll(b, []byte(pair.abs), []byte(pair.client))
	}
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(b)
}

// gwWebSocket relays socket.io over a websocket.
func (s *Server) gwWebSocket(w http.ResponseWriter, r *http.Request, p string) {
	gwSocketsMu.Lock()
	pair, known := gwSockets[gwSid(r)]
	gwSocketsMu.Unlock()
	base := s.cfg.ABSURL
	if known {
		base = pair.base
	}
	if base == "" {
		gwError(w, http.StatusServiceUnavailable, "no Audiobookshelf")
		return
	}
	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer client.CloseNow()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := strings.Replace(strings.Replace(base, "https://", "wss://", 1), "http://", "ws://", 1) + p + "?" + r.URL.RawQuery
	upstream, _, err := websocket.Dial(ctx, up, &websocket.DialOptions{HTTPHeader: http.Header{"User-Agent": {r.Header.Get("User-Agent")}}})
	if err != nil {
		client.Close(websocket.StatusTryAgainLater, "Audiobookshelf unreachable")
		return
	}
	defer upstream.CloseNow()
	client.SetReadLimit(4 << 20)
	upstream.SetReadLimit(16 << 20)
	var mu sync.Mutex
	get := func() (gwPair, bool) { mu.Lock(); defer mu.Unlock(); return pair, known }
	errc := make(chan error, 2)
	go func() { // app → Audiobookshelf
		for {
			typ, msg, err := client.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			if typ == websocket.MessageText {
				if np, ok := s.gwSocketPair(ctx, msg); ok {
					mu.Lock()
					pair, known = np, true
					mu.Unlock()
				}
				if pp, ok := get(); ok {
					msg = bytes.ReplaceAll(msg, []byte(pp.client), []byte(pp.abs))
				}
			}
			if err := upstream.Write(ctx, typ, msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() { // Audiobookshelf → app
		for {
			typ, msg, err := upstream.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			if pp, ok := get(); ok && typ == websocket.MessageText {
				msg = bytes.ReplaceAll(msg, []byte(pp.abs), []byte(pp.client))
			}
			if err := client.Write(ctx, typ, msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	err = <-errc
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		client.Close(ce.Code, ce.Reason)
		upstream.Close(ce.Code, ce.Reason)
	}
}
