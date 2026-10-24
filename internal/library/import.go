package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ImportOptions struct {
	Source          string
	Mounts          map[string]string
	Users           map[string]int64 // source account ID -> existing Pocket user ID; never guessed
	ExcludeAccounts []string         // explicitly discarded source accounts; never inferred from missing mappings
	Apply           bool
}
type ImportReport struct {
	Format           int            `json:"format"`
	Applied          bool           `json:"applied"`
	Fingerprint      string         `json:"fingerprint"`
	Counts           map[string]int `json:"counts"`
	ExcludedRows     map[string]int `json:"excluded_rows,omitempty"`
	UnmappedAccounts int            `json:"unmapped_accounts"`
	FilesAccessible  int            `json:"files_accessible"`
	MissingFiles     int            `json:"missing_files"`
	MissingFileItems []string       `json:"missing_file_items"`
	PreservedOnly    []string       `json:"preserved_only"`
}
type sourceRow map[string]any

func str(v any) string {
	if v == nil {
		return ""
	}
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}
func number(v any) float64 {
	if v == nil || str(v) == "" {
		return 0
	}
	n, err := strconv.ParseFloat(str(v), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return math.NaN()
	}
	return n
}
func truth(v any) bool { return v == true || str(v) == "1" || str(v) == "true" }
func array(v any) []any {
	a, _ := v.([]any)
	if a == nil {
		return []any{}
	}
	return a
}
func object(v any) sourceRow {
	switch v := v.(type) {
	case map[string]any:
		return sourceRow(v)
	case sourceRow:
		return v
	}
	return sourceRow{}
}
func raw(v any) string { b, _ := json.Marshal(v); return string(b) }
func milliseconds(v any) (int64, error) {
	if v == nil || str(v) == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(str(v), 10, 64); err == nil {
		return n, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999 -07:00", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, str(v)); err == nil {
			return t.UnixMilli(), nil
		}
	}
	return 0, errors.New("invalid source timestamp")
}

var sourceTables = map[string][]string{
	"libraries": {"settings", "extraData"}, "libraryFolders": {}, "libraryItems": {"libraryFiles", "extraData"},
	"books":   {"narrators", "audioFiles", "ebookFile", "chapters", "tags", "genres"},
	"authors": {}, "series": {}, "bookAuthors": {}, "bookSeries": {},
	"podcasts": {"tags", "genres"}, "podcastEpisodes": {"audioFile", "chapters", "extraData"},
	"users": {"permissions", "bookmarks"}, "mediaProgresses": {"extraData"},
	"playbackSessions": {"mediaMetadata", "extraData"},
	"collections":      {}, "collectionBooks": {}, "playlists": {}, "playlistMediaItems": {},
}

