package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/feed"
)

type PodcastOptions struct {
	LibraryID string `json:"library_id"`
	FolderID  string `json:"folder_id"`
	URL       string `json:"url"`
}
type PodcastSettings struct {
	Auto     bool `json:"auto_download"`
	Interval int  `json:"interval_seconds"`
	Keep     int  `json:"keep_latest"`
	MaxNew   int  `json:"max_new"`
}

func (c *Catalog) CanManagePodcast(ctx context.Context, uid int64, item string) error {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return err
	}
	it, err := c.Item(ctx, uid, item)
	if err != nil {
		return err
	}
	if it.MediaType != "podcast" || !a.Permissions.Update {
		return ErrNotFound
	}
	return nil
}
func (c *Catalog) Subscribe(ctx context.Context, uid int64, in PodcastOptions, f feed.Feed) (string, error) {
	a, err := c.Account(ctx, uid)
	if err != nil {
		return "", err
	}
	if !a.Permissions.Upload || !a.Permissions.Update || !a.allowsLibrary(in.LibraryID) || !a.Permissions.AllTags || (f.Explicit && !a.Permissions.Explicit) {
		return "", ErrNotFound
	}
	if strings.TrimSpace(f.Title) == "" || len(f.Title) > 2048 {
		return "", ErrInvalid
	}
	var typ string
	if err = c.DB.QueryRowContext(ctx, `SELECT media_type FROM catalog_libraries WHERE id=?`, in.LibraryID).Scan(&typ); err != nil {
		return "", err
	}
	if typ != "podcast" {
		return "", ErrInvalid
	}
	folders, err := c.scanFolders(ctx, in.LibraryID)
	if err != nil {
		return "", err
	}
	var folder *scanFolder
	for i := range folders {
		if folders[i].ID == in.FolderID {
			folder = &folders[i]
			break
		}
	}
	if folder == nil {
		return "", ErrNotFound
	}
	root, err := os.OpenRoot(folder.Root.local)
	if err != nil {
		return "", err
	}
	defer root.Close()
	id, mid := newID(), newID()
	name := "podcast-" + strings.TrimPrefix(id, "native-")
	rel := filepath.Join(folder.Relative, name)
	if err = root.Mkdir(rel, 0755); err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = root.Remove(rel)
		}
	}()
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var existing int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_items WHERE library_id=? AND media_type='podcast' AND json_extract(document,'$.media.feedURL')=?`, in.LibraryID, in.URL).Scan(&existing); err != nil {
		return "", err
	}
	if existing > 0 {
		return "", ErrConflict
	}
	now := time.Now().UnixMilli()
	media := sourceRow{"id": mid, "title": f.Title, "author": f.Author, "description": f.Description, "feedURL": in.URL, "imageURL": f.Image, "language": f.Language, "explicit": f.Explicit, "tags": []any{}, "genres": []any{}}
	doc := sourceRow{"item": sourceRow{"id": id, "path": filepath.Join(folder.Source, name), "libraryFolderId": folder.ID}, "media": media, "episodes": []any{}, "authors": []any{}, "series": []any{}}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_items VALUES(?,?,?,'podcast',?,?,0,?,0,?,?,'[]',?)`, id, in.LibraryID, mid, f.Title, f.Author, f.Explicit, now, now, raw(doc)); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_podcast_subscriptions(item_id,feed_url,actor_id) VALUES(?,?,?)`, id, in.URL, uid); err != nil {
		return "", err
	}
	if err = saveFeed(ctx, tx, id, doc, f); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	committed = true
	return id, nil
}

// Preserve imported episode IDs by matching their original enclosure URLs.
func saveFeed(ctx context.Context, tx *sql.Tx, id string, doc sourceRow, f feed.Feed) error {
	known := map[string]string{}
	for _, v := range array(doc["episodes"]) {
		e := object(v)
		if url := str(e["enclosureURL"]); url != "" {
			known[url] = str(e["id"])
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT episode_id,enclosure_url FROM catalog_podcast_feed_episodes WHERE item_id=?`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ep, url string
		if err = rows.Scan(&ep, &url); err != nil {
			rows.Close()
			return err
		}
		known[url] = ep
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range f.Episodes {
		if old := known[e.URL]; old != "" {
			e.ID = old
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_podcast_feed_episodes VALUES(?,?,?,?,?) ON CONFLICT(item_id,episode_id) DO UPDATE SET enclosure_url=excluded.enclosure_url,published_at=excluded.published_at,document=excluded.document`, id, e.ID, e.URL, e.PublishedAt, raw(e)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE catalog_podcast_subscriptions SET last_check=?,error='' WHERE item_id=?`, time.Now().UnixMilli(), id)
	return err
}
func (c *Catalog) UpdateFeed(ctx context.Context, uid int64, id string, f feed.Feed) error {
	if err := c.CanManagePodcast(ctx, uid, id); err != nil {
		return err
	}
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		return err
	}
	doc := objectJSON(string(it.Document))
	url := str(object(doc["media"])["feedURL"])
	if url == "" {
		return ErrInvalid
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO catalog_podcast_subscriptions(item_id,feed_url,actor_id) VALUES(?,?,?)`, id, url, uid); err != nil {
		return err
	}
	if err = saveFeed(ctx, tx, id, doc, f); err != nil {
		return err
	}
	return tx.Commit()
}
func (c *Catalog) SetPodcastSettings(ctx context.Context, uid int64, id string, in PodcastSettings) error {
	if err := c.CanManagePodcast(ctx, uid, id); err != nil {
		return err
	}
	if in.Interval < 300 || in.Interval > 604800 || in.Keep < 0 || in.Keep > 10000 || in.MaxNew < 1 || in.MaxNew > 100 {
		return ErrInvalid
	}
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		return err
	}
	url := str(object(objectJSON(string(it.Document))["media"])["feedURL"])
	if url == "" {
		return ErrInvalid
	}
	_, err = c.DB.ExecContext(ctx, `INSERT INTO catalog_podcast_subscriptions(item_id,feed_url,actor_id,auto_download,interval_seconds,keep_latest,max_new) VALUES(?,?,?,?,?,?,?) ON CONFLICT(item_id) DO UPDATE SET actor_id=excluded.actor_id,auto_download=excluded.auto_download,interval_seconds=excluded.interval_seconds,keep_latest=excluded.keep_latest,max_new=excluded.max_new`, id, url, uid, in.Auto, in.Interval, in.Keep, in.MaxNew)
	return err
}

type PodcastEpisode struct {
	feed.Episode
	Downloaded bool   `json:"downloaded"`
	JobID      string `json:"job_id,omitempty"`
	Status     string `json:"status,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (c *Catalog) PodcastEpisodes(ctx context.Context, uid int64, id string) ([]PodcastEpisode, error) {
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		return nil, err
	}
	if it.MediaType != "podcast" {
		return nil, ErrNotFound
	}
	doc := objectJSON(string(it.Document))
	downloaded := map[string]sourceRow{}
	for _, v := range array(doc["episodes"]) {
		e := object(v)
		downloaded[str(e["id"])] = e
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT episode_id,document FROM catalog_podcast_feed_episodes WHERE item_id=? ORDER BY published_at DESC,episode_id`, id)
	if err != nil {
		return nil, err
	}
	out := []PodcastEpisode{}
	seen := map[string]bool{}
	for rows.Next() {
		var ep, raw string
		if err = rows.Scan(&ep, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var e PodcastEpisode
		if err = json.Unmarshal([]byte(raw), &e.Episode); err != nil {
			rows.Close()
			return nil, err
		}
		e.Downloaded = downloaded[ep] != nil
		out = append(out, e)
		seen[ep] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for ep, doc := range downloaded {
		if !seen[ep] {
			e := PodcastEpisode{Downloaded: true}
			if err = json.Unmarshal([]byte(raw(doc)), &e.Episode); err != nil {
				return nil, err
			}
			e.ID = ep
			out = append(out, e)
		}
	}
	for i := range out {
		if old := downloaded[out[i].ID]; old != nil {
			file, _, e := c.OpenFile(ctx, uid, id, str(object(old["audioFile"])["ino"]), false)
			out[i].Downloaded = e == nil
			if e == nil {
				file.Close()
			}
			if out[i].Duration == 0 {
				out[i].Duration = number(object(old["audioFile"])["duration"])
			}
		}
	}
	for i := range out {
		err = c.DB.QueryRowContext(ctx, `SELECT id,status,error FROM catalog_podcast_jobs WHERE item_id=? AND episode_id=? ORDER BY created_at DESC LIMIT 1`, id, out[i].ID).Scan(&out[i].JobID, &out[i].Status, &out[i].Error)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return out, nil
}
func (c *Catalog) QueueEpisode(ctx context.Context, uid int64, id, episode string) (string, error) {
	if err := c.CanManagePodcast(ctx, uid, id); err != nil {
		return "", err
	}
	episodes, err := c.PodcastEpisodes(ctx, uid, id)
	if err != nil {
		return "", err
	}
	found := false
	for _, e := range episodes {
		if e.ID == episode {
			if e.Downloaded {
				return "", ErrConflict
			}
			found = true
		}
	}
	if !found {
		return "", ErrNotFound
	}
	job := newID()
	now := time.Now().UnixMilli()
	if _, err = c.DB.ExecContext(ctx, `INSERT INTO catalog_podcast_jobs VALUES(?,?,?,?,'queued',?,?,'')`, job, id, episode, uid, now, now); err != nil {
		return "", ErrConflict
	}
	return job, nil
}

// PublishEpisode validates a downloaded file, then atomically publishes it in
// the show's own directory. Existing media and imported file names are untouched.
func (c *Catalog) PublishEpisode(ctx context.Context, uid int64, id, episode, spool, ffprobe string, jobIDs ...string) error {
	if err := c.CanManagePodcast(ctx, uid, id); err != nil {
		return err
	}
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		return err
	}
	var data string
	if err = c.DB.QueryRowContext(ctx, `SELECT document FROM catalog_podcast_feed_episodes WHERE item_id=? AND episode_id=?`, id, episode).Scan(&data); err != nil {
		return err
	}
	var e feed.Episode
	if err = json.Unmarshal([]byte(data), &e); err != nil {
		return err
	}
	doc := objectJSON(string(it.Document))
	previous := sourceRow(nil)
	oldDuration := 0.0
	for _, v := range array(doc["episodes"]) {
		old := object(v)
		if str(old["id"]) == episode {
			f, _, e := c.OpenFile(ctx, uid, id, str(object(old["audioFile"])["ino"]), false)
			if e == nil {
				f.Close()
				return ErrConflict
			}
			if !errors.Is(e, os.ErrNotExist) && !errors.Is(e, ErrNotFound) {
				return e
			}
			previous = old
			oldDuration = number(object(old["audioFile"])["duration"])
		}
	}
	input, err := os.Open(spool)
	if err != nil {
		return err
	}
	defer input.Close()
	probe, err := Probe(ctx, ffprobe, input)
	if err != nil {
		return err
	}
	if _, err = input.Seek(0, 0); err != nil {
		return err
	}
	folders, err := c.scanFolders(ctx, it.LibraryID)
	if err != nil {
		return err
	}
	path := str(object(doc["item"])["path"])
	var folder *scanFolder
	for i := range folders {
		if under(path, folders[i].Source) {
			folder = &folders[i]
			break
		}
	}
	if folder == nil {
		return ErrNotFound
	}
	rel, err := filepath.Rel(folder.Root.source, path)
	if err != nil || !filepath.IsLocal(rel) {
		return ErrInvalid
	}
	root, err := os.OpenRoot(folder.Root.local)
	if err != nil {
		return err
	}
	defer root.Close()
	// Preserve the actual container format rather than guessing from a signed URL.
	ext, mime := "", ""
	for _, format := range strings.Split(probe.Format, ",") {
		switch format {
		case "mp3":
			ext, mime = ".mp3", "audio/mpeg"
		case "mov", "mp4", "m4a":
			ext, mime = ".m4a", "audio/mp4"
		case "ogg":
			ext, mime = ".ogg", "audio/ogg"
		case "flac":
			ext, mime = ".flac", "audio/flac"
		case "wav":
			ext, mime = ".wav", "audio/wav"
		case "aac":
			ext, mime = ".aac", "audio/aac"
		case "matroska", "webm":
			ext, mime = ".webm", "audio/webm"
		case "asf":
			ext, mime = ".wma", "audio/x-ms-wma"
		case "aiff":
			ext, mime = ".aiff", "audio/aiff"
		}
	}
	if ext == "" {
		return ErrInvalid
	}
	name := "episode-" + strings.TrimPrefix(newID(), "native-") + ext
	target := filepath.Join(rel, name)
	temp := filepath.Join(rel, ".download-"+newID())
	output, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	committed, published := false, false
	defer func() {
		if !committed {
			_ = root.Remove(temp)
			if published {
				_ = root.Remove(target)
			}
		}
	}()
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if err = errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if err = c.CanManagePodcast(ctx, uid, id); err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanItem(tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM catalog_items WHERE id=?`, id))
	if err != nil {
		return err
	}
	if current.UpdatedAt != it.UpdatedAt {
		return ErrConflict
	}
	stat, err := root.Stat(temp)
	if err != nil {
		return err
	}
	fileID := newID()
	audio := sourceRow{"ino": fileID, "duration": probe.Duration, "mimeType": mime, "metadata": sourceRow{"path": filepath.Join(path, name), "filename": name, "size": stat.Size(), "mtimeMs": stat.ModTime().UnixMilli()}}
	edoc := objectJSON(data)
	if previous != nil {
		edoc = previous
	}
	edoc["id"] = episode
	edoc["podcastId"] = it.MediaID
	edoc["audioFile"] = audio
	edoc["chapters"] = probe.Chapters
	edoc["duration"] = probe.Duration
	edoc["createdAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	episodes := []any{}
	for _, v := range array(doc["episodes"]) {
		if str(object(v)["id"]) != episode {
			episodes = append(episodes, v)
		}
	}
	doc["episodes"] = append(episodes, edoc)
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_files VALUES(?,?,'audio',?,?,?,?,?,?,0,0,?)`, id, fileID, folder.Root.id, target, name, mime, stat.Size(), probe.Duration, raw(audio)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_entities VALUES('podcastEpisodes',?,?,?) ON CONFLICT(kind,id) DO UPDATE SET document=excluded.document`, episode, it.LibraryID, raw(edoc)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_items SET document=?,duration=MAX(0,duration-?)+?,updated_at=MAX(updated_at+1,?) WHERE id=?`, raw(doc), oldDuration, probe.Duration, time.Now().UnixMilli(), id); err != nil {
		return err
	}
	if len(jobIDs) > 0 {
		res, e := tx.ExecContext(ctx, `UPDATE catalog_podcast_jobs SET status='complete',updated_at=?,error='' WHERE id=? AND item_id=? AND episode_id=? AND actor_id=? AND status='running'`, time.Now().UnixMilli(), jobIDs[0], id, episode, uid)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return context.Canceled
		}
	}
	if err = root.Rename(temp, target); err != nil {
		return err
	}
	published = true
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
