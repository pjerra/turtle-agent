package gametest

import (
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// TradeStatus is WorldSession::SendTradeStatus (TradeHandler.cpp:36-70 on upstream/1181dev
// 5784fb0): the status, and for a begin the guid of the player who started the trade.
func TradeStatus(s game.TradeStatus, other packet.GUID) []byte {
	w := packet.NewWriter().U32(uint32(s))
	switch s {
	case game.TradeBegin:
		w.GUID(other)
	case game.TradeCloseWindow:
		w.U32(0).U8(0).U32(0)
	case game.TradeOnlyConjured:
		w.U8(0)
	}
	return w.Bytes()
}

// TradeSide is one filled trade slot for TradeExtended.
type TradeSide struct {
	Slot         uint8
	Entry, Count uint32
}

// TradeExtended is WorldSession::SendUpdateTrade (TradeHandler.cpp:84-132): all seven slots,
// the ones in items filled, the rest as the index and 15 zero words.
func TradeExtended(theirs bool, gold uint32, items ...TradeSide) []byte {
	w := packet.NewWriter()
	if theirs {
		w.U8(1)
	} else {
		w.U8(0)
	}
	w.U32(game.TradeSlotCount).U32(game.TradeSlotCount).U32(gold).U32(0)
	for i := uint8(0); i < game.TradeSlotCount; i++ {
		w.U8(i)
		var it *TradeSide
		for j := range items {
			if items[j].Slot == i {
				it = &items[j]
			}
		}
		if it == nil {
			for j := 0; j < 15; j++ {
				w.U32(0)
			}
			continue
		}
		w.U32(it.Entry).U32(1234).U32(it.Count)     // entry, display id, count
		w.U32(0).U64(0)                             // wrapped, gift creator
		w.U32(0).U64(0)                             // enchant, creator
		w.U32(0).U32(0).U32(0).U32(0).U32(0).U32(0) // charges, suffix, random, lock, max durability, durability
	}
	return w.Bytes()
}
