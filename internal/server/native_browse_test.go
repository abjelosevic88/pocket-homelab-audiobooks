package server

import (
	"context"
	"encoding/base64"
	"net/url"
	"testing"
)

func TestNativeBrowseStandardFiltersPagingAndStatistics(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	_, e := s.store.DB.Exec(`UPDATE catalog_items SET tags='["Fantasy"]',document=json_set(document,'$.media.publishedYear','2024','$.media.language','English','$.media.publisher','Publisher','$.media.narrators',json('["A narrator"]'),'$.media.genres',json('["Fantasy"]'),'$.authors',json('[{"id":"writer","name":"Writer"}]'),'$.series',json('[{"id":"series","name":"Series","sequence":"1"}]')) WHERE id='book'`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.store.DB.Exec(`INSERT INTO catalog_items SELECT 'second',library_id,'second-media',media_type,'Second book',author,50,0,0,2,2,'[]',json_set(document,'$.media.title','Second book','$.media.publishedYear','99','$.series',json('[{"id":"series","name":"Series","sequence":"2"}]')) FROM catalog_items WHERE id='book'`)
	if e != nil {
		t.Fatal(e)
	}
	s.catalog().SetProgress(context.Background(), a.u.ID, "book", "", map[string]any{"currentTime": 10})
	filter := func(group, value string) nativeObject {
		t.Helper()
		q := group + "." + base64.StdEncoding.EncodeToString([]byte(value))
		return jsonObject(t, nativeCall(t, h, tok, "GET", "/api/libraries/lib/items?filter="+url.QueryEscape(q), ""))
	}
	for _, test := range []struct {
		group, value string
		count        int
	}{{"tags", "Fantasy", 1}, {"languages", "English", 2}, {"publishers", "Publisher", 2}, {"publishedDecades", "2020", 1}, {"narrators", "A narrator", 2}, {"series", "series", 2}, {"series", "no-series", 0}, {"progress", "in-progress", 1}, {"progress", "not-started", 1}, {"progress", "not-finished", 2}, {"tracks", "multi", 1}, {"tracks", "none", 1}, {"missing", "cover", 2}} {
		if got := filter(test.group, test.value); int(nativeNumber(got["total"])) != test.count {
			t.Fatal(test, got)
		}
	}
	for _, q := range []string{"filter=unknown.eA==", "filter=tags.invalid!"} {
		if w := nativeCall(t, h, tok, "GET", "/api/libraries/lib/items?"+q, ""); w.Code != 400 {
			t.Fatal(w.Code, q)
		}
	}
	huge := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/libraries/lib/items?page=9223372036854775807&limit=500", ""))
	if len(nativeArray(huge["results"])) != 0 {
		t.Fatal(huge)
	}
	ordered := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/libraries/lib/items?sort=media.metadata.publishedYear&desc=1", ""))
	if nativeMap(nativeArray(ordered["results"])[0])["id"] != "book" {
		t.Fatal("year sort was lexical", ordered)
	}
	collapsed := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/libraries/lib/items?collapseseries=1", ""))
	if nativeNumber(collapsed["total"]) != 1 || nativeNumber(nativeMap(nativeMap(nativeArray(collapsed["results"])[0])["collapsedSeries"])["numBooks"]) != 2 {
		t.Fatal(collapsed)
	}
	data := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/libraries/lib/filterdata", ""))
	if len(nativeArray(data["tags"])) != 1 || len(nativeArray(data["publishers"])) != 1 {
		t.Fatal(data)
	}
	stats := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/libraries/lib/stats", ""))
	if nativeNumber(stats["totalItems"]) != 2 || nativeNumber(stats["totalDuration"]) != 150 || nativeNumber(stats["numAudioTracks"]) != 2 || nativeNumber(stats["totalSize"]) != 30 {
		t.Fatal(stats)
	}
	narrators := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/libraries/lib/narrators", ""))
	if nativeNumber(nativeMap(nativeArray(narrators["narrators"])[0])["numBooks"]) != 2 {
		t.Fatal(narrators)
	}
	search := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/libraries/lib/search?q=Fantasy", ""))
	if len(nativeArray(search["tags"])) != 1 || len(nativeArray(search["genres"])) != 1 {
		t.Fatal(search)
	}
	if w := call(t, h, b, "GET", "/api/v1/catalog/items/book", ""); w.Code == 200 {
		t.Fatal("inaccessible book")
	}
	// Restrict tags: aggregated filters and counts must hide the other book too.
	s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.accessAllTags',json('false'),'$.itemTagsSelected',json('["Fantasy"]')) WHERE id='alice'`)
	stats = jsonObject(t, nativeCall(t, h, tok, "GET", "/api/libraries/lib/stats", ""))
	if nativeNumber(stats["totalItems"]) != 1 {
		t.Fatal("count leak", stats)
	}
}
func TestNativeFirstAccountCanUseLibraryWithoutRestart(t *testing.T) {
	s, h, _ := testServer(t)
	s.cfg.Backend = "native"
	w := nativeCall(t, h, "", "POST", "/api/v1/auth/setup", `{"email":"owner@example.com","password":"a secure demo password","name":"Owner"}`)
	if w.Code != 200 && w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var uid int64
	if e := s.store.DB.QueryRow(`SELECT id FROM users WHERE email='owner@example.com'`).Scan(&uid); e != nil {
		t.Fatal(e)
	}
	account, e := s.catalog().Account(context.Background(), uid)
	if e != nil || !account.Permissions.Upload || !account.Permissions.Update {
		t.Fatal(account, e)
	}
	// A second identity cannot inherit the first owner's catalog.
	member := addPerson(t, s, "member@example.com", "unused")
	if e = s.ownerReady(context.Background(), member.u.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.catalog().Account(context.Background(), member.u.ID); e == nil {
		t.Fatal("second account inherited owner access")
	}
}

func TestNativeImportedAuthorTimestampSupportsWebPlayback(t *testing.T) {
	s, h, a, _, _ := nativeHarness(t)
	_, err := s.store.DB.Exec(`UPDATE catalog_items SET document=json_set(document,'$.authors',json('[{"id":"writer","name":"Writer","updatedAt":"2026-10-24 15:44:14.216 +00:00"}]')) WHERE id='book'`)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.nativeItem(context.Background(), a.u.ID, "book")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := nativeDecodeItem(item)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Media.Metadata.Authors[0].UpdatedAt != 1792856654216 {
		t.Fatal(decoded.Media.Metadata.Authors)
	}
	w := call(t, h, a, "POST", "/api/v1/play/book", `{"mime_types":["audio/mpeg"],"client":"test"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
