package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

func (s *Session) registerItems() {
	s.handlers[opcode.SMSGInventoryChangeFailure] = s.onInventoryChangeFailure
}

// Inventory lists the backpack (bag 255, slots 23-38) from the player's own update fields.
func (s *Session) Inventory() []objects.Item {
	return s.store.Backpack(s.selfGUID())
}

// Equipment lists the worn items (inventory slots 0-18), occupied slots only, from the player's own update fields.
func (s *Session) Equipment() []objects.Item {
	return s.store.Equipment(s.selfGUID())
}

// EquipRef picks an item: the first item with Entry in the backpack or the bags, or
// Bag/Slot (Bag defaults to the backpack, 255).
type EquipRef struct {
	Entry uint32
	Bag   *uint8
	Slot  *uint8
}

// Equip equips an item with CMSG_AUTOEQUIP_ITEM, as a right-click in the bags does. The
// server answers a refusal with SMSG_INVENTORY_CHANGE_FAILURE (an inventory_error event)
// and a success with the player's changed inventory fields (ItemHandler.cpp:159-200).
func (s *Session) Equip(ctx context.Context, ref EquipRef) (objects.Item, error) {
	if ref.Entry == 0 && ref.Slot == nil {
		return objects.Item{}, errors.New("equip needs an item_entry or a slot (bag defaults to the backpack, 255)")
	}
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		it, err := s.pickItem(ref)
		if err != nil {
			return nil, err
		}
		return it, s.conn.WritePacket(opcode.CMSGAutoequipItem, game.BuildAutoEquipItem(it.Bag, it.Slot))
	})
	if err != nil {
		return objects.Item{}, err
	}
	return v.(objects.Item), nil
}

// Items lists every item in the backpack and the bags, backpack first.
func (s *Session) Items() []objects.Item {
	return append(s.store.Backpack(s.selfGUID()), s.store.Bags(s.selfGUID())...)
}

// pickItem finds ref: the first item with the entry, in the backpack and then the bags, or
// the item at bag/slot.
// An empty slot is an error, not a CMSG_AUTOEQUIP_ITEM the server would drop.
func (s *Session) pickItem(ref EquipRef) (objects.Item, error) {
	if ref.Slot == nil {
		for _, it := range s.Items() {
			if it.Entry == ref.Entry {
				return it, nil
			}
		}
		return objects.Item{}, fmt.Errorf("item %d is not in the backpack or the bags", ref.Entry)
	}
	bag := objects.BackpackBag
	if ref.Bag != nil {
		bag = *ref.Bag
	}
	it, ok := s.store.ItemAt(s.selfGUID(), bag, *ref.Slot)
	if !ok {
		return objects.Item{}, fmt.Errorf("no item at bag %d slot %d (the backpack is bag 255, slots 23-38; bags are 19-22)", bag, *ref.Slot)
	}
	return it, nil
}

func (s *Session) onInventoryChangeFailure(body []byte) error {
	f, err := game.ParseInventoryChangeFailure(body)
	if err != nil || f.Result == 0 {
		return err
	}
	d := map[string]any{"result": f.Result}
	if f.Result == game.EquipErrCantEquipLevel {
		d["required_level"] = f.RequiredLevel
	}
	if f.Item != 0 {
		d["item_guid"] = f.Item
		if e := s.store.Entry(f.Item); e != 0 {
			d["item_entry"] = e
		}
	}
	s.emit(events.Event{Kind: events.KindInventoryError, Text: fmt.Sprintf("inventory change failed (InventoryResult %d)", f.Result), Data: d})
	return nil
}
