package server

import (
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (s *Server) nativeLibrarySettings(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" || userFrom(r.Context()).Role != "admin" {
		writeErr(w, 403, "library administration requires an administrator")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	c := s.catalog()
	if err := c.CanManageLibraries(ctx, uid); err != nil {
		s.nativeError(w, err)
		return
	}
	if r.Method == "PUT" {
		var in library.LibraryConfig
		if decode(r, &in) != nil {
			writeErr(w, 400, "invalid library settings")
			return
		}
		if in.ID != chi.URLParam(r, "id") {
			writeErr(w, 400, "library ID does not match")
			return
		}
		if err := c.SaveLibrary(ctx, uid, in); err != nil {
			s.nativeError(w, err)
			return
		}
		v, err := c.LibraryConfig(ctx, uid, in.ID)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, v)
		return
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id FROM catalog_libraries ORDER BY display_order,id`)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			s.nativeError(w, err)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		s.nativeError(w, err)
		return
	}
	list := []library.LibraryConfig{}
	for _, id := range ids {
		v, e := c.LibraryConfig(ctx, uid, id)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		list = append(list, v)
	}
	writeJSON(w, 200, list)
}
