package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Tidy reasons, in the order the rules run. The web app groups the preview
// by these keys ("select by reason").
const (
	TidyFileName      = "file_name"       // title is an audio file name
	TidyEditionSuffix = "edition_suffix"  // " - GA", "(Unabridged)", "[GraphicAudio]"
	TidyYearSuffix    = "year_suffix"     // "Title (2003)"
	TidyTrackNumber   = "track_number"    // "01a.Originally Human"
	TidyVolumePrefix  = "volume_prefix"   // "Vol 3 - Title"
	TidySeriesInTitle = "series_in_title" // "Mistborn - 1 - The Final Empire"
	TidyAuthorInTitle = "author_in_title" // "Brandon Sanderson Yumi and ..."
	TidySeriesPrefix  = "series_prefix"   // "<Series> - Title"
	TidyWrapped       = "wrapped"         // "(Title)", "[Title]"
	TidyPlaceholder   = "placeholder"     // "[01]", "061 @", "III", "[Mack Bolan #080]"
	TidyFragment      = "fragment"        // "and the Mechanical Girl"
	TidyCapitalise    = "capitalisation"  // "superman - Doomsday", "THE HOBBIT"
	TidySeries        = "series"          // series filled from folder/subtitle/title
	TidySequence      = "sequence"        // series number filled in
	TidySubtitle      = "subtitle"        // subtitle cleared or filled
)

// Confidence levels of a proposal: the lowest confidence of its rules.
const (
	TidyLow    = 1
	TidyMedium = 2
	TidyHigh   = 3
)

type tidySeries struct{ Name, Sequence string }

// tidyInput is everything the rules look at for one book.
type tidyInput struct {
	Title, Subtitle, Author string
	Series                  []tidySeries
	Rel                     string   // book folder relative to its library folder
	Album                   string   // album tag of the first audio file
	Files                   []string // audio file names in play order
	Edited                  map[string]bool
	// SeriesTwins is true when another book of the same series has exactly
	// the same title (four "Great American Westerns" volumes).
	SeriesTwins bool
}

type tidyProposal struct {
	Title, Subtitle   string
	SetTitle, SetSub  bool
	Series            *tidySeries
	Reasons           []string
	Confidence        int
	Unresolved        string // a broken title nothing could repair
	confidenceByRules map[string]int
}

func (p *tidyProposal) add(reason string, confidence int) {
	if p.confidenceByRules == nil {
		p.confidenceByRules = map[string]int{}
	}
	if old, ok := p.confidenceByRules[reason]; ok {
		p.confidenceByRules[reason] = min(old, confidence)
	} else {
		p.Reasons = append(p.Reasons, reason)
		p.confidenceByRules[reason] = confidence
	}
	if p.Confidence == 0 || confidence < p.Confidence {
		p.Confidence = confidence
	}
}

func (p tidyProposal) empty() bool { return !p.SetTitle && !p.SetSub && p.Series == nil }

