package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

const (
	chestG packet.GUID = 0xF11001E1C1000001 // Baelog's Chest, entry 123329
	bossG  packet.GUID = 107                // the fixture's boss (entry 7)
)

func medallion() objects.Item { return objects.Item{Bag: 255, Slot: 24, Entry: 7740, Count: 1} }
func shaft() objects.Item     { return objects.Item{Bag: 255, Slot: 25, Entry: 7741, Count: 1} }

// lootGame is the fixture's game with the optional parts a loot_object, use_item or boss loot needs: the team of the leader and
// the opening, looting and using, which put items in the bag as the server does.
type lootGame struct {
	*fakeGame
	team    string
	chest   []objects.Item // what the chest holds; opening it moves them to the bag
	corpse  []objects.Item // what the boss's corpse holds
	made    []objects.Item // what using an item makes
	err     error          // LootObject fails with this
	opened  []packet.GUID
	corpses []packet.GUID
	items   []uint32
	tries   int
	atLoot  []int // how many targets had been picked when each chest was opened
}

func (g *lootGame) Team() string { return g.team }

func (g *lootGame) LootObject(ctx context.Context, guid packet.GUID) (Looted, error) {
	g.moved("loot")
	g.tries++
	if g.err != nil {
		return Looted{}, g.err
	}
	g.opened = append(g.opened, guid)
	g.atLoot = append(g.atLoot, len(g.targets))
	return g.take(&g.chest), nil
}

func (g *lootGame) LootCorpse(ctx context.Context, guid packet.GUID) (Looted, error) {
	g.moved("loot")
	g.corpses = append(g.corpses, guid)
	return g.take(&g.corpse), nil
}

func (g *lootGame) take(from *[]objects.Item) Looted {
	var l Looted
	for _, it := range *from {
		g.bag = append(g.bag, it)
		l.Items = append(l.Items, ItemCount{it.Entry, it.Count})
	}
	*from = nil
	return l
}

func (g *lootGame) UseItem(ctx context.Context, entry uint32) error {
	g.moved("item")
	g.items = append(g.items, entry)
	var rest []objects.Item
	for _, it := range g.bag { // the item is used up, as the Medallion is
		if it.Entry != entry {
			rest = append(rest, it)
		}
	}
	g.bag = append(rest, g.made...)
	return nil
}

func lootFixture(t *testing.T, plan string) (*lootGame, *Plan) {
	t.Helper()
	g := &lootGame{fakeGame: newFake()}
	g.add(101, 1, Point{30, 0, 0})
	g.add(107, 7, Point{90, 0, 0})
	g.objs = []objects.GameObject{{GUID: chestG, Entry: 123329, X: 60, Type: objects.GOTypeChest, TypeName: "chest", State: "ready"}}
	g.bag = []objects.Item{{Entry: 6948, Count: 1}}
	return g, mustPlan(t, plan)
}

