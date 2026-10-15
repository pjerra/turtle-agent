package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// flaggedPlan has an area trigger with a stand point, a gossip step with a stand, a use_object with a stand, a needs_walk end, a check_skip hop and a
// pull group, so that every mark of the plan view is drawn once.
const flaggedPlan = `{"version":1,"name":"flagged","map":209,"entrance":{"x":1000,"y":800,"z":9},
 "legs":[{"name":"gate","to":{"x":1100,"y":900,"z":9},"needs_walk":true,
   "via":[{"x":1010,"y":805,"z":9},{"x":1030,"y":820,"z":9,"check_skip":"scorpid pit: the mesh has no link"}],
   "pull_groups":[{"entries":[7001],"positions":[{"x":1150,"y":900,"z":9}],"radius":10,"needs_walk":true}],
   "steps":[
    {"area_trigger":{"id":962,"center":{"x":1120,"y":910,"z":9},"radius":12,"stand":{"x":1115,"y":908,"z":9},"expect":"unit 7271 hostile"}},
    {"gossip":{"npc":777,"near":{"x":1130,"y":920,"z":9},"option":1,"stand":{"x":1128,"y":918,"z":9}}},
    {"use_object":{"entry":555,"near":{"x":1050,"y":850,"z":9},"stand":{"x":1048,"y":848,"z":9}}}],
   "boss":{"entry":6001,"position":{"x":1200,"y":900,"z":9},"needs_walk":true}}]}`

func TestGeometryDrawsLooksMarksAndFlags(t *testing.T) {
	p, err := dungeon.Parse("flagged.json", []byte(flaggedPlan))
	if err != nil {
		t.Fatal(err)
	}
	g := BuildGeometry(p)
	l := g.Legs[0]
	if len(l.Looks) != 1 || l.Looks[0].Gather == nil {
		t.Fatalf("looks: %+v", l.Looks)
	}
	// the look point lies LookStandoff short of the pack on the line from the leg's end (the lead comes from `to` for the first pack)
	px, py := Project(1150, 900)
	lx, ly := l.Looks[0].X, l.Looks[0].Y
	if d := math.Hypot(lx-px, ly-py); math.Abs(d-float64(dungeon.LookStandoff)) > 0.5 {
		t.Errorf("look point %v yd from the pack, want about %v", d, dungeon.LookStandoff)
	}
	kinds := map[string]int{}
	for _, m := range l.Marks {
		kinds[m.Kind]++
	}
	if kinds["trigger"] != 1 || kinds["gossip"] != 1 || kinds["stand"] != 3 {
		t.Errorf("marks: %v", kinds)
	}
	for _, m := range l.Marks {
		if m.Kind == "trigger" && m.R != 12 {
			t.Errorf("trigger radius %v", m.R)
		}
		if m.Kind == "stand" && m.Link == nil {
			t.Errorf("a stand point names what it is for: %+v", m)
		}
	}
	for _, want := range []string{`class="look"`, `class="mk lookpt"`, `class="mk gatherpt"`, `class="trg"`, `class="mk trgmk"`, `class="mk gossip"`, `class="mk stand"`, `class="standline"`,
		`wp via cskip`, `scorpid pit: the mesh has no link`, `wp stage nw`, `mk pin nw`, `mk boss nw`, `needs_walk`} {
		if !strings.Contains(g.SVG, want) {
			t.Errorf("svg lacks %q", want)
		}
	}
	// every mark lies in the drawn box
	vb := g.ViewBox
	for _, m := range l.Marks {
		if m.X-m.R < vb[0] || m.X+m.R > vb[0]+vb[2] || m.Y-m.R < vb[1] || m.Y+m.R > vb[1]+vb[3] {
			t.Errorf("mark %+v outside %v", m, vb)
		}
	}
	// the flags are on the stops too
	var cs, nw int
	for _, st := range l.Stops {
		if st.CheckSkip != "" {
			cs++
		}
		if st.NeedsWalk {
			nw++
		}
	}
	if cs != 1 || nw != 3 {
		t.Errorf("flags on stops: check_skip %d, needs_walk %d (want 1 and 3: the end, the pack, the boss)", cs, nw)
	}
}

func TestGeometryOfAPlanWithoutMarksHasNone(t *testing.T) {
	g := BuildGeometry(tinyPlan(t))
	for _, l := range g.Legs {
		if len(l.Marks) != 0 {
			t.Errorf("tiny has no trigger, gossip or stand point: %+v", l.Marks)
		}
	}
	for _, bad := range []string{`class="trg"`, `class="mk gossip"`, ` nw"`, ` cskip"`} {
		if strings.Contains(g.SVG, bad) {
			t.Errorf("svg has %q", bad)
		}
	}
	if len(g.Legs[0].Looks) != 2 {
		t.Errorf("two packs, two look points: %+v", g.Legs[0].Looks)
	}
}

