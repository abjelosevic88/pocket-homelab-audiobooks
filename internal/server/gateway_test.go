package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
)

func gatewayCall(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Range", "bytes=0-2")
	r.Header.Set("Cookie", "upstream_cookie=must-not-forward")
	r.Header.Set("X-Refresh-Token", "must-not-forward")
	r.Header.Set("X-Forwarded-User", "root")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGatewayPlayerAndIsolation(t *testing.T) {
	s, h, f := testServer(t)
	ana, ben := addPerson(t, s, "ana@example.com", "keyA"), addPerson(t, s, "ben@example.com", "keyB")
	a, at, err := s.store.CreateAPIToken(context.Background(), ana.u.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.store.CreateAPIToken(context.Background(), ben.u.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ token, user string }{{a, "u-keyA"}, {b, "u-keyB"}} {
		res := gatewayCall(h, "GET", "/api/me", tc.token, "")
		if res.Code != 200 || !strings.Contains(res.Body.String(), tc.user) {
			t.Fatalf("me: %d %s", res.Code, res.Body)
		}
	}
	res := gatewayCall(h, "POST", "/api/items/"+bookID+"/play", a, `{"forceDirectPlay":true}`)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "sess-keyA") {
		t.Fatalf("play: %d %s", res.Code, res.Body)
	}
	if res := gatewayCall(h, "GET", "/api/items/"+bookID+"/file/11?token="+a, "", ""); res.Code != 206 || res.Body.String() != "ID3" {
		t.Fatalf("stream: %d %s", res.Code, res.Body)
	}
	if res := gatewayCall(h, "POST", "/api/session/sess-keyA/sync", a, `{"currentTime":900,"timeListened":15}`); res.Code != 200 {
		t.Fatalf("sync: %d %s", res.Code, res.Body)
	}
	if res := gatewayCall(h, "POST", "/api/session/sess-keyA/close", b, `{}`); res.Code != 404 {
		t.Fatalf("other person's session: %d", res.Code)
	}
	if res := gatewayCall(h, "POST", "/api/session/sess-keyA/close", a, `{}`); res.Code != 200 {
		t.Fatalf("close: %d", res.Code)
	}
	f.mu.Lock()
	if len(f.streamed) != 1 || f.streamed[0] != "keyA|bytes=0-2" || len(f.synced) != 1 || len(f.closed) != 1 {
		t.Errorf("stream/sync/close did not reach ABS as Ana: %+v %+v %+v", f.streamed, f.synced, f.closed)
	}
	f.mu.Unlock()
	// Native requests never use the web cookie or AUTH_MODE=none fallback.
	s.cfg.AuthMode = "none"
	for _, token := range []string{"", "wrong", "pcl_invalid"} {
		if res := gatewayCall(h, "GET", "/api/libraries", token, ""); res.Code != 401 {
			t.Errorf("token %q accepted: %d", token, res.Code)
		}
	}
	if res := call(t, h, ana, "GET", "/api/libraries", ""); res.Code != 401 {
		t.Errorf("web cookie authorized gateway: %d", res.Code)
	}
	for _, path := range []string{"/api/users", "/api/api-keys", "/api/me/sessions", "/api/libraries/x/../users", "/api/items/%252e%252e/file/x"} {
		if res := gatewayCall(h, "GET", path, a, ""); res.Code != 403 {
			t.Errorf("unsafe route %s: %d", path, res.Code)
		}
	}
	if res := gatewayCall(h, "POST", "/api/settings", a, `{}`); res.Code != 403 {
		t.Errorf("settings write: %d", res.Code)
	}
	if res := gatewayCall(h, "GET", "/api/v1/auth/me", a, ""); res.Code != 200 || !strings.Contains(res.Body.String(), "ana@example.com") {
		t.Errorf("web API shadowed: %d %s", res.Code, res.Body)
	}
	if err := s.store.DeleteAPIToken(context.Background(), ana.u.ID, at.ID); err != nil {
		t.Fatal(err)
	}
	if res := gatewayCall(h, "GET", "/api/me", a, ""); res.Code != 401 {
		t.Errorf("revoked token accepted: %d", res.Code)
	}
}

func TestGatewayCredentialsHeadersAndPlaylists(t *testing.T) {
	s, _, _ := testServer(t)
	p := addPerson(t, s, "ana@example.com", "api-secret")
	token, _, _ := s.store.CreateAPIToken(context.Background(), p.u.ID, "app")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api-secret" || r.URL.Query().Get("token") != "" {
			t.Error("wrong upstream authentication")
		}
		for _, header := range []string{"Cookie", "X-Refresh-Token", "X-Forwarded-User"} {
			if r.Header.Get(header) != "" {
				t.Errorf("leaked header %s", header)
			}
		}
		if r.URL.Path == "/api/session/session" {
			writeJSON(w, http.StatusOK, map[string]string{"userId": "u-api-secret"})
			return
		}
		w.Header().Set("Set-Cookie", "secret=yes")
		if strings.HasPrefix(r.URL.Path, "/hls/") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			io.WriteString(w, "#EXTM3U\n#EXT-X-MAP:URI=\"init.ts\"\nsegment.ts\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"ana","token":"different-legacy-secret","accessToken":"api-secret","refreshToken":"refresh-secret","nested":{"apiKey":"other-secret"},"number":1780000000123}`)
	}))
	defer upstream.Close()
	s.cfg.ABSURL = upstream.URL
	h := s.Router()
	res := gatewayCall(h, "GET", "/api/me", token, "")
	if res.Code != 200 || res.Header().Get("Set-Cookie") != "" || strings.Contains(res.Body.String(), "secret") || !strings.Contains(res.Body.String(), token) || !strings.Contains(res.Body.String(), "1780000000123") {
		t.Fatalf("credentials escaped: %d %s", res.Code, res.Body)
	}
	res = gatewayCall(h, "GET", "/hls/session/playlist.m3u8?token="+token, "", "")
	if res.Code != 200 || strings.Contains(res.Body.String(), "api-secret") || !strings.Contains(res.Body.String(), "segment.ts?token="+token) {
		t.Fatalf("playlist: %d %s", res.Code, res.Body)
	}
}

