package game

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// InfiniteCooldown is the category cooldown the core sends for a cooldown it keeps for a
// month, "infinity" (Player.cpp:4232-4238).
const InfiniteCooldown uint32 = 0x80000000

// InitialCooldown is one cooldown of SMSG_INITIAL_SPELLS. Item is the item whose spell it is
// (0 for a spell of the spellbook), Cooldown and CategoryCooldown the milliseconds left.
type InitialCooldown struct {
	Spell, Item, Category      uint32
	Cooldown, CategoryCooldown uint32
}

// InitialSpells is SMSG_INITIAL_SPELLS: the spellbook and the cooldowns running at login.
type InitialSpells struct {
	Spells    []uint32
	Cooldowns []InitialCooldown
}

// ParseInitialSpells decodes SMSG_INITIAL_SPELLS (Player::SendInitialSpells, Player.cpp:4175-4247):
// u8 0, u16 count, count x (u16 spell, u16 0), u16 cooldown count, then per cooldown u16 spell,
// u16 item, u16 category, u32 cooldown ms, u32 category cooldown ms. Spell ids are u16 on the
// wire, so a spell id above 65535 cannot be in the list.
func ParseInitialSpells(body []byte) (InitialSpells, error) {
	r := packet.NewReader(body)
	var out InitialSpells
	r.U8()
	n := int(r.U16())
	if n*4 > r.Remaining() { // bound the loop by the bytes left
		return InitialSpells{}, fmt.Errorf("SMSG_INITIAL_SPELLS: %d spells in %d bytes", n, r.Remaining())
	}
	for i := 0; i < n; i++ {
		out.Spells = append(out.Spells, uint32(r.U16()))
		r.U16() // "it's not slot id"
	}
	c := int(r.U16())
	if c*14 > r.Remaining() {
		return InitialSpells{}, fmt.Errorf("SMSG_INITIAL_SPELLS: %d cooldowns in %d bytes", c, r.Remaining())
	}
	for i := 0; i < c; i++ {
		out.Cooldowns = append(out.Cooldowns, InitialCooldown{Spell: uint32(r.U16()), Item: uint32(r.U16()), Category: uint32(r.U16()),
			Cooldown: r.U32(), CategoryCooldown: r.U32()})
	}
	if err := r.Err(); err != nil {
		return InitialSpells{}, fmt.Errorf("SMSG_INITIAL_SPELLS: %w", err)
	}
	return out, nil
}

// ParseRemovedSpell decodes SMSG_REMOVED_SPELL: the spell that left the spellbook, a u16
// (Player::SendSpellRemoved, Player.cpp:22997-23003).
func ParseRemovedSpell(body []byte) (uint32, error) {
	r := packet.NewReader(body)
	id := uint32(r.U16())
	if err := r.Err(); err != nil {
		return 0, fmt.Errorf("SMSG_REMOVED_SPELL: %w", err)
	}
	return id, nil
}

// ParseSupercededSpell decodes SMSG_SUPERCEDED_SPELL: the spellbook entry that a higher rank
// replaces, then the replacing spell, both u16 (Player.cpp:4344-4347, :4440-4444).
func ParseSupercededSpell(body []byte) (old, replacement uint32, err error) {
	r := packet.NewReader(body)
	old, replacement = uint32(r.U16()), uint32(r.U16())
	if err := r.Err(); err != nil {
		return 0, 0, fmt.Errorf("SMSG_SUPERCEDED_SPELL: %w", err)
	}
	return old, replacement, nil
}

// SpellCooldown is one spell of SMSG_SPELL_COOLDOWN with its cooldown in milliseconds.
type SpellCooldown struct {
	Spell, MS uint32
}

// SpellCooldowns is SMSG_SPELL_COOLDOWN: the unit the cooldowns are on and the spells.
type SpellCooldowns struct {
	Unit      packet.GUID
	Cooldowns []SpellCooldown
}

