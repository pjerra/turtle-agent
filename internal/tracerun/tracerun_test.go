package tracerun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/trace"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newClock() *clock { return &clock{t: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)} }

func fr() trace.Frame {
	return trace.Frame{Via: "session", Bots: []trace.Bot{{G: "0x11", Alive: true, Lead: true}}}
}

func TestOffRecorderHasAnIDAndDoesNothing(t *testing.T) {
	c := newClock()
	r := must(Config{Dir: t.TempDir()}, "dungeon_run", "group", c.now)
	if !trace.ValidRunID(r.ID()) || !strings.HasPrefix(r.ID(), "r-20261014T090000-") {
		t.Fatalf("%q", r.ID())
	}
	r.Begin(trace.Header{})
	r.Event("x", nil)
	r.Frame(fr())
	if r.On() || r.Due() || r.Path() != "" {
		t.Fatal("an off recorder is on")
	}
	if info := r.Finish("done", "", nil); info == nil || info.Path != "" {
		t.Fatalf("%+v", info)
	}
	var nilRec *Recorder
	nilRec.Event("x", nil)
	nilRec.Frame(fr())
	if nilRec.ID() != "" || nilRec.On() || nilRec.Finish("done", "", nil) != nil {
		t.Fatal("a nil recorder is not inert")
	}
	// A run id that is not valid is refused: the error says so, the recorder has a generated id and writes nothing.
	dir := t.TempDir()
	r, err := New(Config{On: true, Dir: dir, RunID: "bad id"}, "x", "", nil)
	if err == nil || !strings.Contains(err.Error(), "not valid") || r.ID() == "bad id" || !trace.ValidRunID(r.ID()) {
		t.Fatalf("%v %q", err, r.ID())
	}
	r.Begin(trace.Header{})
	if info := r.Finish("done", "", nil); r.On() || r.Path() != "" || !strings.Contains(info.Error, "not valid") {
		t.Fatalf("%+v", info)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("%v", ents)
	}
}

func TestRecorderWritesScoresAndSummarises(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	r := must(Config{On: true, Dir: dir, RunID: "r-rec", Agent: trace.Agent{Version: "9"}, Meta: map[string]string{"k": "v"}}, "dungeon_run", "remote", c.now)
	if want := filepath.Join(dir, "r-rec.jsonl"); r.Path() != want {
		t.Fatalf("planned path %q", r.Path())
	}
	r.Begin(trace.Header{Plan: "p", Lead: "A", Party: []trace.Member{{G: "0x11", Name: "A", Lead: true}}})
	if !r.On() || Find("r-rec") != r {
		t.Fatal("not registered")
	}
	// The summary exists from the start, as a running run.
	var s trace.Summary
	readJSON(t, filepath.Join(dir, "r-rec.summary.json"), &s)
	if s.Status != "running" || s.Run != "r-rec" || s.Src != "agent" || s.Mode != "remote" || s.AgentVersion != "9" || s.Meta["k"] != "v" || s.Oracles == nil || s.TraceComplete {
		t.Fatalf("%+v", s)
	}
	for i := 0; i < 5; i++ {
		c.t = c.t.Add(time.Second)
		if !r.Due() {
			t.Fatalf("no frame due at %d", i)
		}
		r.Frame(fr())
		if r.Due() {
			t.Fatal("a second frame due at once")
		}
	}
	r.Event("death", map[string]any{"name": "A"})
	c.t = c.t.Add(time.Second)
	info := r.Finish("done", "", map[string]float64{"pulls": 2})
	if info.Frames != 5 || info.Events != 1 || len(info.Checks) != 10 || info.Verdict != trace.Fail || len(info.Failed) != 1 || info.Failed[0] != "deaths" || info.Error != "" {
		t.Fatalf("%+v", info)
	}
	if Find("r-rec") != nil || r.On() {
		t.Fatal("still registered")
	}
	r.Event("late", nil) // after the end nothing is written
	if again := r.Finish("done", "", nil); again.Path != info.Path || again.Frames != 0 {
		t.Fatalf("%+v", again)
	}
	f, err := trace.Read(info.Path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.Mode != "remote" || f.Header.Src != "agent" || f.Header.Start.IsZero() || f.Header.StartUnix == 0 || f.End.Counter("pulls") != 2 || f.End.Seconds != 6 || len(f.Oracles) != 10 || len(f.Events) != 1 || f.Header.ScopeName != "A" {
		t.Fatalf("%+v end %+v", f.Header, f.End)
	}
	readJSON(t, filepath.Join(dir, "r-rec.summary.json"), &s)
	if s.Status != "complete" || s.EndReason != "done" || s.Verdict != trace.Fail || s.EndUnix == 0 || s.Counters["frames"] != float64(5) || s.Counters["pulls"] != float64(2) || s.TraceFile != "r-rec.jsonl" || len(s.Oracles) != 10 || s.ScoredUnix == 0 || !s.TraceComplete {
		t.Fatalf("%+v", s)
	}
}

func TestRecorderThatCannotOpenSaysWhy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "r-x.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := must(Config{On: true, Dir: dir, RunID: "r-x"}, "dungeon_run", "group", nil)
	r.Begin(trace.Header{})
	if r.On() || r.Path() != "" {
		t.Fatal("on")
	}
	if info := r.Finish("done", "", nil); !strings.Contains(info.Error, "exist") {
		t.Fatalf("%+v", info)
	}
}

