package game_test

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
)

// The parsers against the packets of the live run of #61 (realpackets_live_test.go).

func TestRealTrainerList(t *testing.T) {
	l, err := game.ParseTrainerList(realTrainerFishing)
	if err != nil {
		t.Fatal(err)
	}
	want := game.TrainerList{
		Trainer: 0xF130000B12000244, Type: 2, Declared: 2,
		Greeting: "I can teach you how to use a fishing pole to catch fish.",
		Spells: []game.TrainerSpell{
			{Spell: 7734, State: "unavailable", Cost: 500, Level: 1, ReqSkill: 356, ReqSkillValue: 50, ReqSpell1: 7620},
			{Spell: 7733, State: "learnable", Cost: 100, Level: 1},
		},
	}
	if !reflect.DeepEqual(l, want) {
		t.Fatalf("%+v", l)
	}

	l, err = game.ParseTrainerList(realTrainerHerbs)
	if err != nil {
		t.Fatal(err)
	}
	want = game.TrainerList{
		Trainer: 0xF13000038C000241, Type: 2, Declared: 4,
		Greeting: "Searching for herbs requires both knowledge and instinct.",
		Spells: []game.TrainerSpell{
			{Spell: 11994, State: "unavailable", Cost: 50000, ReqSkill: 182, ReqSkillValue: 200, ReqSpell1: 3570},
			{Spell: 3571, State: "unavailable", Cost: 5000, ReqSkill: 182, ReqSkillValue: 125, ReqSpell1: 2368},
			{Spell: 2373, State: "unavailable", Cost: 500, ReqSkill: 182, ReqSkillValue: 50, ReqSpell1: 2366},
			{Spell: 2372, State: "learnable", Cost: 10, PrimaryProfession: true},
		},
	}
	if !reflect.DeepEqual(l, want) {
		t.Fatalf("%+v", l)
	}
	for _, b := range [][]byte{realTrainerFishing, realTrainerHerbs} {
		if _, err := game.ParseTrainerList(b[:len(b)-1]); err == nil {
			t.Fatal("truncated trainer list accepted")
		}
	}
}

func TestRealQuestList(t *testing.T) {
	l, err := game.ParseQuestList(realQuestListCrank)
	if err != nil {
		t.Fatal(err)
	}
	want := game.QuestList{
		NPC: 0xF1300009C2000225, Greeting: "What what?!?  We all have profit to make... and we won't do it by standing idle.",
		Quests: []game.QuestListEntry{
			{ID: 605, Icon: 5, Level: 35, Title: "Singing Blue Shards"},
			{ID: 621, Icon: 5, Level: 44, Title: "Zanzil's Secret"},
		},
	}
	if !reflect.DeepEqual(l, want) {
		t.Fatalf("%+v", l)
	}

	l, err = game.ParseQuestList(realQuestListKrazek)
	if err != nil {
		t.Fatal(err)
	}
	if l.NPC != 0xF130000305000208 || len(l.Quests) != 4 {
		t.Fatalf("%+v", l)
	}
	for i, q := range []game.QuestListEntry{
		{ID: 198, Icon: 5, Level: 32, Title: "Supplies to Private Thorsen"},
		{ID: 201, Icon: 5, Level: 32, Title: "Investigate the Camp"},
		{ID: 616, Icon: 5, Level: 37, Title: "The Haunted Isle"},
		{ID: 2864, Icon: 5, Level: 45, Title: "Tran'rek"},
	} {
		if l.Quests[i] != q {
			t.Errorf("quest %d: %+v", i, l.Quests[i])
		}
	}
	for _, b := range [][]byte{realQuestListCrank, realQuestListKrazek} {
		if _, err := game.ParseQuestList(b[:len(b)-1]); err == nil {
			t.Fatal("truncated quest list accepted")
		}
	}
}

