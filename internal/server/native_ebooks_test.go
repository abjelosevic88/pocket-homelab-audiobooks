package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func addEbookFixture(t *testing.T, s *Server, dir string) {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	files := map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"book.opf":               `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" unique-identifier="id" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">test-reader</dc:identifier><dc:title>Native ebook</dc:title><dc:language>en</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/><item id="two" href="two.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="one"/><itemref idref="two"/></spine></package>`,
		"nav.xhtml":              `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Contents</title></head><body><nav epub:type="toc"><ol><li><a href="one.xhtml">Chapter one</a></li><li><a href="two.xhtml">Chapter two</a></li></ol></nav></body></html>`,
		"one.xhtml":              `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>One</title></head><body><h1>Chapter one</h1>` + strings.Repeat(`<p>A reader keeps their place across devices. This is a generated test book with enough text for several pages.</p>`, 90) + `<script>parent.__ebookAttacked=true</script><img src="https://evil.test/track"/><iframe src="/api/v1/auth/me"/></body></html>`,
		"two.xhtml":              `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Two</title></head><body><h1>Chapter two</h1><p>The end of the generated test book.</p></body></html>`,
	}
	for n, data := range files {
		f, e := w.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		io.WriteString(f, data)
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "book.epub"), b.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	// Two-page PDF with a standard font, generated without external tooling.
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 500] /Resources << /Font << /F1 5 0 R >> >> /Contents 6 0 R >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 500] /Resources << /Font << /F1 5 0 R >> >> /Contents 7 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	for _, text := range []string{"Companion page one", "Companion page two"} {
		stream := "BT /F1 18 Tf 40 420 Td (" + text + ") Tj ET"
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", len(offsets), xref)
	if e := os.WriteFile(filepath.Join(dir, "companion.pdf"), pdf.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	var root string
	if e := s.store.DB.QueryRow(`SELECT local_path FROM catalog_roots WHERE id='root'`).Scan(&root); e != nil {
		t.Fatal(e)
	}
	relative, e := filepath.Rel(root, filepath.Join(dir, "book.epub"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.store.DB.Exec(`INSERT INTO catalog_files VALUES('book','ebook','ebook','root',?,'book.epub','application/epub+zip',?,0,0,0,'{}')`, relative, b.Len()); e != nil {
		t.Fatal(e)
	}
	relativePDF, e := filepath.Rel(root, filepath.Join(dir, "companion.pdf"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.store.DB.Exec(`INSERT INTO catalog_files VALUES('book','pdf','supplement','root',?,'companion.pdf','application/pdf',?,0,0,0,'{}')`, relativePDF, pdf.Len()); e != nil {
		t.Fatal(e)
	}

}
func TestNativeEbookReaderPositionsPermissionsAndCompanions(t *testing.T) {
	s, h, a, b, _ := nativeHarness(t)
	var root string
	s.store.DB.QueryRow(`SELECT local_path FROM catalog_roots WHERE id='root'`).Scan(&root)
	addEbookFixture(t, s, root)
	base := "/api/v1/catalog/items/book/reader"
	info := jsonObject(t, call(t, h, a, "GET", base, ""))
	p := nativeMap(info["position"])
	revision := nativeString(p["revision"])
	if nativeString(info["file_id"]) != "ebook" {
		t.Fatal(info)
	}
	if w := call(t, h, b, "GET", base, ""); w.Code != 404 {
		t.Fatal("foreign ebook access", w.Code)
	}
	if w := call(t, h, b, "GET", base+"/ebook", ""); w.Code != 404 {
		t.Fatal("foreign content access", w.Code)
	}
	body := fmt.Sprintf(`{"user_id":%d,"location":"epubcfi(/6/2!/4/2/1:0)","progress":0.3,"revision":%q}`, a.u.ID, revision)
	saved := jsonObject(t, call(t, h, a, "PUT", base+"?file=ebook", body))
	jsonObject(t, call(t, h, a, "PUT", base+"?file=ebook", body)) // exact retry is accepted
	if w := call(t, h, a, "PUT", base+"?file=ebook", strings.Replace(body, "0.3", "0.2", 1)); w.Code != 409 {
		t.Fatal("stale reader overwrote progress", w.Code)
	}
	var document string
	s.store.DB.QueryRow(`SELECT document FROM catalog_progress WHERE account_id='alice' AND item_id='book'`).Scan(&document)
	if !strings.Contains(document, `"ebookProgress":0.3`) {
		t.Fatal("client progress not updated", document)
	}
	content := call(t, h, a, "GET", base+"/ebook", "")
	if content.Code != 200 || content.Header().Get("Content-Type") != "application/epub+zip" {
		t.Fatal(content.Code, content.Body.String())
	}
	if _, e := zip.NewReader(bytes.NewReader(content.Body.Bytes()), int64(content.Body.Len())); e != nil {
		t.Fatal(e)
	}
	if _, e := s.store.DB.Exec(`INSERT INTO catalog_files SELECT item_id,'companion','supplement',root_id,relative_path,'companion.epub',mime,size,duration,track_index,excluded,document FROM catalog_files WHERE id='ebook'`); e != nil {
		t.Fatal(e)
	}
	companion := jsonObject(t, call(t, h, a, "GET", base+"?file=companion", ""))
	cp := nativeMap(companion["position"])
	jsonObject(t, call(t, h, a, "PUT", base+"?file=companion", fmt.Sprintf(`{"user_id":%d,"location":"epubcfi(/6/4!/4/2/1:0)","progress":0.8,"revision":%q}`, a.u.ID, nativeString(cp["revision"]))))
	again := jsonObject(t, call(t, h, a, "GET", base, ""))
	if nativeString(nativeMap(again["position"])["revision"]) != nativeString(saved["revision"]) {
		t.Fatal("companion overwrote main position")
	}
	if _, e := s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.download',json('false')) WHERE id='alice'`); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{base, base + "/ebook"} {
		if w := call(t, h, a, "GET", path, ""); w.Code != 404 {
			t.Fatal("download permission bypass", w.Code)
		}
	}
}
