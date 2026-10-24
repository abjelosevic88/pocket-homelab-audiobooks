// Package server wires the HTTP API, auth and the embedded SPA.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/config"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// Version is injected at build time.
var Version = "dev"

// Server holds dependencies.
type Server struct {
	cfg        *config.Config
	store      *store.Store
	log        *slog.Logger
	webFS      fs.FS
	started    time.Time
	requests   atomic.Int64
	errors     atomic.Int64
	absHTTP    *http.Client // API calls to Audiobookshelf
	streamHTTP *http.Client // files and HLS: no overall timeout, a book plays for hours

	oidcMu sync.Mutex
	oidc   *oidcProvider // for the issuer in use; rebuilt when it changes

	core    *core.Client
	coreMu  sync.Mutex
	coreOK  bool
	coreErr string

	syncMu   sync.Mutex
	syncing  map[int64]bool      // people whose history is being read
	lastSync map[int64]time.Time // when a read was last asked for

	refreshMu  sync.Mutex
	refreshing map[int64]*sync.Mutex // one token refresh at a time per person

	coreUIDMu sync.Mutex
	coreUID   map[string]string // Core-held key → the Audiobookshelf user it belongs to

	playMu sync.Mutex
	plays  map[string]*playState // playback session → what is playing

	mux http.Handler // the router, for /audiobookshelf/… (absgateway.go)
}

// New creates a server.
func New(cfg *config.Config, st *store.Store, log *slog.Logger, webFS fs.FS) *Server {
	return &Server{cfg: cfg, store: st, log: log, webFS: webFS, started: time.Now(), oidc: &oidcProvider{},
		absHTTP:    &http.Client{Timeout: 30 * time.Second},
		streamHTTP: &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 60 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 16}},
		core:       core.New(cfg.CoreURL, core.Options{DB: st.DB, TokenFile: cfg.DataDir + "/core.token", UserAgent: cfg.UserAgent}),
		syncing:    map[int64]bool{}, lastSync: map[int64]time.Time{}, refreshing: map[int64]*sync.Mutex{}, coreUID: map[string]string{}, plays: map[string]*playState{}}
}

// Bootstrap seeds the first admin (if configured), the box's services and
// the owner's connections from the environment and demo data, then starts
// the background work.
func (s *Server) Bootstrap(ctx context.Context) error {
	if err := s.core.Start(ctx); err != nil {
		return err
	}
	if err := s.loadSecretKey(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.cfg.FilesDir, 0o755); err != nil {
		return fmt.Errorf("files dir: %w", err)
	}
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 && s.cfg.AuthMode == "local" && s.cfg.AdminEmail != "" && s.cfg.AdminPassword != "" {
		if len(s.cfg.AdminPassword) < 8 {
			return errors.New("ADMIN_PASSWORD must be at least 8 characters")
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
		u := &store.User{Email: s.cfg.AdminEmail, Name: s.cfg.AdminName, PasswordHash: string(h), Role: "admin"}
		if err := s.store.CreateUser(ctx, u); err != nil {
			return err
		}
		st, _ := s.store.GetSettings(ctx, 0)
		st.SetupComplete = true
		if err := s.store.SaveBoxSettings(ctx, st); err != nil {
			return err
		}
		s.log.Info("admin user created from environment", "email", u.Email)
	}
	if owner := s.store.OwnerID(ctx); owner > 0 {
		if err := s.ownerReady(ctx, owner); err != nil {
			return err
		}
	}
	_ = s.store.PurgeSessions(ctx)
	go s.janitor(ctx)
	if s.core.Configured() {
		go s.coreLoop(ctx)
	}
	return nil
}

// ownerReady runs once the box owner (the first admin) exists: the
// connections from the environment are the owner's — seeded into the owner's
// own settings only, never into anyone else's — and demo data lands in the
// owner's account.
func (s *Server) ownerReady(ctx context.Context, owner int64) error {
	st, err := s.store.GetSettings(ctx, owner)
	if err != nil {
		return err
	}
	changed := false
	set := func(name string, dst *string, v string) {
		key := fmt.Sprintf("seeded:%d:%s", owner, name)
		if _, _, done := s.store.CacheGet(ctx, key); done || v == "" {
			return
		}
		if *dst == "" {
			*dst = v
			changed = true
		}
		_ = s.store.CachePut(ctx, key, "1")
	}
	set("NotifyURL", &st.NotifyURL, s.cfg.NotifyURL)
	if s.cfg.NotifyFormat != "" && st.NotifyURL == s.cfg.NotifyURL && st.NotifyFormat != s.cfg.NotifyFormat {
		st.NotifyFormat = s.cfg.NotifyFormat
		changed = true
	}
	if changed {
		if err := s.store.SaveUserSettings(ctx, owner, st); err != nil {
			return err
		}
	}
	// the owner's Audiobookshelf account: AUDIOBOOKSHELF_TOKEN, or AUDIOBOOKSHELF_USER + AUDIOBOOKSHELF_PASSWORD
	if s.cfg.ABSURL != "" && (s.cfg.ABSToken != "" || s.cfg.ABSUser != "" && s.cfg.ABSPassword != "") {
		key := fmt.Sprintf("seeded:%d:audiobookshelf", owner)
		if _, _, done := s.store.CacheGet(ctx, key); !done {
			if _, err := s.store.GetConnection(ctx, owner); errors.Is(err, store.ErrNotFound) {
				if err := s.seedOwnerABS(ctx, owner); err != nil {
					// Audiobookshelf may still be starting: try again at the next start
					s.log.Warn("audiobookshelf account from the environment", "err", err)
					return nil
				}
			}
			_ = s.store.CachePut(ctx, key, "1")
		}
	}
	return nil
}

func (s *Server) seedOwnerABS(ctx context.Context, owner int64) error {
	c := s.newABS(s.cfg.ABSURL, s.cfg.ABSToken)
	if s.cfg.ABSToken != "" {
		me, err := c.Me(ctx)
		if err != nil {
			return err
		}
		sealed, err := s.seal(s.cfg.ABSToken)
		if err != nil {
			return err
		}
		if err := s.store.SaveConnection(ctx, &store.Connection{UserID: owner, Kind: "key", Username: me.Username, ABSUserID: me.ID, Secret: sealed}); err != nil {
			return err
		}
		s.log.Info("audiobookshelf account seeded from the environment", "user", me.Username, "kind", "key")
		return nil
	}
	res, err := c.Login(ctx, s.cfg.ABSUser, s.cfg.ABSPassword)
	if err != nil {
		return err
	}
	if err := s.saveSignIn(ctx, owner, nil, "", res); err != nil {
		return err
	}
	s.log.Info("audiobookshelf account seeded from the environment", "user", res.User.Username, "kind", "direct")
	return nil
}

// forgetUser drops in-memory state of a deleted account.
func (s *Server) forgetUser(id int64) {
	s.playMu.Lock()
	for k, p := range s.plays {
		if p.uid == id {
			delete(s.plays, k)
		}
	}
	s.playMu.Unlock()
}

// janitor runs the background work: every SYNC_EVERY each connected person's
// history is read from Audiobookshelf and quiet players are let go; on Monday
// mornings the weekly recaps go out.
func (s *Server) janitor(ctx context.Context) {
	every := s.cfg.SyncEvery
	if every <= 0 {
		every = 5 * time.Minute
	}
	first := time.NewTimer(20 * time.Second)
	tick := time.NewTicker(every)
	slow := time.NewTicker(15 * time.Minute)
	defer tick.Stop()
	defer slow.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			s.syncAll(ctx)
		case <-tick.C:
			s.syncAll(ctx)
			s.sweepPlays(ctx)
		case <-slow.C:
			s.weeklyRecaps(ctx)
		}
	}
}

