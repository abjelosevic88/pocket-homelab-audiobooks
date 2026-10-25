package server

// Author profiles, like Audiobookshelf's author match: Audnexus supplies an
// author's ASIN, biography and photo (by ASIN, or by the closest name), one
// author at a time or as a background job over a library. Photos are stored
// as immutable content-addressed native artwork, the same way as covers;
// Audiobookshelf-imported photos (read-only metadata mounts) keep working.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/metadata"
	"github.com/go-chi/chi/v5"
)

// authorJobProvider marks author jobs in catalog_match_jobs.
const authorJobProvider = "authors"

// maxAuthorImage caps author photo uploads and downloads.
const maxAuthorImage = 10 << 20

var authorJobID = regexp.MustCompile(`^authors-[A-Za-z0-9]{26}$`)

// nativeAuthorRoutes registers JSON author endpoints (authenticated group).
func (s *Server) nativeAuthorRoutes(r chi.Router) {
	r.Get("/catalog/authors/{id}", s.nativeAuthor)
	r.Put("/catalog/authors/{id}", s.nativeAuthor)
	r.Post("/catalog/authors/{id}/match", s.nativeAuthorMatch)
	r.Get("/catalog/author-match-jobs", s.nativeAuthorJobs)
	r.Post("/catalog/author-match-jobs", s.nativeAuthorJobs)
	r.Get("/catalog/author-match-jobs/{job}", s.nativeAuthorJob)
	r.Delete("/catalog/author-match-jobs/{job}", s.nativeAuthorJob)
}

// nativeAuthorMediaRoutes registers the photo endpoints (no timeout/compression group).
func (s *Server) nativeAuthorMediaRoutes(r chi.Router) {
	r.Get("/catalog/authors/{id}/image", s.nativeAuthorImage)
	r.Head("/catalog/authors/{id}/image", s.nativeAuthorImage)
	r.Post("/catalog/authors/{id}/image", s.nativeAuthorImageUpload)
	r.Delete("/catalog/authors/{id}/image", s.nativeAuthorImageUpload)
}

func authorWire(a library.Author, canEdit bool) nativeObject {
	img := ""
	if a.ImagePath != "" {
		img = "/api/v1/catalog/authors/" + a.ID + "/image?v=" + strconv.FormatInt(a.UpdatedAt, 10)
	}
	return nativeObject{"id": a.ID, "name": a.Name, "asin": a.ASIN, "description": a.Description, "image": img, "has_image": a.ImagePath != "", "last_matched": a.LastMatched, "updated_at": a.UpdatedAt, "books": a.Books, "can_edit": canEdit}
}

func (s *Server) authorEditor(ctx context.Context) bool {
	a, err := s.catalog().Account(ctx, actorID(ctx))
	return err == nil && a.Permissions.Update
}

// authorRegion is the Audible provider's region setting (enabled or not).
func (s *Server) authorRegion(ctx context.Context) string {
	var region string
	_ = s.store.DB.QueryRowContext(ctx, `SELECT region FROM catalog_metadata_providers WHERE id='audible'`).Scan(&region)
	if metadata.ValidRegion(region) {
		return region
	}
	return metadata.DefaultAudibleRegion()
}

