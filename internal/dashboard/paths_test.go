package dashboard

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// ---- a small writer of run traces (tracerun files), as the agent writes them

type traceW struct {
	run   string
	lines []string
}

func newTrace(run, plan string, startUnix int64) *traceW {
	w := &traceW{run: run}
	start := time.Unix(startUnix, 0).UTC().Format(time.RFC3339Nano)
	w.add(`{"v":1,"k":"header","run":%q,"src":"agent","t":0,"plan":%q,"map":70,"mode":"remote","lead":"Tank","start":%q,"start_unix":%d}`, run, plan, start, startUnix)
	w.add(`{"v":1,"k":"party","run":%q,"t":0,"g":"0x1","name":"Tank","role":"tank","lead":true}`, run)
	w.add(`{"v":1,"k":"party","run":%q,"t":0,"g":"0x2","name":"Heal","role":"healer"}`, run)
	return w
}

func (w *traceW) add(format string, a ...any) { w.lines = append(w.lines, fmt.Sprintf(format, a...)) }

// frame adds a 1-second frame with the lead at (lx, ly) in game coordinates; cb says the lead is in combat. The healer stands 3 yards off.
func (w *traceW) frame(tMS int64, lx, ly float64, cb bool, act string) {
	w.add(`{"v":1,"k":"frame","run":%q,"src":"agent","t":%d,"via":"feed","seq":%d,"bots":[{"g":"0x1","al":true,"cb":%t,"x":%.1f,"y":%.1f,"z":0,"mv":false,"act":%q,"lead":true},{"g":"0x2","al":true,"cb":false,"x":%.1f,"y":%.1f,"z":0}],"mobs":[]}`,
		w.run, tMS, tMS/1000, cb, lx, ly, act, lx+3, ly)
}

func (w *traceW) event(tMS int64, ev, extra string) {
	if extra != "" {
		extra = "," + extra
	}
	w.add(`{"v":1,"k":"event","run":%q,"src":"agent","t":%d,"ev":%q%s}`, w.run, tMS, ev, extra)
}

func (w *traceW) end(tMS int64, reason string) {
	w.add(`{"v":1,"k":"end","run":%q,"src":"agent","t":%d,"reason":%q,"seconds":%.3f}`, w.run, tMS, reason, float64(tMS)/1000)
}

func (w *traceW) text() string { return strings.Join(w.lines, "\n") + "\n" }

// walk adds frames every second from t0 to t1 (ms) while the lead goes from (x0, y0) to (x1, y1).
func (w *traceW) walk(t0, t1 int64, x0, y0, x1, y1 float64) {
	for t := t0; t <= t1; t += 1000 {
		f := float64(t-t0) / float64(t1-t0)
		w.frame(t, x0+(x1-x0)*f, y0+(y1-y0)*f, false, "follow")
	}
}

func digestOf(w *traceW) *digestBuilder { return digestFile([]byte(w.text())) }

func TestDigestPathsAndPlane(t *testing.T) {
	w := newTrace("r-1", "uld", 1000)
	w.walk(0, 60000, 10, 0, 110, 0) // north: game x grows
	d := digestOf(w).Snapshot(0)
	if d.Run != "r-1" || d.Plan != "uld" || d.Map != 70 || d.Lead != "Tank" {
		t.Fatalf("header: %+v", d)
	}
	if len(d.Paths) != 2 || !d.Paths[0].Lead || d.Paths[0].Name != "Tank" || d.Paths[0].Role != "tank" || d.Paths[1].Name != "Heal" || d.Paths[1].Role != "healer" {
		t.Fatalf("paths: %+v", d.Paths)
	}
	p := d.Paths[0]
	// the lead went from game (10,0) to (110,0): the drawing plane has north up, so y goes from -10 to -110
	first, last := p.P[0], p.P[len(p.P)-1]
	if first != [2]float64{0, -10} || last != [2]float64{0, -110} {
		t.Fatalf("plane: %v .. %v", first, last)
	}
	if len(p.P) != len(p.T) || p.T[0] != 0 || p.T[len(p.T)-1] != 60 {
		t.Fatalf("times %v", p.T)
	}
	if len(p.P) > 5 { // a straight walk is thinned to its ends
		t.Fatalf("a straight walk should be thinned, got %d points", len(p.P))
	}
	if d.Seconds != 60 || d.Ended {
		t.Fatalf("seconds %v ended %v", d.Seconds, d.Ended)
	}
}

