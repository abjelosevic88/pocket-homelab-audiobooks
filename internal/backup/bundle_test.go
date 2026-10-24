package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

func fixture(t *testing.T) (*store.Store, Options) {
	t.Helper()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(dir, "pocket-audiobooks.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for name, data := range map[string][]byte{"secret.key": bytes.Repeat([]byte{42}, 32), "core.token": []byte("test-registration"), "core.token.oidc.json": []byte(`{"issuer":"https://core.invalid","client_secret":"test-secret"}`)} {
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	u := store.User{Email: "backup@example.com", Role: "admin"}
	if err = st.CreateUser(context.Background(), &u); err != nil {
		t.Fatal(err)
	}
	art := filepath.Join(dir, "native-art")
	if err = os.Mkdir(art, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(art, "cover.png"), []byte("immutable cover bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	media := t.TempDir()
	if err = os.WriteFile(filepath.Join(media, "book.mp3"), []byte("external original audio"), 0600); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err = st.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO catalog_roots VALUES('native-art','/pocket-native-art',?)`, art)
	exec(`INSERT INTO catalog_roots VALUES('media','/media',?)`, media)
	exec(`INSERT INTO catalog_libraries VALUES('lib','Books','book',0,'{}')`)
	exec(`INSERT INTO catalog_accounts VALUES('root',?,'root',1,0,'{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"download":true,"update":true,"delete":true}')`, u.ID)
	exec(`INSERT INTO catalog_items VALUES('book','lib','book-media','book','Book','Author',10,0,0,1,1,'[]','{}')`)
	exec(`INSERT INTO catalog_files VALUES('book','cover','cover','native-art','cover.png','cover.png','image/png',21,0,0,0,'{}')`)
	exec(`INSERT INTO catalog_files VALUES('book','audio','audio','media','book.mp3','book.mp3','audio/mpeg',23,10,1,0,'{}')`)
	return st, Options{DataDir: dir, Destination: filepath.Join(t.TempDir(), "backup.tar.gz"), Backend: "native", Version: "test"}
}
func TestBundleRoundTripAndTrashRecovery(t *testing.T) {
	st, opt := fixture(t)
	ctx := context.Background()
	cat := &library.Catalog{DB: st.DB}
	trashed, err := cat.Trash(ctx, 1, "book", "", true, 1)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Create(ctx, st, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ExternalRoots) != 1 || m.MediaIncluded || len(m.Files) != 5 {
		t.Fatal("bundle inventory", m)
	}
	if _, err = Verify(ctx, opt.Destination); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if _, err = Restore(ctx, opt.Destination, dest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"secret.key", "core.token", "core.token.oidc.json"} {
		a, _ := os.ReadFile(filepath.Join(opt.DataDir, name))
		b, _ := os.ReadFile(filepath.Join(dest, name))
		if !bytes.Equal(a, b) {
			t.Fatal("lost credential", name)
		}
	}
	info, _ := os.Stat(dest)
	if info.Mode().Perm() != 0700 {
		t.Fatal("restore directory permissions", info.Mode())
	}
	info, _ = os.Stat(filepath.Join(dest, "pocket-audiobooks.db"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("database permissions", info.Mode())
	}
	restored, err := store.Open(filepath.Join(dest, "pocket-audiobooks.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = (&library.Catalog{DB: restored.DB}).TrashAction(ctx, 1, trashed.ID, "restore"); err != nil {
		t.Fatal("restored trash identity failed", err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "native-art", "cover.png"))
	if err != nil || string(b) != "immutable cover bytes" {
		t.Fatal("artwork recovery", err)
	}
	if _, err = Restore(ctx, opt.Destination, dest); err == nil {
		t.Fatal("overwrote an installation")
	}
	before, _ := os.ReadFile(opt.Destination)
	if _, err = Create(ctx, st, opt); err == nil {
		t.Fatal("overwrote backup")
	}
	after, _ := os.ReadFile(opt.Destination)
	if !bytes.Equal(before, after) {
		t.Fatal("failed backup changed destination")
	}
}
func rewrite(t *testing.T, input, output string, fn func(*tar.Header, []byte) (*tar.Header, []byte)) {
	t.Helper()
	in, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	z := gzip.NewWriter(out)
	tw := tar.NewWriter(z)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		h, b = fn(h, b)
		if h == nil {
			continue
		}
		if e = tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if _, e = tw.Write(b); e != nil {
			t.Fatal(e)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestBundleRejectsCorruptionTraversalAndLinksWithoutPublishing(t *testing.T) {
	st, opt := fixture(t)
	ctx := context.Background()
	if _, err := Create(ctx, st, opt); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*tar.Header, []byte) (*tar.Header, []byte){
		"corrupt": func(h *tar.Header, b []byte) (*tar.Header, []byte) {
			if h.Name == "secret.key" {
				b[0] ^= 0xff
			}
			return h, b
		},
		"duplicate": func(h *tar.Header, b []byte) (*tar.Header, []byte) {
			if h.Name == "secret.key" {
				h.Name = "core.token"
			}
			return h, b
		},
		"traversal": func(h *tar.Header, b []byte) (*tar.Header, []byte) {
			if h.Name == "secret.key" {
				h.Name = "../escape"
			}
			return h, b
		},
		"symlink": func(h *tar.Header, b []byte) (*tar.Header, []byte) {
			if h.Name == "secret.key" {
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "/etc/passwd"
				h.Size = 0
				b = nil
			}
			return h, b
		},
		"missing": func(h *tar.Header, b []byte) (*tar.Header, []byte) {
			if h.Name == "secret.key" {
				return nil, nil
			}
			return h, b
		},
		"no-manifest": func(h *tar.Header, b []byte) (*tar.Header, []byte) {
			if h.Name == "manifest.json" {
				return nil, nil
			}
			return h, b
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			bad := filepath.Join(dir, "bad.tar.gz")
			rewrite(t, opt.Destination, bad, fn)
			dest := filepath.Join(dir, "restore")
			if _, err := Restore(ctx, bad, dest); err == nil {
				t.Fatal("accepted malformed bundle")
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatal("published failed restore")
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".pocket-restore-") {
					t.Fatal("left partial restore")
				}
			}
		})
	}
	t.Run("gzip-footer", func(t *testing.T) {
		b, _ := os.ReadFile(opt.Destination)
		b[len(b)-1] ^= 1
		bad := filepath.Join(t.TempDir(), "bad.gz")
		os.WriteFile(bad, b, 0600)
		if _, err := Verify(ctx, bad); err == nil {
			t.Fatal("accepted corrupt gzip footer")
		}
	})
	t.Run("extra-member", func(t *testing.T) {
		b, _ := os.ReadFile(opt.Destination)
		bad := filepath.Join(t.TempDir(), "bad.gz")
		os.WriteFile(bad, append(b, b...), 0600)
		if _, err := Verify(ctx, bad); err == nil {
			t.Fatal("accepted appended gzip member")
		}
	})
}
func TestBundleMissingKeyAndSymlinkArtworkFail(t *testing.T) {
	for _, name := range []string{"secret.key", "native-art/cover.png"} {
		t.Run(name, func(t *testing.T) {
			st, opt := fixture(t)
			path := filepath.Join(opt.DataDir, name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if name != "secret.key" {
				if err := os.Symlink(filepath.Join(opt.DataDir, "core.token"), path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Create(context.Background(), st, opt); err == nil {
				t.Fatal("accepted missing key or symlink artwork")
			}
			if _, err := os.Stat(opt.Destination); !os.IsNotExist(err) {
				t.Fatal("published broken backup")
			}
		})
	}
}
func TestSnapshotNeverOverwritesAndExclusiveLeaseReleases(t *testing.T) {
	st, opt := fixture(t)
	ctx := context.Background()
	dest := filepath.Join(opt.DataDir, "pocket-audiobooks.db")
	if err := st.Backup(ctx, dest); err == nil {
		t.Fatal("overwrote live database")
	}
	if err := st.DB.Ping(); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(t.TempDir(), "existing")
	os.WriteFile(existing, []byte("keep"), 0600)
	if err := st.Backup(ctx, existing); err == nil {
		t.Fatal("overwrote destination")
	}
	b, _ := os.ReadFile(existing)
	if string(b) != "keep" {
		t.Fatal("changed existing file")
	}
	lease, err := Exclusive(opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if other, e := Exclusive(opt.DataDir); e == nil {
		other.Close()
		t.Fatal("allowed concurrent instance")
	}
	lease.Close()
	other, err := Exclusive(opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
}
