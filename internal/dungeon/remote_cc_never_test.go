package dungeon

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The CC never takes a boss, a kill_first entry, a no_cc entry (undead) or a pet's GUID (live 2026-10-15: Velratha sheeped 8 times, the Witch
// Doctor 5650 ten times against the plan, Zombies and Dead Heroes nine times for one cast).
func TestCCNeverTakesABossAKillFirstEntryUndeadOrAPet(t *testing.T) {
	d := ledgerDriver(t)
	d.plan.CCPriority, d.plan.CCAlways = []uint32{7246, 7247, 5648}, []uint32{7246}
	d.plan.KillFirst, d.plan.NoCC = []uint32{8179, 5650, 7247}, []uint32{7286, 7276}
	d.plan.Legs = []Leg{{Name: "b", Boss: &Boss{Entry: 7795, First: []uint32{8130}, Position: Point{1, 0, 0}}}}
	d.plan.CCPriority = []uint32{7246, 5648}
	mk := func(guid uint64, entry uint32) tbm.HostileRec {
		return tbm.HostileRec{GUID: packet.GUID(0xF130000000000000 | guid), Entry: entry, HPPct: 100, Flags: "-"}
	}
	pet := tbm.HostileRec{GUID: packet.GUID(0xF140000000000009), Entry: 1234, HPPct: 100, Flags: "-"}
	for name, h := range map[string]tbm.HostileRec{"boss": mk(1, 7795), "kill_first": mk(3, 5650), "kill_first healer": mk(4, 7247), "undead": mk(5, 7286), "undead hero": mk(6, 7276), "pet": pet} {
		if d.ccAllowed(h) {
			t.Errorf("%s may be crowd-controlled", name)
		}
	}
	for name, h := range map[string]tbm.HostileRec{"Shadowhunter": mk(7, 7246), "Shadowcaster": mk(8, 5648), "Blood Drinker": mk(9, 5649)} {
		if !d.ccAllowed(h) {
			t.Errorf("%s may not be crowd-controlled", name)
		}
	}
	// A boss's `first` add is no boss: a pack with it (Uldaman's Grimlok and Galgann packs) keeps its CC.
	if !d.ccAllowed(mk(2, 8130)) {
		t.Error("a boss.first add lost its CC")
	}
	// ccPick: a pack of a Witch Doctor, two Zombies and a Blood Drinker takes the Blood Drinker (the only one allowed), never the Witch
	// Doctor or the undead, whatever the distances.
	target := mk(10, 5649)
	target.X = 0
	pack := []tbm.HostileRec{target, mk(11, 5650), mk(12, 7286), mk(13, 7276), mk(14, 5649)}
	pack[1].X, pack[2].X, pack[3].X, pack[4].X = 40, 30, 35, 5
	pick, ok := d.ccPick(target, pack, false)
	if !ok || pick.Entry != 5649 || pick.GUID == target.GUID {
		t.Fatalf("%+v %v", pick, ok)
	}
	// Nothing allowed in the pack: no pick (the pack is pulled as it is).
	if _, ok := d.ccPick(target, pack[:4], false); ok {
		t.Fatal("a pick from a pack of kill_first and undead")
	}
}

// Uldaman's packs that hold a boss's `first` adds (Grimlok's, Galgann's) keep their CC.
func TestCCKeepsUldamanBossFirstAdds(t *testing.T) {
	d := ledgerDriver(t)
	d.plan = loadPlan(t, "../../dungeons/uldaman.json")
	n := 0
	for _, l := range d.plan.Legs {
		if l.Boss == nil {
			continue
		}
		for _, e := range l.Boss.First {
			n++
			if !d.ccAllowed(tbm.HostileRec{GUID: packet.GUID(0xF130000000000000 | uint64(e)), Entry: e, HPPct: 100}) {
				t.Errorf("%s: the add %d of boss %d lost its CC", l.Name, e, l.Boss.Entry)
			}
		}
	}
	if n == 0 {
		t.Fatal("Uldaman has no boss.first entry to check")
	}
}
