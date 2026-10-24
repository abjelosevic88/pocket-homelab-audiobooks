package server

import (
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/ebook"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
)

func (s *Server) nativeReader(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native reading is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	id := chi.URLParam(r, "id")
	fileID := r.URL.Query().Get("file")
	files, primary, err := s.catalog().EbookFiles(ctx, uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if fileID == "" {
		fileID = primary
	}
	position, err := s.catalog().ReadingPosition(ctx, uid, id, fileID)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == "PUT" {
		var in struct {
			library.ReadingPosition
			UserID int64 `json:"user_id"`
		}
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&in); err != nil {
			writeErr(w, 400, "invalid reading position")
			return
		}
		if in.UserID != uid {
			writeErr(w, 409, "reader account changed; reload before saving")
			return
		}
		out, err := s.catalog().SaveReadingPosition(ctx, uid, id, fileID, in.ReadingPosition)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		s.nativeSocketItemProgress(ctx, uid, id, "", "", "Web reader")
		writeJSON(w, 200, out)
		return
	}
	it, err := s.catalog().Item(ctx, uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"title": it.Title, "files": files, "primary": primary, "file_id": fileID, "position": position})
}
func (s *Server) nativeReaderContent(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native reading is not active")
		return
	}
	uid, id, fileID := actorID(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "file")
	if _, err := s.catalog().ReadingPosition(r.Context(), uid, id, fileID); err != nil {
		s.nativeError(w, err)
		return
	}
	file, info, err := s.catalog().OpenFile(r.Context(), uid, id, fileID, true)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		s.nativeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name}))
	if strings.EqualFold(filepath.Ext(info.Name), ".pdf") {
		w.Header().Set("Content-Type", "application/pdf")
		http.ServeContent(w, r, info.Name, stat.ModTime(), file)
		return
	}
	// Prepare off-response so malformed/oversized archives never become a partial
	// success. A private spool bounds RAM and is removed on every exit path.
	tmp, err := os.CreateTemp("", "pocket-epub-*")
	if err != nil {
		s.nativeError(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err = ebook.Prepare(r.Context(), file, stat.Size(), tmp); err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	if _, err = tmp.Seek(0, 0); err != nil {
		s.nativeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/epub+zip")
	http.ServeContent(w, r, info.Name, stat.ModTime(), tmp)
}
