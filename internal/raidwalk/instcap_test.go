package raidwalk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var capT0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func capLedgerWith(server string, ages ...time.Duration) *CapLedger {
	l := &CapLedger{}
	for _, a := range ages {
		l.Record(server, CapEntry{At: capT0.Add(-a), Plan: "zg"}, capT0)
	}
	return l
}

func TestCapCountsPerServerInTheLastHour(t *testing.T) {
	l := capLedgerWith("m910q", 5*time.Minute, 20*time.Minute, 59*time.Minute)
	l.Record("tortoise-test2", CapEntry{At: capT0}, capT0)
	if n := l.Count("m910q", capT0); n != 3 {
		t.Fatalf("m910q count = %d, want 3", n)
	}
	if n := l.Count("tortoise-test2", capT0); n != 1 {
		t.Fatalf("test2 count = %d, want 1", n)
	}
	if n := l.Count("nowhere", capT0); n != 0 {
		t.Fatalf("unknown server count = %d", n)
	}
}

func TestCapWindowAges(t *testing.T) {
	l := capLedgerWith("m910q", 59*time.Minute, 30*time.Minute, 10*time.Minute, 1*time.Minute)
	if n := l.Count("m910q", capT0); n != 4 {
		t.Fatalf("count = %d, want 4", n)
	}
	// The oldest entry (59 min old) leaves the window in 1 minute.
	if w := l.WaitFor("m910q", InstanceCapPerAccount, capT0); w != time.Minute {
		t.Fatalf("wait = %s, want 1m", w)
	}
	if n := l.Count("m910q", capT0.Add(61*time.Second)); n != 3 {
		t.Fatalf("count after the oldest aged out = %d, want 3", n)
	}
	if w := l.WaitFor("m910q", InstanceCapPerAccount, capT0.Add(61*time.Second)); w != 0 {
		t.Fatalf("wait below the threshold = %s, want 0", w)
	}
	// At exactly 60 minutes the entry is out.
	if n := l.Count("m910q", capT0.Add(time.Minute)); n != 3 {
		t.Fatalf("count at the 60 min mark = %d, want 3", n)
	}
}

func TestCapWaitAtFiveWaitsForTheSecondOldest(t *testing.T) {
	l := capLedgerWith("m910q", 50*time.Minute, 40*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	// Dropping to 3 needs the second entry out, at +20 min, not the oldest at +10.
	if w := l.WaitFor("m910q", InstanceCapPerAccount, capT0); w != 20*time.Minute {
		t.Fatalf("wait = %s, want 20m", w)
	}
}

func TestCapRecordPrunesOldEntries(t *testing.T) {
	l := capLedgerWith("m910q", 90*time.Minute, 5*time.Minute)
	l.Record("m910q", CapEntry{At: capT0}, capT0)
	if got := len(l.Servers["m910q"].Entries); got != 2 {
		t.Fatalf("entries kept = %d, want 2", got)
	}
}

func TestCapServerRestartResets(t *testing.T) {
	l := capLedgerWith("m910q", 50*time.Minute, 40*time.Minute, 5*time.Minute, 2*time.Minute)
	start := capT0.Add(-10 * time.Minute) // the worldserver started 10 minutes ago
	if !l.NoteServerStart("m910q", start) {
		t.Fatal("NoteServerStart did not report the restart")
	}
	if n := l.Count("m910q", capT0); n != 2 {
		t.Fatalf("count after the restart = %d, want 2 (only the entries since the start)", n)
	}
	// The same start time again changes nothing.
	if l.NoteServerStart("m910q", start) {
		t.Fatal("same start time reported as a restart")
	}
	// A later start (another restart) drops everything before it.
	if !l.NoteServerStart("m910q", capT0) {
		t.Fatal("a later start was not a restart")
	}
	if n := l.Count("m910q", capT0); n != 0 {
		t.Fatalf("count after the second restart = %d, want 0", n)
	}
	// A zero start (no read) leaves the ledger alone.
	l.Record("m910q", CapEntry{At: capT0}, capT0)
	if l.NoteServerStart("m910q", time.Time{}) || l.Count("m910q", capT0) != 1 {
		t.Fatal("a zero start time changed the ledger")
	}
}

func TestCapReset(t *testing.T) {
	l := capLedgerWith("m910q", time.Minute, 2*time.Minute)
	l.Record("tortoise-test2", CapEntry{At: capT0}, capT0)
	l.Reset("m910q")
	if l.Count("m910q", capT0) != 0 || l.Count("tortoise-test2", capT0) != 1 {
		t.Fatal("Reset must clear that server only")
	}
}

func TestCapCountForAccounts(t *testing.T) {
	l := &CapLedger{}
	l.Record("s", CapEntry{At: capT0.Add(-3 * time.Minute), Accounts: []string{"a1", "a2"}}, capT0)
	l.Record("s", CapEntry{At: capT0.Add(-2 * time.Minute), Accounts: []string{"a2", "a3"}}, capT0)
	if n := l.CountFor("s", []string{"a1"}, capT0); n != 1 {
		t.Fatalf("a1 = %d, want 1", n)
	}
	if n := l.CountFor("s", []string{"a1", "a2"}, capT0); n != 2 {
		t.Fatalf("a1+a2 = %d, want 2 (the busiest)", n)
	}
	// An entry without accounts falls back to counting raids.
	l.Record("s", CapEntry{At: capT0.Add(-time.Minute)}, capT0)
	if n := l.CountFor("s", []string{"a1"}, capT0); n != 3 {
		t.Fatalf("with an unnamed entry = %d, want 3 raids", n)
	}
	if n := l.CountFor("s", nil, capT0); n != 3 {
		t.Fatalf("no accounts = %d, want 3", n)
	}
}

func TestCapFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", InstanceCapFile)
	if l, err := LoadCapLedger(path); err != nil || l.Count("s", capT0) != 0 {
		t.Fatalf("missing file: %v, count %d", err, l.Count("s", capT0))
	}
	for i := 0; i < 2; i++ {
		if _, err := UpdateCapLedger(path, func(l *CapLedger) { l.Record("s", CapEntry{At: capT0, Plan: "zg"}, capT0) }); err != nil {
			t.Fatal(err)
		}
	}
	l, err := LoadCapLedger(path)
	if err != nil || l.Count("s", capT0) != 2 {
		t.Fatalf("reload: %v, count %d", err, l.Count("s", capT0))
	}
}

