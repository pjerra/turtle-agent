package trashcheck

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/patrols"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// Synthetic worlds and plans: the model is pure over the rows and the export, so every case is built here and only the
// Onyxia test reads the real data. The cases stand at x 1000 and up: a row at the zero point has no position (the exporter's
// own convention), so a test point is never the origin.

func spawnAt(guid uint64, entry uint32, name string, x, y, z float32) worldspawn.Spawn {
	return worldspawn.Spawn{
		GUID: guid, Entry: entry, Name: name, LevelMax: 60, Rank: 1, Faction: 103,
		Pos: dungeon.Point{X: x, Y: y, Z: z}, DetectionYd: 20, SpeedWalk: 1,
	}
}

func worldOf(spawns ...worldspawn.Spawn) *worldspawn.World {
	return &worldspawn.World{Map: 249, Spawns: spawns}
}

// rowsOf numbers the steps in order (one leg) as the exporter and the server do.
func rowsOf(steps ...planexport.StepRow) planexport.Rows {
	for i := range steps {
		steps[i].Plan = "probe"
		steps[i].Leg = 0
		steps[i].Seq = i
	}
	return planexport.Rows{Plan: planexport.PlanRow{Plan: "probe", Map: 249}, Steps: steps}
}

func move(x, y, z float32) planexport.StepRow {
	return planexport.StepRow{Kind: planexport.KindMove, X: x, Y: y, Z: z, Comment: "path"}
}

func pull(x, y, z float32, entries ...uint32) planexport.StepRow {
	return planexport.StepRow{Kind: planexport.KindPull, X: x, Y: y, Z: z, Radius: 1, Entries: entries, Comment: "pull"}
}

func opts() Options { return Options{Level: 60, Spread: 3, AggroRate: 1, AssistYd: 10} }

// segTo is the segment ending at a step.
func segTo(t *testing.T, rep *Report, step int) Segment {
	t.Helper()
	for _, s := range rep.Segments {
		if s.To.Step == step {
			return s
		}
	}
	t.Fatalf("no segment ends at step %d (segments %d)", step, len(rep.Segments))
	return Segment{}
}

// segPack is the segment's entry for a pack id.
func segPack(t *testing.T, s Segment, id uint64) SegmentPack {
	t.Helper()
	for _, p := range s.Packs {
		if p.Pack == id {
			return p
		}
	}
	t.Fatalf("segment %d->%d has no pack %d (%+v)", s.From.Step, s.To.Step, id, s.Packs)
	return SegmentPack{}
}

