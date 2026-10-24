package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/config"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

// fakeIdP is a tiny OpenID provider: it signs whoever `email` says.
type fakeIdP struct {
	*httptest.Server
	key, forger *rsa.PrivateKey
	email       string
	verified    bool
	forge       bool
	nonces      map[string]string // code → nonce
	challenges  map[string]string // code → PKCE challenge
}

func newFakeIdP(t *testing.T) *fakeIdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &fakeIdP{key: k, forger: f, verified: true, nonces: map[string]string{}, challenges: map[string]string{}}
	mux := http.NewServeMux()
	p.Server = httptest.NewServer(mux)
	b64 := base64.RawURLEncoding.EncodeToString
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token", "jwks_uri": p.URL + "/jwks"})
	})
	mux.HandleFunc("/api/v1/auth/subject", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "iss": p.URL})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "kid": "k1", "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "music" || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad request", 400)
			return
		}
		code := store.RandomToken(8)
		p.nonces[code], p.challenges[code] = q.Get("nonce"), q.Get("code_challenge")
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, _ := r.BasicAuth()
		code := r.FormValue("code")
		sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if id != "music" || secret != "shh" || p.challenges[code] != b64(sum[:]) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		head, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
		body, _ := json.Marshal(map[string]any{"iss": p.URL, "sub": strings.ToLower(p.email), "auth_revision": int64(1), "aud": "music", "exp": time.Now().Add(time.Hour).Unix(), "nonce": p.nonces[code], "email": p.email, "email_verified": p.verified, "name": "Aleksandar"})
		signing := b64(head) + "." + b64(body)
		h := sha256.Sum256([]byte(signing))
		signer := p.key
		if p.forge {
			signer = p.forger
		}
		sig, _ := rsa.SignPKCS1v15(rand.Reader, signer, crypto.SHA256, h[:])
		json.NewEncoder(w).Encode(map[string]string{"id_token": signing + "." + b64(sig), "access_token": "at", "token_type": "Bearer"})
	})
	t.Cleanup(p.Close)
	return p
}

func testConfig(dir string) *config.Config {
	return &config.Config{DataDir: dir, DBPath: dir + "/t.db", FilesDir: dir + "/files", AuthMode: "local", SessionTTL: time.Hour, UserAgent: "test"}
}

