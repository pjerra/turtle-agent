package game

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// Pets (issue #80 step 4d): the hunter's pet and the warlock's demon. Core cites are tortoise-wow 5d79486 (the checkout the layouts
// were verified against, 2026-10-13); paths are under src/game.
//
// A pet is controlled through its action bar. The server keeps a ten-button bar (CharmInfo::InitPetActionBar, Objects/Unit.cpp:8943-8956): buttons 0 to 2
// are the commands attack, follow and stay, 3 to 6 the pet's spells, 7 to 9 the reactions aggressive, defensive and passive. A button
// is one u32, the action in the low 24 bits and its type in the high 8 (Objects/Unit.h:243-246).

// Button types (ActiveStates, Objects/UnitDefines.h:476-484).
const (
	PetActPassive  uint8 = 0x01 // a passive spell: never cast by hand
	PetActReaction uint8 = 0x06 // a reaction button (action: PetReact*)
	PetActCommand  uint8 = 0x07 // a command button (action: PetCmd*)
	PetActDisabled uint8 = 0x81 // a castable spell, autocast off
	PetActEnabled  uint8 = 0xC1 // a castable spell, autocast on
)

// Commands (CommandStates, Objects/UnitDefines.h:493-498). Dismiss is not sent: a warlock's demon is dismissed by summoning another and a
// hunter's with the spell.
const (
	PetCmdStay    uint32 = 0
	PetCmdFollow  uint32 = 1
	PetCmdAttack  uint32 = 2
	PetCmdDismiss uint32 = 3
)

// Reactions (ReactStates, Objects/UnitDefines.h:486-491).
const (
	PetReactPassive    uint32 = 0
	PetReactDefensive  uint32 = 1
	PetReactAggressive uint32 = 2
)

// The action bar (ActionBarIndex, Objects/UnitDefines.h:503-509).
const (
	PetBarSlots      = 10
	PetBarSpellFirst = 3 // buttons 3 to 6 hold the pet's spells
	PetBarSpellEnd   = 7
)

// PetButton packs a bar button (MAKE_UNIT_ACTION_BUTTON, Objects/Unit.h:246).
func PetButton(action uint32, typ uint8) uint32 { return action&0x00FFFFFF | uint32(typ)<<24 }

// PetButtonAction and PetButtonType split a bar button (Objects/Unit.h:243-244).
func PetButtonAction(b uint32) uint32 { return b & 0x00FFFFFF }
func PetButtonType(b uint32) uint8    { return uint8(b >> 24) }

// BuildPetAction is CMSG_PET_ACTION (Handlers/PetHandler.cpp:35-46): u64 pet guid, u32 button, u64 target guid (0 for none). The
// server drops it unless the pet is the player's and alive (:57-65) and the pet's bar is enabled (:75-78). A command button
// (PetButton(cmd, PetActCommand)) commands the pet, a reaction button sets its reaction, a spell button (type 0x81 or 0xC1) casts the spell.
func BuildPetAction(pet packet.GUID, button uint32, target packet.GUID) []byte {
	return packet.NewWriter().GUID(pet).U32(button).GUID(target).Bytes()
}

// BuildPetSpellAutocast is CMSG_PET_SPELL_AUTOCAST (Handlers/PetHandler.cpp:558-592): u64 pet guid, u32 spell, u8 state (1 on, 0 off).
// The server ignores a spell the pet does not know and a passive one (:575-576) and keeps the bar button in step (:591).
func BuildPetSpellAutocast(pet packet.GUID, spell uint32, on bool) []byte {
	w := packet.NewWriter().GUID(pet).U32(spell)
	if on {
		return w.U8(1).Bytes()
	}
	return w.U8(0).Bytes()
}

// PetBarSpell is a spell on a pet's bar for the test builders: its id and whether the button is the autocast one.
type PetBarSpell struct {
	Spell    uint32
	Autocast bool
}

// PetCooldown is one entry of the cooldown list of SMSG_PET_SPELLS.
type PetCooldown struct {
	SpellID uint32 `json:"spell_id"`
	MS      uint32 `json:"ms"`
}

// PetSpells is SMSG_PET_SPELLS, the pet's action bar and spell list. The server sends it when a pet is summoned or loaded
// (Player::PetSpellInitialize, Objects/Player.cpp:19228-19272), when a charm starts (:19274-19355), and with a body of one zero
// guid when the bar goes (Player::RemovePetActionBar, :19357-19361): then Cleared is set.
//
//	u64 pet guid
//	u32 0                       (in 1.12 the pet's duration is not sent)
//	u8 react state, u8 command state, u8 0, u8 flags (0x08: the bar is disabled, Pet::IsEnabled)
//	u32 button x 10             (CharmInfo::BuildActionBar, Objects/Unit.cpp:9139-9143)
//	u8 n, u32 spell button x n  (a permanent pet's spells; none for a charm or a summoned demon: the bar has them)
//	u8 m, m x (u16 spell, u16 0, u32 cooldown ms, u32 0)   (Unit::WritePetSpellsCooldown, Objects/Unit.cpp:11949-11962)
type PetSpells struct {
	Pet       packet.GUID         `json:"pet"`
	Cleared   bool                `json:"cleared,omitempty"`
	React     uint8               `json:"react"`
	Command   uint8               `json:"command"`
	Enabled   bool                `json:"enabled"`
	Bar       [PetBarSlots]uint32 `json:"bar"`
	Spells    []uint32            `json:"spells"`
	Cooldowns []PetCooldown       `json:"cooldowns"`
}

