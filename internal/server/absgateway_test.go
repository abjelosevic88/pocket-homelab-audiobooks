package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func gwCall(h http.Handler, method, path, tok, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Audiobookshelf apps sign in with an app password and then use the person's own
// Audiobookshelf account through Pocket Audiobooks; its key never reaches them.
func TestABSGateway(t *testing.T) {
	s, h, f := testServer(t)
	ctx := context.Background()
	ana := addPerson(t, s, "ana@example.com", "keyA")
	appTok, _, err := s.store.CreateAPIToken(ctx, ana.u.ID, "Audiobookshelf app")
	if err != nil {
		t.Fatal(err)
	}

	if w := gwCall(h, "GET", "/status", "", ""); !strings.Contains(w.Body.String(), `"authMethods":["local"]`) || !strings.Contains(w.Body.String(), `"app":"audiobookshelf"`) {
		t.Fatalf("status: %s", w.Body)
	}
	if w := gwCall(h, "GET", "/login", "", ""); w.Code == http.StatusMethodNotAllowed {
		t.Fatalf("the web sign-in page is gone: %d", w.Code)
	}
	// sign in with the app password (both at the root and under /audiobookshelf)
	for _, pre := range []string{"", "/audiobookshelf"} {
		w := gwCall(h, "POST", pre+"/login", "", `{"username":"ana@example.com","password":"`+appTok+`"}`)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "keyA") {
			t.Fatalf("%s login: %d %s", pre, w.Code, w.Body)
		}
		var res struct {
			User struct {
				Token, AccessToken, RefreshToken string
			}
			ServerSettings struct {
				AuthActiveAuthMethods []string
			}
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.User.AccessToken != appTok || res.User.Token != appTok || res.User.RefreshToken != appTok || len(res.ServerSettings.AuthActiveAuthMethods) != 1 {
			t.Fatalf("%s login answer: %+v", pre, res)
		}
	}
	// or with the Pocket Audiobooks password: the app gets a token of its own
	hash, _ := bcrypt.GenerateFromPassword([]byte("s3cret-pass"), bcrypt.MinCost)
	ana.u.PasswordHash = string(hash)
	if err := s.store.UpdateUser(ctx, ana.u); err != nil {
		t.Fatal(err)
	}
	w := gwCall(h, "POST", "/login", "", `{"username":"Ana@Example.com","password":"s3cret-pass"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accessToken":"pab_`) {
		t.Fatalf("password login: %d %s", w.Code, w.Body)
	}
	if w := gwCall(h, "POST", "/login", "", `{"username":"ana@example.com","password":"wrong"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}

	// calls go on as Ana's account
	if w := gwCall(h, "GET", "/api/libraries", appTok, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Audiobooks") {
		t.Fatalf("libraries: %d %s", w.Code, w.Body)
	}
	if w := gwCall(h, "GET", "/audiobookshelf/api/me", appTok, ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"keyA"`) {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}
	if w := gwCall(h, "GET", "/audiobookshelf/status", "", ""); !strings.Contains(w.Body.String(), `"authMethods":["local"]`) {
		t.Fatalf("prefixed status: %s", w.Body)
	}
	if w := gwCall(h, "POST", "/audiobookshelf/api/authorize", appTok, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accessToken":"`+appTok+`"`) || strings.Contains(w.Body.String(), "keyA") {
		t.Fatalf("authorize: %d %s", w.Code, w.Body)
	}
	// the web app's API is not reachable under the prefix
	if w := gwCall(h, "GET", "/audiobookshelf/api/v1/auth/status", "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("prefixed own api: %d", w.Code)
	}
	// a stream opened as a plain URL with ?token=
	r := httptest.NewRequest("GET", "/api/items/"+bookID+"/file/11?token="+appTok, nil)
	r.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusPartialContent || len(f.streamed) == 0 || f.streamed[len(f.streamed)-1] != "keyA|bytes=0-" {
		t.Fatalf("stream: %d %v", rec.Code, f.streamed)
	}
	// nothing without a token, and not other accounts or keys
	if w := gwCall(h, "GET", "/api/libraries", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := gwCall(h, "GET", "/api/api-keys", appTok, ""); w.Code != http.StatusForbidden {
		t.Fatalf("api keys: %d", w.Code)
	}
	if w := gwCall(h, "POST", "/api/users", appTok, `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("users: %d", w.Code)
	}
	// the web app's own API is untouched
	if w := gwCall(h, "GET", "/api/v1/auth/status", "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "needs_setup") {
		t.Fatalf("own api: %d %s", w.Code, w.Body)
	}

	// live updates: the token in socket.io's "auth" becomes Ana's key, and back
	w = gwCall(h, "POST", "/socket.io/?EIO=4&transport=polling&sid=s1", "", `42["auth","`+appTok+`"]`)
	if len(f.socket) != 1 || f.socket[0] != `42["auth","keyA"]` || strings.Contains(w.Body.String(), "keyA") || !strings.Contains(w.Body.String(), appTok) {
		t.Fatalf("socket: sent %v, answered %s", f.socket, w.Body)
	}
}
