package server

import (
	"testing"
)

func TestNativeTidyRoutes(t *testing.T) {
	s, h, a, b, _ := nativeHarness(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO catalog_entities VALUES('libraryFolders','folder','lib','{"id":"folder","path":"/media"}')`)
	exec(`UPDATE catalog_items SET title='Mistborn - 2 - The Well of Ascension - GA',document=json_set(document,'$.item.path','/media/Brandon Sanderson/Cosmere/Vol 5 - Mistborn - 2 - The Well of Ascension - GA') WHERE id='book'`)
	if w := call(t, h, a, "GET", "/api/v1/catalog/libraries/lib/tidy", ""); w.Code != 403 {
		t.Fatal("tidy-up without edit permission", w.Code)
	}
	exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE id='alice'`)
	report := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/libraries/lib/tidy", ""))
	changes := nativeArray(report["changes"])
	if len(changes) != 1 {
		t.Fatal(report)
	}
	ch := nativeMap(changes[0])
	if nativeString(nativeMap(nativeArray(ch["changes"])[0])["to"]) != "The Well of Ascension" || nativeString(ch["confidence"]) != "high" {
		t.Fatal(ch)
	}
	if w := call(t, h, b, "POST", "/api/v1/catalog/libraries/lib/tidy", `{"items":[{"id":"book","expected_updated_at":1}]}`); w.Code != 403 {
		t.Fatal("member applied tidy-up", w.Code)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/libraries/lib/tidy", `{"items":[{"id":"book","expected_updated_at":0}]}`); w.Code != 409 {
		t.Fatal("stale tidy-up", w.Code)
	}
	body := string(nativeJSON(nativeObject{"items": []any{nativeObject{"id": "book", "expected_updated_at": ch["expected_updated_at"]}}}))
	if out := jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/libraries/lib/tidy", body)); nativeNumber(out["updated"]) != 1 {
		t.Fatal(out)
	}
	it, err := s.catalog().Item(t.Context(), a.u.ID, "book")
	if err != nil || it.Title != "The Well of Ascension" {
		t.Fatal(it.Title, err)
	}
	covers := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/libraries/lib/covers", ""))
	books := nativeArray(covers["books"])
	if len(books) != 1 || nativeString(nativeMap(books[0])["source"]) != "" {
		t.Fatal(covers)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/libraries/lib/covers", `{"items":[{"id":"book","expected_updated_at":1,"source":"folder"}]}`); w.Code != 200 {
		t.Fatal(w.Code)
	} else if out := jsonObject(t, w); nativeNumber(out["saved"]) != 0 || len(nativeArray(out["failed"])) != 1 {
		t.Fatal(out)
	}
}
