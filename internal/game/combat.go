package game

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Hit info flags (UnitDefines.h:188-205).
const (
	HitInfoMiss     uint32 = 0x00000010
	HitInfoAbsorb   uint32 = 0x00000020
	HitInfoResist   uint32 = 0x00000040
	HitInfoCritical uint32 = 0x00000080
	HitInfoGlancing uint32 = 0x00004000
	HitInfoCrushing uint32 = 0x00008000
)

var victimStates = []string{"unaffected", "normal", "dodge", "parry", "interrupt", "block", "evade", "immune", "deflect"} // UnitDefines.h:175-186

// VictimState names a VictimState value; unknown values read "state_N".
func VictimState(v uint32) string {
	if int(v) < len(victimStates) {
		return victimStates[v]
	}
	return fmt.Sprintf("state_%d", v)
}

type SubDamage struct {
	School         uint32
	Coef           float32
	Damage, Absorb uint32
	Resist         int32
}

type MeleeHit struct {
	HitInfo          uint32
	Attacker, Victim packet.GUID
	Total            uint32
	Subs             []SubDamage
	VictimState      uint32
	SpellID          uint32 // the core always sends 0 here (Unit.cpp:4916)
	Blocked          uint32
}

func (m MeleeHit) Crit() bool { return m.HitInfo&HitInfoCritical != 0 }
func (m MeleeHit) Miss() bool { return m.HitInfo&HitInfoMiss != 0 }

// Outcome is "miss", or the victim state ("normal", "dodge", "parry", "block", …).
func (m MeleeHit) Outcome() string {
	if m.Miss() {
		return "miss"
	}
	return VictimState(m.VictimState)
}

// ParseAttackerStateUpdate decodes SMSG_ATTACKERSTATEUPDATE (Unit.cpp:4888-4921).
func ParseAttackerStateUpdate(body []byte) (MeleeHit, error) {
	r := packet.NewReader(body)
	m := MeleeHit{HitInfo: r.U32(), Attacker: r.PackedGUID(), Victim: r.PackedGUID(), Total: r.U32()}
	n := int(r.U8())
	for i := 0; i < n; i++ {
		m.Subs = append(m.Subs, SubDamage{School: r.U32(), Coef: r.F32(), Damage: r.U32(), Absorb: r.U32(), Resist: r.I32()})
	}
	m.VictimState = r.U32()
	r.U32()
	m.SpellID, m.Blocked = r.U32(), r.U32()
	if err := r.Err(); err != nil {
		return MeleeHit{}, fmt.Errorf("SMSG_ATTACKERSTATEUPDATE: %w", err)
	}
	return m, nil
}

// SpellHitCrit is SPELL_HIT_TYPE_CRIT (SpellDefines.h:112).
const SpellHitCrit uint32 = 0x02

type SpellDamage struct {
	Target, Attacker packet.GUID
	SpellID, Damage  uint32
	School           uint8 // SpellSchools index, 0 physical … 6 arcane (SpellDefines.h:652-661)
	Absorb           uint32
	Resist           int32
	Periodic         bool
	Blocked, HitInfo uint32
}

func (d SpellDamage) Crit() bool { return d.HitInfo&SpellHitCrit != 0 }

// ParseSpellNonMeleeDamageLog decodes SMSG_SPELLNONMELEEDAMAGELOG (Object.cpp:4319-4335).
func ParseSpellNonMeleeDamageLog(body []byte) (SpellDamage, error) {
	r := packet.NewReader(body)
	d := SpellDamage{Target: r.PackedGUID(), Attacker: r.PackedGUID(), SpellID: r.U32(), Damage: r.U32(), School: r.U8(),
		Absorb: r.U32(), Resist: r.I32(), Periodic: r.U8() != 0}
	r.U8()
	d.Blocked, d.HitInfo = r.U32(), r.U32()
	r.U8()
	if err := r.Err(); err != nil {
		return SpellDamage{}, fmt.Errorf("SMSG_SPELLNONMELEEDAMAGELOG: %w", err)
	}
	return d, nil
}

