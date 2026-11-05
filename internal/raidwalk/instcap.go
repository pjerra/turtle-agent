// instcap.go: the per-server ledger of fresh instance placements (the core's "N instance ids per account per hour"
// cap). The core lets one account enter only N distinct instance ids in an hour (stock 5, MAX_INSTANCE_PER_ACCOUNT_PER_HOUR);
// the list lives in the worldserver's memory, so a restart clears it. A raid walk that creates a fresh instance (a
// first run, --place, a walk-super restart, an idle-raid re-placement) records it here, and the next walk reads the
// count before it hires.
//
// The hire output (TBM:HIRE_RAID) does not name the pool accounts it used; the walk reads them with `.pinfo <hire>`
// after the last batch and records their ids on the entry. When every live entry in the window names its accounts the
// ledger counts the core's cap per account (cap/hour), and TryReserve/PeakFor use that. An older entry without accounts
// (or a live reservation from a walk that has not hired yet) falls back to the conservative raid count, one below the
// cap.
package raidwalk

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// InstanceCapWindow is how long the core remembers an entered instance id for an account.
	InstanceCapWindow = time.Hour
	// InstanceCapWarnAt is the stock raid count at which a walk stops starting blindly when the ledger cannot count
	// per account (one below the stock cap, leaving a spare for the walk that is already running).
	InstanceCapWarnAt = InstanceCapPerAccount - 1
	// InstanceCapPerAccount is the stock core's cap per pool account: the 6th distinct instance id in an hour is refused.
	// A local core patch may raise it; the commands take --instance-cap-per-account and the instance-cap-per-account.json
	// file a per-server value.
	InstanceCapPerAccount = 5
	// InstanceCapDefaultWait is the default of --instance-cap-wait.
	InstanceCapDefaultWait = 20 * time.Minute
	// InstanceCapReserveFor is how long a reservation holds a place: a walk that reserved and then died (SIGKILL, a
	// lost host) frees it by itself after this.
	InstanceCapReserveFor = 30 * time.Minute
	// InstanceCapFile is the ledger's file name in the walk state dir.
	InstanceCapFile = "instance-cap.json"
)

// RaidLimit is the conservative raid count for a ledger that cannot count per account: one below the per-account cap,
// because a raid packs 2-3 pool accounts and the walk that is about to start needs the last place. cap 0 means the
// stock core; a cap of 1 still admits the first raid.
func RaidLimit(cap int) int {
	if cap <= 0 {
		cap = InstanceCapPerAccount
	}
	return max(1, cap-1)
}

// CapEntry is one fresh instance placement.
type CapEntry struct {
	At       time.Time `json:"at"`
	Plan     string    `json:"plan,omitempty"`
	Accounts []string  `json:"accounts,omitempty"`
	// Instance is the instance id the placement entered (0 when the walk could not read it, e.g. an older entry).
	// The core counts an instance id once per account, so a reuse placement that re-hires only counts accounts not
	// already recorded for that instance.
	Instance float64 `json:"instance,omitempty"`
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
	// GMInstanceData are one mark per fired --gm-instance-data leg. An old
	// ledger holds the bare "<instance id>/<leg>" strings instead of
	// GMInstanceMark objects; both read back as done. A --reuse-instance
	// restart runs a fresh out dir per attempt, so the out dir's marker
	// cannot tell it: the ledger outlives the attempt, and resending is
	// noisy (not harmful: the gate just stays satisfied).
	GMInstanceData []GMInstanceMark `json:"gm_instance_data,omitempty"`
}

