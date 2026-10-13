package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The player's pet (README "Pets", issue #80 step 4d): a hunter's pet or a warlock's demon. The layouts are in internal/game/pet.go.
//
// Where the facts come from:
//   - which unit is the pet: UNIT_FIELD_SUMMON of the player (Store.SummonOf), which the core sets when the pet joins the map
//     (Unit::SetPet, Objects/Pet.cpp:401). The pet's own row (health, power, target, combat flag) is an ordinary unit row.
//   - the happiness of a hunter's pet: its POWER5 (Pet.cpp:429-430), a unit field the server sends to everyone in view.
//   - the action bar, the reaction and the command state: SMSG_PET_SPELLS when the pet is summoned (Player::PetSpellInitialize) and
//     SMSG_PET_MODE when the bar is switched. The server does not repeat them when an order changes them (HandlePetAction sets the
//     state and sends nothing back), so a sent order is applied to the copy here; the next SMSG_PET_SPELLS corrects it.

// petTracker is what the session keeps of the pet. The bar is kept by the pet it came from: the core sends SMSG_PET_SPELLS right after
// it adds the pet and the field that names it only with the next update block, so the bar can arrive first.
type petTracker struct {
	bar        petBar
	guid       packet.GUID // the pet announced (pet_summoned) and not dismissed yet
	dead       bool
	lastRefuse string // the pet's last refusal, for the pet call
}

type petBar struct {
	pet     packet.GUID
	react   uint8
	command uint8
	enabled bool
	bar     [game.PetBarSlots]uint32
	spells  []uint32
	cds     []game.PetCooldown
}

func (s *Session) registerPet() {
	s.handlers[opcode.SMSGPetSpells] = s.onPetSpells
	s.handlers[opcode.SMSGPetMode] = s.onPetMode
	s.handlers[opcode.SMSGPetCastFailed] = s.onPetCastFailed
	s.handlers[opcode.SMSGPetActionFeedback] = s.onPetFeedback
}

func (s *Session) onPetSpells(body []byte) error {
	p, err := game.ParsePetSpells(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if p.Cleared || p.Pet == 0 {
		s.pet.bar = petBar{}
	} else {
		s.pet.bar = petBar{pet: p.Pet, react: p.React, command: p.Command, enabled: p.Enabled, bar: p.Bar, spells: p.Spells, cds: p.Cooldowns}
	}
	s.mu.Unlock()
	return nil
}

func (s *Session) onPetMode(body []byte) error {
	m, err := game.ParsePetMode(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.pet.bar.pet == m.Pet {
		s.pet.bar.react, s.pet.bar.command, s.pet.bar.enabled = m.React, m.Command, m.Enabled
	}
	s.mu.Unlock()
	return nil
}

func (s *Session) onPetCastFailed(body []byte) error {
	f, err := game.ParsePetCastFailed(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.pet.lastRefuse = f.Name()
	pet := s.pet.guid
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindPetError, SourceGUID: pet, SpellID: f.SpellID, Data: map[string]any{"reason": f.Name(), "code": f.Reason}})
	return nil
}

func (s *Session) onPetFeedback(body []byte) error {
	m, err := game.ParsePetActionFeedback(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	pet := s.pet.guid
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindPetError, SourceGUID: pet, Data: map[string]any{"feedback": game.PetFeedbackName(m)}})
	return nil
}

// watchPet compares the pet the player's field names with the one announced and emits pet_summoned, pet_died, pet_revived and
// pet_dismissed. It runs on the loop after every update block and every destroy; it costs two store lookups.
func (s *Session) watchPet() {
	self := s.selfGUID()
	if self == 0 {
		return
	}
	g := s.store.SummonOf(self)
	var dead bool
	var entry uint32
	if g != 0 {
		hp, max := s.store.Field(g, objects.UnitHealth), s.store.Field(g, objects.UnitMaxHealth)
		dead = max > 0 && hp == 0
		entry = s.store.Entry(g)
	}
	s.mu.Lock()
	prev, prevDead := s.pet.guid, s.pet.dead
	s.pet.guid, s.pet.dead = g, dead
	if g != prev {
		s.pet.lastRefuse = ""
	}
	s.mu.Unlock()
	switch {
	case g != prev:
		if prev != 0 {
			s.emit(events.Event{Kind: events.KindPetDismissed, SourceGUID: prev, TargetGUID: self, Data: map[string]any{"was_dead": prevDead}})
		}
		if g != 0 {
			s.emit(events.Event{Kind: events.KindPetSummoned, SourceGUID: g, TargetGUID: self, Data: map[string]any{"entry": entry, "dead": dead}})
			if dead {
				s.emit(events.Event{Kind: events.KindPetDied, SourceGUID: g, TargetGUID: self})
			}
		}
	case g != 0 && dead && !prevDead:
		s.emit(events.Event{Kind: events.KindPetDied, SourceGUID: g, TargetGUID: self})
	case g != 0 && !dead && prevDead:
		s.emit(events.Event{Kind: events.KindPetRevived, SourceGUID: g, TargetGUID: self})
	}
}

