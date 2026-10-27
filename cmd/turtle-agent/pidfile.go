//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// writePIDFile records this process's pid at path (0600) and returns a removal
// func that deletes the file only while it still holds our pid. An existing
// file naming a live process is refused.
func writePIDFile(path string) (remove func(), err error) {
	ours := os.Getpid()
	if raw, rerr := os.ReadFile(path); rerr == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && pid > 0 {
			if kerr := syscall.Kill(pid, 0); kerr == nil {
				return nil, fmt.Errorf("pid file %s names a running process (pid %d): another agent of this dir?", path, pid)
			}
		}
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.tmp.%d", filepath.Base(path), ours))
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(ours)+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	remove = func() {
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return
		}
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr != nil || pid != ours {
			return
		}
		os.Remove(path)
	}
	return remove, nil
}
