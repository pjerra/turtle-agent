package main

import (
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/rpc"
)

const (
	npcHex           = "0xF130002001000001"
	npcGUID          = packet.GUID(0xF130002001000001)
	npcCreatureEntry = 0x2001
	npcCreatureName  = "Booty Vendor"
)

// npcRig is a rig whose NPC name is already known, so events are logged at once.
func npcRig(t *testing.T) *agentRig {
	t.Helper()
	r := newRig(t)
	r.srv.Send(opcode.SMSGCreatureQueryResponse, packet.NewWriter().U32(npcCreatureEntry).CString(npcCreatureName).U8(0).U8(0).U8(0).CString("").
		U32(0).U32(7).U32(0).U32(0).U32(0).U32(0).U32(1).U8(0).U8(0).Bytes())
	return r
}

// resultEvent runs call in the background (it waits for the answer), lets answer play the
// server, and returns the result's event.
func resultEvent(t *testing.T, r *agentRig, method, params string, answer func()) map[string]any {
	t.Helper()
	got := make(chan map[string]any, 1)
	go func() { got <- r.call(method, params) }()
	answer()
	out := <-got
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: %v", method, out)
	}
	ev, _ := res["event"].(map[string]any)
	if ev == nil {
		t.Fatalf("%s: no event in %v", method, out)
	}
	return ev
}

func TestVendorMethods(t *testing.T) {
	r := npcRig(t)
	ev := resultEvent(t, r, "vendor_list", `{"guid":"`+npcHex+`","wait_ms":2000}`, func() {
		r.expectBody(opcode.CMSGListInventory, game.BuildListInventory(npcGUID))
		r.srv.Send(opcode.SMSGListInventory, gametest.ListInventory(npcGUID, gametest.VendorLine{Entry: 159, Stock: game.UnlimitedStock, Price: 100, BuyCount: 1}))
	})
	if items := ev["data"].(map[string]any)["items"].([]any); ev["kind"] != "vendor_list" || ev["source"] != npcCreatureName || len(items) != 1 || items[0].(map[string]any)["item_entry"] != float64(159) {
		t.Fatalf("%v", ev)
	}

	ev = resultEvent(t, r, "buy", `{"guid":"`+npcHex+`","item_entry":159,"count":2,"wait_ms":2000}`, func() {
		r.expectBody(opcode.CMSGBuyItem, game.BuildBuyItem(npcGUID, 159, 2))
		r.srv.Send(opcode.SMSGBuyItem, gametest.BuyItem(npcGUID, 1, game.UnlimitedStock, 2))
	})
	if d := ev["data"].(map[string]any); ev["kind"] != "vendor_buy" || d["item_entry"] != float64(159) || d["stock"] != float64(-1) {
		t.Fatalf("%v", ev)
	}
	ev = resultEvent(t, r, "buy", `{"guid":"`+npcHex+`","item_entry":159,"wait_ms":2000}`, func() {
		r.expect(opcode.CMSGBuyItem)
		r.srv.Send(opcode.SMSGBuyFailed, gametest.BuyFailed(npcGUID, 159, 2))
	})
	if ev["kind"] != "vendor_buy_failed" || ev["data"].(map[string]any)["reason"] != "not_enough_money" {
		t.Fatalf("%v", ev)
	}
	// Bad requests are errors, not packets.
	for _, c := range []struct{ method, params string }{
		{"buy", `{"guid":"` + npcHex + `"}`},
		{"buy", `{"guid":"` + npcHex + `","item_entry":159,"count":256}`},
		{"sell", `{"guid":"` + npcHex + `","item_entry":2589}`},
		{"sell", `{"guid":"` + npcHex + `"}`},
		{"vendor_list", `{}`},
	} {
		if out := r.call(c.method, c.params); errCode(out) != rpc.CodeGame {
			t.Errorf("%s %s: %v", c.method, c.params, out)
		}
	}
	// A gossip option that opens a vendor list answers the gossip call with it.
	ev = resultEvent(t, r, "gossip_hello", `{"guid":"`+npcHex+`","wait_ms":2000}`, func() {
		r.expect(opcode.CMSGGossipHello)
		r.srv.Send(opcode.SMSGListInventory, gametest.ListInventory(npcGUID))
	})
	if ev["kind"] != "vendor_list" {
		t.Fatalf("%v", ev)
	}
}

