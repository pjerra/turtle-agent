package session

import (
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/spellstate"
)

// registerSpells installs the handlers of the agent's own spell state (internal/spellstate): the
// spellbook, cooldowns and channels. The cast packets (spell_start, spell_go, cast result) are
// handled in combat.go, which feeds the same state.
func (s *Session) registerSpells() {
	s.handlers[opcode.SMSGInitialSpells] = s.onInitialSpells
	s.handlers[opcode.SMSGRemovedSpell] = s.onRemovedSpell
	s.handlers[opcode.SMSGSupercededSpell] = s.onSupercededSpell
	s.handlers[opcode.SMSGSpellCooldown] = s.onSpellCooldown
	s.handlers[opcode.SMSGCooldownEvent] = s.onCooldownEvent
	s.handlers[opcode.SMSGClearCooldown] = s.onClearCooldown
	s.handlers[opcode.SMSGCooldownCheat] = s.onCooldownCheat
	s.handlers[opcode.MSGChannelStart] = s.onChannelStart
	s.handlers[opcode.MSGChannelUpdate] = s.onChannelUpdate
}

// SpellState is the agent's own spellbook, cooldowns and cast state, for a brain to read.
func (s *Session) SpellState() *spellstate.State { return s.spellState }

func (s *Session) onInitialSpells(body []byte) error {
	in, err := game.ParseInitialSpells(body)
	if err != nil {
		return err
	}
	s.spellState.SetInitial(in)
	return nil
}

func (s *Session) onRemovedSpell(body []byte) error {
	id, err := game.ParseRemovedSpell(body)
	if err != nil {
		return err
	}
	if s.spellState.Remove(id) {
		s.emit(events.Event{Kind: events.KindSpellRemoved, SourceGUID: s.selfGUID(), SpellID: id})
	}
	return nil
}

func (s *Session) onSupercededSpell(body []byte) error {
	old, by, err := game.ParseSupercededSpell(body)
	if err != nil {
		return err
	}
	s.spellState.Supersede(old, by)
	s.emit(events.Event{Kind: events.KindSpellSuperseded, SourceGUID: s.selfGUID(), SpellID: old, Data: map[string]any{"by": by}})
	return nil
}

// emitCooldownStart reports a cooldown that began.
func (s *Session) emitCooldownStart(st spellstate.Started) {
	data := map[string]any{"ms": st.MS, "source": st.Source}
	if st.Category != 0 {
		data["category"] = st.Category
		data["category_ms"] = st.CategoryMS
	}
	s.emit(events.Event{Kind: events.KindCooldownStart, SourceGUID: s.selfGUID(), SpellID: st.Spell, Data: data})
}

// onSpellCooldown is a cooldown the server set on a unit: a school lockout after an interrupt
// (Player.cpp:4720-4747), a script. The ones on the player are applied; a pet's are not ours.
func (s *Session) onSpellCooldown(body []byte) error {
	c, err := game.ParseSpellCooldown(body)
	if err != nil {
		return err
	}
	if c.Unit != s.selfGUID() {
		return nil
	}
	for _, cd := range c.Cooldowns {
		s.emitCooldownStart(s.spellState.ServerCooldown(cd.Spell, cd.MS))
	}
	return nil
}

// onCooldownEvent starts a cooldown now, from the spell's own data (Unit.cpp:11822-11836): the
// server sends it when an aura that held a spell back ends.
func (s *Session) onCooldownEvent(body []byte) error {
	e, err := game.ParseCooldownEvent(body)
	if err != nil {
		return err
	}
	if e.Unit != s.selfGUID() {
		return nil
	}
	if st, ok := s.spellState.StartCooldown(e.Spell, spellstate.SourceEvent); ok {
		s.emitCooldownStart(st)
	}
	return nil
}

