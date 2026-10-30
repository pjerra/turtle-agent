//go:build !linux

package main

import (
	"fmt"
	"io"
)

// slotsUnsupported reports the Linux-only refusal for the lease/slot/teardown
// subcommands on non-Linux builds.
func slotsUnsupported(cmd string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "%s: slot leases and teardown need Linux (/proc and flock)\n", cmd)
	return exitUsage
}

func slotsSupported() bool { return false }
