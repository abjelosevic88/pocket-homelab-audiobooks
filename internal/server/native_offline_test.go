package server

import (
	"testing"
	"time"
)

func TestNativeOfflineSessionAPI(t *testing.T) {
	_, h, _, _, tok := nativeHarness(t)
	now := time.Now().UnixMilli()
	session := nativeObject{"id": "downloaded-session", "libraryItemId": "book", "currentTime": 25, "startTime": 0, "timeListening": 25, "startedAt": now - 30000, "updatedAt": now - 1000}
	for i := 0; i < 2; i++ {
		jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/local", string(nativeJSON(session))))
	}
	stats := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/me/listening-stats", ""))
	if nativeNumber(stats["totalTime"]) != 25 {
		t.Fatal("retry double counted", stats)
	}
	batch := nativeObject{"sessions": []any{session, nativeObject{"id": "bad", "libraryItemId": "missing"}}, "deviceInfo": nativeObject{"deviceId": "phone", "deviceName": "Phone"}}
	result := jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/local-all", string(nativeJSON(batch))))
	entries := nativeArray(result["results"])
	if len(entries) != 2 || nativeMap(entries[0])["success"] != true || nativeMap(entries[1])["success"] != false {
		t.Fatal("batch isolation", result)
	}
	me := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/me", ""))
	if nativeNumber(nativeMap(nativeArray(me["mediaProgress"])[0])["currentTime"]) != 25 {
		t.Fatal("offline progress", me)
	}
}
