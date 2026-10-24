package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID)
	r.Use(s.logging)
	r.Use(middleware.Recoverer)

	r.Get("/pocket-ask.js", s.core.AskWidget("audiobooks"))
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Get("/api/health", s.handleHealth)
	if s.cfg.MetricsEnabled {
		r.Get("/metrics", s.handleMetrics)
	}

	r.Route("/api/v1", func(r chi.Router) {
		// Long transfers: uploads, audio, playlists and covers bypass timeout/compression.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/catalog/items/{id}/files/{file}", s.handleCatalogFile)
			r.Head("/catalog/items/{id}/files/{file}", s.handleCatalogFile)
			r.Post("/upload", s.handleUpload)
			r.Post("/catalog/items/{id}/cover", s.nativeCoverUpload)
			r.Get("/media/*", s.handleMedia)
			r.Head("/media/*", s.handleMedia)
			r.Get("/cover/{id}", s.handleCover)
			r.Get("/author-image/{id}", s.handleAuthorImage)
		})

		r.Group(func(r chi.Router) {
			r.Use(middleware.Compress(5))
			r.Use(middleware.Timeout(120 * time.Second))
			r.Post("/auth/login", s.handleLogin)
			r.Post("/auth/setup", s.handleSetup)
			r.Get("/auth/status", s.handleAuthStatus)
			r.Get("/auth/oidc/start", s.handleOIDCStart)
			r.Get("/auth/oidc/callback", s.handleOIDCCallback)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAuth)
				r.Post("/auth/logout", s.handleLogout)
				r.Get("/auth/me", s.handleMe)
				r.Put("/auth/me", s.handleUpdateMe)
				r.Post("/client-error", s.handleClientError)

				r.Get("/connection", s.handleConnection)
				r.Put("/connection", s.handleConnect)
				r.Get("/sync", s.handleSyncStatus)
				r.Post("/sync", s.handleSyncNow)

				r.Get("/home", s.handleHome)
				r.Get("/dashboard", s.handleDashboard)
				r.Get("/day/{date}", s.handleDay)

				r.Get("/catalog/manage", s.nativeManage)
				r.Post("/catalog/libraries", s.nativeLibraryCreate)
				r.Get("/catalog/items/{id}/editor", s.nativeEditor)
				r.Put("/catalog/items/{id}/editor", s.nativeEditor)
				r.Post("/catalog/libraries/{id}/scan", s.nativeScanStart)
				r.Put("/catalog/libraries/{id}/watch", s.nativeWatchSettings)
				r.Delete("/catalog/scans/{job}", s.nativeScanCancelJob)
				r.Get("/catalog/libraries", s.handleCatalogLibraries)
				r.Get("/catalog/items", s.handleCatalogItems)
				r.Get("/catalog/items/{id}", s.handleCatalogItem)
				r.Get("/catalog/me/progress", s.handleCatalogProgress)
				r.Get("/libraries", s.handleLibraries)
				r.Get("/upload", s.handleUploadOptions)
				r.Get("/books", s.handleBooks)
				r.Get("/books/{id}", s.handleBook)
				r.Post("/books/{id}/progress", s.handleProgress)
				r.Post("/books/{id}/bookmarks", s.handleAddBookmark)
				r.Put("/books/{id}/bookmarks", s.handleEditBookmark)
				r.Delete("/books/{id}/bookmarks", s.handleDeleteBookmark)
				r.Get("/series", s.handleSeriesList)
				r.Get("/series/{id}", s.handleSeries)
				r.Get("/authors", s.handleAuthors)
				r.Get("/authors/{id}", s.handleAuthor)
				r.Get("/search", s.handleSearch)

				r.Post("/play/{id}", s.handlePlay)
				r.Post("/sessions/{sid}/sync", s.handleSync)
				r.Get("/next", s.handleNext)
				r.Get("/now-playing", s.handleNowPlaying)

				r.Get("/up-next", s.handleUpNext)
				r.Post("/up-next", s.handleAddUpNext)
				r.Put("/up-next", s.handleReorderUpNext)
				r.Delete("/up-next/{id}", s.handleRemoveUpNext)

				r.Get("/history", s.handleHistory)
				r.Delete("/history/{id}", s.handleDeleteListen)
				r.Get("/finished", s.handleFinished)
				r.Get("/stats", s.handleStats)

				r.Post("/integrations/test", s.handleIntegrationTest)

				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateUser)
				r.Put("/users/{id}", s.handleUpdateUser)
				r.Delete("/users/{id}", s.handleDeleteUser)

				r.Get("/settings", s.handleGetSettings)
				r.Put("/settings", s.handlePutSettings)
				r.Get("/settings/system", s.handleSystemInfo)

				r.Get("/tokens", s.handleListTokens)
				r.Post("/tokens", s.handleCreateToken)
				r.Delete("/tokens/{id}", s.handleDeleteToken)

				r.Get("/backup", s.handleBackup)
				r.Get("/export.json", s.handleExportJSON)
				r.Post("/import", s.handleImportJSON)
			})
		})
	})

	// Native Audiobookshelf routes coexist with the web app's /api/v1 API.
	r.Get("/status", s.handleGatewayStatus)
	r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
	})
	r.HandleFunc("/api/*", s.handleGateway)
	r.HandleFunc("/hls/*", s.handleGateway)
	// Sign-in and live updates for third-party Audiobookshelf apps, and every native
	// route under /audiobookshelf too (absgateway.go). GET /login stays the web sign-in page.
	r.Get("/login", s.handleSPA)
	r.Post("/login", s.gwLogin)
	r.Post("/auth/refresh", s.gwRefresh)
	r.Post("/logout", s.gwLogout)
	r.Post("/api/authorize", s.gwAuthorizeRoute)
	r.HandleFunc("/socket.io/*", s.handleABSSocket)
	r.HandleFunc("/audiobookshelf", s.handleABSPrefixed)
	r.HandleFunc("/audiobookshelf/*", s.handleABSPrefixed)

	r.NotFound(s.handleSPA)
	s.mux = r
	return r
}