// The Uldaman shape in a small plan: a pack and a boss for the Horde only, then a chest to loot and an item to combine.
const lootPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "pull_groups":[{"entries":[1],"positions":[{"x":30,"y":0,"z":0}],"team":"horde"}],
  "steps":[{"loot_object":{"entry":123329,"near":{"x":60,"y":0,"z":0},"expect_item":{"entry":7740,"name":"Gni'kiv Medallion"}}}],
  "boss":{"entry":7,"position":{"x":90,"y":0,"z":0},"team":"horde"}}]}`

func hasEvent(g *fakeGame, action string) bool {
	for _, e := range g.events {
		if e.data["action"] == action {
			return true
		}
	}
	return false
}

// An Alliance leader skips the pack and the boss (noted), walks next to the chest, loots it and finds the Medallion in the bags.
func TestLootObjectStepForTheAlliance(t *testing.T) {
	g, p := lootFixture(t, lootPlan)
	g.team, g.chest = "alliance", []objects.Item{medallion()}
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.targets) != 0 || g.count(".bot action pullback") != 0 {
		t.Fatalf("the Horde's pack was pulled: targets %v chats %q", g.targets, g.chats)
	}
	if fmt.Sprint(g.opened) != fmt.Sprint([]packet.GUID{chestG}) || fmt.Sprint(g.walks) != fmt.Sprint([]Point{{10, 0, 0}, {60, 0, 0}}) {
		t.Fatalf("opened %v walks %v", g.opened, g.walks)
	}
	notes := strings.Join(d.Report().Notes, "\n")
	if !strings.Contains(notes, "pull group 1 of 1 ([1]) is for team horde, the leader is alliance: skipped") ||
		!strings.Contains(notes, "boss 7 is for team horde, the leader is alliance: skipped") || !strings.Contains(notes, "took 1 x item 7740") {
		t.Fatalf("notes:\n%s", notes)
	}
	if !hasEvent(g.fakeGame, "skip_team") || !hasEvent(g.fakeGame, "loot_object") {
		t.Fatalf("events %s", g.kinds())
	}
	if rep := d.Report(); rep.State != "done" || len(rep.Shortcuts) != 0 {
		t.Fatalf("%+v", rep)
	}
}

// A Horde leader fights what the gate holds for its team; the chest it loots all the same.
func TestTeamGateRunsForTheHorde(t *testing.T) {
	g, p := lootFixture(t, lootPlan)
	g.team, g.chest = "horde", []objects.Item{medallion()}
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{101, 107}) || g.count(".bot action pullback") != 2 || len(g.opened) != 1 {
		t.Fatalf("targets %v chats %q opened %v", g.targets, g.chats, g.opened)
	}
	if strings.Contains(strings.Join(d.Report().Notes, "\n"), "skipped") {
		t.Fatalf("something was skipped: %v", d.Report().Notes)
	}
}

// A leader whose team is unknown (a game that cannot tell) runs everything, as before the gate existed.
func TestTeamGatePassesWhenTheTeamIsUnknown(t *testing.T) {
	g, p := lootFixture(t, lootPlan)
	g.chest = []objects.Item{medallion()}
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if len(g.targets) != 2 {
		t.Fatalf("targets %v", g.targets)
	}
}

// The step fails clearly when the loot does not bring the item: the chest was empty, or the open failed twice.
func TestLootObjectStepFailures(t *testing.T) {
	g, p := lootFixture(t, lootPlan)
	g.team = "alliance" // the chest opens but holds something else
	g.chest = []objects.Item{{Bag: 255, Slot: 24, Entry: 5, Count: 1}}
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "Gni'kiv Medallion (item 7740) is not in the leader's bags") || d.Report().State != "failed" {
		t.Fatalf("%v", err)
	}

	g, p = lootFixture(t, lootPlan)
	g.team, g.err = "alliance", errors.New("the cast failed (SpellCastResult 43)")
	d = NewDriver(p, g, Config{GM: GMNone}, g.clock())
	err = d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "loot game object") || !strings.Contains(err.Error(), "SpellCastResult 43") || g.tries != 2 {
		t.Fatalf("%v tries %d", err, g.tries)
	}
}

// A leader that carries the item has nothing to fetch: the chest is left alone.
func TestLootObjectStepSkippedWhenCarried(t *testing.T) {
	g, p := lootFixture(t, lootPlan)
	g.team = "alliance"
	g.bag = append(g.bag, medallion())
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if len(g.opened) != 0 || !strings.Contains(strings.Join(d.Report().Notes, "\n"), "carries Gni'kiv Medallion (item 7740) already") {
		t.Fatalf("opened %v notes %v", g.opened, d.Report().Notes)
	}
}

// A game that cannot open objects says so instead of doing nothing.
func TestLootObjectNeedsAnObjectActor(t *testing.T) {
	g, p := lootFixture(t, lootPlan)
	g.fakeGame.units = nil // no pack or boss to fight: the first action is the chest
	d := NewDriver(p, g.fakeGame, Config{GM: GMNone}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "this game cannot open objects") {
		t.Fatalf("%v", err)
	}
}

const usePlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "steps":[{"use_item":{"entry":7740,"name":"Gni'kiv Medallion","requires_item":{"entry":7741,"name":"The Shaft of Tsol"},"expect_item":{"entry":7733,"name":"Staff of Prehistoria"}}},
           {"use_object":{"entry":124371,"requires_item":{"entry":7733,"name":"Staff of Prehistoria"}}}]}]}`

func useFixture(t *testing.T) (*lootGame, *Plan) {
	t.Helper()
	g, p := lootFixture(t, usePlan)
	g.objs = []objects.GameObject{{GUID: keystone, Entry: 124371, X: 20, State: "ready"}}
	g.made = []objects.Item{{Bag: 255, Slot: 24, Entry: 7733, Count: 1}}
	return g, p
}

// The Medallion with the Shaft in the bags makes the Staff, and the Keystone leg's own check accepts it because the run makes it.
func TestUseItemStepMakesTheStaff(t *testing.T) {
	g, p := useFixture(t)
	g.bag = append(g.bag, medallion(), shaft())
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.items) != "[7740]" || len(g.used) != 1 || g.used[0] != keystone || !hasEvent(g.fakeGame, "use_item") {
		t.Fatalf("items %v used %v", g.items, g.used)
	}
}

// Without the Shaft the failure names it, before the item is used.
func TestUseItemStepNamesTheMissingReagent(t *testing.T) {
	g, p := useFixture(t)
	g.bag = append(g.bag, medallion())
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "The Shaft of Tsol (item 7741)") || !strings.Contains(err.Error(), "Gni'kiv Medallion (item 7740)") || len(g.items) != 0 {
		t.Fatalf("%v items %v", err, g.items)
	}
}

// A use that makes nothing fails after the bounded wait, naming the expected item.
func TestUseItemStepFailsWhenTheItemDoesNotCome(t *testing.T) {
	g, p := useFixture(t)
	g.bag = append(g.bag, medallion(), shaft())
	g.made = nil
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "Staff of Prehistoria (item 7733) is not in the leader's bags after 8 s") || len(g.used) != 0 {
		t.Fatalf("%v", err)
	}
}

