package preflight

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

// fakeProbe answers AgentPID from a map of dir → pid.
type fakeProbe map[string]int

func (p fakeProbe) AgentPID(dir string) (int, bool) {
	pid, ok := p[dir]
	return pid, ok
}

func (fakeProbe) PortListening(int) bool { return false }

// fakeConf serves prepared effective keys.
type fakeConf struct {
	keys map[string]map[string]string
	errs map[string]error
}

func (f fakeConf) EffectiveKeys(_ context.Context, server string) (map[string]string, error) {
	if err := f.errs[server]; err != nil {
		return nil, err
	}
	return f.keys[server], nil
}

const goodHelp = "usage: turtle-agent raid-walk <plan> [--instance-cap-per-account N]"

// fakeBinary writes a shell script that behaves like turtle-agent for the checks. capSupport adds the cap flag to
// its raid-walk help; schemaUnknown makes lease list fail with an unknown-field error.
func fakeBinary(t *testing.T, dir, name string, capSupport, schemaUnknown bool) string {
	t.Helper()
	help := "usage: turtle-agent (no cap flag)"
	if capSupport {
		help = goodHelp
	}
	lease := `echo "m910q 1 Gbmage 8771 free"`
	if schemaUnknown {
		lease = `echo 'json: unknown field "cap_per_account"' >&2; exit 2`
	}
	body := "#!/bin/sh\ncase \"$1\" in\n" +
		"--help) echo " + shellQuote(help) + "; exit 2;;\n" +
		"version) echo 'turtle-agent 1.2.3';;\n" +
		"lease) " + lease + ";;\n" +
		"raid-walk) echo " + shellQuote(help) + "; exit 2;;\n" +
		"esac\nexit 0\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type fixture struct {
	walk  string
	slots string
	keys  string
	bin   string
}

// newFixture makes a walk dir, a slot table with slots 1, 2 and a reserved 3, an instance-cap file, and a plan.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	walk := t.TempDir()
	cfg := t.TempDir()
	f := &fixture{walk: walk, slots: filepath.Join(cfg, "slots.json"), keys: filepath.Join(cfg, "keys.json")}
	slotsJSON := `{"version":1,"servers":[{"name":"m910q","realmd":"127.0.0.1:3724","slots":[
		{"id":"1","account":"TAGENT1","character":"Gbmage","port":8771,"settings_from":"/x/1.json"},
		{"id":"2","account":"TAGENT2","character":"Tacmdaron","port":8772,"settings_from":"/x/2.json"},
		{"id":"3","reserved":"owner rule"},
		{"id":"4","account":"TAGENT4","character":"Tacmdbilo","port":8774,"settings_from":"/x/4.json"}]}]}`
	writeFile(t, f.slots, 0o600, slotsJSON)
	writeFile(t, filepath.Join(cfg, "instance-cap-per-account.json"), 0o600, `{"m910q": 30}`)
	f.bin = fakeBinary(t, t.TempDir(), "turtle-agent", true, false)
	writeFile(t, filepath.Join(walk, "dungeons", "zul_gurub.json"), 0o644, planJSON)
	return f
}

