package library

import (
	"reflect"
	"testing"
)

func TestParseBookPath(t *testing.T) {
	for _, tc := range []struct {
		rel  string
		want pathMetadata
	}{
		{"Book Title", pathMetadata{Title: "Book Title"}},
		{"Author Name/Book Title", pathMetadata{Title: "Book Title", Author: "Author Name"}},
		{"Author/Series/Book 3 - Title", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "3"}},
		{"Author/Series/3 - Title", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "3"}},
		{"Author/Series/#3 - Title", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "3"}},
		{"Author/Series/Vol. 03 - Title", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "3"}},
		{"Author/Series/Volume 1.5 - Title", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "1.5"}},
		{"Author/Series/2. Title", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "2"}},
		{"Author/Series/Title - Book 4", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "4"}},
		// A bare number followed by words is part of the title.
		{"Author/Series/3 Body Problem", pathMetadata{Title: "3 Body Problem", Author: "Author", Series: "Series"}},
		// Without a series folder no sequence is taken from the name.
		{"Author/Book 3 - Title", pathMetadata{Title: "Book 3 - Title", Author: "Author"}},
		{"Author/2019 - Title", pathMetadata{Title: "Title", Author: "Author", Year: "2019"}},
		{"Author/(2019) - Title", pathMetadata{Title: "Title", Author: "Author", Year: "2019"}},
		{"Author/Series/1 - 1984 - Title", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "1", Year: "1984"}},
		{"Author/Series/1984", pathMetadata{Title: "1984", Author: "Author", Series: "Series"}},
		{"Author/Title {Jim Dale}", pathMetadata{Title: "Title", Author: "Author", Narrators: []string{"Jim Dale"}}},
		{"Author/Title {A Reader, B Reader}", pathMetadata{Title: "Title", Author: "Author", Narrators: []string{"A Reader", "B Reader"}}},
		{"Author/Series/Book 2 - Title {Narr Ator}", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "2", Narrators: []string{"Narr Ator"}}},
		{"Author/Title - Subtitle", pathMetadata{Title: "Title - Subtitle", Author: "Author"}},
		{"Author/Title [B0ABCDEFGH]", pathMetadata{Title: "Title", Author: "Author", ASIN: "B0ABCDEFGH"}},
		// Only the last three folders matter.
		{"Fiction/Author/Series/Book 1 - Title", pathMetadata{Title: "Title", Author: "Author", Series: "Series", Sequence: "1"}},
	} {
		got := parseBookPath(tc.rel)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %+v want %+v", tc.rel, got, tc.want)
		}
	}
}

func TestSplitNames(t *testing.T) {
	for in, want := range map[string][]string{
		"Stephen King":                      {"Stephen King"},
		"King, Stephen":                     {"King, Stephen"},
		"Tolkien, J.R.R.":                   {"Tolkien, J.R.R."},
		"Stephen King, Peter Straub":        {"Stephen King", "Peter Straub"},
		"A, B, C":                           {"A", "B", "C"},
		"Stephen King & Peter Straub":       {"Stephen King", "Peter Straub"},
		"Stephen King; Peter Straub":        {"Stephen King", "Peter Straub"},
		"Stephen King and Peter Straub":     {"Stephen King", "Peter Straub"},
		"Friedman, Milton & Friedman, Rose": {"Friedman, Milton", "Friedman, Rose"},
		" ":                                 {},
	} {
		if got := splitNames(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestReadTagsAndYear(t *testing.T) {
	for in, want := range map[string]string{"2019-05-01T00:00:00Z": "2019", "(p) 2004 Publisher": "2004", "2019": "2019", "May 12": "", "20190": ""} {
		if got := yearOf(in); got != want {
			t.Errorf("year %q: %q", in, got)
		}
	}
	got := readTags(map[string]string{"album": "Album", "artist": "A One & B Two", "composer": "Reader", "date": "2001-02-03", "mvnm": "Saga", "mvin": "4/9", "asin": "B0XXXXXXXX", "isbn": "978", "subtitle": "Sub", "publisher": "Pub", "comment": "Desc"}, false)
	want := tagMetadata{Title: "Album", Authors: []string{"A One", "B Two"}, Narrators: []string{"Reader"}, Year: "2001", Series: "Saga", Sequence: "4", ASIN: "B0XXXXXXXX", ISBN: "978", Subtitle: "Sub", Publisher: "Pub", Description: "Desc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tags %+v", got)
	}
	if got := readTags(map[string]string{"title": "Only", "album_artist": "Primary", "artist": "Other", "grouping": "Saga, Book 2"}, true); got.Title != "Only" || got.Authors[0] != "Primary" || got.Series != "Saga" || got.Sequence != "2" {
		t.Fatalf("single file %+v", got)
	}
	if got := readTags(map[string]string{"title": "Track", "grouping": "Fiction"}, false); got.Title != "" || got.Series != "" {
		t.Fatalf("grouping without sequence is not a series %+v", got)
	}
}

func TestClampChapters(t *testing.T) {
	got := clampChapters([]Chapter{{Start: 0, End: 10, Title: "a"}, {Start: 10, End: 21, Title: "b"}, {Start: 20, End: 25, Title: "past"}, {Start: 5, End: 1, Title: "bad"}}, 20)
	want := []Chapter{{ID: 0, Start: 0, End: 10, Title: "a"}, {ID: 1, Start: 10, End: 20, Title: "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestParseOPF(t *testing.T) {
	data := []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
 <metadata>
  <dc:title>OPF Title</dc:title>
  <dc:creator opf:role="aut">Jane Writer</dc:creator>
  <dc:creator opf:role="nrt">Nick Reader</dc:creator>
  <dc:creator id="c3">Second Writer</dc:creator>
  <dc:description>About it</dc:description>
  <dc:publisher>House</dc:publisher>
  <dc:date>2011-03-04</dc:date>
  <dc:language>en</dc:language>
  <dc:subject>Fantasy</dc:subject>
  <dc:identifier opf:scheme="ISBN">9781234567890</dc:identifier>
  <dc:identifier>urn:asin:B0OPFASIN1</dc:identifier>
  <meta name="calibre:series" content="Saga"/>
  <meta name="calibre:series_index" content="2.0"/>
 </metadata>
</package>`)
	got, err := parseOPF(data)
	if err != nil {
		t.Fatal(err)
	}
	m := got.Metadata
	if m["title"] != "OPF Title" || m["description"] != "About it" || m["publisher"] != "House" || m["publishedYear"] != "2011" || m["language"] != "en" || m["isbn"] != "9781234567890" || m["asin"] != "B0OPFASIN1" {
		t.Fatalf("fields %+v", m)
	}
	authors := m["authors"].([]any)
	if len(authors) != 2 || authors[0].(map[string]any)["name"] != "Jane Writer" || authors[1].(map[string]any)["name"] != "Second Writer" {
		t.Fatalf("authors %+v", authors)
	}
	if n := m["narrators"].([]any); len(n) != 1 || n[0] != "Nick Reader" {
		t.Fatalf("narrators %+v", n)
	}
	if s := m["series"].([]any)[0].(map[string]any); s["name"] != "Saga" || s["sequence"] != "2" {
		t.Fatalf("series %+v", s)
	}
	if _, err = parseOPF([]byte("<package><metadata>")); err == nil {
		t.Fatal("truncated OPF accepted")
	}
}

func TestBlankSidecarTitleIsIgnored(t *testing.T) {
	got, err := parseSidecar([]byte(`{"title":"  ","subtitle":"kept"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Metadata["title"]; ok || got.Metadata["subtitle"] != "kept" {
		t.Fatalf("%+v", got.Metadata)
	}
}