func TestCapReservationCountsAndOnlyOneOfThreeIsAdmittedAtThree(t *testing.T) {
	l := capLedgerWith("m910q", 30*time.Minute, 20*time.Minute, 10*time.Minute)
	ok1, n1, _ := l.TryReserve("m910q", "a#1", InstanceCapPerAccount, capT0)
	ok2, n2, w2 := l.TryReserve("m910q", "b#2", InstanceCapPerAccount, capT0)
	ok3, n3, _ := l.TryReserve("m910q", "c#3", InstanceCapPerAccount, capT0)
	if !ok1 || n1 != 3 {
		t.Fatalf("first: admitted %v at %d", ok1, n1)
	}
	if ok2 || n2 != 4 || ok3 || n3 != 4 {
		t.Fatalf("second/third: %v@%d %v@%d, want both refused at 4", ok2, n2, ok3, n3)
	}
	// The reservation expires 30 min after it was made, before the oldest entry (30 min old, 30 more to go) leaves.
	if w2 != InstanceCapReserveFor {
		t.Fatalf("wait = %s, want %s", w2, InstanceCapReserveFor)
	}
	// The same owner asking again does not count against itself.
	if ok, _, _ := l.TryReserve("m910q", "a#1", InstanceCapPerAccount, capT0); !ok {
		t.Fatal("an owner's own reservation blocked it")
	}
}

func TestCapReservationReleaseConfirmExpire(t *testing.T) {
	l := capLedgerWith("s", time.Minute)
	l.TryReserve("s", "a", InstanceCapPerAccount, capT0)
	l.TryReserve("s", "b", InstanceCapPerAccount, capT0)
	if n := l.Count("s", capT0); n != 3 {
		t.Fatalf("1 entry + 2 reservations = %d, want 3", n)
	}
	l.Release("s", "a")
	if n := l.Count("s", capT0); n != 2 {
		t.Fatalf("after release = %d, want 2", n)
	}
	l.Confirm("s", "b", CapEntry{At: capT0}, capT0)
	if n := l.Count("s", capT0); n != 2 || len(l.Servers["s"].Reservations) != 0 {
		t.Fatalf("after confirm = %d, reservations %v", n, l.Servers["s"].Reservations)
	}
	l.TryReserve("s", "c", InstanceCapPerAccount, capT0)
	if n := l.Count("s", capT0.Add(InstanceCapReserveFor+time.Second)); n != 2 {
		t.Fatalf("an expired reservation still counts: %d", n)
	}
	l.Record("s", CapEntry{At: capT0.Add(time.Hour)}, capT0.Add(InstanceCapReserveFor+time.Second))
	if len(l.Servers["s"].Reservations) != 0 {
		t.Fatal("Record kept an expired reservation")
	}
}

func TestCapUpdateMovesACorruptLedgerAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, InstanceCapFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved, err := UpdateCapLedger(path, func(l *CapLedger) { l.Record("s", CapEntry{At: capT0}, capT0) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(moved, path+".corrupt-") {
		t.Fatalf("moved aside to %q", moved)
	}
	if got, _ := os.ReadFile(moved); string(got) != "{not json" {
		t.Fatalf("the corrupt content was not kept: %q", got)
	}
	if l, err := LoadCapLedger(path); err != nil || l.Count("s", capT0) != 1 {
		t.Fatalf("new ledger: %v", err)
	}
}

func TestCapUpdateNeverWritesOverAnUnreadableLedger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, InstanceCapFile)
	if err := os.Mkdir(path, 0o700); err != nil { // reading a directory fails, and it is not a missing file
		t.Fatal(err)
	}
	ran := false
	moved, err := UpdateCapLedger(path, func(*CapLedger) { ran = true })
	if err == nil || ran || moved != "" {
		t.Fatalf("err %v, fn ran %v, moved %q: an unreadable ledger must be refused", err, ran, moved)
	}
	if st, serr := os.Stat(path); serr != nil || !st.IsDir() {
		t.Fatal("the unreadable ledger was replaced")
	}
	if es, _ := filepath.Glob(path + ".corrupt-*"); len(es) != 0 {
		t.Fatalf("an unreadable ledger was moved aside: %v", es)
	}
}

// TestCapCountsPerAccountWhenEveryEntryNamesThem is the walk 37 fix: the old count blocked a walk at four RAIDS on
// the server although the busiest pool account had only three fresh instances. The per-account gate keeps one place of
// headroom (RaidLimit(5) = 4), so the fourth is the last admitted one.
func TestCapCountsPerAccountWhenEveryEntryNamesThem(t *testing.T) {
	l := &CapLedger{}
	for i := 0; i < 3; i++ {
		l.Record("s", CapEntry{At: capT0.Add(-time.Duration(50-10*i) * time.Minute), Accounts: []string{"a1", "a2"}}, capT0)
	}
	if n, known := l.Busiest("s", capT0); !known || n != 3 {
		t.Fatalf("busiest = %d known %v, want 3 true", n, known)
	}
	// Three raids on the server used to fill the ledger; per account the fourth is still allowed.
	if admitted, n, _ := l.TryReserve("s", "w1", InstanceCapPerAccount, capT0); !admitted || n != 3 {
		t.Fatalf("admission at three per account: admitted %v count %d, want true 3", admitted, n)
	}
	l.Release("s", "w1")
	l.Record("s", CapEntry{At: capT0.Add(-5 * time.Minute), Accounts: []string{"a1"}}, capT0)
	if n, known := l.Busiest("s", capT0); !known || n != 4 {
		t.Fatalf("busiest after a fourth entry = %d known %v, want 4 true", n, known)
	}
	// Four is the per-account limit (cap-1 headroom): the walk waits for the oldest a1 entry (50 min old).
	admitted, n, wait := l.TryReserve("s", "w1", InstanceCapPerAccount, capT0)
	if admitted || n != 4 || wait != 10*time.Minute {
		t.Fatalf("admission at four per account: admitted %v count %d wait %s, want false 4 10m", admitted, n, wait)
	}
	// At five the second-oldest a1 entry must leave (the one 40 min old leaves in 20).
	l.Record("s", CapEntry{At: capT0.Add(-time.Minute), Accounts: []string{"a1"}}, capT0)
	if w := l.WaitForBusiest("s", InstanceCapPerAccount, capT0); w != 20*time.Minute {
		t.Fatalf("wait at five = %s, want 20m", w)
	}
}

func TestCapTiedBusiestAccountsAllHaveToLeaveOne(t *testing.T) {
	l := &CapLedger{}
	for i := 0; i < 5; i++ {
		age := time.Duration(50-10*i) * time.Minute
		l.Record("s", CapEntry{At: capT0.Add(-age), Accounts: []string{"a1"}}, capT0)
		l.Record("s", CapEntry{At: capT0.Add(-age - time.Minute), Accounts: []string{"a2"}}, capT0)
	}
	// The limit is four: a1's second-oldest leaves in 20m, a2's in 19m; the busiest is still five until both left
	// one (20m).
	if w := l.WaitForBusiest("s", InstanceCapPerAccount, capT0); w != 20*time.Minute {
		t.Fatalf("wait with two tied busiest accounts = %s, want 20m", w)
	}
}

