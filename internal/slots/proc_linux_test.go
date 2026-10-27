//go:build linux

package slots

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 11. AgentPID trusts only the dir's own binary: the test binary is refused,
// a copied sleep started as <dir>/turtle-agent is alive, then dead after kill.
func TestAgentPIDNeedsTheDirsBinary(t *testing.T) {
	p := LinuxProbe{}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprint(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, alive := p.AgentPID(dir); alive {
		t.Fatal("test binary reported alive as the dir's agent")
	}
	raw, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep")
	}
	bin := filepath.Join(dir, "turtle-agent")
	if err := os.WriteFile(bin, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if err := os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprint(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, alive := p.AgentPID(dir); !alive || pid != cmd.Process.Pid {
		t.Fatalf("copied sleep: pid=%d alive=%v, want alive", pid, alive)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	for range 50 {
		if _, alive := p.AgentPID(dir); !alive {
			return
		}
	}
	t.Fatal("killed process still reported alive")
}
