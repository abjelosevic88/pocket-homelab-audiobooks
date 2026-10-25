package server

import (
	"context"
	"fmt"
	"testing"
)

func reviewCounts(t *testing.T, v nativeObject) map[string]any {
	t.Helper()
	return nativeMap(v["counts"])
}

func TestMatchReviewQueueStateAndAccept(t *testing.T) {
	s, h, a, b, _ := matchHarness(t)
	start := accepted(t, call(t, h, a, "POST", "/api/v1/catalog/match-jobs", `{"ids":["alpha","elsewhere"]}`))
	id := nativeString(start["id"])
	waitFor(t, "job", func() bool {
		job := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/match-jobs/"+id, ""))
		return nativeMap(job["job"])["status"] == "complete"
	})
	base := "/api/v1/catalog/match-jobs/" + id + "/review"
	if _, err := s.store.DB.Exec(`INSERT INTO catalog_match_results(job_id,position,item_id,status,result) VALUES(?,2,'charlie','not_found','{"item_id":"charlie","title":"Charlie","status":"not_found","message":"No results"}')`, id); err != nil {
		t.Fatal(err)
	}

	// Permission: no edit permission → 403; someone else's job → 404.
	if w := call(t, h, b, "GET", base, ""); w.Code != 403 {
		t.Fatal("unprivileged review", w.Code)
	}
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true'),'$.accessAllLibraries',json('true')) WHERE id='bob'`); err != nil {
		t.Fatal(err)
	}
	if w := call(t, h, b, "GET", base, ""); w.Code != 404 {
		t.Fatal("other person's job", w.Code)
	}
	if w := call(t, h, b, "PUT", base+"/1", `{"state":"skipped"}`); w.Code != 404 {
		t.Fatal("other person's review write", w.Code)
	}
	if w := call(t, h, a, "GET", "/api/v1/catalog/match-jobs/match-AAAAAAAAAAAAAAAAAAAAAAAAAA/review", ""); w.Code != 404 {
		t.Fatal("missing job", w.Code)
	}

	q := jsonObject(t, call(t, h, a, "GET", base, ""))
	entries := nativeArray(q["entries"])
	if len(entries) != 2 || reviewCounts(t, q)["pending"] != float64(2) || nativeMap(q["statuses"])["not_found"] != float64(1) {
		t.Fatalf("queue %v", q)
	}
	low := nativeMap(entries[0])
	if low["item_id"] != "elsewhere" || low["status"] != "low_confidence" || low["review"] != "" || low["match_title"] != "Completely Unrelated" {
		t.Fatal("entry", low)
	}
	pos := fmt.Sprint(low["position"])
	if f := jsonObject(t, call(t, h, a, "GET", base+"?status=not_found", "")); len(nativeArray(f["entries"])) != 1 || nativeMap(nativeArray(f["entries"])[0])["item_id"] != "charlie" {
		t.Fatal("filter", f)
	}
	if w := call(t, h, a, "GET", base+"?status=matched", ""); w.Code != 400 {
		t.Fatal("bad filter", w.Code)
	}
	// Confident results are not part of the queue; states are validated.
	if w := call(t, h, a, "PUT", base+"/0", `{"state":"skipped"}`); w.Code != 404 {
		t.Fatal("matched result reviewed", w.Code)
	}
	if w := call(t, h, a, "PUT", base+"/"+pos, `{"state":"maybe"}`); w.Code != 400 {
		t.Fatal("bad state", w.Code)
	}

	// Skip, then undo: the queue resumes where it was.
	if r := jsonObject(t, call(t, h, a, "PUT", base+"/"+pos, `{"state":"skipped"}`)); r["review"] != "skipped" || r["item_id"] != "elsewhere" {
		t.Fatal("skip", r)
	}
	q = jsonObject(t, call(t, h, a, "GET", base, ""))
	if c := reviewCounts(t, q); c["skipped"] != float64(1) || c["reviewed"] != float64(1) || c["pending"] != float64(1) {
		t.Fatal("counts after skip", c)
	}
	jsonObject(t, call(t, h, a, "PUT", base+"/"+pos, `{"state":"pending"}`))
	if c := reviewCounts(t, jsonObject(t, call(t, h, a, "GET", base, ""))); c["pending"] != float64(2) {
		t.Fatal("undo", c)
	}

	// Candidates for the review: the stored provider, top results enriched
	// with details, edition flags for book and candidate, and the folder.
	found := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/items/book/match?provider=audible&detail=3", ""))
	cands := nativeArray(found["candidates"])
	if len(cands) == 0 {
		t.Fatal("no candidates", found)
	}
	top := nativeMap(cands[0])
	if _, ok := top["dramatized"]; !ok || nativeString(nativeArray(nativeMap(top["metadata"])["narrators"])[0]) != "A Reader" {
		t.Fatalf("detailed candidate %v", top)
	}
	if item := nativeMap(found["item"]); item["dramatized"] != false {
		t.Fatal("item edition", item)
	}

	// Accept applies through the editor's optimistic revision.
	it, _ := s.catalog().Item(context.Background(), a.u.ID, "elsewhere")
	if w := call(t, h, a, "POST", "/api/v1/catalog/items/elsewhere/match", fmt.Sprintf(`{"expected_updated_at":%d,"metadata":{"publisher":"Reviewed"}}`, it.UpdatedAt-1)); w.Code != 409 {
		t.Fatal("stale accept", w.Code)
	}
	if c := reviewCounts(t, jsonObject(t, call(t, h, a, "GET", base, ""))); c["accepted"] != float64(0) {
		t.Fatal("a failed accept changed the queue", c)
	}
	if r := jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/items/elsewhere/match", fmt.Sprintf(`{"expected_updated_at":%d,"metadata":{"publisher":"Reviewed"}}`, it.UpdatedAt))); r["ok"] != true {
		t.Fatal("accept", r)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/items/elsewhere/match", fmt.Sprintf(`{"expected_updated_at":%d,"metadata":{"publisher":"Again"}}`, it.UpdatedAt)); w.Code != 409 {
		t.Fatal("second accept with the old revision", w.Code)
	}
	jsonObject(t, call(t, h, a, "PUT", base+"/"+pos, `{"state":"accepted"}`))
	q = jsonObject(t, call(t, h, a, "GET", base, ""))
	if c := reviewCounts(t, q); c["accepted"] != float64(1) || c["pending"] != float64(1) {
		t.Fatal("counts after accept", c)
	}
	if nativeMap(nativeArray(q["entries"])[0])["review"] != "accepted" {
		t.Fatal("entry state", q)
	}

	// Deleting the job's report removes its review state.
	if w := call(t, h, a, "DELETE", "/api/v1/catalog/match-jobs/"+id, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	var n int
	s.store.DB.QueryRow(`SELECT count(*) FROM catalog_match_reviews WHERE job_id=?`, id).Scan(&n)
	if n != 0 {
		t.Fatal("review state kept after delete")
	}
}
