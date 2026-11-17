package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

func (s *Session) registerLoot() {
	s.handlers[opcode.SMSGLootStartRoll] = s.onLootStartRoll
	s.handlers[opcode.SMSGLootRoll] = s.onLootRoll
	s.handlers[opcode.SMSGLootRollWon] = s.onLootRollWon
	s.handlers[opcode.SMSGLootAllPassed] = s.onLootAllPassed
}

// rollKey is how the server finds a roll: the looted object and the loot slot
// (Group.cpp:926-938).
type rollKey struct {
	looted packet.GUID
	slot   uint32
}

// openRoll is an item up for rolls; the session numbers rolls from 1 so events and the
// roll method can name one without the looted guid.
type openRoll struct {
	id      uint64
	item    uint32
	expires time.Time
}

// rollGrace keeps a roll open a little past its countdown, for the server's end-of-roll packets.
const rollGrace = 10 * time.Second

// RollRef picks an open roll: by ID, or by loot Slot (with Looted when two corpses share a slot).
type RollRef struct {
	ID     uint64
	Slot   *uint32
	Looted packet.GUID
}

type RollInfo struct {
	ID     uint64      `json:"roll_id"`
	Looted packet.GUID `json:"looted"`
	Slot   uint32      `json:"item_slot"`
	Item   uint32      `json:"item_entry"`
	Choice string      `json:"choice"`
}

// Roll votes need, greed or pass on an open group-loot roll with CMSG_LOOT_ROLL, as the
// roll frame's buttons do. The server reports the vote back as a loot_roll event; a vote
// on a roll it no longer holds is ignored (Group.cpp:926-938).
func (s *Session) Roll(ctx context.Context, ref RollRef, choice string) (RollInfo, error) {
	vote, ok := game.RollChoiceByName(choice)
	if !ok {
		return RollInfo{}, fmt.Errorf("unknown roll choice %q: use need, greed or pass", choice)
	}
	if ref.ID == 0 && ref.Slot == nil {
		return RollInfo{}, errors.New("roll needs a roll_id or an item_slot (from the loot_roll_start event)")
	}
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		s.pruneRolls(s.now())
		var found []RollInfo
		for k, r := range s.rolls {
			if (ref.ID != 0 && r.id != ref.ID) || (ref.Slot != nil && k.slot != *ref.Slot) || (ref.Looted != 0 && k.looted != ref.Looted) {
				continue
			}
			found = append(found, RollInfo{ID: r.id, Looted: k.looted, Slot: k.slot, Item: r.item, Choice: game.RollChoiceName(vote)})
		}
		switch len(found) {
		case 0:
			return nil, errors.New("no open roll matches; rolls open with a loot_roll_start event and close when won or all passed")
		case 1:
		default:
			return nil, fmt.Errorf("%d open rolls match; give the roll_id or the looted guid", len(found))
		}
		info := found[0]
		return info, s.conn.WritePacket(opcode.CMSGLootRoll, game.BuildLootRoll(info.Looted, info.Slot, vote))
	})
	if err != nil {
		return RollInfo{}, err
	}
	return v.(RollInfo), nil
}

// pruneRolls forgets rolls whose countdown ran out without an end packet (the loot was
// removed, Group.cpp:1104-1111).
func (s *Session) pruneRolls(now time.Time) {
	for k, r := range s.rolls {
		if now.After(r.expires) {
			delete(s.rolls, k)
		}
	}
}

// rollFor returns the open roll for (looted, slot), opening one for a roll that started
// before the session saw it.
func (s *Session) rollFor(looted packet.GUID, slot, item uint32, countdown time.Duration) *openRoll {
	k := rollKey{looted, slot}
	r, ok := s.rolls[k]
	if !ok || r.item != item {
		s.lastRollID++
		r = &openRoll{id: s.lastRollID, item: item, expires: s.now().Add(countdown + rollGrace)}
		s.rolls[k] = r
	}
	return r
}

// rollData is the data every loot event carries; each call builds a fresh map.
func rollData(r *openRoll, looted packet.GUID, slot, item, randomProp uint32) map[string]any {
	d := map[string]any{"roll_id": r.id, "looted": looted, "item_slot": slot, "item_entry": item}
	if randomProp != 0 {
		d["random_property"] = randomProp
	}
	return d
}

func (s *Session) onLootStartRoll(body []byte) error {
	st, err := game.ParseLootStartRoll(body)
	if err != nil {
		return err
	}
	s.pruneRolls(s.now())
	delete(s.rolls, rollKey{st.Looted, st.Slot}) // a new roll on the same slot starts afresh
	r := s.rollFor(st.Looted, st.Slot, st.Item, time.Duration(st.Countdown)*time.Millisecond)
	d := rollData(r, st.Looted, st.Slot, st.Item, st.RandomProp)
	d["countdown_ms"] = st.Countdown
	s.emit(events.Event{Kind: events.KindLootStart, SourceGUID: st.Looted, Data: d})
	return nil
}

func (s *Session) onLootRoll(body []byte) error {
	l, err := game.ParseLootRoll(body)
	if err != nil {
		return err
	}
	r := s.rollFor(l.Looted, l.Slot, l.Item, 0)
	d := rollData(r, l.Looted, l.Slot, l.Item, l.RandomProp)
	choice, rolled := l.Vote()
	d["choice"] = choice
	if rolled {
		d["roll"] = l.Number
	}
	s.emit(events.Event{Kind: events.KindLootRoll, SourceGUID: l.Player, Data: d})
	return nil
}

func (s *Session) onLootRollWon(body []byte) error {
	w, err := game.ParseLootRollWon(body)
	if err != nil {
		return err
	}
	r := s.rollFor(w.Looted, w.Slot, w.Item, 0)
	delete(s.rolls, rollKey{w.Looted, w.Slot})
	d := rollData(r, w.Looted, w.Slot, w.Item, w.RandomProp)
	d["choice"], d["roll"] = w.Choice(), w.Number
	s.emit(events.Event{Kind: events.KindLootWon, SourceGUID: w.Winner, Data: d})
	return nil
}

func (s *Session) onLootAllPassed(body []byte) error {
	p, err := game.ParseLootAllPassed(body)
	if err != nil {
		return err
	}
	r := s.rollFor(p.Looted, p.Slot, p.Item, 0)
	delete(s.rolls, rollKey{p.Looted, p.Slot})
	s.emit(events.Event{Kind: events.KindLootAllPassed, Data: rollData(r, p.Looted, p.Slot, p.Item, p.RandomProp)})
	return nil
}
