//go:build !linux

package main

// teardownOtherPIDs is Linux-only (uid check via syscall.Stat_t, signals).
func teardownOtherPIDs(dir string) []teardownCLIStep {
	return []teardownCLIStep{{Step: "other_pids", Status: "skip", Detail: "slot leases and teardown need Linux (/proc and flock)"}}
}