var (
	tidyAudioExt      = regexp.MustCompile(`(?i)\.(mp3|m4b|m4a|mp4|flac|ogg|opus|wav|aac|wma|aiff)$`)
	tidyEdition       = regexp.MustCompile(`(?i)(?:\s+-\s*(?:GA|graphic\s*audio)|\s*[\(\[](?:GA|graphic\s*audio|unabridged|abridged|dramati[sz]ed(?:\s+adaptation)?|full[\s-]cast(?:\s+dramati[sz]ation)?|audio\s*book|audiobook)[\)\]]|\s+-\s*unabridged)\s*$`)
	tidyYearWrapped   = regexp.MustCompile(`^(.*\S)\s*[\(\[]((?:19|20)\d\d)[\)\]]$`)
	tidyYearBare      = regexp.MustCompile(`^(\S+(?:\s+\S+)+)\s+((?:19|20)\d\d)$`)
	tidyTrack         = regexp.MustCompile(`^(\d{1,3})([a-z]?)(?:\s*[._)]\s*|\s+-\s+)(.*\pL.*)$`)
	tidyVolume        = regexp.MustCompile(`(?i)^(?:vol\.?|volume|book|#)\s*(\d{1,3}(?:\.\d{1,2})?)\s*(?:[-:.]\s*|\s+)(.*\pL.*)$`)
	tidySeriesNumber  = regexp.MustCompile(`^(.*\pL.*?)\s+-\s+#?(\d{1,3}(?:\.\d{1,2})?)\s+-\s+(.*\pL.*)$`)
	tidyRoman         = regexp.MustCompile(`^(?:X{0,3})(?:IX|IV|V?I{0,3})$`)
	tidyNoName        = regexp.MustCompile(`^[\d\s@#.,:;_~\-\[\]()]*$`)
	tidyNumberOnly    = regexp.MustCompile(`(?i)^(?:(?:vol\.?|volume|book|part|pt\.?|no\.?)\s*)?#?\s*\d{1,4}(?:\.\d{1,2})?$`)
	tidyNamedNumber   = regexp.MustCompile(`(?i)^(.*?)[\s~:,\-]*(?:#|\bvol\.?|\bvolume|\bbook|\bpart|\bno\.?)\s*(\d{1,4}(?:\.\d{1,2})?)$`)
	tidyBookOf        = regexp.MustCompile(`(?i)^(.*\pL.*?),?\s+book\s+(\d{1,3}(?:\.\d{1,2})?)$`)
	tidyJunkSubtitle  = regexp.MustCompile(`(?i)^\[?\s*(?:tagged|ripped|encoded|uploaded)\s+by\b.*$`)
	tidyMixedToken    = regexp.MustCompile(`^(?:\pL+\d+.*|\d+\pL+\d+.*)$`)
	tidyOrdinal       = regexp.MustCompile(`(?i)^\d+(?:st|nd|rd|th)$`)
	tidyPartMarker    = regexp.MustCompile(`(?i)[\s\-_.,]*(?:[\(\[]\s*)?(?:(?:disc|disk|cd|part|pt|p|track|chapter)\.?\s*\d{1,3}(?:\s*(?:of|/)\s*\d{1,3})?|\d{1,3}\s*(?:of|/)\s*\d{1,3}|\d{1,3}(?:-\d{1,3})?)(?:\s*[\)\]])?$`)
	tidyTagMarker     = regexp.MustCompile(`(?i)(?:\s*[\(\[]\s*(?:(?:disc|disk|cd|part|pt)\.?\s*)?\d{1,3}(?:\s*(?:of|/|-)\s*\d{1,3})?\s*[\)\]]|\s+-?\s*(?:disc|disk|cd)\s*\d{1,3}(?:\s*of\s*\d{1,3})?)$`)
	tidyBracketVolume = regexp.MustCompile(`(?i)\s*[\[\(]((?:vol\.?|volume|book|part)\s*\d{1,3})[\]\)]`)
	tidyZeroVolume    = regexp.MustCompile(`(?i)\b(vol\.?|volume|book|part)\s+0+(\d)`)
	tidyBracketNumber = regexp.MustCompile(`\s*\[\d{1,3}\]\s*`)
	tidyYearTitle     = regexp.MustCompile(`^[12]\d{3}$`)
	tidyDateTitle     = regexp.MustCompile(`^\d{1,2}[./]\d{1,2}[./]\d{2,4}$`)
	tidyOfPart        = regexp.MustCompile(`(?i)^\s*(?:of|/)\s*\d`)
	tidyDigits        = regexp.MustCompile(`(\d{1,4})`)
	tidyFileYear      = regexp.MustCompile(`\s*[\(\[]?(?:19|20)\d\d[\)\]]?$`)
)

