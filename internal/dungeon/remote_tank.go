package dungeon

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Tank first, in remote mode: the party follows the tank's threat. Behaviour after mod-playerbots (ThreatStrategy, DpsTargetValue,
// TankTargetValue, AttackersValue: AGPL, ideas only; nothing is copied). The feed gives the top three of each mob's threat list as a
// share of the top, so "the tank holds it" is the tank's share (100 when it tops the list) and "loose" is a mob hitting a bot that
// is not the tank.

const (
	// looseGrace is how long a mob must have been fighting the party, hitting a bot that is not the tank, before the tank is sent at it:
	// a mob just pulled is still on its way to the tank.
	looseGrace = 3 * time.Second
	// maxAddOrders bounds the tank orders at one mob.
	maxAddOrders = 3
	// stickShare is the tank's share on the mob the party fights below which the party may be sent at another mob.
	stickShare = 50
	// switchLead is how many points of share another mob must lead the one the party fights by, below stickShare, to be taken instead.
	switchLead = 25
)

// tankFirst is the driver's state for the rules; its maps are made on first use.
type tankFirst struct {
	looseOff   bool                      // the module answered a loose-add order party-scoped (it ignores the bot name): no more of them
	focus      packet.GUID               // the mob the party was last sent at
	heldAt     map[packet.GUID]time.Time // when the tank was first seen holding a mob (the opener delay counts from here)
	ccHeld     map[packet.GUID]bool      // mobs a cc order was accepted for
	ccNoted    map[packet.GUID]bool      // crowd-controlled mobs already counted in cc_respected
	addAt      map[packet.GUID]time.Time // the last tank order at a loose add
	addN       map[packet.GUID]int
	offTankAt  map[packet.GUID]time.Time // when a mob was first seen on a bot that is not the tank (it is taken back to 0 when it goes back)
	sampledN   uint64                    // the frame the tank's share was last sampled at
	shareSum   float64                   // the sum of the samples
	shareCount int
}

func (t *tankFirst) init() {
	if t.heldAt == nil {
		t.heldAt, t.ccHeld, t.ccNoted = map[packet.GUID]time.Time{}, map[packet.GUID]bool{}, map[packet.GUID]bool{}
		t.addAt, t.addN, t.offTankAt = map[packet.GUID]time.Time{}, map[packet.GUID]int{}, map[packet.GUID]time.Time{}
	}
}

// ccd says whether the party leaves the hostile alone. Only a mob a cc order was accepted for can be spared: the feed's `c` flag is
// UNIT_STAT_STUNNED|CONFUSED|FLEEING|ROOT, so on its own it also marks a stunned or rooted mob the tank is fighting. Such a mob is spared
// while the flag is up or no bot is fighting it (a sheep that breaks goes for a bot at once); the mob the party was sent at last (the focus)
// and the tank's own target are never spared.
func (d *RemoteDriver) ccd(h tbm.HostileRec) bool {
	d.tf.init()
	if !d.tf.ccHeld[h.GUID] || h.GUID == d.tf.focus {
		return false
	}
	if m, ok := d.fr.Member(d.tank); ok && d.tank != 0 && m.Target == h.GUID {
		return false
	}
	return h.CrowdControlled() || !d.fr.IsMember(h.Target)
}

// respectCC drops the crowd-controlled mobs of mobs, counting each once in cc_respected. When every mob is crowd-controlled it is the
// last of the fight and the list stays as it is: the party breaks it.
func (d *RemoteDriver) respectCC(mobs []tbm.HostileRec) []tbm.HostileRec {
	free := make([]tbm.HostileRec, 0, len(mobs))
	var held []tbm.HostileRec
	for _, h := range mobs {
		if d.ccd(h) {
			held = append(held, h)
		} else {
			free = append(free, h)
		}
	}
	if len(free) == 0 {
		return mobs
	}
	for _, h := range held {
		if !d.tf.ccNoted[h.GUID] {
			d.tf.ccNoted[h.GUID] = true
			d.remote(func(r *RemoteReport) { r.CCRespected++ })
		}
	}
	return free
}

// tankShare is the tank's share of the top threat on the mob: 100 when it tops the list, 0 when it is not among the top three.
func (d *RemoteDriver) tankShare(h tbm.HostileRec) int {
	if d.tank == 0 {
		return 0
	}
	pct, _ := d.fr.ThreatOf(h.GUID, d.tank)
	return pct
}

// rankByTank puts the mobs in the order the party takes them: the mob the party fights already while the tank still holds it, then
// the mobs by the tank's share of their threat, the tank's own target before the others, and the nearest first. Without a known tank
// the order is the nearest first, as it was.
func (d *RemoteDriver) rankByTank(mobs []tbm.HostileRec) []tbm.HostileRec {
	out := slices.Clone(mobs)
	var tankTarget packet.GUID
	if m, ok := d.fr.Member(d.tank); ok && d.tank != 0 {
		tankTarget = m.Target
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if d.tank != 0 {
			if sa, sb := d.tankShare(a), d.tankShare(b); sa != sb {
				return sa > sb
			}
			if ta, tb := a.GUID == tankTarget, b.GUID == tankTarget; ta != tb {
				return ta
			}
		}
		return distToLead(d, hostilePoint(a)) < distToLead(d, hostilePoint(b))
	})
	// The party keeps the mob it fights while the tank holds it well enough (or while there is no tank to rank by), and, below that,
	// until another mob leads it by switchLead points: no flipping between two mobs the tank holds alike.
	if d.tf.focus != 0 {
		if i := slices.IndexFunc(out, func(h tbm.HostileRec) bool { return h.GUID == d.tf.focus }); i > 0 &&
			(d.tank == 0 || d.tankShare(out[i]) >= stickShare || d.tankShare(out[0])-d.tankShare(out[i]) < switchLead) {
			f := out[i]
			copy(out[1:i+1], out[:i])
			out[0] = f
		}
	}
	return out
}

