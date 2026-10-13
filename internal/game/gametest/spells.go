package gametest

import (
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// InitialSpells is Player::SendInitialSpells (Player.cpp:4175-4247).
func InitialSpells(spells []uint32, cooldowns []game.InitialCooldown) []byte {
	w := packet.NewWriter().U8(0).U16(uint16(len(spells)))
	for _, s := range spells {
		w.U16(uint16(s)).U16(0)
	}
	w.U16(uint16(len(cooldowns)))
	for _, c := range cooldowns {
		w.U16(uint16(c.Spell)).U16(uint16(c.Item)).U16(uint16(c.Category)).U32(c.Cooldown).U32(c.CategoryCooldown)
	}
	return w.Bytes()
}

// LearnedSpell is SMSG_LEARNED_SPELL (Player.cpp:4591-4593): a u32 spell.
func LearnedSpell(spell uint32) []byte { return packet.NewWriter().U32(spell).Bytes() }

// RemovedSpell is Player::SendSpellRemoved (Player.cpp:22997-23003): a u16 spell.
func RemovedSpell(spell uint32) []byte { return packet.NewWriter().U16(uint16(spell)).Bytes() }

// SupercededSpell is SMSG_SUPERCEDED_SPELL (Player.cpp:4344-4347): u16 old, u16 new.
func SupercededSpell(old, replacement uint32) []byte {
	return packet.NewWriter().U16(uint16(old)).U16(uint16(replacement)).Bytes()
}

// SpellCooldown is SMSG_SPELL_COOLDOWN (Player.cpp:4720-4747): the unit, then (spell, ms) pairs.
func SpellCooldown(unit packet.GUID, pairs ...game.SpellCooldown) []byte {
	w := packet.NewWriter().GUID(unit)
	for _, p := range pairs {
		w.U32(p.Spell).U32(p.MS)
	}
	return w.Bytes()
}

// CooldownEvent is SMSG_COOLDOWN_EVENT and SMSG_CLEAR_COOLDOWN (Unit.cpp:11830-11834,
// Player.cpp:22973-22979): u32 spell, then the unit's guid.
func CooldownEvent(spell uint32, unit packet.GUID) []byte {
	return packet.NewWriter().U32(spell).GUID(unit).Bytes()
}

// CooldownCheat is SMSG_COOLDOWN_CHEAT (Player.cpp:22981-22985): the unit's guid.
func CooldownCheat(unit packet.GUID) []byte { return packet.NewWriter().GUID(unit).Bytes() }

// ChannelStart is MSG_CHANNEL_START (Spell.cpp:5053-5101): u32 spell, u32 duration ms.
func ChannelStart(spell, durationMS uint32) []byte {
	return packet.NewWriter().U32(spell).U32(durationMS).Bytes()
}

// ChannelUpdate is MSG_CHANNEL_UPDATE (Spell.cpp:4967-5051): the ms left, 0 for the end.
func ChannelUpdate(leftMS uint32) []byte { return packet.NewWriter().U32(leftMS).Bytes() }
