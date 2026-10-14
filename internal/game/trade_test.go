package game

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/packet"
)

func TestTradeBuilders(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  []byte
		want []byte
	}{
		{"initiate", BuildInitiateTrade(0x1122334455667788), []byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}},
		{"begin", BuildBeginTrade(), nil},
		{"accept", BuildAcceptTrade(), []byte{0, 0, 0, 0}},
		{"unaccept", BuildUnacceptTrade(), nil},
		{"cancel", BuildCancelTrade(), nil},
		{"set item", BuildSetTradeItem(2, 255, 30), []byte{2, 255, 30}},
		{"clear item", BuildClearTradeItem(3), []byte{3}},
		{"set gold", BuildSetTradeGold(0x010203), []byte{3, 2, 1, 0}},
		{"split", BuildSplitItem(255, 30, 19, 2, 5), []byte{255, 30, 19, 2, 5}},
	} {
		if string(tc.got) != string(tc.want) {
			t.Errorf("%s: % X, want % X", tc.name, tc.got, tc.want)
		}
	}
}

func TestParseTradeStatus(t *testing.T) {
	// The bodies WorldSession::SendTradeStatus writes (TradeHandler.cpp:36-70).
	m, err := ParseTradeStatus(packet.NewWriter().U32(1).GUID(0x99).Bytes())
	if err != nil || m != (TradeStatusMsg{Status: TradeBegin, Other: 0x99}) {
		t.Fatalf("%+v %v", m, err)
	}
	for _, s := range []TradeStatus{TradeOpenWindow, TradeAccept, TradeBackToTrade, TradeComplete, TradeCancelled, TradeTargetTooFar} {
		if m, err := ParseTradeStatus(packet.NewWriter().U32(uint32(s)).Bytes()); err != nil || m.Status != s {
			t.Fatalf("%v: %+v %v", s, m, err)
		}
	}
	// Close window carries padding (u32, u8, u32); only conjured a u8.
	if m, err := ParseTradeStatus(packet.NewWriter().U32(12).U32(0).U8(0).U32(0).Bytes()); err != nil || m.Status != TradeCloseWindow {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := ParseTradeStatus([]byte{1, 0}); err == nil {
		t.Fatal("short status")
	}
	if _, err := ParseTradeStatus(packet.NewWriter().U32(1).Bytes()); err == nil {
		t.Fatal("begin without a guid")
	}
}

func TestTradeStatusNamesAndEnds(t *testing.T) {
	for s, want := range map[TradeStatus]string{TradeBusy: "busy", TradeBegin: "begin", TradeOpenWindow: "open", TradeAccept: "accepted",
		TradeBackToTrade: "back_to_trade", TradeComplete: "complete", TradeTargetTooFar: "too_far", TradeOnlyConjured: "only_conjured", 99: "status_99"} {
		if s.Name() != want {
			t.Errorf("%d: %s", s, s.Name())
		}
	}
	for s := TradeBusy; s <= TradeOnlyConjured; s++ {
		open := s == TradeBegin || s == TradeOpenWindow || s == TradeAccept || s == TradeBackToTrade
		if s.Ends() == open {
			t.Errorf("%s: Ends() = %v", s.Name(), s.Ends())
		}
	}
}

// A hand-written SMSG_TRADE_STATUS_EXTENDED (WorldSession::SendUpdateTrade, TradeHandler.cpp:84-132): two items, in slots
// 0 and 3, among seven slots.
func TestParseTradeStatusExtended(t *testing.T) {
	w := packet.NewWriter().U8(1).U32(7).U32(7).U32(250).U32(0)
	for i := uint8(0); i < 7; i++ {
		w.U8(i)
		switch i {
		case 0, 3:
			w.U32(1179 + uint32(i)).U32(555).U32(5 + uint32(i)).U32(0).U64(0).U32(0).U64(0).U32(0).U32(0).U32(0).U32(0).U32(0).U32(0)
		default:
			for j := 0; j < 15; j++ {
				w.U32(0)
			}
		}
	}
	v, err := ParseTradeStatusExtended(w.Bytes())
	want := TradeView{Theirs: true, Gold: 250, Items: []TradeItem{{Slot: 0, Entry: 1179, DisplayID: 555, Count: 5}, {Slot: 3, Entry: 1182, DisplayID: 555, Count: 8}}}
	if err != nil || !reflect.DeepEqual(v, want) {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := ParseTradeStatusExtended(w.Bytes()[:30]); err == nil {
		t.Fatal("short packet")
	}
	if _, err := ParseTradeStatusExtended(append(w.Bytes(), 0)); err == nil || !strings.Contains(err.Error(), "unread") {
		t.Fatalf("trailing byte: %v", err)
	}
	if _, err := ParseTradeStatusExtended(packet.NewWriter().U8(0).U32(50).U32(50).U32(0).U32(0).Bytes()); err == nil {
		t.Fatal("50 slots")
	}
}
