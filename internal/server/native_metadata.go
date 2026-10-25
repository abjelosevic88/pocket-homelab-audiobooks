package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/metadata"
)

type nativeProvider struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	HasSecret bool   `json:"has_secret"`
	Enabled   bool   `json:"enabled"`
	Revision  int64  `json:"revision"`
	Region    string `json:"region"`   // Audible only; empty uses AUDIBLE_REGION
	Priority  int    `json:"priority"` // match order, lowest first
}

func (s *Server) nativeProviders(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	admin := userFrom(r.Context()).Role == "admin"
	if r.Method != "GET" && !admin {
		writeErr(w, 403, "provider management requires an administrator")
		return
	}
	if !admin {
		a, err := s.catalog().Account(r.Context(), actorID(r.Context()))
		if err != nil || !a.Permissions.Update {
			writeErr(w, 403, "metadata editing permission required")
			return
		}
	}
	if r.Method == "GET" {
		q := `SELECT id,kind,name,url,secret<>'',enabled,revision,region,priority FROM catalog_metadata_providers`
		if !admin || r.URL.Query().Get("manage") != "1" {
			q += ` WHERE enabled=1`
		}
		q += ` ORDER BY priority,name`
		rows, err := s.store.DB.QueryContext(r.Context(), q)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		defer rows.Close()
		out := []nativeProvider{}
		for rows.Next() {
			var p nativeProvider
			if err = rows.Scan(&p.ID, &p.Kind, &p.Name, &p.URL, &p.HasSecret, &p.Enabled, &p.Revision, &p.Region, &p.Priority); err != nil {
				s.nativeError(w, err)
				return
			}
			out = append(out, p)
		}
		if err = rows.Err(); err != nil {
			s.nativeError(w, err)
			return
		}
		if r.URL.Path == "/api/search/providers" {
			list := []any{}
			for _, p := range out {
				list = append(list, nativeObject{"value": p.ID, "text": p.Name})
			}
			writeJSON(w, 200, nativeObject{"providers": nativeObject{"books": list, "booksCovers": list, "podcasts": []any{}}})
			return
		}
		writeJSON(w, 200, out)
		return
	}
	var in struct {
		nativeProvider
		Secret      string `json:"secret"`
		ClearSecret bool   `json:"clear_secret"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid provider")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 || len(in.URL) > 2000 || len(in.Secret) > 4096 {
		writeErr(w, 400, "invalid provider")
		return
	}
	if in.ID == "" {
		in.Kind = "custom"
		in.ID = "custom-" + rand.Text()
	}
	if in.Kind == "custom" {
		u, err := fetch.URL(in.URL)
		if err != nil || u.RawQuery != "" {
			writeErr(w, 400, "use an HTTP or HTTPS provider address without a query")
			return
		}
	} else if in.ID != "openlibrary" && in.ID != "google" && in.ID != "audible" {
		writeErr(w, 400, "invalid provider type")
		return
	}
	in.Region = strings.ToLower(strings.TrimSpace(in.Region))
	if in.Kind != "audible" {
		in.Region = ""
	}
	if in.Region != "" && !metadata.ValidRegion(in.Region) {
		writeErr(w, 400, "unknown Audible region")
		return
	}
	if in.Priority == 0 {
		in.Priority = 100
	}
	if in.Priority < 1 || in.Priority > 1000 {
		writeErr(w, 400, "match order must be 1 to 1000")
		return
	}
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	defer tx.Rollback()
	var oldSecret, kind string
	var revision int64
	err = tx.QueryRowContext(r.Context(), `SELECT kind,secret,revision FROM catalog_metadata_providers WHERE id=?`, in.ID).Scan(&kind, &oldSecret, &revision)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.nativeError(w, err)
		return
	}
	if exists && (revision != in.Revision || kind != in.Kind) {
		s.nativeError(w, library.ErrConflict)
		return
	}
	if !exists && in.Kind != "custom" {
		writeErr(w, 400, "invalid provider")
		return
	}
	secret := oldSecret
	if in.ClearSecret {
		secret = ""
	}
	if in.Secret != "" {
		secret, err = s.seal(in.Secret)
		if err != nil {
			s.nativeError(w, err)
			return
		}
	}
	if in.Kind == "google" && in.Enabled && secret == "" {
		writeErr(w, 400, "Google Books needs an API key")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO catalog_metadata_providers(id,kind,name,url,secret,enabled,revision,region,priority) VALUES(?,?,?,?,?,?,1,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,url=excluded.url,secret=excluded.secret,enabled=excluded.enabled,region=excluded.region,priority=excluded.priority,revision=catalog_metadata_providers.revision+1`, in.ID, in.Kind, in.Name, strings.TrimRight(in.URL, "/"), secret, in.Enabled, in.Region, in.Priority)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		s.nativeError(w, err)
		return
	}
	in.Revision = revision + 1
	in.HasSecret = secret != ""
	writeJSON(w, 200, in.nativeProvider)
}
func (s *Server) nativeMetadataClient() metadata.Client {
	return metadata.Client{HTTP: s.nativeFetchHTTP, UserAgent: s.cfg.UserAgent}
}
func (s *Server) nativeProvider(ctx context.Context, id string) (metadata.Provider, error) {
	var p metadata.Provider
	var sealed string
	err := s.store.DB.QueryRowContext(ctx, `SELECT id,kind,url,secret,region FROM catalog_metadata_providers WHERE id=? AND enabled=1`, id).Scan(&p.ID, &p.Kind, &p.URL, &sealed, &p.Region)
	if errors.Is(err, sql.ErrNoRows) {
		return p, library.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if sealed != "" {
		p.Authorization, err = s.open(sealed)
	}
	return p, err
}
func (s *Server) nativeMetadataSearch(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	a, err := s.catalog().Account(r.Context(), actorID(r.Context()))
	if err != nil || !a.Permissions.Update {
		writeErr(w, 403, "metadata editing permission required")
		return
	}
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		provider = "openlibrary"
	}
	p, err := s.nativeProvider(r.Context(), provider)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if id := r.URL.Query().Get("work"); id != "" && p.Kind == "openlibrary" {
		result, err := s.nativeMetadataClient().Work(r.Context(), id)
		if err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if asin := r.URL.Query().Get("asin"); asin != "" && p.Kind == "audible" {
		result, err := s.nativeMetadataClient().AudibleBook(r.Context(), p.ID, p.Region, asin)
		if err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, result)
		return
	}
	q := r.URL.Query()
	results, err := s.nativeMetadataClient().Search(r.Context(), p, firstNonEmpty(q.Get("q"), q.Get("title")), q.Get("author"), q.Get("isbn"))
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if r.URL.Path == "/api/search/covers" {
		covers := []string{}
		for _, v := range results {
			if v.Cover != "" {
				covers = append(covers, v.Cover)
			}
		}
		writeJSON(w, 200, covers)
		return
	}
	if r.URL.Path == "/api/search/books" {
		books := []any{}
		for _, v := range results {
			m := v.Metadata
			m["author"] = strings.Join(v.Authors, ", ")
			m["cover"] = v.Cover
			books = append(books, m)
		}
		writeJSON(w, 200, books)
		return
	}
	writeJSON(w, 200, results)
}
