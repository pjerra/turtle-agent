package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Object steps by entry (remote mode, issue #83; TortoiseBots #933, player-controls.md section 1a "Targets by entry"). The agent's
// character is not with the party, so its object store seldom holds the chest, the door or the boss's corpse of a step, and a GUID of a
// spawn is what it could not give. The module takes the object by its entry and the plan's point instead:
//
//	cmd open|loot entry:<id>@x,y,z <bot>     the server picks the nearest object (for loot also corpse) of that entry within 30 yd of the point
//	cmd use <item> <bot> entry:<id>@x,y,z
//	cmd state entry:<id>@x,y,z               what the object is doing now (a door that opens, the Seal), answered at once
//
// A run that has object steps probes this once at its start with one `cmd state` (probeEntryTargets). An answer of any kind that proves
// the module knows the verb (an ACK, `not-found`, `too-far`) turns it on: object steps are ordered by entry (the GUID of the agent's own
// copy of the object, when it has one, is the retry after `not-found`), a game-object wait polls `cmd state` instead of needing the
// object in view, and, when every object step of the plan can be named by entry, the keep-near behaviour (`.appear`, walks behind the
// party) is off, so run_gm_assists stays 0. A module that refuses the probe (`invalid`, an old module) or does not answer keeps all of
// it as it was: GUIDs, keep-near, and the wait that passes unchecked after objectWaitSettle.

// entryStatePoll is the time between two `cmd state` orders of a game-object wait.
const entryStatePoll = 2 * time.Second

// entryState is the driver's state of the entry targets.
type entryState struct {
	probed bool
	ok     bool // the module answered the probe: orders name objects by entry and waits poll cmd state
	only   bool // ok, and every object step of the plan has an entry: nothing needs the agent in view (keep-near is off)
	// The wait being polled: its step, when it was last asked, and the last answer (for the failure that names it).
	waitKey    [2]int
	waitPolled time.Time
	waitLast   string
	// The game objects (describeRef) a wait has had a `cmd state` answer for in this run: with gone_ok, not-found later counts as done.
	seen map[string]bool
}

// entryPlanScan is what the plan's object steps (from fromLeg on) offer a probe, and whether all of them can be named by entry.
type entryPlanScan struct {
	any     bool   // there is an object step (or a boss loot) at all
	lacking int    // object steps whose reference has no entry (only a position): they need the agent in view
	entry   uint32 // the probe's object
	at      *Point
}

func (d *RemoteDriver) scanEntrySteps(fromLeg int) entryPlanScan {
	var sc entryPlanScan
	offer := func(ref *ObjectRef, stand *Point) {
		sc.any = true
		if ref.Entry == 0 {
			sc.lacking++
			return
		}
		at := ref.Near
		if at == nil {
			at = stand
		}
		if sc.entry == 0 || (sc.at == nil && at != nil) {
			sc.entry, sc.at = ref.Entry, at
		}
	}
	for li := max(fromLeg, 0); li < len(d.plan.Legs); li++ {
		l := d.plan.Legs[li]
		for _, st := range append(append([]Step{}, l.Steps...), l.AfterBoss...) {
			switch {
			case st.UseObject != nil:
				offer(&st.UseObject.ObjectRef, st.UseObject.Stand)
			case st.LootObject != nil:
				offer(&st.LootObject.ObjectRef, st.LootObject.Stand)
			case st.Ritual != nil:
				at := st.Ritual.Position
				offer(&ObjectRef{Entry: st.Ritual.Entry, Near: &at}, st.Ritual.Stand)
			case st.WaitFor != nil && st.WaitFor.GameObject != nil:
				offer(&st.WaitFor.GameObject.ObjectRef, nil)
			}
		}
		if b := l.Boss; b != nil && b.LootItem != nil {
			sc.any = true
			if sc.entry == 0 {
				p := b.Position
				sc.entry, sc.at = b.Entry, &p
			}
		}
	}
	return sc
}

// probeEntryTargets asks the module one `cmd state` to learn whether it takes objects by entry (TortoiseBots #933). It runs at the start
// of a remote run whose plan has object steps; any refusal or silence leaves the GUID path and the keep-near behaviour as they were.
func (d *RemoteDriver) probeEntryTargets(ctx context.Context, fromLeg int) {
	og, ok := d.orderGame()
	sc := d.scanEntrySteps(fromLeg)
	if !ok || !sc.any || sc.entry == 0 {
		return
	}
	d.entry.probed = true
	cmd := "cmd state " + tbm.EntryArg(sc.entry, pointArr(sc.at))
	res, err := og.Order(ctx, cmd)
	var verdict string
	switch {
	case err != nil:
		verdict = "off: the probe " + cmd + " was cut short: " + err.Error()
	case res.Kind == tbm.KindAck:
		d.entry.ok = true
	case res.Err != nil && (res.Err.Code == "not-found" || res.Err.Code == "too-far"):
		d.entry.ok = true // the module knows the verb and answered about the point
	case res.Err != nil:
		verdict = fmt.Sprintf("off: %s was refused: %s (%s): an older module", cmd, res.Err.Code, res.Err.Message)
	default:
		verdict = "off: " + cmd + " got no answer: an older module"
	}
	d.remote(func(r *RemoteReport) { r.OrdersSent++; r.Orders["state"]++ })
	if !d.entry.ok {
		d.remote(func(r *RemoteReport) { r.EntryTargets = verdict })
		d.rep.note("entry targets: " + verdict + "; object steps use the agent's GUIDs, and the agent is kept near the party for them")
		return
	}
	d.entry.only = sc.lacking == 0
	d.remote(func(r *RemoteReport) { r.EntryTargets = "on" })
	if d.entry.only {
		d.rep.note(fmt.Sprintf("entry targets: the module answers %s (%s): object steps name their object by entry and point, game-object waits poll cmd state, and the agent is not kept near the party (no .appear: run_gm_assists stays 0)", cmd, answerOf(res)))
	} else {
		d.rep.note(fmt.Sprintf("entry targets: the module answers %s (%s), but %d object step(s) of the plan name no entry; the agent is still kept near the party for them", cmd, answerOf(res), sc.lacking))
	}
}

func answerOf(res tbm.OrderResult) string {
	if res.Final != "" {
		return res.Final
	}
	return res.Describe()
}

func pointArr(p *Point) *[3]float32 {
	if p == nil {
		return nil
	}
	return &[3]float32{p.X, p.Y, p.Z}
}

// entryFormOf reads the entry and the point out of an order's `entry:<id>[@x,y,z]` argument; ok is false when the command has none.
func entryFormOf(command string) (entry uint32, at *Point, ok bool) {
	for _, f := range strings.Fields(command) {
		rest, found := strings.CutPrefix(f, "entry:")
		if !found {
			continue
		}
		id, pt, _ := strings.Cut(rest, "@")
		n, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return 0, nil, false
		}
		if c := strings.Split(pt, ","); len(c) == 3 {
			var v [3]float64
			good := true
			for i := range c {
				var err error
				if v[i], err = strconv.ParseFloat(c[i], 32); err != nil {
					good = false
				}
			}
			if good {
				at = &Point{float32(v[0]), float32(v[1]), float32(v[2])}
			}
		}
		return uint32(n), at, true
	}
	return 0, nil, false
}

