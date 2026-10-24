// Package media runs local FFmpeg tools against already-authorized regular file
// descriptors. It never follows a caller-supplied media URL or shell command.
package media

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Chapter struct {
	Title      string
	Start, End float64
}
type Options struct {
	FFmpeg, Output, Kind, Container string
	Bitrate                         int
	Inputs                          []*os.File
	Tags                            map[string]string
	Chapters                        []Chapter
	Cover                           *os.File // validated JPEG; optional attached picture
	Duration                        float64
	Progress                        func(float64)
}
type diagnostics struct{ bytes.Buffer }

func (d *diagnostics) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 32768 - d.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		d.Buffer.Write(p)
	}
	return n, nil
}
func escape(s string) string {
	return strings.NewReplacer("\\", "\\\\", "=", "\\=", ";", "\\;", "#", "\\#", "\r", "", "\n", "\\\n", "\x00", "").Replace(s)
}
func metadata(opt Options) string {
	var out strings.Builder
	out.WriteString(";FFMETADATA1\n")
	keys := []string{}
	for k := range opt.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&out, "%s=%s\n", escape(k), escape(opt.Tags[k]))
	}
	for _, c := range opt.Chapters {
		fmt.Fprintf(&out, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n", int64(math.Round(c.Start*1000)), int64(math.Round(c.End*1000)), escape(c.Title))
	}
	return out.String()
}
func Render(ctx context.Context, opt Options) error {
	if len(opt.Inputs) == 0 || len(opt.Inputs) > 256 || opt.Duration <= 0 || math.IsNaN(opt.Duration) || math.IsInf(opt.Duration, 0) {
		return errors.New("select 1 to 256 valid audio tracks")
	}
	if opt.Kind != "m4b" && opt.Kind != "embed" {
		return errors.New("unknown media operation")
	}
	if opt.Kind == "embed" && (len(opt.Inputs) != 1 || (opt.Container != "mp3" && opt.Container != "mp4" && opt.Container != "flac")) {
		return errors.New("metadata embedding needs one MP3, M4A/M4B or FLAC file")
	}
	if opt.Bitrate < 32 || opt.Bitrate > 320 {
		return errors.New("choose 32 to 320 kbps")
	}
	for _, c := range opt.Chapters {
		if math.IsNaN(c.Start) || math.IsNaN(c.End) || math.IsInf(c.End, 0) || c.Start < 0 || c.End <= c.Start || c.End > opt.Duration+1 {
			return errors.New("invalid chapter timings")
		}
	}
	// Publish only a complete file, without replacing a source or another job's output.
	if _, err := os.Lstat(opt.Output); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(opt.Output), ".render-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	output := filepath.Join(stage, "output")
	meta, err := os.CreateTemp("", "pocket-ffmetadata-")
	if err != nil {
		return err
	}
	defer os.Remove(meta.Name())
	defer meta.Close()
	if _, err = io.WriteString(meta, metadata(opt)); err != nil {
		return err
	}
	if _, err = meta.Seek(0, 0); err != nil {
		return err
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-n", "-threads", "2", "-filter_complex_threads", "1"}
	inherited := append([]*os.File{}, opt.Inputs...)
	for i, f := range opt.Inputs {
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() {
			return errors.New("input is not regular audio")
		}
		args = append(args, "-protocol_whitelist", "file,pipe", "-format_whitelist", "mp3,mov,flac,ogg,wav,matroska,webm,aac,asf,aiff,mpeg,mpegts", "-i", fmt.Sprintf("/proc/self/fd/%d", 3+i))
	}
	metadataIndex := len(opt.Inputs)
	inherited = append(inherited, meta)
	args = append(args, "-protocol_whitelist", "file,pipe", "-f", "ffmetadata", "-i", fmt.Sprintf("/proc/self/fd/%d", 3+metadataIndex))
	if opt.Cover != nil {
		inherited = append(inherited, opt.Cover)
		args = append(args, "-protocol_whitelist", "file,pipe", "-f", "image2pipe", "-c:v", "mjpeg", "-i", fmt.Sprintf("/proc/self/fd/%d", 3+metadataIndex+1))
	}
	if opt.Kind == "m4b" {
		var filters, streams strings.Builder
		for i := range opt.Inputs {
			fmt.Fprintf(&filters, "[%d:a:0]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo[a%d];", i, i)
			fmt.Fprintf(&streams, "[a%d]", i)
		}
		fmt.Fprintf(&filters, "%sconcat=n=%d:v=0:a=1[audio]", streams.String(), len(opt.Inputs))
		args = append(args, "-filter_complex", filters.String(), "-map", "[audio]", "-c:a", "aac", "-b:a", strconv.Itoa(opt.Bitrate)+"k")
		opt.Container = "mp4"
	} else {
		args = append(args, "-map", "0:a:0", "-c:a", "copy")
	}
	args = append(args, "-map_metadata", strconv.Itoa(metadataIndex), "-map_chapters", strconv.Itoa(metadataIndex))
	if opt.Cover != nil {
		args = append(args, "-map", fmt.Sprintf("%d:v:0", metadataIndex+1), "-c:v", "copy", "-disposition:v", "attached_pic")
	} else {
		args = append(args, "-vn")
	}
	if opt.Container == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	if opt.Container == "mp3" {
		args = append(args, "-id3v2_version", "3")
	}
	args = append(args, "-threads", "2", "-progress", "pipe:1", "-f", opt.Container, output)
	cmd := exec.CommandContext(ctx, opt.FFmpeg, args...)
	cmd.ExtraFiles = inherited
	cmd.WaitDelay = 5 * time.Second
	var diagnostic diagnostics
	cmd.Stderr = &diagnostic
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(pipe)
	last := time.Time{}
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok && key == "out_time_us" && time.Since(last) > time.Second {
			us, _ := strconv.ParseFloat(value, 64)
			if opt.Progress != nil {
				opt.Progress(math.Min(.99, math.Max(0, us/(opt.Duration*1e6))))
			}
			last = time.Now()
		}
	}
	scanErr := scanner.Err()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanErr != nil {
		return scanErr
	}
	if err != nil {
		return fmt.Errorf("media conversion: %w: %s", err, strings.TrimSpace(diagnostic.String()))
	}
	f, err := os.Open(output)
	if err != nil {
		return err
	}
	err = f.Chmod(0600)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return err
	}
	if err = os.Link(output, opt.Output); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(opt.Output))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
