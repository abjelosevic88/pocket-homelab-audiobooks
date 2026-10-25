package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestNativeSocketWebsocketAndPolling(t *testing.T) {
	s, h, a, _, tok := nativeHarness(t)
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/socket.io/?EIO=4&transport=websocket", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	read := func() string {
		t.Helper()
		_, b, e := conn.Read(ctx)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	write := func(v string) {
		t.Helper()
		if e := conn.Write(ctx, websocket.MessageText, []byte(v)); e != nil {
			t.Fatal(e)
		}
	}
	if msg := read(); !strings.HasPrefix(msg, "0{") {
		t.Fatalf("handshake %s", msg)
	}
	write("40")
	if msg := read(); !strings.HasPrefix(msg, "40{") {
		t.Fatal(msg)
	}
	write(socketEvent("auth", tok))
	if msg := read(); !strings.Contains(msg, `"init"`) {
		t.Fatal(msg)
	}
	info := jsonObject(t, call(t, h, a, "POST", "/api/v1/play/book", `{"client":"browser"}`))
	sid := nativeString(info["session_id"])
	report := nativeObject{"report_id": "socket-report", "observed_at": time.Now().UnixMilli(), "current_time": 30, "listened": 5, "playing": true}
	jsonObject(t, call(t, h, a, "POST", "/api/v1/sessions/"+sid+"/sync", string(nativeJSON(report))))
	if msg := read(); !strings.Contains(msg, `"user_item_progress_updated"`) || !strings.Contains(msg, `"currentTime":30`) {
		t.Fatal(msg)
	}
	// Default Socket.IO clients choose polling when no upgrade is advertised.
	w := nativeCall(t, h, "", "GET", "/socket.io/?EIO=4&transport=polling", "")
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "0{") {
		t.Fatalf("poll open %d %s", w.Code, w.Body.String())
	}
	var opened nativeObject
	if err = json.Unmarshal(w.Body.Bytes()[1:], &opened); err != nil {
		t.Fatal(err)
	}
	path := "/socket.io/?EIO=4&transport=polling&sid=" + nativeString(opened["sid"])
	w = nativeCall(t, h, "", "POST", path, "40\x1e"+socketEvent("auth", tok))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	// One poll may carry several packets (0x1e-separated).
	polled := ""
	for i := 0; i < 2 && !(strings.Contains(polled, "40{") && strings.Contains(polled, `"init"`)); i++ {
		w = nativeCall(t, h, "", "GET", path, "")
		if w.Code != 200 {
			t.Fatalf("poll %d %s", w.Code, w.Body.String())
		}
		polled += w.Body.String() + "\x1e"
	}
	if !strings.HasPrefix(polled, "40{") || !strings.Contains(polled, `"init"`) {
		t.Fatalf("polls %q", polled)
	}
	// Revocation must stop an existing transport, not only the next login.
	if _, err = s.store.DB.Exec(`DELETE FROM api_tokens WHERE user_id=?`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	w = nativeCall(t, h, "", "GET", path, "")
	if w.Code != 401 {
		t.Fatalf("revoked polling token %d", w.Code)
	}
}
