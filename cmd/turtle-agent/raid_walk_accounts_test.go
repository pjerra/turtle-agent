package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

func TestParsePInfoLine(t *testing.T) {
	name, account, ok := parsePInfoLine("Human Warrior |cffffffff|Hplayer:Tankbot|h[Tankbot]|h|r (guid: 42) Account: |cffffffff|Hplayer:<redacted>|h[<redacted>]|h|r (id: 17, banned) GMLevel: 3 Last IP: -")
	if !ok || name != "Tankbot" || account != "17" {
		t.Fatalf("parsed %q %q %v, want Tankbot 17 true", name, account, ok)
	}
	if _, _, ok := parsePInfoLine("You have sent 0 gold."); ok {
		t.Fatal("a non-pinfo line parsed as one")
	}
	if _, _, ok := parsePInfoLine("There is no such player."); ok {
		t.Fatal("the no-player line parsed as one")
	}
}

func TestInstanceCapHiredAccountsGate(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/" + raidwalk.InstanceCapFile
	_, err := raidwalk.UpdateCapLedger(path, func(l *raidwalk.CapLedger) {
		for i := 0; i < 5; i++ {
			l.Record("m910q", raidwalk.CapEntry{At: now.Add(-time.Duration(50-10*i) * time.Minute), Accounts: []string{"a1"}}, now)
		}
		l.Record("m910q", raidwalk.CapEntry{At: now.Add(-time.Minute), Accounts: []string{"a2"}}, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	var slept []time.Duration
	var stderr strings.Builder
	w := &walk{opts: raidWalkOptions{server: "m910q", capWait: 30 * time.Minute}, stderr: &stderr, hireAccounts: []string{"a1", "a2"}}
	w.env = walkEnv{
		now:       func() time.Time { return now },
		sleep:     func(_ context.Context, d time.Duration) error { slept = append(slept, d); now = now.Add(d); return nil },
		capLedger: path,
	}
	if err := w.waitHireAccountCap(context.Background()); err != nil {
		t.Fatalf("waitHireAccountCap: %v", err)
	}
	// The gate keeps one place of headroom (RaidLimit(5) = 4): a1's second-oldest entry (40 min old) leaves in 20 min,
	// and only then is the busiest account below 4.
	if len(slept) != 1 || slept[0] != 20*time.Minute+capWaitMargin {
		t.Fatalf("slept %v, want one wait of 20m + margin", slept)
	}
	if !strings.Contains(stderr.String(), "a hired account already has 5 fresh instances") {
		t.Fatalf("no cap warning: %q", stderr.String())
	}
}

func TestRecordFreshInstanceNamesTheHiredAccounts(t *testing.T) {
	w, now, _, _ := capWalk(t, raidWalkOptions{})
	w.capReserved = true
	w.hireAccounts = []string{"17", "23"}
	w.recordFreshInstance()
	l, err := raidwalk.LoadCapLedger(w.env.capLedger)
	if err != nil {
		t.Fatal(err)
	}
	live := l.Servers["m910q"].Entries
	if len(live) != 1 || strings.Join(live[0].Accounts, ",") != "17,23" {
		t.Fatalf("entries %+v, want one naming 17 and 23", live)
	}
	if n, known := l.PeakFor("m910q", []string{"17"}, *now); !known || n != 1 {
		t.Fatalf("peak for 17 = %d known %v, want 1 true", n, known)
	}
}

// M5: a --reuse-instance placement re-hires into the instance the raid already has. The core counts that instance id
// once per account, so the ledger entry names only the accounts that were not recorded for it yet.
func TestReusePlacementRecordsOnlyTheNewAccounts(t *testing.T) {
	w, now, _, _ := capWalk(t, raidWalkOptions{})
	w.placeInst = 101
	w.capReserved = true
	w.hireAccounts = []string{"17", "23"}
	w.recordFreshInstance()
	w.stampInstance()
	l, err := raidwalk.LoadCapLedger(w.env.capLedger)
	if err != nil {
		t.Fatal(err)
	}
	if e := l.Servers["m910q"].Entries[0]; e.Instance != 101 {
		t.Fatalf("fresh entry instance = %v, want 101 (stamped after the placement)", e.Instance)
	}
	// The next reuse placement re-hires 23 and a new account 29: only 29 is a new (account, instance) pair.
	w.hireAccounts = []string{"23", "29"}
	w.recordReuseInstance()
	l, err = raidwalk.LoadCapLedger(w.env.capLedger)
	if err != nil {
		t.Fatal(err)
	}
	if n, known := l.PeakFor("m910q", []string{"23"}, *now); !known || n != 1 {
		t.Fatalf("account 23 counted %d known %v, want 1 true (already in instance 101)", n, known)
	}
	if n, known := l.PeakFor("m910q", []string{"29"}, *now); !known || n != 1 {
		t.Fatalf("account 29 counted %d known %v, want 1 true (new in instance 101)", n, known)
	}
	if !strings.Contains(w.notes[len(w.notes)-1], "1 newly hired account(s) counted for the reused instance 101") {
		t.Fatalf("notes %v, want the reuse entry line", w.notes)
	}
}
