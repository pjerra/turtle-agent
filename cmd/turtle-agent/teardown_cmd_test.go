package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/slots"
)

type teardownCLIFakeProbe struct {
	pid   int
	alive bool
	ports map[int]bool
}

func (f *teardownCLIFakeProbe) AgentPID(dir string) (int, bool) { return f.pid, f.alive }
func (f *teardownCLIFakeProbe) PortListening(port int) bool     { return f.ports[port] }

func teardownCLIFixture(t *testing.T, rpcSteps []teardownCLIStep, rpcFail bool) (dir, slotsPath, tokenFile string, port int, restore func()) {
	t.Helper()
	oldProbe := teardownProbe
	oldSleep := teardownCLISleep
	teardownCLISleep = func(time.Duration) {}
	teardownProbe = &teardownCLIFakeProbe{ports: map[int]bool{}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port = ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		var reply any
		if rpcFail {
			reply = map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32000, "message": "boom"}}
		} else {
			reply = map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"steps": rpcSteps, "failed": 0}}
		}
		raw, _ := json.Marshal(reply)
		w.Write(raw)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	ln.Close()
	time.Sleep(100 * time.Millisecond)
	dir = t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "turtle-agent")
	os.MkdirAll(cfgDir, 0o700)
	src := filepath.Join(cfgDir, "settings-src.json")
	os.WriteFile(src, []byte(`{"realmd":"127.0.0.1:3724","rpc":"127.0.0.1:0","accounts":{"TESTACC":"pw-not-real"}}`), 0o600)
	table := fmt.Sprintf(`{"version":1,"servers":[{"name":"m910q","realmd":"100.78.24.50:3724","slots":[
		{"id":"1","account":"TESTACC","character":"Gbmage","port":%d,"settings_from":"%s"}]}]}`, port, src)
	slotsPath = filepath.Join(cfgDir, "slots.json")
	os.WriteFile(slotsPath, []byte(table), 0o600)
	if code, _, errOut := runCLI("lease", "acquire", "m910q", "--slot", "1", "--holder", "w-a", "--slots", slotsPath); code != exitOK {
		t.Fatalf("acquire: %d %q", code, errOut)
	}
	bin := filepath.Join(t.TempDir(), "turtle-agent")
	os.WriteFile(bin, []byte("x"), 0o755)
	if code, _, errOut := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath); code != exitOK {
		t.Fatalf("setup: %d %q", code, errOut)
	}
	tokenFile = filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port))
	os.WriteFile(tokenFile, []byte("tok-not-real\n"), 0o600)
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("re-listen: %v", err)
	}
	go srv.Serve(ln2)
	// AgentPID is faked per test below; default to not-alive so agent_exit
	// never signals anything.
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: false, ports: map[int]bool{}}
	return dir, slotsPath, tokenFile, port, func() {
		teardownProbe = oldProbe
		teardownCLISleep = oldSleep
		srv.Close()
	}
}

func teardownCLILeaseHeld(t *testing.T, slotsPath string) bool {
	t.Helper()
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	store := slots.NewStore(cfg, slotsPath)
	states, err := store.List("m910q")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Slot.ID == "1" {
			return s.State == "held"
		}
	}
	return false
}

func TestTeardownCLIKeepsLeaseOnGameFailure(t *testing.T) {
	failSteps := []teardownCLIStep{{Step: "run_stop", Status: "fail", Detail: "run stop: refused"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, failSteps, false)
	defer restore()
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitTeardownFailed {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("no FAIL line: %q", out)
	}
	if !teardownCLILeaseHeld(t, slotsPath) {
		t.Fatalf("lease released despite the game failure")
	}
	code, _, _ = runCLI("teardown", dir, "--slots", slotsPath, "--release-anyway")
	if code != exitTeardownFailed {
		t.Fatalf("second: code %d", code)
	}
	if teardownCLILeaseHeld(t, slotsPath) {
		t.Fatalf("lease kept despite --release-anyway")
	}
}

func TestTeardownCLIDeletesSecretsOnly(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, port, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	os.MkdirAll(filepath.Join(dir, "traces"), 0o755)
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("code %d stdout %q", code, out)
	}
	for _, gone := range []string{"settings.json", "agent.pid", fmt.Sprintf("rpc-%d.token", port)} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s still there", gone)
		}
	}
	for _, stays := range []string{"slot.json", "turtle-agent", "traces"} {
		if _, err := os.Stat(filepath.Join(dir, stays)); err != nil {
			t.Fatalf("%s gone: %v", stays, err)
		}
	}
}

