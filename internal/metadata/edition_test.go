package metadata

import "testing"

func TestDramatizedFlags(t *testing.T) {
	ga := MatchQuery{Title: "Originally Human", Publisher: "GraphicAudio"}
	reading := Candidate{Title: "Originally Human", Metadata: map[string]any{"narrators": []any{"A Reader"}, "publisher": "Podium Audio"}}
	drama := Candidate{Title: "Originally Human", Metadata: map[string]any{"subtitle": "Dramatized Adaptation"}}
	if !ga.Dramatized() || CandidateDramatized(reading) || !CandidateDramatized(drama) {
		t.Fatal("edition flags")
	}
	if ScoreCandidate(ga, reading).Edition || !ScoreCandidate(ga, drama).Edition {
		t.Fatal("edition score must keep a dramatization apart from a reading")
	}
}
