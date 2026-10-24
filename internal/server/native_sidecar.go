package server

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (s *Server) nativeMetadataExport(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native export is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	a, err := s.catalog().Account(ctx, uid)
	if err != nil || !a.Permissions.Download {
		writeErr(w, 403, "download permission required")
		return
	}
	it, err := s.nativeItem(ctx, uid, chi.URLParam(r, "id"))
	if err != nil {
		s.nativeError(w, err)
		return
	}
	meta := nativeMetadata(it)
	out := nativeObject{}
	for _, key := range []string{"title", "subtitle", "description", "publisher", "publishedYear", "publishedDate", "isbn", "asin", "language", "explicit", "abridged", "genres", "narrators"} {
		if v := meta[key]; v != nil {
			out[key] = v
		}
	}
	authors := []string{}
	for _, v := range nativeArray(meta["authors"]) {
		authors = append(authors, nativeString(nativeMap(v)["name"]))
	}
	out["authors"] = authors
	series := []string{}
	for _, v := range nativeArray(meta["series"]) {
		m := nativeMap(v)
		name := nativeString(m["name"])
		if seq := nativeString(m["sequence"]); seq != "" {
			name += " #" + seq
		}
		series = append(series, name)
	}
	out["series"] = series
	media := nativeMap(it["media"])
	out["tags"] = media["tags"]
	out["chapters"] = nativeArray(media["chapters"])
	if nativeString(it["mediaType"]) == "podcast" {
		for _, key := range []string{"author", "feedURL", "imageURL", "itunesId", "itunesPageURL", "releaseDate"} {
			if v := meta[key]; v != nil {
				out[key] = v
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="metadata.json"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(out)
}
