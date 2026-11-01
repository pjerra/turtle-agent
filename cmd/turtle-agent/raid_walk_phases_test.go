package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The walk-22 fix tests: the revive must wait for its own landing before it summons (F4, Q3's stuck lead), the
// frames must carry each bot's target/spell/action/result every 2 s in combat (F3, Q1's hold evidence).

// dist3 is the 3D distance between two positions (the landing checks).
func dist3(a, b [3]float32) float64 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// TestRaidWalkReviveSummonsOnlyAfterTheLanding (walk 22 test2 s4, Q3): a `.go xyz` answers at once and the server
// lands the character a moment later, so a recovery that waits only for the map id summons while its own position
// is still the OLD one. The hires then arrive at that old spot — the lead landed 560 yd back at the ZG entrance,
// walked into the river and froze there until the walk stopped at step 100. The revive must wait until its own
// position is within 10 yd of the point before the first `.summon` leaves (the map is already right: the live
// agent stood on map 309 the whole time).
func TestRaidWalkReviveSummonsOnlyAfterTheLanding(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	var resumeAt time.Time
	resumeHook(f, 24, 28, &resumeAt)
	// The go's map id takes effect at once, its position two status polls later: a wait on the map alone lets the
	// summons out while the commander still stands at the old spot.
	f.agent.mu.Lock()
	f.agent.goLag = 2
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	dists := append([]float64{}, f.agent.summonDist...)
	f.agent.mu.Unlock()
	if len(dists) == 0 {
		t.Fatal("no summons went out: the recovery never reached its summon stage")
	}
	for i, d := range dists {
		if d > reviveLandingYd {
			t.Fatalf("summon %d left %.1f yd from the revive point (want <= %d, the spec's landing radius): the recovery summoned before the commander landed", i, d, reviveLandingYd)
		}
	}
}

// killAllButFromHires makes every hire dead once the roster is hired, but the first keep of the party (walk 22
// test2 s4: TB's own recovery had already revived and teleported the tanks while the agent's frames of the rest
// arrived dead).
func (f *fixture) killAllButFromHires(keep int) {
	prev := f.agent.onGM
	f.agent.onGM = func(line string) {
		if prev != nil {
			prev(line)
		}
		if !strings.HasPrefix(line, ".bot hire raid ") {
			return
		}
		f.agent.mu.Lock()
		if len(f.agent.party) >= 39 {
			for i, n := range f.agent.party {
				if i >= keep {
					f.agent.dead[n] = true
				}
			}
		}
		f.agent.mu.Unlock()
	}
}

// TestRaidWalkSelfRegroupNeverSummonsTheLiving (walk 22 test2 s4, Q3): the run regroups by itself and TB's own
// recovery is already moving the party — the lead and the off-tanks stand alive at the regroup point, while the
// dashboard the agent reads still reports the wipe. The agent's revive then sent `.summon <lead>` 2 ms after its
// own `.go xyz` and dropped the lead 560 yd back at the entrance, its Voidwalker in tow. During a self-regroup the
// agent must never summon the lead, nor a hire the fresh complete view does not report dead; the dead keep the old
// recipe.
func TestRaidWalkSelfRegroupNeverSummonsTheLiving(t *testing.T) {
	t.Run("TB has already revived the lead", func(t *testing.T) {
		const keep = 5 // the lead and the four tanks TB revived first
		f, next, total := bossDoneFixture(t)
		f.opts.wipeResume = 2
		f.agent.startReply = zgStart(total, next)
		step := next - 2
		regroup := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total)
		f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
		f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
		f.dash.fights = mcWipeFights()
		// The wipe the agent acts on comes from the dashboard, which still reports every hire down; the frame rule
		// cannot report it (the lead is alive, and that rule needs every hire dead). The next dashboard read is a
		// plain no-commander answer, so the recovery runs once.
		reads := 0
		f.dash.commandersFn = func() json.RawMessage {
			reads++
			if reads > 1 {
				return json.RawMessage(`{"commanders":[]}`)
			}
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			var b strings.Builder
			b.WriteString(`{"commanders":[{"commander":"Commander","bots":[`)
			for i, n := range f.agent.party {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"name":%q,"alive":false}`, n)
			}
			b.WriteString(`]}]}`)
			return json.RawMessage(b.String())
		}
		f.killAllButFromHires(keep)
		f.agent.mu.Lock()
		f.agent.startQueue = append(f.agent.startQueue, queuedEvent{raw: regroup})
		f.agent.mu.Unlock()
		var reviveAt time.Time
		prev := f.agent.onGM
		f.agent.onGM = func(line string) {
			prev(line)
			if strings.HasPrefix(line, ".revive ") && reviveAt.IsZero() {
				reviveAt = f.clock.now()
			}
		}
		// The run marches on after the regroup and ends at its until once TB's own regroup is done.
		doneQueued := false
		f.agent.viewHook = func() {
			if reviveAt.IsZero() || doneQueued || f.clock.now().Sub(reviveAt) < 15*time.Minute {
				return
			}
			doneQueued = true
			f.agent.mu.Lock()
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
			f.agent.mu.Unlock()
		}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		f.agent.mu.Lock()
		party := append([]string{}, f.agent.party...)
		f.agent.mu.Unlock()
		if len(party) < keep+1 {
			t.Fatalf("%d hires, want more than keep=%d", len(party), keep)
		}
		gm := f.agent.gmLines()
		for i, name := range party {
			want := 0
			if i >= keep {
				want = 1 // the dead hires keep the old recipe
			}
			if got := countPrefix(gm, ".summon "+name); got != want {
				t.Fatalf(".summon %s x%d, want %d (the lead and the living hires are never summoned during a self-regroup)", name, got, want)
			}
		}
		if got := countPrefix(gm, ".summon "); got != len(party)-keep {
			t.Fatalf("%d summons, want %d (one per dead hire)", got, len(party)-keep)
		}
		if got := countPrefix(gm, ".revive "); got != len(party) {
			t.Fatalf("%d revives, want %d (the recovery still revives every hire)", got, len(party))
		}
		if sum := f.summary(); !hasNote(sum, "the run regroups by itself") {
			t.Fatalf("summary notes %q, want the self-regroup recovery note", sum.Notes)
		}
	})

	// The lead is never summoned during a self-regroup even when it is dead: TB teleports it to the step itself,
	// and a summon would drag it back to wherever the agent stands.
	t.Run("the whole raid is still dead", func(t *testing.T) {
		f, next, total := bossDoneFixture(t)
		f.opts.wipeResume = 2
		f.agent.startReply = zgStart(total, next)
		step := next - 2
		regroup := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total)
		f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
		f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
		f.dash.fights = mcWipeFights()
		selfRegroupKill(f, regroup)
		var reviveAt time.Time
		f.agent.onGM = func(line string) {
			if strings.HasPrefix(line, ".revive ") && reviveAt.IsZero() {
				reviveAt = f.clock.now()
			}
		}
		doneQueued := false
		f.agent.viewHook = func() {
			if reviveAt.IsZero() || doneQueued || f.clock.now().Sub(reviveAt) < 15*time.Minute {
				return
			}
			doneQueued = true
			f.agent.mu.Lock()
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
			f.agent.mu.Unlock()
		}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		f.agent.mu.Lock()
		party := append([]string{}, f.agent.party...)
		f.agent.mu.Unlock()
		gm := f.agent.gmLines()
		if got := countPrefix(gm, ".summon "+party[0]); got != 0 {
			t.Fatalf(".summon %s x%d, want none (TB moves the lead itself during a self-regroup): %q", party[0], got, gm)
		}
		if got := countPrefix(gm, ".summon "); got != len(party)-1 {
			t.Fatalf("%d summons, want %d (every hire but the lead)", got, len(party)-1)
		}
	})
}

// TestRaidWalkReviveSummonsGuard pins the guard the revive uses during a self-regroup (walk 22 F4 round 2): the
// lead is never summoned (TB moves it itself, wherever it stands), and no hire is summoned unless the ONE fresh
// complete view taken just before the summons reports it dead. A hire whose freshest frame is merely old, or
// missing, is NOT summoned any more: the old > 10 s fallback was a licence to drag a living hire (or the lead)
// back to wherever the agent stood (walk 22 test2 s4, Q3). No complete view at all (nil set) summons nobody.
func TestRaidWalkReviveSummonsGuard(t *testing.T) {
	w := &walk{mt: "Lead"}
	for _, c := range []struct {
		name        string
		selfRegroup bool
		dead        map[string]bool
		want        bool
	}{
		{"Lead", true, map[string]bool{"Lead": true, "Dead": true}, false}, // the lead: TB moves it, so the agent never summons it
		{"Lead", false, nil, true},                              // outside a self-regroup the old recipe stands
		{"Dead", true, map[string]bool{"Dead": true}, true},     // the fresh view reports it dead
		{"Alive", true, map[string]bool{"Dead": true}, false},   // the fresh view shows it alive: left where it stands
		{"Stale", true, map[string]bool{"Dead": true}, false},   // an old frame is no licence to summon (the fallback is gone)
		{"Unknown", true, map[string]bool{"Dead": true}, false}, // no row at all: no licence either
		{"Dead", true, nil, false},                              // no complete fresh view: nobody is summoned
		{"Dead", false, nil, true},                              // the old recipe needs no view
	} {
		if got := w.reviveSummons(c.name, c.selfRegroup, c.dead); got != c.want {
			t.Fatalf("reviveSummons(%q, selfRegroup=%v, dead=%v) = %v, want %v", c.name, c.selfRegroup, c.dead, got, c.want)
		}
	}
}

// TestRaidWalkFramesCarryBotTargets (walk 22 F3, Q1): the module already sends each bot's target, spell, action and
// result in the P record (internal/tbm/view.go), but savedBot dropped them, so the frames could not show which bot
// hit which Servant with which spell — the evidence the hold needs. They must round-trip into frames.jsonl.
func TestRaidWalkFramesCarryBotTargets(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	f.agent.mu.Lock()
	f.agent.partyExtra = map[string]tbm.PartyRec{
		"Bot02": {Target: packet.GUID(0xDEAD), Spell: 116, Action: "cast", Result: "F"},
	}
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	var rec struct {
		Party []struct {
			Name   string `json:"name"`
			Target string `json:"target"`
			Spell  uint32 `json:"spell"`
			Action string `json:"action"`
			Result string `json:"result"`
		} `json:"party"`
	}
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("frames.jsonl line 1: %v", err)
	}
	want := packet.GUID(0xDEAD).String()
	found := false
	for _, p := range rec.Party {
		if p.Name != "Bot02" {
			continue
		}
		found = true
		if p.Target != want || p.Spell != 116 || p.Action != "cast" || p.Result != "F" {
			t.Fatalf("Bot02 in the frame: target %q spell %d action %q result %q, want %q 116 cast F", p.Target, p.Spell, p.Action, p.Result, want)
		}
	}
	if !found {
		t.Fatalf("no Bot02 in the frame: %+v", rec.Party)
	}
}

// TestRaidWalkFramesEveryTwoSecondsInCombat (walk 22 F3, Q1): the 10 s cadence is too coarse for the pile — the
// Servants died 11 s apart, so consecutive frames could hold nothing but a corpse. While a party member is in
// combat the frames must go out every 2 s, the watch loop's own cadence.
func TestRaidWalkFramesEveryTwoSecondsInCombat(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	f.agent.combatAllFn = func(n int) bool { return true }
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var ts []int64
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var rec struct {
			T int64 `json:"t"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("frames.jsonl line: %v", err)
		}
		ts = append(ts, rec.T)
	}
	if len(ts) < 15 {
		t.Fatalf("%d frames in a walk of ~55 s of combat, want >= 15 (the 2 s cadence): %v", len(ts), ts)
	}
	for i := 1; i < len(ts); i++ {
		if gap := ts[i] - ts[i-1]; gap > 4000 {
			t.Fatalf("frames %d and %d are %d ms apart: want <= 4000 in combat, got %v", i-1, i, gap, ts)
		}
	}
}

