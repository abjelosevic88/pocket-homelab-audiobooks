package server

import "testing"

func TestNativeBulkEditAndMetadataExport(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE id='alice'; INSERT INTO catalog_items SELECT 'second',library_id,'second-media',media_type,'Second book',author,duration,explicit,missing,added_at,updated_at,tags,document FROM catalog_items WHERE id='book'`); err != nil {
		t.Fatal(err)
	}
	if w := call(t, h, b, "POST", "/api/v1/catalog/items/batch/editor", `{"ids":["book"]}`); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/items/batch/editor", `{"ids":["book","second"]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body := `[{"id":"book","expected_updated_at":1,"metadata":{"tags":["batch"]}},{"id":"second","expected_updated_at":0,"metadata":{"tags":["batch"]}}]`
	if w := call(t, h, a, "PUT", "/api/v1/catalog/items/batch/editor", body); w.Code != 409 {
		t.Fatal(w.Code)
	}
	book := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/items/book/editor", ""))
	if len(nativeArray(nativeMap(book["metadata"])["tags"])) != 0 {
		t.Fatal("partially committed")
	}
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/batch/update", `[{"id":"book","mediaPayload":{"tags":["batch"],"metadata":{"authors":[{"name":"Portable author"}],"series":[{"name":"Portable series","sequence":"4"}]}}},{"id":"second","mediaPayload":{"tags":["batch"]}}]`))
	w := call(t, h, a, "GET", "/api/v1/catalog/items/book/metadata", "")
	out := jsonObject(t, w)
	if w.Header().Get("Content-Disposition") != `attachment; filename="metadata.json"` || nativeArray(out["authors"])[0] != "Portable author" || nativeArray(out["series"])[0] != "Portable series #4" || nativeArray(out["tags"])[0] != "batch" {
		t.Fatal(w.Header(), out)
	}
	if w = call(t, h, b, "GET", "/api/v1/catalog/items/book/metadata", ""); w.Code != 403 {
		t.Fatal("metadata download permission", w.Code)
	}
}
