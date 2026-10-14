package dungeon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// When is a pull group gone? The feed lists only what the bots see (their line of sight, a frame cut short drops hostiles), so a group
// that is not listed may well be alive out of sight: deploy-19h skipped all ten groups of a leg that way and the walk after it met two
// packs (remote Uldaman gap G3). A group is counted cleared only with evidence, per position of the group:
//   - mobs of its entries were killed near it in this run (or in this very pull);
//   - mobs of its entries near it are listed dead;
//   - a living party member stands within ClearSeeRadius of it and the (whole) frame lists no hostile of the entries; or
//   - the lead bot walked to it (bounded: once per position) and two whole frames in a row there showed none.
//
// The resume rule (Stepper.ResumeAt) believes an empty view on the same terms.

// noteKilled records where a pull killed mobs, as evidence for the groups that share the spot.
func (d *RemoteDriver) noteKilled(sp fightSpec, t tbm.HostileRec) {
	for _, e := range sp.pick {
		d.killedAt[e] = append(d.killedAt[e], hostilePoint(t))
	}
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
	if len(d.candidates(sp, sp.track)) > 0 {
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
	for _, m := range d.fr.Party {
		if !m.Dead && dist3(partyPoint(m), at) <= ClearSeeRadius {
			return true, fmt.Sprintf("a party member stood within %.0f yd and saw none", float64(ClearSeeRadius))
		}
	}
	return false, ""
}

// confirmGone decides whether a group that is not in view is gone, position by position (a kill marks only the spot it happened at via
// noteKilled: the group's other positions, round a corner, are still looked at). looked remembers the positions the
// lead bot has stood at and seen empty. It reports false (and no error) when a hostile of the group showed itself during the look: the
// caller pulls it. It fails when a position cannot be looked at (the lead bot does not get within ClearSeeRadius of it).
func (d *RemoteDriver) confirmGone(ctx context.Context, sp fightSpec, looked map[int]bool) (cleared bool, why string, err error) {
	if len(sp.anchors) == 0 {
		return true, "the plan gives no positions to look at", nil
	}
	var reasons []string
	byLooking := false
	for i, at := range sp.anchors {
		if ok, w := d.anchorGone(sp, i); ok {
			reasons = append(reasons, w)
			byLooking = byLooking || strings.HasPrefix(w, "a party member")
			continue
		}
		if looked[i] {
			reasons = append(reasons, "the lead bot stood there and saw none")
			continue
		}
		d.remote(func(r *RemoteReport) { r.PackLooks++ })
		d.rep.note(fmt.Sprintf("leg %d: nothing of %v in view; walking to its position %.0f %.0f %.0f to look", d.curLeg, sp.pick, at.X, at.Y, at.Z))
		if err := d.approach(ctx, at, ClearSeeRadius-10); err != nil {
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
		looked[i] = true
		byLooking = true
		reasons = append(reasons, "the lead bot stood there and saw none")
	}
	if byLooking {
		d.remote(func(r *RemoteReport) { r.PacksSeenEmpty++ })
	}
	return true, strings.Join(dedupe(reasons), "; "), nil
}

// lookAt waits at position i for two whole frames that list nothing of the group, with the lead bot within ClearSeeRadius of it. found is
// true when a hostile of the group showed up; seen when the two frames came.
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
		if len(d.candidates(sp, sp.track)) > 0 {
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
