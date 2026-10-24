package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/web"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeHarness(t *testing.T) (*Server, http.Handler, person, person, string) {
	t.Helper()
	s, h, _ := testServer(t)
	s.cfg.Backend = "native"
	blocked := gatewayTransport(func(*http.Request) (*http.Response, error) {
		t.Error("native backend called an upstream service")
		return nil, io.ErrUnexpectedEOF
	})
	s.absHTTP = &http.Client{Transport: blocked}
	s.streamHTTP = &http.Client{Transport: blocked}
	a := addPerson(t, s, "native-a@example.com", "unused-a")
	b := addPerson(t, s, "native-b@example.com", "unused-b")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "audio.mp3"), []byte("0123456789audio"), 0600); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO catalog_roots VALUES('root','/media',?)`, root)
	exec(`INSERT INTO catalog_libraries VALUES('lib','Books','book',0,'{}')`)
	exec(`INSERT INTO catalog_accounts VALUES('alice',?,'Alice',1,0,'{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"download":true}')`, a.u.ID)
	exec(`INSERT INTO catalog_accounts VALUES('bob',?,'Bob',1,0,'{"accessAllLibraries":false,"librariesAccessible":[],"accessAllTags":true}')`, b.u.ID)
	exec(`INSERT INTO catalog_items VALUES('book','lib','media','book','Native book','An author',100,0,0,1,1,'[]',?)`, string(nativeJSON(nativeObject{"media": nativeObject{"title": "Native book", "chapters": []any{nativeObject{"id": 0, "start": 0, "end": 100, "title": "Chapter one"}}, "audioFiles": []any{nativeObject{"ino": "11", "duration": 50, "mimeType": "audio/mpeg"}, nativeObject{"ino": "12", "duration": 50, "mimeType": "audio/mpeg"}}}})))
	for i := 1; i <= 2; i++ {
		exec(`INSERT INTO catalog_files VALUES('book',?,'audio','root','audio.mp3','audio.mp3','audio/mpeg',15,50,?,0,'{}')`, fmt.Sprint(10+i), i)
	}
	tok, _, err := s.store.CreateAPIToken(context.Background(), a.u.ID, "native test")
	if err != nil {
		t.Fatal(err)
	}
	return s, h, a, b, tok
}
func nativeCall(t *testing.T, h http.Handler, tok, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+tok)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if strings.Contains(path, "/file/") {
		r.Header.Set("Range", "bytes=2-5")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func jsonObject(t *testing.T, w *httptest.ResponseRecorder) nativeObject {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	out := nativeObject{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestNativePlaybackThroughWebAndClientAPI(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	for _, path := range []string{"/api/v1/home", "/api/v1/libraries", "/api/v1/books/book", "/api/v1/search?q=Native", "/api/v1/stats", "/api/v1/connection"} {
		w := call(t, h, a, "GET", path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	info := jsonObject(t, call(t, h, a, "POST", "/api/v1/play/book", `{"client":"browser","mime_types":["audio/mpeg"]}`))
	if info["backend"] != "native" || len(nativeArray(info["tracks"])) != 2 {
		t.Fatalf("play: %v", info)
	}
	sid := nativeString(info["session_id"])
	track := nativeMap(nativeArray(info["tracks"])[1])
	w := call(t, h, a, "GET", nativeString(track["url"]), "")
	if w.Code != 206 || w.Body.String() != "0123456789audio" {
		t.Fatalf("web media: %d %q", w.Code, w.Body.String())
	}
	report := nativeObject{"report_id": "receipt-1", "observed_at": time.Now().UnixMilli(), "current_time": 60, "listened": 30, "playing": true}
	for i := 0; i < 2; i++ {
		jsonObject(t, call(t, h, a, "POST", "/api/v1/sessions/"+sid+"/sync", string(nativeJSON(report))))
	}
	if w = call(t, h, b, "POST", "/api/v1/sessions/"+sid+"/sync", string(nativeJSON(report))); w.Code != 404 {
		t.Fatal("cross-user session", w.Code)
	}
	now := jsonObject(t, call(t, h, a, "GET", "/api/v1/now-playing?client=other", ""))
	if now["position"] != float64(60) || now["playing"] != true {
		t.Fatalf("now playing: %v", now)
	}
	stats := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/me/listening-stats", ""))
	if nativeNumber(stats["totalTime"]) != 30 {
		t.Fatalf("retry doubled listening: %v", stats)
	}
	progress := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/me/items-in-progress", ""))
	if len(nativeArray(progress["libraryItems"])) != 1 {
		t.Fatalf("continue: %v", progress)
	}
	// Native app takes over and resumes at the web player's committed position.
	phone := jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"deviceInfo":{"deviceId":"phone","deviceName":"Phone"},"forceDirectPlay":true}`))
	if nativeNumber(phone["startTime"]) != 60 || nativeNumber(phone["currentTime"]) != 60 {
		t.Fatalf("resume: %v", phone)
	}
	psid := nativeString(phone["id"])
	w = nativeCall(t, h, tok, "GET", "/api/items/book/file/12", "")
	if w.Code != 206 || w.Body.String() != "2345" {
		t.Fatalf("client range %d %q", w.Code, w.Body.String())
	}
	// A late browser report counts listening but cannot move the phone backwards.
	report["report_id"] = "late"
	report["current_time"] = 10
	report["observed_at"] = time.Now().UnixMilli() + 1
	report["listened"] = 1
	jsonObject(t, call(t, h, a, "POST", "/api/v1/sessions/"+sid+"/sync", string(nativeJSON(report))))
	me := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/me", ""))
	if nativeNumber(nativeMap(nativeArray(me["mediaProgress"])[0])["currentTime"]) != 60 {
		t.Fatal("handoff rewound progress")
	}
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+psid+"/close", `{}`))
	session, err := s.catalog().Session(context.Background(), a.u.ID, psid)
	if err != nil || session.Position != 60 || !session.Closed {
		t.Fatalf("empty close lost position: %+v %v", session, err)
	}
	// Persistent records stay available through a new server instance.
	restarted := New(s.cfg, s.store, s.log, nil)
	me = jsonObject(t, nativeCall(t, restarted.Router(), tok, "GET", "/api/me", ""))
	if nativeNumber(nativeMap(nativeArray(me["mediaProgress"])[0])["currentTime"]) != 60 {
		t.Fatal("restart lost progress")
	}
}

