package library

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Read the legacy ABMETADATA interchange grammar into the same validated edit
// model as JSON sidecars. It does not execute or modify the source file.
func parseABSidecar(data []byte) (EditOptions, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	header := strings.ToLower(strings.TrimSpace(lines[0]))
	if header != ";abmetadata1" && header != ";abmetadata2" {
		return EditOptions{}, fmt.Errorf("%w: unsupported metadata.abs header", ErrInvalid)
	}
	values := sourceRow{}
	section := ""
	description := []string{}
	chapters := []Chapter{}
	chapter := map[string]string{}
	finishChapter := func() error {
		if section != "chapter" {
			return nil
		}
		start, e := strconv.ParseFloat(chapter["start"], 64)
		if e != nil || !validSeconds(start) {
			return ErrInvalid
		}
		end, e := strconv.ParseFloat(chapter["end"], 64)
		if e != nil || !validSeconds(end) || end < start {
			return ErrInvalid
		}
		chapters = append(chapters, Chapter{Start: start, End: end, Title: chapter["title"]})
		chapter = map[string]string{}
		return nil
	}
	for _, line := range lines[1:] {
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		if lower == "[chapter]" || lower == "[description]" {
			if err := finishChapter(); err != nil {
				return EditOptions{}, err
			}
			section = strings.Trim(lower, "[]")
			continue
		}
		if section == "description" {
			description = append(description, line)
			continue
		}
		if trim == "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, ";") {
			continue
		}
		key, value, ok := strings.Cut(trim, "=")
		if !ok {
			return EditOptions{}, fmt.Errorf("%w: invalid metadata.abs line", ErrInvalid)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if section == "chapter" {
			switch key {
			case "start", "end", "title":
				chapter[key] = value
			default:
				return EditOptions{}, ErrInvalid
			}
			continue
		}
		switch key {
		case "media":
			if value != "book" {
				return EditOptions{}, fmt.Errorf("%w: expected a book sidecar", ErrInvalid)
			}
		case "author":
			key = "authors"
			fallthrough
		case "authors", "narrators", "narrator", "genres", "series":
			if key == "narrator" {
				key = "narrators"
			}
			list := []any{}
			for _, v := range strings.Split(value, ",") {
				if v = strings.TrimSpace(v); v != "" {
					list = append(list, v)
				}
			}
			values[key] = list
		case "tags":
			var tags []any
			if e := json.Unmarshal([]byte(value), &tags); e != nil {
				return EditOptions{}, ErrInvalid
			}
			values[key] = tags
		case "explicit", "abridged":
			switch strings.ToLower(value) {
			case "y", "true":
				values[key] = true
			case "n", "false", "":
				values[key] = false
			default:
				return EditOptions{}, ErrInvalid
			}
		case "title", "subtitle", "publisher", "publishedYear", "publishedDate", "description", "isbn", "asin", "language":
			values[key] = value
		}
	}
	if err := finishChapter(); err != nil {
		return EditOptions{}, err
	}
	if len(description) > 0 {
		values["description"] = strings.TrimSpace(strings.Join(description, "\n"))
	}
	if len(chapters) > 0 {
		sort.SliceStable(chapters, func(i, j int) bool { return chapters[i].Start < chapters[j].Start })
		values["chapters"] = chapters
	}
	return parseSidecar([]byte(raw(values)))
}
