package library_test

import (
	"context"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestOfflineCumulativeRetriesHandoffAndImportReconciliation(t *testing.T) {
	f := setup(t)
	apply(t, f)
	c := f.catalog
	ctx := context.Background()
	now := time.Now().UnixMilli()
	at := now - 3600000
	in := library.OfflineSession{ID: "phone-session", ItemID: "visible", Position: 50, StartPosition: 30, Listened: 20, StartedAt: at - 30000, UpdatedAt: at}
	out, err := c.SyncOffline(ctx, 1, in)
	if err != nil || !out.ProgressApplied {
		t.Fatalf("initial %+v %v", out, err)
	}
	restarted := &library.Catalog{DB: c.DB}
	out, err = restarted.SyncOffline(ctx, 1, in)
	if err != nil || !out.Duplicate {
		t.Fatalf("retry %+v %v", out, err)
	}
	in.Position = 40
	in.Listened = 10
	in.UpdatedAt = at - 1000
	if out, err = c.SyncOffline(ctx, 1, in); err != nil || out.ProgressApplied {
		t.Fatalf("reordered %+v %v", out, err)
	}
	// Repeated concurrent cumulative updates advance by 10 seconds once.
	in.Position = 60
	in.Listened = 30
	in.UpdatedAt = at + 1000
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.SyncOffline(ctx, 1, in); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var total float64
	if err = c.DB.QueryRow(`SELECT SUM(listened) FROM catalog_play_sessions WHERE account_id='alice'`).Scan(&total); err != nil || total != 30 {
		t.Fatalf("duplicate cumulative time %v %f", err, total)
	}
	online, err := c.OpenSession(ctx, 1, "visible", "", "web", "browser")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ReportSession(ctx, 1, online.ID, library.Report{ID: "web-newer", Position: 90, Listened: 1, ObservedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	in.Position = 65
	in.Listened = 35
	in.UpdatedAt = at + 2000
	if out, err = c.SyncOffline(ctx, 1, in); err != nil || out.ProgressApplied {
		t.Fatalf("old offline upload stole head %+v %v", out, err)
	}
	var position float64
	if err = c.DB.QueryRow(`SELECT position FROM catalog_progress WHERE account_id='alice' AND item_id='visible'`).Scan(&position); err != nil || position != 90 {
		t.Fatalf("position %v %f", err, position)
	}
	// A cumulative report for a source session replaces its imported projection;
	// the original is retained for audit and is not counted a second time.
	if _, err = c.DB.Exec(`INSERT INTO catalog_history VALUES('old-phone-session','alice','visible',?)`, `{"timeListening":25,"currentTime":55,"updatedAt":`+strconv.FormatInt(at, 10)+`}`); err != nil {
		t.Fatal(err)
	}
	old := in
	old.ID = "old-phone-session"
	old.Listened = 20
	old.UpdatedAt = at - 1000
	if _, err = c.SyncOffline(ctx, 1, old); err != nil {
		t.Fatal(err)
	}
	var count int
	c.DB.QueryRow(`SELECT count(*) FROM catalog_history WHERE id='old-phone-session'`).Scan(&count)
	if count != 0 {
		t.Fatal("imported history double-counted")
	}
	c.DB.QueryRow(`SELECT count(*) FROM catalog_entities WHERE kind='offlineImportedHistory' AND id='old-phone-session'`).Scan(&count)
	if count != 1 {
		t.Fatal("audit copy lost")
	}
	c.DB.QueryRow(`SELECT sum(listened) FROM catalog_play_sessions WHERE account_id='alice'`).Scan(&total)
	if total != 61 {
		t.Fatal("import maximum lost", total)
	}
	// Caller-supplied IDs never select another person's private stream.
	if _, err = c.SyncOffline(ctx, 2, in); err != nil {
		t.Fatal(err)
	}
	c.DB.QueryRow(`SELECT SUM(listened) FROM catalog_play_sessions WHERE account_id='alice'`).Scan(&total)
	if total != 61 {
		t.Fatal("cross-user stream")
	}
}
