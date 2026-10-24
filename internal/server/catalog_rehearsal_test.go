package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

// Run only against an explicitly supplied staging catalog. Copy it read-only
// before creating a temporary test login. No background jobs or upstream service
// connections start, and original media files are opened only for reading.
func TestNativeCatalogSnapshotRehearsal(t *testing.T) {
	path := os.Getenv("POCKET_NATIVE_REHEARSAL_DB")
	if path == "" {
		t.Skip("set POCKET_NATIVE_REHEARSAL_DB to an imported staging catalog")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	src, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	dest := filepath.Join(temp, "pocket.sqlite")
	_, err = src.Exec(`VACUUM INTO ?`, dest)
	src.Close()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(dest, log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := testConfig(temp)
	s := New(cfg, st, log, nil)
	blocked := gatewayTransport(func(*http.Request) (*http.Response, error) {
		t.Error("rehearsal called an upstream service")
		return nil, io.ErrUnexpectedEOF
	})
	s.absHTTP = &http.Client{Transport: blocked}
	s.streamHTTP = &http.Client{Transport: blocked}
	ctx := context.Background()
	var uid int64
	if err = st.DB.QueryRow(`SELECT pocket_user_id FROM catalog_accounts WHERE pocket_user_id IS NOT NULL AND active=1 AND locked=0 ORDER BY pocket_user_id LIMIT 1`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	tok, _, err := st.CreateAPIToken(ctx, uid, "isolated native rehearsal")
	if err != nil {
		t.Fatal(err)
	}
	h := s.Router()
	get := func(path, rg string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		if rg != "" {
			r.Header.Set("Range", rg)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	libs, err := s.catalog().Libraries(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, lib := range libs {
		total += lib.Count
		w := get("/api/v1/catalog/items?library="+url.QueryEscape(lib.ID)+"&limit=1", "")
		if w.Code != 200 {
			t.Fatalf("library browse: %d %s", w.Code, w.Body.String())
		}
		var page struct {
			Total int `json:"total"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != lib.Count {
			t.Fatal("library count mismatch")
		}
	}
	type sample struct{ item, file, mime string }
	samples := []sample{}
	rows, err := st.DB.Query(`SELECT item_id,id,mime FROM catalog_files WHERE role='audio' AND excluded=0 ORDER BY mime,item_id,track_index`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var f sample
		if err = rows.Scan(&f.item, &f.file, &f.mime); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	checked := map[string]bool{}
	for _, sample := range samples {
		if checked[sample.mime] {
			continue
		}
		file, _, err := s.catalog().OpenFile(ctx, uid, sample.item, sample.file, false)
		if err != nil {
			continue
		}
		stat, err := file.Stat()
		if err != nil {
			file.Close()
			t.Fatal(err)
		}
		if stat.Size() < 64 {
			file.Close()
			continue
		}
		for _, offset := range []int64{0, stat.Size() - 32} {
			expected := make([]byte, 32)
			if _, err = file.ReadAt(expected, offset); err != nil {
				file.Close()
				t.Fatal(err)
			}
			w := get("/api/v1/catalog/items/"+url.PathEscape(sample.item)+"/files/"+url.PathEscape(sample.file), fmt.Sprintf("bytes=%d-%d", offset, offset+31))
			if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), expected) {
				file.Close()
				t.Fatalf("%s byte range mismatch at offset %d: HTTP %d", sample.mime, offset, w.Code)
			}
		}
		file.Close()
		checked[sample.mime] = true
	}
	if len(checked) < 2 {
		t.Fatalf("only %d media types verified", len(checked))
	}
	w := get("/api/v1/catalog/me/progress", "")
	if w.Code != 200 {
		t.Fatalf("progress: %d", w.Code)
	}
	t.Logf("Native snapshot verified without upstream: %d libraries, %d visible items, %d audio MIME types with exact leading/trailing byte ranges", len(libs), total, len(checked))
}
