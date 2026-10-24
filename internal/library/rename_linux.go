//go:build linux

package library

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// Resolve both parents through os.Root, then rename only leaf names using
// RENAME_NOREPLACE. Neither path traversal nor concurrent destination creation
// may turn a recoverable move into an overwrite.
func renameNoReplace(root *os.Root, from, to string) error {
	src, err := root.OpenRoot(filepath.Dir(from))
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := root.OpenRoot(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer dst.Close()
	sf, err := src.Open(".")
	if err != nil {
		return err
	}
	defer sf.Close()
	df, err := dst.Open(".")
	if err != nil {
		return err
	}
	defer df.Close()
	if err = unix.Renameat2(int(sf.Fd()), filepath.Base(from), int(df.Fd()), filepath.Base(to), unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	if err = sf.Sync(); err != nil {
		return err
	}
	return df.Sync()
}
