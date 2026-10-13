// Combat packets written the way the core does, for tests in game and session.
// gametest imports game (see gametest.go), so only external test packages (package game_test) may use it.
package gametest

import (
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// AttackerStateUpdate is Unit::SendAttackStateUpdate with one sub-damage (Unit.cpp:4888-4921).
func AttackerStateUpdate(hitInfo uint32, attacker, victim packet.GUID, total, school, absorb uint32, resist int32, victimState, blocked uint32) []byte {
	w := packet.NewWriter().U32(hitInfo).PackedGUID(attacker).PackedGUID(victim).U32(total).U8(1)
	coef := float32(0)
	if total != 0 {
		coef = 1
	}
	w.U32(school).F32(coef).U32(total).U32(absorb).I32(resist)
	return w.U32(victimState).U32(0).U32(0).U32(blocked).Bytes()
}

// SpellNonMeleeDamage is WorldObject::SendSpellNonMeleeDamageLog (Object.cpp:4319-4335).
func SpellNonMeleeDamage(target, attacker packet.GUID, spell, damage uint32, school uint8, absorb uint32, resist int32, periodic bool, blocked, hitInfo uint32) []byte {
	p := uint8(0)
	if periodic {
		p = 1
	}
	return packet.NewWriter().PackedGUID(target).PackedGUID(attacker).U32(spell).U32(damage).U8(school).U32(absorb).
		I32(resist).U8(p).U8(0).U32(blocked).U32(hitInfo).U8(0).Bytes()
}

func periodicHead(target, caster packet.GUID, spell, auraType uint32) *packet.Writer {
	return packet.NewWriter().PackedGUID(target).PackedGUID(caster).U32(spell).U32(1).U32(auraType) // Unit.cpp:4710-4714
}

// PeriodicDamage is the damage branch of SendPeriodicAuraLog (Unit.cpp:4717-4723).
func PeriodicDamage(target, caster packet.GUID, spell, auraType, amount, school, absorb uint32, resist int32) []byte {
	return periodicHead(target, caster, spell, auraType).U32(amount).U32(school).U32(absorb).I32(resist).Bytes()
}

// PeriodicHeal is the heal branch (Unit.cpp:4724-4727).
func PeriodicHeal(target, caster packet.GUID, spell, auraType, amount uint32) []byte {
	return periodicHead(target, caster, spell, auraType).U32(amount).Bytes()
}

// PeriodicEnergize is the energize branch (Unit.cpp:4728-4732).
func PeriodicEnergize(target, caster packet.GUID, spell, auraType, power, amount uint32) []byte {
	return periodicHead(target, caster, spell, auraType).U32(power).U32(amount).Bytes()
}

// PeriodicLeech is the mana-leech branch (Unit.cpp:4733-4737).
func PeriodicLeech(target, caster packet.GUID, spell, power, amount uint32, multiplier float32) []byte {
	return periodicHead(target, caster, spell, game.AuraPeriodicManaLeech).U32(power).U32(amount).F32(multiplier).Bytes()
}

// SpellHeal is WorldObject::SendHealSpellLog (Object.cpp:4260-4271).
func SpellHeal(target, caster packet.GUID, spell, amount uint32, crit bool) []byte {
	c := uint8(0)
	if crit {
		c = 1
	}
	return packet.NewWriter().PackedGUID(target).PackedGUID(caster).U32(spell).U32(amount).U8(c).Bytes()
}

// SpellEnergize is WorldObject::SendEnergizeSpellLog (Object.cpp:4308-4317).
func SpellEnergize(target, caster packet.GUID, spell, power, amount uint32) []byte {
	return packet.NewWriter().PackedGUID(target).PackedGUID(caster).U32(spell).U32(power).U32(amount).Bytes()
}

// unitTargets is SpellCastTargets::write for a unit target (Spell.cpp:228-273).
func unitTargets(w *packet.Writer, target packet.GUID) {
	if target == 0 {
		w.U16(0)
		return
	}
	w.U16(0x0002).PackedGUID(target)
}

// SpellStart is Spell::SendSpellStart (Spell.cpp:4613-4647), cast flags CAST_FLAG_UNKNOWN2.
func SpellStart(caster packet.GUID, spell, castTime uint32, target packet.GUID) []byte {
	w := packet.NewWriter().PackedGUID(caster).PackedGUID(caster).U32(spell).U16(0x0002).U32(castTime)
	unitTargets(w, target)
	return w.Bytes()
}

// SpellGo is Spell::SendSpellGo (Spell.cpp:4649-4688, :4758-4809), cast flags CAST_FLAG_UNKNOWN9.
// len(missCodes) must equal len(missGUIDs).
func SpellGo(caster packet.GUID, spell uint32, hits, missGUIDs []packet.GUID, missCodes []uint8, target packet.GUID) []byte {
	w := packet.NewWriter().PackedGUID(caster).PackedGUID(caster).U32(spell).U16(0x0100).U8(uint8(len(hits)))
	for _, h := range hits {
		w.GUID(h)
	}
	w.U8(uint8(len(missGUIDs)))
	for i, m := range missGUIDs {
		w.GUID(m).U8(missCodes[i])
		if missCodes[i] == 11 { // SPELL_MISS_REFLECT carries the reflect result
			w.U8(0)
		}
	}
	unitTargets(w, target)
	return w.Bytes()
}

// CastResult is Spell::SendCastResult (Spell.cpp:4581-4611); failReason < 0 means success.
func CastResult(spell uint32, failReason int) []byte {
	w := packet.NewWriter().U32(spell)
	if failReason < 0 {
		return w.U8(0).Bytes()
	}
	return w.U8(2).U8(uint8(failReason)).Bytes()
}

// SpellFailedOther is Spell::SendInterrupted's broadcast (Spell.cpp:4926-4935).
func SpellFailedOther(caster packet.GUID, spell uint32) []byte {
	return packet.NewWriter().GUID(caster).U32(spell).Bytes()
}

// SpellLogMiss is WorldObject::SendSpellMiss (Object.cpp:4127-4140).
func SpellLogMiss(spell uint32, caster, target packet.GUID, miss uint8) []byte {
	return packet.NewWriter().U32(spell).GUID(caster).U8(0).U32(1).GUID(target).U8(miss).Bytes()
}

// ProcResist is WorldObject::SendSpellDamageResist (Object.cpp:4142-4150).
func ProcResist(caster, target packet.GUID, spell uint32) []byte {
	return packet.NewWriter().GUID(caster).GUID(target).U32(spell).U8(0).Bytes()
}

// PartyKill is SMSG_PARTYKILLLOG (Unit.cpp:1127-1129).
func PartyKill(killer, victim packet.GUID) []byte {
	return packet.NewWriter().GUID(killer).GUID(victim).Bytes()
}

// ResurrectRequest is Spell::SendResurrectRequest (Spells/Spell.cpp:5153-5174 on tortoise-wow 5d79486): the caster's guid,
// the length of the name with its terminator, the name (empty when the caster is a player), the sickness byte and the timer byte.
func ResurrectRequest(caster packet.GUID, name string, sickness, timer bool) []byte {
	b := func(v bool) uint8 {
		if v {
			return 1
		}
		return 0
	}
	return packet.NewWriter().GUID(caster).U32(uint32(len(name) + 1)).CString(name).U8(b(sickness)).U8(b(timer)).Bytes()
}

// CorpseReclaimDelay is Player::SendCorpseReclaimDelay (Objects/Player.cpp:22237-22239): the delay in milliseconds.
func CorpseReclaimDelay(ms uint32) []byte { return packet.NewWriter().U32(ms).Bytes() }

// CorpseQuery is WorldSession::HandleCorpseQueryOpcode (Handlers/QueryHandler.cpp:271-327) for a corpse that exists: u8 1, the
// map, the position and the corpse's own map.
func CorpseQuery(mapID int32, x, y, z float32, corpseMap uint32) []byte {
	return packet.NewWriter().U8(1).I32(mapID).F32(x).F32(y).F32(z).U32(corpseMap).Bytes()
}

// NoCorpse is the same handler's answer when the player has no corpse: a single zero byte (:281-286).
func NoCorpse() []byte { return []byte{0} }
