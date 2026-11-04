// raid_walk_instcap.go: the instance-cap check before a walk starts and the ledger entry after it enters. The core
// lets an account enter 5 distinct instance ids per hour, remembered in the worldserver's memory only; hires are
// packed up to 10 a pool account, so a re-placed raid soon meets the 6th and its summons fail. Every walk that
// enters a fresh instance is recorded in raidwalk's ledger (a JSON file beside the slots file). The check and the
// admission are one step under the ledger lock: a walk below 4 in the last hour RESERVES a place (owner holder+pid,
// 30 min), so walks that start together cannot all pass at a count of 3; the reservation becomes the entry once the
// raid has entered and is released on every exit before that. At 4 or more the walk warns and waits
// (--instance-cap-wait), or exits 26 (--instance-cap-fail). A walk never starts a fresh instance without a reservation:
// when the wait runs out, or the ledger cannot be read, it exits 26 too, unless --instance-cap-unsafe lets it go on
// (logged, no reservation).
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
				w.say(now, fmt.Sprintf("instance cap: waiting %s for the ledger, then giving up on it", d))
				if err := w.env.sleep(ctx, d); err != nil {
					return stopf(exitRaidWalkFailed, "interrupted")
				}
				continue
			}
			if !w.opts.capUnsafe {
				return stopf(exitRaidWalkInstanceCap, "%s: no fresh instance is started without a reservation (--instance-cap-unsafe goes on without the ledger)", msg)
			}
			w.say(now, "instance cap: --instance-cap-unsafe: going on without the ledger and without a reservation; no entry will be written over it")
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
			msg := fmt.Sprintf("instance cap: waited %s (--instance-cap-wait), still %d on %s", w.opts.capWait, n, server)
			if !w.opts.capUnsafe {
				w.say(now, msg)
				w.note("%s", msg)
				return stopf(exitRaidWalkInstanceCap, "%s: no fresh instance is started without a reservation (--instance-cap-unsafe goes on anyway)", msg)
			}
			msg += "; --instance-cap-unsafe: going on without a reservation"
			w.say(now, msg)
			w.note("%s", msg)
			return nil
		}
		// The earliest free time is when the count drops below the warn threshold, before the walk's own wait cap and
		// its margin clip it: the operator can plan around it instead of seeing only a duration (walk 32).
		freeAt := now.Add(wait)
		if wait > left {
			wait = left
		} else {
			wait += capWaitMargin
		}
		w.say(now, fmt.Sprintf("instance cap: waiting %s (earliest free time %s) for the oldest of %d to age out of the hour",
			wait.Round(time.Second), freeAt.UTC().Format(time.RFC3339), n))
		if err := w.env.sleep(ctx, wait); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// waitHireAccountCap is the exact gate after the hires: the core refuses an account's sixth fresh instance in an
// hour, so when the busiest hired account is at the cap the walk waits (--instance-cap-wait) before it enters, or
// exits 26 with --instance-cap-fail. A ledger that cannot count per account (an older entry without accounts) is not
// a second gate: phaseInstanceCap's raid count already ran. --instance-cap-unsafe goes on, logged, as before.
func (w *walk) waitHireAccountCap(ctx context.Context) error {
	if w.env.capLedger == "" || len(w.hireAccounts) == 0 {
		return nil
	}
	server := w.opts.server
	started := w.env.now()
	warned := false
	for {
		now := w.env.now()
		var peak int
		var known bool
		var wait time.Duration
		err := w.capUpdate(func(l *raidwalk.CapLedger) {
			peak, known = l.PeakFor(server, w.hireAccounts, now)
			if known {
				wait = l.WaitForAccounts(server, w.hireAccounts, now)
			}
		})
		if err != nil {
			// The ledger was read a moment ago for the reservation; a read that now fails leaves the raid count in
			// charge, exactly as before this check.
			w.note("instance cap: the hired accounts cannot be counted (%v); going on with the raid count", err)
			return nil
		}
		if !known {
			w.note("instance cap: an entry in the hour names no accounts, so the hired accounts are counted as raids")
			return nil
		}
		if peak < raidwalk.InstanceCapPerAccount {
			if warned {
				w.say(now, fmt.Sprintf("instance cap: the busiest hired account has %d fresh instance(s) in the last hour; going on", peak))
			}
			return nil
		}
		if !warned {
			msg := fmt.Sprintf("instance cap: a hired account already has %d fresh instances in the last hour on %s; the 6th is refused by the core", peak, server)
			w.say(now, msg)
			w.note("%s", msg)
			warned = true
		}
		if w.opts.capFail {
			return stopf(exitRaidWalkInstanceCap, "instance cap: the hired accounts are at %d in the last hour on %s (--instance-cap-fail)", peak, server)
		}
		left := w.opts.capWait - now.Sub(started)
		if wait <= 0 {
			wait = capWaitMargin
		}
		if left <= 0 {
			msg := fmt.Sprintf("instance cap: waited %s (--instance-cap-wait), a hired account is still at %d on %s", w.opts.capWait, peak, server)
			if !w.opts.capUnsafe {
				w.say(now, msg)
				w.note("%s", msg)
				return stopf(exitRaidWalkInstanceCap, "%s: no fresh instance is started without a reservation (--instance-cap-unsafe goes on anyway)", msg)
			}
			w.say(now, msg+"; --instance-cap-unsafe: going on without a reservation")
			w.note("%s; --instance-cap-unsafe: going on", msg)
			return nil
		}
		if wait > left {
			wait = left
		} else {
			wait += capWaitMargin
		}
		w.say(now, fmt.Sprintf("instance cap: waiting %s for a hired account's oldest instance to age out of the hour", wait.Round(time.Second)))
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
	e := raidwalk.CapEntry{At: now.UTC(), Plan: w.plan.Name, Accounts: w.hireAccounts}
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
