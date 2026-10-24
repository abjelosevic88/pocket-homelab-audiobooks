package library

// excludeSourcePodcasts removes podcast relationships before planning an import.
// Account rows are retained, but their podcast bookmarks/progress/history and
// podcast-only libraries/lists are omitted. No source database or files change.
func excludeSourcePodcasts(data map[string][]sourceRow, counts map[string]int) {
	libraries, items, media, lists := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range data["libraries"] {
		if str(r["mediaType"]) != "book" {
			libraries[str(r["id"])] = true
		}
	}
	for _, r := range data["libraryItems"] {
		if str(r["mediaType"]) != "book" || libraries[str(r["libraryId"])] {
			items[str(r["id"])] = true
			media[str(r["mediaId"])] = true
		}
	}
	for _, r := range data["podcasts"] {
		media[str(r["id"])] = true
	}
	for _, r := range data["podcastEpisodes"] {
		media[str(r["id"])] = true
	}
	filter := func(table string, drop func(sourceRow) bool) {
		kept := []sourceRow{}
		for _, r := range data[table] {
			if drop(r) {
				counts[table]++
			} else {
				kept = append(kept, r)
			}
		}
		data[table] = kept
	}
	filter("libraries", func(r sourceRow) bool { return libraries[str(r["id"])] })
	filter("libraryFolders", func(r sourceRow) bool { return libraries[str(r["libraryId"])] })
	filter("libraryItems", func(r sourceRow) bool { return items[str(r["id"])] })
	filter("books", func(r sourceRow) bool { return media[str(r["id"])] })
	for _, table := range []string{"bookAuthors", "bookSeries"} {
		filter(table, func(r sourceRow) bool { return media[str(r["bookId"])] })
	}
	for _, table := range []string{"podcasts", "podcastEpisodes"} {
		filter(table, func(sourceRow) bool { return true })
	}
	for _, table := range []string{"mediaProgresses", "playbackSessions", "playlistMediaItems"} {
		filter(table, func(r sourceRow) bool {
			return media[str(r["mediaItemId"])] || str(r["mediaType"]) == "podcast" || str(r["mediaItemType"]) == "podcastEpisode" || items[str(object(r["extraData"])["libraryItemId"])]
		})
	}
	for _, table := range []string{"collections", "playlists"} {
		filter(table, func(r sourceRow) bool {
			if libraries[str(r["libraryId"])] {
				lists[str(r["id"])] = true
				return true
			}
			return false
		})
	}
	filter("collectionBooks", func(r sourceRow) bool { return lists[str(r["collectionId"])] || media[str(r["bookId"])] })
	filter("playlistMediaItems", func(r sourceRow) bool { return lists[str(r["playlistId"])] })
	for _, r := range data["users"] {
		marks := []any{}
		for _, v := range array(r["bookmarks"]) {
			if items[str(object(v)["libraryItemId"])] {
				counts["bookmarks"]++
			} else {
				marks = append(marks, v)
			}
		}
		r["bookmarks"] = marks
	}
}
