package metadata

import "testing"

func TestScoreKeepsDramatizationsAndReadingsApart(t *testing.T) {
	ga := MatchQuery{Title: "Choice Of The Cat", Authors: []string{"E.E. Knight"}, Duration: 11.4 * 3600, Narrators: []string{"GraphicAudio"}}
	reading := Candidate{Title: "Choice of the Cat", Authors: []string{"E.E. Knight"}, Duration: 11.5 * 3600, Metadata: map[string]any{"narrators": []any{map[string]any{"name": "Christian Rummel"}}}}
	if s := ScoreCandidate(ga, reading); s.Confident || s.Edition {
		t.Fatalf("dramatization matched the single-narrator reading: %+v", s)
	}
	drama := Candidate{Title: "Choice of the Cat [Dramatized Adaptation]", Authors: []string{"E.E. Knight"}, Duration: 11.4 * 3600, Metadata: map[string]any{"narrators": []any{"full cast"}}}
	if s := ScoreCandidate(ga, drama); !s.Confident {
		t.Fatalf("dramatized edition not confident: %+v", s)
	}
	plain := MatchQuery{Title: "Choice of the Cat", Authors: []string{"E.E. Knight"}, Duration: 11.4 * 3600}
	if s := ScoreCandidate(plain, drama); s.Confident {
		t.Fatalf("reading matched a dramatization: %+v", s)
	}
}

func TestScoreRuntimeGates(t *testing.T) {
	q := MatchQuery{Title: "Trading in Danger", Authors: []string{"Elizabeth Moon"}, Duration: 15 * 3600}
	near := Candidate{Title: "Trading in Danger", Authors: []string{"Elizabeth Moon"}, Duration: 14 * 3600}
	far := Candidate{Title: "Trading in Danger", Authors: []string{"Elizabeth Moon"}, Duration: 12 * 3600}
	if !ScoreCandidate(q, near).Confident || ScoreCandidate(q, far).Confident {
		t.Fatal("runtime gate should accept ~7% and reject 20% differences")
	}
	// A stored ASIN that points at part 1 of 3 must not vouch for the whole book.
	part := Candidate{Title: "Legacy (1 of 3) [Dramatized Adaptation]", Authors: []string{"A"}, Duration: 6.1 * 3600, Metadata: map[string]any{"asin": "B0X"}}
	whole := MatchQuery{Title: "Legacy - 4 - Questionable Choices", Authors: []string{"A"}, Duration: 3.3 * 3600, ASIN: "B0X", Narrators: []string{"Graphic Audio"}}
	if s := ScoreCandidate(whole, part); !s.Exact || s.Confident {
		t.Fatalf("exact ASIN with far runtime should not be confident: %+v", s)
	}
}
