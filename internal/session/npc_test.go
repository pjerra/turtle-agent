package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/objects/objtest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Creatures of entry 0x2001 (a vendor), 0x2002 (a trainer) and 0x2003 (a quest giver).
const (
	vendorGUID  packet.GUID = 0xF130002001000001
	trainerGUID packet.GUID = 0xF130002002000001
	giverGUID   packet.GUID = 0xF130002003000001
)

// npcWorld enters the world and answers the NPCs' name queries up front, so their events
// are logged at once.
func npcWorld(t *testing.T) *harness {
	t.Helper()
	h := start(t, Options{})
	h.enterWorld()
	for i, name := range []string{"Booty Vendor", "Class Trainer", "Quest Giver"} {
		h.send(opcode.SMSGCreatureQueryResponse, creatureResponse(0x2001+uint32(i), name))
	}
	return h
}

func (h *harness) noPacket(op uint16) {
	h.t.Helper()
	if p, err := h.srv.Expect(op, 150*time.Millisecond); err == nil {
		h.t.Fatalf("unexpected %s % X", opcode.Name(op), p.Body)
	}
}

func lastOfKind(t *testing.T, l *events.Log, kind string) events.Event {
	t.Helper()
	var e []events.Event
	waitFor(t, func() bool { e = ofKind(l, kind); return len(e) > 0 })
	return e[len(e)-1]
}

func TestVendorListAndBuy(t *testing.T) {
	ctx := context.Background()
	h := npcWorld(t)
	if err := h.s.VendorList(ctx, 0); err == nil {
		t.Fatal("vendor_list without a guid accepted")
	}
	if err := h.s.VendorList(ctx, vendorGUID); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGListInventory); string(p.Body) != string(game.BuildListInventory(vendorGUID)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGListInventory, gametest.ListInventory(vendorGUID,
		gametest.VendorLine{Entry: 159, Display: 18084, Stock: game.UnlimitedStock, Price: 100, BuyCount: 1},
		gametest.VendorLine{Entry: 2512, Display: 17, Stock: 20, Price: 4, Durability: 0, BuyCount: 200}))
	e := lastOfKind(t, h.s.Log(), events.KindVendorList)
	items := e.Data["items"].([]map[string]any)
	if e.Source != "Booty Vendor" || len(items) != 2 || items[0]["stock"] != int64(-1) || items[1]["stock"] != int64(20) ||
		items[1]["index"] != uint32(2) || items[1]["buy_count"] != uint32(200) || items[0]["price"] != uint32(100) || e.Data["empty"] != false {
		t.Fatalf("%+v", e)
	}
	h.send(opcode.SMSGListInventory, gametest.ListInventory(vendorGUID))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindVendorList)) == 2 })
	if e := ofKind(h.s.Log(), events.KindVendorList)[1]; e.Data["empty"] != true || len(e.Data["items"].([]map[string]any)) != 0 {
		t.Fatalf("%+v", e)
	}

	// Buy: one purchase by default, the count in purchases.
	for _, bad := range []struct {
		v packet.GUID
		r BuyRef
	}{{0, BuyRef{Entry: 159}}, {vendorGUID, BuyRef{}}, {vendorGUID, BuyRef{Entry: 159, Bag: new(uint8)}}, {vendorGUID, BuyRef{Entry: 159, Bag: new(uint8), Slot: new(uint8)}}} {
		if err := h.s.Buy(ctx, bad.v, bad.r); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	h.noPacket(opcode.CMSGBuyItem)
	if err := h.s.Buy(ctx, vendorGUID, BuyRef{Entry: 2512, Count: 2}); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGBuyItem); string(p.Body) != string(packet.NewWriter().GUID(vendorGUID).U32(2512).U8(2).U8(0).Bytes()) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGBuyItem, gametest.BuyItem(vendorGUID, 2, 18, 2))
	e = lastOfKind(t, h.s.Log(), events.KindVendorBuy)
	if e.Source != "Booty Vendor" || e.Data["item_entry"] != uint32(2512) || e.Data["count"] != uint32(2) || e.Data["stock"] != int64(18) || e.Data["slot"] != uint32(2) {
		t.Fatalf("%+v", e)
	}
	// Unlimited stock reads -1, and a purchase nobody asked for still logs.
	h.send(opcode.SMSGBuyItem, gametest.BuyItem(vendorGUID, 1, game.UnlimitedStock, 1))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindVendorBuy)) == 2 })
	if e := ofKind(h.s.Log(), events.KindVendorBuy)[1]; e.Data["stock"] != int64(-1) || e.Data["item_entry"] != nil {
		t.Fatalf("%+v", e)
	}

	// A refusal names the reason; a purchase in the backpack goes by the player's own guid.
	if err := h.s.Buy(ctx, vendorGUID, BuyRef{Entry: 159, Bag: new(uint8), Slot: new(uint8)}); err == nil {
		t.Fatal("bag 0 accepted")
	}
	bag, slot := uint8(255), uint8(24)
	if err := h.s.Buy(ctx, vendorGUID, BuyRef{Entry: 159, Bag: &bag, Slot: &slot}); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGBuyItemInSlot); string(p.Body) != string(game.BuildBuyItemInSlot(vendorGUID, 159, me.GUID, 24, 1)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGBuyFailed, gametest.BuyFailed(vendorGUID, 159, 2))
	e = lastOfKind(t, h.s.Log(), events.KindVendorBuyFailed)
	if e.Source != "Booty Vendor" || e.Data["item_entry"] != uint32(159) || e.Data["reason"] != "not_enough_money" || e.Data["code"] != uint8(2) {
		t.Fatalf("%+v", e)
	}
	h.send(opcode.SMSGBuyFailed, gametest.BuyFailed(0, 159, 5)) // no vendor found
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindVendorBuyFailed)) == 2 })
	if e := ofKind(h.s.Log(), events.KindVendorBuyFailed)[1]; e.SourceGUID != 0 || e.Data["reason"] != "too_far" {
		t.Fatalf("%+v", e)
	}
	// A bag in a bag slot is addressed by the bag item's guid.
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{19: swordGUID}, map[packet.GUID][2]uint32{swordGUID: {4496, 1}}))
	waitFor(t, func() bool { _, ok := h.s.store.ItemAt(me.GUID, 255, 19); return ok })
	bag, slot = 19, 3
	if err := h.s.Buy(ctx, vendorGUID, BuyRef{Entry: 159, Bag: &bag, Slot: &slot}); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGBuyItemInSlot); string(p.Body) != string(game.BuildBuyItemInSlot(vendorGUID, 159, swordGUID, 3, 1)) {
		t.Fatalf("% X", p.Body)
	}
	h.logout()
	if err := h.s.Buy(ctx, vendorGUID, BuyRef{Entry: 159}); err == nil {
		t.Fatal("buy after logout accepted")
	}
}

