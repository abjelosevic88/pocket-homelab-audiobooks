package server

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/metadata"
)

type fakeAudible struct {
	mu       sync.Mutex
	titles   []string
	blocked  chan struct{}
	cover    []byte
	products map[string]string // search title → product JSON
	details  map[string]string // ASIN → Audnexus JSON
}

func (f *fakeAudible) roundTrip(r *http.Request) (*http.Response, error) {
	body := ""
	switch {
	case r.URL.Host == "api.audible.com" && r.URL.Path == "/1.0/catalog/products":
		title := r.URL.Query().Get("title") + r.URL.Query().Get("keywords")
		f.mu.Lock()
		f.titles = append(f.titles, title)
		f.mu.Unlock()
		if title == "Blocker" && f.blocked != nil {
			close(f.blocked)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		body = `{"products":[` + f.products[title] + `]}`
	case r.URL.Host == "api.audnex.us" && strings.HasPrefix(r.URL.Path, "/books/"):
		d, ok := f.details[strings.TrimPrefix(r.URL.Path, "/books/")]
		if !ok {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
		}
		body = d
	case r.URL.Host == "m.media-amazon.com":
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(f.cover)), Header: http.Header{"Content-Type": {"image/png"}}}, nil
	default:
		return nil, fmt.Errorf("unexpected request %s", r.URL)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
}

