package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
)

type toolSource struct {
	ID        string `json:"id"`
	Signature string `json:"signature"`
}
type toolOptions struct {
	Bitrate   int    `json:"bitrate"`
	Cover     bool   `json:"cover"`
	Container string `json:"container"`
	Extension string `json:"extension"`
}
type toolJob struct {
	ID       string       `json:"id"`
	UserID   int64        `json:"-"`
	ItemID   string       `json:"item_id"`
	Kind     string       `json:"kind"`
	Revision int64        `json:"revision"`
	Source   []toolSource `json:"-"`
	Options  toolOptions  `json:"options"`
	Status   string       `json:"status"`
	Progress float64      `json:"progress"`
	Created  int64        `json:"created_at"`
	Updated  int64        `json:"updated_at"`
	Error    string       `json:"error"`
	SHA256   string       `json:"sha256"`
	Size     int64        `json:"size"`
	Duration float64      `json:"duration"`
}

const toolColumns = `id,user_id,item_id,kind,source_revision,source,options,status,progress,created_at,updated_at,error,output_sha256,output_size,output_duration`

var toolID = regexp.MustCompile(`^tool-[A-Za-z0-9]{26}$`)

func scanTool(row interface{ Scan(...any) error }) (toolJob, error) {
	var j toolJob
	var source, options string
	err := row.Scan(&j.ID, &j.UserID, &j.ItemID, &j.Kind, &j.Revision, &source, &options, &j.Status, &j.Progress, &j.Created, &j.Updated, &j.Error, &j.SHA256, &j.Size, &j.Duration)
	if errors.Is(err, sql.ErrNoRows) {
		err = library.ErrNotFound
	}
	if err != nil {
		return j, err
	}
	if err = json.Unmarshal([]byte(source), &j.Source); err != nil {
		return j, err
	}
	err = json.Unmarshal([]byte(options), &j.Options)
	return j, err
}
func (s *Server) toolJob(ctx context.Context, uid int64, id string) (toolJob, error) {
	if !toolID.MatchString(id) {
		return toolJob{}, library.ErrNotFound
	}
	j, err := scanTool(s.store.DB.QueryRowContext(ctx, `SELECT `+toolColumns+` FROM catalog_tool_jobs WHERE id=? AND user_id=?`, id, uid))
	if err != nil {
		return j, err
	}
	_, err = s.catalog().CanUseTools(ctx, uid, j.ItemID)
	return j, err
}
func (s *Server) toolOutput(j toolJob) (string, error) {
	if !toolID.MatchString(j.ID) || (j.Options.Extension != ".m4b" && j.Options.Extension != ".mp3" && j.Options.Extension != ".flac") {
		return "", library.ErrInvalid
	}
	return filepath.Join(s.cfg.DataDir, "native-tools", j.ID, "output"+j.Options.Extension), nil
}
func (s *Server) captureToolSource(ctx context.Context, uid int64, id, kind string) (library.Item, []toolSource, toolOptions, error) {
	c := s.catalog()
	it, err := c.CanUseTools(ctx, uid, id)
	if err != nil {
		return it, nil, toolOptions{}, err
	}
	tracks, _, _, err := c.Timeline(ctx, uid, id, "")
	if err != nil {
		return it, nil, toolOptions{}, err
	}
	options := toolOptions{Bitrate: 128, Cover: true, Container: "mp4", Extension: ".m4b"}
	if kind != "m4b" && kind != "embed" || len(tracks) == 0 || len(tracks) > 256 {
		return it, nil, options, library.ErrInvalid
	}
	if kind == "embed" {
		if len(tracks) != 1 {
			return it, nil, options, fmt.Errorf("%w: embed metadata into a single file, or create an M4B from all tracks", library.ErrInvalid)
		}
		switch tracks[0].MIME {
		case "audio/mpeg":
			options.Container, options.Extension = "mp3", ".mp3"
		case "audio/mp4", "audio/x-m4b", "audio/m4b":
		case "audio/flac", "audio/x-flac":
			options.Container, options.Extension = "flac", ".flac"
		default:
			return it, nil, options, library.ErrInvalid
		}
	}
	source := []toolSource{}
	for _, tr := range tracks {
		f, _, e := c.OpenFile(ctx, uid, id, tr.ID, true)
		if e != nil {
			return it, nil, options, e
		}
		info, e := f.Stat()
		f.Close()
		if e != nil {
			return it, nil, options, e
		}
		source = append(source, toolSource{tr.ID, library.FileSignature(info)})
	}
	return it, source, options, nil
}
func (s *Server) nativeTools(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native media tools are not active")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	item := chi.URLParam(r, "id")
	id := chi.URLParam(r, "job")
	action := chi.URLParam(r, "action")
	if item != "" {
		if _, err := s.catalog().CanUseTools(ctx, uid, item); err != nil {
			s.nativeError(w, err)
			return
		}
		if r.Method == "POST" {
			var in struct {
				Kind     string `json:"kind"`
				Bitrate  int    `json:"bitrate"`
				Cover    *bool  `json:"cover"`
				Revision int64  `json:"revision"`
			}
			if decode(r, &in) != nil {
				writeErr(w, 400, "invalid media job")
				return
			}
			it, source, options, err := s.captureToolSource(ctx, uid, item, in.Kind)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			if in.Revision != it.UpdatedAt {
				s.nativeError(w, library.ErrConflict)
				return
			}
			if in.Bitrate != 0 {
				options.Bitrate = in.Bitrate
			}
			if options.Bitrate < 32 || options.Bitrate > 320 {
				writeErr(w, 400, "choose 32 to 320 kbps")
				return
			}
			if in.Cover != nil {
				options.Cover = *in.Cover
			}
			id = "tool-" + rand.Text()
			now := time.Now().UnixMilli()
			_, err = s.store.DB.ExecContext(ctx, `INSERT INTO catalog_tool_jobs(id,user_id,item_id,kind,source_revision,source,options,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'queued',?,?)`, id, uid, item, in.Kind, it.UpdatedAt, string(nativeJSON(source)), string(nativeJSON(options)), now, now)
			if err != nil {
				writeErr(w, 409, "a media job is already active for this book")
				return
			}
			writeJSON(w, 202, nativeObject{"id": id})
			return
		}
		rows, err := s.store.DB.QueryContext(ctx, `SELECT `+toolColumns+` FROM catalog_tool_jobs WHERE item_id=? AND user_id=? ORDER BY created_at DESC LIMIT 50`, item, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		jobs := []toolJob{}
		for rows.Next() {
			j, e := scanTool(rows)
			if e != nil {
				rows.Close()
				s.nativeError(w, e)
				return
			}
			jobs = append(jobs, j)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			s.nativeError(w, err)
			return
		}
		it, err := s.catalog().Item(ctx, uid, item)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"revision": it.UpdatedAt, "jobs": jobs})
		return
	}
	j, err := s.toolJob(ctx, uid, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if r.Method == "DELETE" {
		if j.Status == "applying" {
			if err = s.catalog().CancelGenerated(ctx, uid, id); err != nil {
				s.nativeError(w, err)
				return
			}
			writeJSON(w, 200, nativeObject{"ok": true})
			return
		}
		if j.Status != "queued" && j.Status != "running" {
			output, e := s.toolOutput(j)
			if e != nil {
				s.nativeError(w, e)
				return
			}
			// Applied output is already copied into the library; this is only its cache.
			if e = os.RemoveAll(filepath.Dir(output)); e != nil {
				s.nativeError(w, e)
				return
			}
			if _, e = s.store.DB.ExecContext(ctx, `DELETE FROM catalog_tool_jobs WHERE id=? AND status NOT IN ('queued','running','applying')`, id); e != nil {
				s.nativeError(w, e)
				return
			}
			writeJSON(w, 200, nativeObject{"ok": true})
			return
		}
		_, err = s.store.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET status='cancelled',updated_at=? WHERE id=? AND status IN ('queued','running')`, time.Now().UnixMilli(), id)
		s.nativeToolsMu.Lock()
		if cancel := s.nativeToolsCancel[id]; cancel != nil {
			cancel()
		}
		s.nativeToolsMu.Unlock()
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, nativeObject{"ok": true})
		return
	}
	if action == "retry" && r.Method == "POST" {
		if j.Status != "failed" && j.Status != "cancelled" && j.Status != "complete" {
			s.nativeError(w, library.ErrConflict)
			return
		}
		var pending string
		if e := s.store.DB.QueryRowContext(ctx, `SELECT apply_path FROM catalog_tool_jobs WHERE id=?`, id).Scan(&pending); e != nil || pending != "" {
			s.nativeError(w, library.ErrConflict)
			return
		}
		it, source, options, e := s.captureToolSource(ctx, uid, j.ItemID, j.Kind)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		options.Bitrate, options.Cover = j.Options.Bitrate, j.Options.Cover
		_, err = s.store.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET status='queued',source_revision=?,source=?,options=?,progress=0,error='',output_sha256='',output_size=0,output_duration=0,updated_at=? WHERE id=? AND status IN ('failed','cancelled','complete')`, it.UpdatedAt, string(nativeJSON(source)), string(nativeJSON(options)), time.Now().UnixMilli(), id)
		if err != nil {
			writeErr(w, 409, "a media job is already active for this book")
			return
		}
		writeJSON(w, 200, nativeObject{"ok": true})
		return
	}
	if action == "apply" && r.Method == "POST" {
		output, e := s.toolOutput(j)
		if e == nil {
			e = s.catalog().ApplyGenerated(ctx, uid, id, output)
		}
		if e != nil {
			s.nativeError(w, e)
			return
		}
		writeJSON(w, 200, nativeObject{"ok": true})
		return
	}
	if action == "download" && (r.Method == "GET" || r.Method == "HEAD") {
		if j.Status != "complete" && j.Status != "applied" {
			s.nativeError(w, library.ErrConflict)
			return
		}
		name, e := s.toolOutput(j)
		if e != nil {
			s.nativeError(w, e)
			return
		}
		f, e := os.Open(name)
		if e != nil {
			writeErr(w, 410, "generated output is unavailable; rebuild this job")
			return
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Size() != j.Size {
			writeErr(w, 410, "generated output changed; rebuild this job")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="audiobook`+j.Options.Extension+`"`)
		w.Header().Set("Cache-Control", "private, no-store")
		http.ServeContent(w, r, "audiobook"+j.Options.Extension, info.ModTime(), f)
		return
	}
	if action == "" && r.Method == "GET" {
		writeJSON(w, 200, j)
		return
	}
	writeErr(w, 404, "media operation not found")
}
