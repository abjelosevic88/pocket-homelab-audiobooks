package server

// Match and quick match: search the configured metadata providers with a book's
// title and author, rank the candidates, and either let a person choose the
// fields to apply (Match) or apply a confident result automatically (Quick
// match, one book or a background job over many). Writes go through the
// editor's optimistic revision checks and the regular cover pipeline.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/metadata"
	"github.com/go-chi/chi/v5"
)

// nativeMatchRoutes registers the match endpoints in the authenticated API group.
func (s *Server) nativeMatchRoutes(r chi.Router) {
	r.Get("/catalog/items/{id}/match", s.nativeMatchSearch)
	r.Post("/catalog/items/{id}/match", s.nativeMatchApply)
	r.Post("/catalog/items/{id}/quick-match", s.nativeQuickMatch)
	r.Get("/catalog/items/{id}/audible-chapters", s.nativeAudibleChapters)
	r.Get("/catalog/match-jobs", s.nativeMatchJobs)
	r.Post("/catalog/match-jobs", s.nativeMatchJobs)
	r.Get("/catalog/match-jobs/{job}", s.nativeMatchJob)
	r.Delete("/catalog/match-jobs/{job}", s.nativeMatchJob)
	s.nativeMatchReviewRoutes(r)
}

// matchRuns holds the cancel functions of bulk jobs running in this process.
// A queued/running job without an entry was interrupted by a restart.
var matchRuns = struct {
	sync.Mutex
	cancel map[string]context.CancelFunc
}{cancel: map[string]context.CancelFunc{}}

var matchJobID = regexp.MustCompile(`^match-[A-Za-z0-9]{26}$`)

// matchEditor checks native mode and edit permission and loads the book.
func (s *Server) matchEditor(w http.ResponseWriter, r *http.Request) (library.Item, bool) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return library.Item{}, false
	}
	a, err := s.catalog().Account(r.Context(), actorID(r.Context()))
	if err != nil || !a.Permissions.Update {
		writeErr(w, 403, "metadata editing permission required")
		return library.Item{}, false
	}
	it, err := s.catalog().Item(r.Context(), actorID(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.nativeError(w, err)
		return it, false
	}
	if it.MediaType != "book" {
		writeErr(w, 400, "only books can be matched")
		return it, false
	}
	return it, true
}

