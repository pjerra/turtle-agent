package brain

import (
	"slices"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/tactics"
)

// Boss tactics for the agent's own character (DESIGN-DUNGEON-MODES P2). A dungeon plan lists, per boss, what a player does on the fight
// beyond the class rotation (internal/tactics: kill_first, never_target, interrupt, dispel, no_dispel, move_out). dungeon_run in leader
// mode hands the plan's bosses to the brain (Params.Encounters); while a boss, or one of the adds the plan lists with it, is in view,
// alive and in combat, the encounter is ON and the brain bends four of its existing rules:
//
//   - target: ValFight (what the brain fights) takes the nearest kill_first unit before anything else, and drops a never_target unit
//     (unless it is attacking the player: a mob that has the player is fought, as everywhere);
//   - interrupts: an enemy cast on the interrupt list is the first to interrupt (priority 4, above heals and crowd control), by id or
//     name, by the class's own interrupt spell (the table in class.go). A cast the spell data says cannot be interrupted still is not;
//   - dispels: a healer preset dispels an aura on the dispel list first, at any mana, and leaves one on the no_dispel list alone;
//   - move out: a cast on a move_out rule makes a circle round its caster a ground hazard (avoid_ground walks out of it), and a ground
//     effect of a listed spell is a hazard whoever cast it. A tanking player holds its place: it ignores the cast circles.
//
// Behaviour after the ideas of mod-playerbots' dungeon and raid strategies (kill the add first, interrupt, dispel, leave the area;
// GPL-2.0+, ideas only, no code); the numbers and ids are the plan's, from the Turtle core data.

// Encounter is one boss of the plan as the brain needs it.
type Encounter struct {
	Name string // for the events: "uldaman/sentinel"
	// Entries are the creature entries that switch the encounter on: the boss and the units the plan kills before it.
	Entries []uint32
	Tactics tactics.Tactics
}

// ActiveEncounter is the encounter that is on now and the unit that switched it on (the nearest).
type ActiveEncounter struct {
	E    *Encounter
	Unit *objects.Unit
}

// killFirstRange is how far a kill_first unit may stand and still be taken as the target.
const killFirstRange = 40.0

// ValEncounter is the encounter that is on now, nil when none is.
var ValEncounter = &Value[*ActiveEncounter]{Name: "encounter", Calc: func(c *Ctx) *ActiveEncounter {
	if len(c.P.Encounters) == 0 {
		return nil
	}
	for i := range c.Units { // nearest first
		u := &c.Units[i]
		if !hostileMob(u) || !u.InCombat || u.Entry == 0 {
			continue
		}
		for j := range c.P.Encounters {
			if e := &c.P.Encounters[j]; slices.Contains(e.Entries, u.Entry) {
				return &ActiveEncounter{E: e, Unit: u}
			}
		}
	}
	return nil
}}

// tacticsNow is the tactics of the encounter that is on, nil when none is.
func tacticsNow(c *Ctx) *tactics.Tactics {
	if a := ValEncounter.Get(c); a != nil {
		return &a.E.Tactics
	}
	return nil
}

// neverTarget says the plan wants the unit left alone (ValFight skips it as the tank's mob or the selected one; a mob that attacks the
// player is fought as ever).
func neverTarget(c *Ctx, u *objects.Unit) bool {
	t := tacticsNow(c)
	return t != nil && u != nil && slices.Contains(t.NeverTarget, u.Entry)
}

// applyTactics bends the fight's mob by the encounter's kill_first. It runs inside ValFight, before the fight's clock.
func applyTactics(c *Ctx, f *Fight) {
	t := tacticsNow(c)
	if t == nil || len(t.KillFirst) == 0 {
		return
	}
	for i := range c.Units { // nearest first
		u := &c.Units[i]
		if !hostileMob(u) || !u.InCombat || u.Distance < 0 || float64(u.Distance) > killFirstRange || !slices.Contains(t.KillFirst, u.Entry) {
			continue
		}
		if f.Mob == nil || f.Mob.GUID != u.GUID {
			f.Mob, f.Source = u, "kill_first"
		}
		return
	}
}

// tacticInterrupt says the plan wants this enemy cast interrupted first.
func tacticInterrupt(c *Ctx, spell uint32) bool {
	t := tacticsNow(c)
	if t == nil {
		return false
	}
	for _, r := range t.Interrupt {
		if r.Matches(spell, spells.Name) {
			return true
		}
	}
	return false
}

// tacticDispel says the plan wants this aura dispelled first.
func tacticDispel(c *Ctx, a objects.Aura) bool {
	t := tacticsNow(c)
	if t == nil {
		return false
	}
	for _, r := range t.Dispel {
		if r.MatchesAura(a.SpellID, a.Name, spells.Name) {
			return true
		}
	}
	return false
}

// tacticNoDispel says the plan wants this aura left alone.
func tacticNoDispel(c *Ctx, a objects.Aura) bool {
	t := tacticsNow(c)
	if t == nil {
		return false
	}
	for _, r := range t.NoDispel {
		if r.MatchesAura(a.SpellID, a.Name, spells.Name) {
			return true
		}
	}
	return false
}

// tacticGround says a ground effect of this spell is a hazard whoever cast it.
func tacticGround(c *Ctx, spell uint32) bool {
	t := tacticsNow(c)
	if t == nil {
		return false
	}
	for _, m := range t.MoveOut {
		if m.Ground != nil && m.Ground.Matches(spell, spells.Name) {
			return true
		}
	}
	return false
}

// tacticCastHazards are the circles round the enemies that cast a spell of a move_out rule now. A tanking player stays.
func tacticCastHazards(c *Ctx) []Hazard {
	t := tacticsNow(c)
	if t == nil || c.P.Role == RoleTank {
		return nil
	}
	var out []Hazard
	for _, m := range t.MoveOut {
		if m.Cast == nil {
			continue
		}
		for i := range c.Units {
			u := &c.Units[i]
			if !hostileMob(u) || u.Casting == nil || u.Distance < 0 || !m.Cast.Matches(u.Casting.SpellID, spells.Name) {
				continue
			}
			out = append(out, Hazard{Center: unitPos(u), Radius: m.YardsOf(), Spell: u.Casting.SpellID})
		}
	}
	return out
}

// encounterStrategy follows the encounter and says so (brain_state, state "encounter"): when it starts and when it ends. The rules it
// bends live where the behaviour is (ValFight, castPriority, calcDispel, hazards).
func encounterStrategy() *Strategy {
	return &Strategy{
		Name: "encounter",
		Before: func(c *Ctx) {
			name := ""
			if a := ValEncounter.Get(c); a != nil {
				name = a.E.Name
			}
			c.mem.mu.Lock()
			was := c.mem.encounter
			c.mem.encounter = name
			c.mem.mu.Unlock()
			if name == was {
				return
			}
			state, shown := "encounter", name
			if name == "" {
				state, shown = "encounter_end", was
			}
			c.G.Emit(events.Event{Kind: events.KindBrainState, Text: state + " " + shown,
				Data: map[string]any{"state": state, "encounter": shown, "run_id": c.P.RunID}})
		},
	}
}

func init() { strategyBuilders["encounter"] = encounterStrategy }