// ---------- helpers ----------

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.requests.Add(1)
		if ww.Status() >= 500 {
			s.errors.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start).String(), "ip", r.RemoteAddr)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error, context string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, store.ErrConflict), errors.Is(err, core.ErrOperationConflict):
		writeErr(w, http.StatusConflict, "already exists")
		return
	}
	s.log.Error(context, "err", err)
	writeErr(w, http.StatusInternalServerError, context+": "+err.Error())
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20))
	return dec.Decode(v)
}

// decodeKeys decodes JSON into v and also reports which top-level keys were
// present, so PUT handlers can tell "not sent" from "sent empty".
func decodeKeys(r *http.Request, v any) (map[string]bool, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(raw))
	for k := range raw {
		keys[k] = true
	}
	return keys, json.Unmarshal(b, v)
}

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return n
}

func queryInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func queryBool(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true" || v == "yes"
}

func validDate(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// ---------- health / metrics ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"core_outbox": s.coreOutboxHealth(r.Context()), "status": "ok", "version": Version, "uptime": time.Since(s.started).Round(time.Second).String()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts := s.store.Counts(ctx)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP pocket_audiobooks_info Build info.\n# TYPE pocket_audiobooks_info gauge\npocket_audiobooks_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# HELP pocket_audiobooks_uptime_seconds Process uptime.\n# TYPE pocket_audiobooks_uptime_seconds gauge\npocket_audiobooks_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# HELP pocket_audiobooks_http_requests_total HTTP requests served.\n# TYPE pocket_audiobooks_http_requests_total counter\npocket_audiobooks_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# HELP pocket_audiobooks_http_errors_total HTTP 5xx responses.\n# TYPE pocket_audiobooks_http_errors_total counter\npocket_audiobooks_http_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "# HELP pocket_audiobooks_rows Rows per table.\n# TYPE pocket_audiobooks_rows gauge\n")
	for k, v := range counts {
		fmt.Fprintf(w, "pocket_audiobooks_rows{table=%q} %d\n", k, v)
	}
}

// ---------- static / SPA ----------

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.webFS == nil {
		http.Error(w, "web UI not built (run `make web`)", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if f, err := s.webFS.Open(path); err == nil {
		f.Close()
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServerFS(s.webFS).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	r.URL.Path = "/"
	http.FileServerFS(s.webFS).ServeHTTP(w, r)
}

// handleClientError logs a crash the web app caught (ErrorBoundary).
func (s *Server) handleClientError(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Where   string `json:"where"`
		Message string `json:"message"`
		Stack   string `json:"stack"`
		URL     string `json:"url"`
	}
	_ = decode(r, &in)
	s.log.Warn("web app error", "where", trim(in.Where, 60), "message", trim(in.Message, 500), "url", trim(in.URL, 200), "stack", trim(in.Stack, 2000))
	w.WriteHeader(http.StatusNoContent)
}
