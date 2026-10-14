package session

import (
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