// Aura types sent in SMSG_PERIODICAURALOG (SpellAuraDefines.h:54-227).
const (
	AuraPeriodicDamage        uint32 = 3
	AuraPeriodicHeal          uint32 = 8
	AuraObsModHealth          uint32 = 20
	AuraObsModMana            uint32 = 21
	AuraPeriodicEnergize      uint32 = 24
	AuraPeriodicManaLeech     uint32 = 64
	AuraPeriodicDamagePercent uint32 = 89
)

type PeriodicTick struct {
	AuraType, Amount, School, Absorb uint32
	Resist                           int32
	PowerType                        uint32
	Multiplier                       float32
}

// Kind is "damage", "heal", "energize" or "leech" for the aura types ParsePeriodicAuraLog accepts.
func (t PeriodicTick) Kind() string {
	switch t.AuraType {
	case AuraPeriodicDamage, AuraPeriodicDamagePercent:
		return "damage"
	case AuraPeriodicHeal, AuraObsModHealth:
		return "heal"
	case AuraObsModMana, AuraPeriodicEnergize:
		return "energize"
	}
	return "leech"
}

type PeriodicLog struct {
	Target, Caster packet.GUID
	SpellID        uint32
	Ticks          []PeriodicTick
}

// ParsePeriodicAuraLog decodes SMSG_PERIODICAURALOG (Unit.cpp:4703-4744).
func ParsePeriodicAuraLog(body []byte) (PeriodicLog, error) {
	r := packet.NewReader(body)
	p := PeriodicLog{Target: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32()}
	n := r.U32() // the core always writes 1 (Unit.cpp:4713); 16 only guards a hostile count
	if n > 16 {
		return PeriodicLog{}, fmt.Errorf("SMSG_PERIODICAURALOG: %d ticks", n)
	}
	for i := uint32(0); i < n; i++ {
		t := PeriodicTick{AuraType: r.U32()}
		switch t.AuraType {
		case AuraPeriodicDamage, AuraPeriodicDamagePercent:
			t.Amount, t.School, t.Absorb, t.Resist = r.U32(), r.U32(), r.U32(), r.I32()
		case AuraPeriodicHeal, AuraObsModHealth:
			t.Amount = r.U32()
		case AuraObsModMana, AuraPeriodicEnergize:
			t.PowerType, t.Amount = r.U32(), r.U32()
		case AuraPeriodicManaLeech:
			t.PowerType, t.Amount, t.Multiplier = r.U32(), r.U32(), r.F32()
		default:
			return PeriodicLog{}, fmt.Errorf("SMSG_PERIODICAURALOG: aura type %d is never sent (Unit.cpp:4738-4740)", t.AuraType)
		}
		p.Ticks = append(p.Ticks, t)
	}
	if err := r.Err(); err != nil {
		return PeriodicLog{}, fmt.Errorf("SMSG_PERIODICAURALOG: %w", err)
	}
	return p, nil
}

type Heal struct {
	Target, Caster  packet.GUID
	SpellID, Amount uint32
	Crit            bool
}

// ParseSpellHealLog decodes SMSG_SPELLHEALLOG (Object.cpp:4260-4271).
func ParseSpellHealLog(body []byte) (Heal, error) {
	r := packet.NewReader(body)
	h := Heal{Target: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32(), Amount: r.U32(), Crit: r.U8() != 0}
	if err := r.Err(); err != nil {
		return Heal{}, fmt.Errorf("SMSG_SPELLHEALLOG: %w", err)
	}
	return h, nil
}

type Energize struct {
	Target, Caster         packet.GUID
	SpellID, Power, Amount uint32 // Power is a Powers value, 0 mana … 4 happiness (SharedDefines.h:130-138)
}

// ParseSpellEnergizeLog decodes SMSG_SPELLENERGIZELOG (Object.cpp:4308-4317).
func ParseSpellEnergizeLog(body []byte) (Energize, error) {
	r := packet.NewReader(body)
	e := Energize{Target: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32(), Power: r.U32(), Amount: r.U32()}
	if err := r.Err(); err != nil {
		return Energize{}, fmt.Errorf("SMSG_SPELLENERGIZELOG: %w", err)
	}
	return e, nil
}

