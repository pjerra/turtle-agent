package dungeon

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The approach to an object (remote mode). The module's open order is fragile while the carrier is still walking in: the first full
// Uldaman runs sent `cmd open` with the carrier ~20 yd from Baelog's Chest, and the order failed 4 s later; even standing next to it a
// bot's own Opening cast was interrupted when the order arrived on top of a walk. So before an object order the party is walked to the
// object's stand point (the step's `stand`, else a point objectStandoff yards short of the object on the lead bot's path), told to
// stay, and the order goes to the carrier only when the carrier is near the object and has stood still for three frames of the feed (within 0.3 yd:
// in uldaman-remote-4 an order sent after two frames was interrupted by a 1.8 yd move). The order's own walk stays as the fallback: a carrier that never settles gets the order after objectApproachWait, with a note.
const (
	objectStandoff     = 3.0              // yards short of the object at which the lead bot is stood (no `stand` in the plan)
	objectNear         = 5.0              // yards from the object within which the carrier must be
	objectStillFrames  = 3                // frames in a row at the same position that make the carrier "not moving"
	objectStillEps     = 0.3              // yards of difference that still count as the same position
	objectApproachWait = 20 * time.Second // the carrier gets this long to settle, then the order goes out anyway
)

// runEnded says whether err ends the run (a binding lost, a stalled feed, a wipe, the run's own context), as opposed to a failure of
// one part that the caller can note and go on from.
func runEnded(ctx context.Context, err error) bool {
	return err != nil && (ctx.Err() != nil || errors.Is(err, errBindingLost) || errors.Is(err, errFeedLost) || errors.Is(err, errStalled) || errors.Is(err, errWipe) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

// standOf is the stand point the plan gives the object step, if any.
func (d *RemoteDriver) standOf(a Action) *Point {
	st := d.stepOf(a)
	switch {
	case st == nil:
		return nil
	case st.LootObject != nil:
		return st.LootObject.Stand
	case st.UseObject != nil:
		return st.UseObject.Stand
	}
	return nil
}

// standFor is the point the lead bot is stood at for an object at `at`: objectStandoff yards short of it on the navmesh path from the
// lead bot (the straight line without a planner); the lead bot's own place when it is that close already.
func (d *RemoteDriver) standFor(ctx context.Context, at Point) Point {
	lead, ok := d.leadPos()
	if !ok {
		return at
	}
	return d.standoffFrom(ctx, lead, at, objectStandoff)
}

// standoffFrom is the point `standoff` yards before `to` on the path from `from`; `from` when it is nearer than that.
func (d *RemoteDriver) standoffFrom(ctx context.Context, from, to Point, standoff float32) Point {
	path := []Point{from, to}
	if pl, ok := d.g.(PathPlanner); ok {
		if pts, err := pl.PlanPath(ctx, d.plan.Map, from, to); err == nil && len(pts) >= 2 {
			path = pts
		}
	}
	if p, ok := StandoffPoint(path, standoff); ok {
		return p
	}
	return from
}

// approachObject brings the party to the object at `at`, holds it there and waits for the carrier to be near and still. stayed says that a
// `cmd stay` went out, so the caller sends `cmd follow` when the step is over. An error is the run's (binding lost, a wipe); a walk
// that failed or a carrier that does not settle is noted and the order goes out anyway.
func (d *RemoteDriver) approachObject(ctx context.Context, rec *objRec, carrier string, at, stand *Point) (stayed bool, err error) {
	if at == nil {
		return false, nil
	}
	began := d.clk.Now()
	to := *at
	if stand != nil {
		to = *stand
	} else {
		d.look()
		to = d.standFor(ctx, *at)
	}
	note := func(format string, a ...any) {
		text := fmt.Sprintf(format, a...)
		rec.update(func(s *ObjectStepReport) { s.Approach = text })
		d.rep.note(fmt.Sprintf("%s: approach: %s", rec.label(), text))
		d.tr.Event("object_approach", map[string]any{"leg": d.curLeg, "carrier": carrier, "text": text, "x": at.X, "y": at.Y, "z": at.Z})
	}
	if werr := d.doWalk(ctx, to); werr != nil {
		if runEnded(ctx, werr) {
			return false, werr
		}
		d.rep.note(fmt.Sprintf("%s: approach: the party could not be walked to %.1f %.1f %.1f: %v; the order's own walk brings the carrier", rec.label(), to.X, to.Y, to.Z, werr))
	}
	r, err := d.order(ctx, "stay")
	if err != nil {
		return false, err
	}
	if ref, bad := refusedOf(r); bad {
		d.rep.note(fmt.Sprintf("%s: approach: cmd stay was refused: %s (%s)", rec.label(), ref.Code, ref.Message))
	} else {
		stayed = true
	}
	settled := d.clk.Now()
	var prev Point
	var lastN uint64
	frames := 0 // frames in a row at prev
	var dist float64
	for {
		d.look()
		m, ok := d.memberByName(carrier)
		if !ok || m.Dead || d.stale() {
			note("the carrier is not in the feed (or dead, or the feed stalled): the order goes out as it is")
			return stayed, nil
		}
		pos := partyPoint(m)
		dist = dist3(pos, *at)
		if d.n != lastN {
			lastN = d.n
			if frames > 0 && dist3(pos, prev) <= objectStillEps {
				frames++
			} else {
				prev, frames = pos, 1
			}
		}
		if frames >= objectStillFrames && dist <= objectNear {
			note("%s still %.1f yd from the object after %.1f s", carrier, dist, d.clk.Now().Sub(began).Seconds())
			return stayed, nil
		}
		if d.inCombat() {
			note("a fight began while %s was %.1f yd from the object: the order goes out as it is", carrier, dist)
			return stayed, nil
		}
		if d.clk.Now().Sub(settled) >= objectApproachWait {
			state := "moving"
			if frames >= objectStillFrames {
				state = "still"
			}
			note("%s is %s %.1f yd from the object %.0f s after the party stopped: the order goes out anyway, with its own walk", carrier, state, dist, objectApproachWait.Seconds())
			return stayed, nil
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return stayed, err
		}
	}
}

// resumeFollow sends the party back to following the lead bot after an approach held it with `cmd stay`; a refusal is noted.
func (d *RemoteDriver) resumeFollow(ctx context.Context, stayed bool) {
	if !stayed {
		return
	}
	if r, err := d.order(ctx, "follow"); err != nil {
		d.rep.note("cmd follow after the object step: " + err.Error())
	} else if ref, bad := refusedOf(r); bad {
		d.rep.note(fmt.Sprintf("cmd follow after the object step was refused: %s (%s)", ref.Code, ref.Message))
	}
}
