package server

import "testing"

func TestNativeTrackEditingClientPermissionsAndHistory(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	if w := call(t, h, b, "GET", "/api/v1/catalog/items/book/tracks", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE id='alice'`)
	jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/items/book/tracks", ""))
	session := jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"forceDirectPlay":true}`))
	sid := nativeString(session["id"])
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/sync", `{"reportId":"track-playing","currentTime":20,"timeListened":20,"playing":true}`))
	body := `{"orderedFileData":[{"ino":"12","exclude":false},{"ino":"11","exclude":false}]}`
	if w := nativeCall(t, h, tok, "PATCH", "/api/items/book/tracks", body); w.Code != 409 {
		t.Fatal("edited active playback", w.Code)
	}
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/close", `{"reportId":"track-pause","currentTime":20,"timeListened":0,"playing":false}`))
	jsonObject(t, nativeCall(t, h, tok, "PATCH", "/api/items/book/tracks", body))
	view := jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/items/book/tracks", ""))
	if nativeMap(nativeArray(view["tracks"])[0])["id"] != "12" {
		t.Fatal(view)
	}
	if w := nativeCall(t, h, tok, "GET", "/hls/"+sid+"/output.m3u8", ""); w.Code != 409 {
		t.Fatal("stale HLS reused", w.Code)
	}
	// A sync after close is answered like Audiobookshelf (404, so apps reopen),
	// but its listening still counts and its old timeline never moves progress.
	if w := nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/sync", `{"reportId":"track-late","currentTime":25,"timeListened":5,"playing":true}`); w.Code != 404 {
		t.Fatal("sync to ended session", w.Code)
	}
	var pos float64
	s.store.DB.QueryRow(`SELECT position FROM catalog_progress WHERE account_id='alice' AND item_id='book'`).Scan(&pos)
	if pos != 20 {
		t.Fatal("late old timeline overwrote position", pos)
	}
	var listened float64
	s.store.DB.QueryRow(`SELECT listened FROM catalog_play_sessions WHERE id=?`, sid).Scan(&listened)
	if listened != 25 {
		t.Fatal("late listening history lost", listened)
	}
	if w := nativeCall(t, h, tok, "PATCH", "/api/items/book/tracks", `{"orderedFileData":[{"ino":"12"},{"ino":"12"}]}`); w.Code != 400 {
		t.Fatal("duplicate tracks", w.Code)
	}
}