func TestTeardownCLINeverSignalsAForeignPid(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	// Use the real identity probe: agent.pid names this live test process,
	// whose exe is the test binary, not <dir>/turtle-agent. If the exe check
	// is removed, the probe reports alive and this test fails below.
	teardownProbe = slots.LinuxProbe{}
	os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	// other_pids: a pid file older than its process (this one, started after
	// the file's mtime) must be left alone as a pid reuse.
	other := filepath.Join(dir, "sampler.pid")
	os.WriteFile(other, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(other, past, past)
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitTeardownFailed {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if !strings.Contains(out, "gone without a clean exit") {
		t.Fatalf("no gone-pid line: %q", out)
	}
	if !strings.Contains(out, "pid reused by another process: left alone") {
		t.Fatalf("no pid-reused line: %q", out)
	}
	// The foreign pid was never signalled: this process is still here, and
	// its sampler.pid file is untouched.
	if err := syscall.Kill(os.Getpid(), 0); err != nil {
		t.Fatalf("self dead: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("sampler.pid touched: %v", err)
	}
}

func TestTeardownCLIRunsTwice(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	if code, _, _ := runCLI("teardown", dir, "--slots", slotsPath); code != exitOK {
		t.Fatalf("first run failed")
	}
	teardownProbe = &teardownCLIFakeProbe{ports: map[int]bool{}}
	// agent.pid was deleted by the first run: rpc must skip, files (already gone).
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("second run: code %d stdout %q", code, out)
	}
	if !strings.Contains(out, "no agent was started") {
		t.Fatalf("no rpc-skip line: %q", out)
	}
	if !strings.Contains(out, "(already gone)") {
		t.Fatalf("no already-gone line: %q", out)
	}
	if !strings.Contains(out, "not leased by w-a any more") {
		t.Fatalf("no lease-skip line: %q", out)
	}
}

// Item 5 (R2): a slot.json whose token_file points outside the dir deletes
// nothing outside it (nor inside: the tampered manifest refuses the run).
func TestTeardownCLIRejectsOutsideTokenPath(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	outside := filepath.Join(t.TempDir(), "precious.token")
	if err := os.WriteFile(outside, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["token_file"] = outside
	tampered, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitUsage {
		t.Fatalf("tampered manifest: code %d stderr %q, want %d", code, errOut, exitUsage)
	}
	if !strings.Contains(errOut, "outside") {
		t.Fatalf("no outside-dir refusal: %q", errOut)
	}
	// Only the tampered manifest may stop the run: slot.json itself stays,
	// and nothing outside the dir is touched.
	for _, p := range []string{"settings.json", "slot.json"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Fatalf("%s deleted despite the refusal: %v", p, err)
		}
	}
	kept, _ := os.ReadFile(outside)
	if string(kept) != "precious" {
		t.Fatal("outside file touched")
	}
}

// Item 6 (R2): slot.json records the --slots used at setup; teardown without
// --slots uses it instead of the default path.
func TestTeardownUsesManifestSlotsPath(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	raw, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["slots_path"] != slotsPath {
		t.Fatalf("slots_path = %v, want %s", m["slots_path"], slotsPath)
	}
	if m["lease_id"] == nil || m["lease_id"] == "" {
		t.Fatal("slot.json has no lease_id")
	}
	// Move the slots file away from the default path: only the manifest
	// path still leads to the lease.
	alt := filepath.Join(t.TempDir(), "sub", "slots.json")
	rawSlots, err := os.ReadFile(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(alt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alt, rawSlots, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(slotsPath); err != nil {
		t.Fatal(err)
	}
	m["slots_path"] = alt
	fixed, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), fixed, 0o600); err != nil {
		t.Fatal(err)
	}
	// No --slots flag: the lease must still be released via the manifest path.
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: false, ports: map[int]bool{}}
	if code, out, _ := runCLI("teardown", dir); code != exitOK {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if teardownCLILeaseHeld(t, alt) {
		t.Fatal("lease kept without --slots")
	}
}

// A follow step "ending" (the agent's follow loop did not exit in time) is a game-side failure: exit 14, the lease kept.
func TestTeardownCLIFollowEndingIsAFailure(t *testing.T) {
	steps := []teardownCLIStep{{Step: "follow", Status: "ending", Detail: "the player follow loop did not end"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, steps, false)
	defer restore()
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitTeardownFailed {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if !teardownCLILeaseHeld(t, slotsPath) {
		t.Fatalf("lease released despite the follow still ending")
	}
}
