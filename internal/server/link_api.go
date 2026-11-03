package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

// What another Pocket app may do here through a Pocket Core link, besides
// reading listening (links.go): look for a book among the person's
// audiobooks (library:read) and, after it put a new book into a library's
// folder, have that library scanned (library:scan). Pocket Requests uses both
// to file the audiobooks it fetched.

// handleLinkFind answers GET /api/v1/link/find?q= or ?title=&author=.
func (s *Server) handleLinkFind(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, http.StatusNotFound, "the audiobook catalog is not active")
		return
	}
	v := r.URL.Query()
	q := library.FindQuery{Q: strings.TrimSpace(v.Get("q")), Title: strings.TrimSpace(v.Get("title")), Author: strings.TrimSpace(v.Get("author")), Limit: queryInt(r, "limit")}
	if q.Q == "" && q.Title == "" {
		writeErr(w, http.StatusBadRequest, "say what to look for (q, or title and author)")
		return
	}
	if len(q.Q) > 300 || len(q.Title) > 300 || len(q.Author) > 300 {
		writeErr(w, http.StatusBadRequest, "too long")
		return
	}
	items, err := s.catalog().FindBooks(r.Context(), actorID(r.Context()), q)
	if errors.Is(err, library.ErrNotFound) {
		items, err = []library.FoundBook{}, nil // no audiobook account yet: nothing to find
	}
	if err != nil {
		s.catalogError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleLinkScan answers POST /api/v1/link/scan {"path": "..."}: the library
// whose folder holds path is scanned now, or right after the scan that is
// running (202 either way).
func (s *Server) handleLinkScan(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, http.StatusNotFound, "the audiobook catalog is not active")
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(r, &in); err != nil || in.Path == "" {
		writeErr(w, http.StatusBadRequest, "say which folder (path)")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	lib, err := s.catalog().LibraryForPath(ctx, uid, in.Path)
	switch {
	case errors.Is(err, library.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "path must be absolute, without ..")
		return
	case errors.Is(err, library.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no audiobook library of yours holds that folder")
		return
	case err != nil:
		s.catalogError(w, err)
		return
	}
	actor := s.linkScanActor(ctx, uid, lib)
	if actor == 0 {
		writeErr(w, http.StatusConflict, "nobody may scan that library yet (set up its scan schedule once)")
		return
	}
	started := false
	if s.libraryBusy(ctx, lib) == "" && !s.anyScanRunning(ctx) {
		if _, err := s.startNativeScan(actor, lib, false); err == nil {
			started = true
		}
	}
	if !started {
		s.nativeScanMu.Lock()
		if s.linkScans == nil {
			s.linkScans = map[string]int64{}
		}
		s.linkScans[lib] = actor
		s.nativeScanMu.Unlock()
	}
	s.log.Info("scan asked for by a linked app", "app", linkApp(ctx), "library", lib, "started", started)
	writeJSON(w, http.StatusAccepted, map[string]any{"library_id": lib, "started": started})
}

// linkScanActor is whom the scan runs as: the person when they may scan the
// library, else whoever its automatic scans run as (the scan only reads the
// library's own folders; the person may open the library).
func (s *Server) linkScanActor(ctx context.Context, uid int64, lib string) int64 {
	if s.catalog().CanScan(ctx, uid, lib) == nil {
		return uid
	}
	var actor int64
	_ = s.store.DB.QueryRowContext(ctx, `SELECT actor_id FROM catalog_scan_settings WHERE library_id=?`, lib).Scan(&actor)
	if actor > 0 && s.catalog().CanScan(ctx, actor, lib) == nil {
		return actor
	}
	return 0
}

func (s *Server) anyScanRunning(ctx context.Context) bool {
	var running int
	return s.store.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_scan_runs WHERE status='running'`).Scan(&running) != nil || running > 0
}

// runLinkScans starts one scan a linked app asked for while another ran
// (called every scheduler tick before the automatic scans).
func (s *Server) runLinkScans(ctx context.Context) []string {
	s.nativeScanMu.Lock()
	var lib string
	var actor int64
	for l, a := range s.linkScans {
		lib, actor = l, a
		break
	}
	s.nativeScanMu.Unlock()
	if lib == "" || s.anyScanRunning(ctx) || s.libraryBusy(ctx, lib) != "" {
		return nil
	}
	run, err := s.startNativeScan(actor, lib, false)
	if err != nil {
		s.log.Debug("linked scan waits", "library", lib, "error", err)
		return nil
	}
	s.nativeScanMu.Lock()
	delete(s.linkScans, lib)
	s.nativeScanMu.Unlock()
	return []string{run}
}

func linkApp(ctx context.Context) string {
	app, _ := ctx.Value(linkAppKey).(string)
	return app
}
