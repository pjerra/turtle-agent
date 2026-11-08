package session

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

const (
	waterEntry, breadEntry = 1179, 4540
	waterGUID              = packet.GUID(0x4000000000000021)
	breadGUIDT             = packet.GUID(0x4000000000000022)
	splitGUID              = packet.GUID(0x4000000000000023)
	botGUID                = packet.GUID(0x99)
)

func TestPlanTrade(t *testing.T) {
	st := func(entry, count uint32, bag, slot uint8) objects.Item {
		return objects.Item{Bag: bag, Slot: slot, GUID: packet.GUID(0x4000000000000000 + uint64(bag)<<8 + uint64(slot)), Entry: entry, Count: count}
	}
	free := [][2]uint8{{255, 23}, {255, 24}, {255, 25}}
	water20, water20b, water7, bread4 := st(waterEntry, 20, 255, 30), st(waterEntry, 20, 19, 1), st(waterEntry, 7, 255, 31), st(breadEntry, 4, 255, 32)
	type want struct {
		item  objects.Item // the stack in the window or split from
		split bool
		dst   [2]uint8
		count uint32
	}
	for _, tc := range []struct {
		name  string
		items []objects.Item
		ask   []TradeItemReq
		want  []want
		err   string
	}{
		{"exact stack goes whole", []objects.Item{water20, bread4}, []TradeItemReq{{breadEntry, 4}}, []want{{bread4, false, [2]uint8{}, 4}}, ""},
		{"count 0 is one", []objects.Item{bread4}, []TradeItemReq{{breadEntry, 0}}, []want{{bread4, true, [2]uint8{255, 23}, 1}}, ""},
		{"part of a stack splits", []objects.Item{water20, bread4}, []TradeItemReq{{waterEntry, 5}}, []want{{water20, true, [2]uint8{255, 23}, 5}}, ""},
		{"the smallest stack that has enough is split", []objects.Item{water20, water20b, water7}, []TradeItemReq{{waterEntry, 5}}, []want{{water7, true, [2]uint8{255, 23}, 5}}, ""},
		{"an exact stack beats a split", []objects.Item{water20, water7}, []TradeItemReq{{waterEntry, 7}}, []want{{water7, false, [2]uint8{}, 7}}, ""},
		{"several stacks: whole ones, then the last piece", []objects.Item{water20, water20b, water7}, []TradeItemReq{{waterEntry, 30}},
			[]want{{water20b, false, [2]uint8{}, 20}, {water20, true, [2]uint8{255, 23}, 10}}, ""},
		{"two asks for one entry add up", []objects.Item{water20, water7}, []TradeItemReq{{waterEntry, 3}, {waterEntry, 4}}, []want{{water7, false, [2]uint8{}, 7}}, ""},
		{"too few", []objects.Item{water20}, []TradeItemReq{{waterEntry, 21}}, nil, "have 20 of item 1179"},
		{"absent", []objects.Item{water20}, []TradeItemReq{{breadEntry, 1}}, nil, "have 0 of item 4540"},
		{"no entry", []objects.Item{water20}, []TradeItemReq{{0, 1}}, nil, "needs its entry"},
		{"an item without its create block is not counted", []objects.Item{{Bag: 255, Slot: 30, GUID: 5}}, []TradeItemReq{{waterEntry, 1}}, nil, "have 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planTrade(tc.items, free, tc.ask)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var have []want
			for _, s := range got {
				it := s.Item
				if s.Split {
					it = s.Src
				}
				have = append(have, want{it, s.Split, s.Dst, s.Count})
			}
			if !reflect.DeepEqual(have, tc.want) {
				t.Fatalf("plan %+v\nwant %+v", have, tc.want)
			}
		})
	}

	t.Run("no free slot for a split", func(t *testing.T) {
		if _, err := planTrade([]objects.Item{water20}, nil, []TradeItemReq{{waterEntry, 5}}); err == nil || !strings.Contains(err.Error(), "no free bag slot") {
			t.Fatal(err)
		}
	})
	t.Run("the window has six slots", func(t *testing.T) {
		var items []objects.Item
		var ask []TradeItemReq
		for i := uint32(1); i <= 7; i++ {
			items = append(items, st(i, 1, 255, uint8(23+i)))
			ask = append(ask, TradeItemReq{i, 1})
		}
		if _, err := planTrade(items, free, ask); err == nil || !strings.Contains(err.Error(), "takes 7 trade slots") {
			t.Fatal(err)
		}
		if got, err := planTrade(items, free, ask[:6]); err != nil || len(got) != 6 {
			t.Fatalf("%d %v", len(got), err)
		}
	})
}

// tradeRig logs in a player with water (20, backpack slot 30) and bread (4, slot 31), 500 copper, and a nearby player
// "Gorm" (guid 0x99) dist yards away.
func tradeRig(t *testing.T, dist float32) *harness {
	t.Helper()
	h := start(t, Options{TradeSettle: 20 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{30: waterGUID, 31: breadGUIDT},
		map[packet.GUID][2]uint32{waterGUID: {waterEntry, 20}, breadGUIDT: {breadEntry, 4}}))
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.PlayerCoinage: 500}))
	h.send(opcode.SMSGUpdateObject, unitCreate(botGUID, objects.TypePlayer, me.X+dist, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 500, objects.UnitMaxHealth: 500}))
	h.send(opcode.SMSGNameQueryResponse, nameResponse(botGUID, "Gorm"))
	waitFor(t, func() bool {
		return len(h.s.Inventory()) == 2 && h.s.store.Field(me.GUID, objects.PlayerCoinage) == 500 && len(h.s.Units(objects.Filter{Name: "gorm"})) == 1
	})
	return h
}

type tradeOut struct {
	res TradeResult
	err error
}

func startTrade(h *harness, req TradeRequest) chan tradeOut {
	done := make(chan tradeOut, 1)
	go func() {
		r, err := h.s.Trade(context.Background(), req)
		done <- tradeOut{r, err}
	}()
	return done
}

func (h *harness) tradeStatus(s game.TradeStatus) {
	h.send(opcode.SMSGTradeStatus, gametest.TradeStatus(s, 0))
}

// replaceSlot is the server's update for a backpack slot (guid 0 empties it), with the item's create block when it is new.
func replaceSlot(h *harness, slot uint8, g packet.GUID, entry, count uint32) {
	h.t.Helper()
	w := objtest.Header(1)
	if g != 0 {
		w = objtest.Header(2)
		w.U8(uint8(objects.BlockCreate2)).PackedGUID(g).U8(objects.TypeItem).U8(0x10).U32(1)
		objtest.Values(w, 0x30, map[uint16]uint32{objects.FieldEntry: entry, objects.ItemStackCount: count})
	}
	w.U8(uint8(objects.BlockValues)).PackedGUID(me.GUID)
	objtest.Values(w, playerEnd, map[uint16]uint32{objects.PlayerInvSlotHead + 2*uint16(slot): uint32(g), objects.PlayerInvSlotHead + 2*uint16(slot) + 1: uint32(g >> 32)})
	h.send(opcode.SMSGUpdateObject, w.Bytes())
}

