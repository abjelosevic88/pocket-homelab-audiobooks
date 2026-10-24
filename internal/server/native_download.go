package server

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
)

// Archive streams original files with bounded memory. Access and availability
// are checked before writing headers; every file is re-authorized when opened.
func (s *Server) nativeDownload(w http.ResponseWriter, r *http.Request, uid int64, ids []string) {
	a, err := s.catalog().Account(r.Context(), uid)
	if err != nil || !a.Permissions.Download {
		s.nativeError(w, library.ErrNotFound)
		return
	}
	if len(ids) == 0 || len(ids) > 200 {
		writeErr(w, 400, "choose between one and 200 books")
		return
	}
	type file struct{ item, id, name string }
	files := []file{}
	names := map[string]bool{}
	seen := map[string]bool{}
	title := "Audiobooks"
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		item, err := s.catalog().Item(r.Context(), uid, id)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		if len(ids) == 1 {
			title = item.Title
		}
		entries, err := s.catalog().Files(r.Context(), uid, id)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		for _, entry := range entries {
			f, _, err := s.catalog().OpenFile(r.Context(), uid, id, entry.ID, true)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			f.Close()
			name := safeDownloadName(filepath.Base(strings.ReplaceAll(entry.Name, "\\", "/")))
			if name == "." || name == "/" || name == "" {
				name = "file"
			}
			if len(ids) > 1 {
				name = fmt.Sprintf("book-%d/%s", len(seen), name)
			}
			candidate := name
			for i := 2; names[strings.ToLower(candidate)]; i++ {
				candidate = fmt.Sprintf("%d-%s", i, name)
			}
			names[strings.ToLower(candidate)] = true
			files = append(files, file{item: id, id: entry.ID, name: candidate})
		}
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": safeDownloadName(title) + ".zip"}))
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == "HEAD" {
		w.WriteHeader(200)
		return
	}
	archive := zip.NewWriter(w)
	for _, entry := range files {
		if r.Context().Err() != nil {
			return
		}
		f, _, err := s.catalog().OpenFile(r.Context(), uid, entry.item, entry.id, true)
		if err != nil {
			s.log.Warn("native download file unavailable", "item", entry.item, "error", err)
			return
		}
		stat, err := f.Stat()
		if err != nil {
			f.Close()
			return
		}
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		header.SetModTime(stat.ModTime())
		header.SetMode(0644)
		writer, err := archive.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(writer, nativeDownloadReader{ctx: r.Context(), reader: f})
		}
		f.Close()
		if err != nil {
			return
		}
	}
	if err := archive.Close(); err != nil {
		s.log.Debug("native archive interrupted", "error", err)
	}
}
func safeDownloadName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune("/\\:\"*?<>|", r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Audiobook"
	}
	return name
}

type nativeDownloadReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r nativeDownloadReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func (s *Server) nativeDownloadBook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native downloads are not active")
		return
	}
	s.nativeDownload(w, r, actorID(r.Context()), []string{chi.URLParam(r, "id")})
}
