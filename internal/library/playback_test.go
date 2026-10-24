package library_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestNativeSessionResumeRetriesAndDurability(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	s, err := c.OpenSession(ctx, 1, "visible", "", "web", "browser")
	if err != nil {
		t.Fatal(err)
	}
	if s.Position != 30 || s.Duration != 100 || len(s.Tracks) != 1 {
		t.Fatalf("session %+v", s)
	}
	at := time.Now().UnixMilli()
	r := library.Report{ID: "report-a", Position: 45, Listened: 15, ObservedAt: at, Playing: true}
	result, err := c.ReportSession(ctx, 1, s.ID, r)
	if err != nil || !result.ProgressApplied {
		t.Fatalf("report %+v %v", result, err)
	}
	// Reconstruct the engine: no in-memory session or receipt state is required.
	restarted := &library.Catalog{DB: c.DB}
	result, err = restarted.ReportSession(ctx, 1, s.ID, r)
	if err != nil || !result.Duplicate {
		t.Fatalf("retry %+v %v", result, err)
	}
	r.Position = 46
	if _, err = c.ReportSession(ctx, 1, s.ID, r); !errors.Is(err, library.ErrConflict) {
		t.Fatalf("ID reuse %v", err)
	}
	current, err := restarted.Session(ctx, 1, s.ID)
	if err != nil || current.Position != 45 || current.Listened != 15 {
		t.Fatalf("stored %+v %v", current, err)
	}
	var seconds int
	if err = c.DB.QueryRow(`SELECT sum(seconds) FROM listens WHERE user_id=1`).Scan(&seconds); err != nil || seconds != 15 {
		t.Fatalf("history %d %v", seconds, err)
	}
	if _, err = c.Session(ctx, 2, s.ID); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("another person read session")
	}
	if _, err = c.ReportSession(ctx, 2, s.ID, library.Report{ID: "steal", ObservedAt: at, Position: 2}); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("another person synced session")
	}
}
func TestNativeSessionHandoffAndDelayedReports(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	first, err := c.OpenSession(ctx, 1, "visible", "", "web", "browser")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UnixMilli()
	report := func(s string, id string, pos, listened float64, when int64) library.ReportResult {
		t.Helper()
		r, err := c.ReportSession(ctx, 1, s, library.Report{ID: id, Position: pos, Listened: listened, ObservedAt: when, Playing: true})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	report(first.ID, "first", 40, 10, at)
	second, err := c.OpenSession(ctx, 1, "visible", "", "phone", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if second.Position != 40 {
		t.Fatal("handoff did not resume")
	}
	report(second.ID, "new", 70, 30, at+20)
	if r := report(first.ID, "late-old-device", 50, 10, at+30); r.ProgressApplied {
		t.Fatal("superseded device rewound progress")
	}
	if r := report(second.ID, "out-of-order", 60, 10, at+10); r.ProgressApplied {
		t.Fatal("out-of-order report rewound progress")
	}
	var pos float64
	if err = c.DB.QueryRow(`SELECT position FROM catalog_progress WHERE account_id='alice'`).Scan(&pos); err != nil || pos != 70 {
		t.Fatalf("progress %f %v", pos, err)
	}
	old, err := c.Session(ctx, 1, first.ID)
	if err != nil || !old.Closed || old.Playing {
		t.Fatalf("old device %+v %v", old, err)
	}
	var n int
	if err = c.DB.QueryRow(`SELECT sum(seconds) FROM listens WHERE user_id=1`).Scan(&n); err != nil || n != 60 {
		t.Fatalf("delayed seconds %d %v", n, err)
	}
}
func TestNativeSessionFinishAndExplicitReset(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	s, err := c.OpenSession(ctx, 1, "visible", "", "web", "browser")
	if err != nil {
		t.Fatal(err)
	}
	r := library.Report{ID: "finish", Position: 100, Listened: 70, ObservedAt: time.Now().UnixMilli(), Ended: true}
	result, err := c.ReportSession(ctx, 1, s.ID, r)
	if err != nil || !result.FinishedNow {
		t.Fatalf("finish %+v %v", result, err)
	}
	if err = c.SetProgress(ctx, 1, "visible", "", map[string]any{"currentTime": float64(0), "isFinished": false}); err != nil {
		t.Fatal(err)
	}
	r.ID = "late"
	r.ObservedAt += 100
	if result, err = c.ReportSession(ctx, 1, s.ID, r); err != nil || result.ProgressApplied {
		t.Fatalf("old session undid reset: %+v %v", result, err)
	}
	var pos float64
	var done bool
	if err = c.DB.QueryRow(`SELECT position,finished FROM catalog_progress WHERE account_id='alice'`).Scan(&pos, &done); err != nil || pos != 0 || done {
		t.Fatalf("reset %f %v %v", pos, done, err)
	}
	var count int
	if err = c.DB.QueryRow(`SELECT count(*) FROM finishes WHERE user_id=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("finish duplicated %d %v", count, err)
	}
}
func TestNativeSessionFractionalListeningAndInvalidReports(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	s, err := c.OpenSession(ctx, 1, "visible", "", "web", "browser")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UnixMilli()
	for i, id := range []string{"a", "b", "c"} {
		_, err = c.ReportSession(ctx, 1, s.ID, library.Report{ID: id, Position: 31 + float64(i), Listened: 0.4, ObservedAt: at + int64(i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	var seconds int
	if err = c.DB.QueryRow(`SELECT sum(seconds) FROM listens`).Scan(&seconds); err != nil || seconds != 1 {
		t.Fatalf("fractional seconds %d %v", seconds, err)
	}
	for _, r := range []library.Report{{ID: "nan", Position: math.NaN(), ObservedAt: at}, {ID: "rewind", Position: -1, ObservedAt: at}, {ID: "future", Position: 20, ObservedAt: time.Now().Add(time.Hour).UnixMilli()}, {ID: "negative", Listened: -1, ObservedAt: at}} {
		if _, err = c.ReportSession(ctx, 1, s.ID, r); !errors.Is(err, library.ErrInvalid) {
			t.Fatalf("invalid report accepted: %v", err)
		}
	}
	var count int
	if err = c.DB.QueryRow(`SELECT count(*) FROM catalog_play_reports`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("invalid report mutated receipts: %d %v", count, err)
	}
}

// A player whose timeline runs slightly past the probed duration must not be
// rejected: its outbox would otherwise retry the same receipt forever.
func TestNativeSessionClampsOverlongPositionAndFinishesNearEnd(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	s, err := c.OpenSession(ctx, 1, "visible", "", "web", "browser")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UnixMilli()
	if _, err = c.ReportSession(ctx, 1, s.ID, library.Report{ID: "credits", Position: 89, Listened: 5, ObservedAt: at}); err != nil {
		t.Fatal(err)
	}
	var pos float64
	var done bool
	if err = c.DB.QueryRow(`SELECT position,finished FROM catalog_progress WHERE account_id='alice'`).Scan(&pos, &done); err != nil || pos != 89 || done {
		t.Fatalf("11s left finished early: %f %v %v", pos, done, err)
	}
	result, err := c.ReportSession(ctx, 1, s.ID, library.Report{ID: "near-end", Position: 91, Listened: 2, ObservedAt: at + 1})
	if err != nil || !result.FinishedNow {
		t.Fatalf("9s left not finished: %+v %v", result, err)
	}
	if _, err = c.ReportSession(ctx, 1, s.ID, library.Report{ID: "overlong", Position: 1010, Listened: 1, ObservedAt: at + 2}); err != nil {
		t.Fatal("overlong position rejected", err)
	}
	if err = c.DB.QueryRow(`SELECT position,finished FROM catalog_progress WHERE account_id='alice'`).Scan(&pos, &done); err != nil || pos != 100 || !done {
		t.Fatalf("clamp %f %v %v", pos, done, err)
	}
}

// Reports to a superseded session still count listening, but say the session
// ended so clients reopen from the newest progress instead of losing it.
func TestNativeSessionEndedFlagAndReceiptPruning(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	first, err := c.OpenSession(ctx, 1, "visible", "", "web", "browser")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UnixMilli()
	if r, err := c.ReportSession(ctx, 1, first.ID, library.Report{ID: "live", Position: 40, Listened: 10, ObservedAt: at}); err != nil || r.SessionEnded {
		t.Fatalf("live session %+v %v", r, err)
	}
	if _, err = c.OpenSession(ctx, 1, "visible", "", "phone", "phone"); err != nil {
		t.Fatal(err)
	}
	r, err := c.ReportSession(ctx, 1, first.ID, library.Report{ID: "replaced", Position: 50, Listened: 10, ObservedAt: at + 1})
	if err != nil || !r.SessionEnded || r.ProgressApplied {
		t.Fatalf("replaced session %+v %v", r, err)
	}
	if r, err = c.ReportSession(ctx, 1, first.ID, library.Report{ID: "replaced", Position: 50, Listened: 10, ObservedAt: at + 1}); err != nil || !r.Duplicate || !r.SessionEnded {
		t.Fatalf("replaced retry %+v %v", r, err)
	}
	// Old receipts of closed sessions are pruned on the next open; reports observed
	// that long ago on a closed session are ignored rather than counted twice.
	old := time.Now().Add(-31 * 24 * time.Hour).UnixMilli()
	if _, err = c.DB.Exec(`UPDATE catalog_play_reports SET received_at=? WHERE session_id=?`, old, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.OpenSession(ctx, 1, "visible", "", "web", "browser"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = c.DB.QueryRow(`SELECT count(*) FROM catalog_play_reports WHERE session_id=?`, first.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("receipts not pruned %d %v", count, err)
	}
	if r, err = c.ReportSession(ctx, 1, first.ID, library.Report{ID: "ancient", Position: 50, Listened: 10, ObservedAt: old}); err != nil || !r.Duplicate {
		t.Fatalf("ancient report %+v %v", r, err)
	}
	var listened float64
	if err = c.DB.QueryRow(`SELECT listened FROM catalog_play_sessions WHERE id=?`, first.ID).Scan(&listened); err != nil || listened != 20 {
		t.Fatalf("listened %f %v", listened, err)
	}
}

func TestNativeBookmarksIsolation(t *testing.T) {
	f := setup(t)
	apply(t, f)
	ctx := context.Background()
	c := f.catalog
	if err := c.Bookmark(ctx, 1, "visible", 15, "Start", false); err != nil {
		t.Fatal(err)
	}
	if err := c.Bookmark(ctx, 1, "visible", 15, "Renamed", false); err != nil {
		t.Fatal(err)
	}
	a, err := c.Bookmarks(ctx, 1)
	if err != nil || len(a) != 1 {
		t.Fatalf("bookmarks %v %v", a, err)
	}
	b, err := c.Bookmarks(ctx, 2)
	if err != nil || len(b) != 0 {
		t.Fatal("bookmark leaked")
	}
	if err = c.Bookmark(ctx, 1, "hidden", 15, "Hidden", false); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("bookmark access bypass")
	}
	if err = c.Bookmark(ctx, 1, "visible", 15, "", true); err != nil {
		t.Fatal(err)
	}
	a, err = c.Bookmarks(ctx, 1)
	if err != nil || len(a) != 0 {
		t.Fatal("bookmark not removed")
	}
}
