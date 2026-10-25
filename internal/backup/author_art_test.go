package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBundleIncludesAuthorPhotos(t *testing.T) {
	st, opt := fixture(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(opt.DataDir, "native-art", "author.jpg"), []byte("author photo"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO catalog_entities VALUES('authors','a1',NULL,'{"id":"a1","name":"A","imagePath":"/pocket-native-art/author.jpg"}'),('authors','a2','lib','{"id":"a2","name":"B","imagePath":"/metadata/authors/a2.jpg"}')`); err != nil {
		t.Fatal(err)
	}
	m, err := Create(ctx, st, opt)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range m.Files {
		found = found || f.Name == "native-art/author.jpg"
	}
	if !found {
		t.Fatal("author photo missing from bundle", m.Files)
	}
	// A crafted path cannot pull other files into the bundle.
	if _, err = st.DB.Exec(`UPDATE catalog_entities SET document=json_set(document,'$.imagePath','/pocket-native-art/../secret.key') WHERE id='a1'`); err != nil {
		t.Fatal(err)
	}
	opt.Destination = filepath.Join(t.TempDir(), "again.tar.gz")
	if _, err = Create(ctx, st, opt); err == nil {
		t.Fatal("accepted traversal in author photo path")
	}
}
