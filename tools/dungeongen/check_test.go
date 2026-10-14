package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func uldamanCreatures(t *testing.T) []creature {
	t.Helper()
	cs, err := readCreatures(filepath.Join("testdata", "uldaman", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func planWith(t *testing.T, boss string) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Parse("t.json", []byte(`{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"baelog","to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[4852],"positions":[{"x":1,"y":0,"z":0}]}],"boss":{"entry":6906,"position":{"x":1,"y":0,"z":0},"first":[6907,6908]`+boss+`}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The Lost Dwarves (6906-6908) carry npc_flags 2 and faction 122 in the export: an ungated boss is warned about for each of them, a
// boss gated to the Horde is not looked at for the Alliance, and the Alliance-friendly faction is named when the caller says so.
func TestCheckWarnsAboutFriendlyBosses(t *testing.T) {
	cs := uldamanCreatures(t)
	ws := Check(planWith(t, ""), cs, CheckOptions{Team: "alliance", Friendly: map[int]bool{122: true}})
	if len(ws) != 3 {
		t.Fatalf("%q", ws)
	}
	for i, e := range []string{"6906", "6907", "6908"} {
		if !strings.Contains(ws[i], "("+e+")") || !strings.Contains(ws[i], "npc_flags 2 (questgiver): it is an NPC, not a mob") || !strings.Contains(ws[i], "faction 122 is friendly to the alliance") {
			t.Errorf("%s: %s", e, ws[i])
		}
	}
	if !strings.Contains(ws[0], "legs[0] (baelog).boss: Baelog") || !strings.Contains(ws[1], ".boss.first: Eric") {
		t.Fatalf("%q", ws)
	}
	// Without the friendly list the npc_flags still say it.
	ws = Check(planWith(t, ""), cs, CheckOptions{})
	if len(ws) != 3 || strings.Contains(ws[0], "friendly to") {
		t.Fatalf("%q", ws)
	}
	// Gated to the horde: nothing to say for an Alliance leader; a Horde one is meant to fight them, so their flags are not repeated, but a
	// faction the caller names friendly to the Horde is.
	if ws := Check(planWith(t, `,"team":"horde"`), cs, CheckOptions{Team: "alliance", Friendly: map[int]bool{122: true}}); len(ws) != 0 {
		t.Fatalf("a gated boss was looked at: %q", ws)
	}
	if ws := Check(planWith(t, `,"team":"horde"`), cs, CheckOptions{Team: "horde"}); len(ws) != 0 {
		t.Fatalf("%q", ws)
	}
	if ws := Check(planWith(t, `,"team":"horde"`), cs, CheckOptions{Team: "horde", Friendly: map[int]bool{122: true}}); len(ws) != 3 || strings.Contains(ws[0], "npc_flags") {
		t.Fatalf("%q", ws)
	}
}

// The committed plan has no warning for either team: the Lost Dwarves are gated, the other bosses and packs are mobs.
func TestCheckUldamanPlanIsClean(t *testing.T) {
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "uldaman.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Faction 122 is the Ironforge dwarves': friendly to the Alliance, not to the Horde.
	for team, friendly := range map[string]map[int]bool{"alliance": {122: true}, "horde": nil} {
		if ws := Check(p, uldamanCreatures(t), CheckOptions{Team: team, Friendly: friendly, Awakened: uldamanAwakened}); len(ws) != 0 {
			t.Errorf("%s: %q", team, ws)
		}
	}
	// The Stone Keepers and Archaedas spawn friendly (faction 35) and the altars' scripts wake them: without -awakened they are warned about.
	ws := Check(p, uldamanCreatures(t), CheckOptions{Team: "alliance", Friendly: map[int]bool{122: true}})
	if len(ws) != 2 || !strings.Contains(ws[0], "Stone Keeper (4857)") || !strings.Contains(ws[1], "Archaedas (2748)") || !strings.Contains(ws[1], "faction 35 is friendly") {
		t.Errorf("%q", ws)
	}
}

// The two Uldaman entries that spawn friendly and are woken by the altars' scripts (instance_uldaman.cpp, boss_archaedas.cpp).
var uldamanAwakened = map[uint32]bool{4857: true, 2748: true}

// A faction used by NPCs with npc_flags is noted as possibly friendly when the caller does not say; a mob's own faction alone is not.
func TestCheckNotesNPCFactions(t *testing.T) {
	cs := []creature{
		{Entry: 1, Name: "Guard", Faction: 12, NPCFlags: 0},
		{Entry: 2, Name: "Vendor", Faction: 12, NPCFlags: 128},
		{Entry: 3, Name: "Troll", Faction: 59},
	}
	p, err := dungeon.Parse("t.json", []byte(`{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1,3],"positions":[{"x":0,"y":0,"z":0},{"x":1,"y":0,"z":0}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ws := Check(p, cs, CheckOptions{Team: "horde"})
	if len(ws) != 1 || !strings.Contains(ws[0], "Guard (1)") || !strings.Contains(ws[0], "also used by NPCs with npc_flags (Vendor)") || !strings.Contains(ws[0], "FactionTemplate.dbc") {
		t.Fatalf("%q", ws)
	}
}

// Generate puts a warning in the leg notes for a boss that is an NPC or in a faction the caller named friendly.
func TestGenerateWarnsAboutBossNPCs(t *testing.T) {
	o := Options{Name: "uldaman", Map: 70, Bosses: []uint32{6906, 6910}, Cluster: 12, NoteRange: 20, IgnoreFactions: map[int]bool{35: true}, Friendly: map[int]bool{122: true}}
	p, err := run(filepath.Join("testdata", "uldaman"), o)
	if err != nil {
		t.Fatal(err)
	}
	n := p.Legs[0].Notes
	if len(n) < 2 || !strings.Contains(n[0], "WARNING: boss 6906 (Baelog) has npc_flags 2 (questgiver)") || !strings.Contains(n[1], "faction 122") {
		t.Fatalf("%q", n)
	}
	for _, x := range p.Legs[1].Notes {
		if strings.Contains(x, "WARNING") {
			t.Fatalf("Revelosh is a mob: %q", p.Legs[1].Notes)
		}
	}
}

// -strict turns warnings into exit status 1; a clean plan stays 0, and without it warnings do not fail.
func TestCheckFileStrict(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "p.json")
	if err := os.WriteFile(plan, []byte(`{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"boss":{"entry":6906,"position":{"x":1,"y":0,"z":0}}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join("testdata", "uldaman")
	if got := checkFile(plan, data, "alliance", "122", "", false); got != 0 {
		t.Fatalf("warnings without -strict: %d", got)
	}
	if got := checkFile(plan, data, "alliance", "122", "", true); got != 1 {
		t.Fatalf("warnings with -strict: %d", got)
	}
	clean := filepath.Join("..", "..", "dungeons", "uldaman.json")
	if got := checkFile(clean, data, "alliance", "122", "2748,4857", true); got != 0 {
		t.Fatalf("the committed plan: %d", got)
	}
}
