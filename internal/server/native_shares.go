package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

type nativeShare struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	UserID       int64  `json:"-"`
	Kind         string `json:"kind"`
	TargetID     string `json:"target_id"`
	EpisodeID    string `json:"episode_id"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Download     bool   `json:"downloadable"`
	ExpiresAt    int64  `json:"expires_at"`
	CreatedAt    int64  `json:"created_at"`
	Revision     int64  `json:"revision"`
	HasPassword  bool   `json:"has_password"`
	URL          string `json:"url"`
	FeedURL      string `json:"feed_url"`
}

const shareColumns = "id,slug,user_id,kind,target_id,episode_id,name,password_hash,downloadable,expires_at,created_at,revision"

func scanShare(row interface{ Scan(...any) error }) (nativeShare, error) {
	var v nativeShare
	err := row.Scan(&v.ID, &v.Slug, &v.UserID, &v.Kind, &v.TargetID, &v.EpisodeID, &v.Name, &v.PasswordHash, &v.Download, &v.ExpiresAt, &v.CreatedAt, &v.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		err = library.ErrNotFound
	}
	return v, err
}
func (s *Server) shareWire(v nativeShare) nativeShare {
	v.HasPassword = v.PasswordHash != ""
	base := strings.TrimRight(s.cfg.BaseURL, "/")
	v.URL = base + "/share/" + url.PathEscape(v.Slug)
	v.FeedURL = base + "/feed/" + url.PathEscape(v.Slug)
	return v
}
func (s *Server) shareOwner(ctx context.Context, uid int64) error {
	u, err := s.store.GetUser(ctx, uid)
	if err != nil {
		return err
	}
	if u.Role != "admin" {
		return library.ErrNotFound
	}
	a, err := s.catalog().Account(ctx, uid)
	if err != nil {
		return err
	}
	if !a.Permissions.Download {
		return library.ErrNotFound
	}
	return nil
}

// Resolve dynamically and recheck the publisher's current permissions on every
// request. Revoked accounts and removed collection members cannot remain public.
func (s *Server) shareItems(ctx context.Context, v nativeShare) ([]library.Item, error) {
	if err := s.shareOwner(ctx, v.UserID); err != nil {
		return nil, err
	}
	ids := []string{}
	switch v.Kind {
	case "item":
		ids = append(ids, v.TargetID)
	case "collection":
		list, err := s.catalog().GetList(ctx, v.UserID, "collections", v.TargetID)
		if err != nil {
			return nil, err
		}
		for _, entry := range list.Entries {
			ids = append(ids, entry.ItemID)
		}
	case "series":
		rows, err := s.store.DB.QueryContext(ctx, `SELECT i.id FROM catalog_items i WHERE EXISTS (SELECT 1 FROM json_each(i.document,'$.series') j WHERE json_extract(j.value,'$.id')=?) ORDER BY (SELECT CAST(json_extract(j.value,'$.sequence') AS REAL) FROM json_each(i.document,'$.series') j WHERE json_extract(j.value,'$.id')=?),i.title`, v.TargetID, v.TargetID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	default:
		return nil, library.ErrInvalid
	}
	out := []library.Item{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		it, err := s.catalog().Item(ctx, v.UserID, id)
		if errors.Is(err, library.ErrNotFound) && v.Kind != "item" {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, it)
		if len(out) > 2000 {
			return nil, fmt.Errorf("%w: share exceeds 2,000 items", library.ErrInvalid)
		}
	}
	if len(out) == 0 {
		return nil, library.ErrNotFound
	}
	return out, nil
}

var shareSlugPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{4,128}$`)

