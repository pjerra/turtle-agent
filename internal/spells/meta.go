package spells

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed meta.tsv.gz
var packedMeta []byte

// Flags say what a spell is, from its attributes and effects. They are computed by
// tools/spellnamegen, so the numbers are part of meta.tsv.gz; do not renumber.
type Flags uint32

const (
	FlagPassive        Flags = 1 << iota // SPELL_ATTR_PASSIVE: always on, never cast
	FlagRanged                           // SPELL_ATTR_RANGED: a bow, wand or thrown weapon ability
	FlagNextSwing                        // SPELL_ATTR_ON_NEXT_SWING_1/2: Heroic Strike, Maul, Raptor Strike
	FlagChanneled                        // SPELL_ATTR_EX_CHANNELED_1/2
	FlagComboPoints                      // SPELL_ATTR_EX_REQ_(TARGET_)COMBO_POINTS, SpellEntry::NeedsComboPoints: a finisher
	FlagHarmful                          // SPELL_ATTR_NEGATIVE: aimed at an enemy
	FlagNotInCombat                      // SPELL_ATTR_CANT_USED_IN_COMBAT
	FlagStealthOnly                      // SPELL_ATTR_ONLY_STEALTHED
	FlagMoveInterrupts                   // movement breaks the cast or channel
	FlagInterruptible                    // an interrupt (Pummel, Kick, Counterspell) stops the cast or channel
	FlagAutoRepeat                       // SPELL_ATTR_EX2_AUTOREPEAT_FLAG: Auto Shot, Shoot
	FlagCooldownOnFade                   // SPELL_ATTR_DISABLED_WHILE_ACTIVE: the cooldown starts when the aura ends (SMSG_COOLDOWN_EVENT), not at the cast
	FlagDamage                           // an effect or aura deals damage (direct, weapon, periodic, leech)
	FlagHeal                             // an effect or aura heals
	FlagAura                             // an effect applies an aura
	FlagDispel                           // an effect dispels
	FlagInterrupt                        // an effect interrupts a cast
	FlagResurrect                        // an effect resurrects
	FlagEnergize                         // an effect gives power
	FlagTaunt                            // an effect or aura taunts
)

// Has reports whether every flag of f is set.
func (fl Flags) Has(f Flags) bool { return fl&f == f }

var flagNames = []string{"passive", "ranged", "next_swing", "channeled", "combo_points", "harmful", "not_in_combat", "stealth_only",
	"move_interrupts", "interruptible", "auto_repeat", "cooldown_on_fade", "damage", "heal", "aura", "dispel", "interrupt",
	"resurrect", "energize", "taunt"}

