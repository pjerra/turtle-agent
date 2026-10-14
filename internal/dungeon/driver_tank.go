package dungeon

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Tank first, in group mode: the leader targets what the party attacks, so it targets what the tank holds best, leaves crowd-controlled
// mobs alone and has the tank pick up a mob that hits someone else. Behaviour after mod-playerbots (ThreatStrategy, DpsTargetValue,
// TankTargetValue, AttackersValue: AGPL, ideas only; nothing is copied). A 1.12 client has no threat list, so "the tank holds it"
// is read from the units: the mob's target is the tank, the tank's target is the mob. (The remote leader has the real list: remote_tank.go.)

const (
	groupOpenerDelay = 2 * time.Second // the least time the tank has held a mob before the first attack order at it
	groupAddCooldown = 8 * time.Second // the least time between two tank orders at the same loose add
)

// MarkSource is the optional part of a Game that tells the group's raid icons by name (skull, moon, ...) and the unit each is on.
type MarkSource interface {
	RaidMarks() map[string]packet.GUID
}

// TankFirstReport is the report's part for the tank-first rules of a group run (Report.TankFirst; a remote run has the same counters
// in Report.Remote). TankThreatShare is the mean share of the fighting mobs that were on the tank, in percent, over TankThreatSamples
// looks: the units carry no threat list, so it is the mobs' targets, not the threat.
type TankFirstReport struct {
	Tank              string  `json:"tank,omitempty"`
	TankThreatShare   float64 `json:"tank_threat_share"`
	TankThreatSamples int     `json:"tank_threat_samples"`
	LooseAddOrders    int     `json:"loose_add_orders"`
	CCRespected       int     `json:"cc_respected"`
	TargetSwitches    int     `json:"target_switches"`
}

// looseAnswerWindow is how long after a tank order at a loose add a refusal of a "pull" is taken for its answer.
const looseAnswerWindow = 5 * time.Second

// takeLooseAnswer says whether a refused "pull" at now answers a tank order at a loose add (one sent within looseAnswerWindow), and
// uses that order up. The fight's own pull is refused within its first looks, long before the first loose-add order (looseGrace).
func (t *groupTank) takeLooseAnswer(now time.Time) bool {
	t.looseSent = slices.DeleteFunc(t.looseSent, func(at time.Time) bool { return now.Sub(at) > looseAnswerWindow })
	if len(t.looseSent) == 0 {
		return false
	}
	t.looseSent = t.looseSent[1:]
	return true
}

// groupTank is the group driver's state for the rules.
type groupTank struct {
	focus      packet.GUID
	heldAt     map[packet.GUID]time.Time
	sent       map[packet.GUID]bool
	ccNoted    map[packet.GUID]bool
	addAt      map[packet.GUID]time.Time
	addN       map[packet.GUID]int
	looseSent  []time.Time // when the tank orders at loose adds went; a refused "pull" soon after one answers it, not the fight's own pull
	shareSum   float64
	shareCount int
}

func (t *groupTank) init() {
	if t.heldAt == nil {
		t.heldAt, t.sent, t.ccNoted = map[packet.GUID]time.Time{}, map[packet.GUID]bool{}, map[packet.GUID]bool{}
		t.addAt, t.addN = map[packet.GUID]time.Time{}, map[packet.GUID]int{}
	}
}

func (d *Driver) tankReport(f func(*TankFirstReport)) {
	d.rep.update(func(r *Report) {
		if r.TankFirst == nil {
			r.TankFirst = &TankFirstReport{}
		}
		f(r.TankFirst)
	})
}

// groupTankOf finds the tank among the units: a warrior, paladin or druid of the party (the first of them by that order) whose target
// is a living hostile mob in combat, else the first of them by that order. The leader is not among the units.
func (d *Driver) groupTankOf(units []objects.Unit) (objects.Unit, bool) {
	var cands []objects.Unit
	for _, u := range units {
		if u.Type == "player" && d.party[u.GUID] && !u.Dead && !u.Ghost && tankClassRank(u.Class) < 3 {
			cands = append(cands, u)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return tankClassRank(cands[i].Class) < tankClassRank(cands[j].Class) })
	for _, c := range cands {
		for _, m := range units {
			if m.GUID == c.Target && m.Type != "player" && m.Hostile && !m.Dead && m.InCombat {
				return c, true
			}
		}
	}
	if len(cands) > 0 {
		return cands[0], true
	}
	return objects.Unit{}, false
}

