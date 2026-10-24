package server

import (
	"context"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNativeMetadataProviderSecretsAndPermissions(t *testing.T) {
	s, h, a, b, _ := nativeHarness(t)
	if w := call(t, h, b, "GET", "/api/v1/catalog/metadata/providers", ""); w.Code != 403 {
		t.Fatal("unprivileged metadata", w.Code)
	}
	a.u.Role = "admin"
	s.store.UpdateUser(context.Background(), a.u)
	s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE pocket_user_id=?`, a.u.ID)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer metadata-secret" {
			t.Error("custom auth missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"matches":[{"title":"A result","author":"A writer"}]}`))
	}))
	defer upstream.Close()
	s.nativeFetchHTTP = fetch.Client(time.Second, []string{"127.0.0.1"})
	saved := jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/metadata/providers", string(nativeJSON(nativeObject{"kind": "custom", "name": "Test provider", "url": upstream.URL, "enabled": true, "secret": "Bearer metadata-secret"}))))
	if strings.Contains(string(nativeJSON(saved)), "metadata-secret") || saved["has_secret"] != true {
		t.Fatal("secret exposed or not saved")
	}
	var stored string
	s.store.DB.QueryRow(`SELECT secret FROM catalog_metadata_providers WHERE id=?`, saved["id"]).Scan(&stored)
	if strings.Contains(stored, "metadata-secret") || stored == "" {
		t.Fatal("plaintext provider credential")
	}
	w := call(t, h, a, "GET", "/api/v1/catalog/metadata/search?provider="+nativeString(saved["id"])+"&q=book", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "A result") {
		t.Fatalf("search %d %s", w.Code, w.Body.String())
	}
	// Searching never applies metadata or changes a book automatically.
	item, _ := s.catalog().Item(context.Background(), a.u.ID, "book")
	if item.Title != "Native book" {
		t.Fatal("search mutated catalog")
	}
	saved["enabled"] = false
	jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/metadata/providers", string(nativeJSON(saved))))
	if w := call(t, h, a, "GET", "/api/v1/catalog/metadata/search?provider="+nativeString(saved["id"])+"&q=book", ""); w.Code != 404 {
		t.Fatal("disabled provider used", w.Code)
	}
	if w := call(t, h, a, "PUT", "/api/v1/catalog/metadata/providers", string(nativeJSON(saved))); w.Code != 409 {
		t.Fatal("stale provider edit", w.Code)
	}
}
