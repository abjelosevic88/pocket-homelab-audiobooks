package server

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
)

// Ref is a named link (an author, a series).
type Ref struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Sequence string `json:"sequence,omitempty"`
}

// Book is what the web app gets for one audiobook, with the person's progress.
type Book struct {
	EpisodeID   string   `json:"episode_id,omitempty"`
	ID          string   `json:"id"`
	LibraryID   string   `json:"library_id"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle,omitempty"`
	Author      string   `json:"author"`
	Narrator    string   `json:"narrator,omitempty"`
	Series      string   `json:"series,omitempty"` // "Name #2"
	Sequence    string   `json:"sequence,omitempty"`
	Authors     []Ref    `json:"authors,omitempty"`
	SeriesList  []Ref    `json:"series_list,omitempty"`
	Genres      []string `json:"genres"`
	Year        string   `json:"year,omitempty"`
	Duration    float64  `json:"duration"`
	Cover       string   `json:"cover,omitempty"`
	AddedAt     int64    `json:"added_at"`
	Chapters    int      `json:"chapters"`
	Missing     bool     `json:"missing,omitempty"`
	Progress    float64  `json:"progress"`     // 0..1
	CurrentTime float64  `json:"current_time"` // seconds
	Finished    bool     `json:"finished"`
	LastPlayed  int64    `json:"last_played,omitempty"` // ms
	FinishedAt  int64    `json:"finished_at,omitempty"` // ms

	// the book page only
	Description string        `json:"description,omitempty"`
	Publisher   string        `json:"publisher,omitempty"`
	Language    string        `json:"language,omitempty"`
	Explicit    bool          `json:"explicit,omitempty"`
	Abridged    bool          `json:"abridged,omitempty"`
	Narrators   []string      `json:"narrators,omitempty"`
	ChapterList []abs.Chapter `json:"chapter_list,omitempty"`
	Files       int           `json:"files,omitempty"`
	Size        int64         `json:"size,omitempty"`
	Format      string        `json:"format,omitempty"`
}

// progressMap indexes a person's progress by book (books only, not podcast episodes).
func progressMap(me *abs.Me) map[string]abs.MediaProgress {
	out := map[string]abs.MediaProgress{}
	if me == nil {
		return out
	}
	for _, p := range me.MediaProgress {
		if p.EpisodeID == "" {
			out[p.LibraryItemID] = p
		}
	}
	return out
}

func coverURL(it *abs.Item) string {
	if it.Media.CoverPath == "" {
		return ""
	}
	return fmt.Sprintf("/api/v1/cover/%s?v=%d", it.ID, it.UpdatedAt)
}

