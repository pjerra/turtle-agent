package dungeon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// When is a pull group gone? The feed lists only what the bots see (their line of sight, a frame cut short drops hostiles), so a group
// that is not listed may well be alive out of sight: deploy-19h skipped all ten groups of a leg that way and the walk after it met two
// packs (remote Uldaman gap G3). A group is counted cleared only with evidence, per position of the group:
//   - units of the entries the step picks died near it in a won pull of this run (noteKilled: only the units that died, where they died);
//   - units of those entries near it are listed dead;
//   - in a whole frame, a living member of the encounter that the step does not pick (the boss, on a First add's step) stands there and
//     none of the picked entries is in view: the bots have the spot in sight;
//   - a living party member stands within ClearSeeRadius of it and the (whole) frame lists none of the picked entries;
//   - a pull of a mob that stood there failed (no fight, evaded, refused): the bots saw the spot, and the give-up note names the mob; or
//   - the lead bot walked to it (bounded: once per position) and two whole frames in a row there showed none.
//
// "Of the group" is always the entries the step picks (sp.pick), the same set pickTarget takes its target from: a First add's step that
// counted the boss standing at the position as "found" could never pick it, and looped until its pulls ran out. The boss is its own
// step. A required step (the boss) is never cleared by its position alone: the boss must be known dead (bossDead).
//
// The resume rule (Stepper.ResumeAt) believes an empty view on the same terms.

// deadAt is a unit a fight killed: its entry, and where the feed last showed it.
type deadAt struct {
	entry uint32
	at    Point
	guid  packet.GUID // the corpse keeps it: a loot order names it
}

// clearing is what one engage has learnt about the positions of its group.
type clearing struct {
	looked map[int]bool // positions the lead bot stood at and saw empty
	given  []Point      // where the mobs whose pulls failed stood (seen there; the give-up note names them)
}

func newClearing() *clearing { return &clearing{looked: map[int]bool{}} }

// noteKilled records where a won pull's units died, as evidence for the groups that share the spot. A pull that was not won (evaded, no
// fight) proves nothing about the spot it was sent at: its pulled mob is alive.
func (d *RemoteDriver) noteKilled(res fightResult) {
	if res.out != won {
		return
	}
	for _, u := range res.dead {
		d.killedAt[u.entry] = append(d.killedAt[u.entry], u.at)
		if u.guid != 0 {
			if d.killedGUID == nil {
				d.killedGUID = map[packet.GUID]bool{}
			}
			d.killedGUID[u.guid] = true
		}
		d.noteCorpse(u)
	}
}

// bossDead says whether a unit of the entries the step picks (the boss) is known dead: killed in a won pull of this run (its own, or a
// First add's it joined), fought and then gone in any fight of the run (the kill ledger), or listed dead in the feed (near the position; for a boss encounter within 80 yd of the lead bot, like the
// group driver's deadInView).
func (d *RemoteDriver) bossDead(sp fightSpec) (bool, string) {
	for _, e := range sp.pick {
		if len(d.killedAt[e]) > 0 {
			return true, "killed in this run"
		}
	}
	// It may have died in another group's fight (a joiner that pull did not track): the kill ledger saw it fight and then die or go.
	if d.ledgerDead(sp.pick) {
		return true, "killed in this run, in another fight (seen fighting, then dead or gone from two whole frames)"
	}
	// Or its corpse stands in the agent's own view (a boss killed before this driver ran: a resume).
	if d.bossCorpseSeen(sp) {
		return true, "its corpse is in the agent's view"
	}
	for _, h := range d.fr.Hostiles {
		if h.Entry == 0 || h.HPPct > 0 || !slices.Contains(sp.pick, h.Entry) {
			continue
		}
		if ((sp.wide || len(sp.anchors) == 0) && distToLead(d, hostilePoint(h)) <= 80) || d.nearAnchor(sp, h) {
			return true, "seen dead"
		}
	}
	return false, ""
}

