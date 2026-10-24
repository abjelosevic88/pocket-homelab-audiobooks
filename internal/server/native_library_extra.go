package server

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strings"
)

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
