package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// ---- Audiobookshelf, per person ----
//
// Every person listens with their OWN Audiobookshelf account, so progress,
// finished marks and bookmarks stay theirs and the Audiobookshelf apps on the
// phone agree with Pocket Audiobooks. Ways in:
//
//   - key: an API key of their account (Audiobookshelf → Settings → API Keys,
//     made by an admin for that person), sealed with DATA_DIR/secret.key. The
//     owner's can come from AUDIOBOOKSHELF_TOKEN.
//   - direct: they sign in here once with their password; only the refresh
//     token Audiobookshelf hands out is kept (it rotates on every refresh),
//     never the password.
//   - core: the Audiobookshelf login Pocket Core holds for them (Core → My
//     logins: TOKEN, and USER_ID to check it is theirs). Nothing is stored here.
//
// With none of these the library stays empty and Settings → Audiobookshelf says why.

// absConn is how one request reaches the person's Audiobookshelf.
type absConn struct {
	c      *abs.Client
	via    string // key | direct | core
	public string // what a browser opens ("Open in Audiobookshelf")
	key    string // server URL + Audiobookshelf user: the history belongs to exactly this
	user   string // the Audiobookshelf account's name
	userID string
}

// errNoABS means the person has no working way to Audiobookshelf.
type errNoABS struct{ msg string }

func (e *errNoABS) Error() string { return e.msg }

func isNoABS(err error) bool {
	var e *errNoABS
	return errors.As(err, &e)
}

// serverBase is the address used for a key or password account.
func (s *Server) serverBase(c *store.Connection) string {
	if c != nil && c.ServerURL != "" {
		return c.ServerURL
	}
	return s.cfg.ABSURL
}

func validServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return errors.New("the Audiobookshelf address must be http(s)://host[:port]")
	}
	return nil
}

func coreReason(err error, service string) string {
	var le *core.LinkError
	if errors.As(err, &le) {
		switch le.Reason {
		case "off_person":
			return "you switched Pocket Audiobooks' access to " + service + " off in Pocket Core (Settings → Apps acting for me)"
		case "off_admin":
			return "Pocket Audiobooks' access to " + service + " is switched off in Pocket Core (Links)"
		case "no_account":
			return "Pocket Core has no " + service + " login for you yet: add it under Settings → My logins in Pocket Core, or connect here"
		case "not_in_audience":
			return "you do not have access to " + service + " in Pocket Core"
		case "target_missing":
			return service + " is not set up in Pocket Core"
		case "not_declared":
			return "Pocket Core has not seen this Pocket Audiobooks version yet; it re-registers within a minute of starting"
		case "unknown_user":
			return "your email is not a person in Pocket Core"
		}
		return le.Message
	}
	return "Pocket Core did not answer: " + err.Error()
}

func (s *Server) newABS(base, tok string) *abs.Client {
	c := abs.New(base, tok)
	c.UserAgent = s.cfg.UserAgent
	c.HTTP = s.absHTTP
	return c
}

