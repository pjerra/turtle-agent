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

// mageSpells are spells only a mage casts (1.12). The feed's party record carries the bot's last AI action, a spell name cut to 16
// characters, which tells the mage when no list of classes can be had.
var mageSpells = []string{"frostbolt", "fireball", "scorch", "arcane missiles", "arcane explosion", "fire blast", "frost nova", "blink",
	"polymorph", "counterspell", "cone of cold", "blizzard", "flamestrike", "pyroblast", "conjure water", "conjure food", "mana shield",
	"ice barrier", "evocation", "frost armor", "ice armor", "mage armor", "arcane intellect", "arcane brilliance", "dampen magic",
	"amplify magic", "ice block", "presence of mind", "arcane power", "combustion", "cold snap", "remove lesser curse", "slow fall"}

// isMageAction says whether a party record's last action is a mage spell (a name cut at 16 characters matches by its prefix).
func isMageAction(action string) bool {
	a := strings.ToLower(strings.TrimSpace(action))
	if a == "" {
		return false
	}
	for _, s := range mageSpells {
		if a == s || (len(a) >= 15 && strings.HasPrefix(s, a)) {
			return true
		}
	}
	return false
}

// resolveCCBot gives a run that names no cc bot the party's mage (ZF-2 run 3i and run 4: a party named by lead_bot/bots/tank/healer, with
// no prepare hire step to name one, pulled big packs "as they are, with no cc bot" and wiped; run 4 resumed seven times and never had a
// Polymorph, because `.bot roster` lists none of the bound hires and the hire memory of a failed prepare run was gone). Sources, in
// order: the class the agent learned of a hire in any earlier run (RemoteOptions.ClassMemory), what a kept binding's run read
// (HireMemory), `.bot roster` (once, at the start), and the spells the bots cast (the party record's last action). The first mage in the
// feed's party order takes it. A cc_bot the caller gave is never replaced; a run that finds none tries again at each big pack
// (ensureCCBot), since a mage shows itself by casting.
func (d *RemoteDriver) resolveCCBot(ctx context.Context) {
	// An observed run has no pulls of the agent's: the run leader on the server does the fighting, so no cc bot is looked for.
	if d.observing() || strings.TrimSpace(d.opt.CCBot) != "" || d.tx.BigPack <= 0 || !d.haveFr {
		return
	}
	if n := d.mageOfParty(); n != "" {
		d.takeCCBot(n, "the hire memory")
		return
	}
	var names []string
	for _, p := range d.fr.Party {
		names = append(names, p.Name)
	}
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
			for _, n := range names {
				for _, r := range st.Roster {
					if strings.EqualFold(r.Name, n) && r.Class == mageClassID {
						d.takeCCBot(n, "the roster")
						return
					}
				}
			}
		}
	}
	d.rep.note("cc bot: none given and no mage is known in the party yet (the class memory, the hire memory and the roster name none); it is looked for again at each big pack, from the spells the bots cast")
}

// mageOfParty is the first bot of the feed's party known to be a mage from the memories or from its last action ("" for none).
func (d *RemoteDriver) mageOfParty() string {
	for _, p := range d.fr.Party {
		l := strings.ToLower(p.Name)
		if c := d.opt.ClassMemory[l]; c != "" {
			if c == "mage" {
				return p.Name
			}
			continue
		}
		if w, ok := d.opt.HireMemory[l]; ok && w.Class != "" {
			if w.Class == "mage" {
				return p.Name
			}
			continue
		}
		if isMageAction(p.Action) {
			return p.Name
		}
	}
	return ""
}

func (d *RemoteDriver) takeCCBot(n, from string) {
	d.opt.CCBot = n
	d.rep.note(fmt.Sprintf("cc bot: none given; %s, the mage of the party (%s), takes the big-pack and reactive CC", n, from))
}

// ensureCCBot looks again for the party's mage when the run has no cc bot, at most once in ccLookEvery: the spells the bots have cast since
// the start tell the mage.
func (d *RemoteDriver) ensureCCBot() {
	if strings.TrimSpace(d.opt.CCBot) != "" || d.tx.BigPack <= 0 || !d.haveFr {
		return
	}
	now := d.clk.Now()
	if !d.ccLookAt.IsZero() && now.Sub(d.ccLookAt) < ccLookEvery {
		return
	}
	d.ccLookAt = now
	if n := d.mageOfParty(); n != "" {
		d.takeCCBot(n, "its spells")
	}
}

// ccLookEvery is how often a run without a cc bot looks for the mage again.
const ccLookEvery = 10 * time.Second

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
	d.ensureCCBot()
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

// ccAllowed says whether the pack and reactive CC may take h: never a boss of the plan (a boss is not sheeped: live, 8 orders at Velratha, none
// landed), an entry of the plan's kill_first (the healers and wards that are killed first, never sheeped: the Witch Doctor 5650 was sheeped ten
// times against the plan), an entry of no_cc (undead: nine orders at Zombies and Dead Heroes, one cast), or a unit with a pet's GUID.
func (d *RemoteDriver) ccAllowed(h tbm.HostileRec) bool {
	if h.GUID.High() == packet.HighPet { // a Polymorph at a pet's GUID lands nowhere
		return false
	}
	if d.plan == nil {
		return true
	}
	if slices.Contains(d.plan.KillFirst, h.Entry) || slices.Contains(d.plan.NoCC, h.Entry) {
		return false
	}
	for _, l := range d.plan.Legs {
		if b := l.Boss; b != nil && (b.Entry == h.Entry || slices.Contains(b.First, h.Entry)) {
			return false
		}
	}
	return true
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
		if h.GUID == t.GUID || (always && !slices.Contains(d.plan.CCAlways, h.Entry)) || !d.ccAllowed(h) {
			continue // a pack below big_pack is CCed for its cc_always member only; a boss, a kill_first entry, an undead is never taken
		}
		if !found || rank(h) < rank(best) || (rank(h) == rank(best) && dist3(hostilePoint(h), hostilePoint(t)) > dist3(hostilePoint(best), hostilePoint(t))) {
			best, found = h, true
		}
	}
	return best, found
}
