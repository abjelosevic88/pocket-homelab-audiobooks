//go:build linux

package backup

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// Exclusive prevents an offline bundle operation from racing a running server.
// The lock file is permanent; flock releases the lease on close/process exit.
func Exclusive(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(dir, ".instance.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), ".instance.lock")
	if info, e := f.Stat(); e != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("instance lease must be a regular file")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another Pocket Audiobooks process is using this data directory: %w", err)
	}
	return f, nil
}
func publishDirectory(from, to string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