func (s *Session) onClearCooldown(body []byte) error {
	e, err := game.ParseClearCooldown(body)
	if err != nil {
		return err
	}
	if e.Unit != s.selfGUID() {
		return nil
	}
	if s.spellState.Clear(e.Spell) {
		s.emit(events.Event{Kind: events.KindCooldownClear, SourceGUID: s.selfGUID(), SpellID: e.Spell})
	}
	return nil
}

func (s *Session) onCooldownCheat(body []byte) error {
	g, err := game.ParseCooldownCheat(body)
	if err != nil {
		return err
	}
	if g != s.selfGUID() {
		return nil
	}
	ids := s.spellState.ClearAll()
	s.emit(events.Event{Kind: events.KindCooldownClear, SourceGUID: g, Data: map[string]any{"all": true, "spells": len(ids)}})
	return nil
}

func (s *Session) onChannelStart(body []byte) error {
	spell, ms, err := game.ParseChannelStart(body)
	if err != nil {
		return err
	}
	s.spellState.ChannelStarted(spell, ms)
	s.emit(events.Event{Kind: events.KindChannelStart, SourceGUID: s.selfGUID(), SpellID: spell, Data: map[string]any{"ms": ms}})
	return nil
}

func (s *Session) onChannelUpdate(body []byte) error {
	left, err := game.ParseChannelUpdate(body)
	if err != nil {
		return err
	}
	if ended, spell := s.spellState.ChannelUpdated(left); ended {
		s.emit(events.Event{Kind: events.KindChannelEnd, SourceGUID: s.selfGUID(), SpellID: spell})
	}
	return nil
}

// tickSpells reports the cooldowns that ran out since the last tick.
func (s *Session) tickSpells() {
	for _, id := range s.spellState.Expired() {
		s.emit(events.Event{Kind: events.KindCooldownEnd, SourceGUID: s.selfGUID(), SpellID: id})
	}
}

// comboTarget is the guid in PLAYER_FIELD_COMBO_TARGET, two words.
func (s *Session) comboTarget() packet.GUID {
	self := s.selfGUID()
	return packet.GUID(uint64(s.store.Field(self, objects.PlayerComboTarget)) | uint64(s.store.Field(self, objects.PlayerComboTarget+1))<<32)
}

// ComboPoints is the player's combo points (0-5) and the unit they are on (0 with no points),
// from its own update fields: PLAYER_FIELD_BYTES byte 1 and PLAYER_FIELD_COMBO_TARGET.
func (s *Session) ComboPoints() (points uint8, target packet.GUID) {
	points = uint8(s.store.Field(s.selfGUID(), objects.PlayerFieldBytes) >> 8 & 0xFF)
	if points == 0 {
		return 0, 0
	}
	return points, s.comboTarget()
}

// GCDInfo is the global cooldown.
type GCDInfo struct {
	Active      bool  `json:"active"`
	RemainingMS int64 `json:"remaining_ms"`
	TotalMS     int64 `json:"total_ms"`
}

// CastStateInfo is the cast_state result: the cast, the last success and failure, the global
// cooldown and the combo points.
type CastStateInfo struct {
	spellstate.CastState
	GCD         GCDInfo     `json:"gcd"`
	ComboPoints uint8       `json:"combo_points"`
	ComboTarget packet.GUID `json:"combo_target,omitempty"`
}

// CastState reads the player's cast state.
func (s *Session) CastState() (CastStateInfo, error) {
	if err := s.inWorld(); err != nil {
		return CastStateInfo{}, err
	}
	total, left := s.spellState.GCD()
	pts, target := s.ComboPoints()
	return CastStateInfo{CastState: s.spellState.CastState(), ComboPoints: pts, ComboTarget: target,
		GCD: GCDInfo{Active: left > 0, RemainingMS: left.Milliseconds(), TotalMS: total.Milliseconds()}}, nil
}

