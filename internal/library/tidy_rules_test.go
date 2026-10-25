package library

import (
	"slices"
	"testing"
)

// Titles below are taken from (or shaped like) a real Audiobookshelf import
// of a Graphic Audio collection.
func TestProposeTidy(t *testing.T) {
	cases := []struct {
		name string
		in   tidyInput
		// want* empty means "unchanged"; wantSeries is "Name #seq".
		wantTitle, wantSubtitle, wantSeries string
		clearSubtitle                       bool
		reason                              string
		confidence                          int
		unresolved                          bool
	}{
		{name: "track prefix", in: tidyInput{Title: "01a.Originally Human", Subtitle: "World of the Lupi, Book 0", Series: []tidySeries{{"World of the Lupi", ""}}, Rel: "Eileen Wilks/World of the Lupi/01a.Originally Human"},
			wantTitle: "Originally Human", wantSeries: "World of the Lupi #0", reason: TidyTrackNumber, confidence: TidyMedium},
		{name: "series in title keeps a real subtitle", in: tidyInput{Title: "Mistborn - 1 - The Final Empire - GA", Subtitle: "A Mistborn Novel", Series: []tidySeries{{"Cosmere", "4"}}, Rel: "Brandon Sanderson/Cosmere/Vol 4 - Mistborn - 1 - The Final Empire - GA"},
			wantTitle: "The Final Empire", reason: TidySeriesInTitle, confidence: TidyHigh},
		{name: "sub-series becomes the subtitle", in: tidyInput{Title: "Mistborn - 2 - The Well of Ascension - GA", Series: []tidySeries{{"Cosmere", "5"}}, Rel: "Brandon Sanderson/Cosmere/Vol 5 - Mistborn - 2 - The Well of Ascension - GA"},
			wantTitle: "The Well of Ascension", wantSubtitle: "Mistborn, Book 2", reason: TidyEditionSuffix, confidence: TidyHigh},
		{name: "series in title fills an empty series", in: tidyInput{Title: "Honour - 3 - Welcome to New Haden", Rel: "Simon R. Green/Honour - 3 - Welcome to New Haden"},
			wantTitle: "Welcome to New Haden", wantSeries: "Honour #3", reason: TidySeries, confidence: TidyHigh},
		{name: "edition suffix only", in: tidyInput{Title: "Elantris - The Hope of Elantris - GA", Series: []tidySeries{{"Cosmere", "2"}}},
			wantTitle: "Elantris - The Hope of Elantris", reason: TidyEditionSuffix, confidence: TidyHigh},
		{name: "unabridged", in: tidyInput{Title: "The Final Empire (Unabridged)"}, wantTitle: "The Final Empire", reason: TidyEditionSuffix, confidence: TidyHigh},
		{name: "graphicaudio bracket", in: tidyInput{Title: "Shadows Fall (2 of 2) [GraphicAudio]", Series: []tidySeries{{"Shadows Fall", ""}}}, wantTitle: "Shadows Fall (2 of 2)", reason: TidyEditionSuffix, confidence: TidyHigh},
		{name: "wrapped title", in: tidyInput{Title: "[Goblin Queen]", Series: []tidySeries{{"Tamora Carter", ""}}}, wantTitle: "Goblin Queen", reason: TidyWrapped, confidence: TidyHigh},
		{name: "series number from file names", in: tidyInput{Title: "[Mack Bolan #080]", Author: "Don Pendleton", Series: []tidySeries{{"Mack Bolan", "80"}}, Rel: "Don Pendleton/Mack Bolan/080", Album: "[Mack Bolan #080]", Files: []string{"Don Pendleton - Super Bolan 080 A Dying Evil.mp3"}},
			wantTitle: "A Dying Evil", reason: TidyPlaceholder, confidence: TidyMedium},
		{name: "placeholder number hint", in: tidyInput{Title: "[The Executioner #306]", Series: []tidySeries{{"Mack Bolan", ""}}, Rel: "Don Pendleton/Mack Bolan/306", Files: []string{"Don Pendleton - The Executioner 306 Mercy Mission.mp3"}},
			wantTitle: "Mercy Mission", reason: TidyPlaceholder, confidence: TidyMedium},
		{name: "album tag after the number", in: tidyInput{Title: "061 @", Series: []tidySeries{{"Deathlands", "43"}}, Rel: "James Axler/Deathlands/Vol 43 - 061 @", Album: "Deathlands: 043 Dark Emblem", Files: []string{"043 Dark Emblem.mp3", "043 Dark Emblem 02.mp3"}},
			wantTitle: "Dark Emblem", reason: TidyPlaceholder, confidence: TidyMedium},
		{name: "part counters are not the number", in: tidyInput{Title: "2", Series: []tidySeries{{"Stormlight Archive", ""}}, Rel: "Brandon Sanderson/Stormlight Archive/2", Album: "The Stormlight Archive 3: Oathbringer (2 of 6)", Files: []string{"STORMLIGHT0302 - 001.mp3"}},
			wantTitle: "The Stormlight Archive 3: Oathbringer", reason: TidyPlaceholder, confidence: TidyMedium},
		{name: "roman numeral", in: tidyInput{Title: "III", Series: []tidySeries{{"Stony Man", "3"}}, Rel: "Don Pendleton/Stony Man/Vol 3 - III", Files: []string{"Stony Man 03 - Stony Man III 01.mp3"}},
			wantTitle: "Stony Man III", reason: TidyPlaceholder, confidence: TidyHigh},
		{name: "file codes are not titles", in: tidyInput{Title: "[02]", Series: []tidySeries{{"The Finder Chronicles", "2"}}, Rel: "Suzanne Palmer/The Finder Chronicles/Vol 2 - [02]", Album: "The Finder Chronicles 2: Driving The Deep", Files: []string{"001 FINDER02P01.mp3"}},
			wantTitle: "Driving The Deep", reason: TidyPlaceholder, confidence: TidyMedium},
		{name: "volume album tag", in: tidyInput{Title: "[03]", Series: []tidySeries{{"Deadworld", "3"}}, Rel: "Gary Reed/Deadworld/Vol 3", Album: "Deadworld, Volume 3 [Dramatized Adaptation]", Files: []string{"Deadworld 03.m4b"}},
			wantTitle: "Deadworld, Volume 3", reason: TidyPlaceholder, confidence: TidyMedium},
		{name: "twins named after the series", in: tidyInput{Title: "Great American Westerns", Series: []tidySeries{{"Great American Westerns", "2"}}, Rel: "Various/Great American Westerns/Vol 2 - Great American Westerns", SeriesTwins: true},
			wantTitle: "Great American Westerns, Volume 2", reason: TidyPlaceholder, confidence: TidyLow},
		{name: "folder names the twin", in: tidyInput{Title: "The Lord of the Rings", Series: []tidySeries{{"The Lord of the Rings", "1"}}, Rel: "J.R.R. Tolkien/The Lord of the Rings/Vol 1 - The Fellowship of the Ring", SeriesTwins: true},
			wantTitle: "The Fellowship of the Ring", reason: TidyPlaceholder, confidence: TidyMedium},
		{name: "only the series, wrapped", in: tidyInput{Title: "(The Executioner)", Subtitle: "The Executioner, Book 1", Series: []tidySeries{{"Mack Bolan", ""}}, Rel: "Don Pendleton/Mack Bolan/(The Executioner)", Album: "Book 02 - Collectors Edition -", Files: []string{"Don Pendleton - The Executioner 207 Freedom Strike 1996 001-End.m4b.m4b", "Don Pendleton - The Executioner 227 Blood Circle 1997 001-End.m4b.m4b"}},
			unresolved: true},
		{name: "fragment gets the series name", in: tidyInput{Title: "and the Mechanical Girl", Series: []tidySeries{{"Gideon Smith", "1"}}, Files: []string{"Gideon Smith 1 - Gideon Smith and the Mechanical Girl.mp3"}},
			wantTitle: "Gideon Smith and the Mechanical Girl", reason: TidyFragment, confidence: TidyHigh},
		{name: "lower-case start", in: tidyInput{Title: "superman - Doomsday and Beyond"}, wantTitle: "Superman - Doomsday and Beyond", reason: TidyCapitalise, confidence: TidyHigh},
		{name: "all caps", in: tidyInput{Title: "THE WAY OF SHADOWS"}, wantTitle: "The Way of Shadows", reason: TidyCapitalise, confidence: TidyMedium},
		{name: "volume prefix splits a series", in: tidyInput{Title: "Vol 1 - X-Men Mutant Empire - Siege", Rel: "Marvel Comics/Vol 1 - X-Men Mutant Empire - Siege"},
			wantTitle: "Siege", wantSeries: "X-Men Mutant Empire #1", reason: TidyVolumePrefix, confidence: TidyMedium},
		{name: "volume prefix numbers the series", in: tidyInput{Title: "Vol 1 - Blood Bond", Series: []tidySeries{{"Blood Bond", ""}}, Rel: "William W. Johnstone/Vol 1 - Blood Bond"},
			wantTitle: "Blood Bond", wantSeries: "Blood Bond #1", reason: TidySequence, confidence: TidyHigh},
		{name: "author in title", in: tidyInput{Title: "Brandon Sanderson Yumi and the Nightmare Painter", Author: "Brandon Sanderson"}, wantTitle: "Yumi and the Nightmare Painter", reason: TidyAuthorInTitle, confidence: TidyHigh},
		{name: "year in parentheses", in: tidyInput{Title: "Countdown (2009)"}, wantTitle: "Countdown", reason: TidyYearSuffix, confidence: TidyMedium},
		{name: "bare year after a dash", in: tidyInput{Title: "Batman - Dead White 2006"}, wantTitle: "Batman - Dead White", reason: TidyYearSuffix, confidence: TidyLow},
		{name: "file name title", in: tidyInput{Title: "The Hobbit.mp3", Rel: "J.R.R. Tolkien/The Hobbit", Files: []string{"The Hobbit.mp3"}}, wantTitle: "The Hobbit", reason: TidyFileName, confidence: TidyMedium},
		{name: "junk subtitle", in: tidyInput{Title: "The Atlantis Plague", Subtitle: "[Tagged By 'Pamar']", Series: []tidySeries{{"The Origin Mystery", "2"}}}, clearSubtitle: true, reason: TidySubtitle, confidence: TidyHigh},
		{name: "series from the folder", in: tidyInput{Title: "The Atlantis Plague", Rel: "A.G. Riddle/The Origin Mystery/Vol 2 - The Atlantis Plague"}, wantSeries: "The Origin Mystery #2", reason: TidySeries, confidence: TidyHigh},
		{name: "series from the subtitle", in: tidyInput{Title: "Crusade of Eagles", Subtitle: "Eagles, Book 12"}, wantSeries: "Eagles #12", reason: TidySeries, confidence: TidyMedium},

		// Leave alone.
		{name: "book one named like its series", in: tidyInput{Title: "Windswept", Subtitle: "Windswept, Book 1", Series: []tidySeries{{"Windswept", "1"}}, Rel: "Adam Rakunas/Windswept/Vol 1 - Windswept", Files: []string{"Windswept [01] Windswept.mp3"}}},
		{name: "comic trade volume", in: tidyInput{Title: "The Boys Volume 1", Series: []tidySeries{{"The Boys", "1"}}, Rel: "Garth Ennis/The Boys/Vol 1 - The Boys Volume 1"}},
		{name: "year title", in: tidyInput{Title: "1984", Rel: "George Orwell/1984"}},
		{name: "number title", in: tidyInput{Title: "11.22.63"}},
		{name: "number words", in: tidyInput{Title: "3 Body Problem"}},
		{name: "future year", in: tidyInput{Title: "Blade Runner 2049"}},
		{name: "part of a novel", in: tidyInput{Title: "The Lost Metal - Part 1", Series: []tidySeries{{"Cosmere", ""}}, Rel: "Brandon Sanderson/Cosmere/The Lost Metal - Part 1"}},
		{name: "normal audio marker stays", in: tidyInput{Title: "Sixth of the Dusk - Normal Audio", Series: []tidySeries{{"Cosmere", "17.6"}}}},
		{name: "acronym", in: tidyInput{Title: "NASA"}},
		{name: "hand-edited title", in: tidyInput{Title: "[01]", Series: []tidySeries{{"White Sand", "1"}}, Album: "White Sand ~ Volume 01 [01]", Edited: map[string]bool{"title": true}}},
		{name: "hand-edited series", in: tidyInput{Title: "The Atlantis Plague", Rel: "A.G. Riddle/The Origin Mystery/Vol 2 - The Atlantis Plague", Edited: map[string]bool{"series": true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.in.Edited == nil {
				tc.in.Edited = map[string]bool{}
			}
			p := proposeTidy(tc.in)
			if tc.wantTitle == "" && p.SetTitle {
				t.Errorf("title changed to %q", p.Title)
			}
			if tc.wantTitle != "" && (!p.SetTitle || p.Title != tc.wantTitle) {
				t.Errorf("title = %q (set %v), want %q", p.Title, p.SetTitle, tc.wantTitle)
			}
			switch {
			case tc.clearSubtitle && (!p.SetSub || p.Subtitle != ""):
				t.Errorf("subtitle not cleared: %q", p.Subtitle)
			case tc.wantSubtitle != "" && (!p.SetSub || p.Subtitle != tc.wantSubtitle):
				t.Errorf("subtitle = %q, want %q", p.Subtitle, tc.wantSubtitle)
			case tc.wantSubtitle == "" && !tc.clearSubtitle && p.SetSub:
				t.Errorf("subtitle changed to %q", p.Subtitle)
			}
			got := ""
			if p.Series != nil {
				got = seriesLabel(*p.Series)
			}
			if got != tc.wantSeries {
				t.Errorf("series = %q, want %q", got, tc.wantSeries)
			}
			if tc.reason != "" && !slices.Contains(p.Reasons, tc.reason) {
				t.Errorf("reasons %v lack %s", p.Reasons, tc.reason)
			}
			if tc.confidence != 0 && p.Confidence != tc.confidence {
				t.Errorf("confidence %d, want %d", p.Confidence, tc.confidence)
			}
			if tc.reason == "" && !p.empty() {
				t.Errorf("unexpected proposal %+v", p)
			}
			if tc.unresolved != (p.Unresolved != "") {
				t.Errorf("unresolved = %q", p.Unresolved)
			}
		})
	}
}

func TestTidyHelpers(t *testing.T) {
	for in, want := range map[string]bool{"[01]": true, "061 @": true, "Vol 3": true, "#12": true, "VII": true, "Mack Bolan #080": true, "Book 2": true,
		"1984": false, "The Boys Volume 1": false, "Goblin Queen": false, "Catch-22": false} {
		if got := tidyPlaceholder(in, "Mack Bolan"); got != want {
			t.Errorf("tidyPlaceholder(%q) = %v", in, got)
		}
	}
	for in, want := range map[string]string{"White Sand ~ Volume 01 [01]": "White Sand: Volume 1", "Sir Apropos Of Nothing [01] Sir Apropos Of Nothing": "Sir Apropos Of Nothing", "Deadworld [Volume 2]": "Deadworld, Volume 2", "Don Pendleton - Dark Truth (Disc 1 of 7)": "Dark Truth"} {
		if got := cleanTagTitle(in, "Don Pendleton"); got != want {
			t.Errorf("cleanTagTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if !mixedTokens("FINDER01P01") || !mixedTokens("JD1-1x4 WDOA") || mixedTokens("The 2nd Coming") || mixedTokens("X-Men") {
		t.Error("mixedTokens")
	}
	if titleCase("THE RETURN OF THE KING") != "The Return of the King" || titleCase("STONY MAN III") != "Stony Man III" {
		t.Error(titleCase("THE RETURN OF THE KING"), titleCase("STONY MAN III"))
	}
}