// absFor connects to the person's Audiobookshelf account.
func (s *Server) absFor(ctx context.Context, u *store.User) (*absConn, error) {
	if u == nil {
		return nil, &errNoABS{"nobody signed in"}
	}
	if s.cfg.Backend == "native" {
		return s.nativeConnection(ctx, u)
	}
	c, err := s.store.GetConnection(ctx, u.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if c != nil && c.Kind == "off" {
		return nil, &errNoABS{"Audiobookshelf is switched off in Settings → Audiobookshelf"}
	}
	if c != nil && (c.Kind == "key" || c.Kind == "direct") {
		base := s.serverBase(c)
		if base == "" {
			return nil, &errNoABS{"no Audiobookshelf address: set AUDIOBOOKSHELF_URL on the server or enter one in Settings → Audiobookshelf"}
		}
		secret, err := s.open(c.Secret)
		if err != nil {
			return nil, &errNoABS{err.Error()}
		}
		if secret == "" {
			return nil, &errNoABS{"connect Audiobookshelf again in Settings → Audiobookshelf"}
		}
		public := s.cfg.ABSPublicURL
		if c.ServerURL != "" {
			public = c.ServerURL
		}
		ac := &absConn{via: c.Kind, public: firstNonEmpty(public, base), key: base + "|" + c.ABSUserID, user: c.Username, userID: c.ABSUserID}
		if c.Kind == "key" {
			ac.c = s.newABS(base, secret)
			return ac, nil
		}
		access, _ := s.open(c.Access)
		ac.c = s.newABS(base, access)
		ac.c.Refresh = s.refresher(u.ID, base)
		return ac, nil
	}
	// kind core, or nothing chosen yet: Pocket Core when the box has one
	if !s.core.Configured() {
		return nil, &errNoABS{"connect your Audiobookshelf account in Settings → Audiobookshelf"}
	}
	cred, err := s.core.ServiceFor(ctx, u.Email, "audiobookshelf")
	if err != nil {
		return nil, &errNoABS{coreReason(err, "Audiobookshelf")}
	}
	tok := firstNonEmpty(cred.Fields["TOKEN"], cred.Key, cred.Fields["API_KEY"])
	if cred.URL == "" || tok == "" {
		return nil, &errNoABS{"Pocket Core knows your Audiobookshelf account but holds no key for it: add a TOKEN (an API key of your account) under Settings → My logins in Pocket Core, or connect here"}
	}
	base := strings.TrimRight(cred.URL, "/")
	// Core may hand out the public address; inside the box the direct one is faster and needs no TLS
	if s.cfg.ABSURL != "" && cred.PublicURL != "" && strings.EqualFold(base, strings.TrimRight(cred.PublicURL, "/")) {
		base = s.cfg.ABSURL
	}
	ac := &absConn{c: s.newABS(base, tok), via: "core", public: firstNonEmpty(cred.PublicURL, s.cfg.ABSPublicURL, cred.URL)}
	id, name, err := s.coreTokenUser(ctx, ac.c, tok)
	if err != nil {
		return nil, &errNoABS{"Audiobookshelf refused the key Pocket Core holds for you: " + err.Error()}
	}
	// a box-wide admin key acts as its own account, never as this person: refuse it for anyone else
	if want := cred.Fields["USER_ID"]; want != "" && want != id {
		return nil, &errNoABS{"the Audiobookshelf key Pocket Core holds for you belongs to another account (" + name + "): add your own API key under Settings → My logins in Pocket Core, or connect here"}
	}
	ac.userID, ac.user, ac.key = id, name, strings.TrimRight(cred.URL, "/")+"|"+id
	return ac, nil
}

// coreTokenUser finds the Audiobookshelf account a Core-held key belongs to (cached).
func (s *Server) coreTokenUser(ctx context.Context, c *abs.Client, tok string) (string, string, error) {
	k := store.HashToken(tok)
	s.coreUIDMu.Lock()
	v, ok := s.coreUID[k]
	s.coreUIDMu.Unlock()
	if ok {
		id, name, _ := strings.Cut(v, "\x00")
		return id, name, nil
	}
	me, err := c.Me(ctx)
	if err != nil || me.ID == "" {
		if err == nil {
			err = errors.New("no user")
		}
		return "", "", err
	}
	s.coreUIDMu.Lock()
	s.coreUID[k] = me.ID + "\x00" + me.Username
	s.coreUIDMu.Unlock()
	return me.ID, me.Username, nil
}

// refresher trades the person's refresh token for a new access token. One
// refresh at a time per person: Audiobookshelf rotates refresh tokens, so two
// at once would leave one of them holding a spent token.
func (s *Server) refresher(uid int64, base string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		mu := s.refreshLock(uid)
		mu.Lock()
		defer mu.Unlock()
		again := errors.New("sign in to Audiobookshelf again in Settings → Audiobookshelf")
		c, err := s.store.GetConnection(ctx, uid)
		if err != nil || c.Kind != "direct" {
			return "", again
		}
		// another request may have refreshed while this one waited
		if t, err := time.Parse(time.RFC3339Nano, c.UpdatedAt); err == nil && time.Since(t) < 20*time.Second {
			if a, _ := s.open(c.Access); a != "" {
				return a, nil
			}
		}
		rt, err := s.open(c.Secret)
		if err != nil || rt == "" {
			return "", again
		}
		res, err := s.newABS(base, "").RefreshWith(ctx, rt)
		if err != nil {
			return "", again
		}
		sealedR, err := s.seal(firstNonEmpty(res.RefreshToken, rt))
		if err != nil {
			return "", err
		}
		sealedA, err := s.seal(res.AccessToken)
		if err != nil {
			return "", err
		}
		if err := s.store.SaveTokens(ctx, uid, sealedR, sealedA); err != nil {
			return "", err
		}
		return res.AccessToken, nil
	}
}