// A reuse placement re-hires accounts into the instance the raid already has: the core counts that instance once per
// account, so only the accounts not recorded for it are counted.
func TestCapReuseRecordsOnlyTheNewAccountsOfTheInstance(t *testing.T) {
	l := &CapLedger{}
	l.Record("s", CapEntry{At: capT0.Add(-10 * time.Minute), Accounts: []string{"a1", "a2"}, Instance: 101}, capT0)
	if n := l.RecordReuse("s", 101, []string{"a1", "a2"}, "zg", capT0); n != 0 {
		t.Fatalf("re-hiring the same accounts counted %d, want 0", n)
	}
	if n := l.RecordReuse("s", 101, []string{"a2", "a3"}, "zg", capT0); n != 1 {
		t.Fatalf("re-hiring one new account counted %d, want 1", n)
	}
	if n, known := l.Busiest("s", capT0); !known || n != 1 {
		t.Fatalf("busiest = %d known %v, want 1 (a1 and a2 once each, a3 once)", n, known)
	}
	// Another instance counts its accounts again (a new instance id per account).
	if n := l.RecordReuse("s", 102, []string{"a1"}, "zg", capT0); n != 1 {
		t.Fatalf("a1 in another instance counted %d, want 1", n)
	}
	if n, known := l.Busiest("s", capT0); !known || n != 2 {
		t.Fatalf("busiest after a1's second instance = %d known %v, want 2", n, known)
	}
	// An entry whose instance is unknown cannot be deduped: every account counts, as before.
	l.Record("s", CapEntry{At: capT0.Add(-time.Minute), Accounts: []string{"a9"}}, capT0)
	if n := l.RecordReuse("s", 0, []string{"a9"}, "zg", capT0); n != 1 {
		t.Fatalf("an unknown instance deduped an account: counted %d, want 1", n)
	}
}

func TestCapSetInstanceStampsTheEntry(t *testing.T) {
	l := &CapLedger{}
	at := capT0.Add(-time.Minute)
	l.Record("s", CapEntry{At: at, Accounts: []string{"a1"}}, capT0)
	if !l.SetInstance("s", at, 77) {
		t.Fatal("SetInstance did not find the entry")
	}
	if e := l.Servers["s"].Entries[0]; e.Instance != 77 {
		t.Fatalf("instance = %v, want 77", e.Instance)
	}
	if l.SetInstance("s", capT0, 78) {
		t.Fatal("SetInstance stamped an entry that does not exist")
	}
}

func TestCapPerAccountWaitForTheHiredParty(t *testing.T) {
	l := &CapLedger{}
	for i := 0; i < 5; i++ {
		l.Record("s", CapEntry{At: capT0.Add(-time.Duration(50-10*i) * time.Minute), Accounts: []string{"a1"}}, capT0)
	}
	for i := 0; i < 5; i++ {
		l.Record("s", CapEntry{At: capT0.Add(-time.Duration(40-5*i) * time.Minute), Accounts: []string{"a2"}}, capT0)
	}
	// The limit is four (cap-1 headroom): a2's second-oldest entry leaves in 25m.
	if w := l.WaitForAccounts("s", []string{"a2"}, InstanceCapPerAccount, capT0); w != 25*time.Minute {
		t.Fatalf("wait for a2 = %s, want 25m (its second-oldest entry leaves)", w)
	}
	if w := l.WaitForAccounts("s", []string{"a1", "a2"}, InstanceCapPerAccount, capT0); w != 25*time.Minute {
		t.Fatalf("wait for both = %s, want the later of the two (25m)", w)
	}
	if n, known := l.PeakFor("s", []string{"a1", "a2"}, capT0); !known || n != 5 {
		t.Fatalf("peak known %v count %d, want true 5", known, n)
	}
	if w := l.WaitForAccounts("s", []string{"a3"}, InstanceCapPerAccount, capT0); w != 0 {
		t.Fatalf("wait for an unused account = %s, want 0", w)
	}
}

