package raidwalk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/tbm"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

func TestParseGMClear(t *testing.T) {
	sp, err := ParseGMClear("2:1,3, 5")
	if err != nil || sp.Leg != "2" || len(sp.Groups) != 3 || sp.Groups[2] != 5 || sp.Boss {
		t.Fatalf("%+v, %v", sp, err)
	}
	sp, err = ParseGMClear("kurinnaxx:2")
	if err != nil || sp.Leg != "kurinnaxx" || len(sp.Groups) != 1 {
		t.Fatalf("%+v, %v", sp, err)
	}
	for _, bad := range []string{"", "2", "2:", ":1", "2:0", "2:-1", "2:a", "2:1,1", "2:1,,2"} {
		if _, err := ParseGMClear(bad); err == nil {
			t.Errorf("ParseGMClear(%q) accepted", bad)
		}
	}
	b, err := ParseGMClearBoss("kurinnaxx")
	if err != nil || !b.Boss || b.Leg != "kurinnaxx" || len(b.Groups) != 0 {
		t.Fatalf("%+v, %v", b, err)
	}
	for _, bad := range []string{"", " ", "1:2", "1,2"} {
		if _, err := ParseGMClearBoss(bad); err == nil {
			t.Errorf("ParseGMClearBoss(%q) accepted", bad)
		}
	}
}

// gmWorld has pack {1, 2, 3}: spawn 1 stands at the row, spawn 2 is its aggro-link slave 300 yd away, spawn 3 is the slave of 2 (a chain);
// spawn 4 has the row's entry and stands 200 yd off (another pack); spawn 5 is a friendly (faction 35) of the pack's entry.
func gmWorld() *worldspawn.World {
	at := func(x float32) dungeon.Point { return dungeon.Point{X: x, Y: 0, Z: 0} }
	return &worldspawn.World{Map: 509,
		Spawns: []worldspawn.Spawn{
			{GUID: 1, Entry: 100, Faction: 16, DetectionYd: 20, Pos: at(0)},
			{GUID: 2, Entry: 200, Faction: 16, DetectionYd: 20, Pos: at(300)},
			{GUID: 3, Entry: 300, Faction: 16, DetectionYd: 20, Pos: at(310)},
			{GUID: 4, Entry: 100, Faction: 16, DetectionYd: 20, Pos: at(-200)},
			{GUID: 5, Entry: 100, Faction: 35, DetectionYd: 20, Pos: at(2)},
		},
		Links: []worldspawn.Link{{Slave: 2, Master: 1, Flag: 1}, {Slave: 3, Master: 2, Flag: 2}},
	}
}

func gmRows() planexport.Rows {
	return planexport.Rows{
		Legs: []planexport.LegRow{{Leg: 0, Name: "first"}, {Leg: 1, Name: "Second"}},
		Steps: []planexport.StepRow{
			{Leg: 0, Seq: 1, Kind: planexport.KindMove, X: 1, Y: 1, Z: 1},
			{Leg: 1, Seq: 1, Kind: planexport.KindMove, X: 1, Y: 1, Z: 1},
			{Leg: 1, Seq: 2, Kind: planexport.KindPull, Entries: []uint32{100}, Radius: 10, X: 0, Y: 0, Z: 0.5},
			{Leg: 1, Seq: 3, Kind: planexport.KindPull, Entries: []uint32{100}, Radius: 10, X: -200, Y: 0, Z: 0.5},
			{Leg: 1, Seq: 4, Kind: planexport.KindBoss, Entry: 300, Entries: []uint32{300}, Radius: 10, X: 310, Y: 0, Z: 0.5},
		},
	}
}

// A group expands to its whole pack: the linked unit 300 yd away and the unit linked to that one come with it, the same-entry unit of
// another pack and the friendly do not.
func TestPlanGMClearExpandsToWholeLinkedPack(t *testing.T) {
	packs, _, err := PlanGMClear(gmRows(), gmWorld(), []GMClearSpec{{Leg: "Second", Groups: []int{1}}})
	if err != nil || len(packs) != 1 {
		t.Fatalf("%+v, %v", packs, err)
	}
	p := packs[0]
	if p.Leg != "Second" || p.Group != "1" || p.ID != 1 {
		t.Fatalf("pack %+v", p)
	}
	if len(p.Members) != 3 || p.Members[1] != 100 || p.Members[2] != 200 || p.Members[3] != 300 {
		t.Fatalf("members %v, want spawns 1, 2 and 3", p.Members)
	}
	if _, ok := p.Members[4]; ok {
		t.Error("another pack's unit is a member")
	}
	if _, ok := p.Members[5]; ok {
		t.Error("a friendly is a member")
	}
	// Leg by number, group 2 is the other pack, and a second spec naming a pack again lists it once.
	packs, _, err = PlanGMClear(gmRows(), gmWorld(), []GMClearSpec{{Leg: "1", Groups: []int{2, 1}}, {Leg: "second", Groups: []int{1}}})
	if err != nil || len(packs) != 2 || packs[0].Group != "2" || packs[0].ID != 4 || packs[1].ID != 1 {
		t.Fatalf("%+v, %v", packs, err)
	}
}