// coinage sets the player's copper.
func coinage(h *harness, copper uint32) {
	h.t.Helper()
	w := objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(me.GUID)
	objtest.Values(w, playerEnd, map[uint16]uint32{objects.PlayerCoinage: copper})
	h.send(opcode.SMSGUpdateObject, w.Bytes())
}

// An item that leaves its slot with no money coming (moved, used, destroyed) is no sale, and
// neither is anything seen while the store is empty after a teleport.
func TestSaleAndLogChangesNeedEvidence(t *testing.T) {
	ctx := context.Background()
	h := npcWorld(t)
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{23: swordGUID}, map[packet.GUID][2]uint32{swordGUID: {2589, 1}}))
	waitFor(t, func() bool { return len(h.s.Inventory()) == 1 })
	questLog(h, 2, 100, 0)
	waitFor(t, func() bool { return len(h.s.QuestLog()) == 1 })
	if _, err := h.s.Sell(ctx, vendorGUID, EquipRef{Entry: 2589}, 0); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGSellItem)
	if err := h.s.QuestAbandon(ctx, 100); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGQuestlogRemoveQuest)
	if err := h.s.QuestAccept(ctx, giverGUID, 101); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGQuestgiverAcceptQuest)

	// SMSG_NEW_WORLD empties the store: no sale, abandon or accept may be read from that.
	h.send(opcode.SMSGNewWorld, packet.NewWriter().U32(1).F32(1).F32(2).F32(3).F32(0).Bytes())
	h.expect(opcode.MSGMoveWorldportAck)
	time.Sleep(150 * time.Millisecond)
	for _, k := range []string{events.KindVendorSell, events.KindQuestAbandoned, events.KindQuestAccepted} {
		if n := len(ofKind(h.s.Log(), k)); n != 0 {
			t.Fatalf("%s after a teleport: %d", k, n)
		}
	}
	// The player is back with the item gone and the quest gone but no money: only the quest events are real.
	h.send(opcode.SMSGUpdateObject, ownInventory(nil, nil))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindQuestAbandoned)) == 1 })
	if n := len(ofKind(h.s.Log(), events.KindVendorSell)); n != 0 {
		t.Fatalf("sale without money: %d", n)
	}
	coinage(h, 80)
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindVendorSell)) == 1 })
	h.logout()
}

