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
	for _, lib := range libs {
		jobs := []any{}
		if s.catalog().CanScan(ctx, uid, lib.ID) == nil {
			rows, err := s.store.DB.QueryContext(ctx, `SELECT id,status,started_at,finished_at,report FROM catalog_scan_runs WHERE library_id=? ORDER BY started_at DESC LIMIT 5`, lib.ID)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			for rows.Next() {
				var id, status, report string
				var started int64
				var ended *int64
				if err = rows.Scan(&id, &status, &started, &ended, &report); err != nil {
					rows.Close()
					s.nativeError(w, err)
					return
				}
				jobs = append(jobs, nativeObject{"id": id, "status": status, "started_at": started, "finished_at": ended, "report": json.RawMessage(report)})
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				s.nativeError(w, err)
				return
			}
		}
		var watch, interval int
		_ = s.store.DB.QueryRowContext(ctx, `SELECT enabled,interval_seconds FROM catalog_scan_settings WHERE library_id=?`, lib.ID).Scan(&watch, &interval)
		out = append(out, nativeObject{"watch": watch == 1, "interval_seconds": interval, "id": lib.ID, "name": lib.Name, "media_type": lib.MediaType, "count": lib.Count, "can_scan": s.catalog().CanScan(ctx, uid, lib.ID) == nil, "jobs": jobs})
	}
	writeJSON(w, 200, nativeObject{"libraries": out})
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
	var in struct {
		Enabled  bool `json:"enabled"`
		Interval int  `json:"interval_seconds"`
	}
	if err := decode(r, &in); err != nil || in.Interval < 60 || in.Interval > 86400 {
		writeErr(w, 400, "scan interval must be between one minute and one day")
		return
	}
	_, err := s.store.DB.ExecContext(r.Context(), `INSERT INTO catalog_scan_settings VALUES(?,?,?,0,?) ON CONFLICT(library_id) DO UPDATE SET interval_seconds=excluded.interval_seconds,enabled=excluded.enabled,actor_id=excluded.actor_id`, id, in.Interval, in.Enabled, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, nativeObject{"ok": true})
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
		rows, err := s.store.DB.QueryContext(ctx, `SELECT library_id,actor_id FROM catalog_scan_settings WHERE enabled=1 AND NOT EXISTS(SELECT 1 FROM catalog_library_state s WHERE s.library_id=catalog_scan_settings.library_id AND s.archived=1) AND last_started+interval_seconds<=?`, time.Now().Unix())
		if err != nil {
			continue
		}
		type due struct {
			id  string
			uid int64
		}
		all := []due{}
		for rows.Next() {
			var d due
			if rows.Scan(&d.id, &d.uid) == nil {
				all = append(all, d)
			}
		}
		rows.Close()
		for _, d := range all {
			if ctx.Err() != nil {
				return
			}
			var running int
			_ = s.store.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog_scan_runs WHERE library_id=? AND status='running'`, d.id).Scan(&running)
			if running > 0 {
				continue
			}
			_, err = s.store.DB.ExecContext(ctx, `UPDATE catalog_scan_settings SET last_started=? WHERE library_id=?`, time.Now().Unix(), d.id)
			if err != nil {
				continue
			}
			if _, err = s.startNativeScan(d.uid, d.id, false); err != nil {
				s.log.Warn("scheduled native scan did not start", "library", d.id, "error", err)
			}
		}
	}
}