func writeFile(t *testing.T, path string, mode os.FileMode, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

const planJSON = `{
 "version": 1,
 "name": "zul_gurub",
 "kind": "raid",
 "map": 309,
 "entrance": {"x": 0, "y": 0, "z": 0},
 "raid": {"size": 20, "tanks": 3, "healers": 5, "dps": 12},
 "legs": [
  {"name": "high_priestess_arlokk", "to": {"x": 1, "y": 2, "z": 3}, "boss": {"entry": 14515, "position": {"x": 1, "y": 2, "z": 3}}},
  {"name": "hakkar", "to": {"x": 4, "y": 5, "z": 6}, "boss": {"entry": 14834, "position": {"x": 4, "y": 5, "z": 6}}}
 ]
}`

func (f *fixture) cmd(t *testing.T, name, line string) {
	t.Helper()
	writeFile(t, filepath.Join(f.walk, name), 0o644, line+"\n")
}

func (f *fixture) cmdLine(extra string) string {
	return f.bin + " raid-walk zul_gurub --server m910q --dir " + filepath.Join(f.walk, "dungeons") + " " + extra
}

func (f *fixture) run(t *testing.T, deps Deps) *Report {
	t.Helper()
	if deps.Conf == nil {
		deps.Conf = fakeConf{}
	}
	if deps.Probe == nil {
		deps.Probe = fakeProbe{}
	}
	rep, err := Run(context.Background(), Options{Dir: f.walk, SlotsPath: f.slots, KeysPath: f.keys, Deps: deps})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func find(rep *Report, check string) []Result {
	var out []Result
	for _, r := range rep.Results {
		if r.Check == check {
			out = append(out, r)
		}
	}
	return out
}

func findSubject(rep *Report, check, sub string) *Result {
	for i := range rep.Results {
		r := &rep.Results[i]
		if r.Check == check && strings.Contains(r.Subject, sub) {
			return r
		}
	}
	return nil
}

func statuses(rep *Report, check string) []Status {
	var out []Status
	for _, r := range find(rep, check) {
		out = append(out, r.Status)
	}
	return out
}

func hasStatus(rep *Report, check string, st Status) bool {
	for _, s := range statuses(rep, check) {
		if s == st {
			return true
		}
	}
	return false
}

func TestCheckBinaries(t *testing.T) {
	f := newFixture(t)
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --until none"))
	rep := f.run(t, Deps{})
	if !hasStatus(rep, CheckBinary, OK) {
		t.Fatalf("a working binary is not OK: %+v", find(rep, CheckBinary))
	}
	if hasStatus(rep, CheckBinary, Warn) {
		t.Fatalf("one binary warned: %+v", find(rep, CheckBinary))
	}
	// A second binary is a warning.
	other := fakeBinary(t, t.TempDir(), "turtle-agent", true, false)
	f.cmd(t, "cmd-b.txt", other+" raid-walk zul_gurub --server m910q --slot 2 --dir "+filepath.Join(f.walk, "dungeons")+" --until none")
	rep = f.run(t, Deps{})
	warn := findSubject(rep, CheckBinary, "the walk")
	if warn == nil || warn.Status != Warn || !strings.Contains(warn.Detail, other) {
		t.Fatalf("two binaries: %+v", find(rep, CheckBinary))
	}
	// A missing binary is a failure.
	f2 := newFixture(t)
	f2.cmd(t, "cmd-a.txt", "/nonexistent/turtle-agent raid-walk zul_gurub --server m910q --slot 1 --dir "+filepath.Join(f2.walk, "dungeons"))
	rep = f2.run(t, Deps{})
	if !hasStatus(rep, CheckBinary, Fail) {
		t.Fatalf("missing binary: %+v", find(rep, CheckBinary))
	}
}

func TestCheckSchema(t *testing.T) {
	f := newFixture(t)
	bad := fakeBinary(t, t.TempDir(), "turtle-agent", false, true)
	f.cmd(t, "cmd-a.txt", bad+" raid-walk zul_gurub --server m910q --slot 1 --dir "+filepath.Join(f.walk, "dungeons"))
	rep := f.run(t, Deps{})
	res := findSubject(rep, CheckSchema, bad)
	if res == nil || res.Status != Fail || !strings.Contains(res.Detail, "unknown field") {
		t.Fatalf("unknown field: %+v", find(rep, CheckSchema))
	}
	if res.Fix == "" {
		t.Fatal("a failure has no fix hint")
	}
	f2 := newFixture(t)
	f2.cmd(t, "cmd-a.txt", f2.cmdLine("--slot 1"))
	rep = f2.run(t, Deps{})
	if got := findSubject(rep, CheckSchema, f2.bin); got == nil || got.Status != OK {
		t.Fatalf("good schema: %+v", find(rep, CheckSchema))
	}
}

func TestCheckSlotsPath(t *testing.T) {
	f := newFixture(t)
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --slots /tmp/other-slots.json"))
	rep := f.run(t, Deps{})
	if !hasStatus(rep, CheckSlots, Fail) {
		t.Fatalf("a --slots override is not a failure: %+v", find(rep, CheckSlots))
	}
}

func TestCheckInstanceCaps(t *testing.T) {
	f := newFixture(t)
	old := fakeBinary(t, t.TempDir(), "turtle-agent", false, false)
	f.cmd(t, "cmd-a.txt", old+" raid-walk zul_gurub --server m910q --slot 1 --dir "+filepath.Join(f.walk, "dungeons"))
	rep := f.run(t, Deps{})
	res := findSubject(rep, CheckCap, "m910q")
	if res == nil || res.Status != Fail || !strings.Contains(res.Detail, "would use 5") {
		t.Fatalf("old binary and cap 30: %+v", find(rep, CheckCap))
	}

	f2 := newFixture(t)
	f2.cmd(t, "cmd-a.txt", f2.cmdLine("--slot 1"))
	rep = f2.run(t, Deps{})
	res = findSubject(rep, CheckCap, "m910q")
	if res == nil || res.Status != OK || !strings.Contains(res.Detail, "effective cap 30") {
		t.Fatalf("new binary and cap 30: %+v", find(rep, CheckCap))
	}

	f3 := newFixture(t)
	f3.cmd(t, "cmd-a.txt", old+" raid-walk zul_gurub --server m910q --slot 1 --instance-cap-per-account 30 --dir "+filepath.Join(f3.walk, "dungeons"))
	rep = f3.run(t, Deps{})
	if !hasStatus(rep, CheckCap, Fail) {
		t.Fatalf("a flag the binary lacks is not a failure: %+v", find(rep, CheckCap))
	}

	// An explicit stock 5 under a file that says 30 is the same hazard.
	f5 := newFixture(t)
	f5.cmd(t, "cmd-a.txt", f5.cmdLine("--slot 1 --instance-cap-per-account 5"))
	rep = f5.run(t, Deps{})
	if !hasStatus(rep, CheckCap, Fail) {
		t.Fatalf("explicit 5 under a 30 file: %+v", find(rep, CheckCap))
	}

	// A missing cap file is a warning, and every binary then uses the stock 5.
	f4 := newFixture(t)
	os.Remove(filepath.Join(filepath.Dir(f4.slots), "instance-cap-per-account.json"))
	f4.cmd(t, "cmd-a.txt", f4.cmdLine("--slot 1"))
	rep = f4.run(t, Deps{})
	if !hasStatus(rep, CheckCap, Warn) {
		t.Fatalf("missing cap file: %+v", find(rep, CheckCap))
	}
}

func TestCheckLeases(t *testing.T) {
	f := newFixture(t)
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --until none"))
	rep := f.run(t, Deps{})
	if res := findSubject(rep, CheckLease, "m910q/1"); res == nil || res.Status != OK {
		t.Fatalf("free slot: %+v", find(rep, CheckLease))
	}

	// Held by another holder.
	leases := filepath.Join(filepath.Dir(f.slots), "leases", "m910q")
	writeFile(t, filepath.Join(leases, "2.json"), 0o600, `{"server":"m910q","slot":"2","holder":"other","lease_id":"x",
		"acquired":"2026-01-01T00:00:00Z","expires":"2999-01-01T00:00:00Z"}`)
	f.cmd(t, "cmd-b.txt", f.cmdLine("--slot 2 --until none"))
	rep = f.run(t, Deps{})
	res := findSubject(rep, CheckLease, "m910q/2")
	if res == nil || res.Status != Fail || !strings.Contains(res.Detail, "held by other") {
		t.Fatalf("held by another: %+v", find(rep, CheckLease))
	}

	// Two commands on one slot.
	f.cmd(t, "cmd-c.txt", f.cmdLine("--slot 1 --holder a --until none"))
	f.cmd(t, "cmd-d.txt", f.cmdLine("--slot 1 --holder b --until none"))
	rep = f.run(t, Deps{})
	if res := findSubject(rep, CheckLease, "m910q/1"); res == nil || res.Status != Fail || !strings.Contains(res.Detail, "two commands share") {
		t.Fatalf("shared slot: %+v", find(rep, CheckLease))
	}
}

func TestCheckLeaseHeldByThisWalk(t *testing.T) {
	f := newFixture(t)
	out := filepath.Join(f.walk, "run")
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --holder mine --out "+out+" --until none"))
	slotDir := filepath.Join(out, "slot")
	writeFile(t, filepath.Join(filepath.Dir(f.slots), "leases", "m910q", "1.json"), 0o600, `{"server":"m910q","slot":"1","holder":"mine","lease_id":"x",
		"acquired":"2026-01-01T00:00:00Z","expires":"2999-01-01T00:00:00Z","dir":`+strconvQuote(slotDir)+`}`)
	rep := f.run(t, Deps{Probe: fakeProbe{slotDir: 4242}})
	res := findSubject(rep, CheckLease, "m910q/1")
	if res == nil || res.Status != OK || !strings.Contains(res.Detail, "this walk") {
		t.Fatalf("this walk's lease: %+v", find(rep, CheckLease))
	}
	// Same lease, no live agent: the walk would be blocked.
	rep = f.run(t, Deps{Probe: fakeProbe{}})
	res = findSubject(rep, CheckLease, "m910q/1")
	if res == nil || res.Status != Fail {
		t.Fatalf("this walk's lease without an agent: %+v", find(rep, CheckLease))
	}
}

func TestCheckLeaseStale(t *testing.T) {
	f := newFixture(t)
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --until none"))
	writeFile(t, filepath.Join(filepath.Dir(f.slots), "leases", "m910q", "1.json"), 0o600, `{"server":"m910q","slot":"1","holder":"old","lease_id":"x",
		"acquired":"2026-01-01T00:00:00Z","expires":"2026-01-01T01:00:00Z"}`)
	rep := f.run(t, Deps{})
	res := findSubject(rep, CheckLease, "m910q/1")
	if res == nil || res.Status != Warn || !strings.Contains(res.Detail, "stale") {
		t.Fatalf("stale lease: %+v", find(rep, CheckLease))
	}
}

func TestCheckLeaseSlot3(t *testing.T) {
	f := newFixture(t)
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 3 --until none"))
	rep := f.run(t, Deps{})
	res := findSubject(rep, CheckLease, "m910q/3")
	if res == nil || res.Status != Fail || !strings.Contains(res.Detail, "slot 3") {
		t.Fatalf("slot 3: %+v", find(rep, CheckLease))
	}
}

func TestCheckPlacement(t *testing.T) {
	f := newFixture(t)
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --place --until none"))
	rep := f.run(t, Deps{})
	if res := findSubject(rep, CheckPlace, "zul_gurub"); res == nil || res.Status != Fail || !strings.Contains(res.Detail, "needs --sql") {
		t.Fatalf("--place without --sql: %+v", find(rep, CheckPlace))
	}

	sql := filepath.Join(t.TempDir(), "20260101000000_world.sql")
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --place --sql "+sql+" --until none"))
	rep = f.run(t, Deps{})
	if res := findSubject(rep, CheckPlace, "zul_gurub"); res == nil || res.Status != Fail || !strings.Contains(res.Detail, "sql:") {
		t.Fatalf("missing sql: %+v", find(rep, CheckPlace))
	}

	writeFile(t, sql, 0o644, "-- x\n")
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --gm-clear high_priestess_arlokk:1 --until none"))
	rep = f.run(t, Deps{})
	if res := findSubject(rep, CheckPlace, "zul_gurub"); res == nil || res.Status != Fail || !strings.Contains(res.Detail, "creatures.tsv") {
		t.Fatalf("missing spawns: %+v", find(rep, CheckPlace))
	}
	spawns := filepath.Join(f.walk, "dungeons", "..", "tools", "dungeongen", "testdata", "map309")
	writeFile(t, filepath.Join(spawns, "creatures.tsv"), 0o644, "guid\tentry\n")
	rep = f.run(t, Deps{})
	if res := findSubject(rep, CheckPlace, "zul_gurub"); res == nil || res.Status != OK || !strings.Contains(res.Detail, "spawns") {
		t.Fatalf("spawns present: %+v", find(rep, CheckPlace))
	}
}

func sqlExport(t *testing.T, name string, mapID uint32, legs []string) string {
	t.Helper()
	rows := planexport.Rows{
		Plan: planexport.PlanRow{Plan: name, Map: mapID, Source: "turtle-agent abc12345 dungeons/" + name + ".json"},
	}
	for i, l := range legs {
		rows.Legs = append(rows.Legs, planexport.LegRow{Plan: name, Leg: i, Name: l})
	}
	path := filepath.Join(t.TempDir(), planexport.FileName(time.Now()))
	writeFile(t, path, 0o644, planexport.WriteSQL([]planexport.Rows{rows}))
	return path
}

func TestCheckPlans(t *testing.T) {
	f := newFixture(t)
	sql := sqlExport(t, "zul_gurub", 309, []string{"high_priestess_arlokk", "hakkar"})
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --sql "+sql+" --until none"))
	rep := f.run(t, Deps{})
	if res := findSubject(rep, CheckPlan, "zul_gurub"); res == nil || res.Status != OK {
		t.Fatalf("matching export: %+v", find(rep, CheckPlan))
	}

	bad := sqlExport(t, "zul_gurub", 999, []string{"high_priestess_arlokk", "not_a_leg"})
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --sql "+bad+" --until none"))
	rep = f.run(t, Deps{})
	res := findSubject(rep, CheckPlan, "zul_gurub")
	if res == nil || res.Status != Warn || !strings.Contains(res.Detail, "map: export 999") || !strings.Contains(res.Detail, "not_a_leg") {
		t.Fatalf("mismatched export: %+v", find(rep, CheckPlan))
	}

	// A plan edited after its export.
	planPath := filepath.Join(f.walk, "dungeons", "zul_gurub.json")
	future := time.Now().Add(2 * time.Minute)
	if err := os.Chtimes(planPath, future, future); err != nil {
		t.Fatal(err)
	}
	rep = f.run(t, Deps{})
	if res := findSubject(rep, CheckPlan, "zul_gurub"); res == nil || res.Status != Warn || !strings.Contains(res.Detail, "edited") {
		t.Fatalf("stale export: %+v", find(rep, CheckPlan))
	}

	f2 := newFixture(t)
	f2.cmd(t, "cmd-a.txt", f2.cmdLine("--slot 1"))
	rep = f2.run(t, Deps{})
	if res := findSubject(rep, CheckPlan, "zul_gurub"); res == nil || res.Status != OK || !strings.Contains(res.Detail, "no --sql") {
		t.Fatalf("plan only: %+v", find(rep, CheckPlan))
	}
}

func TestCheckServerKeys(t *testing.T) {
	f := newFixture(t)
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --until none"))
	writeFile(t, f.keys, 0o600, `{"keys":{"AiPlayerbot.X":"1","AiPlayerbot.DbPass":"s3cret"}}`)
	conf := fakeConf{keys: map[string]map[string]string{"m910q": {"AiPlayerbot.X": "0", "AiPlayerbot.DbPass": "s3cret"}}}
	rep := f.run(t, Deps{Conf: conf})
	res := findSubject(rep, CheckKeys, "m910q")
	if res == nil || res.Status != Fail || !strings.Contains(res.Detail, "AiPlayerbot.X") {
		t.Fatalf("key mismatch: %+v", find(rep, CheckKeys))
	}
	data, _ := json.Marshal(rep)
	if strings.Contains(string(data), "s3cret") {
		t.Fatal("a secret value was printed")
	}
	conf = fakeConf{keys: map[string]map[string]string{"m910q": {"AiPlayerbot.X": "1", "AiPlayerbot.DbPass": "s3cret"}}}
	rep = f.run(t, Deps{Conf: conf})
	if res := findSubject(rep, CheckKeys, "m910q"); res == nil || res.Status != OK {
		t.Fatalf("keys match: %+v", find(rep, CheckKeys))
	}
	os.Remove(f.keys)
	rep = f.run(t, Deps{Conf: conf})
	if res := findSubject(rep, CheckKeys, f.keys); res == nil || res.Status != Warn {
		t.Fatalf("missing keys file: %+v", find(rep, CheckKeys))
	}
	// The server cannot be read: a failure, not a silent OK.
	writeFile(t, f.keys, 0o600, `{"keys":{"AiPlayerbot.X":"1"}}`)
	rep = f.run(t, Deps{Conf: fakeConf{errs: map[string]error{"m910q": os.ErrPermission}}})
	if res := findSubject(rep, CheckKeys, "m910q"); res == nil || res.Status != Fail || !strings.Contains(res.Detail, "cannot read") {
		t.Fatalf("server unreadable: %+v", find(rep, CheckKeys))
	}
}

func roster(t *testing.T, path string, batches string) {
	t.Helper()
	writeFile(t, path, 0o600, `{"format":"tb-raid-roster","version":1,"level":60,"tier":2,"batches":[`+batches+`]}`)
}

func TestCheckRosters(t *testing.T) {
	f := newFixture(t)
	hire := filepath.Join(f.walk, "roster2.json")
	roster(t, hire, `{"role":"mt","hire":"1 warrior:tank"},{"role":"rest","hire":"5h 12d"}`)
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --hires "+hire+` --from "leg high_priestess_arlokk" --until none`))
	rep := f.run(t, Deps{})
	res := findSubject(rep, CheckRoster, hire)
	if res == nil || res.Status != Warn || !strings.Contains(res.Detail, "Arlokk") {
		t.Fatalf("2 tanks for Arlokk: %+v", find(rep, CheckRoster))
	}

	roster(t, hire, `{"role":"mt","hire":"1 warrior:tank"},{"role":"ot","hire":"2t"},{"role":"rest","hire":"5h 12d"}`)
	rep = f.run(t, Deps{})
	if res := findSubject(rep, CheckRoster, hire); res == nil || res.Status != OK {
		t.Fatalf("3 tanks: %+v", find(rep, CheckRoster))
	}

	os.Remove(hire)
	rep = f.run(t, Deps{})
	if !hasStatus(rep, CheckRoster, Fail) {
		t.Fatalf("missing roster: %+v", find(rep, CheckRoster))
	}

	// --until boss fights no boss, so the thin roster is not warned about.
	f2 := newFixture(t)
	roster(t, hire, `{"role":"mt","hire":"1 warrior:tank"},{"role":"rest","hire":"5h 12d"}`)
	f2.cmd(t, "cmd-a.txt", f2.cmdLine("--slot 1 --hires "+hire))
	rep = f2.run(t, Deps{})
	if res := findSubject(rep, CheckRoster, hire); res == nil || res.Status != OK {
		t.Fatalf("--until boss: %+v", find(rep, CheckRoster))
	}
}

func TestTargetsPairForm(t *testing.T) {
	cmd, err := parseCommand("/bin/turtle-agent raid-walk zul_gurub --server m910q --slot 2,4 --holder 4=h4 "+
		`--from 2="leg hakkar" --out /tmp/o --until none`, "cmd-x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd) != 2 {
		t.Fatalf("%d targets, want 2", len(cmd))
	}
	if cmd[0].slot != "2" || cmd[0].holder != "raid-walk-zul_gurub-s2" || cmd[0].out != "/tmp/o/s2-zul_gurub" {
		t.Fatalf("slot 2: %+v", cmd[0])
	}
	if cmd[1].slot != "4" || cmd[1].holder != "h4" || cmd[1].from != "" || cmd[1].out != "/tmp/o/s4-zul_gurub" {
		t.Fatalf("slot 4: %+v", cmd[1])
	}
	if cmd[0].from != "leg hakkar" {
		t.Fatalf("quoted --from: %q", cmd[0].from)
	}
}

func TestTargetsFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "targets.txt"), 0o644, `# id server slot holder goal -- args
zg1 m910q 1 zg1-a boss:14515 -- zul_gurub --sql /x.sql --until none
zg1 m910q 2 zg1-b full -- zul_gurub --sql /x.sql --until none
`)
	ts, errs := readTargets(dir)
	if len(errs) != 0 {
		t.Fatalf("errors: %+v", errs)
	}
	if len(ts) != 2 {
		t.Fatalf("%d targets, want 2", len(ts))
	}
	if ts[0].server != "m910q" || ts[0].slot != "1" || ts[0].holder != "zg1-a" || ts[0].goalBoss != 14515 {
		t.Fatalf("first target: %+v", ts[0])
	}
	if ts[1].goalBoss != 0 || ts[1].slot != "2" {
		t.Fatalf("second target: %+v", ts[1])
	}
}

