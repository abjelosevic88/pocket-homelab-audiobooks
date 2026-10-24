package media

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func TestM4BCombinesMixedAudioAndEmbedsChaptersCoverAndTags(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := []*os.File{}
	for i, ext := range []string{"mp3", "flac"} {
		name := filepath.Join(dir, "part."+ext)
		rate := "44100"
		if i == 1 {
			rate = "48000"
		}
		cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate="+rate, "-t", "2", name)
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("fixture %v %s", e, b)
		}
		f, e := os.Open(name)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		files = append(files, f)
	}
	cover, err := os.Create(filepath.Join(dir, "cover.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer cover.Close()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{255, 100, 0, 255})
	if err = jpeg.Encode(cover, img, nil); err != nil {
		t.Fatal(err)
	}
	cover.Seek(0, 0)
	opt := Options{FFmpeg: ffmpeg, Output: filepath.Join(dir, "book.m4b"), Kind: "m4b", Bitrate: 64, Inputs: files, Tags: map[string]string{"title": "A title = # ; \\ special\nsecond line", "artist": "An Author"}, Chapters: []Chapter{{"One", 0, 2}, {"Two", 2, 4}}, Duration: 4, Cover: cover}
	if err = Render(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	result, err := os.Open(opt.Output)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	p, err := library.Probe(context.Background(), ffprobe, result)
	if err != nil {
		t.Fatal(err)
	}
	if p.Duration < 3.95 || p.Duration > 4.2 || p.Codec != "aac" || !p.Cover || len(p.Chapters) != 2 || p.Chapters[1].Title != "Two" || p.Tags["title"] != opt.Tags["title"] || p.Tags["artist"] != "An Author" {
		t.Fatalf("converted metadata %+v", p)
	}
	// A pre-existing output cannot be overwritten by a repeated render.
	if err = Render(context.Background(), opt); err == nil {
		t.Fatal("overwrote generated output")
	}
}
func TestMetadataEmbeddingKeepsEncodedAudioAndCancellation(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := filepath.Join(dir, "source.mp3")
	if b, e := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=220", "-t", "2", name).CombinedOutput(); e != nil {
		t.Fatal(e, string(b))
	}
	source, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	opt := Options{FFmpeg: ffmpeg, Output: filepath.Join(dir, "tagged.mp3"), Kind: "embed", Container: "mp3", Bitrate: 128, Duration: 2, Inputs: []*os.File{source}, Tags: map[string]string{"title": "Updated title", "artist": "Updated author"}, Chapters: []Chapter{{"Chapter", 0, 2}}}
	if err = Render(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	result, err := os.Open(opt.Output)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	p, err := library.Probe(context.Background(), ffprobe, result)
	if err != nil || p.Tags["title"] != "Updated title" || p.Codec != "mp3" {
		t.Fatal(p, err)
	}
	// Compare decoded samples, so tag/container changes cannot conceal audio changes.
	decoded := func(path string) string {
		t.Helper()
		b, e := exec.Command(ffmpeg, "-v", "error", "-i", path, "-map", "0:a:0", "-f", "hash", "-hash", "sha256", "-").Output()
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	if decoded(name) != decoded(opt.Output) {
		t.Fatal("metadata operation changed audio")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opt.Output = filepath.Join(dir, "cancelled.mp3")
	if err = Render(ctx, opt); err == nil {
		t.Fatal("cancelled work ran")
	}
	if _, err = os.Stat(opt.Output); !os.IsNotExist(err) {
		t.Fatal("cancelled work left output")
	}
}
