package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
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
// agent summons NOBODY (lead ruling, round 2): every hire still dead is `.revive`d where it lies and TB's regroup
// gathers them; a hire TB has already brought up is not revived (CRASH-2026-11-03).
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
		if got := countPrefix(gm, ".summon "); got != 0 {
			t.Fatalf("%d summons, want none (a self-regroup summons nobody, dead hires included): %q", got, gm)
		}
		// Only the hires still dead after TB's release are revived; the keep living ones are never `.revive`d.
		if got := countPrefix(gm, ".revive "); got != len(party)-keep {
			t.Fatalf("%d revives, want %d (every dead hire, none of the %d TB brought up)", got, len(party)-keep, keep)
		}
		for _, l := range gm {
			for _, n := range party[:keep] {
				if l == ".revive "+n {
					t.Fatalf("%s was revived though TB had already brought it up: %q", n, gm)
				}
			}
		}
		if sum := f.summary(); !hasNote(sum, "the run regroups by itself") {
			t.Fatalf("summary notes %q, want the self-regroup recovery note", sum.Notes)
		}
	})

	// Nobody is summoned during a self-regroup even when the whole raid is dead: TB teleports the lead to the step
	// itself and gathers the party, and a summon would drag a hire back to wherever the agent stands. The dead are
	// revived where they lie.
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
		if got := countPrefix(gm, ".summon "); got != 0 {
			t.Fatalf("%d summons, want none (a dead hire in a self-regroup is revived in place, never summoned): %q", got, gm)
		}
		if got := countPrefix(gm, ".revive "); got != len(party) {
			t.Fatalf("%d revives, want %d (every dead hire is revived where it lies)", got, len(party))
		}
		if sum := f.summary(); !hasNote(sum, "self-regroup: no summons, revived in place") {
			t.Fatalf("notes %q, want the no-summons note", sum.Notes)
		}
	})
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

