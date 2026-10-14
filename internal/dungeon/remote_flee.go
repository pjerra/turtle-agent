package dungeon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Runners and bodies, in remote mode.
//
// A mob that flees at low health (EventAI "flee at 15 % HP": Uldaman's Stonevault Geomancers, the Shadowforge casters, the Flameweavers)
// runs off, finds friends and comes back with them. The fight used to count such a mob as killed when it left the feed (it was
// "fought, nearly dead, gone"), the kill ledger did the same, and the next pull walked up to a group with the runner and its friends
// about to arrive (Uldaman A run 6, Grimlok's corridor). So:
//
//   - a runner is a mob the party has fought, at or below FleePct of its health, that moves away from the party (or a creature the
//     plan lists in `fleers` that the feed shows feared, at a little more health). While it is in view the party is ordered at it
//     (the damage dealers, the tank and the healer keep their own work), every fleeOrderEvery, so that it dies before it is out of reach;
//   - a runner that leaves the feed alive is not a kill, not in the fight and not in the ledger: it is a fled mob. The next pull does not
//     start while one last seen within FleeNearYd of the lead bot may still come back: the party waits for it where it stands, at most
//     FleeWait from the moment it was lost, and fights it (and what comes with it) at the gather spot, with the tank in front;
//   - bodies: a dead bot is raised in place by a caster of the party only when no live hostile stands within ReviveSafeYd of the bodies.
//     Else the wait for the resurrection is skipped and the bot is released with `cmd revive` (it runs back from the graveyard, which
//     gives the mobs time to leave): a resurrection next to mobs that have not reset put the party back on the floor within 30 s.

const (
	fleePct         = 20                      // percent: a runner is at most this healthy (a plan's `fleers` entry: fleePct+fleeKnownExtra)
	fleeWait        = 30 * time.Second        // how long a pull waits for a runner that got away
	fleeNearYd      = 40                      // yards: a runner lost within this of the lead bot is waited for
	reviveSafeYd    = 30                      // yards: no live hostile this near the bodies for a resurrection in place
	fleeStepYd      = 1.5                     // yards a mob must have gained on the party between two frames to be moving away
	fleeChaseYd     = 35                      // yards: a runner farther than this from the party is let go (it is not chased out of the group)
	fleeKnownPct    = 15                      // extra percent of health a known runner (a plan's `fleers` entry) is taken for one at
	fleeOrderMax    = 6                       // attack orders at one runner
	fleeOrderGap    = 2 * time.Second         // least time between two of them
	fleeCap         = 8                       // fled mobs remembered
	fleeDyingPct    = 5                       // percent: a runner gone at this health or less, with the party's hands on it, died
	fleeShotYd      = 30                      // yards: a bot this near a runner that went at fleeDyingPct, ordered at it, would have killed it
	fleeReachYd     = 10                      // yards: a bot this near saw it die without an order
	fleeOrderRecent = 6 * time.Second         // an order at the runner this young counts as the party's hands on it
	fleeRateAge     = 1500 * time.Millisecond // a drop of health this old still says how fast the party was killing it
	fleeAbsent      = 2                       // frames in a row a runner is out of the feed before it counts as fled
	fleeHardCap     = 90 * time.Second        // the longest a wait for runners goes on, fights with what comes back included
)

// fleeSeen is where a mob stood in the frame before and how far from the nearest living bot.
type fleeSeen struct {
	at   Point
	away float64
	n    uint64
	hp   int
	when time.Time
	rate float64   // percent of health lost per second at the last drop it showed
	drop time.Time // ... and when that drop was seen
}

// fledMob is a runner that left the feed alive.
type fledMob struct {
	guid  packet.GUID
	entry uint32
	last  tbm.HostileRec
	since time.Time
	near  bool // within FleeNearYd of the lead bot when it went: worth a wait
}

// fleeState is the driver's memory of runners; its maps are made on first use.
type fleeState struct {
	prev    map[packet.GUID]fleeSeen
	running map[packet.GUID]tbm.HostileRec // the runners as the last frame that showed them left them
	absent  map[packet.GUID]int
	fled    map[packet.GUID]*fledMob
	orderAt map[packet.GUID]time.Time
	orderN  map[packet.GUID]int
	noted   map[packet.GUID]bool
}

