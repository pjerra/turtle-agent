//go:build !unix

package main

import "fmt"

func writePIDFile(path string) (func(), error) {
	return nil, fmt.Errorf("pid-file needs Linux (/proc and flock)")
}
