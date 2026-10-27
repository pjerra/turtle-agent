//go:build linux

package slots

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProbe struct {
	mu        sync.Mutex
	agents    map[string]int
	listening map[int]bool
}

func (f *fakeProbe) AgentPID(dir string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pid, ok := f.agents[dir]
	if !ok {
		return 0, false
	}
	return pid, true
}

func (f *fakeProbe) PortListening(port int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listening[port]
}

func testStore(t *testing.T, body string) *Store {
	t.Helper()
	path := writeSlots(t, body)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStore(cfg, path)
	fp := &fakeProbe{agents: map[string]int{}, listening: map[int]bool{}}
	st.Probe = fp
	now := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	return st
}

func setNow(st *Store, now time.Time) {
	st.Now = func() time.Time { return now }
}

const twoSlotCfg = `{"version":1,"servers":[
	{"name":"m910q","realmd":"10.0.0.1:3724","slots":[
		{"id":"1","account":"TAGENT1","character":"Gbmage","port":8771,"settings_from":"/s.json"},
		{"id":"2","account":"HARNESSC","character":"Glacienne","port":8769,"settings_from":"/s.json"}]}]}`

// 5. Two holders, one slot: the second is refused, --any finds the other.
func TestAcquireTwoHoldersOneSlot(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	if _, err := st.Acquire("m910q", "1", false, "w-a", 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	_, err := st.Acquire("m910q", "1", false, "w-b", 3*time.Hour)
	if err == nil || !strings.Contains(err.Error(), "held by w-a") {
		t.Fatalf("got %v, want a held-by-w-a refusal", err)
	}
	got, err := st.Acquire("m910q", "", true, "w-b", 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lease.Slot != "2" {
		t.Fatalf("w-b --any got slot %q, want 2", got.Lease.Slot)
	}
}

// 6. Concurrent --any: the flock lets exactly 3 succeed on distinct slots.
func TestAcquireConcurrent(t *testing.T) {
	st := testStore(t, `{"version":1,"servers":[
		{"name":"test2","realmd":"10.0.0.2:3724","slots":[
			{"id":"1","account":"A","character":"Gbmage","port":8771,"settings_from":"/s.json"},
			{"id":"2","account":"B","character":"Glacienne","port":8769,"settings_from":"/s.json"},
			{"id":"4","account":"C","character":"Tacmdaron","port":8774,"settings_from":"/s.json"}]}]}`)
	var mu sync.Mutex
	var won []string
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := st.Acquire("test2", "", true, holderName(i), 3*time.Hour)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won = append(won, got.Lease.Slot)
			} else if _, ok := err.(*Refusal); !ok {
				t.Errorf("non-refusal error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if len(won) != 3 {
		t.Fatalf("%d acquires succeeded, want 3", len(won))
	}
	seen := map[string]bool{}
	for _, s := range won {
		if seen[s] {
			t.Fatalf("slot %s leased twice", s)
		}
		seen[s] = true
	}
}

func holderName(i int) string {
	return "w-holder-" + string(rune('a'+i/10)) + string(rune('0'+i%10))
}

// 7. Stale needs BOTH expiry and a dead agent.
func TestStaleNeedsExpiryAndDeadAgent(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	fp := st.Probe.(*fakeProbe)
	base := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	if _, err := st.Acquire("m910q", "1", false, "w-a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDir("m910q", "1", "w-a", "/tmp/ta-w-a"); err != nil {
		t.Fatal(err)
	}
	fp.mu.Lock()
	fp.agents["/tmp/ta-w-a"] = 80353
	fp.mu.Unlock()
	setNow(st, base.Add(2*time.Hour))
	if _, err := st.Acquire("m910q", "1", false, "w-b", time.Hour); err == nil ||
		!strings.Contains(err.Error(), "held by w-a") {
		t.Fatalf("expired+live: got %v, want a held refusal", err)
	}
	fp.mu.Lock()
	delete(fp.agents, "/tmp/ta-w-a")
	fp.mu.Unlock()
	setNow(st, base.Add(30*time.Minute))
	if _, err := st.Acquire("m910q", "1", false, "w-b", time.Hour); err == nil ||
		!strings.Contains(err.Error(), "held by w-a") {
		t.Fatalf("unexpired+dead: got %v, want a held refusal", err)
	}

	setNow(st, base.Add(2*time.Hour))
	got, err := st.Acquire("m910q", "1", false, "w-b", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.TookOver == nil || got.Lease.TakenOverFrom != "w-a" {
		t.Fatalf("takeover = %+v, want TakenOverFrom w-a", got.Lease)
	}
}

// 8. --any skips reserved slots and busy ports; the none-free message names every slot.
func TestAnySkipsReservedAndBusyPorts(t *testing.T) {
	st := testStore(t, `{"version":1,"servers":[
		{"name":"m910q","realmd":"10.0.0.1:3724","slots":[
			{"id":"1","account":"A","character":"Gbmage","port":8771,"settings_from":"/s.json"},
			{"id":"2","account":"B","character":"Glacienne","port":8769,"settings_from":"/s.json"},
			{"id":"3","reserved":"owner rule: never slot 3 on m910q"}]}]}`)
	fp := st.Probe.(*fakeProbe)
	fp.mu.Lock()
	fp.listening[8771] = true
	fp.mu.Unlock()
	got, err := st.Acquire("m910q", "", true, "w-a", 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lease.Slot != "2" {
		t.Fatalf("--any got slot %q, want 2", got.Lease.Slot)
	}
	if _, err := st.Acquire("m910q", "", true, "w-b", 3*time.Hour); err == nil {
		t.Fatal("w-b --any succeeded, want a no-free-slot refusal")
	}
	fp.mu.Lock()
	fp.listening[8769] = true
	fp.mu.Unlock()
	if err := os.Remove(st.leasePath("m910q", "2")); err != nil {
		t.Fatal(err)
	}
	_, err = st.Acquire("m910q", "", true, "w-c", 3*time.Hour)
	if err == nil {
		t.Fatal("want a no-free-slot refusal")
	}
	msg := err.Error()
	for _, want := range []string{"1 port 8771 busy", "2 port 8769 busy", "3 reserved"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
}

// 9. Release: another holder is refused, a live agent blocks, --force wins.
func TestReleaseRules(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	fp := st.Probe.(*fakeProbe)
	if _, err := st.Acquire("m910q", "1", false, "w-a", 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Release("m910q", "1", "w-b", false); err == nil ||
		!strings.Contains(err.Error(), "held by w-a, not w-b") {
		t.Fatalf("got %v, want a held-by refusal", err)
	}
	if err := st.SetDir("m910q", "1", "w-a", "/tmp/ta-w-a"); err != nil {
		t.Fatal(err)
	}
	fp.mu.Lock()
	fp.agents["/tmp/ta-w-a"] = 80353
	fp.mu.Unlock()
	if _, err := st.Release("m910q", "1", "w-a", false); err == nil ||
		!strings.Contains(err.Error(), "run turtle-agent teardown") {
		t.Fatalf("got %v, want a teardown-first refusal", err)
	}
	if _, err := st.Release("m910q", "1", "w-b", true); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.readLease("m910q", "1"); l != nil {
		t.Fatal("forced release left the file")
	}
}

// 10. A second acquire by the same holder renews instead of duplicating.
func TestSameHolderRenews(t *testing.T) {
	st := testStore(t, twoSlotCfg)
	base := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	first, err := st.Acquire("m910q", "1", false, "w-a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	setNow(st, base.Add(30*time.Minute))
	second, err := st.Acquire("m910q", "1", false, "w-a", 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Renewed {
		t.Fatal("second acquire did not renew")
	}
	if !second.Lease.Expires.After(first.Lease.Expires) {
		t.Fatalf("expiry not extended: %v vs %v", second.Lease.Expires, first.Lease.Expires)
	}
	entries, err := os.ReadDir(filepath.Join(st.Dir, "m910q"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d lease files, want 1", len(entries))
	}
}
