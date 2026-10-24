package server

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestNativeArchiveOriginalBytesAndPermissions(t *testing.T) {
	s, h, a, b, tok := nativeHarness(t)
	for _, path := range []string{"/api/items/book/download", "/api/v1/catalog/items/book/download"} {
		w := nativeCall(t, h, tok, "GET", path, "")
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		if len(z.File) != 2 {
			t.Fatalf("files: %d", len(z.File))
		}
		names := map[string]bool{}
		for _, f := range z.File {
			if names[f.Name] || strings.Contains(f.Name, "..") {
				t.Fatal("unsafe or duplicate archive name", f.Name)
			}
			names[f.Name] = true
			r, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			r.Close()
			if err != nil || string(data) != "0123456789audio" {
				t.Fatalf("original bytes: %q %v", data, err)
			}
		}
		head := nativeCall(t, h, tok, "HEAD", path, "")
		if head.Code != 200 || head.Body.Len() != 0 {
			t.Fatalf("HEAD: %d", head.Code)
		}
	}
	if w := call(t, h, b, "GET", "/api/v1/catalog/items/book/download", ""); w.Code != 404 {
		t.Fatal("other account downloaded", w.Code)
	}
	if _, err := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.download',json('false')) WHERE pocket_user_id=?`, a.u.ID); err != nil {
		t.Fatal(err)
	}
	if w := nativeCall(t, h, tok, "GET", "/api/items/book/download", ""); w.Code != 404 {
		t.Fatal("download permission bypassed", w.Code)
	}
	// Streaming remains independently permitted when offline downloads are disabled.
	if w := nativeCall(t, h, tok, "GET", "/api/items/book/file/11", ""); w.Code != 206 {
		t.Fatal("stream permission changed", w.Code)
	}
}