// PetSpell is one spell of the pet's action bar or spell list.
type PetSpell struct {
	SpellID  uint32 `json:"spell_id"`
	Name     string `json:"name,omitempty"`
	Slot     int    `json:"slot"`     // the bar button (3 to 6 for a spell), -1 when it is not on the bar
	Autocast bool   `json:"autocast"` // the button is the autocast one (0xC1)
	Passive  bool   `json:"passive,omitempty"`
}

// PetInfo is the pet RPC's answer: whether the player has a pet and its state. Without a pet only Present (false) and Bar (false) are
// set. Health, power and target are the pet's own unit row; the bar, reaction and command state are the last SMSG_PET_SPELLS with the
// orders sent since applied (see the file's header).
type PetInfo struct {
	Present bool        `json:"present"`
	GUID    packet.GUID `json:"guid,omitempty"`
	Entry   uint32      `json:"entry,omitempty"`
	Name    string      `json:"name,omitempty"`
	Level   uint32      `json:"level,omitempty"`
	Dead    bool        `json:"dead"`

	Health    uint32 `json:"health"`
	MaxHealth uint32 `json:"max_health"`
	PowerType uint8  `json:"power_type"` // 0 mana (a demon), 2 focus (a hunter's pet)
	Power     uint32 `json:"power"`
	MaxPower  uint32 `json:"max_power"`
	// Happiness is the pet's POWER5, 0 to 1,000,000; HappinessState 1 unhappy, 2 content, 3 happy. Both are 0 for a pet that has none (a demon).
	Happiness      uint32 `json:"happiness,omitempty"`
	HappinessState uint8  `json:"happiness_state,omitempty"`
	Loyalty        uint8  `json:"loyalty,omitempty"` // 1 to 6 for a hunter's pet (UNIT_FIELD_BYTES_1 byte 1)

	InCombat bool        `json:"in_combat"`
	Target   packet.GUID `json:"target,omitempty"` // what the pet attacks
	Distance float32     `json:"distance"`
	Moving   bool        `json:"moving"`

	// Bar is true when the session has the pet's action bar (SMSG_PET_SPELLS). React is "passive", "defensive" or "aggressive"; Command is
	// "stay" or "follow" (an attack order does not change it). Enabled is false while the bar is off (the player is mounted).
	Bar     bool       `json:"bar"`
	React   string     `json:"react,omitempty"`
	Command string     `json:"command,omitempty"`
	Enabled bool       `json:"enabled"`
	Spells  []PetSpell `json:"spells,omitempty"`
	// Refused is the reason of the pet's last refused cast (SMSG_PET_CAST_FAILED), kept until the next summon.
	Refused string `json:"refused,omitempty"`
}

var (
	petReactNames   = []string{"passive", "defensive", "aggressive"}
	petCommandNames = []string{"stay", "follow", "attack", "dismiss"}
)

func nameAt(names []string, i uint8) string {
	if int(i) < len(names) {
		return names[i]
	}
	return fmt.Sprintf("state_%d", i)
}