// TestWalkFramesCarryPower (TortoiseBots#1459): the module already sends each bot's power in the P record
// (internal/tbm/view.go:52-54), but savedBot dropped it, so the frames could not show a warrior's rage or a
// healer's mana. Both bot lines must carry power, max_power and power_type; power_type 0 is mana and must be
// written, not omitted.
func TestWalkFramesCarryPower(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	f.agent.mu.Lock()
	f.agent.partyExtra = map[string]tbm.PartyRec{
		"Bot02": {Power: 37, MaxPower: 1000, PowerType: 1}, // the warrior: 37 rage of 1000
		"Bot01": {Power: 0, MaxPower: 2400, PowerType: 0},  // the priest: 0 mana of 2400
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
			Name      string  `json:"name"`
			Power     *uint32 `json:"power"`
			MaxPower  *uint32 `json:"max_power"`
			PowerType *uint8  `json:"power_type"`
		} `json:"party"`
	}
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("frames.jsonl line 1: %v", err)
	}
	want := map[string]struct {
		power, maxPower uint32
		powerType       uint8
	}{
		"Bot02": {37, 1000, 1},
		"Bot01": {0, 2400, 0},
	}
	found := 0
	for _, p := range rec.Party {
		w, ok := want[p.Name]
		if !ok {
			continue
		}
		found++
		if p.Power == nil || p.MaxPower == nil || p.PowerType == nil {
			t.Fatalf("%s in the frame: power %v max_power %v power_type %v, want all three keys", p.Name, p.Power, p.MaxPower, p.PowerType)
		}
		if *p.Power != w.power || *p.MaxPower != w.maxPower || *p.PowerType != w.powerType {
			t.Fatalf("%s in the frame: power %d max_power %d power_type %d, want %d %d %d",
				p.Name, *p.Power, *p.MaxPower, *p.PowerType, w.power, w.maxPower, w.powerType)
		}
	}
	if found != len(want) {
		t.Fatalf("%d of the two bots in the frame: %+v", found, rec.Party)
	}
	line := string(lines[0])
	if !strings.Contains(line, `"name":"Bot01"`) || !strings.Contains(line, `"power_type":0`) {
		t.Fatalf("the priest's line carries no power_type 0 (mana must be written, not omitted): %s", line)
	}
	if !strings.Contains(line, `"power_type":1`) {
		t.Fatalf("the warrior's line carries no power_type 1: %s", line)
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
// as alive in every frame the agent reads after its own `.go xyz`. The quiet check sees a warder 60 yd
// out for its first three turns (>= 15 s of fake time between the wipe's poll and the summons: the hires' last
// frames are stale by then, the state the old > 10 s fallback turned into a licence to summon), and the run's
// done line lands 15 min after the revives so the walk ends at its until.
func selfRegroupWipe(t *testing.T, live func(party []string) []string) (*fixture, []string) {
	t.Helper()
	return selfRegroupWipeWith(t, live, nil)
}

// selfRegroupWipeWith is selfRegroupWipe with setup run on the fixture just before the walk starts.
func selfRegroupWipeWith(t *testing.T, live func(party []string) []string, setup func(f *fixture)) (*fixture, []string) {
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
	var goSeen bool
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
			up := live(f.agent.party)
			for _, n := range up {
				f.agent.dead[n] = false
			}
			// Every hire up means no `.revive` will ever come: the recovery's end is the anchor the done line counts from.
			if reviveAt.IsZero() && len(up) == len(f.agent.party) {
				reviveAt = f.clock.now()
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
	f.agent.combatAllFn = func(n int) bool { return !reviveAt.IsZero() && n%2 == 0 }
	if setup != nil {
		setup(f)
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	party := append([]string{}, f.agent.party...)
	f.agent.mu.Unlock()
	return f, party
}

// TestRaidWalkSelfRegroupSummonsNobody (walk 22 F4, lead ruling of round 2): during a self-regroup the revive sends
// NO summons at all — not the lead, not a living hire, not a dead one — whatever the frames say. The old rules
// summoned a hire whose freshest frame was merely older than 10 s, and a one-fresh-view rule still let a `.summon`
// drag a hire to where the agent stands while TB's regroup moves the party (Q3, walk 22 test2 s4). Every hire is
// `.revive`d where it lies and the note says so. Only the hires still dead on the fresh read are revived.
func TestRaidWalkSelfRegroupSummonsNobody(t *testing.T) {
	for _, c := range []struct {
		name string
		live func(p []string) []string
	}{
		{"TB has every hire up (a stale dead frame is no licence)", func(p []string) []string { return p }},
		{"every hire is still down", nil},
		{"one hire is dead in a fresh view", func(p []string) []string { return p[:len(p)-1] }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, party := selfRegroupWipe(t, c.live)
			gm := f.agent.gmLines()
			if n := countPrefix(gm, ".summon "); n != 0 {
				t.Fatalf("%d summons, want none in a self-regroup: %q", n, gm)
			}
			// Every hire still dead is revived where it lies; one TB has already brought up is left alone.
			up := 0
			if c.live != nil {
				up = len(c.live(party))
			}
			if got := countPrefix(gm, ".revive "); got != len(party)-up {
				t.Fatalf("%d revives, want %d (every hire still dead is revived where it lies)", got, len(party)-up)
			}
			sum := f.summary()
			if !hasNote(sum, "self-regroup: no summons, revived in place") {
				t.Fatalf("notes %q, want the no-summons note", sum.Notes)
			}
			if !hasNote(sum, "the run regroups by itself") {
				t.Fatalf("notes %q, want the self-regroup recovery note", sum.Notes)
			}
		})
	}
}

// TestRaidWalkReviveLandedCheck pins the landing predicate (Codex HIGH, F4 round 2, walk 24 C1): the session
// reports the `.go` destination at once, flagged ZUnknown until the server sends a position, so only a settled
// status counts. x/y must be within 10 yd and z within 30 yd (dropToFloor may move z), on the plan's map. A
// teleport the session has ACKED (TeleportSeq > beforeSeq) is a landing over a navmesh hole, where the z guess
// never settles; still falling blocks it.
func TestRaidWalkReviveLandedCheck(t *testing.T) {
	p := dungeon.Point{X: 100, Y: 200, Z: 50}
	at := func(x, y, z float32) session.StatusInfo {
		return session.StatusInfo{State: session.StatusInWorld, Map: 309, X: x, Y: y, Z: z}
	}
	zUnknown := at(100, 200, 50)
	zUnknown.ZUnknown = true
	falling := at(100, 200, 50)
	falling.Falling = true
	otherMap := at(100, 200, 50)
	otherMap.Map = 1
	for _, c := range []struct {
		name string
		st   session.StatusInfo
		want bool
	}{
		{"settled on the point", at(100, 200, 50), true},
		{"z dropped 25 yd to the floor", at(100, 200, 25), true},
		{"z 40 yd off", at(100, 200, 90), false},
		{"x/y 9 yd off", at(109, 200, 50), true},
		{"x/y 11 yd off", at(111, 200, 50), false},
		{"destination reported with z unknown", zUnknown, false},
		{"still falling", falling, false},
		{"wrong map", otherMap, false},
	} {
		if got := reviveLanded(p, 309, 309, c.st, 0, false); got != c.want {
			t.Fatalf("near go: %s: reviveLanded = %v, want %v", c.name, got, c.want)
		}
	}
	// A far go (from map 0, walk 23): ZUnknown stays set for good after SMSG_NEW_WORLD, so it no longer blocks the
	// landing; the session's worldport ack (TeleportSeq > beforeSeq) is the landing itself, since the session
	// publishes the map no earlier than that write. Every other rule still holds.
	farSeq := func(x, y, z float32) session.StatusInfo {
		st := at(x, y, z)
		st.TeleportSeq = 5
		return st
	}
	farZUnknown, farFalling := farSeq(100, 200, 50), farSeq(100, 200, 50)
	farZUnknown.ZUnknown, farFalling.Falling = true, true
	otherFarMap := farSeq(100, 200, 50)
	otherFarMap.Map = 1
	for _, c := range []struct {
		name string
		st   session.StatusInfo
		want bool
	}{
		{"destination reported with z unknown", farZUnknown, true},
		{"settled on the point", farSeq(100, 200, 50), true},
		{"still falling", farFalling, false},
		{"wrong map", otherFarMap, false},
		{"x/y 11 yd off", farSeq(111, 200, 50), false},
		{"z 40 yd off", farSeq(100, 200, 90), false},
	} {
		if got := reviveLanded(p, 309, 0, c.st, 4, true); got != c.want {
			t.Fatalf("far go: %s: reviveLanded = %v, want %v", c.name, got, c.want)
		}
	}
	// A far go publishes the new map and the sequence bump in one update after the worldport ack (Codex HIGH): a
	// map change with an equal sequence is not a landing, and an unread sequence lands nothing either.
	for _, c := range []struct {
		name      string
		beforeSeq uint64
		haveSeq   bool
		want      bool
	}{
		{"map changed, seq equal", 5, true, false},
		{"map changed, seq advanced", 4, true, true},
		{"map changed, beforeSeq unread", 0, false, false},
	} {
		if got := reviveLanded(p, 309, 0, farSeq(100, 200, 50), c.beforeSeq, c.haveSeq); got != c.want {
			t.Fatalf("far go over a map change: %s: reviveLanded = %v, want %v", c.name, got, c.want)
		}
	}
	// A NEAR go over a navmesh hole (walk 24 C1): the seq advanced past beforeSeq is the landing; an equal seq is
	// not; still falling is not; an unread beforeSeq (haveSeq false) leaves the old rule.
	zUnknownSeq := at(100, 200, 50)
	zUnknownSeq.ZUnknown, zUnknownSeq.TeleportSeq = true, 5
	zUnknownFalling := at(100, 200, 50)
	zUnknownFalling.ZUnknown, zUnknownFalling.TeleportSeq, zUnknownFalling.Falling = true, 5, true
	for _, c := range []struct {
		name      string
		st        session.StatusInfo
		beforeSeq uint64
		haveSeq   bool
		want      bool
	}{
		{"z unknown, seq advanced", zUnknownSeq, 4, true, true},
		{"z unknown, seq equal", zUnknownSeq, 5, false, false},
		{"z unknown, seq advanced but falling", zUnknownFalling, 4, true, false},
		{"z unknown, beforeSeq unread", zUnknownSeq, 0, false, false},
	} {
		if got := reviveLanded(p, 309, 309, c.st, c.beforeSeq, c.haveSeq); got != c.want {
			t.Fatalf("near go over a hole: %s: reviveLanded = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRaidWalkReviveWaitsForTheSettledLanding (Codex HIGH, F4 round 2): the status reports the `.go` destination
// at once with ZUnknown (or Falling) set and settles a few polls later. No summon may leave before it settles.
func TestRaidWalkReviveWaitsForTheSettledLanding(t *testing.T) {
	for _, falling := range []bool{false, true} {
		name := "z unknown"
		if falling {
			name = "falling"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.agent.startReply = startAt("onyxias_lair", 23)
			f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
			f.allDeadFromHires()
			var resumeAt time.Time
			resumeHook(f, 24, 28, &resumeAt)
			f.agent.mu.Lock()
			f.agent.goUnsettled, f.agent.goUnsettledFalling = 3, falling
			f.agent.enterMap = 249 // the live Q3 agent stood in the instance: its go was a near one
			f.agent.mu.Unlock()
			if code := f.run(context.Background()); code != exitOK {
				t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			f.agent.mu.Lock()
			unsettled, summons := f.agent.summonUnsettled, len(f.agent.summonDist)
			f.agent.mu.Unlock()
			if summons == 0 {
				t.Fatal("no summons went out: the recovery never reached its summon stage")
			}
			if unsettled != 0 {
				t.Fatalf("%d summons left right after a status that was not settled (%s): the destination alone is no landing", unsettled, name)
			}
		})
	}
}

// TestRaidWalkEntranceFallbackWaitsForTheLanding (Codex MEDIUM-2): the first revive point never gets quiet, so the
// revive falls back to the entrance. That second `.go xyz` lags like the first, and the summons must wait for ITS
// landing (the old waitMap saw only the map id, already right, and summoned from the old spot).
func TestRaidWalkEntranceFallbackWaitsForTheLanding(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	var recoveryGos int
	f.allDeadFromHires()
	hire := f.agent.onGM
	f.agent.onGM = func(line string) {
		hire(line)
		if strings.HasPrefix(line, ".go xyz") {
			f.agent.mu.Lock()
			if f.agent.runStarted {
				recoveryGos++
			}
			f.agent.mu.Unlock()
		}
	}
	// A warder 60 yd out keeps the FIRST point loud for the whole quiet check; once the entrance go went out the
	// units are quiet again.
	f.agent.unitsFn = func(n int) []objects.Unit {
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		gos := recoveryGos
		f.agent.mu.Unlock()
		if gos >= 2 {
			return []objects.Unit{}
		}
		return []objects.Unit{{Type: "creature", Name: "Onyxian Warder", Hostile: true, Moving: true, X: x + 60, Y: y, Z: z}}
	}
	var resumeAt time.Time
	resumeHook(f, 24, 28, &resumeAt)
	f.agent.mu.Lock()
	f.agent.goLagEvery = 2
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	dists := append([]float64{}, f.agent.summonDist...)
	gos := recoveryGos
	f.agent.mu.Unlock()
	if gos != 2 {
		t.Fatalf("%d recovery teleports, want 2 (the revive point, then the entrance)", gos)
	}
	if len(dists) == 0 {
		t.Fatal("no summons went out: the recovery never reached its summon stage")
	}
	for i, d := range dists {
		if d > reviveLandingYd {
			t.Fatalf("summon %d left %.1f yd from the entrance (want <= %d): the entrance fallback summoned before its landing", i, d, reviveLandingYd)
		}
	}
}

// TestRaidWalkReviveAppearsAtAHireFirst (walk 23, second finding): the revive's `.go xyz` from the portal put the
// commander into a NEW instance copy (m910q: hires in 101, commander in 104; test2: 106 vs 107), so the quiet check
// looked at an empty copy and a summon would drag the hires into a fresh, respawned one. The revive must first
// `.appear <hire>` (the core binds a GM to the target's copy) so its `.go xyz` stays on the map and in the hires' copy.
func TestRaidWalkReviveAppearsAtAHireFirst(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	var resumeAt time.Time
	resumeHook(f, 24, 28, &resumeAt)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	lines := f.agent.gmLines()
	gos, appearAt, recoveryGoAt := 0, -1, -1
	for i, l := range lines {
		if strings.HasPrefix(l, ".go xyz ") {
			gos++
			if gos == 2 && recoveryGoAt < 0 {
				recoveryGoAt = i
			}
		}
		if strings.HasPrefix(l, ".appear ") && appearAt < 0 {
			appearAt = i
		}
	}
	if recoveryGoAt < 0 {
		t.Fatalf("no recovery go: %q", lines)
	}
	visAt := -1
	for i, l := range lines {
		if l == ".gm visible off" {
			visAt = i
			break
		}
	}
	if visAt < 0 || visAt > appearAt || countPrefix(lines, ".gm visible off") != 1 {
		t.Fatalf("want exactly one .gm visible off before the appear (at %d), got it at %d: %q", appearAt, visAt, lines)
	}
	if appearAt < 0 || appearAt > recoveryGoAt {
		t.Fatalf("the recovery go (line %d) was not preceded by a .appear (first at %d): %q", recoveryGoAt, appearAt, lines)
	}
	f.agent.mu.Lock()
	inst, dists := f.agent.instAtRevive, append([]float64{}, f.agent.summonDist...)
	f.agent.mu.Unlock()
	if inst != "hires" {
		t.Fatalf("the commander stood in the %q copy at the revive, want the hires' copy", inst)
	}
	if len(dists) == 0 {
		t.Fatal("no summons went out")
	}
	for i, d := range dists {
		if d > reviveLandingYd {
			t.Fatalf("summon %d left %.1f yd from the revive point (want <= %d)", i, d, reviveLandingYd)
		}
	}
}

// TestRaidWalkReviveLandsAfterAFarTeleport (walk 23, 6 runs exit 19): the commander waits at the portal on the
// continent, so a revive `.go xyz <point> 309` it sends without having joined the hires is a FAR teleport. The session
// lands it through SMSG_NEW_WORLD and reports the point on the plan's map, but leaves ZUnknown set for good (only a near
// teleport drops to the navmesh floor). The map change itself is the landing. Pinned in a self-regroup whose `.appear`
// is refused: the one path that still sends the far go.
func TestRaidWalkReviveLandsAfterAFarTeleport(t *testing.T) {
	f, party := selfRegroupWipeWith(t, nil, func(f *fixture) {
		f.agent.mu.Lock()
		f.agent.appearRefused = true
		f.agent.mu.Unlock()
	})
	gm := f.agent.gmLines()
	f.agent.mu.Lock()
	far := f.agent.recoveryGo && f.agent.commanderInst == "fresh"
	f.agent.mu.Unlock()
	if !far {
		t.Fatalf("no far recovery go into a fresh copy: %q", gm)
	}
	if got := countPrefix(gm, ".revive "); got != len(party) {
		t.Fatalf("%d revives, want %d: the far go never counted as landed", got, len(party))
	}
}

// TestRaidWalkReviveWaitsForTheFreshAppearLanding (review round 1, Codex HIGH): the commander may already stand on the
// plan's map (in a copy of its own), so the map id proves nothing about an `.appear`. The `.go xyz` may leave only
// after a FRESH landing next to the hire; here the appear lands 3 status reads late.
func TestRaidWalkReviveWaitsForTheFreshAppearLanding(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	var resumeAt time.Time
	resumeHook(f, 24, 28, &resumeAt)
	f.agent.mu.Lock()
	f.agent.enterMap, f.agent.appearLag = 249, 3
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	early, inst, summons := f.agent.goBeforeAppear, f.agent.instAtRevive, len(f.agent.summonDist)
	f.agent.mu.Unlock()
	if early != 0 {
		t.Fatalf("%d recovery gos left before the appear landed: the map id alone is no proof", early)
	}
	if inst != "hires" || summons == 0 {
		t.Fatalf("copy at the revive %q, %d summons; want the hires' copy and summons", inst, summons)
	}
}

// TestRaidWalkReviveUnverifiedAppearStops (review round 1): a normal revive (summons follow) whose `.appear` never
// lands would quiet-check an empty copy, so it stops with exit 19 and sends no go, summon or revive.
func TestRaidWalkReviveUnverifiedAppearStops(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	f.agent.mu.Lock()
	f.agent.appearRefused = true
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if end := f.summary().EndReason; !strings.Contains(end, "not revived: could not join the hires' instance (") {
		t.Fatalf("end %q, want the could-not-join reason", end)
	}
	gm := f.agent.gmLines()
	f.agent.mu.Lock()
	recGo := f.agent.recoveryGo
	f.agent.mu.Unlock()
	if recGo || countPrefix(gm, ".summon ") != 0 || countPrefix(gm, ".revive ") != 0 {
		t.Fatalf("recovery go %v, %d summons, %d revives; want none: %q", recGo, countPrefix(gm, ".summon "), countPrefix(gm, ".revive "), gm)
	}
}

// TestRaidWalkSelfRegroupUnverifiedAppearCarriesOn (review round 1): in a self-regroup nobody is summoned and `.revive`
// works by name, so a failed `.appear` (here the gm line itself fails) keeps today's far go and notes it.
func TestRaidWalkSelfRegroupUnverifiedAppearCarriesOn(t *testing.T) {
	f, party := selfRegroupWipeWith(t, nil, func(f *fixture) {
		f.agent.mu.Lock()
		f.agent.gmFail = func(line string) error {
			if strings.HasPrefix(line, ".appear ") {
				return errors.New("no such player")
			}
			return nil
		}
		f.agent.mu.Unlock()
	})
	gm := f.agent.gmLines()
	if got := countPrefix(gm, ".revive "); got != len(party) {
		t.Fatalf("%d revives, want %d", got, len(party))
	}
	if !hasNote(f.summary(), "could not join the hires' instance") {
		t.Fatalf("notes %q, want the could-not-join note", f.summary().Notes)
	}
}

// armStatusFails fails the next n status reads once the revive's `.gm visible off` goes out (the first line of a
// revive, before any status read of it).
func armStatusFails(f *fixture, n int) {
	prev := f.agent.onGM
	f.agent.onGM = func(line string) {
		if prev != nil {
			prev(line)
		}
		if line == ".gm visible off" {
			f.agent.mu.Lock()
			f.agent.statusFailNext = n
			f.agent.mu.Unlock()
		}
	}
}

// ghostRevive is a normal revive whose hires all died and show on map 0 (released to the graveyard): no hire stands on
// the plan's map to appear at.
func ghostRevive(t *testing.T, statusFails int) (*fixture, int) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	var resumeAt time.Time
	resumeHook(f, 24, 28, &resumeAt)
	f.agent.mu.Lock()
	f.agent.ghostsOff = true
	f.agent.mu.Unlock()
	if statusFails > 0 {
		armStatusFails(f, statusFails)
	}
	return f, f.run(context.Background())
}

// TestRaidWalkReviveGhostsOffTheMapKeepTheFarGo (review round 2, Opus M-new): hires released to the graveyard show on
// another map, so there is nobody to appear at. The normal revive keeps the far go and its summons (each resolves to
// the hire's own bound copy) and notes that its quiet check was blind.
func TestRaidWalkReviveGhostsOffTheMapKeepTheFarGo(t *testing.T) {
	f, code := ghostRevive(t, 0)
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".appear "); n != 0 {
		t.Fatalf("%d appears, want none (no hire on the plan's map): %q", n, gm)
	}
	if countPrefix(gm, ".summon ") == 0 {
		t.Fatalf("no summons: %q", gm)
	}
	if !hasNote(f.summary(), "quiet check was blind") {
		t.Fatalf("notes %q, want the blind quiet check note", f.summary().Notes)
	}
}

// TestRaidWalkReviveStatusRetriedBeforeTheGo (review round 2, Codex MEDIUM): a failed status read must not be taken
// for the plan's map (that picks the near rule, and a far go then never lands). Two failed reads are retried and the
// revive goes on; three end a normal revive with exit 19.
func TestRaidWalkReviveStatusRetriedBeforeTheGo(t *testing.T) {
	f, code := ghostRevive(t, 2)
	if code != exitOK {
		t.Fatalf("2 failed reads: exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if countPrefix(f.agent.gmLines(), ".summon ") == 0 {
		t.Fatal("2 failed reads: no summons")
	}
	f, code = ghostRevive(t, 3)
	if code != exitRaidWalkIncident {
		t.Fatalf("3 failed reads: exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if end := f.summary().EndReason; !strings.Contains(end, "not revived: commander status unreadable") {
		t.Fatalf("3 failed reads: end %q", end)
	}
	gm := f.agent.gmLines()
	if countPrefix(gm, ".summon ") != 0 || countPrefix(gm, ".revive ") != 0 {
		t.Fatalf("3 failed reads: summons or revives went out: %q", gm)
	}
}

// TestRaidWalkReviveRetriesThePreAppearRead (review round 2, Opus L1): one failed status read before the appear is
// retried and the appear still counts; two leave the commander unjoined, and a normal revive stops.
func TestRaidWalkReviveRetriesThePreAppearRead(t *testing.T) {
	for _, c := range []struct {
		fails int
		want  int
	}{{1, exitOK}, {2, exitRaidWalkIncident}} {
		f := newFixture(t)
		f.agent.startReply = startAt("onyxias_lair", 23)
		f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
		f.allDeadFromHires()
		var resumeAt time.Time
		resumeHook(f, 24, 28, &resumeAt)
		armStatusFails(f, c.fails)
		if code := f.run(context.Background()); code != c.want {
			t.Fatalf("%d failed reads: exit %d, want %d: %s\n%s", c.fails, code, c.want, f.stderr.String(), f.stdout.String())
		}
		f.agent.mu.Lock()
		inst := f.agent.instAtRevive
		f.agent.mu.Unlock()
		if c.want == exitOK && inst != "hires" {
			t.Fatalf("1 failed read: copy at the revive %q, want the hires'", inst)
		}
		if c.want != exitOK && !strings.Contains(f.summary().EndReason, "could not join the hires' instance") {
			t.Fatalf("2 failed reads: end %q", f.summary().EndReason)
		}
	}
}

// TestRaidWalkSelfRegroupJoinsAfterTheRevives (review round 2, Opus L4): an unjoined self-regroup retries the join once
// its revives have the hires up on the plan's map, and notes the result.
func TestRaidWalkSelfRegroupJoinsAfterTheRevives(t *testing.T) {
	f, _ := selfRegroupWipeWith(t, nil, func(f *fixture) {
		f.agent.mu.Lock()
		f.agent.appearRefusedUntilRevive = true
		f.agent.mu.Unlock()
	})
	gm := f.agent.gmLines()
	firstRevive, lateAppear := -1, -1
	for i, l := range gm {
		if strings.HasPrefix(l, ".revive ") && firstRevive < 0 {
			firstRevive = i
		}
		if strings.HasPrefix(l, ".appear ") && firstRevive >= 0 && lateAppear < 0 {
			lateAppear = i
		}
	}
	if lateAppear < 0 {
		t.Fatalf("no appear after the revives: %q", gm)
	}
	if !hasNote(f.summary(), "joined the hires' instance after the self-regroup revives") {
		t.Fatalf("notes %q, want the late-join note", f.summary().Notes)
	}
	f.agent.mu.Lock()
	inst := f.agent.commanderInst
	f.agent.mu.Unlock()
	if inst != "hires" {
		t.Fatalf("commander ends in the %q copy, want the hires'", inst)
	}
}

// TestRaidWalkAppearLandedCheck pins the fresh-landing predicate of a revive `.appear` (review round 1, Codex HIGH):
// on the plan's map, within 15 yd (x/y) of the hire's last frame position, and moved since the read before the appear.
func TestRaidWalkAppearLandedCheck(t *testing.T) {
	hire := botPos{mapID: 309, x: 100, y: 100, z: 10}
	at := func(m uint32, x, y, z float32) session.StatusInfo {
		return session.StatusInfo{State: session.StatusInWorld, Map: m, X: x, Y: y, Z: z}
	}
	portal := at(0, -11915.8, -1236.43, 96.15)
	for _, c := range []struct {
		name   string
		st     session.StatusInfo
		before session.StatusInfo
		want   bool
	}{
		{"landed next to the hire", at(309, 101, 100, 12), portal, true},
		{"14 yd off", at(309, 114, 100, 12), portal, true},
		{"16 yd off", at(309, 116, 100, 12), portal, false},
		{"on the map but far from the hire (an own copy)", at(309, -11926, -1283, 85), at(309, -11926, -1283, 85), false},
		{"moved on the map, far from the hire", at(309, 300, 300, 10), at(309, 0, 0, 0), false},
		{"wrong map", at(0, 101, 100, 12), portal, false},
		{"already next to the hire, not moved (stale)", at(309, 101, 100, 12), at(309, 101, 100, 12), false},
		{"near go on the map next to the hire", at(309, 101, 100, 12), at(309, 105, 100, 12), true},
	} {
		if got := appearLanded(c.st, 309, hire, c.before, 0); got != c.want {
			t.Fatalf("%s: appearLanded = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRaidWalkAppearHire pins the hire the revive appears at (review round 1, M2): among the hires last seen on the
// plan's map, the living one nearest the revive point; a dead one only when none lives there; none otherwise.
func TestRaidWalkAppearHire(t *testing.T) {
	p := dungeon.Point{X: 100, Y: 100, Z: 0}
	w := &walk{plan: &dungeon.Plan{Map: 309}, mt: "Nack", ots: []string{"Lius"}, rest: []string{"Faellara", "Melis"},
		botAt: map[string]botPos{}}
	if got := w.appearHire(p); got != "" {
		t.Fatalf("no hire known: got %q, want none", got)
	}
	w.botAt["Lius"] = botPos{mapID: 0, x: 100, y: 100}
	w.botAt["Melis"] = botPos{mapID: 309, x: 900, y: 900, dead: true}
	w.botAt["Faellara"] = botPos{mapID: 309, x: 101, y: 100, dead: true}
	if got := w.appearHire(p); got != "Faellara" {
		t.Fatalf("got %q, want Faellara (only dead hires on 309: the nearest)", got)
	}
	w.botAt["Nack"] = botPos{mapID: 309, x: 400, y: 100}
	if got := w.appearHire(p); got != "Nack" {
		t.Fatalf("got %q, want Nack (a living hire before a nearer dead one)", got)
	}
	w.botAt["Melis"] = botPos{mapID: 309, x: 150, y: 100}
	if got := w.appearHire(p); got != "Melis" {
		t.Fatalf("got %q, want Melis (the nearest living hire, not the lead)", got)
	}
}

// neverLanding makes the recovery's `.go xyz` never land (the status keeps the old position).
func neverLanding(f *fixture) {
	f.agent.mu.Lock()
	f.agent.goLag = 1 << 30
	f.agent.mu.Unlock()
}

// TestRaidWalkReviveNeverLanding (spec LOW): a landing that never comes ends the walk with exit 19 and "not revived:
// the commander did not land at the revive point", and nobody is summoned.
func TestRaidWalkReviveNeverLanding(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	neverLanding(f)
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "not revived: the commander did not land at the revive point") {
		t.Fatalf("end %q, want %q", sum.EndReason, "not revived: the commander did not land at the revive point")
	}
	if !strings.Contains(sum.EndReason, "(last read: map") {
		t.Fatalf("end %q, want the last status read in it", sum.EndReason)
	}
	if n := countPrefix(f.agent.gmLines(), ".summon "); n != 0 {
		t.Fatalf("%d summons, want none: the commander never landed", n)
	}
}

// TestRaidWalkReviveLandingHonoursTheRecoveryDeadline (Codex MEDIUM-2): the landing wait is bounded by the
// recovery's entry deadline, not only by its own 30 s. The fake clock jumps to 10 s before the deadline at the
// recovery's go (--wipe-rest 30s: deadline = entry + 90s), the landing never comes, and the walk must end with the
// timed-out note within a few seconds, not after the full 30 s landing wait.
func TestRaidWalkReviveLandingHonoursTheRecoveryDeadline(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.wipeRest = 30 * time.Second
	f.allDeadFromHires()
	neverLanding(f)
	hire := f.agent.onGM
	var goAt time.Time
	f.agent.onGM = func(line string) {
		hire(line)
		if goAt.IsZero() && strings.HasPrefix(line, ".go xyz") {
			f.agent.mu.Lock()
			started := f.agent.runStarted
			f.agent.mu.Unlock()
			if started {
				_ = f.clock.sleep(context.Background(), 80*time.Second)
				goAt = f.clock.now()
			}
		}
	}
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if goAt.IsZero() {
		t.Fatal("the recovery never teleported to the revive point")
	}
	sum := f.summary()
	if !hasNote(sum, "wipe recovery timed out after") {
		t.Fatalf("notes %q end %q, want the recovery-timeout note", sum.Notes, sum.EndReason)
	}
	if !strings.Contains(sum.EndReason, "wipe at step") {
		t.Fatalf("end %q, want the original wipe's ending", sum.EndReason)
	}
	if over := f.clock.now().Sub(goAt); over > 20*time.Second {
		t.Fatalf("the walk ran %s past the go, but the recovery deadline was ~10 s away: the landing wait ignored it", over)
	}
}

// TestRaidWalkNoteFramesWriteErrorOnce: the first frames.jsonl write error is noted, once.
func TestRaidWalkNoteFramesWriteErrorOnce(t *testing.T) {
	ff, err := os.CreateTemp(t.TempDir(), "frames")
	if err != nil {
		t.Fatal(err)
	}
	ff.Close() // every write now fails
	w := &walk{framesF: ff, roster: &raidwalk.Roster{}}
	frame := &tbm.ViewFrame{}
	w.saveFrame(time.Unix(1000, 0), frame)
	w.saveFrame(time.Unix(2000, 0), frame)
	w.saveFrame(time.Unix(3000, 0), frame)
	n := 0
	for _, note := range w.notes {
		if strings.HasPrefix(note, "frames.jsonl:") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d frames.jsonl notes in %q, want exactly 1", n, w.notes)
	}
}

// TestRaidWalkEntranceGoNeverLands (review round 1, Codex/Opus H1): the entrance fallback's `.go xyz` is accepted
// (the map id follows) but the server never lands it, so the commander still stands at the revive point. The old code
// set cur = e before the landing wait and then let D-C read e, whose units the walk could see from p said quiet: a
// "not revived" refusal became a summon with the commander still at p. Now only a LANDED go makes the entrance the
// onyxiaReviveAwayFromEntrancePlan copies the plan to a temp dir with the leg start back at the old revive point
// (49.82 -114.607 -24.3112), so a recovery test has a revive point away from the entrance. The committed plan starts
// the leg at the entrance platform: the old point sits inside the warder patrol lane (plan-check patrol lint), and
// with the revive at the entrance the entrance fallback below would land by proximity and the walk would resume
// instead of refusing.
func onyxiaReviveAwayFromEntrancePlan(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../dungeons/onyxias_lair.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	legs, ok := doc["legs"].([]any)
	if !ok || len(legs) < 1 {
		t.Fatal("the plan has no legs")
	}
	leg, ok := legs[0].(map[string]any)
	if !ok {
		t.Fatal("the first leg is not an object")
	}
	leg["to"] = map[string]any{"x": 49.8206, "y": -114.607, "z": -24.3112}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "onyxias_lair.json"), out, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// checked point, a failed landing skips D-C and refuses, and the refusal records entrance_tried.
func TestRaidWalkEntranceGoNeverLands(t *testing.T) {
	f := newFixture(t)
	f.opts.dir = onyxiaReviveAwayFromEntrancePlan(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	// An in-combat unit 30 yd from the revive point keeps it loud through the 120 s check; from the entrance go on
	// the units are quiet, so the read the old code ran at the entrance would have said quiet.
	f.agent.unitsFn = func(n int) []objects.Unit {
		f.agent.mu.Lock()
		x, y, z, gos := f.agent.x, f.agent.y, f.agent.z, f.agent.recoveryGos
		f.agent.mu.Unlock()
		if gos >= 2 {
			return []objects.Unit{}
		}
		return []objects.Unit{{Type: "creature", Name: "Razzashi Skitterer", Entry: 11338, Hostile: true, InCombat: true, X: x + 30, Y: y, Z: z}}
	}
	f.allDeadFromHires()
	f.agent.mu.Lock()
	f.agent.goStuckFrom = 2 // the entrance go (the second recovery go) is accepted but never lands
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "not revived") {
		t.Fatalf("end %q, want a not-revived ending", sum.EndReason)
	}
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".summon "); n != 0 {
		t.Fatalf("%d summons, want none: the commander never landed at the entrance: %q", n, gm)
	}
	if n := countPrefix(gm, ".revive "); n != 0 {
		t.Fatalf("%d revives, want none: the commander never landed at the entrance", n)
	}
	rr := sum.ReviveRefusal
	if rr == nil {
		t.Fatalf("no revive_refusal: end %q", sum.EndReason)
	}
	if !rr.EntranceTried {
		t.Fatalf("refusal %+v, want entrance_tried", rr)
	}
	// Without the skip-D-C fix the wait would run at the entrance until the recovery's own end (many minutes): this
	// refusal is the 120 s check plus the 30 s landing wait and nothing else.
	if rr.WaitedS > 600 {
		t.Fatalf("waited %ds, want the check and the landing wait only (D-C must not run at a point the commander never reached)", rr.WaitedS)
	}
}

// TestRaidWalkQuietPatrolExemptionIsPerPoint (review round 1, Codex H2): the patrol exemption is "its visible member
// is 80 yd out and leaving", measured from the checked point. A stationary member whose distance grows only because
// the walk switched its point (p -> entrance) is not leaving, so the entrance's read must start from a fresh quiet
// state. With p's distances kept, the entrance read would wrongly exempt the patrol and revive into it.
func TestRaidWalkQuietPatrolExemptionIsPerPoint(t *testing.T) {
	plan, err := dungeon.Load("../../dungeons/onyxias_lair.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(plan, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rev := firstStepPoint(res.Rows)
	if rev == nil {
		t.Fatal("the plan has no first positioned step")
	}
	e := plan.Entrance
	// The patrol's one member stands 60 yd beyond the revive point, away from the entrance: 60 yd from p (< 80, so
	// p's reads never exempt it) and ~126 yd from the entrance (>= 80, and farther than p's 60).
	dx, dy, dz := float64(rev.X-e.X), float64(rev.Y-e.Y), float64(rev.Z-e.Z)
	norm := math.Sqrt(dx*dx + dy*dy + dz*dz)
	member := objects.Unit{Type: "creature", Name: "Zulian Patrolman", Hostile: true, GUID: packet.GUID(0x52050),
		X: rev.X + float32(dx/norm*60), Y: rev.Y + float32(dy/norm*60), Z: rev.Z + float32(dz/norm*60)}
	pat := filepath.Join(t.TempDir(), "patrols.json")
	content := fmt.Sprintf(`{"format":"tb-patrols","version":1,"map":%d,"source":"db","patrols":[{"id":"52050","spawn_guids":[335952],"path":[{"x":%v,"y":%v,"z":%v},{"x":%v,"y":%v,"z":%v}],"period_s":60}]}`,
		plan.Map, rev.X, rev.Y, rev.Z, e.X, e.Y, e.Z)
	if err := os.WriteFile(pat, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.opts.patrols = pat
	f.opts.wipeResume = 0 // the boss-wipe ending: no D-C, so the refusal follows the reads themselves
	f.agent.startReply = startAt("onyxias_lair", 23)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{member} }
	f.allDeadFromHires()
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".summon "); n != 0 {
		t.Fatalf("%d summons, want none: the stationary patrol was exempted by the distance the point switch gave it: %q", n, gm)
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "not revived") {
		t.Fatalf("end %q, want a not-revived ending", sum.EndReason)
	}
	if !strings.Contains(sum.EndReason, "52050") {
		t.Fatalf("end %q, want the patrol named", sum.EndReason)
	}
}

// TestRaidWalkReviveStepIsThePointUsed (review round 1, Opus L3): the refusal's start_step is the step of the revive
// point the walk actually used, not the --from step it resolved: a plain walk revives at the plan's first positioned
// row, whatever step the run starts on.
func TestRaidWalkReviveStepIsThePointUsed(t *testing.T) {
	rows := planexport.Rows{Steps: []planexport.StepRow{{Seq: 1, Kind: "rest"}, {Seq: 2, Kind: "move", X: 10, Y: 20, Z: 30}}}
	w := &walk{rows: rows, startStep: 11}
	if got := w.reviveStep(); got != 2 {
		t.Fatalf("reviveStep = %d, want 2 (step 1 is a point-less rest row)", got)
	}
	r := w.refusal(204, nil, 90*time.Second, true)
	if r.StartStep != 2 || len(r.TriedSteps) != 1 || r.TriedSteps[0] != 2 || !r.EntranceTried {
		t.Fatalf("refusal %+v, want start/tried 2 and entrance_tried", r)
	}
	if !strings.Contains(r.Line(), "tried 2, entrance tried") {
		t.Fatalf("summary line %q, want the entrance noted", r.Line())
	}
	// A placed walk revives at the staged start, and that is its step.
	placed := &walk{rows: rows, startStep: 31, place: &raidwalk.PlacePlan{Start: 31}}
	if got := placed.reviveStep(); got != 31 {
		t.Fatalf("placed reviveStep = %d, want 31", got)
	}
	if rr := placed.refusal(204, nil, 0, false); rr.StartStep != 31 || rr.EntranceTried {
		t.Fatalf("placed refusal %+v, want start 31 without entrance_tried", rr)
	}
}

// TestRaidWalkQuietWaitInterrupt (review round 1, Opus L2): an operator's interrupt during the D-C wait is the
// interrupted ending (exit 18, "interrupted"), never a "not revived" refusal with a revive_refusal: the walk did not
// lose the revive, it was stopped.
func TestRaidWalkQuietWaitInterrupt(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 23)
	ctx, cancel := context.WithCancel(context.Background())
	// The point never goes quiet, so the checks run out and D-C starts; the 50th units read is well inside the wait
	// (the checks are 24 reads each, and the wait runs for many minutes).
	f.agent.unitsFn = func(n int) []objects.Unit {
		if n >= 50 {
			cancel()
		}
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		f.agent.mu.Unlock()
		return []objects.Unit{{Type: "creature", Name: "Razzashi Skitterer", Entry: 11338, Hostile: true, InCombat: true, X: x + 30, Y: y, Z: z}}
	}
	f.allDeadFromHires()
	if code := f.run(ctx); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.EndReason != "interrupted" {
		t.Fatalf("end %q, want interrupted", sum.EndReason)
	}
	if sum.ReviveRefusal != nil {
		t.Fatalf("revive_refusal %+v, want none on an interrupt", sum.ReviveRefusal)
	}
}

// TestRaidWalkSelfRegroupQuietWaitIsCapped (review round 1, Opus M2): a self-regroup's D-C wait ends no later than
// selfRegroupQuietCap past the wipe. TB's own regroup is long over by then, so a point still blocked is not about to
// be freed by it, and the walk refuses instead of waiting the recovery out.
func TestRaidWalkSelfRegroupQuietWaitIsCapped(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.opts.wipeResume = 2
	f.agent.startReply = zgStart(total, next)
	step := next - 2
	regroup := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = mcWipeFights()
	selfRegroupKill(f, regroup)
	// The point never goes quiet: an in-combat unit 30 yd out on every read.
	f.agent.unitsFn = func(int) []objects.Unit {
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		f.agent.mu.Unlock()
		return []objects.Unit{{Type: "creature", Name: "Razzashi Skitterer", Entry: 11338, Hostile: true, InCombat: true, X: x + 30, Y: y, Z: z}}
	}
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "not revived") {
		t.Fatalf("end %q, want a not-revived ending", sum.EndReason)
	}
	rr := sum.ReviveRefusal
	if rr == nil {
		t.Fatalf("no revive_refusal: end %q", sum.EndReason)
	}
	if rr.WaitedS < 180 || rr.WaitedS > 400 {
		t.Fatalf("waited %ds, want about %d (the 120 s check plus D-C to the %s cap past the wipe)",
			rr.WaitedS, int(selfRegroupQuietCap/time.Second), durWords(selfRegroupQuietCap))
	}
}

// TestRaidWalkGhostsRecheckAfterTheWait (review round 1, Opus M2): the D-C wait is long, so the hires' maps are
// re-read when it is over. With one hire released to the graveyard during the wait the walk takes the ghosts path: it
// skips the late join, which would `.appear` at a hire that is no longer on the plan's map.
func TestRaidWalkGhostsRecheckAfterTheWait(t *testing.T) {
	f, _ := selfRegroupWipeWith(t, func(p []string) []string {
		if len(p) == 0 {
			return nil
		}
		return p[1:] // the first hire stays dead (and off the map), the rest come up on the plan's map
	}, func(f *fixture) {
		f.agent.mu.Lock()
		f.agent.ghostsOffN = 1
		f.agent.appearRefusedUntilRevive = true
		f.agent.mu.Unlock()
		var entranceAt time.Time
		prev := f.agent.onGM
		f.agent.onGM = func(line string) {
			prev(line)
			f.agent.mu.Lock()
			gos := f.agent.recoveryGos
			f.agent.mu.Unlock()
			if strings.HasPrefix(line, ".go xyz") && gos == 2 && entranceAt.IsZero() {
				entranceAt = f.clock.now()
			}
		}
		// The revive point AND the entrance stay loud through their own 120 s checks, then go quiet so that the D-C
		// wait finds quiet: what happens at the END of that wait is the point of this test.
		f.agent.unitsFn = func(int) []objects.Unit {
			if entranceAt.IsZero() || f.clock.now().Sub(entranceAt) < 125*time.Second {
				f.agent.mu.Lock()
				x, y, z := f.agent.x, f.agent.y, f.agent.z
				f.agent.mu.Unlock()
				return []objects.Unit{{Type: "creature", Name: "Razzashi Skitterer", Entry: 11338, Hostile: true, InCombat: true, X: x + 30, Y: y, Z: z}}
			}
			return []objects.Unit{}
		}
	})
	gm := f.agent.gmLines()
	firstRevive, lateAppear := -1, -1
	for i, l := range gm {
		if strings.HasPrefix(l, ".revive ") && firstRevive < 0 {
			firstRevive = i
		}
		if strings.HasPrefix(l, ".appear ") && firstRevive >= 0 && lateAppear < 0 {
			lateAppear = i
		}
	}
	sum := f.summary()
	if lateAppear >= 0 {
		t.Fatalf("a late .appear went out (line %d) although a hire is off the map: %q", lateAppear, gm)
	}
	if hasNote(sum, "joined the hires' instance after the self-regroup revives") {
		t.Fatalf("notes %q, want no late-join note", sum.Notes)
	}
	if !hasNote(sum, "are on another map after the wait") {
		t.Fatalf("notes %q, want the post-wait ghosts note", sum.Notes)
	}
}
