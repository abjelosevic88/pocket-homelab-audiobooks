package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	for i, ch := range raw.Chapters {
		start, e1 := strconv.ParseFloat(ch.Start, 64)
		end, e2 := strconv.ParseFloat(ch.End, 64)
		if e1 != nil || e2 != nil || !validSeconds(start) || !validSeconds(end) || end < start || end > out.Duration+1 {
			return ProbeResult{}, fmt.Errorf("invalid embedded chapter")
		}
		out.Chapters = append(out.Chapters, Chapter{ID: i, Start: start, End: end, Title: ch.Tags["title"]})
	}
	return out, nil
}
