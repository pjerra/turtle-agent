//go:build !linux

package slots

import (
	"errors"
	"time"
)

type LinuxProbe struct{}

func (LinuxProbe) AgentPID(dir string) (int, bool) { return 0, false }

func (LinuxProbe) PortListening(port int) bool { return false }

// procStart cannot tell anything off Linux: a lease is never called dead here.
func procStart(pid int) (start uint64, alive, exact bool) { return 0, false, false }

func ProcStart(pid int) (time.Time, error) {
	return time.Time{}, errors.New("slot leases need Linux (/proc and flock)")
}