// The boss form finds the boss row's pack: the boss (entry 300) belongs to the linked chain, so the whole chain is killed.
func TestPlanGMClearBossTakesItsPack(t *testing.T) {
	packs, _, err := PlanGMClear(gmRows(), gmWorld(), []GMClearSpec{{Leg: "Second", Boss: true}})
	if err != nil || len(packs) != 1 || packs[0].Group != GMClearBoss || len(packs[0].Members) != 3 {
		t.Fatalf("%+v, %v", packs, err)
	}
}

func TestPlanGMClearRefusals(t *testing.T) {
	for _, tc := range []struct {
		spec GMClearSpec
		want string
	}{
		{GMClearSpec{Leg: "nosuch", Groups: []int{1}}, "no leg nosuch"},
		{GMClearSpec{Leg: "Second", Groups: []int{3}}, "has 2 pull groups, not 3"},
		{GMClearSpec{Leg: "first", Groups: []int{1}}, "has 0 pull groups"},
		{GMClearSpec{Leg: "first", Boss: true}, "has no boss row"},
	} {
		if _, _, err := PlanGMClear(gmRows(), gmWorld(), []GMClearSpec{tc.spec}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v (want %q)", tc.spec, err, tc.want)
		}
	}
	empty := &worldspawn.World{Map: 509}
	packs, warns, err := PlanGMClear(gmRows(), empty, []GMClearSpec{{Leg: "Second", Groups: []int{1}}})
	if err != nil || len(packs) != 0 || len(warns) != 1 ||
		!strings.Contains(warns[0], "WARN: skipped") || !strings.Contains(warns[0], "group 1") {
		t.Errorf("empty world: packs %v, warnings %v, err %v", packs, warns, err)
	}
}