// proposeTidy runs the title, series and subtitle rules for one book.
func proposeTidy(in tidyInput) tidyProposal {
	p := tidyProposal{}
	path := parseBookPath(in.Rel)
	series := tidySeries{}
	if len(in.Series) > 0 {
		series = in.Series[0]
	}
	var newSeries *tidySeries // series to write, when it changes
	setSeries := func(name, seq string, reason string, conf int) {
		if in.Edited["series"] || name == "" {
			return
		}
		name = strings.TrimSpace(name)
		if series.Name == "" {
			series = tidySeries{Name: name, Sequence: seq}
			newSeries = &series
			p.add(TidySeries, conf)
			if reason != "" {
				p.add(reason, conf)
			}
			return
		}
		if strings.EqualFold(series.Name, name) && series.Sequence == "" && seq != "" {
			series.Sequence = seq
			newSeries = &series
			p.add(TidySequence, conf)
			if reason != "" {
				p.add(reason, conf)
			}
		}
	}

	title := strings.TrimSpace(in.Title)
	subtitle := strings.TrimSpace(in.Subtitle)
	fixTitle := !in.Edited["title"]
	if fixTitle {
		title = tidyTitle(&p, in, title, path, &series, setSeries, &subtitle)
	}

	// Series from the folder (Author/Series/Book) or "<Series>, Book N".
	if path.Series != "" {
		setSeries(path.Series, path.Sequence, "", TidyHigh)
	}
	if m := tidyBookOf.FindStringSubmatch(subtitle); m != nil {
		setSeries(m[1], normalizeSequence(m[2]), "", TidyMedium)
	}

	// Junk subtitles.
	if !in.Edited["subtitle"] {
		if subtitle != "" && (tidyJunkSubtitle.MatchString(subtitle) || strings.EqualFold(subtitle, title)) {
			subtitle = ""
			p.add(TidySubtitle, TidyHigh)
		}
		if subtitle != strings.TrimSpace(in.Subtitle) {
			p.Subtitle, p.SetSub = subtitle, true
		}
	}
	if fixTitle && title != in.Title && validTidyTitle(title) {
		p.Title, p.SetTitle = title, true
	}
	if newSeries != nil {
		p.Series = newSeries
	}
	if p.empty() {
		p.Reasons, p.Confidence = nil, 0
	}
	return p
}