// tankClassRank orders the classes by how likely they are to be the tank: warrior, paladin, druid (3: none of them).
func tankClassRank(class uint8) int {
	switch class {
	case 1:
		return 0
	case 2:
		return 1
	case 11:
		return 2
	}
	return 3
}

// ccdUnit says whether the party leaves the mob alone: it carries a crowd-control aura or the moon icon.
func (d *Driver) ccdUnit(u objects.Unit) bool {
	if u.CrowdControlled() {
		return true
	}
	if src, ok := d.g.(MarkSource); ok {
		return src.RaidMarks()["moon"] == u.GUID && u.GUID != 0
	}
	return false
}

// respectCCUnits drops the crowd-controlled and moon-marked mobs, counting each once in cc_respected; when nothing else is left the
// list stays: the mob is the last of the fight.
func (d *Driver) respectCCUnits(mobs []objects.Unit) []objects.Unit {
	d.tg.init()
	free := make([]objects.Unit, 0, len(mobs))
	var held []objects.Unit
	for _, u := range mobs {
		if d.ccdUnit(u) {
			held = append(held, u)
		} else {
			free = append(free, u)
		}
	}
	if len(free) == 0 {
		return mobs
	}
	for _, u := range held {
		if !d.tg.ccNoted[u.GUID] {
			d.tg.ccNoted[u.GUID] = true
			d.tankReport(func(r *TankFirstReport) { r.CCRespected++ })
		}
	}
	return free
}

// holdScore says how well the tank holds the mob: 3 when the mob hits the tank and the tank targets it, 2 when the mob hits the tank,
// 1 when the tank targets it, 0 otherwise.
func holdScore(tank, u objects.Unit) int {
	s := 0
	if u.Target == tank.GUID {
		s += 2
	}
	if tank.Target == u.GUID {
		s++
	}
	return s
}

// rankGroup orders the mobs the way the party takes them: the mob the tank holds best first, the nearest after; the mob fought
// already stays first while the tank holds it. Without a tank: the nearest first.
func (d *Driver) rankGroup(mobs []objects.Unit, tank objects.Unit, haveTank bool) []objects.Unit {
	out := slices.Clone(mobs)
	sort.SliceStable(out, func(i, j int) bool {
		if haveTank {
			if a, b := holdScore(tank, out[i]), holdScore(tank, out[j]); a != b {
				return a > b
			}
		}
		return out[i].Distance < out[j].Distance
	})
	if f := d.tg.focus; f != 0 {
		if i := slices.IndexFunc(out, func(u objects.Unit) bool { return u.GUID == f }); i > 0 && (!haveTank || holdScore(tank, out[i]) >= 2) {
			u := out[i]
			copy(out[1:i+1], out[:i])
			out[0] = u
		}
	}
	return out
}

// pickAssist chooses the mob the leader targets for the party's attack order: crowd-controlled mobs out, the rest by how well the tank
// holds them. A mob the tank has held for less than groupOpenerDelay and the party was not sent at yet is passed over (the opener).
// ok is false when no mob is ready.
func (d *Driver) pickAssist(mobs []objects.Unit) (objects.Unit, bool) {
	d.tg.init()
	if len(mobs) == 0 {
		return objects.Unit{}, false
	}
	now := d.clk.Now()
	tank, haveTank := d.groupTankOf(d.g.Units())
	for _, u := range d.rankGroup(d.respectCCUnits(mobs), tank, haveTank) {
		if haveTank && u.Target == tank.GUID && !d.tg.sent[u.GUID] {
			if _, ok := d.tg.heldAt[u.GUID]; !ok {
				d.tg.heldAt[u.GUID] = now
			}
			if now.Sub(d.tg.heldAt[u.GUID]) < groupOpenerDelay {
				continue
			}
		}
		return u, true
	}
	return objects.Unit{}, false
}

