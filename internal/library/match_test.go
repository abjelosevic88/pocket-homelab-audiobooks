package library

import (
	"strings"
	"testing"
)

func TestQuickMatchFieldsFillEmptyAndOptions(t *testing.T) {
	cur := map[string]any{"title": "Mine", "subtitle": "", "description": "Keep", "authors": []any{map[string]any{"name": "Me"}}, "series": []any{}, "narrators": []any{}, "genres": []any{"Old"}, "tags": []any{}}
	proposed := map[string]any{"title": "Theirs", "subtitle": "Sub", "description": "New", "authors": []any{map[string]any{"name": "Them"}}, "series": []any{map[string]any{"name": "S", "sequence": "2"}}, "narrators": []string{"Reader"}, "genres": []string{"New"}, "tags": []string{"t"}, "publisher": strings.Repeat("x", 3000)}
	edit, keys := QuickMatchFields(cur, proposed, MatchOptions{})
	if strings.Join(keys, ",") != "subtitle,narrators,series" {
		t.Fatal("defaults fill only empty fields", keys)
	}
	if _, ok := edit["narrators"].([]any); !ok {
		t.Fatal("lists must be JSON arrays for the editor", edit)
	}
	_, keys = QuickMatchFields(cur, proposed, MatchOptions{UpdateAuthors: true})
	if !strings.Contains(strings.Join(keys, ","), "authors") || strings.Contains(strings.Join(keys, ","), "description") {
		t.Fatal("update authors", keys)
	}
	_, keys = QuickMatchFields(cur, proposed, MatchOptions{OverrideDetails: true})
	joined := strings.Join(keys, ",")
	if !strings.Contains(joined, "title") || !strings.Contains(joined, "description") || !strings.Contains(joined, "genres") || strings.Contains(joined, "authors") || strings.Contains(joined, "tags") || strings.Contains(joined, "publisher") {
		t.Fatal("override details (never tags, never oversize values)", keys)
	}
}
