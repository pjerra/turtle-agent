package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/slots"
)

func slotSetupTestEnv(t *testing.T) (slotsPath, srcPath, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "turtle-agent")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	srcPath = filepath.Join(cfgDir, "settings-src.json")
	body := `{"realmd":"127.0.0.1:3724","rpc":"127.0.0.1:0","accounts":{"TESTACC":"pw-not-real"}}`
	if err := os.WriteFile(srcPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	table := `{"version":1,"servers":[{"name":"m910q","realmd":"100.78.24.50:3724","slots":[
		{"id":"1","account":"TESTACC","character":"Gbmage","other_characters":["Gbrother"],"port":28771,"settings_from":"SRC"},
		{"id":"3","reserved":"owner rule: never slot 3 on m910q"}]}]}`
	table = strings.ReplaceAll(table, "SRC", srcPath)
	slotsPath = filepath.Join(cfgDir, "slots.json")
	if err := os.WriteFile(slotsPath, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	return slotsPath, srcPath, home
}

func slotSetupAcquire(t *testing.T, slotsPath, holder string) {
	t.Helper()
	if code, _, errOut := runCLI("lease", "acquire", "m910q", "--slot", "1", "--holder", holder, "--slots", slotsPath); code != exitOK {
		t.Fatalf("acquire: %d %q", code, errOut)
	}
}

func TestSlotSetupPrintsNoSecret(t *testing.T) {
	slotsPath, _, _ := slotSetupTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	bin := filepath.Join(t.TempDir(), "turtle-agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "client")
	code, out, errOut := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if strings.Contains(out+errOut, "pw-not-real") {
		t.Fatalf("output leaks the password: stdout %q stderr %q", out, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 lines, got %q", out)
	}
	if lines[0] != "slot m910q/1 ready in "+dir {
		t.Fatalf("line 1: %q", lines[0])
	}
	if lines[1] != "account TESTACC, character Gbmage, port 28771" {
		t.Fatalf("line 2: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "start: nohup "+dir+"/turtle-agent login --config "+dir+"/settings.json --pid-file "+dir+"/agent.pid Gbmage > "+dir+"/agent.out 2>&1 &") {
		t.Fatalf("line 3: %q", lines[2])
	}
	if lines[3] != "rpc: 127.0.0.1:28771, token file "+dir+"/rpc-28771.token (written at login)" {
		t.Fatalf("line 4: %q", lines[3])
	}
	if lines[4] != "teardown: "+dir+"/turtle-agent teardown "+dir {
		t.Fatalf("line 5: %q", lines[4])
	}
	info, err := os.Stat(filepath.Join(dir, "settings.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings.json mode: %v %v", info, err)
	}
	settings, err := config.Load(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("load back: %v", err)
	}
	if len(settings.Accounts) != 1 {
		t.Fatalf("accounts: %d", len(settings.Accounts))
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if strings.Contains(string(raw), "rpc_token") || strings.Contains(string(raw), "pw-not-real") && len(settings.Accounts) != 1 {
		t.Fatalf("settings.json has a token leak")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["rpc_token"]; ok {
		t.Fatalf("settings.json copies rpc_token")
	}
	if st, err := os.Stat(filepath.Join(dir, "turtle-agent")); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode: %v %v", st, err)
	}
}

func TestSlotSetupNeedsTheLease(t *testing.T) {
	slotsPath, _, _ := slotSetupTestEnv(t)
	bin := filepath.Join(t.TempDir(), "turtle-agent")
	os.WriteFile(bin, []byte("x"), 0o755)
	dir := filepath.Join(t.TempDir(), "client")
	code, _, errOut := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath)
	if code != exitSlotRefused || !strings.Contains(errOut, "slot setup: m910q/1 is not leased by w-a") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	slotSetupAcquire(t, slotsPath, "w-zf")
	code, _, errOut = runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir+"-other", "--binary", bin, "--slots", slotsPath)
	if code != exitSlotRefused {
		t.Fatalf("other holder: code %d stderr %q", code, errOut)
	}
}

func TestSlotSetupNeverOverwrites(t *testing.T) {
	slotsPath, _, _ := slotSetupTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	bin := filepath.Join(t.TempDir(), "turtle-agent")
	os.WriteFile(bin, []byte("x"), 0o755)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "keep.txt")
	os.WriteFile(sentinel, []byte("keep"), 0o600)
	code, _, errOut := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath)
	if code != exitUsage || !strings.Contains(errOut, "exists and is not empty") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	raw, _ := os.ReadFile(sentinel)
	if string(raw) != "keep" {
		t.Fatalf("sentinel touched")
	}
}

func TestSlotSetupSetsStateDir(t *testing.T) {
	slotsPath, srcPath, _ := slotSetupTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	bin := filepath.Join(t.TempDir(), "turtle-agent")
	os.WriteFile(bin, []byte("x"), 0o755)
	dir := filepath.Join(t.TempDir(), "client")
	code, _, errOut := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["state_dir"] != filepath.Dir(srcPath) {
		t.Fatalf("state_dir = %v, want %s", m["state_dir"], filepath.Dir(srcPath))
	}
}

// Item 4 (R2): a takeover between the lease check and the write leaves no
// credentials behind: exit 15, no settings.json, dir cleaned.
func TestSlotSetupTakeoverBeforeWriteLeavesNothing(t *testing.T) {
	slotsPath, _, _ := slotSetupTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	bin := filepath.Join(t.TempDir(), "turtle-agent")
	os.WriteFile(bin, []byte("x"), 0o755)
	dir := filepath.Join(t.TempDir(), "client")
	oldHook := slotSetupPreWriteHook
	slotSetupPreWriteHook = func() {
		// Another holder takes the slot over after setup checked the lease.
		cfg, err := slots.Load(slotsPath)
		if err != nil {
			panic(err)
		}
		st := slots.NewStore(cfg, slotsPath)
		cur, err := st.Get("m910q", "1")
		if err != nil {
			panic(err)
		}
		if _, err := st.Release("m910q", "1", cur.Holder, cur.LeaseID, true); err != nil {
			panic(err)
		}
		if _, err := st.Acquire("m910q", "1", false, "w-zf", 3*time.Hour); err != nil {
			panic(err)
		}
	}
	defer func() { slotSetupPreWriteHook = oldHook }()
	code, _, errOut := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath)
	if code != exitSlotRefused {
		t.Fatalf("takeover: code %d stderr %q, want %d", code, errOut, exitSlotRefused)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("settings.json left behind after a lost lease")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dir not cleaned: %v", entries)
	}
}

