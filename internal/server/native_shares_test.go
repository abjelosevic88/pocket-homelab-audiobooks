package server

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func publicShareCall(h http.Handler, method, path, password string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if password != "" {
		r.SetBasicAuth("reader", password)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	if strings.Contains(path, "/file/") {
		r.Header.Set("Range", "bytes=2-5")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestNativeSharesPasswordExpiryPermissionsAndRevocation(t *testing.T) {
	s, h, a, b, _ := nativeHarness(t)
	s.cfg.BaseURL = "https://audio.example.test"
	create := `{"kind":"item","target_id":"book","name":"A <book> & friends","password":"share-pass-123","downloadable":false}`
	if w := call(t, h, a, "POST", "/api/v1/shares", create); w.Code != 403 {
		t.Fatal("non-admin published share", w.Code)
	}
	s.store.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, a.u.ID)
	v := jsonObject(t, call(t, h, a, "POST", "/api/v1/shares", create))
	slug, id := nativeString(v["slug"]), nativeString(v["id"])
	if strings.Contains(string(nativeJSON(v)), "share-pass") || v["password_hash"] != nil {
		t.Fatal("password leaked", v)
	}
	path := "/share/" + slug
	for _, p := range []string{path, "/feed/" + slug, path + "/file/book/11", path + "/hls/book/11/index.m3u8"} {
		if w := publicShareCall(h, "GET", p, "", nil); w.Code != 401 {
			t.Fatal("password bypass", p, w.Code)
		}
	}
	unlocked := publicShareCall(h, "GET", path, "share-pass-123", nil)
	if unlocked.Code != 200 || !strings.Contains(unlocked.Body.String(), "A &lt;book&gt; &amp; friends") {
		t.Fatal(unlocked.Code, unlocked.Body.String())
	}
	cookies := unlocked.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("no protected share grant")
	}
	if w := publicShareCall(h, "HEAD", path+"/hls/book/11/segment-0-0.ts", "", cookies); w.Code != 200 {
		t.Fatal("share HLS", w.Code)
	}
	audio := publicShareCall(h, "GET", path+"/file/book/11", "", cookies)
	if audio.Code != 206 || audio.Body.String() != "2345" {
		t.Fatal("range", audio.Code, audio.Body.String())
	}
	if w := publicShareCall(h, "GET", path+"/download/book", "", cookies); w.Code != 403 {
		t.Fatal("disabled download", w.Code)
	}
	if w := publicShareCall(h, "GET", path+"/file/another/11", "", cookies); w.Code != 404 {
		t.Fatal("outside share", w.Code)
	}
	rss := publicShareCall(h, "GET", "/feed/"+slug, "", cookies)
	var feed struct {
		Channel struct {
			Title string `xml:"title"`
			Items []struct {
				Enclosure struct {
					URL string `xml:"url,attr"`
				} `xml:"enclosure"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if rss.Code != 200 || xml.Unmarshal(rss.Body.Bytes(), &feed) != nil || len(feed.Channel.Items) != 2 || feed.Channel.Title != "A <book> & friends" {
		t.Fatal("RSS", rss.Code, rss.Body.String())
	}
	if !strings.HasPrefix(feed.Channel.Items[0].Enclosure.URL, s.cfg.BaseURL+path+"/file/") {
		t.Fatal("wrong enclosure URL", feed)
	}
	if w := call(t, h, b, "DELETE", "/api/v1/shares/"+id, ""); w.Code != 403 {
		t.Fatal("foreign revoke", w.Code)
	}
	// Changing the password invalidates a previous signed cookie immediately.
	update := fmt.Sprintf(`{"name":"Renamed","password":"replacement-pass","downloadable":true,"revision":%v}`, v["revision"])
	jsonObject(t, call(t, h, a, "PUT", "/api/v1/shares/"+id, update))
	if w := publicShareCall(h, "GET", path+"/file/book/11", "", cookies); w.Code != 401 {
		t.Fatal("old password cookie survived", w.Code)
	}
	if w := call(t, h, a, "PUT", "/api/v1/shares/"+id, update); w.Code != 409 {
		t.Fatal("stale update accepted", w.Code)
	}
	if w := publicShareCall(h, "GET", path+"/download/book", "replacement-pass", nil); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "PK") {
		t.Fatal("archive", w.Code)
	}
	s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.download',json('false')) WHERE id='alice'`)
	if w := publicShareCall(h, "GET", path, "replacement-pass", nil); w.Code != 404 {
		t.Fatal("publisher permission revoked", w.Code)
	}
	s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.download',json('true')) WHERE id='alice'`)
	s.store.DB.Exec(`UPDATE catalog_shares SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).UnixMilli(), id)
	if w := publicShareCall(h, "GET", path, "replacement-pass", nil); w.Code != 404 {
		t.Fatal("expired share served", w.Code)
	}
	s.store.DB.Exec(`UPDATE catalog_shares SET expires_at=0 WHERE id=?`, id)
	if w := call(t, h, a, "DELETE", "/api/v1/shares/"+id, ""); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := publicShareCall(h, "GET", path, "replacement-pass", nil); w.Code != 404 {
		t.Fatal("revoked share served", w.Code)
	}
	for _, table := range []string{"catalog_history", "catalog_progress", "catalog_play_sessions"} {
		var n int
		if err := s.store.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("public playback changed %s n=%d err=%v", table, n, err)
		}
	}
}
func TestNativeSharesFollowCollectionsAndSeries(t *testing.T) {
	s, h, a, _, _ := nativeHarness(t)
	s.cfg.BaseURL = "https://audio.example.test"
	s.store.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, a.u.ID)
	s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE id='alice'`)
	c := s.catalog()
	list, err := c.CreateList(context.Background(), a.u.ID, "collections", "lib", "Shared", "", []library.ListEntry{{ItemID: "book"}})
	if err != nil {
		t.Fatal(err)
	}
	v := jsonObject(t, call(t, h, a, "POST", "/api/v1/shares", fmt.Sprintf(`{"kind":"collection","target_id":%q}`, list.ID)))
	path := "/feed/" + nativeString(v["slug"])
	if w := publicShareCall(h, "GET", path, "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	empty := []library.ListEntry{}
	if _, err = c.EditList(context.Background(), a.u.ID, "collections", list.ID, library.ListEdit{Entries: &empty}); err != nil {
		t.Fatal(err)
	}
	if w := publicShareCall(h, "GET", path, "", nil); w.Code != 404 {
		t.Fatal("removed member stayed public", w.Code)
	}
	s.store.DB.Exec(`UPDATE catalog_items SET document=json_set(document,'$.series',json('[{"id":"series","name":"Series","sequence":"1"}]')) WHERE id='book'`)
	v = jsonObject(t, call(t, h, a, "POST", "/api/v1/shares", `{"kind":"series","target_id":"series"}`))
	if w := publicShareCall(h, "GET", "/feed/"+nativeString(v["slug"]), "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestNativeSharePasswordThrottle(t *testing.T) {
	s, h, a, _, _ := nativeHarness(t)
	s.cfg.BaseURL = "https://audio.example.test"
	s.store.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, a.u.ID)
	v := jsonObject(t, call(t, h, a, "POST", "/api/v1/shares", `{"kind":"item","target_id":"book","password":"secret-password"}`))
	path := "/share/" + nativeString(v["slug"])
	for i := 0; i < 12; i++ {
		if w := publicShareCall(h, "GET", path, "incorrect", nil); w.Code != 401 {
			t.Fatal(i, w.Code)
		}
	}
	if w := publicShareCall(h, "GET", path, "incorrect", nil); w.Code != 429 {
		t.Fatal("no password throttle", w.Code)
	}
}

func TestNativeShareClientContractsAndEpisodeBoundary(t *testing.T) {
	s, h, a, _, tok := nativeHarness(t)
	s.cfg.BaseURL = "https://audio.example.test"
	s.store.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, a.u.ID)
	feed := jsonObject(t, nativeCall(t, h, tok, "POST", "/api/feeds/item/book/open", `{"slug":"client-feed","serverAddress":"https://untrusted.example"}`))
	f := nativeMap(feed["feed"])
	if len(nativeArray(f["episodes"])) != 2 {
		t.Fatal("feed detail missing tracks", f)
	}
	if nativeString(f["feedUrl"]) != s.cfg.BaseURL+"/feed/client-feed" {
		t.Fatal("untrusted feed host", f)
	}
	all := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/feeds", ""))
	if len(nativeArray(all["feeds"])) != 1 {
		t.Fatal(all)
	}
	if w := nativeCall(t, h, tok, "POST", "/api/feeds/"+nativeString(f["id"])+"/close", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := publicShareCall(h, "GET", "/feed/client-feed", "", nil); w.Code != 404 {
		t.Fatal("closed compatibility feed active")
	}
	// A podcast episode share may not expose another episode even with downloads enabled.
	s.store.DB.Exec(`UPDATE catalog_items SET media_type='podcast',document=json_set(document,'$.episodes',json('[{"id":"one","title":"One","audioFile":{"ino":"11","duration":50}},{"id":"two","title":"Two","audioFile":{"ino":"12","duration":50}}]')) WHERE id='book'`)
	w := nativeCall(t, h, tok, "POST", "/api/share/mediaitem", `{"slug":"one-episode","mediaItemType":"podcastEpisode","mediaItemId":"one","isDownloadable":true,"expiresAt":0}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := publicShareCall(h, "GET", "/share/one-episode/file/book/11", "", nil); w.Code != 206 {
		t.Fatal("shared episode unavailable", w.Code, w.Body.String())
	}
	if w := publicShareCall(h, "GET", "/share/one-episode/file/book/12", "", nil); w.Code != 404 {
		t.Fatal("other episode leaked", w.Code)
	}
	if w := publicShareCall(h, "GET", "/share/one-episode/download/book", "", nil); w.Code != 403 {
		t.Fatal("podcast archive leaked", w.Code)
	}
}