// bookOf turns an Audiobookshelf item into a Book.
func bookOf(it *abs.Item, prog map[string]abs.MediaProgress, full bool) Book {
	m := it.Media.Metadata
	b := Book{ID: it.ID, LibraryID: it.LibraryID, Title: m.Title, Subtitle: m.Subtitle, Author: m.AuthorName, Narrator: m.NarratorName,
		Series: m.SeriesName, Sequence: it.Sequence, Genres: nz(m.Genres), Year: m.PublishedYear, Duration: it.Media.Duration, Cover: coverURL(it),
		AddedAt: it.AddedAt, Chapters: it.Media.NumChapters, Missing: it.IsMissing}
	if b.Title == "" {
		b.Title = "Untitled"
	}
	for _, a := range m.Authors {
		b.Authors = append(b.Authors, Ref{ID: a.ID, Name: a.Name})
	}
	for _, sr := range m.Series {
		b.SeriesList = append(b.SeriesList, Ref{ID: sr.ID, Name: sr.Name, Sequence: sr.Sequence})
		if b.Sequence == "" && len(m.Series) == 1 {
			b.Sequence = sr.Sequence
		}
	}
	if b.Author == "" && len(b.Authors) > 0 {
		names := make([]string, len(b.Authors))
		for i, a := range b.Authors {
			names[i] = a.Name
		}
		b.Author = strings.Join(names, ", ")
	}
	if b.Narrator == "" && len(m.Narrators) > 0 {
		b.Narrator = strings.Join(m.Narrators, ", ")
	}
	if p, ok := prog[it.ID]; ok {
		b.Progress, b.CurrentTime, b.Finished, b.LastPlayed, b.FinishedAt = p.Progress, p.CurrentTime, p.IsFinished, p.LastUpdate, p.FinishedAt
		if b.Progress == 0 && b.Duration > 0 {
			b.Progress = p.CurrentTime / b.Duration
		}
	} else if it.UserMediaProgress != nil {
		p := it.UserMediaProgress
		b.Progress, b.CurrentTime, b.Finished, b.LastPlayed, b.FinishedAt = p.Progress, p.CurrentTime, p.IsFinished, p.LastUpdate, p.FinishedAt
	}
	if b.Finished {
		b.Progress = 1
	}
	if full {
		b.Description = cleanDescription(m.Description)
		b.Publisher, b.Language, b.Explicit, b.Abridged = m.Publisher, m.Language, m.Explicit, m.Abridged
		b.Narrators = m.Narrators
		b.ChapterList = nz(it.Media.Chapters)
		b.Chapters = len(b.ChapterList)
		formats := map[string]bool{}
		for _, f := range it.Media.AudioFiles {
			if f.Exclude {
				continue
			}
			b.Files++
			b.Size += f.Metadata.Size
			if f.Codec != "" {
				formats[strings.ToUpper(f.Codec)] = true
			}
		}
		if b.Size == 0 {
			b.Size = it.Media.Size
		}
		var fs []string
		for k := range formats {
			fs = append(fs, k)
		}
		sort.Strings(fs)
		b.Format = strings.Join(fs, ", ")
	}
	return b
}

func books(items []abs.Item, prog map[string]abs.MediaProgress) []Book {
	out := make([]Book, 0, len(items))
	for i := range items {
		if items[i].MediaType != "" && items[i].MediaType != "book" {
			continue
		}
		out = append(out, bookOf(&items[i], prog, false))
	}
	return out
}

var (
	tagRE   = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li)\s*/?>`)
	stripRE = regexp.MustCompile(`<[^>]*>`)
	blankRE = regexp.MustCompile(`\n{3,}`)
)

// cleanDescription turns a description (often HTML from Audible) into plain paragraphs.
func cleanDescription(s string) string {
	s = tagRE.ReplaceAllString(s, "\n")
	s = stripRE.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = blankRE.ReplaceAllString(strings.ReplaceAll(s, "\r", ""), "\n\n")
	return strings.TrimSpace(s)
}

func nz[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// LibraryInfo is a book library the person can open.
type LibraryInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Icon   string `json:"icon"`
	Count  int    `json:"count"`
	Hidden bool   `json:"hidden"`
}

// bookLibraries lists the person's book libraries (podcast libraries belong to
// Pocket Podcasts), with the ones they hid marked (or left out).
func (s *Server) bookLibraries(ctx context.Context, ac *absConn, withHidden bool) ([]LibraryInfo, error) {
	libs, err := ac.c.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(libs, func(i, j int) bool { return libs[i].DisplayOrder < libs[j].DisplayOrder })
	hidden := map[string]bool{}
	if u := userFrom(ctx); u != nil {
		st, _ := s.store.GetSettings(ctx, u.ID)
		for _, id := range strings.Split(st.HiddenLibraries, ",") {
			if id = strings.TrimSpace(id); id != "" {
				hidden[id] = true
			}
		}
	}
	out := []LibraryInfo{}
	for _, l := range libs {
		if l.MediaType != "book" {
			continue
		}
		li := LibraryInfo{ID: l.ID, Name: l.Name, Icon: l.Icon, Hidden: hidden[l.ID]}
		if li.Hidden && !withHidden {
			continue
		}
		if withHidden {
			if p, err := ac.c.Items(ctx, l.ID, abs.ItemsQuery{Limit: 1}); err == nil {
				li.Count = p.Total
			}
		}
		out = append(out, li)
	}
	return out, nil
}
