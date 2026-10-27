// Command teardown cleans up a slot-setup client dir: game-side teardown
// over RPC, the agent process, stray pids, secrets and the lease.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pjerra/turtle-agent/internal/privfile"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/slots"
)

// exitTeardownFailed is the exit code of a teardown with a failed step (14).
const exitTeardownFailed = 14

const teardownUsage = "usage: turtle-agent teardown [--keep-lease] [--release-anyway] [--gm off|on|keep] [--dry-run] [--json] [--slots FILE] <dir>"

// teardownProbe is the slots.Probe the teardown CLI uses; tests replace it.
var teardownProbe slots.Probe = slots.LinuxProbe{}

// teardownCLISleep waits between liveness polls; tests replace it.
var teardownCLISleep = time.Sleep

type teardownCLIStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type teardownSlotManifest struct {
	Server    string `json:"server"`
	Slot      string `json:"slot"`
	Holder    string `json:"holder"`
	LeaseID   string `json:"lease_id"`
	Account   string `json:"account"`
	Character string `json:"character"`
	Port      int    `json:"port"`
	Dir       string `json:"dir"`
	Binary    string `json:"binary"`
	Settings  string `json:"settings"`
	PIDFile   string `json:"pid_file"`
	TokenFile string `json:"token_file"`
	SlotsPath string `json:"slots_path"`
	Created   string `json:"created"`
}

// teardownCheckManifest treats slot.json paths as assertions: manifest.Dir
// must be the canonical teardown dir, and the settings/pid/binary/token
// paths must be the ones derived inside it (never symlinks, never outside).
// A tampered slot.json refuses the teardown before anything is deleted.
func teardownCheckManifest(dir string, manifest *teardownSlotManifest) error {
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("teardown: %s: %v", dir, err)
	}
	if manifest.Dir != canon && manifest.Dir != dir {
		return fmt.Errorf("teardown: slot.json dir %q is not this dir %q: teardown works on a dir made by slot setup", manifest.Dir, dir)
	}
	if manifest.Port < 1 || manifest.Port > 65535 {
		return fmt.Errorf("teardown: slot.json has a bad port %d", manifest.Port)
	}
	want := map[string]string{
		"settings":   filepath.Join(dir, "settings.json"),
		"pid_file":   filepath.Join(dir, "agent.pid"),
		"binary":     filepath.Join(dir, "turtle-agent"),
		"token_file": filepath.Join(dir, fmt.Sprintf("rpc-%d.token", manifest.Port)),
	}
	got := map[string]string{
		"settings":   manifest.Settings,
		"pid_file":   manifest.PIDFile,
		"binary":     manifest.Binary,
		"token_file": manifest.TokenFile,
	}
	for _, k := range []string{"settings", "pid_file", "binary", "token_file"} {
		if got[k] != want[k] {
			return fmt.Errorf("teardown: slot.json %s %q is outside %s: teardown works on a dir made by slot setup", k, got[k], dir)
		}
		if fi, err := os.Lstat(want[k]); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("teardown: %s is a symlink: teardown works on a dir made by slot setup", want[k])
		}
	}
	return nil
}