func TestTrainerMethods(t *testing.T) {
	r := npcRig(t)
	ev := resultEvent(t, r, "trainer_list", `{"guid":"`+npcHex+`","wait_ms":2000}`, func() {
		r.expectBody(opcode.CMSGTrainerList, game.BuildTrainerList(npcGUID))
		r.srv.Send(opcode.SMSGTrainerList, gametest.TrainerList(npcGUID, 0, "Hi.", gametest.TrainerLine{Spell: 2457, Cost: 10, Level: 1}))
	})
	if sp := ev["data"].(map[string]any)["spells"].([]any); ev["kind"] != "trainer_list" || len(sp) != 1 || sp[0].(map[string]any)["state"] != "learnable" {
		t.Fatalf("%v", ev)
	}
	ev = resultEvent(t, r, "train", `{"guid":"`+npcHex+`","spell_id":2457,"wait_ms":2000}`, func() {
		r.expectBody(opcode.CMSGTrainerBuySpell, game.BuildTrainerBuySpell(npcGUID, 2457))
		r.srv.Send(opcode.SMSGTrainerBuySucceeded, gametest.TrainerBuySucceeded(npcGUID, 2457))
	})
	if ev["kind"] != "trainer_trained" || ev["spell_id"] != float64(2457) {
		t.Fatalf("%v", ev)
	}
	if out := r.call("train", `{"guid":"`+npcHex+`"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
}

func TestQuestMethods(t *testing.T) {
	r := npcRig(t)
	if out := r.call("quest_log", `{}`); out["error"] != nil || len(out["result"].(map[string]any)["quests"].([]any)) != 0 {
		t.Fatalf("%v", out)
	}
	ev := resultEvent(t, r, "quest_query", `{"guid":"`+npcHex+`","quest_id":100,"wait_ms":2000}`, func() {
		r.expectBody(opcode.CMSGQuestgiverQueryQuest, game.BuildQuestgiverQueryQuest(npcGUID, 100))
		r.srv.Send(opcode.SMSGQuestgiverQuestDetails, gametest.QuestGiverQuestDetails(npcGUID, 100, "Rats", "Kill.", "Kill 8.", true, nil, nil, 250, 0))
	})
	if ev["kind"] != "quest_details" || ev["data"].(map[string]any)["title"] != "Rats" {
		t.Fatalf("%v", ev)
	}
	// With the items in hand quest_complete reports the offer, not the request.
	ev = resultEvent(t, r, "quest_complete", `{"guid":"`+npcHex+`","quest_id":100,"wait_ms":2000}`, func() {
		r.expectBody(opcode.CMSGQuestgiverCompleteQuest, game.BuildQuestgiverCompleteQuest(npcGUID, 100))
		r.srv.Send(opcode.SMSGQuestgiverRequestItems, gametest.QuestGiverRequestItems(npcGUID, 100, "Rats", "Tails?", 0, []gametest.QuestReward{{Entry: 4540, Count: 8}}, true))
		r.expectBody(opcode.CMSGQuestgiverRequestReward, game.BuildQuestgiverRequestReward(npcGUID, 100))
		r.srv.Send(opcode.SMSGQuestgiverOfferReward, gametest.QuestGiverOfferReward(npcGUID, 100, "Rats", "Done.", []gametest.QuestReward{{Entry: 118, Count: 2}}, nil, 250, 0))
	})
	if ev["kind"] != "quest_offer_reward" {
		t.Fatalf("%v", ev)
	}
	if out := r.call("quest_reward", `{"guid":"`+npcHex+`","quest_id":100,"choice":3}`); errCode(out) != rpc.CodeGame || !strings.Contains(out["error"].(map[string]any)["message"].(string), "out of range") {
		t.Fatalf("%v", out)
	}
	ev = resultEvent(t, r, "quest_reward", `{"guid":"`+npcHex+`","quest_id":100,"choice":0,"wait_ms":2000}`, func() {
		r.expectBody(opcode.CMSGQuestgiverChooseReward, game.BuildQuestgiverChooseReward(npcGUID, 100, 0))
		r.srv.Send(opcode.SMSGQuestgiverQuestComplete, gametest.QuestGiverQuestComplete(100, 450, 250))
	})
	if d := ev["data"].(map[string]any); ev["kind"] != "quest_rewarded" || d["xp"] != float64(450) || ev["source"] != npcCreatureName {
		t.Fatalf("%v", ev)
	}
	for _, c := range []struct{ method, params string }{
		{"quest_accept", `{"guid":"` + npcHex + `"}`},
		{"quest_abandon", `{"quest_id":999}`},
		{"quest_abandon", `{}`},
		{"quest_hello", `{}`},
	} {
		if out := r.call(c.method, c.params); errCode(out) != rpc.CodeGame {
			t.Errorf("%s %s: %v", c.method, c.params, out)
		}
	}
	// A quest-only NPC answers a hello with its panel.
	ev = resultEvent(t, r, "quest_hello", `{"guid":"`+npcHex+`","wait_ms":2000}`, func() {
		r.expectBody(opcode.CMSGQuestgiverHello, game.BuildQuestgiverHello(npcGUID))
		r.srv.Send(opcode.SMSGQuestgiverQuestList, gametest.QuestGiverQuestList(npcGUID, "Hi.", gametest.QuestListEntry{ID: 100, Title: "Rats"}))
	})
	if ev["kind"] != "quest_list" {
		t.Fatalf("%v", ev)
	}
}
