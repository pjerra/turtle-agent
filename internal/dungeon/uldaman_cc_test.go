package dungeon

import (
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
	"slices"
	"testing"
)

// uldamanCreatureType is creature_template.type (1 beast, 4 elemental, 7 humanoid) of every entry the Uldaman plan's crowd-control
// lists name, from the world database of tortoise-wow 5d79486 (sql/base/tw_world_creature_template.sql). Polymorph lands on beasts and humanoids
// only; the plan's cc lists name nothing else, and no_cc names the elementals.
var uldamanCreatureType = map[uint32]struct {
	name string
	typ  int
}{
	4853: {"Stonevault Geomancer", 7}, 4848: {"Shadowforge Darkcaster", 7}, 7321: {"Stonevault Flameweaver", 7},
	7030: {"Shadowforge Geologist", 7}, 7012: {"Earthen Sculptor", 7}, 7397: {"Earthen Stonecarver", 7},
	4852: {"Stonevault Oracle", 7},
	4857: {"Stone Keeper", 4}, 4860: {"Stone Steward", 4},
}

const (
	creatureBeast, creatureHumanoid = 1, 7
)

// Uldaman's run leader used to send no Polymorph order in ten runs: the plan had no cc_priority, cc_always or kill_first (audit F4).
// Every entry of those lists is a creature of one of the plan's pull groups, a beast or humanoid for the Polymorph; no_cc holds the ones it
// cannot take.
func TestUldamanCCListsAreCCAbleUnitsOfPullGroups(t *testing.T) {
	p := loadUldaman(t)
	inGroups := map[uint32]bool{}
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			for _, e := range g.Entries {
				inGroups[e] = true
			}
		}
	}
	if len(p.CCPriority) == 0 || len(p.CCAlways) == 0 || len(p.KillFirst) == 0 || len(p.NoCC) == 0 {
		t.Fatalf("cc_priority %v cc_always %v kill_first %v no_cc %v", p.CCPriority, p.CCAlways, p.KillFirst, p.NoCC)
	}
	for name, es := range map[string][]uint32{"cc_priority": p.CCPriority, "cc_always": p.CCAlways, "kill_first": p.KillFirst} {
		for _, e := range es {
			c, known := uldamanCreatureType[e]
			if !known {
				t.Errorf("%s %d: not in the creature table of this test", name, e)
				continue
			}
			if !inGroups[e] {
				t.Errorf("%s %d (%s): in no pull group of any leg", name, e, c.name)
			}
			if c.typ != creatureHumanoid && c.typ != creatureBeast {
				t.Errorf("%s %d (%s): type %d takes no Polymorph", name, e, c.name, c.typ)
			}
			if slices.Contains(p.NoCC, e) {
				t.Errorf("%s %d (%s): also in no_cc", name, e, c.name)
			}
		}
	}
	for _, e := range p.CCAlways {
		if !slices.Contains(p.CCPriority, e) {
			t.Errorf("cc_always %d is not in cc_priority", e)
		}
		if !slices.Contains(p.Fleers, e) { // the reason for sheeping it in any pack: it is a caster that runs at 15 % and drags the next group in
			t.Errorf("cc_always %d is not one of the plan's fleers", e)
		}
	}
	for _, e := range p.NoCC {
		c := uldamanCreatureType[e]
		if c.typ == creatureHumanoid || c.typ == creatureBeast || c.name == "" {
			t.Errorf("no_cc %d (%s): type %d takes a Polymorph", e, c.name, c.typ)
		}
		if !inGroups[e] {
			t.Errorf("no_cc %d (%s): in no pull group", e, c.name)
		}
	}
	// The cc bot's pick: the Geomancers and Flameweavers are taken before the Earthen Sculptors; the healers are killed, never sheeped.
	if p.CCPriority[0] != 4853 || slices.Contains(p.CCPriority, 4852) {
		t.Errorf("cc_priority %v", p.CCPriority)
	}
}

// The cc bot's pick on Uldaman's real plan: a Grimlok-corridor pack of a Geomancer and two Brawlers is sheeped on the Geomancer even below
// big_pack (cc_always), never on the Brawlers; a pack of Stone Stewards (elementals) gives no pick; the healer Oracle is never sheeped.
func TestUldamanPackCCPicksTheCasterAndNeverTheElemental(t *testing.T) {
	d := ledgerDriver(t)
	d.plan = loadPlan(t, uldamanPlan)
	mk := func(guid uint64, entry uint32, x float32) tbm.HostileRec {
		return tbm.HostileRec{GUID: packet.GUID(0xF130000000000000 | guid), Entry: entry, HPPct: 100, Flags: "-", X: x}
	}
	target := mk(1, 4855, 0)
	pick, ok := d.ccPick(target, []tbm.HostileRec{target, mk(2, 4855, 30), mk(3, 4853, 5)}, true)
	if !ok || pick.Entry != 4853 {
		t.Fatalf("pick %+v %v", pick, ok)
	}
	stew := mk(4, 4860, 0)
	if _, ok := d.ccPick(stew, []tbm.HostileRec{stew, mk(5, 4860, 9), mk(6, 4857, 9)}, false); ok {
		t.Error("an elemental was picked")
	}
	if d.ccAllowed(mk(7, 4852, 0)) {
		t.Error("the Oracle (kill_first) may be sheeped")
	}
}