func TestDigestRecentMinutes(t *testing.T) {
	w := newTrace("r-1", "uld", 1000)
	// a zig-zag walk that stays in the digest's points: 10 minutes, a corner every 30 s
	for i := 0; i <= 20; i++ {
		x := float64(i * 20)
		y := 0.0
		if i%2 == 1 {
			y = 40
		}
		w.frame(int64(i)*30000, x, y, false, "follow")
	}
	all := digestOf(w).Snapshot(0)
	cut := digestOf(w).Snapshot(2)
	if len(cut.Paths) == 0 || len(cut.Paths[0].P) >= len(all.Paths[0].P) || len(cut.Paths[0].P) == 0 {
		t.Fatalf("2 minutes should keep fewer points: %d of %d", len(cut.Paths[0].P), len(all.Paths[0].P))
	}
	for _, ts := range cut.Paths[0].T {
		if ts < 600-120 {
			t.Fatalf("a point of %v s is older than 2 minutes at 600 s", ts)
		}
	}
}

func TestDigestStalls(t *testing.T) {
	w := newTrace("r-1", "uld", 1000)
	w.event(0, "leg", `"leg":2,"name":"hall","phase":"start"`)
	w.walk(0, 10000, 0, 0, 50, 0)
	for ts := int64(11000); ts <= 50000; ts += 1000 { // 39 s standing still, not in combat, then a rest of 20 s
		w.frame(ts, 50, 0, false, "stay")
	}
	w.event(40000, "rest", `"leg":2,"seconds":20.5`)
	w.walk(51000, 60000, 50, 0, 50, 90)
	for ts := int64(61000); ts <= 80000; ts += 1000 { // 19 s: too short
		w.frame(ts, 50, 90, false, "stay")
	}
	w.walk(81000, 90000, 50, 90, 50, 200)
	for ts := int64(91000); ts <= 140000; ts += 1000 { // 49 s in a fight
		w.frame(ts, 50, 200, true, "attack")
	}
	w.walk(141000, 150000, 50, 200, 50, 300)
	for ts := int64(151000); ts <= 200000; ts += 1000 { // still standing at the end: an open stall
		w.frame(ts, 50, 300, false, "reach pull")
	}
	d := digestOf(w).Snapshot(0)
	if len(d.Stalls) != 3 {
		t.Fatalf("want 3 stalls (rest, fight, open), got %+v", d.Stalls)
	}
	if s := d.Stalls[0]; s.Secs < 38 || s.Reason != "resting before the next pull" || s.Leg != 2 || s.Open {
		t.Fatalf("rest stall: %+v", s)
	}
	if x := d.Stalls[0].X; x != -0 && math.Abs(x) > 0.01 {
		t.Fatalf("plane x of a stall at game y 0 should be 0: %v", x)
	}
	if s := d.Stalls[1]; s.Reason != "in a fight" || s.Secs < 48 {
		t.Fatalf("fight stall: %+v", s)
	}
	if s := d.Stalls[2]; s.Reason != "waiting to pull" || !s.Open {
		t.Fatalf("open stall: %+v", s)
	}
	// a stall of 29 s does not count
	w2 := newTrace("r-2", "uld", 1000)
	for ts := int64(0); ts <= 29000; ts += 1000 {
		w2.frame(ts, 5, 5, false, "stay")
	}
	if d := digestOf(w2).Snapshot(0); len(d.Stalls) != 0 {
		t.Fatalf("29 s is no stall: %+v", d.Stalls)
	}
}

