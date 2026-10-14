// Package symlinktest lets symlink tests run where the OS allows creating
// symlinks and skip where it refuses: Windows needs Developer Mode or
// administrator rights (ERROR_PRIVILEGE_NOT_HELD). The link protections
// themselves are also checked with directory junctions in pkg/platform.
package symlinktest

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// errPrivilegeNotHeld is ERROR_PRIVILEGE_NOT_HELD.
const errPrivilegeNotHeld = syscall.Errno(1314)

const skipReason = "creating symlinks needs Developer Mode or administrator rights on Windows"

// Create creates a symlink, or skips the test where the OS refuses to.
func Create(t *testing.T, oldname, newname string) {
	t.Helper()
	err := os.Symlink(oldname, newname)
	if errors.Is(err, errPrivilegeNotHeld) {
		t.Skip(skipReason)
	}
	require.NoError(t, err)
}

// Require skips a test up front, before it opens anything, where the OS
// refuses to create symlinks.
func Require(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	Create(t, dir, dir+"-probe")
	require.NoError(t, os.Remove(dir+"-probe"))
}
