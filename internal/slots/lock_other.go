//go:build !unix

package slots

import "errors"

func lockDir(dir string) (func(), error) {
	return nil, errors.New("slot leases need Linux (/proc and flock)")
}
