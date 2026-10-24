package library_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func trashFixture(t *testing.T) fixture {
	f := setup(t)
	apply(t, f)
	if _, err := f.catalog.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.delete',json('true')) WHERE id='alice'; UPDATE catalog_files SET relative_path='another.mp3' WHERE item_id<>'visible'; INSERT INTO catalog_entities VALUES('libraryFolders','folder','public','{"path":"/media"}'); UPDATE catalog_items SET document=json_set(document,'$.item.path','/media/chapter.mp3','$.item.libraryFolderId','folder') WHERE id='visible'`); err != nil {
		t.Fatal(err)
	}
	return f
}
func TestTrashMoveRestoreConflictsRecoveryAndHistory(t *testing.T) {
	f := trashFixture(t)
	ctx := context.Background()
	it, _ := f.catalog.Item(ctx, 1, "visible")
	v, err := f.catalog.Trash(ctx, 1, it.ID, "", true, it.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != "deleted" {
		t.Fatal(v)
	}
	if _, err = f.catalog.Item(ctx, 1, it.ID); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("removed item still visible", err)
	}
	if _, err = os.Stat(filepath.Join(f.root, "chapter.mp3")); !os.IsNotExist(err) {
		t.Fatal("source not moved")
	}
	var progress, history int
	f.catalog.DB.QueryRow(`SELECT count(*) FROM catalog_progress WHERE item_id='visible'`).Scan(&progress)
	f.catalog.DB.QueryRow(`SELECT count(*) FROM catalog_history WHERE item_id='visible'`).Scan(&history)
	if progress != 2 || history != 1 {
		t.Fatal("deletion lost personal state", progress, history)
	}
	// A process restart after the rename but before the final transaction is safe.
	f.catalog.DB.Exec(`UPDATE catalog_trash SET state='moving' WHERE id=?`, v.ID)
	if err = f.catalog.RecoverTrash(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.root, "chapter.mp3"), []byte("new unrelated file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.catalog.TrashAction(ctx, 1, v.ID, "restore"); !errors.Is(err, library.ErrConflict) {
		t.Fatal("restore overwrote new file", err)
	}
	b, _ := os.ReadFile(filepath.Join(f.root, "chapter.mp3"))
	if string(b) != "new unrelated file" {
		t.Fatal("new file changed")
	}
	if err = os.Remove(filepath.Join(f.root, "chapter.mp3")); err != nil {
		t.Fatal(err)
	}
	restored, err := f.catalog.TrashAction(ctx, 1, v.ID, "retry")
	if err != nil || restored.State != "restored" {
		t.Fatal(restored, err)
	}
	b, err = os.ReadFile(filepath.Join(f.root, "chapter.mp3"))
	if err != nil || string(b) != "0123456789audio" {
		t.Fatal("original not restored", err, string(b))
	}
	if _, err = f.catalog.Item(ctx, 1, it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.catalog.TrashAction(ctx, 1, v.ID, "purge"); !errors.Is(err, library.ErrConflict) {
		t.Fatal("purged restored entry", err)
	}
}
func TestCatalogOnlyTrashDoesNotResurrectOnScan(t *testing.T) {
	f := trashFixture(t)
	ctx := context.Background()
	it, _ := f.catalog.Item(ctx, 1, "visible")
	if _, err := f.catalog.Trash(ctx, 2, it.ID, "", false, it.UpdatedAt); err == nil {
		t.Fatal("missing delete grant accepted")
	}
	v, err := f.catalog.Trash(ctx, 1, it.ID, "", false, it.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.root, "chapter.mp3")); err != nil {
		t.Fatal("catalog-only deletion moved original", err)
	}
	f.catalog.DB.Exec(`UPDATE catalog_accounts SET permissions=json_set(permissions,'$.update',json('true'),'$.accessAllTags',json('true'),'$.accessExplicitContent',json('true')) WHERE id='alice'`)
	if _, err = f.catalog.Scan(ctx, 1, "public", library.ScanOptions{FFprobe: "nonexistent-probe-must-not-run"}); err != nil {
		t.Fatal("scanner touched removed file", err)
	}
	page, err := f.catalog.Items(ctx, 1, library.Query{LibraryID: "public", Search: "visible"})
	if err != nil || page.Total != 0 {
		t.Fatal("deleted book resurrected", page, err)
	}
	if _, err = f.catalog.TrashAction(ctx, 1, v.ID, "restore"); err != nil {
		t.Fatal(err)
	}
	page, _ = f.catalog.Items(ctx, 1, library.Query{LibraryID: "public", Search: "visible"})
	if page.Total != 1 {
		t.Fatal("restore did not reveal book", page)
	}
}
func TestTrashPurgeAndChangedFileProtection(t *testing.T) {
	f := trashFixture(t)
	ctx := context.Background()
	it, _ := f.catalog.Item(ctx, 1, "visible")
	v, err := f.catalog.Trash(ctx, 1, it.ID, "", true, it.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(f.root, ".pocket-trash", v.ID, "*"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	original, _ := os.ReadFile(files[0])
	stat, _ := os.Stat(files[0])
	if err = os.WriteFile(files[0], []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.catalog.TrashAction(ctx, 1, v.ID, "purge"); !errors.Is(err, library.ErrConflict) {
		t.Fatal("purged changed file", err)
	}
	if b, _ := os.ReadFile(files[0]); string(b) != "changed" {
		t.Fatal("changed file was removed")
	}
	// Restore the exact fixture and its timestamp to retry the interrupted purge.
	os.WriteFile(files[0], original, 0600)
	os.Chtimes(files[0], stat.ModTime(), stat.ModTime())
	result, err := f.catalog.TrashAction(ctx, 1, v.ID, "retry")
	if err != nil || result.State != "purged" {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatal("purge did not remove trash file")
	}
	if _, err = f.catalog.TrashAction(ctx, 1, v.ID, "restore"); !errors.Is(err, library.ErrConflict) {
		t.Fatal("restored purged bytes", err)
	}
}

func TestTrashPreservesPhysicalFileSharedThroughAnotherRoot(t *testing.T) {
	f := trashFixture(t)
	ctx := context.Background()
	if _, err := f.catalog.DB.Exec(`INSERT INTO catalog_roots VALUES('alias','/alias',?); UPDATE catalog_files SET root_id='alias',relative_path='chapter.mp3',name='chapter.mp3' WHERE item_id<>'visible'`, f.root); err != nil {
		t.Fatal(err)
	}
	it, _ := f.catalog.Item(ctx, 1, "visible")
	v, err := f.catalog.Trash(ctx, 1, it.ID, "", true, it.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.root, "chapter.mp3")); err != nil {
		t.Fatal("removed another root's referenced file", err)
	}
	if _, err = f.catalog.TrashAction(ctx, 1, v.ID, "purge"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.root, "chapter.mp3")); err != nil {
		t.Fatal("purged shared file", err)
	}
}
