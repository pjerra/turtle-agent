package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// zgWalk19Fixture is a Zul'Gurub walk with the dashboard off (walk 19 test2 had none), --until step 184 and
// two recoveries, one already spent (wipe-resume 1/2 at step 29).
func zgWalk19Fixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.opts.plan, f.opts.holder = "zul_gurub", "raid-walk-zul_gurub"
	f.opts.until = "step 184"
	f.opts.wipeResume = 2
	p, err := dungeon.Load(filepath.Join(f.opts.dir, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.agent.instMap = p.Map
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{
			"TBM:RUN|zul_gurub|running|41/1104|high_priestess_jeklik|boss|fight|fighting|until step 184 (step 184)",
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	return f
}

// TestRaidWalkWipeResumeWalk19TrashTigerOffLegRecovers replays ZG walk 19 test2 s4 (frames.jsonl 02:36-02:40,
// run.log step 89): Jeklik 14517 engaged and killed long before; at step 89 (leg high_priest_venoxis) the raid
// fights Zulian Tigers 11361 as trash (60 %, a target) until 88 s before the wipe, while Venoxis 14507 and his
// Razzashi Cobra 11373 stand in view at 100 % with no target until 38 s before it; then all 19 hires die
// (TB's "2 near wipes" pause). 11361 is a `first` add of Thekal (another leg), so the tiger fight is trash: no
// boss is engaged, no dashboard, one recovery left = the wipe recovers. On 9eb13bf the tiger rows latched
// bossLatchAt and bossUnknown skipped ("wipe resume skipped: boss state unknown", exit 19).
func TestRaidWalkWipeResumeWalk19TrashTigerOffLegRecovers(t *testing.T) {
	f := zgWalk19Fixture(t)
	f.opts.wipeResume = 2
	t0 := f.clock.now()
	onVenoxis, wiped := false, false
	wipeAt := t0.Add(800 * time.Second)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		el := now.Sub(t0)
		if !onVenoxis && el >= 300*time.Second {
			onVenoxis = true
			f.agent.mu.Lock()
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: "TBM:RUN|zul_gurub|running|89/1104|high_priest_venoxis|pull|begin|fighting|until step 184 (step 184)"})
			f.agent.mu.Unlock()
		}
		if !wiped && !now.Before(wipeAt) {
			wiped = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: "TBM:RUN|zul_gurub|paused|89/1104|high_priest_venoxis|pull|begin|19 dead: 2 near wipes within 10 min (steps 89 and 89, 0 min 4 s apart; 2 wipes, 3 near wipes this run): the run stops here. Regroup (cmd revive party), then .bot run resume, or .bot run stop|until step 184 (step 184)"})
			f.agent.mu.Unlock()
		}
		var rows []tbm.HostileRec
		if el < 45*time.Second {
			// Jeklik's fight on her boss step: engaged, the run leader fights it.
			rows = append(rows, tbm.HostileRec{GUID: 9, Entry: 14517, HPPct: 40, Target: packet.GUID(1000)})
		}
		if el >= 310*time.Second && now.Before(wipeAt.Add(-38*time.Second)) {
			rows = append(rows,
				tbm.HostileRec{GUID: 20, Entry: 14507, HPPct: 100, Target: 0},
				tbm.HostileRec{GUID: 21, Entry: 11373, HPPct: 100, Target: 0})
		}
		if !now.Before(wipeAt.Add(-150*time.Second)) && now.Before(wipeAt.Add(-88*time.Second)) {
			rows = append(rows,
				tbm.HostileRec{GUID: 30, Entry: 11361, HPPct: 60, Target: packet.GUID(1001)},
				tbm.HostileRec{GUID: 31, Entry: 11361, HPPct: 27, Target: packet.GUID(1002)})
		}
		return rows
	}
	resumes := 0
	f.agent.onResume = func() {
		resumes++
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: "TBM:RUN|zul_gurub|running|89/1104|high_priest_venoxis|pull|begin|resumed|until step 184 (step 184)"},
			queuedEvent{raw: "TBM:RUN|zul_gurub|done|184/1104|high_priest_venoxis|move|walk|stopped before step 184 (until step 184): the party stays where it stands"})
	}
	code := f.run(context.Background())
	sum := f.summary()
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip (a trash tiger of another leg is no boss)", sum.Notes)
	}
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if sum.WipeResumes != 1 || resumes != 1 {
		t.Fatalf("wipe_resumes %d, resumes %d, want 1 and 1", sum.WipeResumes, resumes)
	}
	if !hasNote(sum, "wipe-resume 1/2 at step 89") {
		t.Fatalf("notes %q, want the step-89 recovery note", sum.Notes)
	}
}