func TestTradeHandsOverASplitStackWholeStackAndGold(t *testing.T) {
	h := tradeRig(t, 3)
	done := startTrade(h, TradeRequest{Name: "gorm", Items: []TradeItemReq{{waterEntry, 5}, {breadEntry, 4}}, Gold: 150})

	if p := h.expect(opcode.CMSGInitiateTrade); string(p.Body) != string(game.BuildInitiateTrade(botGUID)) {
		t.Fatalf("initiate % X", p.Body)
	}
	// The bot answers with CMSG_BEGIN_TRADE: both clients get the open status.
	h.tradeStatus(game.TradeOpenWindow)
	// 5 of the 20 water go to the first free slot, 23.
	if p := h.expect(opcode.CMSGSplitItem); string(p.Body) != string(game.BuildSplitItem(255, 30, 255, 23, 5)) {
		t.Fatalf("split % X", p.Body)
	}
	replaceSlot(h, 23, splitGUID, waterEntry, 5)
	for i, want := range [][]byte{game.BuildSetTradeItem(0, 255, 23), game.BuildSetTradeItem(1, 255, 31), game.BuildSetTradeGold(150)} {
		op := opcode.CMSGSetTradeItem
		if i == 2 {
			op = opcode.CMSGSetTradeGold
		}
		if p := h.expect(op); string(p.Body) != string(want) {
			t.Fatalf("set %d: % X, want % X", i, p.Body, want)
		}
	}
	for i := 0; i < 3; i++ {
		h.tradeStatus(game.TradeBackToTrade) // the core's reply to each change
	}
	if p := h.expect(opcode.CMSGAcceptTrade); string(p.Body) != string(game.BuildAcceptTrade()) {
		t.Fatalf("accept % X", p.Body)
	}
	// The bot adds a gift of its own, accepts, and the trade completes; the items leave the bags.
	h.send(opcode.SMSGTradeStatusExtended, gametest.TradeExtended(true, 25, gametest.TradeSide{Slot: 0, Entry: 2512, Count: 3}))
	h.tradeStatus(game.TradeAccept)
	replaceSlot(h, 23, 0, 0, 0)
	replaceSlot(h, 31, 0, 0, 0)
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.PlayerCoinage: 350}))
	h.tradeStatus(game.TradeComplete)

	o := <-done
	if o.err != nil {
		t.Fatal(o.err)
	}
	want := TradeResult{Result: "completed", Status: "complete", Partner: botGUID, PartnerName: "Gorm",
		Gave:     []TradeGave{{Entry: waterEntry, Count: 5, Split: true}, {Entry: breadEntry, Count: 4}},
		Gold:     150,
		Received: []game.TradeItem{{Slot: 0, Entry: 2512, DisplayID: 1234, Count: 3}}, ReceivedGold: 25,
		PartnerAccepted: true, Verified: true}
	if !reflect.DeepEqual(o.res, want) {
		t.Fatalf("%+v\nwant %+v", o.res, want)
	}
	// Every status is an event, the other player the source; a complete trade has no cancel behind it.
	var got []string
	for _, e := range ofKind(h.s.Log(), events.KindTradeStatus) {
		got = append(got, e.Data["status"].(string))
		if e.SourceGUID != botGUID {
			t.Errorf("source %v", e.SourceGUID)
		}
	}
	if strings.Join(got, ",") != "open,back_to_trade,back_to_trade,back_to_trade,accepted,complete" {
		t.Fatalf("events %v", got)
	}
	if p, err := h.srv.Expect(opcode.CMSGCancelTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("cancel after a complete trade % X", p.Body)
	}
	if h.s.trade.active {
		t.Fatal("trade still active")
	}
}

func TestTradeByGuidWithoutAnythingToSplit(t *testing.T) {
	h := tradeRig(t, 9.5)
	done := startTrade(h, TradeRequest{To: botGUID, Items: []TradeItemReq{{breadEntry, 4}}})
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	if p := h.expect(opcode.CMSGSetTradeItem); string(p.Body) != string(game.BuildSetTradeItem(0, 255, 31)) {
		t.Fatalf("% X", p.Body)
	}
	h.expect(opcode.CMSGAcceptTrade)
	replaceSlot(h, 31, 0, 0, 0)
	h.tradeStatus(game.TradeComplete)
	if o := <-done; o.err != nil || o.res.Result != "completed" || !o.res.Verified || o.res.Gold != 0 {
		t.Fatalf("%+v %v", o.res, o.err)
	}
	if p, err := h.srv.Expect(opcode.CMSGSplitItem, 50*time.Millisecond); err == nil {
		t.Fatalf("split % X", p.Body)
	}
}

// The core refuses an accept within a second of the last change with back_to_trade; Trade accepts again.
func TestTradeAcceptsAgainAfterBackToTrade(t *testing.T) {
	h := tradeRig(t, 3)
	done := startTrade(h, TradeRequest{Name: "Gorm", Gold: 10})
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	h.expect(opcode.CMSGSetTradeGold)
	h.expect(opcode.CMSGAcceptTrade)
	h.tradeStatus(game.TradeBackToTrade)
	h.expect(opcode.CMSGAcceptTrade)
	h.tradeStatus(game.TradeComplete)
	o := <-done
	if o.err != nil || o.res.Result != "completed" {
		t.Fatalf("%+v %v", o.res, o.err)
	}
	if o.res.Verified || !strings.Contains(o.res.Detail, "did not show the change") {
		t.Fatalf("the purse never dropped, so the result is unverified: %+v", o.res)
	}
}

func TestTradeGivesUpAfterRepeatedBackToTrade(t *testing.T) {
	h := tradeRig(t, 3)
	done := startTrade(h, TradeRequest{Name: "Gorm", Gold: 10})
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	h.expect(opcode.CMSGSetTradeGold)
	for i := 0; i < tradeMaxAccepts; i++ {
		h.expect(opcode.CMSGAcceptTrade)
		h.tradeStatus(game.TradeBackToTrade)
	}
	h.expect(opcode.CMSGCancelTrade)
	h.tradeStatus(game.TradeCancelled)
	o := <-done
	var tf *TradeFailed
	if !errors.As(o.err, &tf) || o.res.Result != "cancelled" || !strings.Contains(o.res.Detail, "back to trade 4 times") {
		t.Fatalf("%+v %v", o.res, o.err)
	}
}

