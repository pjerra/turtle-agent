package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// npcWait is how long a request waits for the evidence of its result before the session
// forgets it: a sale or an accepted quest has no packet of its own, only changed fields.
const npcWait = 5 * time.Second

func (s *Session) registerVendor() {
	s.handlers[opcode.SMSGListInventory] = s.onListInventory
	s.handlers[opcode.SMSGBuyItem] = s.onBuyItem
	s.handlers[opcode.SMSGBuyFailed] = s.onBuyFailed
	s.handlers[opcode.SMSGSellItem] = s.onSellItem
	s.handlers[opcode.SMSGTrainerList] = s.onTrainerList
	s.handlers[opcode.SMSGTrainerBuySucceeded] = s.onTrainerBuySucceeded
	s.handlers[opcode.SMSGTrainerBuyFailed] = s.onTrainerBuyFailed
}

type pendingBuy struct {
	vendor  packet.GUID
	entry   uint32
	count   uint8
	expires time.Time
}

// pendingSell is a sale waiting for the sold item to leave its slot and the player's money
// to go up. The core answers a sale with the changed inventory and money fields, and only a
// refusal with a packet.
type pendingSell struct {
	vendor  packet.GUID
	item    objects.Item // as it was when the sale was sent
	count   uint32       // 0: the whole stack
	money   uint32       // the player's copper when the sale was sent
	expires time.Time
}

// VendorList asks a vendor for its list (CMSG_LIST_INVENTORY). The answer is a vendor_list
// event. A dead player gets no answer at all (ItemHandler.cpp:789-791); for a living one out
// of reach of a vendor NPC the core sends a SMSG_SELL_ITEM refusal (a vendor_sell_failed
// event with item_guid 0).
func (s *Session) VendorList(ctx context.Context, vendor packet.GUID) error {
	if vendor == 0 {
		return errors.New("vendor_list needs the vendor's guid")
	}
	return s.send(ctx, opcode.CMSGListInventory, game.BuildListInventory(vendor))
}

// BuyRef says what to buy. Bag and Slot, when set, name where the item goes (CMSG_BUY_ITEM_IN_SLOT):
// bag 255 is the backpack, 19-22 a bag, and the slot is inside it. Without them the server picks.
type BuyRef struct {
	Entry uint32
	Count uint8 // purchases, 0: one; each hands over the item's buy_count
	Bag   *uint8
	Slot  *uint8
}

// Buy buys an item from a vendor (CMSG_BUY_ITEM, or CMSG_BUY_ITEM_IN_SLOT with a slot). The
// result is a vendor_buy or vendor_buy_failed event; a full bag is an inventory_error event.
// The vendor need not have been listed first: the server checks its own list.
func (s *Session) Buy(ctx context.Context, vendor packet.GUID, ref BuyRef) error {
	if vendor == 0 || ref.Entry == 0 {
		return errors.New("buy needs the vendor's guid and an item_entry")
	}
	if ref.Slot == nil && ref.Bag != nil {
		return errors.New("buy: bag needs a slot")
	}
	count := max(ref.Count, 1)
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		body := game.BuildBuyItem(vendor, ref.Entry, count)
		op := opcode.CMSGBuyItem
		if ref.Slot != nil {
			bag := objects.BackpackBag
			if ref.Bag != nil {
				bag = *ref.Bag
			}
			bagGUID, err := s.bagGUID(bag)
			if err != nil {
				return nil, err
			}
			body, op = game.BuildBuyItemInSlot(vendor, ref.Entry, bagGUID, *ref.Slot, count), opcode.CMSGBuyItemInSlot
		}
		s.noteBuy(vendor, ref.Entry, count)
		return nil, s.conn.WritePacket(op, body)
	})
	return err
}

// noteBuy, noteSell and the quest note functions record a request the agent sent, whose
// result the session turns into an event once the evidence arrives. The RPC methods call
// them before writing the packet; a replay calls them for the request packets in a capture.
func (s *Session) noteBuy(vendor packet.GUID, entry uint32, count uint8) {
	s.prunePending(s.now())
	s.npc.buys = append(s.npc.buys, pendingBuy{vendor: vendor, entry: entry, count: count, expires: s.now().Add(npcWait)})
}

func (s *Session) noteSell(vendor packet.GUID, it objects.Item, count uint32) {
	s.prunePending(s.now())
	money, _ := s.store.Money(s.selfGUID())
	s.npc.sells = append(s.npc.sells, pendingSell{vendor: vendor, item: it, count: count, money: money, expires: s.now().Add(npcWait)})
}

