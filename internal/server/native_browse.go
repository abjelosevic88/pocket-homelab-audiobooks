package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Clamp before multiplying: a very large page must yield an empty page, not an
// integer overflow and negative slice offset.
func nativePage(total, page, limit int) (int, int) {
	if total <= 0 || limit <= 0 {
		return 0, 0
	}
	if page < 0 {
		page = 0
	}
	if page > total/limit {
		return total, total
	}
	start := page * limit
	return start, min(total, start+min(limit, total-start))
}
func nativeFilter(raw string) (string, string, error) {
	if raw == "" {
		return "", "", nil
	}
	group, value, encoded := strings.Cut(raw, ".")
	switch group {
	case "abridged", "explicit", "issues", "recent":
		if encoded {
			return "", "", fmt.Errorf("invalid filter")
		}
		return group, "", nil
	case "genres", "tags", "series", "authors", "progress", "narrators", "publishers", "publishedDecades", "missing", "languages", "tracks", "ebooks":
	default:
		return "", "", fmt.Errorf("unsupported filter")
	}
	if !encoded || len(value) > 8192 {
		return "", "", fmt.Errorf("invalid filter")
	}
	decoded, e := url.PathUnescape(value)
	if e != nil {
		return "", "", e
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, e := enc.DecodeString(decoded); e == nil {
			return group, string(b), nil
		}
	}
	return "", "", fmt.Errorf("invalid filter encoding")
}
func nativeMatchFilter(it, p nativeObject, group, value string) bool {
	m := nativeMap(it["media"])
	meta := nativeMetadata(it)
	switch group {
	case "":
		return true
	case "progress":
		finished := nativeBool(p["isFinished"])
		audio := nativeNumber(p["currentTime"]) > 0
		ebook := nativeNumber(p["ebookProgress"]) > 0
		switch value {
		case "finished":
			return finished
		case "not-finished":
			return !finished
		case "not-started":
			return !finished && !audio && !ebook
		case "in-progress":
			return !finished && (audio || ebook)
		case "audio-in-progress":
			return !finished && audio
		case "ebook-in-progress":
			return !finished && ebook && len(nativeArray(m["audioFiles"])) == 0
		case "ebook-finished":
			return finished && m["ebookFile"] != nil && len(nativeArray(m["audioFiles"])) == 0
		}
	case "series", "authors":
		list := nativeArray(meta[group])
		if group == "series" && value == "no-series" {
			return len(list) == 0
		}
		for _, v := range list {
			if nativeString(nativeMap(v)["id"]) == value {
				if group == "series" {
					it["sequence"] = nativeMap(v)["sequence"]
				}
				return true
			}
		}
	case "tags", "genres", "narrators":
		list := meta[group]
		if group == "tags" {
			list = m["tags"]
		}
		for _, v := range nativeArray(list) {
			if nativeString(v) == value {
				return true
			}
		}
	case "publishers":
		return nativeString(meta["publisher"]) == value
	case "languages":
		return nativeString(meta["language"]) == value
	case "publishedDecades":
		start, e := strconv.Atoi(value)
		year, e2 := strconv.Atoi(nativeString(meta["publishedYear"]))
		return e == nil && e2 == nil && year >= start && year-start <= 9
	case "abridged", "explicit":
		return nativeBool(meta[group])
	case "issues":
		return nativeBool(it["isMissing"]) || nativeBool(it["isInvalid"])
	case "recent":
		return nativeNumber(it["addedAt"]) >= float64(time.Now().AddDate(0, 0, -60).UnixMilli())
	case "tracks":
		n := len(nativeArray(m["audioFiles"]))
		switch value {
		case "none":
			return n == 0
		case "multi":
			return n > 1
		case "single":
			return n == 1
		}
	case "ebooks":
		switch value {
		case "ebook":
			return m["ebookFile"] != nil
		case "no-ebook":
			return m["ebookFile"] == nil
		}
	case "missing":
		switch value {
		case "asin", "isbn", "subtitle", "publishedYear", "description", "publisher", "language":
			return nativeString(meta[value]) == ""
		case "cover":
			return nativeString(m["coverPath"]) == ""
		case "tags", "chapters":
			return len(nativeArray(m[value])) == 0
		case "genres", "narrators", "authors", "series":
			return len(nativeArray(meta[value])) == 0
		}
	}
	return false
}
func nativeSortItems(items []nativeObject, key string, desc bool, seed string) {
	if seed == "" {
		seed = time.Now().UTC().Format("2006-01-02")
	}
	value := func(it nativeObject) (string, float64, bool) {
		m := nativeMap(it["media"])
		meta := nativeMetadata(it)
		p := nativeMap(it["userMediaProgress"])
		switch key {
		case "addedAt", "updatedAt", "size", "birthtimeMs", "mtimeMs":
			return "", nativeNumber(it[key]), true
		case "media.numTracks":
			return "", nativeNumber(m["numTracks"]), true
		case "media.duration":
			return "", nativeNumber(m["duration"]), true
		case "media.metadata.publishedYear":
			n, _ := strconv.ParseFloat(nativeString(meta["publishedYear"]), 64)
			return "", n, true
		case "sequence":
			n, _ := strconv.ParseFloat(nativeString(it["sequence"]), 64)
			return "", n, true
		case "progress":
			return "", nativeNumber(p["lastUpdate"]), true
		case "progress.createdAt":
			return "", nativeNumber(p["startedAt"]), true
		case "progress.finishedAt":
			return "", nativeNumber(p["finishedAt"]), true
		case "media.metadata.author", "media.metadata.authorName", "media.metadata.authorNameLF", "media.metadata.narratorName", "media.metadata.publisher":
			return nativeString(meta[strings.TrimPrefix(key, "media.metadata.")]), 0, false
		case "random":
			sum := sha256.Sum256([]byte(seed + "\x00" + nativeString(it["id"])))
			return hex.EncodeToString(sum[:]), 0, false
		default:
			if series := nativeMap(it["collapsedSeries"]); len(series) > 0 {
				return nativeString(series["name"]), 0, false
			}
			return nativeString(meta["title"]), 0, false
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		av, an, num := value(items[i])
		bv, bn, _ := value(items[j])
		cmp := strings.Compare(strings.ToLower(av), strings.ToLower(bv))
		if num {
			cmp = 0
			if an < bn {
				cmp = -1
			} else if an > bn {
				cmp = 1
			}
		}
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(nativeString(nativeMetadata(items[i])["title"])), strings.ToLower(nativeString(nativeMetadata(items[j])["title"])))
		}
		if cmp == 0 {
			return nativeString(items[i]["id"]) < nativeString(items[j]["id"])
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}
func nativeCollapseSeries(items []nativeObject) []nativeObject {
	groups := map[string][]nativeObject{}
	series := map[string]nativeObject{}
	out := []nativeObject{}
	for _, it := range items {
		list := nativeArray(nativeMetadata(it)["series"])
		if len(list) == 0 {
			out = append(out, it)
			continue
		}
		g := nativeMap(list[0])
		id := nativeString(g["id"])
		if id == "" {
			out = append(out, it)
			continue
		}
		it["sequence"] = g["sequence"]
		series[id] = g
		groups[id] = append(groups[id], it)
	}
	for id, books := range groups {
		nativeSortItems(books, "sequence", false, "")
		it := books[0]
		g := series[id]
		it["collapsedSeries"] = nativeObject{"id": id, "name": g["name"], "nameIgnorePrefix": g["name"], "sequence": it["sequence"], "numBooks": len(books)}
		out = append(out, it)
	}
	return out
}
func nativeFilterData(items []nativeObject) nativeObject {
	out := nativeObject{"bookCount": 0, "podcastCount": 0, "numIssues": 0, "loadedAt": time.Now().UnixMilli()}
	sets := map[string]map[string]bool{}
	for _, k := range []string{"genres", "tags", "narrators", "languages", "publishers", "publishedDecades"} {
		sets[k] = map[string]bool{}
	}
	for _, it := range items {
		kind := "bookCount"
		if it["mediaType"] == "podcast" {
			kind = "podcastCount"
		}
		out[kind] = nativeNumber(out[kind]) + 1
		if nativeBool(it["isMissing"]) || nativeBool(it["isInvalid"]) {
			out["numIssues"] = nativeNumber(out["numIssues"]) + 1
		}
		meta := nativeMetadata(it)
		for _, k := range []string{"genres", "narrators", "tags"} {
			v := meta[k]
			if k == "tags" {
				v = nativeMap(it["media"])[k]
			}
			for _, val := range nativeArray(v) {
				if s := nativeString(val); s != "" {
					sets[k][s] = true
				}
			}
		}
		for k, field := range map[string]string{"languages": "language", "publishers": "publisher"} {
			if v := nativeString(meta[field]); v != "" {
				sets[k][v] = true
			}
		}
		if n, e := strconv.Atoi(nativeString(meta["publishedYear"])); e == nil && n > 0 {
			sets["publishedDecades"][strconv.Itoa(n/10*10)] = true
		}
	}
	for k, set := range sets {
		values := []string{}
		for v := range set {
			values = append(values, v)
		}
		sort.Strings(values)
		out[k] = values
	}
	return out
}

func (s *Server) nativeShelves(items []nativeObject, progress []any, limit int) []any {
	byEpisode := map[string]nativeObject{}
	for _, v := range progress {
		p := nativeMap(v)
		byEpisode[nativeString(p["libraryItemId"])+"/"+nativeString(p["episodeId"])] = p
	}
	continuing, reading, finished, readAgain, recent, discover, episodes := []nativeObject{}, []nativeObject{}, []nativeObject{}, []nativeObject{}, []nativeObject{}, []nativeObject{}, []nativeObject{}
	podcast := false
	for _, it := range items {
		recent = append(recent, it)
		m := nativeMap(it["media"])
		if it["mediaType"] == "podcast" {
			podcast = true
			for _, v := range nativeArray(m["episodes"]) {
				ep := nativeMap(v)
				p := byEpisode[nativeString(it["id"])+"/"+nativeString(ep["id"])]
				copy := nativeObject{}
				for k, v := range it {
					copy[k] = v
				}
				copy["recentEpisode"], copy["userMediaProgress"] = ep, p
				episodes = append(episodes, copy)
				if nativeBool(p["isFinished"]) {
					finished = append(finished, copy)
				} else if nativeNumber(p["currentTime"]) > 0 && !nativeBool(p["hideFromContinueListening"]) {
					continuing = append(continuing, copy)
				}
			}
			continue
		}
		p := nativeMap(it["userMediaProgress"])
		audio := len(nativeArray(m["audioFiles"])) > 0
		if nativeBool(p["isFinished"]) {
			if audio {
				finished = append(finished, it)
			} else {
				readAgain = append(readAgain, it)
			}
		} else if !nativeBool(p["hideFromContinueListening"]) {
			if audio && nativeNumber(p["currentTime"]) > 0 {
				continuing = append(continuing, it)
			} else if !audio && nativeNumber(p["ebookProgress"]) > 0 {
				reading = append(reading, it)
			} else if nativeNumber(p["currentTime"]) == 0 && nativeNumber(p["ebookProgress"]) == 0 {
				discover = append(discover, it)
			}
		}
	}
	for _, list := range [][]nativeObject{continuing, reading} {
		nativeSortItems(list, "progress", true, "")
	}
	for _, list := range [][]nativeObject{finished, readAgain} {
		nativeSortItems(list, "progress.finishedAt", true, "")
	}
	nativeSortItems(recent, "addedAt", true, "")
	nativeSortItems(discover, "random", false, "")
	out := []any{}
	add := func(id, label, kind string, list []nativeObject) {
		if len(list) > 0 {
			out = append(out, nativeObject{"id": id, "label": label, "type": kind, "total": len(list), "entities": list[:min(limit, len(list))]})
		}
	}
	typ := "book"
	if podcast {
		typ = "episode"
	}
	add("continue-listening", "Continue Listening", typ, continuing)
	add("continue-reading", "Continue Reading", "book", reading)
	if podcast {
		sort.SliceStable(episodes, func(i, j int) bool {
			return nativeNumber(nativeMap(episodes[i]["recentEpisode"])["publishedAt"]) > nativeNumber(nativeMap(episodes[j]["recentEpisode"])["publishedAt"])
		})
		add("newest-episodes", "Newest Episodes", "episode", episodes)
		add("recently-added", "Recently Added", "podcast", recent)
	} else {
		groups := map[string][]nativeObject{}
		for _, it := range items {
			for _, v := range nativeArray(nativeMetadata(it)["series"]) {
				g := nativeMap(v)
				copy := nativeObject{}
				for k, v := range it {
					copy[k] = v
				}
				copy["sequence"] = g["sequence"]
				groups[nativeString(g["id"])] = append(groups[nativeString(g["id"])], copy)
			}
		}
		next := []nativeObject{}
		seen := map[string]bool{}
		for _, books := range groups {
			hasFinished := false
			for _, it := range books {
				if nativeBool(nativeMap(it["userMediaProgress"])["isFinished"]) {
					hasFinished = true
				}
			}
			if !hasFinished {
				continue
			}
			nativeSortItems(books, "sequence", false, "")
			for _, it := range books {
				p := nativeMap(it["userMediaProgress"])
				if !nativeBool(p["isFinished"]) {
					id := nativeString(it["id"])
					if !seen[id] && nativeNumber(p["currentTime"]) == 0 && !nativeBool(p["hideFromContinueListening"]) {
						next = append(next, it)
						seen[id] = true
					}
					break
				}
			}
		}
		nativeSortItems(next, "media.metadata.title", false, "")
		add("continue-series", "Continue Series", "book", next)
		add("recently-added", "Recently Added", "book", recent)
		add("discover", "Discover", "book", discover)
	}
	add("listen-again", "Listen Again", typ, finished)
	add("read-again", "Read Again", "book", readAgain)
	return out
}
