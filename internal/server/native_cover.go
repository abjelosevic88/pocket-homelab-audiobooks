package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/fetch"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *Server) nativeCoverUpload(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native editing is not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	id := chi.URLParam(r, "id")
	a, err := s.catalog().Account(ctx, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if !a.Permissions.Update {
		writeErr(w, 404, "not found")
		return
	}
	_, err = s.catalog().Item(ctx, uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	if err = r.ParseMultipartForm(1 << 20); err != nil {
		writeErr(w, 400, "invalid cover upload")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "choose a cover image")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, 400, "invalid image")
		return
	}
	updated, err := s.nativeSaveCover(ctx, uid, id, data)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, nativeObject{"ok": true, "updated_at": updated})
}
func (s *Server) nativeSaveCover(ctx context.Context, uid int64, id string, data []byte) (int64, error) {
	a, err := s.catalog().Account(ctx, uid)
	if err != nil || !a.Permissions.Update {
		return 0, library.ErrNotFound
	}
	it, err := s.catalog().Item(ctx, uid, id)
	if err != nil {
		return 0, err
	}
	if len(data) > 16<<20 {
		return 0, library.ErrInvalid
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40000000 {
		return 0, fmt.Errorf("%w: use a JPEG or PNG cover under 40 megapixels", library.ErrInvalid)
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + "." + format
	dir := filepath.Join(s.cfg.DataDir, "native-art")
	if err = os.MkdirAll(dir, 0755); err != nil {
		return 0, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	// Content-addressed artwork is immutable, so concurrent updates cannot alter
	// another book's image. Keep originals in the catalog and source filesystem.
	output, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err == nil {
		_, err = output.Write(data)
		if err == nil {
			err = output.Sync()
		}
		closeErr := output.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = root.Remove(name)
			return 0, err
		}
	} else if !os.IsExist(err) {
		return 0, err
	}
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_roots VALUES('native-art','/pocket-native-art',?) ON CONFLICT(id) DO UPDATE SET local_path=excluded.local_path`, dir); err != nil {
		return 0, err
	}
	path := "/pocket-native-art/" + name
	doc := string(nativeJSON(nativeObject{"role": "cover", "metadata": nativeObject{"path": path, "filename": name}}))
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_files VALUES(?,'native-cover','cover','native-art',?,?,?, ?,0,0,0,?) ON CONFLICT(item_id,id) DO UPDATE SET relative_path=excluded.relative_path,name=excluded.name,mime=excluded.mime,size=excluded.size,document=excluded.document`, id, name, name, "image/"+format, len(data), doc); err != nil {
		return 0, err
	}
	updated := max(it.UpdatedAt+1, time.Now().UnixMilli())
	if err = tx.QueryRowContext(ctx, `UPDATE catalog_items SET document=json_set(document,'$.media.coverPath',?),updated_at=max(updated_at+1,?) WHERE id=? RETURNING updated_at`, path, updated, id).Scan(&updated); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
}

func (s *Server) nativeCoverURL(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native metadata is not active")
		return
	}
	uid := actorID(r.Context())
	id := chi.URLParam(r, "id")
	a, err := s.catalog().Account(r.Context(), uid)
	if err != nil || !a.Permissions.Update {
		writeErr(w, 403, "metadata editing permission required")
		return
	}
	if _, err = s.catalog().Item(r.Context(), uid, id); err != nil {
		s.nativeError(w, err)
		return
	}
	var in struct {
		URL string `json:"url"`
	}
	if err = decode(r, &in); err != nil {
		writeErr(w, 400, "invalid cover URL")
		return
	}
	data, _, err := fetch.Get(r.Context(), s.nativeFetchHTTP, in.URL, s.cfg.UserAgent, "", 16<<20)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	updated, err := s.nativeSaveCover(r.Context(), uid, id, data)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, nativeObject{"ok": true, "updated_at": updated})
}
