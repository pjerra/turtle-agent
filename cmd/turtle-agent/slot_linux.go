//go:build linux

package main

import "io"

func slotsSupported() bool { return true }

func slotsUnsupported(string, io.Writer) int { return exitUsage }
