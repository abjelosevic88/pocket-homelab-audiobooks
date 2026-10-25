package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ---- client address ----

// trustedPeer reports whether ip is a configured reverse proxy.
func (s *Server) trustedPeer(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range s.cfg.TrustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP is the address a request came from. Forwarding headers are only
// believed when the immediate peer is a trusted proxy (TRUSTED_PROXIES), and
// X-Forwarded-For is read from the right: the first address that is not one
// of our proxies is the client (anything left of it is client-supplied).
func (s *Server) clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if h, _, err := net.SplitHostPort(peer); err == nil {
		peer = h
	}
	if !s.cfg.TrustProxy || !s.trustedPeer(net.ParseIP(peer)) {
		return peer
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		hops := strings.Split(strings.Join(xff, ","), ",")
		client := ""
		for i := len(hops) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(hops[i]))
			if ip == nil {
				break // garbage: stop at the last address a proxy vouched for
			}
			client = ip.String()
			if !s.trustedPeer(ip) {
				return client
			}
		}
		if client != "" {
			return client
		}
		return peer
	}
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}
	return peer
}

// realIP replaces RemoteAddr with clientIP (what the logs, the share and
// login limiters see). True-Client-IP and other headers are never believed.
func (s *Server) realIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = s.clientIP(r)
		next.ServeHTTP(w, r)
	})
}

// ---- cross-site requests ----

// tokenAuthenticated reports whether the request carries its credentials
// explicitly (a bearer token or API key), which a browser never adds to a
// cross-site request on its own.
func tokenAuthenticated(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return true
	}
	return r.Header.Get("X-API-Key") != ""
}

// sameOrigin refuses state-changing requests a browser sent on behalf of
// another site. The session cookie is SameSite=Lax, which sibling sites under
// the same domain still get, and AUTH_MODE none/proxy have ambient
// credentials, so unsafe methods must come from this app's own origin: the
// Origin header (or Referer when a browser omits Origin) has to name this host
// or BASE_URL. Requests with neither header are not from a browser page and
// pass; token-authenticated requests always pass.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
			next.ServeHTTP(w, r)
			return
		}
		if tokenAuthenticated(r) || s.originAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeErr(w, http.StatusForbidden, "cross-site request refused")
	})
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
		if origin == "" {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false // includes Origin: null (sandboxed frames, file://)
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if b, err := url.Parse(s.cfg.BaseURL); err == nil && b.Host != "" && strings.EqualFold(u.Host, b.Host) {
		return true
	}
	return false
}

// sameOriginNavigation guards GETs with side effects: a browser marks
// requests another site started (Sec-Fetch-Site same-site / cross-site);
// this app's own pages, a typed URL and non-browser clients pass.
func sameOriginNavigation(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

// ---- /metrics ----

// metricsAllowed: the METRICS_TOKEN as a bearer token, or a signed-in admin.
func (s *Server) metricsAllowed(r *http.Request) bool {
	if t := s.cfg.MetricsToken; t != "" {
		if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") &&
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(h[7:])), []byte(t)) == 1 {
			return true
		}
	}
	u, err := s.resolveUser(r)
	return err == nil && u != nil && u.Role == "admin"
}

// ---- password sign-in ----

// loginSlots bounds concurrent bcrypt comparisons for password sign-in.
var loginSlots = make(chan struct{}, 4)

// passwordAttempt runs check under the per-address attempt limit (shared by
// the web sign-in and Audiobookshelf apps' sign-in). It answers false with
// limited=true when the address has used up its attempts for now.
func (s *Server) passwordAttempt(r *http.Request, check func() bool) (ok, limited bool) {
	peer := sharePeer(r)
	if !s.loginAttempts.permit(peer) {
		return false, true
	}
	select {
	case loginSlots <- struct{}{}:
	case <-r.Context().Done():
		return false, false
	}
	ok = check()
	<-loginSlots
	if ok {
		s.loginAttempts.reset(peer)
	}
	return ok, false
}
