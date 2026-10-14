package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// entryLedger remembers when the account entered a new dungeon instance, so that a remote run does not try a 6th within the hour (the
// core refuses it per account: MAX_INSTANCE_PER_ACCOUNT_PER_HOUR = 5). The owned bots share the agent's account, so the ledger is
// per agent account. It is a small JSON file of unix times next to the settings file (named by a hash of the account, so the
// account name stays out of it); with no path it lives in memory only. A mangosd restart clears the server's list but not this
// file: the run's ignore_entry_limit overrides it.
type entryLedger struct {
	path string
	mu   sync.Mutex
	at   []time.Time
	read bool
}

type ledgerFile struct {
	Entries []int64 `json:"entries"`
}

// load reads the file once; a missing or unreadable file is an empty ledger.
func (l *entryLedger) load() {
	if l.read {
		return
	}
	l.read = true
	if l.path == "" {
		return
	}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	var f ledgerFile
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	for _, s := range f.Entries {
		l.at = append(l.at, time.Unix(s, 0))
	}
}

// prune drops the entries older than an hour before now.
func (l *entryLedger) prune(now time.Time) {
	keep := l.at[:0]
	for _, t := range l.at {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	l.at = keep
}

// Recent implements dungeon.EntryLedger.
func (l *entryLedger) Recent(now time.Time) []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	l.prune(now)
	return append([]time.Time(nil), l.at...)
}

// Record implements dungeon.EntryLedger.
func (l *entryLedger) Record(now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	l.prune(now)
	l.at = append(l.at, now)
	if l.path == "" {
		return nil
	}
	var f ledgerFile
	for _, t := range l.at {
		f.Entries = append(f.Entries, t.Unix())
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(l.path, raw, 0o600)
}