func TestDigestMarksAndEvents(t *testing.T) {
	w := newTrace("r-1", "uld", 1000)
	w.walk(0, 10000, 0, 0, 100, 0)
	w.event(5000, "order", `"reply":"err","code":"unreachable","message":"The lead bot has no walkable path to that point.","text":"cmd move -121.40 336.20 -44.10","verb":"move","via":"cmd"`)
	w.event(5500, "order", `"reply":"err","code":"tank-busy","message":"busy","text":"cmd pullback 0x1","verb":"pullback"`) // timing, not ground
	w.event(5600, "order", `"reply":"ack","text":"cmd move 1 2 3","verb":"move"`)
	w.event(6000, "order", `"reply":"err","code":"no-los","message":"no line of sight","text":"cmd pullback 0x1","verb":"pullback"`)
	w.event(7000, "unreachable", `"kind":"pull","leg":1,"what":"entry 5648: could not start moving","x":10,"y":20,"z":0`)
	w.event(8000, "stuck", `"name":"Heal","seconds":5.1,"leg":1,"x":30,"y":0,"z":0`)
	w.event(8500, "stuck", `"name":"Heal","seconds":6.1,"leg":1,"x":31,"y":0,"z":0`) // the same spot a moment later: one mark
	w.event(9000, "death", `"a":"0x2","name":"Heal","leg":1`)
	w.event(9100, "death", `"a":"0x9","name":"Gone","leg":1`) // a body the frames never showed falls back to the lead
	w.event(9200, "rest", `"leg":1,"seconds":0.00004`)        // a skipped gate: no spot
	w.event(9300, "note", `"text":"leg 1: entry 5648 stands within 22 yd; the party gathers and pulls it before walking on"`)
	w.event(9400, "note", `"text":"leg 1: the party gathers first"`) // the same place within 20 s: one spot
	w.event(2000, "pull_start", `"entry":4850,"guid":"0xf1","leg":0,"target":"entry 4850","x":50,"y":5,"z":0`)
	w.event(4000, "pull_end", `"kills":3,"leg":0,"outcome":"won","seconds":2`)
	w.event(6500, "pull_start", `"entry":4851,"guid":"0xf2","leg":1,"target":"x","x":70,"y":5,"z":0`)
	w.event(8200, "pull_end", `"kills":0,"leg":1,"outcome":"no_engage","seconds":1`)
	w.event(8300, "step", `"action":"loot_object","leg":0,"ok":true,"step":0,"text":"leg 0 (baelog): after_boss step 0: loot game object 123329 near -356 113 -43"`)
	w.event(8400, "step", `"action":"use_object","leg":1,"ok":false,"step":2,"text":"leg 1 (x): step 2: use game object 7 near 10 20 0"`)
	w.event(8500, "step", `"action":"walk","leg":0,"ok":true,"text":"leg 0: walk to 1 2 3"`) // not an object step
	w.event(1000, "leg", `"leg":0,"name":"baelog","phase":"start"`)
	w.event(5000, "leg", `"leg":0,"name":"baelog","phase":"end","result":"done"`)
	w.event(5000, "leg", `"leg":1,"name":"keystone","phase":"start"`)
	w.end(10000, "failed")
	d := digestOf(w).Snapshot(0)

	if len(d.Refusals) != 4 { // unreachable move, no-los pullback, unreachable ev, one stuck
		for _, r := range d.Refusals {
			t.Logf("%+v", r)
		}
		t.Fatalf("refusals %d", len(d.Refusals))
	}
	r0 := d.Refusals[0]
	if r0.Kind != "refused" || r0.X != -336.2 || r0.Y != 121.4 || !strings.Contains(r0.Text, "no walkable path") || r0.T != 5 { // the walk's goal in the plane
		t.Fatalf("refused: %+v", r0)
	}
	if r1 := d.Refusals[1]; r1.Kind != "refused" || !strings.HasPrefix(r1.Text, "no-los:") || r1.Y != -100 { // the lead's place (the frames come first in this file)
		t.Fatalf("pullback refusal sits at the lead: %+v", r1)
	}
	if u := d.Refusals[2]; u.Kind != "unreachable" || u.X != -20 || u.Y != -10 || u.Leg != 1 || !strings.Contains(u.Text, "entry 5648") {
		t.Fatalf("unreachable: %+v", u)
	}
	if st := d.Refusals[3]; st.Kind != "stuck" || st.Text != "Heal stuck" {
		t.Fatalf("stuck: %+v", st)
	}
	if len(d.Deaths) != 2 || d.Deaths[0].Text != "Heal died" {
		t.Fatalf("deaths %+v", d.Deaths)
	}
	// the healer's last frame put it 3 yd further north than the lead, who ended at game x 100: plane (0, -103)
	if got := d.Deaths[0]; got.X != 0 || got.Y != -103 {
		t.Fatalf("death place %+v", got)
	}
	if len(d.Rests) != 0 {
		t.Fatalf("a rest under 3 s is no rest spot: %+v", d.Rests)
	}
	if len(d.Gathers) != 1 || d.Gathers[0].Leg != 1 {
		t.Fatalf("gathers %+v", d.Gathers)
	}
	if len(d.Pulls) != 2 || d.Pulls[0].Outcome != "won" || d.Pulls[0].Kills != 3 || d.Pulls[1].Outcome != "no_engage" || d.Pulls[1].Leg != 1 {
		t.Fatalf("pulls %+v", d.Pulls)
	}
	if len(d.Objects) != 2 {
		t.Fatalf("objects %+v", d.Objects)
	}
	if o := d.Objects[0]; o.Action != "loot_object" || o.Entry != 123329 || !o.OK || !o.After || o.X != -113 || o.Y != 356 {
		t.Fatalf("loot object %+v", o)
	}
	if o := d.Objects[1]; o.OK || o.Step != 2 || o.Entry != 7 {
		t.Fatalf("failed object %+v", o)
	}
	if len(d.Legs) != 2 || d.Legs[0].Result != "done" || d.Legs[0].StartS != 1 || d.Legs[0].EndS != 5 || d.LegsDone != 1 {
		t.Fatalf("legs %+v done %d", d.Legs, d.LegsDone)
	}
	if !d.Ended || d.Reason != "failed" || d.Seconds != 10 {
		t.Fatalf("end: %+v", d)
	}
}