// entryNotFound is the failure of an order by entry that found nothing: it names the entry and the point the plan gave.
func entryNotFound(command string, res tbm.OrderResult) error {
	entry, at, _ := entryFormOf(command)
	where := "near the bots"
	if at != nil {
		where = fmt.Sprintf("within 30 yd of %.0f %.0f %.0f", at.X, at.Y, at.Z)
	}
	return fmt.Errorf("%s refused: not-found (%s): no spawned game object or corpse of entry %d %s, within the bots' reach (ReactDistance); "+
		"the plan's entry or position is wrong, the object is gone, or the party stands too far from it", res.Intent, res.Err.Message, entry, where)
}

// carryObject sends the order of an object step (`cmd open` of a chest or a door) with the verbs' build function: by entry and the
// plan's point when the action has one (the server picks the object), by the GUID of the agent's own copy otherwise. When the server finds
// nothing and the agent has a GUID, that is sent once as the fallback.
func (d *RemoteDriver) carryObject(ctx context.Context, rec *objRec, a Action, build func(arg string) string) (res tbm.OrderResult, failed bool, err error) {
	if a.SpawnGUID != 0 {
		// The plan names the exact spawn: open that one by its GUID, never a neighbour of the same entry that an entry order would pick.
		return d.carryOrFallback(ctx, rec, build(tbm.GUIDArg(a.SpawnGUID)), objectPoint(a))
	}
	if a.Entry == 0 {
		return d.carryOrFallback(ctx, rec, build(tbm.GUIDArg(a.GUID)), objectPoint(a))
	}
	res, failed, err = d.carryOrFallback(ctx, rec, build(tbm.EntryArg(a.Entry, pointArr(a.EntryAt))), objectPoint(a))
	if err == nil || a.GUID == 0 || res.Code() != "not-found" || runEnded(ctx, err) {
		return res, failed, err
	}
	d.rep.note(fmt.Sprintf("%s: nothing by entry %d; trying the GUID %s of the agent's own copy", rec.label(), a.Entry, tbm.GUIDArg(a.GUID)))
	return d.carryOrFallback(ctx, rec, build(tbm.GUIDArg(a.GUID)), objectPoint(a))
}

// ---- game-object waits ----

// wantedState is the state the plan waits for, in the module's words (`cmd state` says alternative for the plan's active_alt).
func wantedState(plan string) string {
	if s := strings.ToLower(strings.TrimSpace(plan)); s != "active_alt" {
		return s
	}
	return "alternative"
}