func (f *fleeState) init() {
	if f.prev == nil {
		f.prev, f.running, f.absent = map[packet.GUID]fleeSeen{}, map[packet.GUID]tbm.HostileRec{}, map[packet.GUID]int{}
		f.fled, f.orderAt, f.orderN, f.noted = map[packet.GUID]*fledMob{}, map[packet.GUID]time.Time{}, map[packet.GUID]int{}, map[packet.GUID]bool{}
	}
}

// isFleer says whether the plan lists the entry as one that runs at low health.
func (d *RemoteDriver) isFleer(entry uint32) bool {
	return d.plan != nil && slices.Contains(d.plan.Fleers, entry)
}

// nearestBotYd is the distance from p to the nearest living bot of the party; -1 when none is alive.
func (d *RemoteDriver) nearestBotYd(p Point) float64 {
	best := -1.0
	for _, m := range d.fr.Party {
		if m.Dead {
			continue
		}
		if dd := dist3(partyPoint(m), p); best < 0 || dd < best {
			best = dd
		}
	}
	return best
}

// isRunner judges one hostile of the newest frame: movingAway says it gained fleeStepYd on the party since the frame before.
func (d *RemoteDriver) isRunner(h tbm.HostileRec, movingAway bool) bool {
	if h.HPPct <= 0 || h.Entry == 0 || h.Evading() || h.Stealthed() {
		return false
	}
	if !d.engagedWith(h) && h.HPPct >= 100 {
		return false // not fought
	}
	d.tf.init()
	if d.tf.ccHeld[h.GUID] {
		return false // a mob the party crowd-controlled by order stands still on purpose
	}
	switch {
	case h.HPPct <= d.tx.FleePct && movingAway:
		return true
	case d.isFleer(h.Entry) && h.CrowdControlled() && h.HPPct <= d.tx.FleePct+fleeKnownPct:
		return true // the feed's `c` covers fear: a creature known to flee, at low health, and not crowd-controlled by order
	}
	return false
}

// noteFleeing folds a new frame into the runners. It runs once per new frame (look), after the ledger.
func (d *RemoteDriver) noteFleeing() {
	f := &d.fl
	f.init()
	seen := make(map[packet.GUID]bool, len(d.fr.Hostiles))
	for _, h := range d.fr.Hostiles {
		if h.Entry == 0 {
			continue
		}
		seen[h.GUID] = true
		if h.HPPct <= 0 {
			delete(f.running, h.GUID)
			delete(f.fled, h.GUID)
			delete(f.prev, h.GUID)
			continue
		}
		p := hostilePoint(h)
		away := d.nearestBotYd(p)
		prev, had := f.prev[h.GUID]
		now := d.clk.Now()
		cur := fleeSeen{at: p, away: away, n: d.n, hp: h.HPPct, when: now}
		if had {
			cur.rate, cur.drop = prev.rate, prev.drop // the last drop it showed stays for fleeRateAge
			if prev.hp > h.HPPct && now.After(prev.when) {
				cur.rate, cur.drop = float64(prev.hp-h.HPPct)/now.Sub(prev.when).Seconds(), now
			}
		}
		f.prev[h.GUID] = cur
		f.absent[h.GUID] = 0
		moving := had && away >= 0 && prev.away >= 0 && away-prev.away >= fleeStepYd && dist3(p, prev.at) >= fleeStepYd
		_, was := f.running[h.GUID]
		switch {
		case d.isRunner(h, moving):
			f.running[h.GUID] = h
			if !was && !f.noted[h.GUID] {
				f.noted[h.GUID] = true
				d.rep.note(fmt.Sprintf("leg %d: %s at %d%% moves away from the party: a runner", d.curLeg, d.hostileName(h), h.HPPct))
			}
		case was && h.HPPct > d.tx.FleePct+fleeKnownPct:
			delete(f.running, h.GUID) // it healed, or was never one
		case was && !moving && !h.CrowdControlled():
			delete(f.running, h.GUID) // it turned: it fights again
		default:
			if was {
				f.running[h.GUID] = h // still running, or out of breath at a distance: the sample stays
			}
		}
		if _, back := f.fled[h.GUID]; back {
			delete(f.fled, h.GUID) // it is back in view: the normal fight takes it
		}
	}
	if d.fr.Truncated || d.fr.AllDead() {
		return // an absence says nothing
	}
	for g, h := range f.running {
		if seen[g] {
			continue
		}
		if f.absent[g]++; f.absent[g] < fleeAbsent {
			continue
		}
		if _, done := f.fled[g]; done {
			continue
		}
		if d.diedInView(h) {
			delete(f.running, g) // it fell under the party's hands at the edge of the feed: a kill like any other
			continue
		}
		lead, _ := d.leadPos()
		near := dist3(hostilePoint(h), lead) <= float64(d.tx.FleeNearYd)
		if len(f.fled) >= fleeCap {
			continue
		}
		f.fled[g] = &fledMob{guid: g, entry: h.Entry, last: h, since: d.clk.Now(), near: near}
		d.remote(func(r *RemoteReport) { r.Fled++ })
		d.rep.note(fmt.Sprintf("leg %d: %s ran off at %d%% health to %.0f %.0f and left the feed; it is not counted as killed", d.curLeg, d.hostileName(h), h.HPPct, h.X, h.Y))
	}
}