func TestDigestIgnoresJunkAndCaps(t *testing.T) {
	b := newDigestBuilder()
	for _, l := range []string{"", "not json", `{"v":1,"k":"frame","t":"x"`, `{"v":1,"k":"mobs","g":"0x1"}`, `{"k":"event","ev":"death","a":"zz"}`, `{"v":1,"k":"event","t":5,"ev":"pull_end","leg":3,"outcome":"won"}`} {
		b.Line([]byte(l)) // must not panic
	}
	if d := b.Snapshot(0); len(d.Paths) != 0 || len(d.Pulls) != 0 {
		t.Fatalf("junk made %+v", d)
	}
	w := newTrace("r-1", "uld", 0)
	w.walk(0, 5000, 0, 0, 10, 0)
	for i := 0; i < maxMarks+50; i++ {
		w.event(int64(i)*100, "unreachable", fmt.Sprintf(`"kind":"walk","leg":0,"what":"w%d","x":%d,"y":0,"z":0`, i, i))
	}
	d := digestOf(w).Snapshot(0)
	if len(d.Refusals) != maxMarks || len(d.Truncated) != 1 || d.Truncated[0] != "refusals" {
		t.Fatalf("cap: %d marks, truncated %v", len(d.Refusals), d.Truncated)
	}
	if !strings.Contains(d.Refusals[len(d.Refusals)-1].Text, fmt.Sprintf("w%d", maxMarks+49)) || strings.Contains(d.Refusals[0].Text, ": w0") {
		t.Fatalf("the oldest marks go first: %q .. %q", d.Refusals[0].Text, d.Refusals[len(d.Refusals)-1].Text)
	}
}

// ---- the history of a dungeon: ghost and death heat

