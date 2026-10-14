// Package tactics is the per-boss tactics block of a dungeon plan (plan field bosses[].tactics, phase P2 of
// DESIGN-DUNGEON-MODES): the four things a player on a boss fight does beyond the class rotation. It holds the file format only; the
// bot brain (internal/brain) acts on it for the agent's own character when the agent leads and plays (mode leader).
//
// The concepts are the ones mod-playerbots' dungeon and raid strategies are made of (kill the add first, interrupt the cast, dispel
// the aura, leave the area), written down here as data. Nothing is copied from it (GPL-2.0+, behaviour only); the entries and spell
// ids of the shipped plans come from the Turtle core's creature_spells and EventAI tables.
//
//	"tactics": {
//	  "kill_first":   [7209],                              creature entries to kill before anything else while the boss fight is on
//	  "never_target": [12345],                             creature entries the leader never attacks (a scarab swarm, a ward)
//	  "interrupt":    [16006, "Lightning Bolt"],           enemy casts to interrupt first (a spell id or its name)
//	  "dispel":       ["Fevered Plague", 8600],            auras to dispel from the party first (healers)
//	  "no_dispel":    ["Mutating Injection"],              auras that hurt more when dispelled (healers leave them)
//	  "move_out": [{"cast": "Fire Nova", "yards": 12},     leave a circle of that radius round the caster while it casts the spell
//	               {"ground": 10132}]                      leave the ground effect (a dynamic object) of that spell, whoever cast it
//	}
package tactics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Limits: a plan is read by a person; a block bigger than this is a mistake.
const (
	MaxList     = 32
	MaxYards    = 60
	DefaultYard = 10
)

// SpellRef is a spell named by id or by name. In JSON it is a number or a string. A name matches every rank of the spell and any spell
// whose name is the same (the Turtle data has several "Fire Nova"s: the name is what the 1.12 client shows).
type SpellRef struct {
	ID   uint32
	Name string
}

// UnmarshalJSON reads a number (the id) or a string (the name).
func (s *SpellRef) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var n string
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		n = strings.TrimSpace(n)
		if n == "" {
			return fmt.Errorf("an empty spell name")
		}
		*s = SpellRef{Name: n}
		return nil
	}
	var id uint32
	if err := json.Unmarshal(b, &id); err != nil || id == 0 {
		return fmt.Errorf("a spell is an id (a positive number) or a name, not %s", b)
	}
	*s = SpellRef{ID: id}
	return nil
}

// MarshalJSON writes the id, or the name when there is no id.
func (s SpellRef) MarshalJSON() ([]byte, error) {
	if s.ID != 0 {
		return json.Marshal(s.ID)
	}
	return json.Marshal(s.Name)
}

// String is the id or the name, for messages.
func (s SpellRef) String() string {
	if s.ID != 0 {
		return fmt.Sprint(s.ID)
	}
	return s.Name
}

// Matches reports whether the spell (its id, and its name as nameOf gives it) is the one the reference names.
func (s SpellRef) Matches(id uint32, nameOf func(uint32) string) bool {
	if s.ID != 0 {
		return s.ID == id
	}
	return s.Name != "" && strings.EqualFold(s.Name, nameOf(id))
}

// MatchesAura reports whether the reference names an aura: by id, or by the aura's name (as the unit shows it) or its spell's name.
func (s SpellRef) MatchesAura(id uint32, name string, nameOf func(uint32) string) bool {
	if s.ID != 0 {
		return s.ID == id
	}
	return s.Name != "" && (strings.EqualFold(s.Name, name) || strings.EqualFold(s.Name, nameOf(id)))
}

// MoveOut is one reason to leave the place: Cast is an enemy cast in view (the leader leaves a circle of Yards round the caster
// while it casts), Ground is a ground effect of the spell (a dynamic object, whoever cast it; its own radius is used).
type MoveOut struct {
	Cast   *SpellRef `json:"cast,omitempty"`
	Ground *SpellRef `json:"ground,omitempty"`
	Yards  float32   `json:"yards,omitempty"` // with cast; 0 takes DefaultYard
}

// Tactics is the block of one boss.
type Tactics struct {
	KillFirst   []uint32   `json:"kill_first,omitempty"`
	NeverTarget []uint32   `json:"never_target,omitempty"`
	Interrupt   []SpellRef `json:"interrupt,omitempty"`
	Dispel      []SpellRef `json:"dispel,omitempty"`
	NoDispel    []SpellRef `json:"no_dispel,omitempty"`
	MoveOut     []MoveOut  `json:"move_out,omitempty"`
}

// Empty reports a block with nothing in it.
func (t *Tactics) Empty() bool {
	return t == nil || (len(t.KillFirst) == 0 && len(t.NeverTarget) == 0 && len(t.Interrupt) == 0 && len(t.Dispel) == 0 && len(t.NoDispel) == 0 && len(t.MoveOut) == 0)
}

// Validate checks the block; nameOK, when not nil, says a spell name exists in the spell data (the plan loader passes spells' name
// check, so that a typo is an error at the load and not a rule that never fires).
func (t *Tactics) Validate(nameOK func(string) bool) error {
	if t == nil {
		return nil
	}
	lists := []struct {
		what string
		n    int
	}{{"kill_first", len(t.KillFirst)}, {"never_target", len(t.NeverTarget)}, {"interrupt", len(t.Interrupt)}, {"dispel", len(t.Dispel)},
		{"no_dispel", len(t.NoDispel)}, {"move_out", len(t.MoveOut)}}
	for _, l := range lists {
		if l.n > MaxList {
			return fmt.Errorf("%s: %d entries, at most %d", l.what, l.n, MaxList)
		}
	}
	for _, e := range t.KillFirst {
		if e == 0 {
			return fmt.Errorf("kill_first: entry 0")
		}
	}
	for _, e := range t.NeverTarget {
		if e == 0 {
			return fmt.Errorf("never_target: entry 0")
		}
		for _, k := range t.KillFirst {
			if k == e {
				return fmt.Errorf("entry %d is in both kill_first and never_target", e)
			}
		}
	}
	check := func(what string, refs []SpellRef) error {
		for _, r := range refs {
			if r.ID == 0 && r.Name == "" {
				return fmt.Errorf("%s: an empty spell", what)
			}
			if r.ID == 0 && nameOK != nil && !nameOK(r.Name) {
				return fmt.Errorf("%s: no spell is named %q", what, r.Name)
			}
		}
		return nil
	}
	if err := check("interrupt", t.Interrupt); err != nil {
		return err
	}
	if err := check("dispel", t.Dispel); err != nil {
		return err
	}
	if err := check("no_dispel", t.NoDispel); err != nil {
		return err
	}
	for i, m := range t.MoveOut {
		if (m.Cast == nil) == (m.Ground == nil) {
			return fmt.Errorf("move_out[%d]: exactly one of cast and ground", i)
		}
		if m.Ground != nil && m.Yards != 0 {
			return fmt.Errorf("move_out[%d]: yards goes with cast (a ground effect has its own radius)", i)
		}
		if m.Yards < 0 || m.Yards > MaxYards {
			return fmt.Errorf("move_out[%d]: yards %v, use 1-%d", i, m.Yards, MaxYards)
		}
		r := m.Cast
		if r == nil {
			r = m.Ground
		}
		if err := check(fmt.Sprintf("move_out[%d]", i), []SpellRef{*r}); err != nil {
			return err
		}
	}
	return nil
}

// YardsOf is the radius of a cast rule.
func (m MoveOut) YardsOf() float32 {
	if m.Yards > 0 {
		return m.Yards
	}
	return DefaultYard
}
