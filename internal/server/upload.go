package server

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func (s *Server) audiobookUploadLimit() int64 {
	if s.cfg.MaxAudiobookUploadBytes > 0 {
		return s.cfg.MaxAudiobookUploadBytes
	}
	return 10 << 30
}

func (s *Server) handleUploadOptions(w http.ResponseWriter, r *http.Request) {
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	me, err := ac.c.Me(r.Context())
	if err != nil {
		s.absErr(w, err)
		return
	}
	out := map[string]any{"allowed": me.Permissions.Upload, "max_bytes": s.audiobookUploadLimit(), "libraries": []any{}}
	if me.Permissions.Upload {
		libs, err := ac.c.Libraries(r.Context())
		if err != nil {
			s.absErr(w, err)
			return
		}
		books := []any{}
		for _, lib := range libs {
			if lib.MediaType == "book" {
				books = append(books, lib)
			}
		}
		out["libraries"] = books
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	ac := s.ab(w, r)
	if ac == nil {
		return
	}
	s.proxyABSUpload(w, r, ac)
}

// Stream multipart bytes directly to ABS. Never buffer an audiobook in RAM or
// spool it onto the Pocket data volume. ABS validates library/folder access.
func (s *Server) proxyABSUpload(w http.ResponseWriter, r *http.Request, ac *absConn) {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeErr(w, http.StatusForbidden, "upload from this app's own page")
			return
		}
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		writeErr(w, http.StatusBadRequest, "choose files to upload")
		return
	}
	limit := s.audiobookUploadLimit()
	if r.ContentLength > limit {
		writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds the server's size limit")
		return
	}
	// Refresh the account before sending an unrepeatable stream, and reject
	// people without permission before forwarding any of their file bytes.
	me, err := ac.c.Me(r.Context())
	if err != nil {
		s.absErr(w, err)
		return
	}
	if !me.Permissions.Upload {
		writeErr(w, http.StatusForbidden, "your Audiobookshelf account does not have upload permission")
		return
	}
	target, err := url.Parse(ac.c.Base)
	if err != nil || target.Host == "" {
		writeErr(w, http.StatusBadGateway, "invalid Audiobookshelf address")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.URL.Path = strings.TrimRight(target.Path, "/") + "/api/upload"
			p.Out.URL.RawPath = ""
			p.Out.URL.RawQuery = ""
			p.Out.Header = http.Header{}
			p.Out.Header.Set("Content-Type", r.Header.Get("Content-Type"))
			p.Out.Header.Set("Authorization", "Bearer "+ac.c.Token())
			p.Out.Header.Set("User-Agent", s.cfg.UserAgent)
		},
		Transport: s.streamHTTP.Transport,
		ModifyResponse: func(res *http.Response) error {
			res.Header.Del("Set-Cookie")
			res.Header.Set("Cache-Control", "no-store")
			// The web UI accepts ABS's plain-text OK; preserve status for native apps.
			if res.StatusCode >= 300 && res.StatusCode < 400 {
				return errors.New("unexpected upload redirect")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return
			}
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds the server's size limit")
				return
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				writeErr(w, http.StatusBadRequest, "upload interrupted")
				return
			}
			writeErr(w, http.StatusBadGateway, "Audiobookshelf could not finish the upload; check the library before retrying")
		},
	}
	proxy.ServeHTTP(w, r)
}
