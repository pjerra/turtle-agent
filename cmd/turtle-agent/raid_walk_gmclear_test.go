package main

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/slots"
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

// A --gm-clear group whose row holds no hostile spawn (a boss's or an event's speaker) is skipped with a WARN note,
// never an error, and the groups before and after it are still planned (walk 31: Mandokir group 18, the Vilebranch
// Speaker, aborted the whole tool).
func TestValidateGMClearSkipsBossGroupWithWarn(t *testing.T) {
	dir := t.TempDir()
	const creatures = "guid\tentry\tname\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\n" +
		"1\t100\tTrash\t60\t0\t16\t0\t10\t0\t0\t0\t0\t0\n" +
		"2\t200\tVilebranch Speaker\t60\t0\t35\t2\t100\t0\t0\t0\t0\t0\n"
	if err := os.WriteFile(filepath.Join(dir, "creatures.tsv"), []byte(creatures), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := planexport.Rows{
		Plan: planexport.PlanRow{Plan: "zul_gurub", Map: 309},
		Legs: []planexport.LegRow{{Plan: "zul_gurub", Leg: 0, Name: "one"}},
		Steps: []planexport.StepRow{
			{Plan: "zul_gurub", Leg: 0, Seq: 1, Kind: planexport.KindMove, X: 1, Y: 1, Z: 1},
			{Plan: "zul_gurub", Leg: 0, Seq: 2, Kind: planexport.KindPull, Entries: []uint32{100}, Radius: 10, X: 10},
			{Plan: "zul_gurub", Leg: 0, Seq: 3, Kind: planexport.KindPull, Entries: []uint32{200}, Radius: 10, X: 100},
		},
	}
	w := &walk{plan: &dungeon.Plan{Name: "zul_gurub", Map: 309}, rows: rows,
		opts: raidWalkOptions{dir: "../../dungeons", gmClear: stringList{"one:1,2"}, gmClearSpawns: dir}}
	if err := w.validateGMClear(); err != nil {
		t.Fatalf("validateGMClear aborted: %v", err)
	}
	if len(w.gmClear) != 1 || w.gmClear[0].Group != "1" {
		t.Fatalf("packs %+v, want group 1's pack", w.gmClear)
	}
	if len(w.gmClearWarn) != 1 || !strings.Contains(w.gmClearWarn[0], "group 2") {
		t.Fatalf("warnings %v, want the group 2 WARN", w.gmClearWarn)
	}
	notes := strings.Join(w.notes, "\n")
	if !strings.Contains(notes, "WARN: skipped leg one group 2 (step 3)") {
		t.Fatalf("notes %q, want the group 2 WARN", notes)
	}
	if !strings.Contains(notes, "1 packs to kill") {
		t.Fatalf("notes %q, want group 1 planned", notes)
	}
}

// gmClearPack plans the ZG pack of leg 2 group 2 (nine members: the walk-26b pack 0 stand pull) from the export in dir and returns it with its members' world guids.
func gmClearPack(t *testing.T, dir string) (raidwalk.GMClearPack, []packet.GUID) {
	t.Helper()
	res, err := planexport.Build(zgPlan(t), planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	world, _, err := worldspawn.Load(dir, 309)
	if err != nil {
		t.Fatal(err)
	}
	packs, _, err := raidwalk.PlanGMClear(res.Rows, world, []raidwalk.GMClearSpec{{Leg: "2", Groups: []int{2}}})
	if err != nil || len(packs) != 1 {
		t.Fatalf("packs %v, err %v", packs, err)
	}
	var guids []packet.GUID
	for _, g := range packs[0].SpawnGUIDs() {
		guids = append(guids, packet.GUID(uint64(packet.HighUnit)<<48|uint64(packs[0].Members[g])<<24|g))
	}
	return packs[0], guids
}

// gmClearView is the fake client's view: the pack's units whose spawn point lies within the read radius of where the commander stands
// (the fake agent's last .go), each dead or alive as dead says. Units outside the radius are not in the object store.
func gmClearView(f *fixture, pack raidwalk.GMClearPack, guids []packet.GUID, dead func(packet.GUID) bool) []objects.Unit {
	f.agent.mu.Lock()
	cx, cy := float64(f.agent.x), float64(f.agent.y)
	f.agent.mu.Unlock()
	var out []objects.Unit
	for _, g := range guids {
		pt := pack.Positions[raidwalk.SpawnGUID(g)]
		if math.Hypot(float64(pt.X)-cx, float64(pt.Y)-cy) > gmClearReadYd {
			continue
		}
		out = append(out, objects.Unit{GUID: g, Type: "creature", Entry: g.Entry(), Dead: dead(g)})
	}
	return out
}

// gmClearFarExport copies the ZG export to a temp dir with one member of the pack moved 400 yd east of the rest, so the pack is wider
// than the commander's view. It returns the dir and the moved member's spawn guid.
func gmClearFarExport(t *testing.T, member uint64) string {
	t.Helper()
	dst := t.TempDir()
	ents, err := os.ReadDir(gmClearZGSpawns)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		raw, err := os.ReadFile(filepath.Join(gmClearZGSpawns, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if e.Name() == "creatures.tsv" {
			lines := strings.Split(string(raw), "\n")
			xi := -1
			for i, l := range lines {
				cols := strings.Split(l, "\t")
				if i == 0 {
					for j, c := range cols {
						if c == "x" {
							xi = j
						}
					}
					continue
				}
				if len(cols) > xi && cols[0] == strconv.FormatUint(member, 10) {
					x, _ := strconv.ParseFloat(cols[xi], 64)
					cols[xi] = strconv.FormatFloat(x+400, 'f', 3, 64)
					lines[i] = strings.Join(cols, "\t")
				}
			}
			raw = []byte(strings.Join(lines, "\n"))
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// A placed walk with --gm-clear sends, for every live member of the pack, the kill, reads its death from the unit's frames, logs it,
// and marks the summary ASSISTED. It does this after the corridor clear and before the quiet check.
func TestPlaceGMClearKillsWholePackAndMarksSummary(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	if len(guids) < 2 {
		t.Fatalf("the test pack must hold linked members, has %d", len(guids))
	}
	// A unit outside the pack stands in the room too, and one member is a corpse already: neither is killed.
	stranger := packet.GUID(uint64(packet.HighUnit)<<48 | 11111<<24 | 777)
	corpse := guids[0]
	f.agent.unitsFn = func(int) []objects.Unit {
		us := gmClearView(f, pack, guids, func(g packet.GUID) bool {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			return f.agent.killed[g] || g == corpse
		})
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
	if !sum.Assisted || sum.GMClearPartial || len(sum.GMClear) != len(guids) {
		t.Fatalf("summary assisted %v partial %v, gm_clear %+v", sum.Assisted, sum.GMClearPartial, sum.GMClear)
	}
	nKilled := 0
	for _, e := range sum.GMClear {
		if !e.Killed || e.Leg != pack.Leg || e.Group != "2" || e.Entry == 0 || !strings.HasPrefix(e.GUID, "0xF130") {
			t.Errorf("entry %+v", e)
		}
		if e.Reason == raidwalk.GMClearAlreadyDead {
			if e.Tries != 0 || e.GUID != corpse.String() {
				t.Errorf("already-dead entry %+v", e)
			}
			continue
		}
		nKilled++
		if e.Tries != 1 {
			t.Errorf("entry %+v", e)
		}
	}
	if nKilled != len(guids)-1 {
		t.Errorf("%d killed entries", nKilled)
	}
	text, err := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(string(text), "\n", 2)[0]
	if !strings.Contains(first, "ASSISTED") || strings.Contains(first, "PARTIAL") || !strings.Contains(string(text), "this is not a real clear") {
		t.Fatalf("summary.txt:\n%s", text)
	}
	if !strings.Contains(f.stdout.String(), "ASSISTED") {
		t.Fatalf("the result line does not say ASSISTED: %q", f.stdout.String())
	}
	runLog, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, e := range sum.GMClear {
		if e.Tries == 0 {
			continue
		}
		lines++
		want := raidwalk.GMClearLogLine(e)
		if !strings.Contains(string(runLog), want) {
			t.Errorf("run.log lacks %q", want)
		}
	}
	if lines == 0 || !strings.Contains(string(runLog), "("+pack.Leg+":2)") {
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
	if !strings.Contains(out.String(), "ASSISTED") || !strings.Contains(out.String(), "| "+pack.Leg+" | 2 |") || strings.Contains(out.String(), "PARTIAL") {
		t.Fatalf("report:\n%s", out.String())
	}
}

// A unit that survives the first line gets one retry; one that survives both is recorded as alive and the clear is PARTIAL (the walk
// goes on).
func TestPlaceGMClearRetriesOnceAndReportsSurvivors(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t, gmClearZGSpawns)
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
		return gmClearView(f, pack, guids, func(g packet.GUID) bool { f.agent.mu.Lock(); defer f.agent.mu.Unlock(); return f.agent.killed[g] })
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
	if e := by[stubborn.String()]; e.Killed || e.Tries != gmClearTries || e.Reason != raidwalk.GMClearAlive {
		t.Errorf("stubborn unit %+v", e)
	}
	// gmClearTries lines at most per unit, and the second round sends none to the stubborn one.
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
		if strings.Contains(note, fmt.Sprintf("alive after %d", gmClearTries)) && strings.Contains(note, stubborn.String()) {
			found = true
		}
	}
	if !found || !sum.Assisted || !sum.GMClearPartial {
		t.Errorf("notes %v assisted %v partial %v", sum.Notes, sum.Assisted, sum.GMClearPartial)
	}
	text, _ := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if !strings.HasPrefix(string(text), "raid-walk ") || !strings.Contains(strings.SplitN(string(text), "\n", 2)[0], "ASSISTED PARTIAL") || !strings.Contains(string(text), "PARTIAL: 1 members left (0 not seen, 1 alive)") {
		t.Errorf("summary.txt:\n%s", text)
	}
}

// A member that stands outside the commander's view of the rest (400 yd away, over the 150 yd read) is visited at its own spawn point,
// killed, and the clear is not PARTIAL. The first read of every stop is empty here (nothing shows until the members have been "seen"
// once), and both rounds still run.
func TestPlaceGMClearVisitsMembersOutsideTheViewAndReadsTwice(t *testing.T) {
	_, guids := gmClearPack(t, gmClearZGSpawns)
	far := guids[len(guids)-1]
	dir := gmClearFarExport(t, raidwalk.SpawnGUID(far))
	pack, guids := gmClearPack(t, dir)
	if pack.Radius <= 150 {
		t.Fatalf("the moved export must make the pack wider than the view: radius %.0f", pack.Radius)
	}
	if n := len(pack.Stops()); n < 2 {
		t.Fatalf("a pack wider than the view needs several stops, has %d", n)
	}
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = dir
	reads := 0
	f.agent.unitsFn = func(int) []objects.Unit {
		reads++
		if reads <= 1 { // the first read of the walk shows nothing
			return []objects.Unit{}
		}
		return gmClearView(f, pack, guids, func(g packet.GUID) bool { f.agent.mu.Lock(); defer f.agent.mu.Unlock(); return f.agent.killed[g] })
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.GMClearPartial || len(sum.GMClear) != len(guids) {
		t.Fatalf("partial %v, gm_clear %+v", sum.GMClearPartial, sum.GMClear)
	}
	for _, e := range sum.GMClear {
		if !e.Killed {
			t.Errorf("entry %+v", e)
		}
	}
	killedFar := false
	for _, l := range f.agentLog() {
		if l == "gm_kill "+far.String() {
			killedFar = true
		}
	}
	if !killedFar {
		t.Fatal("the member outside the view was never killed")
	}
	// Both rounds ran at every stop: at least two reads per stop, the first of them empty.
	if reads < 2*len(pack.Stops()) {
		t.Fatalf("%d reads for %d stops", reads, len(pack.Stops()))
	}
	// The commander went to the far member's own spawn.
	pt := pack.Positions[raidwalk.SpawnGUID(far)]
	goLine := "gm .go xyz " + num32(pt.X) + " " + num32(pt.Y) + " "
	seen := false
	for _, l := range f.agentLog() {
		seen = seen || strings.HasPrefix(l, goLine)
	}
	if !seen {
		t.Fatalf("no %q in the log", goLine)
	}
}

// With no pack member ever in view, every expected member is recorded as not seen, the clear is PARTIAL, and the walk still reads twice.
func TestPlaceGMClearNothingInViewIsPartial(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, _ := gmClearPack(t, gmClearZGSpawns)
	reads := 0
	f.agent.unitsFn = func(int) []objects.Unit { reads++; return []objects.Unit{} }
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if !sum.Assisted || !sum.GMClearPartial || len(sum.GMClear) != len(pack.Members) {
		t.Fatalf("assisted %v partial %v, gm_clear %v", sum.Assisted, sum.GMClearPartial, sum.GMClear)
	}
	for _, e := range sum.GMClear {
		if e.Killed || e.Reason != raidwalk.GMClearNotSeen || !strings.HasPrefix(e.GUID, "spawn ") {
			t.Errorf("entry %+v", e)
		}
	}
	if reads < gmClearRounds {
		t.Errorf("only %d reads: an empty read must not skip the second round", reads)
	}
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "gm_kill") {
			t.Fatalf("a kill went out: %s", l)
		}
	}
	text, _ := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if !strings.Contains(strings.SplitN(string(text), "\n", 2)[0], "ASSISTED PARTIAL") || !strings.Contains(string(text), "PARTIAL: 9 members left (9 not seen, 0 alive)") {
		t.Fatalf("summary.txt:\n%s", text)
	}
}

// Walk 37 raid e (ZG walk 37): the supervisor's restart kept --gm-clear and the
// tool ran, but run.log held no gm-clear: line for the group, so the report
// could not tell whether the flag acted on this placement. Every named group
// gets one run.log line, including a group with nothing to clear.
func TestPlaceGMClearLogsOneLinePerGroup(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	f.agent.unitsFn = func(int) []objects.Unit {
		return gmClearView(f, pack, guids, func(g packet.GUID) bool {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			return f.agent.killed[g]
		})
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	label := raidwalk.GMClearLabel(pack.Leg, "2")
	if !strings.Contains(string(raw), "gm-clear: "+label) {
		t.Fatalf("run.log has no gm-clear: line for group %s:\n%s", label, raw)
	}
	// The corridor clear despawns the whole pack: still one line, naming that
	// there was nothing to clear.
	f2, tk := newPlaceFixture(t)
	f2.opts.gmClear = stringList{"2:2"}
	f2.opts.gmClearSpawns = gmClearZGSpawns
	tk.clearto = func(string) (int, error) { return 1, nil }
	tk.cleartoRemoved = func(string) []any {
		out := make([]any, 0, len(guids))
		for _, g := range guids {
			out = append(out, map[string]any{"g": g.String(), "e": g.Entry(), "n": "despawned"})
		}
		return out
	}
	f2.agent.unitsFn = func(int) []objects.Unit {
		return gmClearView(f2, pack, guids, func(g packet.GUID) bool { return false })
	}
	f2.agent.startReply = zgPlaceReply(t)
	if code := f2.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f2.stderr.String())
	}
	raw2, err := os.ReadFile(filepath.Join(f2.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw2), "gm-clear: "+label) || !strings.Contains(string(raw2), "nothing to clear") {
		t.Fatalf("run.log has no nothing-to-clear line for group %s:\n%s", label, raw2)
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
	f2.opts.gmClear = stringList{"2:2"}
	f2.opts.gmClearSpawns = t.TempDir()
	if code := f2.run(context.Background()); code != exitUsage || !strings.Contains(f2.stderr.String(), "no world export") {
		t.Fatalf("exit %d, stderr %q", code, f2.stderr.String())
	}
}

// The dry run lists the packs and sends nothing.
func TestDryRunListsGMClearPacks(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	f.opts.dryRun = true
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "8c gm-clear: high_priestess_marli:2 pack ") {
		t.Fatalf("dry run:\n%s", f.stdout.String())
	}
}

// gm_kill is authorized from the dialled host:port, never from server_name alone: it must be exactly the world_addr of the
// test server server_name names in the slots table. The host compares case-insensitively, the port exactly. A test server
// with no world_addr is refused (its realmd is not a stand-in). An ordinary agent (server_name not a test server) is
// refused with no reason.
func TestGMKillAllowedNeedsTheConfiguredAddress(t *testing.T) {
	servers := []slots.Server{
		{Name: "m910q", Realmd: "10.1.1.1:3724", WorldAddr: "10.1.1.1:8085"},
		{Name: "tortoise-test2", Realmd: "10.2.2.2:3724", WorldAddr: "10.2.2.3:8085"},
	}
	cases := []struct {
		name, server, dialed string
		want                 bool
		reasonHas            string // "" = no reason expected
	}{
		{"test2 dialled at its world_addr", "tortoise-test2", "10.2.2.3:8085", true, ""},
		{"m910q dialled at its world_addr", "m910q", "10.1.1.1:8085", true, ""},
		{"m910q same host, its realmd port", "m910q", "10.1.1.1:3724", false, "gm_kill refused"},
		{"m910q same host, another port", "m910q", "10.1.1.1:9999", false, "gm_kill refused"},
		{"test2 same host, another port", "tortoise-test2", "10.2.2.3:8086", false, "gm_kill refused"},
		{"settings say tortoise-test2, session dialled a live host", "tortoise-test2", "203.0.113.9:8085", false, "gm_kill refused"},
		{"settings say tortoise-test2, dialled its realmd host, not its world_addr", "tortoise-test2", "10.2.2.2:8085", false, "gm_kill refused"},
		{"settings say m910q, dialled the other test server", "m910q", "10.2.2.3:8085", false, "gm_kill refused"},
		{"dialled address without a port", "m910q", "10.1.1.1", false, "gm_kill refused"},
		{"ordinary agent", "tortoise", "10.1.1.1:8085", false, ""},
		{"no server_name", "", "10.1.1.1:8085", false, ""},
		{"server_name case differs", "M910Q", "10.1.1.1:8085", false, ""},
		{"server_name has a trailing space", "m910q ", "10.1.1.1:8085", false, ""},
	}
	for _, c := range cases {
		got, why := gmKillAllowed(config.Settings{ServerName: c.server}, c.dialed, servers)
		if got != c.want {
			t.Errorf("%s: allowed %v, want %v (reason %q)", c.name, got, c.want, why)
		}
		if (why != "") != (c.reasonHas != "") || (c.reasonHas != "" && !strings.Contains(why, c.reasonHas)) {
			t.Errorf("%s: reason %q, want one containing %q", c.name, why, c.reasonHas)
		}
	}
	// A test server named in settings but missing from the slots table is refused with a reason.
	if got, why := gmKillAllowed(config.Settings{ServerName: "m910q"}, "10.1.1.1:8085", nil); got || !strings.Contains(why, "no entry") {
		t.Errorf("no slots entry: allowed %v, reason %q", got, why)
	}
	// A test server whose slots entry has no world_addr is refused on every address, its realmd included.
	noWorld := []slots.Server{{Name: "m910q", Realmd: "10.1.1.1:3724"}}
	for _, dialed := range []string{"10.1.1.1:3724", "10.1.1.1:8085"} {
		if got, why := gmKillAllowed(config.Settings{ServerName: "m910q"}, dialed, noWorld); got || !strings.Contains(why, "no world_addr") {
			t.Errorf("no world_addr, dialled %s: allowed %v, reason %q", dialed, got, why)
		}
	}
	// The host compares case-insensitively; the port still has to match exactly.
	hostname := []slots.Server{{Name: "m910q", WorldAddr: "test.local:8085"}}
	if got, why := gmKillAllowed(config.Settings{ServerName: "m910q"}, "TEST.Local:8085", hostname); !got || why != "" {
		t.Errorf("host in another case, same port: allowed %v, reason %q", got, why)
	}
	if got, _ := gmKillAllowed(config.Settings{ServerName: "m910q"}, "TEST.Local:8086", hostname); got {
		t.Errorf("host in another case, other port: allowed")
	}
}

// gmKillAuthorize reads the slots table from its default path (under HOME). A missing or loose table refuses gm_kill.
func TestGMKillAuthorizeReadsTheSlotsTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := config.Settings{ServerName: "tortoise-test2"}
	if got, why := gmKillAuthorize(st, "10.2.2.3:8085"); got || !strings.Contains(why, "could not be read") {
		t.Fatalf("no slots file: allowed %v, reason %q", got, why)
	}
	dir := filepath.Join(home, ".config", "turtle-agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	table := `{"version":1,"servers":[{"name":"tortoise-test2","realmd":"10.2.2.2:3724","world_addr":"10.2.2.3:8085","slots":[]}]}`
	if err := os.WriteFile(filepath.Join(dir, "slots.json"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, why := gmKillAuthorize(st, "10.2.2.3:8085"); !got || why != "" {
		t.Fatalf("matching host: allowed %v, reason %q", got, why)
	}
	if got, why := gmKillAuthorize(st, "203.0.113.9:8085"); got || !strings.Contains(why, "gm_kill refused") {
		t.Fatalf("live host under a test server_name: allowed %v, reason %q", got, why)
	}
	if got, why := gmKillAuthorize(config.Settings{ServerName: "tortoise"}, "203.0.113.9:8085"); got || why != "" {
		t.Fatalf("ordinary agent: allowed %v, reason %q", got, why)
	}
}

// Walk 32: every gm_kill was refused (the slots table had no world_addr for the test server), so the groups stayed
// alive after the two tries, and the placement then failed on the quiet (exit 18) or the member list (PARTIAL). A
// group that cannot be cleared must WARN and the placement must go on WITHOUT it: its entries join the quiet's and the
// landing read's skip list, so an alive member cannot refuse the placement, and the ASSISTED line lists the group.
func TestPlaceGMClearUnclearedGroupDoesNotFailThePlacement(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	f.agent.killFn = func(packet.GUID, int) bool { return false }
	f.agent.unitsFn = func(int) []objects.Unit {
		return gmClearView(f, pack, guids, func(packet.GUID) bool { return false })
	}
	// The quiet sees one of the pack's units inside the landing reach on every read: only the skip list keeps it out
	// of the placement's verdict (before the fix this refused the placement, exit 18).
	_, plan := placePlanRows(t)
	entry := pack.Members[raidwalk.SpawnGUID(guids[0])]
	p.quiet = func(int) bool { return false }
	p.quietNear = []any{quietUnitAt(entry, plan.Point.X+10, plan.Point.Y, plan.Point.Z)}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (an uncleared group must not fail the placement): %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !sum.Assisted || !sum.GMClearPartial {
		t.Fatalf("summary assisted %v partial %v, want an ASSISTED PARTIAL clear", sum.Assisted, sum.GMClearPartial)
	}
	warn := ""
	for _, n := range sum.Notes {
		if strings.Contains(n, "not cleared") && strings.Contains(n, "placement goes on without it") {
			warn = n
		}
	}
	if !strings.Contains(warn, pack.Leg+":2") {
		t.Fatalf("notes %q, want the group's WARN (%s:2)", sum.Notes, pack.Leg)
	}
	// Every member got its retries (four lines, three seconds apart).
	f.agent.mu.Lock()
	log := append([]string{}, f.agent.log...)
	killAt := append([]time.Time{}, f.agent.killAt...)
	f.agent.mu.Unlock()
	for _, g := range guids {
		n := 0
		var at []time.Time
		k := 0
		for _, l := range log {
			if !strings.HasPrefix(l, "gm_kill ") {
				continue
			}
			if l == "gm_kill "+g.String() {
				n++
				if k < len(killAt) {
					at = append(at, killAt[k])
				}
			}
			k++
		}
		if n != gmClearTries {
			t.Errorf("member %s got %d kill lines, want %d", g, n, gmClearTries)
		}
		for i := 1; i < len(at); i++ {
			if gap := at[i].Sub(at[i-1]); gap < gmClearRetryGap {
				t.Errorf("member %s: %s between kill lines, want at least %s", g, gap, gmClearRetryGap)
			}
		}
	}
	// The quiet carried the pack's entries as skips.
	skips := ""
	for _, c := range f.agentLog() {
		if strings.HasPrefix(c, "tt quiet ") {
			skips = c
		}
	}
	for _, g := range pack.SpawnGUIDs() {
		if e := pack.Members[g]; !strings.Contains(skips, "skip=") || !strings.Contains(skips, strconv.FormatUint(uint64(e), 10)) {
			t.Errorf("the quiet does not skip entry %d: %q", e, skips)
		}
	}
	text, err := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "groups not cleared: "+pack.Leg+":2") {
		t.Fatalf("summary.txt lacks the uncleared group:\n%s", text)
	}
}

// Walk 35 (Jin'do hall groups 5-13, 52 members "alive after 4 tries", 15-25 min per placement): the corridor clear had already
// despawned those units in the same instance. The server no longer has them, so `.die` on their guid does nothing while the
// client's object store still shows them alive. The tool must not spend a line on a member its own clearto removed: it is
// recorded as despawned at once, the clear stays whole, and no 32 s retry run happens.
func TestPlaceGMClearSkipsMembersTheCorridorClearRemoved(t *testing.T) {
	f, tk := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	gone := guids[0] // the corridor clear's victim
	tk.clearto = func(string) (int, error) { return 1, nil }
	tk.cleartoRemoved = func(string) []any {
		return []any{map[string]any{"g": gone.String(), "e": gone.Entry(), "n": "despawned"}}
	}
	f.agent.unitsFn = func(int) []objects.Unit {
		// The stale store still lists the despawned unit as alive, exactly as the walk-35 client did.
		return gmClearView(f, pack, guids, func(g packet.GUID) bool {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			return f.agent.killed[g] && g != gone
		})
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	for _, l := range f.agentLog() {
		if l == "gm_kill "+gone.String() {
			t.Fatalf("the tool sent a line for the member its own clearto despawned: %v", f.agentLog())
		}
	}
	sum := f.summary()
	if sum.GMClearPartial || len(sum.GMClear) != len(guids) {
		t.Fatalf("partial %v, gm_clear %+v", sum.GMClearPartial, sum.GMClear)
	}
	by := map[string]raidwalk.GMClearEntry{}
	for _, e := range sum.GMClear {
		by[e.GUID] = e
	}
	e, ok := by["spawn "+strconv.FormatUint(raidwalk.SpawnGUID(gone), 10)]
	if !ok || !e.Killed || e.Tries != 0 || e.Reason != raidwalk.GMClearDespawned {
		t.Errorf("the despawned member's entry is %+v (want killed, 0 tries, %q)", e, raidwalk.GMClearDespawned)
	}
	if !hasNote(sum, "already despawned by this walk's own clear") {
		t.Errorf("notes %q do not explain the despawned member", sum.Notes)
	}
	text, err := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(text), "PARTIAL") {
		t.Fatalf("summary.txt:\n%s", text)
	}
}

// A member that cannot die costs gmClearTries * (gmClearDeathWait + gmClearRetryGap) = 32 s. The clear's budget stops the tool
// after five minutes instead of running for half an hour: the rest of the pack is recorded as not tried, a WARN names the budget,
// and the placement still goes on.
func TestPlaceGMClearStopsAtTheBudget(t *testing.T) {
	old := gmClearBudget
	gmClearBudget = 40 * time.Second
	t.Cleanup(func() { gmClearBudget = old })
	f, _ := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	f.agent.killFn = func(packet.GUID, int) bool { return false } // nothing dies
	f.agent.unitsFn = func(int) []objects.Unit {
		return gmClearView(f, pack, guids, func(packet.GUID) bool { return false })
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	lines := 0
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "gm_kill ") {
			lines++
		}
	}
	// 40 s: the first member's four tries take 32 s (4 x 5 s death wait + 3 x 3 s gap); the second one gets its first line
	// at 29 s and a second at 37 s (5 s + 3 s gap), and the check at 45 s stops it. No line goes out past the budget.
	if want := 6; lines != want {
		t.Fatalf("%d kill lines, want %d (the budget is %s)", lines, want, gmClearBudget)
	}
	sum := f.summary()
	if !sum.GMClearPartial || len(sum.GMClear) != len(guids) {
		t.Fatalf("partial %v, gm_clear %+v", sum.GMClearPartial, sum.GMClear)
	}
	untried := 0
	for _, e := range sum.GMClear {
		if e.Reason == raidwalk.GMClearGaveUp {
			untried++
			if e.Killed || e.Tries != 0 {
				t.Errorf("untried entry %+v", e)
			}
		}
	}
	if untried != len(guids)-2 {
		t.Errorf("%d untried members, want %d: %+v", untried, len(guids)-2, sum.GMClear)
	}
	if !hasNote(sum, "gave up after 40s") && !hasNote(sum, "gave up after 40.0s") {
		t.Errorf("notes %q do not name the budget", sum.Notes)
	}
	text, err := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "not tried") {
		t.Fatalf("summary.txt:\n%s", text)
	}
}

// The world export default must work for an installed binary: the per-user data dir is the default, and a checkout's
// tools/dungeongen/testdata is the fallback (never the binary's own directory).
func TestWorldExportDirDefaultsToTheInstalledDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installed := filepath.Join(home, ".local", "share", "turtle-agent", "world", "map309")
	if err := os.MkdirAll(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "creatures.tsv"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	planDir := filepath.Join(t.TempDir(), "dungeons")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	w := &walk{opts: raidWalkOptions{dir: planDir}, plan: &dungeon.Plan{Map: 309}}
	if got := w.worldExportDir(); got != installed {
		t.Fatalf("worldExportDir = %q, want the installed %q", got, installed)
	}
	// A checkout beside the plan folder is the fallback when the installed dir has no export.
	repo := filepath.Join(filepath.Dir(planDir), "tools", "dungeongen", "testdata", "map309")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "creatures.tsv"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(installed); err != nil {
		t.Fatal(err)
	}
	if got := w.worldExportDir(); got != repo {
		t.Fatalf("worldExportDir = %q, want the checkout %q", got, repo)
	}
	// With neither, the refusal names the installed path (the one an installed binary can have) and the checkout.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if got := w.worldExportDir(); got != installed {
		t.Fatalf("worldExportDir = %q, want the installed %q when neither exists", got, installed)
	}
	for _, want := range []string{installed, repo} {
		if !strings.Contains(w.worldExportWhere(), want) {
			t.Fatalf("worldExportWhere = %q, missing %q", w.worldExportWhere(), want)
		}
	}
	// --gm-clear-spawns wins.
	w.opts.gmClearSpawns = "/exports/map309"
	if got := w.worldExportDir(); got != "/exports/map309" {
		t.Fatalf("--gm-clear-spawns not used: %q", got)
	}
}
