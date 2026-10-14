package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

var t0 = time.Date(2026, 10, 14, 16, 40, 0, 0, time.UTC)

const statusJSON = `{"state":"in_world","character":"Zfmagea","guid":"0x000000000000028A","level":46,"map":209,"x":1200.5,"y":840.25,"z":9,
 "health":1003,"max_health":1003,"unknown_opcodes":{"SMSG_AUTH_RESPONSE":1},"password":"hunter2hunter2","accounts":{"BOB":"pw"}}`

func dungeonJSON(current string, progressAgo time.Duration) string {
	return fmt.Sprintf(`{"running":true,"state":"running","last_progress_at":%q,"stalled_for_s":0,"stall_level":"none",
 "report":{"plan":"tiny","map":209,"state":"running","started":%q,"seconds":300,"current":%q,"wipes":1,"party_downs":2,"leader_deaths":0,
  "deaths":[{"name":"Puvur","leg":0,"at":%q}],
  "legs":[{"leg":0,"name":"one","result":"running","pulls":7}],
  "rest_share":12.5,"rest_gates":[{"result":"rested"},{"result":"rested"}],"run_id":"r-1","trace_file":"traces/r-1.jsonl",
  "watchdog":{"last_progress_at":%q,"last_progress":"the lead moved","waiting":"walk leg 0 (one): pull group 2 of 3; last order cmd move 1 2 3 -> ack"},
  "remote":{"mode":"remote","lead_bot":"Forthur","bots":["Puvur","Wortolan"],"tank":"Forthur","gm_commands":1,"run_gm_assists":3,"agent_walks":4,
   "threat_overrides":5,"gathers":14,"walk_stops":2,"orders_sent":98,"orders_refused":16,"orders_unanswered":1,"unreachable":3,
   "tune_changes":[{"key":"threat_wait_s","old":6,"new":12,"source":"file","at":%q,"leg":0,"boundary":"run_start"}],
   "plan_reloads":[{"ok":true,"at":%q,"leg":0,"boundary":"pull_group_start","file":"/x/y/tiny.json","ops":[{"op":"add"},{"op":"remove"}]}]},
  "ready":{"bots":[{"name":"Forthur","class":"warrior","role":"tank"},{"name":"Puvur","class":"priest","role":"healer"},{"name":"Wortolan","class":"mage","role":"dps"}]}}}`,
		t0.Add(-progressAgo).Format(time.RFC3339Nano), t0.Add(-300*time.Second).Format(time.RFC3339Nano), current, t0.Format(time.RFC3339Nano),
		t0.Add(-progressAgo).Format(time.RFC3339Nano), t0.Add(-200*time.Second).Format(time.RFC3339Nano), t0.Add(-100*time.Second).Format(time.RFC3339Nano))
}

const viewJSON = `{"have":true,"n":20,"age_ms":700,"stats":{"frames":20},"frame":{"seq":9,"lead":"0x00000000000002DC",
 "party":[
  {"guid":"0x00000000000002DC","name":"Forthur","hp":1000,"max_hp":2000,"power":50,"max_power":100,"power_type":1,"x":1000,"y":800,"map":209,"target":"0xF130001C4E00AC91","dead":false,"state":"c","action":"tank"},
  {"guid":"0x00000000000002DD","name":"Puvur","hp":300,"max_hp":1000,"power":500,"max_power":1000,"power_type":0,"x":1030,"y":840,"map":209,"target":"0x00000000000002DC","dead":false,"state":"n"},
  {"guid":"0x00000000000002DE","name":"Wortolan","hp":0,"max_hp":900,"power":0,"max_power":900,"power_type":0,"x":1000,"y":800,"map":209,"dead":true,"state":"d"}],
 "hostiles":[
  {"guid":"0xF130001C4E00AC91","entry":7001,"x":1002,"y":805,"hp_pct":80,"flags":"-","reaction":"h","kind":"-","target":"0x00000000000002DC"},
  {"guid":"0xF130001C4E00AC92","entry":9999,"x":1100,"y":900,"hp_pct":100,"flags":"c","reaction":"n","kind":"-"},
  {"guid":"0xF130001C4E00AC93","entry":8001,"x":1010,"y":805,"hp_pct":100,"flags":"-","reaction":"h","kind":"-"}]}}`

