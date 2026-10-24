package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	it, err := s.catalog().Item(ctx, uid, id)
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
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40000000 {
		writeErr(w, 400, "use a JPEG or PNG cover under 40 megapixels")
		return
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + "." + format
	dir := filepath.Join(s.cfg.DataDir, "native-art")
	if err = os.MkdirAll(dir, 0755); err != nil {
		s.nativeError(w, err)
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		s.nativeError(w, err)
		return
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
			s.nativeError(w, err)
			return
		}
	} else if !os.IsExist(err) {
		s.nativeError(w, err)
		return
	}
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_roots VALUES('native-art','/pocket-native-art',?) ON CONFLICT(id) DO UPDATE SET local_path=excluded.local_path`, dir); err != nil {
		s.nativeError(w, err)
		return
	}
	path := "/pocket-native-art/" + name
	doc := string(nativeJSON(nativeObject{"role": "cover", "metadata": nativeObject{"path": path, "filename": name}}))
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_files VALUES(?,'native-cover','cover','native-art',?,?,?, ?,0,0,0,?) ON CONFLICT(item_id,id) DO UPDATE SET relative_path=excluded.relative_path,name=excluded.name,mime=excluded.mime,size=excluded.size,document=excluded.document`, id, name, name, "image/"+format, len(data), doc); err != nil {
		s.nativeError(w, err)
		return
	}
	updated := max(it.UpdatedAt+1, time.Now().UnixMilli())
	if err = tx.QueryRowContext(ctx, `UPDATE catalog_items SET document=json_set(document,'$.media.coverPath',?),updated_at=max(updated_at+1,?) WHERE id=? RETURNING updated_at`, path, updated, id).Scan(&updated); err != nil {
		s.nativeError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		s.nativeError(w, err)
		return
	}
	writeJSON(w, 200, nativeObject{"ok": true, "updated_at": updated})
}