func (s *Server) refreshLock(uid int64) *sync.Mutex {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	m, ok := s.refreshing[uid]
	if !ok {
		m = &sync.Mutex{}
		s.refreshing[uid] = m
	}
	return m
}

// ab is absFor for the signed-in person, writing the error itself.
func (s *Server) ab(w http.ResponseWriter, r *http.Request) *absConn {
	ac, err := s.absFor(r.Context(), userFrom(r.Context()))
	if err != nil {
		if isNoABS(err) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "not_connected"})
			return nil
		}
		s.fail(w, err, "audiobookshelf")
		return nil
	}
	return ac
}

// absErr writes a failed Audiobookshelf call.
func (s *Server) absErr(w http.ResponseWriter, err error) {
	switch {
	case abs.IsNotFound(err):
		writeErr(w, http.StatusNotFound, "not found")
	case abs.IsAuth(err):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "not_connected"})
	case errors.Is(err, context.Canceled):
	default:
		s.log.Warn("audiobookshelf", "err", err)
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}

// ---- Settings → Audiobookshelf ----

func (s *Server) handleConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if s.cfg.Backend == "native" {
		_, err := s.catalog().Account(ctx, u.ID)
		writeJSON(w, http.StatusOK, map[string]any{"connected": err == nil, "kind": "native", "mode": "native", "username": u.Name, "server_url": false, "backend": "native", "core": s.coreConnected(), "core_configured": s.core.Configured(), "sync": s.store.GetSync(ctx, u.ID)})
		return
	}
	c, _ := s.store.GetConnection(ctx, u.ID)
	out := map[string]any{
		"server_url":      s.cfg.ABSURL != "",
		"core":            s.coreConnected(),
		"core_configured": s.core.Configured(),
		"sync":            s.store.GetSync(ctx, u.ID),
		"syncing":         s.isSyncing(u.ID),
	}
	mode := "core"
	if c != nil {
		mode = c.Kind
		out["username"] = c.Username
		out["custom_url"] = c.ServerURL
	} else if !s.core.Configured() {
		mode = "key"
	}
	out["mode"] = mode
	custom := ""
	if c != nil {
		custom = c.ServerURL
	}
	if base := firstNonEmpty(custom, s.cfg.ABSURL); base != "" {
		sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		if st, err := s.newABS(base, "").Status(sctx); err == nil {
			out["password_login"] = st.LocalLogin()
			out["server_version"] = st.ServerVersion
		}
		cancel()
	}
	ac, err := s.absFor(ctx, u)
	if err != nil {
		out["connected"] = false
		out["message"] = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	me, err := ac.c.Me(ctx)
	if err != nil {
		out["connected"] = false
		out["message"] = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["connected"] = true
	out["via"] = ac.via
	out["account"] = me.Username
	out["admin"] = me.Type == "root" || me.Type == "admin"
	if libs, err := s.bookLibraries(ctx, ac, true); err == nil {
		out["libraries"] = libs
	}
	out["in_progress"] = countInProgress(me)
	out["public_url"] = ac.public
	writeJSON(w, http.StatusOK, out)
}

func countInProgress(me *abs.Me) int {
	n := 0
	for _, p := range me.MediaProgress {
		if p.EpisodeID == "" && !p.IsFinished && p.CurrentTime > 0 {
			n++
		}
	}
	return n
}

// handleConnect saves an API key, signs in with a password (keeping only
// the refresh token), or switches to Pocket Core / off.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Mode     string `json:"mode"` // key | direct | core | off
		URL      string `json:"url"`
		Key      string `json:"key"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	old, _ := s.store.GetConnection(ctx, u.ID)
	switch in.Mode {
	case "off", "core":
		if in.Mode == "core" && !s.core.Configured() {
			writeErr(w, http.StatusBadRequest, "there is no Pocket Core on this box")
			return
		}
		s.signOutOld(ctx, old)
		if err := s.store.SaveConnection(ctx, &store.Connection{UserID: u.ID, Kind: in.Mode}); err != nil {
			s.fail(w, err, "save connection")
			return
		}
	case "key":
		base, custom, ok := s.pickBase(w, in.URL)
		if !ok {
			return
		}
		key := strings.TrimSpace(in.Key)
		if key == "" || strings.Contains(key, "••") {
			writeErr(w, http.StatusBadRequest, "paste an API key of your Audiobookshelf account (Settings → API Keys in Audiobookshelf)")
			return
		}
		me, err := s.newABS(base, key).Me(ctx)
		if err != nil {
			if abs.IsAuth(err) {
				err = errors.New("Audiobookshelf refused this key: it is wrong, expired or switched off")
			}
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		sealed, err := s.seal(key)
		if err != nil {
			s.fail(w, err, "seal")
			return
		}
		s.signOutOld(ctx, old)
		conn := &store.Connection{UserID: u.ID, Kind: "key", ServerURL: custom, Username: me.Username, ABSUserID: me.ID, Secret: sealed}
		if err := s.store.SaveConnection(ctx, conn); err != nil {
			s.fail(w, err, "save connection")
			return
		}
	case "direct":
		_, custom, ok := s.pickBase(w, in.URL)
		if !ok {
			return
		}
		in.Username = strings.TrimSpace(in.Username)
		if in.Username == "" {
			writeErr(w, http.StatusBadRequest, "enter your Audiobookshelf username")
			return
		}
		res, err := s.newABS(firstNonEmpty(custom, s.cfg.ABSURL), "").Login(ctx, in.Username, in.Password)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.saveSignIn(ctx, u.ID, old, custom, res); err != nil {
			s.fail(w, err, "save connection")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "mode is key, direct, core or off")
		return
	}
	s.historySoon(u.ID)
	s.handleConnection(w, r)
}

// pickBase is the Audiobookshelf address a sign-in goes to (the box's, or one typed in).
func (s *Server) pickBase(w http.ResponseWriter, raw string) (base, custom string, ok bool) {
	custom = strings.TrimRight(strings.TrimSpace(raw), "/")
	if custom != "" {
		if err := validServerURL(custom); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return "", "", false
		}
	}
	base = firstNonEmpty(custom, s.cfg.ABSURL)
	if base == "" {
		writeErr(w, http.StatusBadRequest, "enter the Audiobookshelf address")
		return "", "", false
	}
	return base, custom, true
}

func (s *Server) saveSignIn(ctx context.Context, uid int64, old *store.Connection, custom string, res *abs.LoginResult) error {
	kind, secret := "direct", res.RefreshToken
	if secret == "" {
		// a server before 2.26 hands out one long-lived token: keep it as a key
		kind, secret = "key", res.AccessToken
	}
	sealedR, err := s.seal(secret)
	if err != nil {
		return err
	}
	sealedA := ""
	if kind == "direct" {
		if sealedA, err = s.seal(res.AccessToken); err != nil {
			return err
		}
	}
	s.signOutOld(ctx, old)
	return s.store.SaveConnection(ctx, &store.Connection{UserID: uid, Kind: kind, ServerURL: custom, Username: res.User.Username, ABSUserID: res.User.ID, Secret: sealedR, Access: sealedA})
}

// signOutOld ends the refresh token of a password sign-in this app received.
func (s *Server) signOutOld(ctx context.Context, old *store.Connection) {
	if old == nil || old.Kind != "direct" || old.Secret == "" {
		return
	}
	rt, err := s.open(old.Secret)
	if err != nil || rt == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = s.newABS(s.serverBase(old), "").Logout(cctx, rt)
}
