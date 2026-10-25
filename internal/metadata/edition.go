package metadata

// Dramatized reports whether the library book is a full-cast production
// (GraphicAudio and similar), judged from its title, subtitle, narrators and
// publisher. The match review shows it next to each candidate's edition so the
// dramatization-versus-reading protection stays visible.
func (q MatchQuery) Dramatized() bool { return q.dramatized() }

// CandidateDramatized reports whether a provider result is a full-cast production.
func CandidateDramatized(c Candidate) bool { return candidateDramatized(c) }
