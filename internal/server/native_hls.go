package server

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

// Segments are generated on demand from an already-authorized, confined file
// descriptor. No persistent child process, public cache, or upstream server is
// needed. Seeking into a long book converts only the requested six seconds.
var nativeEncoders = make(chan struct{}, 2)

const nativeSegmentSeconds = 6.0

func (s *Server) nativeHLS(w http.ResponseWriter, r *http.Request, uid int64, sessionID, name string) {
	session, err := s.catalog().Session(r.Context(), uid, sessionID)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	// An ended session's stream keeps working while the timeline it advertised is
	// unchanged: another device opening the book must not cut this one's buffer.
	// Session already confines the stream to its owner and item.
	if session.Closed {
		it, err := s.catalog().Item(r.Context(), uid, session.ItemID)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		if it.UpdatedAt > session.StartedAt {
			writeErr(w, 409, "playback session ended; start a new session")
			return
		}
	}
	tracks, _, _, err := s.catalog().Timeline(r.Context(), uid, session.ItemID, session.EpisodeID)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	s.nativeHLSTracks(w, r, uid, session.ItemID, tracks, name)
}

// Also used by public shares after validating the exact item/file membership.
// This does not create a listening session or touch the publisher's progress.
func (s *Server) nativeHLSTracks(w http.ResponseWriter, r *http.Request, uid int64, itemID string, tracks []library.Track, name string) {
	if name == "index.m3u8" {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
		// Relative segment paths work behind both the web media route and clients'
		// optional /audiobookshelf prefix. Propagate only the caller's query token.
		token := ""
		if t := r.URL.Query().Get("token"); t != "" {
			q := r.URL.Query()
			q.Set("token", t)
			token = "?" + q.Encode()
		}
		for i, tr := range tracks {
			if i > 0 {
				b.WriteString("#EXT-X-DISCONTINUITY\n")
			}
			for n := 0; n < int(math.Ceil(tr.Duration/nativeSegmentSeconds)); n++ {
				duration := math.Min(nativeSegmentSeconds, tr.Duration-float64(n)*nativeSegmentSeconds)
				fmt.Fprintf(&b, "#EXTINF:%.6f,\nsegment-%d-%d.ts%s\n", duration, i, n, token)
			}
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "private, no-store")
		if r.Method != "HEAD" {
			_, _ = fmt.Fprint(w, b.String())
		}
		return
	}
	parts := strings.Split(strings.TrimSuffix(name, ".ts"), "-")
	if len(parts) != 3 || parts[0] != "segment" || !strings.HasSuffix(name, ".ts") {
		writeErr(w, 404, "segment not found")
		return
	}
	ti, e1 := strconv.Atoi(parts[1])
	n, e2 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || ti < 0 || ti >= len(tracks) || n < 0 || float64(n) >= math.Ceil(tracks[ti].Duration/nativeSegmentSeconds) {
		writeErr(w, 404, "segment not found")
		return
	}
	tr := tracks[ti]
	file, _, err := s.catalog().OpenFile(r.Context(), uid, itemID, tr.ID, false)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == "HEAD" {
		w.WriteHeader(200)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	select {
	case nativeEncoders <- struct{}{}:
		defer func() { <-nativeEncoders }()
	case <-ctx.Done():
		writeErr(w, 503, "conversion busy; retry shortly")
		return
	}
	at := float64(n) * nativeSegmentSeconds
	duration := math.Min(nativeSegmentSeconds, tr.Duration-at)
	cmd := exec.CommandContext(ctx, s.cfg.FFmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-threads", "1", "-filter_threads", "1", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mp3,mov,flac,ogg,wav,matroska,webm,aac,asf,aiff,mpeg,mpegts", "-ss", fmt.Sprintf("%.6f", at), "-i", "/proc/self/fd/3", "-t", fmt.Sprintf("%.6f", duration), "-map", "0:a:0", "-vn", "-sn", "-dn", "-c:a", "aac", "-b:a", "96k", "-ac", "2", "-ar", "48000", "-output_ts_offset", fmt.Sprintf("%.6f", at), "-mpegts_copyts", "1", "-f", "mpegts", "pipe:1")
	cmd.ExtraFiles = []*os.File{file}
	var out bytes.Buffer
	cmd.Stdout = &out
	// Do not expose paths or embedded source metadata in errors returned to apps.
	if err = cmd.Run(); err != nil {
		s.log.Warn("native conversion failed", "item", itemID, "error", err)
		writeErr(w, 502, "audio conversion failed")
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(out.Len()))
	_, _ = w.Write(out.Bytes())
}

func (s *Server) nativePlaybackWire(session library.Session, it nativeObject, transcode bool) nativeObject {
	out := nativeSessionWire(session, it)
	if transcode {
		out["playMethod"] = 2
		out["audioTracks"] = []any{nativeObject{"index": 1, "startOffset": 0, "duration": session.Duration, "title": nativeMetadata(it)["title"], "mimeType": "application/vnd.apple.mpegurl", "contentUrl": "/hls/" + session.ID + "/index.m3u8"}}
	}
	return out
}
