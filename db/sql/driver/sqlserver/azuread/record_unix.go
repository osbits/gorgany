//go:build unix

package azuread

import (
	"os"
	"syscall"
)

// writableByOthers says why a user other than the one running the process could have written
// the file info describes, and is empty when none could: it belongs to another user, or its mode
// lets its group or everyone write it. A file the process wrote is its user's, 0600.
func writableByOthers(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != uint32(os.Getuid()) {
		return "belongs to another user"
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "can be written by other users"
	}
	return ""
}
