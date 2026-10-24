package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

type nativeObject = map[string]any

func nativeMap(v any) nativeObject {
	m, _ := v.(map[string]any)
	if m == nil {
		return nativeObject{}
	}
	return m
}
func nativeArray(v any) []any {
	switch list := v.(type) {
	case []string:
		out := make([]any, len(list))
		for i, v := range list {
			out[i] = v
		}
		return out
	case []nativeObject:
		out := make([]any, len(list))
		for i, v := range list {
			out[i] = v
		}
		return out
	}
	a, _ := v.([]any)
	if a == nil {
		return []any{}
	}
	return a
}
func nativeString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func nativeNumber(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}
func nativeBool(v any) bool { return v == true || nativeNumber(v) == 1 }
func nativeMillis(v any) int64 {
	if n := nativeNumber(v); n > 0 {
		return int64(n)
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999 -07:00"} {
		if t, e := time.Parse(layout, nativeString(v)); e == nil {
			return t.UnixMilli()
		}
	}
	return 0
}
func nativeJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func (s *Server) nativeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, library.ErrNotFound):
		writeErr(w, 404, "not found")
	case errors.Is(err, library.ErrInvalid), errors.Is(err, library.ErrNoAudio):
		writeErr(w, 400, err.Error())
	case errors.Is(err, library.ErrConflict):
		writeErr(w, 409, err.Error())
	default:
		s.catalogError(w, err)
	}
}
func (s *Server) nativeProgress(ctx context.Context, uid int64) ([]any, error) {
	account, err := s.catalog().Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT p.id,p.item_id,p.episode_id,p.position,p.finished,p.updated_at,p.document,i.duration FROM catalog_progress p JOIN catalog_items i ON i.id=p.item_id WHERE p.account_id=? ORDER BY p.updated_at DESC`, account.ID)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for rows.Next() {
		var id, item, episode, raw string
		var pos, duration float64
		var done bool
		var updated int64
		if err = rows.Scan(&id, &item, &episode, &pos, &done, &updated, &raw, &duration); err != nil {
			rows.Close()
			return nil, err
		}
		d := nativeObject{}
		if err = json.Unmarshal([]byte(raw), &d); err != nil {
			rows.Close()
			return nil, err
		}
		if episode != "" {
			duration = nativeNumber(d["duration"])
		}
		d["id"], d["libraryItemId"], d["episodeId"], d["currentTime"], d["duration"], d["isFinished"], d["lastUpdate"] = id, item, episode, pos, duration, done, updated
		d["hideFromContinueListening"] = nativeBool(d["hideFromContinueListening"])
		d["startedAt"] = nativeMillis(firstNative(d["startedAt"], d["createdAt"]))
		d["finishedAt"] = nativeMillis(d["finishedAt"])
		d["progress"] = min(1, pos/max(duration, 1))
		if done {
			d["progress"] = 1
		}
		out = append(out, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	filtered := []any{}
	for _, p := range out {
		if _, err = s.catalog().Item(ctx, uid, nativeString(nativeMap(p)["libraryItemId"])); err == nil {
			filtered = append(filtered, p)
		} else if !errors.Is(err, library.ErrNotFound) {
			return nil, err
		}
	}
	return filtered, nil
}
func firstNative(a, b any) any {
	if a != nil {
		return a
	}
	return b
}
func (s *Server) nativeMe(ctx context.Context, uid int64) (nativeObject, error) {
	a, err := s.catalog().Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	u, err := s.store.GetUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	progress, err := s.nativeProgress(ctx, uid)
	if err != nil {
		return nil, err
	}
	bookmarks, err := s.catalog().Bookmarks(ctx, uid)
	if err != nil {
		return nil, err
	}
	return nativeObject{"id": a.ID, "username": u.Name, "email": u.Email, "type": "user", "isActive": true, "permissions": a.Permissions, "librariesAccessible": a.Permissions.Libraries, "mediaProgress": progress, "bookmarks": bookmarks}, nil
}
func (s *Server) nativeItem(ctx context.Context, uid int64, id string) (nativeObject, error) {
	it, err := s.catalog().Item(ctx, uid, id)
	if err != nil {
		return nil, err
	}
	doc := nativeObject{}
	if err = json.Unmarshal(it.Document, &doc); err != nil {
		return nil, err
	}
	m := nativeMap(doc["media"])
	meta := nativeObject{}
	for _, k := range []string{"title", "subtitle", "publishedYear", "publishedDate", "publisher", "description", "isbn", "asin", "language", "explicit", "abridged", "genres", "narrators", "author", "feedURL", "itunesId", "itunesPageURL", "imageURL", "releaseDate"} {
		meta[k] = m[k]
	}
	meta["title"] = it.Title
	meta["explicit"], meta["abridged"] = nativeBool(m["explicit"]), nativeBool(m["abridged"])
	authors := nativeArray(doc["authors"])
	for _, entry := range authors {
		author := nativeMap(entry)
		// Imported SQLite entity timestamps are SQL strings; client responses
		// use milliseconds, including nested authors in expanded book metadata.
		author["updatedAt"] = nativeMillis(author["updatedAt"])
		author["addedAt"] = nativeMillis(author["addedAt"])
	}
	meta["authors"] = authors
	meta["series"] = nativeArray(doc["series"])
	meta["authorName"], meta["authorNameLF"] = it.Author, it.Author
	narrators := []string{}
	for _, v := range nativeArray(m["narrators"]) {
		narrators = append(narrators, nativeString(v))
	}
	meta["narratorName"] = strings.Join(narrators, ", ")
	media := nativeObject{"id": it.MediaID, "metadata": meta, "coverPath": m["coverPath"], "tags": it.Tags, "duration": it.Duration, "chapters": nativeArray(m["chapters"]), "audioFiles": nativeArray(m["audioFiles"]), "ebookFile": m["ebookFile"], "episodes": nativeArray(doc["episodes"])}
	files, e := s.catalog().Files(ctx, uid, it.ID)
	if e != nil {
		return nil, e
	}
	size := int64(0)
	audio := []any{}
	libraryFiles := []any{}
	invalid := false
	indexed := map[string]nativeObject{}
	for _, v := range nativeArray(m["audioFiles"]) {
		d := nativeMap(v)
		indexed[nativeString(d["ino"])] = d
	}
	for _, f := range files {
		size += f.Size
		file := nativeObject{"ino": f.ID, "metadata": nativeObject{"filename": f.Name, "size": f.Size}, "isSupplementary": f.Role == "supplement"}
		libraryFiles = append(libraryFiles, file)
		if f.Role == "audio" {
			d := indexed[f.ID]
			if d == nil {
				d = nativeObject{}
			}
			d["ino"], d["duration"], d["index"], d["exclude"], d["mimeType"] = f.ID, f.Duration, f.Index, f.Excluded, f.MIME
			md := nativeMap(d["metadata"])
			md["filename"], md["size"] = f.Name, f.Size
			d["metadata"] = md
			audio = append(audio, d)
			if !f.Excluded && f.Duration <= 0 {
				invalid = true
			}
		}
	}
	media["size"] = size
	if it.MediaType == "book" {
		media["audioFiles"] = audio
	}
	media["numAudioFiles"], media["numTracks"], media["numChapters"] = len(audio), len(audio), len(nativeArray(m["chapters"]))
	if it.MediaType == "podcast" {
		media["numTracks"] = media["numEpisodes"]
		media["numAudioFiles"] = media["numEpisodes"]
	}
	return nativeObject{"id": it.ID, "libraryId": it.LibraryID, "mediaType": it.MediaType, "addedAt": it.AddedAt, "updatedAt": it.UpdatedAt, "isMissing": it.Missing, "isInvalid": invalid, "size": size, "birthtimeMs": nativeMap(doc["item"])["birthtimeMs"], "mtimeMs": nativeMap(doc["item"])["mtimeMs"], "media": media, "libraryFiles": libraryFiles}, nil
}
func (s *Server) nativeItems(ctx context.Context, uid int64, lib string) ([]nativeObject, error) {
	out := []nativeObject{}
	for offset := 0; ; offset += 200 {
		p, err := s.catalog().Items(ctx, uid, library.Query{LibraryID: lib, Offset: offset, Limit: 200})
		if err != nil {
			return nil, err
		}
		for _, i := range p.Items {
			it, err := s.nativeItem(ctx, uid, i.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, it)
		}
		if offset+len(p.Items) >= p.Total {
			break
		}
	}
	return out, nil
}
func (s *Server) nativeLibraries(ctx context.Context, uid int64) ([]any, error) {
	libs, err := s.catalog().Libraries(ctx, uid)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, l := range libs {
		var raw string
		if err = s.store.DB.QueryRowContext(ctx, `SELECT document FROM catalog_libraries WHERE id=?`, l.ID).Scan(&raw); err != nil {
			return nil, err
		}
		m := nativeObject{}
		if err = json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, err
		}
		m["id"], m["name"], m["mediaType"] = l.ID, l.Name, l.MediaType
		m["numItems"] = l.Count
		folders := []any{}
		rows, err := s.store.DB.QueryContext(ctx, `SELECT document FROM catalog_entities WHERE kind='libraryFolders' AND library_id=? ORDER BY id`, l.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			f := nativeObject{}
			if err = json.Unmarshal([]byte(raw), &f); err != nil {
				rows.Close()
				return nil, err
			}
			f["fullPath"] = f["path"]
			folders = append(folders, f)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		m["folders"] = folders
		out = append(out, m)
	}
	return out, nil
}
func nativeMetadata(it nativeObject) nativeObject {
	return nativeMap(nativeMap(it["media"])["metadata"])
}
func nativeSessionWire(sess library.Session, it nativeObject) nativeObject {
	title := nativeMetadata(it)["title"]
	if sess.EpisodeID != "" {
		for _, v := range nativeArray(nativeMap(it["media"])["episodes"]) {
			e := nativeMap(v)
			if nativeString(e["id"]) == sess.EpisodeID {
				title = e["title"]
				break
			}
		}
	}

	return nativeObject{"libraryItem": it, "id": sess.ID, "userId": sess.AccountID, "libraryItemId": sess.ItemID, "episodeId": sess.EpisodeID, "libraryId": it["libraryId"], "mediaType": it["mediaType"], "displayTitle": title, "displayAuthor": nativeMetadata(it)["authorName"], "duration": sess.Duration, "currentTime": sess.Position, "startTime": sess.StartPosition, "timeListening": sess.Listened, "startedAt": sess.StartedAt, "updatedAt": sess.UpdatedAt, "audioTracks": sess.Tracks, "chapters": sess.Chapters, "playMethod": 0, "mediaPlayer": "pocket-audiobooks-native", "mediaMetadata": nativeMetadata(it), "deviceInfo": nativeObject{"deviceId": sess.Client, "deviceName": sess.Device, "clientName": "Pocket Audiobooks"}, "date": time.UnixMilli(sess.StartedAt).In(time.Local).Format("2006-01-02"), "playing": sess.Playing, "closed": sess.Closed}
}
func (s *Server) nativeHistory(ctx context.Context, uid int64) error {
	// Import the old book sessions into the existing integration/history tables.
	// Native reports already write those transactionally and are never re-polled.
	a, err := s.catalog().Account(ctx, uid)
	if err != nil {
		return err
	}
	state := s.store.GetSync(ctx, uid)
	if state.ServerKey == "native|"+a.ID && state.Backfilled {
		return nil
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT h.id,h.item_id,h.document FROM catalog_history h JOIN catalog_items i ON i.id=h.item_id WHERE h.account_id=? AND i.media_type='book'`, a.ID)
	if err != nil {
		return err
	}
	type row struct{ id, item, doc string }
	all := []row{}
	for rows.Next() {
		var v row
		if err = rows.Scan(&v.id, &v.item, &v.doc); err != nil {
			rows.Close()
			return err
		}
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range all {
		it, err := s.catalog().Item(ctx, uid, v.item)
		if errors.Is(err, library.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		d := nativeObject{}
		if err = json.Unmarshal([]byte(v.doc), &d); err != nil {
			return err
		}
		listen := store.Listen{ABSID: v.id, ItemID: it.ID, LibraryID: it.LibraryID, Title: it.Title, Author: it.Author, Duration: int(it.Duration), Seconds: int(nativeNumber(d["timeListening"])), StartPos: int(nativeNumber(d["startTime"])), EndPos: int(nativeNumber(d["currentTime"])), StartedAt: store.MillisUTC(nativeMillis(d["createdAt"])), UpdatedAt: store.MillisUTC(nativeMillis(d["updatedAt"])), Source: "audiobookshelf"}
		if _, err = s.store.UpsertListen(ctx, uid, &listen); err != nil {
			return err
		}
	}
	return s.store.SaveSync(ctx, uid, store.SyncState{ServerKey: "native|" + a.ID, Backfilled: true, SyncedAt: store.Now()})
}
func (s *Server) nativeGroups(ctx context.Context, items []nativeObject, key string) []nativeObject {
	groups := map[string]nativeObject{}
	for _, it := range items {
		for _, entry := range nativeArray(nativeMetadata(it)[key]) {
			m := nativeMap(entry)
			id := nativeString(m["id"])
			if id == "" {
				continue
			}
			g := groups[id]
			if g == nil {
				g = nativeObject{"id": id, "name": m["name"], "description": m["description"], "imagePath": m["imagePath"], "books": []any{}, "libraryItems": []any{}, "addedAt": it["addedAt"]}
				groups[id] = g
			}
			copy := nativeObject{}
			for k, v := range it {
				copy[k] = v
			}
			if key == "series" {
				copy["sequence"] = m["sequence"]
			}
			g["books"] = append(nativeArray(g["books"]), copy)
			g["libraryItems"] = g["books"]
			g["numBooks"] = len(nativeArray(g["books"]))
		}
	}
	out := []nativeObject{}
	for _, g := range groups {
		var raw string
		if err := s.store.DB.QueryRowContext(ctx, `SELECT document FROM catalog_entities WHERE kind=? AND id=?`, key, g["id"]).Scan(&raw); err == nil {
			var entity nativeObject
			if json.Unmarshal([]byte(raw), &entity) == nil {
				for _, k := range []string{"description", "imagePath"} {
					if entity[k] != nil {
						g[k] = entity[k]
					}
				}
			}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return nativeString(out[i]["name"]) < nativeString(out[j]["name"]) })
	return out
}

// abs types remain the public compatibility contract, not the storage engine.
func nativeDecodeItem(it nativeObject) (abs.Item, error) {
	var out abs.Item
	err := json.Unmarshal(nativeJSON(it), &out)
	return out, err
}

func nativeMapJSON(v any) nativeObject {
	out := nativeObject{}
	_ = json.Unmarshal(nativeJSON(v), &out)
	return out
}