func teardownCmd(args []string, stdout, stderr io.Writer) int {
	if !slotsSupported() {
		return slotsUnsupported("teardown", stderr)
	}
	o, code := parseTeardownArgs(args, stderr)
	if code != exitOK {
		return code
	}
	// Aliases so the body below reads as before.
	keepLease, releaseAnyway, gm := &o.keepLease, &o.releaseAnyway, &o.gm
	dryRun, asJSON, slotsPath := &o.dryRun, &o.asJSON, &o.slotsPath
	dir := o.dir
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(stderr, "teardown: %v\n", err)
		return exitUsage
	}
	dir = absDir
	raw, err := privfile.Read(filepath.Join(dir, "slot.json"), 64*1024)
	if err != nil {
		fmt.Fprintf(stderr, "teardown: %s/slot.json missing or unreadable: teardown works on a dir made by slot setup\n", dir)
		return exitUsage
	}
	var manifest teardownSlotManifest
	if err := json.Unmarshal(raw, &manifest); err != nil ||
		manifest.Server == "" || manifest.Slot == "" || manifest.Holder == "" || manifest.Port == 0 {
		fmt.Fprintf(stderr, "teardown: %s/slot.json missing or unreadable: teardown works on a dir made by slot setup\n", dir)
		return exitUsage
	}
	if err := teardownCheckManifest(dir, &manifest); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
	}
	if *slotsPath == "" {
		*slotsPath = manifest.SlotsPath
	}

	steps := []teardownCLIStep{}
	failed := 0
	add := func(step, status, detail string) {
		steps = append(steps, teardownCLIStep{Step: step, Status: status, Detail: detail})
		if status == "fail" {
			failed++
		}
	}

	// 2. Game-side teardown over RPC.
	pid, alive := teardownProbe.AgentPID(dir)
	pidFilePresent := teardownPIDFilePresent(dir)
	gameFailed := false
	if alive {
		rpcSteps, rerr := teardownCallAgent(dir, manifest.Port, manifest.TokenFile, *dryRun, *gm)
		if rerr != nil {
			add("rpc", "fail", rerr.Error())
			gameFailed = true
		} else {
			for _, s := range rpcSteps {
				add(s.Step, s.Status, s.Detail)
				if s.Status == "fail" {
					gameFailed = true
				}
			}
		}
	} else if !pidFilePresent {
		add("rpc", "skip", "no agent was started from this dir (or it logged out and removed agent.pid)")
	} else {
		add("rpc", "fail", fmt.Sprintf("agent pid %d is gone without a clean exit: game-side steps not run (its hires end after the server's offline grace)", pid))
		gameFailed = true
	}

	if *dryRun {
		// 3-7 become plan lines; nothing is changed.
		teardownPlanSteps(dir, manifest, pid, alive, *keepLease, add)
		return teardownReport(manifest, steps, failed, *asJSON, stdout)
	}

	// 3. agent_exit (only when it was alive).
	if alive {
		step, status, detail := teardownAgentExit(dir, pid)
		add(step, status, detail)
	}

	// 4. port.
	if teardownProbe.PortListening(manifest.Port) {
		add("port", "fail", fmt.Sprintf("port %d still has a listener (not this dir's agent: left alone)", manifest.Port))
	} else {
		add("port", "ok", fmt.Sprintf("port %d closed", manifest.Port))
	}

	// 5. other_pids.
	for _, s := range teardownOtherPIDs(dir) {
		add(s.Step, s.Status, s.Detail)
	}

	// 6. files: only the derived in-dir paths, never the manifest's strings.
	step, status, detail := teardownDeleteFiles(dir, manifest.Port)
	add(step, status, detail)

	// 7. lease.
	if *keepLease {
		add("lease", "skip", "kept (--keep-lease)")
	} else if gameFailed && !*releaseAnyway {
		add("lease", "fail", fmt.Sprintf("kept: game-side teardown failed; check the slot, then turtle-agent lease release %s %s --holder %s",
			manifest.Server, manifest.Slot, manifest.Holder))
	} else {
		step, status, detail := teardownReleaseLease(*slotsPath, manifest)
		add(step, status, detail)
	}

	return teardownReport(manifest, steps, failed, *asJSON, stdout)
}

func teardownPIDFilePresent(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, "agent.pid"))
	return err == nil
}

func teardownReport(manifest teardownSlotManifest, steps []teardownCLIStep, failed int, asJSON bool, stdout io.Writer) int {
	if asJSON {
		out := struct {
			Server string            `json:"server"`
			Slot   string            `json:"slot"`
			Steps  []teardownCLIStep `json:"steps"`
			Failed int               `json:"failed"`
		}{Server: manifest.Server, Slot: manifest.Slot, Steps: steps, Failed: failed}
		raw, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(stdout, string(raw))
	} else {
		fmt.Fprintf(stdout, "teardown %s/%s (%s, port %d):\n", manifest.Server, manifest.Slot, manifest.Character, manifest.Port)
		for _, s := range steps {
			tag := map[string]string{"ok": "ok", "skip": "skip", "fail": "FAIL", "plan": "plan"}[s.Status]
			if tag == "" {
				tag = s.Status
			}
			fmt.Fprintf(stdout, "%-4s %-14s %s\n", tag, s.Step, s.Detail)
		}
		ok, skipped := 0, 0
		for _, s := range steps {
			switch s.Status {
			case "ok":
				ok++
			case "skip", "plan":
				skipped++
			}
		}
		fmt.Fprintf(stdout, "teardown: %d ok, %d skipped, %d failed\n", ok, skipped, failed)
	}
	if failed > 0 {
		return exitTeardownFailed
	}
	return exitOK
}

