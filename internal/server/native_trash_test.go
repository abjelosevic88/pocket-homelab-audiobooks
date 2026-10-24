package server

import (
	"context"
	"encoding/json"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"testing"
)

func TestNativeTrashRoutesPermissionsAndRestore(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	if w := call(t, h, a, "POST", "/api/v1/catalog/items/book/trash", `{"move_files":true,"revision":1}`); w.Code != 404 {
		t.Fatal("missing delete permission", w.Code)
	}
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.delete',json('true')) WHERE id='alice'`); err != nil {
		t.Fatal(err)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/items/book/trash", `{"move_files":true,"revision":0}`); w.Code != 409 {
		t.Fatal("stale revision", w.Code)
	}
	v := jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/items/book/trash", `{"move_files":true,"revision":1}`))
	id := nativeString(v["id"])
	if v["state"] != "deleted" {
		t.Fatal(v)
	}
	if w := nativeCall(t, h, tok, "GET", "/api/items/book/file/11", ""); w.Code != 404 {
		t.Fatal("deleted media accessible", w.Code)
	}
	if w := call(t, h, b, "POST", "/api/v1/catalog/trash/"+id+"/restore", `{}`); w.Code != 404 {
		t.Fatal("other user's library", w.Code)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/trash/"+id+"/purge", `{}`); w.Code != 400 {
		t.Fatal("missing purge confirmation", w.Code)
	}
	jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/trash/"+id+"/restore", `{}`))
	if w := nativeCall(t, h, tok, "GET", "/api/items/book/file/12", ""); w.Code != 206 {
		t.Fatal("restore bytes", w.Code)
	}
	// The client DELETE contract also uses recoverable trash, never immediate purge.
	jsonObject(t, nativeCall(t, h, tok, "DELETE", "/api/items/book?hard=1", ""))
	var entries []library.TrashEntry
	w := call(t, h, a, "GET", "/api/v1/catalog/trash", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &entries) != nil || len(entries) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if entries[0].State != "deleted" {
		t.Fatal(entries)
	}
	if _, err := s.catalog().TrashAction(context.Background(), a.u.ID, entries[0].ID, "restore"); err != nil {
		t.Fatal(err)
	}
}