// ParseSpellCooldown decodes SMSG_SPELL_COOLDOWN: the unit's guid, then (u32 spell, u32 ms)
// pairs to the end of the packet (Player::ProhibitSpellSchool, Player.cpp:4720-4747: a school
// lockout after an interrupt; Player::SendSpellCooldown, :22988-22995, one spell). The core
// does not send it for the cooldown of a spell the player cast itself.
func ParseSpellCooldown(body []byte) (SpellCooldowns, error) {
	r := packet.NewReader(body)
	out := SpellCooldowns{Unit: r.GUID()}
	for r.Remaining() >= 8 {
		out.Cooldowns = append(out.Cooldowns, SpellCooldown{Spell: r.U32(), MS: r.U32()})
	}
	if err := r.Err(); err != nil {
		return SpellCooldowns{}, fmt.Errorf("SMSG_SPELL_COOLDOWN: %w", err)
	}
	if r.Remaining() != 0 {
		return SpellCooldowns{}, fmt.Errorf("SMSG_SPELL_COOLDOWN: %d stray bytes", r.Remaining())
	}
	return out, nil
}

// CooldownEvent is SMSG_COOLDOWN_EVENT: the client starts the spell's cooldown now, from
// its own spell data (Unit::CooldownEvent, Unit.cpp:11822-11836).
type CooldownEvent struct {
	Spell uint32
	Unit  packet.GUID
}

// ParseCooldownEvent decodes SMSG_COOLDOWN_EVENT: u32 spell, guid.
func ParseCooldownEvent(body []byte) (CooldownEvent, error) {
	r := packet.NewReader(body)
	e := CooldownEvent{Spell: r.U32(), Unit: r.GUID()}
	if err := r.Err(); err != nil {
		return CooldownEvent{}, fmt.Errorf("SMSG_COOLDOWN_EVENT: %w", err)
	}
	return e, nil
}

// ParseClearCooldown decodes SMSG_CLEAR_COOLDOWN: u32 spell, guid of the unit it was on
// (Player::SendClearCooldown, Player.cpp:22973-22979).
func ParseClearCooldown(body []byte) (CooldownEvent, error) {
	r := packet.NewReader(body)
	e := CooldownEvent{Spell: r.U32(), Unit: r.GUID()}
	if err := r.Err(); err != nil {
		return CooldownEvent{}, fmt.Errorf("SMSG_CLEAR_COOLDOWN: %w", err)
	}
	return e, nil
}

// ParseCooldownCheat decodes SMSG_COOLDOWN_CHEAT, every cooldown of the unit cleared: its
// guid (Player::SendClearAllCooldowns, Player.cpp:22981-22985).
func ParseCooldownCheat(body []byte) (packet.GUID, error) {
	r := packet.NewReader(body)
	g := r.GUID()
	if err := r.Err(); err != nil {
		return 0, fmt.Errorf("SMSG_COOLDOWN_CHEAT: %w", err)
	}
	return g, nil
}

// ParseChannelStart decodes MSG_CHANNEL_START, sent to the caster alone: u32 spell, u32
// duration ms (Spell::SendChannelStart, Spell.cpp:5053-5101).
func ParseChannelStart(body []byte) (spell, durationMS uint32, err error) {
	r := packet.NewReader(body)
	spell, durationMS = r.U32(), r.U32()
	if err := r.Err(); err != nil {
		return 0, 0, fmt.Errorf("MSG_CHANNEL_START: %w", err)
	}
	return spell, durationMS, nil
}

// ParseChannelUpdate decodes MSG_CHANNEL_UPDATE: the ms the channel has left, 0 when it ended
// or was interrupted (Spell::SendChannelUpdate, Spell.cpp:4967-5051).
func ParseChannelUpdate(body []byte) (uint32, error) {
	r := packet.NewReader(body)
	ms := r.U32()
	if err := r.Err(); err != nil {
		return 0, fmt.Errorf("MSG_CHANNEL_UPDATE: %w", err)
	}
	return ms, nil
}

