// raid_walk_instcap.go: the instance-cap check before a walk starts and the ledger entry after it enters. The core
// lets an account enter 5 distinct instance ids per hour, remembered in the worldserver's memory only; hires are
// packed up to 10 a pool account, so a re-placed raid soon meets the 6th and its summons fail. Every walk that
// enters a fresh instance is recorded in raidwalk's ledger (a JSON file beside the slots file); at 4 or more in the
// last hour the walk warns and waits (--instance-cap-wait), or exits 26 (--instance-cap-fail).
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// capWaitMargin is added to each wait so the entry is surely out of the window when the walk looks again.
const capWaitMargin = 5 * time.Second

// phaseInstanceCap is phase 1b: the ledger check, before the lease and the hires.
func (w *walk) phaseInstanceCap(ctx context.Context) error {
	path := w.env.capLedger
	if path == "" {
		return nil
	}
	server := w.opts.server
	if w.opts.capReset {
		if err := raidwalk.UpdateCapLedger(path, func(l *raidwalk.CapLedger) { l.Reset(server) }); err != nil {
			w.note("instance cap: reset of the ledger failed: %v", err)
		} else {
			w.note("instance cap: ledger for %s reset (--instance-cap-reset)", server)
		}
	}
	if w.env.serverStart != nil {
		if start, ok := w.env.serverStart(ctx, server); ok {
			err := raidwalk.UpdateCapLedger(path, func(l *raidwalk.CapLedger) {
				if l.NoteServerStart(server, start) {
					w.note("instance cap: %s restarted at %s, ledger reset", server, start.UTC().Format(time.RFC3339))
				}
			})
			if err != nil {
				w.note("instance cap: server start not recorded: %v", err)
			}
		}
	}
	started := w.env.now()
	warned := false
	for {
		l, err := raidwalk.LoadCapLedger(path)
		if err != nil {
			w.note("instance cap: ledger unreadable (%v); counting from empty", err)
		}
		now := w.env.now()
		n := l.Count(server, now)
		if n < raidwalk.InstanceCapWarnAt {
			if warned {
				w.say(now, fmt.Sprintf("instance cap: %d fresh instance(s) in the last hour on %s; going on", n, server))
			}
			return nil
		}
		warn := fmt.Sprintf("instance cap: %d fresh instances in the last hour on %s; the 6th is refused by the core", n, server)
		if !warned {
			w.say(now, warn)
			w.note("%s", warn)
			warned = true
		}
		if w.opts.capFail {
			return stopf(exitRaidWalkInstanceCap, "%s (--instance-cap-fail)", warn)
		}
		wait := l.WaitFor(server, now)
		left := w.opts.capWait - now.Sub(started)
		if wait <= 0 {
			// The count and the wait disagree only on a clock step; look again after a short pause.
			wait = capWaitMargin
		}
		if left <= 0 {
			msg := fmt.Sprintf("instance cap: waited %s (--instance-cap-wait), still %d on %s; going on", w.opts.capWait, n, server)
			w.say(now, msg)
			w.note("%s", msg)
			return nil
		}
		if wait > left {
			wait = left
		} else {
			wait += capWaitMargin
		}
		w.say(now, fmt.Sprintf("instance cap: waiting %s for the oldest of %d to age out of the hour", wait.Round(time.Second), n))
		if err := w.env.sleep(ctx, wait); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// say prints a line to the walk's stderr and run.log.
func (w *walk) say(now time.Time, text string) {
	fmt.Fprintln(w.stderr, "raid-walk: "+text)
	w.logLine(now, text)
}

// recordFreshInstance writes the ledger entry once the raid has entered its instance. The hire output does not name
// the pool accounts, so the entry carries none and later walks count raids.
func (w *walk) recordFreshInstance() {
	path := w.env.capLedger
	if path == "" {
		return
	}
	now := w.env.now()
	e := raidwalk.CapEntry{At: now.UTC(), Plan: w.plan.Name}
	if err := raidwalk.UpdateCapLedger(path, func(l *raidwalk.CapLedger) { l.Record(w.opts.server, e, now) }); err != nil {
		w.note("instance cap: ledger entry not written: %v", err)
	}
}