// One quest (575) through its panels: details, the request for two items, the offer once
// they are held, and the reward. Money is the same 22 silver in all three.
func TestRealQuestPanels(t *testing.T) {
	const npc = 0xF1300009BF000229
	d, err := game.ParseQuestDetails(realQuestDetails)
	if err != nil {
		t.Fatal(err)
	}
	if d.NPC != npc || d.Quest != 575 || d.Title != "Supply and Demand" || !d.AutoAccept ||
		d.Objective != "Bring 2 Large River Crocolisk Skins to Drizzlik in Booty Bay." ||
		d.Money != 2200 || d.RewardSpell != 0 || len(d.Choices) != 0 || len(d.Rewards) != 0 ||
		!strings.HasPrefix(d.Details, "\"The finest leathercrafter") {
		t.Fatalf("%+v", d)
	}

	ri, err := game.ParseQuestRequestItems(realQuestRequestItems)
	if err != nil {
		t.Fatal(err)
	}
	if ri.NPC != npc || ri.Quest != 575 || ri.Title != "Supply and Demand" || ri.Money != 0 || ri.Completable ||
		!reflect.DeepEqual(ri.Items, []game.QuestItem{{Entry: 4053, Count: 2}}) ||
		!strings.HasPrefix(ri.Text, "My equipment is all ready to go, $N") {
		t.Fatalf("%+v", ri)
	}

	o, err := game.ParseQuestOfferReward(realQuestOfferReward)
	if err != nil {
		t.Fatal(err)
	}
	if o.NPC != npc || o.Quest != 575 || o.Title != "Supply and Demand" || o.Money != 2200 || o.RewardSpell != 0 ||
		len(o.Choices) != 0 || len(o.Rewards) != 0 || !strings.HasPrefix(o.Text, "These skins should do.") {
		t.Fatalf("%+v", o)
	}

	c, err := game.ParseQuestComplete(realQuestComplete)
	if err != nil {
		t.Fatal(err)
	}
	if c.Quest != 575 || c.XP != 1250 || c.Money != 2200 || len(c.Rewards) != 0 {
		t.Fatalf("%+v", c)
	}

	for name, f := range map[string]func() error{
		"details": func() error { _, err := game.ParseQuestDetails(realQuestDetails[:len(realQuestDetails)-1]); return err },
		"request": func() error {
			_, err := game.ParseQuestRequestItems(realQuestRequestItems[:len(realQuestRequestItems)-1])
			return err
		},
		"offer": func() error {
			_, err := game.ParseQuestOfferReward(realQuestOfferReward[:len(realQuestOfferReward)-1])
			return err
		},
		"complete": func() error {
			_, err := game.ParseQuestComplete(realQuestComplete[:len(realQuestComplete)-1])
			return err
		},
	} {
		if f() == nil {
			t.Errorf("truncated %s accepted", name)
		}
	}
}

func TestRealLootResponse(t *testing.T) {
	l, err := game.ParseLootResponse(realLootRefused)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Refused || l.GUID != 0xF130000615000884 || l.Err != 4 || l.Money != 0 || len(l.Items) != 0 {
		t.Fatalf("%+v", l)
	}

	l, err = game.ParseLootResponse(realLootItems)
	if err != nil {
		t.Fatal(err)
	}
	want := game.LootResponse{
		GUID: 0xF130000615000884, Type: 1,
		Items: []game.LootItem{
			{Slot: 0, Item: 2799, Count: 1, DisplayID: 7129},
			{Slot: 1, Item: 5637, Count: 1, DisplayID: 2460},
		},
	}
	if !reflect.DeepEqual(l, want) {
		t.Fatalf("%+v", l)
	}

	l, err = game.ParseLootResponse(realLootMoneyItem)
	if err != nil {
		t.Fatal(err)
	}
	want = game.LootResponse{
		GUID: 0xF130000619000A41, Type: 1, Money: 90,
		Items: []game.LootItem{{Slot: 0, Item: 4338, Count: 1, DisplayID: 7384}},
	}
	if !reflect.DeepEqual(l, want) {
		t.Fatalf("%+v", l)
	}
	for _, b := range [][]byte{realLootRefused, realLootItems, realLootMoneyItem} {
		if _, err := game.ParseLootResponse(b[:len(b)-1]); err == nil {
			t.Fatal("truncated loot response accepted")
		}
	}
}

// SMSG_FORCE_RUN_SPEED_CHANGE: packed guid, counter, speed. The test character's guid is
// 0x207 in this run. The mount speed is the horse's 160 % of 7 yd/s; the GM ones are
// .modify speed 2 and back to 1.
func TestRealForceRunSpeedChange(t *testing.T) {
	for _, c := range []struct {
		name    string
		body    []byte
		counter uint32
		speed   float32
	}{
		{"mounted", realForceRunMounted, 0x3A, 11.2},
		{"dismounted", realForceRunUnmounted, 0x3B, 7},
		{"gm speed 2", realForceRunGM2, 2, 14},
		{"gm speed 1", realForceRunGM1, 5, 7},
	} {
		got, err := game.ParseMovementChange(c.body, true)
		if err != nil || got.GUID != 0x207 || got.Counter != c.counter || math.Abs(float64(got.Speed-c.speed)) > 0.001 {
			t.Errorf("%s: %+v, %v", c.name, got, err)
		}
		if _, err := game.ParseMovementChange(c.body[:len(c.body)-1], true); err == nil {
			t.Errorf("%s: truncated change accepted", c.name)
		}
	}
}