func (s *Server) nativeAuthor(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	id := chi.URLParam(r, "id")
	if r.Method == "GET" {
		a, err := s.catalog().Author(ctx, uid, id)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, authorWire(a, s.authorEditor(ctx)))
		return
	}
	if !s.authorEditor(ctx) {
		writeErr(w, 403, "metadata editing permission required")
		return
	}
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		ASIN        *string `json:"asin"`
		RemoveImage bool    `json:"remove_image"`
		ImageURL    string  `json:"image_url"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "invalid author")
		return
	}
	edit := library.AuthorEdit{Name: in.Name, Description: in.Description, ASIN: in.ASIN}
	if in.RemoveImage {
		empty := ""
		edit.ImagePath = &empty
	} else if strings.TrimSpace(in.ImageURL) != "" {
		if _, err := s.catalog().Author(ctx, uid, id); err != nil {
			s.nativeError(w, err)
			return
		}
		data, _, err := fetch.Get(ctx, s.nativeFetchHTTP, in.ImageURL, s.cfg.UserAgent, "", maxAuthorImage)
		if err != nil {
			writeErr(w, 502, "photo: "+err.Error())
			return
		}
		path, err := s.storeNativeArt(ctx, data, maxAuthorImage)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		edit.ImagePath = &path
	}
	a, err := s.catalog().EditAuthor(ctx, uid, id, edit)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, authorWire(a, true))
}

// nativeAuthorImage serves an author's photo to people who can see the author.
func (s *Server) nativeAuthorImage(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	a, err := s.catalog().Author(r.Context(), actorID(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if a.ImagePath == "" {
		writeErr(w, 404, "image not found")
		return
	}
	if r.URL.Query().Get("v") != "" {
		// Versioned by the author's updated time: a new photo gets a new URL.
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=300")
	}
	if strings.HasPrefix(a.ImagePath, "/pocket-native-art/") {
		w.Header().Set("ETag", `"`+strings.TrimSuffix(filepath.Base(a.ImagePath), filepath.Ext(a.ImagePath))+`"`)
	}
	s.nativeImage(w, r, a.ImagePath)
}

// nativeAuthorImageUpload replaces (POST multipart "file") or removes (DELETE) a photo.
func (s *Server) nativeAuthorImageUpload(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	id := chi.URLParam(r, "id")
	if !s.authorEditor(ctx) {
		writeErr(w, 403, "metadata editing permission required")
		return
	}
	if _, err := s.catalog().Author(ctx, uid, id); err != nil {
		s.nativeError(w, err)
		return
	}
	path := ""
	if r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthorImage+(1<<20))
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			writeErr(w, 400, "invalid photo upload (up to 10 MB)")
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, err := r.FormFile("file")
		if err != nil {
			writeErr(w, 400, "choose a photo")
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxAuthorImage+1))
		if err != nil {
			writeErr(w, 400, "invalid image")
			return
		}
		if path, err = s.storeNativeArt(ctx, data, maxAuthorImage); err != nil {
			s.nativeError(w, err)
			return
		}
	}
	a, err := s.catalog().EditAuthor(ctx, uid, id, library.AuthorEdit{ImagePath: &path})
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, authorWire(a, true))
}

// artFormat validates artwork bytes: JPEG, PNG or WebP under 40 megapixels.
func artFormat(data []byte) (string, error) {
	bad := fmt.Errorf("%w: use a JPEG, PNG or WebP image under 40 megapixels", library.ErrInvalid)
	width, height, format := 0, 0, ""
	if cfg, f, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && (f == "jpeg" || f == "png") {
		width, height, format = cfg.Width, cfg.Height, f
	} else if w, h, ok := webpSize(data); ok {
		width, height, format = w, h, "webp"
	} else {
		return "", bad
	}
	if width <= 0 || height <= 0 || int64(width)*int64(height) > 40000000 {
		return "", bad
	}
	return format, nil
}

// webpSize reads a WebP image's canvas size from its RIFF header.
func webpSize(b []byte) (int, int, bool) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, false
	}
	switch string(b[12:16]) {
	case "VP8 ": // lossy: key frame start code, then 14-bit sizes
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), true
	case "VP8L": // lossless: signature, then 14-bit (size-1) fields
		if b[20] != 0x2f {
			return 0, 0, false
		}
		v := binary.LittleEndian.Uint32(b[21:25])
		return int(v&0x3fff) + 1, int((v>>14)&0x3fff) + 1, true
	case "VP8X": // extended: 24-bit (size-1) canvas
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1, true
	}
	return 0, 0, false
}

// storeNativeArt validates and stores artwork content-addressed under the
// data directory and returns its catalog path ("/pocket-native-art/<sha256>.<ext>").
func (s *Server) storeNativeArt(ctx context.Context, data []byte, limit int) (string, error) {
	if len(data) == 0 || len(data) > limit {
		return "", fmt.Errorf("%w: images can be up to %d MB", library.ErrInvalid, limit>>20)
	}
	format, err := artFormat(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + "." + format
	dir := filepath.Join(s.cfg.DataDir, "native-art")
	if err = os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err == nil {
		_, err = out.Write(data)
		if err == nil {
			err = out.Sync()
		}
		if e := out.Close(); err == nil {
			err = e
		}
		if err != nil {
			_ = root.Remove(name)
			return "", err
		}
	} else if !os.IsExist(err) {
		return "", err
	}
	if _, err = s.store.DB.ExecContext(ctx, `INSERT INTO catalog_roots VALUES('native-art','/pocket-native-art',?) ON CONFLICT(id) DO UPDATE SET local_path=excluded.local_path`, dir); err != nil {
		return "", err
	}
	return "/pocket-native-art/" + name, nil
}

// authorMatchResult is one author's match outcome.
type authorMatchResult struct {
	AuthorID  string   `json:"author_id"`
	Name      string   `json:"name"`
	Status    string   `json:"status"` // matched, skipped, not_found, failed
	Message   string   `json:"message,omitempty"`
	ASIN      string   `json:"asin,omitempty"`
	MatchName string   `json:"match_name,omitempty"`
	Distance  int      `json:"distance,omitempty"`
	Fields    []string `json:"fields,omitempty"`
	Image     bool     `json:"image,omitempty"`
}

// matchAuthorOne looks an author up on Audnexus and saves the profile. By ASIN
// when given (or already known), otherwise by the closest name. fillOnly keeps
// an existing bio and photo, as a bulk match does by default.
func (s *Server) matchAuthorOne(ctx context.Context, uid int64, id, region, asin, name string, fillOnly bool) authorMatchResult {
	res := authorMatchResult{AuthorID: id}
	cur, err := s.catalog().Author(ctx, uid, id)
	if err != nil {
		res.Status, res.Message = "failed", "Author is no longer available"
		return res
	}
	res.Name = cur.Name
	c := s.nativeMetadataClient()
	asin = strings.ToUpper(strings.TrimSpace(asin))
	name = strings.TrimSpace(name)
	if name == "" {
		name = cur.Name
	}
	var p metadata.AuthorProfile
	switch {
	case asin != "":
		p, err = c.AudnexusAuthor(ctx, region, asin)
	case cur.ASIN != "" && name == cur.Name:
		if p, err = c.AudnexusAuthor(ctx, region, cur.ASIN); errors.Is(err, metadata.ErrNoAuthor) {
			p, err = c.FindAudnexusAuthor(ctx, region, name)
		}
	default:
		p, err = c.FindAudnexusAuthor(ctx, region, name)
	}
	if ctx.Err() != nil {
		res.Status, res.Message = "failed", "Cancelled"
		return res
	}
	if errors.Is(err, metadata.ErrNoAuthor) {
		res.Status, res.Message = "not_found", "No matching author on Audible"
		return res
	}
	if err != nil {
		res.Status, res.Message = "failed", err.Error()
		return res
	}
	res.ASIN, res.MatchName, res.Distance = p.ASIN, p.Name, p.Distance
	edit := library.AuthorEdit{Matched: true}
	if p.ASIN != cur.ASIN && (cur.ASIN == "" || !fillOnly || asin != "") {
		edit.ASIN = &p.ASIN
		res.Fields = append(res.Fields, "asin")
	}
	if p.Description != "" && p.Description != cur.Description && (!fillOnly || strings.TrimSpace(cur.Description) == "") {
		edit.Description = &p.Description
		res.Fields = append(res.Fields, "description")
	}
	if p.Image != "" && (!fillOnly || cur.ImagePath == "") {
		data, _, err := fetch.Get(ctx, s.nativeFetchHTTP, p.Image, s.cfg.UserAgent, "", maxAuthorImage)
		if err == nil {
			var path string
			if path, err = s.storeNativeArt(ctx, data, maxAuthorImage); err == nil && path != cur.ImagePath {
				edit.ImagePath = &path
				res.Image = true
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				res.Status, res.Message = "failed", "Cancelled"
				return res
			}
			res.Message = "Photo could not be downloaded"
		}
	}
	if len(res.Fields) == 0 && !res.Image {
		res.Status = "skipped"
		if res.Message == "" {
			res.Message = "Nothing to fill in"
		}
		return res
	}
	if _, err = s.catalog().EditAuthor(ctx, uid, id, edit); err != nil {
		res.Status, res.Message, res.Fields, res.Image = "failed", err.Error(), nil, false
		return res
	}
	res.Status = "matched"
	return res
}

// nativeAuthorMatch matches one author: {asin?, name?}.
func (s *Server) nativeAuthorMatch(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	if !s.authorEditor(ctx) {
		writeErr(w, 403, "metadata editing permission required")
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.catalog().Author(ctx, uid, id); err != nil {
		s.nativeError(w, err)
		return
	}
	var in struct {
		ASIN string `json:"asin"`
		Name string `json:"name"`
	}
	if r.ContentLength != 0 && decode(r, &in) != nil {
		writeErr(w, 400, "invalid author match")
		return
	}
	if len(in.Name) > 500 {
		writeErr(w, 400, "search text is too long")
		return
	}
	if in.ASIN = strings.ToUpper(strings.TrimSpace(in.ASIN)); in.ASIN != "" && !metadata.LooksLikeASIN(in.ASIN) {
		writeErr(w, 400, "an ASIN has ten letters and digits")
		return
	}
	res := s.matchAuthorOne(ctx, uid, id, s.authorRegion(ctx), in.ASIN, in.Name, false)
	out := nativeObject{"result": res}
	if a, err := s.catalog().Author(ctx, uid, id); err == nil {
		out["author"] = authorWire(a, true)
	}
	status := 200
	if res.Status == "failed" {
		status = 502
		out["error"] = res.Message
	}
	writeJSON(w, status, out)
}

func (s *Server) nativeAuthorJobs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	if !s.authorEditor(ctx) {
		writeErr(w, 403, "metadata editing permission required")
		return
	}
	if r.Method == "GET" {
		lib := r.URL.Query().Get("library")
		rows, err := s.store.DB.QueryContext(ctx, `SELECT id FROM catalog_match_jobs WHERE user_id=? AND provider=? AND (?='' OR library_id=?) ORDER BY created_at DESC LIMIT 10`, uid, authorJobProvider, lib, lib)
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
		jobs := []nativeObject{}
		for _, id := range ids {
			j, err := s.loadAuthorJob(ctx, id, uid)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			jobs = append(jobs, j)
		}
		writeJSON(w, 200, jobs)
		return
	}
	in := struct {
		LibraryID   string `json:"library_id"`
		OnlyMissing *bool  `json:"only_missing"`
	}{}
	if decode(r, &in) != nil {
		writeErr(w, 400, "invalid author match job")
		return
	}
	onlyMissing := in.OnlyMissing == nil || *in.OnlyMissing
	targets, err := s.catalog().AuthorTargets(ctx, uid, in.LibraryID, onlyMissing)
	if err != nil {
		if errors.Is(err, library.ErrInvalid) {
			writeErr(w, 400, "choose a book library")
			return
		}
		s.nativeError(w, err)
		return
	}
	if len(targets) == 0 {
		msg := "there are no authors to match"
		if onlyMissing {
			msg = "every author already has a photo and a biography"
		}
		writeErr(w, 400, msg)
		return
	}
	// Shares the one-bulk-job-per-person rule with book quick match.
	var active string
	if e := s.store.DB.QueryRowContext(ctx, `SELECT id FROM catalog_match_jobs WHERE user_id=? AND status IN ('queued','running') LIMIT 1`, uid).Scan(&active); e == nil {
		matchRuns.Lock()
		_, alive := matchRuns.cancel[active]
		matchRuns.Unlock()
		if alive {
			writeErr(w, 409, "a match is already running")
			return
		}
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE catalog_match_jobs SET status='failed',error='Interrupted by server restart',updated_at=? WHERE id=? AND status IN ('queued','running')`, time.Now().UnixMilli(), active)
	}
	id := "authors-" + rand.Text()
	now := time.Now().UnixMilli()
	options := nativeObject{"only_missing": onlyMissing}
	if _, err = s.store.DB.ExecContext(ctx, `INSERT INTO catalog_match_jobs(id,user_id,library_id,provider,options,items,status,total,created_at,updated_at) VALUES(?,?,?,?,?,?,'running',?,?,?)`, id, uid, in.LibraryID, authorJobProvider, string(nativeJSON(options)), string(nativeJSON(targets)), len(targets), now, now); err != nil {
		s.nativeError(w, err)
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	matchRuns.Lock()
	matchRuns.cancel[id] = cancel
	matchRuns.Unlock()
	go s.runAuthorJob(runCtx, cancel, id, uid, targets, s.authorRegion(ctx), onlyMissing)
	writeJSON(w, 202, nativeObject{"id": id, "total": len(targets)})
}