// A practice goal is a targets.txt goal exactly as the autopilot reads it (practice:<ENTRY>:<CYCLES>): the live proof
// on walk 74's dir was refused with "want full, leg:<L> or boss:<ENTRY>" (round 2). The goal also decides the
// roster's fight scope: a practice run fights that boss, so a thin roster is warned about it like a boss goal.
func TestTargetsFilePracticeGoal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "targets.txt"), 0o644, `zg9 m910q 1 zg9-a practice:14834:10 -- zul_gurub --sql /x.sql
zg9 m910q 2 zg9-b practice:14834 -- zul_gurub --sql /x.sql
`)
	ts, errs := readTargets(dir)
	if len(ts) != 1 {
		t.Fatalf("%d targets, want 1: %+v", len(ts), errs)
	}
	if ts[0].goalBoss != 14834 || ts[0].goalLeg != "" || ts[0].slot != "1" {
		t.Fatalf("practice target: %+v", ts[0])
	}
	if len(errs) != 1 || !strings.Contains(errs[0].msg, "practice:<ENTRY>:<CYCLES>") {
		t.Fatalf("the malformed practice goal: %+v", errs)
	}

	f := newFixture(t)
	hire := filepath.Join(f.walk, "roster2.json")
	roster(t, hire, `{"role":"mt","hire":"1 warrior:tank"},{"role":"rest","hire":"5h 12d"}`)
	writeFile(t, filepath.Join(f.walk, "x.sql"), 0o644, "")
	writeFile(t, filepath.Join(f.walk, "targets.txt"), 0o644,
		"p m910q 2 p-h practice:14834:10 -- zul_gurub --sql "+filepath.Join(f.walk, "x.sql")+" --hires "+hire+"\n")
	rep := f.run(t, Deps{})
	if res := findSubject(rep, CheckRoster, hire); res == nil || res.Status != Warn || !strings.Contains(res.Detail, "Hakkar") {
		t.Fatalf("2 tanks for a Hakkar practice: %+v", find(rep, CheckRoster))
	}
}