func TestNativeTranscodingIsLocalAndSeekable(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	s, h, a, _, tok := nativeHarness(t)
	s.cfg.FFmpeg = ffmpeg
	var root string
	if err = s.store.DB.QueryRow(`SELECT local_path FROM catalog_roots WHERE id='root'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "7", "-c:a", "libmp3lame", filepath.Join(root, "audio.mp3"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture audio: %v %s", err, out)
	}
	if _, err = s.store.DB.Exec(`UPDATE catalog_files SET duration=7 WHERE item_id='book'`); err != nil {
		t.Fatal(err)
	}
	info := jsonObject(t, call(t, h, a, "POST", "/api/v1/play/book", `{"client":"browser","mime_types":["audio/unsupported"],"force_transcode":true}`))
	if info["method"] != "hls" {
		t.Fatalf("no conversion: %v", info)
	}
	path := nativeString(nativeMap(nativeArray(info["tracks"])[0])["url"])
	playlist := call(t, h, a, "GET", path, "")
	if playlist.Code != 200 || !strings.Contains(playlist.Body.String(), "#EXT-X-DISCONTINUITY") || strings.Count(playlist.Body.String(), "#EXTINF:") != 4 {
		t.Fatalf("playlist %d %s", playlist.Code, playlist.Body.String())
	}
	for _, segment := range []string{"segment-0-0.ts", "segment-0-1.ts", "segment-1-0.ts"} {
		path := strings.TrimSuffix(path, "index.m3u8") + segment
		w := call(t, h, a, "GET", path, "")
		if w.Code != 200 || w.Body.Len() < 188 || w.Body.Bytes()[0] != 0x47 {
			t.Fatalf("segment %s %d %q", segment, w.Code, w.Body.String())
		}
		// A real decoder must accept every segment, including a seek into file two.
		cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-i", "pipe:0", "-f", "null", "-")
		cmd.Stdin = strings.NewReader(w.Body.String())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("decode %s: %v %s", segment, err, out)
		}
	}
	sid := nativeString(info["session_id"])
	if w := nativeCall(t, h, tok, "GET", "/hls/"+sid+"/segment-9-0.ts", ""); w.Code != 404 {
		t.Fatalf("invalid track %d", w.Code)
	}
	if w := nativeCall(t, h, "bad-token", "GET", "/hls/"+sid+"/index.m3u8", ""); w.Code != 401 {
		t.Fatalf("anonymous HLS %d", w.Code)
	}
}

// Optional real-browser acceptance, using generated media and a disposable DB.
// No live library or notification/Core configuration is loaded.
func TestNativeBrowserAcceptance(t *testing.T) {
	cdp := os.Getenv("POCKET_NATIVE_BROWSER_CDP")
	if cdp == "" {
		t.Skip("set POCKET_NATIVE_BROWSER_CDP for Chromium acceptance")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	s, _, a, _, _ := nativeHarness(t)
	s.cfg.FFmpeg = ffmpeg
	s.cfg.FFprobe = ffprobe
	if _, err = s.store.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.upload',json('true'),'$.update',json('true')) WHERE pocket_user_id=?`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	var root string
	if err = s.store.DB.QueryRow(`SELECT local_path FROM catalog_roots WHERE id='root'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "Test Book"), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		name := fmt.Sprintf("part%d.mp3", i)
		cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "7", filepath.Join(root, "Test Book", name))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("audio fixture: %v %s", err, out)
		}
		if _, err = s.store.DB.Exec(`UPDATE catalog_files SET relative_path=?,name=?,duration=7 WHERE item_id='book' AND id=?`, filepath.Join("Test Book", name), name, fmt.Sprint(10+i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Remove(filepath.Join(root, "audio.mp3")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.DB.Exec(`UPDATE catalog_items SET duration=14,document=json_set(document,'$.media.chapters',json('[{"id":0,"start":0,"end":14,"title":"One"}]'),'$.item',json('{"path":"/media/Test Book","libraryFolderId":"folder"}')) WHERE id='book'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.DB.Exec(`INSERT INTO catalog_entities VALUES('libraryFolders','folder','lib','{"id":"folder","path":"/media"}')`); err != nil {
		t.Fatal(err)
	}
	s.webFS, err = fs.Sub(web.Dist, "dist")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Router())
	defer server.Close()
	script := filepath.Join("..", "..", "scripts", "test-native-browser.mjs")
	cmd := exec.Command("node", script)
	cmd.Env = append(os.Environ(), "BASE="+server.URL, "CDP="+cdp, "PAB_COOKIE="+a.cookie.Value, "PAB_COOKIE_NAME="+a.cookie.Name, "PAB_UPLOAD="+filepath.Join(root, "Test Book", "part1.mp3"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(string(out))
}
