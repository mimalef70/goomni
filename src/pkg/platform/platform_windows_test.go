//go:build windows

package platform

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// A directory junction needs no privilege on Windows (unlike a symlink), so it
// exercises the reparse-point protections everywhere the tests run.
func junction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestDirectoryScanSkipsJunctions(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "real"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0600))
	junction(t, filepath.Join(root, "jn"), outside)

	parent, err := OpenDirNoFollow(root)
	require.NoError(t, err)
	defer parent.Close()
	child, err := OpenDirAt(parent, "real")
	require.NoError(t, err)
	child.Close()
	_, err = OpenDirAt(parent, "jn")
	require.ErrorIs(t, err, ErrSkip, "a junction is never followed")
	_, err = OpenDirAt(parent, "file")
	require.ErrorIs(t, err, ErrSkip, "a file is not a directory")
	_, err = OpenDirAt(parent, "missing")
	require.ErrorIs(t, err, ErrSkip, "a vanished entry is skipped")

	_, err = OpenDirNoFollow(filepath.Join(root, "jn"))
	require.Error(t, err, "a junction root is refused")
}

func TestLockFileRefusesJunctionAndIsExclusive(t *testing.T) {
	dir := t.TempDir()
	junction(t, filepath.Join(dir, "lock-link"), t.TempDir())
	_, err := OpenLockFile(filepath.Join(dir, "lock-link"))
	require.Error(t, err, "a reparse point is not a lock file")

	path := filepath.Join(dir, "owner.lock")
	first, err := OpenLockFile(path)
	require.NoError(t, err)
	defer first.Close()
	require.NoError(t, LockExclusive(first))
	second, err := OpenLockFile(path)
	require.NoError(t, err)
	defer second.Close()
	require.Error(t, LockExclusive(second), "a second owner is refused while the first holds the lock")
	require.NoError(t, Unlock(first))
	require.NoError(t, LockExclusive(second), "the lock is free after unlock")
	require.NoError(t, Unlock(second))
}

func TestMakePrivateSatisfiesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	require.NoError(t, MakePrivate(path))
	info, err := os.Stat(path)
	require.NoError(t, err)
	private, err := PrivateFile(path, info)
	require.NoError(t, err)
	require.True(t, private)
	// the owner can still use it
	require.NoError(t, os.WriteFile(path, []byte("secret"), 0600))

	// a service account (the one the GoOmni service runs as) may read it
	grant := func(file, trustee string) {
		out, err := exec.Command("icacls", file, "/grant", trustee+":R").CombinedOutput()
		require.NoError(t, err, string(out))
	}
	grant(path, "NT SERVICE\\TrustedInstaller")
	private, err = PrivateFile(path, info)
	require.NoError(t, err)
	require.True(t, private, "a service SID is trusted")

	// any other trustee makes it public: Users, and also groups a denylist would miss
	for _, trustee := range []string{"*S-1-5-32-545", "*S-1-5-4", "*S-1-2-0"} { // Users, INTERACTIVE, LOCAL
		public := filepath.Join(t.TempDir(), "public.key")
		require.NoError(t, os.WriteFile(public, nil, 0600))
		require.NoError(t, MakePrivate(public))
		grant(public, trustee)
		info, err = os.Stat(public)
		require.NoError(t, err)
		private, err = PrivateFile(public, info)
		require.NoError(t, err)
		require.False(t, private, trustee)
	}
}

func TestFreeBytesAndSQLiteURI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "with space")
	require.NoError(t, os.Mkdir(dir, 0700))
	free, err := FreeBytes(dir)
	require.NoError(t, err)
	require.Greater(t, free, int64(0))

	db := filepath.Join(dir, "probe.db")
	_, err = FreeBytes(db) // missing, like statfs on Unix
	require.Error(t, err)

	uri := SQLiteURI(db)
	require.True(t, strings.HasPrefix(uri, "file:///"), uri)
	conn, err := sql.Open("sqlite", uri)
	require.NoError(t, err)
	_, err = conn.Exec("CREATE TABLE t (x int)")
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	_, err = os.Stat(db)
	require.False(t, errors.Is(err, os.ErrNotExist), "the database is created at the given path")
	free, err = FreeBytes(db) // an existing file: its folder's volume
	require.NoError(t, err)
	require.Greater(t, free, int64(0))
}
