package gametest

import (
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// PetBar is the action bar a fresh pet has (CharmInfo::InitPetActionBar, Objects/Unit.cpp:8943-8956) with the given spells on
// buttons 3 to 6, autocast on or off by the matching flag: attack, follow, stay, up to four spells, then aggressive, defensive, passive.
func PetBar(spells ...game.PetBarSpell) [game.PetBarSlots]uint32 {
	var b [game.PetBarSlots]uint32
	b[0] = game.PetButton(game.PetCmdAttack, game.PetActCommand)
	b[1] = game.PetButton(game.PetCmdFollow, game.PetActCommand)
	b[2] = game.PetButton(game.PetCmdStay, game.PetActCommand)
	for i := game.PetBarSpellFirst; i < game.PetBarSpellEnd; i++ {
		b[i] = game.PetButton(0, game.PetActDisabled)
	}
	for i, sp := range spells {
		typ := game.PetActDisabled
		if sp.Autocast {
			typ = game.PetActEnabled
		}
		b[game.PetBarSpellFirst+i] = game.PetButton(sp.Spell, typ)
	}
	b[7] = game.PetButton(game.PetReactAggressive, game.PetActReaction)
	b[8] = game.PetButton(game.PetReactDefensive, game.PetActReaction)
	b[9] = game.PetButton(game.PetReactPassive, game.PetActReaction)
	return b
}

// PetSpells is SMSG_PET_SPELLS as Player::PetSpellInitialize writes it (Objects/Player.cpp:19228-19272).
func PetSpells(pet packet.GUID, react, command uint8, disabled bool, bar [game.PetBarSlots]uint32, spellList []uint32, cds ...game.PetCooldown) []byte {
	w := packet.NewWriter().GUID(pet).U32(0).U8(react).U8(command).U8(0)
	if disabled {
		w.U8(0x08)
	} else {
		w.U8(0)
	}
	for _, b := range bar {
		w.U32(b)
	}
	w.U8(uint8(len(spellList)))
	for _, s := range spellList {
		w.U32(s)
	}
	w.U8(uint8(len(cds)))
	for _, c := range cds {
		w.U16(uint16(c.SpellID)).U16(0).U32(c.MS).U32(0)
	}
	return w.Bytes()
}

// PetSpellsCleared is Player::RemovePetActionBar (Objects/Player.cpp:19357-19361): a zero guid.
func PetSpellsCleared() []byte { return packet.NewWriter().GUID(0).Bytes() }

// PetMode is Pet::SetEnabled (Objects/Pet.cpp:2481-2492).
func PetMode(pet packet.GUID, react, command uint8, disabled bool) []byte {
	w := packet.NewWriter().GUID(pet).U8(react).U8(command).U8(0)
	if disabled {
		return w.U8(0x08).Bytes()
	}
	return w.U8(0).Bytes()
}

// PetCastFailed is Unit::SendPetCastFail (Objects/Unit.cpp:9587-9600).
func PetCastFailed(spell uint32, reason uint8) []byte {
	return packet.NewWriter().U32(spell).U8(2).U8(reason).Bytes()
}

// PetActionFeedback is Unit::SendPetActionFeedback (Objects/Unit.cpp:9602-9610).
func PetActionFeedback(msg uint8) []byte { return []byte{msg} }
