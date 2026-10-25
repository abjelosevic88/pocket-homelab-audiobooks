package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/config"
)

func makeAdmin(t *testing.T, s *Server, p person) {
	t.Helper()
	p.u.Role = "admin"
	if err := s.store.UpdateUser(context.Background(), p.u); err != nil {
		t.Fatal(err)
	}
}

func setPassword(t *testing.T, s *Server, p person, pw string) {
	t.Helper()
	h, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	p.u.PasswordHash = string(h)
	if err := s.store.UpdateUser(context.Background(), p.u); err != nil {
		t.Fatal(err)
	}
}

// leakyUpstream answers everything with an error page carrying a marker that
// must never reach a client.
func leakyUpstream(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"INTERNAL-SECRET-MARKER"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConnectIsNotAnOpenFetcher(t *testing.T) {
	s, h, _ := testServer(t)
	ana := addPerson(t, s, "ana@example.com", "keyA")
	ben := addPerson(t, s, "ben@example.com", "keyB")
	makeAdmin(t, s, ana)
	leak := leakyUpstream(t)

	// a member cannot point the server at an address of their choosing
	if w := call(t, h, ben, "PUT", "/api/v1/connection", `{"mode":"key","url":"`+leak.URL+`","key":"k"}`); w.Code != http.StatusForbidden {
		t.Fatalf("member custom url: %d %s", w.Code, w.Body)
	}
	// an admin may, but the upstream answer is never echoed
	w := call(t, h, ana, "PUT", "/api/v1/connection", `{"mode":"key","url":"`+leak.URL+`","key":"k"}`)
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "MARKER") {
		t.Fatalf("admin custom url: %d %s", w.Code, w.Body)
	}
	w = call(t, h, ana, "PUT", "/api/v1/connection", `{"mode":"direct","url":"`+leak.URL+`","username":"u","password":"p"}`)
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "MARKER") {
		t.Fatalf("admin direct sign-in: %d %s", w.Code, w.Body)
	}

	// the native backend has no connection to change
	s.cfg.Backend = "native"
	if w := call(t, h, ben, "PUT", "/api/v1/connection", `{"mode":"key","url":"http://127.0.0.1:1","key":"k"}`); w.Code != http.StatusNotFound {
		t.Fatalf("native connect: %d %s", w.Code, w.Body)
	}
}