func TestDiscoverAgentsReadsOnlyRPCAndDungeonsDir(t *testing.T) {
	dir := t.TempDir()
	home, _ := os.UserHomeDir()
	must(t, os.WriteFile(filepath.Join(dir, "settings-1.json"), []byte(`{"rpc":"127.0.0.1:8765","dungeons_dir":"~/ta-x/dungeons","accounts":{"BOB":"hunter2"},"mmaps_dir":"/m"}`), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "settings-2.json"), []byte(`{"rpc":"127.0.0.1:8766"}`), 0o600))
	as, err := DiscoverAgents(dir, "auto")
	if err != nil || len(as) != 2 {
		t.Fatalf("%+v %v", as, err)
	}
	if as[0].DungeonsDir != filepath.Join(home, "ta-x", "dungeons") || as[1].DungeonsDir != "" {
		t.Errorf("dungeons_dir: %q and %q", as[0].DungeonsDir, as[1].DungeonsDir)
	}
	if s := fmt.Sprintf("%+v", as); strings.Contains(s, "hunter2") || strings.Contains(s, "/m") {
		t.Errorf("another field of the settings reached the agent record: %s", s)
	}
}

// copyFixture makes a dashboard folder and a slot folder that both hold tiny.json, the slot's with the given content.
func copyFixture(t *testing.T, slotBody string) (*planCache, Agent) {
	t.Helper()
	dash, slot := t.TempDir(), t.TempDir()
	tiny, _ := os.ReadFile("testdata/tiny.json")
	must(t, os.WriteFile(filepath.Join(dash, "tiny.json"), tiny, 0o644))
	if slotBody != "" {
		must(t, os.WriteFile(filepath.Join(slot, "tiny.json"), []byte(slotBody), 0o644))
	}
	return newPlanCache([]string{dash}), Agent{ID: "8765", Addr: "127.0.0.1:8765", DungeonsDir: slot}
}

func TestPlanCopyNamesTheFileDrawnAndTheFileRead(t *testing.T) {
	tiny, _ := os.ReadFile("testdata/tiny.json")
	// the slot holds the same bytes: the run's copy is drawn and said to be the same
	c, a := copyFixture(t, string(tiny))
	cp, plan, geo, errText := c.copyFor(a, "tiny", reportedPlan{})
	if plan == nil || errText != "" || cp.Run == nil || !cp.Same || cp.Drawn.Role != "slot" || geo.Source == nil || geo.Source.Role != "slot" {
		t.Fatalf("same: %+v %v", cp, errText)
	}
	if !strings.Contains(cp.Text, a.DungeonsDir) || !strings.Contains(cp.Text, "same file content") {
		t.Errorf("text: %s", cp.Text)
	}
	// the slot holds an older route: drawn from the slot, flagged as different, both shas named
	older := strings.Replace(string(tiny), `"x":1010,"y":805`, `"x":1011,"y":806`, 1)
	c, a = copyFixture(t, older)
	cp, plan, _, _ = c.copyFor(a, "tiny", reportedPlan{})
	if plan == nil || cp.Same || cp.Drawn.Role != "slot" || cp.Run.SHA256 == cp.Dash.SHA256 || !strings.Contains(cp.Text, "differs") || !strings.Contains(cp.Text, cp.Dash.Short()) {
		t.Fatalf("differs: %+v", cp)
	}
	if p := plan.Legs[0].Via[0]; p.X != 1011 {
		t.Errorf("the plan drawn must be the slot's: via %+v", p)
	}
	// the slot's folder has no such file: the dashboard's copy is drawn and the text says the run reads another file
	c, a = copyFixture(t, "")
	cp, plan, _, _ = c.copyFor(a, "tiny", reportedPlan{})
	if plan == nil || cp.Run != nil || cp.Drawn.Role != "dashboard" || !strings.Contains(cp.Text, "has no tiny.json") {
		t.Fatalf("missing: %+v", cp)
	}
	// the slot's file does not load
	c, a = copyFixture(t, "{not json")
	cp, plan, _, _ = c.copyFor(a, "tiny", reportedPlan{})
	if plan == nil || cp.Run != nil || !strings.Contains(cp.Text, "does not load") {
		t.Fatalf("broken: %+v", cp)
	}
	// the settings name no dungeons_dir
	c, a = copyFixture(t, "")
	a.DungeonsDir = ""
	cp, _, _, _ = c.copyFor(a, "tiny", reportedPlan{})
	if cp.Run != nil || !strings.Contains(cp.Text, "no dungeons_dir") {
		t.Fatalf("no dir: %+v", cp)
	}
	// a plan name that is a path never reaches the disk
	if _, plan, _, e := c.copyFor(a, "../etc/passwd", reportedPlan{}); plan != nil || e == "" {
		t.Error("a path is not a plan name")
	}
}

