// instcap.go: the per-server ledger of fresh instance placements (the core's "5 instance ids per account per hour"
// cap). The core lets one account enter only 5 distinct instance ids in an hour; the list lives in the worldserver's
// memory, so a restart clears it. A raid walk that creates a fresh instance (a first run, --place, a walk-super
// restart, an idle-raid re-placement) records it here, and the next walk reads the count before it hires.
//
// The hire output (TBM:HIRE_RAID) does not name the pool accounts it used, so an entry usually carries no accounts
// and the count is raids on the server, which can only over-count (a raid packs 2-3 accounts of the pool). When every
// entry in the window names its accounts, CountFor counts per account instead.
package raidwalk

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	// InstanceCapWindow is how long the core remembers an entered instance id for an account.
	InstanceCapWindow = time.Hour
	// InstanceCapWarnAt is the count at which a walk stops starting blindly (the core refuses the 6th, so the 5th is
	// the last that works; 4 leaves a spare for the walk that is already running).
	InstanceCapWarnAt = 4
	// InstanceCapDefaultWait is the default of --instance-cap-wait.
	InstanceCapDefaultWait = 20 * time.Minute
	// InstanceCapReserveFor is how long a reservation holds a place: a walk that reserved and then died (SIGKILL, a
	// lost host) frees it by itself after this.
	InstanceCapReserveFor = 30 * time.Minute
	// InstanceCapFile is the ledger's file name in the walk state dir.
	InstanceCapFile = "instance-cap.json"
)

// CapEntry is one fresh instance placement.
type CapEntry struct {
	At       time.Time `json:"at"`
	Plan     string    `json:"plan,omitempty"`
	Accounts []string  `json:"accounts,omitempty"`
}

// CapReservation is a place held for a walk that was admitted and has not entered its instance yet. It counts like an
// entry until it is confirmed, released or expired, so walks that start together cannot all pass at a count of 3.
type CapReservation struct {
	Owner   string    `json:"owner"` // holder + pid
	At      time.Time `json:"at"`
	Expires time.Time `json:"expires"`
}

// CapServer is one server's entries and reservations and the worldserver start time the ledger last saw.
type CapServer struct {
	ServerStart  time.Time        `json:"server_start,omitempty"`
	Entries      []CapEntry       `json:"entries"`
	Reservations []CapReservation `json:"reservations,omitempty"`
}

// CapLedger is the ledger file: one CapServer per server name.
type CapLedger struct {
	Servers map[string]*CapServer `json:"servers"`
}

func (l *CapLedger) server(name string) *CapServer {
	if l.Servers == nil {
		l.Servers = map[string]*CapServer{}
	}
	s := l.Servers[name]
	if s == nil {
		s = &CapServer{}
		l.Servers[name] = s
	}
	return s
}