// sampleTankShare adds one sample per new frame: the mean of the tank's share over the fighting mobs that have a threat record.
func (d *RemoteDriver) sampleTankShare(mobs []tbm.HostileRec) {
	d.tf.init()
	if d.tank == 0 || d.n == d.tf.sampledN {
		return
	}
	sum, n := 0, 0
	for _, h := range mobs {
		if d.ccd(h) {
			continue
		}
		if t, ok := d.fr.Threat(h.GUID); ok && len(t.Top) > 0 {
			sum += d.tankShare(h)
			n++
		}
	}
	if n == 0 {
		return
	}
	d.tf.sampledN = d.n
	d.tf.shareSum += float64(sum) / float64(n)
	d.tf.shareCount++
	mean := math.Round(d.tf.shareSum/float64(d.tf.shareCount)*10) / 10
	count := d.tf.shareCount
	d.remote(func(r *RemoteReport) { r.TankThreatShare, r.TankThreatSamples = mean, count })
}

// tankFirstTick runs every look of a fight: it samples the tank's share and sends the tank at loose adds.
func (d *RemoteDriver) tankFirstTick(ctx context.Context, mobs []tbm.HostileRec) error {
	d.sampleTankShare(mobs)
	return d.tankAtLooseAdds(ctx, mobs)
}

// tankAtLooseAdds sends the tank at a mob that has been hitting a bot that is not the tank for looseGrace (counted from the look
// that first showed it on that bot). The order is `.bot cmd attack <guid> <tank>` (TortoiseBots #928: with a bot name only that bot of the
// party goes). It is not `cmd pull`: that was refused `tank-busy` whenever the tank was already meleeing another mob, which is exactly
// when a loose add needs it (the first remote Uldaman run: 19 refusals). `cmd pull` stays for opening pulls (sendPull).
// At most one order per look; AddCooldown apart for the same mob, maxAddOrders for the fight.
func (d *RemoteDriver) tankAtLooseAdds(ctx context.Context, mobs []tbm.HostileRec) error {
	d.tf.init()
	if d.tank == 0 || d.tf.looseOff {
		return nil
	}
	tank, ok := d.fr.Member(d.tank)
	if !ok || tank.Dead || tank.Name == "" {
		return nil
	}
	now := d.clk.Now()
	for _, h := range d.respectCC(mobs) {
		if _, ok := d.engagedAt[h.GUID]; !ok {
			d.engagedAt[h.GUID] = now
		}
		if h.Target == 0 || h.Target == d.tank || !d.fr.IsMember(h.Target) {
			delete(d.tf.offTankAt, h.GUID)
			continue
		}
		if _, ok := d.tf.offTankAt[h.GUID]; !ok {
			d.tf.offTankAt[h.GUID] = now // the look that first showed the mob on a bot that is not the tank
		}
		if now.Sub(d.tf.offTankAt[h.GUID]) < looseGrace {
			continue
		}
		if d.tankShare(h) >= 100 { // the tank tops its list: the mob is about to turn
			continue
		}
		if at, ok := d.tf.addAt[h.GUID]; (ok && now.Sub(at) < d.opt.AddCooldown) || d.tf.addN[h.GUID] >= maxAddOrders {
			continue
		}
		d.tf.addAt[h.GUID] = now
		d.tf.addN[h.GUID]++
		d.remote(func(r *RemoteReport) { r.LooseAddOrders++ })
		who := h.Target.String()
		if m, ok := d.fr.Member(h.Target); ok {
			who = m.Name
		}
		d.rep.note(fmt.Sprintf("leg %d: %s is on %s, not the tank; the tank is sent at it", d.curLeg, d.hostileName(h), who))
		r, err := d.order(ctx, "attack", tbm.GUIDArg(h.GUID), tank.Name)
		if err != nil {
			return err
		}
		if ref, bad := refusedOf(r); bad {
			d.rep.note(fmt.Sprintf("the tank order at %s was refused: %s (%s)", d.hostileName(h), ref.Code, ref.Message))
		}
		// `cmd attack <guid> <bot>` must come back scoped to the bot. A module from before #928 ignores the name and sends the whole party
		// at the add: no tank-first then, so the orders stop for the run.
		if r.Ack != nil && !strings.EqualFold(r.Ack.Scope, "bot:"+tank.Name) {
			d.tf.looseOff = true
			d.rep.note(fmt.Sprintf("the tank order at %s was answered with scope %q, not bot:%s: this module does not take a bot name on cmd attack (TortoiseBots #928); no more loose-add orders this run", d.hostileName(h), r.Ack.Scope, tank.Name))
		}
		return nil
	}
	return nil
}

// noteFocus records the mob the party was sent at, counting a switch when the one before is still among mobs.
func (d *RemoteDriver) noteFocus(g packet.GUID, mobs []tbm.HostileRec) {
	prev := d.tf.focus
	d.tf.focus = g
	if prev != 0 && prev != g && slices.ContainsFunc(mobs, func(h tbm.HostileRec) bool { return h.GUID == prev }) {
		d.remote(func(r *RemoteReport) { r.TargetSwitches++ })
	}
}
