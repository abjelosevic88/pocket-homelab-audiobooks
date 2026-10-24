package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/url"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

type EpisodeEdit struct {
	Expected int64          `json:"revision"`
	Fields   map[string]any `json:"fields"`
}

// Episode text is stored as plain text and HTML-escaped for clients that render
// descriptions as markup. Scripts and embedded resources never survive edits.
func episodeText(s string) string {
	z := xhtml.NewTokenizer(strings.NewReader(s))
	var out strings.Builder
	skip := 0
	for {
		kind := z.Next()
		if kind == xhtml.ErrorToken {
			if z.Err() == io.EOF {
				break
			}
			break
		}
		t := z.Token()
		switch kind {
		case xhtml.StartTagToken:
			if t.Data == "script" || t.Data == "style" {
				skip++
			}
		case xhtml.EndTagToken:
			if (t.Data == "script" || t.Data == "style") && skip > 0 {
				skip--
			}
			if skip == 0 && (t.Data == "p" || t.Data == "div" || t.Data == "br") {
				out.WriteByte('\n')
			}
		case xhtml.TextToken:
			if skip == 0 {
				out.WriteString(t.Data)
			}
		}
	}
	return html.EscapeString(strings.TrimSpace(out.String()))
}
func (c *Catalog) EpisodeDocument(ctx context.Context, uid int64, item, episode string) (map[string]any, int64, error) {
	it, err := c.Item(ctx, uid, item)
	if err != nil {
		return nil, 0, err
	}
	if it.MediaType != "podcast" {
		return nil, 0, ErrNotFound
	}
	var ep sourceRow
	for _, v := range array(objectJSON(string(it.Document))["episodes"]) {
		if str(object(v)["id"]) == episode {
			ep = object(v)
			break
		}
	}
	if ep == nil {
		var doc string
		err = c.DB.QueryRowContext(ctx, `SELECT document FROM catalog_podcast_feed_episodes WHERE item_id=? AND episode_id=?`, item, episode).Scan(&doc)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, ErrNotFound
		}
		if err != nil {
			return nil, 0, err
		}
		ep = objectJSON(doc)
	}
	var patch string
	err = c.DB.QueryRowContext(ctx, `SELECT document FROM catalog_podcast_episode_edits WHERE item_id=? AND episode_id=?`, item, episode).Scan(&patch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}
	for k, v := range objectJSON(patch) {
		ep[k] = v
	}
	normalizeEpisode(ep)
	ep["libraryItemId"] = item
	return ep, it.UpdatedAt, nil
}
func (c *Catalog) EditEpisode(ctx context.Context, uid int64, item, episode string, in EpisodeEdit) error {
	if err := c.CanManagePodcast(ctx, uid, item); err != nil {
		return err
	}
	ep, revision, err := c.EpisodeDocument(ctx, uid, item, episode)
	if err != nil {
		return err
	}
	if revision != in.Expected {
		return ErrConflict
	}
	if len(in.Fields) == 0 || len(in.Fields) > 12 {
		return ErrInvalid
	}
	patch := sourceRow{}
	for k, v := range in.Fields {
		switch k {
		case "title", "subtitle", "description", "pubDate", "episode", "season", "episodeType":
			value, ok := v.(string)
			if !ok || len(value) > 100000 {
				return ErrInvalid
			}
			if k != "description" && len(value) > 4096 {
				return ErrInvalid
			}
			if k == "title" && strings.TrimSpace(value) == "" {
				return ErrInvalid
			}
			if k == "episodeType" && value != "" && value != "full" && value != "trailer" && value != "bonus" {
				return ErrInvalid
			}
			patch[k] = episodeText(value)
			if k == "title" && patch[k] == "" {
				return ErrInvalid
			}
		case "publishedAt":
			n, ok := v.(float64)
			if !ok || !validSeconds(n) || n > 32503680000000 {
				return ErrInvalid
			}
			patch[k] = int64(n)
		case "enclosure":
			if v == nil {
				patch["enclosureURL"] = ""
				patch["enclosureType"] = ""
				patch["enclosureSize"] = 0
				continue
			}
			m := object(v)
			u, e := url.Parse(str(m["url"]))
			if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return ErrInvalid
			}
			if len(str(m["type"])) > 256 || number(m["length"]) < 0 {
				return ErrInvalid
			}
			patch["enclosureURL"] = u.String()
			patch["enclosureType"] = str(m["type"])
			patch["enclosureSize"] = number(m["length"])
		case "chapters":
			var chapters []Chapter
			if e := json.Unmarshal([]byte(raw(v)), &chapters); e != nil || len(chapters) > 10000 {
				return ErrInvalid
			}
			duration := number(ep["duration"])
			if duration == 0 {
				duration = number(object(ep["audioFile"])["duration"])
			}
			last := 0.0
			for i := range chapters {
				ch := &chapters[i]
				if !validSeconds(ch.Start) || !validSeconds(ch.End) || ch.Start < last || ch.End < ch.Start || ch.End > duration+1 || len(ch.Title) > 4096 {
					return ErrInvalid
				}
				ch.ID = i
				ch.Title = episodeText(ch.Title)
				last = ch.End
			}
			patch[k] = chapters
		default:
			return ErrInvalid
		}
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	it, err := scanItem(tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE id=?`, item))
	if err != nil {
		return err
	}
	if it.UpdatedAt != revision {
		return ErrConflict
	}
	var old string
	err = tx.QueryRowContext(ctx, `SELECT document FROM catalog_podcast_episode_edits WHERE item_id=? AND episode_id=?`, item, episode).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	combined := objectJSON(old)
	if combined == nil {
		combined = sourceRow{}
	}
	for k, v := range patch {
		combined[k] = v
		ep[k] = v
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_podcast_episode_edits VALUES(?,?,?) ON CONFLICT(item_id,episode_id) DO UPDATE SET document=excluded.document`, item, episode, raw(combined)); err != nil {
		return err
	}
	doc := objectJSON(string(it.Document))
	for _, entry := range array(doc["episodes"]) {
		e := object(entry)
		if str(e["id"]) == episode {
			for k, v := range patch {
				e[k] = v
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_entities SET document=? WHERE kind='podcastEpisodes' AND id=?`, raw(ep), episode); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_items SET document=?,updated_at=MAX(updated_at+1,?) WHERE id=?`, raw(doc), time.Now().UnixMilli(), item); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeEpisode(ep sourceRow) {
	for _, key := range []string{"publishedAt", "createdAt", "updatedAt"} {
		if v, ok := ep[key]; ok {
			if n, e := milliseconds(v); e == nil {
				ep[key] = n
			}
		}
	}
	if number(ep["duration"]) == 0 {
		ep["duration"] = number(object(ep["audioFile"])["duration"])
	}
}