// leaves returns, oldest first, the moment each live item stops counting: an entry leaves the window an hour after
// it was made, a reservation at its expiry. An entry stamped in the future (a clock step) counts as live.
func (l *CapLedger) leaves(name string, now time.Time) []time.Time {
	s := l.Servers[name]
	if s == nil {
		return nil
	}
	var out []time.Time
	for _, e := range s.Entries {
		if now.Sub(e.At) < InstanceCapWindow {
			out = append(out, e.At.Add(InstanceCapWindow))
		}
	}
	for _, r := range s.Reservations {
		if now.Before(r.Expires) {
			out = append(out, r.Expires)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// live returns the server's entries still inside the window at now, oldest first.
func (l *CapLedger) live(name string, now time.Time) []CapEntry {
	s := l.Servers[name]
	if s == nil {
		return nil
	}
	var out []CapEntry
	for _, e := range s.Entries {
		if now.Sub(e.At) < InstanceCapWindow {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Count is the number of fresh instances recorded for the server in the last hour plus the live reservations
// (raids, not accounts).
func (l *CapLedger) Count(name string, now time.Time) int { return len(l.leaves(name, now)) }

// CountFor is Count narrowed to accounts when every live entry names its accounts: the most placements any one of
// them has in the window. With no accounts given, or an entry or reservation that does not name its accounts, it is
// Count.
func (l *CapLedger) CountFor(name string, accounts []string, now time.Time) int {
	live := l.live(name, now)
	if len(accounts) == 0 || l.Count(name, now) != len(live) {
		return l.Count(name, now)
	}
	per := map[string]int{}
	for _, e := range live {
		if len(e.Accounts) == 0 {
			return len(live)
		}
		for _, a := range e.Accounts {
			per[a]++
		}
	}
	most := 0
	for _, a := range accounts {
		if per[a] > most {
			most = per[a]
		}
	}
	return most
}

// WaitFor is how long until the count drops below InstanceCapWarnAt: zero when it already is, else until the item
// that has to leave does (the oldest at a count of 4, the second at a count of 5).
func (l *CapLedger) WaitFor(name string, now time.Time) time.Duration {
	lv := l.leaves(name, now)
	if len(lv) < InstanceCapWarnAt {
		return 0
	}
	return lv[len(lv)-InstanceCapWarnAt].Sub(now)
}

// TryReserve is the admission step, to run inside UpdateCapLedger so it is atomic with the count: when the count
// (entries plus live reservations, the owner's own reservation excluded) is below InstanceCapWarnAt it holds a place
// for owner and reports admitted; otherwise it reports the count and the wait until it falls below.
func (l *CapLedger) TryReserve(name, owner string, now time.Time) (admitted bool, count int, wait time.Duration) {
	l.Release(name, owner)
	count = l.Count(name, now)
	if count >= InstanceCapWarnAt {
		return false, count, l.WaitFor(name, now)
	}
	s := l.server(name)
	s.Reservations = append(s.Reservations, CapReservation{Owner: owner, At: now, Expires: now.Add(InstanceCapReserveFor)})
	return true, count, 0
}

// Release drops owner's reservation on the server (no reservation: nothing happens).
func (l *CapLedger) Release(name, owner string) {
	s := l.Servers[name]
	if s == nil {
		return
	}
	keep := s.Reservations[:0:0]
	for _, r := range s.Reservations {
		if r.Owner != owner {
			keep = append(keep, r)
		}
	}
	s.Reservations = keep
}

// Confirm turns owner's reservation into the entry e.
func (l *CapLedger) Confirm(name, owner string, e CapEntry, now time.Time) {
	l.Release(name, owner)
	l.Record(name, e, now)
}

// Record adds an entry for the server and prunes the entries that aged out.
func (l *CapLedger) Record(name string, e CapEntry, now time.Time) {
	s := l.server(name)
	keep := s.Entries[:0:0]
	for _, o := range s.Entries {
		if now.Sub(o.At) < InstanceCapWindow {
			keep = append(keep, o)
		}
	}
	s.Entries = append(keep, e)
	s.pruneReservations(now)
}

func (s *CapServer) pruneReservations(now time.Time) {
	keep := s.Reservations[:0:0]
	for _, r := range s.Reservations {
		if now.Before(r.Expires) {
			keep = append(keep, r)
		}
	}
	s.Reservations = keep
}

// NoteServerStart tells the ledger when the server's worldserver started. The core's list lives in memory, so every
// entry from before that start no longer counts: it is dropped. It reports whether the server had restarted since
// the ledger last saw it (an entry dropped, or a changed start time).
func (l *CapLedger) NoteServerStart(name string, start time.Time) bool {
	if start.IsZero() {
		return false
	}
	s := l.server(name)
	restarted := false
	if !s.ServerStart.IsZero() && start.Sub(s.ServerStart) > time.Minute {
		restarted = true
	}
	keep := s.Entries[:0:0]
	for _, e := range s.Entries {
		if e.At.Before(start) {
			restarted = true
			continue
		}
		keep = append(keep, e)
	}
	s.Entries = keep
	s.ServerStart = start
	return restarted
}

// Reset forgets every entry of the server (the operator knows the worldserver restarted).
func (l *CapLedger) Reset(name string) {
	if s := l.Servers[name]; s != nil {
		s.Entries = nil
		s.Reservations = nil
	}
}

// ErrCapCorrupt marks a ledger file that is there and readable but is not valid JSON.
var ErrCapCorrupt = errors.New("instance-cap ledger is corrupt")

// LoadCapLedger reads the ledger file. A missing file is an empty ledger. A file that is not valid JSON returns an
// empty ledger and an error wrapping ErrCapCorrupt; any other read error returns an empty ledger and that error,
// which must not be taken as "no history" (UpdateCapLedger refuses to save over it).
func LoadCapLedger(path string) (*CapLedger, error) {
	l := &CapLedger{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if err := json.Unmarshal(data, l); err != nil {
		return &CapLedger{}, fmt.Errorf("%w: %s: %v", ErrCapCorrupt, path, err)
	}
	return l, nil
}

// saveCapLedger writes the ledger through a temp file and a rename.
func saveCapLedger(path string, l *CapLedger) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".instance-cap-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// UpdateCapLedger loads the ledger, runs fn on it and saves it, under a lock file so concurrent walks do not lose
// each other's change (the admission check and its reservation are one call). A lock held for over 10 s is taken as
// stale. It returns the path the old file was moved to when it was corrupt ("" otherwise): the corrupt file is moved
// aside to <path>.corrupt-<stamp> and a new ledger started, so the history loss is visible. A file that cannot be read
// at all is an error, fn is not run and nothing is written over it.
func UpdateCapLedger(path string, fn func(*CapLedger)) (movedAside string, err error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var held bool
	for i := 0; i < 100 && !held; i++ {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			held = true
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if st, serr := os.Stat(lock); serr == nil && time.Since(st.ModTime()) > 10*time.Second {
			os.Remove(lock)
			continue
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !held {
		return "", fmt.Errorf("instance-cap ledger lock %s is busy", lock)
	}
	defer os.Remove(lock)
	l, lerr := LoadCapLedger(path)
	if lerr != nil {
		if !errors.Is(lerr, ErrCapCorrupt) {
			return "", fmt.Errorf("instance-cap ledger unreadable, not overwritten: %w", lerr)
		}
		aside := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(path, aside); err != nil {
			return "", fmt.Errorf("instance-cap ledger corrupt and could not be moved aside: %w", err)
		}
		movedAside = aside
	}
	fn(l)
	return movedAside, saveCapLedger(path, l)
}
