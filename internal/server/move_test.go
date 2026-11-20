package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// moveServer starts Audiobooks behind a fake Core whose pcx_<who>_<export|import|read>
// tokens are Pocket Move links acting for <who>@example.com.
func moveServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/auth/introspect" {
			io.WriteString(w, `{}`)
			return
		}
		var in struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		parts := strings.SplitN(strings.TrimPrefix(in.Token, "pcx_"), "_", 2)
		if len(parts) != 2 {
			io.WriteString(w, `{"active":false}`)
			return
		}
		scope := map[string]string{"export": core.ScopeMoveExport, "import": core.ScopeMoveImport, "read": "listening:read"}[parts[1]]
		email := parts[0] + "@example.com"
		fmt.Fprintf(w, `{"active":true,"kind":"delegated","scope":%q,"audience":"audiobooks","user":{"id":1,"sub":%q,"email":%q,"name":%q,"role":"member"},"app":{"id":7,"slug":"move","name":"Pocket Move"}}`, scope, email, email, parts[0])
	}))
	t.Cleanup(coreSrv.Close)
	abs, _ := newFakeABS(t)
	cfg := testConfig(t.TempDir())
	cfg.ABSURL = abs.URL
	cfg.CoreURL = coreSrv.URL
	st := openTestStore(t, cfg)
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err := s.loadSecretKey(); err != nil {
		t.Fatal(err)
	}
	return s, s.Router()
}

func moveCall(t *testing.T, h http.Handler, tok, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMoveLinkScopes(t *testing.T) {
	s, h := moveServer(t)
	addPerson(t, s, "bob@example.com", "keyB")
	exp, imp, read := "pcx_bob_export", "pcx_bob_import", "pcx_bob_read"
	for _, c := range []struct {
		tok, method, path string
		want              int
	}{
		{exp, "GET", core.PortablePath, 200},
		{exp, "GET", "/api/v1/export.json", 200},
		{exp, "POST", "/api/v1/import", 403},
		{exp, "GET", "/api/v1/settings", 403},
		{exp, "GET", "/api/v1/up-next", 403},
		{exp, "GET", "/api/v1/backup", 403},
		{imp, "GET", core.PortablePath, 200},
		{imp, "GET", "/api/v1/export.json", 403},
		{imp, "PUT", "/api/v1/settings", 403},
		{imp, "POST", "/api/v1/up-next", 403},
		{imp, "POST", "/api/v1/backup", 403},
		{read, "GET", core.PortablePath, 403},
		{read, "GET", "/api/v1/export.json", 403},
	} {
		if w := moveCall(t, h, c.tok, c.method, c.path, "{}"); w.Code != c.want {
			t.Errorf("%s %s %s → %d, want %d (%s)", c.tok, c.method, c.path, w.Code, c.want, w.Body)
		}
	}
	w := moveCall(t, h, exp, "GET", core.PortablePath, "")
	var p core.Portable
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || len(p.Exports) != 1 || len(p.Imports) != 1 || p.Exports[0].Kind != core.KindPocket {
		t.Fatalf("portable: %s", w.Body)
	}
}

// Move brings Alice's listening to Bob: the import adds once, and nothing of
// one person shows up for the other.
func TestMoveImportsAddOnce(t *testing.T) {
	s, h := moveServer(t)
	ctx := context.Background()
	alice, bob := addPerson(t, s, "alice@example.com", "keyA"), addPerson(t, s, "bob@example.com", "keyB")
	for _, l := range []store.Listen{
		{ItemID: bookID, Title: "The Hobbit", StartedAt: "2026-11-01T20:00:00Z", UpdatedAt: "2026-11-01T21:00:00Z", Seconds: 900, ABSID: "sess-1"},
		{ItemID: bookID, Title: "The Hobbit", StartedAt: "2026-11-02T20:00:00Z", UpdatedAt: "2026-11-02T21:00:00Z", Seconds: 600},
	} {
		if _, err := s.store.UpsertListen(ctx, alice.u.ID, &l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.store.AddFinish(ctx, alice.u.ID, &store.Finish{ItemID: bookID, Title: "The Hobbit", FinishedAt: "2026-11-03T20:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddUpNext(ctx, alice.u.ID, &store.UpNext{ItemID: "next-book", Title: "Next"}, false); err != nil {
		t.Fatal(err)
	}

	w := moveCall(t, h, "pcx_alice_export", "GET", "/api/v1/export.json", "")
	export := w.Body.String()
	if w.Code != 200 || !strings.Contains(export, "The Hobbit") || !strings.Contains(export, "next-book") {
		t.Fatalf("alice's export: %d %s", w.Code, export)
	}
	if w := moveCall(t, h, "pcx_bob_export", "GET", "/api/v1/export.json", ""); strings.Contains(w.Body.String(), "Hobbit") || strings.Contains(w.Body.String(), "next-book") {
		t.Fatalf("bob's export holds alice's listening: %s", w.Body)
	}

	imp := func() map[string]any {
		w := moveCall(t, h, "pcx_bob_import", "POST", "/api/v1/import", export)
		if w.Code != 200 {
			t.Fatalf("import → %d %s", w.Code, w.Body)
		}
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	n := func(m map[string]any, k string) float64 { v, _ := m[k].(float64); return v }
	first := imp()
	if n(first, "listens") != 2 || n(first, "finished") != 1 || n(first, "up_next") != 1 {
		t.Fatalf("first import: %v", first)
	}
	second := imp()
	if n(second, "listens")+n(second, "finished")+n(second, "up_next") != 0 {
		t.Fatalf("second import added: %v", second)
	}
	if l, total, _ := s.store.Listens(ctx, bob.u.ID, store.ListenFilter{Limit: -1}); len(l) != 2 || total != 2 {
		t.Fatalf("bob's listens: %d", len(l))
	}
	if l, _, _ := s.store.Listens(ctx, alice.u.ID, store.ListenFilter{Limit: -1}); len(l) != 2 {
		t.Fatalf("alice's listens changed: %d", len(l))
	}
	if u, _ := s.store.UpNextList(ctx, bob.u.ID); len(u) != 1 {
		t.Fatalf("bob's up next: %v", u)
	}
}
