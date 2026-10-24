package server

import (
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (s *Server) nativeTracks(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native tracks are not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	id := chi.URLParam(r, "id")
	c := s.catalog()
	a, err := c.Account(ctx, uid)
	if err != nil || !a.Permissions.Update {
		writeErr(w, 403, "editing permission required")
		return
	}
	if r.Method == "PUT" {
		var in library.TrackEdit
		if decode(r, &in) != nil {
			writeErr(w, 400, "invalid tracks")
			return
		}
		if err = c.EditTracks(ctx, uid, id, in); err != nil {
			s.nativeError(w, err)
			return
		}
	}
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	files, err := c.Files(ctx, uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	audio := []library.File{}
	for _, f := range files {
		if f.Role == "audio" {
			audio = append(audio, f)
		}
	}
	writeJSON(w, 200, nativeObject{"revision": it.UpdatedAt, "tracks": audio})
}
func (s *Server) nativeTracksCompat(w http.ResponseWriter, r *http.Request, uid int64, id string) {
	var in struct {
		Files []struct {
			ID       string `json:"ino"`
			Excluded bool   `json:"exclude"`
		} `json:"orderedFileData"`
		Reset bool `json:"resetChapters"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "invalid tracks")
		return
	}
	it, err := s.catalog().Item(r.Context(), uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	edit := library.TrackEdit{Expected: it.UpdatedAt, ResetChapters: in.Reset}
	for _, f := range in.Files {
		edit.Tracks = append(edit.Tracks, library.TrackChoice{ID: f.ID, Excluded: f.Excluded})
	}
	if err = s.catalog().EditTracks(r.Context(), uid, id, edit); err != nil {
		s.nativeError(w, err)
		return
	}
	item, err := s.nativeItem(r.Context(), uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, item)
}