func matchHarness(t *testing.T) (*Server, http.Handler, person, person, *fakeAudible) {
	t.Helper()
	s, h, a, b, _ := nativeHarness(t)
	old := metadata.AudibleLimiter
	metadata.AudibleLimiter = metadata.NewLimiter(0)
	t.Cleanup(func() { metadata.AudibleLimiter = old })
	var img bytes.Buffer
	png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	f := &fakeAudible{cover: img.Bytes(), products: map[string]string{
		"Native book": `{"asin":"B000000001","title":"Native Book","authors":[{"name":"An Author"}],"runtime_length_min":2,"product_images":{"500":"https://m.media-amazon.com/images/I/x._SL500_.jpg"}},{"asin":"B000000009","title":"Native Book Two","authors":[{"name":"Other"}]}`,
		"Alpha":       `{"asin":"B0000000A1","title":"Alpha","authors":[{"name":"An author"}]}`,
		"Charlie":     `{"asin":"B0000000C1","title":"Charlie","authors":[{"name":"An author"}]}`,
		"Elsewhere":   `{"asin":"B0000000E1","title":"Completely Unrelated","authors":[{"name":"Someone"}]}`,
	}, details: map[string]string{
		"B000000001": `{"asin":"B000000001","title":"Native Book","authors":[{"name":"An Author"}],"narrators":[{"name":"A Reader"}],"seriesPrimary":{"name":"Saga","position":"Book 3"},"summary":"<p>From <b>Audible</b></p>","publisherName":"Pub","releaseDate":"2020-02-03T00:00:00.000Z","language":"english","genres":[{"name":"Fiction","type":"genre"}],"image":"https://m.media-amazon.com/images/I/x.jpg","isbn":"9780000000001","runtimeLengthMin":2}`,
	}}
	s.nativeFetchHTTP = &http.Client{Transport: gatewayTransport(f.roundTrip)}
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE id='alice'; UPDATE catalog_items SET document=json_set(document,'$.media.description','Keep me') WHERE id='book'`); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Alpha", "Blocker", "Charlie", "Elsewhere"} {
		if _, err := s.store.DB.Exec(`INSERT INTO catalog_items SELECT ?,library_id,?,media_type,?,author,duration,explicit,missing,added_at,updated_at,tags,json_set(document,'$.media.title',?) FROM catalog_items WHERE id='book'`, strings.ToLower(title), "m-"+title, title, title); err != nil {
			t.Fatal(err)
		}
	}
	return s, h, a, b, f
}

func TestMatchSearchQuickMatchFillsOnlyEmptyFields(t *testing.T) {
	s, h, a, b, _ := matchHarness(t)
	if w := call(t, h, b, "GET", "/api/v1/catalog/items/book/match", ""); w.Code != 403 {
		t.Fatal("unprivileged match", w.Code)
	}
	found := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/items/book/match", ""))
	cands := nativeArray(found["candidates"])
	if found["provider"] != "audible" || len(cands) != 2 {
		t.Fatalf("search %v", found)
	}
	best := nativeMap(cands[0])
	if best["id"] != "B000000001" || nativeMap(best["score"])["confident"] != true {
		t.Fatalf("ranking %v", cands)
	}
	if nativeMap(cands[1])["score"].(map[string]any)["confident"] == true {
		t.Fatal("wrong book confident")
	}
	// Searching never changes the book.
	if it, _ := s.catalog().Item(context.Background(), a.u.ID, "book"); it.Title != "Native book" {
		t.Fatal("search mutated catalog")
	}

	res := jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/items/book/quick-match", `{}`))
	if res["status"] != "matched" || res["cover"] != true {
		t.Fatalf("quick match %v", res)
	}
	fields := strings.Join(func() []string {
		out := []string{}
		for _, v := range nativeArray(res["fields"]) {
			out = append(out, nativeString(v))
		}
		return out
	}(), ",")
	if strings.Contains(fields, "description") || strings.Contains(fields, "title") || !strings.Contains(fields, "publisher") || !strings.Contains(fields, "isbn") || !strings.Contains(fields, "series") {
		t.Fatal("fields", fields)
	}
	ed := nativeMap(jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/items/book/editor", ""))["metadata"])
	if ed["description"] != "Keep me" || ed["title"] != "Native book" || ed["publisher"] != "Pub" || ed["asin"] != "B000000001" || ed["language"] != "English" {
		t.Fatalf("fill-empty semantics %v", ed)
	}
	if series := nativeMap(nativeArray(ed["series"])[0]); series["name"] != "Saga" || series["sequence"] != "3" {
		t.Fatal("series", series)
	}
	if nativeString(nativeArray(ed["narrators"])[0]) != "A Reader" || len(nativeArray(ed["tags"])) != 0 {
		t.Fatal("narrators/tags", ed)
	}
	// Everything filled and a cover present: nothing more to do.
	if res = jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/items/book/quick-match", `{}`)); res["status"] != "skipped" {
		t.Fatalf("second run %v", res)
	}
	// The item has an ASIN now: the lookup is direct and exact; override replaces details.
	if res = jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/items/book/quick-match", `{"override_details":true}`)); res["status"] != "matched" || res["exact"] != true {
		t.Fatalf("override %v", res)
	}
	ed = nativeMap(jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/items/book/editor", ""))["metadata"])
	if ed["description"] != "From Audible" || ed["title"] != "Native Book" {
		t.Fatal("override details", ed)
	}
	if res = jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/items/elsewhere/quick-match", `{}`)); res["status"] != "low_confidence" {
		t.Fatalf("low confidence %v", res)
	}
	if it, _ := s.catalog().Item(context.Background(), a.u.ID, "elsewhere"); it.Title != "Elsewhere" {
		t.Fatal("low confidence changed the book")
	}

	// Manual apply uses the editor's revision check.
	it, _ := s.catalog().Item(context.Background(), a.u.ID, "alpha")
	if w := call(t, h, a, "POST", "/api/v1/catalog/items/alpha/match", fmt.Sprintf(`{"expected_updated_at":%d,"metadata":{"publisher":"Chosen"}}`, it.UpdatedAt-1)); w.Code != 409 {
		t.Fatal("stale apply", w.Code)
	}
	applied := jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/items/alpha/match", fmt.Sprintf(`{"expected_updated_at":%d,"metadata":{"publisher":"Chosen"},"cover":"https://m.media-amazon.com/images/I/x.jpg"}`, it.UpdatedAt)))
	if applied["ok"] != true {
		t.Fatal(applied)
	}
	if it2, _ := s.catalog().Item(context.Background(), a.u.ID, "alpha"); !strings.Contains(string(it2.Document), "Chosen") || !strings.Contains(string(it2.Document), "pocket-native-art") {
		t.Fatal("apply", string(it2.Document))
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBulkQuickMatchJobReportsAndCancels(t *testing.T) {
	s, h, a, _, f := matchHarness(t)
	if w := call(t, h, a, "POST", "/api/v1/catalog/match-jobs", `{"library_id":"missing"}`); w.Code != 404 {
		t.Fatal("inaccessible library", w.Code)
	}
	start := accepted(t, call(t, h, a, "POST", "/api/v1/catalog/match-jobs", `{"ids":["alpha","charlie","elsewhere"]}`))
	id := nativeString(start["id"])
	job := nativeObject{}
	waitFor(t, "job", func() bool {
		job = jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/match-jobs/"+id, ""))
		return nativeMap(job["job"])["status"] == "complete"
	})
	counts := nativeMap(nativeMap(job["job"])["counts"])
	if counts["matched"] != float64(2) || counts["low_confidence"] != float64(1) || len(nativeArray(job["results"])) != 3 {
		t.Fatalf("report %v", job)
	}
	if r := nativeMap(nativeArray(job["results"])[2]); r["item_id"] != "elsewhere" || r["match_title"] != "Completely Unrelated" {
		t.Fatal(r)
	}
	if filtered := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/match-jobs/"+id+"?status=matched", "")); len(nativeArray(filtered["results"])) != 2 {
		t.Fatal("filter", filtered)
	}

	// Cancellation stops between books and the blocked request is abandoned.
	f.blocked = make(chan struct{})
	f.titles = nil
	start = accepted(t, call(t, h, a, "POST", "/api/v1/catalog/match-jobs", `{"ids":["alpha","blocker","charlie"]}`))
	id = nativeString(start["id"])
	<-f.blocked
	if w := call(t, h, a, "POST", "/api/v1/catalog/match-jobs", `{"ids":["alpha"]}`); w.Code != 409 {
		t.Fatal("second concurrent job", w.Code)
	}
	if w := call(t, h, a, "DELETE", "/api/v1/catalog/match-jobs/"+id, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	waitFor(t, "runner exit", func() bool {
		matchRuns.Lock()
		defer matchRuns.Unlock()
		return matchRuns.cancel[id] == nil
	})
	job = jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/match-jobs/"+id, ""))
	if nativeMap(job["job"])["status"] != "cancelled" || len(nativeArray(job["results"])) != 1 {
		t.Fatalf("cancelled job %v", job)
	}
	f.mu.Lock()
	for _, title := range f.titles {
		if title == "Charlie" {
			t.Fatal("job continued after cancellation")
		}
	}
	f.mu.Unlock()
	// Jobs are private and finished ones can be removed.
	list := call(t, h, a, "GET", "/api/v1/catalog/match-jobs", "")
	if !strings.Contains(list.Body.String(), id) {
		t.Fatal("list", list.Body.String())
	}
	if w := call(t, h, a, "DELETE", "/api/v1/catalog/match-jobs/"+id, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	var n int
	s.store.DB.QueryRow(`SELECT count(*) FROM catalog_match_results WHERE job_id=?`, id).Scan(&n)
	if n != 0 {
		t.Fatal("results kept after delete")
	}
}

func TestInterruptedMatchJobIsReportedAndAudibleChaptersClamp(t *testing.T) {
	s, h, a, _, f := matchHarness(t)
	s.store.DB.Exec(`INSERT INTO catalog_match_jobs(id,user_id,status,total,created_at,updated_at) VALUES('match-AAAAAAAAAAAAAAAAAAAAAAAAAA',?,'running',5,1,1)`, a.u.ID)
	job := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/match-jobs/match-AAAAAAAAAAAAAAAAAAAAAAAAAA", ""))
	if j := nativeMap(job["job"]); j["status"] != "failed" || !strings.Contains(nativeString(j["error"]), "restart") {
		t.Fatal(job)
	}
	f.details["B000000001/chapters"] = `{"asin":"B000000001","isAccurate":true,"runtimeLengthMs":400000,"chapters":[{"title":"One","startOffsetMs":0,"lengthMs":60000},{"title":"Two","startOffsetMs":60000,"lengthMs":60000},{"title":"Three","startOffsetMs":120000,"lengthMs":280000}]}`
	ch := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/items/book/audible-chapters?asin=B000000001", ""))
	list := nativeArray(ch["chapters"])
	if ch["mismatch"] != true || len(list) != 2 || nativeMap(list[1])["end"] != float64(100) {
		t.Fatalf("chapters %v", ch)
	}
}

func accepted(t *testing.T, w *httptest.ResponseRecorder) nativeObject {
	t.Helper()
	if w.Code != 202 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	w.Code = 200
	return jsonObject(t, w)
}
