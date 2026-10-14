package game

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0 (TradeHandler.cpp,
// SharedDefines.h:1836-1859, Player.h:658-660).

// The trade window has six slots for items to hand over and a seventh that is not traded
// (an item a spell, such as a lockpick, is cast on).
const (
	TradeSlotsTraded = 6
	TradeSlotCount   = 7
)

// TradeDistance is TRADE_DISTANCE (Object.h:47), the 3D distance between the two players'
// centres past which the core refuses to open or finish a trade.
const TradeDistance float32 = 11.11

// TradeStatus is a TradeStatus code of SMSG_TRADE_STATUS (SharedDefines.h:1836-1859).
type TradeStatus uint32

const (
	TradeBusy          TradeStatus = 0
	TradeBegin         TradeStatus = 1 // the other player started a trade with us; carries their guid
	TradeOpenWindow    TradeStatus = 2 // both clients open the window
	TradeCancelled     TradeStatus = 3
	TradeAccept        TradeStatus = 4 // the other player accepted
	TradeBusy2         TradeStatus = 5
	TradeNoTarget      TradeStatus = 6
	TradeBackToTrade   TradeStatus = 7 // our accept was withdrawn or refused: the window stays open
	TradeComplete      TradeStatus = 8
	TradeRejected      TradeStatus = 9
	TradeTargetTooFar  TradeStatus = 10
	TradeWrongFaction  TradeStatus = 11
	TradeCloseWindow   TradeStatus = 12
	TradeUnknown13     TradeStatus = 13
	TradeIgnoreYou     TradeStatus = 14
	TradeYouStunned    TradeStatus = 15
	TradeTargetStunned TradeStatus = 16
	TradeYouDead       TradeStatus = 17
	TradeTargetDead    TradeStatus = 18
	TradeYouLogout     TradeStatus = 19
	TradeTargetLogout  TradeStatus = 20
	TradeTrialAccount  TradeStatus = 21
	TradeOnlyConjured  TradeStatus = 22
)

var tradeStatusNames = [...]string{
	"busy", "begin", "open", "cancelled", "accepted", "busy", "no_target", "back_to_trade", "complete", "rejected",
	"too_far", "wrong_faction", "close_window", "cancelled", "ignored", "you_stunned", "target_stunned",
	"you_dead", "target_dead", "you_logout", "target_logout", "trial_account", "only_conjured",
}

// Name is the status as events and RPC results spell it; an unknown code is "status_<n>".
func (s TradeStatus) Name() string {
	if int(s) < len(tradeStatusNames) {
		return tradeStatusNames[s]
	}
	return fmt.Sprintf("status_%d", uint32(s))
}

// Ends reports whether the status closes the trade window, or says a trade never opened.
// Everything but the begin, open, accept and back-to-trade messages does.
func (s TradeStatus) Ends() bool {
	switch s {
	case TradeBegin, TradeOpenWindow, TradeAccept, TradeBackToTrade:
		return false
	}
	return true
}

// BuildInitiateTrade is CMSG_INITIATE_TRADE: the full u64 guid of the player to trade with
// (HandleInitiateTradeOpcode, TradeHandler.cpp:608-618). Only the other player's client gets
// SMSG_TRADE_STATUS begin; the window opens for both when they answer with CMSG_BEGIN_TRADE.
func BuildInitiateTrade(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// BuildBeginTrade is CMSG_BEGIN_TRADE, the answer to a begin status; it has no body
// (TradeHandler.cpp:580-598).
func BuildBeginTrade() []byte { return nil }

// BuildAcceptTrade is CMSG_ACCEPT_TRADE. The core reads and drops one u32 (TradeHandler.cpp:292).
func BuildAcceptTrade() []byte { return packet.NewWriter().U32(0).Bytes() }

// BuildUnacceptTrade is CMSG_UNACCEPT_TRADE, withdrawing our accept; no body (TradeHandler.cpp:571).
func BuildUnacceptTrade() []byte { return nil }

// BuildCancelTrade is CMSG_CANCEL_TRADE, closing the window for both players; no body
// (TradeHandler.cpp:601-607).
func BuildCancelTrade() []byte { return nil }

// BuildSetTradeItem is CMSG_SET_TRADE_ITEM: u8 trade slot (0-6), then the bag and slot the
// item sits in, addressed as for CMSG_AUTOEQUIP_ITEM (TradeHandler.cpp:737-745). The whole
// stack is traded; split first to give part of it.
func BuildSetTradeItem(tradeSlot, bag, slot uint8) []byte { return []byte{tradeSlot, bag, slot} }

// BuildClearTradeItem is CMSG_CLEAR_TRADE_ITEM: u8 trade slot (TradeHandler.cpp:801-806).
func BuildClearTradeItem(tradeSlot uint8) []byte { return []byte{tradeSlot} }

// BuildSetTradeGold is CMSG_SET_TRADE_GOLD: u32 copper to hand over (TradeHandler.cpp:715-722).
// The core drops it silently when it is more than the player holds.
func BuildSetTradeGold(copper uint32) []byte { return packet.NewWriter().U32(copper).Bytes() }

// BuildSplitItem is CMSG_SPLIT_ITEM: source bag and slot, destination bag and slot (an empty
// slot), u8 count to move (ItemHandler.cpp:36-66).
func BuildSplitItem(srcBag, srcSlot, dstBag, dstSlot, count uint8) []byte {
	return []byte{srcBag, srcSlot, dstBag, dstSlot, count}
}

// TradeStatusMsg is SMSG_TRADE_STATUS. Only the begin status carries more than the code: the
// guid of the player who started the trade (WorldSession::SendTradeStatus, TradeHandler.cpp:36-70).
type TradeStatusMsg struct {
	Status TradeStatus
	Other  packet.GUID // TradeBegin only
}

func ParseTradeStatus(body []byte) (TradeStatusMsg, error) {
	r := packet.NewReader(body)
	m := TradeStatusMsg{Status: TradeStatus(r.U32())}
	if m.Status == TradeBegin {
		m.Other = r.GUID()
	}
	// The other statuses add padding the client ignores (close window: u32, u8, u32; only
	// conjured: u8), so trailing bytes are not an error here.
	if err := r.Err(); err != nil {
		return m, fmt.Errorf("SMSG_TRADE_STATUS: %w", err)
	}
	return m, nil
}

// TradeItem is one filled slot of SMSG_TRADE_STATUS_EXTENDED.
type TradeItem struct {
	Slot      uint8  `json:"slot"`
	Entry     uint32 `json:"item_entry"`
	DisplayID uint32 `json:"display_id,omitempty"`
	Count     uint32 `json:"count"`
}

// TradeView is one side of the window as SMSG_TRADE_STATUS_EXTENDED shows it: the gold and the
// items in the slots (WorldSession::SendUpdateTrade, TradeHandler.cpp:84-132).
type TradeView struct {
	Theirs bool        `json:"theirs"` // the other player's side (the packet's trader_state byte is 1)
	Gold   uint32      `json:"gold"`   // copper
	Spell  uint32      `json:"spell,omitempty"`
	Items  []TradeItem `json:"items"`
}

// ParseTradeStatusExtended reads SMSG_TRADE_STATUS_EXTENDED: u8 trader state (1: the other
// player's side, 0: our own), u32 slot count twice, u32 gold, u32 spell, then per slot a u8
// index and 15 u32 (entry, display id, count, wrapped, gift creator guid, enchant, creator
// guid, charges, suffix factor, random property, lock id, max durability, durability); an empty
// slot is the index and 15 zero words.
func ParseTradeStatusExtended(body []byte) (TradeView, error) {
	r := packet.NewReader(body)
	v := TradeView{Theirs: r.U8() == 1, Items: []TradeItem{}}
	n := r.U32()
	r.U32()
	v.Gold, v.Spell = r.U32(), r.U32()
	if n > TradeSlotCount {
		return v, fmt.Errorf("SMSG_TRADE_STATUS_EXTENDED: %d slots", n)
	}
	for i := uint32(0); i < n && r.Err() == nil; i++ {
		it := TradeItem{Slot: r.U8(), Entry: r.U32(), DisplayID: r.U32(), Count: r.U32()}
		for j := 0; j < 12; j++ {
			r.U32()
		}
		if it.Entry != 0 {
			v.Items = append(v.Items, it)
		}
	}
	return v, done(r, "SMSG_TRADE_STATUS_EXTENDED")
}
