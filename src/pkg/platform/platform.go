// Package platform holds the few operating-system specific operations GoOmni
// needs: exclusive ownership locks on the database and media root, free disk
// space, symlink-safe directory opening for the media scan, and the "private
// file" check of the master key file. The Unix implementation keeps the
// original calls; the Windows implementation provides equivalents so GoOmni
// can run as a native Windows process (see docs/windows.md).
package platform

import "errors"

// ErrSkip reports a directory entry the media scan must skip: it disappeared,
// is not a directory, or is a symlink/reparse point.
var ErrSkip = errors.New("entry skipped")
