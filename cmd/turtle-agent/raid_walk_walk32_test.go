package main

// Walk 32's boss-stop bug: a wipe on a boss step the RUN LEADER fights (--until none, or a boss-done until that goes
// past the boss) was read as a boss wipe the walk owns, so `.bot run stop` went out at the wipe line and the run ended
// (a-r1 Mar'li 11:45:11, e-r3 Hakkar 13:13:31). The walk must follow the normal wipe-resume flow instead.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// zgBossRowOf is the 1-based step of the ZG plan row whose boss is entry, its short leg label and the row.
func zgBossRowOf(t *testing.T, entry uint32) (step int, leg string, row planexport.StepRow) {
	t.Helper()
	rows := zgRows(t)
	for i, s := range rows.Steps {
		if s.Kind != planexport.KindBoss || s.Entry != entry {
			continue
		}
		for _, l := range rows.Legs {
			if l.Leg == s.Leg {
				leg = l.Name
			}
		}
		return i + 1, leg, s
	}
	t.Fatalf("no boss row of entry %d in the ZG rows", entry)
	return 0, "", planexport.StepRow{}
}

// zgRunLine is a TBM:RUN status line of the ZG walk (label "" omits the until label, as --until none's run has none).
func zgRunLine(step, total int, leg, kind, phase, note, label string) string {
	line := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|%s|%s|%s|%s", step, total, leg, kind, phase, note)
	if label != "" {
		line += "|" + label
	}
	return line
}

// zgBossWipeFixture is a ZG walk over until/label starting on the boss's own step, the frames holding that boss
// engaged (full HP, targeting the main tank) on every poll, and a wipe on the step that kills every hire in the poll
// that serves the line (the live TB order). The run ends with its own "plan done" once the wipe is handled.
func zgBossWipeFixture(t *testing.T, entry uint32, until, label, wipeNote string) (*fixture, int) {
	t.Helper()
	f := newFixture(t)
	f.opts.plan, f.opts.holder = "zul_gurub", "raid-walk-zul_gurub"
	f.opts.until = until
	f.opts.wipeResume = 3
	f.opts.wipeRest = 5 * time.Minute
	p := zgPlan(t)
	f.agent.instMap = p.Map
	f.agent.enterMap = p.Map
	step, leg, row := zgBossRowOf(t, entry)
	total := len(zgRows(t).Steps)
	f.agent.pos = map[string][3]float32{"Bot01": {row.X, row.Y, row.Z}}
	start := zgRunLine(step, total, leg, "boss", "begin", "fighting", label)
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: start, Lines: []string{start, "TBM:ACTION_ACK|run start|party|1|-"}}}
	// The boss the frames hold: engaged on the main tank on every poll (the gate must see it at the wipe).
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: entry, X: row.X, Y: row.Y, Z: row.Z, HPPct: 100, Target: packet.GUID(1000)}}
	}
	f.agent.unitsFn = func(n int) []objects.Unit { return nil }
	selfRegroupKill(f, zgRunLine(step, total, leg, "boss", "begin", wipeNote, label))
	done := fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|%s|boss|begin|plan done (speed 1.0 (test); 1 wipe this run)", step, total, leg)
	f.agent.mu.Lock()
	f.agent.startQueue = append(f.agent.startQueue, queuedEvent{raw: done})
	f.agent.mu.Unlock()
	return f, step
}

func TestRaidWalkBossStepWipeRecoversUnderTheRunLeadersFight(t *testing.T) {
	for _, c := range []struct {
		name  string
		boss  uint32
		note  string
		label string
	}{
		// a-r1 11:45:10-11: "20 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)" then
		// "> .bot run stop" 0.7 s later.
		{"Mar'li wipe under --until none (a-r1)", 14510,
			"20 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", ""},
		// e-r3 13:13:31: the same shape, "(2 of 2 at this step)", the stop 42 ms after the line.
		{"Hakkar wipe under --until none (e-r3)", 14834,
			"20 dead (1 wipe, 1 near wipe this run): regrouping by itself (2 of 2 at this step)", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := zgBossWipeFixture(t, c.boss, "none", c.label, c.note)
			if code := f.run(context.Background()); code != exitOK {
				t.Fatalf("exit %d, want 0 (notes %v): %s\n%s", code, f.summaryNotes(), f.stderr.String(), f.stdout.String())
			}
			gm := f.agent.gmLines()
			if n := countPrefix(gm, ".bot run stop"); n != 0 {
				t.Fatalf("%d run stops, want none (a boss-step wipe the run leader fights recovers; gm %q)", n, gm)
			}
			sum := f.summary()
			if sum.WipeResumes != 1 {
				t.Fatalf("wipe_resumes %d, want 1: notes %q", sum.WipeResumes, sum.Notes)
			}
			if !hasNote(sum, "the run regroups by itself") {
				t.Fatalf("notes %q, want the self-regroup recovery note", sum.Notes)
			}
			if hasNote(sum, "wipe resume skipped") {
				t.Fatalf("notes %q, want no skipped recovery", sum.Notes)
			}
			if n := countPrefix(gm, ".revive "); n == 0 {
				t.Fatalf("no revive went out: the wipe never recovered (gm %q)", gm)
			}
		})
	}
}

// The boss-done shape: the until clause that goes past the boss step (the resolved "--until boss-done: step B+1"), so
// the run leader fights the boss until it is DEAD. A wipe on the boss step there recovers too.
func TestRaidWalkBossStepWipeRecoversUnderBossDone(t *testing.T) {
	step, _, _ := zgBossRowOf(t, 14599)
	label := fmt.Sprintf("until step %d (step %d)", step+1, step+1)
	f, _ := zgBossWipeFixture(t, 14599, fmt.Sprintf("step %d", step+1), label,
		"20 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s\n%s", code, f.summaryNotes(), f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want none (the boss-done until hands the boss to the run leader; gm %q)", n, gm)
	}
	if sum := f.summary(); sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1: notes %q", sum.WipeResumes, sum.Notes)
	}
}

// summaryNotes is the finished walk's summary notes (nil when it wrote no summary).
func (f *fixture) summaryNotes() []string {
	raw, err := os.ReadFile(filepath.Join(f.out, "summary.json"))
	if err != nil {
		return nil
	}
	var s raidwalk.Summary
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return s.Notes
}