// tidyTitle runs the title rules in order; each one sees the previous result.
func tidyTitle(p *tidyProposal, in tidyInput, title string, path pathMetadata, series *tidySeries, setSeries func(string, string, string, int), subtitle *string) string {
	stem := func(name string) string { return strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name))) }

	// The title is an audio file name: use the folder's title instead.
	if len(in.Files) > 0 && (tidyAudioExt.MatchString(title) || (len(in.Files) == 1 && strings.EqualFold(title, stem(in.Files[0])))) {
		if folder := cleanFolderTitle(path.Title); folder != "" && !tidyPlaceholder(folder, series.Name) && !strings.EqualFold(folder, title) {
			title = folder
			p.add(TidyFileName, TidyMedium)
		} else if t := tidyAudioExt.ReplaceAllString(title, ""); t != title {
			title = strings.TrimSpace(t)
			p.add(TidyFileName, TidyHigh)
		}
	}
	for {
		t := strings.TrimSpace(tidyEdition.ReplaceAllString(title, ""))
		if t == title || t == "" {
			break
		}
		title = t
		p.add(TidyEditionSuffix, TidyHigh)
	}
	if m := tidyYearWrapped.FindStringSubmatch(title); m != nil && strings.ContainsFunc(m[1], unicode.IsLetter) {
		title = strings.TrimSpace(m[1])
		p.add(TidyYearSuffix, TidyMedium)
	} else if m := tidyYearBare.FindStringSubmatch(title); m != nil && strings.Contains(title, " - ") {
		// Only "Series - Story 2006": a bare year can be part of a title.
		if y, _ := strconv.Atoi(m[2]); y <= time.Now().Year() {
			title = strings.TrimSpace(m[1])
			p.add(TidyYearSuffix, TidyLow)
		}
	}
	if m := tidyTrack.FindStringSubmatch(title); m != nil && !tidyPlaceholder(m[3], series.Name) {
		title = strings.TrimSpace(m[3])
		p.add(TidyTrackNumber, TidyHigh)
	}
	if m := tidyVolume.FindStringSubmatch(title); m != nil {
		rest, seq := strings.TrimSpace(m[2]), normalizeSequence(m[1])
		if a, b, ok := strings.Cut(rest, " - "); ok && series.Name == "" && path.Series == "" && !in.Edited["series"] && strings.ContainsFunc(b, unicode.IsLetter) {
			// "Vol 1 - X-Men Mutant Empire - Siege" without a series.
			setSeries(a, seq, TidyVolumePrefix, TidyMedium)
			rest = strings.TrimSpace(b)
		} else if series.Name != "" && strings.EqualFold(series.Name, rest) {
			setSeries(series.Name, seq, TidyVolumePrefix, TidyHigh)
		} else if series.Name == "" && path.Series != "" {
			setSeries(path.Series, seq, TidyVolumePrefix, TidyHigh)
		} else if series.Name == "" {
			// "Vol 1 - Blood Bond" in an author folder: the book opens a series of its name.
			setSeries(rest, seq, TidyVolumePrefix, TidyLow)
		}
		title = rest
		p.add(TidyVolumePrefix, TidyHigh)
	}
	if m := tidySeriesNumber.FindStringSubmatch(title); m != nil && !tidyPlaceholder(m[3], series.Name) {
		name, seq, rest := strings.TrimSpace(m[1]), normalizeSequence(m[2]), strings.TrimSpace(m[3])
		switch {
		case series.Name == "" || strings.EqualFold(series.Name, name):
			setSeries(name, seq, "", TidyHigh)
		case *subtitle == "" && !in.Edited["subtitle"]:
			// A sub-series inside another series: "Mistborn, Book 1" under Cosmere.
			*subtitle = name + ", Book " + seq
			p.add(TidySubtitle, TidyHigh)
		}
		title = rest
		p.add(TidySeriesInTitle, TidyHigh)
	}
	if author := strings.TrimSpace(in.Author); author != "" && len(title) > len(author)+3 && strings.EqualFold(title[:len(author)], author) {
		rest := strings.TrimLeft(title[len(author):], " -:")
		if title[len(author)] == ' ' && strings.ContainsFunc(rest, unicode.IsLetter) && !tidyPlaceholder(rest, series.Name) {
			title = rest
			p.add(TidyAuthorInTitle, TidyHigh)
		}
	}
	if name := series.Name; name != "" && len(title) > len(name)+3 && strings.EqualFold(title[:len(name)], name) {
		for _, sep := range []string{" - ", ": "} {
			if rest, ok := strings.CutPrefix(title[len(name):], sep); ok && strings.ContainsFunc(rest, unicode.IsLetter) && !tidyPlaceholder(rest, name) {
				title = strings.TrimSpace(rest)
				p.add(TidySeriesPrefix, TidyMedium)
				break
			}
		}
	}
	wrappedSeries := false
	if inner, ok := unwrapTitle(title); ok {
		if tidyPlaceholder(inner, series.Name) || strings.EqualFold(inner, series.Name) || subtitleSeries(*subtitle, inner) {
			// "(The Executioner)": only the series, repaired below like any placeholder.
			title, wrappedSeries = inner, true
		} else {
			title = inner
			p.add(TidyWrapped, TidyHigh)
		}
	}
	// A book named like its series is normal (book 1); only twins are broken.
	placeholder := wrappedSeries || tidyPlaceholder(title, series.Name) || (in.SeriesTwins && strings.EqualFold(title, series.Name))
	if placeholder {
		if t, conf := repairPlaceholder(in, title, path, *series); t != "" {
			title = t
			p.add(TidyPlaceholder, conf)
		} else {
			p.Unresolved = TidyPlaceholder
			return strings.TrimSpace(in.Title)
		}
	}
	if r, _ := firstRune(title); unicode.IsLower(r) {
		word, _, _ := strings.Cut(title, " ")
		switch strings.ToLower(word) {
		case "and", "in", "of", "vs", "vs.", "versus", "meets", "&":
			if series.Name != "" {
				title = series.Name + " " + title
				conf := TidyMedium
				if filesMention(in, title) {
					conf = TidyHigh
				}
				p.add(TidyFragment, conf)
			}
		default:
			title = string(unicode.ToUpper(r)) + title[len(string(r)):]
			p.add(TidyCapitalise, TidyHigh)
		}
	}
	if shouting(title) {
		title = titleCase(title)
		p.add(TidyCapitalise, TidyMedium)
	}
	return strings.TrimSpace(title)
}

