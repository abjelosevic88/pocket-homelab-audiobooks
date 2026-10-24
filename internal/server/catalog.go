package server

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
)

// These additive endpoints expose the native catalog for migration verification.
// They never call upstream. The existing player stays on its current backend
// until the independent playback engine and client contracts are validated.
func (s *Server) catalog() *library.Catalog { return &library.Catalog{DB: s.store.DB, BooksOnly: true} }
func (s *Server) catalogError(w http.ResponseWriter, err error) {
	if errors.Is(err, library.ErrNotFound) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		writeErr(w, http.StatusNotFound, "catalog entry not found")
		return
	}
	s.log.Error("native catalog", "err", err)
	writeErr(w, http.StatusInternalServerError, "native catalog could not complete the request")
}
func (s *Server) handleCatalogLibraries(w http.ResponseWriter, r *http.Request) {
	out, err := s.catalog().Libraries(r.Context(), actorID(r.Context()))
	if err != nil {
		s.catalogError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleCatalogItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.catalog().Items(r.Context(), actorID(r.Context()), library.Query{LibraryID: q.Get("library"), Search: q.Get("q"), MediaType: q.Get("type"), Offset: queryInt(r, "offset"), Limit: queryInt(r, "limit")})
	if err != nil {
		s.catalogError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleCatalogItem(w http.ResponseWriter, r *http.Request) {
	c := s.catalog()
	ctx := r.Context()
	uid := actorID(ctx)
	id := chi.URLParam(r, "id")
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		s.catalogError(w, err)
		return
	}
	files, err := c.Files(ctx, uid, id)
	if err != nil {
		s.catalogError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"item": it, "files": files})
}
func (s *Server) handleCatalogProgress(w http.ResponseWriter, r *http.Request) {
	out, err := s.catalog().Progress(r.Context(), actorID(r.Context()))
	if err != nil {
		s.catalogError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleCatalogFile(w http.ResponseWriter, r *http.Request) {
	file, info, err := s.catalog().OpenFile(r.Context(), actorID(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "file"), queryBool(r, "download"))
	if err != nil {
		s.catalogError(w, err)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		s.catalogError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	// Only audio and raster artwork may render inline. Ebooks and companion files
	// are downloads, so imported active content cannot run under the app's origin.
	inline := info.Role == "audio" && strings.HasPrefix(info.MIME, "audio/") || info.Role == "cover" && (info.MIME == "image/jpeg" || info.MIME == "image/png" || info.MIME == "image/webp")
	if !inline || queryBool(r, "download") {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name}))
	}
	if inline {
		w.Header().Set("Content-Type", info.MIME)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, info.Name, stat.ModTime(), file)
}