// writeRun writes a trace of at least historyMinSize bytes under dir/folder/ta/traces.
func writeRun(t *testing.T, dir, folder string, w *traceW) string {
	t.Helper()
	p := filepath.Join(dir, folder, "ta", "traces", w.run+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(w.text()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fullRun is a run of totalS seconds through legs sections, the lead walking east in game x; every section done when finished.
func fullRun(run, plan string, start int64, totalS int64, legsDone int, reason string, deaths [][2]float64) *traceW {
	w := newTrace(run, plan, start)
	per := totalS * 1000 / int64(max(legsDone, 1))
	for l := 0; l < legsDone; l++ {
		t0 := int64(l) * per
		w.event(t0, "leg", fmt.Sprintf(`"leg":%d,"name":"s%d","phase":"start"`, l, l))
		w.walk(t0, t0+per-1000, float64(l*100+10), 0, float64(l*100+110), 0)
		w.event(t0+per-1000, "leg", fmt.Sprintf(`"leg":%d,"name":"s%d","phase":"end","result":"done"`, l, l))
	}
	for _, d := range deaths { // the healer dies at d
		w.add(`{"v":1,"k":"frame","run":%q,"t":%d,"bots":[{"g":"0x1","al":true,"x":%.1f,"y":0,"lead":true},{"g":"0x2","al":false,"x":%.1f,"y":%.1f}]}`, run, totalS*1000-500, float64(legsDone*100+10), d[0], d[1])
		w.event(totalS*1000-400, "death", `"a":"0x2","name":"Heal","leg":0`)
	}
	w.end(totalS*1000, reason)
	return w
}

func TestHistoryGhostAndHeat(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(10000, 0)
	clock := func() time.Time { return now }
	// uld: a failed run that got 2 sections in 300 s, a failed one with 3 sections in 400 s, a run of another plan
	writeRun(t, dir, "a", fullRun("r-a", "uld", 100, 300, 2, "failed", [][2]float64{{10, 10}}))
	writeRun(t, dir, "b", fullRun("r-b", "uld", 200, 400, 3, "failed", [][2]float64{{12, 11}, {500, 500}}))
	writeRun(t, dir, "c", fullRun("r-c", "zf", 300, 500, 4, "done", nil))
	h := NewHistory(clock, dir, "")

	var g GhostAnswer
	must := func(b []byte, v any) {
		t.Helper()
		reflect.ValueOf(v).Elem().SetZero() // an answer leaves out empty fields: start from nothing
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
	}
	must(h.Ghost("uld", "", 0), &g)
	if !g.Found || g.Run != "r-b" || g.Complete || g.LegsDone != 3 || g.Folder != "b" || g.Runs != 2 {
		t.Fatalf("no run is done: the furthest wins: %+v", g)
	}
	if len(g.Path) < 2 || g.Path[0][2] != 0 || len(g.Legs) != 3 {
		t.Fatalf("ghost path %+v legs %+v", g.Path, g.Legs)
	}
	must(h.Ghost("uld", "r-b", 0), &g) // not this run itself
	if g.Run != "r-a" || g.Runs != 1 {
		t.Fatalf("exclude: %+v", g)
	}
	must(h.Ghost("zf", "", 0), &g)
	if !g.Found || !g.Complete || g.Run != "r-c" {
		t.Fatalf("zf: %+v", g)
	}
	must(h.Ghost("nothing", "", 0), &g)
	if g.Found || g.Why == "" {
		t.Fatalf("nothing: %+v", g)
	}

	// a quicker complete run beats the partial ones, and a slower complete one does not beat it
	writeRun(t, dir, "d", fullRun("r-d", "uld", 400, 900, 4, "done", nil))
	writeRun(t, dir, "e", fullRun("r-e", "uld", 500, 1200, 4, "done", nil))
	must(h.Ghost("uld", "", 0), &g)
	if g.Run != "r-b" { // the folders are listed again only after historyRescan
		t.Fatalf("cached: %+v", g)
	}
	now = now.Add(historyRescan + time.Second)
	must(h.Ghost("uld", "", 0), &g)
	if g.Run != "r-d" || !g.Complete || g.Seconds != 900 {
		t.Fatalf("fastest clear: %+v", g)
	}

	// the plan has 6 sections: the best run did 4, so it is no clear; with 4 it is one. A run that started at a later section is no clear either.
	must(h.Ghost("uld", "", 6), &g)
	if g.Complete || g.PlanLegs != 6 || g.LegsDone != 4 {
		t.Fatalf("plan with 6 sections: %+v", g)
	}
	must(h.Ghost("uld", "", 4), &g)
	if !g.Complete {
		t.Fatalf("plan with 4 sections: %+v", g)
	}

	var hm HeatAnswer
	must(h.Heat("uld"), &hm)
	if hm.Runs != 4 || hm.Deaths != 3 || len(hm.Points) != 3 {
		t.Fatalf("heat %+v", hm)
	}
	must(h.Heat("nothing"), &hm)
	if hm.Why == "" || hm.Runs != 0 || hm.Points == nil {
		t.Fatalf("empty heat %+v", hm)
	}
}

func TestHistoryGhostPartialRunIsNoClear(t *testing.T) {
	dir := t.TempDir()
	w := fullRun("r-late", "zf", 100, 200, 1, "done", nil)
	w.lines[0] = strings.Replace(w.lines[0], `"start_unix":`, `"from_leg":7,"start_unix":`, 1) // a run that started at section 7
	p := filepath.Join(dir, "r-late.jsonl")                                                    // straight under the root: no capture folder
	os.WriteFile(p, []byte(w.text()), 0o644)
	h := NewHistory(nil, dir)
	var g GhostAnswer
	if err := json.Unmarshal(h.Ghost("zf", "", 0), &g); err != nil {
		t.Fatal(err)
	}
	if !g.Found || g.Complete || g.Folder != "traces" || g.LegsDone != 1 {
		t.Fatalf("a finished run that began at section 7 is not a clear: %+v", g)
	}
}

func TestHistoryHeatBoundedToLatestRuns(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < HeatRuns+5; i++ {
		writeRun(t, dir, fmt.Sprintf("f%02d", i), fullRun(fmt.Sprintf("r-%02d", i), "uld", int64(1000+i), 100, 1, "failed", [][2]float64{{float64(i), 0}}))
	}
	h := NewHistory(nil, dir)
	var hm HeatAnswer
	if err := json.Unmarshal(h.Heat("uld"), &hm); err != nil {
		t.Fatal(err)
	}
	if hm.Runs != HeatRuns || hm.Deaths != HeatRuns {
		t.Fatalf("the latest %d runs only: %+v", HeatRuns, hm)
	}
	for _, p := range hm.Points { // the five oldest runs (deaths at game x 0..4) are left out: plane y is -x
		if p[1] > -4.5 {
			t.Fatalf("an old run's death is in: %v", p)
		}
	}
}

func TestHistoryIgnoresOtherFilesAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	w := fullRun("r-a", "uld", 100, 300, 2, "done", nil)
	writeRun(t, dir, "a", w)
	writeRun(t, dir, "copy", w) // the same run copied into another folder
	os.WriteFile(filepath.Join(dir, "a", "ta", "traces", "r-a.summary.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(dir, "a", "botwatch.jsonl"), []byte(strings.Repeat("x", 9000)), 0o644)
	os.WriteFile(filepath.Join(dir, "a", "r-tiny.jsonl"), []byte(w.lines[0]+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a", "r-bad.jsonl"), []byte(strings.Repeat("not json\n", 1000)), 0o644)
	h := NewHistory(nil, dir, filepath.Join(dir, "missing"))
	var g GhostAnswer
	if err := json.Unmarshal(h.Ghost("uld", "", 0), &g); err != nil {
		t.Fatal(err)
	}
	if !g.Found || g.Runs != 1 {
		t.Fatalf("%+v", g)
	}
}

// ---- the live follow of a growing trace

func TestLiveFollowsGrowingTrace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r-live.jsonl")
	w := newTrace("r-live", "uld", 1000)
	w.walk(0, 20000, 0, 0, 40, 0)
	full := w.text()
	cut := len(full) - 30 // the writer is in the middle of a line
	if err := os.WriteFile(path, []byte(full[:cut]), 0o644); err != nil {
		t.Fatal(err)
	}
	l := NewLive()
	now := time.Now()
	if _, ok := l.JSON(path, 0); ok {
		t.Fatal("a trace nobody advanced has no digest")
	}
	l.Advance(path, now)
	b, ok := l.JSON(path, 0)
	if !ok {
		t.Fatal("no digest after advance")
	}
	var d RunDigest
	json.Unmarshal(b, &d)
	if d.Run != "r-live" || len(d.Paths) != 2 || d.Seconds != 19 {
		t.Fatalf("first read (the half line is left for later): %+v", d)
	}
	// the writer finishes the line and adds more
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(full[cut:])
	f.WriteString(`{"v":1,"k":"event","run":"r-live","t":21000,"ev":"death","a":"0x2","name":"Heal","leg":0}` + "\n")
	f.Close()
	l.Advance(path, now.Add(time.Second))
	b2, _ := l.JSON(path, 0)
	json.Unmarshal(b2, &d)
	if d.Seconds != 21 || len(d.Deaths) != 1 {
		t.Fatalf("second read: %+v", d)
	}
	// nothing new: the cached answer is the same bytes
	l.Advance(path, now.Add(2*time.Second))
	b3, _ := l.JSON(path, 0)
	if string(b3) != string(b2) {
		t.Fatal("answer changed without new lines")
	}
	// a file that was replaced by a shorter one starts over
	os.WriteFile(path, []byte(newTrace("r-new", "uld", 5000).text()), 0o644)
	l.Advance(path, now.Add(3*time.Second))
	b4, _ := l.JSON(path, 0)
	json.Unmarshal(b4, &d)
	if d.Run != "r-new" || len(d.Deaths) != 0 {
		t.Fatalf("restart: %+v", d)
	}
	// a trace nobody has asked for in liveIdle is dropped
	l.Advance(filepath.Join(dir, "other.jsonl"), now.Add(liveIdle+time.Minute))
	if _, ok := l.JSON(path, 0); ok {
		t.Fatal("an idle file should be dropped")
	}
	// out-of-range minutes are bounded, a missing file is no panic
	l.Advance(filepath.Join(dir, "gone.jsonl"), now)
}

// ---- the routes

func TestPathRoutes(t *testing.T) {
	tests := t.TempDir()
	writeRun(t, tests, "a", fullRun("r-a", "uld", 100, 300, 2, "done", [][2]float64{{10, 10}}))
	live := writeRun(t, tests, "live", newTraceWithWalk("r-live", "uld", 50000))
	p := NewPoller(Options{TestsDir: tests, ConfigDir: t.TempDir()})
	p.livePath["8765"] = live
	p.live.Advance(live, time.Now())
	h := Handler(p, tests, nil)
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "127.0.0.1:8790"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/api/live/8765?min=2")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"run":"r-live"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("live: %d %s", w.Code, w.Body.String())
	}
	if get("/api/live/9999").Code != 404 || get("/api/live/abc").Code != 404 || get("/api/live/..%2f8765").Code != 404 {
		t.Fatal("unknown or bad agent ids are 404")
	}
	w = get("/api/ghost/uld?exclude=r-live")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"run":"r-a"`) {
		t.Fatalf("ghost: %d %s", w.Code, w.Body.String())
	}
	if get("/api/ghost/Bad%20Plan").Code != 404 || get("/api/ghost/uld?exclude=a%2Fb").Code != 404 || get("/api/heat/Bad.Plan").Code != 404 {
		t.Fatal("bad plan names and run ids are 404")
	}
	w = get("/api/heat/uld")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"deaths":1`) {
		t.Fatalf("heat: %d %s", w.Code, w.Body.String())
	}
	// read-only like the rest, and the host guard covers the new routes
	r := httptest.NewRequest(http.MethodPost, "/api/ghost/uld", nil)
	r.Host = "127.0.0.1:8790"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/heat/uld", nil)
	r.Host = "evil.example"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host: %d", rec.Code)
	}
	// the live answer never carries a secret field name
	for _, k := range []string{"token", "password", "account"} {
		if strings.Contains(strings.ToLower(get("/api/live/8765").Body.String()), k) {
			t.Fatalf("%q in the live answer", k)
		}
	}
}

func newTraceWithWalk(run, plan string, start int64) *traceW {
	w := newTrace(run, plan, start)
	w.walk(0, 4000000/1000, 0, 0, 30, 0)
	for i := 0; i < 80; i++ { // enough bytes to be an indexed run too
		w.event(int64(i), "note", `"text":"filler filler filler filler filler filler filler filler"`)
	}
	return w
}

// ---- the plan's drawing carries what the layers need

func TestGeometryPacksObjectsPatrols(t *testing.T) {
	p, err := dungeon.Parse("g.json", []byte(`{"version":1,"name":"g","map":209,"entrance":{"x":0,"y":0,"z":0},
 "notes":["Revelosh (6910) is the boss."],
 "legs":[{"name":"hall","to":{"x":100,"y":0,"z":0},
  "via":[{"x":50,"y":0,"z":0}],
  "pull_groups":[
   {"entries":[4850,4851],"positions":[{"x":20,"y":10,"z":0},{"x":25,"y":15,"z":0}],"radius":12,
    "patrol":{"entries":[4999],"path":[{"x":10,"y":30,"z":0},{"x":60,"y":30,"z":0},{"x":60,"y":60,"z":0}],"period_s":90}},
   {"entries":[7320],"positions":[{"x":60,"y":-10,"z":0}],"ambush":true}],
  "steps":[{"use_object":{"entry":1234,"near":{"x":40,"y":5,"z":0}}},{"wait_for":{"gameobject":{"entry":1235,"near":{"x":80,"y":5,"z":0},"state":"active"}}}],
  "boss":{"entry":6910,"position":{"x":95,"y":0,"z":0}},
  "after_boss":[{"loot_object":{"entry":9999,"near":{"x":96,"y":2,"z":0}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	g := BuildGeometry(p)
	l := g.Legs[0]
	if len(l.Packs) != 2 {
		t.Fatalf("packs %+v", l.Packs)
	}
	a, b := l.Packs[0], l.Packs[1]
	// the plane: x -> -y, y -> -x
	if a.Index != 0 || a.X != -10 || a.Y != -20 || a.R != 12 || a.N != 2 || len(a.Points) != 2 || a.Ambush || a.Label == "" {
		t.Fatalf("pack 0 %+v", a)
	}
	if a.Patrol == nil || len(a.Patrol.Path) != 3 || a.Patrol.PeriodS != 90 || len(a.Patrol.Entries) != 1 || a.Patrol.Path[1].X != -30 || a.Patrol.Path[1].Y != -60 {
		t.Fatalf("patrol %+v", a.Patrol)
	}
	if b.Index != 1 || !b.Ambush || b.R != defaultPackRadius || b.Patrol != nil {
		t.Fatalf("pack 1 %+v", b)
	}
	if l.BossAt == nil || l.BossAt.X != 0 || l.BossAt.Y != -95 || l.Boss != 6910 {
		t.Fatalf("boss %+v", l.BossAt)
	}
	if len(l.Objects) != 3 {
		t.Fatalf("objects %+v", l.Objects)
	}
	if o := l.Objects[0]; o.Kind != "use" || o.Entry != 1234 || o.Step != 0 || o.After || o.X != -5 || o.Y != -40 {
		t.Fatalf("use %+v", o)
	}
	if o := l.Objects[1]; o.Kind != "door" || o.Entry != 1235 || o.Step != 1 {
		t.Fatalf("door %+v", o)
	}
	if o := l.Objects[2]; o.Kind != "loot" || !o.After || o.Step != 0 {
		t.Fatalf("loot %+v", o)
	}
	b2, _ := json.Marshal(g)
	if !strings.Contains(string(b2), `"packs"`) || !strings.Contains(string(b2), `"boss_at"`) {
		t.Fatal("the drawing's JSON lacks packs or boss_at")
	}
	if strings.Count(g.SVG, "mk obj") != 3 {
		t.Fatalf("the static drawing lost its objects: %d", strings.Count(g.SVG, "mk obj"))
	}
}
