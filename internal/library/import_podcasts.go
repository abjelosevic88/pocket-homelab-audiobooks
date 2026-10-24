package library

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Global subscription jobs need an explicitly selected retained identity. They
// never borrow an arbitrary account just because it happens to be mapped first.
func importPodcastSubscription(opt ImportOptions, users []sourceRow, item, media sourceRow) (statement, bool, error) {
	id := str(item["id"])
	url := str(media["feedURL"])
	p := PodcastSettings{Auto: truth(media["autoDownloadEpisodes"]), Interval: 3600, Keep: int(number(media["maxEpisodesToKeep"])), MaxNew: 3, Schedule: strings.TrimSpace(str(media["autoDownloadSchedule"])), Timezone: opt.ScheduleTimezone}
	if media["maxNewEpisodesToDownload"] != nil {
		p.MaxNew = int(number(media["maxNewEpisodesToDownload"]))
	}
	if p.Keep < 0 || p.Keep > 10000 || p.MaxNew < 1 || p.MaxNew > 100 {
		return statement{}, false, fmt.Errorf("podcast %s: unsupported source download limits", id)
	}
	if p.Schedule != "" && p.Timezone == "" {
		return statement{}, false, fmt.Errorf("podcast %s: specify --schedule-timezone to preserve source calendar timing", id)
	}
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	last, err := milliseconds(media["lastEpisodeCheck"])
	if err != nil {
		return statement{}, false, fmt.Errorf("podcast %s: invalid last episode check", id)
	}
	after := time.Now()
	if last > 0 {
		after = time.UnixMilli(last)
	}
	next, err := NextPodcastCheck(p, after)
	if err != nil {
		return statement{}, false, fmt.Errorf("podcast %s: %w", id, err)
	}
	var actor any
	if opt.PodcastOwner != "" {
		for _, u := range users {
			if str(u["id"]) != opt.PodcastOwner {
				continue
			}
			uid, ok := opt.Users[opt.PodcastOwner]
			var permissions Permissions
			if json.Unmarshal([]byte(raw(u["permissions"])), &permissions) != nil {
				return statement{}, false, ErrInvalid
			}
			a := Account{Permissions: permissions}
			tags := []string{}
			for _, v := range array(media["tags"]) {
				tags = append(tags, str(v))
			}
			it := Item{LibraryID: str(item["libraryId"]), Explicit: truth(media["explicit"]), Tags: tags}
			if !ok || !truth(u["isActive"]) || truth(u["isLocked"]) || !permissions.Update || !permissions.Upload || p.Keep > 0 && !permissions.Delete || !a.allows(it) {
				return statement{}, false, fmt.Errorf("podcast %s: selected worker account lacks required permissions", id)
			}
			actor = uid
		}
		if actor == nil {
			return statement{}, false, fmt.Errorf("podcast worker account must be retained and mapped")
		}
	}
	return statement{`INSERT INTO catalog_podcast_subscriptions(item_id,feed_url,actor_id,auto_download,interval_seconds,keep_latest,max_new,last_check,schedule,timezone,next_check,download_watermark) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, []any{id, url, actor, p.Auto, p.Interval, p.Keep, p.MaxNew, last, p.Schedule, p.Timezone, next.UnixMilli(), last}}, actor == nil, nil
}