// anchorGone says whether position i of the group is known to be empty without walking there, and on what evidence.
func (d *RemoteDriver) anchorGone(sp fightSpec, i int) (bool, string) {
	at, r := sp.anchors[i], float64(d.groupRadius(sp))
	for _, e := range sp.pick {
		for _, p := range d.killedAt[e] {
			if dist3(p, at) <= r {
				return true, "killed earlier in this run"
			}
		}
	}
	if len(d.candidates(sp, sp.pick)) > 0 {
		return false, ""
	}
	for _, h := range d.fr.Hostiles {
		if h.Entry != 0 && h.HPPct <= 0 && slices.Contains(sp.pick, h.Entry) && dist3(hostilePoint(h), at) <= r {
			return true, "seen dead"
		}
	}
	if !d.haveFr || d.stale() || d.fr.Truncated {
		return false, ""
	}
	for _, h := range d.fr.Hostiles {
		if h.Entry != 0 && h.HPPct > 0 && slices.Contains(sp.track, h.Entry) && !slices.Contains(sp.pick, h.Entry) && dist3(hostilePoint(h), at) <= r {
			return true, fmt.Sprintf("the bots see %d standing there and none of %v", h.Entry, sp.pick)
		}
	}
	for _, m := range d.fr.Party {
		if !m.Dead && dist3(partyPoint(m), at) <= ClearSeeRadius {
			return true, fmt.Sprintf("a party member stood within %.0f yd and saw none", float64(ClearSeeRadius))
		}
	}
	return false, ""
}

// positionSure says whether position i has evidence, from the feed now or from this engage (looked at, a failed pull there).
func (d *RemoteDriver) positionSure(sp fightSpec, i int, cl *clearing) (bool, string) {
	if ok, w := d.anchorGone(sp, i); ok {
		return true, w
	}
	if cl.looked[i] {
		return true, "the lead bot stood there and saw none"
	}
	for _, p := range cl.given {
		if dist3(p, sp.anchors[i]) <= float64(d.groupRadius(sp)) {
			return true, "a mob given up on stood there"
		}
	}
	return false, ""
}

// unseenPositions lists the positions of the group without evidence.
func (d *RemoteDriver) unseenPositions(sp fightSpec, cl *clearing) []string {
	var out []string
	for i, at := range sp.anchors {
		if ok, _ := d.positionSure(sp, i, cl); !ok {
			out = append(out, fmt.Sprintf("%.0f %.0f %.0f", at.X, at.Y, at.Z))
		}
	}
	return out
}

// confirmGone decides whether a group that is not in view is gone, position by position (a kill marks only the spot it happened at via
// noteKilled: the group's other positions, round a corner, are still looked at). It reports false (and no error) when a hostile of the
// group showed itself during the look: the caller pulls it. It fails when a position cannot be looked at (the lead bot does not get
// within ClearSeeRadius of it).
func (d *RemoteDriver) confirmGone(ctx context.Context, sp fightSpec, cl *clearing) (cleared bool, why string, err error) {
	if len(sp.anchors) == 0 {
		return true, "the plan gives no positions to look at", nil
	}
	var reasons []string
	byLooking := false
	for i, at := range sp.anchors {
		if ok, w := d.positionSure(sp, i, cl); ok {
			reasons = append(reasons, w)
			byLooking = byLooking || strings.HasPrefix(w, "a party member")
			continue
		}
		d.remote(func(r *RemoteReport) { r.PackLooks++ })
		d.rep.note(fmt.Sprintf("leg %d: nothing of %v in view; walking to %.0f yd of its position %.0f %.0f %.0f to look", d.curLeg, sp.pick, float64(LookStandoff), at.X, at.Y, at.Z))
		// The look stops out of the pack's aggro range (remote_gather.go): 15 yd short of it (ZF-1) pulled the pack and two more.
		if err := d.approachArrive(ctx, at, LookStandoff, lookArrive); err != nil {
			return false, "", err
		}
		found, seen, err := d.lookAt(ctx, sp, i)
		if err != nil {
			return false, "", err
		}
		if found {
			return false, "", nil
		}
		if !seen {
			here, _ := d.leadPos()
			return false, "", fmt.Errorf("could not get a view of the pack at %.0f %.0f %.0f (the lead bot is %.0f yd away): not counting it as cleared", at.X, at.Y, at.Z, dist3(here, at))
		}
		cl.looked[i] = true
		byLooking = true
		reasons = append(reasons, "the lead bot stood there and saw none")
	}
	if byLooking {
		d.remote(func(r *RemoteReport) { r.PacksSeenEmpty++ })
	}
	return true, strings.Join(dedupe(reasons), "; "), nil
}