func firstRune(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

func unwrapTitle(s string) (string, bool) {
	if len(s) < 3 {
		return "", false
	}
	open, close := s[0], s[len(s)-1]
	if !((open == '(' && close == ')') || (open == '[' && close == ']')) {
		return "", false
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	// "(A) and (B)" is not one wrapped title.
	if strings.ContainsAny(inner, "()[]") || inner == "" {
		return "", false
	}
	return inner, true
}

// subtitleSeries reports a title that only repeats the series named by a
// "<Series>, Book N" subtitle ("(The Executioner)", "The Executioner, Book 1").
func subtitleSeries(subtitle, title string) bool {
	m := tidyBookOf.FindStringSubmatch(subtitle)
	return m != nil && strings.EqualFold(strings.TrimSpace(m[1]), strings.TrimSpace(title))
}

// tidyPlaceholder reports a title that names nothing: numbers, symbols, a
// roman numeral, "Vol 3", or "<Series> #12".
func tidyPlaceholder(title, series string) bool {
	t := strings.TrimSpace(title)
	if tidyYearTitle.MatchString(t) || tidyDateTitle.MatchString(t) {
		return false // "1984", "11.22.63": real titles
	}
	if t == "" || tidyNoName.MatchString(t) || tidyNumberOnly.MatchString(t) || tidyRoman.MatchString(t) {
		return true
	}
	if inner, ok := unwrapTitle(t); ok {
		return tidyPlaceholder(inner, series)
	}
	// "Mack Bolan #080" names nothing; "The Boys Volume 1" is a real comic title.
	if m := tidyNamedNumber.FindStringSubmatch(t); m != nil && (strings.TrimSpace(m[1]) == "" || strings.Contains(t, "#")) {
		return true
	}
	return false
}

// repairPlaceholder finds a real title for a book whose title names nothing.
func repairPlaceholder(in tidyInput, title string, path pathMetadata, series tidySeries) (string, int) {
	// "III" in Stony Man is "Stony Man III".
	if series.Name != "" && tidyRoman.MatchString(title) && title != "" {
		t := series.Name + " " + title
		if filesMention(in, t) {
			return t, TidyHigh
		}
		return t, TidyMedium
	}
	usable := func(v string) bool {
		if inner, ok := unwrapTitle(v); ok && inner == strings.TrimSpace(v[1:len(v)-1]) {
			return false // "(Phil Dragash)": a narrator or edition note, not a title
		}
		return v != "" && !tidyPlaceholder(v, series.Name) && !strings.EqualFold(v, title) && !strings.EqualFold(v, series.Name) && !mixedTokens(v) && !tidyVolume.MatchString(v) && !subtitleSeries(in.Subtitle, v)
	}
	// The folder name is the most deliberate source when it names the book.
	if v := cleanFolderTitle(path.Title); usable(v) {
		return v, TidyMedium
	}
	hints := []string{series.Sequence, path.Sequence}
	if m := tidyDigits.FindStringSubmatch(title); m != nil {
		hints = append(hints, normalizeSequence(m[1]))
	}
	// The album tag, then every file name, after the book's number:
	// "Deathlands: 043 Dark Emblem", "Super Bolan 080 A Dying Evil.mp3".
	if in.Album != "" {
		if t := numberedTitle([]string{in.Album}, hints); usable(t) {
			return t, TidyMedium
		}
	}
	if t := numberedTitle(in.Files, hints); usable(t) {
		return t, TidyMedium
	}
	if v := cleanTagTitle(in.Album, in.Author); usable(v) {
		return v, TidyMedium
	}
	if series.Name != "" {
		seq := series.Sequence
		if seq == "" {
			seq = path.Sequence
		}
		if seq != "" {
			word := "Book"
			if strings.Contains(strings.ToLower(in.Title+" "+in.Album+" "+in.Rel), "vol") || strings.HasPrefix(strings.TrimSpace(in.Title), "[") {
				word = "Volume"
			}
			return series.Name + ", " + word + " " + seq, TidyLow
		}
	}
	return "", 0
}

// numberedTitle reads a title out of the audio file names around the book's
// number: "Don Pendleton - Super Bolan 080 A Dying Evil.mp3" with number 80,
// "021 Twilight Children.mp3" with number 21. Every file must agree.
func numberedTitle(files []string, hints []string) string {
	if len(files) > 60 {
		files = files[:60]
	}
	for _, hint := range hints {
		if hint == "" {
			continue
		}
		n, err := strconv.ParseFloat(hint, 64)
		if err != nil || n != float64(int(n)) {
			continue
		}
		num := regexp.MustCompile(`(?:^|[^\d.])0*` + strconv.Itoa(int(n)) + `([^\d.].*|$)`)
		found := ""
		agree := true
		for _, f := range files {
			s := strings.TrimSuffix(f, filepath.Ext(f))
			s = strings.TrimSpace(tidyFileYear.ReplaceAllString(s, ""))
			m := num.FindStringSubmatch(s)
			if m == nil {
				agree = false
				break
			}
			rest := strings.TrimLeft(m[1], " -_.~:])")
			if tidyOfPart.MatchString(m[1]) {
				// "(2 of 6)" is a part counter, not the book's number.
				agree = false
				break
			}
			for {
				t := strings.TrimRight(tidyPartMarker.ReplaceAllString(rest, ""), " -_.~:[(")
				t = cleanFolderTitle(strings.TrimSpace(tidyFileYear.ReplaceAllString(t, "")))
				if t == rest {
					break
				}
				rest = t
			}
			if rest == "" || !strings.ContainsFunc(rest, unicode.IsLetter) || mixedTokens(rest) || tidyPlaceholder(rest, "") {
				agree = false
				break
			}
			if found == "" {
				found = rest
			} else if !strings.EqualFold(found, rest) {
				agree = false
				break
			}
		}
		if agree && found != "" {
			return found
		}
	}
	return ""
}

// mixedTokens rejects file-name codes like "FINDER01P01" or "JD1-1x4".
func mixedTokens(s string) bool {
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, "[](),.-_")
		if tidyMixedToken.MatchString(w) && !tidyOrdinal.MatchString(w) {
			return true
		}
	}
	return false
}

