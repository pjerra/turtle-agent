package dungeon

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Big packs (owner decision 2026-10-14): the party takes at most about BigPack mobs at once. Zul'Farrak's packs of 5 to 10 were too much
// for a level-46 tank even with the threat back-off (hires-2: two wipes of 7- and 10-mob pulls; Uldaman's smaller packs ran clean). A pack
// is pulled as one (its mobs are linked), so it cannot be split; the cut is one member crowd-controlled before the pull: the cc bot (the
// hired mage, RemoteOptions.CCBot) gets `cmd cc moon <guid> <bot>` and Polymorphs it, and the party leaves it alone (tankFirst.ccHeld)
// until it is the last of the fight. The mark goes on a member of the plan's cc_priority entries if one is there (the healers and casters),
// else on the member farthest from the pull target, and never on the pull target itself. A run with no cc bot notes the big pack and
// pulls it as it is. A pack with a member of the plan's cc_always entries (Zul'Farrak's Shadowhunter and its Hex) gets the CC whatever its
// size, on that member.

// mageClassID is the class id of a mage in the roster (TBM:ROSTER, the game's class table).
const mageClassID = 8

// resolveCCBot gives a run that names no cc bot the party's mage (ZF-2 run 3i: a party named by lead_bot/bots/tank/healer, with no
// prepare hire step to name one, pulled a nine-mob pack "as it is, with no cc bot" and wiped). The mage is read from what an earlier
// run knew of the hires (RemoteOptions.HireMemory), else from `.bot roster` when the game answers plain bot lines; the first mage in the
// feed's party order takes it. A party without a mage says so once. A cc_bot the caller gave is never replaced.
func (d *RemoteDriver) resolveCCBot(ctx context.Context) {
	if strings.TrimSpace(d.opt.CCBot) != "" || d.tx.BigPack <= 0 || !d.haveFr {
		return
	}
	var names []string
	for _, p := range d.fr.Party {
		names = append(names, p.Name)
	}
	isMage := map[string]bool{}
	for _, n := range names {
		if w, ok := d.opt.HireMemory[strings.ToLower(n)]; ok && w.Class != "" {
			isMage[strings.ToLower(n)] = w.Class == "mage"
		}
	}
	if len(isMage) < len(names) {
		if bl, ok := d.g.(BotLiner); ok {
			var st tbm.Status
			_, settled, err := bl.BotLines(ctx, "roster", prepareRosterWait, func(line string) bool {
				if !strings.HasPrefix(line, "TBM:") {
					return false
				}
				st.Add(line)
				return strings.HasPrefix(line, "TBM:ROSTER_END") || strings.HasPrefix(line, "TBM:ROSTER_ERROR")
			})
			if err == nil && settled {
				for _, r := range st.Roster {
					if _, known := isMage[strings.ToLower(r.Name)]; !known {
						isMage[strings.ToLower(r.Name)] = r.Class == mageClassID
					}
				}
			}
		}
	}
	for _, n := range names {
		if isMage[strings.ToLower(n)] {
			d.opt.CCBot = n
			d.rep.note(fmt.Sprintf("cc bot: none given; %s, the mage of the party, takes the big-pack and reactive CC", n))
			return
		}
	}
	d.rep.note("cc bot: none given and no mage is known in the party (the hire memory and the roster name none); big packs are pulled as they are")
}

// BigPack is the size of a pack (living mobs the bots see within the group's radius of the pull target) that gets a CC before the pull.
const BigPack = 5

