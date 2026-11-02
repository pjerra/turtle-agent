//go:build unix

package dashboard

import (
	"os"
	"syscall"
)

// oNoFollow makes an open fail on a final component that is a symbolic link (the control log is ours alone).
const oNoFollow = syscall.O_NOFOLLOW

// ownedByUser reports whether the file belongs to the user running the dashboard.
func ownedByUser(st os.FileInfo) bool {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(sys.Uid) == os.Getuid()
	}
	return true
}