// With the Staff already carried (a GM gave it) the Medallion is left unused and the Shaft is not asked for.
func TestUseItemStepSkippedWhenTheResultIsCarried(t *testing.T) {
	g, p := useFixture(t)
	g.bag = append(g.bag, medallion(), objects.Item{Entry: 7733, Count: 1})
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if len(g.items) != 0 || len(g.used) != 1 {
		t.Fatalf("items %v used %v", g.items, g.used)
	}
}

// A run that starts at a leg whose step needs the Staff, with neither the Staff nor a step that makes it, fails before it moves.
func TestRequiredItemMadeByAnEarlierStep(t *testing.T) {
	g, p := useFixture(t)
	g.bag = []objects.Item{{Entry: 6948, Count: 1}}
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	err := d.checkRequiredItems(0)
	if err == nil || !strings.Contains(err.Error(), "The Shaft of Tsol (item 7741)") {
		t.Fatalf("the use_item's reagent should be asked for: %v", err)
	}
	p.Legs[0].Steps[0].UseItem.RequiresItem = nil
	if err := d.checkRequiredItems(0); err != nil {
		t.Fatalf("the Staff is made by the use_item: %v", err)
	}
	p.Legs[0].Steps = p.Legs[0].Steps[1:] // only the Keystone is left: nothing makes the Staff
	if err := d.checkRequiredItems(0); err == nil || !strings.Contains(err.Error(), "Staff of Prehistoria") {
		t.Fatalf("%v", err)
	}
}

const bossLootPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "boss":{"entry":7,"position":{"x":90,"y":0,"z":0},"loot_item":{"entry":7741,"name":"The Shaft of Tsol"}}}]}`

// After the kill the leader walks to the corpse, loots it and checks that the Shaft is in its bags.
func TestBossLootItem(t *testing.T) {
	g, p := lootFixture(t, bossLootPlan)
	g.corpse = []objects.Item{shaft()}
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.corpses) != fmt.Sprint([]packet.GUID{bossG}) {
		t.Fatalf("corpses %v", g.corpses)
	}
	if last := g.walks[len(g.walks)-1]; last != (Point{90, 0, 0}) {
		t.Fatalf("the leader did not walk to the corpse: %v", g.walks)
	}
}

// When the Shaft is not in the bags after the loot (a companion took it first) the leg fails and says so.
func TestBossLootItemMissing(t *testing.T) {
	g, p := lootFixture(t, bossLootPlan)
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "The Shaft of Tsol (item 7741) is not in the leader's bags") || !strings.Contains(err.Error(), "a companion may have looted it first") {
		t.Fatalf("%v", err)
	}
}

// A Horde-gated boss with a loot item is skipped whole for the Alliance: no corpse to loot, and the run needs no such item.
func TestGatedBossLootIsSkippedWithTheBoss(t *testing.T) {
	plan := strings.Replace(bossLootPlan, `"position":{"x":90,"y":0,"z":0},`, `"position":{"x":90,"y":0,"z":0},"team":"horde",`, 1)
	g, p := lootFixture(t, plan)
	g.team = "alliance"
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if len(g.corpses) != 0 || len(g.targets) != 0 {
		t.Fatalf("corpses %v targets %v", g.corpses, g.targets)
	}
}

const afterPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "boss":{"entry":7,"position":{"x":90,"y":0,"z":0},"team":"horde"},
  "after_boss":[{"loot_object":{"entry":123329,"near":{"x":60,"y":0,"z":0},"expect_item":{"entry":7740}}}]},
 {"name":"b","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":30,"y":0,"z":0}]}]}]}`

// The steps after the boss run once it is dead: a Horde leader fights it first and loots the chest by its camp after; an Alliance
// leader, for whom the boss is skipped, goes straight to the chest. The leg after them starts only then.
func TestAfterBossStepsRunAfterTheKill(t *testing.T) {
	for _, c := range []struct {
		team    string
		targets int
	}{{"horde", 1}, {"alliance", 0}} {
		g, p := lootFixture(t, afterPlan)
		g.team, g.chest = c.team, []objects.Item{medallion()}
		d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
		if err := d.Run(context.Background(), 0); err != nil {
			t.Fatalf("%s: %v\n%+v", c.team, err, d.Report())
		}
		if fmt.Sprint(g.atLoot) != fmt.Sprint([]int{c.targets}) || len(g.targets) != c.targets+1 {
			t.Fatalf("%s: targets at the loot %v, in all %v", c.team, g.atLoot, g.targets)
		}
	}
	// The label tells an after-boss step from a step.
	g, p := lootFixture(t, afterPlan)
	g.team, g.chest = "alliance", []objects.Item{medallion()}
	if err := NewDriver(p, g, Config{GM: GMNone}, g.clock()).Run(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range g.events {
		found = found || (e.data["action"] == "loot_object" && e.data["ok"] == true)
	}
	if !found {
		t.Fatalf("events %s", g.kinds())
	}
}