func TestTradeTimesOutAndCancels(t *testing.T) {
	h := tradeRig(t, 3)
	// The window never opens: nobody answered the request.
	done := startTrade(h, TradeRequest{Name: "Gorm", Gold: 10, OpenWait: 100 * time.Millisecond})
	h.expect(opcode.CMSGInitiateTrade)
	go func() { h.expect(opcode.CMSGCancelTrade); h.tradeStatus(game.TradeCancelled) }()
	o := <-done
	var tf *TradeFailed
	if !errors.As(o.err, &tf) || o.res.Result != "timeout" || !strings.Contains(o.res.Detail, "did not open") || o.res.PartnerName != "Gorm" {
		t.Fatalf("%+v %v", o.res, o.err)
	}
	if len(o.res.Gave) != 0 {
		t.Fatalf("nothing was put in the window: %+v", o.res.Gave)
	}

	// The window opens but the other side never accepts: the cancel goes out and the bags are as they were.
	done = startTrade(h, TradeRequest{Name: "Gorm", Items: []TradeItemReq{{breadEntry, 4}}, Wait: 150 * time.Millisecond})
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	h.expect(opcode.CMSGSetTradeItem)
	h.expect(opcode.CMSGAcceptTrade)
	go func() { h.expect(opcode.CMSGCancelTrade); h.tradeStatus(game.TradeCancelled) }()
	o = <-done
	if !errors.As(o.err, &tf) || o.res.Result != "timeout" || !strings.Contains(o.res.Detail, "did not accept") || o.res.PartnerAccepted ||
		len(o.res.Gave) != 1 || len(h.s.Inventory()) != 2 {
		t.Fatalf("%+v %v", o.res, o.err)
	}
}

func TestTradeRefusedAndCancelledByTheServer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status game.TradeStatus
		result string
	}{
		{"busy", game.TradeBusy, "busy"},
		{"no target", game.TradeNoTarget, "no_target"},
		{"too far", game.TradeTargetTooFar, "too_far"},
		{"ignored", game.TradeIgnoreYou, "ignored"},
		{"wrong faction", game.TradeWrongFaction, "wrong_faction"},
		{"target dead", game.TradeTargetDead, "target_dead"},
		{"closed", game.TradeCloseWindow, "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tradeRig(t, 3)
			done := startTrade(h, TradeRequest{Name: "Gorm", Gold: 10})
			h.expect(opcode.CMSGInitiateTrade)
			h.send(opcode.SMSGTradeStatus, gametest.TradeStatus(tc.status, 0)) // instead of opening
			o := <-done
			var tf *TradeFailed
			if !errors.As(o.err, &tf) || o.res.Result != tc.result {
				t.Fatalf("%+v %v", o.res, o.err)
			}
			// The window is closed already: no CMSG_CANCEL_TRADE.
			if p, err := h.srv.Expect(opcode.CMSGCancelTrade, 50*time.Millisecond); err == nil {
				t.Fatalf("cancel % X", p.Body)
			}
		})
	}
}

// The core cancels the trade when it refuses an item (a soulbound one) while the agent fills the window.
func TestTradeCancelledWhileFillingTheWindow(t *testing.T) {
	h := tradeRig(t, 3)
	done := startTrade(h, TradeRequest{Name: "Gorm", Items: []TradeItemReq{{breadEntry, 4}}})
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	h.expect(opcode.CMSGSetTradeItem)
	h.tradeStatus(game.TradeCancelled)
	o := <-done
	if o.err == nil || o.res.Result != "cancelled" || !strings.Contains(o.res.Detail, "soulbound") {
		t.Fatalf("%+v %v", o.res, o.err)
	}
}

func TestTradeChecksBeforeItTouchesAnything(t *testing.T) {
	h := tradeRig(t, 3)
	far := tradeRigPlayer(h, 0x98, "Faraway", 30)
	dead := tradeRigPlayer(h, 0x97, "Deadguy", 2)
	h.send(opcode.SMSGUpdateObject, valuesUpdate(dead, map[uint16]uint32{objects.UnitHealth: 0}))
	waitFor(t, func() bool { u, _ := h.s.store.Get(dead, 0, 0, 0); return u.Dead })
	for _, tc := range []struct {
		name string
		req  TradeRequest
		want string
	}{
		{"nothing to give", TradeRequest{Name: "Gorm"}, "needs items or gold"},
		{"no player", TradeRequest{Gold: 1}, "needs a player"},
		{"unknown name", TradeRequest{Name: "Nobody", Gold: 1}, `no player named "Nobody"`},
		{"too far by name", TradeRequest{Name: "Faraway", Gold: 1}, "30.0 yd away, past trade range"},
		{"too far by guid", TradeRequest{To: far, Gold: 1}, "30.0 yd away, past trade range"},
		{"dead", TradeRequest{Name: "Deadguy", Gold: 1}, "is dead"},
		{"a creature is no one to trade with", TradeRequest{To: wolfGUID, Gold: 1}, "not a player in view"},
		{"yourself", TradeRequest{To: me.GUID, Gold: 1}, "cannot trade with yourself"},
		{"too much gold", TradeRequest{Name: "Gorm", Gold: 501}, "501 copper wanted, the purse holds 500"},
		{"too many items", TradeRequest{Name: "Gorm", Items: []TradeItemReq{{breadEntry, 5}}}, "have 4 of item 4540"},
		{"item not carried", TradeRequest{Name: "Gorm", Items: []TradeItemReq{{9999, 1}}}, "have 0 of item 9999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := <-startTrade(h, tc.req)
			var tf *TradeFailed
			if o.err == nil || errors.As(o.err, &tf) || !strings.Contains(o.err.Error(), tc.want) {
				t.Fatalf("%+v %v", o.res, o.err)
			}
		})
	}
	if p, err := h.srv.Expect(opcode.CMSGInitiateTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("a trade was started: % X", p.Body)
	}
	// A part of a name that fits two players in view is ambiguous; the full name is not.
	tradeRigPlayer(h, 0x96, "Gormley", 4)
	o := <-startTrade(h, TradeRequest{Name: "gor", Gold: 1})
	if o.err == nil || !strings.Contains(o.err.Error(), "matches 2 players") {
		t.Fatalf("%+v %v", o.res, o.err)
	}
}

func tradeRigPlayer(h *harness, g packet.GUID, name string, dist float32) packet.GUID {
	h.t.Helper()
	h.send(opcode.SMSGUpdateObject, unitCreate(g, objects.TypePlayer, me.X+dist, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 500, objects.UnitMaxHealth: 500}))
	h.send(opcode.SMSGNameQueryResponse, nameResponse(g, name))
	waitFor(h.t, func() bool { return len(h.s.Units(objects.Filter{Name: strings.ToLower(name)})) >= 1 })
	return g
}

