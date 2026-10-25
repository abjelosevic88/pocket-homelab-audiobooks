package server

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const toneHz = 1000.0

// hlsFixture makes both files of the harness book one generated tone of the
// given length (FLAC at 44.1 kHz, so conversion also resamples).
func hlsFixture(t *testing.T, s *Server, seconds float64) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	s.cfg.FFmpeg = ffmpeg
	var root string
	if err = s.store.DB.QueryRow(`SELECT local_path FROM catalog_roots WHERE id='root'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%g:sample_rate=44100", toneHz), "-t", fmt.Sprint(seconds), "-c:a", "flac", filepath.Join(root, "audio.flac"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture audio: %v %s", err, out)
	}
	if _, err = s.store.DB.Exec(`UPDATE catalog_files SET relative_path='audio.flac',name='audio.flac',mime='audio/flac',duration=? WHERE item_id='book'`, seconds); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.DB.Exec(`UPDATE catalog_items SET duration=? WHERE id='book'`, 2*seconds); err != nil {
		t.Fatal(err)
	}
	return ffmpeg
}

// decodeMono decodes MPEG-TS bytes to 48 kHz mono float samples.
func decodeMono(t *testing.T, ffmpeg string, ts []byte) []float32 {
	t.Helper()
	cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-i", "pipe:0", "-ac", "1", "-ar", "48000", "-f", "f32le", "pipe:1")
	cmd.Stdin = bytes.NewReader(ts)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("decode: %v %s", err, errb.String())
	}
	pcm := make([]float32, out.Len()/4)
	if err := binary.Read(&out, binary.LittleEndian, pcm); err != nil {
		t.Fatal(err)
	}
	return pcm
}

// toneBreaks measures how far a decoded tone strays from one continuous sine:
// the phase (radians) and relative amplitude of every 10 ms window, against the
// first. Missing, repeated or primed samples at a segment boundary move the
// phase (one sample is 0.13 rad at 1 kHz) or dip the amplitude.
func toneBreaks(pcm []float32) (phase, amp float64, at int) {
	const n, hop, skip = 480, 120, 4096 // 480 samples = exactly ten periods
	w := 2 * math.Pi * toneHz / 48000
	refPhase, refAmp := 0.0, 0.0
	for k := skip; k+n < len(pcm)-skip; k += hop {
		var si, co float64
		for i := k; i < k+n; i++ {
			si += float64(pcm[i]) * math.Sin(w*float64(i))
			co += float64(pcm[i]) * math.Cos(w*float64(i))
		}
		p, a := math.Atan2(co, si), 2*math.Hypot(si, co)/n
		if k == skip {
			refPhase, refAmp = p, a
			continue
		}
		d := math.Abs(math.Remainder(p-refPhase, 2*math.Pi))
		if d > phase {
			phase, at = d, k
		}
		amp = math.Max(amp, math.Abs(a/refAmp-1))
	}
	return phase, amp, at
}

func segmentBytes(t *testing.T, s *Server, tok, sid string, track, from, to int) []byte {
	t.Helper()
	var all bytes.Buffer
	for n := from; n < to; n++ {
		w := nativeCall(t, s.Router(), tok, "GET", fmt.Sprintf("/hls/%s/segment-%d-%d.ts", sid, track, n), "")
		if w.Code != 200 || w.Body.Len() < 188 || w.Body.Bytes()[0] != 0x47 {
			t.Fatalf("segment %d-%d: %d %q", track, n, w.Code, w.Body.String())
		}
		all.Write(w.Body.Bytes())
	}
	return all.Bytes()
}

func hlsSession(t *testing.T, s *Server, a person) string {
	t.Helper()
	info := jsonObject(t, call(t, s.Router(), a, "POST", "/api/v1/play/book", `{"client":"browser","mime_types":["audio/unsupported"],"force_transcode":true}`))
	if info["method"] != "hls" {
		t.Fatalf("no conversion: %v", info)
	}
	return nativeString(info["session_id"])
}

func firstPTS(t *testing.T, ffmpeg string, ts []byte) float64 {
	t.Helper()
	probe := filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
	cmd := exec.Command(probe, "-v", "error", "-show_entries", "packet=pts_time", "-of", "csv=p=0", "-i", "pipe:0")
	cmd.Stdin = bytes.NewReader(ts)
	out, err := cmd.Output()
	if err != nil {
		t.Skip("ffprobe unavailable next to ffmpeg")
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.SplitN(string(out), "\n", 2)[0], ",")), 64)
	return v
}

// Segments of one continuous encoder join without a click or gap: the decoded
// concatenation is one unbroken sine, from the start and after a seek.
func TestNativeHLSSegmentsAreGapless(t *testing.T) {
	s, _, a, _, tok := nativeHarness(t)
	ffmpeg := hlsFixture(t, s, 40)
	sid := hlsSession(t, s, a)
	segments := hlsSegments(40)
	ts := segmentBytes(t, s, tok, sid, 0, 0, segments)
	pcm := decodeMono(t, ffmpeg, ts)
	if got := float64(len(pcm)) / 48000; math.Abs(got-40) > 0.1 {
		t.Fatalf("decoded %.3fs of a 40s file", got)
	}
	phase, amp, at := toneBreaks(pcm)
	t.Logf("continuous: worst phase %.4f rad, amplitude %.2f%%", phase, amp*100)
	if phase > 0.05 || amp > 0.05 {
		t.Fatalf("discontinuity: phase %.3f rad, amplitude %.1f%% near %.3fs", phase, amp*100, float64(at)/48000)
	}
	// The second file of the book follows; its encoder starts at its beginning.
	if pts := firstPTS(t, ffmpeg, segmentBytes(t, s, tok, sid, 1, 0, 1)); pts > 0.05 {
		t.Fatalf("second file starts at %.3f", pts)
	}

	// A seek starts a fresh encoder at the asked segment with matching timestamps;
	// everything after it is again one continuous tone.
	sid = hlsSession(t, s, a)
	ts = segmentBytes(t, s, tok, sid, 0, 3, segments)
	if pts := firstPTS(t, ffmpeg, ts); math.Abs(pts-18) > 0.05 {
		t.Fatalf("seeked segment starts at %.3f, want 18", pts)
	}
	if phase, amp, at := toneBreaks(decodeMono(t, ffmpeg, ts)); phase > 0.05 || amp > 0.05 {
		t.Fatalf("discontinuity after seek: phase %.3f rad, amplitude %.1f%% near %.3fs", phase, amp*100, float64(at)/48000)
	}

	// The detector itself must catch the former per-segment encoding (seek
	// before -i and fresh AAC priming in every segment).
	var root string
	_ = s.store.DB.QueryRow(`SELECT local_path FROM catalog_roots WHERE id='root'`).Scan(&root)
	var old bytes.Buffer
	for n := 0; n < segments; n++ {
		at := float64(n) * nativeSegmentSeconds
		cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-ss", fmt.Sprintf("%.6f", at), "-i", filepath.Join(root, "audio.flac"), "-t", fmt.Sprintf("%.6f", math.Min(nativeSegmentSeconds, 40-at)), "-c:a", "aac", "-b:a", "96k", "-ac", "2", "-ar", "48000", "-output_ts_offset", fmt.Sprintf("%.6f", at), "-mpegts_copyts", "1", "-f", "mpegts", "pipe:1")
		cmd.Stdout = &old
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}
	phase, amp, at = toneBreaks(decodeMono(t, ffmpeg, old.Bytes()))
	t.Logf("per-segment encoding: worst phase %.4f rad, amplitude %.2f%% near %.3fs", phase, amp*100, float64(at)/48000)
	if phase < 0.05 && amp < 0.05 {
		t.Fatalf("detector missed per-segment encoding (phase %.3f, amplitude %.3f)", phase, amp)
	}
}

func hlsStreamOf(s *Server, sid string) *hlsStream {
	m := s.hlsManager()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streams["s:"+sid]
}

func hlsWaitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func processState(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// The encoder runs ahead of the listener only so far, restarts on far seeks,
// and leaves nothing behind once its session closes or goes quiet.
func TestNativeHLSEncoderLifecycle(t *testing.T) {
	s, h, a, _, tok := nativeHarness(t)
	// Leftovers of a previous run are removed when the cache opens.
	junk := filepath.Join(s.cfg.DataDir, "cache", "hls", "stale", "t0-1.ts")
	if err := os.MkdirAll(filepath.Dir(junk), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(junk, []byte("old"), 0o600)
	hlsFixture(t, s, 700)
	s.hlsManager()
	if _, err := os.Stat(junk); !os.IsNotExist(err) {
		t.Fatal("stale segments survived start")
	}
	sid := hlsSession(t, s, a)
	segmentBytes(t, s, tok, sid, 0, 0, 1)
	st := hlsStreamOf(s, sid)
	e := st.enc.Load()
	// Paused a bounded distance ahead instead of converting the whole file.
	hlsWaitFor(t, "encoder to pause ahead", func() bool { return e.next.Load() > hlsAheadPause })
	time.Sleep(600 * time.Millisecond)
	if n := e.next.Load(); n > hlsAheadPause+10 || !e.running() {
		t.Fatalf("encoder ran on to segment %d (running %v)", n, e.running())
	}
	if runtime.GOOS == "linux" {
		if state := processState(e.cmd.Process.Pid); state != "T" && state != "t" {
			t.Fatalf("encoder not paused: state %q", state)
		}
	}
	// A request within reach resumes the same encoder.
	segmentBytes(t, s, tok, sid, 0, 40, 41)
	if st.enc.Load() != e {
		t.Fatal("near request restarted the encoder")
	}
	// A far seek restarts it exactly there.
	segmentBytes(t, s, tok, sid, 0, 110, 111)
	moved := st.enc.Load()
	if moved == e || moved.start != 110 || e.running() {
		t.Fatalf("far seek: start %d, old running %v", moved.start, e.running())
	}
	// Closing the session from its own player ends the encoder and its files.
	jsonObject(t, call(t, h, a, "POST", "/api/v1/sessions/"+sid+"/sync", `{"report_id":"close-1","observed_at":`+strconv.FormatInt(time.Now().UnixMilli(), 10)+`,"current_time":660,"listened":1,"close":true}`))
	hlsWaitFor(t, "closed stream cleanup", func() bool {
		_, err := os.Stat(st.dir)
		return !moved.running() && os.IsNotExist(err)
	})

	// A stream nobody asks for any more is reaped.
	prev := hlsIdle.Get()
	hlsIdle.Set(300 * time.Millisecond)
	defer hlsIdle.Set(prev)
	go s.hlsReaper()
	sid = hlsSession(t, s, a)
	segmentBytes(t, s, tok, sid, 0, 0, 1)
	st = hlsStreamOf(s, sid)
	hlsWaitFor(t, "idle stream cleanup", func() bool {
		_, err := os.Stat(st.dir)
		return hlsStreamOf(s, sid) == nil && !st.enc.Load().running() && os.IsNotExist(err)
	})
}

// Public shares convert in their own pool, so they cannot take the owner's
// encoders; a full pool hands slots over from quiet streams or says busy.
func TestNativeHLSEncoderPools(t *testing.T) {
	s, h, a, _, tok := nativeHarness(t)
	s.cfg.HLSEncoders, s.cfg.HLSShareEncoders = 1, 1
	s.cfg.BaseURL = "https://audio.example.test"
	hlsFixture(t, s, 700)
	prevWait, prevAge := hlsSlotWait.Get(), hlsEvictAge.Get()
	defer func() { hlsSlotWait.Set(prevWait); hlsEvictAge.Set(prevAge) }()
	hlsSlotWait.Set(400 * time.Millisecond)
	hlsEvictAge.Set(time.Hour)

	first := hlsSession(t, s, a)
	segmentBytes(t, s, tok, first, 0, 0, 1)
	// The owner's single slot is busy; a share still converts.
	s.store.DB.Exec(`UPDATE users SET role='admin' WHERE id=?`, a.u.ID)
	v := jsonObject(t, call(t, h, a, "POST", "/api/v1/shares", `{"kind":"item","target_id":"book","name":"Shared"}`))
	if w := publicShareCall(h, "GET", "/share/"+nativeString(v["slug"])+"/hls/book/11/segment-0-2.ts", "", nil); w.Code != 200 || w.Body.Len() < 188 {
		t.Fatalf("share conversion while owner converts: %d %s", w.Code, w.Body.String())
	}
	// A second owner stream waits, then reports busy while the first is active.
	second := hlsSession(t, s, a)
	if w := nativeCall(t, h, tok, "GET", "/hls/"+second+"/segment-0-0.ts", ""); w.Code != 503 {
		t.Fatalf("full pool answered %d", w.Code)
	}
	// Once the first stream goes quiet its slot moves to the new listener.
	hlsEvictAge.Set(50 * time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	segmentBytes(t, s, tok, second, 0, 0, 1)
	if e := hlsStreamOf(s, first).enc.Load(); e.running() {
		t.Fatal("quiet stream kept its encoder")
	}
}