func TestPlanListFirstFolderWinsAndBuildFillsTheRest(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	tiny, _ := os.ReadFile("testdata/tiny.json")
	must(t, os.WriteFile(filepath.Join(d1, "tiny.json"), tiny, 0o644))
	must(t, os.WriteFile(filepath.Join(d2, "tiny.json"), []byte("{}"), 0o644))
	must(t, os.WriteFile(filepath.Join(d2, "Not A Name.json"), tiny, 0o644))
	c := newPlanCache([]string{d1, d2})
	by := map[string]PlanInfo{}
	for _, pi := range c.list() {
		by[pi.Name] = pi
	}
	if by["tiny"].Source.Dir != d1 || by["tiny"].Legs != 1 || by["tiny"].Map != 209 || by["tiny"].Source.SHA256 == "" {
		t.Errorf("tiny: %+v", by["tiny"])
	}
	if by["uldaman"].Source.Role != "build" {
		t.Errorf("a plan only the build has: %+v", by["uldaman"])
	}
	if _, ok := by["Not A Name"]; ok {
		t.Error("a file that is not a plan name is not listed")
	}
}

func TestPlanEndpointsServeTheCopyOfTheAskedAgent(t *testing.T) {
	c, a := copyFixture(t, strings.Replace(mustRead(t, "testdata/tiny.json"), `"x":1010,"y":805`, `"x":1011,"y":806`, 1))
	p := NewPoller(Options{PlansDirs: c.dirs, ConfigDir: t.TempDir()})
	p.known = []Agent{a}
	h := Handler(p, "", nil)
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1:8790"
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := get("/api/plan/tiny")
	var dash, slot Geometry
	if code != 200 || json.Unmarshal([]byte(body), &dash) != nil || dash.Source == nil || dash.Source.Role != "dashboard" {
		t.Fatalf("dashboard copy: %d %.200s", code, body)
	}
	code, body = get("/api/plan/tiny?agent=8765")
	if code != 200 || json.Unmarshal([]byte(body), &slot) != nil || slot.Source == nil || slot.Source.Role != "slot" || slot.Source.SHA256 == dash.Source.SHA256 {
		t.Fatalf("slot copy: %d %.200s", code, body)
	}
	if code, _ = get("/api/plan/tiny?agent=1"); code != 200 { // an unknown agent gets the dashboard's copy
		t.Errorf("unknown agent: %d", code)
	}
	code, body = get("/api/plans")
	if code != 200 || !strings.Contains(body, `"name":"tiny"`) {
		t.Errorf("plans: %d %.200s", code, body)
	}
	code, body = get("/api/plan-copies/tiny")
	var rows []CopyRow
	if code != 200 || json.Unmarshal([]byte(body), &rows) != nil || len(rows) != 1 || rows[0].Agent != "8765" || rows[0].Same || rows[0].Run == nil {
		t.Fatalf("copies: %d %s", code, body)
	}
	if code, _ = get("/api/plan-copies/..%2Fetc"); code != 404 {
		t.Errorf("a path is not a plan name: %d", code)
	}
	// the view stays read-only
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/plans", nil)
	req.Host = "127.0.0.1:8790"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A run that reports the file it was loaded from is drawn from that file, wherever the settings point, and the card says when the file moved on.
func TestPlanCopyFollowsTheFileTheRunReports(t *testing.T) {
	tiny := mustRead(t, "testdata/tiny.json")
	dash := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dash, "tiny.json"), []byte(tiny), 0o644))
	runDir := t.TempDir() // not the dungeons_dir of the settings: the run was started with another folder
	older := strings.Replace(tiny, `"x":1010,"y":805`, `"x":1011,"y":806`, 1)
	file := filepath.Join(runDir, "tiny.json")
	must(t, os.WriteFile(file, []byte(older), 0o644))
	c := newPlanCache([]string{dash})
	a := Agent{ID: "8765", DungeonsDir: t.TempDir()}
	sum := sha256.Sum256([]byte(older))
	rp := reportedPlan{Plan: "tiny", File: file, SHA: hex.EncodeToString(sum[:])}
	cp, plan, _, _ := c.copyFor(a, "tiny", rp)
	if plan == nil || cp.Run == nil || !cp.Reported || cp.Run.Path != file || cp.Changed || plan.Legs[0].Via[0].X != 1011 || !strings.Contains(cp.Text, "reports it was loaded from") {
		t.Fatalf("reported: %+v", cp)
	}
	// the file is edited after the run read it: still drawn, flagged as changed
	must(t, os.WriteFile(file, []byte(strings.Replace(older, `"x":1011,"y":806`, `"x":1012,"y":807`, 1)), 0o644))
	cp, plan, _, _ = c.copyFor(a, "tiny", rp)
	if plan == nil || !cp.Changed || plan.Legs[0].Via[0].X != 1012 || !strings.Contains(cp.Text, "changed since the run last read it") {
		t.Fatalf("changed: %+v", cp)
	}
	// a report for another plan, a relative path, or another file name is not followed: the settings' folder is used instead
	for _, bad := range []reportedPlan{{Plan: "other", File: file}, {Plan: "tiny", File: "tiny.json"}, {Plan: "tiny", File: filepath.Join(runDir, "evil.json")}} {
		if cp, _, _, _ := c.copyFor(a, "tiny", bad); cp.Reported {
			t.Errorf("%+v must not be followed", bad)
		}
	}
}
