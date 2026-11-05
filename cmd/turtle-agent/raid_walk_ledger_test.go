package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// Walk 38: a --slots file in another directory keeps its leases and its cap ledger there, so its walk cannot see the
// default ledger's live leases and two raids can take one slot. The walk refuses to start (exit 2) while the default
// leases hold a live lease for the same server; --allow-split-ledger is the test-sandbox override.
func TestRaidWalkRefusesSplitLeaseLedger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defDir := filepath.Join(home, ".config", "turtle-agent")
	if err := os.MkdirAll(filepath.Join(defDir, "leases", "m910q"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The default ledger's live lease: zg38-hl1 holds m910q slot 7 for another hour.
	live := fmt.Sprintf(`{"server":"m910q","slot":"7","holder":"zg38-hl1","lease_id":"0123456789abcdef0123456789abcdef","acquired":%q,"expires":%q}`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(defDir, "leases", "m910q", "7.json"), []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}
	// The split ledger the walk was told to use.
	second := t.TempDir()
	slotsBody := `{"version":1,"servers":[
		{"name":"m910q","realmd":"10.0.0.1:3724","slots":[
			{"id":"7","account":"A","character":"Gbmage","port":8771,"settings_from":"/s.json"}]},
		{"name":"tortoise-test2","realmd":"10.0.0.2:3724","slots":[
			{"id":"1","account":"A","character":"Gbmage","port":8772,"settings_from":"/s.json"}]}]}`
	secondSlots := filepath.Join(second, "slots-w38.json")
	if err := os.WriteFile(secondSlots, []byte(slotsBody), 0o600); err != nil {
		t.Fatal(err)
	}

	o := raidWalkOptions{slotsPath: secondSlots, server: "m910q"}
	if _, _, err := defaultWalkEnv(&o); err == nil || !strings.Contains(err.Error(), "one ledger per server") {
		t.Fatalf("a split lease ledger started: %v", err)
	} else {
		for _, want := range []string{"--slots " + secondSlots, "keeps its leases in " + filepath.Join(second, "leases"), "live leases for m910q exist in " + filepath.Join(defDir, "leases"), "zg38-hl1"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q does not name %q", err, want)
			}
		}
	}
	// The real command maps it to exit 2 before anything runs.
	var out, errOut bytes.Buffer
	code := raidWalkCmd(context.Background(), []string{"zul_gurub", "--slots", secondSlots, "--server", "m910q"}, &out, &errOut)
	if code != exitUsage {
		t.Fatalf("raid-walk exit %d, want %d (usage); stderr:\n%s", code, exitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), "one ledger per server") {
		t.Fatalf("the exit-2 message is missing:\n%s", errOut.String())
	}
	// --allow-split-ledger is the test-sandbox override.
	o = raidWalkOptions{slotsPath: secondSlots, server: "m910q", allowSplitLedger: true}
	if _, _, err := defaultWalkEnv(&o); err != nil {
		t.Fatalf("--allow-split-ledger was refused: %v", err)
	}
}

// The same split, but the default ledger holds no live lease for the server (a separate sandbox): the walk starts.
func TestRaidWalkSplitLeaseLedgerWithoutLiveDefaultLease(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defDir := filepath.Join(home, ".config", "turtle-agent")
	if err := os.MkdirAll(filepath.Join(defDir, "leases", "m910q"), 0o700); err != nil {
		t.Fatal(err)
	}
	// An expired lease with no live agent is stale: it does not count.
	stale := fmt.Sprintf(`{"server":"m910q","slot":"7","holder":"gone","lease_id":"0123456789abcdef0123456789abcdef","acquired":%q,"expires":%q,"dir":%q}`,
		time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339), time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), filepath.Join(home, "no-such-dir"))
	if err := os.WriteFile(filepath.Join(defDir, "leases", "m910q", "7.json"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	second := t.TempDir()
	slotsBody := `{"version":1,"servers":[
		{"name":"m910q","realmd":"10.0.0.1:3724","slots":[
			{"id":"7","account":"A","character":"Gbmage","port":8771,"settings_from":"/s.json"}]}]}`
	secondSlots := filepath.Join(second, "slots-w38.json")
	if err := os.WriteFile(secondSlots, []byte(slotsBody), 0o600); err != nil {
		t.Fatal(err)
	}
	o := raidWalkOptions{slotsPath: secondSlots, server: "m910q"}
	env, _, err := defaultWalkEnv(&o)
	if err != nil {
		t.Fatalf("a stale default lease refused the split ledger: %v", err)
	}
	// The split ledger's own paths are used (the cap file beside it, and the stock 5 without one).
	if want := filepath.Join(second, raidwalk.InstanceCapFile); env.capLedger != want {
		t.Fatalf("capLedger = %q, want %q", env.capLedger, want)
	}
	if got := env.capPerAccount; got != raidwalk.InstanceCapPerAccount {
		t.Fatalf("cap = %d, want the stock %d", got, raidwalk.InstanceCapPerAccount)
	}
}
