package library_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/feed"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestPodcastCalendarTimezonesDSTAndDayIntersection(t *testing.T) {
	cases := []struct{ spec, zone, start, want string }{
		{"0 * * * *", "Europe/Sarajevo", "2026-10-24T16:18:18Z", "2026-10-24T17:00:00Z"},
		{"0 9 * * monday", "Europe/Sarajevo", "2026-10-24T16:18:18Z", "2026-10-26T08:00:00Z"},
		{"0 9 1 * mon", "UTC", "2026-10-24T16:18:18Z", "2027-02-01T09:00:00Z"},
		{"0 9 * * 7", "UTC", "2026-10-24T16:18:18Z", "2026-10-25T09:00:00Z"},
		{"0 9 * * 5-7", "UTC", "2026-10-24T16:18:18Z", "2026-10-25T09:00:00Z"},
		{"0 30 2 * * *", "Europe/Sarajevo", "2026-03-28T23:00:00Z", "2026-03-30T00:30:00Z"},
		{"0 30 2 * * *", "Europe/Sarajevo", "2026-10-25T00:31:00Z", "2026-10-25T01:30:00Z"},
		{"*/15 * * * * *", "UTC", "2026-10-24T16:18:18Z", "2026-10-24T16:18:30Z"},
		{"0 0 1 january *", "UTC", "2026-10-24T16:18:18Z", "2027-01-01T00:00:00Z"},
	}
	for _, tt := range cases {
		t.Run(tt.spec+tt.start, func(t *testing.T) {
			start, _ := time.Parse(time.RFC3339, tt.start)
			got, err := library.NextPodcastCheck(library.PodcastSettings{Schedule: tt.spec, Timezone: tt.zone}, start)
			if err != nil || got.UTC().Format(time.RFC3339) != tt.want {
				t.Fatalf("got %v %v want %s", got, err, tt.want)
			}
		})
	}
	for _, spec := range []string{"* * *", "0 0 31 2 *", "0 25 * * *", "0 0 * * 8", "0 0 * * */0", "0 0 * * 6-2"} {
		if _, err := library.NextPodcastCheck(library.PodcastSettings{Schedule: spec, Timezone: "UTC"}, time.Now()); err == nil {
			t.Fatal("invalid schedule accepted", spec)
		}
	}
}
func podcastImportFixture(t *testing.T) fixture {
	f := setup(t)
	sourceExec(t, f, `ALTER TABLE podcasts ADD COLUMN feedURL TEXT; ALTER TABLE podcasts ADD COLUMN autoDownloadEpisodes INTEGER; ALTER TABLE podcasts ADD COLUMN autoDownloadSchedule TEXT; ALTER TABLE podcasts ADD COLUMN maxEpisodesToKeep INTEGER; ALTER TABLE podcasts ADD COLUMN maxNewEpisodesToDownload INTEGER; ALTER TABLE podcasts ADD COLUMN lastEpisodeCheck TEXT;
 INSERT INTO podcasts VALUES('pod','Imported podcast','["allowed"]','[]','https://example.com/feed',1,'0 9 * * mon',4,6,'2026-10-24T16:18:18Z');
 INSERT INTO libraryItems VALUES('poditem','public','pod','podcast','',0,'2026-10-24T16:18:18Z','2026-10-24T16:18:18Z','[]','{}');
 UPDATE users SET permissions=json_set(permissions,'$.upload',json('true'),'$.update',json('true'),'$.delete',json('true')) WHERE id='alice'`)
	f.opt.PodcastOwner = "alice"
	f.opt.ScheduleTimezone = "Europe/Sarajevo"
	return f
}
func TestImportPodcastSchedulePreservesLimitsOwnerAndFailedCheckWatermark(t *testing.T) {
	f := podcastImportFixture(t)
	ctx := context.Background()
	before, _ := os.ReadFile(f.source)
	report := apply(t, f)
	after, _ := os.ReadFile(f.source)
	if string(before) != string(after) || report.Counts["podcast_subscriptions"] != 1 || report.UnassignedSubscriptions != 0 {
		t.Fatal("import evidence", report)
	}
	sub, err := f.catalog.PodcastSubscription(ctx, 1, "poditem")
	expected, _ := time.Parse(time.RFC3339, "2026-10-26T08:00:00Z")
	if err != nil || !sub.Auto || sub.Keep != 4 || sub.MaxNew != 6 || sub.Timezone != "Europe/Sarajevo" || sub.Schedule != "0 9 * * mon" || sub.Actor == nil || *sub.Actor != 1 || sub.NextCheck != expected.UnixMilli() {
		t.Fatal(sub, err)
	}
	if err = f.catalog.PodcastCheckFailed(ctx, "poditem"); err != nil {
		t.Fatal(err)
	}
	failed, err := f.catalog.PodcastSubscription(ctx, 1, "poditem")
	if err != nil || failed.LastCheck != sub.LastCheck || failed.Error == "" || failed.NextCheck <= time.Now().UnixMilli() {
		t.Fatal("failed check lost watermark", failed, err)
	}
	if err = f.catalog.UpdateFeed(ctx, 1, "poditem", feed.Feed{}); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.catalog.PodcastSubscription(ctx, 1, "poditem")
	if err != nil || recovered.LastCheck <= sub.LastCheck || recovered.Error != "" || recovered.Keep != sub.Keep || recovered.Schedule != sub.Schedule {
		t.Fatal("successful check", recovered, err)
	}
}
func TestImportPodcastSchedulesRequireTimezoneAndExplicitRetainedOwner(t *testing.T) {
	t.Run("timezone", func(t *testing.T) {
		f := podcastImportFixture(t)
		f.opt.ScheduleTimezone = ""
		if _, err := f.catalog.Import(context.Background(), f.opt); err == nil {
			t.Fatal("guessed source timezone")
		}
		assertEmpty(t, f)
	})
	t.Run("unassigned", func(t *testing.T) {
		f := podcastImportFixture(t)
		f.opt.PodcastOwner = ""
		report := apply(t, f)
		sub, err := f.catalog.PodcastSubscription(context.Background(), 1, "poditem")
		if err != nil || report.UnassignedSubscriptions != 1 || sub.Actor != nil || !sub.Auto {
			t.Fatal(sub, report, err)
		}
	})
	t.Run("excluded", func(t *testing.T) {
		f := podcastImportFixture(t)
		f.opt.ExcludeAccounts = []string{"alice"}
		delete(f.opt.Users, "alice")
		if _, err := f.catalog.Import(context.Background(), f.opt); err == nil {
			t.Fatal("excluded worker accepted")
		}
		assertEmpty(t, f)
	})
	t.Run("permission", func(t *testing.T) {
		f := podcastImportFixture(t)
		sourceExec(t, f, `UPDATE users SET permissions=json_set(permissions,'$.delete',json('false')) WHERE id='alice'`)
		if _, err := f.catalog.Import(context.Background(), f.opt); err == nil {
			t.Fatal("retention without delete accepted")
		}
		assertEmpty(t, f)
	})
}

func TestImportedPodcastWithoutFeedKeepsDormantSettings(t *testing.T) {
	f := podcastImportFixture(t)
	sourceExec(t, f, `UPDATE podcasts SET feedURL='',autoDownloadEpisodes=0`)
	r := apply(t, f)
	sub, err := f.catalog.PodcastSubscription(context.Background(), 1, "poditem")
	if err != nil || r.Counts["podcast_subscriptions"] != 1 || sub.Auto || sub.Schedule != "0 9 * * mon" {
		t.Fatal(sub, r, err)
	}
	if err = f.catalog.SetPodcastSettings(context.Background(), 1, "poditem", sub.PodcastSettings); err != nil {
		t.Fatal("cannot save retention for local episodes", err)
	}
	sub.Auto = true
	if err = f.catalog.SetPodcastSettings(context.Background(), 1, "poditem", sub.PodcastSettings); err == nil {
		t.Fatal("automatic feed check enabled without feed")
	}
}
