package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

const gmClearZGSpawns = "../../tools/dungeongen/testdata/map309"

// gmClearArgs are the flags of a placed walk that a --gm-clear test adds to.
func gmClearArgs(extra ...string) []string {
	return append([]string{"zul_gurub", "--place", "--from", "leg high_priest_thekal", "--sql", "walk.sql"}, extra...)
}

func TestParseGMClearFlags(t *testing.T) {
	var stderr bytes.Buffer
	o, code := parseRaidWalkArgs(gmClearArgs("--gm-clear", "2:1,3", "--gm-clear", "high_priestess_marli:2", "--gm-clear-boss", "2"), &stderr)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if len(o.gmClear) != 2 || o.gmClear[0] != "2:1,3" || len(o.gmClearBoss) != 1 || o.gmClearBoss[0] != "2" {
		t.Fatalf("options %+v", o)
	}
	specs, err := parseGMClearSpecs(o.gmClear, o.gmClearBoss)
	if err != nil || len(specs) != 3 || specs[0].Leg != "2" || len(specs[0].Groups) != 2 || !specs[2].Boss {
		t.Fatalf("specs %+v, err %v", specs, err)
	}
	// Off by default: no flag, no spec.
	o, code = parseRaidWalkArgs(gmClearArgs(), &stderr)
	if code != exitOK || len(o.gmClear) != 0 || len(o.gmClearBoss) != 0 {
		t.Fatalf("default options %+v exit %d", o, code)
	}
}

func TestParseGMClearRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"production server", gmClearArgs("--server", "tortoise", "--gm-clear", "2:1"), "test servers only"},
		{"production server, boss form", gmClearArgs("--server", "tortoise", "--gm-clear-boss", "2"), "test servers only"},
		{"no place", []string{"zul_gurub", "--gm-clear", "2:1"}, "needs --place"},
		{"no group", gmClearArgs("--gm-clear", "2"), "want \"<leg>:<group>"},
		{"empty groups", gmClearArgs("--gm-clear", "2:"), "want \"<leg>:<group>"},
		{"group zero", gmClearArgs("--gm-clear", "2:0"), "not a pull group number"},
		{"group word", gmClearArgs("--gm-clear", "2:x"), "not a pull group number"},
		{"group twice", gmClearArgs("--gm-clear", "2:1,1"), "named twice"},
		{"boss with groups", gmClearArgs("--gm-clear-boss", "2:1"), "want a leg number or name"},
	} {
		var stderr bytes.Buffer
		if _, code := parseRaidWalkArgs(tc.args, &stderr); code != exitUsage || !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("%s: exit %d, stderr %q (want %q)", tc.name, code, stderr.String(), tc.want)
		}
	}
	// Both test servers pass.
	for _, server := range []string{"m910q", "tortoise-test2"} {
		var stderr bytes.Buffer
		if _, code := parseRaidWalkArgs(gmClearArgs("--server", server, "--gm-clear", "2:1"), &stderr); code != exitOK {
			t.Errorf("%s: exit %d, stderr %q", server, code, stderr.String())
		}
	}
}