// Item 4 (R2): a failure midway (here: an unreadable binary) cleans the dir.
func TestSlotSetupFailureMidwayCleansDir(t *testing.T) {
	slotsPath, _, _ := slotSetupTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	dir := filepath.Join(t.TempDir(), "client")
	code, _, _ := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir,
		"--binary", filepath.Join(t.TempDir(), "no-such-binary"), "--slots", slotsPath)
	if code != exitUsage {
		t.Fatalf("code %d, want %d", code, exitUsage)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dir not cleaned: %v", entries)
	}
}

// Item 1 (R3): a pre-existing caller-supplied --dungeons-dir is never
// removed by cleanup: a forced late failure leaves the sentinel intact.
func TestSlotSetupExternalDungeonsSurvivesFailure(t *testing.T) {
	slotsPath, _, _ := slotSetupTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	bin := filepath.Join(t.TempDir(), "turtle-agent")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "shared-dungeons")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(external, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "client")
	oldHook := slotSetupPreWriteHook
	slotSetupPreWriteHook = func() {
		cfg, err := slots.Load(slotsPath)
		if err != nil {
			panic(err)
		}
		st := slots.NewStore(cfg, slotsPath)
		cur, err := st.Get("m910q", "1")
		if err != nil {
			panic(err)
		}
		if _, err := st.Release("m910q", "1", cur.Holder, cur.LeaseID, true); err != nil {
			panic(err)
		}
		if _, err := st.Acquire("m910q", "1", false, "w-zf", 3*time.Hour); err != nil {
			panic(err)
		}
	}
	defer func() { slotSetupPreWriteHook = oldHook }()
	code, _, _ := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--dungeons-dir", external, "--slots", slotsPath)
	if code != exitSlotRefused {
		t.Fatalf("code %d, want %d", code, exitSlotRefused)
	}
	raw, err := os.ReadFile(sentinel)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("external --dungeons-dir touched: %q %v", raw, err)
	}
}

// Item 2 (R3): a --dir that is a symlink is refused before anything is
// written through it.
func TestSlotSetupRefusesSymlinkDir(t *testing.T) {
	slotsPath, _, _ := slotSetupTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	bin := filepath.Join(t.TempDir(), "turtle-agent")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", link, "--binary", bin, "--slots", slotsPath)
	if code != exitUsage || !strings.Contains(errOut, "symlink") {
		t.Fatalf("code %d stderr %q, want a symlink refusal", code, errOut)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote through the symlink: %v", entries)
	}
}

// Item 2 (R3): the dir is re-checked right before settings.json is
// written: a swap to a symlink refuses and writes nothing in the target.
func TestSlotSetupDirSwapBeforeWriteRefused(t *testing.T) {
	slotsPath, _, _ := slotSetupTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	bin := filepath.Join(t.TempDir(), "turtle-agent")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	dir := filepath.Join(base, "client")
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	oldHook := slotSetupPreWriteHook
	slotSetupPreWriteHook = func() {
		if err := os.Rename(dir, dir+".bak"); err != nil {
			panic(err)
		}
		if err := os.Symlink(target, dir); err != nil {
			panic(err)
		}
	}
	defer func() { slotSetupPreWriteHook = oldHook }()
	code, _, errOut := runCLI("slot", "setup", "m910q", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath)
	if code != exitUsage || !strings.Contains(errOut, "replaced") {
		t.Fatalf("code %d stderr %q, want a replacement refusal", code, errOut)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("settings written through the swapped symlink: %v", entries)
	}
}