// bagGUID is the GUID the server wants for a destination bag: the player's own for the
// backpack, the bag item's for a bag in inventory slots 19-22 (ItemHandler.cpp:743-760).
func (s *Session) bagGUID(bag uint8) (packet.GUID, error) {
	if bag == objects.BackpackBag {
		return s.selfGUID(), nil
	}
	if bag < objects.BagFirst || bag >= objects.BagEnd {
		return 0, fmt.Errorf("buy: bag %d is neither the backpack (255) nor a bag (19-22)", bag)
	}
	it, ok := s.store.ItemAt(s.selfGUID(), objects.BackpackBag, bag)
	if !ok {
		return 0, fmt.Errorf("buy: no bag in slot %d", bag)
	}
	return it.GUID, nil
}

// Sell sells an item to a vendor (CMSG_SELL_ITEM) and returns it as it was. The item is picked as
// for equip: its entry (first in the backpack) or bag and slot. A count of 0 sells the whole
// stack. The result is a vendor_sell event once the item has left its slot (or its stack shrank),
// or a vendor_sell_failed event.
func (s *Session) Sell(ctx context.Context, vendor packet.GUID, ref EquipRef, count uint32) (objects.Item, error) {
	if vendor == 0 {
		return objects.Item{}, errors.New("sell needs the vendor's guid")
	}
	if ref.Entry == 0 && ref.Slot == nil {
		return objects.Item{}, errors.New("sell needs an item_entry or a slot (bag defaults to the backpack, 255)")
	}
	if count > 255 {
		return objects.Item{}, errors.New("sell: count is at most 255 per call")
	}
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		it, err := s.pickItem(ref)
		if err != nil {
			return nil, err
		}
		if it.Count != 0 && count > it.Count {
			return nil, fmt.Errorf("sell: the stack holds %d, not %d", it.Count, count)
		}
		s.prunePending(s.now())
		// One sale in flight: the core sends nothing for a sale, so the money it brings is
		// only attributable when no other sale waits for the same money.
		if len(s.npc.sells) != 0 {
			return nil, errors.New("sell: the previous sale is still waiting for its vendor_sell or vendor_sell_failed (up to 5 s); sell one item at a time")
		}
		s.noteSell(vendor, it, count)
		return it, s.conn.WritePacket(opcode.CMSGSellItem, game.BuildSellItem(vendor, it.GUID, uint8(count)))
	})
	if err != nil {
		return objects.Item{}, err
	}
	return v.(objects.Item), nil
}

func (s *Session) onListInventory(body []byte) error {
	l, err := game.ParseListInventory(body)
	if err != nil {
		return err
	}
	items := make([]map[string]any, len(l.Items))
	for i, it := range l.Items {
		items[i] = map[string]any{"index": it.Index, "item_entry": it.Entry, "display_id": it.DisplayID, "stock": it.Stock,
			"price": it.Price, "durability": it.Durability, "buy_count": it.BuyCount}
	}
	s.emit(events.Event{Kind: events.KindVendorList, SourceGUID: l.Vendor, Data: map[string]any{"items": items, "empty": l.Empty}})
	return nil
}

func (s *Session) onBuyItem(body []byte) error {
	b, err := game.ParseBuyItem(body)
	if err != nil {
		return err
	}
	d := map[string]any{"slot": b.Slot, "count": b.Count, "stock": int64(b.Stock)}
	if b.Stock == game.UnlimitedStock {
		d["stock"] = int64(-1)
	}
	// The server counts its own list, which may hold lines this player is not shown, so the
	// slot cannot name the item: the oldest purchase asked of this vendor does.
	for i, p := range s.npc.buys {
		if p.vendor == b.Vendor {
			d["item_entry"] = p.entry
			s.npc.buys = append(s.npc.buys[:i], s.npc.buys[i+1:]...)
			break
		}
	}
	s.emit(events.Event{Kind: events.KindVendorBuy, SourceGUID: b.Vendor, Data: d})
	return nil
}

func (s *Session) onBuyFailed(body []byte) error {
	f, err := game.ParseBuyFailed(body)
	if err != nil {
		return err
	}
	for i, p := range s.npc.buys {
		if p.entry == f.Item {
			s.npc.buys = append(s.npc.buys[:i], s.npc.buys[i+1:]...)
			break
		}
	}
	s.emit(events.Event{Kind: events.KindVendorBuyFailed, SourceGUID: f.Vendor,
		Text: "buy failed: " + game.BuyErrorName(f.Code),
		Data: map[string]any{"item_entry": f.Item, "code": f.Code, "reason": game.BuyErrorName(f.Code)}})
	return nil
}

func (s *Session) onSellItem(body []byte) error {
	f, err := game.ParseSellItem(body)
	if err != nil {
		return err
	}
	d := map[string]any{"code": f.Code, "reason": game.SellErrorName(f.Code)}
	if f.Item != 0 {
		d["item_guid"] = f.Item
	}
	for i, p := range s.npc.sells {
		if f.Item != 0 && p.item.GUID == f.Item || f.Item == 0 && p.vendor == f.Vendor {
			d["item_entry"] = p.item.Entry
			s.npc.sells = append(s.npc.sells[:i], s.npc.sells[i+1:]...)
			break
		}
	}
	s.emit(events.Event{Kind: events.KindVendorSellFailed, SourceGUID: f.Vendor, Text: "sell failed: " + game.SellErrorName(f.Code), Data: d})
	return nil
}

