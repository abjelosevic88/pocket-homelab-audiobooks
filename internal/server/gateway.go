package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// The Pocket Homelab player already speaks ABS. Keep its paths and response
// shapes, but resolve its Pocket token to the person's own ABS connection.
// In particular, /api/me contains a legacy ABS token different from the API
// key used for the request: redact credential fields, not just the known key.
var gatewayReads = regexp.MustCompile(`^/(api/(libraries(/[^/]+(/(items|stats|filterdata|search|personalized|series(/[^/]+)?|authors|narrators|collections|playlists|recent-episodes|download))?)?|items/[^/]+(/(cover|file/[^/]+(/download)?|download|ebook(/[^/]+)?))?|authors/[^/]+(/image)?|series/[^/]+|me(/(items-in-progress|listening-stats|listening-sessions|progress/[^/]+(/[^/]+)?))?|session/[^/]+|playlists(/[^/]+)?|collections(/[^/]+)?|podcasts/[^/]+/(checknew|episode/[^/]+))|hls/[^/]+/[^/]+)$`)
var gatewayWrites = map[string]*regexp.Regexp{
	"POST":   regexp.MustCompile(`^/api/(items/[^/]+/play(/[^/]+)?|items/batch/(get|delete)|session/(local|local-all|[^/]+/(sync|close))|me/(progress/batch/update|sync-local-progress|item/[^/]+/bookmark)|upload)$`),
	"PATCH":  regexp.MustCompile(`^/api/me/(progress/[^/]+(/[^/]+)?|item/[^/]+/bookmark)$`),
	"DELETE": regexp.MustCompile(`^/api/(me/(progress/[^/]+|item/[^/]+/bookmark/[^/]+)|items/[^/]+)$`),
}

func gatewayAllowed(method, path string) bool {
	// Reject noncanonical paths before matching; upstream routers decode paths too.
	if strings.ContainsAny(path, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." || strings.Contains(part, "%") {
			return false
		}
	}
	if method == http.MethodGet || method == http.MethodHead {
		return gatewayReads.MatchString(path)
	}
	re := gatewayWrites[method]
	return re != nil && re.MatchString(path)
}

func gatewayToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if t := r.Header.Get("X-API-Key"); t != "" {
		return t
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return r.URL.Query().Get("token")
	}
	return ""
}

func (s *Server) gatewayUser(ctx context.Context, token string) (*store.User, error) {
	// Link tokens have narrower scopes and must never enter the native gateway.
	if token == "" || core.IsLinkToken(token) {
		return nil, store.ErrNotFound
	}
	if s.core.Configured() && core.IsCoreToken(token) {
		return s.coreUser(ctx, token)
	}
	return s.store.GetUserByAPIToken(ctx, token)
}

// Explicit token auth only: cookies, proxy headers and AUTH_MODE=none must not
// turn the native API into an anonymous route to the owner's ABS account.
func (s *Server) handleGateway(w http.ResponseWriter, r *http.Request) {
	token := gatewayToken(r)
	u, err := s.gatewayUser(r.Context(), token)
	if err != nil || u == nil {
		writeErr(w, http.StatusUnauthorized, "use a Pocket Audiobooks API token or Pocket Core device token")
		return
	}
	if !gatewayAllowed(r.Method, r.URL.Path) {
		writeErr(w, http.StatusForbidden, "this Audiobookshelf operation is not available through Pocket Audiobooks")
		return
	}
	ac, err := s.absFor(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "connect your Audiobookshelf account in Pocket Audiobooks settings")
		return
	}
	// ABS serves HLS without authentication and lets admins inspect any open
	// session. Check ownership here, including when this person's account is root.
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	sessionID := ""
	if len(parts) == 3 && parts[0] == "hls" {
		sessionID = parts[1]
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "session" && parts[2] != "local" && parts[2] != "local-all" {
		sessionID = parts[2]
	}
	if sessionID != "" && !gatewayOwnSession(r.Context(), ac, sessionID) {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	target, err := url.Parse(ac.c.Base)
	if err != nil || target.Host == "" {
		writeErr(w, http.StatusBadGateway, "invalid Audiobookshelf address")
		return
	}

	if r.Method == "POST" && r.URL.Path == "/api/upload" {
		s.proxyABSUpload(w, r, ac)
		return
	}
	// Replay small JSON writes if the ABS access token needs refreshing.
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	}
	client := *s.streamHTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	proxy := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			q := p.Out.URL.Query()
			q.Del("token")
			p.Out.URL.RawQuery = q.Encode()
			// Forward only player headers. Never forward Pocket cookies, credentials,
			// client-supplied proxy identity or ABS refresh tokens.
			p.Out.Header = http.Header{}
			for _, key := range []string{"Accept", "Content-Type", "Range", "If-Range", "If-None-Match", "If-Modified-Since", "User-Agent"} {
				if values := p.In.Header.Values(key); len(values) > 0 {
					p.Out.Header[key] = values
				}
			}
			p.Out.Header.Set("Authorization", "Bearer "+ac.c.Token())
			p.Out.Header.Set("Accept-Encoding", "identity")
		},
		Transport: gatewayTransport(func(req *http.Request) (*http.Response, error) {
			req.RequestURI = ""
			return ac.c.DoWith(&client, req)
		}),
		ModifyResponse: func(res *http.Response) error { return gatewayResponse(res, ac, token) },
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == nil {
				writeErr(w, http.StatusBadGateway, "Audiobookshelf could not complete the request")
			}
		},
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
}