func TestGatewayRefreshReplaysBody(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != `{"currentTime":123}` {
			t.Errorf("lost replay body: %s", raw)
		}
		attempts++
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	c := abs.New(upstream.URL, "expired")
	c.Refresh = func(context.Context) (string, error) { return "fresh", nil }
	req, _ := http.NewRequest("POST", upstream.URL+"/api/session/s/sync", strings.NewReader(`{"currentTime":123}`))
	req.Header.Set("Authorization", "Bearer expired")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || attempts != 2 {
		t.Fatalf("refresh status=%d attempts=%d", res.StatusCode, attempts)
	}
}

func TestGatewayRoutesUsedByPocketHomelab(t *testing.T) {
	for _, path := range []string{"/api/libraries", "/api/libraries/l/stats", "/api/libraries/l/filterdata", "/api/libraries/l/items", "/api/libraries/l/search", "/api/items/i", "/api/me", "/api/me/items-in-progress", "/api/libraries/l/personalized", "/api/libraries/l/series", "/api/libraries/l/authors", "/api/authors/a", "/api/me/listening-stats", "/api/podcasts/p/checknew", "/api/items/i/file/1", "/api/items/i/cover", "/api/authors/a/image", "/hls/s/output.m3u8"} {
		if !gatewayAllowed("GET", path) {
			t.Errorf("missing GET %s", path)
		}
	}
	for _, pair := range [][2]string{{"POST", "/api/items/i/play"}, {"POST", "/api/items/i/play/e"}, {"POST", "/api/session/s/sync"}, {"POST", "/api/session/s/close"}, {"PATCH", "/api/me/progress/i"}, {"DELETE", "/api/me/progress/p"}, {"POST", "/api/me/item/i/bookmark"}, {"DELETE", "/api/me/item/i/bookmark/60"}, {"DELETE", "/api/items/i"}, {"POST", "/api/items/batch/delete"}, {"POST", "/api/upload"}} {
		if !gatewayAllowed(pair[0], pair[1]) {
			t.Errorf("missing %s %s", pair[0], pair[1])
		}
	}
}

func TestGatewayJSONCredentialFields(t *testing.T) {
	ac := &absConn{c: abs.New("http://abs.example/base", "key"), public: "https://abs.example"}
	result := gatewayJSON(map[string]any{"token": "legacy", "accessToken": "fresh", "refreshToken": "refresh", "url": "http://abs.example/base/api/items/i/file/1?token=key"}, ac, "pab_example")
	b, _ := json.Marshal(result)
	if strings.Contains(string(b), "legacy") || strings.Contains(string(b), "fresh") || strings.Contains(string(b), "abs.example") || !strings.Contains(string(b), `/api/items/i/file/1?token=pab_example`) {
		t.Fatal(string(b))
	}
}

func TestGatewayBlocksOtherPersonsHLSAndAdminSessions(t *testing.T) {
	s, _, _ := testServer(t)
	p := addPerson(t, s, "admin@example.com", "root-key")
	token, _, _ := s.store.CreateAPIToken(context.Background(), p.u.ID, "app")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ABS allows root to inspect another person's session and serves HLS
		// without checking the user. The gateway must do the ownership check.
		if r.URL.Path == "/api/session/other-session" {
			writeJSON(w, http.StatusOK, map[string]string{"userId": "another-person"})
			return
		}
		t.Errorf("foreign session/media reached upstream: %s", r.URL.Path)
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	s.cfg.ABSURL = upstream.URL
	h := s.Router()
	for _, tc := range [][2]string{{"GET", "/hls/other-session/output.m3u8"}, {"POST", "/api/session/other-session/sync"}, {"GET", "/api/session/other-session"}} {
		res := gatewayCall(h, tc[0], tc[1], token, "")
		if res.Code != http.StatusNotFound {
			t.Fatalf("foreign session accessible: %s %s: %d", tc[0], tc[1], res.Code)
		}
	}
}