// diedInView says whether a runner that is gone from the feed died, not escaped: it was nearly dead when the feed last showed it (a few
// percent, or no more than it lost in a second and a half), with the party's hands on it: a damage dealer ordered at it a moment ago and a living bot within spell range, or a bot within reach. A mob
// that is out of the feed at more health than that, or with nobody near, ran off alive.
func (d *RemoteDriver) diedInView(h tbm.HostileRec) bool {
	// Nearly dead: at most fleeDyingPct, or what it was losing a second (at its last drop, seen within fleeRateAge of its last frame)
	// takes it in a second and a half.
	seen := d.fl.prev[h.GUID]
	rate := 0.0
	if !seen.drop.IsZero() && seen.when.Sub(seen.drop) <= fleeRateAge {
		rate = seen.rate
	}
	if h.HPPct > fleeDyingPct && float64(h.HPPct) > rate*1.5 {
		return false
	}
	p := hostilePoint(h)
	if at, ok := d.fl.orderAt[h.GUID]; ok && d.clk.Now().Sub(at) <= fleeOrderRecent && d.botNear(p, fleeShotYd) {
		return true
	}
	return d.botNear(p, fleeReachYd)
}

// isRunning says whether the mob was a runner when the feed last showed it.
func (d *RemoteDriver) isRunning(g packet.GUID) bool {
	_, ok := d.fl.running[g]
	return ok
}

// wasFled says whether the mob left the feed as a runner.
func (d *RemoteDriver) wasFled(g packet.GUID) bool {
	_, ok := d.fl.fled[g]
	return ok
}

// runnerIn is the nearest runner of mobs that the party can still get at.
func (d *RemoteDriver) runnerIn(mobs []tbm.HostileRec) (tbm.HostileRec, bool) {
	var best tbm.HostileRec
	bestD, found := 0.0, false
	for _, h := range mobs {
		if !d.isRunning(h.GUID) || d.fl.orderN[h.GUID] >= fleeOrderMax {
			continue
		}
		away := d.nearestBotYd(hostilePoint(h))
		if away < 0 || away > fleeChaseYd {
			continue
		}
		if !found || away < bestD {
			best, bestD, found = h, away, true
		}
	}
	return best, found
}

// focusRunner orders the party at a runner of mobs: the damage dealers each (`cmd attack <guid> <bot>`, #928), so that the tank keeps
// the mobs it holds and the healer keeps healing; the whole party when the tank is not known or the module takes no bot name. It
// reports whether the assist is taken by the runner (an order went, or one went a moment ago): then no order at another mob goes out.
func (d *RemoteDriver) focusRunner(ctx context.Context, mobs []tbm.HostileRec) (bool, error) {
	h, ok := d.runnerIn(mobs)
	if !ok {
		return false, nil
	}
	now := d.clk.Now()
	if at, had := d.fl.orderAt[h.GUID]; had && now.Sub(at) < fleeOrderGap {
		return true, nil
	}
	d.fl.orderAt[h.GUID] = now
	d.fl.orderN[h.GUID]++
	d.attacked[h.GUID] = true
	d.tf.init()
	d.remote(func(r *RemoteReport) { r.FleeOrders++ })
	if d.fl.orderN[h.GUID] == 1 {
		d.rep.note(fmt.Sprintf("leg %d: %s is running at %d%%; the party is sent at it before it is out of reach", d.curLeg, d.hostileName(h), h.HPPct))
	}
	var bots []string
	if d.tank != 0 && !d.tf.looseOff {
		for _, m := range d.fr.Party {
			if m.Dead || m.GUID == d.tank || (d.opt.Healer != "" && strings.EqualFold(m.Name, d.opt.Healer)) || m.Name == "" {
				continue
			}
			bots = append(bots, m.Name)
		}
	}
	if len(bots) == 0 {
		r, err := d.order(ctx, "attack", tbm.GUIDArg(h.GUID))
		if err != nil {
			return true, err
		}
		if _, bad := refusedOf(r); !bad {
			d.tf.sentGUID, d.tf.sentAt = h.GUID, now
		}
		d.noteFocus(h.GUID, mobs)
		return true, nil
	}
	for _, b := range bots {
		r, err := d.order(ctx, "attack", tbm.GUIDArg(h.GUID), b)
		if err != nil {
			return true, err
		}
		if r.Ack != nil && !strings.EqualFold(r.Ack.Scope, "bot:"+b) {
			d.tf.looseOff = true // a module from before #928 sent the whole party: the one order did it
			break
		}
	}
	d.tf.sentGUID, d.tf.sentAt = h.GUID, now
	d.noteFocus(h.GUID, mobs)
	return true, nil
}

