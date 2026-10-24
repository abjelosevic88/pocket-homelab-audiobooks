package library

import "testing"

func TestBookOnlyImportDropsPodcastRelationshipsAndKeepsBooks(t *testing.T) {
	data := map[string][]sourceRow{
		"libraries":      {{"id": "books", "mediaType": "book"}, {"id": "pods", "mediaType": "podcast"}},
		"libraryFolders": {{"id": "bf", "libraryId": "books"}, {"id": "pf", "libraryId": "pods"}},
		"libraryItems":   {{"id": "book", "mediaId": "bm", "libraryId": "books", "mediaType": "book"}, {"id": "pod", "mediaId": "pm", "libraryId": "pods", "mediaType": "podcast"}},
		"books":          {{"id": "bm"}}, "podcasts": {{"id": "pm"}}, "podcastEpisodes": {{"id": "ep", "podcastId": "pm"}},
		"mediaProgresses":    {{"id": "bp", "mediaItemId": "bm"}, {"id": "pp", "mediaItemId": "ep"}},
		"playbackSessions":   {{"id": "bs", "mediaItemId": "bm"}, {"id": "ps", "mediaItemId": "ep"}, {"id": "old", "mediaItemId": "deleted", "mediaItemType": "podcastEpisode"}},
		"playlists":          {{"id": "mix", "libraryId": "books"}, {"id": "pod-list", "libraryId": "pods"}},
		"playlistMediaItems": {{"id": "one", "playlistId": "mix", "mediaItemId": "bm"}, {"id": "two", "playlistId": "mix", "mediaItemId": "ep"}, {"id": "three", "playlistId": "pod-list", "mediaItemId": "bm"}},
		"users":              {{"id": "root", "bookmarks": []any{sourceRow{"libraryItemId": "book", "time": 42}, sourceRow{"libraryItemId": "pod", "time": 10}}}},
	}
	counts := map[string]int{}
	excludeSourcePodcasts(data, counts)
	for _, table := range []string{"libraries", "libraryFolders", "libraryItems", "books", "mediaProgresses", "playbackSessions", "playlists", "playlistMediaItems", "users"} {
		if len(data[table]) != 1 {
			t.Fatalf("%s: %v", table, data[table])
		}
	}
	if len(data["podcasts"]) != 0 || len(data["podcastEpisodes"]) != 0 || len(array(data["users"][0]["bookmarks"])) != 1 {
		t.Fatal(data)
	}
	if counts["playbackSessions"] != 2 || counts["playlistMediaItems"] != 2 || counts["bookmarks"] != 1 {
		t.Fatal(counts)
	}
}
