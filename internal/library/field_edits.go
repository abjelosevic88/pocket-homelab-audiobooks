package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// tidyFields are the values library tidy-up may rewrite. A change to one of
// them through the book editor, the bulk editor or a match is remembered in
// catalog_item_edits, and tidy-up leaves that field alone from then on.
func tidyFields(it Item) map[string]string {
	var doc sourceRow
	_ = json.Unmarshal(it.Document, &doc)
	media := object(doc["media"])
	series := []string{}
	for _, s := range array(doc["series"]) {
		ref := object(s)
		series = append(series, strings.ToLower(strings.TrimSpace(str(ref["name"])))+"#"+strings.TrimSpace(str(ref["sequence"])))
	}
	return map[string]string{"title": it.Title, "subtitle": str(media["subtitle"]), "series": strings.Join(series, "\x00")}
}

func recordFieldEdits(ctx context.Context, tx *sql.Tx, id string, before map[string]string) error {
	after, err := scanItem(tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE id=?`, id))
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for field, value := range tidyFields(after) {
		if value == before[field] {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_item_edits VALUES(?,?,?) ON CONFLICT(item_id,field) DO UPDATE SET edited_at=excluded.edited_at`, id, field, now); err != nil {
			return err
		}
	}
	return nil
}