func TestExitCodes(t *testing.T) {
	f := newFixture(t)
	f.cmd(t, "cmd-a.txt", f.cmdLine("--slot 1 --until none"))
	writeFile(t, f.keys, 0o600, `{"keys":{}}`)
	rep := f.run(t, Deps{})
	if rep.ExitCode() != 0 {
		t.Fatalf("want 0, got %d: %+v", rep.ExitCode(), rep.Results)
	}
	rep.Results = append(rep.Results, Result{Check: "x", Status: Warn})
	if rep.ExitCode() != 1 {
		t.Fatalf("want 1, got %d", rep.ExitCode())
	}
	rep.Results = append(rep.Results, Result{Check: "x", Status: Fail})
	if rep.ExitCode() != 2 {
		t.Fatalf("want 2, got %d", rep.ExitCode())
	}
}

func TestParseServerConf(t *testing.T) {
	out := "__FILE__ /home/pk/etc/aiplayerbot.conf\nAiPlayerbot.X = 1\n#AiPlayerbot.X = 0\n" +
		"__FILE__ /home/pk/etc/modules/tortoise_bots.conf\n__MISSING__\n"
	keys, files := parseServerConf(out)
	if files != 1 {
		t.Fatalf("files = %d, want 1", files)
	}
	if keys["AiPlayerbot.X"] != "1" {
		t.Fatalf("keys: %+v", keys)
	}
}

func TestConfLines(t *testing.T) {
	text := `[AiPlayerbotConf]
#AiPlayerbot.X = 4
AiPlayerbot.X = 39
AiPlayerbot.Y = "on"
; AiPlayerbot.Z = 1
`
	keys := ParseConfLines(text)
	if keys["AiPlayerbot.X"] != "39" || keys["AiPlayerbot.Y"] != "on" {
		t.Fatalf("effective keys: %+v", keys)
	}
	if _, ok := keys["AiPlayerbot.Z"]; ok {
		t.Fatal("a ; comment became a key")
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