// tinyPlan has one leg with a pull group (entry 7001), a second pull group (entry 8001) and a boss.
func tinyPlan(t *testing.T) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Parse("tiny.json", []byte(`{"version":1,"name":"tiny","map":209,"entrance":{"x":1000,"y":800,"z":9},
 "legs":[{"name":"one","to":{"x":1100,"y":900,"z":9},
   "via":[{"x":1010,"y":805,"z":9},{"x":1012,"y":810,"z":9,"direct":true}],
   "pull_groups":[{"entries":[7001],"positions":[{"x":1002,"y":805,"z":9}],"radius":10},{"entries":[8001],"positions":[{"x":1010,"y":805,"z":9}],"radius":5}],
   "steps":[{"use_object":{"entry":555,"near":{"x":1050,"y":850,"z":9}}}],
   "boss":{"entry":6001,"position":{"x":1100,"y":900,"z":9}}},
  {"name":"two <b>","to":{"x":1200,"y":900,"z":9},"pull_groups":[{"entries":[7001],"positions":[{"x":1150,"y":900,"z":9}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildCardFromStatus(t *testing.T) {
	in := Inputs{Agent: Agent{ID: "8768", Addr: "127.0.0.1:8768"}, Now: t0, Status: json.RawMessage(statusJSON),
		Dungeon: json.RawMessage(dungeonJSON("leg 0 (one): pull group 2 of 3", 150*time.Second)), View: json.RawMessage(viewJSON),
		Plan: tinyPlan(t), Folder: "zf-hires-2", Links: []Link{{Name: "trace", Path: "zf-hires-2/traces/r-1.jsonl"}},
		Trace: &TraceSnapshot{SHA: "c7775d5b9baf", Orders: []Order{{Text: "cmd move 1 2 3", Reply: "ACK"}, {Text: "cmd attack 0x1", Reply: "ERR", Code: "invalid-target", Message: "No unit"}}}}
	c := BuildCard(in)
	if !c.Online || c.Character != "Zfmagea" || c.Level != 46 || c.Map != 209 {
		t.Fatalf("identity: %+v", c)
	}
	r := c.Run
	if r.State != "running" || r.Phase != "walk" || r.Leg != 0 || r.LegName != "one" || r.Group != 2 || r.Groups != 3 || r.Minutes != 5 || r.Mode != "remote" {
		t.Fatalf("run: %+v", r)
	}
	if c.Label.Plan != "tiny" || c.Label.Folder != "zf-hires-2" || c.SHA != "c7775d5b9baf" {
		t.Fatalf("label/sha: %+v %q", c.Label, c.SHA)
	}
	n := c.Counters
	if n.Deaths != 1 || n.Wipes != 1 || n.PartyDowns != 2 || n.ThreatOverrides != 5 || n.RestPct != 12.5 || n.RestGates != 2 || n.Gathers != 14 ||
		n.WalkStops != 2 || n.GMCommands != 1 || n.GMAssists != 3 || n.OrdersSent != 98 || n.OrdersRefused != 16 || n.Pulls != 7 {
		t.Fatalf("counters: %+v", n)
	}
	if c.Stall.Level != "yellow" || c.Stall.Seconds != 150 {
		t.Fatalf("stall: %+v", c.Stall)
	}
	if len(c.Orders) != 2 || c.LastRefusal != "invalid-target No unit" {
		t.Fatalf("orders: %+v %q", c.Orders, c.LastRefusal)
	}
	if len(c.Changes) != 2 || c.Changes[0].Kind != "tune_change" || c.Changes[1].Kind != "plan_reload" ||
		!strings.Contains(c.Changes[0].Text, "threat_wait_s 6 -> 12") || !strings.Contains(c.Changes[1].Text, "2 change(s)") || !strings.Contains(c.Changes[1].Text, "tiny.json") ||
		strings.Contains(c.Changes[1].Text, "/x/y") {
		t.Fatalf("changes: %+v", c.Changes)
	}
	// bots: lead first, roles from the ready block, distance to the tank in yards, target names
	if len(c.Bots) != 3 || c.Bots[0].Name != "Forthur" || !c.Bots[0].Lead {
		t.Fatalf("bots: %+v", c.Bots)
	}
	tank, healer, dead := c.Bots[0], c.Bots[1], c.Bots[2]
	if tank.Class != "warrior" || tank.Role != "tank" || tank.HPPct != 50 || tank.PowerPct != 50 || tank.PowerType != "rage" || tank.Target != "entry 7001" {
		t.Fatalf("tank: %+v", tank)
	}
	if healer.Role != "healer" || healer.HPPct != 30 || healer.PowerType != "mana" || healer.Target != "Forthur" || healer.DistTank == nil || *healer.DistTank != 50 {
		t.Fatalf("healer: %+v dist=%v", healer, healer.DistTank)
	}
	if dead.Alive || !dead.Known {
		t.Fatalf("dead bot: %+v", dead)
	}
	// mobs: reaction n is neutral, group 1 of leg 0 is behind group 2 (cleared), group 2 is the current one
	if len(c.Mobs) != 3 {
		t.Fatalf("mobs: %+v", c.Mobs)
	}
	m0, m1, m2 := c.Mobs[0], c.Mobs[1], c.Mobs[2]
	if m0.Kind != "hostile" || m0.Leg != 0 || m0.Group != 0 || !m0.Cleared || !m0.Engaged || m0.Target != "Forthur" {
		t.Fatalf("mob 0: %+v", m0)
	}
	if m1.Kind != "neutral" || !m1.CC || m1.Group != -1 || m1.Cleared {
		t.Fatalf("mob 1: %+v", m1)
	}
	if m2.Kind != "hostile" || m2.Group != 1 || m2.Cleared {
		t.Fatalf("mob 2: %+v", m2)
	}
	if !c.Feed.Have || c.Feed.Stale || c.Feed.AgeMS != 700 {
		t.Fatalf("feed: %+v", c.Feed)
	}
	// the commander stands on the plan's map: a dot at the projected position (north up, west left)
	if c.Commander == nil || c.Commander.X != -840.25 || c.Commander.Y != -1200.5 {
		t.Fatalf("commander dot: %+v", c.Commander)
	}
}

func TestCardStallLevels(t *testing.T) {
	for _, tc := range []struct {
		ago   time.Duration
		level string
	}{{10 * time.Second, "none"}, {119 * time.Second, "none"}, {120 * time.Second, "yellow"}, {299 * time.Second, "yellow"}, {300 * time.Second, "red"}, {20 * time.Minute, "red"}} {
		c := BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(dungeonJSON("leg 0 (one): walk to 1 2 3", tc.ago))})
		if c.Stall.Level != tc.level {
			t.Errorf("stalled %v: level %q, want %q", tc.ago, c.Stall.Level, tc.level)
		}
	}
	// a run that is not going has no stall
	d := strings.Replace(dungeonJSON("x", 20*time.Minute), `"state":"running","last_progress_at"`, `"state":"failed","last_progress_at"`, 1)
	d = strings.Replace(d, `"report":{"plan":"tiny","map":209,"state":"running"`, `"report":{"plan":"tiny","map":209,"state":"failed"`, 1)
	c := BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(d)})
	if c.Stall.Level != "none" || c.Run.Phase != "failed" {
		t.Fatalf("ended run: stall %+v phase %q", c.Stall, c.Run.Phase)
	}
}

func TestCardPhases(t *testing.T) {
	for _, tc := range []struct{ current, waiting, want string }{
		{"prepare hire: x", "prepare hire", "prepare"},
		{"enter: the party is not inside", "enter", "enter"},
		{"leg 1 (b): kill boss 6001", "boss kill", "boss"},
		{"leg 1 (b): pull group 2 of 3", "", "pull"},
		{"leg 1 (b): walk to 1.0 2.0 3.0", "", "walk"},
		{"leg 1 (b): step 0: use_object", "", "step"},
		{"leg 1 (b): walk to 1 2 3", "rest leg 1 (b)", "rest"},
		{"", "", "starting"},
	} {
		if got := phaseOf("running", tc.current, tc.waiting, 0); got != tc.want && !(tc.current == "leg 1 (b): pull group 2 of 3") {
			t.Errorf("phaseOf(%q, %q) = %q, want %q", tc.current, tc.waiting, got, tc.want)
		}
	}
	if got := phaseOf("running", "leg 1 (b): pull group 2 of 3", "", 2); got != "pull" {
		t.Errorf("group in hand: %q", got)
	}
}

func TestOfflineCard(t *testing.T) {
	c := BuildCard(Inputs{Agent: Agent{ID: "8767", Addr: "127.0.0.1:8767"}, Now: t0, StatusErr: "connection refused"})
	if c.Online || c.Error != "connection refused" || c.Bots == nil || c.Orders == nil {
		t.Fatalf("%+v", c)
	}
}

// The cards are built from fields the model names; no field of the model, at any depth, may carry a secret-looking name.
func TestCardModelHasNoSecretFields(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" {
				name = f.Name
			}
			if SecretKey(name) || SecretKey(f.Name) {
				t.Errorf("%s.%s serialises as %q: a secret-looking name", path, f.Name, name)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(reflect.TypeOf(Card{}), "Card")
	walk(reflect.TypeOf(Order{}), "Order")
}

func TestScrub(t *testing.T) {
	in := `{"state":"in_world","password":"x","nested":{"rpc_token":"abc","list":[{"Accounts":{"a":"b"},"ok":1.50}],"login_name":"n"},
 "text":"error with tokenvalue123456 inside","short":"abc","unknown_opcodes":{"SMSG_AUTH_RESPONSE":1,"SMSG_EMOTE":3},"big":12345678901234567890}`
	out, err := Scrub([]byte(in), "tokenvalue123456", "abc")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["password"]; ok {
		t.Error("password kept")
	}
	nested := m["nested"].(map[string]any)
	if _, ok := nested["rpc_token"]; ok {
		t.Error("rpc_token kept")
	}
	if _, ok := nested["login_name"]; ok {
		t.Error("login_name kept")
	}
	if l := nested["list"].([]any)[0].(map[string]any); l["Accounts"] != nil || l["ok"] == nil {
		t.Errorf("list entry: %v", l)
	}
	if m["text"] != redacted {
		t.Errorf("a string with the token stays: %v", m["text"])
	}
	if m["short"] != "abc" {
		t.Errorf("a short secret must not blank text: %v", m["short"])
	}
	if ops := m["unknown_opcodes"].(map[string]any); ops["SMSG_AUTH_RESPONSE"] != nil || ops["SMSG_EMOTE"] == nil {
		t.Errorf("opcodes: %v", ops)
	}
	if !strings.Contains(string(out), "12345678901234567890") {
		t.Errorf("numbers must survive exactly: %s", out)
	}
	if _, err := Scrub([]byte(`{"a":`)); err == nil {
		t.Error("invalid JSON must be an error, not passed through")
	}
}

func TestGeometry(t *testing.T) {
	g := BuildGeometry(tinyPlan(t))
	if g.Map != 209 || g.Plan != "tiny" || len(g.Legs) != 2 {
		t.Fatalf("%+v", g)
	}
	// every drawn point lies in the view box: the route, the pull circles with their radius, the boss, the object
	vb := g.ViewBox
	inside := func(x, y, r float64) bool {
		return x-r >= vb[0] && x+r <= vb[0]+vb[2] && y-r >= vb[1] && y+r <= vb[1]+vb[3]
	}
	for _, l := range g.Legs {
		for _, p := range l.Path {
			if !inside(p.X, p.Y, 0) {
				t.Errorf("leg %d path point %+v outside %v", l.Index, p, vb)
			}
		}
	}
	for _, pos := range [][2]float32{{1002, 805}, {1150, 900}} {
		x, y := Project(pos[0], pos[1])
		if !inside(x, y, 10) {
			t.Errorf("pull circle at %v outside %v", pos, vb)
		}
	}
	if vb[2] < minSpan || vb[3] < minSpan {
		t.Errorf("view box %v narrower than %v yd", vb, minSpan)
	}
	// north (+x) is up, west (+y) is left
	nx, ny := Project(1100, 800)
	sx, sy := Project(1000, 800)
	if ny >= sy || nx != sx {
		t.Errorf("north must be up: %v,%v vs %v,%v", nx, ny, sx, sy)
	}
	wx, _ := Project(1000, 900)
	ex, _ := Project(1000, 800)
	if wx >= ex {
		t.Errorf("west must be left: %v vs %v", wx, ex)
	}
	svg := g.SVG
	for _, want := range []string{`class="route" data-leg="0"`, `class="direct" data-leg="0"`, `class="pull" data-leg="0" data-group="0"`, `r="10.0"`, `r="5.0"`, `r="25.0"`,
		`class="boss"`, `boss 6001`, `class="obj use"`, `use 555`, `class="entrance"`, `data-leg="1" data-group="0"`} {
		if !strings.Contains(svg, want) {
			t.Errorf("svg lacks %q", want)
		}
	}
	if strings.Contains(svg, "<b>") || !strings.Contains(svg, "two &lt;b&gt;") {
		t.Errorf("a leg name must be escaped in the svg: %s", svg)
	}
	// the direct step is its own dashed line, so leg 0 has a plain route (entrance, via) and a direct line to the second via point
	if strings.Count(svg, `class="direct"`) != 1 {
		t.Errorf("want one direct line, got %d", strings.Count(svg, `class="direct"`))
	}
	if !json.Valid(mustJSON(t, g)) {
		t.Error("geometry is not JSON")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGeometryOfARealPlan(t *testing.T) {
	for _, name := range []string{"uldaman", "zulfarrak"} {
		p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", name+".json"))
		if err != nil {
			t.Skipf("%s: %v", name, err)
		}
		g := BuildGeometry(p)
		if len(g.Legs) != len(p.Legs) || g.ViewBox[2] <= minSpan || strings.Count(g.SVG, "<circle class=\"pull") == 0 {
			t.Errorf("%s: %d legs, box %v", name, len(g.Legs), g.ViewBox)
		}
	}
}

func TestGroupOf(t *testing.T) {
	p := tinyPlan(t)
	for _, tc := range []struct {
		leg, wantLeg, wantGroup int
		entry                   uint32
		x, y                    float32
		boss                    bool
	}{
		{0, 0, 0, 7001, 1004, 805, false},   // inside group 0's radius
		{0, 1, 0, 7001, 1150, 905, false},   // the same entry in leg 1's pack
		{0, -1, -1, 7001, 1050, 805, false}, // too far from either pack
		{0, -1, -1, 4242, 1002, 805, false}, // not an entry of the plan
		{0, 0, -1, 6001, 1100, 900, true},   // the boss
		{1, 1, 0, 7001, 1150, 900, false},   // the current leg wins
	} {
		l, g, boss := GroupOf(p, tc.leg, tc.entry, tc.x, tc.y)
		if l != tc.wantLeg || g != tc.wantGroup || boss != tc.boss {
			t.Errorf("GroupOf(leg %d, %d @ %v,%v) = %d,%d,%v want %d,%d,%v", tc.leg, tc.entry, tc.x, tc.y, l, g, boss, tc.wantLeg, tc.wantGroup, tc.boss)
		}
	}
}

func TestSafeJoinRefusesTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "tests")
	outside := filepath.Join(base, "secret.txt")
	must(t, os.MkdirAll(filepath.Join(root, "run1", "traces"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "run1", "traces", "r.jsonl"), []byte("{}\n"), 0o644))
	must(t, os.WriteFile(outside, []byte("secret"), 0o644))
	must(t, os.Symlink(outside, filepath.Join(root, "link.txt")))
	must(t, os.Symlink(base, filepath.Join(root, "linkdir")))
	if got, err := SafeJoin(root, "run1/traces/r.jsonl"); err != nil || !strings.HasSuffix(got, "r.jsonl") {
		t.Fatalf("a file in the folder: %q %v", got, err)
	}
	for _, rel := range []string{"", "..", "../secret.txt", "run1/../../secret.txt", "run1/..\\..\\secret.txt", "/etc/passwd", outside, "link.txt", "linkdir/secret.txt",
		"run1", "run1/traces/r.jsonl\x00.txt", "run1/../..", "./../secret.txt", "run1//../../secret.txt", "nope.txt"} {
		if got, err := SafeJoin(root, rel); err == nil {
			t.Errorf("SafeJoin(%q) = %q, want an error", rel, got)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestFilesHandlerServesOnlyTheTestsFolder(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "tests")
	must(t, os.MkdirAll(filepath.Join(root, "run1"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "run1", "REPORT.md"), []byte("# hello"), 0o644))
	must(t, os.WriteFile(filepath.Join(base, "secret.txt"), []byte("TOPSECRET"), 0o644))
	must(t, os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(root, "run1", "ln.txt")))
	srv := httptest.NewServer(Handler(NewPoller(Options{TestsDir: root, ConfigDir: t.TempDir(), Agents: "auto"}), root))
	defer srv.Close()
	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/files/run1/REPORT.md"); code != 200 || body != "# hello" {
		t.Fatalf("report: %d %q", code, body)
	}
	for _, p := range []string{"/files/../secret.txt", "/files/%2e%2e/secret.txt", "/files/run1/%2e%2e/%2e%2e/secret.txt", "/files/run1/..%2f..%2fsecret.txt", "/files/run1/ln.txt",
		"/files//etc/passwd", "/files/run1/", "/files/", "/files/run1/%5c..%5c..%5csecret.txt", "/files/%00"} {
		code, body := get(p)
		if code == 200 || strings.Contains(body, "TOPSECRET") {
			t.Errorf("GET %s = %d %q", p, code, body)
		}
	}
	// only GET and HEAD
	resp, err := http.Post(srv.URL+"/api/state", "application/json", strings.NewReader("{}"))
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", resp.StatusCode)
	}
	// a request that names another host (DNS rebinding) is refused
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Host = "evil.example.com"
	resp, err = http.DefaultClient.Do(req)
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign host: %d", resp.StatusCode)
	}
	if code, body := get("/"); code != 200 || !strings.Contains(body, "turtle-agent live dashboard") {
		t.Errorf("index: %d", code)
	}
	for _, f := range []string{"/app.js", "/app.css"} {
		if code, _ := get(f); code != 200 {
			t.Errorf("%s: %d", f, code)
		}
	}
	if code, _ := get("/api/plan/..%2f..%2fetc"); code == 200 {
		t.Error("plan name traversal")
	}
}

func TestDiscoverAgents(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) { must(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)) }
	write("settings.json", `{"accounts":{"A":"pw"}}`)
	write("settings-1.json", `{"rpc":"127.0.0.1:8765","accounts":{"BOB":"hunter2"},"rpc_token":"abcdefghijklmnopqrstuvwxyz0123456789"}`)
	write("settings-2.json", `{"rpc":"localhost:8766"}`)
	write("settings-3.json", `{"rpc":"0.0.0.0:8767"}`)
	write("settings-4.json", `not json`)
	as, err := DiscoverAgents(dir, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 2 || as[0].Addr != "127.0.0.1:8765" || as[1].Addr != "127.0.0.1:8766" {
		t.Fatalf("auto: %+v", as)
	}
	if as[0].TokenPath != filepath.Join(dir, "rpc-8765.token") {
		t.Errorf("token path: %s", as[0].TokenPath)
	}
	for _, a := range as {
		if strings.Contains(fmt.Sprintf("%+v", a), "hunter2") {
			t.Errorf("a secret reached the agent record: %+v", a)
		}
	}
	as, err = DiscoverAgents(dir, "auto,8770,127.0.0.1:8765")
	if err != nil || len(as) != 3 || as[2].ID != "8770" {
		t.Fatalf("auto plus list: %+v %v", as, err)
	}
	as, err = DiscoverAgents(dir, "8771, 8772")
	if err != nil || len(as) != 2 {
		t.Fatalf("list: %+v %v", as, err)
	}
	for _, bad := range []string{"10.0.0.5:8765", "example.com:80", "0.0.0.0:8765", "127.0.0.1:0"} {
		if _, err := DiscoverAgents(dir, bad); err == nil {
			t.Errorf("%q must be refused: agents are loopback only", bad)
		}
	}
}

func TestClientIsReadOnly(t *testing.T) {
	cl := NewClient(Agent{ID: "1", Addr: "127.0.0.1:1"}, time.Second)
	for _, m := range []string{"chat", "gm", "logout", "teleport", "commander", "tune_set", "dungeon_run", "dungeon_stop", "brain_start", "walk_to", "cast", "invite", ""} {
		if _, err := cl.Call(context.Background(), m); err != ErrNotAllowed {
			t.Errorf("%q: %v, want ErrNotAllowed", m, err)
		}
	}
}

// fakeAgent answers the four read-only methods with replies full of secrets, and records every call.
type fakeAgent struct {
	mu      sync.Mutex
	methods []string
	auth    []string
	srv     *httptest.Server
}

func newFakeAgent(t *testing.T, dungeonBody string) *fakeAgent {
	f := &fakeAgent{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
			Params any    `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.methods = append(f.methods, req.Method)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if req.Params != nil {
			http.Error(w, "the dashboard sends no parameters", 400)
			return
		}
		var result string
		switch req.Method {
		case "status":
			result = statusJSON
		case "dungeon_status":
			result = dungeonBody
		case "bot_view":
			result = viewJSON
		case "tune_list":
			result = `{"tunables":[{"key":"threat_wait_s","value":12,"base":6,"source":"file"},{"key":"x","value":1,"base":1,"source":"base"}],"rpc_token":"TOKENVALUE-0123456789"}`
		default:
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`))
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, result)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestPollerEndToEndIsReadOnlyAndSecretFree(t *testing.T) {
	dir := t.TempDir()
	tests := filepath.Join(dir, "tests")
	traces := filepath.Join(dir, "work", "traces")
	plans := filepath.Join(dir, "plans")
	cfg := filepath.Join(dir, "cfg")
	for _, d := range []string{tests, traces, plans, cfg, filepath.Join(tests, "grp-a", "traces")} {
		must(t, os.MkdirAll(d, 0o755))
	}
	tinyJSON, _ := os.ReadFile("testdata/tiny.json")
	must(t, os.WriteFile(filepath.Join(plans, "tiny.json"), tinyJSON, 0o644))
	traceFile := filepath.Join(tests, "grp-a", "traces", "r-1.jsonl")
	must(t, os.WriteFile(traceFile, []byte(strings.Join([]string{
		`{"v":1,"k":"header","run":"r-1","t":0,"agent_sha":"c7775d5b9baf3b0f88e0d2ce41f0a29a2149e3bc","map":209,"plan":"tiny","start":"2026-10-14T16:35:00Z"}`,
		`{"v":1,"k":"party","run":"r-1","t":10,"g":"0x2dc","name":"Forthur","role":"tank","lead":true}`,
		`{"v":1,"k":"event","run":"r-1","t":2000,"ev":"order","ms":97,"reply":"ack","text":"cmd move 1 2 3","verb":"move","via":"cmd"}`,
		`{"v":1,"k":"event","run":"r-1","t":3000,"ev":"order","ms":50,"reply":"err","text":"cmd attack 0x1","verb":"attack","via":"cmd","code":"invalid-target","message":"No unit"}`, ""}, "\n")), 0o644))
	must(t, os.WriteFile(filepath.Join(tests, "grp-a", "REPORT.md"), []byte("r"), 0o644))
	// the agent reports its trace by absolute path
	f := newFakeAgent(t, strings.Replace(dungeonJSON("leg 0 (one): pull group 2 of 3", time.Second), `traces/r-1.jsonl`, traceFile, 1))
	u, _ := url.Parse(f.srv.URL)
	const token = "TOKENVALUE-0123456789"
	must(t, os.WriteFile(filepath.Join(cfg, "rpc-"+u.Port()+".token"), []byte(token+"\n"), 0o600))
	p := NewPoller(Options{ConfigDir: cfg, Agents: u.Host, TestsDir: tests, TracesDir: traces, PlansDirs: []string{plans}, Now: func() time.Time { return t0 }})
	p.Tick(context.Background())

	f.mu.Lock()
	defer f.mu.Unlock()
	allowed := map[string]bool{"status": true, "dungeon_status": true, "tune_list": true, "bot_view": true}
	for _, m := range f.methods {
		if !allowed[m] {
			t.Errorf("the dashboard called %q", m)
		}
	}
	if len(f.methods) != 4 {
		t.Errorf("one poll is four calls, got %v", f.methods)
	}
	for _, a := range f.auth {
		if a != "Bearer "+token {
			t.Errorf("authorization %q", a)
		}
	}
	body := string(p.State())
	raw, ok := p.Raw(u.Port())
	if !ok {
		t.Fatal("no raw panel")
	}
	for name, doc := range map[string]string{"state": body, "raw": string(raw)} {
		for _, secret := range []string{token, "hunter2", "password", "accounts", "rpc_token", "\"BOB\"", "AUTH_RESPONSE"} {
			if strings.Contains(doc, secret) {
				t.Errorf("%s contains %q:\n%s", name, secret, doc)
			}
		}
		var v any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatalf("%s is not JSON: %v", name, err)
		}
		assertNoSecretKeys(t, name, v)
	}
	var state struct {
		Agents []Card `json:"agents"`
		Plans  []string
	}
	must(t, json.Unmarshal(p.State(), &state))
	if len(state.Agents) != 1 {
		t.Fatalf("agents: %s", body)
	}
	c := state.Agents[0]
	if c.SHA != "c7775d5b9baf" || len(c.Orders) != 2 || c.Orders[1].Reply != "ERR" || c.Label.Folder != "grp-a" || len(c.Links) < 2 || c.Links[0].Path != "grp-a/traces/r-1.jsonl" {
		t.Fatalf("trace not folded in: sha %q orders %+v folder %q links %+v", c.SHA, c.Orders, c.Label.Folder, c.Links)
	}
	if c.Bots[0].Role != "tank" || len(c.Tunables) != 1 || c.Tunables[0].Key != "threat_wait_s" {
		t.Errorf("bots/tunables: %+v %+v", c.Bots[0], c.Tunables)
	}
	if geo, errText := p.Geometry("tiny"); geo == nil || errText != "" {
		t.Errorf("geometry: %v", errText)
	}
	if geo, _ := p.Geometry("../../etc/passwd"); geo != nil {
		t.Error("geometry of a path")
	}
}

func assertNoSecretKeys(t *testing.T, where string, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if SecretKey(k) {
				t.Errorf("%s has the key %q", where, k)
			}
			assertNoSecretKeys(t, where, e)
		}
	case []any:
		for _, e := range x {
			assertNoSecretKeys(t, where, e)
		}
	}
}

func TestResolveTrace(t *testing.T) {
	dir := t.TempDir()
	tests, traces := filepath.Join(dir, "tests"), filepath.Join(dir, "agent", "traces")
	must(t, os.MkdirAll(filepath.Join(tests, "g"), 0o755))
	must(t, os.MkdirAll(traces, 0o755))
	must(t, os.WriteFile(filepath.Join(tests, "g", "a.jsonl"), []byte("{}\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(traces, "b.jsonl"), []byte("{}\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "evil.jsonl"), []byte("{}\n"), 0o644))
	must(t, os.Symlink(filepath.Join(dir, "evil.jsonl"), filepath.Join(tests, "g", "ln.jsonl")))
	p := NewPoller(Options{ConfigDir: dir, TestsDir: tests, TracesDir: traces})
	if path, link, folder, ok := p.resolveTrace(filepath.Join(tests, "g", "a.jsonl")); !ok || link != "g/a.jsonl" || folder != "g" || !strings.HasSuffix(path, "a.jsonl") {
		t.Errorf("tests file: %q %q %q %v", path, link, folder, ok)
	}
	if _, link, folder, ok := p.resolveTrace("traces/b.jsonl"); !ok || link != "" || folder != "traces" {
		t.Errorf("relative to the agent folder: %q %q %v", link, folder, ok)
	}
	for _, bad := range []string{"", "/etc/passwd", filepath.Join(dir, "evil.jsonl"), filepath.Join(tests, "g", "ln.jsonl"), filepath.Join(tests, "g", "..", "..", "evil.jsonl"),
		"../evil.jsonl", "traces/../../evil.jsonl", filepath.Join(tests, "g", "a.txt"), "/proc/self/environ.jsonl"} {
		if path, _, _, ok := p.resolveTrace(bad); ok {
			t.Errorf("resolveTrace(%q) = %q, want refused", bad, path)
		}
	}
}

func TestTailerIsIncrementalAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.jsonl")
	line := func(s string) string { return s + "\n" }
	hdr := line(`{"v":1,"k":"header","run":"r","agent_sha":"abcdef0123456789abcdef","map":70,"plan":"uldaman","start":"2026-10-14T14:00:00Z"}`)
	must(t, os.WriteFile(path, []byte(hdr+line(`{"v":1,"k":"party","t":1,"g":"0x2cd","name":"Graepanr","role":"tank","lead":true}`)), 0o644))
	tl := NewTailer()
	s, err := tl.Read(path, t0)
	must(t, err)
	if s.SHA != "abcdef012345" || s.Plan != "uldaman" || s.Map != 70 || len(s.Party) != 1 || s.Party[0].Role != "tank" {
		t.Fatalf("%+v", s)
	}
	appendTo := func(text string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		must(t, err)
		defer f.Close()
		_, err = f.WriteString(text)
		must(t, err)
	}
	for i := 0; i < 14; i++ {
		appendTo(line(fmt.Sprintf(`{"v":1,"k":"event","t":%d,"ev":"order","reply":"ack","text":"cmd move %d","via":"cmd"}`, 1000+i, i)))
		appendTo(line(fmt.Sprintf(`{"v":1,"k":"frame","t":%d,"via":"feed","bots":[],"mobs":[]}`, 1000+i)))
	}
	// a half-written line is left until it is complete
	appendTo(`{"v":1,"k":"event","t":5000,"ev":"order","reply":"err","text":"cmd half`)
	s, err = tl.Read(path, t0)
	must(t, err)
	if len(s.Orders) != maxOrders || s.Orders[maxOrders-1].Text != "cmd move 13" || s.Orders[0].Text != "cmd move 4" {
		t.Fatalf("orders: %+v", s.Orders)
	}
	appendTo(`","code":"no-los"}` + "\n")
	s, err = tl.Read(path, t0)
	must(t, err)
	last := s.Orders[len(s.Orders)-1]
	if last.Text != "cmd half" || last.Reply != "ERR" || last.Code != "no-los" {
		t.Fatalf("the completed line: %+v", last)
	}
	// the file is cut: start over, no stale state
	must(t, os.WriteFile(path, []byte(hdr), 0o644))
	s, err = tl.Read(path, t0)
	must(t, err)
	if len(s.Orders) != 0 || s.SHA == "" {
		t.Fatalf("after truncation: %+v", s)
	}
	if _, err := tl.Read(filepath.Join(filepath.Dir(path), "missing.jsonl"), t0); err == nil {
		t.Error("a missing file must be an error")
	}
	// idle trackers are dropped
	tl.Read(path, t0.Add(traceIdle+time.Minute))
	if _, err := tl.Read(path, t0.Add(2*traceIdle)); err != nil {
		t.Fatal(err)
	}
}

func TestTailerReadsALargeFileFromItsEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	f, err := os.Create(path)
	must(t, err)
	fmt.Fprintln(f, `{"v":1,"k":"header","run":"big","agent_sha":"deadbeefdeadbeefdead","map":1,"plan":"p","start":"2026-10-14T14:00:00Z"}`)
	pad := strings.Repeat("x", 900)
	for i := 0; i < 8000; i++ { // about 7 MB
		fmt.Fprintf(f, `{"v":1,"k":"event","t":%d,"ev":"note","text":"%s"}`+"\n", i, pad)
	}
	fmt.Fprintln(f, `{"v":1,"k":"event","t":9000,"ev":"order","reply":"ack","text":"cmd last","via":"cmd"}`)
	f.Close()
	s, err := NewTailer().Read(path, t0)
	must(t, err)
	if s.SHA != "deadbeefdead" || len(s.Orders) != 1 || s.Orders[0].Text != "cmd last" {
		t.Fatalf("%+v", s)
	}
}