// teardownRPCReply tolerates both the envelope {result:{steps,failed}} and a
// raw result {steps,failed}.
type teardownRPCReply struct {
	Result *struct {
		Steps  []teardownCLIStep `json:"steps"`
		Failed int               `json:"failed"`
	} `json:"result"`
	Steps []teardownCLIStep `json:"steps"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func teardownCallAgent(dir string, port int, tokenFile string, dryRun bool, gm string) ([]teardownCLIStep, error) {
	token, err := rpc.ReadTokenFile(tokenFile)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "teardown",
		"params":  map[string]any{"dry_run": dryRun, "gm": gm},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/rpc", port), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var reply teardownRPCReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if reply.Error != nil {
		return []teardownCLIStep{{Step: "rpc", Status: "fail", Detail: reply.Error.Message}}, nil
	}
	if reply.Result != nil {
		return reply.Result.Steps, nil
	}
	return reply.Steps, nil
}

// teardownAgentExit waits for the agent pid to end, then SIGTERM/SIGKILL.
// Each wait loop is bounded by iterations as well as time so a fake probe
// that always reports alive cannot hang tests (the sleeps are injected).
func teardownAgentExit(dir string, pid int) (string, string, string) {
	waitLoop := func(d time.Duration, poll time.Duration) bool {
		deadline := time.Now().Add(d)
		for i := 0; i < 1000 && time.Now().Before(deadline); i++ {
			if !teardownPIDAlive(dir, pid) {
				return true
			}
			teardownCLISleep(poll)
			if _, alive := teardownProbe.AgentPID(dir); !alive {
				return true
			}
		}
		return !teardownPIDAlive(dir, pid)
	}
	if waitLoop(20*time.Second, 500*time.Millisecond) {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	if !teardownPIDAlive(dir, pid) {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	// Re-check identity before signalling.
	if _, alive := teardownProbe.AgentPID(dir); !alive {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	if pid == os.Getpid() {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	_ = proc.Signal(syscall.SIGTERM)
	if waitLoop(40*time.Second, 500*time.Millisecond) {
		return "agent_exit", "ok", fmt.Sprintf("pid %d ended by SIGTERM", pid)
	}
	if !teardownPIDAlive(dir, pid) {
		return "agent_exit", "ok", fmt.Sprintf("pid %d ended by SIGTERM", pid)
	}
	_ = proc.Signal(syscall.SIGKILL)
	if waitLoop(5*time.Second, 500*time.Millisecond) {
		return "agent_exit", "fail", fmt.Sprintf("pid %d killed (SIGKILL): the character may stay in world", pid)
	}
	return "agent_exit", "fail", fmt.Sprintf("pid %d killed (SIGKILL): the character may stay in world", pid)
}

func teardownPIDAlive(dir string, pid int) bool {
	got, alive := teardownProbe.AgentPID(dir)
	return alive && got == pid
}

// teardownDeleteFiles removes settings.json, the derived token file and
// agent.pid. All paths are derived inside dir (the manifest was asserted by
// teardownCheckManifest); a token_file outside the dir never reaches Remove.
func teardownDeleteFiles(dir string, port int) (string, string, string) {
	type gone struct {
		name string
		path string
	}
	token := fmt.Sprintf("rpc-%d.token", port)
	targets := []gone{
		{"settings.json", filepath.Join(dir, "settings.json")},
		{token, filepath.Join(dir, token)},
		{"agent.pid", filepath.Join(dir, "agent.pid")},
	}
	var deleted, missing []string
	for _, t := range targets {
		if err := os.Remove(t.path); err == nil {
			deleted = append(deleted, t.name)
		} else {
			missing = append(missing, t.name+" (already gone)")
		}
	}
	detail := "deleted " + strings.Join(append(deleted, missing...), ", ")
	return "files", "ok", detail
}

func teardownReleaseLease(slotsPath string, manifest teardownSlotManifest) (string, string, string) {
	path := slotsPath
	if path == "" {
		path = slots.DefaultPath()
	}
	cfg, err := slots.Load(path)
	if err != nil {
		return "lease", "fail", err.Error()
	}
	store := slots.NewStore(cfg, path)
	if _, err := store.Release(manifest.Server, manifest.Slot, manifest.Holder, manifest.LeaseID, false); err != nil {
		if r, ok := err.(*slots.Refusal); ok {
			return "lease", "skip", fmt.Sprintf("not leased by %s any more (%s)", manifest.Holder, r.Msg)
		}
		return "lease", "fail", err.Error()
	}
	return "lease", "ok", fmt.Sprintf("released %s/%s", manifest.Server, manifest.Slot)
}

// teardownPlanSteps appends plan lines for steps 3-7 during --dry-run.
func teardownPlanSteps(dir string, manifest teardownSlotManifest, pid int, alive, keepLease bool, add func(string, string, string)) {
	if alive {
		add("agent_exit", "plan", fmt.Sprintf("would wait for pid %d to exit", pid))
	} else {
		add("agent_exit", "plan", "no agent running")
	}
	if teardownProbe.PortListening(manifest.Port) {
		add("port", "plan", fmt.Sprintf("would check port %d", manifest.Port))
	} else {
		add("port", "plan", fmt.Sprintf("port %d closed", manifest.Port))
	}
	add("other_pids", "plan", "would end stray pids of this dir")
	add("files", "plan", "would delete settings.json, token file and agent.pid")
	if keepLease {
		add("lease", "plan", "would keep the lease (--keep-lease)")
	} else {
		add("lease", "plan", fmt.Sprintf("would release %s/%s", manifest.Server, manifest.Slot))
	}
}
