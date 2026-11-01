package main

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Positions from ZG walk 19 test2 s4 frames.jsonl and dungeons/zul_gurub.json.
var (
	w19TigerA = [2]float32{-11960.7, -1789.8} // trash Zulian Tiger 11361 at step 89, ~330 yd from Thekal
	w19TigerB = [2]float32{-11968.7, -1777.8}
	w19Venox  = [2]float32{-12029.8, -1707.9} // Venoxis 14507 at his plan position
	w19Cobra  = [2]float32{-12021.2, -1719.7} // Razzashi Cobra 11373 next to him
	zgThekal  = [2]float32{-11703.2, -1996.8} // Thekal 14599's plan position
	// zgTigerPull is the 11361 trash pull group nearest Thekal in zul_gurub.json (leg high_priest_thekal, pull
	// group index 4): 56.5 yd from his position.
	zgTigerPull = [2]float32{-11717.4, -1942.1}
)

// zgWalk19Fixture is a Zul'Gurub walk with the dashboard off (walk 19 test2 had none), --until step 184 and one
// recovery left (--wipe-resume 2; the live walk had spent one at step 29). The run starts on startLine.
func zgWalk19Fixture(t *testing.T, startLine string) *fixture {
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
		Ack:   &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line:  "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{startLine, "TBM:ACTION_ACK|run start|party|1|-"}}}
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	return f
}

const (
	w19JeklikStart = "TBM:RUN|zul_gurub|running|41/1104|high_priestess_jeklik|boss|fight|fighting|until step 184 (step 184)"
	w19PlainStart  = "TBM:RUN|zul_gurub|running|88/1104|high_priest_venoxis|move|walk|walking|until step 184 (step 184)"
	w19VenoxisLine = "TBM:RUN|zul_gurub|running|89/1104|high_priest_venoxis|pull|begin|fighting|until step 184 (step 184)"
)

// w19Scenario drives one walk-19 shaped walk: optionally Jeklik engaged on her boss step for the first 45 s (it
// spends the tracker's one-shot boss latch, as in the live walk), the run's status moving to legLine at 300 s,
// rows(now, wipeAt) in the frames from 310 s, and every hire dead at 800 s (with walk 19's "2 near wipes" pause
// line). A resume is answered with a running line and the until stop.
func w19Scenario(t *testing.T, jeklik bool, legLine string, rows func(now, wipeAt time.Time) []tbm.HostileRec) (*fixture, *int) {
	t.Helper()
	start := w19JeklikStart
	if !jeklik {
		start = w19PlainStart
	}
	f := zgWalk19Fixture(t, start)
	t0 := f.clock.now()
	moved, wiped := false, false
	wipeAt := t0.Add(800 * time.Second)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		el := now.Sub(t0)
		if !moved && el >= 300*time.Second {
			moved = true
			f.agent.mu.Lock()
			f.agent.lines = append(f.agent.lines, queuedEvent{raw: legLine})
			f.agent.mu.Unlock()
		}
		if !wiped && !now.Before(wipeAt) {
			wiped = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: "TBM:RUN|zul_gurub|paused|" + strings.Join(strings.Split(legLine, "|")[3:5], "|") + "|pull|begin|19 dead: 2 near wipes within 10 min (steps 89 and 89, 0 min 4 s apart; 2 wipes, 3 near wipes this run): the run stops here. Regroup (cmd revive party), then .bot run resume, or .bot run stop|until step 184 (step 184)"})
			f.agent.mu.Unlock()
		}
		var out []tbm.HostileRec
		if jeklik && el < 45*time.Second {
			out = append(out, tbm.HostileRec{GUID: 9, Entry: 14517, HPPct: 40, Target: packet.GUID(1000), X: -12289.1, Y: -1384.1})
		}
		if el >= 310*time.Second {
			out = append(out, rows(now, wipeAt)...)
		}
		return out
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
	return f, &resumes
}

// w19StepRows are the step-89 frames of walk 19: Venoxis and his cobra in view at 100 % with no target until 38 s
// before the wipe, and tigers 11361 engaged (a target, hurt) at the given spots from 150 s to 88 s before it.
func w19StepRows(tigers [][2]float32) func(now, wipeAt time.Time) []tbm.HostileRec {
	return func(now, wipeAt time.Time) []tbm.HostileRec {
		var out []tbm.HostileRec
		if now.Before(wipeAt.Add(-38 * time.Second)) {
			out = append(out,
				tbm.HostileRec{GUID: 20, Entry: 14507, HPPct: 100, X: w19Venox[0], Y: w19Venox[1]},
				tbm.HostileRec{GUID: 21, Entry: 11373, HPPct: 100, X: w19Cobra[0], Y: w19Cobra[1]})
		}
		if !now.Before(wipeAt.Add(-150*time.Second)) && now.Before(wipeAt.Add(-88*time.Second)) {
			for i, p := range tigers {
				out = append(out, tbm.HostileRec{GUID: packet.GUID(30 + i), Entry: 11361, HPPct: 60 - 30*i, Target: packet.GUID(1001 + i), X: p[0], Y: p[1]})
			}
		}
		return out
	}
}