// bigPackCC puts the cc bot's mark on one member of t's pack when the pack has BigPack or more mobs and no mark is on it yet.
func (d *RemoteDriver) bigPackCC(ctx context.Context, t tbm.HostileRec, sp fightSpec) error {
	if d.tx.BigPack <= 0 {
		return nil
	}
	d.tf.init()
	// A patrol member within patrol_aggro of the target walks into the fight: it counts (remote_patrol.go).
	pack := d.withPatrols(d.packOf(t, float64(d.groupRadius(sp))), hostilePoint(t))
	always := slices.ContainsFunc(pack, func(h tbm.HostileRec) bool { return h.GUID != t.GUID && slices.Contains(d.plan.CCAlways, h.Entry) })
	if len(pack) < d.tx.BigPack && !always {
		return nil
	}
	if len(pack) >= d.tx.BigPack {
		d.remote(func(r *RemoteReport) { r.BigPacks++ })
	}
	for _, h := range pack {
		if d.ccStillOn(h) {
			return nil // a CC rule (or an earlier try) already took one, and it holds
		}
	}
	bot := strings.TrimSpace(d.opt.CCBot)
	if bot == "" {
		d.rep.note(fmt.Sprintf("leg %d: the pack of %s has %d mobs (big_pack %d) and the run has no cc bot; pulled as it is", d.curLeg, d.hostileName(t), len(pack), d.tx.BigPack))
		return nil
	}
	if m, ok := d.memberByName(bot); !ok || m.Dead {
		d.rep.note(fmt.Sprintf("leg %d: the pack of %s has %d mobs; the cc bot %s is not with the party alive; pulled as it is", d.curLeg, d.hostileName(t), len(pack), bot))
		return nil
	}
	pick, ok := d.ccPick(t, pack, always)
	if !ok {
		return nil
	}
	d.ccDone[pick.GUID] = true
	d.remote(func(r *RemoteReport) { r.CCOrders++ })
	d.ccOrdered[pick.GUID] = d.clk.Now()
	r, err := d.order(ctx, "cc", "moon", tbm.GUIDArg(pick.GUID), bot)
	if err != nil {
		return err
	}
	if ref, bad := refusedOf(r); bad {
		d.rep.note(fmt.Sprintf("leg %d: big-pack cc of %s by %s was refused: %s (%s); the pack of %d is pulled as it is", d.curLeg, d.hostileName(pick), bot, ref.Code, ref.Message, len(pack)))
		return nil
	}
	d.tf.ccHeld[pick.GUID] = true
	d.remote(func(r *RemoteReport) { r.BigPackCC++ })
	d.rep.note(fmt.Sprintf("leg %d: the pack of %s has %d mobs; %s takes %s with its CC before the pull", d.curLeg, d.hostileName(t), len(pack), bot, d.hostileName(pick)))
	return nil
}

// packOf is t and the living hostiles the bots see that would come with it: within radius yards of t and no more than AheadMaxDZ above
// or below it (another floor does not join), not fighting the party yet, not evading, and no bystander (critter, civilian, guard,
// unattackable). Every pull counts its pack this way, the plan's group pulls and the walk's pulls on the way (pullAhead) alike.
func (d *RemoteDriver) packOf(t tbm.HostileRec, radius float64) []tbm.HostileRec {
	var pack []tbm.HostileRec
	for _, h := range d.fr.Hostiles {
		if h.Entry == 0 || h.HPPct <= 0 || !h.Hostile() || h.Bystander() || h.Evading() || d.engagedWith(h) {
			continue
		}
		if math.Abs(float64(h.Z-t.Z)) > float64(d.tx.AheadMaxDZ) {
			continue
		}
		if dist3(hostilePoint(h), hostilePoint(t)) <= radius {
			pack = append(pack, h)
		}
	}
	return pack
}

// ccFresh is how long (a cast and its aura) an accepted CC order counts as holding before the feed shows the mob crowd-controlled.
const ccFresh = 4 * time.Second

// ccStillOn says whether h is held by a CC right now: the feed shows it crowd-controlled, or an order for it was accepted a moment
// ago. A mob the earlier pull of its pack left asleep long ago is not: Polymorph lapses in 20 to 50 s, and a pack re-pulled after its
// first mob died (Uldaman hires-3, group 4: the sheep from the first pull had run out when the second pull woke six mobs) needs its
// CC again. The party's own "leave it alone" mark (tankFirst.ccHeld) is not evidence that the CC is still on.
func (d *RemoteDriver) ccStillOn(h tbm.HostileRec) bool {
	if h.CrowdControlled() {
		return true
	}
	at, ok := d.ccOrdered[h.GUID]
	return ok && d.clk.Now().Sub(at) < ccFresh
}

// ccPick is the member of pack the CC takes: never the pull target; a cc_always member when there is one (always), then the plan's
// cc_priority entries (in their order), the member farthest from the target among equals.
func (d *RemoteDriver) ccPick(t tbm.HostileRec, pack []tbm.HostileRec, always bool) (tbm.HostileRec, bool) {
	rank := func(h tbm.HostileRec) int {
		if i := slices.Index(d.plan.CCPriority, h.Entry); i >= 0 {
			return i
		}
		return len(d.plan.CCPriority)
	}
	var best tbm.HostileRec
	found := false
	for _, h := range pack {
		if h.GUID == t.GUID || (always && !slices.Contains(d.plan.CCAlways, h.Entry)) {
			continue // a pack below big_pack is CCed for its cc_always member only
		}
		if !found || rank(h) < rank(best) || (rank(h) == rank(best) && dist3(hostilePoint(h), hostilePoint(t)) > dist3(hostilePoint(best), hostilePoint(t))) {
			best, found = h, true
		}
	}
	return best, found
}
