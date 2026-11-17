package game_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const (
	npc  packet.GUID = 0xF130002001000001
	bag3 packet.GUID = 0x4000000000000009
)

// The builders write what the core's handlers read: ItemHandler.cpp:542-560 (sell),
// :734-782 (buy), :784-796 (list); NPCHandler.cpp:108-115, :282-290 (trainer);
// QuestHandler.cpp (quests).
func TestBuildNPCRequests(t *testing.T) {
	w := func() *packet.Writer { return packet.NewWriter() }
	for _, c := range []struct {
		name      string
		got, want []byte
	}{
		{"list", game.BuildListInventory(npc), w().GUID(npc).Bytes()},
		{"buy", game.BuildBuyItem(npc, 159, 3), w().GUID(npc).U32(159).U8(3).U8(0).Bytes()},
		{"buy in slot", game.BuildBuyItemInSlot(npc, 159, bag3, 4, 2), w().GUID(npc).U32(159).GUID(bag3).U8(4).U8(2).Bytes()},
		{"sell", game.BuildSellItem(npc, bag3, 0), w().GUID(npc).GUID(bag3).U8(0).Bytes()},
		{"trainer list", game.BuildTrainerList(npc), w().GUID(npc).Bytes()},
		{"train", game.BuildTrainerBuySpell(npc, 2457), w().GUID(npc).U32(2457).Bytes()},
		{"quest hello", game.BuildQuestgiverHello(npc), w().GUID(npc).Bytes()},
		{"quest query", game.BuildQuestgiverQueryQuest(npc, 7), w().GUID(npc).U32(7).Bytes()},
		{"quest accept", game.BuildQuestgiverAcceptQuest(npc, 7), w().GUID(npc).U32(7).Bytes()},
		{"quest complete", game.BuildQuestgiverCompleteQuest(npc, 7), w().GUID(npc).U32(7).Bytes()},
		{"quest request reward", game.BuildQuestgiverRequestReward(npc, 7), w().GUID(npc).U32(7).Bytes()},
		{"quest choose", game.BuildQuestgiverChooseReward(npc, 7, 2), w().GUID(npc).U32(7).U32(2).Bytes()},
		{"quest remove", game.BuildQuestlogRemoveQuest(19), []byte{19}},
	} {
		if !bytes.Equal(c.got, c.want) {
			t.Errorf("%s: % X, want % X", c.name, c.got, c.want)
		}
	}
}

