//go:build !unix

package dashboard

import "os"

// oNoFollow is nothing off unix; the Lstat check in openControlLog still refuses a link.
const oNoFollow = 0

// ownedByUser cannot tell off unix, so it trusts the file (the token file is checked the same way there).
func ownedByUser(os.FileInfo) bool { return true }
