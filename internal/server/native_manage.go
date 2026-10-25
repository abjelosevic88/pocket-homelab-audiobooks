package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
)

func (s *Server) nativeManage(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native library management is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	libs, err := s.catalog().Libraries(ctx, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	out := []any{}
	now := time.Now()
	for _, lib := range libs {
		jobs := []any{}
		if s.catalog().CanScan(ctx, uid, lib.ID) == nil {
			rows, err := s.store.DB.QueryContext(ctx, `SELECT id,status,started_at,finished_at,report,origin FROM catalog_scan_runs WHERE library_id=? ORDER BY started_at DESC LIMIT 5`, lib.ID)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			for rows.Next() {
				var id, status, report, origin string
				var started int64
				var ended *int64
				if err = rows.Scan(&id, &status, &started, &ended, &report, &origin); err != nil {
					rows.Close()
					s.nativeError(w, err)
					return
				}
				jobs = append(jobs, nativeObject{"id": id, "status": status, "started_at": started, "finished_at": ended, "report": json.RawMessage(report), "origin": origin})
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				s.nativeError(w, err)
				return
			}
		}
		settings, _ := s.loadScanSettings(ctx, lib.ID)
		entry := nativeObject{"watch": settings.enabled, "interval_seconds": settings.interval, "window_start": formatClock(settings.schedule.WindowStart), "window_end": formatClock(settings.schedule.WindowEnd), "next_scan_at": nil, "last_auto_started_at": nil, "id": lib.ID, "name": lib.Name, "media_type": lib.MediaType, "count": lib.Count, "can_scan": s.catalog().CanScan(ctx, uid, lib.ID) == nil, "jobs": jobs}
		if settings.lastStarted > 0 {
			entry["last_auto_started_at"] = settings.lastStarted * 1000
		}
		if settings.enabled {
			entry["next_scan_at"] = settings.schedule.next(settings.last(), now).UnixMilli()
		}
		out = append(out, entry)
	}
	zone, offset := now.Zone()
	writeJSON(w, 200, nativeObject{"libraries": out, "timezone": nativeObject{"name": time.Local.String(), "abbreviation": zone, "offset_seconds": offset}})
}
func (s *Server) startNativeScan(uid int64, lib string, force bool, sidecars ...bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	started := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		defer cancel()
		var id string
		report, err := s.catalog().Scan(ctx, uid, lib, library.ScanOptions{FFprobe: s.cfg.FFprobe, FFmpeg: s.cfg.FFmpeg, ArtDir: filepath.Join(s.cfg.DataDir, "native-art"), Force: force, ImportMetadata: len(sidecars) > 0 && sidecars[0], Started: func(value string) {
			id = value
			s.nativeScanMu.Lock()
			s.nativeScanCancel[id] = cancel
			s.nativeScanMu.Unlock()
			started <- id
		}})
		s.nativeScanMu.Lock()
		delete(s.nativeScanCancel, id)
		s.nativeScanMu.Unlock()
		if err != nil {
			s.log.Warn("native library scan", "library", lib, "error", err)
		} else {
			s.log.Info("native library scan complete", "library", lib, "added", report.Added, "updated", report.Updated, "missing", report.Missing, "quarantined", report.Quarantined)
			s.nativeScanComplete(lib, id, report.Added, report.Updated, report.Missing)
		}
		done <- err
	}()
	select {
	case id := <-started:
		return id, nil
	case err := <-done:
		// A small scan can finish before the caller is scheduled again.
		select {
		case id := <-started:
			return id, nil
		default:
		}
		if err == nil {
			err = errors.New("scan did not start")
		}
		return "", err
	}
}
func (s *Server) nativeScanStart(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native library management is not active")
		return
	}
	var in struct {
		Force          bool `json:"force"`
		ImportMetadata bool `json:"import_metadata"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	id, err := s.startNativeScan(actorID(r.Context()), chi.URLParam(r, "id"), in.Force, in.ImportMetadata)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 202, nativeObject{"id": id, "status": "running"})
}
func (s *Server) nativeScanCancelJob(w http.ResponseWriter, r *http.Request) {
	var lib string
	id := chi.URLParam(r, "job")
	if err := s.store.DB.QueryRowContext(r.Context(), `SELECT library_id FROM catalog_scan_runs WHERE id=?`, id).Scan(&lib); err != nil {
		writeErr(w, 404, "scan not found")
		return
	}
	if err := s.catalog().CanScan(r.Context(), actorID(r.Context()), lib); err != nil {
		s.nativeError(w, err)
		return
	}
	s.nativeScanMu.Lock()
	cancel := s.nativeScanCancel[id]
	s.nativeScanMu.Unlock()
	if cancel != nil {
		cancel()
	}
	writeJSON(w, 200, nativeObject{"ok": true})
}

func (s *Server) nativeLibraryCreate(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" || userFrom(r.Context()).Role != "admin" {
		writeErr(w, 403, "library administration requires an administrator")
		return
	}
	var in library.NewLibrary
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	id, err := s.catalog().CreateLibrary(r.Context(), actorID(r.Context()), in)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 201, nativeObject{"id": id})
}
func (s *Server) nativeEditor(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native library editing is not active")
		return
	}
	uid := actorID(r.Context())
	id := chi.URLParam(r, "id")
	a, err := s.catalog().Account(r.Context(), uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if !a.Permissions.Update {
		writeErr(w, 404, "not found")
		return
	}
	if r.Method == "PUT" {
		var in library.EditOptions
		if err = decode(r, &in); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
		if err = s.catalog().Edit(r.Context(), uid, id, in); err != nil {
			s.nativeError(w, err)
			return
		}
	}
	it, err := s.catalog().Item(r.Context(), uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	wire, err := s.nativeItem(r.Context(), uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	metadata := nativeMetadata(wire)
	metadata["tags"] = it.Tags
	// Editor sends only supported metadata fields, preserving unknown source data.
	editable := nativeObject{}
	for _, key := range []string{"title", "subtitle", "description", "publisher", "publishedYear", "publishedDate", "isbn", "asin", "language", "explicit", "abridged", "genres", "narrators", "tags", "authors", "series"} {
		value := metadata[key]
		if value == nil {
			switch key {
			case "authors", "series", "genres", "tags", "narrators":
				value = []any{}
			case "explicit", "abridged":
				value = false
			default:
				value = ""
			}
		}
		editable[key] = value
	}
	writeJSON(w, 200, nativeObject{"expected_updated_at": it.UpdatedAt, "metadata": editable, "chapters": nativeMap(wire["media"])["chapters"]})
}

// scanSettingsInput is the body of the watch endpoints.
type scanSettingsInput struct {
	Enabled     bool   `json:"enabled"`
	Interval    int    `json:"interval_seconds"`
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
}

// parse validates the input and returns window bounds (-1 when unset).
func (in scanSettingsInput) parse() (int, int, error) {
	if in.Interval < 60 || in.Interval > 86400 {
		return 0, 0, errors.New("scan interval must be between one minute and one day")
	}
	start, err := parseClock(in.WindowStart)
	if err != nil {
		return 0, 0, errors.New("window_start must be HH:MM")
	}
	end, err := parseClock(in.WindowEnd)
	if err != nil {
		return 0, 0, errors.New("window_end must be HH:MM")
	}
	if (start < 0) != (end < 0) {
		return 0, 0, errors.New("set both window_start and window_end, or neither")
	}
	if start >= 0 && start == end {
		return 0, 0, errors.New("the scan window must not start and end at the same time")
	}
	return start, end, nil
}

func nullableClock(v int) any {
	if v < 0 {
		return nil
	}
	return v
}

func (s *Server) saveScanSettings(ctx context.Context, uid int64, lib string, in scanSettingsInput, start, end int) error {
	_, err := s.store.DB.ExecContext(ctx, `INSERT INTO catalog_scan_settings(library_id,interval_seconds,enabled,last_started,actor_id,window_start,window_end) VALUES(?,?,?,0,?,?,?) ON CONFLICT(library_id) DO UPDATE SET interval_seconds=excluded.interval_seconds,enabled=excluded.enabled,actor_id=excluded.actor_id,window_start=excluded.window_start,window_end=excluded.window_end`, lib, in.Interval, in.Enabled, uid, nullableClock(start), nullableClock(end))
	return err
}

func (s *Server) nativeWatchSettings(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native library management is not active")
		return
	}
	uid := actorID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.catalog().CanScan(r.Context(), uid, id); err != nil {
		s.nativeError(w, err)
		return
	}
	var in scanSettingsInput
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	start, end, err := in.parse()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err = s.saveScanSettings(r.Context(), uid, id, in, start, end); err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, nativeObject{"ok": true})
}

// nativeWatchAll applies one schedule to every library the person can scan.
func (s *Server) nativeWatchAll(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native library management is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	var in scanSettingsInput
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	start, end, err := in.parse()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	libs, err := s.catalog().Libraries(ctx, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	updated := []string{}
	for _, lib := range libs {
		if s.catalog().CanScan(ctx, uid, lib.ID) != nil {
			continue
		}
		if err = s.saveScanSettings(ctx, uid, lib.ID, in, start, end); err != nil {
			s.nativeError(w, err)
			return
		}
		updated = append(updated, lib.ID)
	}
	writeJSON(w, 200, nativeObject{"ok": true, "libraries": updated})
}

// scanSettingsRow is one library's stored automatic scan configuration.
type scanSettingsRow struct {
	enabled     bool
	interval    int
	lastStarted int64 // unix seconds
	actor       int64
	schedule    scanSchedule
}

func (s *Server) loadScanSettings(ctx context.Context, lib string) (scanSettingsRow, bool) {
	var row scanSettingsRow
	var start, end *int
	err := s.store.DB.QueryRowContext(ctx, `SELECT enabled,interval_seconds,last_started,actor_id,window_start,window_end FROM catalog_scan_settings WHERE library_id=?`, lib).Scan(&row.enabled, &row.interval, &row.lastStarted, &row.actor, &start, &end)
	row.schedule = scanSchedule{Interval: time.Duration(row.interval) * time.Second, WindowStart: -1, WindowEnd: -1}
	if start != nil && end != nil {
		row.schedule.WindowStart, row.schedule.WindowEnd = *start, *end
	}
	return row, err == nil
}

func (row scanSettingsRow) last() time.Time {
	if row.lastStarted <= 0 {
		return time.Time{}
	}
	return time.Unix(row.lastStarted, 0)
}

// libraryBusy reports why an automatic scan must wait: a scan already runs,
// or a quick match / author job in this process is editing its books.
func (s *Server) libraryBusy(ctx context.Context, lib string) string {
	var running int
	_ = s.store.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_scan_runs WHERE library_id=? AND status='running'`, lib).Scan(&running)
	if running > 0 {
		return "scan"
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id FROM catalog_match_jobs WHERE status IN ('queued','running') AND (library_id=? OR library_id='')`, lib)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			continue
		}
		matchRuns.Lock()
		_, alive := matchRuns.cancel[id]
		matchRuns.Unlock()
		if alive {
			return "match"
		}
	}
	return ""
}

func (s *Server) nativeScanLoop(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	defer func() {
		s.nativeScanMu.Lock()
		defer s.nativeScanMu.Unlock()
		for _, cancel := range s.nativeScanCancel {
			cancel()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s.runDueScans(ctx, time.Now())
	}
}

// runDueScans starts every automatic scan due at now.
func (s *Server) runDueScans(ctx context.Context, now time.Time) []string {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT library_id FROM catalog_scan_settings WHERE enabled=1 AND NOT EXISTS(SELECT 1 FROM catalog_library_state s WHERE s.library_id=catalog_scan_settings.library_id AND s.archived=1)`)
	if err != nil {
		return nil
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	started := []string{}
	for _, id := range ids {
		if ctx.Err() != nil {
			return started
		}
		row, ok := s.loadScanSettings(ctx, id)
		if !ok || !row.schedule.due(row.last(), now) {
			continue
		}
		if busy := s.libraryBusy(ctx, id); busy != "" {
			s.log.Debug("scheduled native scan waits", "library", id, "busy", busy)
			continue
		}
		if _, err = s.store.DB.ExecContext(ctx, `UPDATE catalog_scan_settings SET last_started=? WHERE library_id=?`, now.Unix(), id); err != nil {
			continue
		}
		run, err := s.startNativeScan(row.actor, id, false)
		if err != nil {
			s.log.Warn("scheduled native scan did not start", "library", id, "error", err)
			continue
		}
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE catalog_scan_runs SET origin='auto' WHERE id=?`, run)
		started = append(started, run)
	}
	return started
}