// TestRaidWalkWipeResumeWalk19TigerOnThekalLegSkips: the same frames, but the run is on Thekal's leg, where
// the Zulian Tigers are the boss's own `first` adds. An engaged tiger 88 s before the wipe still latches and
// the wipe ends the old way, with the latch named in the note.
func TestRaidWalkWipeResumeWalk19TigerOnThekalLegSkips(t *testing.T) {
	f := zgWalk19Fixture(t)
	t0 := f.clock.now()
	onThekal, wiped := false, false
	wipeAt := t0.Add(800 * time.Second)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		el := now.Sub(t0)
		if !onThekal && el >= 300*time.Second {
			onThekal = true
			f.agent.mu.Lock()
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: "TBM:RUN|zul_gurub|running|150/1104|high_priest_thekal|pull|begin|fighting|until step 184 (step 184)"})
			f.agent.mu.Unlock()
		}
		if !wiped && !now.Before(wipeAt) {
			wiped = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
		var rows []tbm.HostileRec
		if el < 45*time.Second {
			rows = append(rows, tbm.HostileRec{GUID: 9, Entry: 14517, HPPct: 40, Target: packet.GUID(1000)})
		}
		if !now.Before(wipeAt.Add(-150*time.Second)) && now.Before(wipeAt.Add(-88*time.Second)) {
			rows = append(rows, tbm.HostileRec{GUID: 30, Entry: 11361, HPPct: 60, Target: packet.GUID(1001)})
		}
		return rows
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0", sum.WipeResumes)
	}
	want := "wipe resume skipped: boss state unknown (boss 11361 latched"
	found := false
	for _, n := range sum.Notes {
		if strings.HasPrefix(n, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes %q, want a note starting %q", sum.Notes, want)
	}
}

// TestBossRowCountsFirstAddOnlyOnItsLeg pins the walk-19 rule: a boss or hold entry always counts, a `first`
// add counts on its own boss's leg and whenever the run's leg is unknown (fail closed), never on another known leg.
func TestBossRowCountsFirstAddOnlyOnItsLeg(t *testing.T) {
	w := &walk{
		bossSet:   map[uint32]bool{14599: true, 11361: true, 14507: true, 11373: true, 500: true},
		bossCore:  map[uint32]bool{14599: true, 14507: true, 500: true},
		firstLegs: map[uint32][]string{11361: {"high_priest_thekal"}, 11373: {"high_priest_venoxis"}},
		legNames:  map[string]bool{"high_priest_thekal": true, "high_priest_venoxis": true},
	}
	for _, tc := range []struct {
		name  string
		have  bool
		leg   string
		entry uint32
		want  bool
	}{
		{"not a plan entry", true, "high_priest_venoxis", 9999, false},
		{"boss on another leg", true, "high_priest_venoxis", 14599, true},
		{"hold entry", true, "high_priest_venoxis", 500, true},
		{"first add on its own leg", true, "high_priest_venoxis", 11373, true},
		{"first add on another known leg (walk 19)", true, "high_priest_venoxis", 11361, false},
		{"first add before any status", false, "", 11361, true},
		{"first add with no leg", true, "", 11361, true},
		{"first add with a numbered leg", true, "3", 11361, true},
	} {
		w.haveStatus, w.lastStatus = tc.have, tbm.RunStatus{Leg: tc.leg}
		if got := w.bossRowCounts(tc.entry); got != tc.want {
			t.Errorf("%s: bossRowCounts(%d) = %v, want %v", tc.name, tc.entry, got, tc.want)
		}
	}
}
