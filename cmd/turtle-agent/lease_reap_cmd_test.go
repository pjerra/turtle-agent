package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/slots"
)

// deadProcessPID runs a short command and returns the pid it had, now reaped.
func deadProcessPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// hostName is the name a lease records for this machine.
func hostName(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil || h == "" {
		t.Skip("no hostname")
	}
	return h
}

// writeLeaseFile writes one lease file into the store of slotsPath.
func writeLeaseFile(t *testing.T, slotsPath string, l slots.Lease) string {
	t.Helper()
	dir := filepath.Join(slots.LeasesDir(slotsPath), l.Server)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, l.Slot+".json")
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// walk 47's fix: `lease reap --dry-run` reports the dead holder and removes nothing; the real run removes it and
// leaves a live holder's lease alone.
func TestLeaseReapDryRunThenRemoves(t *testing.T) {
	slotsPath := leaseTestSlots(t)
	now := time.Now().UTC()
	deadPath := writeLeaseFile(t, slotsPath, slots.Lease{
		Server: "m910q", Slot: "1", Holder: "w-dead", LeaseID: "dead-id",
		Acquired: now, Expires: now.Add(time.Hour), Renewed: now,
		HolderPID: deadProcessPID(t), HolderHost: hostName(t),
	})
	livePath := writeLeaseFile(t, slotsPath, slots.Lease{
		Server: "m910q", Slot: "2", Holder: "w-live", LeaseID: "live-id",
		Acquired: now, Expires: now.Add(time.Hour), Renewed: now,
		HolderPID: os.Getpid(), HolderHost: hostName(t),
	})

	code, out, errOut := runCLI("lease", "reap", "--dry-run", "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("dry run: code %d stderr %q", code, errOut)
	}
	if !strings.Contains(out, "would reap m910q/1 held by w-dead") {
		t.Fatalf("dry run stdout %q", out)
	}
	if strings.Contains(out, "m910q/2") {
		t.Fatalf("dry run named the live holder: %q", out)
	}
	for _, p := range []string{deadPath, livePath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("the dry run removed %s: %v", p, err)
		}
	}

	code, out, errOut = runCLI("lease", "reap", "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("reap: code %d stderr %q", code, errOut)
	}
	if !strings.Contains(out, "reaped m910q/1 held by w-dead") {
		t.Fatalf("reap stdout %q", out)
	}
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Fatalf("the dead holder's lease is still there: %v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("the live holder's lease was removed: %v", err)
	}
}

// The heartbeat: a walk takes a short lease and renews it while it runs; a transient renewal failure is logged and
// retried, never dropped, and a refusal (the lease is no longer ours) stops the walk.
func TestRaidWalkLeaseHeartbeatRetriesAndNeverDrops(t *testing.T) {
	oldRenew, oldRetry := walkLeaseRenew, walkLeaseRenewRetry
	walkLeaseRenew, walkLeaseRenewRetry = 2*time.Millisecond, time.Millisecond
	t.Cleanup(func() { walkLeaseRenew, walkLeaseRenewRetry = oldRenew, oldRetry })

	var mu sync.Mutex
	calls := 0
	var ttl time.Duration
	env := walkEnv{
		now: time.Now,
		acquire: func(server, slot string, any bool, holder string, got time.Duration) (slots.Acquired, error) {
			ttl = got
			return slots.Acquired{Lease: slots.Lease{Server: server, Slot: "1", Holder: holder, LeaseID: "lid"},
				Slot: slots.Slot{ID: "1"}}, nil
		},
		renew: func(server, slot, holder, leaseID string, got time.Duration) (slots.Lease, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls <= 2 {
				return slots.Lease{}, errors.New("flock: timeout")
			}
			return slots.Lease{}, nil
		},
	}
	sb := &syncBuffer{}
	w := &walk{opts: raidWalkOptions{server: "m910q", slot: "1", holder: "w-a"}, env: env,
		stdout: io.Discard, stderr: sb}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.cancel = cancel
	if err := w.phaseLease(ctx); err != nil {
		t.Fatal(err)
	}
	if ttl != 15*time.Minute {
		t.Fatalf("the walk leased for %v, want the short 15m", ttl)
	}
	waitFor(t, "three renew attempts", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls >= 3
	})
	if w.leaseLost.Load() {
		t.Fatal("a transient renewal failure lost the lease")
	}
	if !strings.Contains(sb.String(), "retrying") || !strings.Contains(sb.String(), "flock: timeout") {
		t.Fatalf("the renewal failure was not loud: %q", sb.String())
	}
}

func TestRaidWalkLeaseRefusalStopsTheWalk(t *testing.T) {
	oldRenew, oldRetry := walkLeaseRenew, walkLeaseRenewRetry
	walkLeaseRenew, walkLeaseRenewRetry = 2*time.Millisecond, time.Millisecond
	t.Cleanup(func() { walkLeaseRenew, walkLeaseRenewRetry = oldRenew, oldRetry })

	env := walkEnv{
		now: time.Now,
		acquire: func(server, slot string, any bool, holder string, got time.Duration) (slots.Acquired, error) {
			return slots.Acquired{Lease: slots.Lease{Server: server, Slot: "1", Holder: holder, LeaseID: "lid"},
				Slot: slots.Slot{ID: "1"}}, nil
		},
		renew: func(server, slot, holder, leaseID string, got time.Duration) (slots.Lease, error) {
			return slots.Lease{}, &slots.Refusal{Msg: "leased to someone else"}
		},
	}
	sb := &syncBuffer{}
	w := &walk{opts: raidWalkOptions{server: "m910q", slot: "1", holder: "w-a"}, env: env,
		stdout: io.Discard, stderr: sb}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.cancel = cancel
	if err := w.phaseLease(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the refusal to stop the walk", func() bool { return w.leaseLost.Load() })
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a refused renewal did not cancel the walk")
	}
	if !strings.Contains(sb.String(), "not ours") {
		t.Fatalf("the refusal was not loud: %q", sb.String())
	}
}

// raid-autopilot frees the dead holder's lease at start, before its reconciliation reads the slot.
func TestAutoStartReapsADeadHoldersLease(t *testing.T) {
	f := newAutoFixture(t)
	f.lease("w-dead", "held")
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql --slots " + f.slotsPath)
	dead := deadProcessPID(t)
	now := time.Now().UTC()
	path := writeLeaseFile(t, f.slotsPath, slots.Lease{
		Server: "srv", Slot: "1", Holder: "w-dead", LeaseID: "dead-id",
		Acquired: now, Expires: now.Add(time.Hour), Renewed: now,
		HolderPID: dead, HolderHost: hostName(t),
	})
	if code := f.run(); code != exitOK {
		t.Fatalf("exit %d, want %d (a refused id is not an escalation):\n%s", code, exitOK, f.events())
	}
	if ev := f.events(); !strings.Contains(ev, "reap: freed srv/1") {
		t.Fatalf("the start did not reap the dead holder's lease:\n%s", ev)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the dead holder's lease is still there: %v", err)
	}
}
