//go:build !unix

package routeedit

import "os"

const oNoFollow = 0

func ownedByUser(os.FileInfo) bool { return true }
