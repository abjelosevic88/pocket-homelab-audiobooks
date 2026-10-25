package library

import "testing"

func TestPickEbookKeepsCurrentThenPrefersEPUB(t *testing.T) {
	file := func(rel string) sourceRow {
		return sourceRow{"metadata": map[string]any{"relPath": rel, "filename": rel}}
	}
	mobi, epub, guide := file("Book.mobi"), file("Book.epub"), file("Book [Companion].epub")
	if got := pickEbook([]sourceRow{mobi, epub}, nil); str(object(got["metadata"])["relPath"]) != "Book.epub" {
		t.Fatalf("new book picked %v, want EPUB", got)
	}
	if got := pickEbook([]sourceRow{epub, guide, mobi}, file("Book [Companion].epub")); str(object(got["metadata"])["relPath"]) != "Book [Companion].epub" {
		t.Fatalf("rescan replaced the chosen ebook with %v", got)
	}
	disc1, disc2 := file("Author/Book/CD 01/Book.epub"), file("Author/Book/CD 02/Book.epub")
	if got := pickEbook([]sourceRow{disc1, disc2}, file("CD 02/Book.epub")); str(object(got["metadata"])["relPath"]) != "Author/Book/CD 02/Book.epub" {
		t.Fatalf("book-relative imported path matched %v", got)
	}
	if got := pickEbook([]sourceRow{mobi, epub}, file("Gone.epub")); str(object(got["metadata"])["relPath"]) != "Book.epub" {
		t.Fatalf("missing current ebook fell back to %v, want EPUB", got)
	}
}
