package server

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/feed"
)

func (s *Server) nativePodcastLibrary(w http.ResponseWriter, r *http.Request, uid int64, lib, action string, items []nativeObject, progress []any, limit, page int) {
	ctx := r.Context()
	respond := func(v any) { writeJSON(w, 200, v) }
	switch action {
	case "opml":
		subs := []feed.Subscription{}
		for _, it := range items {
			if u := nativeString(nativeMetadata(it)["feedURL"]); u != "" {
				subs = append(subs, feed.Subscription{Title: nativeString(nativeMetadata(it)["title"]), URL: u})
			}
		}
		data, e := feed.ExportOPML(subs)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="podcasts.opml"`)
		w.Write(data)
	case "podcast-titles":
		a, e := s.catalog().Account(ctx, uid)
		if e != nil || !a.Permissions.Update {
			writeErr(w, 403, "podcast editing permission required")
			return
		}
		out := []any{}
		for _, it := range items {
			m := nativeMetadata(it)
			out = append(out, nativeObject{"title": m["title"], "itunesId": m["itunesId"], "libraryItemId": it["id"], "libraryId": lib})
		}
		respond(nativeObject{"podcasts": out})
	case "episode-downloads":
		allowed := map[string]nativeObject{}
		for _, it := range items {
			allowed[nativeString(it["id"])] = it
		}
		rows, e := s.store.DB.QueryContext(ctx, `SELECT j.id,j.item_id,j.episode_id,j.status,j.created_at,j.updated_at FROM catalog_podcast_jobs j JOIN catalog_items i ON i.id=j.item_id WHERE i.library_id=? AND j.status IN ('queued','running') ORDER BY j.created_at,j.id`, lib)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		type job struct {
			id, item, episode, status string
			created, updated          int64
		}
		jobs := []job{}
		for rows.Next() {
			var j job
			if e = rows.Scan(&j.id, &j.item, &j.episode, &j.status, &j.created, &j.updated); e != nil {
				rows.Close()
				s.nativeError(w, e)
				return
			}
			jobs = append(jobs, j)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			s.nativeError(w, e)
			return
		}
		queue := []any{}
		var current any
		for _, j := range jobs {
			it, ok := allowed[j.item]
			if !ok {
				continue
			}
			ep, _, e := s.catalog().EpisodeDocument(ctx, uid, j.item, j.episode)
			if e != nil {
				continue
			}
			d := episodeCompat(ep)
			d["id"], d["libraryItemId"], d["libraryId"], d["episodeDisplayTitle"], d["podcastTitle"], d["status"] = j.id, j.item, lib, ep["title"], nativeMetadata(it)["title"], j.status
			d["url"], d["createdAt"], d["startedAt"], d["isFinished"], d["failed"] = ep["enclosureURL"], j.created, j.updated, false, false
			if j.status == "running" && current == nil {
				current = d
			} else {
				queue = append(queue, d)
			}
		}
		respond(nativeObject{"currentDownload": current, "queue": queue})
	case "recent-episodes":
		byEpisode := map[string]nativeObject{}
		for _, v := range progress {
			p := nativeMap(v)
			byEpisode[nativeString(p["libraryItemId"])+"/"+nativeString(p["episodeId"])] = p
		}
		episodes := []nativeObject{}
		for _, it := range items {
			m := nativeMap(it["media"])
			pod := nativeObject{}
			for k, v := range m {
				if k != "episodes" {
					pod[k] = v
				}
			}
			pod["episodes"] = []any{}
			for _, v := range nativeArray(m["episodes"]) {
				ep := nativeMap(v)
				p := byEpisode[nativeString(it["id"])+"/"+nativeString(ep["id"])]
				if nativeBool(p["isFinished"]) {
					continue
				}
				ep["podcast"], ep["libraryId"], ep["userMediaProgress"] = pod, lib, p
				episodes = append(episodes, ep)
			}
		}
		sort.Slice(episodes, func(i, j int) bool {
			a, b := nativeNumber(episodes[i]["publishedAt"]), nativeNumber(episodes[j]["publishedAt"])
			if a == b {
				return nativeString(episodes[i]["id"]) < nativeString(episodes[j]["id"])
			}
			return a > b
		})
		start, end := nativePage(len(episodes), page, limit)
		respond(nativeObject{"episodes": episodes[start:end], "total": len(episodes), "limit": limit, "page": page})
	}
}
func nativeNarrators(items []nativeObject) []nativeObject {
	counts := map[string]int{}
	for _, it := range items {
		seen := map[string]bool{}
		for _, v := range nativeArray(nativeMetadata(it)["narrators"]) {
			name := nativeString(v)
			if name != "" && !seen[name] {
				seen[name] = true
				counts[name]++
			}
		}
	}
	out := []nativeObject{}
	for name, count := range counts {
		out = append(out, nativeObject{"id": base64.StdEncoding.EncodeToString([]byte(name)), "name": name, "numBooks": count})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(nativeString(out[i]["name"])) < strings.ToLower(nativeString(out[j]["name"]))
	})
	return out
}
func (s *Server) nativeLibraryStats(r *http.Request, items []nativeObject) nativeObject {
	size, duration := 0.0, 0.0
	tracks := 0
	genres := map[string]int{}
	for _, it := range items {
		m := nativeMap(it["media"])
		size += nativeNumber(it["size"])
		duration += nativeNumber(m["duration"])
		if it["mediaType"] == "podcast" {
			tracks += len(nativeArray(m["episodes"]))
		} else {
			tracks += len(nativeArray(m["audioFiles"]))
		}
		for _, g := range nativeArray(nativeMetadata(it)["genres"]) {
			genres[nativeString(g)]++
		}
	}
	genreCounts := []nativeObject{}
	for g, n := range genres {
		genreCounts = append(genreCounts, nativeObject{"genre": g, "count": n})
	}
	sort.Slice(genreCounts, func(i, j int) bool {
		a, b := nativeNumber(genreCounts[i]["count"]), nativeNumber(genreCounts[j]["count"])
		if a == b {
			return nativeString(genreCounts[i]["genre"]) < nativeString(genreCounts[j]["genre"])
		}
		return a > b
	})
	authors := s.nativeGroups(r.Context(), items, "authors")
	sort.SliceStable(authors, func(i, j int) bool {
		return nativeNumber(authors[i]["numBooks"]) > nativeNumber(authors[j]["numBooks"])
	})
	largest := append([]nativeObject{}, items...)
	longest := append([]nativeObject{}, items...)
	nativeSortItems(largest, "size", true, "")
	nativeSortItems(longest, "media.duration", true, "")
	return nativeObject{"totalItems": len(items), "totalSize": size, "totalDuration": duration, "numAudioTracks": tracks, "numAuthors": len(authors), "totalAuthors": len(authors), "authorsWithCount": authors[:min(10, len(authors))], "totalGenres": len(genres), "genresWithCount": genreCounts, "numSeries": len(s.nativeGroups(r.Context(), items, "series")), "largestItems": largest[:min(10, len(largest))], "longestItems": longest[:min(10, len(longest))]}
}