func (s *Server) nativeShares(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native sharing is not active")
		return
	}
	uid := actorID(r.Context())
	if u := userFrom(r.Context()); u == nil || u.Role != "admin" {
		writeErr(w, 403, "sharing requires an administrator")
		return
	}
	if r.Method != "GET" && r.Method != "DELETE" {
		if err := s.shareOwner(r.Context(), uid); err != nil {
			writeErr(w, 403, "sharing requires an administrator with download permission")
			return
		}
	}

	w.Header().Set("Cache-Control", "private, no-store")
	id := chi.URLParam(r, "share")
	if r.Method == "GET" {
		rows, err := s.store.DB.QueryContext(r.Context(), `SELECT `+shareColumns+` FROM catalog_shares WHERE user_id=? ORDER BY created_at DESC`, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		defer rows.Close()
		out := []nativeShare{}
		for rows.Next() {
			v, e := scanShare(rows)
			if e != nil {
				s.nativeError(w, e)
				return
			}
			out = append(out, s.shareWire(v))
		}
		if err = rows.Err(); err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, out)
		return
	}
	if r.Method == "DELETE" {
		result, err := s.store.DB.ExecContext(r.Context(), `DELETE FROM catalog_shares WHERE id=? AND user_id=?`, id, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			writeErr(w, 404, "share not found")
			return
		}
		w.WriteHeader(204)
		return
	}
	var in struct {
		Kind     string  `json:"kind"`
		Target   string  `json:"target_id"`
		Episode  string  `json:"episode_id"`
		Name     string  `json:"name"`
		Slug     string  `json:"slug"`
		Password *string `json:"password"`
		Download bool    `json:"downloadable"`
		Expires  int64   `json:"expires_at"`
		Revision int64   `json:"revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&in); err != nil {
		writeErr(w, 400, "invalid share")
		return
	}
	if in.Expires < 0 || in.Expires != 0 && in.Expires <= time.Now().UnixMilli() || len(in.Name) > 300 || in.Password != nil && (len(*in.Password) > 72 || len(*in.Password) > 0 && len(*in.Password) < 8) {
		writeErr(w, 400, "choose a future expiry and a password of 8–72 bytes, or leave it empty")
		return
	}
	var v nativeShare
	var err error
	if id != "" {
		v, err = scanShare(s.store.DB.QueryRowContext(r.Context(), `SELECT `+shareColumns+` FROM catalog_shares WHERE id=? AND user_id=?`, id, uid))
		if err != nil {
			s.nativeError(w, err)
			return
		}
		if v.Revision != in.Revision {
			s.nativeError(w, library.ErrConflict)
			return
		}
	} else {
		token := make([]byte, 24)
		if _, err = rand.Read(token); err != nil {
			s.nativeError(w, err)
			return
		}
		v = nativeShare{ID: base64.RawURLEncoding.EncodeToString(token[:12]), Slug: base64.RawURLEncoding.EncodeToString(token), UserID: uid, Kind: in.Kind, TargetID: in.Target, EpisodeID: in.Episode, CreatedAt: time.Now().UnixMilli(), Revision: 1}
		if in.Slug != "" {
			if !shareSlugPattern.MatchString(in.Slug) {
				writeErr(w, 400, "slug must be 4–128 letters, numbers, hyphens or underscores")
				return
			}
			v.Slug = in.Slug
		}
	}
	if in.Password != nil {
		v.PasswordHash = ""
		if *in.Password != "" {
			hash, e := bcrypt.GenerateFromPassword([]byte(*in.Password), bcrypt.DefaultCost)
			if e != nil {
				s.nativeError(w, e)
				return
			}
			v.PasswordHash = string(hash)
		}
	}
	v.Name = strings.TrimSpace(in.Name)
	v.Download = in.Download
	v.ExpiresAt = in.Expires
	items, err := s.shareItems(r.Context(), v)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if v.EpisodeID != "" {
		if v.Kind != "item" {
			writeErr(w, 400, "episodes can only be shared individually")
			return
		}
		if _, _, _, err = s.catalog().Timeline(r.Context(), uid, v.TargetID, v.EpisodeID); err != nil {
			s.nativeError(w, err)
			return
		}
	}
	if v.Name == "" {
		v.Name = items[0].Title
		if v.Kind == "collection" {
			if list, e := s.catalog().GetList(r.Context(), uid, "collections", v.TargetID); e == nil {
				v.Name = list.Name
			}
		}
		if v.Kind == "series" {
			doc := nativeObject{}
			_ = json.Unmarshal(items[0].Document, &doc)
			for _, entry := range nativeArray(doc["series"]) {
				sr := nativeMap(entry)
				if nativeString(sr["id"]) == v.TargetID {
					v.Name = nativeString(sr["name"])
					break
				}
			}
		}

	}
	if base, e := url.Parse(s.cfg.BaseURL); e != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && base.Scheme != "http") {
		writeErr(w, 400, "set BASE_URL before creating a public share")
		return
	}
	if id == "" {
		_, err = s.store.DB.ExecContext(r.Context(), `INSERT INTO catalog_shares (`+shareColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Slug, uid, v.Kind, v.TargetID, v.EpisodeID, v.Name, v.PasswordHash, v.Download, v.ExpiresAt, v.CreatedAt, v.Revision)
		if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
			writeErr(w, 409, "share slug already exists")
			return
		}
	} else {
		var result sql.Result
		result, err = s.store.DB.ExecContext(r.Context(), `UPDATE catalog_shares SET name=?,password_hash=?,downloadable=?,expires_at=?,revision=revision+1 WHERE id=? AND user_id=? AND revision=?`, v.Name, v.PasswordHash, v.Download, v.ExpiresAt, v.ID, uid, in.Revision)
		if err == nil {
			n, _ := result.RowsAffected()
			if n == 0 {
				err = library.ErrConflict
			}
			v.Revision++
		}
	}
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, s.shareWire(v))
}
func (s *Server) shareAccess(w http.ResponseWriter, r *http.Request) (nativeShare, bool) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "share not found")
		return nativeShare{}, false
	}
	slug := chi.URLParam(r, "slug")
	v, err := scanShare(s.store.DB.QueryRowContext(r.Context(), `SELECT `+shareColumns+` FROM catalog_shares WHERE slug=?`, slug))
	if err != nil || v.ExpiresAt > 0 && v.ExpiresAt <= time.Now().UnixMilli() {
		writeErr(w, 404, "share not found")
		return v, false
	}
	if err = s.shareOwner(r.Context(), v.UserID); err != nil {
		writeErr(w, 404, "share not found")
		return v, false
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	if v.PasswordHash != "" {
		cookieName := "pab_share_" + v.ID
		if cookie, e := r.Cookie(cookieName); e == nil {
			parts := strings.Split(cookie.Value, ".")
			if len(parts) == 2 {
				var expires int64
				fmt.Sscan(parts[0], &expires)
				expected := s.sign("share:" + v.ID + ":" + v.PasswordHash + ":" + parts[0])
				if expected != "" && expires > time.Now().Unix() && subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) == 1 {
					return v, true
				}
			}
		}
		_, password, ok := r.BasicAuth()
		valid := false
		if ok && len(password) <= 72 {
			peer := sharePeer(r)
			if !s.nativeShareAttempts.permit(peer) {
				w.Header().Set("Retry-After", "60")
				writeErr(w, 429, "too many password attempts; retry shortly")
				return v, false
			}
			select {
			case sharePasswordSlots <- struct{}{}:
			case <-r.Context().Done():
				return v, false
			}
			valid = bcrypt.CompareHashAndPassword([]byte(v.PasswordHash), []byte(password)) == nil
			<-sharePasswordSlots
			if valid {
				s.nativeShareAttempts.reset(peer)
			}
		}
		if !valid {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm="Pocket share %s", charset="UTF-8"`, v.ID))
			writeErr(w, 401, "this share requires its password")
			return v, false
		}
		expires := fmt.Sprint(time.Now().Add(24 * time.Hour).Unix())
		sig := s.sign("share:" + v.ID + ":" + v.PasswordHash + ":" + expires)
		if sig == "" {
			writeErr(w, 503, "share authentication unavailable")
			return v, false
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: expires + "." + sig, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	}
	return v, true
}
