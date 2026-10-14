//go:build !windows

package platform

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// SQLiteURI turns an absolute database path into a SQLite file: URI.
func SQLiteURI(abs string) string {
	return (&url.URL{Scheme: "file", Path: abs}).String()
}

// SyncDir flushes a directory so a rename or create inside it is durable.
func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// MakePrivate restricts a newly created secret file to its owner. On Unix the
// file is already created with mode 0600, so there is nothing to do.
func MakePrivate(path string) error { return nil }

// OpenLockFile creates or opens a lock file without following a symlink at path.
func OpenLockFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// LockExclusive takes a non-blocking exclusive lock; it fails if another process holds it.
func LockExclusive(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// Unlock releases a lock taken with LockExclusive.
func Unlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// FreeBytes returns the space available to this process on the filesystem holding path.
func FreeBytes(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// OpenDirNoFollow opens a directory, refusing a symlink at path.
func OpenDirNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// OpenDirAt opens the child directory name of parent without following symlinks.
// It returns ErrSkip when the entry vanished, is not a directory or is a symlink.
func OpenDirAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
		return nil, ErrSkip
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), nil
}

// PrivateFile reports whether only the owner can access the file (no group/other permission bits).
func PrivateFile(path string, info os.FileInfo) (bool, error) {
	return info.Mode().Perm()&0077 == 0, nil
}
