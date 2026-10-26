package planexport

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// The plan attributes the run leader's rows cannot carry: each one the plan sets is a warning (TortoiseBots #984: nothing is dropped
// silently), naming every place it stands and what the run leader does instead. The attributes the rows do carry (the points, pull
// groups, bosses, holds, steps, unit lists, team and ambush flags, direct and exact via points) are not listed, nor are those that are
// no run attribute at all: the plan's name, version, status, kind, map and notes, a via's check_skip (turtle-agent's plan check only),
// a pull group's equiv (the pull-test scenario only), a step's fail_text and a use_item's name (both in the row's comment).
var unexpressedWhy = map[string]string{
	"entrance/teleports/portal": "the run leader starts inside the instance (`.bot run start` checks the party is in it); getting there is the caller's",
	"level_band":                "the run leader leads the bound party at whatever level it is",
	"hire_composition":          "the run leader leads the bound party as it is",
	"fleers":                    "no column; the bots' own AI chases or lets go a fleeing mob",
	"no_cc":                     "no column; the run leader's crowd control picks only from cc_priority and cc_always",
	"summoned_kill_first":       "no column; only kill_first is focused",
	"raid":                      "no column; the run leader leads one party",
	"watchdog":                  "the agent's watchdog; the run leader has its own ceilings",
	"avoid":                     "the run leader does not know avoid boxes; export with --mmaps to route its hops around them (a fight can still drag the party in)",
	"skip":                      "no skip list column: the run leader pulls a hostile within 15 yd of a hop point that no later step owns, a skipped unit too",
	"leave":                     "no column or step kind: the spawns are free text in the leg row's comment and the run leader still pulls, fights and hops to them like any hostile (a module part that reads the list would need a step kind or a flag on the leg's pull and hop rows)",
	"leftover_entries":          "no sweep of idle summoned units; the bots fight what aggroes",
	"rest":                      "the run leader's own rest lines apply",
	"companions_loot false":     "dropped (--drop-companions-loot): the bots loot as they like on this leg",
	"companions_loot false (the chests exported)": "not carried: turn the bots' looting off for this leg (setup), or a module with the trapped-chest rule; the setup of the carrier run: looting off for every bot, party loot method free-for-all, the Executioner's Key 8444 on the lead bot, at least 16 free bag slots on the lead bot, a fresh instance (a looted grave respawns after 2 h); flag 256 needs a TortoiseBots module with the gone-is-done step flag: an older module ignores it and pauses on a despawned grave after a wipe (then a .bot run skip skips the row)",
	"needs_walk":                  "the point was not snapped to the navmesh by a mapping walk; the run leader walks to it as given",
	"patrol":                      "no patrol path or period: the run leader pulls the pack where it stands when it comes",
	"boss.tactics":                "the plan's tactics are the agent's; a bound party uses the module's boss tactics (docs/guides/boss-tactics.md)",
	"boss.companions_loot":        "no column; the bots loot the boss as they like",
	"stand (loot_object, ritual)": "no stand point in the row: the bots walk to the object themselves",
	"object spawn/range":          "the row names the object by entry near its point only (the module's own search range)",
	"loot_object batch":           "no batches: the chests are opened one step at a time",
	"wait_for agent_only":         "no row: the plan says why the run leader needs none (quoted); the agent's driver runs the wait",
	"boss.first under a hold":     "the hold row lists them as the event's units, not as a kill order; only the plan's kill_first is focused",
	"wait_for with a hold":        "the row is the wait's hold (the event's spot, fought until its instance data value); the wait's own condition (a unit, an object, hostiles gone) and its fail_text are the agent's only",
	"gossip stand/by/retry_s":     "the row is the order `cmd gossip <npc> <lead bot> <option>`: the lead bot talks (the tank or carrier of a plan is the same bot), walks to the NPC itself, asks once (no retry), and ends on the order's ACK; the stand point, the talker and retry_s are the agent's only (fail_text is in the row's comment)",
	"area_trigger expect/timeout": "only the walk into the zone is a row (a move to the stand point); the expect check and its timeout are not run (the comment names the expect)",
}

// sweepsLeftovers says whether the leg's LeftoverEntries are carried by the export anyway: with CarrierChests a companions_loot-false
// leg's exported chest rows end each in the sweep pull of the same entries, so the entries are in the rows and not warned here.
func (o Options) sweepsLeftovers(l dungeon.Leg) bool {
	if !o.CarrierChests || l.CompanionsLoot == nil || *l.CompanionsLoot {
		return false
	}
	for _, st := range l.Steps {
		if st.LootObject != nil {
			return true
		}
	}
	return false
}

