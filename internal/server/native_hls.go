package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

// Converted playback is one continuous FFmpeg HLS muxer per stream (a playback
// session, or one public-share listener and file), like Audiobookshelf and
// Jellyfin: segment boundaries fall inside one encoder run, so there is no AAC
// priming or seek rounding between segments. Encoders start where the player
// asks, run a few minutes ahead of it and then pause, restart on far seeks,
// and stop after a couple of idle minutes. Segments live in DATA_DIR/cache/hls
// (never /tmp, a tmpfs on small hosts) under a size cap; the directory is
// wiped at start because no encoder of a previous run survives it.
const (
	nativeSegmentSeconds = 6.0
	hlsAheadPause        = 50 // segments produced beyond the newest request before the encoder pauses
	hlsAheadResume       = 30 // ...and resumes
	hlsKeepBehind        = 60 // segments kept behind the newest request (rewinds without re-encoding)
	hlsFarSeek           = 20 // a request this far beyond the encoder restarts it at that segment
	hlsTail              = 0.1
)

var errHLSBusy = errors.New("conversion busy; retry shortly")

// Timings (atomic so tests may shorten them while encoders run).
var hlsIdle, hlsWait, hlsSlotWait, hlsEvictAge durationVar

func init() {
	hlsIdle.Set(2 * time.Minute)
	hlsWait.Set(30 * time.Second)
	hlsSlotWait.Set(15 * time.Second)
	hlsEvictAge.Set(15 * time.Second) // encoders of streams quiet this long yield their slot
}

type durationVar struct{ v atomic.Int64 }

func (d *durationVar) Get() time.Duration  { return time.Duration(d.v.Load()) }
func (d *durationVar) Set(v time.Duration) { d.v.Store(int64(v)) }

const (
	hlsPoolUser = iota
	hlsPoolShare
)

type hlsManager struct {
	once    sync.Once
	mu      sync.Mutex
	root    string
	streams map[string]*hlsStream
	running [2]int
	limit   [2]int
	cap     int64
}

type hlsStream struct {
	key, dir string
	pool     int
	uid      int64
	item     string
	sig      string
	tracks   []library.Track
	last     atomic.Int64 // unix nanos of the newest request
	want     atomic.Int64 // track<<32 | segment of the newest request
	gone     atomic.Bool
	mu       sync.Mutex // serializes encoder changes
	enc      atomic.Pointer[hlsEncoder]
}

type hlsEncoder struct {
	track, start int
	cmd          *exec.Cmd
	done         chan struct{}
	err          error // valid once done is closed
	next         atomic.Int64
	killed       atomic.Bool
	stderr       bytes.Buffer
}

func (e *hlsEncoder) running() bool {
	select {
	case <-e.done:
		return false
	default:
		return true
	}
}
func (e *hlsEncoder) kill() {
	e.killed.Store(true)
	if e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
}
func (e *hlsEncoder) stop() {
	e.kill()
	<-e.done
}

// A tail shorter than hlsTail joins the previous segment: FFmpeg cuts on whole
// AAC frames and may not open a segment for a few milliseconds of audio.
func hlsSegments(duration float64) int {
	return max(1, int(math.Ceil((duration-hlsTail)/nativeSegmentSeconds)))
}
func hlsSegmentDuration(duration float64, n int) float64 {
	if n == hlsSegments(duration)-1 {
		return math.Max(0.01, duration-float64(n)*nativeSegmentSeconds)
	}
	return nativeSegmentSeconds
}
func (st *hlsStream) segment(track, n int) string {
	return filepath.Join(st.dir, fmt.Sprintf("t%d-%d.ts", track, n))
}
func (st *hlsStream) touch(track, n int) {
	st.last.Store(time.Now().UnixNano())
	st.want.Store(int64(track)<<32 | int64(n))
}
func (st *hlsStream) wanted() (int, int) {
	v := st.want.Load()
	return int(v >> 32), int(v & 0xffffffff)
}
func (st *hlsStream) idle() time.Duration { return time.Since(time.Unix(0, st.last.Load())) }

func (s *Server) hlsManager() *hlsManager {
	m := &s.hls
	m.once.Do(func() {
		m.root = filepath.Join(s.cfg.DataDir, "cache", "hls")
		m.streams = map[string]*hlsStream{}
		m.limit = [2]int{s.cfg.HLSEncoders, s.cfg.HLSShareEncoders}
		if m.limit[hlsPoolUser] <= 0 {
			m.limit[hlsPoolUser] = 4
		}
		if m.limit[hlsPoolShare] <= 0 {
			m.limit[hlsPoolShare] = 2
		}
		m.cap = s.cfg.HLSCacheBytes
		if m.cap <= 0 {
			m.cap = 1 << 30
		}
		if err := os.RemoveAll(m.root); err != nil {
			s.log.Warn("hls cache cleanup", "error", err)
		}
		_ = os.MkdirAll(m.root, 0o700)
		go s.hlsReaper()
	})
	return m
}