func TestTradeParseError(t *testing.T) {
	h := tradeRig(t, 3)
	h.send(opcode.SMSGTradeStatus, []byte{1, 0}) // a short body is a parse error, not a crash
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindParseError)) == 1 })
}

// A trade the other side starts is open on the core for both players, and a player with one open cannot start another:
// the agent declines it with CMSG_CANCEL_TRADE, and a trade of its own gets through afterwards.
func TestTradeDeclinesAnIncomingTrade(t *testing.T) {
	h := tradeRig(t, 3)
	h.send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeBegin, botGUID))
	h.expect(opcode.CMSGCancelTrade)
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindTradeStatus)) == 1 })
	e := ofKind(h.s.Log(), events.KindTradeStatus)[0]
	if e.Data["status"] != "begin" || e.Data["declined"] != true || e.SourceGUID != botGUID || !strings.Contains(e.Text, "Gorm") {
		t.Fatalf("%+v", e)
	}
	if p, err := h.srv.Expect(opcode.CMSGBeginTrade, 50*time.Millisecond); err == nil {
		t.Fatalf("answered % X", p.Body)
	}
	// The core's cancel reply has not come yet when our own trade starts: Trade cancels again, waits for the reply,
	// and only then initiates; the reply is not mistaken for the end of the new trade.
	done := startTrade(h, TradeRequest{Name: "Gorm", Gold: 10})
	h.expect(opcode.CMSGCancelTrade)
	if p, err := h.srv.Expect(opcode.CMSGInitiateTrade, 50*time.Millisecond); err == nil {
		t.Fatalf("initiated before the old trade closed: % X", p.Body)
	}
	h.tradeStatus(game.TradeCancelled)
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	h.expect(opcode.CMSGSetTradeGold)
	h.expect(opcode.CMSGAcceptTrade)
	h.tradeStatus(game.TradeComplete)
	if o := <-done; o.err != nil || o.res.Result != "completed" {
		t.Fatalf("%+v %v", o.res, o.err)
	}
	// With nothing pending, a begin while our own trade runs is only logged.
	done = startTrade(h, TradeRequest{Name: "Gorm", Gold: 10, OpenWait: 100 * time.Millisecond})
	h.expect(opcode.CMSGInitiateTrade)
	h.send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeBegin, botGUID))
	go func() { h.expect(opcode.CMSGCancelTrade); h.tradeStatus(game.TradeCancelled) }()
	if o := <-done; o.res.Result != "timeout" {
		t.Fatalf("%+v %v", o.res, o.err)
	}
}

// The purse check behind verified allows for gold the other side put in the window.
func TestTradeVerifiedCountsGoldReceived(t *testing.T) {
	h := tradeRig(t, 3)
	done := startTrade(h, TradeRequest{Name: "Gorm", Gold: 100})
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	h.expect(opcode.CMSGSetTradeGold)
	h.expect(opcode.CMSGAcceptTrade)
	h.send(opcode.SMSGTradeStatusExtended, gametest.TradeExtended(true, 30))
	h.tradeStatus(game.TradeAccept)
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.PlayerCoinage: 430})) // 500 - 100 + 30
	h.tradeStatus(game.TradeComplete)
	if o := <-done; o.err != nil || !o.res.Verified || o.res.ReceivedGold != 30 {
		t.Fatalf("%+v %v", o.res, o.err)
	}
}

// The split goes to a slot that is free when it is sent, not the one the plan saw.
func TestTradeSplitPicksTheSlotAtSplitTime(t *testing.T) {
	h := tradeRig(t, 3)
	done := startTrade(h, TradeRequest{Name: "Gorm", Items: []TradeItemReq{{waterEntry, 5}}})
	h.expect(opcode.CMSGInitiateTrade)
	// Something lands in slot 23, the planned destination, before the window opens.
	replaceSlot(h, 23, packet.GUID(0x4000000000000030), 2589, 1)
	waitFor(t, func() bool { _, ok := h.s.store.ItemAt(me.GUID, 255, 23); return ok })
	h.tradeStatus(game.TradeOpenWindow)
	if p := h.expect(opcode.CMSGSplitItem); string(p.Body) != string(game.BuildSplitItem(255, 30, 255, 24, 5)) {
		t.Fatalf("split % X", p.Body)
	}
	replaceSlot(h, 24, splitGUID, waterEntry, 5)
	h.expect(opcode.CMSGSetTradeItem)
	h.expect(opcode.CMSGAcceptTrade)
	h.tradeStatus(game.TradeCancelled)
	<-done
}

// ---- step trades (trade_open, trade_expect, trade_set_item, trade_accept, trade_cancel, trade_status, cast on a trade slot) ----

const (
	bracersEntry = 1420
	bracersGUID  = packet.GUID(0x4000000000000041)
	enchantSpell = 7418
)

type stepOut struct {
	st  TradeStepState
	err error
}

func startExpect(h *harness, from packet.GUID, name string, wait time.Duration) chan stepOut {
	done := make(chan stepOut, 1)
	go func() {
		st, err := h.s.TradeExpect(context.Background(), from, name, wait)
		done <- stepOut{st, err}
	}()
	return done
}

func startOpen(h *harness, to packet.GUID, name string) chan stepOut {
	done := make(chan stepOut, 1)
	go func() {
		st, err := h.s.TradeOpen(context.Background(), to, name, 0)
		done <- stepOut{st, err}
	}()
	return done
}

// armed waits until trade_expect has armed the accept.
func armed(t *testing.T, h *harness) {
	t.Helper()
	waitFor(t, func() bool { return h.s.TradeStatus().ArmedFrom != "" })
}

func (h *harness) begin(from packet.GUID) {
	h.send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeBegin, from))
}

// openedStep opens a step trade with Gorm and returns once the window is open; nothing is sent after that.
func openedStep(t *testing.T) *harness {
	t.Helper()
	h := tradeRig(t, 3)
	replaceSlot(h, 23, bracersGUID, bracersEntry, 1)
	waitFor(t, func() bool { _, ok := h.s.store.ItemAt(me.GUID, 255, 23); return ok })
	done := startOpen(h, botGUID, "")
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	if o := <-done; o.err != nil || !o.st.Open || !o.st.Active || !o.st.Step {
		t.Fatalf("%+v %v", o.st, o.err)
	}
	return h
}

