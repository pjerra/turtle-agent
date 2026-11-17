package game

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// BuildListInventory is CMSG_LIST_INVENTORY: the vendor's GUID (ItemHandler.cpp:784-796).
func BuildListInventory(vendor packet.GUID) []byte { return packet.NewWriter().GUID(vendor).Bytes() }

// BuildBuyItem is CMSG_BUY_ITEM: vendor GUID, item entry, u8 count and a u8 the core reads
// and drops (ItemHandler.cpp:772-782). The count is in purchases: the server hands over
// count * the item's BuyCount (Player.cpp:20289-20497).
func BuildBuyItem(vendor packet.GUID, item uint32, count uint8) []byte {
	return packet.NewWriter().GUID(vendor).U32(item).U8(count).U8(0).Bytes()
}

// BuildBuyItemInSlot is CMSG_BUY_ITEM_IN_SLOT: vendor GUID, item entry, the GUID of the
// destination bag (the player's own GUID for the backpack), the slot in that bag and a u8
// count (ItemHandler.cpp:734-770).
func BuildBuyItemInSlot(vendor packet.GUID, item uint32, bag packet.GUID, slot, count uint8) []byte {
	return packet.NewWriter().GUID(vendor).U32(item).GUID(bag).U8(slot).U8(count).Bytes()
}

// BuildSellItem is CMSG_SELL_ITEM: vendor GUID, item GUID and a u8 count, 0 for the whole
// stack (ItemHandler.cpp:542-560, :606-614).
func BuildSellItem(vendor, item packet.GUID, count uint8) []byte {
	return packet.NewWriter().GUID(vendor).GUID(item).U8(count).Bytes()
}

// UnlimitedStock is the stock a vendor line sends when the supply is not limited.
const UnlimitedStock uint32 = 0xFFFFFFFF

// VendorItem is one line of SMSG_LIST_INVENTORY (ItemHandler.cpp:931-981). Index counts
// the lines the server sent, from 1; it is not the slot SMSG_BUY_ITEM reports, since the
// server leaves out lines the player cannot use.
type VendorItem struct {
	Index      uint32 `json:"index"`
	Entry      uint32 `json:"item_entry"`
	DisplayID  uint32 `json:"display_id"`
	Stock      int64  `json:"stock"` // -1: unlimited
	Price      uint32 `json:"price"` // copper, after the reputation discount
	Durability uint32 `json:"durability"`
	BuyCount   uint32 `json:"buy_count"` // items per purchase: a stack of ammo or reagents
}

// VendorList is SMSG_LIST_INVENTORY (ItemHandler.cpp:861-970). An empty list carries a
// result byte (0: the vendor has no inventory).
type VendorList struct {
	Vendor packet.GUID
	Items  []VendorItem
	Empty  bool
}

func ParseListInventory(body []byte) (VendorList, error) {
	r := packet.NewReader(body)
	l := VendorList{Vendor: r.GUID()}
	n := r.U8()
	for i := uint8(0); i < n && r.Err() == nil; i++ {
		it := VendorItem{Index: r.U32(), Entry: r.U32(), DisplayID: r.U32(), Stock: int64(r.U32()),
			Price: r.U32(), Durability: r.U32(), BuyCount: r.U32()}
		if it.Stock == int64(UnlimitedStock) {
			it.Stock = -1
		}
		if r.Err() == nil {
			l.Items = append(l.Items, it)
		}
	}
	if n == 0 && r.Err() == nil {
		l.Empty = true
		if r.Remaining() > 0 {
			r.U8() // the error code: only "no inventory" exists
		}
	}
	return l, done(r, "SMSG_LIST_INVENTORY")
}

// buyErrors is the BuyResult enum SMSG_BUY_FAILED carries (Item.h:117-128).
var buyErrors = map[uint8]string{
	0: "cant_find_item", 1: "item_already_sold", 2: "not_enough_money", 4: "seller_dont_like_you",
	5: "too_far", 7: "item_sold_out", 8: "cant_carry_more", 11: "rank_required", 12: "reputation_required",
}

// BuyErrorName names a BuyResult code.
func BuyErrorName(code uint8) string {
	if n, ok := buyErrors[code]; ok {
		return n
	}
	return fmt.Sprintf("unknown(%d)", code)
}

// BuyFailed is SMSG_BUY_FAILED: the vendor (zero when none was found), the item entry and
// the BuyResult (Player.cpp:13589-13599).
type BuyFailed struct {
	Vendor packet.GUID
	Item   uint32
	Code   uint8
}

func ParseBuyFailed(body []byte) (BuyFailed, error) {
	r := packet.NewReader(body)
	f := BuyFailed{Vendor: r.GUID(), Item: r.U32(), Code: r.U8()}
	return f, done(r, "SMSG_BUY_FAILED")
}

// BuyItem is SMSG_BUY_ITEM, the server's answer to a purchase: the vendor, its line number
// counted from 1 in the vendor's own list, the stock left (UnlimitedStock when not
// limited) and the number of purchases (Player.cpp:20490-20496).
type BuyItem struct {
	Vendor packet.GUID
	Slot   uint32
	Stock  uint32
	Count  uint32
}

func ParseBuyItem(body []byte) (BuyItem, error) {
	r := packet.NewReader(body)
	b := BuyItem{Vendor: r.GUID(), Slot: r.U32(), Stock: r.U32(), Count: r.U32()}
	return b, done(r, "SMSG_BUY_ITEM")
}

// sellErrors is the SellResult enum SMSG_SELL_ITEM carries (Item.h:130-138).
var sellErrors = map[uint8]string{
	1: "cant_find_item", 2: "cant_sell_item", 3: "cant_find_vendor", 4: "not_your_item", 5: "unknown", 6: "only_empty_bag",
}

// SellErrorName names a SellResult code.
func SellErrorName(code uint8) string {
	if n, ok := sellErrors[code]; ok {
		return n
	}
	return fmt.Sprintf("unknown(%d)", code)
}

// SellFailed is SMSG_SELL_ITEM: the core sends it for refusals only (a sale is answered by
// the inventory and money fields): the vendor (zero when not found), the item and the
// SellResult (Player.cpp:13601-13611).
type SellFailed struct {
	Vendor, Item packet.GUID
	Code         uint8
}

func ParseSellItem(body []byte) (SellFailed, error) {
	r := packet.NewReader(body)
	f := SellFailed{Vendor: r.GUID(), Item: r.GUID(), Code: r.U8()}
	return f, done(r, "SMSG_SELL_ITEM")
}
