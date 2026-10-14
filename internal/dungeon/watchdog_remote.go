package dungeon

import (
	"context"
	"fmt"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The remote driver's side of the watchdog (watchdog.go).

// wdShortLeg is the leg (yards) of the move a stalled walk is sent again with.
const wdShortLeg = 40

func (d *RemoteDriver) initWatchdog() {
	d.wd = newWatchdog(watchdogFor(d.plan, d.cfg), d.clk.Now, wdHooks{
		look: d.wdLook,
		event: func(name string, data map[string]any) {
			d.tr.Event(name, data)
		},
		note:   d.rep.note,
		silent: d.wdSilent,
	})
	d.rep.wd = d.wd
}

// wdLook is the Stepper's position and the lead bot's.
func (d *RemoteDriver) wdLook() (wdPos, Point, bool) {
	if d.st == nil {
		return wdPos{}, Point{}, false
	}
	lead, ok := d.leadPos()
	if !ok {
		return wdPos{}, Point{}, false
	}
	leg, ph, idx := d.st.Position()
	return wdPos{leg, ph, idx}, lead, true
}

// hostilesHP is each hostile's health percent, for the watchdog's per-mob lows (watchdog.lowLocked).
func hostilesHP(hs []tbm.HostileRec) map[packet.GUID]int {
	out := make(map[packet.GUID]int, len(hs))
	for _, h := range hs {
		out[h.GUID] = max(h.HPPct, 0)
	}
	return out
}

// feedQuiet says whether the feed has gone quiet: no frame yet, or none for FeedStale.
func (d *RemoteDriver) feedQuiet() bool {
	return d.haveFr && (d.stalled || d.clk.Now().Sub(d.nAt) > d.opt.FeedStale)
}

// resendView sends `commander view on` again: a binding that survived an earlier run, or a module that dropped the feed, may have it off.
func (d *RemoteDriver) resendView(ctx context.Context) (string, error) {
	d.viewResent = true
	r, err := d.g.Command(ctx, "commander view on")
	switch {
	case err != nil:
		return "", fmt.Errorf("commander view on: %w", err)
	case r.Kind == tbm.KindErr && r.Err != nil:
		return "sent commander view on again; it was refused: " + describeBindErr(r.Err), nil
	}
	return "sent commander view on again", nil
}

// resendViewIfSilent re-sends the view once when the feed has been quiet for half of FeedLost, before the run is failed for it.
func (d *RemoteDriver) resendViewIfSilent(ctx context.Context) {
	if d.viewResent || !d.haveFr || d.unbinding || d.clk.Now().Sub(d.nAt) <= d.opt.FeedLost/2 {
		return
	}
	what, err := d.resendView(ctx)
	if err != nil {
		what = err.Error()
	}
	d.rep.note(fmt.Sprintf("the feed has been silent for %.0f s: %s", d.clk.Now().Sub(d.nAt).Seconds(), what))
}

// wdSilent is the recovery of a stall whose feed is silent: the view is switched on again. Whatever the phase was waiting for, it waits
// on frames.
func (d *RemoteDriver) wdSilent(ctx context.Context) (string, bool, error) {
	if !d.feedQuiet() {
		return "", false, nil
	}
	what, err := d.resendView(ctx)
	return "the feed is silent: " + what, true, err
}

// wdReplanWalk is the recovery of a walk that stalled: the walk is planned again from where the lead bot stands now, with a shorter
// leg than the walk's own.
//
// While the party fights (a pack that came at it on the way, before the walk's loop has seen it) no move is sent: it would drag the lead
// bot off the fight. The walk's loop fights it and walks on after it.
func (d *RemoteDriver) wdReplanWalk(ctx context.Context, p Point) (string, error) {
	if d.haveFr && len(d.fr.EngagedHostiles()) > 0 {
		return "the party is fighting: no move sent (the walk goes on after the fight)", nil
	}
	here, ok := d.leadPos()
	if !ok {
		return "the lead bot is not in the feed: nothing to plan from", nil
	}
	wp := d.waypoint(ctx, here, p, wdShortLeg)
	r, err := d.order(ctx, "move", moveArgs(wp)...)
	if err != nil {
		return "", err
	}
	if ref, bad := refusedOf(r); bad {
		return fmt.Sprintf("planned the walk again from %.0f %.0f %.0f: cmd move %.0f %.0f %.0f was refused (%s)", here.X, here.Y, here.Z, wp.X, wp.Y, wp.Z, ref.Code), nil
	}
	return fmt.Sprintf("planned the walk again from %.0f %.0f %.0f: cmd move %.0f %.0f %.0f (a leg of at most %d yd)", here.X, here.Y, here.Z, wp.X, wp.Y, wp.Z, wdShortLeg), nil
}

// wdRecoverStep is the recovery of a boss step that has none of its own deeper in: the retries of the carrier's loot order are cancelled.
func (d *RemoteDriver) wdRecoverStep(k ActionKind) func(context.Context) (string, error) {
	if k != ActKill {
		return nil
	}
	return func(context.Context) (string, error) {
		d.wdObject.set()
		return "cancelled the carrier order's retries", nil
	}
}
