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
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/slots"
	"github.com/pjerra/turtle-agent/internal/trashcheck"
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
	// Every test server passes.
	for _, server := range []string{"m910q", "tortoise-test2", "tortoise-test3"} {
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

// gmPatrolGUIDOf reads the creature_movement guid a plan patrol's source names (the first number in it).
func gmPatrolGUIDOf(source string) uint64 {
	i := strings.IndexFunc(source, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return 0
	}
	j := i
	for j < len(source) && source[j] >= '0' && source[j] <= '9' {
		j++
	}
	n, _ := strconv.ParseUint(source[i:j], 10, 64)
	return n
}

// TestGMClearClearsOnlyTheRowsOwnZGPatrol: a --gm-clear group clears the patrol its own row's plan names and no other.
// ZG's 11352 walks seven loops (49761 at Jeklik's stair, 49698/51947/51962/91493 at Venoxis's, 91435 at Thekal's, 91444
// at Arlokk's, 49705 at Mandokir's, 51948 at Jin'do's); matching by entry and distance cleared every loop whose path came
// near the row, so clearing one leg's group killed other legs' patrollers (Mandokir's row cleared Venoxis's 91493, and
// every Venoxis patrol row cleared 91493 as well). The invariant: the cleared spawns are the row's trash packs' plus its
// own patrol's, nothing else.
func TestGMClearClearsOnlyTheRowsOwnZGPatrol(t *testing.T) {
	res, err := planexport.Build(zgPlan(t), planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	world, _, err := worldspawn.Load(gmClearZGSpawns, 309)
	if err != nil {
		t.Fatal(err)
	}
	// ownPatrol is the world patrol a plan source names: the one holding that spawn guid, or the one whose source is it.
	ownPatrol := func(guid uint64) *worldspawn.Patrol {
		for i := range world.Patrols {
			p := &world.Patrols[i]
			for _, g := range p.SpawnGUIDs {
				if g == guid {
					return p
				}
			}
			if gmPatrolGUIDOf(p.Source) == guid {
				return p
			}
		}
		return nil
	}
	ordinal := map[int]int{}
	checked := 0
	for _, st := range res.Rows.Steps {
		if st.Kind != planexport.KindPull && st.Kind != planexport.KindHold {
			continue
		}
		ordinal[st.Leg]++
		if st.PatrolSource == "" {
			continue
		}
		guid := gmPatrolGUIDOf(st.PatrolSource)
		own := ownPatrol(guid)
		if own == nil {
			t.Errorf("leg %d seq %d: the plan's patrol %q names no world patrol", st.Leg, st.Seq, st.PatrolSource)
			continue
		}
		packs, warns, err := raidwalk.PlanGMClear(res.Rows, world, []raidwalk.GMClearSpec{{Leg: strconv.Itoa(st.Leg), Groups: []int{ordinal[st.Leg]}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(warns) != 0 {
			t.Errorf("leg %d seq %d (%q): warnings %v", st.Leg, st.Seq, st.PatrolSource, warns)
		}
		got := map[uint64]bool{}
		for _, p := range packs {
			for g := range p.Members {
				got[g] = true
			}
		}
		allowed := map[uint64]bool{}
		for _, p := range trashcheck.ClearedPacks(world, st) {
			for _, g := range p.Members {
				allowed[g] = true
			}
		}
		for _, g := range own.SpawnGUIDs {
			allowed[g] = true
			if !got[g] {
				t.Errorf("leg %d seq %d (%q): its own patrol's spawn %d is not cleared", st.Leg, st.Seq, st.PatrolSource, g)
			}
		}
		for g := range got {
			if !allowed[g] {
				t.Errorf("leg %d seq %d (%q): spawn %d is neither the row's trash nor its own patrol (patrol %d)", st.Leg, st.Seq, st.PatrolSource, g, guid)
			}
		}
		checked++
	}
	if checked < 10 {
		t.Errorf("only %d patrol rows checked", checked)
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
	// gmClearTries lines at most per unit; the second round (skipped: every member is settled) sends none to the stubborn one.
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
// killed, and the clear is not PARTIAL. The whole first round reads nothing here (nothing shows until the members have been "seen"
// once): an empty round does not end the pack, so the second round runs and kills. The instance is re-read after every `.go xyz`
// (the same map does not prove the same copy): one read per stop per round, plus the clear's final check.
func TestPlaceGMClearVisitsMembersOutsideTheViewAndReadsPerStop(t *testing.T) {
	_, guids := gmClearPack(t, gmClearZGSpawns)
	far := guids[len(guids)-1]
	dir := gmClearFarExport(t, raidwalk.SpawnGUID(far))
	pack, guids := gmClearPack(t, dir)
	if pack.Radius <= 150 {
		t.Fatalf("the moved export must make the pack wider than the view: radius %.0f", pack.Radius)
	}
	stops := len(pack.Stops())
	if stops < 2 {
		t.Fatalf("a pack wider than the view needs several stops, has %d", stops)
	}
	f, tk := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = dir
	var quiet atomic.Bool
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "quiet") {
			quiet.Store(true)
		}
		return tk.answer(cmd)
	}
	reads := 0
	f.agent.unitsFn = func(int) []objects.Unit {
		dead := func(g packet.GUID) bool { f.agent.mu.Lock(); defer f.agent.mu.Unlock(); return f.agent.killed[g] }
		if quiet.Load() { // the walk's quiet check, after the clear
			return gmClearView(f, pack, guids, dead)
		}
		reads++
		if reads <= stops { // every stop of the first round shows nothing
			return []objects.Unit{}
		}
		return gmClearView(f, pack, guids, dead)
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	// One instance read per stop per round, then the clear's own final check, before the quiet check. Round 1 walks
	// every stop (all read empty), round 2 ends at the far member once the pack is settled (the settled break leaves
	// the round's later stops unread).
	inst, goes, started := 0, 0, false
	firstGo := "gm .go xyz " + num32(pack.Stops()[0].X) + " " + num32(pack.Stops()[0].Y) + " "
	for _, l := range f.agentLog() {
		if !started {
			started = strings.HasPrefix(l, firstGo)
			continue
		}
		if strings.HasPrefix(l, "tt quiet") {
			break
		}
		if l == "tt q inst" {
			inst++
		}
		if strings.HasPrefix(l, "gm .go xyz ") {
			goes++
		}
	}
	// The clear ran goes stops and read the instance once per stop, with one more read for its final check.
	if !started || goes == 0 || inst != goes+1 {
		t.Fatalf("%d `q inst` for %d clear `.go xyz` (started %v), want stops+1: one per stop + the final check", inst, goes, started)
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
	// An empty first round still runs: every stop of round 2 was read, and the skip waits for a member to appear at each.
	if want := 2 * stops; reads < want {
		t.Fatalf("%d clear reads for %d stops, want at least %d (round 1 empty, round 2 to the far member)", reads, stops, want)
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

// agentLinesPrefix counts the agent-log lines starting with prefix (the fake records toolkit and GM calls there).
func agentLinesPrefix(f *fixture, prefix string) int {
	n := 0
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// A pack whose every member died in the first round is not visited again. Walk 71 paid the second .go xyz, 2 s sleep and
// read of every pack (52 packs, 114 `go xyz`, ~230 s of a 322 s pre-clear) for no kill: the second round can only kill a
// member the first round did not see. The first round's kill set is unchanged.
func TestPlaceGMClearSkipsTheSecondRoundOfADeadPack(t *testing.T) {
	f, tk := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	stops := pack.Stops()
	if len(stops) != 1 {
		t.Fatalf("the test pack has %d stops, want 1: the second visit must be what this test skips", len(stops))
	}
	var quiet atomic.Bool
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "quiet") {
			quiet.Store(true)
		}
		return tk.answer(cmd)
	}
	reads := 0
	f.agent.unitsFn = func(int) []objects.Unit {
		dead := func(g packet.GUID) bool { f.agent.mu.Lock(); defer f.agent.mu.Unlock(); return f.agent.killed[g] }
		if !quiet.Load() {
			reads++
		}
		return gmClearView(f, pack, guids, dead)
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	// The one round that killed every live member, and no second: the test fails at 2 before the skip.
	if reads != 1 {
		t.Fatalf("%d clear reads, want 1: the whole pack died in the first round", reads)
	}
	goLine := "gm .go xyz " + num32(stops[0].X) + " " + num32(stops[0].Y) + " "
	if n := agentLinesPrefix(f, goLine); n != 1 {
		t.Fatalf("%d visits to the pack's stop, want 1", n)
	}
	sum := f.summary()
	if sum.GMClearPartial || len(sum.GMClear) != len(guids) {
		t.Fatalf("partial %v, gm_clear %+v", sum.GMClearPartial, sum.GMClear)
	}
	for _, e := range sum.GMClear {
		if !e.Killed || e.Entry == 0 {
			t.Errorf("entry %+v", e)
		}
	}
}

// A later stop that lands in another copy stops the clear: the same map does not prove the same instance, so the
// commander re-reads its instance after every `.go xyz` and before every units read or kill. Here the second stop's
// read reports another instance: the clear stops on the existing error path with no second-stop read and no kill there.
func TestPlaceGMClearSecondStopInstanceMismatchStopsTheClear(t *testing.T) {
	_, guids := gmClearPack(t, gmClearZGSpawns)
	far := guids[len(guids)-1]
	dir := gmClearFarExport(t, raidwalk.SpawnGUID(far))
	pack, guids := gmClearPack(t, dir)
	stops := pack.Stops()
	if len(stops) < 2 {
		t.Fatalf("a pack wider than the view needs several stops, has %d", len(stops))
	}
	f, tk := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = dir
	// The copy moves under the walk at the second stop's own `.go xyz`: every later `q inst` names another instance.
	var armed atomic.Bool
	started := false
	firstGo := ".go xyz " + num32(stops[0].X) + " " + num32(stops[0].Y) + " "
	prevOnGM := f.agent.onGM
	f.agent.onGM = func(line string) {
		if prevOnGM != nil {
			prevOnGM(line)
		}
		if armed.Load() {
			return
		}
		if !started {
			started = strings.HasPrefix(line, firstGo)
			return
		}
		if strings.HasPrefix(line, ".go xyz ") {
			armed.Store(true) // this stop's check already sees the other copy
		}
	}
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if cmd == "q inst" && armed.Load() {
			return ttReplyOf(t, map[string]any{"map": 309, "inst": 104}), nil
		}
		return tk.answer(cmd)
	}
	postFlipReads, postFlipKills := 0, 0
	f.agent.unitsFn = func(int) []objects.Unit {
		dead := func(g packet.GUID) bool { f.agent.mu.Lock(); defer f.agent.mu.Unlock(); return f.agent.killed[g] }
		if armed.Load() {
			postFlipReads++
		}
		return gmClearView(f, pack, guids, dead)
	}
	f.agent.killFn = func(g packet.GUID, tries int) bool {
		if armed.Load() {
			postFlipKills++
		}
		return true
	}
	f.agent.startReply = zgPlaceReply(t)
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d: %s\n%s", code, exitRaidWalkFailed, f.stderr.String(), f.stdout.String())
	}
	if reason := f.summary().EndReason; !strings.Contains(reason, "not the raid's 103") {
		t.Fatalf("end reason %q, want the second stop's instance mismatch", reason)
	}
	// The second stop was visited, and the first stop killed before the copy moved.
	secondGo := "gm .go xyz " + num32(stops[1].X) + " " + num32(stops[1].Y) + " "
	seen, kills, killedFar := false, 0, false
	for _, l := range f.agentLog() {
		seen = seen || strings.HasPrefix(l, secondGo)
		if g, ok := strings.CutPrefix(l, "gm_kill "); ok {
			kills++
			killedFar = killedFar || g == far.String()
		}
	}
	if !seen {
		t.Fatalf("no %q in the log: the walk never reached the second stop", secondGo)
	}
	if kills == 0 {
		t.Fatal("no kill went out: the first stop must clear before the copy moves")
	}
	if postFlipReads != 0 {
		t.Fatalf("%d units reads after the second stop's .go, want none: no read without the instance check", postFlipReads)
	}
	if postFlipKills != 0 || killedFar {
		t.Fatalf("%d post-move kills (far member killed: %v): no kill without the instance check", postFlipKills, killedFar)
	}
}

// Round 1 sees part of a pack and misses one member; the second round still runs and kills it. The far member hides
// through round 1's reads (one read per stop) and shows from round 2 on: the pack is unsettled while a member is
// unseen, so round 2 walks again and the far member's own stop kills it. The clear is whole.
func TestPlaceGMClearSecondRoundKillsAMemberMissedInRoundOne(t *testing.T) {
	_, guids := gmClearPack(t, gmClearZGSpawns)
	far := guids[len(guids)-1]
	dir := gmClearFarExport(t, raidwalk.SpawnGUID(far))
	pack, guids := gmClearPack(t, dir)
	stops := pack.Stops()
	if len(stops) < 2 {
		t.Fatalf("a pack wider than the view needs several stops, has %d", len(stops))
	}
	f, tk := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = dir
	var quiet atomic.Bool
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "quiet") {
			quiet.Store(true)
		}
		return tk.answer(cmd)
	}
	reads := 0
	f.agent.unitsFn = func(int) []objects.Unit {
		dead := func(g packet.GUID) bool { f.agent.mu.Lock(); defer f.agent.mu.Unlock(); return f.agent.killed[g] }
		if quiet.Load() { // the walk's quiet check, after the clear
			return gmClearView(f, pack, guids, dead)
		}
		reads++
		us := gmClearView(f, pack, guids, dead)
		if reads <= len(stops) {
			// Round 1's reads: the far member never shows, so the pack ends the round with a member unseen.
			kept := us[:0]
			for _, u := range us {
				if u.GUID != far {
					kept = append(kept, u)
				}
			}
			us = kept
		}
		return us
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.GMClearPartial || len(sum.GMClear) != len(guids) {
		t.Fatalf("partial %v, gm_clear %+v", sum.GMClearPartial, sum.GMClear)
	}
	// Round 1 read every stop and killed nothing far; round 2 ran and read again.
	if reads <= len(stops) {
		t.Fatalf("%d clear reads for %d stops, want more: the unseen member must run the second round", reads, len(stops))
	}
	killedFar := false
	for _, l := range f.agentLog() {
		if l == "gm_kill "+far.String() {
			killedFar = true
		}
	}
	if !killedFar {
		t.Fatal("the member missed in round 1 was never killed in round 2")
	}
	for _, e := range sum.GMClear {
		if !e.Killed {
			t.Errorf("entry %+v", e)
		}
	}
}

// The clear's final instance check fails: after the packs are killed and the commander is back at the start point, a
// `q inst` naming another copy fails the walk on the existing error path, and no kill goes out after that read.
func TestPlaceGMClearFinalInstanceCheckFailureSendsNoKill(t *testing.T) {
	f, tk := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	// The copy moves under the walk once every live member is killed: every later `q inst` is the clear's final
	// check (kills, sleeps and unit reads send no `q inst` between the last kill and it).
	allKilled := func() bool {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		for _, g := range guids {
			if !f.agent.killed[g] {
				return false
			}
		}
		return true
	}
	var armed atomic.Bool
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if cmd == "q inst" && allKilled() {
			armed.Store(true)
			return ttReplyOf(t, map[string]any{"map": 309, "inst": 104}), nil
		}
		return tk.answer(cmd)
	}
	f.agent.unitsFn = func(int) []objects.Unit {
		dead := func(g packet.GUID) bool { f.agent.mu.Lock(); defer f.agent.mu.Unlock(); return f.agent.killed[g] }
		return gmClearView(f, pack, guids, dead)
	}
	postFlipKills := 0
	f.agent.killFn = func(g packet.GUID, tries int) bool {
		if armed.Load() {
			postFlipKills++
		}
		return true
	}
	f.agent.startReply = zgPlaceReply(t)
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d: %s\n%s", code, exitRaidWalkFailed, f.stderr.String(), f.stdout.String())
	}
	if reason := f.summary().EndReason; !strings.Contains(reason, "not the raid's 103") {
		t.Fatalf("end reason %q, want the final instance check's mismatch", reason)
	}
	if !armed.Load() {
		t.Fatal("the copy never moved: the run ended before the clear's final check")
	}
	if postFlipKills != 0 {
		t.Fatalf("%d kills after the failed final check, want none", postFlipKills)
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
		{Name: "tortoise-test3", Realmd: "10.3.3.3:3724", WorldAddr: "10.3.3.4:8085"},
	}
	cases := []struct {
		name, server, dialed string
		want                 bool
		reasonHas            string // "" = no reason expected
	}{
		{"test2 dialled at its world_addr", "tortoise-test2", "10.2.2.3:8085", true, ""},
		{"m910q dialled at its world_addr", "m910q", "10.1.1.1:8085", true, ""},
		{"test3 dialled at its world_addr", "tortoise-test3", "10.3.3.4:8085", true, ""},
		{"m910q same host, its realmd port", "m910q", "10.1.1.1:3724", false, "gm_kill refused"},
		{"m910q same host, another port", "m910q", "10.1.1.1:9999", false, "gm_kill refused"},
		{"test2 same host, another port", "tortoise-test2", "10.2.2.3:8086", false, "gm_kill refused"},
		{"settings say tortoise-test2, session dialled a live host", "tortoise-test2", "203.0.113.9:8085", false, "gm_kill refused"},
		{"settings say tortoise-test2, dialled its realmd host, not its world_addr", "tortoise-test2", "10.2.2.2:8085", false, "gm_kill refused"},
		{"settings say m910q, dialled the other test server", "m910q", "10.2.2.3:8085", false, "gm_kill refused"},
		{"settings say test3, dialled another test server", "tortoise-test3", "10.1.1.1:8085", false, "gm_kill refused"},
		{"dialled address without a port", "m910q", "10.1.1.1", false, "gm_kill refused"},
		{"ordinary agent", "tortoise", "10.1.1.1:8085", false, ""},
		{"no server_name", "", "10.1.1.1:8085", false, ""},
		{"unknown server", "prod-realm", "10.3.3.4:8085", false, ""},
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
	table := `{"version":1,"servers":[{"name":"tortoise-test2","realmd":"10.2.2.2:3724","world_addr":"10.2.2.3:8085","slots":[]},{"name":"tortoise-test3","realmd":"10.3.3.3:3724","world_addr":"10.3.3.4:8085","slots":[]}]}`
	if err := os.WriteFile(filepath.Join(dir, "slots.json"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, why := gmKillAuthorize(st, "10.2.2.3:8085"); !got || why != "" {
		t.Fatalf("matching host: allowed %v, reason %q", got, why)
	}
	if got, why := gmKillAuthorize(config.Settings{ServerName: "tortoise-test3"}, "10.3.3.4:8085"); !got || why != "" {
		t.Fatalf("test3 matching host: allowed %v, reason %q", got, why)
	}
	if got, why := gmKillAuthorize(st, "203.0.113.9:8085"); got || !strings.Contains(why, "gm_kill refused") {
		t.Fatalf("live host under a test server_name: allowed %v, reason %q", got, why)
	}
	if got, why := gmKillAuthorize(config.Settings{ServerName: "tortoise"}, "203.0.113.9:8085"); got || why != "" {
		t.Fatalf("ordinary agent: allowed %v, reason %q", got, why)
	}
	if got, why := gmKillAuthorize(config.Settings{ServerName: "prod-realm"}, "10.3.3.4:8085"); got || why != "" {
		t.Fatalf("unknown server: allowed %v, reason %q", got, why)
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

// Walk 74: 28 of the --gm-clear's 53 stops visited a pack the corridor sweep had already despawned whole (run.log:
// `nothing to clear`, summary.txt: "1 of 1 members were already despawned by this walk's own clear"), 28 x ~2.1 s =
// 59 s spent walking to packs with no unit left. The settled check ran only from round 2 on, so round 1 always visited;
// a pack settled before its first visit must get no stop at all, exactly as the later rounds skip it.
func TestPlaceGMClearSkipsAPackTheCorridorClearSettledWhole(t *testing.T) {
	f, tk := newPlaceFixture(t)
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	tk.clearto = func(string) (int, error) { return len(guids), nil }
	tk.cleartoRemoved = func(string) []any {
		removed := make([]any, 0, len(guids))
		for _, g := range guids {
			removed = append(removed, map[string]any{"g": g.String(), "e": g.Entry(), "n": "despawned"})
		}
		return removed
	}
	// Nothing of the pack is left in the client's store either (the corridor clear despawned every member).
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	for _, stop := range pack.Stops() {
		goLine := "gm .go xyz " + num32(stop.X) + " " + num32(stop.Y) + " "
		if n := agentLinesPrefix(f, goLine); n != 0 {
			t.Fatalf("%d visits to the settled pack's stop (%s), want none", n, strings.TrimSpace(goLine))
		}
	}
	if n := agentLinesPrefix(f, "gm_kill "); n != 0 {
		t.Fatalf("%d kill lines for a pack its own clear despawned, want none", n)
	}
	sum := f.summary()
	if sum.GMClearPartial || len(sum.GMClear) != len(guids) {
		t.Fatalf("partial %v, gm_clear %+v", sum.GMClearPartial, sum.GMClear)
	}
	for _, e := range sum.GMClear {
		if !e.Killed || e.Reason != raidwalk.GMClearDespawned || e.Tries != 0 {
			t.Errorf("entry %+v, want killed, despawned, no line", e)
		}
	}
	if !hasNote(sum, "already despawned by this walk's own clear") {
		t.Errorf("notes %q do not explain the settled pack", sum.Notes)
	}
}

// gmLandAgent answers the two RPCs gmClearLand sends: the `.go xyz` (recorded with the fake clock) and the status
// polls. landAt is the status read that first reports a settled landing; never keeps z the session's guess for good
// (a navmesh hole).
type gmLandAgent struct {
	clock   *fakeClock
	goLine  string
	goAt    time.Time
	reads   int  // status reads after the `.go` (the fake's landing reads)
	landing bool // the `.go` went out, so the reads now describe the landing
	landAt  int  // the first landing read at which the position is settled (0: every read)
	seqAt   int  // the first landing read at which the server's teleport ack moves TeleportSeq (0: never)
	bumped  bool
	never   bool
	seq     uint64
	units   int
	unitAt  int // the first units read that holds the pack's unit (0: never)
	at      []time.Time
	// statusCost is each status RPC's wall time (0: instant). The fake clock advances by the real time the call
	// took, so the walk's absolute deadline sees the RPC's cost; a context deadline cuts the cost short.
	statusCost time.Duration
}

// advance moves the fake clock by d: a fake RPC's simulated wall time.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// gmLandUnit is the pack member the fake's units read reports: the guid the tests poll for. gmLandPack keys it by its
// spawn guid, as a pack's SpawnGUIDs does.
var (
	gmLandUnit = packet.GUID(0xF13000012B000001)
	gmLandPack = []uint64{raidwalk.SpawnGUID(gmLandUnit)}
)

func (a *gmLandAgent) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	switch method {
	case "gm":
		text := params.(map[string]string)["text"]
		if strings.HasPrefix(text, ".go xyz ") {
			a.goLine, a.goAt, a.reads, a.units, a.landing = text, a.clock.now(), 0, 0, true
		}
	case "status":
		if a.statusCost > 0 {
			t0 := time.Now()
			timer := time.NewTimer(a.statusCost)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
			}
			a.clock.advance(time.Since(t0))
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		a.reads++
		a.at = append(a.at, a.clock.now())
		if a.landing && a.seqAt > 0 && a.reads >= a.seqAt && !a.bumped {
			a.bumped = true
			a.seq++ // the server's teleport ack, counted by the session
		}
		st := out.(*session.StatusInfo)
		*st = session.StatusInfo{State: session.StatusInWorld, Map: 309, X: 10, Y: 20, Z: 30, TeleportSeq: a.seq}
		if !a.never && a.landing && a.reads < a.landAt {
			st.ZUnknown = true // the destination is reported, the position is still the session's guess
		}
	case "units":
		a.units++
		us := out.(*[]objects.Unit)
		*us = nil
		if a.unitAt > 0 && a.units >= a.unitAt {
			*us = []objects.Unit{{GUID: gmLandUnit}}
		}
	}
	return nil
}

// Walk 74's clear paid a fixed 2 s after every `.go xyz` (53 stops, 157 s for the phase). The landing is now polled
// every gmClearLandPoll and needs the session's teleport count to move, so a stop the server settles at the first read
// carries on after the gmClearSettle settle, one that settles a poll later waits just that poll, and a status that
// never lands falls back to the old fixed 2 s.
func TestGMClearLandPollsTheLanding(t *testing.T) {
	newWalk := func(t *testing.T, a *gmLandAgent) *walk {
		return &walk{plan: &dungeon.Plan{Map: 309}, env: walkEnv{now: a.clock.now, sleep: a.clock.sleep}, agent: a}
	}
	t.Run("the first read lands", func(t *testing.T) {
		a := &gmLandAgent{clock: newFakeClock(), landAt: 1, seqAt: 1}
		start := a.clock.now()
		if err := newWalk(t, a).gmClearLand(context.Background(), 10, 20, 30); err != nil {
			t.Fatal(err)
		}
		if got := a.clock.now().Sub(start); got != gmClearSettle {
			t.Fatalf("a landed stop took %s, want the %s settle", got, gmClearSettle)
		}
		if a.goLine != ".go xyz 10 20 30 309" {
			t.Fatalf("go line %q, want the stop on the plan's map", a.goLine)
		}
		if a.reads != 1 {
			t.Fatalf("%d status reads, want 1", a.reads)
		}
	})
	t.Run("a later poll lands", func(t *testing.T) {
		a := &gmLandAgent{clock: newFakeClock(), landAt: 3, seqAt: 3}
		start := a.clock.now()
		if err := newWalk(t, a).gmClearLand(context.Background(), 10, 20, 30); err != nil {
			t.Fatal(err)
		}
		if got, want := a.clock.now().Sub(start), 2*gmClearLandPoll+gmClearSettle; got != want {
			t.Fatalf("a stop that settles on the 3rd read took %s, want %s", got, want)
		}
		if a.reads != 3 {
			t.Fatalf("%d status reads, want 3", a.reads)
		}
	})
	t.Run("no landing falls back to the fixed wait", func(t *testing.T) {
		a := &gmLandAgent{clock: newFakeClock(), never: true}
		start := a.clock.now()
		if err := newWalk(t, a).gmClearLand(context.Background(), 10, 20, 30); err != nil {
			t.Fatal(err)
		}
		if got, want := a.clock.now().Sub(start), gmClearLandWait+gmClearFallbackWait; got != want {
			t.Fatalf("an unsettled landing took %s, want the ceiling and the old wait (%s)", got, want)
		}
		if want := int(gmClearLandWait / gmClearLandPoll); a.reads != want {
			t.Fatalf("%d status reads, want %d", a.reads, want)
		}
	})
	// Codex round 2 HIGH: `waited` counted only the sleeps, so each status RPC (up to 10 s) stretched the 3 s
	// ceiling. The ceiling is a deadline: a 2 s status read still gives up at gmClearLandWait and falls back.
	t.Run("a slow status cannot stretch the ceiling", func(t *testing.T) {
		a := &gmLandAgent{clock: newFakeClock(), never: true, statusCost: 2 * time.Second}
		if err := newWalk(t, a).gmClearLand(context.Background(), 10, 20, 30); err != nil {
			t.Fatal(err)
		}
		if got := a.clock.now().Sub(a.goAt); got > gmClearLandWait+gmClearFallbackWait+500*time.Millisecond {
			t.Fatalf("an unsettled landing with a 2 s status took %s, want the %s ceiling and the %s fallback",
				got, gmClearLandWait, gmClearFallbackWait)
		}
		if a.reads > 2 {
			t.Fatalf("%d status reads, want at most 2: the deadline stops the poll", a.reads)
		}
	})
}

// The stop's units read is a poll of the pack: the landing arrives before the object store holds it, so the read
// carries on as soon as a pack member shows and otherwise ends at the old 2 s ceiling.
func TestGMClearStopUnitsPollsThePack(t *testing.T) {
	newWalk := func(t *testing.T, a *gmLandAgent) *walk {
		return &walk{plan: &dungeon.Plan{Map: 309}, env: walkEnv{now: a.clock.now, sleep: a.clock.sleep}, agent: a}
	}
	t.Run("the first read shows the pack", func(t *testing.T) {
		a := &gmLandAgent{clock: newFakeClock(), unitAt: 1}
		start := a.clock.now()
		units, err := newWalk(t, a).gmClearStopUnits(context.Background(), "2:2", gmLandPack)
		if err != nil {
			t.Fatal(err)
		}
		if got := a.clock.now().Sub(start); got != 0 {
			t.Fatalf("a visible pack took %s, want none", got)
		}
		if a.units != 1 || !gmClearUnitsShown(units, gmLandPack) {
			t.Fatalf("%d units reads, shown %v, want 1 and the pack", a.units, gmClearUnitsShown(units, gmLandPack))
		}
	})
	t.Run("a late pack is polled", func(t *testing.T) {
		a := &gmLandAgent{clock: newFakeClock(), unitAt: 3}
		start := a.clock.now()
		if _, err := newWalk(t, a).gmClearStopUnits(context.Background(), "2:2", gmLandPack); err != nil {
			t.Fatal(err)
		}
		if got, want := a.clock.now().Sub(start), 2*gmClearUnitPoll; got != want {
			t.Fatalf("a pack that shows on the 3rd read took %s, want %s", got, want)
		}
		if a.units != 3 {
			t.Fatalf("%d units reads, want 3", a.units)
		}
	})
	t.Run("no pack ends at the ceiling", func(t *testing.T) {
		a := &gmLandAgent{clock: newFakeClock()}
		start := a.clock.now()
		if _, err := newWalk(t, a).gmClearStopUnits(context.Background(), "2:2", gmLandPack); err != nil {
			t.Fatal(err)
		}
		if got := a.clock.now().Sub(start); got != gmClearUnitWait {
			t.Fatalf("an empty read took %s, want the %s ceiling", got, gmClearUnitWait)
		}
		if want := int(gmClearUnitWait/gmClearUnitPoll) + 1; a.units != want {
			t.Fatalf("%d units reads, want %d", a.units, want)
		}
	})
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
