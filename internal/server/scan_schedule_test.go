package server

import (
	"context"
	"testing"
	"time"
	_ "time/tzdata" // DST tests must not depend on the host's zoneinfo
)

func sarajevo(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Sarajevo")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// simulate ticks the scheduler once a minute (with a little jitter, as the
// real ticker has) and returns the start times of automatic scans.
func simulate(sc scanSchedule, from, to time.Time) []time.Time {
	var runs []time.Time
	var last time.Time
	for now := from; now.Before(to); now = now.Add(time.Minute + 150*time.Millisecond) {
		if sc.due(last, now) {
			runs = append(runs, now)
			last = now
		}
	}
	return runs
}

func TestScanWindowWrapsMidnight(t *testing.T) {
	loc := sarajevo(t)
	sc := scanSchedule{Interval: time.Hour, WindowStart: 22 * 60, WindowEnd: 6 * 60}
	for _, c := range []struct {
		h, m int
		in   bool
	}{{21, 59, false}, {22, 0, true}, {23, 30, true}, {0, 0, true}, {5, 59, true}, {6, 0, false}, {12, 0, false}} {
		_, _, ok := sc.current(time.Date(2026, 6, 10, c.h, c.m, 0, 0, loc))
		if ok != c.in {
			t.Errorf("%02d:%02d in window = %v, want %v", c.h, c.m, ok, c.in)
		}
	}
	runs := simulate(sc, time.Date(2026, 6, 10, 12, 0, 0, 0, loc), time.Date(2026, 6, 11, 12, 0, 0, 0, loc))
	if len(runs) != 8 {
		t.Fatalf("hourly 22:00-06:00 ran %d times: %v", len(runs), runs)
	}
	for _, r := range runs {
		if h := r.Hour(); h >= 6 && h < 22 {
			t.Fatal("scan outside the window", r)
		}
	}
}

func TestScanDailyAtTimeRunsOncePerDay(t *testing.T) {
	loc := sarajevo(t)
	sc := scanSchedule{Interval: 24 * time.Hour, WindowStart: 3 * 60, WindowEnd: 4 * 60}
	at := func(d, h, m int) time.Time { return time.Date(2026, 6, d, h, m, 0, 0, loc) }
	if sc.due(time.Time{}, at(10, 2, 59)) {
		t.Fatal("due before the window")
	}
	if got := sc.next(time.Time{}, at(10, 2, 59)); !got.Equal(at(10, 3, 0)) {
		t.Fatal("next before window", got)
	}
	if !sc.due(time.Time{}, at(10, 3, 0)) {
		t.Fatal("not due at window start")
	}
	// Already ran today: the next scan is tomorrow at 03:00, not 24h later.
	last := at(10, 3, 1)
	if sc.due(last, at(10, 3, 40)) {
		t.Fatal("ran twice in one window")
	}
	if got := sc.next(last, at(10, 3, 40)); !got.Equal(at(11, 3, 0)) {
		t.Fatal("next daily", got)
	}
	if !sc.due(last, at(11, 3, 0)) {
		t.Fatal("tomorrow's window start must not wait for a full 24 hours")
	}
	// A busy or stopped server catches up later in the window, not after it.
	if !sc.due(at(9, 3, 0), at(10, 3, 45)) || sc.due(at(9, 3, 0), at(10, 4, 0)) {
		t.Fatal("catch-up inside the window only")
	}
	runs := simulate(sc, at(1, 0, 0), at(8, 0, 0))
	if len(runs) != 7 {
		t.Fatal("daily runs drifted", runs)
	}
	for _, r := range runs {
		if r.Hour() != 3 || r.Minute() > 1 {
			t.Fatal("daily run drifted from 03:00", r)
		}
	}
}

func TestScanScheduleDSTTransitions(t *testing.T) {
	loc := sarajevo(t)
	// 2026-03-29: 02:00 CET jumps to 03:00 CEST, so 02:30 does not exist.
	spring := scanSchedule{Interval: 24 * time.Hour, WindowStart: 2*60 + 30, WindowEnd: 3*60 + 30}
	runs := simulate(spring, time.Date(2026, 3, 28, 12, 0, 0, 0, loc), time.Date(2026, 3, 30, 12, 0, 0, 0, loc))
	if len(runs) != 2 || runs[0].Day() != 29 || runs[1].Day() != 30 {
		t.Fatal("spring-forward day must still get exactly one scan", runs)
	}
	// 2026-10-25: 03:00 CEST falls back to 02:00 CET, so 02:00-03:00 happens twice.
	fall := scanSchedule{Interval: 24 * time.Hour, WindowStart: 2 * 60, WindowEnd: 3 * 60}
	runs = simulate(fall, time.Date(2026, 10, 24, 12, 0, 0, 0, loc), time.Date(2026, 10, 26, 12, 0, 0, 0, loc))
	if len(runs) != 2 || runs[0].Day() != 25 || runs[1].Day() != 26 {
		t.Fatal("fall-back day must not scan twice", runs)
	}
	// Hourly inside 01:00-06:00 across the fall-back: wall clock bounds hold
	// and runs stay an interval apart in real time.
	hourly := scanSchedule{Interval: time.Hour, WindowStart: 60, WindowEnd: 6 * 60}
	runs = simulate(hourly, time.Date(2026, 10, 24, 12, 0, 0, 0, loc), time.Date(2026, 10, 25, 12, 0, 0, 0, loc))
	if len(runs) < 5 {
		t.Fatal("too few hourly runs", runs)
	}
	for i, r := range runs {
		if r.Hour() < 1 || r.Hour() >= 6 {
			t.Fatal("hourly run outside window", r)
		}
		if i > 0 && r.Sub(runs[i-1]) < time.Hour-scanSlack {
			t.Fatal("hourly runs too close", runs[i-1], r)
		}
	}
	// A window bounded by a nonexistent time still has its full length.
	gap := scanSchedule{Interval: 24 * time.Hour, WindowStart: 2*60 + 15, WindowEnd: 2*60 + 45}
	start, end := gap.occurrence(time.Date(2026, 3, 29, 12, 0, 0, 0, loc))
	if end.Sub(start) != 30*time.Minute {
		t.Fatal("window in the DST gap", start, end)
	}
}

func TestScanNextWithoutWindowAndShortIntervals(t *testing.T) {
	loc := sarajevo(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, loc)
	open := scanSchedule{Interval: 15 * time.Minute, WindowStart: -1, WindowEnd: -1}
	if got := open.next(time.Time{}, now); !got.Equal(now) {
		t.Fatal("never scanned must be due now", got)
	}
	if got := open.next(now.Add(-5*time.Minute), now); !got.Equal(now.Add(10 * time.Minute)) {
		t.Fatal(got)
	}
	if got := open.next(now.Add(-time.Hour), now); !got.Equal(now) {
		t.Fatal("overdue", got)
	}
	// Six-hourly inside 01:00-06:00: the next slot falls after the window, so
	// it moves to the next opening.
	six := scanSchedule{Interval: 6 * time.Hour, WindowStart: 60, WindowEnd: 6 * 60}
	last := time.Date(2026, 6, 10, 1, 0, 0, 0, loc)
	if got := six.next(last, time.Date(2026, 6, 10, 2, 0, 0, 0, loc)); !got.Equal(time.Date(2026, 6, 11, 1, 0, 0, 0, loc)) {
		t.Fatal("six-hourly window", got)
	}
	// Thirty minutes inside a window: the next slot stays in the window.
	half := scanSchedule{Interval: 30 * time.Minute, WindowStart: 60, WindowEnd: 6 * 60}
	if got := half.next(last, time.Date(2026, 6, 10, 1, 10, 0, 0, loc)); !got.Equal(last.Add(30 * time.Minute)) {
		t.Fatal("half-hourly window", got)
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]int{"": -1, "00:00": 0, "03:00": 180, "23:59": 1439} {
		if got, err := parseClock(in); err != nil || got != want {
			t.Error(in, got, err)
		}
	}
	for _, bad := range []string{"24:00", "3:00", "03:60", "0300", "aa:bb", "03:00:00", "-1:00"} {
		if _, err := parseClock(bad); err == nil {
			t.Error("accepted", bad)
		}
	}
	if formatClock(185) != "03:05" || formatClock(-1) != "" {
		t.Fatal("formatClock")
	}
}

func TestNativeWatchScheduleAPI(t *testing.T) {
	s, h, a, b, _ := nativeHarness(t)
	ctx := context.Background()
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE id='alice'`); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"enabled":true,"interval_seconds":30}`,
		`{"enabled":true,"interval_seconds":90000}`,
		`{"enabled":true,"interval_seconds":3600,"window_start":"25:00","window_end":"06:00"}`,
		`{"enabled":true,"interval_seconds":3600,"window_start":"01:00"}`,
		`{"enabled":true,"interval_seconds":3600,"window_start":"01:00","window_end":"01:00"}`,
		`{"enabled":true,"interval_seconds":3600,"window_start":"1:00","window_end":"06:00"}`,
	} {
		if w := call(t, h, a, "PUT", "/api/v1/catalog/libraries/lib/watch", body); w.Code != 400 {
			t.Error("accepted", body, w.Code)
		}
		if w := call(t, h, a, "PUT", "/api/v1/catalog/watch", body); w.Code != 400 {
			t.Error("bulk accepted", body, w.Code)
		}
	}
	jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/libraries/lib/watch", `{"enabled":true,"interval_seconds":86400,"window_start":"03:00","window_end":"04:00"}`))
	lib := nativeMap(jsonObject(t, call(t, h, a, "GET", "/api/v1/catalog/manage", ""))["libraries"].([]any)[0])
	if lib["window_start"] != "03:00" || lib["window_end"] != "04:00" || lib["watch"] != true || lib["next_scan_at"] == nil {
		t.Fatal(lib)
	}
	// Legacy per-minute values stay valid through the API.
	jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/libraries/lib/watch", `{"enabled":true,"interval_seconds":60}`))
	row, ok := s.loadScanSettings(ctx, "lib")
	if !ok || row.schedule.windowed() || row.interval != 60 {
		t.Fatal("window not cleared", row)
	}
	out := jsonObject(t, call(t, h, a, "PUT", "/api/v1/catalog/watch", `{"enabled":true,"interval_seconds":21600,"window_start":"22:00","window_end":"06:00"}`))
	if ids := out["libraries"].([]any); len(ids) != 1 || ids[0] != "lib" {
		t.Fatal("bulk", out)
	}
	if row, _ = s.loadScanSettings(ctx, "lib"); row.schedule.WindowStart != 22*60 || row.schedule.WindowEnd != 6*60 || row.interval != 21600 {
		t.Fatal("bulk not stored", row)
	}
	// Someone without scan rights changes nothing.
	if out = jsonObject(t, call(t, h, b, "PUT", "/api/v1/catalog/watch", `{"enabled":false,"interval_seconds":3600}`)); len(out["libraries"].([]any)) != 0 {
		t.Fatal("member bulk", out)
	}
	if w := call(t, h, b, "PUT", "/api/v1/catalog/libraries/lib/watch", `{"enabled":false,"interval_seconds":3600}`); w.Code == 200 {
		t.Fatal("member watch", w.Code)
	}
}