func TestSamplerTakesFramesUntilStopped(t *testing.T) {
	r := must(Config{On: true, Dir: t.TempDir(), RunID: "r-bg", Background: true, FrameEvery: 5 * time.Millisecond}, "dungeon_run", "group", nil)
	r.Begin(trace.Header{})
	var n atomic.Int32
	stop := r.Sampler(t.Context(), func() { n.Add(1); r.Frame(fr()) })
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	got := n.Load()
	time.Sleep(30 * time.Millisecond)
	if got < 3 || n.Load() != got {
		t.Fatalf("%d samples, %d after the stop", got, n.Load())
	}
	if info := r.Finish("stopped", "", nil); info.Frames == 0 {
		t.Fatalf("%+v", info)
	}
	// Without Background there is no goroutine.
	r2 := must(Config{On: true, Dir: t.TempDir()}, "x", "", nil)
	r2.Sampler(t.Context(), func() { t.Error("sampled") })()
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
}

// A member that stands still in a walk for more than 5 s gets a stuck event with its position while the run goes, and an unstuck one
// when it moves again; combat edges and the mobs registry are written as frames come.
func TestRecorderWritesStuckCombatAndMobRecordsAsFramesCome(t *testing.T) {
	c := newClock()
	r := must(Config{On: true, Dir: t.TempDir(), RunID: "r-stuck"}, "dungeon_run", "group", c.now)
	r.Begin(trace.Header{Lead: "Tank", Party: []trace.Member{{G: "0x11", Name: "Tank", Lead: true}}})
	r.Party(trace.Member{G: "0x12", Name: "Mage"})
	r.Event("walk_start", nil)
	for s := 0; s < 12; s++ {
		c.t = c.t.Add(time.Second)
		follower := trace.Bot{G: "0x12", Alive: true, X: 50, Y: 1, Z: 2, Combat: s >= 10}
		if s >= 9 {
			follower.X = 50 + float32(s)*5
		}
		f := trace.Frame{Via: "session", Bots: []trace.Bot{{G: "0x11", Alive: true, Lead: true, X: float32(s) * 7}, follower}}
		if s == 10 {
			f.Mobs = []trace.Mob{{G: "0xf1", HPPct: 90, Entry: 299, Name: "Ogre"}}
		}
		r.Frame(f)
	}
	r.Event("walk_end", nil)
	info := r.Finish("done", "", nil)
	f, err := trace.Read(info.Path)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range f.Events {
		switch e.Ev {
		case "stuck", "unstuck":
			if e.D["name"] != "Mage" || e.D["a"] != "0x12" || e.D["x"] != float64(50) || e.D["y"] != float64(1) || e.D["z"] != float64(2) {
				t.Fatalf("%+v", e)
			}
			got = append(got, e.Ev)
		case "combat_on", "combat_off":
			if e.D["a"] != "0x12" || e.D["name"] != "Mage" {
				t.Fatalf("%+v", e)
			}
			got = append(got, e.Ev)
		}
	}
	if strings.Join(got, ",") != "stuck,unstuck,combat_on" {
		t.Fatalf("%v", got)
	}
	if m := f.Mobs["0xf1"]; m.Entry != 299 || m.Name != "Ogre" || f.Name("0x12") != "Mage" {
		t.Fatalf("mobs %+v party %+v", f.Mobs, f.Party)
	}
	for _, ck := range info.Checks {
		if ck.ID == "stuck" && ck.Verdict != trace.Fail {
			t.Fatalf("%+v", ck)
		}
	}
}

func must(cfg Config, source, mode string, now func() time.Time) *Recorder {
	r, err := New(cfg, source, mode, now)
	if err != nil {
		panic(err)
	}
	return r
}

// Records that arrive while Finish scores the file (it parses outside the recorder's lock) are dropped, not written after the end.
func TestNothingIsWrittenAfterTheEndRecord(t *testing.T) {
	c := newClock()
	r := must(Config{On: true, Dir: t.TempDir(), RunID: "r-late"}, "dungeon_run", "group", c.now)
	r.Begin(trace.Header{})
	info := r.Finish("done", "", nil)
	r.Event("late", nil)
	r.Frame(fr())
	f, err := trace.Read(info.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Events) != 0 || len(f.Frames) != 0 || f.End == nil || len(f.Oracles) != 10 {
		t.Fatalf("%+v", f)
	}
}
