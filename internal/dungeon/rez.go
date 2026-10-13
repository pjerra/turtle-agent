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

// brainRezWaiting is the rest gate's first answer to a dead member: while the leader's brain can resurrect it, the gate waits for
// that (bounded by Config.BrainRezWait from the first dead member it saw) instead of reviving by GM, ordering the corpse runs of a
// wipe recovery or stopping the run. since is the gate's own memory of when the wait began; it reports whether to go on waiting.
func (d *Driver) brainRezWaiting(since *time.Time) bool {
	rb, ok := d.cfg.Brain.(RezBrain)
	if !ok || !rb.CanRez() {
		return false
	}
	now := d.clk.Now()
	if since.IsZero() {
		*since = now
		d.rep.note(fmt.Sprintf("leg %d: dead members %v; waiting up to %.0f s for the leader's brain to resurrect them", max(d.curLeg, 0), d.membersDead(), d.cfg.BrainRezWait.Seconds()))
	}
	return now.Sub(*since) < d.cfg.BrainRezWait
}
