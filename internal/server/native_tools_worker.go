package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/media"
)

func (s *Server) nativeToolsLoop(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.runToolJob(ctx)
		}
	}
}
func (s *Server) runToolJob(ctx context.Context) bool {
	j, err := scanTool(s.store.DB.QueryRowContext(ctx, `SELECT `+toolColumns+` FROM catalog_tool_jobs WHERE status='queued' ORDER BY created_at LIMIT 1`))
	if err != nil {
		return false
	}
	result, err := s.store.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET status='running',progress=0,updated_at=? WHERE id=? AND status='queued'`, time.Now().UnixMilli(), j.ID)
	if err != nil {
		return false
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return false
	}
	jobCtx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	s.nativeToolsMu.Lock()
	s.nativeToolsCancel[j.ID] = cancel
	s.nativeToolsMu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-tick.C:
				current, e := s.toolJob(jobCtx, j.UserID, j.ID)
				if e != nil || current.Status != "running" {
					cancel()
					return
				}
			}
		}
	}()
	err = s.renderTool(jobCtx, j)
	cancel()
	<-done
	s.nativeToolsMu.Lock()
	delete(s.nativeToolsCancel, j.ID)
	s.nativeToolsMu.Unlock()
	if err != nil {
		status := "failed"
		message := err.Error()
		if len(message) > 2000 {
			message = message[:2000]
		}
		if ctx.Err() != nil {
			status = "queued"
			message = "Interrupted; resuming after restart"
		} else if errors.Is(err, context.Canceled) {
			status = "cancelled"
			message = "Media job cancelled"
		}
		finish, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = s.store.DB.ExecContext(finish, `UPDATE catalog_tool_jobs SET status=?,error=?,updated_at=? WHERE id=? AND status='running'`, status, message, time.Now().UnixMilli(), j.ID)
	}
	return true
}
func (s *Server) renderTool(ctx context.Context, j toolJob) error {
	c := s.catalog()
	it, err := c.CanUseTools(ctx, j.UserID, j.ItemID)
	if err != nil {
		return err
	}
	if it.UpdatedAt != j.Revision {
		return fmt.Errorf("book changed; retry to use current metadata and tracks")
	}
	tracks, chapters, duration, err := c.Timeline(ctx, j.UserID, j.ItemID, "")
	if err != nil {
		return err
	}
	if len(tracks) != len(j.Source) {
		return library.ErrConflict
	}
	inputs := []*os.File{}
	defer func() {
		for _, f := range inputs {
			f.Close()
		}
	}()
	for i, tr := range tracks {
		if tr.ID != j.Source[i].ID {
			return library.ErrConflict
		}
		f, _, e := c.OpenFile(ctx, j.UserID, j.ItemID, tr.ID, true)
		if e != nil {
			return e
		}
		inputs = append(inputs, f)
		info, e := f.Stat()
		if e != nil {
			return e
		}
		if library.FileSignature(info) != j.Source[i].Signature {
			return fmt.Errorf("source audio changed; retry the job")
		}
	}
	stage, err := os.MkdirTemp(s.cfg.DataDir, ".media-job-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	doc := nativeMapJSON(it.Document)
	m := nativeMap(doc["media"])
	list := func(key string) string {
		out := []string{}
		for _, v := range nativeArray(m[key]) {
			out = append(out, nativeString(v))
		}
		return strings.Join(out, "; ")
	}
	tags := map[string]string{"title": it.Title, "album": it.Title, "artist": it.Author, "album_artist": it.Author, "composer": list("narrators"), "genre": list("genres"), "date": nativeString(m["publishedYear"]), "publisher": nativeString(m["publisher"]), "comment": nativeString(m["description"]), "language": nativeString(m["language"])}
	ch := []media.Chapter{}
	for _, v := range chapters {
		ch = append(ch, media.Chapter{Title: v.Title, Start: v.Start, End: v.End})
	}
	var cover *os.File
	if j.Options.Cover {
		files, e := c.Files(ctx, j.UserID, j.ItemID)
		if e != nil {
			return e
		}
		sort.SliceStable(files, func(i, j int) bool { return files[i].ID == "native-cover" && files[j].ID != "native-cover" })
		for _, v := range files {
			if v.Role != "cover" {
				continue
			}
			original, _, e := c.OpenFile(ctx, j.UserID, j.ItemID, v.ID, true)
			if e != nil {
				return e
			}
			b, e := io.ReadAll(io.LimitReader(original, (16<<20)+1))
			original.Close()
			if e != nil {
				return e
			}
			if len(b) > 16<<20 {
				return errors.New("cover is too large to embed")
			}
			cfg, _, e := image.DecodeConfig(bytes.NewReader(b))
			if e != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40000000 {
				return errors.New("use a JPEG, PNG or GIF cover under 40 megapixels")
			}
			decoded, _, e := image.Decode(bytes.NewReader(b))
			if e != nil {
				return e
			}
			cover, e = os.OpenFile(filepath.Join(stage, "cover.jpg"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if e != nil {
				return e
			}
			defer cover.Close()
			if e = jpeg.Encode(cover, decoded, &jpeg.Options{Quality: 90}); e != nil {
				return e
			}
			if _, e = cover.Seek(0, 0); e != nil {
				return e
			}
			break
		}
	}
	output := filepath.Join(stage, "output"+j.Options.Extension)
	err = media.Render(ctx, media.Options{FFmpeg: s.cfg.FFmpeg, Output: output, Kind: j.Kind, Container: j.Options.Container, Bitrate: j.Options.Bitrate, Inputs: inputs, Tags: tags, Chapters: ch, Cover: cover, Duration: duration, Progress: func(p float64) {
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET progress=?,updated_at=? WHERE id=? AND status='running'`, p, time.Now().UnixMilli(), j.ID)
	}})
	if err != nil {
		return err
	}
	f, err := os.Open(output)
	if err != nil {
		return err
	}
	defer f.Close()
	probed, err := library.Probe(ctx, s.cfg.FFprobe, f)
	if err != nil {
		return err
	}
	if math.Abs(probed.Duration-duration) > math.Max(2, .05*float64(len(inputs))) {
		return errors.New("converted duration differs from the original timeline; output was not published")
	}
	for i, input := range inputs {
		info, e := input.Stat()
		if e != nil {
			return e
		}
		if library.FileSignature(info) != j.Source[i].Signature {
			return errors.New("audio changed while converting; retry")
		}
	}
	current, err := c.CanUseTools(ctx, j.UserID, j.ItemID)
	if err != nil {
		return err
	}
	if current.UpdatedAt != j.Revision {
		return errors.New("book metadata changed while converting; retry")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	destination, err := s.toolOutput(j)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	if err = os.Rename(output, destination); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return err
	}
	_, err = s.store.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET status='complete',progress=1,output_sha256=?,output_size=?,output_duration=?,updated_at=?,error='' WHERE id=? AND status='running'`, hex.EncodeToString(hash.Sum(nil)), size, probed.Duration, time.Now().UnixMilli(), j.ID)
	return err
}

// Recover journals before scanners start. Missing disposable output after a
// restore becomes a rebuildable job; adopted media remains in the library.
func (s *Server) recoverNativeTools(ctx context.Context) error {
	if _, err := s.store.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET status='queued',progress=0,error='Interrupted; rebuilding output' WHERE status='running'`); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.cfg.DataDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".media-job-") {
			if err = os.RemoveAll(filepath.Join(s.cfg.DataDir, e.Name())); err != nil {
				return err
			}
		}
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT `+toolColumns+` FROM catalog_tool_jobs WHERE status IN ('complete','applying')`)
	if err != nil {
		return err
	}
	jobs := []toolJob{}
	for rows.Next() {
		j, e := scanTool(rows)
		if e != nil {
			rows.Close()
			return e
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		output, e := s.toolOutput(j)
		if e != nil {
			return e
		}
		if j.Status == "applying" {
			if e = s.catalog().ApplyGenerated(ctx, j.UserID, j.ID, output); e != nil {
				_, _ = s.store.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET error=? WHERE id=? AND status='applying'`, e.Error(), j.ID)
				s.log.Error("media adoption needs attention", "job", j.ID, "error", e)
			}
		} else if info, e := os.Stat(output); e != nil || !info.Mode().IsRegular() || info.Size() != j.Size {
			if _, err = s.store.DB.ExecContext(ctx, `UPDATE catalog_tool_jobs SET status='failed',error='Generated output is unavailable; rebuild this job' WHERE id=? AND status='complete'`, j.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