// fledPending lists the fled mobs a pull must wait for: last seen near the lead bot, not dead, not past FleeWait.
func (d *RemoteDriver) fledPending() []*fledMob {
	var out []*fledMob
	now := d.clk.Now()
	for g, m := range d.fl.fled {
		if mob := d.ledger[g]; mob != nil && mob.dead {
			delete(d.fl.fled, g)
			continue
		}
		if now.Sub(m.since) >= d.tx.FleeWait {
			continue
		}
		if m.near {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b *fledMob) int { return a.since.Compare(b.since) })
	return out
}

// waitFled holds the next pull while a mob that ran off in an earlier fight may still come back. It waits where the party stands, out
// of the way, helping with whatever engages (the runner back at low health, and the friends it brings), for at most FleeWait from the
// moment the mob was lost. It is the gate of engage (a pull) and costs nothing when no mob ran.
func (d *RemoteDriver) waitFled(ctx context.Context, label string) error {
	pending := d.fledPending()
	if len(pending) == 0 {
		return nil
	}
	began := d.clk.Now()
	defer func() {
		el := d.clk.Now().Sub(began).Seconds()
		d.remote(func(r *RemoteReport) { r.FleeWaitS += el })
	}()
	d.remote(func(r *RemoteReport) { r.FleeWaits++ })
	var names []string
	for _, m := range pending {
		names = append(names, d.hostileName(m.last))
	}
	d.rep.note(fmt.Sprintf("%s: not pulling on: %s ran off at low health and may come back with friends; waiting up to %.0f s for it where the party stands", label, strings.Join(names, ", "), d.tx.FleeWait.Seconds()))
	defer d.wd.enter("flee wait", "", nil)() // the wait has limits of its own
	hard := began.Add(fleeHardCap)
	for {
		if err := d.check(); err != nil {
			return err
		}
		if d.clk.Now().After(hard) {
			d.rep.note(fmt.Sprintf("%s: the wait for the mobs that ran off has gone on for %.0f s; pulling on", label, fleeHardCap.Seconds()))
			return nil
		}
		if eng := d.fr.EngagedHostiles(); len(eng) > 0 {
			d.watchThreat(eng)
			if err := d.tankFirstTick(ctx, eng); err != nil {
				return err
			}
			if err := d.assist(ctx, eng); err != nil {
				return err
			}
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			continue
		}
		if len(d.fledPending()) == 0 {
			d.rep.note(fmt.Sprintf("%s: the mobs that ran off are dealt with or did not come back in %.0f s; pulling on", label, d.clk.Now().Sub(began).Seconds()))
			return nil
		}
		if err := d.tick(ctx, d.cfg.Poll*2); err != nil {
			return err
		}
	}
}

// hostilesNearCorpses are the live hostiles within ReviveSafeYd of a dead bot of the party (a body lies where the bot fell).
func (d *RemoteDriver) hostilesNearCorpses() []tbm.HostileRec {
	var bodies []Point
	for _, m := range d.fr.Party {
		if m.Dead {
			bodies = append(bodies, partyPoint(m))
		}
	}
	if len(bodies) == 0 {
		return nil
	}
	var out []tbm.HostileRec
	for _, h := range d.fr.Hostiles {
		if h.Entry == 0 || h.HPPct <= 0 || !h.Hostile() || h.Bystander() {
			continue
		}
		for _, b := range bodies {
			if dist3(hostilePoint(h), b) < float64(d.tx.ReviveSafeYd) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}