func loadSource(ctx context.Context, path string) (map[string][]sourceRow, error) {
	full, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: full, RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var check string
	if err = tx.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return nil, err
	}
	if check != "ok" {
		return nil, errors.New("source database integrity check failed")
	}
	data := map[string][]sourceRow{}
	for table, fields := range sourceTables {
		// Never load upstream password hashes, tokens, email, sessions or API keys.
		query := `SELECT * FROM "` + table + `" ORDER BY id`
		if table == "users" {
			query = `SELECT id,username,isActive,isLocked,permissions,bookmarks FROM users ORDER BY id`
		}
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("read source table %s: %w", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		data[table] = []sourceRow{}
		for rows.Next() {
			values := make([]any, len(columns))
			ptr := make([]any, len(columns))
			for i := range values {
				ptr[i] = &values[i]
			}
			if err = rows.Scan(ptr...); err != nil {
				rows.Close()
				return nil, err
			}
			row := sourceRow{}
			for i, k := range columns {
				if b, ok := values[i].([]byte); ok {
					values[i] = string(b)
				}
				row[k] = values[i]
			}
			for _, key := range fields {
				v, exists := row[key]
				if !exists {
					rows.Close()
					return nil, fmt.Errorf("unsupported source schema: %s.%s missing", table, key)
				}
				if v == nil || str(v) == "" {
					continue
				}
				var decoded any
				if err = json.Unmarshal([]byte(str(v)), &decoded); err != nil {
					rows.Close()
					return nil, fmt.Errorf("invalid JSON in %s.%s", table, key)
				}
				switch key {
				case "libraryFiles", "narrators", "audioFiles", "chapters", "tags", "genres", "bookmarks":
					if decoded != nil {
						if _, ok := decoded.([]any); !ok {
							rows.Close()
							return nil, fmt.Errorf("%s.%s must be an array", table, key)
						}
					}
				default:
					if decoded != nil {
						if _, ok := decoded.(map[string]any); !ok {
							rows.Close()
							return nil, fmt.Errorf("%s.%s must be an object", table, key)
						}
					}
				}
				row[key] = decoded
			}
			if str(row["id"]) == "" {
				rows.Close()
				return nil, fmt.Errorf("missing ID in %s", table)
			}
			data[table] = append(data[table], row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return data, tx.Commit()
}

type importRoot struct{ id, source, local string }

func importRoots(mounts map[string]string) ([]importRoot, error) {
	roots := []importRoot{}
	for source, local := range mounts {
		if !filepath.IsAbs(source) || filepath.Clean(source) != source || !filepath.IsAbs(local) {
			return nil, errors.New("mounts must map clean absolute source paths to absolute local directories")
		}
		root, err := os.OpenRoot(local)
		if err != nil {
			return nil, fmt.Errorf("open mount %s: %w", source, err)
		}
		root.Close()
		sum := sha256.Sum256([]byte(source))
		roots = append(roots, importRoot{hex.EncodeToString(sum[:16]), source, filepath.Clean(local)})
	}
	sort.Slice(roots, func(i, j int) bool {
		if len(roots[i].source) == len(roots[j].source) {
			return roots[i].source < roots[j].source
		}
		return len(roots[i].source) > len(roots[j].source)
	})
	return roots, nil
}
func mapPath(path string, roots []importRoot) (importRoot, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return importRoot{}, "", errors.New("source file path is not canonical")
	}
	for _, r := range roots {
		rel, err := filepath.Rel(r.source, path)
		if err == nil && filepath.IsLocal(rel) {
			return r, rel, nil
		}
	}
	return importRoot{}, "", errors.New("source file has no configured mount")
}

type statement struct {
	sql  string
	args []any
}

// Import reads a consistent source transaction, validates every planned row and
// file mapping, and applies all catalog changes in one transaction. Existing
// catalogs are never overwritten, even on a repeat invocation.
func (c *Catalog) Import(ctx context.Context, opt ImportOptions) (ImportReport, error) {
	report := ImportReport{Format: 1, Counts: map[string]int{}, MissingFileItems: []string{}, PreservedOnly: []string{"podcast subscription management", "collections and playlists", "source credentials and settings remain in the private source snapshot"}}
	data, err := loadSource(ctx, opt.Source)
	if err != nil {
		return report, err
	}
	roots, err := importRoots(opt.Mounts)
	if err != nil {
		return report, err
	}
	fingerprint := sha256.Sum256([]byte(raw(data)))
	report.Fingerprint = hex.EncodeToString(fingerprint[:])
	report.ExcludedRows, err = excludeSourceAccounts(data, opt)
	if err != nil {
		return report, err
	}
	pending := []statement{}
	add := func(table, columns, marks string, args ...any) {
		pending = append(pending, statement{`INSERT INTO ` + table + ` (` + columns + `) VALUES (` + marks + `)`, args})
	}
	for _, r := range roots {
		add("catalog_roots", "id,source_path,local_path", "?,?,?", r.id, r.source, r.local)
	}
	for _, l := range data["libraries"] {
		add("catalog_libraries", "id,name,media_type,display_order,document", "?,?,?,?,?", l["id"], str(l["name"]), l["mediaType"], l["displayOrder"], raw(l))
	}
	sourceAccounts := map[string]bool{}
	mappedUsers := map[int64]bool{}
	for _, a := range data["users"] {
		id := str(a["id"])
		sourceAccounts[id] = true
		if _, ok := a["permissions"].(map[string]any); !ok {
			return report, fmt.Errorf("invalid permissions for source account %s", id)
		}
		var user any
		if uid, ok := opt.Users[id]; ok {
			if uid <= 0 || mappedUsers[uid] {
				return report, errors.New("account mappings must name distinct existing Pocket user IDs")
			}
			mappedUsers[uid] = true
			user = uid
			var exists int
			if err = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id=?`, uid).Scan(&exists); err != nil {
				return report, err
			}
			if exists != 1 {
				return report, fmt.Errorf("Pocket user %d does not exist", uid)
			}
		} else {
			report.UnmappedAccounts++
		}
		add("catalog_accounts", "id,pocket_user_id,username,active,locked,permissions", "?,?,?,?,?,?", id, user, str(a["username"]), truth(a["isActive"]), truth(a["isLocked"]), raw(a["permissions"]))
	}
	for id := range opt.Users {
		if !sourceAccounts[id] {
			return report, fmt.Errorf("unknown source account in user map: %s", id)
		}
	}
	media := map[string]sourceRow{}
	itemsByMedia := map[string]string{}
	items := map[string]sourceRow{}
	authors := map[string]sourceRow{}
	authorRefs := map[string][]any{}
	series := map[string]sourceRow{}
	seriesRefs := map[string][]any{}
	episodes := map[string][]any{}
	episodeItem := map[string]string{}
	for _, a := range data["authors"] {
		authors[str(a["id"])] = a
	}
	for _, s := range data["series"] {
		series[str(s["id"])] = s
	}
	for _, ref := range data["bookAuthors"] {
		id := str(ref["bookId"])
		a, ok := authors[str(ref["authorId"])]
		if !ok {
			return report, errors.New("orphan book author")
		}
		authorRefs[id] = append(authorRefs[id], a)
	}
	for _, ref := range data["bookSeries"] {
		id := str(ref["bookId"])
		s, ok := series[str(ref["seriesId"])]
		if !ok {
			return report, errors.New("orphan book series")
		}
		seriesRefs[id] = append(seriesRefs[id], sourceRow{"id": s["id"], "name": s["name"], "sequence": ref["sequence"]})
	}
	for _, m := range append(append([]sourceRow{}, data["books"]...), data["podcasts"]...) {
		media[str(m["id"])] = m
	}
	for _, it := range data["libraryItems"] {
		id := str(it["id"])
		items[id] = it
		itemsByMedia[str(it["mediaId"])] = id
	}
	for mid := range media {
		if itemsByMedia[mid] == "" {
			return report, fmt.Errorf("media %s has no library item", mid)
		}
	}
	for _, ep := range data["podcastEpisodes"] {
		pod := str(ep["podcastId"])
		if itemsByMedia[pod] == "" || str(items[itemsByMedia[pod]]["mediaType"]) != "podcast" {
			return report, fmt.Errorf("episode %s has no podcast item", str(ep["id"]))
		}
		episodes[pod] = append(episodes[pod], ep)
		episodeItem[str(ep["id"])] = itemsByMedia[pod]
	}
	missingItems := map[string]bool{}
	for _, it := range data["libraryItems"] {
		id, mid := str(it["id"]), str(it["mediaId"])
		m, ok := media[mid]
		if !ok {
			return report, fmt.Errorf("item %s has no media record", id)
		}
		added, err := milliseconds(it["createdAt"])
		if err != nil {
			return report, err
		}
		updated, err := milliseconds(it["updatedAt"])
		if err != nil {
			return report, err
		}
		author := str(it["authorNamesFirstLast"])
		if author == "" {
			author = str(m["author"])
		}
		doc := sourceRow{"item": it, "media": m, "authors": array(authorRefs[mid]), "series": array(seriesRefs[mid]), "episodes": array(episodes[mid])}
		add("catalog_items", "id,library_id,media_id,media_type,title,author,duration,explicit,missing,added_at,updated_at,tags,document", "?,?,?,?,?,?,?,?,?,?,?,?,?", id, it["libraryId"], mid, it["mediaType"], str(m["title"]), author, number(m["duration"]), truth(m["explicit"]), truth(it["isMissing"]), added, updated, raw(array(m["tags"])), raw(doc))
		// Consolidate tracked files with richer audio and ebook metadata. Some podcast
		// episode files aren't present in libraryFiles, so include them explicitly.
		files := map[string]sourceRow{}
		for _, f := range array(it["libraryFiles"]) {
			v := object(f)
			files[str(v["ino"])] = v
		}
		merge := func(v sourceRow, role string) error {
			if len(v) == 0 {
				return nil
			}
			ino := str(v["ino"])
			if ino == "" {
				return errors.New("source file has no ID")
			}
			old := files[ino]
			if old == nil {
				old = sourceRow{}
			}
			for k, val := range v {
				old[k] = val
			}
			old["role"] = role
			files[ino] = old
			return nil
		}
		for _, f := range array(m["audioFiles"]) {
			if err = merge(object(f), "audio"); err != nil {
				return report, err
			}
		}
		if err = merge(object(m["ebookFile"]), "ebook"); err != nil {
			return report, err
		}
		for _, e := range episodes[mid] {
			if err = merge(object(object(e)["audioFile"]), "audio"); err != nil {
				return report, err
			}
		}
		if cover := str(m["coverPath"]); cover != "" {
			sum := sha256.Sum256([]byte(cover))
			files["cover-"+hex.EncodeToString(sum[:8])] = sourceRow{"role": "cover", "metadata": sourceRow{"path": cover, "filename": filepath.Base(cover)}}
		}
		keys := make([]string, 0, len(files))
		for k := range files {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, fid := range keys {
			f := files[fid]
			meta := object(f["metadata"])
			path := str(meta["path"])
			if fid == "" || path == "" {
				return report, fmt.Errorf("item %s has a file without ID or path", id)
			}
			root, rel, err := mapPath(path, roots)
			if err != nil {
				return report, fmt.Errorf("map file for item %s: %w", id, err)
			}
			role := str(f["role"])
			if role == "" {
				role = "supplement"
				if str(f["fileType"]) == "ebook" {
					role = "ebook"
				}
			}
			mime := str(f["mimeType"])
			if mime == "" {
				mime = mimeFor(path)
			}
			size := int64(number(meta["size"]))
			dir, err := os.OpenRoot(root.local)
			if err != nil {
				return report, err
			}
			fh, openErr := dir.Open(rel)
			dir.Close()
			if openErr != nil {
				if errors.Is(openErr, os.ErrNotExist) {
					report.MissingFiles++
					missingItems[id] = true
				} else {
					return report, fmt.Errorf("cannot safely open file for item %s: %w", id, openErr)
				}
			} else {
				info, err := fh.Stat()
				fh.Close()
				if err != nil {
					return report, err
				}
				if !info.Mode().IsRegular() {
					return report, fmt.Errorf("item %s contains a nonregular file", id)
				}
				size = info.Size()
				report.FilesAccessible++
			}
			add("catalog_files", "item_id,id,role,root_id,relative_path,name,mime,size,duration,track_index,excluded,document", "?,?,?,?,?,?,?,?,?,?,?,?", id, fid, role, root.id, rel, filepath.Base(path), mime, size, number(f["duration"]), int(number(f["index"])), truth(f["exclude"]), raw(f))
		}
	}
	for _, table := range []string{"libraryFolders", "authors", "series", "bookAuthors", "bookSeries", "podcastEpisodes", "collections", "collectionBooks", "playlists", "playlistMediaItems"} {
		for _, entity := range data[table] {
			add("catalog_entities", "kind,id,library_id,document", "?,?,?,?", table, entity["id"], entity["libraryId"], raw(entity))
		}
	}
	for _, p := range data["mediaProgresses"] {
		mid := str(p["mediaItemId"])
		item, episode := itemsByMedia[mid], ""
		if item == "" {
			item, episode = episodeItem[mid], mid
		}
		if item == "" {
			return report, fmt.Errorf("progress %s refers to missing media", str(p["id"]))
		}
		updated, err := milliseconds(p["updatedAt"])
		if err != nil {
			return report, err
		}
		add("catalog_progress", "id,account_id,item_id,episode_id,position,finished,updated_at,document", "?,?,?,?,?,?,?,?", p["id"], p["userId"], item, episode, number(p["currentTime"]), truth(p["isFinished"]), updated, raw(p))
	}
	for _, h := range data["playbackSessions"] {
		mid := str(h["mediaItemId"])
		var item any
		if id := itemsByMedia[mid]; id != "" {
			item = id
		} else if id = episodeItem[mid]; id != "" {
			item = id
		}
		add("catalog_history", "id,account_id,item_id,document", "?,?,?,?", h["id"], h["userId"], item, raw(h))
	}
	for _, a := range data["users"] {
		for _, value := range array(a["bookmarks"]) {
			b := object(value)
			item := str(b["libraryItemId"])
			if _, ok := items[item]; !ok {
				return report, errors.New("bookmark refers to missing item")
			}
			add("catalog_bookmarks", "account_id,item_id,position,document", "?,?,?,?", a["id"], item, number(b["time"]), raw(b))
		}
	}
	for k := range missingItems {
		report.MissingFileItems = append(report.MissingFileItems, k)
	}
	sort.Strings(report.MissingFileItems)
	for table, values := range data {
		report.Counts[table] = len(values)
	}
	report.Counts["catalog_files"] = report.FilesAccessible + report.MissingFiles
	// Execute the same transaction for dry runs, then roll it back: checks every
	// FK/unique/check constraint without leaving a partial import behind.
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	var existing int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM catalog_imports)+(SELECT count(*) FROM catalog_items)+(SELECT count(*) FROM catalog_libraries)+(SELECT count(*) FROM catalog_accounts)+(SELECT count(*) FROM catalog_roots)`).Scan(&existing); err != nil {
		return report, err
	}
	if existing > 0 {
		return report, ErrImported
	}
	for _, stmt := range pending {
		if _, err = tx.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return report, fmt.Errorf("catalog import validation: %w", err)
		}
	}
	report.Applied = opt.Apply
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_imports(id,source_fingerprint,imported_at,report) VALUES(?,?,?,?)`, report.Fingerprint, report.Fingerprint, time.Now().UTC().Format(time.RFC3339Nano), raw(report)); err != nil {
		report.Applied = false
		return report, err
	}
	if !opt.Apply {
		return report, nil
	}
	if err = tx.Commit(); err != nil {
		report.Applied = false
		return report, err
	}
	return report, nil
}

func mimeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return "audio/mpeg"
	case ".m4b", ".m4a", ".mp4":
		return "audio/mp4"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".wav":
		return "audio/wav"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".epub":
		return "application/epub+zip"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

// ReadJSONFile bounds operator-provided migration configuration and rejects
// trailing values. The files may contain account identifiers, never passwords.
func ReadJSONFile(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	if err = d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return errors.New("configuration must contain exactly one JSON value")
	}
	return nil
}

// Exclusions affect only the new import, never the read-only source snapshot.
func excludeSourceAccounts(data map[string][]sourceRow, opt ImportOptions) (map[string]int, error) {
	excluded := map[string]bool{}
	known := map[string]bool{}
	for _, u := range data["users"] {
		known[str(u["id"])] = true
	}
	for _, id := range opt.ExcludeAccounts {
		if !known[id] {
			return nil, fmt.Errorf("excluded account %q does not exist", id)
		}
		if _, mapped := opt.Users[id]; mapped {
			return nil, fmt.Errorf("account %q is both mapped and excluded", id)
		}
		excluded[id] = true
	}
	counts := map[string]int{}
	filter := func(table, key string, ids map[string]bool) {
		kept := []sourceRow{}
		for _, row := range data[table] {
			if ids[str(row[key])] {
				counts[table]++
			} else {
				kept = append(kept, row)
			}
		}
		data[table] = kept
	}
	for _, pair := range [][3]string{{"playlists", "playlistMediaItems", "playlistId"}, {"collections", "collectionBooks", "collectionId"}} {
		owned := map[string]bool{}
		for _, row := range data[pair[0]] {
			if excluded[str(row["userId"])] {
				owned[str(row["id"])] = true
			}
		}
		filter(pair[1], pair[2], owned)
		filter(pair[0], "id", owned)
	}
	filter("users", "id", excluded) // bookmarks are embedded in each source user.
	filter("mediaProgresses", "userId", excluded)
	filter("playbackSessions", "userId", excluded)
	return counts, nil
}
