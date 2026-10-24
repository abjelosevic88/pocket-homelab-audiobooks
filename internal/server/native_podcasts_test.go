package server

import (
	"context"
	"fmt"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNativePodcastSubscribeCheckDownloadAndPlayback(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	s, h, a, b, tok := nativeHarness(t)
	s.cfg.FFprobe = ffprobe
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "sample.mp3")
	if out, err := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "2", source).CombinedOutput(); err != nil {
		t.Fatalf("audio %s %v", out, err)
	}
	audio, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	bad := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.xml":
			w.Header().Set("Content-Type", "application/rss+xml")
			fmt.Fprint(w, `<rss><channel><title>Test podcast</title><author>Test host</author><item><guid>one</guid><title>Episode one</title><enclosure url="/episode.mp3" type="audio/mpeg"/><pubDate>Fri, 23 Oct 2026 00:00:00 GMT</pubDate></item></channel></rss>`)
		case "/episode.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
			if bad {
				w.Write([]byte("not valid audio"))
			} else {
				w.Write(audio)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	s.nativeFetchHTTP = fetch.Client(time.Second, []string{"127.0.0.1"})
	if _, err = s.store.DB.Exec(`INSERT INTO catalog_roots VALUES('podroot','/podcasts',?)`, root); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.DB.Exec(`INSERT INTO catalog_libraries VALUES('pods','Podcasts','podcast',1,'{}');INSERT INTO catalog_entities VALUES('libraryFolders','podfolder','pods','{"id":"podfolder","path":"/podcasts"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true'),'$.upload',json('true')) WHERE pocket_user_id=?`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	payload := string(nativeJSON(nativeObject{"library_id": "pods", "folder_id": "podfolder", "url": upstream.URL + "/feed.xml"}))
	if w := call(t, h, b, "POST", "/api/v1/podcasts/subscribe", payload); w.Code != 403 {
		t.Fatal("unprivileged subscription", w.Code)
	}
	w := call(t, h, a, "POST", "/api/v1/podcasts/subscribe", payload)
	if w.Code != 201 {
		t.Fatalf("subscribe %d %s", w.Code, w.Body.String())
	}
	w.Code = 200
	id := nativeString(jsonObject(t, w)["id"])
	info := jsonObject(t, call(t, h, a, "GET", "/api/v1/podcasts/"+id, ""))
	eps := nativeArray(info["episodes"])
	if len(eps) != 1 {
		t.Fatal(info)
	}
	episode := nativeString(nativeMap(eps[0])["id"])
	// A repeated check retains the identity and cannot duplicate an episode.
	jsonObject(t, call(t, h, a, "POST", "/api/v1/podcasts/"+id+"/check", `{}`))
	queue := func() string {
		t.Helper()
		w := call(t, h, a, "POST", "/api/v1/podcasts/"+id+"/episodes/"+episode+"/download", `{}`)
		if w.Code != 202 {
			t.Fatalf("queue %d %s", w.Code, w.Body.String())
		}
		w.Code = 200
		return nativeString(jsonObject(t, w)["job_id"])
	}
	cancelled := queue()
	jsonObject(t, call(t, h, a, "DELETE", "/api/v1/podcasts/jobs/"+cancelled, ""))
	if s.runPodcastJob(context.Background()) {
		t.Fatal("cancelled job ran")
	}
	bad = true
	failed := queue()
	s.runPodcastJob(context.Background())
	var state string
	s.store.DB.QueryRow(`SELECT status FROM catalog_podcast_jobs WHERE id=?`, failed).Scan(&state)
	if state != "failed" {
		t.Fatal("bad audio published", state)
	}
	bad = false
	job := queue()
	if !s.runPodcastJob(context.Background()) {
		t.Fatal("job not run")
	}
	s.store.DB.QueryRow(`SELECT status FROM catalog_podcast_jobs WHERE id=?`, job).Scan(&state)
	if state != "complete" {
		t.Fatal("download failed", state)
	}
	info = jsonObject(t, call(t, h, a, "GET", "/api/v1/podcasts/"+id, ""))
	eps = nativeArray(info["episodes"])
	if len(eps) != 1 || nativeMap(eps[0])["downloaded"] != true {
		t.Fatal("download visibility", info)
	}
	session := jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/"+id+"/play/"+episode, `{"forceDirectPlay":true}`))
	if nativeNumber(session["duration"]) < 2 {
		t.Fatal("episode timeline", session)
	}
	files, err := s.catalog().Files(context.Background(), a.u.ID, id)
	if err != nil || len(files) != 1 {
		t.Fatalf("files %+v %v", files, err)
	}
	stream := nativeCall(t, h, tok, "GET", "/api/items/"+id+"/file/"+files[0].ID, "")
	if stream.Code != 206 {
		t.Fatal("episode streaming", stream.Code)
	}
	// A missing downloaded episode can be repaired without changing its episode
	// identity or saved listening position.
	sid := nativeString(session["id"])
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/sync", `{"currentTime":1,"timeListened":1,"reportId":"podcast-progress"}`))
	var rel string
	if err = s.store.DB.QueryRow(`SELECT relative_path FROM catalog_files WHERE item_id=? AND id=?`, id, files[0].ID).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatal(err)
	}
	info = jsonObject(t, call(t, h, a, "GET", "/api/v1/podcasts/"+id, ""))
	if nativeMap(nativeArray(info["episodes"])[0])["downloaded"] != false {
		t.Fatal("missing file still marked downloaded")
	}
	repair := queue()
	s.runPodcastJob(context.Background())
	s.store.DB.QueryRow(`SELECT status FROM catalog_podcast_jobs WHERE id=?`, repair).Scan(&state)
	if state != "complete" {
		t.Fatal("repair failed", state)
	}
	tracks, _, _, err := s.catalog().Timeline(context.Background(), a.u.ID, id, episode)
	if err != nil || len(tracks) != 1 || tracks[0].ID == files[0].ID {
		t.Fatalf("repair changed timeline %+v %v", tracks, err)
	}
	book, err := s.nativeEpisodeBook(context.Background(), a.u.ID, id, episode)
	if err != nil || book.CurrentTime != 1 || book.EpisodeID != episode {
		t.Fatalf("repair lost progress %+v %v", book, err)
	}
	if w = call(t, h, a, "GET", "/api/v1/podcasts/opml", ""); w.Code != 200 {
		t.Fatal("OPML export", w.Code)
	}
	if w = call(t, h, b, "GET", "/api/v1/podcasts/"+id, ""); w.Code != 404 {
		t.Fatal("cross-user podcast", w.Code)
	}
}