// EffectRow is one effect of a spell.
type EffectRow struct {
	Effect        uint8   `json:"effect"`              // SPELL_EFFECT_*
	Aura          uint8   `json:"aura,omitempty"`      // SPELL_AURA_* of an apply-aura effect
	Base          int32   `json:"base"`                // EffectBasePoints
	Dice          int32   `json:"dice,omitempty"`      // EffectBaseDice, added to a value of one die side or none
	Sides         int32   `json:"sides,omitempty"`     // EffectDieSides: above 1 a roll of dice..sides is added
	PerLevel      float32 `json:"per_level,omitempty"` // per caster level above the spell's level
	PerComboPoint float32 `json:"per_combo_point,omitempty"`
}

// ReagentRow is an item a spell uses up.
type ReagentRow struct {
	Item  uint32 `json:"item_entry"`
	Count uint32 `json:"count"`
}

// SpellRow is one spell of the spellbook with its metadata and whether it can be cast now.
type SpellRow struct {
	SpellID uint32 `json:"spell_id"`
	Name    string `json:"name"`
	Rank    uint8  `json:"rank,omitempty"`
	// Highest is true for the highest known rank of the name (the only one of a spell without ranks).
	Highest bool `json:"highest"`
	// Meta is false for a spell the generated table does not hold: only its id and name are known.
	Meta         bool         `json:"meta"`
	Level        uint8        `json:"level,omitempty"`
	Passive      bool         `json:"passive,omitempty"`
	Flags        []string     `json:"flags"`
	CastMS       uint32       `json:"cast_ms"`
	RangeMin     float32      `json:"range_min,omitempty"`
	RangeMax     float32      `json:"range_max,omitempty"`
	Power        string       `json:"power,omitempty"` // mana, rage, focus, energy, health; omitted for a spell with no cost
	Cost         uint32       `json:"cost,omitempty"`
	CostPct      uint32       `json:"cost_pct,omitempty"` // percent of base mana instead of Cost
	CooldownMS   uint32       `json:"cooldown_ms,omitempty"`
	Category     uint32       `json:"category,omitempty"`
	CategoryCDMS uint32       `json:"category_cd_ms,omitempty"`
	GCDMS        uint32       `json:"gcd_ms"`
	School       string       `json:"school,omitempty"`
	Dispel       string       `json:"dispel,omitempty"`
	Mechanic     uint8        `json:"mechanic,omitempty"`
	DurationMS   int32        `json:"duration_ms,omitempty"`
	Effects      []EffectRow  `json:"effects"`
	Reagents     []ReagentRow `json:"reagents,omitempty"`
	// Ready is false while the spell's cooldown, its category's or the global cooldown runs, or the
	// server refused it as not ready a moment ago; RemainingMS says how long, Why what holds it.
	Ready       bool   `json:"ready"`
	RemainingMS int64  `json:"remaining_ms"`
	Why         string `json:"why,omitempty"`
	// Affordable is whether the player holds the flat cost now; null for a cost in percent of base
	// mana, which the agent cannot work out.
	Affordable *bool `json:"affordable,omitempty"`
}

// SpellsFilter selects the rows of Spells.
type SpellsFilter struct {
	SpellID uint32
	Name    string // a part of the name, case-insensitive
	Highest bool   // only the highest known rank of each name
	Passive bool   // include passive spells (talents, proficiencies, racials)
}

var schoolNames = []string{"physical", "holy", "fire", "nature", "frost", "shadow", "arcane"}
var powerNames = map[int8]string{spells.PowerMana: "mana", spells.PowerRage: "rage", spells.PowerFocus: "focus", spells.PowerEnergy: "energy", spells.PowerHealth: "health"}
var dispelNames = []string{"", "magic", "curse", "disease", "poison", "stealth", "invisibility", "all", "npc_only", "enrage", "zg_ticket"}