// The CI smoke test: AUTH_MODE=none, a plain GET, the native backend.
func TestConnectionSmokeWithoutAuth(t *testing.T) {
	s, h, _ := testServer(t)
	s.cfg.AuthMode, s.cfg.Backend = "none", "native"
	r := httptest.NewRequest("GET", "/api/v1/connection", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"backend":"native"`) {
		t.Fatalf("smoke: %d %s", w.Code, w.Body)
	}
}

func TestNotificationTestDoesNotEcho(t *testing.T) {
	s, h, _ := testServer(t)
	ana := addPerson(t, s, "ana@example.com", "keyA")
	ben := addPerson(t, s, "ben@example.com", "keyB")
	makeAdmin(t, s, ana)
	leak := leakyUpstream(t)

	if w := call(t, h, ben, "POST", "/api/v1/integrations/test", `{"which":"notify","url":"`+leak.URL+`","format":"json"}`); w.Code != http.StatusForbidden {
		t.Fatalf("member typed url: %d %s", w.Code, w.Body)
	}
	w := call(t, h, ana, "POST", "/api/v1/integrations/test", `{"which":"notify","url":"`+leak.URL+`","format":"json"}`)
	if !strings.Contains(w.Body.String(), "HTTP 500") || strings.Contains(w.Body.String(), "MARKER") {
		t.Fatalf("admin test: %d %s", w.Code, w.Body)
	}
	// a member tests the saved address and learns only that it failed
	st, _ := s.store.GetSettings(context.Background(), ben.u.ID)
	st.NotifyURL, st.NotifyFormat = leak.URL, "json"
	if err := s.store.SaveUserSettings(context.Background(), ben.u.ID, st); err != nil {
		t.Fatal(err)
	}
	w = call(t, h, ben, "POST", "/api/v1/integrations/test", `{"which":"notify","url":"`+leak.URL+`"}`)
	if !strings.Contains(w.Body.String(), `"ok":false`) || strings.Contains(w.Body.String(), "MARKER") || strings.Contains(w.Body.String(), "500") {
		t.Fatalf("member saved test: %d %s", w.Code, w.Body)
	}
}

func TestEmailChangeNeedsPassword(t *testing.T) {
	s, h, _ := testServer(t)
	ana := addPerson(t, s, "ana@example.com", "keyA")
	setPassword(t, s, ana, "old-password")
	if w := call(t, h, ana, "PUT", "/api/v1/auth/me", `{"email":"attacker@example.com"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("email without password: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, ana, "PUT", "/api/v1/auth/me", `{"email":"attacker@example.com","current_password":"wrong"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("email with wrong password: %d %s", w.Code, w.Body)
	}
	// unchanged email and a name change need no password
	if w := call(t, h, ana, "PUT", "/api/v1/auth/me", `{"email":"ana@example.com","name":"Ana"}`); w.Code != http.StatusOK {
		t.Fatalf("name only: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, ana, "PUT", "/api/v1/auth/me", `{"email":"ana2@example.com","current_password":"old-password"}`); w.Code != http.StatusOK {
		t.Fatalf("email with password: %d %s", w.Code, w.Body)
	}
	if u, _ := s.store.GetUser(context.Background(), ana.u.ID); u.Email != "ana2@example.com" {
		t.Fatalf("email not changed: %s", u.Email)
	}

	// an account that signs in with single sign-on only takes its email from there
	ben := addPerson(t, s, "ben@example.com", "keyB")
	ben.u.PasswordHash = ""
	if err := s.store.UpdateUser(context.Background(), ben.u); err != nil {
		t.Fatal(err)
	}
	if w := call(t, h, ben, "PUT", "/api/v1/auth/me", `{"email":"attacker@example.com"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("sso email change: %d %s", w.Code, w.Body)
	}
}

func TestCrossSiteWritesRefused(t *testing.T) {
	s, h, _ := testServer(t)
	s.cfg.BaseURL = "https://audio.example.org"
	ana := addPerson(t, s, "ana@example.com", "keyA")
	tok, _, err := s.store.CreateAPIToken(context.Background(), ana.u.ID, "script")
	if err != nil {
		t.Fatal(err)
	}
	post := func(headers map[string]string, cookie bool) int {
		r := httptest.NewRequest("POST", "/api/v1/up-next", strings.NewReader(`{"item_id":"`+bookID+`"}`))
		r.Header.Set("Content-Type", "text/plain")
		if cookie {
			r.AddCookie(ana.cookie)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for name, hdr := range map[string]map[string]string{
		"sibling origin":  {"Origin": "https://evil.example.org"},
		"null origin":     {"Origin": "null"},
		"sibling referer": {"Referer": "https://evil.example.org/page"},
	} {
		if code := post(hdr, true); code != http.StatusForbidden {
			t.Fatalf("%s: %d", name, code)
		}
	}
	for name, hdr := range map[string]map[string]string{
		"same host":      {"Origin": "http://example.com"},
		"base url":       {"Origin": "https://audio.example.org"},
		"no browser":     {},
		"bearer foreign": {"Origin": "https://evil.example.org", "Authorization": "Bearer " + tok},
	} {
		if code := post(hdr, name != "bearer foreign"); code == http.StatusForbidden {
			t.Fatalf("%s refused", name)
		}
	}
	// AUTH_MODE=none: ambient credentials, same rule
	s.cfg.AuthMode = "none"
	if code := post(map[string]string{"Origin": "https://evil.example.org"}, false); code != http.StatusForbidden {
		t.Fatalf("auth none cross-site: %d", code)
	}

	// a backup is not written because another site linked to it
	makeAdmin(t, s, ana)
	s.cfg.AuthMode = "local"
	r := httptest.NewRequest("GET", "/api/v1/backup", nil)
	r.AddCookie(ana.cookie)
	r.Header.Set("Sec-Fetch-Site", "same-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site backup: %d", w.Code)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.cfg.DataDir, "backups")); len(entries) != 0 {
		t.Fatal("cross-site GET wrote a backup")
	}
}

func TestClientIPOnlyFromTrustedProxies(t *testing.T) {
	s, _, _ := testServer(t)
	s.cfg.TrustProxy = true
	var err error
	if s.cfg.TrustedProxies, err = config.ParseCIDRs(config.DefaultTrustedProxies); err != nil {
		t.Fatal(err)
	}
	ip := func(peer string, headers map[string]string) string {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = peer
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return s.clientIP(r)
	}
	cases := []struct {
		peer    string
		headers map[string]string
		want    string
	}{
		{"203.0.113.9:5000", map[string]string{"X-Forwarded-For": "1.2.3.4", "X-Real-IP": "1.2.3.4"}, "203.0.113.9"},
		{"172.18.0.2:5000", map[string]string{"X-Forwarded-For": "6.6.6.6, 1.2.3.4"}, "1.2.3.4"},
		{"172.18.0.2:5000", map[string]string{"X-Forwarded-For": "1.2.3.4, 10.0.0.3"}, "1.2.3.4"},
		{"172.18.0.2:5000", map[string]string{"True-Client-IP": "6.6.6.6"}, "172.18.0.2"},
		{"127.0.0.1:5000", map[string]string{"X-Real-IP": "5.6.7.8"}, "5.6.7.8"},
		{"172.18.0.2:5000", map[string]string{"X-Forwarded-For": "1.2.3.4, garbage"}, "172.18.0.2"},
	}
	for _, c := range cases {
		if got := ip(c.peer, c.headers); got != c.want {
			t.Errorf("%s %v: got %s, want %s", c.peer, c.headers, got, c.want)
		}
	}
	s.cfg.TrustProxy = false
	if got := ip("172.18.0.2:5000", map[string]string{"X-Forwarded-For": "1.2.3.4"}); got != "172.18.0.2" {
		t.Errorf("TRUST_PROXY=false: %s", got)
	}
}

func TestAttemptTableNeverLocksEveryoneOut(t *testing.T) {
	var a shareAttempts
	for i := 0; i < shareAttemptsMax; i++ {
		if !a.permit(fmt.Sprint("flood-", i)) {
			t.Fatal("flood entry refused")
		}
	}
	if !a.permit("someone-else") {
		t.Fatal("a full table refused a new address")
	}
	if len(a.entries) > shareAttemptsMax {
		t.Fatalf("table grew to %d", len(a.entries))
	}
	for i := 0; i < 12; i++ {
		a.permit("guesser")
	}
	if a.permit("guesser") {
		t.Fatal("the 13th attempt within a minute passed")
	}
}

func TestPasswordSignInIsLimited(t *testing.T) {
	s, h, _ := testServer(t)
	ana := addPerson(t, s, "ana@example.com", "keyA")
	setPassword(t, s, ana, "right-password")
	codes := []int{}
	for i := 0; i < 13; i++ {
		w := gwCall(h, "POST", "/api/v1/auth/login", "", `{"email":"ana@example.com","password":"wrong"}`)
		codes = append(codes, w.Code)
	}
	if codes[0] != http.StatusUnauthorized || codes[12] != http.StatusTooManyRequests {
		t.Fatalf("web sign-in codes: %v", codes)
	}
	// the Audiobookshelf apps' sign-in shares the same budget
	if w := gwCall(h, "POST", "/login", "", `{"username":"ana@example.com","password":"right-password"}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("gateway after limit: %d", w.Code)
	}
}

func TestPasswordChangeEndsOtherSignIns(t *testing.T) {
	s, h, _ := testServer(t)
	ctx := context.Background()
	ana := addPerson(t, s, "ana@example.com", "keyA")
	setPassword(t, s, ana, "old-password")
	other, err := s.store.CreateSession(ctx, ana.u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	personal, _, err := s.store.CreateAPIToken(ctx, ana.u.ID, "Homepage")
	if err != nil {
		t.Fatal(err)
	}
	login := func(ua string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"ana@example.com","password":"old-password"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("gateway login: %d %s", w.Code, w.Body)
		}
	}
	countGateway := func() int {
		n := 0
		toks, _ := s.store.ListAPITokens(ctx, ana.u.ID)
		for _, tk := range toks {
			if strings.HasPrefix(tk.Name, gwPasswordTokenName) {
				n++
			}
		}
		return n
	}
	// signing in again from the same app rotates its one token
	login("Audiobookshelf/2.20 iPhone")
	login("Audiobookshelf/2.20 iPhone")
	login("ShelfPlayer/1.0")
	if n := countGateway(); n != 2 {
		t.Fatalf("gateway tokens after three sign-ins from two apps: %d", n)
	}

	if w := call(t, h, ana, "PUT", "/api/v1/auth/me", `{"current_password":"old-password","new_password":"new-password"}`); w.Code != http.StatusOK {
		t.Fatalf("password change: %d %s", w.Code, w.Body)
	}
	if _, err := s.store.GetSessionUser(ctx, ana.cookie.Value); err != nil {
		t.Fatal("the session that changed the password was signed out")
	}
	if _, err := s.store.GetSessionUser(ctx, other); err == nil {
		t.Fatal("another session survived the password change")
	}
	if n := countGateway(); n != 0 {
		t.Fatalf("password-minted app tokens survived: %d", n)
	}
	if _, err := s.store.GetUserByAPIToken(ctx, personal); err != nil {
		t.Fatal("a personal token was revoked")
	}

	// an admin reset signs the person out everywhere
	ben := addPerson(t, s, "ben@example.com", "keyB")
	makeAdmin(t, s, ben)
	if w := call(t, h, ben, "PUT", fmt.Sprintf("/api/v1/users/%d", ana.u.ID), `{"password":"reset-password"}`); w.Code != http.StatusOK {
		t.Fatalf("admin reset: %d %s", w.Code, w.Body)
	}
	if _, err := s.store.GetSessionUser(ctx, ana.cookie.Value); err == nil {
		t.Fatal("session survived an admin reset")
	}
	if _, err := s.store.GetSessionUser(ctx, ben.cookie.Value); err != nil {
		t.Fatal("the admin was signed out by resetting someone else")
	}
}

func TestTokenLastUsedIsThrottled(t *testing.T) {
	s, _, _ := testServer(t)
	ctx := context.Background()
	ana := addPerson(t, s, "ana@example.com", "keyA")
	tok, info, err := s.store.CreateAPIToken(ctx, ana.u.ID, "player")
	if err != nil {
		t.Fatal(err)
	}
	lastUsed := func() string {
		var v *string
		_ = s.store.DB.QueryRow(`SELECT last_used_at FROM api_tokens WHERE id=?`, info.ID).Scan(&v)
		if v == nil {
			return ""
		}
		return *v
	}
	if _, err := s.store.GetUserByAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	first := lastUsed()
	if first == "" {
		t.Fatal("first use not recorded")
	}
	if _, err := s.store.GetUserByAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if lastUsed() != first {
		t.Fatal("last use rewritten within a minute")
	}
	old := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := s.store.DB.Exec(`UPDATE api_tokens SET last_used_at=? WHERE id=?`, old, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.GetUserByAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if lastUsed() == old {
		t.Fatal("stale last use not refreshed")
	}
}

func TestDownloadBackupsArePruned(t *testing.T) {
	dir := t.TempDir()
	keep := []string{"unrelated.db", "pocket-audiobooks-pre-restore.db"}
	for _, n := range keep {
		_ = os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	var names []string
	for i := 0; i < 8; i++ {
		ext := ".db"
		if i%2 == 0 {
			ext = ".tar.gz"
		}
		n := fmt.Sprintf("pocket-audiobooks-202610%02dT120000.000000000Z%s", 10+i, ext)
		names = append(names, n)
		_ = os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	if err := pruneDownloadBackups(dir, 5); err != nil {
		t.Fatal(err)
	}
	for i, n := range names {
		_, err := os.Stat(filepath.Join(dir, n))
		if exists := err == nil; exists != (i >= 3) {
			t.Fatalf("%s exists=%v", n, exists)
		}
	}
	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("%s was deleted", n)
		}
	}
}

func TestMetricsNeedAuth(t *testing.T) {
	s, _, _ := testServer(t)
	s.cfg.MetricsEnabled, s.cfg.MetricsToken = true, "metrics-secret"
	h := s.Router()
	get := func(auth string) int {
		r := httptest.NewRequest("GET", "/metrics", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := get(""); c != http.StatusUnauthorized {
		t.Fatalf("anonymous metrics: %d", c)
	}
	if c := get("Bearer wrong"); c != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", c)
	}
	if c := get("Bearer metrics-secret"); c != http.StatusOK {
		t.Fatalf("metrics token: %d", c)
	}
}
