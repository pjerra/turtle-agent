package dungeon

import (
	"fmt"
	"time"
)

// RezBrain is the optional half of Brain (the leader's bot brain, step 4e): CanRez says the brain can resurrect a dead member of the
// party out of combat now (its class has a resurrection and it sees a dead member in reach or near). A brain without it never resurrects.
type RezBrain interface {
	CanRez() bool
}

// rezWatch is a rest gate's memory of the wait for the brain's resurrection.
type rezWatch struct {
	since time.Time // the brain was given the dead members from here
	probe bool      // the brain's first look has been given its poll
}

// brainRezWaiting is the rest gate's first answer to a dead member: while the leader's brain can resurrect it, the gate waits for
// that (bounded by Config.BrainRezWait from the first dead member it saw) instead of reviving by GM, ordering the corpse runs of a
// wipe recovery or stopping the run. It reports whether to go on waiting. A brain started at this very gate has not looked at the
// party yet, so the first answer "cannot" is given one poll to change; the next one stands.
func (d *Driver) brainRezWaiting(w *rezWatch) bool {
	rb, ok := d.cfg.Brain.(RezBrain)
	if !ok {
		return false
	}
	now := d.clk.Now()
	if !rb.CanRez() {
		if w.since.IsZero() && !w.probe {
			w.probe = true
			return true
		}
		return false
	}
	if w.since.IsZero() {
		w.since = now
		d.rep.note(fmt.Sprintf("leg %d: dead members %v; waiting up to %.0f s for the leader's brain to resurrect them", max(d.curLeg, 0), d.membersDead(), d.cfg.BrainRezWait.Seconds()))
	}
	return now.Sub(w.since) < d.cfg.BrainRezWait
}