type gatewayTransport func(*http.Request) (*http.Response, error)

func (f gatewayTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func gatewayResponse(res *http.Response, ac *absConn, token string) error {
	res.Header.Del("Set-Cookie")
	// Authenticated answers must not be shared across people by a proxy cache.
	res.Header.Set("Cache-Control", "private, no-store")
	if location := res.Header.Get("Location"); location != "" {
		rewritten := gatewayText(location, ac, token)
		u, err := url.Parse(rewritten)
		if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
			return errors.New("unexpected upstream redirect")
		}
		res.Header.Set("Location", rewritten)
	}
	if res.Request.Method == http.MethodHead {
		return nil
	}
	ct := strings.ToLower(res.Header.Get("Content-Type"))
	isJSON := strings.Contains(ct, "json")
	isPlaylist := strings.Contains(ct, "mpegurl") || strings.HasSuffix(res.Request.URL.Path, ".m3u8")
	if !isJSON && !isPlaylist {
		return nil
	}
	defer res.Body.Close()
	const limit = 32 << 20
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(b) > limit {
		return errors.New("invalid upstream response")
	}
	if isJSON {
		var v any
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return err
		}
		b, err = json.Marshal(gatewayJSON(v, ac, token))
		if err != nil {
			return err
		}
	} else {
		b = []byte(gatewayPlaylist(gatewayText(string(b), ac, token), token))
	}
	res.Body = io.NopCloser(bytes.NewReader(b))
	res.ContentLength = int64(len(b))
	res.Header.Del("Content-Length")
	res.Header.Del("ETag")
	res.Header.Del("Content-Encoding")
	return nil
}

func gatewayJSON(v any, ac *absConn, token string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			switch strings.ToLower(k) {
			case "token", "accesstoken":
				x[k] = token
			case "refreshtoken", "password", "passwordhash", "apikey", "apikeys":
				delete(x, k)
			default:
				x[k] = gatewayJSON(value, ac, token)
			}
		}
	case []any:
		for i, value := range x {
			x[i] = gatewayJSON(value, ac, token)
		}
	case string:
		return gatewayText(x, ac, token)
	}
	return v
}

func gatewayText(text string, ac *absConn, token string) string {
	for _, base := range []string{ac.c.Base, ac.public} {
		if base != "" {
			text = strings.ReplaceAll(text, strings.TrimRight(base, "/")+"/", "/")
		}
	}
	if upstream := ac.c.Token(); upstream != "" {
		text = strings.ReplaceAll(text, upstream, token)
		text = strings.ReplaceAll(text, url.QueryEscape(upstream), url.QueryEscape(token))
	}
	return text
}

func (s *Server) handleGatewayStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.newABS(s.cfg.ABSURL, "").Status(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Audiobookshelf unavailable")
		return
	}
	// Apps sign in here with an app password (absgateway.go), never the upstream OIDC flow.
	writeJSON(w, http.StatusOK, map[string]any{"app": "audiobookshelf", "serverVersion": st.ServerVersion, "isInit": st.IsInit, "language": "en-us",
		"authMethods": []string{"local"}, "authFormData": map[string]any{}, "gateway": "Pocket Audiobooks"})
}

// Playback session ids are also ABS HLS stream ids.
func gatewayOwnSession(ctx context.Context, ac *absConn, id string) bool {
	req, err := ac.c.Request(ctx, http.MethodGet, "/api/session/"+url.PathEscape(id), nil)
	if err != nil {
		return false
	}
	res, err := ac.c.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false
	}
	var session struct {
		UserID string `json:"userId"`
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&session) == nil && session.UserID != "" && session.UserID == ac.userID
}

var gatewayPlaylistURI = regexp.MustCompile(`URI="([^"]+)"`)

func gatewayPlaylist(body, token string) string {
	withToken := func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.IsAbs() || u.Host != "" {
			return raw
		}
		q := u.Query()
		q.Set("token", token)
		u.RawQuery = q.Encode()
		return u.String()
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			lines[i] = gatewayPlaylistURI.ReplaceAllStringFunc(line, func(match string) string { return `URI="` + withToken(match[5:len(match)-1]) + `"` })
		} else {
			lines[i] = withToken(line)
		}
	}
	return strings.Join(lines, "\n")
}
