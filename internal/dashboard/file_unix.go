//go:build unix

package dashboard

import (
	"errors"
	"fmt"
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

// openRefusal names the refusal behind an open that failed on a link (O_NOFOLLOW) or a folder (O_RDWR); any other error is returned as it is.
func openRefusal(path string, err error) error {
	switch {
	case errors.Is(err, syscall.ELOOP):
		return fmt.Errorf("control log %s is a symbolic link", path)
	case errors.Is(err, syscall.EISDIR):
		return fmt.Errorf("control log %s is not a regular file", path)
	}
	return err
}
