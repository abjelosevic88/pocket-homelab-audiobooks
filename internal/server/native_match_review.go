package server

// Match review: a person walks the uncertain results of a bulk quick match
// (low confidence, no result) one book at a time, re-searches the provider,
// and accepts a candidate through the regular apply endpoint or skips the
// book. The review state is kept per result so the queue can be resumed.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/metadata"
	"github.com/go-chi/chi/v5"
)

// reviewStatuses are the quick-match outcomes that need a person.
var reviewStatuses = []string{"low_confidence", "not_found"}

func (s *Server) nativeMatchReviewRoutes(r chi.Router) {
	r.Get("/catalog/match-jobs/{job}/review", s.nativeMatchReview)
	r.Put("/catalog/match-jobs/{job}/review/{position}", s.nativeMatchReviewSet)
}

// reviewCandidate is a ranked candidate plus its edition, so the review can
// show "Dramatized" or "Reading" next to the book's own edition.
type reviewCandidate struct {
	metadata.Ranked
	Dramatized bool `json:"dramatized"`
}

// detailCandidates enriches the first n candidates (at most 3) with the
// provider's detail record and scores them again, so narrators, series and
// runtime are known before a person accepts one. Requests go through the
// shared Audible limiter like every other provider call.
func (s *Server) detailCandidates(ctx context.Context, p metadata.Provider, q metadata.MatchQuery, ranked []metadata.Ranked, n int) []reviewCandidate {
	n = min(max(n, 0), 3, len(ranked))
	for i := 0; i < n && ctx.Err() == nil; i++ {
		cand := s.matchDetail(ctx, p, ranked[i].Candidate)
		ranked[i] = metadata.Ranked{Candidate: cand, Score: metadata.ScoreCandidate(q, cand)}
	}
	top := ranked[:n]
	sort.SliceStable(top, func(i, j int) bool {
		if top[i].Score.Exact != top[j].Score.Exact {
			return top[i].Score.Exact
		}
		return top[i].Score.Total > top[j].Score.Total
	})
	out := make([]reviewCandidate, 0, len(ranked))
	for _, c := range ranked {
		out = append(out, reviewCandidate{Ranked: c, Dramatized: metadata.CandidateDramatized(c.Candidate)})
	}
	return out
}

// itemFolder is the book's folder as the library shows it.
func itemFolder(it library.Item) string {
	var doc struct {
		Item struct {
			Path    string `json:"path"`
			RelPath string `json:"relPath"`
		} `json:"item"`
	}
	_ = json.Unmarshal(it.Document, &doc)
	if doc.Item.Path != "" {
		return doc.Item.Path
	}
	return doc.Item.RelPath
}

// reviewJob checks native mode and edit permission and loads one of the
// person's own book quick-match jobs.
func (s *Server) reviewJob(w http.ResponseWriter, r *http.Request) (matchJob, bool) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return matchJob{}, false
	}
	ctx := r.Context()
	uid := actorID(ctx)
	a, err := s.catalog().Account(ctx, uid)
	if err != nil || !a.Permissions.Update {
		writeErr(w, 403, "metadata editing permission required")
		return matchJob{}, false
	}
	id := chi.URLParam(r, "job")
	if !matchJobID.MatchString(id) {
		writeErr(w, 404, "not found")
		return matchJob{}, false
	}
	j, err := s.loadMatchJob(ctx, s.store.DB.QueryRowContext(ctx, `SELECT `+matchJobColumns+` FROM catalog_match_jobs WHERE id=? AND user_id=? AND provider<>?`, id, uid, authorJobProvider))
	if err != nil {
		s.nativeError(w, err)
		return j, false
	}
	return j, true
}

// reviewFilter turns ?status= into the result statuses to list.
func reviewFilter(v string) ([]string, bool) {
	switch v {
	case "", "all":
		return reviewStatuses, true
	case "low_confidence", "not_found":
		return []string{v}, true
	}
	return nil, false
}

// nativeMatchReview lists a job's uncertain results with their review state.
func (s *Server) nativeMatchReview(w http.ResponseWriter, r *http.Request) {
	j, ok := s.reviewJob(w, r)
	if !ok {
		return
	}
	statuses, ok := reviewFilter(r.URL.Query().Get("status"))
	if !ok {
		writeErr(w, 400, "status must be low_confidence or not_found")
		return
	}
	ctx := r.Context()
	rows, err := s.store.DB.QueryContext(ctx, `SELECT m.position,m.status,m.result,coalesce(v.state,''),coalesce(v.updated_at,0)
 FROM catalog_match_results m LEFT JOIN catalog_match_reviews v ON v.job_id=m.job_id AND v.position=m.position
 WHERE m.job_id=? AND m.status IN ('low_confidence','not_found') ORDER BY m.position`, j.ID)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	defer rows.Close()
	entries := []nativeObject{}
	counts := map[string]int{"total": 0, "reviewed": 0, "accepted": 0, "skipped": 0, "pending": 0}
	byStatus := map[string]int{}
	for rows.Next() {
		var pos int
		var at int64
		var status, raw, state string
		if err = rows.Scan(&pos, &status, &raw, &state, &at); err != nil {
			s.nativeError(w, err)
			return
		}
		byStatus[status]++
		if !containsString(statuses, status) {
			continue
		}
		e := nativeObject{}
		_ = json.Unmarshal([]byte(raw), &e)
		e["position"], e["status"], e["review"], e["reviewed_at"] = pos, status, state, at
		entries = append(entries, e)
		counts["total"]++
		if state == "" {
			counts["pending"]++
		} else {
			counts["reviewed"]++
			counts[state]++
		}
	}
	if err = rows.Err(); err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, nativeObject{"job": j, "entries": entries, "counts": counts, "statuses": byStatus})
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// nativeMatchReviewSet records a review decision for one result: accepted,
// skipped, or pending again (undo).
func (s *Server) nativeMatchReviewSet(w http.ResponseWriter, r *http.Request) {
	j, ok := s.reviewJob(w, r)
	if !ok {
		return
	}
	pos, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil || pos < 0 {
		writeErr(w, 404, "not found")
		return
	}
	var in struct {
		State string `json:"state"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "invalid review")
		return
	}
	in.State = strings.TrimSpace(in.State)
	if in.State != "accepted" && in.State != "skipped" && in.State != "pending" {
		writeErr(w, 400, "state must be accepted, skipped or pending")
		return
	}
	ctx := r.Context()
	var status, item string
	if err = s.store.DB.QueryRowContext(ctx, `SELECT status,item_id FROM catalog_match_results WHERE job_id=? AND position=?`, j.ID, pos).Scan(&status, &item); err != nil || !containsString(reviewStatuses, status) {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.nativeError(w, err)
			return
		}
		writeErr(w, 404, "this result does not need review")
		return
	}
	now := time.Now().UnixMilli()
	if in.State == "pending" {
		_, err = s.store.DB.ExecContext(ctx, `DELETE FROM catalog_match_reviews WHERE job_id=? AND position=?`, j.ID, pos)
		now = 0
	} else {
		_, err = s.store.DB.ExecContext(ctx, `INSERT INTO catalog_match_reviews(job_id,position,state,updated_at) VALUES(?,?,?,?)
 ON CONFLICT(job_id,position) DO UPDATE SET state=excluded.state,updated_at=excluded.updated_at`, j.ID, pos, in.State, now)
	}
	if err != nil {
		s.nativeError(w, err)
		return
	}
	state := in.State
	if state == "pending" {
		state = ""
	}
	writeJSON(w, 200, nativeObject{"position": pos, "item_id": item, "review": state, "reviewed_at": now})
}
