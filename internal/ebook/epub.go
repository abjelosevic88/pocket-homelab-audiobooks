// Package ebook prepares untrusted EPUB archives for the browser reader. Originals
// remain unchanged; only the authenticated reader receives this derived archive.
package ebook

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"path"
	"strings"

	xhtml "golang.org/x/net/html"
)

const MaxArchive = 128 << 20
const MaxExpanded = 256 << 20
const maxEntry = 32 << 20
const Policy = "default-src 'none'; img-src blob: data:; style-src 'unsafe-inline' blob: data:; font-src blob: data:; media-src blob: data:; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'"

func Prepare(ctx context.Context, source io.ReaderAt, size int64, target io.Writer) error {
	if size <= 0 || size > MaxArchive {
		return errors.New("EPUB exceeds the 128 MiB reader limit")
	}
	z, err := zip.NewReader(source, size)
	if err != nil {
		return errors.New("invalid EPUB archive")
	}
	if len(z.File) > 10000 {
		return errors.New("EPUB contains too many files")
	}
	seen := map[string]bool{}
	var total uint64
	for _, f := range z.File {
		n := f.Name
		if n == "" || strings.HasPrefix(n, "/") || strings.Contains(n, "\\") || path.Clean(n) != strings.TrimSuffix(n, "/") || n == ".." || strings.HasPrefix(n, "../") || f.Mode()&fs.ModeSymlink != 0 {
			return errors.New("unsafe EPUB archive path")
		}
		if seen[n] {
			return errors.New("duplicate EPUB archive path")
		}
		seen[n] = true
		if f.UncompressedSize64 > maxEntry {
			return errors.New("EPUB entry exceeds reader limit")
		}
		total += f.UncompressedSize64
		if total > MaxExpanded {
			return errors.New("expanded EPUB exceeds reader limit")
		}
	}
	if !seen["META-INF/container.xml"] {
		return errors.New("EPUB package descriptor is missing")
	}
	masks, err := fontMasks(z)
	if err != nil {
		return err
	}
	out := zip.NewWriter(target)
	// The mimetype entry must be first and uncompressed.
	m, err := out.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err = io.WriteString(m, "application/epub+zip"); err != nil {
		return err
	}
	for _, f := range z.File {
		if err = ctx.Err(); err != nil {
			return err
		}
		if f.FileInfo().IsDir() || (f.Name == "mimetype" || f.Name == "META-INF/encryption.xml") {
			continue
		}
		r, e := f.Open()
		if e != nil {
			return e
		}
		b, e := io.ReadAll(io.LimitReader(r, maxEntry+1))
		r.Close()
		if e != nil {
			return e
		}
		if len(b) > maxEntry {
			return errors.New("EPUB entry exceeds reader limit")
		}
		if mask, ok := masks[f.Name]; ok {
			for i := 0; i < len(b) && i < mask.count; i++ {
				b[i] ^= mask.key[i%len(mask.key)]
			}
		}
		switch strings.ToLower(path.Ext(f.Name)) {
		case ".xhtml", ".html", ".htm", ".svg":
			b, e = sanitize(b, strings.HasSuffix(strings.ToLower(f.Name), ".svg"))
			if e != nil {
				return e
			}
		case ".opf", ".xml", ".ncx":
			// XML parsers must not resolve entities or external resources. The browser's
			// DOMParser is non-fetching; reject DTDs as well for a deterministic package.
			if bytes.Contains(bytes.ToUpper(b), []byte("<!ENTITY")) {
				return errors.New("EPUB package DTDs are unsupported")
			}
		}
		w, e := out.Create(f.Name)
		if e != nil {
			return e
		}
		if _, e = w.Write(b); e != nil {
			return e
		}
	}
	return out.Close()
}

func sanitize(b []byte, svg bool) ([]byte, error) {
	doc, err := xhtml.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	var head, svgRoot *xhtml.Node
	var clean func(*xhtml.Node)
	clean = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			switch strings.ToLower(n.Data) {
			case "head":
				head = n
			case "svg":
				if svgRoot == nil {
					svgRoot = n
				}
			case "script", "iframe", "object", "embed", "applet", "base", "meta", "animate", "animatemotion", "animatetransform", "set", "foreignobject":
				// Preserve element positions for ordinary EPUB CFIs, but remove behavior.
				n.Data = "span"
				n.Attr = nil
				for n.FirstChild != nil {
					n.RemoveChild(n.FirstChild)
				}
			case "style":
				if svg {
					for n.FirstChild != nil {
						n.RemoveChild(n.FirstChild)
					}
				}
			case "form":
				n.Data = "div"
			}
			attrs := n.Attr[:0]
			for _, a := range n.Attr {
				key := strings.ToLower(a.Key)
				if svg && (key == "style" || key == "filter") {
					continue
				}
				if strings.HasPrefix(key, "on") || key == "srcdoc" || key == "action" || key == "formaction" || key == "target" || key == "ping" || key == "srcset" {
					continue
				}
				if key == "href" || key == "src" || key == "poster" || key == "data" {
					v := strings.TrimSpace(a.Val)
					if svg && !strings.HasPrefix(v, "#") {
						continue
					}
					// Only relative archive references and fragment links survive. Resource
					// replacement will turn those into blob URLs before rendering.
					if strings.Contains(v, ":") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\r\n\t\\") {
						continue
					}
				}
				attrs = append(attrs, a)
			}
			n.Attr = attrs
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			clean(child)
		}
	}
	clean(doc)
	if head != nil {
		meta := &xhtml.Node{Type: xhtml.ElementNode, Data: "meta", Attr: []xhtml.Attribute{{Key: "http-equiv", Val: "Content-Security-Policy"}, {Key: "content", Val: Policy}}}
		head.InsertBefore(meta, head.FirstChild)
	}
	var out bytes.Buffer
	// XHTML must stay well formed; html.Render emits HTML-style void elements.
	var render func(*xhtml.Node) error
	render = func(n *xhtml.Node) error {
		switch n.Type {
		case xhtml.TextNode:
			out.WriteString(html.EscapeString(n.Data))
		case xhtml.ElementNode:
			out.WriteString("<" + n.Data)
			hasNS := false
			for _, a := range n.Attr {
				k := a.Key
				if a.Namespace != "" {
					k = a.Namespace + ":" + k
				}
				if k == "xmlns" {
					hasNS = true
				}
				fmt.Fprintf(&out, " %s=\"%s\"", k, html.EscapeString(a.Val))
			}
			if n.Data == "html" && !hasNS {
				out.WriteString(` xmlns="http://www.w3.org/1999/xhtml"`)
			}
			if n.FirstChild == nil {
				out.WriteString(" />")
				return nil
			}
			out.WriteByte('>')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := render(c); err != nil {
				return err
			}
		}
		if n.Type == xhtml.ElementNode {
			out.WriteString("</" + n.Data + ">")
		}
		return nil
	}
	if svg {
		if svgRoot == nil {
			return nil, errors.New("invalid SVG image")
		}
		doc = svgRoot
		has := false
		for _, a := range doc.Attr {
			if a.Key == "xmlns" {
				has = true
			}
		}
		if !has {
			doc.Attr = append(doc.Attr, xhtml.Attribute{Key: "xmlns", Val: "http://www.w3.org/2000/svg"})
		}
	}
	err = render(doc)
	return out.Bytes(), err
}
