package metadata

import "testing"

func cand(title string, authors []string, duration float64, meta map[string]any) Candidate {
	if meta == nil {
		meta = map[string]any{}
	}
	return Candidate{ID: title, Title: title, Authors: authors, Duration: duration, Metadata: meta}
}

func TestCleanTitleAndAuthors(t *testing.T) {
	for in, want := range map[string]string{
		"Project Hail Mary (Unabridged)":         "Project Hail Mary",
		"Ender's Game (Ender's Saga, Book 1)":    "Ender's Game",
		"[2019] Recursion - Unabridged":          "Recursion",
		"01 - The Hobbit":                        "The Hobbit",
		"1984":                                   "1984",
		"(Unabridged)":                           "(Unabridged)",
		"Dune: Dramatized Audiobook [Full Cast]": "Dune",
	} {
		if got := CleanTitle(in); got != want {
			t.Errorf("CleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if a := SplitAuthors("Terry Pratchett, Neil Gaiman & Someone and Other"); len(a) != 4 || a[1] != "Neil Gaiman" {
		t.Fatal(a)
	}
}

func TestScoringRanksAndGatesConfidence(t *testing.T) {
	q := MatchQuery{Title: "The Way of Kings (Unabridged)", Authors: []string{"Sanderson, Brandon"}, Duration: 2730*60 + 30}
	ranked := Rank(q, []Candidate{
		cand("Words of Radiance", []string{"Brandon Sanderson"}, 2900*60, nil),
		cand("Way of Kings", []string{"Brandon Sanderson"}, 2730*60, nil),
		cand("The Way of Kings", []string{"Someone Else"}, 0, nil),
	})
	if ranked[0].Title != "Way of Kings" || !ranked[0].Score.Confident || ranked[0].Score.Title != 1 || ranked[0].Score.Author != 1 || ranked[0].Score.Duration != 1 {
		t.Fatalf("best %+v", ranked[0])
	}
	for _, r := range ranked[1:] {
		if r.Score.Confident {
			t.Fatalf("wrong book or author is confident: %+v", r)
		}
	}
	// Initials and reordered names still match the author.
	if s := ScoreCandidate(MatchQuery{Title: "The Hobbit", Authors: []string{"J.R.R. Tolkien"}}, cand("The Hobbit", []string{"J. R. R. Tolkien"}, 0, nil)); !s.Confident {
		t.Fatalf("initials %+v", s)
	}
	// A runtime far from the book's rejects an otherwise perfect match (abridged/other edition).
	if s := ScoreCandidate(MatchQuery{Title: "Dune", Authors: []string{"Frank Herbert"}, Duration: 3600 * 21}, cand("Dune", []string{"Frank Herbert"}, 3600*5, nil)); s.Confident || s.Duration != 0 {
		t.Fatalf("duration gate %+v", s)
	}
	// Exact identifiers win regardless of title.
	if s := ScoreCandidate(MatchQuery{Title: "Track 01", ASIN: "B003ZWFO7E"}, cand("The Way of Kings", nil, 0, map[string]any{"asin": "B003ZWFO7E"})); !s.Exact || !s.Confident || s.Total != 1 {
		t.Fatalf("asin %+v", s)
	}
	if s := ScoreCandidate(MatchQuery{Title: "x", ISBN: "978-1-4272-0976-4"}, cand("y", nil, 0, map[string]any{"isbn": "9781427209764"})); !s.Exact {
		t.Fatalf("isbn %+v", s)
	}
	// Missing author on our side needs a near-identical title.
	if s := ScoreCandidate(MatchQuery{Title: "Dune Messiah"}, cand("Dune", []string{"Frank Herbert"}, 0, nil)); s.Confident {
		t.Fatalf("no author %+v", s)
	}
	if s := ScoreCandidate(MatchQuery{Title: "Dune"}, cand("Dune", []string{"Frank Herbert"}, 0, nil)); !s.Confident {
		t.Fatalf("no author exact title %+v", s)
	}
	// A candidate without authors cannot confidently match a book that has one.
	if s := ScoreCandidate(MatchQuery{Title: "Dune", Authors: []string{"Frank Herbert"}}, cand("Dune", nil, 0, nil)); s.Confident {
		t.Fatalf("missing candidate author %+v", s)
	}
}
