//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pjerra/turtle-agent/internal/privfile"
	"github.com/pjerra/turtle-agent/internal/slots"
)

// teardownOtherPIDs signals stray uid-owned pids from this dir's *.pid files.
func teardownOtherPIDs(dir string) []teardownCLIStep {
	entries, err := filepath.Glob(filepath.Join(dir, "*.pid"))
	if err != nil || len(entries) == 0 {
		return []teardownCLIStep{{Step: "other_pids", Status: "skip", Detail: "no pid files"}}
	}
	ours := uint32(os.Getuid())
	var steps []teardownCLIStep
	found := false
	for _, path := range entries {
		if filepath.Base(path) == "agent.pid" {
			continue
		}
		found = true
		name := filepath.Base(path)
		raw, err := privfile.Read(path, 64)
		if err != nil {
			steps = append(steps, teardownCLIStep{Step: "other_pids", Status: "skip", Detail: name + ": not running"})
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil || pid <= 0 {
			steps = append(steps, teardownCLIStep{Step: "other_pids", Status: "skip", Detail: name + ": not running"})
			continue
		}
		if err := syscall.Kill(pid, 0); err != nil {
			steps = append(steps, teardownCLIStep{Step: "other_pids", Status: "skip", Detail: name + ": not running"})
			continue
		}
		st, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
		if err != nil {
			steps = append(steps, teardownCLIStep{Step: "other_pids", Status: "skip", Detail: name + ": not running"})
			continue
		}
		if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != ours {
			steps = append(steps, teardownCLIStep{Step: "other_pids", Status: "skip", Detail: name + ": not running"})
			continue
		}
		start, serr := slots.ProcStart(pid)
		finfo, ferr := os.Stat(path)
		if serr != nil || ferr != nil || start.After(finfo.ModTime().Add(time.Second)) {
			steps = append(steps, teardownCLIStep{Step: "other_pids", Status: "skip", Detail: name + ": pid reused by another process: left alone"})
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if err := syscall.Kill(pid, 0); err != nil {
				break
			}
			teardownCLISleep(500 * time.Millisecond)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			steps = append(steps, teardownCLIStep{Step: "other_pids", Status: "ok", Detail: fmt.Sprintf("%s: pid %d ended by SIGTERM", name, pid)})
		} else {
			steps = append(steps, teardownCLIStep{Step: "other_pids", Status: "fail", Detail: fmt.Sprintf("%s: pid %d still running after SIGTERM: left alone", name, pid)})
		}
	}
	if !found {
		return []teardownCLIStep{{Step: "other_pids", Status: "skip", Detail: "no pid files"}}
	}
	return steps
}
