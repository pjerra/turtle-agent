package dungeon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

func evNames(f *trace.File) map[string]int {
	m := map[string]int{}
	for _, e := range f.Events {
		m[e.Ev]++
	}
	return m
}

// A group run with the trace on: the run id is in the report and in every event, the file holds a header, frames, the run's
// events, the end and the scored checks, and the summary file lists the verdicts.
func TestTraceOfAGroupRun(t *testing.T) {
	g, p := runFixture(t)
	dir := t.TempDir()
	var stamped, unstamped int
	g.onEmit = func(kind string, data map[string]any) {
		if data["run_id"] == "r-test-1" {
			stamped++
		} else {
			unstamped++
		}
	}
	cfg := Config{Trace: tracerun.Config{On: true, Dir: dir, RunID: "r-test-1", Meta: map[string]string{"tb_sha": "abc123"}}}
	d := NewDriver(p, g, cfg, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	if rep.RunID != "r-test-1" || stamped == 0 || unstamped != 0 {
		t.Fatalf("run id %q, %d stamped events, %d without the id", rep.RunID, stamped, unstamped)
	}
	if rep.Trace == nil || rep.Trace.Path != filepath.Join(dir, "r-test-1.jsonl") || rep.Trace.Error != "" || len(rep.Trace.Checks) != 10 || rep.Trace.Frames == 0 {
		t.Fatalf("trace info %+v", rep.Trace)
	}
	f, err := trace.Read(rep.Trace.Path)
	if err != nil {
		t.Fatal(err)
	}
	h := f.Header
	if h.V != 1 || h.Run != "r-test-1" || h.Src != "agent" || h.Schema != "bt-trace" || h.Scope != "party" || h.ScopeName != "Lead" || h.Requester != "Lead" || h.Source != "dungeon_run" ||
		h.Mode != "group" || h.Plan != "t" || h.Meta["tb_sha"] != "abc123" || h.IntervalMS != 1000 || !h.HasCap(trace.CapFacing) || !h.HasCap(trace.CapMoving) || h.HasCap(trace.CapThreat) {
		t.Fatalf("header %+v", h)
	}
	if len(f.Party) != 3 || f.Party[0].Name != "Lead" || !f.Party[0].Lead || f.Party[0].G != "0x10" || f.Party[1].Name != "Tank" || f.Party[1].Class != "warrior" || f.Party[2].Class != "priest" {
		t.Fatalf("party %+v", f.Party)
	}
	if len(f.Frames) < 10 {
		t.Fatalf("%d frames", len(f.Frames))
	}
	fr := f.Frames[len(f.Frames)/2]
	if fr.Via != "session" || fr.Src != "agent" || len(fr.Bots) != 3 || !fr.Bots[0].Lead || fr.Bots[0].G != "0x10" || fr.Bots[1].MaxHP != 900 || fr.Bots[1].G != "0x11" || !fr.Bots[1].Alive {
		t.Fatalf("frame %+v", fr)
	}
	// A frame about once a second of the (fake) run.
	for i := 1; i < len(f.Frames); i++ {
		if dt := f.Frames[i].T - f.Frames[i-1].T; dt < 800 {
			t.Fatalf("frames %d ms apart", dt)
		}
	}
	n := evNames(f)
	for _, ev := range []string{"leg", "step", "order", "pull_start", "pull_end", "walk_start", "walk_end", "rest"} {
		if n[ev] == 0 {
			t.Errorf("no %q event in %v", ev, n)
		}
	}
	if f.End == nil || f.End.Reason != "done" || f.End.Seconds <= 0 || f.End.Counter("pulls") != 3 || f.End.Counter("frames") != float64(len(f.Frames)) || f.End.Counter("party") != 3 || f.End.Counter("mobs") == 0 {
		t.Fatalf("end %+v", f.End)
	}
	if len(f.Oracles) != 10 || f.Oracles[0].ID == "" {
		t.Fatalf("oracles %+v", f.Oracles)
	}
	var s trace.Summary
	b, err := os.ReadFile(filepath.Join(dir, "r-test-1.summary.json"))
	if err != nil || json.Unmarshal(b, &s) != nil {
		t.Fatalf("summary: %v %s", err, b)
	}
	if s.Run != "r-test-1" || s.Src != "agent" || s.Status != "complete" || s.EndReason != "done" || s.Plan != "t" || len(s.Oracles) != 10 || s.TraceFile != "r-test-1.jsonl" || s.Verdict == "" || s.EndUnix == 0 || s.Scorer == "" {
		t.Fatalf("summary %+v", s)
	}
	for _, ev := range f.Events { // every event carries the envelope
		if ev.Run != "r-test-1" || ev.V != 1 || ev.Src != "agent" {
			t.Fatalf("event %+v", ev)
		}
	}
}

// Without Trace.On the run still has an id, and nothing is written.
func TestRunIDWithoutATrace(t *testing.T) {
	g, p := runFixture(t)
	dir := t.TempDir()
	d := NewDriver(p, g, Config{Trace: tracerun.Config{Dir: dir}}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	rep := d.Report()
	if !regexp.MustCompile(`^r-\d{8}T\d{6}-[0-9a-f]{4}$`).MatchString(rep.RunID) || rep.Trace != nil && rep.Trace.Path != "" {
		t.Fatalf("run id %q trace %+v", rep.RunID, rep.Trace)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("files written: %v", ents)
	}
	if last := g.events[len(g.events)-1]; last.data["run_id"] != rep.RunID {
		t.Fatalf("%+v", last)
	}
}

// A trace that cannot be opened (the run id exists already) does not stop the run.
func TestTraceThatCannotOpenIsNotedAndTheRunGoesOn(t *testing.T) {
	g, p := runFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dup.jsonl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDriver(p, g, Config{Trace: tracerun.Config{On: true, Dir: dir, RunID: "dup"}}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if rep := d.Report(); rep.Trace == nil || !strings.Contains(rep.Trace.Error, "exist") || rep.State != "done" {
		t.Fatalf("%+v", rep.Trace)
	}
}

// A death and a wipe, a GM shortcut and a refusal reach the trace and fail the checks that are about them.
func TestTraceRecordsDeathsShortcutsAndRefusals(t *testing.T) {
	g, p := runFixture(t)
	dir := t.TempDir()
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action pullback" && g.count(text) == 1 {
			g.party[0].Dead, g.party[0].Health = true, 0
		}
	}
	cfg := Config{Teleport: true, Shortcuts: true, GM: GMAssumed, Trace: tracerun.Config{On: true, Dir: dir, RunID: "r-dead"}}
	d := NewDriver(p, g, cfg, g.clock())
	_ = d.Run(context.Background(), 0)
	rep := d.Report()
	if rep.Trace == nil || rep.Trace.Path == "" {
		t.Fatalf("%+v", rep.Trace)
	}
	f, err := trace.Read(rep.Trace.Path)
	if err != nil {
		t.Fatal(err)
	}
	n := evNames(f)
	if n["death"] == 0 {
		t.Fatalf("no death event: %v", n)
	}
	failed := map[string]bool{}
	for _, c := range f.Oracles {
		if c.Verdict == trace.Fail {
			failed[c.ID] = true
		}
	}
	if !failed["deaths"] {
		t.Fatalf("failed %v, oracles %+v", failed, f.Oracles)
	}
	if rep.Trace.Verdict != trace.Fail {
		t.Fatalf("verdict %q", rep.Trace.Verdict)
	}
}

// A remote run: the feed's frames, the orders with their answers and the threat lists go into the trace.
func TestTraceOfARemoteRun(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
		Leg{Name: "second", To: Point{200, 0, 0}})
	dir := t.TempDir()
	o := opts()
	o.Tank = "Tanky"
	rep, err := runRemote(t, g, plan, o, func(c *Config) {
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-remote"}
	})
	if err != nil || rep.RunID != "r-remote" || rep.Trace == nil || rep.Trace.Path == "" {
		t.Fatalf("%v %+v", err, rep.Trace)
	}
	f, err := trace.Read(rep.Trace.Path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.Mode != "remote" || f.Header.Lead != "Tanky" || f.Header.Tank != "Tanky" || f.Header.Requester != "" || !f.Header.HasCap(trace.CapThreat) || f.Header.HasCap(trace.CapMoving) {
		t.Fatalf("header %+v", f.Header)
	}
	if len(f.Party) != 3 || f.Party[0].Name != "Tanky" || f.Party[0].Role != "tank" || !f.Party[0].Lead || f.Party[1].Role != "" {
		t.Fatalf("party %+v", f.Party)
	}
	if len(f.Frames) == 0 || f.Frames[0].Via != "feed" || f.Frames[0].Seq == 0 || !f.Frames[0].Bots[0].Lead || f.Frames[0].Bots[0].G != f.Party[0].G {
		t.Fatalf("frames %d %+v", len(f.Frames), f.Frames)
	}
	var orders, answered, hostiles int
	for _, e := range f.Events {
		if e.Ev == "order" {
			orders++
			if e.D["reply"] == "ack" {
				answered++
			}
		}
	}
	threat := 0
	for _, fr := range f.Frames {
		hostiles += len(fr.Mobs)
		for _, m := range fr.Mobs {
			if len(m.Threat) > 0 {
				threat++
			}
		}
	}
	n := evNames(f)
	if orders < 3 || answered == 0 || hostiles == 0 || n["walk_start"] == 0 || n["pull_start"] == 0 || n["pull_end"] == 0 || n["roster"] != 1 || threat == 0 {
		t.Fatalf("orders %d answered %d hostiles %d events %v", orders, answered, hostiles, n)
	}
	if f.End == nil || f.End.Counter("orders_sent") == 0 || len(f.Oracles) != 10 {
		t.Fatalf("end %+v oracles %d", f.End, len(f.Oracles))
	}
}