// selfRegroupWipe runs the self-regroup wipe the fresh-view tests share and returns the fixture and the hire
// order. live names the hires TB's own regroup has up by the time the agent's summons would leave (nil: nobody,
// every hire still down in the frames); it is called with the roster once the hires exist, and those hires count
// as alive in every frame the agent reads after its own `.go xyz`. noView makes the FIRST bot_view call after
// that `.go xyz` fail, so the recovery gets no complete fresh view at all. The quiet check sees a warder 60 yd
// out for its first three turns (>= 15 s of fake time between the wipe's poll and the summons: the hires' last
// frames are stale by then, the state the old > 10 s fallback turned into a licence to summon), and the run's
// done line lands 15 min after the revives so the walk ends at its until.
func selfRegroupWipe(t *testing.T, live func(party []string) []string, noView bool) (*fixture, []string) {
	t.Helper()
	f, next, total := bossDoneFixture(t)
	f.opts.wipeResume = 2
	f.agent.startReply = zgStart(total, next)
	step := next - 2
	regroup := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total)
	f.agent.unitsFn = func(n int) []objects.Unit {
		if n > 3 {
			return []objects.Unit{}
		}
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		f.agent.mu.Unlock()
		return []objects.Unit{{Type: "creature", Name: "Onyxian Warder", Hostile: true, Moving: true, X: x + 60, Y: y, Z: z}}
	}
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = mcWipeFights()
	selfRegroupKill(f, regroup)

	// goSeen is set when the recovery's own teleport goes out. From the wipe's poll on the agent's frames of
	// the hires are the all-dead ones, and nothing reads a frame again before the summons (waitRevivePoint
	// reads the status, quietCheck the units): the state the old stale-state fallback summoned on.
	var goSeen, viewFailed bool
	var reviveAt time.Time
	f.agent.onGM = func(line string) {
		switch {
		case strings.HasPrefix(line, ".revive ") && reviveAt.IsZero():
			reviveAt = f.clock.now()
		case strings.HasPrefix(line, ".go xyz"):
			// The recovery's own teleport is the go that goes out with the whole raid down: the enter
			// teleport (and the portal one) precede the wipe.
			f.agent.mu.Lock()
			down := len(f.agent.party) > 0
			for _, n := range f.agent.party {
				if !f.agent.dead[n] {
					down = false
					break
				}
			}
			f.agent.mu.Unlock()
			if down {
				goSeen = true
			}
		}
	}
	doneRaw := fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)
	doneQueued := false
	f.agent.viewHook = func() {
		if goSeen && live != nil {
			f.agent.mu.Lock()
			for _, n := range live(f.agent.party) {
				f.agent.dead[n] = false
			}
			f.agent.mu.Unlock()
		}
		if reviveAt.IsZero() || doneQueued || f.clock.now().Sub(reviveAt) < 15*time.Minute {
			return
		}
		doneQueued = true
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines, queuedEvent{raw: doneRaw})
		f.agent.mu.Unlock()
	}
	if noView {
		f.agent.viewFn = func(n int) (bool, string) {
			if goSeen && !viewFailed {
				viewFailed = true
				return true, ""
			}
			return false, ""
		}
	}
	f.agent.combatAllFn = func(n int) bool { return !reviveAt.IsZero() && n%2 == 0 }
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	party := append([]string{}, f.agent.party...)
	f.agent.mu.Unlock()
	return f, party
}