// A pack the walk passes 21 yd from, with an aggro radius of 20 and a spread of 3, wakes: the danger is reported before the
// step that pulls it, and that step is the plan's own pull.
func TestBodyPullFlaggedBeforeTargeting(t *testing.T) {
	w := worldOf(spawnAt(1, 100, "Trash", 1050, 21, 0))
	rep := Check(rowsOf(move(1000, 0, 0), move(1100, 0, 0), move(1200, 0, 0), pull(1050, 21, 0, 100)), w, opts())

	// The spread is what makes 21 yd a wake: without it (reach 20) the segment would be clear.
	first := segTo(t, rep, 2)
	if first.Verdict != VerdictDanger {
		t.Fatalf("step 1->2: verdict %q, want danger (21 yd from a 20 yd aggro radius + 3 yd spread)", first.Verdict)
	}
	p := segPack(t, first, 1)
	if p.Class != ClassDanger || p.TargetedAt == nil || *p.TargetedAt != 4 {
		t.Fatalf("pack class %q targeted %v, want danger and targeted at step 4", p.Class, p.TargetedAt)
	}
	if len(p.Wakers) != 1 || p.Wakers[0].GUID != 1 || p.Wakers[0].MinYd < 20.9 || p.Wakers[0].MinYd > 21.1 {
		t.Fatalf("wakers %+v, want guid 1 at 21 yd", p.Wakers)
	}
	text := rep.Text()
	for _, want := range []string{"DANGER: body pull: step 2 walks within 21.0 yd of Trash", "targeted at step 4", "pack of 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	// The pull row itself is the plan's clear of the pack.
	if own := segTo(t, rep, 4); own.Verdict != VerdictOwn {
		t.Errorf("step 3->4: verdict %q, want own", own.Verdict)
	}
}

// The approach stand right before the pull is the intended clear too: walking to it is not a body pull.
func TestOwnPullIsNotDanger(t *testing.T) {
	w := worldOf(spawnAt(1, 100, "Trash", 1050, 21, 0))
	rep := Check(rowsOf(move(1000, 0, 0), move(1050, 0, 0), pull(1050, 21, 0, 100)), w, opts())

	if s := segTo(t, rep, 2); s.Verdict != VerdictOwn {
		t.Fatalf("step 1->2 (to the stand): verdict %q, want own", s.Verdict)
	}
	if s := segTo(t, rep, 3); s.Verdict != VerdictOwn {
		t.Fatalf("step 2->3 (the pull): verdict %q, want own", s.Verdict)
	}
	if rep.Counts.Danger != 0 {
		t.Errorf("%d danger segments, want 0", rep.Counts.Danger)
	}
}

// A pack the plan cleared is gone: the later walk past its spawn wakes nothing, and a cleared patrol's loop is no window either.
func TestDeadPackIgnoredAfterTargeting(t *testing.T) {
	puller := spawnAt(1, 100, "Trash", 1010, 0, 0)
	warder := spawnAt(2, 100, "Warder", 1010, 0, 0)
	warder.MovementType = 2
	w := worldOf(puller, warder)
	w.Patrols = []worldspawn.Patrol{{
		ID: "2", GUIDs: []string{"0xF13000641000C2"}, SpawnGUIDs: []uint64{2}, Entries: []uint32{100},
		Name: "Warder", Path: []dungeon.Point{{X: 1008, Y: 0}, {X: 1012, Y: 0}},
		WaitsMS: []int{0, 0}, PeriodS: 3.2, PeriodEstimate: true, Source: "creature_movement 2",
	}}
	rep := Check(rowsOf(move(1000, 0, 0), pull(1010, 0, 0, 100), move(1100, 0, 0), move(1010, 0, 0)), w, opts())

	for _, step := range []int{3, 4} {
		s := segTo(t, rep, step)
		if s.Verdict != VerdictClear || len(s.Packs) != 0 || len(s.Patrols) != 0 {
			t.Errorf("step %d: verdict %q with %d packs and %d patrols, want clear after the step 2 pull", step, s.Verdict, len(s.Packs), len(s.Patrols))
		}
	}
}

// A pack no row clears stays alive: the whole walk passes it as a danger.
func TestUnplannedPackIsDangerAllWalk(t *testing.T) {
	w := worldOf(spawnAt(1, 100, "Trash", 1050, 10, 0))
	rep := Check(rowsOf(move(1000, 0, 0), move(1050, 0, 0), move(1100, 0, 0)), w, opts())

	for _, step := range []int{2, 3} {
		s := segTo(t, rep, step)
		if s.Verdict != VerdictDanger {
			t.Fatalf("step %d: verdict %q, want danger", step, s.Verdict)
		}
		p := segPack(t, s, 1)
		if !p.Unplanned || p.TargetedAt != nil {
			t.Errorf("step %d: unplanned %v targeted %v, want unplanned", step, p.Unplanned, p.TargetedAt)
		}
	}
	if !strings.Contains(rep.Text(), "unplanned") {
		t.Errorf("text does not name the unplanned pack:\n%s", rep.Text())
	}
}

// The family assist hop is one level: C, 8 yd from B but 16 yd from the waker A, stays out of the fight.
func TestAssistHopOneLevelOnly(t *testing.T) {
	a := spawnAt(1, 100, "A", 1050, 0, 0)
	b := spawnAt(2, 100, "B", 1050, 8, 0)
	c := spawnAt(3, 100, "C", 1050, 16, 0)
	d := spawnAt(4, 100, "Other faction", 1050, -8, 0)
	d.Faction = 14
	for _, s := range []*worldspawn.Spawn{&a, &b, &c, &d} {
		s.DetectionYd = 3
	}
	rep := Check(rowsOf(move(1000, 0, 0), move(1100, 0, 0)), worldOf(a, b, c, d), Options{Level: 60, AssistYd: 10})
	s := segTo(t, rep, 2)

	pa := segPack(t, s, 1)
	if len(pa.Wakers) != 1 || pa.Wakers[0].GUID != 1 {
		t.Fatalf("A's wakers %+v, want only guid 1", pa.Wakers)
	}
	pb := segPack(t, s, 2)
	if len(pb.Wakers) != 0 || len(pb.Assisted) != 1 || pb.Assisted[0] != 2 {
		t.Fatalf("B assisted %v wakers %+v, want assisted [2] and no waker", pb.Assisted, pb.Wakers)
	}
	for _, p := range s.Packs {
		if p.Pack == 3 {
			t.Fatalf("C is in the fight: the assist hop is one level, not a chain")
		}
		if p.Pack == 4 {
			t.Fatalf("the other-faction spawn joined: the assist hop is same-faction only")
		}
	}
}

// A spawn with NO_ASSIST never joins another's fight.
func TestNoAssistFlagHonoured(t *testing.T) {
	a := spawnAt(1, 100, "A", 1050, 0, 0)
	b := spawnAt(2, 100, "NoAssist", 1050, 8, 0)
	for _, s := range []*worldspawn.Spawn{&a, &b} {
		s.DetectionYd = 3
	}
	b.FlagsExtra = worldspawn.NoAssist
	rep := Check(rowsOf(move(1000, 0, 0), move(1100, 0, 0)), worldOf(a, b), Options{Level: 60, AssistYd: 10})
	s := segTo(t, rep, 2)
	for _, p := range s.Packs {
		if p.Pack == 2 {
			t.Fatalf("the NO_ASSIST spawn joined: %+v", p)
		}
	}
}

// An aggro link joins two spawns into one pack: the master comes along though only the slave woke.
func TestLinkedPackBroughtAlong(t *testing.T) {
	a := spawnAt(1, 100, "Master", 1050, 0, 0)
	b := spawnAt(2, 100, "Slave", 1050, 8, 0)
	for _, s := range []*worldspawn.Spawn{&a, &b} {
		s.DetectionYd = 3
	}
	w := worldOf(a, b)
	w.Links = []worldspawn.Link{{Slave: 2, Master: 1, Flag: 0x1}}
	rep := Check(rowsOf(move(1000, 0, 0), move(1100, 0, 0)), w, Options{Level: 60, AssistYd: 1})
	s := segTo(t, rep, 2)
	if s.Verdict != VerdictDanger {
		t.Fatalf("verdict %q, want danger", s.Verdict)
	}
	p := segPack(t, s, 1)
	if p.Brings != 2 || len(p.Wakers) != 1 || len(s.Packs) != 1 {
		t.Fatalf("pack %+v (%d packs), want one pack of 2 with one waker", p, len(s.Packs))
	}
	if !strings.Contains(rep.Text(), "pack of 2") {
		t.Errorf("text does not bring the linked spawn:\n%s", rep.Text())
	}
}

// The 40 yd square loop (period 64 s at 1 yd walk speed, 2.5 yd/s) crosses the route on one side: one window, 18.4 s.
func TestPatrolWindowsSquareLoop(t *testing.T) {
	lead := spawnAt(52050, 12129, "Warder", 980, 20, 0)
	lead.MovementType = 2
	w := worldOf(lead)
	w.Patrols = []worldspawn.Patrol{{
		ID: "52050", GUIDs: []string{"0xF130002F61000CB2"}, SpawnGUIDs: []uint64{52050}, Entries: []uint32{12129},
		Name: "Warder",
		Path: []dungeon.Point{
			{X: 980, Y: 20}, {X: 1020, Y: 20}, {X: 1020, Y: 60}, {X: 980, Y: 60},
		},
		WaitsMS: []int{0, 0, 0, 0}, PeriodS: 64, PeriodEstimate: true, Source: "creature_movement 52050",
	}}
	rep := Check(rowsOf(move(960, 0, 0), move(1040, 0, 0)), w, opts())
	s := segTo(t, rep, 2)
	if s.Verdict != VerdictPatrol {
		t.Fatalf("verdict %q, want patrol", s.Verdict)
	}
	if len(s.Patrols) != 1 {
		t.Fatalf("%d patrols, want 1: %+v", len(s.Patrols), s.Patrols)
	}
	h := s.Patrols[0]
	if len(h.Windows) != 1 {
		t.Fatalf("windows %+v, want one (merged across the loop's start)", h.Windows)
	}
	if got := h.Windows[0].ToS - h.Windows[0].FromS; got < 17.4 || got > 19.4 {
		t.Errorf("window %.1f-%.1f s = %.1f s, want 18.4 s +- 1", h.Windows[0].FromS, h.Windows[0].ToS, got)
	}
	if h.At != "route" {
		t.Errorf("at %q, want route", h.At)
	}
	if h.PeriodS != 64 || h.Estimate != true {
		t.Errorf("period %.1f estimate %v, want 64 and an estimate", h.PeriodS, h.Estimate)
	}
}

// Step 1 is a stand (the revive point): a loop 23 yd from it is inside the stand's reach (aggro 20 + 5) though not the route's
// (aggro 20), so the window is reported even where the walk does not pass.
func TestStepOneIsAStand(t *testing.T) {
	lead := spawnAt(52051, 12129, "Warder", 1010, 23, 0)
	w := worldOf(lead)
	w.Patrols = []worldspawn.Patrol{{
		ID: "52051", GUIDs: []string{"0xF130002F61000CB3"}, SpawnGUIDs: []uint64{52051}, Entries: []uint32{12129},
		Name:    "Warder",
		Path:    []dungeon.Point{{X: 1008, Y: 23}, {X: 1012, Y: 23}},
		WaitsMS: []int{0, 0}, PeriodS: 3.2, PeriodEstimate: true, Source: "creature_movement 52051",
	}}
	rep := Check(rowsOf(move(1010, 0, 0), move(1100, 0, 0)), w, Options{Level: 60, AssistYd: 10})
	s := segTo(t, rep, 2)
	if len(s.Patrols) != 1 {
		t.Fatalf("%d patrols, want the step-1 window: %+v", len(s.Patrols), s.Patrols)
	}
	if s.Patrols[0].At != "start" {
		t.Fatalf("at %q, want start (step 1 is the stand)", s.Patrols[0].At)
	}
	if s.Verdict != VerdictPatrol {
		t.Errorf("verdict %q, want patrol", s.Verdict)
	}
}

// A spawn 20 yd below the route (all samples within its 2D reach) is on another floor: ignored and counted. One 10 yd below is
// not, and wakes.
func TestOtherFloorIgnored(t *testing.T) {
	w := worldOf(spawnAt(1, 100, "Under floor", 1050, 0, -20), spawnAt(2, 100, "Down a ledge", 1050, 0, -10))
	rep := Check(rowsOf(move(1000, 0, 0), move(1100, 0, 0)), w, Options{Level: 60, AggroRate: 1, AssistYd: 10})
	if !hasNote(rep, "1 spawns on other floors ignored") {
		t.Errorf("notes %v, want the other-floor count", rep.Notes)
	}
	s := segTo(t, rep, 2)
	if s.Verdict != VerdictDanger {
		t.Fatalf("verdict %q, want danger from the 10 yd ledge spawn", s.Verdict)
	}
	p := segPack(t, s, 2)
	if len(p.Wakers) != 1 || p.Wakers[0].GUID != 2 {
		t.Errorf("wakers %+v, want only guid 2", p.Wakers)
	}
	for _, pk := range s.Packs {
		if pk.Pack == 1 {
			t.Errorf("the other-floor spawn woke: %+v", pk)
		}
	}
}

// A recording replaces the DB loop it names and a second one is added.
func TestRecordedPatrolReplacesDBPatrol(t *testing.T) {
	lead := spawnAt(52050, 12129, "Warder", 1000, 0, 0)
	lead.MovementType = 2
	w := worldOf(lead)
	w.Patrols = []worldspawn.Patrol{{
		ID: "52050", GUIDs: []string{"0xF130002F61000CB2"}, SpawnGUIDs: []uint64{52050}, Entries: []uint32{12129},
		Name: "Warder", Path: []dungeon.Point{{X: 1000, Y: 0}, {X: 1040, Y: 0}, {X: 1040, Y: 40}, {X: 1000, Y: 40}},
		WaitsMS: []int{0, 0, 0, 0}, PeriodS: 64, PeriodEstimate: true, Source: "creature_movement 52050",
	}}
	rec := []patrols.Patrol{
		{
			ID: "52050", GUIDs: []string{"0xF130002F61000CB2"}, SpawnGUIDs: []uint32{52050}, Entries: []uint32{12129},
			Name: "Warder", Path: []dungeon.Point{{X: 1000, Y: 0}, {X: 1005, Y: 0}},
			PeriodS: 33.4, PeriodEstimate: false, Source: "record m910q",
		},
		{
			ID: "777", Entries: []uint32{4242}, Name: "Other",
			Path: []dungeon.Point{{X: 900, Y: 0}, {X: 905, Y: 0}}, PeriodS: 12, PeriodEstimate: false,
		},
	}
	opt := opts()
	opt.Recorded = rec
	rep := Check(rowsOf(move(900, 0, 0), move(910, 0, 0)), w, opt)

	if len(rep.Patrols) != 2 {
		t.Fatalf("%d patrols, want 2 (the replacement and the added one): %+v", len(rep.Patrols), rep.Patrols)
	}
	var got *PatrolRow
	for i := range rep.Patrols {
		if rep.Patrols[i].ID == "52050" {
			if got != nil {
				t.Fatalf("two patrols with id 52050: the DB loop was not replaced")
			}
			got = &rep.Patrols[i]
		}
	}
	if got == nil {
		t.Fatalf("no patrol 52050 in %+v", rep.Patrols)
	}
	if got.PeriodS != 33.4 || got.Estimate || len(got.Path) != 2 || got.Points != 2 {
		t.Errorf("patrol 52050 %+v, want the recorded period 33.4, no estimate, its 2-point path", *got)
	}
}

// Text lists one line per finding and the counts line last (before the notes).
func TestTextCountsAndFindings(t *testing.T) {
	w := worldOf(spawnAt(1, 100, "Trash", 1050, 10, 0))
	rep := Check(rowsOf(move(1000, 0, 0), move(1100, 0, 0)), w, opts())
	text := rep.Text()
	lines := strings.Split(strings.TrimSpace(text), "\n")
	findings := 0
	for _, l := range lines {
		if strings.Contains(l, "DANGER:") || strings.Contains(l, "PATROL:") {
			findings++
		}
	}
	if findings != len(rep.Findings()) || findings == 0 {
		t.Errorf("%d finding lines, want %d:\n%s", findings, len(rep.Findings()), text)
	}
	want := fmt.Sprintf("probe: %d steps, %d segments: 0 clear, 0 own, 0 patrol, 1 danger", rep.Steps, len(rep.Segments))
	if !strings.Contains(text, want) {
		t.Errorf("text lacks %q:\n%s", want, text)
	}
}

func hasNote(rep *Report, want string) bool {
	for _, n := range rep.Notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}
