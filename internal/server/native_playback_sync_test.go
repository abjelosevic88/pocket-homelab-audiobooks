package server

import (
	"context"
	"testing"
)

// Audiobookshelf apps reopen only after a 404; a superseded session must say so
// while still keeping its listening seconds.
func TestNativeGatewaySyncOnReplacedSessionIs404(t *testing.T) {
	s, h, _, _, tok := nativeHarness(t)
	first := nativeString(jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"forceDirectPlay":true,"deviceInfo":{"deviceId":"phone"}}`))["id"])
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+first+"/sync", `{"currentTime":10,"timeListened":10}`))
	second := jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"forceDirectPlay":true,"deviceInfo":{"deviceId":"tablet"}}`))
	if nativeNumber(second["currentTime"]) != 10 {
		t.Fatal("new session did not resume newest progress", second["currentTime"])
	}
	if w := nativeCall(t, h, tok, "POST", "/api/session/"+first+"/sync", `{"currentTime":20,"timeListened":10}`); w.Code != 404 {
		t.Fatal("replaced session sync", w.Code, w.Body.String())
	}
	var listened float64
	s.store.DB.QueryRow(`SELECT listened FROM catalog_play_sessions WHERE id=?`, first).Scan(&listened)
	if listened != 20 {
		t.Fatal("replaced session lost listening", listened)
	}
	var pos float64
	s.store.DB.QueryRow(`SELECT position FROM catalog_progress WHERE account_id='alice' AND item_id='book'`).Scan(&pos)
	if pos != 10 {
		t.Fatal("replaced session moved progress", pos)
	}
	// Closing a replaced session is still acknowledged.
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+first+"/close", `{"currentTime":20,"timeListened":0}`))
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+nativeString(second["id"])+"/sync", `{"currentTime":30,"timeListened":20}`))
}

// Audiobookshelf syncs carry no receipt ID; an identical retried payload must
// not add its listening a second time.
func TestNativeGatewaySyncRetryIsIdempotent(t *testing.T) {
	s, h, _, _, tok := nativeHarness(t)
	sid := nativeString(jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"forceDirectPlay":true}`))["id"])
	body := `{"currentTime":15,"timeListened":15,"duration":100}`
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/sync", body))
	if retry := jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/sync", body)); retry["duplicate"] != true {
		t.Fatal("retry not recognised", retry)
	}
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/sync", `{"currentTime":30,"timeListened":15,"duration":100}`))
	var listened float64
	s.store.DB.QueryRow(`SELECT listened FROM catalog_play_sessions WHERE id=?`, sid).Scan(&listened)
	if listened != 30 {
		t.Fatal("retried sync double counted", listened)
	}
}

// Another device opening the book closes this session, but its in-flight stream
// keeps buffering; only the owner can read it.
func TestNativeHLSOutlivesHandoff(t *testing.T) {
	s, h, _, b, tok := nativeHarness(t)
	sid := nativeString(jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"forceDirectPlay":true}`))["id"])
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"forceDirectPlay":true}`))
	if w := nativeCall(t, h, tok, "GET", "/hls/"+sid+"/index.m3u8", ""); w.Code != 200 {
		t.Fatal("handoff cut the stream", w.Code, w.Body.String())
	}
	other, _, err := s.store.CreateAPIToken(context.Background(), b.u.ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	if w := nativeCall(t, h, other, "GET", "/hls/"+sid+"/index.m3u8", ""); w.Code == 200 {
		t.Fatal("another person read the stream")
	}
}
