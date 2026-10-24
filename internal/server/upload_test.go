package server

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadUsesOwnAccountAndStreamsMultipart(t *testing.T) {
	s, _, _ := testServer(t)
	ana := addPerson(t, s, "ana@example.com", "uploadA")
	ben := addPerson(t, s, "ben@example.com", "uploadB")
	uploads := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch r.URL.Path {
		case "/api/me":
			writeJSON(w, 200, map[string]any{"id": token, "permissions": map[string]bool{"upload": token == "uploadA"}})
		case "/api/libraries":
			writeJSON(w, 200, map[string]any{"libraries": []any{map[string]any{"id": "books", "name": "Books", "mediaType": "book", "folders": []any{map[string]string{"id": "folder", "fullPath": "/books"}}}, map[string]string{"id": "podcasts", "mediaType": "podcast"}}})
		case "/api/upload":
			uploads++
			if token != "uploadA" || r.Header.Get("Cookie") != "" {
				t.Errorf("wrong identity or forwarded cookie")
			}
			reader, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			fields := map[string]string{}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					w.WriteHeader(400)
					return
				}
				data, err := io.ReadAll(part)
				if err != nil {
					w.WriteHeader(400)
					return
				}
				fields[part.FormName()] = string(data)
			}
			if fields["library"] != "books" || fields["folder"] != "folder" || fields["title"] != "Test Book" || fields["0"] != "audio-data" {
				t.Errorf("multipart changed: %v", fields)
			}
			w.WriteHeader(200)
			io.WriteString(w, "OK")
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	s.cfg.ABSURL = upstream.URL
	h := s.Router()
	opts := call(t, h, ana, "GET", "/api/v1/upload", "")
	if opts.Code != 200 || !strings.Contains(opts.Body.String(), `"fullPath":"/books"`) || strings.Contains(opts.Body.String(), "podcasts") {
		t.Fatalf("options: %d %s", opts.Code, opts.Body)
	}
	opts = call(t, h, ben, "GET", "/api/v1/upload", "")
	if opts.Code != 200 || !strings.Contains(opts.Body.String(), `"allowed":false`) {
		t.Fatalf("permission: %d %s", opts.Code, opts.Body)
	}
	var body bytes.Buffer
	mp := multipart.NewWriter(&body)
	for k, v := range map[string]string{"library": "books", "folder": "folder", "title": "Test Book"} {
		mp.WriteField(k, v)
	}
	f, _ := mp.CreateFormFile("0", "test.m4b")
	f.Write([]byte("audio-data"))
	mp.Close()
	send := func(p person, path, token, origin string, chunked bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, bytes.NewReader(body.Bytes()))
		req.Header.Set("Content-Type", mp.FormDataContentType())
		req.AddCookie(p.cookie)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if chunked {
			req.ContentLength = -1
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}
	if res := send(ana, "/api/v1/upload", "", "", false); res.Code != 200 || res.Body.String() != "OK" {
		t.Fatalf("upload: %d %s", res.Code, res.Body)
	}
	if res := send(ben, "/api/v1/upload", "", "", false); res.Code != 403 {
		t.Fatalf("unpermitted upload: %d", res.Code)
	}
	if res := send(ana, "/api/v1/upload", "", "https://other.example", false); res.Code != 403 {
		t.Fatalf("cross-origin upload: %d", res.Code)
	}
	token, _, _ := s.store.CreateAPIToken(context.Background(), ana.u.ID, "native app")
	if res := send(ana, "/api/upload", token, "", false); res.Code != 200 {
		t.Fatalf("native upload: %d %s", res.Code, res.Body)
	}
	if uploads != 2 {
		t.Fatalf("unauthorized bytes forwarded: %d requests", uploads)
	}
	s.cfg.MaxAudiobookUploadBytes = 10
	if res := send(ana, "/api/v1/upload", "", "", false); res.Code != 413 {
		t.Fatalf("oversized upload: %d", res.Code)
	}
	if res := send(ana, "/api/v1/upload", "", "", true); res.Code != 413 {
		t.Fatalf("chunked oversized upload: %d %s", res.Code, res.Body)
	}
}