type SpellTargets struct {
	Mask       uint16
	Unit, Item packet.GUID // Unit holds the unit, object or corpse guid
	Src, Dst   [3]float32
	Str        string
}

// readSpellTargets is SpellCastTargets::write read back (Spell.cpp:228-273). TARGET_FLAG_UNK2
// (0x10000) is in the core's guid condition but cannot survive the u16 mask; nothing sets it.
func readSpellTargets(r *packet.Reader) SpellTargets {
	t := SpellTargets{Mask: r.U16()}
	if t.Mask&(TargetFlagUnit|TargetFlagPvPCorpse|TargetFlagObject|TargetFlagCorpse) != 0 {
		t.Unit = r.PackedGUID()
	}
	if t.Mask&(TargetFlagItem|TargetFlagTradeItem) != 0 {
		t.Item = r.PackedGUID()
	}
	if t.Mask&TargetFlagSourceLocation != 0 {
		t.Src = [3]float32{r.F32(), r.F32(), r.F32()}
	}
	if t.Mask&TargetFlagDestLocation != 0 {
		t.Dst = [3]float32{r.F32(), r.F32(), r.F32()}
	}
	if t.Mask&TargetFlagString != 0 {
		t.Str = r.CString()
	}
	return t
}

// CastFlagAmmo adds two u32 (ammo display, inventory type) after the targets (Spell.h:60-71,
// Spell.cpp:4754-4755).
const CastFlagAmmo uint16 = 0x0020

type SpellStart struct {
	Item, Caster packet.GUID
	SpellID      uint32
	Flags        uint16
	CastTime     uint32
	Targets      SpellTargets
}

// ParseSpellStart decodes SMSG_SPELL_START (Spell.cpp:4613-4647).
func ParseSpellStart(body []byte) (SpellStart, error) {
	r := packet.NewReader(body)
	s := SpellStart{Item: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32(), Flags: r.U16(), CastTime: r.U32()}
	s.Targets = readSpellTargets(r)
	if s.Flags&CastFlagAmmo != 0 {
		r.U32()
		r.U32()
	}
	if err := r.Err(); err != nil {
		return SpellStart{}, fmt.Errorf("SMSG_SPELL_START: %w", err)
	}
	return s, nil
}

// SpellMissReflect is SPELL_MISS_REFLECT; miss reasons are at SpellDefines.h:93-107.
const SpellMissReflect uint8 = 11

var spellMissNames = []string{"none", "miss", "resist", "dodge", "parry", "block", "evade", "immune", "immune", "deflect", "absorb", "reflect"}

// SpellMissName names a SpellMissInfo value; unknown values read "miss_N".
func SpellMissName(code uint8) string {
	if int(code) < len(spellMissNames) {
		return spellMissNames[code]
	}
	return fmt.Sprintf("miss_%d", code)
}

type SpellMiss struct {
	Target packet.GUID
	Reason uint8
}

type SpellGo struct {
	Item, Caster packet.GUID
	SpellID      uint32
	Flags        uint16
	Hits         []packet.GUID
	Misses       []SpellMiss
	Targets      SpellTargets
}

// ParseSpellGo decodes SMSG_SPELL_GO (Spell.cpp:4649-4688, :4758-4809).
func ParseSpellGo(body []byte) (SpellGo, error) {
	r := packet.NewReader(body)
	g := SpellGo{Item: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32(), Flags: r.U16()}
	for n := int(r.U8()); n > 0; n-- {
		g.Hits = append(g.Hits, r.GUID())
	}
	for n := int(r.U8()); n > 0; n-- {
		m := SpellMiss{Target: r.GUID(), Reason: r.U8()}
		if m.Reason == SpellMissReflect {
			r.U8()
		}
		g.Misses = append(g.Misses, m)
	}
	g.Targets = readSpellTargets(r)
	if g.Flags&CastFlagAmmo != 0 {
		r.U32()
		r.U32()
	}
	if err := r.Err(); err != nil {
		return SpellGo{}, fmt.Errorf("SMSG_SPELL_GO: %w", err)
	}
	return g, nil
}