// castFailNames name SpellCastResult, in code order (SpellDefines.h:284-433).
var castFailNames = []string{
	"affecting_combat",
	"already_at_full_health",
	"already_at_full_power",
	"already_being_tamed",
	"already_have_charm",
	"already_have_summon",
	"already_open",
	"aura_bounced",
	"autotrack_interrupted",
	"bad_implicit_targets",
	"bad_targets",
	"cant_be_charmed",
	"cant_be_disenchanted",
	"cant_be_prospected",
	"cant_cast_on_tapped",
	"cant_duel_while_invisible",
	"cant_duel_while_stealthed",
	"cant_stealth",
	"caster_aurastate",
	"caster_dead",
	"charmed",
	"chest_in_use",
	"confused",
	"dont_report",
	"equipped_item",
	"equipped_item_class",
	"equipped_item_class_mainhand",
	"equipped_item_class_offhand",
	"error",
	"fizzle",
	"fleeing",
	"food_lowlevel",
	"highlevel",
	"hunger_satiated",
	"immune",
	"interrupted",
	"interrupted_combat",
	"item_already_enchanted",
	"item_gone",
	"item_not_found",
	"item_not_ready",
	"level_requirement",
	"line_of_sight",
	"lowlevel",
	"low_castlevel",
	"mainhand_empty",
	"moving",
	"need_ammo",
	"need_ammo_pouch",
	"need_exotic_ammo",
	"nopath",
	"not_behind",
	"not_fishable",
	"not_here",
	"not_infront",
	"not_in_control",
	"not_known",
	"not_mounted",
	"not_on_taxi",
	"not_on_transport",
	"not_ready",
	"not_shapeshift",
	"not_standing",
	"not_tradeable",
	"not_trading",
	"not_unsheathed",
	"not_while_ghost",
	"no_ammo",
	"no_charges_remain",
	"no_champion",
	"no_combo_points",
	"no_dueling",
	"no_endurance",
	"no_fish",
	"no_items_while_shapeshifted",
	"no_mounts_allowed",
	"no_pet",
	"no_power",
	"nothing_to_dispel",
	"nothing_to_steal",
	"only_abovewater",
	"only_daytime",
	"only_indoors",
	"only_mounted",
	"only_nighttime",
	"only_outdoors",
	"only_shapeshift",
	"only_stealthed",
	"only_underwater",
	"out_of_range",
	"pacified",
	"possessed",
	"reagents",
	"requires_area",
	"requires_spell_focus",
	"rooted",
	"silenced",
	"spell_in_progress",
	"spell_learned",
	"spell_unavailable",
	"stunned",
	"targets_dead",
	"target_affecting_combat",
	"target_aurastate",
	"target_dueling",
	"target_enemy",
	"target_enraged",
	"target_friendly",
	"target_in_combat",
	"target_is_player",
	"target_not_dead",
	"target_not_in_party",
	"target_not_looted",
	"target_not_player",
	"target_no_pockets",
	"target_no_weapons",
	"target_unskinnable",
	"thirst_satiated",
	"too_close",
	"too_many_of_item",
	"totems",
	"training_points",
	"try_again",
	"unit_not_behind",
	"unit_not_infront",
	"wrong_pet_food",
	"not_while_fatigued",
	"target_not_in_instance",
	"not_while_trading",
	"target_not_in_raid",
	"disenchant_while_looting",
	"prospect_while_looting",
	"prospect_need_more",
	"target_freeforall",
	"no_edible_corpses",
	"only_battlegrounds",
	"target_not_ghost",
	"too_many_skills",
	"transform_unusable",
	"wrong_weather",
	"damage_immune",
	"prevented_by_mechanic",
	"play_time",
	"reputation",
	"min_skill",
	"unknown"}

// SpellCastResultOK is SPELL_CAST_OK (SpellDefines.h:433), the code of no failure.
const SpellCastResultOK uint8 = 0xFF

// Cast result codes the agent acts on (SpellDefines.h:284-433).
const (
	CastFailedInterrupted uint8 = 35
	CastFailedNotReady    uint8 = 60
)

// CastFailName names a SpellCastResult code ("out_of_range"); an unknown code reads "failed_N".
func CastFailName(code uint8) string {
	if int(code) < len(castFailNames) {
		return castFailNames[code]
	}
	return fmt.Sprintf("failed_%d", code)
}
