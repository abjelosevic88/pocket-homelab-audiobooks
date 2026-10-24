package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

const bookID = "li-0001"

// fakeABS answers the calls the app makes and remembers whose key did what.
type fakeABS struct {
	mu       sync.Mutex
	streamed []string // key|range
	synced   []string // key|session|time
	closed   []string
	finished map[string]bool // key → book marked finished
	socket   []string        // socket.io bodies as Audiobookshelf got them
}

func (f *fakeABS) key(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func newFakeABS(t *testing.T) (*httptest.Server, *fakeABS) {
	t.Helper()
	f := &fakeABS{finished: map[string]bool{}}
	item := map[string]any{"id": bookID, "libraryId": "lib1", "mediaType": "book", "addedAt": 1, "updatedAt": 2,
		"media": map[string]any{"coverPath": "/x/cover.jpg", "duration": 3600.0, "numAudioFiles": 2, "numChapters": 2,
			"metadata": map[string]any{"title": "The Hobbit", "authorName": "J.R.R. Tolkien", "genres": []string{"Fantasy"},
				"authors": []map[string]any{{"id": "au1", "name": "J.R.R. Tolkien"}}, "series": []map[string]any{{"id": "se1", "name": "Middle-earth", "sequence": "1"}}},
			"chapters":   []map[string]any{{"id": 0, "start": 0, "end": 1800, "title": "An Unexpected Party"}, {"id": 1, "start": 1800, "end": 3600, "title": "Roast Mutton"}},
			"audioFiles": []map[string]any{{"index": 1, "ino": "11", "duration": 1800, "mimeType": "audio/mpeg"}, {"index": 2, "ino": "12", "duration": 1800, "mimeType": "audio/mpeg"}}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"app":"audiobookshelf","serverVersion":"2.37.2","isInit":true,"authMethods":["local"]}`)
	})
	mux.HandleFunc("/api/me", func(w http.ResponseWriter, r *http.Request) {
		k := f.key(r)
		f.mu.Lock()
		done := f.finished[k]
		f.mu.Unlock()
		prog := []map[string]any{{"id": "mp-" + k, "libraryItemId": bookID, "currentTime": 600, "duration": 3600, "progress": 0.17, "lastUpdate": 5, "isFinished": done, "finishedAt": map[bool]int64{true: time.Now().UnixMilli(), false: 0}[done]}}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-" + k, "username": "user-" + k, "type": "user", "mediaProgress": prog, "bookmarks": []any{}})
	})
	mux.HandleFunc("/api/libraries", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"libraries":[{"id":"lib1","name":"Audiobooks","mediaType":"book"},{"id":"pod","name":"Podcasts","mediaType":"podcast"}]}`)
	})
	mux.HandleFunc("/api/libraries/lib1/items", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{item}, "total": 1})
	})
	mux.HandleFunc("/api/items/"+bookID, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(item) })
	mux.HandleFunc("/api/items/"+bookID+"/play", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-" + f.key(r), "libraryItemId": bookID, "displayTitle": "The Hobbit", "duration": 3600, "playMethod": 0, "currentTime": 600,
			"audioTracks": []map[string]any{{"index": 1, "startOffset": 0, "duration": 1800, "contentUrl": "/api/items/" + bookID + "/file/11", "mimeType": "audio/mpeg"},
				{"index": 2, "startOffset": 1800, "duration": 1800, "contentUrl": "/api/items/" + bookID + "/file/12", "mimeType": "audio/mpeg"}}})
	})
	mux.HandleFunc("/api/items/"+bookID+"/file/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Query().Get("token"), "pab_") {
			t.Errorf("an app token reached Audiobookshelf in the URL")
		}
		if q := r.URL.Query().Get("token"); q != "" && r.Header.Get("Authorization") == "" {
			r.Header.Set("Authorization", "Bearer "+q)
		}
		f.mu.Lock()
		f.streamed = append(f.streamed, f.key(r)+"|"+r.Header.Get("Range"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "ID3")
	})
	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/session/"), "/")
		if parts[0] != "sess-"+f.key(r) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]string{"id": parts[0], "userId": "u-" + f.key(r)})
			return
		}
		var d map[string]any
		_ = json.NewDecoder(r.Body).Decode(&d)
		f.mu.Lock()
		if parts[1] == "close" {
			f.closed = append(f.closed, f.key(r)+"|"+parts[0])
		} else {
			f.synced = append(f.synced, f.key(r)+"|"+parts[0])
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/me/progress/"+bookID, func(w http.ResponseWriter, r *http.Request) {
		var d map[string]any
		_ = json.NewDecoder(r.Body).Decode(&d)
		if d["isFinished"] == true {
			f.mu.Lock()
			f.finished[f.key(r)] = true
			f.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/me/listening-sessions", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UnixMilli()
		_ = json.NewEncoder(w).Encode(map[string]any{"total": 1, "numPages": 1, "page": 0, "itemsPerPage": 100, "sessions": []map[string]any{{
			"id": "old-" + f.key(r), "libraryItemId": bookID, "mediaType": "book", "displayTitle": "The Hobbit", "displayAuthor": "J.R.R. Tolkien",
			"duration": 3600, "timeListening": 1200, "startTime": 0, "currentTime": 1200, "startedAt": now - 3_600_000, "updatedAt": now - 3_000_000,
			"deviceInfo": map[string]any{"clientName": "Abs Android", "deviceName": "Pixel"}, "mediaMetadata": map[string]any{"title": "The Hobbit", "authorName": "J.R.R. Tolkien"}},
			// Audra reports a podcast episode as a "book" without a library: not a book here
			{"id": "pod-" + f.key(r), "libraryItemId": "li-podcast", "mediaType": "book", "displayTitle": "A YouTube video", "duration": 600, "timeListening": 300,
				"startedAt": now - 3_600_000, "updatedAt": now - 3_500_000}}})
	})
	mux.HandleFunc("/api/authorize", func(w http.ResponseWriter, r *http.Request) {
		k := f.key(r)
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": "u-" + k, "username": "user-" + k, "token": "legacy-" + k, "accessToken": k},
			"userDefaultLibraryId": "lib1", "serverSettings": map[string]any{"authActiveAuthMethods": []string{"openid"}}})
	})
	mux.HandleFunc("/socket.io/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.socket = append(f.socket, string(b))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, `42["init",{"token":"`+strings.TrimPrefix(string(b), "42[\"auth\",\"")+`"}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, f
}