func TestParseListInventory(t *testing.T) {
	got, err := game.ParseListInventory(gametest.ListInventory(npc,
		gametest.VendorLine{Entry: 159, Display: 18084, Stock: game.UnlimitedStock, Price: 100, BuyCount: 1},
		gametest.VendorLine{Entry: 2512, Display: 17, Stock: 20, Price: 4, Durability: 9, BuyCount: 200}))
	want := game.VendorList{Vendor: npc, Items: []game.VendorItem{
		{Index: 1, Entry: 159, DisplayID: 18084, Stock: -1, Price: 100, BuyCount: 1},
		{Index: 2, Entry: 2512, DisplayID: 17, Stock: 20, Price: 4, Durability: 9, BuyCount: 200}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v\nwant %+v", got, err, want)
	}
	// No lines: count 0 and the result byte (ItemHandler.cpp:907-914).
	if got, err := game.ParseListInventory(gametest.ListInventory(npc)); err != nil || !got.Empty || len(got.Items) != 0 || got.Vendor != npc {
		t.Fatalf("%+v %v", got, err)
	}
	// The same, as the end of the function writes it: a count of 0 and no result byte.
	if got, err := game.ParseListInventory(packet.NewWriter().GUID(npc).U8(0).Bytes()); err != nil || !got.Empty {
		t.Fatalf("%+v %v", got, err)
	}
	full := gametest.ListInventory(npc, gametest.VendorLine{Entry: 1})
	for _, bad := range [][]byte{full[:len(full)-1], append(full[:len(full):len(full)], 0), full[:8]} {
		if _, err := game.ParseListInventory(bad); err == nil {
			t.Errorf("% X accepted", bad)
		}
	}
}

// Bytes written out by hand from the core's code, not by the test writers: SMSG_LIST_INVENTORY
// with one line (ItemHandler.cpp:916-981) and SMSG_BUY_FAILED (Player.cpp:13589-13599).
func TestParseLiteralBytes(t *testing.T) {
	list := []byte{
		0x01, 0x00, 0x00, 0x01, 0x20, 0x00, 0x30, 0xF1, // vendor guid 0xF130002001000001
		0x01,                   // one line
		0x01, 0x00, 0x00, 0x00, // line number
		0x9F, 0x00, 0x00, 0x00, // item 159
		0xA4, 0x46, 0x00, 0x00, // display id 18084
		0xFF, 0xFF, 0xFF, 0xFF, // unlimited
		0x64, 0x00, 0x00, 0x00, // price 100
		0x00, 0x00, 0x00, 0x00, // durability
		0x01, 0x00, 0x00, 0x00, // buy count
	}
	got, err := game.ParseListInventory(list)
	want := game.VendorList{Vendor: npc, Items: []game.VendorItem{{Index: 1, Entry: 159, DisplayID: 18084, Stock: -1, Price: 100, BuyCount: 1}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v\nwant %+v", got, err, want)
	}
	failed := []byte{0x01, 0x00, 0x00, 0x01, 0x20, 0x00, 0x30, 0xF1, 0x9F, 0x00, 0x00, 0x00, 0x02}
	if f, err := game.ParseBuyFailed(failed); err != nil || f != (game.BuyFailed{Vendor: npc, Item: 159, Code: 2}) {
		t.Fatalf("%+v %v", f, err)
	}
	// The reward money of the quest panels is signed (GetRewOrReqMoney): -50 copper is a fee.
	complete := []byte{
		0x07, 0, 0, 0, // quest 7
		0x03, 0, 0, 0, // 3
		0xC2, 0x01, 0, 0, // 450 xp
		0xCE, 0xFF, 0xFF, 0xFF, // -50
		0, 0, 0, 0, // no items
	}
	if c, err := game.ParseQuestComplete(complete); err != nil || c.Money != -50 || c.XP != 450 || c.Quest != 7 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestParseBuySell(t *testing.T) {
	if b, err := game.ParseBuyItem(gametest.BuyItem(npc, 3, 18, 2)); err != nil || b != (game.BuyItem{Vendor: npc, Slot: 3, Stock: 18, Count: 2}) {
		t.Fatalf("%+v %v", b, err)
	}
	if f, err := game.ParseBuyFailed(gametest.BuyFailed(npc, 159, 11)); err != nil || f != (game.BuyFailed{Vendor: npc, Item: 159, Code: 11}) || game.BuyErrorName(f.Code) != "rank_required" {
		t.Fatalf("%+v %v", f, err)
	}
	if f, err := game.ParseSellItem(gametest.SellItem(npc, bag3, 6)); err != nil || f != (game.SellFailed{Vendor: npc, Item: bag3, Code: 6}) || game.SellErrorName(f.Code) != "only_empty_bag" {
		t.Fatalf("%+v %v", f, err)
	}
	if game.BuyErrorName(99) != "unknown(99)" || game.SellErrorName(0) != "unknown(0)" {
		t.Fatal("unknown codes")
	}
	for _, p := range []func([]byte) error{
		func(b []byte) error { _, err := game.ParseBuyItem(b); return err },
		func(b []byte) error { _, err := game.ParseBuyFailed(b); return err },
		func(b []byte) error { _, err := game.ParseSellItem(b); return err },
	} {
		if p([]byte{1, 2, 3}) == nil {
			t.Error("short body accepted")
		}
	}
}

func TestParseTrainerList(t *testing.T) {
	body := gametest.TrainerList(npc, 2, "Hello.",
		gametest.TrainerLine{Spell: 2457, State: 0, Cost: 10, Level: 1},
		gametest.TrainerLine{Spell: 2018, State: 1, Cost: 100, PrimaryProf: true, Level: 5, ReqSkill: 164, ReqSkillVal: 1, Req1: 9, Req2: 8})
	if len(body) != 8+4+4+2*38+len("Hello.")+1 { // the core reserves 38 bytes a spell (NPCHandler.cpp:208)
		t.Fatalf("length %d", len(body))
	}
	got, err := game.ParseTrainerList(body)
	want := game.TrainerList{Trainer: npc, Type: 2, Declared: 2, Greeting: "Hello.", Spells: []game.TrainerSpell{
		{Spell: 2457, State: "learnable", Cost: 10, Level: 1},
		{Spell: 2018, State: "unavailable", Cost: 100, Level: 5, ReqSkill: 164, ReqSkillValue: 1, ReqSpell1: 9, ReqSpell2: 8, PrimaryProfession: true}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v\nwant %+v", got, err, want)
	}
	if got, err := game.ParseTrainerList(gametest.TrainerList(npc, 0, "")); err != nil || len(got.Spells) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := game.ParseTrainerList(body[:len(body)-10]); err == nil {
		t.Fatal("truncated list accepted")
	}
	// The core counts a line it did not write (NPCHandler.cpp:121-126, :229-236): the header
	// says 3 spells and 2 follow. The list still parses, and Declared tells.
	short := append([]byte(nil), body...)
	short[12] = 3
	if l, err := game.ParseTrainerList(short); err != nil || l.Declared != 3 || !reflect.DeepEqual(l.Spells, want.Spells) || l.Greeting != "Hello." {
		t.Fatalf("%+v %v", l, err)
	}
	// More lines than declared, or a line cut short, is a broken packet.
	short[12] = 1
	if _, err := game.ParseTrainerList(short); err == nil {
		t.Fatal("more spells than declared accepted")
	}
	if _, err := game.ParseTrainerList(append(body[:20:20], 0)); err == nil {
		t.Fatal("cut line accepted")
	}
	if s, err := game.ParseTrainerSucceeded(gametest.TrainerBuySucceeded(npc, 5)); err != nil || s != (game.TrainerSucceeded{Trainer: npc, Spell: 5}) {
		t.Fatalf("%+v %v", s, err)
	}
	f, err := game.ParseTrainerFailed(gametest.TrainerBuyFailed(npc, 5, 2))
	if err != nil || f != (game.TrainerFailed{Trainer: npc, Spell: 5, Code: 2}) || game.TrainerFailName(f.Code) != "not_enough_skill" {
		t.Fatalf("%+v %v", f, err)
	}
	if game.TrainerFailName(0) != "unavailable" || game.TrainerFailName(1) != "not_enough_money" || game.TrainerFailName(9) != "unknown(9)" || game.TrainerStateName(7) != "unknown(7)" {
		t.Fatal("names")
	}
}

func TestParseQuestPanels(t *testing.T) {
	choices := []gametest.QuestReward{{Entry: 2589, Count: 1, Display: 9}, {Entry: 118, Count: 2, Display: 4}}
	rewards := []gametest.QuestReward{{Entry: 4540, Count: 5}}
	items := func(r []gametest.QuestReward) []game.QuestItem {
		var out []game.QuestItem
		for _, it := range r {
			out = append(out, game.QuestItem{Entry: it.Entry, Count: it.Count})
		}
		return out
	}

	l, err := game.ParseQuestList(gametest.QuestGiverQuestList(npc, "Well met.", gametest.QuestListEntry{ID: 7, Icon: 2, Level: 5, Title: "Rats"}))
	if err != nil || !reflect.DeepEqual(l, game.QuestList{NPC: npc, Greeting: "Well met.", Quests: []game.QuestListEntry{{ID: 7, Icon: 2, Level: 5, Title: "Rats"}}}) {
		t.Fatalf("%+v %v", l, err)
	}

	d, err := game.ParseQuestDetails(gametest.QuestGiverQuestDetails(npc, 7, "Rats", "Kill them.", "Kill 8.", true, choices, rewards, 250, 55))
	want := game.QuestDetails{NPC: npc, Quest: 7, Title: "Rats", Details: "Kill them.", Objective: "Kill 8.", AutoAccept: true,
		Choices: items(choices), Rewards: items(rewards), Money: 250, RewardSpell: 55}
	if err != nil || !reflect.DeepEqual(d, want) {
		t.Fatalf("got %+v %v\nwant %+v", d, err, want)
	}
	// Hidden rewards: three zero words (GossipDef.cpp:575-580).
	hidden := gametest.QuestGiverQuestDetails(npc, 7, "Rats", "d", "o", false, nil, nil, 0, 0)
	if d, err := game.ParseQuestDetails(hidden); err != nil || d.AutoAccept || len(d.Choices) != 0 {
		t.Fatalf("%+v %v", d, err)
	}

	r, err := game.ParseQuestRequestItems(gametest.QuestGiverRequestItems(npc, 7, "Rats", "Tails?", 30, rewards, true))
	if err != nil || !reflect.DeepEqual(r, game.QuestRequestItems{NPC: npc, Quest: 7, Title: "Rats", Text: "Tails?", Money: 30, Items: items(rewards), Completable: true}) {
		t.Fatalf("%+v %v", r, err)
	}
	if r, err := game.ParseQuestRequestItems(gametest.QuestGiverRequestItems(npc, 7, "Rats", "Tails?", 0, nil, false)); err != nil || r.Completable || len(r.Items) != 0 {
		t.Fatalf("%+v %v", r, err)
	}

	o, err := game.ParseQuestOfferReward(gametest.QuestGiverOfferReward(npc, 7, "Rats", "Done.", choices, rewards, 250, 55))
	if err != nil || !reflect.DeepEqual(o, game.QuestOfferReward{NPC: npc, Quest: 7, Title: "Rats", Text: "Done.", Choices: items(choices), Rewards: items(rewards), Money: 250, RewardSpell: 55}) {
		t.Fatalf("%+v %v", o, err)
	}

	c, err := game.ParseQuestComplete(gametest.QuestGiverQuestComplete(7, 450, 250, rewards[0]))
	if err != nil || !reflect.DeepEqual(c, game.QuestComplete{Quest: 7, XP: 450, Money: 250, Rewards: items(rewards)}) {
		t.Fatalf("%+v %v", c, err)
	}

	for name, p := range map[string]func([]byte) error{
		"list":    func(b []byte) error { _, err := game.ParseQuestList(b); return err },
		"details": func(b []byte) error { _, err := game.ParseQuestDetails(b); return err },
		"request": func(b []byte) error { _, err := game.ParseQuestRequestItems(b); return err },
		"offer":   func(b []byte) error { _, err := game.ParseQuestOfferReward(b); return err },
		"done":    func(b []byte) error { _, err := game.ParseQuestComplete(b); return err },
	} {
		if p([]byte{1, 2, 3}) == nil {
			t.Errorf("%s: short body accepted", name)
		}
	}
}

func TestParseQuestSmallPackets(t *testing.T) {
	if f, err := game.ParseQuestFailedAtTaker(packet.NewWriter().U32(7).U32(17).Bytes()); err != nil || f != (game.QuestFailedAtTaker{Quest: 7, Reason: 17}) {
		t.Fatalf("%+v %v", f, err)
	}
	if v, err := game.ParseQuestInvalid(packet.NewWriter().U32(13).Bytes()); err != nil || v != 13 || game.QuestInvalidName(v) != "already_on_quest" {
		t.Fatalf("%d %v", v, err)
	}
	if _, err := game.ParseQuestInvalid(nil); err == nil {
		t.Fatal("empty body accepted")
	}
	if game.QuestInvalidName(4) != "inventory_full" || game.QuestInvalidName(50) != "inventory_full" || game.QuestInvalidName(99) != "unknown(99)" {
		t.Fatal("names")
	}
	if id, err := game.ParseQuestID(packet.NewWriter().U32(7).Bytes(), "X"); err != nil || id != 7 {
		t.Fatalf("%d %v", id, err)
	}
	if u, err := game.ParseQuestUpdateItem(packet.NewWriter().U32(4540).U32(3).Bytes()); err != nil || u != (game.QuestUpdateItem{Item: 4540, Added: 3}) {
		t.Fatalf("%+v %v", u, err)
	}
	u, err := game.ParseQuestUpdateKill(gametest.QuestUpdateAddKill(7, 299, 3, 8, npc))
	if err != nil || u != (game.QuestUpdateKill{Quest: 7, Entry: 299, Count: 3, Required: 8, Victim: npc}) {
		t.Fatalf("%+v %v", u, err)
	}
}