func TestSellItems(t *testing.T) {
	ctx := context.Background()
	h := npcWorld(t)
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{23: swordGUID, 30: potionGUID},
		map[packet.GUID][2]uint32{swordGUID: {2589, 1}, potionGUID: {118, 5}}))
	waitFor(t, func() bool { return len(h.s.Inventory()) == 2 })

	for _, bad := range []struct {
		v     packet.GUID
		ref   EquipRef
		count uint32
	}{{0, EquipRef{Entry: 2589}, 0}, {vendorGUID, EquipRef{}, 0}, {vendorGUID, EquipRef{Entry: 4537}, 0}, {vendorGUID, EquipRef{Entry: 118}, 6}, {vendorGUID, EquipRef{Entry: 118}, 256}} {
		if _, err := h.s.Sell(ctx, bad.v, bad.ref, bad.count); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	h.noPacket(opcode.CMSGSellItem)

	// Two of five: the stack shrinks.
	it, err := h.s.Sell(ctx, vendorGUID, EquipRef{Entry: 118}, 2)
	if err != nil || it.GUID != potionGUID {
		t.Fatalf("%+v %v", it, err)
	}
	if p := h.expect(opcode.CMSGSellItem); string(p.Body) != string(packet.NewWriter().GUID(vendorGUID).GUID(potionGUID).U8(2).Bytes()) {
		t.Fatalf("% X", p.Body)
	}
	if n := len(ofKind(h.s.Log(), events.KindVendorSell)); n != 0 {
		t.Fatalf("sold before the server said so: %d", n)
	}
	w := objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(potionGUID)
	objtest.Values(w, 0x30, map[uint16]uint32{objects.ItemStackCount: 3})
	h.send(opcode.SMSGUpdateObject, w.Bytes())
	// The stack shrank, but a sale shows in the money too: nothing is reported before it.
	waitFor(t, func() bool { it, _ := h.s.store.ItemAt(me.GUID, 255, 30); return it.Count == 3 })
	if n := len(ofKind(h.s.Log(), events.KindVendorSell)); n != 0 {
		t.Fatalf("sold without money: %d", n)
	}
	coinage(h, 150)
	e := lastOfKind(t, h.s.Log(), events.KindVendorSell)
	if e.Source != "Booty Vendor" || e.Data["item_entry"] != uint32(118) || e.Data["count"] != uint32(2) || e.Data["item_guid"] != potionGUID || e.Data["money"] != uint32(150) {
		t.Fatalf("%+v", e)
	}

	// The whole sword, count 0: it leaves its slot.
	slot := uint8(23)
	if _, err := h.s.Sell(ctx, vendorGUID, EquipRef{Slot: &slot}, 0); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGSellItem); string(p.Body) != string(packet.NewWriter().GUID(vendorGUID).GUID(swordGUID).U8(0).Bytes()) {
		t.Fatalf("% X", p.Body)
	}
	w = objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(me.GUID)
	objtest.Values(w, playerEnd, map[uint16]uint32{objects.PlayerInvSlotHead + 46: 0, objects.PlayerInvSlotHead + 47: 0})
	h.send(opcode.SMSGUpdateObject, w.Bytes())
	waitFor(t, func() bool { _, ok := h.s.store.ItemAt(me.GUID, 255, 23); return !ok })
	if n := len(ofKind(h.s.Log(), events.KindVendorSell)); n != 1 {
		t.Fatalf("sold without money: %d", n)
	}
	coinage(h, 400)
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindVendorSell)) == 2 })
	if e := ofKind(h.s.Log(), events.KindVendorSell)[1]; e.Data["item_entry"] != uint32(2589) || e.Data["count"] != uint32(1) {
		t.Fatalf("%+v", e)
	}

	// A refusal names the item and the reason, and the sale is not waited for any more.
	if _, err := h.s.Sell(ctx, vendorGUID, EquipRef{Entry: 118}, 1); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGSellItem)
	// One sale in flight: a second is refused without a packet until this one resolves.
	if _, err := h.s.Sell(ctx, vendorGUID, EquipRef{Entry: 118}, 1); err == nil {
		t.Fatal("second sale accepted while one is in flight")
	}
	h.noPacket(opcode.CMSGSellItem)
	h.send(opcode.SMSGSellItem, gametest.SellItem(vendorGUID, potionGUID, 2))
	e = lastOfKind(t, h.s.Log(), events.KindVendorSellFailed)
	if e.Source != "Booty Vendor" || e.Data["item_entry"] != uint32(118) || e.Data["reason"] != "cant_sell_item" || e.Data["item_guid"] != potionGUID {
		t.Fatalf("%+v", e)
	}
	// The pending list belongs to the owner loop: read it there.
	v, _ := h.s.do(ctx, func() (any, error) { return len(h.s.npc.sells), nil })
	if v.(int) != 0 {
		t.Fatalf("%d sales still pending", v)
	}
	// No vendor in reach: the core names no vendor and no item.
	h.send(opcode.SMSGSellItem, gametest.SellItem(0, 0, 3))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindVendorSellFailed)) == 2 })
	if e := ofKind(h.s.Log(), events.KindVendorSellFailed)[1]; e.Data["reason"] != "cant_find_vendor" || e.Data["item_guid"] != nil {
		t.Fatalf("%+v", e)
	}
	h.logout()
}

