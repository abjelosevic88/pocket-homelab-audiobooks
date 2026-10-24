package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func toolHarness(t *testing.T) (*Server, http.Handler, person, person, string) {
	t.Helper()
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("ffmpeg unavailable")
	}
	probe, e := exec.LookPath("ffprobe")
	if e != nil {
		t.Fatal(e)
	}
	s, h, a, b, tok := nativeHarness(t)
	s.cfg.FFmpeg = ffmpeg
	s.cfg.FFprobe = probe
	var root string
	s.store.DB.QueryRow(`SELECT local_path FROM catalog_roots WHERE id='root'`).Scan(&root)
	os.Remove(filepath.Join(root, "audio.mp3"))
	os.Mkdir(filepath.Join(root, "Book"), 0700)
	for i := 1; i <= 2; i++ {
		path := filepath.Join(root, "Book", fmt.Sprintf("%d.mp3", i))
		if out, e := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "2", path).CombinedOutput(); e != nil {
			t.Fatal(e, string(out))
		}
		f, e := os.Open(path)
		if e != nil {
			t.Fatal(e)
		}
		p, e := library.Probe(context.Background(), probe, f)
		info, _ := f.Stat()
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.store.DB.Exec(`UPDATE catalog_files SET relative_path=?,name=?,size=?,duration=? WHERE id=?`, filepath.Join("Book", fmt.Sprintf("%d.mp3", i)), filepath.Base(path), info.Size(), p.Duration, fmt.Sprint(10+i)); e != nil {
			t.Fatal(e)
		}
	}
	s.store.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true')) WHERE id='alice'`)
	s.store.DB.Exec(`UPDATE catalog_items SET duration=(SELECT sum(duration) FROM catalog_files WHERE item_id='book'),document=json_set(document,'$.item',json('{"path":"/media/Book","libraryFolderId":"folder"}'),'$.media.chapters',json('[{"id":0,"start":0,"end":4,"title":"Chapter one"}]')) WHERE id='book'`)
	s.store.DB.Exec(`INSERT INTO catalog_entities VALUES('libraryFolders','folder','lib','{"path":"/media"}')`)
	return s, h, a, b, tok
}
func enqueueTool(t *testing.T, s *Server, h http.Handler, a person) string {
	t.Helper()
	it, e := s.catalog().Item(context.Background(), a.u.ID, "book")
	if e != nil {
		t.Fatal(e)
	}
	w := call(t, h, a, "POST", "/api/v1/catalog/items/book/tools", fmt.Sprintf(`{"kind":"m4b","revision":%d,"cover":false}`, it.UpdatedAt))
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	w.Code = 200
	return nativeString(jsonObject(t, w)["id"])
}
func renderJob(t *testing.T, s *Server, h http.Handler, a person) string {
	t.Helper()
	id := enqueueTool(t, s, h, a)
	if !s.runToolJob(context.Background()) {
		t.Fatal("worker found no job")
	}
	j, e := s.toolJob(context.Background(), a.u.ID, id)
	if e != nil || j.Status != "complete" || j.Size <= 0 || j.Duration < 4 {
		t.Fatal(j, e)
	}
	return id
}
func TestNativeMediaJobAppliesWithoutLosingOriginalsOrPersonalState(t *testing.T) {
	s, h, a, b, tok := toolHarness(t)
	ctx := context.Background()
	id := renderJob(t, s, h, a)
	if w := call(t, h, b, "GET", "/api/v1/catalog/tools/"+id, ""); w.Code == 200 {
		t.Fatal("another user saw job")
	}
	if w := call(t, h, b, "POST", "/api/v1/catalog/tools/"+id+"/apply", `{}`); w.Code == 200 {
		t.Fatal("another user applied job")
	}
	j, _ := s.toolJob(ctx, a.u.ID, id)
	path, _ := s.toolOutput(j)
	f, _ := os.Open(path)
	p, e := library.Probe(ctx, s.cfg.FFprobe, f)
	f.Close()
	if e != nil || p.Tags["title"] != "Native book" || len(p.Chapters) != 1 {
		t.Fatal(p, e)
	}
	if w := call(t, h, a, "GET", "/api/v1/catalog/tools/"+id+"/download", ""); w.Code != 200 || int64(w.Body.Len()) != j.Size {
		t.Fatal(w.Code)
	}
	s.catalog().SetProgress(ctx, a.u.ID, "book", "", map[string]any{"currentTime": 1.0})
	s.catalog().Bookmark(ctx, a.u.ID, "book", 1, "keep me", false)
	session := jsonObject(t, nativeCall(t, h, tok, "POST", "/api/items/book/play", `{"forceDirectPlay":true}`))
	sid := nativeString(session["id"])
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/sync", `{"reportId":"playing","currentTime":1,"timeListened":1,"playing":true}`))
	if w := call(t, h, a, "POST", "/api/v1/catalog/tools/"+id+"/apply", `{}`); w.Code != 409 {
		t.Fatal("active playback", w.Code, w.Body.String())
	}
	jsonObject(t, nativeCall(t, h, tok, "POST", "/api/session/"+sid+"/close", `{"reportId":"pause","currentTime":1,"timeListened":0,"playing":false}`))
	jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/tools/"+id+"/apply", `{}`))
	jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/tools/"+id+"/apply", `{}`))
	tracks, chapters, _, e := s.catalog().Timeline(ctx, a.u.ID, "book", "")
	if e != nil || len(tracks) != 1 || len(chapters) != 1 {
		t.Fatal(tracks, chapters, e)
	}
	for _, old := range []string{"11", "12"} {
		f, meta, e := s.catalog().OpenFile(ctx, a.u.ID, "book", old, true)
		if e != nil || !meta.Excluded {
			t.Fatal(meta, e)
		}
		f.Close()
	}
	if _, e = s.catalog().Scan(ctx, a.u.ID, "lib", library.ScanOptions{FFprobe: s.cfg.FFprobe, Force: true}); e != nil {
		t.Fatal(e)
	}
	after, _, _, e := s.catalog().Timeline(ctx, a.u.ID, "book", "")
	if e != nil || len(after) != 1 || after[0].ID != tracks[0].ID {
		t.Fatal("scan changed playback", after, e)
	}
	var pos float64
	var doc string
	s.store.DB.QueryRow(`SELECT position,document FROM catalog_progress WHERE account_id='alice' AND item_id='book'`).Scan(&pos, &doc)
	marks, markErr := s.catalog().Bookmarks(ctx, a.u.ID)
	if pos != 1 || markErr != nil || len(marks) != 1 {
		t.Fatal("personal state changed", pos, doc)
	}
	jsonObject(t, call(t, h, a, "DELETE", "/api/v1/catalog/tools/"+id, ""))
	f, _, e = s.catalog().OpenFile(ctx, a.u.ID, "book", tracks[0].ID, true)
	if e != nil {
		t.Fatal("deleting cache removed adopted file", e)
	}
	f.Close()
}
func TestNativeMediaRecoveryCancellationAndStaleSources(t *testing.T) {
	s, h, a, _, _ := toolHarness(t)
	ctx := context.Background()
	cancelled := enqueueTool(t, s, h, a)
	jsonObject(t, call(t, h, a, "DELETE", "/api/v1/catalog/tools/"+cancelled, ""))
	if s.runToolJob(ctx) {
		t.Fatal("ran cancelled job")
	}
	stale := enqueueTool(t, s, h, a)
	s.store.DB.Exec(`UPDATE catalog_items SET updated_at=updated_at+1 WHERE id='book'`)
	s.runToolJob(ctx)
	j, _ := s.toolJob(ctx, a.u.ID, stale)
	if j.Status != "failed" {
		t.Fatal(j)
	}
	jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/tools/"+stale+"/retry", `{}`))
	s.runToolJob(ctx)
	j, _ = s.toolJob(ctx, a.u.ID, stale)
	if j.Status != "complete" {
		t.Fatal(j)
	}
	output, _ := s.toolOutput(j)
	os.Remove(output)
	if e := s.recoverNativeTools(ctx); e != nil {
		t.Fatal(e)
	}
	j, _ = s.toolJob(ctx, a.u.ID, stale)
	if j.Status != "failed" {
		t.Fatal("missing output not marked", j)
	}
	id := renderJob(t, s, h, a)
	j, _ = s.toolJob(ctx, a.u.ID, id)
	output, _ = s.toolOutput(j)
	// Simulate interruption after the durable adoption journal, before copying.
	s.store.DB.Exec(`UPDATE catalog_tool_jobs SET status='applying',apply_root='root',apply_path=?,apply_file_id=? WHERE id=?`, "Book/recovered.m4b", "recovered-file", id)
	if e := s.recoverNativeTools(ctx); e != nil {
		t.Fatal(e)
	}
	j, _ = s.toolJob(ctx, a.u.ID, id)
	if j.Status != "applied" {
		t.Fatal(j)
	}
	tracks, _, _, e := s.catalog().Timeline(ctx, a.u.ID, "book", "")
	if e != nil || len(tracks) != 1 || tracks[0].ID != "recovered-file" {
		t.Fatal(tracks, e)
	}
}
func TestNativeMediaUnfinishedAdoptionCanBeCancelledAndTamperingIsRejected(t *testing.T) {
	s, h, a, _, _ := toolHarness(t)
	ctx := context.Background()
	id := renderJob(t, s, h, a)
	j, _ := s.toolJob(ctx, a.u.ID, id)
	output, _ := s.toolOutput(j)
	original, e := os.ReadFile(output)
	if e != nil {
		t.Fatal(e)
	}
	tampered := append([]byte{}, original...)
	tampered[0] ^= 1
	os.WriteFile(output, tampered, 0600)
	if w := call(t, h, a, "POST", "/api/v1/catalog/tools/"+id+"/apply", `{}`); w.Code == 200 {
		t.Fatal("tampered artifact adopted")
	}
	j, _ = s.toolJob(ctx, a.u.ID, id)
	if j.Status != "applying" {
		t.Fatal(j)
	}
	jsonObject(t, call(t, h, a, "DELETE", "/api/v1/catalog/tools/"+id, ""))
	j, _ = s.toolJob(ctx, a.u.ID, id)
	if j.Status != "cancelled" {
		t.Fatal(j)
	}
	tracks, _, _, e := s.catalog().Timeline(ctx, a.u.ID, "book", "")
	if e != nil || len(tracks) != 2 {
		t.Fatal(tracks, e)
	}
	jsonObject(t, call(t, h, a, "POST", "/api/v1/catalog/tools/"+id+"/retry", `{}`))
	s.runToolJob(ctx)
	j, _ = s.toolJob(ctx, a.u.ID, id)
	if j.Status != "complete" {
		t.Fatal(j)
	}
}