// ---- Pocket Core ----

// coreLoop registers with Core on start (retrying until it answers) and
// re-registers every 30 minutes so version and last-seen stay fresh.
func (s *Server) coreLoop(ctx context.Context) {
	wait := 5 * time.Second
	for {
		err := s.registerWithCore(ctx)
		s.coreMu.Lock()
		s.coreOK, s.coreErr = err == nil, ""
		if err != nil {
			s.coreErr = err.Error()
		}
		s.coreMu.Unlock()
		if err != nil {
			s.log.Warn("pocket core: registration failed, retrying", "err", err, "in", wait.String())
		} else {
			wait = 30 * time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err != nil && wait < 5*time.Minute {
			wait *= 2
		}
	}
}

// eventTopics are what Audiobooks publishes on the Core bus.
var eventTopics = []string{"audiobooks.finished"}

func (s *Server) registerWithCore(ctx context.Context) error {
	m := core.Manifest{
		Slug: "audiobooks", Name: "Pocket Audiobooks",
		Description: "Audiobooks for the whole household: Audiobookshelf underneath, with a calm web player that keeps your place (continue on the phone, continue here), chapters, bookmarks, speed and a sleep timer, series and authors, up next, and a listening history it owns with stats and streaks.",
		Icon:        "/favicon.svg", Version: Version,
		URL: s.cfg.CoreAppURL, PublicURL: s.cfg.BaseURL, HealthPath: "/healthz", EnvPrefix: "AUDIOBOOKS",
		Publishes: eventTopics,
		Offers:    linkOffers,
		Services: []core.ServiceUse{
			{Type: "audiobookshelf", Reason: "Your audiobooks: Pocket Audiobooks plays them with your own Audiobookshelf account, so your place, finished books and bookmarks stay yours in every app"},
		},
	}
	if s.cfg.BaseURL != "" {
		// ask Core for a relying-party client so "Sign in with Pocket Core" needs no configuration
		m.OIDC = &core.OIDCRequest{RedirectURIs: []string{s.oidcCallbackURL()}}
	}
	reg, err := s.core.Register(ctx, s.cfg.CoreRegistrationKey, m)
	if err != nil {
		return err
	}
	if reg.Token != "" {
		s.log.Info("pocket core: registered, app token stored", "core", s.cfg.CoreURL)
	} else {
		s.log.Debug("pocket core: re-registered", "core", s.cfg.CoreURL)
	}
	return nil
}

// coreStatus summarises the Pocket Core link for the About page.
func (s *Server) coreStatus() map[string]any {
	s.coreMu.Lock()
	defer s.coreMu.Unlock()
	return map[string]any{"configured": s.core.Configured(), "url": s.cfg.CoreURL, "registered": s.coreOK, "error": s.coreErr}
}

// coreConnected is "configured AND registered": only then are Core-only choices live.
func (s *Server) coreConnected() bool {
	s.coreMu.Lock()
	defer s.coreMu.Unlock()
	return s.core.Configured() && s.coreOK
}

// publish puts an event on the Core bus for one person (no-op without Core).
func (s *Server) publish(ctx context.Context, u *store.User, topic, subject string, payload map[string]any) {
	if !s.core.Configured() || u == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["user_email"] = u.Email
	if err := s.core.Publish(ctx, topic, subject, payload); err != nil {
		s.log.Debug("pocket core: publish failed", "topic", topic, "err", err)
	}
}