// waitOnState handles a game-object wait by asking the module (`cmd state entry:<id>@x,y,z` every entryStatePoll): the wait ends when the
// object is in the wanted state; not-found is "not there yet"; the step's own timeout (the Stepper's) ends the polling, and its failure
// carries the last answer (stateWaitError). The error is the run's, as objectLook's.
func (d *RemoteDriver) waitOnState(ctx context.Context, a Action, w *ObjectState) {
	og, _ := d.orderGame()
	release := d.wd.hold() // the plan's wait has its own timeout_s: the watchdog does not count it
	defer release()
	key := [2]int{a.Leg, a.Step}
	if d.entry.waitKey != key {
		d.entry.waitKey, d.entry.waitPolled, d.entry.waitLast = key, time.Time{}, ""
	}
	if now := d.clk.Now(); d.entry.waitPolled.IsZero() || now.Sub(d.entry.waitPolled) >= entryStatePoll {
		d.entry.waitPolled = now
		cmd := "cmd state " + tbm.EntryArg(w.Entry, pointArr(w.Near))
		res, err := og.Order(ctx, cmd)
		if err != nil {
			d.stepFailedRecover(ctx, fmt.Errorf("%s: %w", cmd, err))
			return
		}
		d.remote(func(r *RemoteReport) { r.OrdersSent++; r.Orders["state"]++; r.StatePolls++ })
		switch {
		case res.Kind == tbm.KindAck && res.State != nil:
			d.entry.waitLast = fmt.Sprintf("state:%s flags:%s", res.State.State, res.State.Flags)
			if d.entry.seen == nil {
				d.entry.seen = map[string]bool{}
			}
			d.entry.seen[describeRef(&w.ObjectRef)] = true
			if strings.EqualFold(res.State.State, wantedState(w.State)) {
				d.rep.note(fmt.Sprintf("%s: cmd state says %s is %s (guid %d)", a.Label, describeRef(&w.ObjectRef), res.State.State, res.State.GUID))
				d.st.SkipStep()
				d.entry.waitKey = [2]int{-1, -1}
				return
			}
		case res.Kind == tbm.KindAck:
			d.entry.waitLast = "an answer cmd state could not be read: " + res.Final
		case res.Code() == "not-found":
			d.entry.waitLast = "not-found: no spawned object of that entry there yet (or out of the bots' reach)"
			if w.GoneOK && d.entry.seen[describeRef(&w.ObjectRef)] {
				// Seen by an earlier answer and gone now: the script removed it after using it (gone_ok).
				d.rep.note(fmt.Sprintf("%s: cmd state says %s is gone after it was seen: counted as done (gone_ok)", a.Label, describeRef(&w.ObjectRef)))
				d.st.SkipStep()
				d.entry.waitKey = [2]int{-1, -1}
				return
			}
		case res.Code() == "too-far":
			d.entry.waitLast = "too-far: the point is farther from the bots than ReactDistance"
		case res.Code() == "not-bound" || res.Code() == "disabled":
			d.stepFailedRecover(ctx, fmt.Errorf("%w: %s answered %s", errBindingLost, res.Intent, res.Code()))
			return
		case res.Code() == "invalid":
			// The probe said the module knows cmd state; an answer that says otherwise means it does not (restarted on an older build, say).
			d.entry.ok, d.entry.only = false, false
			d.remote(func(r *RemoteReport) { r.EntryTargets = "off: cmd state was refused as invalid during the run" })
			d.rep.note(fmt.Sprintf("%s: cmd state was refused as invalid (%s): back to the agent's own view for object steps", a.Label, res.Err.Message))
			return
		default:
			d.entry.waitLast = res.Describe()
		}
	}
	if err := d.tick(ctx, min(d.cfg.WaitPoll, a.Remaining)); err != nil {
		d.stepFailedRecover(ctx, err)
	}
}

// stateWaitError is the failure of a game-object wait that timed out in entry mode: the Stepper's text plus what the module said last.
func (d *RemoteDriver) stateWaitError(a Action) error {
	if d.entry.waitLast == "" {
		return errors.New(a.Err)
	}
	return fmt.Errorf("%s (last cmd state answer: %s)", a.Err, d.entry.waitLast)
}

// agentGUID is the GUID the agent's own hands act on in the fallback of an object step. An action by entry may have none (the agent did
// not see the object when the step began): then the GUID the module chose for the carrier's order (ACTION_TARGET), else the object in the
// agent's store now that it stands at it, looked for a few seconds.
func (d *RemoteDriver) agentGUID(ctx context.Context, a Action, rec *objRec) (packet.GUID, error) {
	if a.GUID != 0 {
		return a.GUID, nil
	}
	if g := rec.targetGUID(); g != 0 {
		return g, nil
	}
	og, ok := d.orderGame()
	if !ok || a.Entry == 0 {
		return 0, errors.New("the agent has no GUID for the object")
	}
	for try := 0; try < 4; try++ {
		for _, o := range og.GameObjects() {
			if o.Entry == a.Entry && o.Distance >= 0 && (a.EntryAt == nil || dist3(Point{o.X, o.Y, o.Z}, *a.EntryAt) <= 30) {
				return o.GUID, nil
			}
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return 0, err
		}
	}
	return 0, fmt.Errorf("the agent stands at game object %d but does not see it, and the module named no GUID for it", a.Entry)
}