// Spells lists the player's known spells with their metadata, cooldowns and whether each can be
// cast now, sorted by name and rank.
func (s *Session) Spells(f SpellsFilter) ([]SpellRow, error) {
	if err := s.inWorld(); err != nil {
		return nil, err
	}
	st := s.spellState
	self := s.selfGUID()
	needle := strings.ToLower(strings.TrimSpace(f.Name))
	top := map[string]uint32{} // lower-case name -> highest known id
	for _, id := range st.KnownIDs() {
		name := strings.ToLower(spells.Name(id))
		if cur, ok := top[name]; !ok || rankLess(cur, id) {
			top[name] = id
		}
	}
	rows := []SpellRow{}
	for _, id := range st.KnownIDs() {
		name := spells.Name(id)
		if f.SpellID != 0 && id != f.SpellID {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(name), needle) {
			continue
		}
		m, hasMeta := spells.Lookup(id)
		if m.Flags.Has(spells.FlagPassive) && !f.Passive && f.SpellID == 0 {
			continue
		}
		highest := top[strings.ToLower(name)] == id
		if f.Highest && !highest {
			continue
		}
		left, why := st.Remaining(id)
		row := SpellRow{SpellID: id, Name: name, Rank: m.Rank, Highest: highest, Meta: hasMeta, Level: m.Level,
			Passive: m.Flags.Has(spells.FlagPassive), Flags: m.Flags.Names(), CastMS: m.CastMS, RangeMin: m.RangeMin, RangeMax: m.RangeMax,
			Cost: m.Cost, CostPct: m.CostPct, CooldownMS: m.CooldownMS, Category: m.Category, CategoryCDMS: m.CategoryCDMS, GCDMS: m.GCDMS,
			Mechanic: m.Mechanic, DurationMS: m.DurationMS, Effects: []EffectRow{}, Ready: left <= 0, RemainingMS: max(left.Milliseconds(), 0), Why: why}
		if m.Cost > 0 || m.CostPct > 0 {
			row.Power = powerNames[m.Power]
		}
		if int(m.School) < len(schoolNames) && hasMeta {
			row.School = schoolNames[m.School]
		}
		if int(m.Dispel) < len(dispelNames) {
			row.Dispel = dispelNames[m.Dispel]
		}
		for _, e := range m.Effects {
			if e.Effect != 0 || e.Aura != 0 {
				row.Effects = append(row.Effects, EffectRow{Effect: e.Effect, Aura: e.Aura, Base: e.Base, Dice: e.Dice, Sides: e.Sides, PerLevel: e.PerLevel, PerComboPoint: e.PerComboPoint})
			}
		}
		for _, r := range m.Reagents {
			row.Reagents = append(row.Reagents, ReagentRow{Item: r.Item, Count: r.Count})
		}
		if hasMeta && m.CostPct == 0 {
			ok := s.affordable(self, m)
			row.Affordable = &ok
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := strings.ToLower(rows[i].Name), strings.ToLower(rows[j].Name); a != b {
			return a < b
		}
		if rows[i].Rank != rows[j].Rank {
			return rows[i].Rank < rows[j].Rank
		}
		return rows[i].SpellID < rows[j].SpellID
	})
	return rows, nil
}

// rankLess orders two spell ids of one name by rank, then id.
func rankLess(a, b uint32) bool {
	ma, _ := spells.Lookup(a)
	mb, _ := spells.Lookup(b)
	if ma.Rank != mb.Rank {
		return ma.Rank < mb.Rank
	}
	return a < b
}

// affordable reports whether the player holds the flat cost of m in the power it is paid in.
// A rage cost and the rage held are both in tenths.
func (s *Session) affordable(self packet.GUID, m spells.Meta) bool {
	if m.Cost == 0 {
		return true
	}
	if m.Power == spells.PowerHealth {
		return s.store.Field(self, objects.UnitHealth) > m.Cost
	}
	if m.Power < spells.PowerMana || m.Power > spells.PowerEnergy {
		return false
	}
	return s.store.Field(self, objects.UnitPower1+uint16(m.Power)) >= m.Cost
}

// spellGCDMS is the global cooldown left, in ms, for status and the player's unit row.
func (s *Session) spellGCDMS() int64 {
	_, left := s.spellState.GCD()
	return left.Milliseconds()
}