// unexpressed lists the attributes of the plan's exported legs (legs before `upto`, the optional ones left out) that the rows cannot carry.
func unexpressed(p *dungeon.Plan, opt Options, upto int) []string {
	var order []string
	where := map[string][]string{}
	add := func(attr, at string) {
		if _, ok := where[attr]; !ok {
			order = append(order, attr)
		}
		where[attr] = append(where[attr], at)
	}
	if p.EntranceTrigger != 0 || p.EntranceTele != "" || p.OutsideTele != "" || p.Portal != nil {
		add("entrance/teleports/portal", "plan")
	}
	if len(p.LevelBand) > 0 {
		add("level_band", fmt.Sprintf("plan %v", p.LevelBand))
	}
	if len(p.HireComposition) > 0 {
		add("hire_composition", "plan "+strings.Join(p.HireComposition, ","))
	}
	if len(p.Fleers) > 0 {
		add("fleers", fmt.Sprintf("plan %v", p.Fleers))
	}
	if len(p.NoCC) > 0 {
		add("no_cc", fmt.Sprintf("plan %v", p.NoCC))
	}
	if len(p.SummonedKillFirst) > 0 {
		add("summoned_kill_first", fmt.Sprintf("plan %v", p.SummonedKillFirst))
	}
	if p.Raid != nil {
		add("raid", "plan")
	}
	if p.Watchdog != nil {
		add("watchdog", "plan")
	}
	if len(p.Avoid) > 0 && opt.Route == nil {
		add("avoid", fmt.Sprintf("plan, %d boxes", len(p.Avoid)))
	}
	for li, l := range p.Legs {
		if li >= upto {
			break
		}
		if (l.Optional != nil && opt.WithoutOptional) || slices.Contains(opt.SkipLegs, l.Name) {
			continue // left out of the export, with its own warning
		}
		leg := fmt.Sprintf("leg %d (%s)", li, l.Name)
		if len(l.Skip) > 0 {
			add("skip", fmt.Sprintf("%s %v", leg, l.Skip))
		}
		for _, lv := range l.Leave {
			add("leave", fmt.Sprintf("%s %d at %.1f %.1f", leg, lv.Entry, lv.At.X, lv.At.Y))
		}
		// The leftovers are carried anyway when the leg's chests are carrier rows: every exported chest's sweep pull names them.
		if len(l.LeftoverEntries) > 0 && !opt.sweepsLeftovers(l) {
			add("leftover_entries", fmt.Sprintf("%s %v", leg, l.LeftoverEntries))
		}
		if l.Rest != nil {
			add("rest", leg)
		}
		if l.CompanionsLoot != nil && !*l.CompanionsLoot && opt.DropCompanionsLoot {
			if opt.CarrierChests {
				add("companions_loot false (the chests exported)", leg)
			} else {
				add("companions_loot false", leg)
			}
		}
		if l.NeedsWalk {
			add("needs_walk", leg+" to")
		}
		for gi, g := range l.PullGroups {
			if g.Patrol != nil {
				add("patrol", fmt.Sprintf("%s pull group %d", leg, gi))
			}
			if g.NeedsWalk {
				add("needs_walk", fmt.Sprintf("%s pull group %d", leg, gi))
			}
		}
		if b := l.Boss; b != nil {
			if b.Tactics != nil {
				add("boss.tactics", fmt.Sprintf("%s boss %d", leg, b.Entry))
			}
			if b.CompanionsLoot != nil {
				add("boss.companions_loot", fmt.Sprintf("%s boss %d", leg, b.Entry))
			}
			if b.NeedsWalk {
				add("needs_walk", fmt.Sprintf("%s boss %d", leg, b.Entry))
			}
			if b.Hold != nil && len(b.First) > 0 {
				add("boss.first under a hold", fmt.Sprintf("%s boss %d first %v", leg, b.Entry, b.First))
			}
		}
		steps := func(list string, ss []dungeon.Step) {
			for si, st := range ss {
				at := fmt.Sprintf("%s %s[%d]", leg, list, si)
				ref := func(r dungeon.ObjectRef) {
					if r.Spawn != 0 || r.Range != 0 {
						add("object spawn/range", at)
					}
				}
				switch {
				case st.UseObject != nil:
					ref(st.UseObject.ObjectRef)
				case st.LootObject != nil:
					ref(st.LootObject.ObjectRef)
					if st.LootObject.Stand != nil {
						add("stand (loot_object, ritual)", at)
					}
					if st.LootObject.Batch != "" {
						add("loot_object batch", at)
					}
				case st.WaitFor != nil && st.WaitFor.Hold != nil:
					add("wait_for with a hold", at)
				case st.WaitFor != nil && st.WaitFor.AgentOnly != "":
					add("wait_for agent_only", fmt.Sprintf("%s %q", at, st.WaitFor.AgentOnly))
				case st.WaitFor != nil && st.WaitFor.GameObject != nil:
					ref(st.WaitFor.GameObject.ObjectRef)
				case st.Ritual != nil:
					if st.Ritual.Stand != nil {
						add("stand (loot_object, ritual)", at)
					}
				case st.Gossip != nil:
					add("gossip stand/by/retry_s", at)
				case st.AreaTrigger != nil:
					add("area_trigger expect/timeout", at)
				}
			}
		}
		steps("steps", l.Steps)
		steps("after_boss", l.AfterBoss)
	}
	out := make([]string, 0, len(order))
	for _, attr := range order {
		out = append(out, fmt.Sprintf("%s (%s): not in the rows: %s", attr, strings.Join(where[attr], "; "), unexpressedWhy[attr]))
	}
	return out
}