func TestTrainer(t *testing.T) {
	ctx := context.Background()
	h := npcWorld(t)
	if err := h.s.TrainerList(ctx, trainerGUID); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGTrainerList); string(p.Body) != string(game.BuildTrainerList(trainerGUID)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGTrainerList, gametest.TrainerList(trainerGUID, 0, "Greetings, $N.",
		gametest.TrainerLine{Spell: 2457, State: 0, Cost: 10, Level: 1},
		gametest.TrainerLine{Spell: 78, State: 1, Cost: 100, Level: 4, Req1: 2457, ReqSkill: 43, ReqSkillVal: 5},
		gametest.TrainerLine{Spell: 2018, State: 2, Level: 5, PrimaryProf: true}))
	e := lastOfKind(t, h.s.Log(), events.KindTrainerList)
	sp := e.Data["spells"].([]map[string]any)
	if e.Source != "Class Trainer" || e.Data["greeting"] != "Greetings, $N." || e.Data["trainer_type"] != uint32(0) || len(sp) != 3 ||
		sp[0]["spell_id"] != uint32(2457) || sp[0]["state"] != "learnable" || sp[0]["cost"] != uint32(10) ||
		sp[1]["state"] != "unavailable" || sp[1]["req_spell_1"] != uint32(2457) || sp[1]["req_skill_value"] != uint32(5) || sp[1]["level"] != uint8(4) ||
		sp[2]["state"] != "known" || sp[2]["primary_profession"] != true {
		t.Fatalf("%+v", e)
	}
	if err := h.s.Train(ctx, trainerGUID, 0); err == nil {
		t.Fatal("train without a spell accepted")
	}
	if err := h.s.Train(ctx, trainerGUID, 2457); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGTrainerBuySpell); string(p.Body) != string(game.BuildTrainerBuySpell(trainerGUID, 2457)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGTrainerBuySucceeded, gametest.TrainerBuySucceeded(trainerGUID, 2457))
	h.send(opcode.SMSGTrainerBuyFailed, gametest.TrainerBuyFailed(trainerGUID, 78, 1))
	ok := lastOfKind(t, h.s.Log(), events.KindTrainerTrained)
	bad := lastOfKind(t, h.s.Log(), events.KindTrainerFailed)
	if ok.Source != "Class Trainer" || ok.SpellID != 2457 || bad.SpellID != 78 || bad.Data["reason"] != "not_enough_money" || bad.Data["code"] != uint32(1) {
		t.Fatalf("%+v\n%+v", ok, bad)
	}
	h.logout()
}