// ParsePetSpells decodes SMSG_PET_SPELLS.
func ParsePetSpells(body []byte) (PetSpells, error) {
	r := packet.NewReader(body)
	var p PetSpells
	p.Pet = r.GUID()
	if r.Remaining() == 0 && r.Err() == nil { // RemovePetActionBar: the guid alone
		p.Cleared = true
		return p, nil
	}
	r.U32()
	p.React, p.Command = r.U8(), r.U8()
	r.U8()
	p.Enabled = r.U8()&0x08 == 0
	for i := range p.Bar {
		p.Bar[i] = r.U32()
	}
	n := int(r.U8())
	for i := 0; i < n; i++ {
		p.Spells = append(p.Spells, r.U32())
	}
	m := int(r.U8())
	for i := 0; i < m; i++ {
		id := r.U16()
		r.U16()
		ms := r.U32()
		r.U32()
		p.Cooldowns = append(p.Cooldowns, PetCooldown{SpellID: uint32(id), MS: ms})
	}
	if err := r.Err(); err != nil {
		return PetSpells{}, fmt.Errorf("SMSG_PET_SPELLS: %w", err)
	}
	return p, nil
}

// PetMode is SMSG_PET_MODE (Pet::SetEnabled, Objects/Pet.cpp:2481-2492), sent when the pet's bar is switched on or off (a mount
// disables it): u64 pet guid, u8 react state, u8 command state, u8 0, u8 flags (0x08: disabled).
type PetMode struct {
	Pet     packet.GUID `json:"pet"`
	React   uint8       `json:"react"`
	Command uint8       `json:"command"`
	Enabled bool        `json:"enabled"`
}

// ParsePetMode decodes SMSG_PET_MODE.
func ParsePetMode(body []byte) (PetMode, error) {
	r := packet.NewReader(body)
	m := PetMode{Pet: r.GUID(), React: r.U8(), Command: r.U8()}
	r.U8()
	m.Enabled = r.U8()&0x08 == 0
	if err := r.Err(); err != nil {
		return PetMode{}, fmt.Errorf("SMSG_PET_MODE: %w", err)
	}
	return m, nil
}

// PetCastFailed is SMSG_PET_CAST_FAILED (Unit::SendPetCastFail, Objects/Unit.cpp:9587-9600): u32 spell, u8 2, u8 SpellCastResult.
type PetCastFailed struct {
	SpellID uint32 `json:"spell_id"`
	Reason  uint8  `json:"reason"`
}

// Name is the reason as the cast refusals of the player are named.
func (p PetCastFailed) Name() string { return CastFailName(p.Reason) }

// ParsePetCastFailed decodes SMSG_PET_CAST_FAILED.
func ParsePetCastFailed(body []byte) (PetCastFailed, error) {
	r := packet.NewReader(body)
	p := PetCastFailed{SpellID: r.U32()}
	r.U8()
	p.Reason = r.U8()
	if err := r.Err(); err != nil {
		return PetCastFailed{}, fmt.Errorf("SMSG_PET_CAST_FAILED: %w", err)
	}
	return p, nil
}

var petFeedback = []string{"none", "pet_dead", "nothing_to_attack", "cant_attack_target", "no_path"}

// PetFeedbackName names the u8 of SMSG_PET_ACTION_FEEDBACK (ActionFeedback, Objects/Pet.h:90-96; Unit::SendPetActionFeedback,
// Objects/Unit.cpp:9602-9610): the server's answer to an attack order it refused.
func PetFeedbackName(msg uint8) string {
	if int(msg) < len(petFeedback) {
		return petFeedback[msg]
	}
	return fmt.Sprintf("feedback_%d", msg)
}

// ParsePetActionFeedback decodes SMSG_PET_ACTION_FEEDBACK: one u8.
func ParsePetActionFeedback(body []byte) (uint8, error) {
	r := packet.NewReader(body)
	m := r.U8()
	if err := r.Err(); err != nil {
		return 0, fmt.Errorf("SMSG_PET_ACTION_FEEDBACK: %w", err)
	}
	return m, nil
}

// Happiness states of a hunter pet (Pet::GetHappinessState, Objects/Pet.cpp:929-936): the happiness power runs 0 to 1,000,000 and a
// state is a third of it (HAPPINESS_LEVEL_SIZE 333000, Objects/Pet.h:131).
const (
	HappinessLevelSize = 333000
	PetUnhappy         = 1
	PetContent         = 2
	PetHappy           = 3
)

// PetHappinessState is 1 (unhappy), 2 (content) or 3 (happy) for the happiness power value.
func PetHappinessState(v uint32) uint8 {
	switch {
	case v < HappinessLevelSize:
		return PetUnhappy
	case v >= 2*HappinessLevelSize:
		return PetHappy
	}
	return PetContent
}