func (s *Server) runAuthorJob(ctx context.Context, cancel context.CancelFunc, id string, uid int64, targets []string, region string, fillOnly bool) {
	defer func() {
		matchRuns.Lock()
		delete(matchRuns.cancel, id)
		matchRuns.Unlock()
		cancel()
	}()
	db := s.store.DB
	status := "complete"
	for i, author := range targets {
		if ctx.Err() != nil {
			status = "cancelled"
			break
		}
		res := s.matchAuthorOne(ctx, uid, author, region, "", "", fillOnly)
		if ctx.Err() != nil {
			status = "cancelled"
			break
		}
		if _, err := db.Exec(`INSERT INTO catalog_match_results VALUES(?,?,?,?,?)`, id, i, author, res.Status, string(nativeJSON(res))); err != nil {
			status = "failed"
			_, _ = db.Exec(`UPDATE catalog_match_jobs SET error=? WHERE id=?`, err.Error(), id)
			break
		}
		_, _ = db.Exec(`UPDATE catalog_match_jobs SET done=?,updated_at=? WHERE id=? AND status='running'`, i+1, time.Now().UnixMilli(), id)
	}
	_, _ = db.Exec(`UPDATE catalog_match_jobs SET status=?,updated_at=? WHERE id=? AND status='running'`, status, time.Now().UnixMilli(), id)
	if s.log != nil {
		s.log.Info("author match finished", "job", id, "status", status)
	}
}

