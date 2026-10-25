package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Stock Socket.IO clients open with polling and upgrade to WebSocket; after the
// probe the pending poll ends with a noop and pushes arrive on the socket.
func TestNativeSocketPollingUpgradesToWebsocket(t *testing.T) {
	prev := realtimeWatchEvery.Get()
	realtimeWatchEvery.Set(50 * time.Millisecond)
	defer realtimeWatchEvery.Set(prev)
	s, h, a, _, tok := nativeHarness(t)
	server := httptest.NewServer(h)
	defer server.Close()
	w := nativeCall(t, h, "", "GET", "/socket.io/?EIO=4&transport=polling", "")
	var opened struct {
		SID      string   `json:"sid"`
		Upgrades []string `json:"upgrades"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes()[1:], &opened) != nil || len(opened.Upgrades) != 1 || opened.Upgrades[0] != "websocket" {
		t.Fatalf("handshake %d %s", w.Code, w.Body.String())
	}
	path := "/socket.io/?EIO=4&transport=polling&sid=" + opened.SID
	if w = nativeCall(t, h, "", "POST", path, "40\x1e"+socketEvent("auth", tok)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = nativeCall(t, h, "", "GET", path, ""); !strings.Contains(w.Body.String(), `"init"`) {
		t.Fatalf("poll %s", w.Body.String())
	}
	// A poll is pending while the client probes the WebSocket.
	pending := make(chan string, 1)
	go func() { pending <- nativeCall(t, h, "", "GET", path, "").Body.String() }()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/socket.io/?EIO=4&transport=websocket&sid="+opened.SID, nil)
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
	if err = conn.Write(ctx, websocket.MessageText, []byte("2probe")); err != nil {
		t.Fatal(err)
	}
	if msg := read(); msg != "3probe" {
		t.Fatal("probe answer", msg)
	}
	select {
	case msg := <-pending:
		if msg != "6" {
			t.Fatal("pending poll got", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending poll not released")
	}
	if err = conn.Write(ctx, websocket.MessageText, []byte("5")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if w = nativeCall(t, h, "", "GET", path, ""); w.Code != 400 {
		t.Fatal("polling after upgrade", w.Code)
	}
	// Progress from another device and an edit reach the upgraded socket.
	info := jsonObject(t, call(t, h, a, "POST", "/api/v1/play/book", `{"client":"browser"}`))
	sid := nativeString(info["session_id"])
	jsonObject(t, call(t, h, a, "POST", "/api/v1/sessions/"+sid+"/sync", `{"report_id":"up-1","observed_at":`+strconv.FormatInt(time.Now().UnixMilli(), 10)+`,"current_time":42,"listened":5,"playing":true}`))
	if msg := read(); !strings.Contains(msg, `"user_item_progress_updated"`) || !strings.Contains(msg, `"currentTime":42`) {
		t.Fatal(msg)
	}
	if _, err = s.store.DB.Exec(`UPDATE catalog_items SET title='Renamed live',document=json_set(document,'$.media.metadata.title','Renamed live'),updated_at=updated_at+100000 WHERE id='book'`); err != nil {
		t.Fatal(err)
	}
	for {
		msg := read()
		if strings.HasPrefix(msg, `42["item_updated"`) {
			if !strings.Contains(msg, `"id":"book"`) {
				t.Fatal(msg)
			}
			break
		}
	}
	jsonObject(t, call(t, h, a, "POST", "/api/v1/sessions/"+sid+"/sync", `{"report_id":"up-2","observed_at":`+strconv.FormatInt(time.Now().UnixMilli(), 10)+`,"current_time":43,"listened":1,"close":true}`))
	for {
		if msg := read(); strings.HasPrefix(msg, `42["user_session_closed"`) {
			if !strings.Contains(msg, sid) {
				t.Fatal(msg)
			}
			break
		}
	}
}

type sseReader struct {
	t   *testing.T
	sc  *bufio.Scanner
	out chan map[string]any
}

func openEvents(t *testing.T, url string, p person) (*sseReader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url+"/api/v1/events", nil)
	req.AddCookie(p.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	r := &sseReader{t: t, sc: bufio.NewScanner(res.Body), out: make(chan map[string]any, 64)}
	go func() {
		defer close(r.out)
		for r.sc.Scan() {
			line := r.sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			v := map[string]any{}
			if json.Unmarshal([]byte(line[6:]), &v) == nil {
				r.out <- v
			}
		}
	}()
	return r, func() { cancel(); res.Body.Close() }
}

// next returns the next event of a type, or nil after the timeout.
func (r *sseReader) next(kind string, timeout time.Duration) map[string]any {
	deadline := time.After(timeout)
	for {
		select {
		case v, ok := <-r.out:
			if !ok {
				return nil
			}
			if v["type"] == kind {
				return v
			}
		case <-deadline:
			return nil
		}
	}
}

// The web app's event stream carries the person's own progress, sessions and
// the items they can read — nobody else's.
func TestNativeWebEventsArePerPerson(t *testing.T) {
	prev := realtimeWatchEvery.Get()
	realtimeWatchEvery.Set(50 * time.Millisecond)
	defer realtimeWatchEvery.Set(prev)
	s, h, a, b, tok := nativeHarness(t)
	server := httptest.NewServer(h)
	defer server.Close()
	if anon, err := http.Get(server.URL + "/api/v1/events"); err != nil || anon.StatusCode != 401 {
		t.Fatal("anonymous events", err)
	}
	mine, closeMine := openEvents(t, server.URL, a)
	defer closeMine()
	theirs, closeTheirs := openEvents(t, server.URL, b)
	defer closeTheirs()
	if mine.next("hello", 2*time.Second) == nil || theirs.next("hello", 2*time.Second) == nil {
		t.Fatal("no hello")
	}
	// A phone (Audiobookshelf app) opens and reports.
	sid := nativeString(jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"forceDirectPlay":true,"deviceInfo":{"deviceId":"phone","deviceName":"Phone"}}`))["id"])
	if v := mine.next("session", 2*time.Second); v == nil || v["session_id"] != sid || v["closed"] != false {
		t.Fatal("session open event", v)
	}
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/sync", `{"currentTime":25,"timeListened":5}`))
	v := mine.next("progress", 2*time.Second)
	if v == nil || v["item_id"] != "book" || v["session_id"] != sid || nativeNumber(nativeMap(v["progress"])["currentTime"]) != 25 {
		t.Fatal("progress event", v)
	}
	if _, err := s.store.DB.Exec(`UPDATE catalog_items SET updated_at=updated_at+100000 WHERE id='book'`); err != nil {
		t.Fatal(err)
	}
	if v = mine.next("items", 3*time.Second); v == nil || !strings.Contains(string(nativeJSON(v["updated"])), `"book"`) {
		t.Fatal("item event", v)
	}
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/close", `{"currentTime":26,"timeListened":1}`))
	if v = mine.next("session", 2*time.Second); v == nil || v["closed"] != true {
		t.Fatal("session close event", v)
	}
	// Bob may not read the book: none of it reached him.
	for _, kind := range []string{"session", "progress", "items"} {
		if v := theirs.next(kind, 300*time.Millisecond); v != nil {
			t.Fatal("leaked to another person", v)
		}
	}
	// Scan results reach only people who can see the library.
	s.nativeScanComplete("lib", "scan-1", 1, 2, 0)
	if v = mine.next("scan_complete", 2*time.Second); v == nil || nativeNumber(v["updated"]) != 2 {
		t.Fatal("scan event", v)
	}
	if v = theirs.next("scan_complete", 300*time.Millisecond); v != nil {
		t.Fatal("scan of an inaccessible library leaked", v)
	}
}
