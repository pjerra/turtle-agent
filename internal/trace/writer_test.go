package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRunIDs(t *testing.T) {
	id := NewRunID(time.Date(2026, 10, 14, 9, 30, 12, 0, time.UTC))
	if !regexp.MustCompile(`^r-20261014T093012-[0-9a-f]{4}$`).MatchString(id) || !ValidRunID(id) {
		t.Fatalf("%q", id)
	}
	if NewRunID(time.Now()) == NewRunID(time.Now()) {
		t.Fatal("two ids the same")
	}
	for _, ok := range []string{"r-1", "scenario.7_a", "A", strings.Repeat("a", 64)} {
		if !ValidRunID(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-x", ".x", "a/b", "a b", "../x", "x\n", strings.Repeat("a", 65)} {
		if ValidRunID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func open(t *testing.T, o Options) *Writer {
	t.Helper()
	if o.Dir == "" {
		o.Dir = t.TempDir()
	}
	if o.RunID == "" {
		o.RunID = "r-t"
	}
	w, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func frame(t int64) Frame {
	return Frame{Envelope: Envelope{T: t}, Via: "session", Bots: []Bot{{G: "0x11", Alive: true, State: "n", HP: 100, MaxHP: 100, X: 1, Y: 2, Z: 3, Lead: true}}}
}

// Every line starts with the same five fields (v, k, run, src, t); the header names the schema and the limits; the file reads back
// whole.
func TestWriterSchema(t *testing.T) {
	w := open(t, Options{})
	fa := 30
	w.Header(Header{Source: "dungeon_run", Mode: "group", Plan: "uldaman", Start: time.Unix(1_000_000, 0).UTC(), StartUnix: 1_000_000, AgentVersion: "0.1.0", AgentSHA: "abc",
		Caps: []string{CapFacing}, Party: []Member{{G: "0x11", Name: "Tank", Class: "warrior", Level: 40, Lead: true}}, Meta: map[string]string{"tb_sha": "def"}})
	w.Party(500, Member{G: "0x12", Name: "Heal"})
	w.Party(500, Member{G: "0x12", Name: "Heal"}) // once
	fr := frame(1000)
	fr.Bots[0].Facing = &fa
	fr.Mobs = []Mob{{G: "0xf1", HPPct: 80, Combat: true, Entry: 299, Name: "Ogre", Threat: []Threat{{"0x11", 100}, {"0x12", 60}}}}
	w.Frame(fr)
	w.Mob(1000, MobInfo{G: "0xf1", Entry: 299, Name: "Ogre"})
	w.Mob(1000, MobInfo{G: "0xf1", Entry: 299, Name: "Ogre"})
	w.Event(1500, "order", map[string]any{"intent": "pullback", "a": "0x11", "v": 7, "t": "x"})
	w.End(End{Envelope: Envelope{T: 2000}, Ended: time.Unix(1_000_002, 0).UTC(), Reason: "done", Seconds: 2, Counters: map[string]any{"pulls": 3}})
	w.Oracles([]Check{{ID: "x", Name: "X", Verdict: Pass, Value: 1, Threshold: 2, Unit: "s", Evidence: []int64{5}}})
	w.Close()
	b, _ := os.ReadFile(w.Path())
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	kinds := []string{"header", "party", "party", "frame", "mobs", "event", "end", "oracle"}
	if len(lines) != len(kinds) {
		t.Fatalf("%d lines:\n%s", len(lines), b)
	}
	for i, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		if m["v"] != float64(1) || m["k"] != kinds[i] || m["run"] != "r-t" || m["src"] != "agent" || m["t"] == nil {
			t.Errorf("line %d: %v", i, m)
		}
		if !strings.HasPrefix(l, `{"v":1,"k":"`+kinds[i]+`","run":"r-t","src":"agent","t":`) {
			t.Errorf("line %d does not start with the envelope: %s", i, l)
		}
	}
	// The event is flat; a field named like an envelope field is renamed, not allowed to overwrite it.
	if !strings.Contains(lines[5], `"ev":"order"`) || !strings.Contains(lines[5], `"a":"0x11"`) || !strings.Contains(lines[5], `"x_v":7`) || !strings.Contains(lines[5], `"x_t":"x"`) || strings.Contains(lines[5], `"d":`) {
		t.Fatalf("%s", lines[5])
	}
	if !strings.Contains(lines[3], `"thr":[["0x11",100],["0x12",60]]`) || !strings.Contains(lines[3], `"fa":30`) || strings.Contains(lines[3], `"entry"`) {
		t.Fatalf("%s", lines[3])
	}
	f, err := Read(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	h := f.Header
	if h.Schema != "bt-trace" || h.Scope != "party" || h.IntervalMS != 1000 || h.MaxBytes != DefaultMaxBytes || h.Meta["tb_sha"] != "def" || !h.HasCap(CapFacing) || h.HasCap(CapMoving) || h.Src != "agent" {
		t.Fatalf("header %+v", h)
	}
	if len(f.Party) != 2 || f.Party[0].Class != "warrior" || f.Name("0x12") != "Heal" || f.Name("0x99") != "0x99" {
		t.Fatalf("party %+v", f.Party)
	}
	if len(f.Frames) != 1 || f.Frames[0].Bots[0].X != 1 || *f.Frames[0].Bots[0].Facing != 30 || f.Frames[0].Mobs[0].Threat[1].Pct != 60 || f.Mobs["0xf1"].Name != "Ogre" {
		t.Fatalf("%+v", f.Frames)
	}
	if len(f.Events) != 1 || f.Events[0].Ev != "order" || f.Events[0].D["intent"] != "pullback" || f.Events[0].D["x_v"] != float64(7) || f.Events[0].T != 1500 {
		t.Fatalf("%+v", f.Events)
	}
	c := f.End.Counters
	if f.End == nil || f.End.Counter("pulls") != 3 || f.End.Counter("frames") != 1 || f.End.Counter("events") != 1 || f.End.Counter("party") != 2 || f.End.Counter("mobs") != 1 ||
		f.End.Counter("records") != 7 || c["truncated"] != false || f.End.Counter("dropped") != 0 || f.End.Reason != "done" {
		t.Fatalf("end %+v", f.End)
	}
	if len(f.Oracles) != 1 || f.Oracles[0].ID != "x" || f.Oracles[0].Evidence[0] != 5 || f.Seconds() != 2 {
		t.Fatalf("oracles %+v", f.Oracles)
	}
}

// Frames faster than the interval are sampled away (not counted as dropped); a feed that runs a little fast is not halved.
func TestFrameSampling(t *testing.T) {
	w := open(t, Options{FrameEvery: time.Second})
	wrote := 0
	for ms := int64(0); ms <= 10_000; ms += 300 {
		if w.Frame(frame(ms)) {
			wrote++
		}
	}
	if wrote < 9 || wrote > 12 {
		t.Fatalf("%d frames of 34 offered over 10 s", wrote)
	}
	w2 := open(t, Options{RunID: "r-t2", Dir: filepath.Dir(w.Path())})
	w2.Header(Header{Source: "x"})
	n := 0
	for ms := int64(0); ms < 10_000; ms += 950 {
		if w2.Frame(frame(ms)) {
			n++
		}
	}
	if n != 11 {
		t.Fatalf("a feed at 950 ms: %d frames, want all 11", n)
	}
	w2.End(End{})
	if f, _ := ReadAfterClose(t, w2); f.End.Counter("dropped") != 0 {
		t.Fatalf("sampling counted as a loss: %+v", f.End.Counters)
	}
}

func ReadAfterClose(t *testing.T, w *Writer) (*File, error) {
	t.Helper()
	w.Close()
	f, err := Read(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	return f, err
}

// At the size limit the frames stop with a note in the trace; events go on a quarter further; the end record is always written.
func TestSizeLimitStopsFramesThenEventsAndSaysSo(t *testing.T) {
	const max = 6000
	w := open(t, Options{MaxBytes: max, FrameEvery: time.Millisecond})
	w.Header(Header{Source: "dungeon_run"})
	for i := int64(0); i < 200; i++ {
		w.Frame(frame(i * 10))
	}
	if !w.Truncated() {
		t.Fatal("the limit was not noted")
	}
	afterFrames := w.Bytes()
	for i := 0; i < 200; i++ {
		w.Event(int64(i), "step", map[string]any{"n": i})
	}
	w.End(End{Envelope: Envelope{T: 9}, Reason: "done"})
	w.Oracles([]Check{{ID: "a", Verdict: NA}})
	f, _ := ReadAfterClose(t, w)
	if w.Bytes() > max+max/4+2000 || afterFrames < max {
		t.Fatalf("size %d after frames %d, limit %d", w.Bytes(), afterFrames, max)
	}
	limits := 0
	for _, e := range f.Events {
		if e.Ev == "trace_limit" {
			limits++
		}
	}
	e := f.End
	if limits != 2 || e.Counters["truncated"] != true || e.Counter("dropped_frames") == 0 || e.Counter("dropped_events") == 0 || e.Counter("frames") >= 200 || len(f.Events) == 0 || len(f.Oracles) != 1 {
		t.Fatalf("limit events %d, end %+v, %d events, %d oracles", limits, e, len(f.Events), len(f.Oracles))
	}
	// The frames thin out before the limit: past half of it only a third are taken.
	if e.Counter("frames") < 10 {
		t.Fatalf("%v frames", e.Counter("frames"))
	}
}

func TestOpenRefusesAnExistingFileAndABadID(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(Options{Dir: dir, RunID: "a/b"}); err == nil {
		t.Fatal("a path as a run id")
	}
	w, err := Open(Options{Dir: dir, RunID: "same"})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := Open(Options{Dir: dir, RunID: "same"}); err == nil || !strings.Contains(err.Error(), "exist") {
		t.Fatalf("%v", err)
	}
	if _, err := Open(Options{Dir: filepath.Join(dir, "new", "deeper"), RunID: "x"}); err != nil {
		t.Fatalf("the folder is made: %v", err)
	}
}

// Rotation of the folder: the oldest generated-id traces and their summaries go, others stay.
func TestPruneKeepsTheNewestRuns(t *testing.T) {
	dir := t.TempDir()
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("r-20261014T0900%02d-abcd", i)
		touch(id + ".jsonl")
		touch(id + ".summary.json")
	}
	touch("mine.jsonl")
	touch("notes.txt")
	Prune(dir, 3, "r-20261014T090001-abcd")
	left, _ := os.ReadDir(dir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	got := strings.Join(names, " ")
	// The current run (the oldest) is never deleted; the next oldest goes.
	for _, want := range []string{"r-20261014T090001-abcd.jsonl", "r-20261014T090003-abcd.jsonl", "r-20261014T090005-abcd.summary.json", "mine.jsonl", "notes.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s missing from %s", want, got)
		}
	}
	for _, gone := range []string{"r-20261014T090002-abcd.jsonl", "r-20261014T090002-abcd.summary.json"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s kept", gone)
		}
	}
	// Open prunes by itself.
	for i := 6; i <= 9; i++ {
		touch(fmt.Sprintf("r-20261014T0900%02d-abcd.jsonl", i))
	}
	w, err := Open(Options{Dir: dir, RunID: "fresh", Keep: 2})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	var gen int
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if generated.MatchString(e.Name()) {
			gen++
		}
	}
	if gen != 2 {
		t.Fatalf("%d generated-id files kept", gen)
	}
}

func TestSummaryIsWrittenWholeNextToTheTraceInTheServersShape(t *testing.T) {
	w := open(t, Options{})
	w.Header(Header{Source: "dungeon_run", Mode: "remote", Plan: "uldaman", Lead: "Tanky", ScopeName: "Tanky", Start: time.Unix(1_000_000, 0).UTC(), StartUnix: 1_000_000, AgentVersion: "0.1.0", Map: 70,
		Party: []Member{{G: "0x11", Name: "Tanky", Lead: true}}})
	w.End(End{Envelope: Envelope{T: 2500}, Ended: time.Unix(1_000_002, 0).UTC(), Reason: "done", Seconds: 2.5, Counters: map[string]any{"deaths": 0}})
	w.Close()
	f, err := Read(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Not scored yet: no verdict, an empty oracles array (a reader never sees null).
	s := SummaryOf(f, w.Bytes(), nil, time.Unix(1_000_100, 0))
	if err := WriteSummary(w.Path(), s); err != nil {
		t.Fatal(err)
	}
	s = SummaryOf(f, w.Bytes(), []Check{{ID: "a", Verdict: Fail, Count: 2}, {ID: "b", Verdict: Pass}}, time.Unix(1_000_100, 0))
	if err := WriteSummary(w.Path(), s); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Dir(w.Path()))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "r-t.jsonl r-t.summary.json" {
		t.Fatalf("%v", names)
	}
	b, _ := os.ReadFile(SummaryPath(w.Path()))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	// The server's summary keys, all there.
	for _, k := range []string{"v", "schema", "run", "src", "status", "end_reason", "scope", "scope_name", "trace_file", "start_unix", "end_unix", "duration_ms", "interval_ms", "map", "instance",
		"counters", "oracles", "verdict", "scored_unix", "scorer", "trace_complete", "bad_lines"} {
		if _, ok := m[k]; !ok {
			t.Errorf("summary has no %q: %s", k, b)
		}
	}
	if m["src"] != "agent" || m["status"] != "complete" || m["end_reason"] != "done" || m["trace_file"] != "r-t.jsonl" || m["verdict"] != "fail" || m["duration_ms"] != float64(2500) ||
		m["start_unix"] != float64(1_000_000) || m["end_unix"] != float64(1_000_002) || m["trace_complete"] != true || m["map"] != float64(70) || len(m["oracles"].([]any)) != 2 {
		t.Fatalf("%s", b)
	}
	o := m["oracles"].([]any)[0].(map[string]any)
	if o["id"] != "a" || o["verdict"] != "fail" || o["count"] != float64(2) {
		t.Fatalf("%v", o)
	}
	// A run that has not ended is running, with an empty oracles list.
	f.End = nil
	r := SummaryOf(f, 10, nil, time.Now())
	rb, _ := json.Marshal(r)
	if r.Status != "running" || r.TraceComplete || !strings.Contains(string(rb), `"oracles":[]`) || strings.Contains(string(rb), `"verdict"`) {
		t.Fatalf("%s", rb)
	}
}

func TestVerdictFolds(t *testing.T) {
	cases := []struct {
		in     []Check
		want   string
		failed string
	}{
		{nil, NA, ""},
		{[]Check{{ID: "a", Verdict: NA}}, NA, ""},
		{[]Check{{ID: "a", Verdict: Pass}, {ID: "b", Verdict: NA}}, Pass, ""},
		{[]Check{{ID: "a", Verdict: Pass}, {ID: "b", Verdict: Fail}, {ID: "c", Verdict: Fail}}, Fail, "b,c"},
	}
	for _, c := range cases {
		v, f := Verdict(c.in)
		if v != c.want || strings.Join(f, ",") != c.failed {
			t.Errorf("%+v: %s %v", c.in, v, f)
		}
	}
}

func TestReadIsTolerantAndRefusesANewerSchema(t *testing.T) {
	hdr := `{"v":1,"k":"header","run":"r","src":"server","t":0,"schema":"bt-trace","scope":"party","caps":["facing","moving"]}`
	f, err := Parse("t", []byte(hdr+"\n"+`{"v":1,"k":"mystery","run":"r"}`+"\n"+`{"v":1,"k":"event","run":"r","src":"server","t":5,"ev":"loot","a":"0x1","s":"Item","new_field":3}`+"\n"+`{"v":1,"k":"frame","run":"r","t":1,"bots":[{"g":"0x1","al":true`))
	if err != nil {
		t.Fatal(err)
	}
	// A server trace reads too: the same envelope, its caps, its flat events.
	if len(f.Events) != 1 || f.Events[0].D["s"] != "Item" || f.Events[0].D["new_field"] != float64(3) || f.Header.Src != "server" || !f.Header.HasCap("moving") || len(f.Frames) != 0 ||
		f.Skipped != 1 || f.End != nil || f.Seconds() != 0.005 {
		t.Fatalf("%+v", f)
	}
	if _, err := Parse("t", []byte(`{"v":2,"k":"header","run":"r"}`)); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("%v", err)
	}
	if _, err := Parse("t", []byte(`{"v":1,"k":"event","run":"r","t":1,"ev":"x"}`)); err != ErrNoHeader {
		t.Fatalf("%v", err)
	}
}
