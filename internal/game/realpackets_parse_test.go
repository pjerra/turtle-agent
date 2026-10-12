package game_test

import (
	"reflect"
	"testing"

	"github.com/pjerra/turtle-agent/internal/game"
)

// The parsers against packets the real server sent (realpackets_test.go).

func TestRealGossip(t *testing.T) {
	m, err := game.ParseGossipMessage(realGossipClasses)
	if err != nil {
		t.Fatal(err)
	}
	if m.NPC != 0xF13001732F3D0917 || m.TextID != 0xFFFFFF || len(m.Options) != 8 || len(m.Quests) != 0 {
		t.Fatalf("%+v", m)
	}
	for i, want := range []string{"Warrior", "Paladin", "Hunter", "Rogue", "Priest", "Mage", "Warlock", "Druid"} {
		if o := m.Options[i]; o.Index != uint32(i) || o.Text != want || o.Icon != 0 || o.Coded {
			t.Errorf("option %d: %+v", i, o)
		}
	}

	m, err = game.ParseGossipMessage(realGossipRaces)
	if err != nil {
		t.Fatal(err)
	}
	if back, ok := m.Option(4); len(m.Options) != 5 || !ok || back.Text != "< Back" || back.Icon != 7 {
		t.Fatalf("%+v", m)
	}

	m, err = game.ParseGossipMessage(realGossipInnkeeper)
	if err != nil {
		t.Fatal(err)
	}
	want := game.GossipMenu{
		NPC: 0xF130001A97000219, TextID: 825,
		Options: []game.GossipOption{{Index: 0, Icon: 5, Text: "Make this inn your home."}, {Index: 1, Icon: 1, Text: "I want to browse your goods."}},
		Quests:  []game.GossipQuest{{ID: 60155, Icon: 5, Level: 39, Title: "South Seas Delicacy"}},
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("%+v", m)
	}
	for _, b := range [][]byte{realGossipClasses, realGossipRaces, realGossipInnkeeper} {
		if _, err := game.ParseGossipMessage(b[:len(b)-1]); err == nil {
			t.Fatal("truncated gossip accepted")
		}
	}
}

func TestRealVendorList(t *testing.T) {
	l, err := game.ParseListInventory(realVendorList)
	if err != nil {
		t.Fatal(err)
	}
	if l.Vendor != 0xF130001A97000219 || l.Empty || len(l.Items) != 12 {
		t.Fatalf("%v empty=%v, %d items", l.Vendor, l.Empty, len(l.Items))
	}
	if first := (game.VendorItem{Index: 1, Entry: 787, DisplayID: 24697, Stock: -1, Price: 25, BuyCount: 5}); l.Items[0] != first {
		t.Fatalf("first line %+v", l.Items[0])
	}
	if last := (game.VendorItem{Index: 12, Entry: 8766, DisplayID: 68560, Stock: -1, Price: 4000, BuyCount: 5}); l.Items[11] != last {
		t.Fatalf("last line %+v", l.Items[11])
	}
	for i, it := range l.Items {
		if it.Index != uint32(i+1) || it.Stock != -1 {
			t.Errorf("line %d: %+v", i, it)
		}
	}
	if _, err := game.ParseListInventory(realVendorList[:len(realVendorList)-1]); err == nil {
		t.Fatal("truncated vendor list accepted")
	}
}

// Raid icons: a delta is u8 0, icon, guid; the full list is u8 1 then icon and guid per set
// icon; a zero guid clears the icon.
func TestRealRaidTargetUpdate(t *testing.T) {
	for _, c := range []struct {
		name string
		body []byte
		want game.RaidTargetUpdate
	}{
		{"full list", realRaidFullSkull, game.RaidTargetUpdate{Full: true, Marks: []game.RaidMark{{Icon: 7, GUID: 0xF130000FEB000A03}}}},
		{"full list, moon", realRaidFullMoon, game.RaidTargetUpdate{Full: true, Marks: []game.RaidMark{{Icon: 4, GUID: 0xF1300012F40082F5}}}},
		{"set", realRaidSetMoon, game.RaidTargetUpdate{Marks: []game.RaidMark{{Icon: 4, GUID: 0xF130000619000A15}}}},
		{"clear", realRaidClearMoon, game.RaidTargetUpdate{Marks: []game.RaidMark{{Icon: 4}}}},
	} {
		got, err := game.ParseRaidTargetUpdate(c.body)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, %v", c.name, got, err)
		}
	}
}

// Movement orders: packed guid, counter (and the throw for a knock back). The own character's
// guid is 0x208 in this run.
func TestRealMovementOrders(t *testing.T) {
	c, err := game.ParseMovementChange(realForceRoot, false)
	if err != nil || c.GUID != 0x208 || c.Counter != 4 {
		t.Fatalf("%+v, %v", c, err)
	}
	k, err := game.ParseKnockBack(realKnockBack1)
	if err != nil {
		t.Fatal(err)
	}
	if k.GUID != 0x208 || k.Counter != 1 || k.XYSpeed != 10 || k.ZSpeed != -10 || k.Cos > -0.848 || k.Cos < -0.849 || k.Sin < 0.529 || k.Sin > 0.530 {
		t.Fatalf("%+v", k)
	}
	k, err = game.ParseKnockBack(realKnockBack2)
	if err != nil || k.Counter != 3 || k.XYSpeed != 10 || k.ZSpeed != -10 || k.Cos > -0.999 {
		t.Fatalf("%+v, %v", k, err)
	}
	// The order is a unit vector: the server sends the direction as cos and sin.
	if n := k.Cos*k.Cos + k.Sin*k.Sin; n < 0.999 || n > 1.001 {
		t.Fatalf("direction %v is not a unit vector", n)
	}
	if _, err := game.ParseKnockBack(realKnockBack1[:len(realKnockBack1)-1]); err == nil {
		t.Fatal("truncated knockback accepted")
	}
	if _, err := game.ParseMovementChange(realForceRoot[:len(realForceRoot)-1], false); err == nil {
		t.Fatal("truncated root accepted")
	}
}

// SMSG_NEW_WORLD is map, x, y, z, orientation; SMSG_TRANSFER_PENDING precedes it with the map.
func TestRealNewWorld(t *testing.T) {
	for _, c := range []struct {
		body []byte
		want game.NewWorld
	}{
		{realNewWorldOutland, game.NewWorld{Map: 70, X: -226.8, Y: 49.09, Z: -46.03, O: 1.39}},
		{realNewWorldKalimdor, game.NewWorld{Map: 1, X: -1287, Y: -2379.6, Z: 91.7, O: 3.14159}},
		{realNewWorldEK, game.NewWorld{Map: 0, X: -14195, Y: 425, Z: 14, O: 4.71239}},
	} {
		got, err := game.ParseNewWorld(c.body)
		if err != nil || got != c.want {
			t.Errorf("% X: %+v, %v", c.body, got, err)
		}
		if _, err := game.ParseNewWorld(c.body[:len(c.body)-1]); err == nil {
			t.Errorf("truncated % X accepted", c.body)
		}
	}
	if m, err := game.ParseTransferPending(realTransferPending); err != nil || m != 70 {
		t.Fatalf("%d, %v", m, err)
	}
}

func TestRealLootMoney(t *testing.T) {
	if n, err := game.ParseLootMoneyNotify(realLootMoney); err != nil || n != 57 {
		t.Fatalf("%d, %v", n, err)
	}
}