func TestCapFallsBackToRaidsOnAnUnnamedEntry(t *testing.T) {
	l := &CapLedger{}
	for i := 0; i < 3; i++ {
		l.Record("s", CapEntry{At: capT0.Add(-time.Duration(30-10*i) * time.Minute), Accounts: []string{"a1"}}, capT0)
	}
	l.Record("s", CapEntry{At: capT0.Add(-time.Minute)}, capT0) // an old walk's entry
	if n, known := l.Busiest("s", capT0); known || n != 4 {
		t.Fatalf("busiest with an unnamed entry = %d known %v, want 4 false", n, known)
	}
	if admitted, n, _ := l.TryReserve("s", "w1", InstanceCapPerAccount, capT0); admitted || n != 4 {
		t.Fatalf("admission with an unnamed entry: admitted %v count %d, want false 4", admitted, n)
	}
	if n, known := l.PeakFor("s", []string{"a1"}, capT0); known || n != 4 {
		t.Fatalf("peak with an unnamed entry = %d known %v, want 4 false", n, known)
	}
}

// TestCapThirtyLetsSixRaidsAnHour pins the local core patch (MAX_INSTANCE_PER_ACCOUNT_PER_HOUR 30): six fresh raids
// in one hour on one server, two pool accounts each, all pass the ledger's gate.
func TestCapThirtyLetsSixRaidsAnHour(t *testing.T) {
	l := &CapLedger{}
	for i := 0; i < 6; i++ {
		owner := "w" + string(rune('0'+i))
		admitted, n, wait := l.TryReserve("m910q", owner, 30, capT0)
		if !admitted {
			t.Fatalf("raid %d refused at count %d (wait %s), want admitted below the cap of 30", i+1, n, wait)
		}
		l.Confirm("m910q", owner, CapEntry{At: capT0, Accounts: []string{"a1", "a2"}}, capT0)
	}
	if n, known := l.Busiest("m910q", capT0); !known || n != 6 {
		t.Fatalf("busiest account = %d known %v, want 6 true", n, known)
	}
}

// TestCapFiveKeepsTheFifthOut is the stock core's behaviour with the per-account headroom: the gate admits below
// RaidLimit(5) = 4 per account, so the fifth placement waits.
func TestCapFiveKeepsTheFifthOut(t *testing.T) {
	l := &CapLedger{}
	for i := 0; i < 4; i++ {
		owner := "w" + string(rune('0'+i))
		if admitted, n, _ := l.TryReserve("m910q", owner, 5, capT0); !admitted {
			t.Fatalf("raid %d refused at count %d, want admitted below 4", i+1, n)
		}
		l.Confirm("m910q", owner, CapEntry{At: capT0, Accounts: []string{"a1", "a2"}}, capT0)
	}
	admitted, n, wait := l.TryReserve("m910q", "w4", 5, capT0)
	if admitted || n != 4 || wait != time.Hour {
		t.Fatalf("fifth: admitted %v count %d wait %s, want false 4 1h", admitted, n, wait)
	}
}

// TestCapRaidCountScalesWithTheCap: an old entry that names no accounts keeps the conservative raid count, one below
// the per-account cap (4 for the stock 5, 29 for a patched 30).
func TestCapRaidCountScalesWithTheCap(t *testing.T) {
	l := &CapLedger{}
	for i := 0; i < 28; i++ {
		l.Record("s", CapEntry{At: capT0.Add(-time.Duration(i) * time.Minute)}, capT0)
	}
	if admitted, n, _ := l.TryReserve("s", "w", 30, capT0); !admitted || n != 28 {
		t.Fatalf("cap 30 at 28 raids: admitted %v count %d, want true 28", admitted, n)
	}
	l.Release("s", "w")
	l.Record("s", CapEntry{At: capT0.Add(-29 * time.Minute)}, capT0)
	if admitted, n, _ := l.TryReserve("s", "w", 30, capT0); admitted || n != 29 {
		t.Fatalf("cap 30 at 29 raids: admitted %v count %d, want false 29", admitted, n)
	}

	l5 := &CapLedger{}
	for i := 0; i < 3; i++ {
		l5.Record("s", CapEntry{At: capT0.Add(-time.Duration(i) * time.Minute)}, capT0)
	}
	if admitted, n, _ := l5.TryReserve("s", "w", 5, capT0); !admitted || n != 3 {
		t.Fatalf("cap 5 at 3 raids: admitted %v count %d, want true 3", admitted, n)
	}
	l5.Release("s", "w")
	l5.Record("s", CapEntry{At: capT0.Add(-4 * time.Minute)}, capT0)
	if admitted, n, _ := l5.TryReserve("s", "w", 5, capT0); admitted || n != 4 {
		t.Fatalf("cap 5 at 4 raids: admitted %v count %d, want false 4", admitted, n)
	}
}