// Names lists the set flags by name, in bit order: ["ranged", "damage"].
func (fl Flags) Names() []string {
	out := []string{}
	for i, n := range flagNames {
		if fl&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// Effect is one of a spell's three effects. A direct effect's value is Base + Dice, plus a
// roll of 1..Sides when Sides is above 1, plus PerLevel for each caster level above the
// spell's level, plus PerComboPoint for each combo point (Object.cpp:4395-4440).
type Effect struct {
	Effect        uint8 // SPELL_EFFECT_* (SpellDefines.h:140-275)
	Aura          uint8 // SPELL_AURA_* when Effect applies an aura (SpellAuraDefines.h)
	Base, Dice    int32
	Sides         int32
	PerLevel      float32
	PerComboPoint float32
}

// Reagent is an item a cast uses up.
type Reagent struct {
	Item, Count uint32
}

// Power types of a spell's cost (Powers, UnitDefines.h; -2 is health).
const (
	PowerMana   int8 = 0
	PowerRage   int8 = 1
	PowerFocus  int8 = 2
	PowerEnergy int8 = 3
	PowerHealth int8 = -2
)

// Meta is what the core's spell_template and the server's DBCs say about one spell. Rage
// costs and the rage a unit holds are both in tenths, as the DBC and the update fields have them.
type Meta struct {
	ID    uint32
	Rank  uint8 // the N of "Rank N" in the spell's subtext; 0 when it has none
	Level uint8 // the character level the spell needs (SpellLevel)
	// BaseLevel and MaxLevel bound the level scaling of the effects.
	BaseLevel, MaxLevel uint8
	Power               int8   // the power the cost is paid in
	Cost                uint32 // flat cost
	CostPct             uint32 // percent of the caster's base mana, instead of Cost
	CastMS              uint32 // base cast time; 0 for an instant spell
	RangeMin, RangeMax  float32
	CooldownMS          uint32 // the spell's own cooldown
	Category            uint32 // spells sharing a category share CategoryCDMS
	CategoryCDMS        uint32
	GCDCategory         uint32 // the global cooldown group the spell waits for and starts (133 for most)
	GCDMS               uint32 // 0 when the spell starts no global cooldown
	School              uint8  // 0 physical, 1 holy, 2 fire, 3 nature, 4 frost, 5 shadow, 6 arcane
	Dispel              uint8  // DispelType (SpellDefines.h:611-623)
	Mechanic            uint8  // Mechanics (SpellDefines.h:553-589)
	DurationMS          int32  // the aura's base duration; -1 or 0 for none
	Flags               Flags
	Effects             [3]Effect
	Reagents            []Reagent
}

var (
	metaOnce sync.Once
	metas    map[uint32]*Meta
)

// Lookup is the metadata of spell id, false for an id the table does not hold: a spell that
// came with the server's database after the table was made, or one with nothing to say.
func Lookup(id uint32) (Meta, bool) {
	metaOnce.Do(func() {
		var err error
		if metas, err = ParseMeta(bytes.NewReader(packedMeta)); err != nil {
			metas = map[uint32]*Meta{}
		}
	})
	m, ok := metas[id]
	if !ok {
		return Meta{}, false
	}
	return *m, true
}

// Warm loads every table now. The first Lookup parses the whole metadata (28000 lines, about
// 20 ms, ten times that under the race detector), which a session's loop should not do in the middle of a fight.
func Warm() {
	Name(0)
	IsMount(0)
	Lookup(0)
}

// Cooldown is the spell's own cooldown as a duration.
func (m Meta) Cooldown() time.Duration { return time.Duration(m.CooldownMS) * time.Millisecond }

// CastTime is the spell's base cast time as a duration.
func (m Meta) CastTime() time.Duration { return time.Duration(m.CastMS) * time.Millisecond }

// ParseMeta reads a meta.tsv.gz stream: the table of every spell with something to say.
func ParseMeta(r io.Reader) (map[uint32]*Meta, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	out := map[uint32]*Meta{}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		m, err := parseMetaLine(line)
		if err != nil {
			return nil, fmt.Errorf("meta line %d: %w", n, err)
		}
		out[m.ID] = m
	}
	return out, sc.Err()
}

const metaColumns = 25

func parseMetaLine(line string) (*Meta, error) {
	c := strings.Split(line, "\t")
	if len(c) != metaColumns {
		return nil, fmt.Errorf("%d columns, want %d", len(c), metaColumns)
	}
	var err error
	i := func(s string) int64 {
		if s == "" {
			return 0
		}
		v, e := strconv.ParseInt(s, 10, 64)
		if e != nil && err == nil {
			err = e
		}
		return v
	}
	f := func(s string) float32 {
		if s == "" {
			return 0
		}
		v, e := strconv.ParseFloat(s, 32)
		if e != nil && err == nil {
			err = e
		}
		return float32(v)
	}
	m := &Meta{
		ID: uint32(i(c[0])), Rank: uint8(i(c[1])), Level: uint8(i(c[2])), BaseLevel: uint8(i(c[3])), MaxLevel: uint8(i(c[4])),
		Power: int8(i(c[5])), Cost: uint32(i(c[6])), CostPct: uint32(i(c[7])), CastMS: uint32(i(c[8])),
		RangeMin: f(c[9]), RangeMax: f(c[10]), CooldownMS: uint32(i(c[11])), Category: uint32(i(c[12])), CategoryCDMS: uint32(i(c[13])),
		GCDCategory: uint32(i(c[14])), GCDMS: uint32(i(c[15])), School: uint8(i(c[16])), Dispel: uint8(i(c[17])), Mechanic: uint8(i(c[18])),
		DurationMS: int32(i(c[19])), Flags: Flags(i(c[20])),
	}
	for k := 0; k < 3; k++ {
		if c[21+k] == "" {
			continue
		}
		p := strings.Split(c[21+k], ",")
		for len(p) < 7 {
			p = append(p, "")
		}
		m.Effects[k] = Effect{Effect: uint8(i(p[0])), Aura: uint8(i(p[1])), Base: int32(i(p[2])), Dice: int32(i(p[3])), Sides: int32(i(p[4])),
			PerLevel: f(p[5]), PerComboPoint: f(p[6])}
	}
	if c[24] != "" {
		for _, rg := range strings.Split(c[24], ",") {
			item, count, ok := strings.Cut(rg, "*")
			if !ok {
				return nil, fmt.Errorf("reagent %q", rg)
			}
			m.Reagents = append(m.Reagents, Reagent{Item: uint32(i(item)), Count: uint32(i(count))})
		}
	}
	return m, err
}

// FlagInput is what FlagsOf reads: the template's attribute words, interrupt flags and the
// (effect, aura) pair of each effect.
type FlagInput struct {
	Attributes, AttributesEx, AttributesEx2 uint32
	InterruptFlags, ChannelInterruptFlags   uint32
	Effects                                 [3][2]int64
}

// FlagsOf derives a spell's Flags. Constants are from SpellDefines.h (attributes :722-856,
// interrupt flags :436-455, effects :140-275) and SpellAuraDefines.h.
func FlagsOf(in FlagInput) Flags {
	var fl Flags
	set := func(cond bool, f Flags) {
		if cond {
			fl |= f
		}
	}
	set(in.Attributes&0x40 != 0, FlagPassive)
	set(in.Attributes&0x02 != 0, FlagRanged)
	set(in.Attributes&(0x04|0x400) != 0, FlagNextSwing)
	set(in.AttributesEx&(0x04|0x40) != 0, FlagChanneled)
	set(in.AttributesEx&(0x100000|0x400000) != 0, FlagComboPoints)
	set(in.Attributes&0x04000000 != 0, FlagHarmful)
	set(in.Attributes&0x10000000 != 0, FlagNotInCombat)
	set(in.Attributes&0x20000 != 0, FlagStealthOnly)
	set(in.InterruptFlags&0x01 != 0 || in.ChannelInterruptFlags&0x08 != 0, FlagMoveInterrupts)
	set(in.InterruptFlags&0x04 != 0 || in.ChannelInterruptFlags&0x04 != 0, FlagInterruptible)
	set(in.AttributesEx2&0x20 != 0, FlagAutoRepeat)
	set(in.Attributes&0x02000000 != 0, FlagCooldownOnFade)
	for _, e := range in.Effects {
		effect, aura := e[0], e[1]
		appliesAura := false
		switch effect {
		case 2, 9, 17, 31, 58, 121: // school damage, health leech, weapon damage (no school, percent, normal), normalized weapon damage
			fl |= FlagDamage
		case 10, 67, 75: // heal, heal max health, heal mechanical
			fl |= FlagHeal
		case 6, 27, 35, 128, 129, 132, 133: // apply aura, persistent area aura, area auras (party, friend, enemy, raid, owner)
			fl |= FlagAura
			appliesAura = true
		case 38, 108: // dispel, dispel mechanic
			fl |= FlagDispel
		case 68: // interrupt cast
			fl |= FlagInterrupt
		case 18, 113: // resurrect, resurrect new
			fl |= FlagResurrect
		case 30: // energize
			fl |= FlagEnergize
		case 114: // attack me
			fl |= FlagTaunt
		}
		if appliesAura {
			switch aura {
			case 3, 53, 89: // periodic damage, periodic leech, periodic damage percent
				fl |= FlagDamage
			case 8, 20: // periodic heal, obs mod health
				fl |= FlagHeal
			case 11: // mod taunt
				fl |= FlagTaunt
			}
		}
	}
	return fl
}
