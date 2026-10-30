//go:build !linux

package slots

import (
	"errors"
	"time"
)

type LinuxProbe struct{}

func (LinuxProbe) AgentPID(dir string) (int, bool) { return 0, false }

func (LinuxProbe) PortListening(port int) bool { return false }

func ProcStart(pid int) (time.Time, error) {
	return time.Time{}, errors.New("slot leases need Linux (/proc and flock)")
}