type person struct {
	u      *store.User
	cookie *http.Cookie
}

func testServer(t *testing.T) (*Server, http.Handler, *fakeABS) {
	t.Helper()
	srv, f := newFakeABS(t)
	cfg := testConfig(t.TempDir())
	cfg.ABSURL = srv.URL
	st := openTestStore(t, cfg)
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err := s.loadSecretKey(); err != nil {
		t.Fatal(err)
	}
	return s, s.Router(), f
}

func addPerson(t *testing.T, s *Server, email, key string) person {
	t.Helper()
	ctx := context.Background()
	u := &store.User{Email: email, Name: email, Role: "member", PasswordHash: "x"}
	if err := s.store.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	sealed, _ := s.seal(key)
	if err := s.store.SaveConnection(ctx, &store.Connection{UserID: u.ID, Kind: "key", Username: "user-" + key, ABSUserID: "u-" + key, Secret: sealed}); err != nil {
		t.Fatal(err)
	}
	id, err := s.store.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return person{u: u, cookie: &http.Cookie{Name: sessionCookie, Value: id}}
}

func call(t *testing.T, h http.Handler, p person, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(p.cookie)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if strings.Contains(path, "/media/") {
		r.Header.Set("Range", "bytes=0-")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// History, finished books and up next are strictly per person: another
// person's ids answer 404 and never show up in lists.
func TestEverythingIsPerPerson(t *testing.T) {
	s, h, _ := testServer(t)
	ctx := context.Background()
	ana, ben := addPerson(t, s, "ana@example.com", "keyA"), addPerson(t, s, "ben@example.com", "keyB")
	l := store.Listen{ItemID: bookID, Title: "The Hobbit", StartedAt: store.Now(), UpdatedAt: store.Now(), Seconds: 900}
	if _, err := s.store.UpsertListen(ctx, ana.u.ID, &l); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.AddFinish(ctx, ana.u.ID, &store.Finish{ItemID: bookID, Title: "The Hobbit", FinishedAt: store.Now()}); err != nil {
		t.Fatal(err)
	}
	if r := call(t, h, ana, "POST", "/api/v1/up-next", `{"item_id":"`+bookID+`"}`); r.Code != http.StatusCreated {
		t.Fatalf("up next: %d %s", r.Code, r.Body)
	}

	if r := call(t, h, ben, "GET", "/api/v1/history", ""); !strings.Contains(r.Body.String(), `"total":0`) {
		t.Fatalf("ben sees ana's history: %s", r.Body)
	}
	if r := call(t, h, ben, "DELETE", "/api/v1/history/"+strconv.FormatInt(l.ID, 10), ""); r.Code != http.StatusNotFound {
		t.Fatalf("ben deleted ana's listen: %d", r.Code)
	}
	if r := call(t, h, ben, "GET", "/api/v1/finished", ""); strings.TrimSpace(r.Body.String()) != "[]" {
		t.Fatalf("ben sees ana's finished books: %s", r.Body)
	}
	if r := call(t, h, ben, "GET", "/api/v1/up-next", ""); strings.TrimSpace(r.Body.String()) != "[]" {
		t.Fatalf("ben sees ana's up next: %s", r.Body)
	}
	if r := call(t, h, ben, "DELETE", "/api/v1/up-next/"+bookID, ""); r.Code != http.StatusNotFound {
		t.Fatalf("ben removed ana's up next: %d", r.Code)
	}
	if r := call(t, h, ben, "GET", "/api/v1/day/"+store.Today(), ""); !strings.Contains(r.Body.String(), `"seconds":0`) {
		t.Fatalf("ben sees ana's day: %s", r.Body)
	}
	if r := call(t, h, ana, "GET", "/api/v1/history", ""); !strings.Contains(r.Body.String(), `"total":1`) {
		t.Fatalf("ana lost her history: %s", r.Body)
	}
}

// Playing opens a session with the person's own key, streams through the app
// with that key (never in a URL), and reports go to that person's session.
func TestPlaybackActsAsThePerson(t *testing.T) {
	s, h, f := testServer(t)
	ana, ben := addPerson(t, s, "ana@example.com", "keyA"), addPerson(t, s, "ben@example.com", "keyB")
	r := call(t, h, ana, "POST", "/api/v1/play/"+bookID, `{"mime_types":["audio/mpeg"],"client":"tab1","device":"Firefox on Linux"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("play: %d %s", r.Code, r.Body)
	}
	var p struct {
		SessionID string      `json:"session_id"`
		Tracks    []PlayTrack `json:"tracks"`
		Current   float64     `json:"current_time"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &p)
	if p.SessionID != "sess-keyA" || len(p.Tracks) != 2 || p.Tracks[1].Start != 1800 || p.Current != 600 {
		t.Fatalf("unexpected play answer: %s", r.Body)
	}
	if !strings.HasPrefix(p.Tracks[0].URL, "/api/v1/media/api/items/") {
		t.Fatalf("track not proxied: %s", p.Tracks[0].URL)
	}
	if r := call(t, h, ana, "GET", p.Tracks[1].URL, ""); r.Code != http.StatusPartialContent || r.Body.String() != "ID3" {
		t.Fatalf("stream: %d %q", r.Code, r.Body)
	}
	if got := f.streamed; len(got) != 1 || got[0] != "keyA|bytes=0-" {
		t.Fatalf("streamed as %v", got)
	}
	if r := call(t, h, ana, "GET", "/api/v1/media/api/items/x/../../etc", ""); r.Code != http.StatusNotFound {
		t.Fatalf("media path not checked: %d", r.Code)
	}
	if r := call(t, h, ana, "POST", "/api/v1/sessions/sess-keyA/sync", `{"current_time":700,"listened":15,"duration":3600,"playing":true,"item_id":"`+bookID+`"}`); r.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", r.Code, r.Body)
	}
	// ben can't report into ana's session: Audiobookshelf refuses his key for it
	_ = call(t, h, ben, "POST", "/api/v1/sessions/sess-keyA/sync", `{"current_time":5,"listened":5,"duration":3600,"playing":true}`)
	if r := call(t, h, ana, "POST", "/api/v1/sessions/sess-keyA/sync", `{"current_time":3600,"listened":10,"duration":3600,"ended":true,"item_id":"`+bookID+`"}`); r.Code != http.StatusOK {
		t.Fatalf("close: %d %s", r.Code, r.Body)
	}
	if len(f.synced) != 1 || f.synced[0] != "keyA|sess-keyA" || len(f.closed) != 1 {
		t.Fatalf("reports: synced %v closed %v", f.synced, f.closed)
	}
	fs, _ := s.store.Finishes(context.Background(), ana.u.ID, "", "")
	if len(fs) != 1 || fs[0].Title != "The Hobbit" {
		t.Fatalf("finish not kept: %+v", fs)
	}
}

// History is read from Audiobookshelf's listening sessions, every app's.
func TestHistoryComesFromListeningSessions(t *testing.T) {
	s, h, _ := testServer(t)
	ana := addPerson(t, s, "ana@example.com", "keyA")
	if err := s.syncUser(context.Background(), ana.u.ID, false); err != nil {
		t.Fatal(err)
	}
	// reading again adds nothing
	if err := s.syncUser(context.Background(), ana.u.ID, true); err != nil {
		t.Fatal(err)
	}
	r := call(t, h, ana, "GET", "/api/v1/history", "")
	if !strings.Contains(r.Body.String(), `"total":1`) || !strings.Contains(r.Body.String(), `"device":"Abs Android · Pixel"`) || !strings.Contains(r.Body.String(), `"seconds":1200`) {
		t.Fatalf("history: %s", r.Body)
	}
	r = call(t, h, ana, "GET", "/api/v1/stats?range=7d", "")
	if !strings.Contains(r.Body.String(), `"seconds":1200`) {
		t.Fatalf("stats: %s", r.Body)
	}
}
