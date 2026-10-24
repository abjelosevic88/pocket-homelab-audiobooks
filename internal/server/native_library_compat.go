package server

import (
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"net/http"
	"strings"
)

func (s *Server) nativeLibraryWrite(w http.ResponseWriter, r *http.Request, uid int64) {
	u, err := s.store.GetUser(r.Context(), uid)
	if err != nil || u.Role != "admin" {
		writeErr(w, 403, "library administration requires an administrator")
		return
	}
	ctx := r.Context()
	c := s.catalog()
	if err = c.CanManageLibraries(ctx, uid); err != nil {
		s.nativeError(w, err)
		return
	}
	if r.URL.Path == "/api/libraries" && r.Method == "POST" {
		var in struct {
			Name      string                  `json:"name"`
			MediaType string                  `json:"mediaType"`
			Folders   []library.LibraryFolder `json:"folders"`
		}
		if decode(r, &in) != nil {
			writeErr(w, 400, "invalid library")
			return
		}
		paths := []string{}
		for _, f := range in.Folders {
			paths = append(paths, f.Path)
		}
		id, e := c.CreateLibrary(ctx, uid, library.NewLibrary{Name: in.Name, MediaType: in.MediaType, Paths: paths})
		if e != nil {
			s.nativeError(w, e)
			return
		}
		libs, e := s.nativeLibraries(ctx, uid)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		for _, l := range libs {
			if nativeString(nativeMap(l)["id"]) == id {
				writeJSON(w, 200, l)
				return
			}
		}
		writeErr(w, 500, "created library unavailable")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.URL.Path == "/api/libraries/order" && r.Method == "POST" {
		var order []library.LibraryOrder
		if decode(r, &order) != nil {
			writeErr(w, 400, "invalid library order")
			return
		}
		if err = c.ReorderLibraries(ctx, uid, order); err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"success": true})
		return
	}
	if len(parts) < 3 {
		writeErr(w, 501, "library operation is not implemented")
		return
	}
	id := parts[2]
	if len(parts) == 4 && parts[3] == "scan" && r.Method == "POST" {
		var in struct {
			Force bool `json:"force"`
		}
		if decode(r, &in) != nil {
			writeErr(w, 400, "invalid scan request")
			return
		}
		job, e := s.startNativeScan(uid, id, in.Force)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		writeJSON(w, 200, nativeObject{"scanId": job, "success": true})
		return
	}
	if len(parts) != 3 {
		writeErr(w, 501, "library operation is not implemented")
		return
	}
	v, err := c.LibraryConfig(ctx, uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	switch r.Method {
	case "DELETE":
		v.Archived = true
	case "PATCH":
		var in struct {
			Name    *string                  `json:"name"`
			Media   *string                  `json:"mediaType"`
			Folders *[]library.LibraryFolder `json:"folders"`
			Order   *int                     `json:"displayOrder"`
		}
		if decode(r, &in) != nil {
			writeErr(w, 400, "invalid library settings")
			return
		}
		if in.Name != nil {
			v.Name = *in.Name
		}
		if in.Media != nil {
			v.MediaType = *in.Media
		}
		if in.Folders != nil {
			v.Folders = *in.Folders
		}
		if in.Order != nil {
			v.Order = *in.Order
		}
	default:
		writeErr(w, 405, "method not allowed")
		return
	}
	if err = c.SaveLibrary(ctx, uid, v); err != nil {
		s.nativeError(w, err)
		return
	}
	if v.Archived {
		writeJSON(w, 200, nativeObject{"success": true, "archived": true})
		return
	}
	libs, err := s.nativeLibraries(ctx, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	for _, l := range libs {
		if nativeString(nativeMap(l)["id"]) == id {
			writeJSON(w, 200, l)
			return
		}
	}
	writeErr(w, 404, "library not found")
}
