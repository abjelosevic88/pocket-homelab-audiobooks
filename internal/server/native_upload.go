package server

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func (s *Server) nativeUpload(w http.ResponseWriter, r *http.Request, uid int64) {
	a, err := s.catalog().Account(r.Context(), uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if !a.Permissions.Upload {
		writeErr(w, 403, "upload permission required")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || !strings.EqualFold(u.Host, r.Host) {
			writeErr(w, 403, "upload from this app's own page")
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.audiobookUploadLimit())
	reader, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "multipart upload required")
		return
	}
	// A private spool permits fields in any order while keeping multi-gigabyte
	// uploads out of memory. Only a successful Ingest publishes library files.
	temp, err := os.MkdirTemp(s.cfg.DataDir, ".native-upload-")
	if err != nil {
		s.nativeError(w, err)
		return
	}
	defer os.RemoveAll(temp)
	fields := map[string]string{}
	files := []library.UploadFile{}
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			writeErr(w, 400, "incomplete upload or size limit exceeded")
			return
		}
		name := part.FileName()
		if name == "" {
			data, e := io.ReadAll(io.LimitReader(part, 65537))
			part.Close()
			if e != nil || len(data) > 65536 {
				writeErr(w, 400, "upload field too large")
				return
			}
			fields[part.FormName()] = string(data)
			continue
		}
		if len(files) >= 1024 {
			part.Close()
			writeErr(w, 400, "too many files")
			return
		}
		path := filepath.Join(temp, strconv.Itoa(len(files)))
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			part.Close()
			s.nativeError(w, e)
			return
		}
		_, e = io.Copy(f, part)
		closeErr := f.Close()
		part.Close()
		if e != nil || closeErr != nil {
			writeErr(w, 400, "incomplete upload or size limit exceeded")
			return
		}
		files = append(files, library.UploadFile{Name: name, Path: path})
	}
	id, err := s.catalog().Ingest(r.Context(), uid, library.UploadOptions{LibraryID: fields["library"], FolderID: fields["folder"], Title: fields["title"], Author: fields["author"], Series: fields["series"], FFprobe: s.cfg.FFprobe, Files: files})
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 201, nativeObject{"id": id, "success": true})
}