// petSpells lists the spells of a bar: the spell buttons first, then the spells the list holds that no button shows.
func petSpells(b petBar) []PetSpell {
	var out []PetSpell
	seen := map[uint32]bool{}
	for i, btn := range b.bar {
		t := game.PetButtonType(btn)
		id := game.PetButtonAction(btn)
		if id == 0 || (t != game.PetActEnabled && t != game.PetActDisabled && t != game.PetActPassive) {
			continue
		}
		seen[id] = true
		out = append(out, PetSpell{SpellID: id, Name: spells.Name(id), Slot: i, Autocast: t == game.PetActEnabled, Passive: t == game.PetActPassive})
	}
	for _, btn := range b.spells {
		id := game.PetButtonAction(btn)
		if id == 0 || seen[id] {
			continue
		}
		t := game.PetButtonType(btn)
		seen[id] = true
		out = append(out, PetSpell{SpellID: id, Name: spells.Name(id), Slot: -1, Autocast: t == game.PetActEnabled, Passive: t == game.PetActPassive})
	}
	return out
}

// Pet is the state of the player's pet: Present is false when it has none. It is a read, so it works in any state; the unit row is the
// store's as of the last update.
func (s *Session) Pet() PetInfo {
	self := s.selfGUID()
	if self == 0 {
		return PetInfo{}
	}
	g := s.store.SummonOf(self)
	if g == 0 {
		return PetInfo{}
	}
	x, y, z, _ := s.selfPos()
	u, ok := s.store.Get(g, x, y, z)
	if !ok {
		return PetInfo{}
	}
	u.Name, _ = s.cachedName(g)
	info := PetInfo{Present: true, GUID: g, Entry: u.Entry, Name: u.Name, Level: u.Level, Dead: u.Dead,
		Health: u.Health, MaxHealth: u.MaxHealth, PowerType: u.PowerType, Power: u.Power, MaxPower: u.MaxPower,
		InCombat: u.InCombat, Target: u.Target, Distance: u.Distance, Moving: u.Moving}
	if h, max := s.store.Field(g, objects.UnitPower5), s.store.Field(g, objects.UnitMaxPower5); max > 0 {
		info.Happiness, info.HappinessState = h, game.PetHappinessState(h)
		info.Loyalty = uint8(s.store.Field(g, objects.UnitBytes1) >> 8 & 0xFF)
	}
	s.mu.Lock()
	b := s.pet.bar
	info.Refused = s.pet.lastRefuse
	s.mu.Unlock()
	if b.pet == g {
		info.Bar, info.Enabled = true, b.enabled
		info.React, info.Command = nameAt(petReactNames, b.react), nameAt(petCommandNames, b.command)
		info.Spells = petSpells(b)
	}
	return info
}

// PetOrder is one order to the pet (pet_action). Action is one of:
//   - "attack": the pet attacks Target (0: the player's target). The server refuses a target the player may not attack and one the pet
//     cannot reach, with SMSG_PET_ACTION_FEEDBACK (a pet_error event).
//   - "follow", "stay": the command buttons. Follow also ends an attack.
//   - "passive", "defensive", "aggressive": the reaction buttons. Passive also stops the pet's attack.
//   - "cast": a pet spell by Slot (the bar button, 3 to 6), SpellID or Name (the spell must be on the bar), at Target (0: the player's target; the
//     server drops a target the spell does not need).
//   - "autocast_on", "autocast_off": switch the autocast of a spell of the pet (SpellID, Name or Slot).
type PetOrder struct {
	Action  string
	Target  packet.GUID
	Slot    *int
	SpellID uint32
	Name    string
}

// ErrNoPet is the answer to an order when the player has no pet.
var ErrNoPet = errors.New("the player has no pet")