type CastResult struct {
	SpellID uint32
	Failed  bool
	Reason  uint8 // SpellCastResult (SpellDefines.h:284-433)
}

// ParseCastResult decodes SMSG_CAST_RESULT (Spell.cpp:4581-4611); reason-specific extras are skipped.
func ParseCastResult(body []byte) (CastResult, error) {
	r := packet.NewReader(body)
	c := CastResult{SpellID: r.U32()}
	if r.U8() != 0 {
		c.Failed, c.Reason = true, r.U8()
	}
	if err := r.Err(); err != nil {
		return CastResult{}, fmt.Errorf("SMSG_CAST_RESULT: %w", err)
	}
	return c, nil
}

type CastFailedOther struct {
	Caster  packet.GUID
	SpellID uint32
}

// ParseSpellFailedOther decodes SMSG_SPELL_FAILED_OTHER (Spell.cpp:4926-4935). The core never
// sends SMSG_SPELL_FAILURE; this broadcast is the only interrupt notice.
func ParseSpellFailedOther(body []byte) (CastFailedOther, error) {
	r := packet.NewReader(body)
	c := CastFailedOther{Caster: r.GUID(), SpellID: r.U32()}
	if err := r.Err(); err != nil {
		return CastFailedOther{}, fmt.Errorf("SMSG_SPELL_FAILED_OTHER: %w", err)
	}
	return c, nil
}

type SpellLogMiss struct {
	SpellID uint32
	Caster  packet.GUID
	Misses  []SpellMiss
}

// ParseSpellLogMiss decodes SMSG_SPELLLOGMISS (Object.cpp:4127-4140, Spell.cpp:4937-4964).
func ParseSpellLogMiss(body []byte) (SpellLogMiss, error) {
	r := packet.NewReader(body)
	m := SpellLogMiss{SpellID: r.U32(), Caster: r.GUID()}
	extra := r.U8() != 0 // two floats per target when set; the core never sets it
	// The count is uncapped in the core (Spell.cpp:4956), so bound it by the bytes left.
	n := r.U32()
	per := uint64(9)
	if extra {
		per = 17
	}
	if uint64(n)*per > uint64(r.Remaining()) {
		return SpellLogMiss{}, fmt.Errorf("SMSG_SPELLLOGMISS: %d targets in %d bytes", n, r.Remaining())
	}
	for i := uint32(0); i < n; i++ {
		m.Misses = append(m.Misses, SpellMiss{Target: r.GUID(), Reason: r.U8()})
		if extra {
			r.Skip(8)
		}
	}
	if err := r.Err(); err != nil {
		return SpellLogMiss{}, fmt.Errorf("SMSG_SPELLLOGMISS: %w", err)
	}
	return m, nil
}

// ProcResist is a proc the target resisted ("Your X was resisted by Y").
type ProcResist struct {
	Caster, Target packet.GUID
	SpellID        uint32
}

// ParseProcResist decodes SMSG_PROCRESIST (Object.cpp:4142-4150); the trailing log-format byte is 0.
func ParseProcResist(body []byte) (ProcResist, error) {
	r := packet.NewReader(body)
	p := ProcResist{Caster: r.GUID(), Target: r.GUID(), SpellID: r.U32()}
	r.U8()
	if err := r.Err(); err != nil {
		return ProcResist{}, fmt.Errorf("SMSG_PROCRESIST: %w", err)
	}
	return p, nil
}

type Kill struct {
	Killer, Victim packet.GUID
}

// ParsePartyKillLog decodes SMSG_PARTYKILLLOG (Unit.cpp:1127-1129), sent to the killer
// (Unit.cpp:1183) and the killer's group.
func ParsePartyKillLog(body []byte) (Kill, error) {
	r := packet.NewReader(body)
	k := Kill{Killer: r.GUID(), Victim: r.GUID()}
	if err := r.Err(); err != nil {
		return Kill{}, fmt.Errorf("SMSG_PARTYKILLLOG: %w", err)
	}
	return k, nil
}