func wantRecovered(t *testing.T, f *fixture, code, resumes int, step string) {
	t.Helper()
	sum := f.summary()
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip (a trash tiger off Thekal's spot is no boss)", sum.Notes)
	}
	if hasNote(sum, "boss 11361") || hasNote(sum, "boss row 11361") {
		t.Fatalf("notes %q, want no boss handling of the trash tiger", sum.Notes)
	}
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if sum.WipeResumes != 1 || resumes != 1 {
		t.Fatalf("wipe_resumes %d, resumes %d, want 1 and 1", sum.WipeResumes, resumes)
	}
	if !hasNote(sum, "wipe-resume 1/2 at step "+step) {
		t.Fatalf("notes %q, want the step-%s recovery note", sum.Notes, step)
	}
}

func dist2(a, b [2]float32) float64 {
	dx, dy := float64(a[0]-b[0]), float64(a[1]-b[1])
	return math.Sqrt(dx*dx + dy*dy)
}

// TestRaidWalkWipeResumeWalk19TrashTigerOffLegRecovers replays ZG walk 19 test2 s4 (frames.jsonl 02:36-02:40,
// run.log step 89): Jeklik 14517 engaged and killed long before; at step 89 (leg high_priest_venoxis) the raid
// fights Zulian Tigers 11361 as trash ~330 yd from Thekal's position until 88 s before the wipe, Venoxis and his
// cobra stand in view at 100 % with no target, then all 19 hires die. No boss engaged, no dashboard, a recovery
// left = the wipe recovers. On 9eb13bf the tiger rows latched bossLatchAt and bossUnknown skipped (exit 19).
func TestRaidWalkWipeResumeWalk19TrashTigerOffLegRecovers(t *testing.T) {
	if d := dist2(w19TigerA, zgThekal); d < 300 {
		t.Fatalf("walk-19 tiger %.1f yd from Thekal, want ~330", d)
	}
	f, resumes := w19Scenario(t, true, w19VenoxisLine, w19StepRows([][2]float32{w19TigerA, w19TigerB}))
	code := f.run(context.Background())
	wantRecovered(t, f, code, *resumes, "89")
}

// TestRaidWalkWipeResumeWalk19NoJeklikTrackerRecovers is the same step-89 replay without Jeklik's fight, so the
// tracker's one-shot boss latch is still unspent when the trash tigers fight: an off-spot tiger must neither use
// it up nor send the walk into handleBoss (9d6535f: the tracker latched on 11361 and handleBoss ran).
func TestRaidWalkWipeResumeWalk19NoJeklikTrackerRecovers(t *testing.T) {
	f, resumes := w19Scenario(t, false, w19VenoxisLine, w19StepRows([][2]float32{w19TigerA, w19TigerB}))
	code := f.run(context.Background())
	wantRecovered(t, f, code, *resumes, "89")
}

// TestRaidWalkWipeResumeTigerTrashOnThekalLegRecovers: tiger trash on Thekal's own approach leg, at the plan's
// nearest 11361 pull group (56.5 yd from Thekal's position, outside FirstAddBossRadius), is trash too.
func TestRaidWalkWipeResumeTigerTrashOnThekalLegRecovers(t *testing.T) {
	if d := dist2(zgTigerPull, zgThekal); d <= 40 || d > 60 {
		t.Fatalf("pull group %.1f yd from Thekal, want 56.5", d)
	}
	f, resumes := w19Scenario(t, false, "TBM:RUN|zul_gurub|running|150/1104|high_priest_thekal|pull|begin|fighting|until step 184 (step 184)",
		w19StepRows([][2]float32{zgTigerPull}))
	code := f.run(context.Background())
	wantRecovered(t, f, code, *resumes, "150")
}

// TestRaidWalkWipeResumeThekalPhase2TigersSkip: Thekal phase 2 - the status already moved on to arlokk (TB marked
// Thekal's step done when 14599 died in phase 1), while tigers fight within 40 yd of Thekal's position. That is
// boss evidence on any leg: the wipe 88 s later ends the old way, the note naming the latched first add.
func TestRaidWalkWipeResumeThekalPhase2TigersSkip(t *testing.T) {
	near := [2]float32{zgThekal[0] + 10, zgThekal[1] - 12}
	f, resumes := w19Scenario(t, true, "TBM:RUN|zul_gurub|running|160/1104|high_priestess_arlokk|move|walk|walking|until step 184 (step 184)",
		w19StepRows([][2]float32{near}))
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 0 || *resumes != 0 {
		t.Fatalf("wipe_resumes %d, resumes %d, want 0", sum.WipeResumes, *resumes)
	}
	want := "wipe resume skipped: boss state unknown (boss row 11361 (first add of high_priest_thekal) latched "
	found := false
	for _, n := range sum.Notes {
		if strings.HasPrefix(n, want) && strings.HasSuffix(n, "s ago, under 120 s)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes %q, want a note starting %q", sum.Notes, want)
	}
}