// lookAt waits at position i for two whole frames that list nothing the step can pick, with the lead bot within ClearSeeRadius of it.
// found is true when a hostile of those entries showed up (pickTarget can then pick it); seen when the two frames came.
func (d *RemoteDriver) lookAt(ctx context.Context, sp fightSpec, i int) (found, seen bool, err error) {
	at := sp.anchors[i]
	clean, last := 0, d.n
	for polls := 0; polls < 10 && clean < 2; polls++ {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return false, false, err
		}
		if d.stale() || d.n == last {
			continue
		}
		last = d.n
		if len(d.candidates(sp, sp.pick)) > 0 {
			return true, false, nil
		}
		if l, ok := d.leadPos(); d.fr.Truncated || !ok || dist3(l, at) > ClearSeeRadius {
			continue
		}
		clean++
	}
	return false, clean >= 2, nil
}

func dedupe(in []string) []string {
	var out []string
	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// gatedLook is the look before a part gated to the other team is skipped (Action.TeamGate). The feed lists only the hostiles the bots
// see, so a gated pack that is not listed may stand out of sight, hostile: the Lost Dwarves stand 3-4 yd from the chest the next step
// opens. The part is skipped only on the evidence a pack needs to count as cleared (confirmGone: a living bot within ClearSeeRadius of
// each position and a whole frame that lists none of its mobs, else the lead bot walks up to look, once per position); a mob of it
// in view, or one that shows itself on the look, makes it a normal fight. skip is true when the step is over.
func (d *RemoteDriver) gatedLook(ctx context.Context, a Action) (skip bool, err error) {
	if a.TeamGate == "" {
		return false, nil
	}
	var sp fightSpec
	switch {
	case a.Kind == ActPull:
		sp = fightSpec{pick: a.Entries, track: a.Entries, skip: a.Skip, anchors: a.Positions, radius: a.Radius, sneak: true, passed: map[packet.GUID]bool{}}
	case a.Kind == ActKill && a.Boss != nil:
		enc := append(slices.Clone(a.Boss.First), a.Boss.Entry)
		sp = fightSpec{pick: enc, track: enc, skip: a.Skip, anchors: []Point{a.Boss.Position}, wide: true, passed: map[packet.GUID]bool{}}
	default:
		return false, nil
	}
	fight := func(why string) {
		d.noteTeamGate(a, fmt.Sprintf("%s, but its mobs are hostile to the party (%s): fought", a.TeamGate, why))
	}
	if len(d.candidates(sp, sp.pick)) > 0 {
		fight("in view")
		return false, nil
	}
	cleared, why, err := d.confirmGone(ctx, sp, newClearing())
	if err != nil {
		return false, fmt.Errorf("%s: not skipping it blind, and it could not be looked at: %w", a.TeamGate, err)
	}
	if !cleared {
		fight("seen on the look")
		return false, nil
	}
	d.noteTeamGate(a, fmt.Sprintf("%s: skipped (looked: %s)", a.TeamGate, why))
	return true, nil
}

func (d *RemoteDriver) noteTeamGate(a Action, text string) {
	d.rep.note(text)
	action := "skip_team"
	if strings.HasSuffix(text, ": fought") {
		action = "team_override"
	}
	d.emit(events.KindDungeonStep, text, map[string]any{"leg": a.Leg, "action": action, "ok": true, "mode": "remote"})
}