func TestScheduledScanWaitsForBusyLibrary(t *testing.T) {
	s, _, a, _, _ := nativeHarness(t)
	ctx := context.Background()
	if got := s.libraryBusy(ctx, "lib"); got != "" {
		t.Fatal(got)
	}
	// A match job left behind by a restart does not block scans.
	if _, err := s.store.DB.Exec(`INSERT INTO catalog_match_jobs(id,user_id,library_id,status,created_at,updated_at) VALUES('match-stale',?,'lib','running',0,0)`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.libraryBusy(ctx, "lib"); got != "" {
		t.Fatal("stale match job blocks", got)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO catalog_match_jobs(id,user_id,library_id,status,created_at,updated_at) VALUES('match-live',?,'lib','running',0,0)`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	matchRuns.Lock()
	matchRuns.cancel["match-live"] = func() {}
	matchRuns.Unlock()
	t.Cleanup(func() { matchRuns.Lock(); delete(matchRuns.cancel, "match-live"); matchRuns.Unlock() })
	if got := s.libraryBusy(ctx, "lib"); got != "match" {
		t.Fatal("live match job", got)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO catalog_scan_settings(library_id,interval_seconds,enabled,last_started,actor_id) VALUES('lib',900,1,0,?)`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	if started := s.runDueScans(ctx, time.Now()); len(started) != 0 {
		t.Fatal("scan started during a match job", started)
	}
	matchRuns.Lock()
	delete(matchRuns.cancel, "match-live")
	matchRuns.Unlock()
	if _, err := s.store.DB.Exec(`INSERT INTO catalog_scan_runs(id,library_id,started_at,status,report) VALUES('scan-x','lib',0,'running','{}')`); err != nil {
		t.Fatal(err)
	}
	if got := s.libraryBusy(ctx, "lib"); got != "scan" {
		t.Fatal("running scan", got)
	}
	if started := s.runDueScans(ctx, time.Now()); len(started) != 0 {
		t.Fatal("second scan started", started)
	}
	var last int64
	_ = s.store.DB.QueryRow(`SELECT last_started FROM catalog_scan_settings WHERE library_id='lib'`).Scan(&last)
	if last != 0 {
		t.Fatal("skipped scan consumed its slot", last)
	}
	// Another library due while this scan runs waits instead of losing its slot.
	if _, err := s.store.DB.Exec(`INSERT INTO catalog_libraries SELECT 'lib2',name,media_type,display_order+1,document FROM catalog_libraries WHERE id='lib'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO catalog_scan_settings(library_id,interval_seconds,enabled,last_started,actor_id) VALUES('lib2',900,1,0,?)`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	if started := s.runDueScans(ctx, time.Now()); len(started) != 0 {
		t.Fatal("scan started while another library scans", started)
	}
	_ = s.store.DB.QueryRow(`SELECT last_started FROM catalog_scan_settings WHERE library_id='lib2'`).Scan(&last)
	if last != 0 {
		t.Fatal("waiting library consumed its slot", last)
	}
}
