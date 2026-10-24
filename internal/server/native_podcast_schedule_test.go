package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestNativePodcastScheduleFailureRestartAndDisabledWorker(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	ctx := context.Background()
	fail := true
	recent := time.Now().Add(-time.Hour).Truncate(time.Second)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "offline", 503)
			return
		}
		fmt.Fprintf(w, `<rss><channel><title>Show</title><item><guid>new</guid><title>New episode</title><pubDate>%s</pubDate><enclosure url="https://example.com/episode.mp3" type="audio/mpeg"/></item></channel></rss>`, recent.Format(time.RFC1123Z))
	}))
	defer upstream.Close()
	s.nativeFetchHTTP = fetch.Client(time.Second, []string{"127.0.0.1"})
	if _, err := s.store.DB.Exec(`INSERT INTO catalog_items SELECT 'pod',library_id,'pod-media','podcast','Show','Host',0,0,0,added_at,updated_at,'[]',json_object('item',json_object('id','pod'),'media',json_object('feedURL',?),'episodes',json('[]')) FROM catalog_items WHERE id='book'`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true'),'$.upload',json('true')) WHERE pocket_user_id=?`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	config := library.PodcastSettings{Auto: true, Interval: 3600, MaxNew: 3, Schedule: "0 * * * *", Timezone: "Europe/Sarajevo"}
	body := string(nativeJSON(config))
	if w := call(t, h, b, "PUT", "/api/v1/podcasts/pod/settings", body); w.Code == 200 {
		t.Fatal("unauthorized scheduling")
	}
	jsonObject(t, call(t, h, a, "PUT", "/api/v1/podcasts/pod/settings", body))
	baseline := recent.Add(-time.Hour).UnixMilli()
	if _, err := s.store.DB.Exec(`UPDATE catalog_podcast_subscriptions SET last_check=?,download_watermark=?,next_check=0 WHERE item_id='pod'`, baseline, baseline); err != nil {
		t.Fatal(err)
	}
	if !s.checkOnePodcast(ctx) {
		t.Fatal("due check not run")
	}
	sub, err := s.catalog().PodcastSubscription(ctx, a.u.ID, "pod")
	if err != nil || sub.LastCheck != baseline || sub.Error == "" || sub.NextCheck <= time.Now().UnixMilli() {
		t.Fatal("failure lost watermark", sub, err)
	}
	if s.checkOnePodcast(ctx) {
		t.Fatal("failed check retried without backoff")
	}
	fail = false
	// Model a crash after a successful feed fetch but before queue insertion.
	f, err := s.readPodcastFeed(ctx, upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.catalog().UpdateFeed(ctx, a.u.ID, "pod", f); err != nil {
		t.Fatal(err)
	}
	var wm int64
	s.store.DB.QueryRow(`SELECT download_watermark FROM catalog_podcast_subscriptions WHERE item_id='pod'`).Scan(&wm)
	if wm != baseline {
		t.Fatal("feed refresh skipped unqueued window", wm)
	}
	s.store.DB.Exec(`UPDATE catalog_podcast_subscriptions SET next_check=0 WHERE item_id='pod'`)
	if !s.checkOnePodcast(ctx) {
		t.Fatal("restarted check not run")
	}
	var n int
	s.store.DB.QueryRow(`SELECT count(*) FROM catalog_podcast_jobs WHERE item_id='pod'`).Scan(&n)
	if n != 1 {
		t.Fatal("missed newly published episode", n)
	}
	// A duplicate replay reuses a pending job and still advances its watermark.
	s.store.DB.Exec(`UPDATE catalog_podcast_subscriptions SET next_check=0,download_watermark=? WHERE item_id='pod'`, baseline)
	s.checkOnePodcast(ctx)
	s.store.DB.QueryRow(`SELECT count(*) FROM catalog_podcast_jobs WHERE item_id='pod'`).Scan(&n)
	s.store.DB.QueryRow(`SELECT download_watermark FROM catalog_podcast_subscriptions WHERE item_id='pod'`).Scan(&wm)
	if n != 1 || wm <= baseline {
		t.Fatal("retry duplicated queue or lost watermark", n, wm)
	}
	item := jsonObject(t, nativeCall(t, h, tok, "GET", "/api/items/pod", ""))
	media := nativeMap(item["media"])
	if media["autoDownloadEpisodes"] != true || media["autoDownloadSchedule"] != "0 * * * *" || media["scheduleTimezone"] != "Europe/Sarajevo" {
		t.Fatal("client settings", media)
	}
	s.store.DB.Exec(`UPDATE catalog_podcast_subscriptions SET next_check=0,auto_download=0 WHERE item_id='pod'`)
	if s.checkOnePodcast(ctx) {
		t.Fatal("disabled subscription ran")
	}
	// Failed check never deletes existing feed state or its queued work.
	episodes, err := s.catalog().PodcastEpisodes(ctx, a.u.ID, "pod")
	if err != nil || len(episodes) != 1 || episodes[0].Status != "queued" {
		t.Fatal(episodes, err)
	}
}
