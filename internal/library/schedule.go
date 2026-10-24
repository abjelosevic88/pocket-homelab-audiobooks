package library

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// Podcast calendars use the source's AND rule for day-of-month/day-of-week,
// rather than Unix cron's OR rule. The Go parser supplies calendar/DST arithmetic.
func podcastCalendar(expression, zone string) (cron.Schedule, uint64, *time.Location, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		return nil, 0, nil, fmt.Errorf("%w: use an IANA timezone", ErrInvalid)
	}
	fields := strings.Fields(strings.ToLower(expression))
	if len(fields) != 5 && len(fields) != 6 {
		return nil, 0, nil, fmt.Errorf("%w: use five or six cron fields", ErrInvalid)
	}
	days := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	day := fields[len(fields)-1]
	for i, name := range days {
		day = strings.ReplaceAll(day, name, strconv.Itoa(i))
		day = strings.ReplaceAll(day, name[:3], strconv.Itoa(i))
	}
	// Expand only the weekday field to support both 0 and 7 for Sunday.
	var mask uint64
	for _, part := range strings.Split(day, ",") {
		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			step, err = strconv.Atoi(stepText)
			if err != nil || step < 1 || step > 7 {
				return nil, 0, nil, ErrInvalid
			}
		}
		lo, hi := 0, 7
		if base != "*" {
			a, b, ranged := strings.Cut(base, "-")
			lo, err = strconv.Atoi(a)
			if err != nil {
				return nil, 0, nil, ErrInvalid
			}
			hi = lo
			if ranged {
				hi, err = strconv.Atoi(b)
				if err != nil {
					return nil, 0, nil, ErrInvalid
				}
			} else if hasStep {
				hi = 7
			}
		}
		if lo < 0 || hi > 7 || hi < lo {
			return nil, 0, nil, ErrInvalid
		}
		for n := lo; n <= hi; n += step {
			mask |= 1 << uint(n%7)
		}
	}
	fields[len(fields)-1] = "*"
	months := []string{"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"}
	for _, name := range months {
		fields[len(fields)-2] = strings.ReplaceAll(fields[len(fields)-2], name, name[:3])
	}
	p := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := p.Parse("CRON_TZ=" + zone + " " + strings.Join(fields, " "))
	if err != nil {
		return nil, 0, nil, fmt.Errorf("%w: invalid cron schedule", ErrInvalid)
	}
	return schedule, mask, loc, nil
}

// NextPodcastCheck is strictly later than after. Missed runs coalesce into one
// check; successful-feed watermarks, not attempted runs, determine new episodes.
func NextPodcastCheck(in PodcastSettings, after time.Time) (time.Time, error) {
	if in.Schedule == "" {
		return after.Add(time.Duration(in.Interval) * time.Second), nil
	}
	schedule, days, loc, err := podcastCalendar(in.Schedule, in.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	limit := after.AddDate(28, 0, 0)
	for cursor := after; cursor.Before(limit); {
		next := schedule.Next(cursor)
		if next.IsZero() || !next.Before(limit) {
			break
		}
		local := next.In(loc)
		if days&(1<<uint(local.Weekday())) != 0 {
			return next, nil
		}
		// Skip the unmatched local date, including a midnight DST transition.
		boundary := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
		for !boundary.After(next) || boundary.Day() == local.Day() {
			boundary = boundary.Add(time.Hour)
		}
		cursor = boundary.Add(-time.Second)
	}
	return time.Time{}, fmt.Errorf("%w: schedule has no upcoming occurrence", ErrInvalid)
}