// matchProviders returns the requested enabled provider, or every enabled
// provider in match order.
func (s *Server) matchProviders(ctx context.Context, id string) ([]metadata.Provider, error) {
	if id != "" {
		p, err := s.nativeProvider(ctx, id)
		if err != nil {
			return nil, err
		}
		return []metadata.Provider{p}, nil
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id FROM catalog_metadata_providers WHERE enabled=1 ORDER BY priority,name`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, v)
	}
	rows.Close()
	out := []metadata.Provider{}
	for _, v := range ids {
		p, err := s.nativeProvider(ctx, v)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("enable a metadata provider first")
	}
	return out, nil
}

func matchQuery(it library.Item, cur map[string]any) metadata.MatchQuery {
	q := metadata.MatchQuery{Title: it.Title, Duration: it.Duration, ASIN: nativeString(cur["asin"]), ISBN: nativeString(cur["isbn"]), Subtitle: nativeString(cur["subtitle"])}
	for _, a := range nativeArray(cur["authors"]) {
		if n := nativeString(nativeMap(a)["name"]); n != "" {
			q.Authors = append(q.Authors, n)
		}
	}
	if len(q.Authors) == 0 && it.Author != "" {
		q.Authors = metadata.SplitAuthors(it.Author)
	}
	for _, n := range nativeArray(cur["narrators"]) {
		if name := nativeString(n); name != "" {
			q.Narrators = append(q.Narrators, name)
		}
	}
	q.Publisher = nativeString(cur["publisher"])
	return q
}

// candidates searches one provider for a book. An Audible book with an ASIN is
// looked up directly first; a title search follows unless that was exact.
func (s *Server) matchCandidates(ctx context.Context, p metadata.Provider, q metadata.MatchQuery, title, author string) ([]metadata.Ranked, error) {
	c := s.nativeMetadataClient()
	found := []metadata.Candidate{}
	if p.Kind == "audible" && q.ASIN != "" {
		if cand, err := c.AudibleBook(ctx, p.ID, p.Region, q.ASIN); err == nil {
			found = append(found, cand)
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if len(found) == 0 || !metadata.ScoreCandidate(q, found[0]).Exact {
		res, err := c.Search(ctx, p, title, author, "")
		if err != nil && len(found) == 0 {
			return nil, err
		}
		for _, cand := range res {
			if len(found) > 0 && cand.ID == found[0].ID {
				continue
			}
			found = append(found, cand)
		}
	}
	return metadata.Rank(q, found), nil
}

// matchDetail enriches a search result with the provider's detail record.
func (s *Server) matchDetail(ctx context.Context, p metadata.Provider, cand metadata.Candidate) metadata.Candidate {
	c := s.nativeMetadataClient()
	switch p.Kind {
	case "audible":
		if d, err := c.AudibleBook(ctx, p.ID, p.Region, cand.ID); err == nil {
			return metadata.Merge(cand, d)
		}
	case "openlibrary":
		if extra, err := c.Work(ctx, cand.ID); err == nil {
			return metadata.Merge(cand, metadata.Candidate{Metadata: extra})
		}
	}
	return cand
}

func firstAuthor(q metadata.MatchQuery) string {
	if len(q.Authors) > 0 {
		return q.Authors[0]
	}
	return ""
}

func (s *Server) nativeMatchSearch(w http.ResponseWriter, r *http.Request) {
	it, ok := s.matchEditor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	cur, hasCover := library.CurrentMetadata(it)
	q := matchQuery(it, cur)
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if title == "" {
		title = metadata.CleanTitle(q.Title)
	}
	author := firstAuthor(q)
	if _, set := r.URL.Query()["author"]; set {
		author = strings.TrimSpace(r.URL.Query().Get("author"))
	}
	if len(title) > 500 || len(author) > 500 {
		writeErr(w, 400, "search text is too long")
		return
	}
	providers, err := s.matchProviders(ctx, r.URL.Query().Get("provider"))
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			s.nativeError(w, err)
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	detail, _ := strconv.Atoi(r.URL.Query().Get("detail"))
	out := nativeObject{"item": nativeObject{"id": it.ID, "title": it.Title, "author": it.Author, "duration": it.Duration, "expected_updated_at": it.UpdatedAt, "has_cover": hasCover, "metadata": cur, "path": itemFolder(it), "dramatized": q.Dramatized()}, "query": nativeObject{"title": title, "author": author}, "candidates": []reviewCandidate{}, "errors": []string{}}
	errs := []string{}
	for _, p := range providers {
		ranked, err := s.matchCandidates(ctx, p, q, title, author)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			errs = append(errs, p.ID+": "+err.Error())
			continue
		}
		if len(ranked) > 0 {
			out["provider"] = p.ID
			out["candidates"] = s.detailCandidates(ctx, p, q, ranked, detail)
			break
		}
	}
	out["errors"] = errs
	writeJSON(w, 200, out)
}

// nativeMatchApply saves the fields a person chose in the Match dialog. The
// cover is downloaded before anything changes, so a bad URL leaves the book as it was.
func (s *Server) nativeMatchApply(w http.ResponseWriter, r *http.Request) {
	it, ok := s.matchEditor(w, r)
	if !ok {
		return
	}
	var in struct {
		Expected int64          `json:"expected_updated_at"`
		Metadata map[string]any `json:"metadata"`
		Cover    string         `json:"cover"`
	}
	if decode(r, &in) != nil || (len(in.Metadata) == 0 && in.Cover == "") {
		writeErr(w, 400, "choose at least one field to apply")
		return
	}
	if in.Expected != it.UpdatedAt {
		s.nativeError(w, library.ErrConflict)
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	var cover []byte
	if in.Cover != "" {
		var err error
		if cover, _, err = fetch.Get(ctx, s.nativeFetchHTTP, in.Cover, s.cfg.UserAgent, "", 16<<20); err != nil {
			writeErr(w, 502, "cover: "+err.Error())
			return
		}
	}
	updated := it.UpdatedAt
	if len(in.Metadata) > 0 {
		if err := s.catalog().Edit(ctx, uid, it.ID, library.EditOptions{Expected: in.Expected, Metadata: in.Metadata}); err != nil {
			s.nativeError(w, err)
			return
		}
	}
	if cover != nil {
		var err error
		if updated, err = s.nativeSaveCover(ctx, uid, it.ID, cover); err != nil {
			s.nativeError(w, err)
			return
		}
	} else if fresh, err := s.catalog().Item(ctx, uid, it.ID); err == nil {
		updated = fresh.UpdatedAt
	}
	writeJSON(w, 200, nativeObject{"ok": true, "updated_at": updated})
}

// matchResult is one book's quick-match outcome.
type matchResult struct {
	ItemID    string   `json:"item_id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"` // matched, skipped, low_confidence, not_found, failed
	Message   string   `json:"message,omitempty"`
	Provider  string   `json:"provider,omitempty"`
	MatchID   string   `json:"match_id,omitempty"`
	MatchName string   `json:"match_title,omitempty"`
	MatchBy   string   `json:"match_author,omitempty"`
	Score     float64  `json:"score,omitempty"`
	Exact     bool     `json:"exact,omitempty"`
	Fields    []string `json:"fields,omitempty"`
	Cover     bool     `json:"cover,omitempty"`
}

type quickMatchInput struct {
	Provider string `json:"provider"`
	library.MatchOptions
}

func (s *Server) quickMatchOne(ctx context.Context, uid int64, id string, providers []metadata.Provider, o library.MatchOptions) matchResult {
	res := matchResult{ItemID: id}
	it, err := s.catalog().Item(ctx, uid, id)
	if err != nil {
		res.Status, res.Message = "failed", "Book is no longer available"
		return res
	}
	res.Title = it.Title
	if it.MediaType != "book" {
		res.Status, res.Message = "skipped", "Not an audiobook"
		return res
	}
	cur, hasCover := library.CurrentMetadata(it)
	q := matchQuery(it, cur)
	var best *metadata.Ranked
	var bestProvider metadata.Provider
	errs := []string{}
	for _, p := range providers {
		ranked, err := s.matchCandidates(ctx, p, q, metadata.CleanTitle(q.Title), firstAuthor(q))
		if ctx.Err() != nil {
			res.Status, res.Message = "failed", "Cancelled"
			return res
		}
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if len(ranked) > 0 && (best == nil || ranked[0].Score.Total > best.Score.Total || (ranked[0].Score.Confident && !best.Score.Confident)) {
			best, bestProvider = &ranked[0], p
		}
		if best != nil && best.Score.Confident {
			break
		}
	}
	if best == nil {
		if len(errs) > 0 {
			res.Status, res.Message = "failed", strings.Join(errs, "; ")
		} else {
			res.Status, res.Message = "not_found", "No results"
		}
		return res
	}
	res.Provider, res.MatchID, res.MatchName, res.MatchBy = bestProvider.ID, best.ID, best.Title, strings.Join(best.Authors, ", ")
	res.Score, res.Exact = math.Round(best.Score.Total*100)/100, best.Score.Exact
	if !best.Score.Confident {
		res.Status, res.Message = "low_confidence", "Best result is not a confident match"
		return res
	}
	cand := s.matchDetail(ctx, bestProvider, best.Candidate)
	if ctx.Err() != nil {
		res.Status, res.Message = "failed", "Cancelled"
		return res
	}
	// The detail record can reveal a conflicting identifier (e.g. ISBN).
	if sc := metadata.ScoreCandidate(q, cand); !best.Score.Exact && !sc.Confident {
		res.Status, res.Message = "low_confidence", "Details did not confirm the match"
		return res
	}
	proposed := cand.Metadata
	if bestProvider.Kind == "openlibrary" || bestProvider.Kind == "google" {
		// Book catalogues describe the print edition: its publisher is not the
		// audiobook's, so quick match leaves that field for audio providers.
		proposed = maps.Clone(proposed)
		delete(proposed, "publisher")
	}
	fields, keys := library.QuickMatchFields(cur, proposed, o)
	var cover []byte
	if cand.Cover != "" && (!hasCover || o.OverrideCover) {
		if data, _, err := fetch.Get(ctx, s.nativeFetchHTTP, cand.Cover, s.cfg.UserAgent, "", 16<<20); err == nil {
			cover = data
		} else {
			res.Message = "Cover could not be downloaded"
		}
	}
	if len(fields) == 0 && cover == nil {
		res.Status = "skipped"
		if res.Message == "" {
			res.Message = "Nothing to fill in"
		}
		return res
	}
	if len(fields) > 0 {
		if err = s.catalog().Edit(ctx, uid, it.ID, library.EditOptions{Expected: it.UpdatedAt, Metadata: fields}); err != nil {
			res.Status, res.Message = "failed", err.Error()
			if errors.Is(err, library.ErrConflict) {
				res.Message = "Book changed while matching; try again"
			}
			return res
		}
	}
	if cover != nil {
		if _, err = s.nativeSaveCover(ctx, uid, it.ID, cover); err == nil {
			res.Cover = true
		} else {
			res.Message = "Cover was not saved: " + err.Error()
		}
	}
	res.Status, res.Fields = "matched", keys
	return res
}

func (s *Server) nativeQuickMatch(w http.ResponseWriter, r *http.Request) {
	it, ok := s.matchEditor(w, r)
	if !ok {
		return
	}
	var in quickMatchInput
	if r.ContentLength != 0 && decode(r, &in) != nil {
		writeErr(w, 400, "invalid quick match options")
		return
	}
	providers, err := s.matchProviders(r.Context(), in.Provider)
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			s.nativeError(w, err)
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, s.quickMatchOne(r.Context(), actorID(r.Context()), it.ID, providers, in.MatchOptions))
}

// nativeAudibleChapters proposes Audible chapter markers for the chapter
// editor. Chapters are clamped to the book; a runtime more than two minutes
// off is reported so the editor can warn before replacing chapters.
func (s *Server) nativeAudibleChapters(w http.ResponseWriter, r *http.Request) {
	it, ok := s.matchEditor(w, r)
	if !ok {
		return
	}
	cur, _ := library.CurrentMetadata(it)
	asin := strings.TrimSpace(r.URL.Query().Get("asin"))
	if asin == "" {
		asin = nativeString(cur["asin"])
	}
	if asin == "" {
		writeErr(w, 400, "enter the book's ASIN")
		return
	}
	region := r.URL.Query().Get("region")
	if !metadata.ValidRegion(region) {
		region = ""
		if p, err := s.nativeProvider(r.Context(), "audible"); err == nil {
			region = p.Region
		}
	}
	ch, err := s.nativeMetadataClient().AudibleChapterList(r.Context(), region, asin)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	chapters := []library.Chapter{}
	for _, c := range ch.Chapters {
		if it.Duration > 0 && c.Start >= it.Duration {
			break
		}
		end := c.End
		if it.Duration > 0 && end > it.Duration {
			end = it.Duration
		}
		chapters = append(chapters, library.Chapter{ID: len(chapters), Title: c.Title, Start: c.Start, End: end})
	}
	diff := ch.Runtime - it.Duration
	writeJSON(w, 200, nativeObject{"asin": ch.ASIN, "region": ch.Region, "accurate": ch.Accurate, "runtime": ch.Runtime, "duration": it.Duration, "difference": diff, "mismatch": it.Duration > 0 && math.Abs(diff) > 120, "chapters": chapters})
}

type matchJob struct {
	ID        string               `json:"id"`
	LibraryID string               `json:"library_id"`
	Provider  string               `json:"provider"`
	Options   library.MatchOptions `json:"options"`
	Status    string               `json:"status"`
	Total     int                  `json:"total"`
	Done      int                  `json:"done"`
	Error     string               `json:"error"`
	Created   int64                `json:"created_at"`
	Updated   int64                `json:"updated_at"`
	Counts    map[string]int       `json:"counts"`
}

const matchJobColumns = `id,library_id,provider,options,status,total,done,error,created_at,updated_at`

func (s *Server) loadMatchJob(ctx context.Context, row interface{ Scan(...any) error }) (matchJob, error) {
	var j matchJob
	var options string
	if err := row.Scan(&j.ID, &j.LibraryID, &j.Provider, &options, &j.Status, &j.Total, &j.Done, &j.Error, &j.Created, &j.Updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = library.ErrNotFound
		}
		return j, err
	}
	_ = json.Unmarshal([]byte(options), &j.Options)
	if j.Status == "queued" || j.Status == "running" {
		matchRuns.Lock()
		_, alive := matchRuns.cancel[j.ID]
		matchRuns.Unlock()
		if !alive {
			j.Status, j.Error = "failed", "Interrupted by server restart"
			_, _ = s.store.DB.ExecContext(ctx, `UPDATE catalog_match_jobs SET status='failed',error=?,updated_at=? WHERE id=? AND status IN ('queued','running')`, j.Error, time.Now().UnixMilli(), j.ID)
		}
	}
	j.Counts = map[string]int{}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT status,count(*) FROM catalog_match_results WHERE job_id=? GROUP BY status`, j.ID)
	if err != nil {
		return j, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err = rows.Scan(&st, &n); err != nil {
			return j, err
		}
		j.Counts[st] = n
	}
	return j, rows.Err()
}

