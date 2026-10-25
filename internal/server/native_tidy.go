package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/metadata"
	"github.com/go-chi/chi/v5"
)

// Library tidy-up: repair broken titles/series/subtitles and find missing
// covers. Both are preview first, then apply what the person ticked.
func (s *Server) nativeTidyRoutes(r chi.Router) {
	r.Get("/catalog/libraries/{id}/tidy", s.nativeTidyPreview)
	r.Post("/catalog/libraries/{id}/tidy", s.nativeTidyApply)
	r.Get("/catalog/libraries/{id}/covers", s.nativeCoversPreview)
	r.Post("/catalog/libraries/{id}/covers", s.nativeCoversApply)
}

func (s *Server) tidyEditor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native editing is not active")
		return 0, false
	}
	uid := actorID(r.Context())
	a, err := s.catalog().Account(r.Context(), uid)
	if err != nil || !a.Permissions.Update {
		writeErr(w, 403, "editing permission required")
		return 0, false
	}
	return uid, true
}

func (s *Server) nativeTidyPreview(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.tidyEditor(w, r)
	if !ok {
		return
	}
	report, err := s.catalog().TidyPreview(r.Context(), uid, chi.URLParam(r, "id"))
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, report)
}

func (s *Server) nativeTidyApply(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.tidyEditor(w, r)
	if !ok {
		return
	}
	var in struct {
		Items []library.TidySelection `json:"items"`
	}
	if decode(r, &in) != nil || len(in.Items) == 0 || len(in.Items) > 5000 {
		writeErr(w, 400, "select 1 to 5000 books")
		return
	}
	n, err := s.catalog().TidyApply(r.Context(), uid, chi.URLParam(r, "id"), in.Items)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, nativeObject{"updated": n})
}

// coverLookup is an Audible cover found for a book without a local one.
type coverLookup struct {
	library.CoverCandidate
	CoverURL string `json:"cover_url,omitempty"`
	Match    string `json:"match,omitempty"` // the Audible title a search matched
	Reason   string `json:"reason,omitempty"`
}

// nativeCoversPreview lists books without a cover and where each one's cover
// would come from: embedded art, a folder image, Audible by ASIN, and with
// ?search=1 an Audible title search confident enough for quick match.
func (s *Server) nativeCoversPreview(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.tidyEditor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	found, err := s.catalog().MissingCovers(ctx, uid, chi.URLParam(r, "id"), s.cfg.FFprobe)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	search := r.URL.Query().Get("search") == "1"
	out := []coverLookup{}
	lookups := 0
	for _, c := range found {
		v := coverLookup{CoverCandidate: c}
		if v.Source == "" && lookups < 40 && ctx.Err() == nil && (metadata.LooksLikeASIN(c.ASIN) || search) {
			lookups++
			s.audibleCover(ctx, uid, &v, search)
		}
		if v.Source == "" && v.Reason == "" {
			v.Reason = "no embedded picture, folder image or ASIN"
		}
		out = append(out, v)
	}
	writeJSON(w, 200, nativeObject{"books": out})
}

// audibleCover fills an Audible cover from the book's ASIN, or (search) from
// a confident title + author match.
func (s *Server) audibleCover(ctx context.Context, uid int64, v *coverLookup, search bool) {
	p, err := s.nativeProvider(ctx, "audible")
	if err != nil {
		v.Reason = "the Audible provider is off"
		return
	}
	c := s.nativeMetadataClient()
	if metadata.LooksLikeASIN(v.ASIN) {
		if cand, err := c.AudibleBook(ctx, p.ID, p.Region, v.ASIN); err == nil && cand.Cover != "" {
			v.Source, v.Detail, v.CoverURL, v.Match = library.CoverAudible, v.ASIN, cand.Cover, cand.Title
			return
		}
		v.Reason = "Audible has no cover for ASIN " + v.ASIN
	}
	if !search {
		return
	}
	it, err := s.catalog().Item(ctx, uid, v.ID)
	if err != nil {
		return
	}
	cur, _ := library.CurrentMetadata(it)
	q := matchQuery(it, cur)
	title := metadata.CleanTitle(strings.TrimSuffix(strings.TrimSuffix(it.Title, " - Normal Audio"), " - GA"))
	ranked, err := s.matchCandidates(ctx, p, q, title, firstAuthor(q))
	if err != nil || len(ranked) == 0 {
		v.Reason = "no Audible match for the title"
		return
	}
	best := ranked[0]
	if !best.Score.Confident || best.Cover == "" {
		v.Reason = "no confident Audible match (best: " + best.Title + ")"
		return
	}
	v.Source, v.Detail, v.CoverURL, v.Match, v.Reason = library.CoverAudible, best.ID, best.Cover, best.Title, ""
}

// nativeCoversApply saves the covers the person ticked. Each book is saved on
// its own; the result lists what failed and why.
func (s *Server) nativeCoversApply(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.tidyEditor(w, r)
	if !ok {
		return
	}
	var in struct {
		Items []struct {
			ID       string `json:"id"`
			Expected int64  `json:"expected_updated_at"`
			Source   string `json:"source"`
			Detail   string `json:"detail"` // the ASIN for Audible covers
		} `json:"items"`
	}
	if decode(r, &in) != nil || len(in.Items) == 0 || len(in.Items) > 500 {
		writeErr(w, 400, "select 1 to 500 books")
		return
	}
	ctx := r.Context()
	lib := chi.URLParam(r, "id")
	saved, failed := 0, []nativeObject{}
	for _, v := range in.Items {
		fail := func(msg string) { failed = append(failed, nativeObject{"id": v.ID, "error": msg}) }
		it, err := s.catalog().Item(ctx, uid, v.ID)
		if err != nil || it.LibraryID != lib {
			fail("not found")
			continue
		}
		if it.UpdatedAt != v.Expected {
			fail("changed since the preview")
			continue
		}
		var data []byte
		switch v.Source {
		case library.CoverEmbedded, library.CoverFolder:
			data, err = s.catalog().LocalCover(ctx, uid, v.ID, v.Source, s.cfg.FFprobe, s.cfg.FFmpeg)
		case library.CoverAudible:
			data, err = s.audibleCoverData(ctx, uid, v.ID, v.Detail)
		default:
			err = library.ErrInvalid
		}
		if err == nil {
			_, err = s.nativeSaveCover(ctx, uid, v.ID, data)
		}
		if err != nil {
			fail(err.Error())
			continue
		}
		saved++
	}
	writeJSON(w, 200, nativeObject{"saved": saved, "failed": failed})
}

// audibleCoverData downloads the cover Audible reports for a product; the
// client names only the ASIN, never the image URL.
func (s *Server) audibleCoverData(ctx context.Context, uid int64, id, asin string) ([]byte, error) {
	if !metadata.LooksLikeASIN(asin) {
		return nil, library.ErrInvalid
	}
	p, err := s.nativeProvider(ctx, "audible")
	if err != nil {
		return nil, err
	}
	cand, err := s.nativeMetadataClient().AudibleBook(ctx, p.ID, p.Region, asin)
	if err != nil {
		return nil, err
	}
	if cand.Cover == "" {
		return nil, library.ErrNotFound
	}
	if _, err = s.catalog().Item(ctx, uid, id); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, _, err := fetch.Get(ctx, s.nativeFetchHTTP, cand.Cover, s.cfg.UserAgent, "", 16<<20)
	return data, err
}
