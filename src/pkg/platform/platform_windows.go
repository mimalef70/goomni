//go:build windows

package platform

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SQLiteURI turns an absolute database path into a SQLite file: URI. A drive
// path needs the file:///C:/dir/name.db form; a bare C:\ path is an invalid
// URI authority.
func SQLiteURI(abs string) string {
	return (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(abs)}).String()
}

// SyncDir is a no-op on Windows: a directory cannot be flushed through a
// read-only handle (FlushFileBuffers fails with access denied), and NTFS journals
// metadata changes such as creates and renames.
func SyncDir(path string) error { return nil }

// MakePrivate replaces the file's inherited access list with a protected one
// that grants only its owner (the current user), SYSTEM and Administrators — the
// Windows counterpart of creating it with mode 0600. Call it right after
// creating the empty file, before writing the secret.
func MakePrivate(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	var entries []windows.EXPLICIT_ACCESS
	for _, sid := range []*windows.SID{user.User.Sid, system, admins} {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// Symlinks and other reparse points (junctions, mount points) are never followed.
// Since Go 1.23, Lstat reports symlinks as ModeSymlink and other reparse points as ModeIrregular.
func isLink(info os.FileInfo) bool {
	return info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}

// OpenLockFile creates or opens a lock file, refusing a symlink or reparse point at path.
// Go opens Windows handles as non-inheritable, the equivalent of O_CLOEXEC.
func OpenLockFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && (isLink(info) || !info.Mode().IsRegular()) {
		return nil, fmt.Errorf("lock file %s is not a regular file", path)
	}
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
}

// LockExclusive takes a non-blocking exclusive byte-range lock on the first byte
// (allowed beyond end of file); it fails if another process holds it. Windows
// releases the lock when the handle is closed or the process exits, as flock does.
func LockExclusive(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
}

// Unlock releases a lock taken with LockExclusive.
func Unlock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}

// FreeBytes returns the space available to this process on the volume holding path
// (a directory, or a file whose folder is used). A missing path is an error, as
// statfs reports on Unix (callers keep the previous value then).
func FreeBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	dir := path
	if !info.IsDir() {
		dir = filepath.Dir(path)
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err = windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return int64(avail), nil
}

// OpenDirNoFollow opens a directory, refusing a symlink or reparse point at path.
func OpenDirNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if isLink(info) {
		return nil, fmt.Errorf("%s is a link", path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", path)
	}
	return os.Open(path)
}

// OpenDirAt opens the child directory name of parent without following links.
// It returns ErrSkip when the entry vanished, is not a directory or is a link.
// Windows has no openat: the entry is checked with Lstat first. The scan only
// counts bytes for metrics, so the small check-then-open window is acceptable.
func OpenDirAt(parent *os.File, name string) (*os.File, error) {
	path := filepath.Join(parent.Name(), name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSkip
	}
	if err != nil {
		return nil, err
	}
	if isLink(info) || !info.IsDir() {
		return nil, ErrSkip
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSkip
	}
	return f, err
}

// PrivateFile is the Windows counterpart of "no group/other permission bits":
// the file's DACL must exist and allow access only to the file's owner, the
// current user, SYSTEM, Administrators and service accounts (NT SERVICE\*, which
// no person can log on as). Any other allowed trustee — Everyone, Users,
// INTERACTIVE, another user — makes the file public. Deny entries and entries
// that only apply to children are ignored.
func PrivateFile(path string, info os.FileInfo) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, err
	}
	if dacl == nil {
		return false, nil // a NULL DACL grants everyone full access
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, err
	}
	allowed := []*windows.SID{owner, user.User.Sid}
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, e := windows.CreateWellKnownSid(t)
		if e != nil {
			return false, e
		}
		allowed = append(allowed, sid)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Mask == 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		default:
			return false, nil // object or conditional entries are not evaluated, so not trusted
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trusted(sid, allowed) {
			return false, nil
		}
	}
	return true, nil
}

// trusted reports whether sid is one of allowed or a service SID (S-1-5-80-...).
func trusted(sid *windows.SID, allowed []*windows.SID) bool {
	for _, a := range allowed {
		if sid.Equals(a) {
			return true
		}
	}
	return strings.HasPrefix(sid.String(), "S-1-5-80-")
}