// loadAuthorJob reads a person's author job in the book job's wire shape,
// with options {only_missing}.
func (s *Server) loadAuthorJob(ctx context.Context, id string, uid int64) (nativeObject, error) {
	j, err := s.loadMatchJob(ctx, s.store.DB.QueryRowContext(ctx, `SELECT `+matchJobColumns+` FROM catalog_match_jobs WHERE id=? AND user_id=? AND provider=?`, id, uid, authorJobProvider))
	if err != nil {
		return nil, err
	}
	var options string
	_ = s.store.DB.QueryRowContext(ctx, `SELECT options FROM catalog_match_jobs WHERE id=?`, id).Scan(&options)
	out := nativeMapJSON(j)
	opts := nativeObject{}
	_ = json.Unmarshal([]byte(options), &opts)
	out["options"] = opts
	delete(out, "provider")
	return out, nil
}

func (s *Server) nativeAuthorJob(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	id := chi.URLParam(r, "job")
	if !authorJobID.MatchString(id) {
		writeErr(w, 404, "not found")
		return
	}
	j, err := s.loadAuthorJob(ctx, id, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if r.Method == "DELETE" {
		if st := nativeString(j["status"]); st == "running" || st == "queued" {
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

// imageType is an artwork file's content type by extension (JPEG by default,
// as Audiobookshelf stores covers and author photos).
func imageType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	}
	return "image/jpeg"
}
