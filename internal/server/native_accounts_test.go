package server

import (
	"context"
	"testing"
)

func TestNativeAccountBindingAndRevocation(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	if w := call(t, h, a, "GET", "/api/v1/catalog/accounts", ""); w.Code != 403 {
		t.Fatal("member could administer", w.Code)
	}
	a.u.Role = "admin"
	if err := s.store.UpdateUser(context.Background(), a.u); err != nil {
		t.Fatal(err)
	}
	data := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/accounts", ""))
	accounts := nativeArray(data["accounts"])
	if len(accounts) != 2 {
		t.Fatal(data)
	}
	var bob nativeObject
	for _, v := range accounts {
		if nativeString(nativeMap(v)["id"]) == "bob" {
			bob = nativeMap(v)
		}
	}
	stale := string(nativeJSON(bob))
	p := nativeMap(bob["permissions"])
	p["accessAllLibraries"] = true
	p["accessExplicitContent"] = true
	bob["permissions"] = p
	changed := jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/accounts", string(nativeJSON(bob))))
	if w := call(t, h, b, "GET", "/api/v1/books/book", ""); w.Code != 200 {
		t.Fatalf("grant not applied: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, h, a, "PUT", "/api/v1/catalog/accounts", stale); w.Code != 409 {
		t.Fatal("stale account edit", w.Code)
	}
	changed["user_id"] = a.u.ID
	if w := call(t, h, a, "PUT", "/api/v1/catalog/accounts", string(nativeJSON(changed))); w.Code != 409 {
		t.Fatal("private account reassigned", w.Code)
	}
	changed["user_id"] = b.u.ID
	changed["locked"] = true
	jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/accounts", string(nativeJSON(changed))))
	if w := call(t, h, b, "GET", "/api/v1/books/book", ""); w.Code == 200 {
		t.Fatal("locked user kept library access")
	}
	// Existing Core/native tokens resolve the same native owner, never another account.
	if w := nativeCall(t, h, tok, "GET", "/api/items/book", ""); w.Code != 200 {
		t.Fatal("owner token affected", w.Code)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO catalog_accounts VALUES('unbound',NULL,'Imported person',1,0,'{}')`); err != nil {
		t.Fatal(err)
	}
	c := addPerson(t, s, "new-native@example.com", "unused")
	data = jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/accounts", ""))
	for _, v := range nativeArray(data["accounts"]) {
		m := nativeMap(v)
		if m["id"] == "unbound" {
			m["user_id"] = c.u.ID
			jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/accounts", string(nativeJSON(m))))
		}
	}
	account, err := s.catalog().Account(context.Background(), c.u.ID)
	if err != nil || account.ID != "unbound" {
		t.Fatalf("explicit mapping %+v %v", account, err)
	}
	if _, err = s.catalog().Account(context.Background(), b.u.ID); err == nil {
		t.Fatal("locked mapping reused")
	}
}
