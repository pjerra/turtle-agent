// raid_walk_instcap.go: the instance-cap check before a walk starts and the ledger entry after it enters. The core
// lets an account enter 5 distinct instance ids per hour, remembered in the worldserver's memory only; hires are
// packed up to 10 a pool account, so a re-placed raid soon meets the 6th and its summons fail. Every walk that
// enters a fresh instance is recorded in raidwalk's ledger (a JSON file beside the slots file). The check and the
// admission are one step under the ledger lock: a walk below 4 in the last hour RESERVES a place (owner holder+pid,
// 30 min), so walks that start together cannot all pass at a count of 3; the reservation becomes the entry once the
// raid has entered and is released on every exit before that. At 4 or more the walk warns and waits
// (--instance-cap-wait), or exits 26 (--instance-cap-fail).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

const (
	// capWaitMargin is added to each wait so the entry is surely out of the window when the walk looks again.
	capWaitMargin = 5 * time.Second
	// capUnreadableWait is the one cycle a walk waits when the ledger cannot be read, before it goes on without it.
	capUnreadableWait = time.Minute
)

// capOwnerName is this walk's reservation owner.
func (w *walk) capOwnerName() string { return fmt.Sprintf("%s#%d", w.opts.holder, os.Getpid()) }

// capUpdate runs fn on the ledger under its lock and reports a corrupt file that was moved aside.
func (w *walk) capUpdate(fn func(*raidwalk.CapLedger)) error {
	moved, err := raidwalk.UpdateCapLedger(w.env.capLedger, fn)
	if moved != "" {
		msg := fmt.Sprintf("instance cap: the ledger was corrupt; moved to %s and a new ledger started (the earlier history is lost)", moved)
		w.say(w.env.now(), msg)
		w.note("%s", msg)
	}
	return err
}

// phaseInstanceCap is phase 1b: the ledger check and reservation, before the lease and the hires.
func (w *walk) phaseInstanceCap(ctx context.Context) error {
	if w.env.capLedger == "" {
		return nil
	}
	server := w.opts.server
	w.capOwner = w.capOwnerName()
	if w.opts.capReset {
		if err := w.capUpdate(func(l *raidwalk.CapLedger) { l.Reset(server) }); err != nil {
			w.note("instance cap: reset of the ledger failed: %v", err)
		} else {
			w.note("instance cap: ledger for %s reset (--instance-cap-reset)", server)
		}
	}
	if w.env.serverStart != nil {
		if start, ok := w.env.serverStart(ctx, server); ok {
			err := w.capUpdate(func(l *raidwalk.CapLedger) {
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
	warned, unreadable := false, 0
	for {
		now := w.env.now()
		var admitted bool
		var n int
		var wait time.Duration
		err := w.capUpdate(func(l *raidwalk.CapLedger) { admitted, n, wait = l.TryReserve(server, w.capOwner, now) })
		if err != nil {
			// The ledger cannot be read (not merely corrupt): it is never written over, and the walk does not guess.
			msg := fmt.Sprintf("instance cap: the ledger cannot be read, so the count on %s is unknown: %v", server, err)
			w.say(now, msg)
			w.note("%s", msg)
			if w.opts.capFail {
				return stopf(exitRaidWalkInstanceCap, "%s (--instance-cap-fail)", msg)
			}
			if unreadable == 0 && w.opts.capWait > 0 {
				unreadable++
				d := min(capUnreadableWait, w.opts.capWait)
				w.say(now, fmt.Sprintf("instance cap: waiting %s for the ledger, then going on without it", d))
				if err := w.env.sleep(ctx, d); err != nil {
					return stopf(exitRaidWalkFailed, "interrupted")
				}
				continue
			}
			w.say(now, "instance cap: going on without the ledger; no entry will be written over it")
			return nil
		}
		if admitted {
			w.capReserved = true
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

// recordFreshInstance turns the walk's reservation into the ledger entry once the raid has entered its instance (no
// reservation, e.g. a walk that went on past the wait cap: a plain entry). The hire output does not name the pool
// accounts, so the entry carries none and later walks count raids.
func (w *walk) recordFreshInstance() {
	if w.env.capLedger == "" {
		return
	}
	now := w.env.now()
	e := raidwalk.CapEntry{At: now.UTC(), Plan: w.plan.Name}
	owner := w.capOwnerName()
	err := w.capUpdate(func(l *raidwalk.CapLedger) { l.Confirm(w.opts.server, owner, e, now) })
	if err != nil {
		w.note("instance cap: ledger entry not written: %v", err)
	}
	w.capReserved = false
}

// releaseCapReservation gives the reservation back when the walk ended before its raid entered.
func (w *walk) releaseCapReservation() {
	if !w.capReserved || w.env.capLedger == "" {
		return
	}
	w.capReserved = false
	owner := w.capOwnerName()
	if err := w.capUpdate(func(l *raidwalk.CapLedger) { l.Release(w.opts.server, owner) }); err != nil {
		w.note("instance cap: reservation not released (it expires in %s): %v", raidwalk.InstanceCapReserveFor, err)
	}
}