// questLog sets the quest log slot of the player: the quest id and the state word.
func questLog(h *harness, slot int, quest, state uint32) {
	h.t.Helper()
	w := objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(me.GUID)
	base := objects.PlayerQuestLog1 + uint16(3*slot)
	objtest.Values(w, playerEnd, map[uint16]uint32{base: quest, base + 1: state})
	h.send(opcode.SMSGUpdateObject, w.Bytes())
}

func TestQuestAcceptAndAbandon(t *testing.T) {
	ctx := context.Background()
	h := npcWorld(t)
	h.send(opcode.SMSGUpdateObject, ownInventory(nil, nil)) // the player exists, with an empty log

	// A quest giver answers a hello with its list, or with the panel of its one quest.
	if err := h.s.QuestHello(ctx, giverGUID); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGQuestgiverHello); string(p.Body) != string(game.BuildQuestgiverHello(giverGUID)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGQuestgiverQuestList, gametest.QuestGiverQuestList(giverGUID, "Well met.",
		gametest.QuestListEntry{ID: 100, Icon: 2, Level: 5, Title: "Rats in the Cellar"}, gametest.QuestListEntry{ID: 101, Icon: 4, Level: 6, Title: "More Rats"}))
	e := lastOfKind(t, h.s.Log(), events.KindQuestList)
	q := e.Data["quests"].([]map[string]any)
	if e.Source != "Quest Giver" || e.Data["greeting"] != "Well met." || len(q) != 2 || q[1]["title"] != "More Rats" || q[0]["id"] != uint32(100) {
		t.Fatalf("%+v", e)
	}
	if err := h.s.QuestQuery(ctx, giverGUID, 100); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGQuestgiverQueryQuest); string(p.Body) != string(game.BuildQuestgiverQueryQuest(giverGUID, 100)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGQuestgiverQuestDetails, gametest.QuestGiverQuestDetails(giverGUID, 100, "Rats in the Cellar", "Kill them.", "Kill 8 rats.", true,
		[]gametest.QuestReward{{Entry: 2589, Count: 1, Display: 9}, {Entry: 118, Count: 2}}, []gametest.QuestReward{{Entry: 4540, Count: 5}}, 250, 0))
	e = lastOfKind(t, h.s.Log(), events.KindQuestDetails)
	ch := e.Data["choices"].([]map[string]any)
	if e.Data["quest_id"] != uint32(100) || e.Data["title"] != "Rats in the Cellar" || e.Data["objectives"] != "Kill 8 rats." || e.Data["can_accept"] != true ||
		len(ch) != 2 || ch[1]["item_entry"] != uint32(118) || ch[1]["count"] != uint32(2) || e.Data["money"] != int32(250) ||
		e.Data["rewards"].([]map[string]any)[0]["count"] != uint32(5) {
		t.Fatalf("%+v", e)
	}

	// Accept: no packet answers, the quest turns up in the log.
	if err := h.s.QuestAccept(ctx, giverGUID, 0); err == nil {
		t.Fatal("accept without a quest accepted")
	}
	if err := h.s.QuestAccept(ctx, giverGUID, 100); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGQuestgiverAcceptQuest); string(p.Body) != string(game.BuildQuestgiverAcceptQuest(giverGUID, 100)) {
		t.Fatalf("% X", p.Body)
	}
	if n := len(ofKind(h.s.Log(), events.KindQuestAccepted)); n != 0 {
		t.Fatalf("accepted before the log changed: %d", n)
	}
	questLog(h, 3, 100, 0)
	e = lastOfKind(t, h.s.Log(), events.KindQuestAccepted)
	if e.Source != "Quest Giver" || e.Data["quest_id"] != uint32(100) || e.Data["slot"] != uint8(3) || e.Data["complete"] != false {
		t.Fatalf("%+v", e)
	}
	if err := h.s.QuestAccept(ctx, giverGUID, 100); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("accept twice: %v", err)
	}
	if log := h.s.QuestLog(); len(log) != 1 || log[0].Slot != 3 || log[0].ID != 100 || log[0].Complete {
		t.Fatalf("%+v", log)
	}

	// A refusal is a quest_error.
	h.send(opcode.SMSGQuestgiverQuestInvalid, packet.NewWriter().U32(13).Bytes())
	h.send(opcode.SMSGQuestlogFull, nil)
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindQuestError)) == 2 })
	if errs := ofKind(h.s.Log(), events.KindQuestError); errs[0].Data["reason"] != "already_on_quest" || errs[0].Data["error"] != "invalid" || errs[1].Data["error"] != "log_full" {
		t.Fatalf("%+v", errs)
	}

	// Abandon: the log slot is what the server is asked to clear.
	if err := h.s.QuestAbandon(ctx, 999); err == nil || !strings.Contains(err.Error(), "not in the quest log") {
		t.Fatalf("abandon unknown: %v", err)
	}
	if err := h.s.QuestAbandon(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGQuestlogRemoveQuest); string(p.Body) != string([]byte{3}) {
		t.Fatalf("% X", p.Body)
	}
	questLog(h, 3, 0, 0)
	e = lastOfKind(t, h.s.Log(), events.KindQuestAbandoned)
	if e.Data["quest_id"] != uint32(100) || len(h.s.QuestLog()) != 0 {
		t.Fatalf("%+v", e)
	}
	h.logout()
}

