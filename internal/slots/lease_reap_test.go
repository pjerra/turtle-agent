//go:build linux

package slots

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// finishedPID runs a short command and returns the pid it had, now reaped: nothing answers for it.
func finishedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// walk 47: a walk renews a SHORT lease every few minutes, and a live holder keeps its slot across the original expiry.
func TestRenewKeepsALiveHoldersShortLease(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	st.HolderPID = os.Getpid()
	base := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	acq, err := st.Acquire("m910q", "1", false, "w-a", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if acq.Lease.HolderPID != os.Getpid() {
		t.Fatalf("holder pid = %d, want %d", acq.Lease.HolderPID, os.Getpid())
	}
	if acq.Lease.HolderHost == "" {
		t.Fatal("the lease records no holder host")
	}
	if acq.Lease.HolderStart == 0 {
		t.Fatal("the lease records no holder start time")
	}
	if !acq.Lease.Renewed.Equal(base) {
		t.Fatalf("renewed = %v, want the acquire time %v", acq.Lease.Renewed, base)
	}
	// 10 minutes in, before the 15-minute expiry, the holder renews.
	setNow(st, base.Add(10*time.Minute))
	ren, err := st.Renew("m910q", "1", "w-a", acq.Lease.LeaseID, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ren.Expires.Equal(base.Add(25 * time.Minute)) {
		t.Fatalf("renewed expiry = %v, want %v", ren.Expires, base.Add(25*time.Minute))
	}
	if !ren.Renewed.Equal(base.Add(10 * time.Minute)) {
		t.Fatalf("renewed = %v, want %v", ren.Renewed, base.Add(10*time.Minute))
	}
	// Past the ORIGINAL expiry but inside the renewed one, the slot is still the live holder's.
	setNow(st, base.Add(20*time.Minute))
	if _, err := st.Acquire("m910q", "1", false, "w-b", time.Hour); err == nil ||
		!strings.Contains(err.Error(), "held by w-a") {
		t.Fatalf("a renewed live lease: got %v, want a held refusal", err)
	}
}

// walk 47: the walk's process died with the laptop. The slot is free at once, not after the TTL (up to 720 min).
func TestDeadHolderFreesTheSlotBeforeExpiry(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	st.HolderPID = finishedPID(t)
	acq, err := st.Acquire("m910q", "1", false, "w-dead", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if acq.Lease.Expires.Before(st.Now().Add(59 * time.Minute)) {
		t.Fatalf("the fixture lease is not long: %v", acq.Lease.Expires)
	}
	if !st.IsStale(&acq.Lease) {
		t.Fatal("a lease whose recorded holder is gone is not stale")
	}
	states, err := st.List("m910q")
	if err != nil {
		t.Fatal(err)
	}
	if states[0].State != "stale" {
		t.Fatalf("slot 1 state = %q, want stale", states[0].State)
	}
	got, err := st.Acquire("m910q", "1", false, "w-b", time.Hour)
	if err != nil {
		t.Fatalf("a dead holder did not free the slot: %v", err)
	}
	if got.TookOver == nil || got.Lease.TakenOverFrom != "w-dead" {
		t.Fatalf("takeover = %+v, want TakenOverFrom w-dead", got.Lease)
	}
}

// A recycled pid is not the holder: the recorded start time differs, so the lease is dead although the pid is alive.
func TestReusedHolderPIDCountsAsDead(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	st.HolderPID = os.Getpid()
	acq, err := st.Acquire("m910q", "1", false, "w-a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if acq.Lease.HolderStart == 0 {
		t.Fatal("the fixture lease records no start time")
	}
	old := holderProc
	holderProc = func(pid int) (uint64, bool, bool) { return acq.Lease.HolderStart + 7, true, true }
	defer func() { holderProc = old }()
	if !st.IsStale(&acq.Lease) {
		t.Fatal("a reused pid (another start time) is not stale")
	}
	if _, err := st.Acquire("m910q", "1", false, "w-b", time.Hour); err != nil {
		t.Fatalf("a reused holder pid did not free the slot: %v", err)
	}
}

// Another host's lease is never freed by a pid check: that pid means nothing here, and the expiry decides.
func TestOtherHostLeaseIsNotReapedByPID(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	st.HolderPID = finishedPID(t)
	if _, err := st.Acquire("m910q", "1", false, "w-dead", time.Hour); err != nil {
		t.Fatal(err)
	}
	l, err := st.readLease("m910q", "1")
	if err != nil || l == nil {
		t.Fatalf("read lease: %v", err)
	}
	l.HolderHost = "another-host"
	if err := st.writeLease(l); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Acquire("m910q", "1", false, "w-b", time.Hour); err == nil {
		t.Fatal("a lease of another host was taken over before its expiry")
	}
	setNow(st, st.Now().Add(2*time.Hour))
	if _, err := st.Acquire("m910q", "1", false, "w-b", time.Hour); err != nil {
		t.Fatalf("an expired lease of another host was not taken over: %v", err)
	}
}

// A lease written before the holder fields existed keeps the old rule: expiry AND a dead agent.
func TestOldFormatLeaseStillWorks(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	now := st.Now()
	old := Lease{Server: "m910q", Slot: "1", Holder: "w-old", LeaseID: "old-id",
		Acquired: now, Expires: now.Add(time.Hour)}
	if err := st.writeLease(&old); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Acquire("m910q", "1", false, "w-b", time.Hour); err == nil ||
		!strings.Contains(err.Error(), "held by w-old") {
		t.Fatalf("old-format lease before expiry: got %v, want a held refusal", err)
	}
	setNow(st, now.Add(2*time.Hour))
	if _, err := st.Acquire("m910q", "1", false, "w-b", time.Hour); err != nil {
		t.Fatalf("old-format lease after expiry: %v", err)
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "holder_") || strings.Contains(string(raw), "renewed") {
		t.Fatalf("an old-format lease gained the new fields: %s", raw)
	}
}

// Reap removes only the dead local holders; a dry run reports them and removes nothing.
func TestReapDeadLeases(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	now := st.Now()
	dead := finishedPID(t)
	st.HolderPID = dead
	if _, err := st.Acquire("m910q", "1", false, "w-dead", time.Hour); err != nil {
		t.Fatal(err)
	}
	st.HolderPID = os.Getpid()
	if _, err := st.Acquire("m910q", "2", false, "w-live", time.Hour); err != nil {
		t.Fatal(err)
	}
	other := Lease{Server: "m910q", Slot: "9", Holder: "w-other", LeaseID: "other-id",
		Acquired: now, Expires: now.Add(time.Hour), HolderPID: dead, HolderHost: "another-host"}
	if err := st.writeLease(&other); err != nil {
		t.Fatal(err)
	}

	got, err := ReapDeadLeases(st.Dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Server != "m910q" || got[0].Slot != "1" || got[0].Holder != "w-dead" {
		t.Fatalf("dry run reported %+v, want only m910q/1 w-dead", got)
	}
	if _, err := os.Stat(st.leasePath("m910q", "1")); err != nil {
		t.Fatalf("the dry run removed the lease: %v", err)
	}

	got, err = ReapDeadLeases(st.Dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Slot != "1" {
		t.Fatalf("reap removed %+v, want only m910q/1", got)
	}
	if _, err := os.Stat(st.leasePath("m910q", "1")); !os.IsNotExist(err) {
		t.Fatalf("the dead holder's lease file is still there: %v", err)
	}
	for _, slot := range []string{"2", "9"} {
		if _, err := os.Stat(st.leasePath("m910q", slot)); err != nil {
			t.Fatalf("reap removed slot %s's lease: %v", slot, err)
		}
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "m910q", "1.json")); !os.IsNotExist(err) {
		t.Fatalf("lease file still present: %v", err)
	}
}

// Codex HIGH: an unreadable /proc/<pid>/stat is NOT a dead holder. Under hidepid, or for another user's process,
// the read fails while the process runs; only ESRCH (kill 0) says the pid is gone.
func TestUnreadableHolderStatIsAlive(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	st.HolderPID = os.Getpid()
	old := readProcStat
	readProcStat = func(pid int) ([]byte, error) { return nil, fs.ErrPermission }
	defer func() { readProcStat = old }()
	if _, err := st.Acquire("m910q", "1", false, "w-a", time.Hour); err != nil {
		t.Fatal(err)
	}
	l, err := st.readLease("m910q", "1")
	if err != nil || l == nil {
		t.Fatalf("read lease: %v", err)
	}
	if l.HolderNS == "" {
		t.Fatal("the lease records no pid namespace")
	}
	if st.IsStale(l) {
		t.Fatal("a live holder whose stat is unreadable (hidepid, another user) was called dead")
	}
	got, err := ReapDeadLeases(st.Dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("reap removed %+v, want nothing", got)
	}
}

// The same unreadable stat with a pid that is gone (kill 0 -> ESRCH) still frees the slot.
func TestUnreadableHolderStatWithESRCHIsDead(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	st.HolderPID = finishedPID(t)
	old := readProcStat
	readProcStat = func(pid int) ([]byte, error) { return nil, fs.ErrPermission }
	defer func() { readProcStat = old }()
	if _, err := st.Acquire("m910q", "1", false, "w-dead", time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := ReapDeadLeases(st.Dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Slot != "1" {
		t.Fatalf("reap removed %+v, want m910q/1", got)
	}
}

// Opus hardening: every WSL distro reports the same hostname, so the pid namespace must match too. A lease from
// another namespace (or one written before the field existed) is never reaped by pid, only by its expiry.
func TestLeaseNeedsTheSamePIDNamespaceToReap(t *testing.T) {
	for _, tc := range []struct {
		name string
		ns   string
	}{
		{"another namespace", "pid:[1]"},
		{"round-1 lease without a namespace", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testStore(t, twoSlotCfg)
			st.HolderPID = finishedPID(t)
			if _, err := st.Acquire("m910q", "1", false, "w-dead", time.Hour); err != nil {
				t.Fatal(err)
			}
			l, err := st.readLease("m910q", "1")
			if err != nil || l == nil {
				t.Fatalf("read lease: %v", err)
			}
			if l.HolderNS == "" {
				t.Fatal("the acquired lease records no pid namespace")
			}
			l.HolderNS = tc.ns
			if err := st.writeLease(l); err != nil {
				t.Fatal(err)
			}
			if st.IsStale(l) {
				t.Fatal("a lease with a dead pid but another pid namespace was called stale")
			}
			got, err := ReapDeadLeases(st.Dir, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("reap named %+v, want nothing", got)
			}
			setNow(st, st.Now().Add(2*time.Hour))
			if _, err := st.Acquire("m910q", "1", false, "w-b", time.Hour); err != nil {
				t.Fatalf("the expired lease was not taken over: %v", err)
			}
		})
	}
}