func (s *Server) nativeMatchJobs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	a, err := s.catalog().Account(ctx, uid)
	if err != nil || !a.Permissions.Update {
		writeErr(w, 403, "metadata editing permission required")
		return
	}
	if r.Method == "GET" {
		lib := r.URL.Query().Get("library")
		rows, err := s.store.DB.QueryContext(ctx, `SELECT id FROM catalog_match_jobs WHERE user_id=? AND provider<>? AND (?='' OR library_id=?) ORDER BY created_at DESC LIMIT 10`, uid, authorJobProvider, lib, lib)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		jobs := []matchJob{}
		for _, id := range ids {
			j, err := s.loadMatchJob(ctx, s.store.DB.QueryRowContext(ctx, `SELECT `+matchJobColumns+` FROM catalog_match_jobs WHERE id=?`, id))
			if err != nil {
				s.nativeError(w, err)
				return
			}
			jobs = append(jobs, j)
		}
		writeJSON(w, 200, jobs)
		return
	}
	var in struct {
		quickMatchInput
		LibraryID string   `json:"library_id"`
		IDs       []string `json:"ids"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "invalid quick match job")
		return
	}
	targets, err := s.catalog().MatchTargets(ctx, uid, in.LibraryID, in.IDs)
	if err != nil {
		if errors.Is(err, library.ErrInvalid) {
			writeErr(w, 400, "choose a book library or select up to 1000 books")
			return
		}
		s.nativeError(w, err)
		return
	}
	if len(targets) == 0 {
		writeErr(w, 400, "there are no books to match")
		return
	}
	providers, err := s.matchProviders(ctx, in.Provider)
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			s.nativeError(w, err)
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	// One bulk job per person keeps provider traffic polite.
	var active string
	if e := s.store.DB.QueryRowContext(ctx, `SELECT id FROM catalog_match_jobs WHERE user_id=? AND status IN ('queued','running') LIMIT 1`, uid).Scan(&active); e == nil {
		if j, e := s.loadMatchJob(ctx, s.store.DB.QueryRowContext(ctx, `SELECT `+matchJobColumns+` FROM catalog_match_jobs WHERE id=?`, active)); e == nil && (j.Status == "queued" || j.Status == "running") {
			writeErr(w, 409, "a quick match is already running")
			return
		}
	}
	id := "match-" + rand.Text()
	now := time.Now().UnixMilli()
	if _, err = s.store.DB.ExecContext(ctx, `INSERT INTO catalog_match_jobs(id,user_id,library_id,provider,options,items,status,total,created_at,updated_at) VALUES(?,?,?,?,?,?,'running',?,?,?)`, id, uid, in.LibraryID, in.Provider, string(nativeJSON(in.MatchOptions)), string(nativeJSON(targets)), len(targets), now, now); err != nil {
		s.nativeError(w, err)
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	matchRuns.Lock()
	matchRuns.cancel[id] = cancel
	matchRuns.Unlock()
	go s.runMatchJob(runCtx, cancel, id, uid, targets, providers, in.MatchOptions)
	writeJSON(w, 202, nativeObject{"id": id, "total": len(targets)})
}

func (s *Server) runMatchJob(ctx context.Context, cancel context.CancelFunc, id string, uid int64, targets []string, providers []metadata.Provider, o library.MatchOptions) {
	defer func() {
		matchRuns.Lock()
		delete(matchRuns.cancel, id)
		matchRuns.Unlock()
		cancel()
	}()
	db := s.store.DB
	status := "complete"
	for i, item := range targets {
		if ctx.Err() != nil {
			status = "cancelled"
			break
		}
		res := s.quickMatchOne(ctx, uid, item, providers, o)
		if ctx.Err() != nil {
			// The interrupted book was not changed; do not report it.
			status = "cancelled"
			break
		}
		now := time.Now().UnixMilli()
		if _, err := db.Exec(`INSERT INTO catalog_match_results VALUES(?,?,?,?,?)`, id, i, item, res.Status, string(nativeJSON(res))); err != nil {
			status = "failed"
			_, _ = db.Exec(`UPDATE catalog_match_jobs SET error=? WHERE id=?`, err.Error(), id)
			break
		}
		_, _ = db.Exec(`UPDATE catalog_match_jobs SET done=?,updated_at=? WHERE id=? AND status='running'`, i+1, now, id)
	}
	_, _ = db.Exec(`UPDATE catalog_match_jobs SET status=?,updated_at=? WHERE id=? AND status='running'`, status, time.Now().UnixMilli(), id)
	if s.log != nil {
		s.log.Info("quick match finished", "job", id, "status", status)
	}
}

func (s *Server) nativeMatchJob(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	id := chi.URLParam(r, "job")
	if !matchJobID.MatchString(id) {
		writeErr(w, 404, "not found")
		return
	}
	j, err := s.loadMatchJob(ctx, s.store.DB.QueryRowContext(ctx, `SELECT `+matchJobColumns+` FROM catalog_match_jobs WHERE id=? AND user_id=?`, id, uid))
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if r.Method == "DELETE" {
		if j.Status == "running" || j.Status == "queued" {
			_, err = s.store.DB.ExecContext(ctx, `UPDATE catalog_match_jobs SET status='cancelled',updated_at=? WHERE id=? AND status IN ('queued','running')`, time.Now().UnixMilli(), id)
			matchRuns.Lock()
			if cancel := matchRuns.cancel[id]; cancel != nil {
				cancel()
			}
			matchRuns.Unlock()
		} else {
			_, err = s.store.DB.ExecContext(ctx, `DELETE FROM catalog_match_jobs WHERE id=? AND user_id=?`, id, uid)
		}
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"ok": true})
		return
	}
	filter := r.URL.Query().Get("status")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT result FROM catalog_match_results WHERE job_id=? AND (?='' OR status=?) ORDER BY position LIMIT ? OFFSET ?`, id, filter, filter, limit, max(0, offset))
	if err != nil {
		s.nativeError(w, err)
		return
	}
	defer rows.Close()
	results := []json.RawMessage{}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			s.nativeError(w, err)
			return
		}
		results = append(results, json.RawMessage(v))
	}
	writeJSON(w, 200, nativeObject{"job": j, "results": results})
}
