package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/ebook"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	cfg.Backend = "native"
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

	// Validate every imported book through the typed web contract as well as
	// the native client's loosely typed JSON. Source entity dates differ.
	for _, lib := range libs {
		if lib.MediaType != "book" {
			continue
		}
		items, e := s.nativeItems(ctx, uid, lib.ID)
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range items {
			if _, e = nativeDecodeItem(item); e != nil {
				t.Fatalf("imported web book %v: %v", item["id"], e)
			}
		}
	}
	if w := get("/api/v1/home", ""); w.Code != 200 {
		t.Fatalf("native web home: %d %s", w.Code, w.Body.String())
	}

	if w := get("/api/podcasts", ""); w.Code != 404 {
		t.Fatalf("podcast API available: %d", w.Code)
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
		playPath := "/api/items/" + sample.item + "/play"
		item, err := s.nativeItem(ctx, uid, sample.item)
		if err != nil {
			t.Fatal(err)
		}
		if item["mediaType"] == "podcast" {
			found := false
			for _, v := range nativeArray(nativeMap(item["media"])["episodes"]) {
				e := nativeMap(v)
				if nativeString(nativeMap(e["audioFile"])["ino"]) == sample.file {
					playPath += "/" + nativeString(e["id"])
					found = true
					break
				}
			}
			if !found {
				t.Fatal("episode mapping missing")
			}
		}
		play := httptest.NewRequest("POST", playPath, strings.NewReader(`{"deviceInfo":{"deviceId":"rehearsal"},"forceDirectPlay":true}`))
		play.Header.Set("Authorization", "Bearer "+tok)
		pw := httptest.NewRecorder()
		h.ServeHTTP(pw, play)
		info := jsonObject(t, pw)
		if len(nativeArray(info["audioTracks"])) < 1 {
			t.Fatal("missing native playback tracks")
		}
		body := string(nativeJSON(nativeObject{"reportId": "rehearsal-" + sample.mime, "reportedAt": time.Now().UnixMilli(), "currentTime": nativeNumber(info["currentTime"]) + 1, "timeListened": 1}))
		report := httptest.NewRequest("POST", "/api/session/"+nativeString(info["id"])+"/sync", strings.NewReader(body))
		report.Header.Set("Authorization", "Bearer "+tok)
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, report)
		if rw.Code != 200 {
			t.Fatalf("native report: %d %s", rw.Code, rw.Body.String())
		}

	}
	if len(checked) < 2 {
		t.Fatalf("only %d media types verified", len(checked))
	}
	w := get("/api/v1/catalog/me/progress", "")
	if w.Code != 200 {
		t.Fatalf("progress: %d", w.Code)
	}
	for _, path := range []string{"/api/v1/home", "/api/v1/libraries", "/api/v1/stats", "/api/me/items-in-progress", "/api/me/listening-stats"} {
		w := get(path, "")
		if w.Code != 200 {
			t.Fatalf("native route %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if os.Getenv("POCKET_NATIVE_REHEARSAL_EBOOKS") == "1" {
		rows, e := st.DB.Query(`SELECT item_id,id FROM catalog_files WHERE role IN ('ebook','supplement') AND lower(name) LIKE '%.epub'`)
		if e != nil {
			t.Fatal(e)
		}
		ebooks := [][2]string{}
		for rows.Next() {
			var pair [2]string
			if e = rows.Scan(&pair[0], &pair[1]); e != nil {
				rows.Close()
				t.Fatal(e)
			}
			ebooks = append(ebooks, pair)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			t.Fatal(e)
		}
		for _, pair := range ebooks {
			f, _, e := s.catalog().OpenFile(ctx, uid, pair[0], pair[1], true)
			if e != nil {
				t.Errorf("EPUB file %s: %v", pair[1], e)
				continue
			}
			stat, e := f.Stat()
			if e == nil {
				e = ebook.Prepare(ctx, f, stat.Size(), io.Discard)
			}
			f.Close()
			if e != nil {
				t.Errorf("EPUB file %s: %v", pair[1], e)
			}
		}
		t.Logf("Prepared %d imported EPUB files read-only", len(ebooks))
	}
	if os.Getenv("POCKET_NATIVE_REHEARSAL_SCAN") == "1" {
		probe, err := exec.LookPath("ffprobe")
		if err != nil {
			t.Fatal(err)
		}
		for _, lib := range libs {
			if lib.MediaType != "book" {
				continue
			}
			report, err := s.catalog().Scan(ctx, uid, lib.ID, library.ScanOptions{FFprobe: probe})
			if err != nil {
				t.Fatalf("real-library scan: %v", err)
			}
			t.Logf("Scan rehearsal: added=%d updated=%d missing=%d files=%d probed=%d unchanged=%d invalid=%d", report.Added, report.Updated, report.Missing, report.Files, report.Probed, report.Unchanged, report.Invalid)
		}
	}
	t.Logf("Native snapshot verified without upstream: %d libraries, %d visible items, %d audio MIME types with exact leading/trailing byte ranges", len(libs), total, len(checked))
}
