package dungeon

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Gather, then kill pack by pack (Zul'Farrak hires-1, 2026-10-14; the owner: "they just run and aggro and die"). At pull group 3 the
// lead bot was sent 15 yd short of the group's position to look for it, in two moves of 24 and 20 yd while the hunter stood 35 yd back
// drinking; the walk came within aggro range of the group, of a pack the plan did not list and of a patrolling pair, and seven mobs
// came at once. Inside the dungeon (a leg begun, not the walk to the portal) a walk now goes like this:
//   - it moves in hops of at most CarefulHop yards;
//   - before each hop the party gathers on the lead bot: every living bot within GatherNear yards, a wait bounded by GatherWait that
//     sends nothing (a bot still drinking finishes and comes up); after it the walk goes on with a note naming who was still away;
//   - before each hop a living hostile the bots see within AggroGuard yards of the hop (the straight line from the lead bot to the hop's
//     end), not fighting the party yet, on the hop's floor (8 yd up or down) and not one a step of the plan owns (planOwns), is pulled to the gathered party with `cmd pullback` and fought to the end before the walk goes
//     on (`ahead_pulls`); a won fight's dead mobs mark their spots, as any won pull does (noteKilled), so a later group standing there
//     has its evidence. One that draws no fight or is refused is passed over for the rest of the leg (it may aggro: the walk's fight
//     branch takes it). At most maxAheadPulls per walk.
// A pull gathers the party first as well, and the look at a group's position (confirmGone) stops LookStandoff yards short of it: out of
// the pack's aggro range (about 20 yd for a mob of the party's level), still inside ClearSeeRadius.

const (
	// GatherNear is how near (yards) every living bot must stand to the lead bot before a pull or a hop.
	GatherNear = 12
	// AggroGuard is how near (yards) to the next hop a hostile the bots see must stand to be pulled before the hop.
	AggroGuard = 22
	// GatherClear is how far (yards) the end of a hop, where the party stops and gathers, stays from a hostile no step of the current leg
	// owns: one nearer is pulled first (ZF hires-2 run 6: the party gathered 15 yd from a room of ten mobs of a later leg and wiped).
	GatherClear = 30
	// LookStandoff is how far short (yards) of a group's position the lead bot stops to look for it.
	LookStandoff = ClearSeeRadius - 4
	// lookArrive is how near the look's stand point the lead bot must get (the default 5 yd could leave it outside ClearSeeRadius).
	lookArrive = 3
	// maxAheadPulls bounds the pulls one walk makes on its way.
	maxAheadPulls = 12
	// gatherProgress is how much nearer (yards) a bot must have come during a gather's whole wait to be waited for again.
	gatherProgress = 2
	// aheadMaxDZ is how far above or below the hop (yards) a hostile may stand to be pulled before it: one on another floor is not in
	// the walk's way.
	aheadMaxDZ = 8
)

// careful says whether walks go the careful way (above): inside the dungeon, and not while a pull of the walk itself is going on (a
// pull's own approach, from sendPull, walks straight).
func (d *RemoteDriver) careful() bool {
	return d.curLeg >= 0 && d.outside == nil && !d.aheadPulling
}

// legSkip are the entries the current leg leaves alone.
func (d *RemoteDriver) legSkip() []uint32 {
	if d.curLeg >= 0 && d.curLeg < len(d.plan.Legs) {
		return d.plan.Legs[d.curLeg].Skip
	}
	return nil
}

// awayBot is a living bot that stands too far from the lead bot.
type awayBot struct {
	guid packet.GUID
	name string
	dist float64
}

// gatherState is what the gathers of one leg have learnt: the bots a gather gave up on because they did not come any closer during its
// whole wait (one that cannot path to the lead bot would otherwise cost the full wait at every hop and pull: about 7 min over an Uldaman
// leg). They are not waited for again in this leg until they come within GatherNear of the lead bot by themselves.
type gatherState struct {
	leg       int
	ignored   map[packet.GUID]bool
	restNoted map[packet.GUID]bool // ignored bots the rest gate has said it does not wait for (once each per leg)
}

// gatherIgnored is the set of bots the gathers of the current leg no longer wait for (a new leg starts it empty).
func (d *RemoteDriver) gatherIgnored() map[packet.GUID]bool {
	if d.gatherSt.ignored == nil || d.gatherSt.leg != d.curLeg {
		d.gatherSt = gatherState{leg: d.curLeg, ignored: map[packet.GUID]bool{}, restNoted: map[packet.GUID]bool{}}
	}
	return d.gatherSt.ignored
}

// awayFromLead lists the living bots on the lead bot's map that stand more than r yards from it, leaving out the ones the leg's gathers
// gave up on (one of those that is back within r is waited for again from then on).
func (d *RemoteDriver) awayFromLead(r float64) []awayBot {
	l, ok := d.fr.LeadRec()
	if !ok {
		return nil
	}
	ignored := d.gatherIgnored()
	var out []awayBot
	for _, m := range d.fr.Party {
		if m.GUID == l.GUID || m.Dead || m.Map != l.Map {
			continue
		}
		dd := dist3(partyPoint(m), partyPoint(l))
		if dd <= r {
			if ignored[m.GUID] {
				delete(ignored, m.GUID)
				delete(d.gatherSt.restNoted, m.GUID)
				d.rep.note(fmt.Sprintf("leg %d: %s is back with the party; the gathers wait for it again", d.curLeg, m.Name))
			}
			continue
		}
		if !ignored[m.GUID] {
			out = append(out, awayBot{guid: m.GUID, name: m.Name, dist: dd})
		}
	}
	return out
}

func awayText(bs []awayBot) string {
	var out []string
	for _, b := range bs {
		out = append(out, fmt.Sprintf("%s %.0f yd", b.name, b.dist))
	}
	return strings.Join(out, ", ")
}

// gather waits until every living bot stands within GatherNear yards of the lead bot, at most GatherWait, sending nothing. A fight ends
// it at once (the caller's fight logic owns that), and so does a negative GatherWait. why says what the party gathers for, for the note.
func (d *RemoteDriver) gather(ctx context.Context, why string) error {
	if d.opt.GatherWait < 0 {
		return nil
	}
	began := d.clk.Now()
	waited := false
	first := map[packet.GUID]float64{} // each away bot's distance when the wait first saw it
	for {
		if len(d.fr.EngagedHostiles()) > 0 {
			return nil
		}
		away := d.awayFromLead(float64(d.tx.GatherNear))
		for _, b := range away {
			if _, ok := first[b.guid]; !ok {
				first[b.guid] = b.dist
			}
		}
		if !d.stale() && len(away) == 0 {
			if waited {
				d.remote(func(r *RemoteReport) { r.GatherWaitS += d.clk.Now().Sub(began).Seconds() })
			}
			return nil
		}
		if !waited {
			waited = true
			d.remote(func(r *RemoteReport) { r.Gathers++ })
		}
		if el := d.clk.Now().Sub(began); el >= d.opt.GatherWait {
			d.remote(func(r *RemoteReport) {
				r.GatherWaitS += el.Seconds()
				r.GatherTimeouts++
			})
			d.rep.note(fmt.Sprintf("leg %d: the party did not gather within %.0f yd of the lead bot in %.0f s before %s (%s); going on", d.curLeg, float64(d.tx.GatherNear), d.opt.GatherWait.Seconds(), why, awayText(away)))
			// The healer is the member that matters most to have in place before a pull (live, zf-zum2-s2: "came no closer in 12 s" twelve
			// times, and the healer died first in 8 of 10 deadly fights): when it is among the stragglers it is summoned at this first
			// timeout, not waited for again.
			if landed := d.summonHealerStraggler(ctx, away); len(landed) > 0 {
				var rest []awayBot
				for _, b := range away {
					if !slices.Contains(landed, b.guid) {
						rest = append(rest, b)
					}
				}
				away = rest
			}
			// A bot that got no closer in the whole wait is not coming (it cannot path, or it is stuck): the leg's gathers stop waiting
			// for it until it is back by itself.
			var stuck []string
			for _, b := range away {
				if b.dist >= first[b.guid]-float64(d.tx.GatherProgress) {
					d.gatherIgnored()[b.guid] = true
					stuck = append(stuck, b.name)
				}
			}
			if len(stuck) > 0 {
				d.remote(func(r *RemoteReport) { r.GatherGaveUp += len(stuck) })
				d.rep.note(fmt.Sprintf("leg %d: %s came no closer in %.0f s; the gathers of this leg do not wait for it again until it is back within %.0f yd", d.curLeg, strings.Join(stuck, ", "), d.opt.GatherWait.Seconds(), float64(d.tx.GatherNear)))
			}
			return nil
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// distToSegment is the distance from p to the segment a-b.
func distToSegment(p, a, b Point) float64 {
	abx, aby, abz := float64(b.X-a.X), float64(b.Y-a.Y), float64(b.Z-a.Z)
	l2 := abx*abx + aby*aby + abz*abz
	if l2 == 0 {
		return dist3(p, a)
	}
	t := (float64(p.X-a.X)*abx + float64(p.Y-a.Y)*aby + float64(p.Z-a.Z)*abz) / l2
	t = math.Max(0, math.Min(1, t))
	return dist3(p, lerp(a, b, t))
}

// hostileAhead is the living hostile the bots see within AggroGuard yards of the hop from here to wp, or within GatherClear yards of the hop's
// end (where the party will gather), that is not fighting the party yet,
// the nearest to the lead bot, and no more than aheadMaxDZ above or below the hop (another floor is not in the way). Players, stealthed,
// evading and crowd-controlled units, any unit a step of the plan owns (planOwns), mobs the bots refused and those a pull on the way
// passed over in this leg are left out. Neutral units and critters the module lists are told apart by the record's reaction and kind (TortoiseBots #943): only reaction h (or none, from an older module) is pulled, and
// never a critter, civilian, guard or unattackable unit.
func (d *RemoteDriver) hostileAhead(here, wp Point) (tbm.HostileRec, bool) {
	if d.stale() {
		return tbm.HostileRec{}, false
	}
	var best tbm.HostileRec
	bestD := math.MaxFloat64
	for _, h := range d.fr.Hostiles {
		if h.Entry == 0 || h.HPPct <= 0 || h.Stealthed() || h.Evading() || h.CrowdControlled() || d.aheadPassed(h.GUID) {
			continue
		}
		if _, refused := d.skipped[h.GUID]; refused || d.engagedWith(h) || d.planOwns(h) {
			continue
		}
		// TortoiseBots #943: only a hostile is pulled, never a critter, civilian, guard or unattackable unit. engagedWith above already
		// left out every unit fighting the party; a neutral that targets a bot is fighting it: the walk's fight branch takes it.
		if !h.Hostile() || h.Bystander() {
			continue
		}
		p := hostilePoint(h)
		if (distToSegment(p, here, wp) > float64(d.tx.AggroGuard) && dist3(p, wp) > float64(d.tx.GatherClear)) || p.Z < min(here.Z, wp.Z)-d.tx.AheadMaxDZ || p.Z > max(here.Z, wp.Z)+d.tx.AheadMaxDZ {
			continue
		}
		if dd := dist3(p, here); dd < bestD {
			best, bestD = h, dd
		}
	}
	return best, bestD < math.MaxFloat64
}

// planOwns says whether h is one a step of the plan pulls or leaves alone: a boss or a First add of any leg (by entry,
// wherever it stands), an entry any leg skips (the dormant Stone Keepers and Vault statues of Uldaman's last legs, 3-15 yd from the walks
// of legs 10 and 12), or a unit with an entry of a pull group of the current or an earlier leg (of any leg for a team-gated group) standing within
// that group's radius of its positions. Those are
// pulled in the plan's order by their steps, each from a gathered party (a team-gated one, or a statue before its ritual, may not be
// pulled at all); the walk pulls only what the plan does not place there: a patrol away from its group, a pack the plan does not list.
func (d *RemoteDriver) planOwns(h tbm.HostileRec) bool {
	at := hostilePoint(h)
	for li, l := range d.plan.Legs {
		if b := l.Boss; b != nil && (h.Entry == b.Entry || slices.Contains(b.First, h.Entry)) {
			return true
		}
		if slices.Contains(l.Skip, h.Entry) {
			return true
		}
		for _, g := range l.PullGroups {
			// A pack of a later leg in this leg's way is pulled like a pack the plan does not list: the party would otherwise stop
			// and gather beside it (ZF hires-2 run 6: the walk of leg 0 went through Velratha's packs of leg 2 and wiped). An earlier
			// leg's pack (done, or skipped by from_leg and left alive on purpose) and a team-gated pack stay the plan's.
			if li > d.curLeg && d.curLeg >= 0 && g.Team == "" {
				continue
			}
			if !slices.Contains(g.Entries, h.Entry) {
				continue
			}
			r := g.Radius
			if r <= 0 {
				r = d.cfg.PackRadius
			}
			for _, p := range g.Positions {
				if dist3(at, p) <= float64(r) {
					return true
				}
			}
		}
	}
	return false
}

// aheadPassed says whether a pull on the way passed g over in the current leg (it drew no fight or evaded).
func (d *RemoteDriver) aheadPassed(g packet.GUID) bool {
	leg, ok := d.aheadPassedLeg[g]
	return ok && leg == d.curLeg
}

// pullAhead pulls h, a hostile near the walk's next hop, to the gathered party and fights it to the end. A won fight marks where its
// mobs died; any other outcome passes h over for the rest of the walk.
func (d *RemoteDriver) pullAhead(ctx context.Context, h tbm.HostileRec) error {
	d.aheadPulling = true
	defer func() { d.aheadPulling = false }()
	d.remote(func(r *RemoteReport) { r.AheadPulls++ })
	d.rep.note(fmt.Sprintf("leg %d: %s at %.0f %.0f %.0f stands within %.0f yd of the walk's next hop; the party gathers and pulls it before walking on", d.curLeg, d.hostileName(h), h.X, h.Y, h.Z, float64(d.tx.AggroGuard)))
	// What would come with h is counted as for a planned pull (packOf, the pack radius, h's floor): a pull of big_pack mobs or more, or
	// of a pack with a cc_always member, is a big pull. It is gathered for like the rest, and fight() puts the pack CC on one member
	// (bigPackCC: Polymorph by cc_priority) before the pullback to the gathered party; with no cc bot it is pulled as it is, noted.
	pack := d.withPatrols(d.packOf(h, float64(d.cfg.PackRadius)), hostilePoint(h))
	if d.tx.BigPack > 0 && (len(pack) >= d.tx.BigPack || slices.ContainsFunc(pack, func(o tbm.HostileRec) bool { return o.GUID != h.GUID && slices.Contains(d.plan.CCAlways, o.Entry) })) {
		d.remote(func(r *RemoteReport) { r.AheadBigPulls++ })
		d.rep.note(fmt.Sprintf("leg %d: the pull of %s on the way is a big pull (%d mobs within %.0f yd, big_pack %d): the party gathers first and the pack gets its CC before the pull", d.curLeg, d.hostileName(h), len(pack), float64(d.cfg.PackRadius), d.tx.BigPack))
	}
	if err := d.gather(ctx, "a pull on the way"); err != nil {
		return err
	}
	// The pack is what the bots see around it: the fight is won when all of those are dead.
	track := []uint32{h.Entry}
	for _, o := range d.fr.Hostiles {
		if o.Entry != 0 && o.HPPct > 0 && dist3(hostilePoint(o), hostilePoint(h)) <= float64(d.cfg.PackRadius) && !slices.Contains(track, o.Entry) {
			track = append(track, o.Entry)
		}
	}
	sp := fightSpec{pick: []uint32{h.Entry}, track: track, skip: d.legSkip(), anchors: []Point{hostilePoint(h)}, limit: d.cfg.FightTimeout, sneak: true, passed: map[packet.GUID]bool{}}
	d.legStat(func(l *LegReport) { l.Pulls++ })
	res, err := d.tracedFight(ctx, h, sp)
	if err != nil {
		return err
	}
	if res.out == won {
		d.noteKilled(res)
		return nil
	}
	// Passed over for the rest of the leg, like a refused pull for the run: a mob that drew no fight once is not pulled at every walk.
	// It is never counted cleared (nothing marks its spot).
	if d.aheadPassedLeg == nil {
		d.aheadPassedLeg = map[packet.GUID]int{}
	}
	d.aheadPassedLeg[h.GUID] = d.curLeg
	if res.out == refused {
		d.skipped[h.GUID] = res.why
	}
	d.rep.note(fmt.Sprintf("leg %d: the pull of %s on the way %s (%s); walking on", d.curLeg, d.hostileName(h), outcomeName(res.out), res.why))
	return nil
}

// summonHealerStraggler summons the named healer (RemoteOptions.Healer) with `cmd summon` when it is among the away bots at a gather's
// timeout, on a GM account, out of combat and not summoned in the last rejoin gap: the same GM assist as the rejoin (run_gm_assists,
// rejoin_tps). It waits up to rejoinLand for the bot to land by the lead bot and puts the party back on follow. It returns the GUIDs that
// landed.
func (d *RemoteDriver) summonHealerStraggler(ctx context.Context, away []awayBot) []packet.GUID {
	if d.opt.Healer == "" || d.tx.RejoinTPDist <= 0 || d.inCombat() || d.leadOutside() {
		return nil
	}
	var b *awayBot
	for i := range away {
		if strings.EqualFold(away[i].name, d.opt.Healer) {
			b = &away[i]
		}
	}
	f, ok := d.g.(Follower)
	if b == nil || !ok || !d.agentGM(ctx, f) {
		return nil
	}
	if d.rejoin.last == nil {
		d.rejoin.last = map[packet.GUID]time.Time{}
	}
	if last, ok := d.rejoin.last[b.guid]; ok && d.clk.Now().Sub(last) < d.tx.RejoinGap {
		return nil
	}
	if !d.rejoinSummon(ctx, rejoinBot{guid: b.guid, name: b.name, dist: b.dist}) {
		return nil
	}
	until := d.clk.Now().Add(rejoinLand)
	for d.clk.Now().Before(until) {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return nil
		}
		if m, ok := d.fr.Member(b.guid); ok && !m.Dead && !farHas(d.rejoinFarBotsAny(float64(d.tx.GatherNear)), b.guid) {
			if r, err := d.order(ctx, "follow"); err == nil {
				_ = r
			}
			delete(d.gatherIgnored(), b.guid)
			return []packet.GUID{b.guid}
		}
	}
	return nil
}

// rejoinFarBotsAny is rejoinFarBots without the named-bots filter: every living bot farther than yd from the lead bot or on another map.
func (d *RemoteDriver) rejoinFarBotsAny(yd float64) []rejoinBot {
	var out []rejoinBot
	l, ok := d.fr.LeadRec()
	if !ok {
		return nil
	}
	for _, m := range d.fr.Party {
		if m.GUID == l.GUID || m.Dead {
			continue
		}
		if m.Map != l.Map {
			out = append(out, rejoinBot{guid: m.GUID, name: m.Name, dist: math.Inf(1), other: true})
		} else if dd := dist3(partyPoint(m), partyPoint(l)); dd > yd {
			out = append(out, rejoinBot{guid: m.GUID, name: m.Name, dist: dd})
		}
	}
	return out
}