// PetAction sends an order to the pet and applies it to the copy of the pet's state; it fails without a pet, with a dead pet, while the
// pet's bar is off, and for a spell that is not on the bar.
func (s *Session) PetAction(ctx context.Context, o PetOrder) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		info := s.Pet()
		if !info.Present {
			return nil, ErrNoPet
		}
		if info.Dead {
			return nil, errors.New("pet_action: the pet is dead")
		}
		if info.Bar && !info.Enabled {
			return nil, errors.New("pet_action: the pet's action bar is off")
		}
		act := strings.ToLower(strings.TrimSpace(o.Action))
		target := o.Target
		if target == 0 && (act == "attack" || act == "cast") {
			s.mu.Lock()
			target = s.target
			s.mu.Unlock()
		}
		send := func(button uint32, t packet.GUID) error {
			return s.conn.WritePacket(opcode.CMSGPetAction, game.BuildPetAction(info.GUID, button, t))
		}
		switch act {
		case "attack":
			if target == 0 {
				return nil, errors.New("pet_action attack: no target (give target or select one)")
			}
			return nil, send(game.PetButton(game.PetCmdAttack, game.PetActCommand), target)
		case "follow", "stay":
			cmd, state := game.PetCmdFollow, uint8(1)
			if act == "stay" {
				cmd, state = game.PetCmdStay, 0
			}
			if err := send(game.PetButton(cmd, game.PetActCommand), 0); err != nil {
				return nil, err
			}
			s.setPetState(info.GUID, func(b *petBar) { b.command = state })
			return nil, nil
		case "passive", "defensive", "aggressive":
			react := map[string]uint32{"passive": game.PetReactPassive, "defensive": game.PetReactDefensive, "aggressive": game.PetReactAggressive}[act]
			if err := send(game.PetButton(react, game.PetActReaction), 0); err != nil {
				return nil, err
			}
			s.setPetState(info.GUID, func(b *petBar) { b.react = uint8(react) })
			return nil, nil
		case "cast":
			sp, err := pickPetSpell(info, o)
			if err != nil {
				return nil, err
			}
			if sp.Slot < 0 || sp.Passive {
				return nil, fmt.Errorf("pet_action cast: %s is not a castable button of the pet's bar", petSpellLabel(sp))
			}
			typ := game.PetActDisabled
			if sp.Autocast {
				typ = game.PetActEnabled
			}
			return nil, send(game.PetButton(sp.SpellID, typ), target)
		case "autocast_on", "autocast_off":
			sp, err := pickPetSpell(info, o)
			if err != nil {
				return nil, err
			}
			if sp.Passive {
				return nil, fmt.Errorf("pet_action %s: %s is passive", act, petSpellLabel(sp))
			}
			on := act == "autocast_on"
			if err := s.conn.WritePacket(opcode.CMSGPetSpellAutocast, game.BuildPetSpellAutocast(info.GUID, sp.SpellID, on)); err != nil {
				return nil, err
			}
			s.setPetState(info.GUID, func(b *petBar) {
				for i, btn := range b.bar {
					if game.PetButtonAction(btn) == sp.SpellID && game.PetButtonType(btn) != game.PetActPassive {
						t := game.PetActDisabled
						if on {
							t = game.PetActEnabled
						}
						b.bar[i] = game.PetButton(sp.SpellID, t)
					}
				}
			})
			return nil, nil
		}
		return nil, fmt.Errorf("pet_action: unknown action %q (attack, follow, stay, passive, defensive, aggressive, cast, autocast_on, autocast_off)", o.Action)
	})
	return err
}

func (s *Session) setPetState(pet packet.GUID, fn func(*petBar)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pet.bar.pet == pet {
		fn(&s.pet.bar)
	}
}

func petSpellLabel(sp PetSpell) string {
	if sp.Name != "" {
		return fmt.Sprintf("%s (%d)", sp.Name, sp.SpellID)
	}
	return fmt.Sprint(sp.SpellID)
}

// pickPetSpell finds the spell an order names among the pet's spells: by slot, spell id or name (the highest rank on the bar when several
// share the name).
func pickPetSpell(info PetInfo, o PetOrder) (PetSpell, error) {
	if !info.Bar {
		return PetSpell{}, errors.New("pet_action: the pet's action bar is not known yet (no SMSG_PET_SPELLS)")
	}
	var found *PetSpell
	for i := range info.Spells {
		sp := &info.Spells[i]
		switch {
		case o.Slot != nil && sp.Slot == *o.Slot:
		case o.SpellID != 0 && sp.SpellID == o.SpellID:
		case o.Name != "" && strings.EqualFold(sp.Name, o.Name):
		default:
			continue
		}
		if found == nil || sp.SpellID > found.SpellID {
			found = sp
		}
	}
	if found == nil {
		return PetSpell{}, fmt.Errorf("pet_action: the pet has no such spell (slot, spell_id or name) on its bar; it has %s", petSpellList(info.Spells))
	}
	return *found, nil
}

func petSpellList(sps []PetSpell) string {
	if len(sps) == 0 {
		return "none"
	}
	var parts []string
	for _, sp := range sps {
		parts = append(parts, fmt.Sprintf("%s@%d", petSpellLabel(sp), sp.Slot))
	}
	return strings.Join(parts, ", ")
}
