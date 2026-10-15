//go:build unix

package routeedit

import (
	"os"
	"syscall"
)

// oNoFollow makes an open fail on a final component that is a symbolic link.
const oNoFollow = syscall.O_NOFOLLOW

// ownedByUser reports whether the file belongs to the user running the process.
func ownedByUser(st os.FileInfo) bool {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(sys.Uid) == os.Getuid()
	}
	return true
}