// GMInstanceMark is one --gm-instance-data firing: the leg's lines went out
// in inst at At, with the hash of the exact command list sent. The hash
// makes a CHANGED command list resend: Done compares it, and a changed list
// is not done. A repeat of the same list stays silent.
type GMInstanceMark struct {
	// Inst and Leg are the mark's key: a bare "<inst>/<leg>" string in an
	// old ledger reads back as this with zero At and empty Hash.
	Inst string `json:"inst,omitempty"`
	Leg  string `json:"leg,omitempty"`
	// Mark is the old ledger's bare "<inst>/<leg>" string ("": a new mark).
	Mark string `json:"mark,omitempty"`
	// At is when the leg's lines went out. A mark only counts when it is
	// newer than that instance's cap-ledger entry's first-seen time: the
	// core reuses instance ids, so a mark in instance 7 counts only for
	// the instance 7 the entry recorded.
	At time.Time `json:"at,omitempty"`
	// Hash is the hash of the exact command list sent. A changed list
	// (another hash) is a new firing, not a done one.
	Hash string `json:"hash,omitempty"`
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

// WaitFor is how long until the raid count drops below RaidLimit(cap): zero when it already is, else until the item
// that has to leave does (the oldest at a count of 4 for cap 5, the second at a count of 5).
func (l *CapLedger) WaitFor(name string, cap int, now time.Time) time.Duration {
	lv := l.leaves(name, now)
	limit := RaidLimit(cap)
	if len(lv) < limit {
		return 0
	}
	return lv[len(lv)-limit].Sub(now)
}

// accountsKnown reports whether every live item names its accounts: entries carry them, reservations do not.
func (l *CapLedger) accountsKnown(name string, now time.Time) bool {
	if l.Count(name, now) != len(l.live(name, now)) {
		return false // a live reservation, whose accounts are unknown
	}
	for _, e := range l.live(name, now) {
		if len(e.Accounts) == 0 {
			return false
		}
	}
	return true
}

// Busiest is the most fresh instances any one pool account has entered in the window. known is false when the
// ledger cannot say (a live reservation or an entry without accounts): the caller then keeps the raid count.
func (l *CapLedger) Busiest(name string, now time.Time) (count int, known bool) {
	if !l.accountsKnown(name, now) {
		return l.Count(name, now), false
	}
	per := map[string]int{}
	for _, e := range l.live(name, now) {
		for _, a := range e.Accounts {
			per[a]++
			if per[a] > count {
				count = per[a]
			}
		}
	}
	return count, true
}

// PeakFor is Busiest narrowed to accounts: the most placements any of them has in the window. known is false when
// an entry in the window names no accounts, so the caller must fall back to the raid count.
func (l *CapLedger) PeakFor(name string, accounts []string, now time.Time) (peak int, known bool) {
	live := l.live(name, now)
	if len(accounts) == 0 {
		return l.Count(name, now), false
	}
	per := map[string]int{}
	for _, e := range live {
		if len(e.Accounts) == 0 {
			return l.Count(name, now), false
		}
		for _, a := range e.Accounts {
			per[a]++
		}
	}
	for _, a := range accounts {
		if per[a] > peak {
			peak = per[a]
		}
	}
	return peak, true
}

// WaitForAccounts is how long until every account in accounts is below the per-account limit (RaidLimit(cap), one
// place of headroom): zero when they all are, else until the entry that has to leave does.
func (l *CapLedger) WaitForAccounts(name string, accounts []string, cap int, now time.Time) time.Duration {
	limit := RaidLimit(cap)
	set := map[string]bool{}
	for _, a := range accounts {
		set[a] = true
	}
	per := map[string][]time.Time{}
	for _, e := range l.live(name, now) {
		for _, a := range e.Accounts {
			if set[a] {
				per[a] = append(per[a], e.At.Add(InstanceCapWindow))
			}
		}
	}
	var until time.Time
	for _, ts := range per {
		if len(ts) < limit {
			continue
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		if at := ts[len(ts)-limit]; at.After(until) {
			until = at
		}
	}
	if until.IsZero() {
		return 0
	}
	return until.Sub(now)
}

// WaitForBusiest is how long until the busiest pool account is below the per-account limit (RaidLimit(cap), one place
// of headroom): zero when the ledger cannot count per account (WaitFor then) or one already is, else until that
// account's entry that has to leave does.
func (l *CapLedger) WaitForBusiest(name string, cap int, now time.Time) time.Duration {
	limit := RaidLimit(cap)
	if !l.accountsKnown(name, now) {
		return l.WaitFor(name, cap, now)
	}
	per := map[string][]time.Time{}
	for _, e := range l.live(name, now) {
		for _, a := range e.Accounts {
			per[a] = append(per[a], e.At.Add(InstanceCapWindow))
		}
	}
	peak := 0
	for _, ts := range per {
		if len(ts) > peak {
			peak = len(ts)
		}
	}
	if peak < limit {
		return 0
	}
	// Every account at the peak has to lose one before the busiest is below the limit: the latest of those leaves.
	var until time.Time
	for _, ts := range per {
		if len(ts) != peak {
			continue
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		if at := ts[len(ts)-limit]; at.After(until) {
			until = at
		}
	}
	return until.Sub(now)
}

// TryReserve is the admission step, to run inside UpdateCapLedger so it is atomic with the count. When the ledger
// names every live entry's accounts it counts per pool account and the busiest account must be below RaidLimit(cap)
// (one place of headroom); otherwise it keeps the same conservative raid count, because a raid packs 2-3 pool
// accounts. On admission it holds a place for owner; otherwise it reports the count and the wait until it falls.
func (l *CapLedger) TryReserve(name, owner string, cap int, now time.Time) (admitted bool, count int, wait time.Duration) {
	l.Release(name, owner)
	if cap <= 0 {
		cap = InstanceCapPerAccount
	}
	limit := RaidLimit(cap)
	count, known := l.Busiest(name, now)
	if count >= limit {
		if known {
			return false, count, l.WaitForBusiest(name, cap, now)
		}
		return false, count, l.WaitFor(name, cap, now)
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

// gmInstanceDataMark is the old ledger's bare mark of a leg fired in an
// instance ("<instance id>/<leg>").
func gmInstanceDataMark(inst, leg string) string { return inst + "/" + leg }

// gmMarkKey splits a mark back into its instance and leg ("": no key).
func gmMarkKey(m GMInstanceMark) (inst, leg string) {
	if m.Inst != "" || m.Leg != "" {
		return m.Inst, m.Leg
	}
	if i := strings.LastIndex(m.Mark, "/"); i >= 0 {
		return m.Mark[:i], m.Mark[i+1:]
	}
	return "", ""
}

// NoteGMInstanceData remembers that leg's lines went out in inst (once: a
// mark read back counts as done). at is when they went out, hash the hash
// of the exact command list sent (GMInstanceDataHash): a changed list is a
// new firing, not a done one.
func (l *CapLedger) NoteGMInstanceData(name, inst, leg string, at time.Time, hash string) {
	s := l.server(name)
	mark := GMInstanceMark{Inst: inst, Leg: leg, At: at.UTC(), Hash: hash}
	for i, m := range s.GMInstanceData {
		if mi, ml := gmMarkKey(m); mi == inst && ml == leg {
			s.GMInstanceData[i] = mark
			return
		}
	}
	s.GMInstanceData = append(s.GMInstanceData, mark)
}

// GMInstanceDataDone says leg's lines already went out in inst for this
// command-list hash. A mark only counts when it is not older than that
// instance's first-seen cap-ledger entry time (the instance's own At): the
// core reuses instance ids, so a mark from an older instance 7 is not a
// firing in the new instance 7. A mark whose stored hash names another
// command list is not done either: the list changed, resend. An old bare
// mark (no time, no hash) reads back as done, as before. now bounds the
// first-seen read to live entries.
func (l *CapLedger) GMInstanceDataDone(name, inst, leg, hash string, now time.Time) bool {
	s := l.Servers[name]
	if s == nil {
		return false
	}
	first := time.Time{}
	for _, e := range s.Entries {
		if now.Sub(e.At) >= InstanceCapWindow {
			continue
		}
		if fmt.Sprintf("%.0f", e.Instance) != inst {
			continue
		}
		if first.IsZero() || e.At.Before(first) {
			first = e.At
		}
	}
	for _, m := range s.GMInstanceData {
		if mi, ml := gmMarkKey(m); mi != inst || ml != leg {
			continue
		}
		if m.Hash != "" && hash != "" && m.Hash != hash {
			continue
		}
		if !m.At.IsZero() && !first.IsZero() && m.At.Before(first) {
			continue
		}
		return true
	}
	return false
}

// RecordReuse adds the accounts a reuse placement newly hired into the instance the raid already had. The core counts
// an instance id once per account, so an account already recorded for that instance is not counted again. A placement
// whose instance is unknown (0) cannot be deduped: it counts every account, as before.
func (l *CapLedger) RecordReuse(name string, inst float64, accounts []string, plan string, now time.Time) int {
	if len(accounts) == 0 {
		return 0
	}
	seen := map[string]bool{}
	if inst != 0 {
		for _, e := range l.live(name, now) {
			if e.Instance != inst {
				continue
			}
			for _, a := range e.Accounts {
				seen[a] = true
			}
		}
	}
	fresh := make([]string, 0, len(accounts))
	for _, a := range accounts {
		if !seen[a] {
			fresh = append(fresh, a)
		}
	}
	if len(fresh) == 0 {
		return 0
	}
	l.Record(name, CapEntry{At: now.UTC(), Plan: plan, Accounts: fresh, Instance: inst}, now)
	return len(fresh)
}

// SetInstance stamps the entry made at at with the instance id the raid entered: a reuse placement's dedupe needs it.
// It reports whether the entry was still live.
func (l *CapLedger) SetInstance(name string, at time.Time, inst float64) bool {
	s := l.Servers[name]
	if s == nil {
		return false
	}
	for i := range s.Entries {
		if s.Entries[i].At.Equal(at) {
			s.Entries[i].Instance = inst
			return true
		}
	}
	return false
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
// entry from before that start no longer counts: it is dropped, and so is every --gm-instance-data mark in those
// instances (an id the core reuses after a restart names a new copy). It reports whether the server had restarted
// since the ledger last saw it (an entry or mark dropped, or a changed start time).
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
	kept := s.GMInstanceData[:0:0]
	for _, m := range s.GMInstanceData {
		if !m.At.IsZero() && m.At.Before(start) {
			restarted = true
			continue
		}
		kept = append(kept, m)
	}
	s.GMInstanceData = kept
	s.ServerStart = start
	return restarted
}

// Reset forgets every entry of the server (the operator knows the worldserver
// restarted), and every --gm-instance-data mark with it: the ids may be
// reused by new copies.
func (l *CapLedger) Reset(name string) {
	if s := l.Servers[name]; s != nil {
		s.Entries = nil
		s.Reservations = nil
		s.GMInstanceData = nil
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

// errLedgerLockBusy is acquireLedgerLock's answer when the lock stayed held past the retry budget.
var errLedgerLockBusy = errors.New("ledger lock busy")

// The lock retry budget: ledgerLockTries polls, ledgerLockPoll apart (about 5 s).
const (
	ledgerLockTries = 100
	ledgerLockPoll  = 50 * time.Millisecond
)

// UpdateCapLedger loads the ledger, runs fn on it and saves it, under an advisory lock (flock on <path>.lock, held
// for the critical section) so concurrent walks do not lose each other's change (the admission check and its
// reservation are one call). The lock is never taken over by age: a live holder keeps it however long it holds, and a
// dead holder's lock is released by the kernel. It returns the path the old file was moved to when it was corrupt ("" otherwise): the corrupt file is moved
// aside to <path>.corrupt-<stamp> and a new ledger started, so the history loss is visible. A file that cannot be read
// at all is an error, fn is not run and nothing is written over it.
func UpdateCapLedger(path string, fn func(*CapLedger)) (movedAside string, err error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := acquireLedgerLock(lock)
	if errors.Is(err, errLedgerLockBusy) {
		return "", fmt.Errorf("instance-cap ledger lock %s is busy", lock)
	}
	if err != nil {
		return "", err
	}
	defer releaseLedgerLock(f)
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
