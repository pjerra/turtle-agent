//go:build linux

package slots

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pjerra/turtle-agent/internal/privfile"
)

type LinuxProbe struct{}

func (LinuxProbe) AgentPID(dir string) (int, bool) {
	if dir == "" {
		return 0, false
	}
	raw, err := privfile.Read(filepath.Join(dir, "agent.pid"), 64)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return pid, false
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return pid, false
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	want := filepath.Join(dir, "turtle-agent")
	abs, err := filepath.Abs(dir)
	if err == nil {
		if evaled, err := filepath.EvalSymlinks(abs); err == nil {
			want = filepath.Join(evaled, "turtle-agent")
		} else {
			want = filepath.Join(abs, "turtle-agent")
		}
	}
	return pid, exe == want
}

func (LinuxProbe) PortListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func ProcStart(pid int) (time.Time, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, err
	}
	s := string(raw)
	rparen := strings.LastIndex(s, ")")
	if rparen < 0 {
		return time.Time{}, fmt.Errorf("bad /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[rparen+1:])
	if len(fields) < 20 {
		return time.Time{}, fmt.Errorf("bad /proc/%d/stat", pid)
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	statRaw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	var btime uint64
	for _, line := range strings.Split(string(statRaw), "\n") {
		if strings.HasPrefix(line, "btime ") {
			v, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			btime = v
			break
		}
	}
	if btime == 0 {
		return time.Time{}, fmt.Errorf("no btime in /proc/stat")
	}
	// USER_HZ is 100 on all mainstream Linux ports (x86_64, arm64).
	const hz = 100.0
	return time.Unix(int64(btime)+int64(float64(startTicks)/hz), 0), nil
}