// checkSells emits vendor_sell for each pending sale whose item has left its slot (or whose
// stack shrank) while the player's money went up. The event is inferred: the core sends no
// packet for a sale. The money test keeps an equip, a use or a destroy of the same item from
// passing for a sale, and a sale the core drops silently (ItemHandler.cpp:553-554) from
// passing either; an item still in some bag has moved, not sold. Money that arrives meanwhile
// (loot, a quest) with a real sale in flight is still counted as its price.
func (s *Session) checkSells() {
	self := s.selfGUID()
	money, _ := s.store.Money(self)
	kept := s.npc.sells[:0]
	for _, p := range s.npc.sells {
		cur, ok := s.store.ItemAt(self, p.item.Bag, p.item.Slot)
		sold := uint32(0)
		switch {
		case !ok || cur.GUID != p.item.GUID:
			if s.inBags(self, p.item.GUID) {
				kept = append(kept, p) // moved to another slot, not sold
				continue
			}
			sold = p.item.Count // 0 when the stack was never seen
		case p.item.Count != 0 && cur.Count < p.item.Count:
			sold = p.item.Count - cur.Count
		default:
			kept = append(kept, p)
			continue
		}
		if money <= p.money {
			kept = append(kept, p) // the item moved, the money has not arrived yet, or it was no sale
			continue
		}
		d := map[string]any{"item_guid": p.item.GUID, "item_entry": p.item.Entry, "bag": p.item.Bag, "slot": p.item.Slot, "money": money - p.money}
		if sold != 0 {
			d["count"] = sold
		}
		s.emit(events.Event{Kind: events.KindVendorSell, SourceGUID: p.vendor, Data: d})
	}
	s.npc.sells = kept
}

// inBags reports whether the item g is still in the player's backpack or bags.
func (s *Session) inBags(self, g packet.GUID) bool {
	for _, list := range [][]objects.Item{s.store.Backpack(self), s.store.Bags(self)} {
		for _, it := range list {
			if it.GUID == g {
				return true
			}
		}
	}
	return false
}

// TrainerList asks a trainer for its list (CMSG_TRAINER_LIST). The answer is a trainer_list
// event; the core sends nothing when the NPC is out of reach or trains nothing for this
// player.
func (s *Session) TrainerList(ctx context.Context, trainer packet.GUID) error {
	if trainer == 0 {
		return errors.New("trainer_list needs the trainer's guid")
	}
	return s.send(ctx, opcode.CMSGTrainerList, game.BuildTrainerList(trainer))
}

// Train buys a spell from a trainer (CMSG_TRAINER_BUY_SPELL). The result is a trainer_trained
// or trainer_failed event.
func (s *Session) Train(ctx context.Context, trainer packet.GUID, spell uint32) error {
	if trainer == 0 || spell == 0 {
		return errors.New("train needs the trainer's guid and a spell_id")
	}
	return s.send(ctx, opcode.CMSGTrainerBuySpell, game.BuildTrainerBuySpell(trainer, spell))
}

func (s *Session) onTrainerList(body []byte) error {
	l, err := game.ParseTrainerList(body)
	if err != nil {
		return err
	}
	spells := make([]map[string]any, len(l.Spells))
	for i, sp := range l.Spells {
		m := map[string]any{"spell_id": sp.Spell, "state": sp.State, "cost": sp.Cost, "level": sp.Level}
		for k, v := range map[string]uint32{"req_skill": sp.ReqSkill, "req_skill_value": sp.ReqSkillValue, "req_spell_1": sp.ReqSpell1, "req_spell_2": sp.ReqSpell2} {
			if v != 0 {
				m[k] = v
			}
		}
		if sp.PrimaryProfession {
			m["primary_profession"] = true
		}
		spells[i] = m
	}
	d := map[string]any{"trainer_type": l.Type, "greeting": l.Greeting, "spells": spells}
	if int(l.Declared) != len(l.Spells) {
		// The core counts a line it sometimes does not write (see ParseTrainerList).
		d["declared"] = l.Declared
	}
	s.emit(events.Event{Kind: events.KindTrainerList, SourceGUID: l.Trainer, Data: d})
	return nil
}

func (s *Session) onTrainerBuySucceeded(body []byte) error {
	t, err := game.ParseTrainerSucceeded(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindTrainerTrained, SourceGUID: t.Trainer, SpellID: t.Spell})
	return nil
}

func (s *Session) onTrainerBuyFailed(body []byte) error {
	f, err := game.ParseTrainerFailed(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindTrainerFailed, SourceGUID: f.Trainer, SpellID: f.Spell,
		Text: "training failed: " + game.TrainerFailName(f.Code),
		Data: map[string]any{"code": f.Code, "reason": game.TrainerFailName(f.Code)}})
	return nil
}