func TestTradeExpectAcceptsArmedPlayer(t *testing.T) {
	h := tradeRig(t, 3)
	done := startExpect(h, botGUID, "", 5*time.Second)
	armed(t, h)
	h.begin(botGUID)
	if p := h.expect(opcode.CMSGBeginTrade); len(p.Body) != 0 {
		t.Fatalf("begin trade % X", p.Body)
	}
	h.tradeStatus(game.TradeOpenWindow)
	o := <-done
	if o.err != nil || !o.st.Open || !o.st.Active || !o.st.Step || o.st.Partner != botGUID || o.st.ArmedFrom != "" {
		t.Fatalf("%+v %v", o.st, o.err)
	}
	e := ofKind(h.s.Log(), events.KindTradeStatus)[0]
	if e.Data["status"] != "begin" || e.Data["accepted"] != true || e.Data["declined"] != nil {
		t.Fatalf("%+v", e)
	}
	if p, err := h.srv.Expect(opcode.CMSGCancelTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("cancelled % X", p.Body)
	}
}

func TestTradeExpectByNameAcceptsArmedPlayer(t *testing.T) {
	h := tradeRig(t, 3) // Gorm is in view: the name is resolved to his guid when the accept is armed
	done := startExpect(h, 0, "gorm", 5*time.Second)
	armed(t, h)
	if got := h.s.TradeStatus().ArmedFrom; got != botGUID.String() {
		t.Fatalf("armed for %q, want the resolved guid %s", got, botGUID)
	}
	h.begin(botGUID)
	h.expect(opcode.CMSGBeginTrade)
	h.tradeStatus(game.TradeOpenWindow)
	if o := <-done; o.err != nil || !o.st.Open || o.st.Partner != botGUID {
		t.Fatalf("%+v %v", o.st, o.err)
	}
}

// A player not in view yet is matched on the cached name when his begin arrives.
func TestTradeExpectByNameForAPlayerNotInView(t *testing.T) {
	const newcomer = packet.GUID(0x77)
	h := tradeRig(t, 3)
	done := startExpect(h, 0, "Newcomer", 5*time.Second)
	armed(t, h)
	if got := h.s.TradeStatus().ArmedFrom; got != "Newcomer" {
		t.Fatalf("armed for %q, want the name kept", got)
	}
	h.send(opcode.SMSGNameQueryResponse, nameResponse(newcomer, "Newcomer"))
	waitFor(t, func() bool {
		v, _ := h.s.do(context.Background(), func() (any, error) { _, ok := h.s.cachedName(newcomer); return ok, nil })
		return v == true
	})
	h.begin(newcomer)
	h.expect(opcode.CMSGBeginTrade)
	h.tradeStatus(game.TradeOpenWindow)
	if o := <-done; o.err != nil || o.st.Partner != newcomer {
		t.Fatalf("%+v %v", o.st, o.err)
	}
}

func TestTradeExpectStillDeclinesOthers(t *testing.T) {
	h := tradeRig(t, 3)
	other := tradeRigPlayer(h, 0x98, "Mallory", 4)
	done := startExpect(h, botGUID, "", 400*time.Millisecond)
	armed(t, h)
	h.begin(other)
	h.expect(opcode.CMSGCancelTrade)
	if p, err := h.srv.Expect(opcode.CMSGBeginTrade, 50*time.Millisecond); err == nil {
		t.Fatalf("accepted the wrong player % X", p.Body)
	}
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindTradeStatus)) == 1 })
	if e := ofKind(h.s.Log(), events.KindTradeStatus)[0]; e.Data["declined"] != true || e.Data["accepted"] != nil {
		t.Fatalf("%+v", e)
	}
	if o := <-done; o.err == nil {
		t.Fatalf("expected the wait to time out: %+v", o.st)
	}
}

func TestTradeExpectTimesOutAndDisarms(t *testing.T) {
	h := tradeRig(t, 3)
	o := <-startExpect(h, botGUID, "", 50*time.Millisecond)
	if o.err == nil || !strings.Contains(o.err.Error(), "no trade request from") || !strings.Contains(o.err.Error(), "within 50ms") {
		t.Fatalf("%+v %v", o.st, o.err)
	}
	if h.s.TradeStatus().ArmedFrom != "" {
		t.Fatal("still armed")
	}
	h.begin(botGUID)
	h.expect(opcode.CMSGCancelTrade)
	if p, err := h.srv.Expect(opcode.CMSGBeginTrade, 50*time.Millisecond); err == nil {
		t.Fatalf("accepted after the wait ended % X", p.Body)
	}
}

func TestTradeExpectRefusedWhileATradeIsActive(t *testing.T) {
	h := openedStep(t)
	if o := <-startExpect(h, botGUID, "", 50*time.Millisecond); o.err == nil || !strings.Contains(o.err.Error(), "another trade is running") {
		t.Fatalf("%+v %v", o.st, o.err)
	}
}

// Trade releases its window when it returns; a step trade must stay open for the calls that follow.
func TestTradeOpenKeepsWindowOpen(t *testing.T) {
	h := openedStep(t)
	if p, err := h.srv.Expect(opcode.CMSGCancelTrade, 300*time.Millisecond); err == nil {
		t.Fatalf("the window was cancelled % X", p.Body)
	}
	if st := h.s.TradeStatus(); !st.Active || !st.Open || st.PartnerName != "Gorm" {
		t.Fatalf("%+v", st)
	}
}

func TestTradeOpenFailsWhenRefusedOrUnanswered(t *testing.T) {
	h := tradeRig(t, 3)
	done := startOpen(h, 0, "Gorm")
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeBusy)
	var tf *TradeFailed
	if o := <-done; !errors.As(o.err, &tf) || tf.Result.Result != "busy" {
		t.Fatalf("%+v %v", o.st, o.err)
	}
	if st := h.s.TradeStatus(); st.Active || st.Step {
		t.Fatalf("%+v", st)
	}
	done = make(chan stepOut, 1)
	go func() {
		st, err := h.s.TradeOpen(context.Background(), botGUID, "", 100*time.Millisecond)
		done <- stepOut{st, err}
	}()
	h.expect(opcode.CMSGInitiateTrade)
	go func() { h.expect(opcode.CMSGCancelTrade); h.tradeStatus(game.TradeCancelled) }()
	if o := <-done; !errors.As(o.err, &tf) || tf.Result.Result != "timeout" {
		t.Fatalf("%+v %v", o.st, o.err)
	}
	// Neither failure leaves a trade behind: the next request goes out.
	done = startOpen(h, botGUID, "")
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	if o := <-done; o.err != nil || !o.st.Open {
		t.Fatalf("%+v %v", o.st, o.err)
	}
}

type setOut struct {
	r   TradeSetResult
	err error
}

func startSet(h *harness, slot uint8, entry uint32) chan setOut {
	ch := make(chan setOut, 1)
	go func() {
		r, err := h.s.TradeSetItem(context.Background(), slot, EquipRef{Entry: entry})
		ch <- setOut{r, err}
	}()
	return ch
}

