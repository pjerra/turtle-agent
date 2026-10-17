package dungeon

import "fmt"

// Keyed objects: a use_object on a locked game object does nothing for a bot that does not carry the key ("no-item", zf-r5: Troll Cage
// 141073 needs the Executioner's Key, dropped by the Sandfury Executioner the leg kills, and nothing in the leg looted it; the run died at the
// cage). The plan check (CheckPlan, `plan-check`) and a test over the shipped plans look for the two halves of that gap offline (KeyGaps):
//
//   - a use_object on an object of objectLockItems (the objects whose lock is known to name an item) must say so in requires_item;
//   - every requires_item of a use_object or use_item must come from somewhere: an earlier step or boss loot_item in the plan order (a
//     loot_object's or use_item's expect_item, a boss's loot_item), or be the item of the leg's own `optional` block (an item no honest run
//     carries; the leg is skipped without it).
//
// The plan order is the run's: per leg the steps, then the boss (and its loot_item), then after_boss. The loader does not apply the rules:
// small plans of the tests and the run-leader export use locked objects without their sources.

// objectLockItems maps the game objects whose lock needs an item to that item (gameobject_template data0 -> Lock.dbc). It holds what the
// plans use; add an object when a plan uses a locked one.
var objectLockItems = map[uint32]ItemRef{
	// Zul'Farrak's Troll Cages (lock 420, the stairs event): the Sandfury Executioner (7274) drops the key, 100 %.
	141070: {Entry: 8444, Name: "Executioner's Key"},
	141071: {Entry: 8444, Name: "Executioner's Key"},
	141072: {Entry: 8444, Name: "Executioner's Key"},
	141073: {Entry: 8444, Name: "Executioner's Key"},
	141074: {Entry: 8444, Name: "Executioner's Key"},
	// Uldaman's Keystone (lock 359): the Staff of Prehistoria, made from the Shaft of Tsol and the Gni'kiv Medallion.
	124371: {Entry: 7733, Name: "Staff of Prehistoria"},
}

// KeyGap is one step whose key (or item) the plan does not provide.
type KeyGap struct {
	Leg     int
	LegName string
	Path    string // "steps[0]" or "after_boss[1]" of the leg
	At      Point  // where the object is, when the step says
	Why     string
}

func (g KeyGap) String() string {
	return fmt.Sprintf("leg %d (%s) %s: %s", g.Leg, g.LegName, g.Path, g.Why)
}

// KeyGaps lists the keyed objects and item needs of the plan that nothing provides, from leg `from` on (what the earlier legs make counts).
func (p *Plan) KeyGaps(from int) []KeyGap {
	var out []KeyGap
	made := map[uint32]bool{}
	for li, l := range p.Legs {
		if l.Optional != nil && l.Optional.Item != 0 {
			made[l.Optional.Item] = true
		}
		check := func(path string, st Step) {
			var needs []ItemRef
			var at Point
			add := func(why string) {
				if li >= from {
					out = append(out, KeyGap{Leg: li, LegName: l.Name, Path: path, At: at, Why: why})
				}
			}
			switch {
			case st.UseObject != nil:
				u := st.UseObject
				if u.Near != nil {
					at = *u.Near
				}
				if want, ok := objectLockItems[u.Entry]; ok && (u.RequiresItem == nil || u.RequiresItem.Entry != want.Entry) {
					add(fmt.Sprintf("object %d has a lock that needs %s: add requires_item for it (a bot without the key is refused \"no-item\")", u.Entry, want))
				}
				if u.RequiresItem != nil {
					needs = append(needs, *u.RequiresItem)
				}
			case st.UseItem != nil && st.UseItem.RequiresItem != nil:
				needs = append(needs, *st.UseItem.RequiresItem)
			}
			for _, n := range needs {
				if !made[n.Entry] {
					add(fmt.Sprintf("needs %s, which no step before it makes: add a boss loot_item, a loot_object or use_item with that expect_item, or an optional leg for it", n))
				}
			}
			switch {
			case st.LootObject != nil && st.LootObject.ExpectItem != nil:
				made[st.LootObject.ExpectItem.Entry] = true
			case st.UseItem != nil && st.UseItem.ExpectItem != nil:
				made[st.UseItem.ExpectItem.Entry] = true
			}
		}
		for si, st := range l.Steps {
			check(fmt.Sprintf("steps[%d]", si), st)
		}
		if l.Boss != nil && l.Boss.LootItem != nil {
			made[l.Boss.LootItem.Entry] = true
		}
		for si, st := range l.AfterBoss {
			check(fmt.Sprintf("after_boss[%d]", si), st)
		}
	}
	return out
}