// TestRaidWalkSelfRegroupSummonsOnlyTheFreshView (walk 22 F4 round 2, Codex HIGH): during a self-regroup the
// revive may summon only what ONE fresh complete bot view taken just before the summons reports dead. The old
// rule summoned a hire whose freshest frame was merely older than 10 s (or missing), so the revive's own
// quiet-check delay could turn a dead frame stale while TB had the hire up and running: the `.summon` then
// dragged a living hire — or the lead itself — back to wherever the agent stood (Q3, walk 22 test2 s4). No
// complete fresh view summons nobody (the regroup owns the movement), and the revive still raises the dead
// where they lie.
func TestRaidWalkSelfRegroupSummonsOnlyTheFreshView(t *testing.T) {
	t.Run("a stale dead frame is no licence to summon", func(t *testing.T) {
		f, party := selfRegroupWipe(t, func(p []string) []string { return p }, false)
		gm := f.agent.gmLines()
		if n := countPrefix(gm, ".summon "); n != 0 {
			t.Fatalf("%d summons, want none: the fresh view shows every hire alive (TB's own regroup has them up), and a stale dead frame is no licence to summon: %q", n, gm)
		}
		if got := countPrefix(gm, ".revive "); got != len(party) {
			t.Fatalf("%d revives, want %d (the revive half is unchanged)", got, len(party))
		}
		sum := f.summary()
		if hasNote(sum, "self-regroup: no fresh view, no summons") {
			t.Fatalf("notes %q, want no fresh-view note: the complete view was there", sum.Notes)
		}
		if !hasNote(sum, "the run regroups by itself") {
			t.Fatalf("notes %q, want the self-regroup recovery note", sum.Notes)
		}
	})

	t.Run("no complete fresh view summons nobody", func(t *testing.T) {
		f, party := selfRegroupWipe(t, nil, true)
		gm := f.agent.gmLines()
		if n := countPrefix(gm, ".summon "); n != 0 {
			t.Fatalf("%d summons, want none: the recovery had no complete fresh view, so nobody may be summoned: %q", n, gm)
		}
		if got := countPrefix(gm, ".revive "); got != len(party) {
			t.Fatalf("%d revives, want %d (the revive half is unchanged)", got, len(party))
		}
		sum := f.summary()
		if !hasNote(sum, "self-regroup: no fresh view, no summons") {
			t.Fatalf("notes %q, want the no-fresh-view note", sum.Notes)
		}
	})

	t.Run("a bot dead in the fresh view is summoned once", func(t *testing.T) {
		f, party := selfRegroupWipe(t, func(p []string) []string { return p[:len(p)-1] }, false)
		last := party[len(party)-1]
		gm := f.agent.gmLines()
		if n := countPrefix(gm, ".summon "); n != 1 {
			t.Fatalf("%d summons, want exactly 1 (only the hire the fresh view reports dead): %q", n, gm)
		}
		if got := countPrefix(gm, ".summon "+last); got != 1 {
			t.Fatalf(".summon %s x%d, want 1 (the one the fresh view reports dead): %q", last, got, gm)
		}
		if got := countPrefix(gm, ".revive "); got != len(party) {
			t.Fatalf("%d revives, want %d (the revive half is unchanged)", got, len(party))
		}
	})
}
