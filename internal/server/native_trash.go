package server

import (
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (s *Server) nativeTrash(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native trash is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	c := s.catalog()
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == "GET" {
		list, err := c.TrashList(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, list)
		return
	}
	if item := chi.URLParam(r, "id"); item != "" {
		var in struct {
			Move     bool   `json:"move_files"`
			Revision int64  `json:"revision"`
			Episode  string `json:"episode_id"`
		}
		if err := decode(r, &in); err != nil {
			writeErr(w, 400, "invalid trash request")
			return
		}
		v, err := c.Trash(ctx, uid, item, in.Episode, in.Move, in.Revision)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, v)
		return
	}
	action := chi.URLParam(r, "action")
	if action == "purge" {
		var in struct {
			Confirm bool `json:"confirm"`
		}
		if decode(r, &in) != nil || !in.Confirm {
			writeErr(w, 400, "confirm permanent removal of the files in this trash entry")
			return
		}
	}
	v, err := c.TrashAction(ctx, uid, chi.URLParam(r, "trash"), action)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) nativeDeleteCompat(w http.ResponseWriter, r *http.Request, uid int64, ids []string) {
	if len(ids) == 0 || len(ids) > 1000 {
		writeErr(w, 400, "choose between 1 and 1000 items")
		return
	}
	results := []any{}
	success := true
	for _, id := range ids {
		it, err := s.catalog().Item(r.Context(), uid, id)
		if err == nil {
			_, err = s.catalog().Trash(r.Context(), uid, id, "", r.URL.Query().Get("hard") == "1", it.UpdatedAt)
		}
		if len(ids) == 1 && err != nil {
			s.nativeError(w, err)
			return
		}
		status := "deleted"
		if err != nil {
			status = "failed"
			success = false
		}
		results = append(results, nativeObject{"id": id, "status": status})
	}
	writeJSON(w, 200, nativeObject{"success": success, "results": results})
}