func (s *Server) hlsReaper() {
	m := &s.hls
	for {
		time.Sleep(min(20*time.Second, hlsIdle.Get()/2))
		m.mu.Lock()
		stale := []*hlsStream{}
		for k, st := range m.streams {
			if st.idle() > hlsIdle.Get() {
				delete(m.streams, k)
				stale = append(stale, st)
			}
		}
		m.mu.Unlock()
		for _, st := range stale {
			hlsDrop(st)
		}
	}
}

// hlsDrop ends a stream that left the manager: its encoder and its segments.
// It takes no stream lock, so any caller may use it.
func hlsDrop(st *hlsStream) {
	st.gone.Store(true)
	for {
		e := st.enc.Load()
		if e != nil {
			e.stop()
		}
		if st.enc.Load() == e {
			break
		}
	}
	_ = os.RemoveAll(st.dir)
}

// hlsCloseSession drops a playback session's stream when its own player closes
// the session. A session ended by another device keeps streaming (nativeHLS).
func (s *Server) hlsCloseSession(sessionID string) {
	m := s.hlsManager()
	m.mu.Lock()
	st := m.streams["s:"+sessionID]
	delete(m.streams, "s:"+sessionID)
	m.mu.Unlock()
	if st != nil {
		go hlsDrop(st)
	}
}

func hlsSignature(tracks []library.Track) string {
	var b strings.Builder
	for _, t := range tracks {
		fmt.Fprintf(&b, "%s:%.3f;", t.ID, t.Duration)
	}
	return b.String()
}

func (s *Server) hlsStream(key string, pool int, uid int64, item string, tracks []library.Track) *hlsStream {
	m := s.hlsManager()
	sig := hlsSignature(tracks)
	m.mu.Lock()
	st := m.streams[key]
	var old *hlsStream
	if st != nil && (st.sig != sig || st.uid != uid || st.item != item || st.gone.Load()) {
		old, st = st, nil
	}
	if st == nil {
		sum := sha256.Sum256([]byte(key + "\x00" + strconv.FormatInt(time.Now().UnixNano(), 10)))
		st = &hlsStream{key: key, pool: pool, uid: uid, item: item, sig: sig, tracks: tracks, dir: filepath.Join(m.root, hex.EncodeToString(sum[:12]))}
		st.last.Store(time.Now().UnixNano())
		m.streams[key] = st
	}
	m.mu.Unlock()
	if old != nil {
		go hlsDrop(old)
	}
	return st
}

