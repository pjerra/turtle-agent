package dungeon

import (
	"context"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// The group driver's side of the watchdog (watchdog.go).

// watchdogFor is the stall levels of a run: the plan's own, with the run's parameter on top field by field.
func watchdogFor(plan *Plan, cfg Config) WatchdogConfig {
	var c WatchdogConfig
	if plan != nil && plan.Watchdog != nil {
		c = *plan.Watchdog
	}
	return c.Merge(cfg.Watchdog)
}

func wdPhaseName(k ActionKind) string {
	switch k {
	case ActKill:
		return "boss"
	case ActLoot, ActUse, ActItem:
		return "object step"
	}
	return string(k)
}

// wdObjectKind says whether k is an object step: its end is progress.
func wdObjectKind(k ActionKind) bool { return k == ActLoot || k == ActUse || k == ActItem }

// healthPct is the summed health percent of the units: it falls while the party wears them down.
func healthPct(us []objects.Unit) int {
	n := 0
	for _, u := range us {
		if u.MaxHealth > 0 {
			n += int(100 * uint64(u.Health) / uint64(u.MaxHealth))
		}
	}
	return n
}

func (d *Driver) initWatchdog() {
	d.wd = newWatchdog(watchdogFor(d.plan, d.cfg), d.clk.Now, wdHooks{
		look: d.wdLook,
		event: func(name string, data map[string]any) {
			d.tr.Event(name, data)
		},
		note: d.rep.note,
	})
	d.rep.wd = d.wd
}

// wdLook is the Stepper's position and the leader's.
func (d *Driver) wdLook() (wdPos, Point, bool) {
	if d.st == nil {
		return wdPos{}, Point{}, false
	}
	leg, ph, idx := d.st.Position()
	return wdPos{leg, ph, idx}, d.g.Position(), true
}

// wdRecoverStep is the recovery of an action that has none of its own deeper in: the object steps' wait for the item is given up, and
// the step then fails or goes on by what it finds in the bags.
func (d *Driver) wdRecoverStep(k ActionKind) func(context.Context) (string, error) {
	if !wdObjectKind(k) && k != ActKill {
		return nil
	}
	return func(context.Context) (string, error) {
		d.wdObject.set()
		return "gave up the wait for the step's result; the step ends with what the bags show", nil
	}
}