// gmClearWorld is the ZG pack of leg 2 group 1 (nine members) and the world guids of its members.
func gmClearPack(t *testing.T) (raidwalk.GMClearPack, []packet.GUID) {
	t.Helper()
	res, err := planexport.Build(zgPlan(t), planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	world, _, err := worldspawn.Load(gmClearZGSpawns, 309)
	if err != nil {
		t.Fatal(err)
	}
	packs, err := raidwalk.PlanGMClear(res.Rows, world, []raidwalk.GMClearSpec{{Leg: "2", Groups: []int{1}}})
	if err != nil || len(packs) != 1 {
		t.Fatalf("packs %v, err %v", packs, err)
	}
	var guids []packet.GUID
	for g, e := range packs[0].Members {
		guids = append(guids, packet.GUID(uint64(packet.HighUnit)<<48|uint64(e)<<24|g))
	}
	return packs[0], guids
}

func gmClearUnitsOf(guids []packet.GUID, dead func(packet.GUID) bool) []objects.Unit {
	var out []objects.Unit
	for _, g := range guids {
		out = append(out, objects.Unit{GUID: g, Type: "creature", Entry: g.Entry(), Dead: dead(g)})
	}
	return out
}

// A placed walk with --gm-clear sends, for every live member of the pack, the kill, reads its death from the unit's frames, logs it,
// and marks the summary ASSISTED. It does this after the corridor clear and before the quiet check.
func TestPlaceGMClearKillsWholePackAndMarksSummary(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:1"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t)
	if len(guids) < 2 {
		t.Fatalf("the test pack must hold linked members, has %d", len(guids))
	}
	// A second pack's unit (not a member) and a unit that is already dead stand in the room too: neither is touched.
	stranger := packet.GUID(uint64(packet.HighUnit)<<48 | 11111<<24 | 777)
	corpse := guids[0]
	f.agent.unitsFn = func(int) []objects.Unit {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		us := gmClearUnitsOf(guids, func(g packet.GUID) bool { return f.agent.killed[g] || g == corpse })
		return append(us, objects.Unit{GUID: stranger, Type: "creature", Entry: 11111})
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}

	log := f.agentLog()
	kills := map[string]int{}
	for _, l := range log {
		if g, ok := strings.CutPrefix(l, "gm_kill "); ok {
			kills[g]++
		}
	}
	if len(kills) != len(guids)-1 {
		t.Fatalf("kills %v, want %d live members", kills, len(guids)-1)
	}
	for _, g := range guids {
		if g == corpse {
			if kills[g.String()] != 0 {
				t.Errorf("the dead member %s was killed again", g)
			}
		} else if kills[g.String()] != 1 {
			t.Errorf("member %s killed %d times", g, kills[g.String()])
		}
	}
	if kills[stranger.String()] != 0 {
		t.Error("a unit outside the pack was killed")
	}
	// The order: teleport to the pack, the kill, the read of the death, then back to the start before the quiet and the move.
	orderedAt(t, log, []string{"tt clearto ", "gm .go xyz ", "gm_kill 0xF130", "unit 0xF130", "gm .go xyz ", "tt quiet ", "tt place "})
	// Every kill is confirmed by a read of the unit after the line.
	for g := range kills {
		orderedAt(t, log, []string{"gm_kill " + g, "unit " + g})
	}

	sum := f.summary()
	if !sum.Assisted || len(sum.GMClear) != len(guids)-1 {
		t.Fatalf("summary assisted %v, gm_clear %+v", sum.Assisted, sum.GMClear)
	}
	for _, e := range sum.GMClear {
		if !e.Killed || e.Tries != 1 || e.Leg != pack.Leg || e.Group != "1" || e.Entry == 0 || !strings.HasPrefix(e.GUID, "0xF130") {
			t.Errorf("entry %+v", e)
		}
	}
	text, err := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(string(text), "\n", 2)[0]
	if !strings.Contains(first, "ASSISTED") || !strings.Contains(string(text), "this is not a real clear") {
		t.Fatalf("summary.txt:\n%s", text)
	}
	if !strings.Contains(f.stdout.String(), "ASSISTED") {
		t.Fatalf("the result line does not say ASSISTED: %q", f.stdout.String())
	}
	runLog, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range sum.GMClear {
		want := raidwalk.GMClearLogLine(e)
		if !strings.Contains(string(runLog), want) {
			t.Errorf("run.log lacks %q", want)
		}
	}
	if !strings.Contains(string(runLog), "gm-clear: killed ") || !strings.Contains(string(runLog), "("+pack.Leg+":1)") {
		t.Errorf("run.log:\n%s", runLog)
	}
	// The report reads the folder's summary and marks itself.
	var out, errb bytes.Buffer
	if err := os.WriteFile(filepath.Join(f.out, "fights-full.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := raidWalkReportCmd([]string{f.out}, &out, &errb); code != exitOK {
		t.Fatalf("report exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "ASSISTED") || !strings.Contains(out.String(), "| "+pack.Leg+" | 1 |") {
		t.Fatalf("report:\n%s", out.String())
	}
}

// A unit that survives the first line gets one retry; one that survives both is reported (entry killed false, a note) and the walk goes on.
func TestPlaceGMClearRetriesOnceAndReportsSurvivors(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:1"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	_, guids := gmClearPack(t)
	slow, stubborn := guids[0], guids[1]
	f.agent.killFn = func(g packet.GUID, tries int) bool {
		switch g {
		case slow:
			return tries >= 2
		case stubborn:
			return false
		}
		return true
	}
	f.agent.unitsFn = func(int) []objects.Unit {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		return gmClearUnitsOf(guids, func(g packet.GUID) bool { return f.agent.killed[g] })
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	by := map[string]raidwalk.GMClearEntry{}
	for _, e := range sum.GMClear {
		by[e.GUID] = e
	}
	if e := by[slow.String()]; !e.Killed || e.Tries != 2 {
		t.Errorf("slow unit %+v", e)
	}
	if e := by[stubborn.String()]; e.Killed || e.Tries != 2 {
		t.Errorf("stubborn unit %+v", e)
	}
	// Two lines at most per unit, and the second read round does not send a third to the stubborn one.
	n := 0
	for _, l := range f.agentLog() {
		if l == "gm_kill "+stubborn.String() {
			n++
		}
	}
	if n != gmClearTries {
		t.Errorf("the stubborn unit got %d lines, want %d", n, gmClearTries)
	}
	found := false
	for _, note := range sum.Notes {
		if strings.Contains(note, "would not die") && strings.Contains(note, stubborn.String()) {
			found = true
		}
	}
	if !found || !sum.Assisted {
		t.Errorf("notes %v assisted %v", sum.Notes, sum.Assisted)
	}
	text, _ := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if !strings.Contains(string(text), "would not die") {
		t.Errorf("summary.txt:\n%s", text)
	}
}

// With no pack member in view the walk is still ASSISTED: the flag was used, and the summary says nothing died.
func TestPlaceGMClearNothingInViewStillAssisted(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:1"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if !sum.Assisted || len(sum.GMClear) != 0 {
		t.Fatalf("assisted %v, gm_clear %v", sum.Assisted, sum.GMClear)
	}
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "gm_kill") {
			t.Fatalf("a kill went out: %s", l)
		}
	}
}

// Without the flag nothing changes: no kill, no assisted mark, no gm_clear field.
func TestPlaceWithoutGMClearIsNotAssisted(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "assisted") || strings.Contains(string(raw), "gm_clear") {
		t.Fatalf("summary.json mentions gm-clear:\n%s", raw)
	}
	text, _ := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if strings.Contains(string(text), "ASSISTED") {
		t.Fatalf("summary.txt:\n%s", text)
	}
}

// validate refuses a leg or group the plan does not have, before any GM command.
func TestValidateGMClearUnknownGroup(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:99"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	if code := f.run(context.Background()); code != exitUsage || !strings.Contains(f.stderr.String(), "has 6 pull groups, not 99") {
		t.Fatalf("exit %d, stderr %q", code, f.stderr.String())
	}
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "gm ") {
			t.Fatalf("a GM line went out before the refusal: %s", l)
		}
	}
	f2, _ := newPlaceFixture(t)
	f2.opts.gmClear = stringList{"2:1"}
	f2.opts.gmClearSpawns = t.TempDir()
	if code := f2.run(context.Background()); code != exitUsage || !strings.Contains(f2.stderr.String(), "no world export") {
		t.Fatalf("exit %d, stderr %q", code, f2.stderr.String())
	}
}

// The dry run lists the packs and sends nothing.
func TestDryRunListsGMClearPacks(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:1"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	f.opts.dryRun = true
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "8c gm-clear: high_priestess_marli:1 pack ") {
		t.Fatalf("dry run:\n%s", f.stdout.String())
	}
}
