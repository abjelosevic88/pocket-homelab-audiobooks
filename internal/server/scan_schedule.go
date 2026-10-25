package server

import (
	"fmt"
	"time"
)

// scanSlack absorbs scheduler tick jitter: the loop ticks once a minute, so a
// scan due a few milliseconds after a tick would otherwise slip a whole
// minute every cycle.
const scanSlack = 30 * time.Second

// scanSchedule decides when an automatic library scan may start. Window
// bounds are minutes after local midnight in the server's time zone; a
// window whose end is before its start wraps past midnight. A daily
// interval with a window runs once per window, as soon as it opens.
type scanSchedule struct {
	Interval    time.Duration
	WindowStart int // -1 when scans may start at any time
	WindowEnd   int
}

func (sc scanSchedule) windowed() bool { return sc.WindowStart >= 0 && sc.WindowEnd >= 0 }
func (sc scanSchedule) daily() bool    { return sc.Interval >= 24*time.Hour }

// windowLength is the wall-clock length of the window.
func (sc scanSchedule) windowLength() time.Duration {
	return time.Duration(((sc.WindowEnd-sc.WindowStart)%1440+1440)%1440) * time.Minute
}

// occurrence returns the window opening on the local calendar day of day.
// Wall clock times that do not exist (spring forward) are normalised by
// time.Date; an end that lands on or before the start keeps the window's
// wall-clock length instead, so every day has exactly one non-empty window.
func (sc scanSchedule) occurrence(day time.Time) (time.Time, time.Time) {
	y, m, d := day.Date()
	loc := day.Location()
	start := time.Date(y, m, d, sc.WindowStart/60, sc.WindowStart%60, 0, 0, loc)
	ed := d
	if sc.WindowEnd <= sc.WindowStart {
		ed++
	}
	end := time.Date(y, m, ed, sc.WindowEnd/60, sc.WindowEnd%60, 0, 0, loc)
	if !end.After(start) {
		end = start.Add(sc.windowLength())
	}
	return start, end
}

// current returns the window containing now, if any.
func (sc scanSchedule) current(now time.Time) (time.Time, time.Time, bool) {
	y, m, d := now.Date()
	for _, off := range []int{0, -1} {
		start, end := sc.occurrence(time.Date(y, m, d+off, 12, 0, 0, 0, now.Location()))
		if !now.Before(start) && now.Before(end) {
			return start, end, true
		}
	}
	return time.Time{}, time.Time{}, false
}

// nextOpen returns the first window start strictly after t.
func (sc scanSchedule) nextOpen(t time.Time) time.Time {
	y, m, d := t.Date()
	for off := -1; off <= 3; off++ {
		start, _ := sc.occurrence(time.Date(y, m, d+off, 12, 0, 0, 0, t.Location()))
		if start.After(t) {
			return start
		}
	}
	return t.Add(24 * time.Hour) // unreachable for a valid window
}

// next returns when the next automatic scan becomes due. last is the start of
// the previous automatic scan (zero when there was none). The result is never
// before now.
func (sc scanSchedule) next(last, now time.Time) time.Time {
	if !sc.windowed() {
		if last.IsZero() {
			return now
		}
		t := last.Add(sc.Interval)
		if t.Before(now) {
			return now
		}
		return t
	}
	if sc.daily() {
		if start, _, ok := sc.current(now); ok && last.Before(start) {
			return now
		}
		return sc.nextOpen(now)
	}
	t := now
	if !last.IsZero() && last.Add(sc.Interval).After(now) {
		t = last.Add(sc.Interval)
	}
	if _, _, ok := sc.current(t); ok {
		return t
	}
	return sc.nextOpen(t)
}

// due reports whether a scan should start at now.
func (sc scanSchedule) due(last, now time.Time) bool {
	if sc.windowed() {
		if _, _, ok := sc.current(now); !ok {
			return false
		}
	}
	return !sc.next(last, now).After(now.Add(scanSlack))
}

// parseClock reads "HH:MM" as minutes after midnight; "" means unset (-1).
func parseClock(v string) (int, error) {
	if v == "" {
		return -1, nil
	}
	var h, m int
	if len(v) != 5 || v[2] != ':' {
		return 0, fmt.Errorf("time must be HH:MM")
	}
	if _, err := fmt.Sscanf(v, "%02d:%02d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("time must be HH:MM")
	}
	return h*60 + m, nil
}

func formatClock(minutes int) string {
	if minutes < 0 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}
