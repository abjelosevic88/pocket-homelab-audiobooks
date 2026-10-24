package server

import (
	"encoding/json"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"net/http"
)

func (s *Server) nativeBulkEditor(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native editing is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	a, err := s.catalog().Account(ctx, uid)
	if err != nil || !a.Permissions.Update {
		writeErr(w, 403, "editing permission required")
		return
	}
	if r.Method == "PUT" {
		var edits []library.ItemEdit
		if decode(r, &edits) != nil {
			writeErr(w, 400, "invalid edits")
			return
		}
		if err = s.catalog().EditMany(ctx, uid, edits); err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"updated": len(edits)})
		return
	}
	var in struct {
		IDs []string `json:"ids"`
	}
	if decode(r, &in) != nil || len(in.IDs) == 0 || len(in.IDs) > 100 {
		writeErr(w, 400, "select 1 to 100 books")
		return
	}
	out := []any{}
	for _, id := range in.IDs {
		it, e := s.catalog().Item(ctx, uid, id)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		if it.MediaType != "book" {
			writeErr(w, 400, "select books only")
			return
		}
		out = append(out, nativeObject{"id": it.ID, "title": it.Title, "expected_updated_at": it.UpdatedAt})
	}
	writeJSON(w, 200, out)
}
func (s *Server) nativeBulkCompat(w http.ResponseWriter, r *http.Request, uid int64) {
	var input []struct {
		ID    string       `json:"id"`
		Media nativeObject `json:"mediaPayload"`
	}
	if decode(r, &input) != nil || len(input) == 0 || len(input) > 100 {
		writeErr(w, 400, "select 1 to 100 books")
		return
	}
	edits := []library.ItemEdit{}
	for _, v := range input {
		it, e := s.catalog().Item(r.Context(), uid, v.ID)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		fields := nativeMap(v.Media["metadata"])
		if tags, ok := v.Media["tags"]; ok {
			fields["tags"] = tags
		}
		edit := library.ItemEdit{ID: v.ID, EditOptions: library.EditOptions{Expected: it.UpdatedAt, Metadata: fields}}
		if chapters, ok := v.Media["chapters"]; ok {
			var ch []library.Chapter
			if json.Unmarshal(nativeJSON(chapters), &ch) != nil {
				writeErr(w, 400, "invalid chapters")
				return
			}
			edit.Chapters = &ch
		}
		edits = append(edits, edit)
	}
	if err := s.catalog().EditMany(r.Context(), uid, edits); err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, nativeObject{"updates": len(edits), "success": true})
}
