//go:build !unix

package dashboard

import "os"

// oNoFollow is nothing off unix; openControlLog refuses a link with an Lstat before it opens.
const oNoFollow = 0

// ownedByUser cannot tell off unix, so it trusts the file (the token file is checked the same way there).
func ownedByUser(os.FileInfo) bool { return true }

// openRefusal: off unix the open reports its own errors, so nothing is renamed here.
func openRefusal(_ string, err error) error { return err }