// The core answers a set with SetAccepted(false), which sends back_to_trade to the setter (Player.cpp:529-541, 589-599); the
// setter gets no view of its own side (Update(true) goes to the trader only).
func TestTradeSetItemNonTradedSlot(t *testing.T) {
	h := openedStep(t)
	ch := startSet(h, 6, bracersEntry)
	if p := h.expect(opcode.CMSGSetTradeItem); !bytes.Equal(p.Body, []byte{0x06, 0xFF, 0x17}) {
		t.Fatalf("set trade item % X", p.Body)
	}
	h.tradeStatus(game.TradeBackToTrade)
	o := <-ch
	if o.err != nil || o.r != (TradeSetResult{TradeSlot: 6, Bag: 255, Slot: 23, GUID: bracersGUID, ItemEntry: bracersEntry}) {
		t.Fatalf("%+v %v", o.r, o.err)
	}
	// The core refuses an item (a soulbound one) by closing the window.
	ch = startSet(h, 5, bracersEntry)
	h.expect(opcode.CMSGSetTradeItem)
	h.tradeStatus(game.TradeCancelled)
	var tf *TradeFailed
	if o := <-ch; !errors.As(o.err, &tf) || tf.Result.Result != "cancelled" {
		t.Fatalf("%+v %v", o.r, o.err)
	}
	if h.s.TradeStatus().Active {
		t.Fatal("the closed window still counts as active")
	}
}