// hlsAcquire takes an encoder slot of the stream's pool, asking encoders of
// streams that went quiet to give theirs up. Public shares have their own,
// smaller pool, so anonymous listeners can never starve the owner's players.
func (s *Server) hlsAcquire(ctx context.Context, st *hlsStream) error {
	m := s.hlsManager()
	deadline := time.Now().Add(hlsSlotWait.Get())
	for {
		m.mu.Lock()
		if m.running[st.pool] < m.limit[st.pool] {
			m.running[st.pool]++
			m.mu.Unlock()
			return nil
		}
		var victim *hlsEncoder
		var age time.Duration
		for _, other := range m.streams {
			if other == st || other.pool != st.pool {
				continue
			}
			if e := other.enc.Load(); e != nil && e.running() && !e.killed.Load() && other.idle() > hlsEvictAge.Get() && other.idle() > age {
				victim, age = e, other.idle()
			}
		}
		m.mu.Unlock()
		if victim != nil {
			victim.kill()
		}
		if time.Now().After(deadline) {
			return errHLSBusy
		}
		select {
		case <-ctx.Done():
			return errHLSBusy
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func (s *Server) hlsRelease(pool int) {
	m := &s.hls
	m.mu.Lock()
	m.running[pool]--
	m.mu.Unlock()
}

func hlsDirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// hlsMakeRoom keeps the cache under its cap by dropping the least recently used
// other streams, then this stream's own segments.
func (s *Server) hlsMakeRoom(st *hlsStream) error {
	m := s.hlsManager()
	for hlsDirSize(m.root) > m.cap {
		m.mu.Lock()
		var victim *hlsStream
		for _, other := range m.streams {
			if other != st && (victim == nil || other.idle() > victim.idle()) {
				victim = other
			}
		}
		if victim != nil {
			delete(m.streams, victim.key)
		}
		m.mu.Unlock()
		if victim == nil {
			break
		}
		hlsDrop(victim)
	}
	if hlsDirSize(m.root) <= m.cap {
		return nil
	}
	entries, _ := os.ReadDir(st.dir)
	for _, e := range entries {
		_ = os.Remove(filepath.Join(st.dir, e.Name()))
	}
	if hlsDirSize(m.root) > m.cap {
		return errHLSBusy
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hlsEnsure returns the encoder that will produce segment n of track ti,
// starting (or restarting) one when none runs near it. Callers hold st.mu.
func (s *Server) hlsEnsure(ctx context.Context, st *hlsStream, ti, n int) (*hlsEncoder, error) {
	if st.gone.Load() {
		return nil, errHLSBusy
	}
	if e := st.enc.Load(); e != nil && e.track == ti && n >= e.start {
		next := e.next.Load()
		ahead := int64(n) >= next || fileExists(st.segment(ti, n))
		if e.running() && !e.killed.Load() && ahead && int64(n) <= next+hlsFarSeek {
			return e, nil
		}
		// A finished run covered everything from its start: a segment past what
		// it made lies past the end of the audio.
		if !e.running() && e.err == nil && !e.killed.Load() && ahead {
			return e, nil
		}
	}
	if e := st.enc.Load(); e != nil && e.running() {
		e.stop()
	}
	if err := s.hlsMakeRoom(st); err != nil {
		return nil, err
	}
	if err := s.hlsAcquire(ctx, st); err != nil {
		return nil, err
	}
	e, err := s.hlsStart(st, ti, n)
	if err != nil {
		s.hlsRelease(st.pool)
		return nil, err
	}
	return e, nil
}

func (s *Server) hlsStart(st *hlsStream, ti, n int) (*hlsEncoder, error) {
	if err := os.MkdirAll(st.dir, 0o700); err != nil {
		return nil, err
	}
	// Segments of files other than this one and the listener's are not needed
	// (the listener may still be finishing the file before this one).
	wt, _ := st.wanted()
	entries, _ := os.ReadDir(st.dir)
	keep := []string{fmt.Sprintf("t%d-", ti), fmt.Sprintf("t%d-", wt)}
	for _, e := range entries {
		name := e.Name()
		if (!strings.HasPrefix(name, keep[0]) && !strings.HasPrefix(name, keep[1])) || strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(st.dir, name))
		}
	}
	tr := st.tracks[ti]
	// Ownership is checked again for every encoder: the stream's owner must
	// still be able to read this item and file. Only the descriptor reaches FFmpeg.
	file, _, err := s.catalog().OpenFile(context.Background(), st.uid, st.item, tr.ID, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	at := float64(n) * nativeSegmentSeconds
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-threads", "1", "-filter_threads", "1",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", "mp3,mov,flac,ogg,wav,matroska,webm,aac,asf,aiff,mpeg,mpegts"}
	if n > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.6f", at))
	}
	args = append(args, "-i", "/proc/self/fd/3", "-map", "0:a:0", "-vn", "-sn", "-dn", "-c:a", "aac", "-b:a", "96k", "-ac", "2", "-ar", "48000")
	if n > 0 {
		args = append(args, "-output_ts_offset", fmt.Sprintf("%.6f", at))
	}
	// temp_file renames each segment into place only once it is complete.
	pattern := strings.ReplaceAll(st.dir, "%", "%%") + string(filepath.Separator) + fmt.Sprintf("t%d-%%d.ts", ti)
	args = append(args, "-f", "hls", "-hls_time", strconv.Itoa(int(nativeSegmentSeconds)), "-hls_list_size", "0", "-hls_flags", "temp_file",
		"-hls_segment_options", "mpegts_copyts=1", "-start_number", strconv.Itoa(n), "-hls_segment_filename", pattern,
		filepath.Join(st.dir, fmt.Sprintf("t%d.m3u8", ti)))
	e := &hlsEncoder{track: ti, start: n, done: make(chan struct{})}
	e.next.Store(int64(n))
	e.cmd = exec.Command(s.cfg.FFmpeg, args...)
	e.cmd.ExtraFiles = []*os.File{file}
	e.cmd.Stderr = &limitedBuffer{b: &e.stderr, n: 4096}
	hlsProcAttr(e.cmd)
	if err = e.cmd.Start(); err != nil {
		return nil, err
	}
	st.enc.Store(e)
	go func() {
		err := e.cmd.Wait()
		e.advance(st)
		e.err = err
		close(e.done)
		s.hlsRelease(st.pool)
		if e.killed.Load() {
			return
		}
		if err != nil {
			// Never expose paths or embedded source metadata to apps.
			s.log.Warn("native conversion failed", "item", st.item, "error", err, "detail", strings.TrimSpace(e.stderr.String()))
			return
		}
		// Carry on into the book's next file while the listener is here.
		if ti+1 < len(st.tracks) && st.idle() < 30*time.Second && !st.gone.Load() {
			st.mu.Lock()
			if st.enc.Load() == e && !st.gone.Load() {
				ctx, cancel := context.WithTimeout(context.Background(), hlsSlotWait.Get())
				_, _ = s.hlsEnsure(ctx, st, ti+1, 0)
				cancel()
			}
			st.mu.Unlock()
		}
	}()
	go e.monitor(st)
	return e, nil
}

// advance counts the complete segments on disk.
func (e *hlsEncoder) advance(st *hlsStream) {
	for fileExists(st.segment(e.track, int(e.next.Load()))) {
		e.next.Add(1)
	}
}

// monitor keeps the encoder a few minutes ahead of the listener and prunes
// segments far behind, so a twenty-hour file never sits on disk whole.
func (e *hlsEncoder) monitor(st *hlsStream) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	paused := false
	pruned := e.start
	for {
		select {
		case <-e.done:
			return
		case <-tick.C:
		}
		e.advance(st)
		wt, wn := st.wanted()
		ahead := int(e.next.Load()) - wn
		if wt < e.track {
			ahead = int(e.next.Load()) + hlsSegments(st.tracks[wt].Duration) - wn
		} else if wt > e.track {
			ahead = 0
		}
		if !paused && ahead > hlsAheadPause {
			hlsPause(e.cmd, true)
			paused = true
		} else if paused && ahead < hlsAheadResume {
			hlsPause(e.cmd, false)
			paused = false
		}
		if wt == e.track {
			for ; pruned < wn-hlsKeepBehind; pruned++ {
				_ = os.Remove(st.segment(e.track, pruned))
			}
		}
	}
}

type limitedBuffer struct {
	b *bytes.Buffer
	n int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

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
	s.nativeHLSTracks(w, r, uid, session.ItemID, tracks, name, "s:"+session.ID, hlsPoolUser)
}

// Also used by public shares after validating the exact item/file membership
// (pool hlsPoolShare). This does not create a listening session or touch the
// publisher's progress. key names the stream whose encoder serves the request.
func (s *Server) nativeHLSTracks(w http.ResponseWriter, r *http.Request, uid int64, itemID string, tracks []library.Track, name, key string, pool int) {
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
			for n := 0; n < hlsSegments(tr.Duration); n++ {
				fmt.Fprintf(&b, "#EXTINF:%.6f,\nsegment-%d-%d.ts%s\n", hlsSegmentDuration(tr.Duration, n), i, n, token)
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
	if e1 != nil || e2 != nil || ti < 0 || ti >= len(tracks) || n < 0 || n >= hlsSegments(tracks[ti].Duration) {
		writeErr(w, 404, "segment not found")
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == "HEAD" {
		w.WriteHeader(200)
		return
	}
	st := s.hlsStream(key, pool, uid, itemID, tracks)
	st.touch(ti, n)
	path := st.segment(ti, n)
	ctx, cancel := context.WithTimeout(r.Context(), hlsWait.Get())
	defer cancel()
	for attempt := 0; ; attempt++ {
		if f, err := os.Open(path); err == nil {
			defer f.Close()
			if info, err := f.Stat(); err == nil {
				w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
			}
			_, _ = io.Copy(w, f)
			return
		}
		st.mu.Lock()
		e, err := s.hlsEnsure(ctx, st, ti, n)
		st.mu.Unlock()
		if err != nil {
			if errors.Is(err, errHLSBusy) {
				w.Header().Set("Retry-After", "2")
				writeErr(w, 503, errHLSBusy.Error())
				return
			}
			s.nativeError(w, err)
			return
		}
		if s.hlsAwait(ctx, st, e, path) {
			continue
		}
		if ctx.Err() != nil {
			w.Header().Set("Retry-After", "2")
			writeErr(w, 503, "conversion is slow; retry shortly")
			return
		}
		// Another request moved the encoder elsewhere: ask again for this one.
		if e.killed.Load() && attempt < 3 {
			continue
		}
		if e.err != nil {
			writeErr(w, 502, "audio conversion failed")
			return
		}
		// The audio ended before the timeline said it would.
		s.hlsSilence(w, r, hlsSegmentDuration(tracks[ti].Duration, n), float64(n)*nativeSegmentSeconds)
		return
	}
}

// hlsAwait waits for a segment to appear while its encoder runs.
func (s *Server) hlsAwait(ctx context.Context, st *hlsStream, e *hlsEncoder, path string) bool {
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		if fileExists(path) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-e.done:
			return fileExists(path)
		case <-tick.C:
			st.last.Store(time.Now().UnixNano())
		}
	}
}

func (s *Server) hlsSilence(w http.ResponseWriter, r *http.Request, duration, at float64) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.FFmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo",
		"-t", fmt.Sprintf("%.6f", duration), "-c:a", "aac", "-b:a", "96k", "-output_ts_offset", fmt.Sprintf("%.6f", at), "-mpegts_copyts", "1", "-f", "mpegts", "pipe:1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
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