// A group with no spawn is skipped with a warning, and the groups after it are still planned: the walk never aborts
// because one group of the leg is a boss's or an event's (walk 31: Mandokir group 18, the Vilebranch Speaker).
func TestPlanGMClearSkipsAGroupWithNoSpawn(t *testing.T) {
	world := gmWorld()
	world.Spawns = world.Spawns[:3] // spawn 4, the second pack's only member, is gone
	packs, warns, err := PlanGMClear(gmRows(), world, []GMClearSpec{{Leg: "Second", Groups: []int{2, 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].Group != "1" || packs[0].ID != 1 {
		t.Fatalf("packs %+v, want group 1's pack", packs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "WARN: skipped") ||
		!strings.Contains(warns[0], "group 2") || !strings.Contains(warns[0], "step 4") {
		t.Fatalf("warnings %v", warns)
	}
}

func unitGUID(entry, spawn uint32) packet.GUID {
	return packet.GUID(uint64(packet.HighUnit)<<48 | uint64(entry)<<24 | uint64(spawn))
}

// LiveMembers takes the live creatures whose spawn guid AND entry are the pack's: a corpse, a player, another spawn of the same entry and
// a unit with a member's spawn number but another entry are left alone.
func TestGMClearLiveMembers(t *testing.T) {
	packs, _, err := PlanGMClear(gmRows(), gmWorld(), []GMClearSpec{{Leg: "Second", Groups: []int{1}}})
	if err != nil {
		t.Fatal(err)
	}
	units := []objects.Unit{
		{GUID: unitGUID(300, 3)},
		{GUID: unitGUID(100, 1)},
		{GUID: unitGUID(200, 2), Dead: true},
		{GUID: unitGUID(100, 4)},                               // another pack
		{GUID: unitGUID(999, 2)},                               // member's spawn number, wrong entry
		{GUID: packet.GUID(uint64(packet.HighPlayer)<<48 | 1)}, // a player
	}
	got := packs[0].LiveMembers(units)
	if len(got) != 2 || got[0].GUID != unitGUID(100, 1) || got[1].GUID != unitGUID(300, 3) {
		t.Fatalf("%+v", got)
	}
	if SpawnGUID(unitGUID(100, 1)) != 1 {
		t.Error("SpawnGUID")
	}
}

func assistedMeta() SummaryMeta {
	return SummaryMeta{Plan: "aq20", Map: 509, Server: "m910q", GMClearUsed: true, GMClearLegs: []string{"Second"},
		GMClear: []GMClearEntry{
			{Leg: "Second", Group: "1", Entry: 100, GUID: unitGUID(100, 1).String(), Killed: true, Tries: 1},
			{Leg: "Second", Group: "boss", Entry: 300, GUID: unitGUID(300, 3).String(), Killed: false, Tries: 2, Reason: GMClearAlive},
		}}
}

// The summary of a walk that used --gm-clear says assisted, lists every unit, marks the leg, and its text line says ASSISTED.
func TestSummaryMarksAssisted(t *testing.T) {
	s := BuildSummary(nil, assistedMeta())
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back["gm_clear_partial"] != true {
		t.Fatalf("gm_clear_partial missing: %s", raw)
	}
	if back["assisted"] != true {
		t.Fatalf("assisted missing: %s", raw)
	}
	list, _ := back["gm_clear"].([]any)
	if len(list) != 2 {
		t.Fatalf("gm_clear %v", back["gm_clear"])
	}
	first := list[0].(map[string]any)
	if first["leg"] != "Second" || first["group"] != "1" || first["killed"] != true || first["guid"] == "" {
		t.Fatalf("entry %v", first)
	}
	txt := s.Text()
	head := strings.SplitN(txt, "\n", 2)[0]
	if !strings.HasSuffix(head, " ASSISTED PARTIAL") {
		t.Fatalf("first line %q", head)
	}
	if !strings.Contains(txt, "ASSISTED: --gm-clear killed 1 units by GM command (Second); this is not a real clear; PARTIAL: 1 members left (0 not seen, 1 alive)") {
		t.Fatalf("summary.txt:\n%s", txt)
	}
	// Without the tool: no assisted, no gm_clear, no ASSISTED word.
	plain := BuildSummary(nil, SummaryMeta{Plan: "aq20"})
	raw, _ = json.Marshal(plain)
	if strings.Contains(string(raw), "assisted") || strings.Contains(string(raw), "gm_clear") || strings.Contains(plain.Text(), "ASSISTED") {
		t.Fatalf("plain summary mentions gm-clear:\n%s\n%s", raw, plain.Text())
	}
}

// The legs the tool killed in carry the assisted mark in summary.legs; the others do not.
func TestSummaryMarksAssistedLegs(t *testing.T) {
	tr := NewTracker(nil, nil)
	now := time.Now()
	tr.Feed(now, tbm.RunStatus{Plan: "aq20", State: tbm.RunRunning, Step: 1, Steps: 4, Leg: "first", Kind: "move"})
	tr.Feed(now.Add(10*time.Second), tbm.RunStatus{Plan: "aq20", State: tbm.RunRunning, Step: 2, Steps: 4, Leg: "Second", Kind: "pull"})
	tr.Feed(now.Add(20*time.Second), tbm.RunStatus{Plan: "aq20", State: tbm.RunRunning, Step: 3, Steps: 4, Leg: "Second", Kind: "pull"})
	s := BuildSummary(tr, assistedMeta())
	marks := map[string]bool{}
	for _, l := range s.Legs {
		marks[l.Leg] = l.Assisted
	}
	if len(s.Legs) != 2 || marks["first"] || !marks["Second"] {
		t.Fatalf("legs %+v", s.Legs)
	}
}

// A used --gm-clear that found nothing is still assisted.
func TestSummaryAssistedWithoutKills(t *testing.T) {
	s := BuildSummary(nil, SummaryMeta{Plan: "aq20", GMClearUsed: true})
	if !s.Assisted || !strings.Contains(s.Text(), "ASSISTED: --gm-clear killed 0 units") {
		t.Fatalf("%v\n%s", s.Assisted, s.Text())
	}
}

func TestReportMarksAssisted(t *testing.T) {
	m := assistedMeta()
	rep := BuildReport(nil, nil, ReportOptions{GMClear: m.GMClear, Assisted: true})
	md := rep.Markdown()
	if !rep.Assisted || !strings.Contains(md, "**ASSISTED: --gm-clear killed 1 units") || !strings.Contains(md, "| Second | boss | 300 |") || !strings.Contains(md, "| NO | 2 | alive |") || !strings.Contains(md, "PARTIAL: 1 members left") {
		t.Fatalf("report:\n%s", md)
	}
	js, err := rep.JSON()
	if err != nil || !strings.Contains(string(js), `"assisted": true`) || !strings.Contains(string(js), `"gm_clear"`) {
		t.Fatalf("%v\n%s", err, js)
	}
	plain := BuildReport(nil, nil, ReportOptions{})
	if strings.Contains(plain.Markdown(), "ASSISTED") {
		t.Fatal("a plain report says ASSISTED")
	}
}

// A pack wider than the commander's view gets one stop per cluster, at member spawns; a compact pack gets one.
func TestGMClearStops(t *testing.T) {
	pt := func(x float32) dungeon.Point { return dungeon.Point{X: x} }
	compact := GMClearPack{Members: map[uint64]uint32{1: 1, 2: 1, 3: 1}, Positions: map[uint64]dungeon.Point{1: pt(0), 2: pt(60), 3: pt(-90)}}
	if st := compact.Stops(); len(st) != 1 || st[0] != pt(0) {
		t.Fatalf("compact stops %v", st)
	}
	wide := GMClearPack{Members: map[uint64]uint32{1: 1, 2: 1, 3: 1, 4: 1}, Positions: map[uint64]dungeon.Point{1: pt(0), 2: pt(50), 3: pt(400), 4: pt(430)}}
	st := wide.Stops()
	if len(st) != 2 || st[0] != pt(0) || st[1] != pt(400) {
		t.Fatalf("wide stops %v", st)
	}
	if g := wide.SpawnGUIDs(); len(g) != 4 || g[0] != 1 || g[3] != 4 {
		t.Fatalf("guids %v", g)
	}
}

// SeenMembers keeps corpses (the caller decides), LiveMembers drops them.
func TestGMClearSeenMembersKeepsCorpses(t *testing.T) {
	packs, _, err := PlanGMClear(gmRows(), gmWorld(), []GMClearSpec{{Leg: "Second", Groups: []int{1}}})
	if err != nil {
		t.Fatal(err)
	}
	units := []objects.Unit{{GUID: unitGUID(100, 1)}, {GUID: unitGUID(200, 2), Dead: true}, {GUID: unitGUID(100, 4)}}
	if got := packs[0].SeenMembers(units); len(got) != 2 {
		t.Fatalf("seen %+v", got)
	}
	if got := packs[0].LiveMembers(units); len(got) != 1 || got[0].GUID != unitGUID(100, 1) {
		t.Fatalf("live %+v", got)
	}
}

// The ASSISTED line names the groups that were not cleared, with a leg's consecutive group numbers compressed: walk
// 32's failing groups read "high_priestess_arlokk:12-17", "hakkar_outer:6-22" and "high_priestess_marli:1-6".
func TestGMClearUnclearedGroups(t *testing.T) {
	var es []GMClearEntry
	for _, g := range []string{"12", "13", "14", "15", "16", "17", "19"} {
		es = append(es, GMClearEntry{Leg: "high_priestess_arlokk", Group: g, GUID: "spawn 1", Reason: GMClearAlive})
	}
	es = append(es,
		GMClearEntry{Leg: "high_priestess_marli", Group: "1", GUID: "spawn 2", Reason: GMClearAlive},
		GMClearEntry{Leg: "high_priestess_marli", Group: "1", GUID: "spawn 3", Reason: GMClearAlive},
		GMClearEntry{Leg: "hakkar_outer", Group: GMClearBoss, GUID: "spawn 4", Reason: GMClearNotSeen},
		// A killed member's group is not listed.
		GMClearEntry{Leg: "hakkar_outer", Group: "6", Killed: true},
	)
	got := GMClearUncleared(es)
	want := []string{"hakkar_outer:boss", "high_priestess_arlokk:12-17,19", "high_priestess_marli:1"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("uncleared %v, want %v", got, want)
	}
	line := GMClearSummaryLine(es, []string{"high_priestess_arlokk", "high_priestess_marli", "hakkar_outer"})
	if !strings.Contains(line, "groups not cleared: hakkar_outer:boss, high_priestess_arlokk:12-17,19, high_priestess_marli:1") {
		t.Fatalf("line %q", line)
	}
	if got := GMClearUncleared([]GMClearEntry{{Leg: "x", Group: "1", Killed: true}}); len(got) != 0 {
		t.Fatalf("a fully killed clear lists %v", got)
	}
}