// Only the first back_to_trade after our own send counts: one that came before it (the cast's reply, a partner's change) does
// not. Setting the item the agent itself last set in that slot changes nothing and sends nothing; a record of it ends with the trade.
func TestTradeSetItemCountsOnlyBackToTradeAfterSendAndSkipsUnchanged(t *testing.T) {
	h := openedStep(t)
	h.tradeStatus(game.TradeBackToTrade) // an earlier change's reply, before our set
	waitFor(t, func() bool { return h.s.TradeStatus().Status == "back_to_trade" })
	ch := startSet(h, 6, bracersEntry)
	h.expect(opcode.CMSGSetTradeItem)
	select {
	case o := <-ch:
		t.Fatalf("returned on an older back_to_trade: %+v %v", o.r, o.err)
	case <-time.After(150 * time.Millisecond):
	}
	h.tradeStatus(game.TradeBackToTrade)
	if o := <-ch; o.err != nil || o.r.Unchanged || o.r.ItemEntry != bracersEntry {
		t.Fatalf("%+v %v", o.r, o.err)
	}
	// Same item, same slot, from the agent's own record: success without a packet or a wait.
	o := <-startSet(h, 6, bracersEntry)
	if o.err != nil || !o.r.Unchanged || o.r.TradeSlot != 6 || o.r.GUID != bracersGUID {
		t.Fatalf("%+v %v", o.r, o.err)
	}
	if p, err := h.srv.Expect(opcode.CMSGSetTradeItem, 100*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
	// The same item in another slot is a change.
	ch = startSet(h, 4, bracersEntry)
	if p := h.expect(opcode.CMSGSetTradeItem); !bytes.Equal(p.Body, []byte{0x04, 0xFF, 0x17}) {
		t.Fatalf("set % X", p.Body)
	}
	h.tradeStatus(game.TradeBackToTrade)
	if o := <-ch; o.err != nil || o.r.Unchanged {
		t.Fatalf("%+v %v", o.r, o.err)
	}
	// Cancelling ends the record: a new trade sends the set again.
	cancel := make(chan string, 1)
	go func() { st, _ := h.s.TradeCancel(context.Background()); cancel <- st }()
	h.expect(opcode.CMSGCancelTrade)
	h.tradeStatus(game.TradeCancelled)
	<-cancel
	done := startOpen(h, botGUID, "")
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	if o := <-done; o.err != nil {
		t.Fatal(o.err)
	}
	ch = startSet(h, 6, bracersEntry)
	h.expect(opcode.CMSGSetTradeItem)
	h.tradeStatus(game.TradeBackToTrade)
	if o := <-ch; o.err != nil || o.r.Unchanged {
		t.Fatalf("a fresh trade must send the set: %+v %v", o.r, o.err)
	}
}

func TestTradeSetItemNeedsOpenWindow(t *testing.T) {
	h := tradeRig(t, 3)
	replaceSlot(h, 23, bracersGUID, bracersEntry, 1)
	waitFor(t, func() bool { _, ok := h.s.store.ItemAt(me.GUID, 255, 23); return ok })
	if _, err := h.s.TradeSetItem(context.Background(), 6, EquipRef{Entry: bracersEntry}); err == nil || !strings.Contains(err.Error(), "no trade window is open") {
		t.Fatalf("err = %v", err)
	}
	if p, err := h.srv.Expect(opcode.CMSGSetTradeItem, 50*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
	h = openedStep(t)
	if _, err := h.s.TradeSetItem(context.Background(), 7, EquipRef{Entry: bracersEntry}); err == nil || !strings.Contains(err.Error(), "0-6") {
		t.Fatalf("slot 7: %v", err)
	}
	if _, err := h.s.TradeSetItem(context.Background(), 6, EquipRef{Entry: 9999}); err == nil || !strings.Contains(err.Error(), "item 9999") {
		t.Fatalf("missing item: %v", err)
	}
	if p, err := h.srv.Expect(opcode.CMSGSetTradeItem, 50*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
}

func TestCastOnTradeSlotNeedsWindow(t *testing.T) {
	h := tradeRig(t, 3)
	if err := h.s.CastOnTradeSlot(context.Background(), enchantSpell, 6); err == nil || !strings.Contains(err.Error(), "no trade window is open: trade_open or trade_expect first") {
		t.Fatalf("err = %v", err)
	}
	if p, err := h.srv.Expect(opcode.CMSGCastSpell, 50*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
	h = openedStep(t)
	if err := h.s.CastOnTradeSlot(context.Background(), enchantSpell, 6); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGCastSpell); !bytes.Equal(p.Body, game.BuildCastSpellOnTradeItem(enchantSpell, 6)) || !bytes.Equal(p.Body, []byte{0xFA, 0x1C, 0, 0, 0, 0x10, 0x01, 0x06}) {
		t.Fatalf("cast % X", p.Body)
	}
	// Slots 0-5 go out too: the core answers ITEM_NOT_READY, which a test can see. Slot 7 and spell 0 are refused here.
	if err := h.s.CastOnTradeSlot(context.Background(), enchantSpell, 3); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGCastSpell); !bytes.Equal(p.Body, []byte{0xFA, 0x1C, 0, 0, 0, 0x10, 0x01, 0x03}) {
		t.Fatalf("cast % X", p.Body)
	}
	if err := h.s.CastOnTradeSlot(context.Background(), enchantSpell, 7); err == nil {
		t.Fatal("slot 7")
	}
	if err := h.s.CastOnTradeSlot(context.Background(), 0, 6); err == nil {
		t.Fatal("spell 0")
	}
	if p, err := h.srv.Expect(opcode.CMSGCastSpell, 50*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
}

func TestTradeAcceptStepCompletes(t *testing.T) {
	h := openedStep(t)
	type accOut struct {
		r   TradeResult
		err error
	}
	ch := make(chan accOut, 1)
	go func() {
		r, err := h.s.TradeAccept(context.Background(), 5*time.Second)
		ch <- accOut{r, err}
	}()
	start := time.Now()
	if p := h.expect(opcode.CMSGAcceptTrade); !bytes.Equal(p.Body, game.BuildAcceptTrade()) {
		t.Fatalf("accept % X", p.Body)
	}
	if d := time.Since(start); d < 15*time.Millisecond {
		t.Fatalf("accepted after %s, before the settle delay", d)
	}
	h.tradeStatus(game.TradeAccept)
	h.tradeStatus(game.TradeComplete)
	o := <-ch
	if o.err != nil || o.r.Result != "completed" || o.r.Partner != botGUID || o.r.PartnerName != "Gorm" || !o.r.PartnerAccepted {
		t.Fatalf("%+v %v", o.r, o.err)
	}
	if st := h.s.TradeStatus(); st.Active || st.Step {
		t.Fatalf("%+v", st)
	}
	if p, err := h.srv.Expect(opcode.CMSGCancelTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("cancel after a complete trade % X", p.Body)
	}
}

// A back_to_trade after our accept means the window changed: the step accept does not accept again by itself, it returns
// result changed with the window so the caller sees what the partner did.
func TestTradeAcceptStepReturnsChangedOnBackToTrade(t *testing.T) {
	h := openedStep(t)
	type accOut struct {
		r   TradeResult
		err error
	}
	ch := make(chan accOut, 1)
	go func() {
		r, err := h.s.TradeAccept(context.Background(), 5*time.Second)
		ch <- accOut{r, err}
	}()
	h.expect(opcode.CMSGAcceptTrade)
	h.send(opcode.SMSGTradeStatusExtended, gametest.TradeExtended(true, 7, gametest.TradeSide{Slot: 0, Entry: 2512, Count: 3}))
	h.tradeStatus(game.TradeBackToTrade)
	o := <-ch
	if o.err != nil || o.r.Result != "changed" || o.r.TradeStatus == nil || o.r.TradeStatus.TheirGold != 7 || len(o.r.TradeStatus.TheirItems) != 1 || !o.r.TradeStatus.Open {
		t.Fatalf("%+v %v", o.r, o.err)
	}
	if p, err := h.srv.Expect(opcode.CMSGAcceptTrade, 150*time.Millisecond); err == nil {
		t.Fatalf("accepted again by itself % X", p.Body)
	}
	if p, err := h.srv.Expect(opcode.CMSGUnacceptTrade, 50*time.Millisecond); err == nil {
		t.Fatalf("unaccepted a window the core already un-accepted % X", p.Body)
	}
	if st := h.s.TradeStatus(); !st.Active || !st.Open {
		t.Fatalf("%+v", st)
	}
}

// A step trade whose partner does not accept keeps its window, but our accept must not stand on the core: it is withdrawn
// with CMSG_UNACCEPT_TRADE. The caller decides (trade_accept again or trade_cancel).
func TestTradeAcceptStepTimeoutKeepsWindow(t *testing.T) {
	h := openedStep(t)
	ch := make(chan error, 1)
	go func() { _, err := h.s.TradeAccept(context.Background(), 100*time.Millisecond); ch <- err }()
	h.expect(opcode.CMSGAcceptTrade)
	var tf *TradeFailed
	if err := <-ch; !errors.As(err, &tf) || tf.Result.Result != "timeout" {
		t.Fatalf("%v", err)
	}
	if p := h.expect(opcode.CMSGUnacceptTrade); len(p.Body) != 0 {
		t.Fatalf("unaccept % X", p.Body)
	}
	if st := h.s.TradeStatus(); !st.Active || !st.Open {
		t.Fatalf("%+v", st)
	}
	if p, err := h.srv.Expect(opcode.CMSGCancelTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("cancelled % X", p.Body)
	}
}

func TestTradeAcceptNeedsAStepTrade(t *testing.T) {
	h := tradeRig(t, 3)
	if _, err := h.s.TradeAccept(context.Background(), time.Second); err == nil || !strings.Contains(err.Error(), "no trade window is open") {
		t.Fatalf("err = %v", err)
	}
	if p, err := h.srv.Expect(opcode.CMSGAcceptTrade, 50*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
}

func TestTradeCancelStep(t *testing.T) {
	h := openedStep(t)
	ch := make(chan string, 1)
	go func() { st, _ := h.s.TradeCancel(context.Background()); ch <- st }()
	h.expect(opcode.CMSGCancelTrade)
	h.tradeStatus(game.TradeCancelled)
	if st := <-ch; st != "cancelled" {
		t.Fatalf("status %q", st)
	}
	if st := h.s.TradeStatus(); st.Active || st.Step || st.Open {
		t.Fatalf("%+v", st)
	}
	if st, err := h.s.TradeCancel(context.Background()); err != nil || st != "cancelled" {
		t.Fatalf("%q %v", st, err)
	}
	if p, err := h.srv.Expect(opcode.CMSGCancelTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("a second cancel went out % X", p.Body)
	}
}

// trader_state 0 is our own side of the window, 1 the partner's (SendUpdateTrade); the pending enchant rides in the spell word.
func TestTradeStatusShowsBothSides(t *testing.T) {
	h := openedStep(t)
	h.send(opcode.SMSGTradeStatusExtended, gametest.TradeExtendedSpell(false, 12, enchantSpell))
	h.send(opcode.SMSGTradeStatusExtended, gametest.TradeExtended(true, 25, gametest.TradeSide{Slot: 6, Entry: bracersEntry, Count: 1}))
	waitFor(t, func() bool { st := h.s.TradeStatus(); return st.MySpell == enchantSpell && len(st.TheirItems) == 1 })
	st := h.s.TradeStatus()
	if st.MyGold != 12 || st.TheirGold != 25 || st.TheirItems[0].Slot != 6 || st.TheirItems[0].Entry != bracersEntry || len(st.MyItems) != 0 || st.TheirSpell != 0 {
		t.Fatalf("%+v", st)
	}
	// With no trade the snapshot is empty and its lists are arrays.
	idle := tradeRig(t, 3).s.TradeStatus()
	if idle.Active || idle.TheirItems == nil || idle.MyItems == nil {
		t.Fatalf("%+v", idle)
	}
}

func TestOneShotTradeRefusedDuringStepTrade(t *testing.T) {
	h := openedStep(t)
	_, err := h.s.Trade(context.Background(), TradeRequest{Name: "Gorm", Gold: 10})
	var tf *TradeFailed
	if err == nil || errors.As(err, &tf) || !strings.Contains(err.Error(), "another trade is running") {
		t.Fatalf("err = %v", err)
	}
	if p, err := h.srv.Expect(opcode.CMSGCancelTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("the refused call cancelled the step trade % X", p.Body)
	}
}

// A stranger's request is declined and its closing status must not leak into the armed state: the armed player's request
// that follows starts a clean trade.
func TestTradeExpectSurvivesAStrangersClosingStatus(t *testing.T) {
	h := tradeRig(t, 3)
	stranger := tradeRigPlayer(h, 0x98, "Mallory", 4)
	done := startExpect(h, botGUID, "", 5*time.Second)
	armed(t, h)
	h.begin(stranger)
	h.expect(opcode.CMSGCancelTrade)
	h.tradeStatus(game.TradeCancelled) // the core's reply to the decline
	// The armed state is untouched by it: not ended, still armed.
	waitFor(t, func() bool { return h.s.TradeStatus().Status == "cancelled" })
	v, _ := h.s.do(context.Background(), func() (any, error) { return h.s.trade.ended || h.s.trade.open || h.s.trade.incoming != 0, nil })
	if v == true || h.s.TradeStatus().ArmedFrom == "" {
		t.Fatalf("the stranger's closing status reached the armed state: %+v", h.s.TradeStatus())
	}
	h.begin(botGUID)
	h.expect(opcode.CMSGBeginTrade)
	h.tradeStatus(game.TradeOpenWindow)
	o := <-done
	if o.err != nil || !o.st.Open || o.st.Partner != botGUID || o.st.Status != "open" {
		t.Fatalf("%+v %v", o.st, o.err)
	}
	if st := h.s.TradeStatus(); !st.Active || !st.Open || !st.Step || st.Partner != botGUID {
		t.Fatalf("%+v", st)
	}
	if p, err := h.srv.Expect(opcode.CMSGCancelTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("cancelled the armed trade % X", p.Body)
	}
}

// A step window the partner closed counts as released: a stranger's request is declined again and our own trade starts.
func TestStrangerDeclinedAfterAStepTradeEnded(t *testing.T) {
	h := openedStep(t)
	h.tradeStatus(game.TradeCancelled) // the partner closes the window; no step call released it
	waitFor(t, func() bool { return h.s.TradeStatus().Status == "cancelled" })
	stranger := tradeRigPlayer(h, 0x98, "Mallory", 4)
	h.begin(stranger)
	h.expect(opcode.CMSGCancelTrade)
	h.tradeStatus(game.TradeCancelled) // the core's reply to the decline
	if st := h.s.TradeStatus(); st.Active || st.Step || st.Open {
		t.Fatalf("%+v", st)
	}
	done := startOpen(h, botGUID, "")
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	if o := <-done; o.err != nil || !o.st.Open {
		t.Fatalf("%+v %v", o.st, o.err)
	}
}

// trade_expect arms on a full name only: a part of a name that fits a player in view is an error, not an arm that never fires.
func TestTradeExpectNeedsTheFullName(t *testing.T) {
	h := tradeRig(t, 3)
	o := <-startExpect(h, 0, "Gor", 200*time.Millisecond)
	if o.err == nil || !strings.Contains(o.err.Error(), `no player named "Gor"`) {
		t.Fatalf("%+v %v", o.st, o.err)
	}
	if h.s.TradeStatus().ArmedFrom != "" {
		t.Fatal("armed on a partial name")
	}
	// A partial name also fails when two players in view contain it.
	tradeRigPlayer(h, 0x96, "Gormley", 4)
	if o := <-startExpect(h, 0, "gor", 200*time.Millisecond); o.err == nil || !strings.Contains(o.err.Error(), `no player named "gor"`) {
		t.Fatalf("%+v %v", o.st, o.err)
	}
}

// A live trade_expect arm is not wiped by another trade call.
func TestLiveArmBlocksOtherTradeCalls(t *testing.T) {
	h := tradeRig(t, 3)
	done := startExpect(h, botGUID, "", 3*time.Second)
	armed(t, h)
	if o := <-startOpen(h, botGUID, ""); o.err == nil || !strings.Contains(o.err.Error(), "trade_expect is waiting") {
		t.Fatalf("trade_open: %+v %v", o.st, o.err)
	}
	if _, err := h.s.Trade(context.Background(), TradeRequest{Name: "Gorm", Gold: 10}); err == nil || !strings.Contains(err.Error(), "trade_expect is waiting") {
		t.Fatalf("trade: %v", err)
	}
	if o := <-startExpect(h, botGUID, "", 50*time.Millisecond); o.err == nil || !strings.Contains(o.err.Error(), "trade_expect is waiting") {
		t.Fatalf("trade_expect: %+v %v", o.st, o.err)
	}
	if p, err := h.srv.Expect(opcode.CMSGInitiateTrade, 50*time.Millisecond); err == nil {
		t.Fatalf("initiated % X", p.Body)
	}
	// The arm survived all three: the armed request is still taken.
	h.begin(botGUID)
	h.expect(opcode.CMSGBeginTrade)
	h.tradeStatus(game.TradeOpenWindow)
	if o := <-done; o.err != nil || !o.st.Open {
		t.Fatalf("%+v %v", o.st, o.err)
	}
}

// A one-shot trade is not a step window: a trade-slot cast is not sent during it.
func TestCastOnTradeSlotRefusedDuringOneShotTrade(t *testing.T) {
	h := tradeRig(t, 3)
	done := startTrade(h, TradeRequest{Name: "Gorm", Gold: 10})
	h.expect(opcode.CMSGInitiateTrade)
	h.tradeStatus(game.TradeOpenWindow)
	h.expect(opcode.CMSGSetTradeGold)
	if err := h.s.CastOnTradeSlot(context.Background(), enchantSpell, 6); err == nil || !strings.Contains(err.Error(), "one-shot trade") {
		t.Fatalf("err = %v", err)
	}
	h.expect(opcode.CMSGAcceptTrade)
	h.tradeStatus(game.TradeComplete)
	if o := <-done; o.err != nil {
		t.Fatal(o.err)
	}
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.CMSGCastSpell {
			t.Fatalf("cast sent during a one-shot trade % X", p.Body)
		}
	}
}