func TestNativeMediaRecoversAfterPublishingBeforeCatalogCommit(t *testing.T) {
	s, h, a, _, _ := toolHarness(t)
	ctx := context.Background()
	id := renderJob(t, s, h, a)
	if _, e := s.store.DB.Exec(`CREATE TRIGGER fail_tool_commit BEFORE INSERT ON catalog_files WHEN NEW.role='audio' BEGIN SELECT RAISE(ABORT,'simulated crash'); END`); e != nil {
		t.Fatal(e)
	}
	if w := call(t, h, a, "POST", "/api/v1/catalog/tools/"+id+"/apply", `{}`); w.Code == 200 {
		t.Fatal("injected failure ignored")
	}
	var root, path string
	s.store.DB.QueryRow(`SELECT r.local_path,j.apply_path FROM catalog_tool_jobs j JOIN catalog_roots r ON r.id=j.apply_root WHERE j.id=?`, id).Scan(&root, &path)
	if _, e := os.Stat(filepath.Join(root, path)); e != nil {
		t.Fatal("file was not published before commit failure", e)
	}
	tracks, _, _, e := s.catalog().Timeline(ctx, a.u.ID, "book", "")
	if e != nil || len(tracks) != 2 {
		t.Fatal("partial catalog committed", tracks, e)
	}
	if _, e = s.catalog().Scan(ctx, a.u.ID, "lib", library.ScanOptions{FFprobe: s.cfg.FFprobe}); e == nil {
		t.Fatal("scan adopted unfinished output")
	}
	s.store.DB.Exec(`DROP TRIGGER fail_tool_commit`)
	if e = s.recoverNativeTools(ctx); e != nil {
		t.Fatal(e)
	}
	j, _ := s.toolJob(ctx, a.u.ID, id)
	if j.Status != "applied" {
		t.Fatal(j)
	}
	tracks, _, _, e = s.catalog().Timeline(ctx, a.u.ID, "book", "")
	if e != nil || len(tracks) != 1 {
		t.Fatal(tracks, e)
	}
}
