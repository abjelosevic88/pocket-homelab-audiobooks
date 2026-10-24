package ebook

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"path"
	"strings"
)

type fontMask struct {
	key   []byte
	count int
}

// EPUB font obfuscation is part of the publication format, not DRM. Decode only
// the two published font algorithms; encrypted book content remains unsupported.
// IDPF: https://www.w3.org/TR/epub-33/#sec-font-obfuscation
func fontMasks(z *zip.Reader) (map[string]fontMask, error) {
	entries := map[string]*zip.File{}
	for _, f := range z.File {
		entries[f.Name] = f
	}
	masks := map[string]fontMask{}
	if entries["META-INF/encryption.xml"] == nil {
		return masks, nil
	}
	readXML := func(name string, out any) error {
		f := entries[name]
		if f == nil || f.UncompressedSize64 > 1<<20 {
			return errors.New("invalid EPUB font descriptor")
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		return xml.NewDecoder(io.LimitReader(r, (1<<20)+1)).Decode(out)
	}
	var encryption struct {
		Data []struct {
			Method struct {
				Algorithm string `xml:"Algorithm,attr"`
			} `xml:"EncryptionMethod"`
			Cipher struct {
				Reference struct {
					URI string `xml:"URI,attr"`
				} `xml:"CipherReference"`
			} `xml:"CipherData"`
		} `xml:"EncryptedData"`
	}
	if err := readXML("META-INF/encryption.xml", &encryption); err != nil {
		return nil, errors.New("invalid EPUB encryption descriptor")
	}
	if len(encryption.Data) == 0 {
		return masks, nil
	}
	var container struct {
		Files []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := readXML("META-INF/container.xml", &container); err != nil || len(container.Files) == 0 {
		return nil, errors.New("EPUB package descriptor missing")
	}
	var pkg struct {
		Unique string `xml:"unique-identifier,attr"`
		IDs    []struct {
			ID    string `xml:"id,attr"`
			Value string `xml:",chardata"`
		} `xml:"metadata>identifier"`
	}
	if err := readXML(container.Files[0].Path, &pkg); err != nil {
		return nil, err
	}
	identifier := ""
	for _, id := range pkg.IDs {
		if id.ID == pkg.Unique {
			identifier = strings.Map(func(r rune) rune {
				if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
					return -1
				}
				return r
			}, id.Value)
		}
	}
	if identifier == "" {
		return nil, errors.New("obfuscated EPUB font has no publication identifier")
	}
	for _, d := range encryption.Data {
		name, err := url.PathUnescape(d.Cipher.Reference.URI)
		if err != nil || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || entries[name] == nil {
			return nil, errors.New("invalid EPUB font reference")
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".ttf", ".otf", ".woff", ".woff2":
		default:
			return nil, errors.New("encrypted EPUB content requires an external reader")
		}
		var m fontMask
		switch d.Method.Algorithm {
		case "http://www.idpf.org/2008/embedding":
			sum := sha1.Sum([]byte(identifier))
			m = fontMask{sum[:], 1040}
		case "http://ns.adobe.com/pdf/enc#RC":
			uuid := strings.ReplaceAll(strings.TrimPrefix(strings.ToLower(identifier), "urn:uuid:"), "-", "")
			key, err := hex.DecodeString(uuid)
			if err != nil || len(key) != 16 {
				return nil, errors.New("invalid Adobe font publication identifier")
			}
			m = fontMask{key, 1024}
		default:
			return nil, errors.New("encrypted EPUB content requires an external reader")
		}
		if _, ok := masks[name]; ok {
			return nil, errors.New("duplicate EPUB font mask")
		}
		masks[name] = m
	}
	return masks, nil
}