func filesMention(in tidyInput, title string) bool {
	t := strings.ToLower(title)
	for _, f := range append([]string{in.Album}, in.Files...) {
		if f != "" && strings.Contains(strings.ToLower(f), t) {
			return true
		}
	}
	return false
}

// cleanFolderTitle drops edition noise from a folder-derived title.
func cleanFolderTitle(s string) string {
	s = strings.TrimSpace(s)
	for {
		t := strings.TrimSpace(tidyEdition.ReplaceAllString(s, ""))
		if t == s {
			return s
		}
		s = t
	}
}

// cleanTagTitle turns an album tag into a title candidate: no author prefix,
// no "~ Volume 01 [01]" disc noise, no edition suffix.
func cleanTagTitle(album, author string) string {
	s := strings.TrimSpace(album)
	if s == "" {
		return ""
	}
	if author != "" {
		if rest, ok := strings.CutPrefix(s, author+" - "); ok {
			s = rest
		}
	}
	for {
		t := strings.Trim(tidyTagMarker.ReplaceAllString(s, ""), " -_.~:")
		t = cleanFolderTitle(t)
		if t == s {
			break
		}
		s = t
	}
	// "Sir Apropos Of Nothing [01] Sir Apropos Of Nothing" names one thing twice.
	s = strings.Join(strings.Fields(tidyBracketNumber.ReplaceAllString(s, " ")), " ")
	if w := strings.Fields(s); len(w)%2 == 0 && len(w) > 0 && strings.EqualFold(strings.Join(w[:len(w)/2], " "), strings.Join(w[len(w)/2:], " ")) {
		s = strings.Join(w[:len(w)/2], " ")
	}
	s = tidyBracketVolume.ReplaceAllString(s, ", $1")
	s = tidyZeroVolume.ReplaceAllString(s, "$1 $2")
	return strings.ReplaceAll(s, " ~ ", ": ")
}

func validTidyTitle(s string) bool {
	return s != "" && len(s) <= 2048 && strings.ContainsFunc(s, unicode.IsLetter)
}

// shouting is an ALL CAPS title of real words (not "III" or "NASA").
func shouting(s string) bool {
	letters, words := 0, 0
	for _, w := range strings.Fields(s) {
		n := 0
		for _, r := range w {
			if unicode.IsLower(r) {
				return false
			}
			if unicode.IsLetter(r) {
				n++
			}
		}
		letters += n
		if n >= 3 {
			words++
		}
	}
	return letters >= 6 && words >= 2 && !tidyRoman.MatchString(s)
}

var tidySmallWords = map[string]bool{"a": true, "an": true, "the": true, "and": true, "but": true, "or": true, "nor": true, "for": true, "of": true, "in": true, "on": true, "at": true, "to": true, "by": true, "vs": true, "vs.": true, "from": true, "with": true}

func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if i > 0 && tidySmallWords[w] && !strings.HasSuffix(words[i-1], ":") {
			continue
		}
		if tidyRoman.MatchString(strings.ToUpper(w)) && len(w) > 1 {
			words[i] = strings.ToUpper(w)
			continue
		}
		r, ok := firstRune(w)
		if ok {
			words[i] = string(unicode.ToUpper(r)) + w[len(string(r)):]
		}
	}
	return strings.Join(words, " ")
}
