package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) nativeAPI(w http.ResponseWriter, r *http.Request, uid int64) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx := r.Context()
	c := s.catalog()
	account, err := c.Account(ctx, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	path := r.URL.Path
	if strings.ContainsAny(path, "\\\x00%") {
		writeErr(w, 400, "invalid path")
		return
	}
	for _, v := range strings.Split(path, "/") {
		if v == "." || v == ".." {
			writeErr(w, 400, "invalid path")
			return
		}
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	method := r.Method
	read := method == "GET" || method == "HEAD"
	respond := func(v any) { writeJSON(w, 200, v) }
	body := func() (nativeObject, bool) {
		v := nativeObject{}
		if err := decode(r, &v); err != nil && !(errors.Is(err, io.EOF) && strings.HasSuffix(path, "/close")) {
			writeErr(w, 400, "invalid JSON")
			return nil, false
		}
		return v, true
	}
	if len(parts) == 3 && parts[0] == "hls" && read {
		s.nativeHLS(w, r, uid, parts[1], parts[2])
		return
	}
	if read && (path == "/api/search/providers" || path == "/api/search/books" || path == "/api/search/covers") {
		u, err := s.store.GetUser(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		r = r.WithContext(context.WithValue(ctx, userKey, u))
		if path == "/api/search/providers" {
			s.nativeProviders(w, r)
		} else {
			s.nativeMetadataSearch(w, r)
		}
		return
	}
	if path == "/api/feeds" || strings.HasPrefix(path, "/api/feeds/") || strings.HasPrefix(path, "/api/share/mediaitem") {
		s.nativeShareCompat(w, r, uid)
		return
	}
	if path == "/api/search/podcast" || path == "/api/podcasts" || strings.HasPrefix(path, "/api/podcasts/") {
		writeErr(w, 404, "podcasts are handled by your dedicated podcast app")
		return
	}
	if path == "/api/upload" && method == "POST" {
		s.nativeUpload(w, r, uid)
		return
	}
	if path == "/status" && read {
		respond(nativeObject{"app": "audiobookshelf", "serverVersion": "2.37.2", "backend": "Pocket Audiobooks native", "isInit": true, "authMethods": []string{"local"}})
		return
	}
	if path == "/api/me" && read {
		v, err := s.nativeMe(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		respond(v)
		return
	}
	if path == "/api/authorize" && method == "POST" {
		me, err := s.nativeMe(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		libs, err := s.nativeLibraries(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		first := ""
		if len(libs) > 0 {
			first = nativeString(nativeMap(libs[0])["id"])
		}
		respond(nativeObject{"user": me, "userDefaultLibraryId": first, "serverSettings": nativeObject{"authActiveAuthMethods": []string{"local"}}, "Source": "Pocket Audiobooks native"})
		return
	}
	if method == "POST" && (path == "/api/session/local" || path == "/api/session/local-all") {
		s.nativeOffline(w, r, uid, path == "/api/session/local-all")
		return
	}
	if len(parts) >= 2 && parts[0] == "api" && (parts[1] == "collections" || parts[1] == "playlists") {
		s.nativeListAPI(w, r, uid, parts[1], parts[2:])
		return
	}
	if strings.HasPrefix(path, "/api/libraries") && !read {
		s.nativeLibraryWrite(w, r, uid)
		return
	}
	if path == "/api/libraries" && read {
		libs, err := s.nativeLibraries(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		respond(nativeObject{"libraries": libs})
		return
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "libraries" && read {
		s.nativeLibraryAPI(w, r, uid, parts[2], parts[3:])
		return
	}
	if path == "/api/items/batch/update" && method == "POST" {
		s.nativeBulkCompat(w, r, uid)
		return
	}
	if path == "/api/items/batch/delete" && method == "POST" {
		var in struct {
			IDs []string `json:"libraryItemIds"`
		}
		if decode(r, &in) != nil {
			writeErr(w, 400, "invalid item list")
			return
		}
		s.nativeDeleteCompat(w, r, uid, in.IDs)
		return
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "items" {
		if len(parts) == 3 && method == "DELETE" {
			s.nativeDeleteCompat(w, r, uid, []string{parts[2]})
			return
		}
		id := parts[2]
		if len(parts) == 4 && parts[3] == "tracks" && method == "PATCH" {
			s.nativeTracksCompat(w, r, uid, id)
			return
		}
		if len(parts) == 3 && read {
			it, err := s.nativeItem(ctx, uid, id)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			progress, err := s.nativeProgress(ctx, uid)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			for _, p := range progress {
				if nativeMap(p)["libraryItemId"] == id {
					it["userMediaProgress"] = p
					break
				}
			}
			respond(it)
			return
		}
		if len(parts) >= 4 && parts[3] == "play" && method == "POST" {
			in, ok := body()
			if !ok {
				return
			}

			episode := ""
			if len(parts) == 5 {
				episode = parts[4]
			}
			tracks, _, _, err := c.Timeline(ctx, uid, id, episode)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			transcode := nativeBool(in["forceTranscode"])
			if !nativeBool(in["forceDirectPlay"]) && len(nativeArray(in["supportedMimeTypes"])) > 0 {
				supported := map[string]bool{}
				for _, v := range nativeArray(in["supportedMimeTypes"]) {
					supported[nativeString(v)] = true
				}
				for _, tr := range tracks {
					if !supported[tr.MIME] {
						transcode = true
					}
				}
			}
			if transcode {
				if _, err = exec.LookPath(s.cfg.FFmpeg); err != nil {
					writeErr(w, 503, "audio converter is unavailable")
					return
				}
			}
			device := nativeMap(in["deviceInfo"])
			sess, err := c.OpenSession(ctx, uid, id, episode, nativeString(device["deviceId"]), nativeString(device["deviceName"]))
			if err != nil {
				s.nativeError(w, err)
				return
			}
			it, err := s.nativeItem(ctx, uid, id)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			respond(s.nativePlaybackWire(sess, it, transcode))
			return
		}
		if len(parts) >= 4 && read {
			switch parts[3] {
			case "file":
				if len(parts) == 5 || len(parts) == 6 {
					s.nativeFile(w, r, uid, id, parts[4], len(parts) == 6)
					return
				}
			case "download":
				s.nativeDownload(w, r, uid, []string{id})
				return
			case "cover", "ebook":
				files, err := c.Files(ctx, uid, id)
				if err != nil {
					s.nativeError(w, err)
					return
				}
				want := parts[3]
				if want == "cover" {
					for _, f := range files {
						if f.ID == "native-cover" {
							s.nativeFile(w, r, uid, id, f.ID, false)
							return
						}
					}
				}
				primary := ""
				if want == "ebook" {
					if item, e := s.nativeItem(ctx, uid, id); e == nil {
						primary = nativeString(nativeMap(nativeMap(item["media"])["ebookFile"])["ino"])
					}
				}
				for _, f := range files {
					match := f.Role == want
					if want == "ebook" && len(parts) == 5 && f.Role == "supplement" {
						ext := strings.ToLower(filepath.Ext(f.Name))
						match = ext == ".epub" || ext == ".pdf" || ext == ".mobi" || ext == ".azw3" || ext == ".cbr" || ext == ".cbz"
					}
					if want == "ebook" && len(parts) < 5 && primary != "" {
						match = f.ID == primary
					}
					if match && (len(parts) < 5 || parts[4] == f.ID) {
						s.nativeFile(w, r, uid, id, f.ID, want == "ebook")
						return
					}
				}
				writeErr(w, 404, "file not found")
				return
			}
		}
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "session" {
		sid := parts[2]
		sess, err := c.Session(ctx, uid, sid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		if len(parts) == 3 && read {
			it, err := s.nativeItem(ctx, uid, sess.ItemID)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			respond(nativeSessionWire(sess, it))
			return
		}
		if len(parts) == 4 && method == "POST" && (parts[3] == "sync" || parts[3] == "close") {
			in, ok := body()
			if !ok {
				return
			}
			report := library.Report{ID: nativeString(in["reportId"]), Position: nativeNumber(in["currentTime"]), Listened: nativeNumber(in["timeListened"]), ObservedAt: nativeMillis(in["reportedAt"]), Playing: parts[3] == "sync", Close: parts[3] == "close"}
			if _, exists := in["currentTime"]; !exists {
				report.Position = sess.Position
			}
			if playing, ok := in["playing"].(bool); ok {
				report.Playing = playing && !report.Close
			}
			if report.ObservedAt == 0 {
				report.ObservedAt = time.Now().UnixMilli()
			}
			if report.ID == "" {
				// Audiobookshelf apps send no receipt ID. Derive one from the fields a
				// retry repeats verbatim, so a resent sync is a duplicate, not a delta.
				key := nativeObject{"kind": parts[3]}
				for _, k := range []string{"currentTime", "timeListened", "duration", "updatedAt", "reportedAt", "syncedAt"} {
					key[k] = in[k]
				}
				sum := sha256.Sum256([]byte(sid + "\x00" + string(nativeJSON(key))))
				report.ID = "compat-" + hex.EncodeToString(sum[:])
				report.Replay = true
			}
			out, err := c.ReportSession(ctx, uid, sid, report)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			if out.SessionEnded && parts[3] == "sync" {
				// Audiobookshelf answers 404 for sessions it no longer holds; its apps
				// then open a new session from the newest progress. Listening counted.
				writeErr(w, 404, "playback session ended; start a new session")
				return
			}
			if out.ProgressApplied {
				s.nativeSocketProgress(ctx, uid, sid)
			}
			if out.FinishedNow {
				s.nativeNotifyFinished(ctx, uid, sess.ItemID)
			}
			respond(out)
			return
		}
	}
	if path == "/api/me/items-in-progress" && read {
		s.nativeInProgress(w, r, uid)
		return
	}
	if path == "/api/me/listening-stats" && read {
		s.nativeListeningStats(w, r, uid)
		return
	}
	if path == "/api/me/listening-sessions" && read {
		all, err := s.nativeSessions(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		per := queryInt(r, "itemsPerPage")
		if per <= 0 {
			per = 100
		}
		per = min(per, 500)
		page := max(0, queryInt(r, "page"))
		start := min(page*per, len(all))
		end := min(start+per, len(all))
		respond(nativeObject{"sessions": all[start:end], "total": len(all), "numPages": (len(all) + per - 1) / per, "page": page, "itemsPerPage": per})
		return
	}
	if len(parts) >= 4 && parts[0] == "api" && parts[1] == "me" && parts[2] == "progress" {
		id := parts[3]
		if method == "PATCH" && len(parts) <= 5 {
			in, ok := body()
			if !ok {
				return
			}
			delete(in, "duration")
			delete(in, "progress")
			episode := ""
			if len(parts) == 5 {
				episode = parts[4]
			}
			finished, err := c.EditProgress(ctx, uid, id, episode, in)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			if finished {
				s.nativeNotifyFinished(ctx, uid, id)
			}
			respond(nativeObject{"success": true})
			return
		}
		var item, episode string
		err = s.store.DB.QueryRowContext(ctx, `SELECT item_id,episode_id FROM catalog_progress WHERE account_id=? AND id=?`, account.ID, id).Scan(&item, &episode)
		if errors.Is(err, sql.ErrNoRows) {
			err = library.ErrNotFound
		}
		if err != nil {
			s.nativeError(w, err)
			return
		}
		if method == "DELETE" && len(parts) == 4 {
			if err = c.SetProgress(ctx, uid, item, episode, map[string]any{"currentTime": float64(0), "isFinished": false}); err != nil {
				s.nativeError(w, err)
				return
			}
			respond(nativeObject{"success": true})
			return
		}
		if read && len(parts) == 5 && parts[4] == "remove-from-continue-listening" {
			if err = c.SetProgress(ctx, uid, item, episode, map[string]any{"hideFromContinueListening": true}); err != nil {
				s.nativeError(w, err)
				return
			}
			respond(nativeObject{"success": true})
			return
		}
	}
	if len(parts) >= 5 && parts[0] == "api" && parts[1] == "me" && parts[2] == "item" && parts[4] == "bookmark" {
		id := parts[3]
		var pos float64
		title := ""
		remove := method == "DELETE"
		if remove && len(parts) == 6 {
			pos, err = strconv.ParseFloat(parts[5], 64)
			if err != nil {
				writeErr(w, 400, "invalid bookmark time")
				return
			}
		} else if (method == "POST" || method == "PATCH") && len(parts) == 5 {
			in, ok := body()
			if !ok {
				return
			}
			pos = nativeNumber(in["time"])
			title = nativeString(in["title"])
		} else {
			writeErr(w, 405, "method not allowed")
			return
		}
		if err = c.Bookmark(ctx, uid, id, pos, title, remove); err != nil {
			s.nativeError(w, err)
			return
		}
		respond(nativeObject{"libraryItemId": id, "time": pos, "title": title})
		return
	}
	if len(parts) >= 3 && parts[0] == "api" && (parts[1] == "authors" || parts[1] == "series") && read {
		items, err := s.nativeItems(ctx, uid, "")
		if err != nil {
			s.nativeError(w, err)
			return
		}
		for _, g := range s.nativeGroups(ctx, items, parts[1]) {
			if nativeString(g["id"]) == parts[2] {
				if len(parts) == 4 && parts[3] == "image" {
					s.nativeImage(w, r, nativeString(g["imagePath"]))
					return
				}
				respond(g)
				return
			}
		}
		writeErr(w, 404, "not found")
		return
	}
	writeErr(w, 501, "this operation is not implemented by the native backend yet")
}
func (s *Server) nativeFile(w http.ResponseWriter, r *http.Request, uid int64, item, file string, download bool) {
	u, err := s.store.GetUser(r.Context(), uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", item)
	rc.URLParams.Add("file", file)
	ctx := context.WithValue(r.Context(), userKey, u)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rc)
	r = r.Clone(ctx)
	if download {
		q := r.URL.Query()
		q.Set("download", "1")
		r.URL.RawQuery = q.Encode()
	}
	s.handleCatalogFile(w, r)
}
func (s *Server) nativeImage(w http.ResponseWriter, r *http.Request, path string) {
	rows, err := s.store.DB.QueryContext(r.Context(), `SELECT source_path,local_path FROM catalog_roots ORDER BY length(source_path) DESC`)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	root, relative := "", ""
	for rows.Next() {
		var src, dst string
		if err = rows.Scan(&src, &dst); err != nil {
			rows.Close()
			s.nativeError(w, err)
			return
		}
		rel, e := filepath.Rel(src, path)
		if e == nil && filepath.IsLocal(rel) {
			root, relative = dst, rel
			break
		}
	}
	rows.Close()
	if root == "" {
		writeErr(w, 404, "image not found")
		return
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		writeErr(w, 404, "image not found")
		return
	}
	defer dir.Close()
	file, err := dir.Open(relative)
	if err != nil {
		writeErr(w, 404, "image not found")
		return
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() {
		writeErr(w, 404, "image not found")
		return
	}
	w.Header().Set("Content-Type", imageType(path))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), file)
}
func (s *Server) nativeSessions(ctx context.Context, uid int64) ([]nativeObject, error) {
	a, err := s.catalog().Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	sessions, err := s.catalog().RecentSessions(ctx, uid, -1)
	if err != nil {
		return nil, err
	}
	// Many sessions share a book; build each item's wire object only once.
	items := map[string]nativeObject{}
	item := func(id string) (nativeObject, error) {
		if it, ok := items[id]; ok {
			return it, nil
		}
		it, err := s.nativeItem(ctx, uid, id)
		if err == nil {
			items[id] = it
		}
		return it, err
	}
	out := []nativeObject{}
	for _, sess := range sessions {
		it, err := item(sess.ItemID)
		if err != nil {
			return nil, err
		}
		out = append(out, nativeSessionWire(sess, it))
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id,item_id,document FROM catalog_history WHERE account_id=?`, a.ID)
	if err != nil {
		return nil, err
	}
	type row struct {
		id   string
		item sql.NullString
		raw  string
	}
	all := []row{}
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.id, &r.item, &r.raw); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		if !r.item.Valid {
			continue
		}
		it, err := item(r.item.String)
		if errors.Is(err, library.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		d := nativeObject{}
		if err = json.Unmarshal([]byte(r.raw), &d); err != nil {
			return nil, err
		}
		d["id"], d["libraryItemId"], d["libraryId"], d["mediaType"] = r.id, r.item.String, it["libraryId"], it["mediaType"]
		d["startedAt"], d["updatedAt"] = nativeMillis(d["createdAt"]), nativeMillis(d["updatedAt"])
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return nativeNumber(out[i]["updatedAt"]) > nativeNumber(out[j]["updatedAt"]) })
	return out, nil
}

func (s *Server) nativeLibraryAPI(w http.ResponseWriter, r *http.Request, uid int64, lib string, tail []string) {
	ctx := r.Context()
	libs, err := s.nativeLibraries(ctx, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	var current nativeObject
	for _, l := range libs {
		if nativeString(nativeMap(l)["id"]) == lib {
			current = nativeMap(l)
			break
		}
	}
	if current == nil {
		writeErr(w, 404, "library not found")
		return
	}
	if len(tail) == 0 {
		writeJSON(w, 200, current)
		return
	}
	items, err := s.nativeItems(ctx, uid, lib)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	progress, err := s.nativeProgress(ctx, uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	byItem := map[string]nativeObject{}
	for _, p := range progress {
		m := nativeMap(p)
		if nativeString(m["episodeId"]) == "" {
			byItem[nativeString(m["libraryItemId"])] = m
		}
	}
	for _, it := range items {
		it["userMediaProgress"] = byItem[nativeString(it["id"])]
	}
	respond := func(v any) { writeJSON(w, 200, v) }
	q := r.URL.Query()
	limit := queryInt(r, "limit")
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	page := max(0, queryInt(r, "page"))
	switch tail[0] {
	case "collections", "playlists":
		lists, err := s.catalog().Lists(ctx, uid, tail[0], lib)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		all := []any{}
		for _, l := range lists {
			v, err := s.nativeListWire(ctx, uid, l)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			all = append(all, v)
		}
		start, end := nativePage(len(all), page, limit)
		respond(nativeObject{"results": all[start:end], "total": len(all), "limit": limit, "page": page})

	case "items":
		group, value, e := nativeFilter(q.Get("filter"))
		if e != nil {
			writeErr(w, 400, e.Error())
			return
		}
		filtered := []nativeObject{}
		for _, it := range items {
			if nativeMatchFilter(it, byItem[nativeString(it["id"])], group, value) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
		if q.Get("collapseseries") == "1" && group != "series" {
			items = nativeCollapseSeries(items)
		}
		nativeSortItems(items, q.Get("sort"), q.Get("desc") == "1", q.Get("seed"))
		start, end := nativePage(len(items), page, limit)
		respond(nativeObject{"results": items[start:end], "total": len(items), "limit": limit, "page": page, "sortBy": q.Get("sort"), "sortDesc": q.Get("desc") == "1", "filterBy": q.Get("filter"), "mediaType": current["mediaType"]})

	case "series", "authors":
		groups := s.nativeGroups(ctx, items, tail[0])
		if len(tail) > 1 {
			for _, g := range groups {
				if nativeString(g["id"]) == tail[1] {
					respond(g)
					return
				}
			}
			writeErr(w, 404, "not found")
			return
		}
		if tail[0] == "authors" {
			start, end := nativePage(len(groups), page, limit)
			respond(nativeObject{"authors": groups, "results": groups[start:end], "total": len(groups), "limit": limit, "page": page})
			return
		}
		start, end := nativePage(len(groups), page, limit)
		respond(nativeObject{"results": groups[start:end], "total": len(groups)})
	case "search":
		term := strings.ToLower(strings.TrimSpace(q.Get("q")))
		if term == "" {
			writeErr(w, 400, "search text is required")
			return
		}
		matches := []any{}
		for _, it := range items {
			m := nativeMetadata(it)
			text := nativeString(m["title"]) + " " + nativeString(m["subtitle"]) + " " + nativeString(m["authorName"]) + " " + nativeString(m["author"]) + " " + nativeString(m["narratorName"])
			if strings.Contains(strings.ToLower(text), term) && len(matches) < limit {
				matches = append(matches, nativeObject{"libraryItem": it})
			}
		}
		authors, series := []any{}, []any{}
		for _, g := range s.nativeGroups(ctx, items, "authors") {
			if strings.Contains(strings.ToLower(nativeString(g["name"])), term) && len(authors) < limit {
				authors = append(authors, g)
			}
		}
		for _, g := range s.nativeGroups(ctx, items, "series") {
			if strings.Contains(strings.ToLower(nativeString(g["name"])), term) && len(series) < limit {
				series = append(series, nativeObject{"series": g, "books": g["books"]})
			}
		}
		result := nativeObject{"book": []any{}, "podcast": []any{}, "authors": authors, "series": series}
		kind := "book"
		if current["mediaType"] == "podcast" {
			kind = "podcast"
		}
		result[kind] = matches
		for _, key := range []string{"narrators", "tags", "genres"} {
			counts := map[string]int{}
			for _, it := range items {
				list := nativeMetadata(it)[key]
				if key == "tags" {
					list = nativeMap(it["media"])[key]
				}
				for _, v := range nativeArray(list) {
					name := nativeString(v)
					if strings.Contains(strings.ToLower(name), term) {
						counts[name]++
					}
				}
			}
			names := []string{}
			for name := range counts {
				names = append(names, name)
			}
			sort.Strings(names)
			values := []any{}
			for _, name := range names[:min(limit, len(names))] {
				values = append(values, nativeObject{"name": name, "numBooks": counts[name], "numItems": counts[name]})
			}
			result[key] = values
		}
		respond(result)

	case "personalized":
		respond(s.nativeShelves(items, progress, limit))

	case "stats":
		respond(s.nativeLibraryStats(r, items))
	case "narrators":
		respond(nativeObject{"narrators": nativeNarrators(items)})
	case "download":
		ids := strings.Split(q.Get("ids"), ",")
		allowed := map[string]bool{}
		for _, it := range items {
			allowed[nativeString(it["id"])] = true
		}
		for _, id := range ids {
			if !allowed[id] {
				writeErr(w, 404, "book not found in library")
				return
			}
		}
		s.nativeDownload(w, r, uid, ids)

	case "filterdata":
		data := nativeFilterData(items)
		data["authors"] = s.nativeGroups(ctx, items, "authors")
		data["series"] = s.nativeGroups(ctx, items, "series")
		data["authorCount"] = len(data["authors"].([]nativeObject))
		data["seriesCount"] = len(data["series"].([]nativeObject))
		respond(data)

	default:
		writeErr(w, 501, "native library operation not implemented yet")
	}
}

// Native web reports use durable client-generated receipt IDs. This bypasses
// the compatibility client's delta-only sync call and preserves retry identity.
func (s *Server) nativeWebSync(w http.ResponseWriter, r *http.Request) {
	var in struct {
		library.Report
		ItemID string `json:"item_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	uid := actorID(r.Context())
	id := chi.URLParam(r, "sid")
	// Older web bundles can finish their current session during an upgrade. New
	// bundles always provide a stable report ID and observed timestamp.
	if in.ID == "" {
		in.ID = "legacy-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if in.ObservedAt == 0 {
		in.ObservedAt = time.Now().UnixMilli()
	}
	out, err := s.catalog().ReportSession(r.Context(), uid, id, in.Report)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if out.ProgressApplied {
		s.nativeSocketProgress(r.Context(), uid, id)
	}
	if out.FinishedNow {
		if session, err := s.catalog().Session(r.Context(), uid, id); err == nil {
			s.nativeNotifyFinished(r.Context(), uid, session.ItemID)
		}
	}

	writeJSON(w, 200, nativeObject{"ok": true, "duplicate": out.Duplicate, "progress_applied": out.ProgressApplied, "session_ended": out.SessionEnded})
}

// Read durable sessions so another device remains visible after a server restart.
func (s *Server) nativeNowPlaying(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	out := nativeObject{"active": false, "server_time": now.UnixMilli()}
	sessions, err := s.catalog().RecentSessions(r.Context(), actorID(r.Context()), 100)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	for _, sess := range sessions {
		if sess.Closed || sess.ID == r.URL.Query().Get("session") || (r.URL.Query().Get("client") != "" && sess.Client == deviceID(r.URL.Query().Get("client"))) || now.UnixMilli()-sess.UpdatedAt > 120000 {
			continue
		}
		it, err := s.catalog().Item(r.Context(), actorID(r.Context()), sess.ItemID)
		if err != nil {
			continue
		}
		out = nativeObject{"active": true, "server_time": now.UnixMilli(), "playing": sess.Playing && now.UnixMilli()-sess.UpdatedAt < 40000, "item_id": it.ID, "episode_id": sess.EpisodeID, "title": it.Title, "author": it.Author, "cover": "/api/v1/cover/" + it.ID, "position": sess.Position, "duration": sess.Duration, "device": firstNonEmpty(sess.Device, "another device"), "app": "Pocket Audiobooks", "updated_at": sess.UpdatedAt}
		break
	}
	writeJSON(w, 200, out)
}

func (s *Server) nativeNotifyFinished(ctx context.Context, uid int64, item string) {
	var finish store.Finish
	err := s.store.DB.QueryRowContext(ctx, `SELECT item_id,title,author,duration,finished_at FROM finishes WHERE user_id=? AND item_id=? ORDER BY finished_at DESC LIMIT 1`, uid, item).Scan(&finish.ItemID, &finish.Title, &finish.Author, &finish.Duration, &finish.FinishedAt)
	if err != nil {
		return
	}
	u, err := s.store.GetUser(ctx, uid)
	if err == nil {
		s.finished(ctx, u, &finish)
	}
}
