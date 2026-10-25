package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Probe never gives ffprobe a user-controlled URL. Only a confined regular file
// descriptor is inherited, with network and playlist demuxers disabled.
type ProbeResult struct {
	Format   string
	Duration float64
	Tags     map[string]string
	Chapters []Chapter
	Codec    string
	Cover    bool
}
type boundedProbeOutput struct{ bytes.Buffer }

func (b *boundedProbeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, fmt.Errorf("media metadata exceeds limit")
	}
	return b.Buffer.Write(p)
}
func Probe(ctx context.Context, binary string, file *os.File) (ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-v", "error", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mp3,mov,flac,ogg,wav,matroska,webm,aac,asf,aiff,mpeg,mpegts", "-show_format", "-show_streams", "-show_chapters", "-of", "json", "/proc/self/fd/3")
	cmd.ExtraFiles = []*os.File{file}
	var data boundedProbeOutput
	cmd.Stdout = &data
	var diagnostics boundedProbeOutput
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(diagnostics.String())
		if len(detail) > 1024 {
			detail = detail[:1024]
		}
		return ProbeResult{}, fmt.Errorf("probe audio: %w (%s)", err, detail)
	}
	var raw struct {
		Format struct {
			Name     string            `json:"format_name"`
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Type        string            `json:"codec_type"`
			Codec       string            `json:"codec_name"`
			Duration    string            `json:"duration"`
			Tags        map[string]string `json:"tags"`
			Disposition struct {
				Attached int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
		Chapters []struct {
			Start string            `json:"start_time"`
			End   string            `json:"end_time"`
			Tags  map[string]string `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(data.Bytes(), &raw); err != nil {
		return ProbeResult{}, err
	}
	out := ProbeResult{Format: raw.Format.Name, Tags: map[string]string{}, Chapters: []Chapter{}}
	out.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	for _, st := range raw.Streams {
		if st.Disposition.Attached == 1 {
			out.Cover = true
		}
		if st.Type != "audio" {
			continue
		}
		if out.Codec == "" {
			out.Codec = st.Codec
		}
		if out.Duration <= 0 {
			out.Duration, _ = strconv.ParseFloat(st.Duration, 64)
		}
		for k, v := range st.Tags {
			out.Tags[strings.ToLower(k)] = v
		}
	}
	for k, v := range raw.Format.Tags {
		out.Tags[strings.ToLower(k)] = v
	}
	if out.Codec == "" || !validSeconds(out.Duration) || out.Duration <= 0 {
		return ProbeResult{}, ErrNoAudio
	}
	for _, ch := range raw.Chapters {
		start, e1 := strconv.ParseFloat(ch.Start, 64)
		end, e2 := strconv.ParseFloat(ch.End, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		out.Chapters = append(out.Chapters, Chapter{Start: start, End: end, Title: ch.Tags["title"]})
	}
	out.Chapters = clampChapters(out.Chapters, out.Duration)
	return out, nil
}

// clampChapters keeps a file playable when its embedded chapter table is a
// little longer than the audio (common after re-encoding): ends past the
// duration are clamped and chapters starting at or beyond it are dropped.
func clampChapters(in []Chapter, duration float64) []Chapter {
	out := []Chapter{}
	for _, ch := range in {
		if !validSeconds(ch.Start) || !validSeconds(ch.End) || ch.End < ch.Start || ch.Start >= duration {
			continue
		}
		ch.End = min(ch.End, duration)
		ch.ID = len(out)
		out = append(out, ch)
	}
	return out
}

// ExtractCover copies the first embedded picture out of an audio file. Like
// Probe it reads only an inherited descriptor with network protocols disabled.
func ExtractCover(ctx context.Context, binary string, file *os.File) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, codec := range []string{"copy", "png"} {
		cmd := exec.CommandContext(ctx, binary, "-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", "/proc/self/fd/3", "-map", "0:v:0", "-frames:v", "1", "-c:v", codec, "-f", "image2pipe", "pipe:1")
		cmd.ExtraFiles = []*os.File{file}
		var data boundedCoverOutput
		cmd.Stdout = &data
		if err := cmd.Run(); err != nil {
			return nil, "", fmt.Errorf("extract cover: %w", err)
		}
		if _, format, err := image.DecodeConfig(bytes.NewReader(data.Bytes())); err == nil && (format == "jpeg" || format == "png") {
			return data.Bytes(), format, nil
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("embedded cover is not a JPEG or PNG image")
}

type boundedCoverOutput struct{ bytes.Buffer }

func (b *boundedCoverOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16<<20 {
		return 0, fmt.Errorf("embedded cover exceeds limit")
	}
	return b.Buffer.Write(p)
}