func TestQuestHandIn(t *testing.T) {
	ctx := context.Background()
	h := npcWorld(t)
	h.send(opcode.SMSGUpdateObject, ownInventory(nil, nil))
	// Quest 100, all four counters at their needs, state complete.
	questLog(h, 0, 100, 0x01<<24|8)
	waitFor(t, func() bool { return len(h.s.QuestLog()) == 1 })
	if q := h.s.QuestLog()[0]; !q.Complete || q.Failed || q.Counts != [4]uint8{8, 0, 0, 0} {
		t.Fatalf("%+v", q)
	}

	// Items not in hand: the panel is shown and nothing presses on.
	if err := h.s.QuestComplete(ctx, giverGUID, 100); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGQuestgiverCompleteQuest); string(p.Body) != string(game.BuildQuestgiverCompleteQuest(giverGUID, 100)) {
		t.Fatalf("% X", p.Body)
	}
	items := []gametest.QuestReward{{Entry: 4540, Count: 8, Display: 3}}
	h.send(opcode.SMSGQuestgiverRequestItems, gametest.QuestGiverRequestItems(giverGUID, 100, "Rats in the Cellar", "Bring me 8 tails.", 0, items, false))
	e := lastOfKind(t, h.s.Log(), events.KindQuestRequestItems)
	if e.Source != "Quest Giver" || e.Data["completable"] != false || e.Data["items"].([]map[string]any)[0]["count"] != uint32(8) || e.Data["text"] != "Bring me 8 tails." {
		t.Fatalf("%+v", e)
	}
	h.noPacket(opcode.CMSGQuestgiverRequestReward)

	// Items in hand: the session presses Complete Quest, and the offer follows.
	if err := h.s.QuestComplete(ctx, giverGUID, 100); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGQuestgiverCompleteQuest)
	h.send(opcode.SMSGQuestgiverRequestItems, gametest.QuestGiverRequestItems(giverGUID, 100, "Rats in the Cellar", "Bring me 8 tails.", 0, items, true))
	if p := h.expect(opcode.CMSGQuestgiverRequestReward); string(p.Body) != string(game.BuildQuestgiverRequestReward(giverGUID, 100)) {
		t.Fatalf("% X", p.Body)
	}
	// A request_items nobody asked a hand-in for is only shown.
	h.send(opcode.SMSGQuestgiverRequestItems, gametest.QuestGiverRequestItems(giverGUID, 100, "Rats in the Cellar", "Bring me 8 tails.", 0, items, true))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindQuestRequestItems)) == 3 })
	h.noPacket(opcode.CMSGQuestgiverRequestReward)

	choices := []gametest.QuestReward{{Entry: 2589, Count: 1}, {Entry: 118, Count: 2}}
	h.send(opcode.SMSGQuestgiverOfferReward, gametest.QuestGiverOfferReward(giverGUID, 100, "Rats in the Cellar", "Well done.", choices, nil, 250, 0))
	e = lastOfKind(t, h.s.Log(), events.KindQuestOfferReward)
	if e.Data["quest_id"] != uint32(100) || len(e.Data["choices"].([]map[string]any)) != 2 || e.Data["text"] != "Well done." || e.Data["money"] != int32(250) {
		t.Fatalf("%+v", e)
	}

	// The offer has two choices: one must be named, and within range.
	pick := func(n uint32) *uint32 { return &n }
	if err := h.s.QuestReward(ctx, giverGUID, 100, nil); err == nil || !strings.Contains(err.Error(), "2 reward choices") {
		t.Fatalf("no choice: %v", err)
	}
	if err := h.s.QuestReward(ctx, giverGUID, 100, pick(2)); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("choice 2: %v", err)
	}
	if err := h.s.QuestReward(ctx, giverGUID, 100, pick(game.QuestRewardChoices)); err == nil {
		t.Fatal("choice 6 accepted")
	}
	h.noPacket(opcode.CMSGQuestgiverChooseReward)
	if err := h.s.QuestReward(ctx, giverGUID, 100, pick(1)); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGQuestgiverChooseReward); string(p.Body) != string(game.BuildQuestgiverChooseReward(giverGUID, 100, 1)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGQuestgiverQuestComplete, gametest.QuestGiverQuestComplete(100, 450, 250, gametest.QuestReward{Entry: 118, Count: 2}))
	e = lastOfKind(t, h.s.Log(), events.KindQuestRewarded)
	r := e.Data["rewards"].([]map[string]any)
	if e.Source != "Quest Giver" || e.Data["quest_id"] != uint32(100) || e.Data["xp"] != uint32(450) || e.Data["money"] != int32(250) || len(r) != 1 || r[0]["item_entry"] != uint32(118) {
		t.Fatalf("%+v", e)
	}

	// A quest with no choices takes the reward without one.
	h.send(opcode.SMSGQuestgiverOfferReward, gametest.QuestGiverOfferReward(giverGUID, 101, "More Rats", "Thanks.", nil, nil, 0, 0))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindQuestOfferReward)) == 2 })
	if err := h.s.QuestReward(ctx, giverGUID, 101, nil); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGQuestgiverChooseReward); string(p.Body) != string(game.BuildQuestgiverChooseReward(giverGUID, 101, 0)) {
		t.Fatalf("% X", p.Body)
	}
	// A hand-in the NPC cannot take, and the progress packets.
	h.send(opcode.SMSGQuestgiverQuestFailed, packet.NewWriter().U32(101).U32(4).Bytes())
	wolfName(h) // the kill update names its victim
	h.send(opcode.SMSGQuestupdateAddKill, gametest.QuestUpdateAddKill(102, 299, 3, 8, wolfGUID))
	h.send(opcode.SMSGQuestupdateAddItem, packet.NewWriter().U32(4540).U32(2).Bytes())
	h.send(opcode.SMSGQuestupdateComplete, packet.NewWriter().U32(102).Bytes())
	h.send(opcode.SMSGQuestupdateFailed, packet.NewWriter().U32(103).Bytes())
	h.send(opcode.SMSGQuestupdateFailedtimer, packet.NewWriter().U32(104).Bytes())
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindQuestUpdate)) == 5 })
	h.logout()
	if e := ofKind(h.s.Log(), events.KindQuestError); len(e) != 1 || e[0].Data["error"] != "failed_at_giver" || e[0].Data["reason"] != "inventory_full" || e[0].Data["quest_id"] != uint32(101) {
		t.Fatalf("%+v", e)
	}
	var got []string
	for _, e := range ofKind(h.s.Log(), events.KindQuestUpdate) {
		got = append(got, e.Data["update"].(string))
	}
	if strings.Join(got, ",") != "kill,item,complete,failed,timer_failed" {
		t.Fatalf("%v", got)
	}
	if k := ofKind(h.s.Log(), events.KindQuestUpdate)[0]; k.Data["quest_id"] != uint32(102) || k.Data["entry"] != uint32(299) || k.Data["count"] != uint32(3) || k.Data["required"] != uint32(8) || k.TargetGUID != wolfGUID {
		t.Fatalf("%+v", k)
	}
	if k := ofKind(h.s.Log(), events.KindQuestUpdate)[1]; k.Data["added"] == nil || k.Data["count"] != nil {
		t.Fatalf("item update must carry the amount added, not a total: %+v", k)
	}
	if st := h.s.Status(); st.ParseErrors != 0 || len(st.Unknown) != 0 {
		t.Fatalf("parse errors %d, unknown opcodes %v", st.ParseErrors, st.Unknown)
	}
}
