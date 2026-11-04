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
	// a1's oldest entry (50 min old) leaves in 10 min, then the busiest account is below the cap.
	if len(slept) != 1 || slept[0] != 10*time.Minute+capWaitMargin {
		t.Fatalf("slept %v, want one wait of 10m + margin", slept)
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
