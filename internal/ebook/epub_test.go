package ebook

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range files {
		f, e := w.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte(data))
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestPrepareEPUBRemovesActiveContentAndRetainsReadableXML(t *testing.T) {
	source := archive(t, map[string]string{"META-INF/container.xml": `<container/>`, "book.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Test</title><meta http-equiv="refresh" content="0;url=https://evil.test"/><style>p{color:blue}</style></head><body><h1 id="one">Chapter</h1><p>Hello &amp; goodbye<br/>Next line</p><script>parent.hacked=true</script><iframe src="/api/v1/auth/me"></iframe><img src="https://evil.test/pixel" onerror="alert(1)"/><a href="javascript:alert(1)" ping="https://evil.test">Link</a><img src="images/cover.png"/></body></html>`, "cover.svg": `<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="20"/></svg>`})
	var out bytes.Buffer
	if err := Prepare(context.Background(), bytes.NewReader(source), int64(len(source)), &out); err != nil {
		t.Fatal(err)
	}
	z, e := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range z.File {
		if f.Name != "book.xhtml" && f.Name != "cover.svg" {
			continue
		}
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		r.Close()
		text := string(b)
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			_, e = d.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatalf("not XML: %v %s", e, text)
			}
		}
		if f.Name == "book.xhtml" {
			for _, bad := range []string{"parent.hacked", "onerror", "evil.test", "javascript:", "<script", "<iframe", "http-equiv=\"refresh\""} {
				if strings.Contains(text, bad) {
					t.Fatalf("active content %s: %s", bad, text)
				}
			}
			for _, good := range []string{"Content-Security-Policy", "images/cover.png", "Chapter", "Next line", "p{color:blue}"} {
				if !strings.Contains(text, good) {
					t.Fatalf("lost %s", good)
				}
			}
		} else if !strings.HasPrefix(text, "<svg") {
			t.Fatalf("SVG wrapped in HTML: %s", text)
		}
	}
}
func TestPrepareRejectsInvalidArchives(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "a/../../escape", "META-INF/encryption.xml"} {
		t.Run(name, func(t *testing.T) {
			b := archive(t, map[string]string{"META-INF/container.xml": "<container/>", name: "bad"})
			if e := Prepare(context.Background(), bytes.NewReader(b), int64(len(b)), io.Discard); e == nil {
				t.Fatal("accepted unsafe EPUB")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := archive(t, map[string]string{"META-INF/container.xml": "<container/>"})
	if e := Prepare(ctx, bytes.NewReader(b), int64(len(b)), io.Discard); e == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestFontObfuscationAndDRMRefusal(t *testing.T) {
	for _, tc := range []struct {
		algorithm, id string
		key           []byte
		count         int
	}{
		{"http://www.idpf.org/2008/embedding", "urn:test:font", []byte{0xa9, 0x98}, 1040},
		{"http://ns.adobe.com/pdf/enc#RC", "urn:uuid:00112233-4455-6677-8899-aabbccddeeff", []byte{0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, 1024},
	} {
		t.Run(tc.algorithm, func(t *testing.T) {
			key := tc.key
			if strings.Contains(tc.algorithm, "idpf") {
				sum := sha1.Sum([]byte(tc.id))
				key = sum[:]
			}
			original := bytes.Repeat([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 160)
			masked := append([]byte{}, original...)
			for i := 0; i < tc.count; i++ {
				masked[i] ^= key[i%len(key)]
			}
			files := map[string]string{
				"META-INF/container.xml":  `<container><rootfiles><rootfile full-path="book.opf"/></rootfiles></container>`,
				"book.opf":                `<package unique-identifier="book"><metadata><identifier id="book">` + tc.id + `</identifier></metadata></package>`,
				"META-INF/encryption.xml": `<encryption><EncryptedData><EncryptionMethod Algorithm="` + tc.algorithm + `"/><CipherData><CipherReference URI="font.ttf"/></CipherData></EncryptedData></encryption>`,
				"font.ttf":                string(masked),
			}
			b := archive(t, files)
			var out bytes.Buffer
			if e := Prepare(context.Background(), bytes.NewReader(b), int64(len(b)), &out); e != nil {
				t.Fatal(e)
			}
			z, _ := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			for _, f := range z.File {
				if f.Name == "META-INF/encryption.xml" {
					t.Fatal("obsolete encryption descriptor retained")
				}
				if f.Name == "font.ttf" {
					r, _ := f.Open()
					actual, _ := io.ReadAll(r)
					r.Close()
					if !bytes.Equal(actual, original) {
						t.Fatal("font not restored exactly")
					}
				}
			}
			files["META-INF/encryption.xml"] = strings.Replace(files["META-INF/encryption.xml"], tc.algorithm, "urn:DRM:unsupported", 1)
			b = archive(t, files)
			if e := Prepare(context.Background(), bytes.NewReader(b), int64(len(b)), io.Discard); e == nil {
				t.Fatal("unknown encryption accepted")
			}
		})
	}
}
