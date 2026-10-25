package library

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

type opfText struct {
	ID     string `xml:"id,attr"`
	Role   string `xml:"role,attr"`
	Scheme string `xml:"scheme,attr"`
	Text   string `xml:",chardata"`
}

type opfPackage struct {
	Metadata struct {
		Titles       []opfText `xml:"title"`
		Creators     []opfText `xml:"creator"`
		Contributors []opfText `xml:"contributor"`
		Description  []opfText `xml:"description"`
		Publisher    []opfText `xml:"publisher"`
		Date         []opfText `xml:"date"`
		Language     []opfText `xml:"language"`
		Subjects     []opfText `xml:"subject"`
		Identifiers  []opfText `xml:"identifier"`
		Meta         []struct {
			Name     string `xml:"name,attr"`
			Content  string `xml:"content,attr"`
			Property string `xml:"property,attr"`
			Refines  string `xml:"refines,attr"`
			Text     string `xml:",chardata"`
		} `xml:"meta"`
	} `xml:"metadata"`
}

// parseOPF reads Calibre/EPUB package metadata into the same validated edit
// model as metadata.json. Go's XML decoder never resolves external entities.
func parseOPF(data []byte) (EditOptions, error) {
	var pkg opfPackage
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	if err := dec.Decode(&pkg); err != nil {
		return EditOptions{}, fmt.Errorf("%w: invalid OPF: %v", ErrInvalid, err)
	}
	m := pkg.Metadata
	first := func(list []opfText) string {
		for _, v := range list {
			if t := strings.TrimSpace(v.Text); t != "" {
				return t
			}
		}
		return ""
	}
	// EPUB 3 declares creator roles with <meta refines="#id" property="role">.
	roles := map[string]string{}
	values := sourceRow{}
	for _, meta := range m.Meta {
		switch {
		case meta.Property == "role" && strings.HasPrefix(meta.Refines, "#"):
			roles[strings.TrimPrefix(meta.Refines, "#")] = strings.TrimSpace(meta.Text)
		case meta.Name == "calibre:series" && strings.TrimSpace(meta.Content) != "":
			values["calibre:series"] = strings.TrimSpace(meta.Content)
		case meta.Name == "calibre:series_index":
			values["calibre:series_index"] = normalizeSequence(meta.Content)
		}
	}
	authors, narrators := []any{}, []any{}
	for i, c := range append(append([]opfText{}, m.Creators...), m.Contributors...) {
		name := strings.TrimSpace(c.Text)
		role := strings.ToLower(strings.TrimSpace(c.Role))
		if role == "" {
			role = strings.ToLower(roles[c.ID])
		}
		creator := i < len(m.Creators)
		switch {
		case name == "":
		case role == "nrt":
			narrators = append(narrators, name)
		case role == "aut" || (role == "" && creator):
			authors = append(authors, name)
		}
	}
	if v := first(m.Titles); v != "" {
		values["title"] = v
	}
	if len(authors) > 0 {
		values["authors"] = authors
	}
	if len(narrators) > 0 {
		values["narrators"] = narrators
	}
	for k, list := range map[string][]opfText{"description": m.Description, "publisher": m.Publisher, "language": m.Language} {
		if v := first(list); v != "" {
			values[k] = v
		}
	}
	if y := yearOf(first(m.Date)); y != "" {
		values["publishedYear"] = y
	}
	genres := []any{}
	for _, s := range m.Subjects {
		if t := strings.TrimSpace(s.Text); t != "" {
			genres = append(genres, t)
		}
	}
	if len(genres) > 0 {
		values["genres"] = genres
	}
	for _, id := range m.Identifiers {
		text := strings.TrimSpace(id.Text)
		lower := strings.ToLower(text)
		scheme := strings.ToLower(strings.TrimSpace(id.Scheme))
		switch {
		case scheme == "isbn" || strings.HasPrefix(lower, "urn:isbn:") || strings.HasPrefix(lower, "isbn:"):
			if _, ok := values["isbn"]; !ok && text != "" {
				values["isbn"] = text[strings.LastIndex(text, ":")+1:]
			}
		case scheme == "asin" || scheme == "amazon" || scheme == "mobi-asin" || strings.HasPrefix(lower, "urn:asin:") || strings.HasPrefix(lower, "asin:"):
			if _, ok := values["asin"]; !ok && text != "" {
				values["asin"] = text[strings.LastIndex(text, ":")+1:]
			}
		}
	}
	if name, ok := values["calibre:series"].(string); ok {
		entry := map[string]any{"name": name, "sequence": ""}
		if seq, ok := values["calibre:series_index"].(string); ok {
			entry["sequence"] = seq
		}
		values["series"] = []any{entry}
	}
	delete(values, "calibre:series")
	delete(values, "calibre:series_index")
	return parseSidecar([]byte(raw(values)))
}