func openTestStore(t *testing.T, cfg *config.Config) *store.Store {
	t.Helper()
	st, err := store.Open(cfg.DBPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestOIDCSignIn(t *testing.T) {
	idp := newFakeIdP(t)
	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCClientSecret, cfg.OIDCName = idp.URL, "music", "shh", "PocketID"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := openTestStore(t, cfg)
	ts := httptest.NewUnstartedServer(nil)
	cfg.BaseURL = "http://" + ts.Listener.Addr().String()
	s := New(cfg, st, log, nil)
	if err := s.core.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = s.Router()
	ts.Start()
	t.Cleanup(ts.Close)
	ctx := context.Background()
	_ = st.CreateUser(ctx, &store.User{Email: "aleksandar@example.com", Name: "A", Role: "admin", PasswordHash: "x"})

	// signIn runs the browser dance and returns where it ended up plus the client.
	signIn := func(next string) (*url.URL, *http.Client) {
		jar, _ := cookiejar.New(nil)
		var last *url.URL
		cl := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			last = req.URL
			if strings.HasPrefix(req.URL.String(), ts.URL) && !strings.HasPrefix(req.URL.Path, "/api/") {
				return http.ErrUseLastResponse // back in the app
			}
			return nil
		}}
		res, err := cl.Get(ts.URL + "/api/v1/auth/oidc/start?next=" + url.QueryEscape(next))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return last, cl
	}
	me := func(cl *http.Client) int {
		res, err := cl.Get(ts.URL + "/api/v1/auth/me")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	idp.email = "Aleksandar@Example.com"
	end, cl := signIn("/lists/1")
	if end == nil || end.Path != "/lists/1" || me(cl) != 200 {
		t.Fatalf("happy path ended at %v, me=%d", end, me(cl))
	}

	// the status tells the login page about the button
	var status map[string]any
	res, _ := http.Get(ts.URL + "/api/v1/auth/status")
	json.NewDecoder(res.Body).Decode(&status)
	res.Body.Close()
	if status["oidc"] != true || status["oidc_name"] != "PocketID" {
		t.Errorf("status %v", status)
	}

	for _, c := range []struct {
		name, email string
		verified    bool
		forge       bool
		next, want  string
	}{
		{"unknown email", "stranger@example.com", true, false, "/", "No Pocket Audiobooks account"},
		{"unverified email", "aleksandar@example.com", false, false, "/", "verified email"},
		{"forged signature", "aleksandar@example.com", true, true, "/", "did not check out"},
	} {
		idp.email, idp.verified, idp.forge = c.email, c.verified, c.forge
		end, cl := signIn(c.next)
		if end == nil || end.Path != "/login" || !strings.Contains(end.Query().Get("error"), c.want) || me(cl) != 401 {
			t.Errorf("%s: ended at %v", c.name, end)
		}
	}

	// with OIDC_AUTO_CREATE the stranger gets a member account
	cfg.OIDCAutoCreate = true
	idp.email, idp.verified, idp.forge = "stranger@example.com", true, false
	if end, cl := signIn("/"); end == nil || end.Path != "/" || me(cl) != 200 {
		t.Errorf("auto-create: ended at %v", end)
	} else if u, err := st.GetUserByEmail(ctx, "stranger@example.com"); err != nil || u.Role != "member" {
		t.Errorf("auto-created user: %v %v", u, err)
	}

	// the return path cannot leave the site
	idp.email = "aleksandar@example.com"
	if end, _ := signIn("//evil.example/x"); end == nil || end.Host != strings.TrimPrefix(ts.URL, "http://") || end.Path != "/" {
		t.Errorf("open redirect: %v", end)
	}
	// a callback without the browser's cookie is refused
	res, _ = (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(ts.URL + "/api/v1/auth/oidc/callback?code=x&state=y")
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "/login?error=") {
		t.Errorf("cookieless callback → %q", loc)
	}
	res.Body.Close()
}

// TestOIDCSettingsSource covers where the relying-party settings come from:
// nothing without Core and without OIDC_*, the Core-issued client (persisted
// by the SDK next to the app token) on a box, and OIDC_* winning over it.
func TestOIDCSettingsSource(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// no Core, no env: off, and the routes answer 404
	dir := t.TempDir()
	cfg := testConfig(dir)
	s := New(cfg, openTestStore(t, cfg), log, nil)
	if s.oidcEnabled() || s.oidcName() != "" {
		t.Fatalf("enabled without any settings: %q", s.oidcName())
	}
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/api/v1/auth/oidc/start")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if res.StatusCode != 404 || body["error"] == nil {
		t.Errorf("start without settings: %d %v", res.StatusCode, body)
	}
	res, _ = http.Get(srv.URL + "/api/v1/auth/status")
	body = nil
	json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if body["oidc"] != false {
		t.Errorf("status without settings: %v", body)
	}

	// the client Core issued on registration, as the SDK persists it
	dir = t.TempDir()
	cfg = testConfig(dir)
	cfg.CoreURL, cfg.BaseURL = "http://core.invalid", "http://app.invalid"
	if err := os.WriteFile(dir+"/core.token.oidc.json", []byte(`{"issuer":"http://core.invalid/","client_id":"pco_music","client_secret":"s3"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s = New(cfg, openTestStore(t, cfg), log, nil)
	issuer, id, secret, name := s.oidcSettings()
	if issuer != "http://core.invalid" || id != "pco_music" || secret != "s3" || name != "Pocket Core" || !s.oidcEnabled() || !s.oidcAutoCreate() {
		t.Errorf("core-issued client: %q %q %q %q enabled=%v auto=%v", issuer, id, secret, name, s.oidcEnabled(), s.oidcAutoCreate())
	}
	p := s.provider(issuer)
	if p.issuerURL != issuer || s.provider(issuer) != p {
		t.Errorf("provider not cached for %q", issuer)
	}

	// explicit OIDC_* wins, and the provider follows the issuer
	cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCClientSecret, cfg.OIDCName = "http://idp.invalid", "me", "", "PocketID"
	if issuer, id, _, name = s.oidcSettings(); issuer != "http://idp.invalid" || id != "me" || name != "PocketID" || s.oidcAutoCreate() {
		t.Errorf("env settings: %q %q %q auto=%v", issuer, id, name, s.oidcAutoCreate())
	}
	if np := s.provider(issuer); np == p || np.issuerURL != issuer {
		t.Errorf("provider not rebuilt for the new issuer")
	}
}