// assistOrder targets u and says "attack" in party chat, noting the switch of focus. It reports whether the order went.
func (d *Driver) assistOrder(ctx context.Context, u objects.Unit, among []objects.Unit) (bool, error) {
	if err := d.g.Target(ctx, u.GUID); err != nil {
		return false, nil
	}
	if err := d.order(ctx, "attack"); err != nil {
		return false, err
	}
	d.tg.init()
	d.tg.sent[u.GUID] = true
	if prev := d.tg.focus; prev != 0 && prev != u.GUID && slices.ContainsFunc(among, func(x objects.Unit) bool { return x.GUID == prev && !x.Dead }) {
		d.tankReport(func(r *TankFirstReport) { r.TargetSwitches++ })
	}
	d.tg.focus = u.GUID
	return true, nil
}

// focusGoneIn says whether the mob the party was last sent at is no longer among the mobs.
func (d *Driver) focusGoneIn(mobs []objects.Unit) bool {
	f := d.tg.focus
	return f != 0 && !slices.ContainsFunc(mobs, func(u objects.Unit) bool { return u.GUID == f })
}

// tankFirstGroup runs at every look of a fight: it samples how many of the mobs are on the tank and sends the tank at a loose add (a mob
// hitting a party member that is not the tank): the leader targets it and says ".bot action pull 0" (the tank walks up and keeps the mob,
// briefly holding the others), then targets the mob of the party's attack again. At most one order per look; groupAddCooldown apart for the
// same mob, three for the fight.
func (d *Driver) tankFirstGroup(ctx context.Context, mobs []objects.Unit) error {
	d.tg.init()
	units := d.g.Units()
	tank, ok := d.groupTankOf(units)
	if !ok || len(mobs) == 0 {
		return nil
	}
	now := d.clk.Now()
	on, n := 0, 0
	for _, u := range mobs {
		if d.ccdUnit(u) {
			continue
		}
		n++
		if u.Target == tank.GUID {
			on++
		}
	}
	if n > 0 {
		d.tg.shareSum += 100 * float64(on) / float64(n)
		d.tg.shareCount++
		mean, count, name := math.Round(d.tg.shareSum/float64(d.tg.shareCount)*10)/10, d.tg.shareCount, tank.Name
		d.tankReport(func(r *TankFirstReport) { r.TankThreatShare, r.TankThreatSamples, r.Tank = mean, count, name })
	}
	for _, u := range d.respectCCUnits(mobs) {
		if u.Target == 0 || u.Target == tank.GUID || !d.party[u.Target] || !u.InCombat || u.Distance < 0 || u.Distance > 40 {
			continue
		}
		if at, seen := d.tg.addAt[u.GUID]; !seen {
			d.tg.addAt[u.GUID] = now.Add(-groupAddCooldown + looseGrace) // the first order waits looseGrace from the first sight
			continue
		} else if now.Sub(at) < groupAddCooldown || d.tg.addN[u.GUID] >= maxAddOrders {
			continue
		}
		d.tg.addAt[u.GUID] = now
		d.tg.addN[u.GUID]++
		d.tg.looseSent = append(d.tg.looseSent, now)
		d.tankReport(func(r *TankFirstReport) { r.LooseAddOrders++ })
		d.rep.note(fmt.Sprintf("leg %d: %s is on a bot that is not the tank; the tank is sent at it", d.curLeg, u.Name))
		if err := d.g.Target(ctx, u.GUID); err != nil {
			return nil
		}
		if err := d.order(ctx, "pull 0"); err != nil {
			return err
		}
		if f := d.tg.focus; f != 0 && f != u.GUID {
			_ = d.g.Target(ctx, f) // the party's mob stays the leader's target
		}
		return nil
	}
	return nil
}
