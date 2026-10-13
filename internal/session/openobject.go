package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5d79486.

// OpeningSpell is the spell the agent casts at a chest or a node: 6477 "Opening" (effect 33, SPELL_EFFECT_OPEN_LOCK). Every player
// race has it with 3365, 6247, 6478, 21651 and 22810, all of the same name and effect (playercreateinfo_spell; spells.meta), and the
// core's own comment on the chest line-of-sight check names it (Spell.cpp:5921). The core lets a client cast only a spell in the
// spell book (SpellHandler.cpp:340-347).
const OpeningSpell uint32 = 6477

// OpenObject opens a chest or a node the way the client does: it casts an Opening spell at it (CMSG_CAST_SPELL with
// TARGET_FLAG_OBJECT, game.BuildCastSpellOnObject) and waits up to wait for the server's loot window (SMSG_LOOT_RESPONSE).
// CMSG_GAMEOBJ_USE on a chest only runs its scripts and sends no loot, and CMSG_LOOT on a game object guid is refused
// (GameObject.cpp:1493-1514, LootHandler.cpp:414-450): the loot of an object comes from the spell, whose OPEN_LOCK effect checks the lock
// and sends the window (SpellEffects.cpp:1915-1988, 1860-1912). The cast needs the player within the object's interact distance, in
// line of sight and standing still (Spell.cpp:5920-5935); it takes the spell's cast time, so wait covers that. spell 0 is OpeningSpell.
// A failed cast (a lock the player cannot open, out of range) is the error; one the server drops without a word (the object is not
// spawned) runs out the wait.
func (s *Session) OpenObject(ctx context.Context, g packet.GUID, spell uint32, wait time.Duration) (*LootWindow, error) {
	if g == 0 {
		return nil, errors.New("open_object needs a guid")
	}
	if spell == 0 {
		spell = OpeningSpell
	}
	var failed uint64
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		x, y, z, _ := s.selfPos()
		o, ok := s.store.GameObject(g, x, y, z)
		if !ok {
			return nil, fmt.Errorf("game object %v is not in view", g)
		}
		if o.Type != objects.GOTypeChest {
			return nil, fmt.Errorf("game object %v is a %s, not a chest or node: open_object opens a lootable object; use_object works a door, lever or goober", g, o.TypeName)
		}
		if s.walk != nil {
			return nil, errors.New("walking; wait for walk_to to finish (a cast is broken by movement)")
		}
		failed = s.mount.failed
		s.loot.skip = 0
		if s.loot.win != nil {
			s.loot.skip = s.loot.win.GUID
		}
		s.loot.pending, s.loot.ans, s.loot.ansWin = true, lootAnswer{}, nil
		return nil, s.conn.WritePacket(opcode.CMSGCastSpell, game.BuildCastSpellOnObject(spell, g))
	})
	if err != nil {
		return nil, err
	}
	w, err := s.awaitLootAnswer(ctx, wait, &failed)
	if err != nil {
		return nil, fmt.Errorf("open %v with spell %d: %w", g, spell, err)
	}
	return w, nil
}

// LootedObject is what LootObject got out of an object: the window as the server opened it and the slots now in the bags.
type LootedObject struct {
	Window *LootWindow `json:"window"`
	Taken  []LootSlot  `json:"taken"`
	Money  uint32      `json:"money,omitempty"`
	Left   []LootSlot  `json:"left,omitempty"` // slots still in the window that could not be taken (a full bag, a roll)
}

// LootObject opens the object (OpenObject), takes everything the player may take (TakeLoot) and closes the window. A slot the server
// did not remove within takeWait is in Left.
func (s *Session) LootObject(ctx context.Context, g packet.GUID, spell uint32, openWait, takeWait time.Duration) (LootedObject, error) {
	w, err := s.OpenObject(ctx, g, spell, openWait)
	if err != nil {
		return LootedObject{}, err
	}
	return s.takeAll(ctx, w, takeWait)
}

// LootCorpse opens the corpse (OpenLoot), takes everything the player may take and closes the window.
func (s *Session) LootCorpse(ctx context.Context, g packet.GUID, wait time.Duration) (LootedObject, error) {
	w, err := s.OpenLoot(ctx, g, "", wait)
	if err != nil {
		return LootedObject{}, err
	}
	return s.takeAll(ctx, w, wait)
}

// takeAll takes the items and money of the open window w and closes it. A window with nothing in it was closed already.
func (s *Session) takeAll(ctx context.Context, w *LootWindow, wait time.Duration) (LootedObject, error) {
	out := LootedObject{Window: w, Taken: []LootSlot{}}
	if len(w.Items) == 0 && w.Money == 0 {
		return out, nil
	}
	r, err := s.TakeLoot(ctx, LootTake{All: true}, wait)
	if err != nil && len(r.Taken) == 0 && len(r.Skipped) == 0 {
		return out, err
	}
	out.Money = r.Money
	bySlot := map[int]LootSlot{}
	for _, it := range w.Items {
		bySlot[int(it.Slot)] = it
	}
	for _, slot := range r.Taken {
		out.Taken = append(out.Taken, bySlot[slot])
	}
	for _, slot := range append(append([]int{}, r.Left...), r.Skipped...) {
		out.Left = append(out.Left, bySlot[slot])
	}
	if cur, _ := s.LootWindow(ctx); cur != nil && cur.GUID == w.GUID {
		if err := s.CloseLoot(ctx, wait); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Race is the player's race id (UNIT_FIELD_BYTES_0, low byte), 0 until the player's create block has arrived.
func (s *Session) Race() uint8 { return uint8(s.store.Field(s.selfGUID(), objects.UnitBytes0)) }

// Team names the faction side of a race id, "alliance" or "horde" ("" for an id that is neither). The core takes it from the race's base
// language (Player::TeamForRace, Player.cpp:7755-7773); the race masks name the same sets: human 1, dwarf 3, night elf 4, gnome 7 and
// high elf 10 are the Alliance, orc 2, undead 5, tauren 6, troll 8 and goblin 9 the Horde (SharedDefines.h:41-70).
func Team(race uint8) string {
	switch race {
	case 1, 3, 4, 7, 10:
		return "alliance"
	case 2, 5, 6, 8, 9:
		return "horde"
	}
	return ""
}

// Team is the player's side, from its race ("" while the race is unknown).
func (s *Session) Team() string { return Team(s.Race()) }
