package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/slots"
)

// scoutFakeAgent is the scout's GM on a fake test server: it queues the module's and the toolkit's replies.
type scoutFakeAgent struct {
	t     *testing.T
	clock *fakeClock

	mu     sync.Mutex
	gm     []string
	tt     []string
	events []events.Event
	seq    uint64
	status session.StatusInfo

	gmOn      bool // answer `.gm on` with "GM mode is ON"
	cfg       bool // answer `.bot scout cfg`
	toolkit   bool // answer `q inst`
	walkReply bool // answer `.bot scout walk`
	// instCalls returns the instance id of the n-th `q inst` (1-based).
	instCalls func(n int) float64
	instN     int
	// dropFirst path queries dropped before any answer (the retry proves the pipeline); dropAt keys are points.
	dropFirst int
	dropAt    map[string]int
	pathSeen  []string
	// holdFirstReply keeps the first path answer back and sends it after the next one (the ids must still match).
	holdFirstReply bool
	held           []string
	// units is the `q units` answer (nil: an empty list).
	units map[string]any
	// losIn is the `q los` answer per guid.
	losIn map[string]bool
	// walkStop is where `.bot scout where` stands still (default: the walk's target).
	walkStop       [3]float32
	walkStopSet    bool
	walkTarget     [3]float32
	walkMovingOnce bool
	// farLimit (yd, 0 off) refuses a `q units` or `q los` whose point is further from the GM, as the toolkit does (point_far).
	farLimit float64
	// refuse refuses the next `q units` or `q los` as point_far whatever its point (0 off).
	refuse int
	// noAck answers a `.go xyz` with the new position but no TeleportSeq bump (the server's teleport ack never comes).
	noAck bool
	// trace is every GM and toolkit line in the order sent ("gm ..." or "tt ...").
	trace []string
}

func newScoutFakeAgent(t *testing.T, clock *fakeClock) *scoutFakeAgent {
	return &scoutFakeAgent{t: t, clock: clock, gmOn: true, cfg: true, toolkit: true, walkReply: true,
		status:    session.StatusInfo{State: session.StatusInWorld, Map: 1, Character: "Scout"},
		instCalls: func(int) float64 { return 1 }, losIn: map[string]bool{}}
}

// queue adds one system event carrying line as its raw protocol line.
func (a *scoutFakeAgent) queue(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queueLocked(line, line)
}

func (a *scoutFakeAgent) queueText(text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queueLocked("", text)
}

// queueLocked queues one system event; the caller holds the lock (Call answers a gm line under it).
func (a *scoutFakeAgent) queueLocked(raw, text string) {
	a.seq++
	e := events.Event{Seq: a.seq, Kind: events.KindSystem, Time: a.clock.now(), Text: text}
	if raw != "" {
		e.Data = map[string]any{"raw": raw}
	}
	a.events = append(a.events, e)
}

func (a *scoutFakeAgent) Call(_ context.Context, method string, params, out any, _ time.Duration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch method {
	case "status":
		st := a.status
		if p, ok := out.(*session.StatusInfo); ok {
			*p = st
		}
	case "gm":
		text := ""
		if m, ok := params.(map[string]string); ok {
			text = m["text"]
		}
		a.gm = append(a.gm, text)
		a.trace = append(a.trace, "gm "+text)
		a.gmCall(text)
	case "tt":
		var cmd string
		if p, ok := params.(ttParams); ok {
			cmd = p.Cmd
		}
		a.tt = append(a.tt, cmd)
		a.trace = append(a.trace, "tt "+cmd)
		if err := a.farCheck(cmd); err != nil {
			return err
		}
		res := a.ttCall(cmd)
		if p, ok := out.(*ttResult); ok {
			*p = res
		}
	case "events":
		p, ok := params.(map[string]any)
		if !ok {
			return nil
		}
		since, _ := p["since"].(uint64)
		var page events.Page
		for _, e := range a.events {
			if e.Seq > since {
				page.Events = append(page.Events, e)
			}
		}
		page.Next = since
		if len(page.Events) > 0 {
			page.Next = page.Events[len(page.Events)-1].Seq
		}
		if q, ok := out.(*events.Page); ok {
			*q = page
		}
	default:
		a.t.Fatalf("unexpected RPC %q", method)
	}
	return nil
}

// gmCall answers one GM line the way the module would.
func (a *scoutFakeAgent) gmCall(line string) {
	f := strings.Fields(line)
	switch {
	case line == ".gm on":
		if a.gmOn {
			a.queueLocked("", "GM mode is ON")
		} else {
			a.queueLocked("", "GM mode is OFF")
		}
	case line == raidwalk.GMVisibleOff:
	case len(f) == 6 && f[0] == ".go" && f[1] == "xyz":
		var x, y, z float32
		var m uint32
		fmt.Sscan(f[2], &x)
		fmt.Sscan(f[3], &y)
		fmt.Sscan(f[4], &z)
		fmt.Sscan(f[5], &m)
		a.status.Map, a.status.X, a.status.Y, a.status.Z = m, x, y, z
		if !a.noAck {
			a.status.TeleportSeq++ // the server's teleport ack, counted by the session
		}
	case len(f) >= 4 && f[0] == ".bot" && f[1] == "scout":
		switch f[2] {
		case "cfg":
			if a.cfg {
				a.queueLocked("TBM:SCOUT_CFG|"+strings.TrimPrefix(f[3], "#")+"|150.00|1.00|10.00", "TBM:SCOUT_CFG")
			}
		case "path":
			id := strings.TrimPrefix(f[3], "#")
			var x, y, z float32
			fmt.Sscan(f[4], &x)
			fmt.Sscan(f[5], &y)
			fmt.Sscan(f[6], &z)
			key := fmt.Sprintf("%.2f %.2f %.2f", x, y, z)
			a.pathSeen = append(a.pathSeen, key)
			if a.dropFirst > 0 {
				a.dropFirst--
				return
			}
			if a.dropAt[key] > 0 {
				a.dropAt[key]--
				return
			}
			line := fmt.Sprintf("TBM:SCOUT_PATH|%s|ok|0x1|10.00|%.2f %.2f %.2f|0.00|%.2f|0|0", id, x, y, z, z)
			if a.holdFirstReply {
				a.holdFirstReply = false
				a.held = append(a.held, line)
				return
			}
			a.queueLocked(line, line)
			for _, h := range a.held {
				a.queueLocked(h, h)
			}
			a.held = nil
		case "walk":
			id := strings.TrimPrefix(f[3], "#")
			var x, y, z float32
			fmt.Sscan(f[4], &x)
			fmt.Sscan(f[5], &y)
			fmt.Sscan(f[6], &z)
			a.walkTarget = [3]float32{x, y, z}
			if a.walkReply {
				a.queueLocked("TBM:SCOUT_WALK|"+id+"|started|0x1", "TBM:SCOUT_WALK")
			}
		case "where":
			id := strings.TrimPrefix(f[3], "#")
			stop := a.walkTarget
			if a.walkStopSet {
				stop = a.walkStop
			}
			moving := 0
			if a.walkMovingOnce {
				moving = 1
				a.walkMovingOnce = false
			}
			line := fmt.Sprintf("TBM:SCOUT_POS|%s|%.2f %.2f %.2f|%d|0|0", id, stop[0], stop[1], stop[2], moving)
			a.queueLocked(line, line)
		}
	}
}

// farCheck refuses a `q units` or `q los` read the way the toolkit does: a point more than farLimit yd from the GM, or any
// read while refuse is set, is a point_far error (the rpc error text the scout matches).
func (a *scoutFakeAgent) farCheck(cmd string) error {
	f := strings.Fields(cmd)
	if len(f) < 2 || (f[1] != "units" && f[1] != "los") {
		return nil
	}
	if a.refuse > 0 {
		a.refuse--
		return fmt.Errorf("rpc error -32003: eluna: point_far: point is 200 yd from you (max 150): .go xyz there first")
	}
	if a.farLimit <= 0 {
		return nil
	}
	off := 2
	if f[1] == "units" {
		off = 3
	}
	if len(f) < off+3 {
		return nil
	}
	var p [3]float64
	for i := range p {
		fmt.Sscan(f[off+i], &p[i])
	}
	d := math.Sqrt(math.Pow(p[0]-float64(a.status.X), 2) + math.Pow(p[1]-float64(a.status.Y), 2) + math.Pow(p[2]-float64(a.status.Z), 2))
	if d > a.farLimit {
		return fmt.Errorf("rpc error -32003: eluna: point_far: point is %.0f yd from you (max %.0f): .go xyz there first", d, a.farLimit)
	}
	return nil
}

// ttCall answers one toolkit command.
func (a *scoutFakeAgent) ttCall(cmd string) ttResult {
	f := strings.Fields(cmd)
	switch {
	case cmd == "q inst":
		if !a.toolkit {
			return ttResult{}
		}
		a.instN++
		return ttReply(map[string]any{"inst": a.instCalls(a.instN)})
	case len(f) >= 2 && f[1] == "units":
		if a.units == nil {
			return ttReply(map[string]any{"units": []any{}, "n": 0})
		}
		return ttReply(a.units)
	case len(f) >= 2 && f[1] == "los":
		var los []any
		for _, g := range f[5:] {
			los = append(los, map[string]any{"g": g, "in": a.losIn[g], "d": 1.0})
		}
		return ttReply(map[string]any{"los": los, "n": len(los)})
	}
	return ttResult{}
}

func ttReply(v any) ttResult {
	raw, _ := json.Marshal(v)
	return ttResult{Replies: []json.RawMessage{raw}}
}

// scoutFakeEnv is the slot environment: setup writes a slot manifest, teardown counts.
type scoutFakeEnv struct {
	t             *testing.T
	clock         *fakeClock
	agent         *scoutFakeAgent
	dir           string
	teardownCalls int
	releases      []string
	acquireSlot   string // lease slot ("" default "1")
	setupFails    bool
	bigSleep      bool // every sleep advances the clock by 40 s (the time ceiling test)
	nowCalls      int
}

func (e *scoutFakeEnv) walkEnv() walkEnv {
	now := func() time.Time { return e.clock.now() }
	sleep := func(ctx context.Context, d time.Duration) error {
		if e.bigSleep {
			d = 40 * time.Second
		}
		return e.clock.sleep(ctx, d)
	}
	slot := e.acquireSlot
	if slot == "" {
		slot = "1"
	}
	lease := slots.Lease{Server: "tortoise-test2", Slot: slot, Holder: "plan-scout-zul_gurub", LeaseID: "abc",
		Acquired: now(), Expires: now().Add(time.Hour)}
	return walkEnv{
		now:        now,
		sleep:      sleep,
		startAgent: func(teardownSlotManifest) (int, error) { return 1234, nil },
		acquire: func(server, want string, any bool, holder string, ttl time.Duration) (slots.Acquired, error) {
			if any {
				lease.Slot = slot
			} else {
				lease.Slot = want
			}
			lease.Holder = holder
			return slots.Acquired{Lease: lease}, nil
		},
		renew: func(server, slot, holder, leaseID string, ttl time.Duration) (slots.Lease, error) { return lease, nil },
		release: func(server, slot, holder, leaseID string, force bool) (slots.Lease, error) {
			e.releases = append(e.releases, slot)
			return lease, nil
		},
		setup: func(args []string, stdout, stderr io.Writer) int {
			if e.setupFails {
				fmt.Fprint(stdout, "slot setup failed")
				return 1
			}
			e.writeManifest(args)
			return exitOK
		},
		teardown: func(args []string, stdout, stderr io.Writer) int {
			e.teardownCalls++
			fmt.Fprint(stdout, `{"failed":0}`)
			return exitOK
		},
		capLedger: e.dir + "/instance-cap.json",
	}
}

// writeManifest writes the slot.json slot setup would leave (0600, derived paths).
func (e *scoutFakeEnv) writeManifest(args []string) {
	dir := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--dir" {
			dir = args[i+1]
		}
	}
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	port := 4700
	m := teardownSlotManifest{Server: "tortoise-test2", Slot: "1", Holder: "plan-scout-zul_gurub", LeaseID: "abc",
		Character: "Scout", Port: port, Dir: dir, Settings: filepath.Join(dir, "settings.json"),
		PIDFile: filepath.Join(dir, "agent.pid"), Binary: filepath.Join(dir, "turtle-agent"),
		TokenFile: filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port))}
	b, _ := json.Marshal(m)
	writePrivate(e.t, filepath.Join(dir, "slot.json"), b)
	writePrivate(e.t, m.Settings, []byte(`{"realmd":"127.0.0.1:3724","server_name":"tortoise-test2","world_addr":"127.0.0.1:8085","accounts":{"Fixture":"fixture-pw"}}`))
	writePrivate(e.t, m.PIDFile, []byte("1234"))
	writePrivate(e.t, m.Binary, []byte("#!/bin/sh\n"))
	writePrivate(e.t, m.TokenFile, []byte("fixture-token"))
}

func writePrivate(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(path, 0o600)
}

// useScoutLive swaps the live environment for a fake one.
func useScoutLive(t *testing.T, env *scoutFakeEnv) {
	t.Helper()
	old := planScoutLiveEnv
	planScoutLiveEnv = func(*planScoutOptions) (walkEnv, walkDeps, error) {
		deps := walkDeps{openAgent: func(dir string) (walkAgent, teardownSlotManifest, error) {
			m, err := readWalkManifest(dir)
			if err != nil {
				return nil, teardownSlotManifest{}, err
			}
			return env.agent, m, nil
		}}
		return env.walkEnv(), deps, nil
	}
	t.Cleanup(func() { planScoutLiveEnv = old })
}

// writeLivePlan writes a plan with a portal and nPulls pull groups 200 yd apart.
func writeLivePlan(t *testing.T, dir, name string, nPulls int) string {
	t.Helper()
	groups := make([]dungeon.PullGroup, nPulls)
	for i := range groups {
		x := float32(60 + i*200)
		groups[i] = dungeon.PullGroup{Entries: []uint32{100}, Positions: []dungeon.Point{{X: x}},
			Approach: []dungeon.Point{{X: x - 10}}}
	}
	p := &dungeon.Plan{Version: 1, Name: name, Map: 1, Kind: "dungeon",
		Entrance: dungeon.Point{X: 40},
		Portal:   &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 10}},
		Legs:     []dungeon.Leg{{Name: "one", To: dungeon.Point{X: 40}, PullGroups: groups}}}
	b, err := dungeon.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeSlotsFile writes a private slots table naming the test server.
func writeSlotsFile(t *testing.T, dir string) string {
	t.Helper()
	_ = os.Chmod(dir, 0o700)
	cfg := map[string]any{"version": 1, "servers": []any{map[string]any{"name": "tortoise-test2",
		"realmd": "127.0.0.1:3724", "world_addr": "127.0.0.1:8085", "slots": []any{}}}}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "slots.json")
	writePrivate(t, path, b)
	return path
}

// scoutLiveArgs is the command line of a live run on the fake.
func scoutLiveArgs(dir, name, slotsPath, out string, extra ...string) []string {
	args := []string{"plan-scout", name, "--dir", dir, "--mmaps", dir, "--out", out, "--slots", slotsPath,
		"--live", "tortoise-test2"}
	return append(args, extra...)
}

// liveFixture is one prepared live run.
type liveFixture struct {
	dir, name, slotsPath, out string
	env                       *scoutFakeEnv
	agent                     *scoutFakeAgent
}

func newLiveFixture(t *testing.T, nPulls int) *liveFixture {
	t.Helper()
	return newFixtureWithPlan(t, "scout_live", func(dir, name string) { writeLivePlan(t, dir, name, nPulls) })
}

// newFixtureWithPlan is a live fixture whose plan write makes the plan in dir.
func newFixtureWithPlan(t *testing.T, name string, write func(dir, name string)) *liveFixture {
	t.Helper()
	useScoutNet(t)
	dir := t.TempDir()
	write(dir, name)
	slotsPath := writeSlotsFile(t, dir)
	clock := newFakeClock()
	agent := newScoutFakeAgent(t, clock)
	env := &scoutFakeEnv{t: t, clock: clock, agent: agent, dir: dir}
	useScoutLive(t, env)
	return &liveFixture{dir: dir, name: name, slotsPath: slotsPath, out: filepath.Join(dir, "out"), env: env, agent: agent}
}

func (f *liveFixture) run(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	args := scoutLiveArgs(f.dir, f.name, f.slotsPath, f.out, extra...)
	return runCLI(args...)
}

func (f *liveFixture) report(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.out, "scout-"+f.name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep map[string]any
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

// 19. Slot 3 is refused from the flag and from a lease; a non-test server is refused before anything is sent.
func TestPlanScoutSlot3Refused(t *testing.T) {
	f := newLiveFixture(t, 1)
	if code, _, _ := f.run(t, "--slot", "3"); code != exitSlotRefused {
		t.Fatalf("--slot 3: code %d", code)
	}
	f.env.acquireSlot = "3"
	if code, _, errOut := f.run(t); code != exitSlotRefused || !strings.Contains(errOut, "slot 3") {
		t.Fatalf("lease slot 3: code %d, %q", code, errOut)
	}
	if len(f.env.releases) != 1 || f.env.teardownCalls != 0 {
		t.Fatalf("released %v, teardown %d", f.env.releases, f.env.teardownCalls)
	}
	if len(f.agent.gm) != 0 {
		t.Fatalf("nothing may be sent: %v", f.agent.gm)
	}
}

func TestPlanScoutNonTestServerRefused(t *testing.T) {
	f := newLiveFixture(t, 1)
	args := scoutLiveArgs(f.dir, f.name, f.slotsPath, f.out)
	for i := range args {
		if args[i] == "tortoise-test2" {
			args[i] = "live-server"
		}
	}
	code, _, errOut := runCLI(args...)
	if code != exitLivePhaseRefused || !strings.Contains(errOut, "not a test server") {
		t.Fatalf("code %d, %q", code, errOut)
	}
	if len(f.agent.gm) != 0 || len(f.agent.tt) != 0 || f.env.teardownCalls != 0 {
		t.Fatalf("nothing may be opened: gm %v tt %v teardown %d", f.agent.gm, f.agent.tt, f.env.teardownCalls)
	}
}

// 20. The allow-list refuses the dangerous lines, and a whole live run sends only allowed ones.
func TestPlanScoutAllowList(t *testing.T) {
	allowed := []string{".gm on", ".gm visible off", ".go xyz 1 2 3 1", ".go xyz 1 2 3 2",
		".instance unbind 1", ".bot scout cfg #1", ".bot scout where #12", ".bot scout walk #3 1.00 2.00 3.00",
		".bot scout path #4 1 2 3", ".bot scout path #5 1 2 3 from 4 5 6"}
	for _, l := range allowed {
		if !scoutAllowedLine(l, 1, 2) {
			t.Fatalf("refused %q", l)
		}
	}
	refused := []string{".die", ".die 0x1", ".tele name Scout aq20", ".tele Scout", ".bot run start", ".bot cmd open 1",
		".go xyz 1 2 3 3", ".go xyz 1 2 3 99", ".instance unbind 2", ".appear Scout", ".summon Scout",
		".bot scout cfg", ".bot scout cfg 12", ".bot scout path #1 1 2", ".bot scout path #1 1 2 3 from 4 5",
		".bot scout walk #1 1 2 NaN", ".gm visible on", ".bot hire raid 1 level=60"}
	for _, l := range refused {
		if scoutAllowedLine(l, 1, 2) {
			t.Fatalf("accepted %q", l)
		}
	}
	for _, c := range []string{"q inst", "q units 35 -12000.50 -1510.00 64.70 all", "q units 60 1 2 3 all",
		"q los 1 2 3 0xF130000000000001", "q los 1 2 3 " + strings.Repeat("0xF130000000000001 ", 7) + "0xF130000000000001"} {
		if !scoutAllowedTT(c, 1) {
			t.Fatalf("refused tt %q", c)
		}
	}
	for _, c := range []string{"q inst 1", "q units 61 1 2 3 all", "q units 35 1 2 3",
		"q los 1 2 3", "q los 1 2 3 " + strings.Repeat("0xF130000000000001 ", 8) + "0xF130000000000001",
		"q los 1 2 3 nope", "quiet 40 1 2 3 w=5 dz=8", "q units 35 1 2 3 all dead", "reset"} {
		if scoutAllowedTT(c, 1) {
			t.Fatalf("accepted tt %q", c)
		}
	}
	// A full live run sends nothing outside the list.
	f := newLiveFixture(t, 1)
	if code, _, errOut := f.run(t); code != exitOK && code != exitPlanScoutFail {
		t.Fatalf("live run: code %d, %q", code, errOut)
	}
	if len(f.agent.gm) == 0 || len(f.agent.tt) == 0 {
		t.Fatalf("no lines sent: gm %v tt %v", f.agent.gm, f.agent.tt)
	}
	for _, l := range f.agent.gm {
		if !scoutAllowedLine(l, 1, 1) {
			t.Fatalf("sent a GM line outside the list: %q", l)
		}
	}
	for _, c := range f.agent.tt {
		if !scoutAllowedTT(c, 1) {
			t.Fatalf("sent a toolkit line outside the list: %q", c)
		}
	}
	for _, l := range f.agent.gm {
		if strings.Contains(l, ".die") || strings.Contains(l, ".tele ") || strings.Contains(l, ".bot run") ||
			strings.Contains(l, ".bot cmd") {
			t.Fatalf("sent a forbidden line: %q", l)
		}
	}
}

// 21. No GM mode (or no module, or no toolkit) refuses the run (18) before entering.
func TestPlanScoutGMModeRequired(t *testing.T) {
	f := newLiveFixture(t, 1)
	f.agent.gmOn = false
	if code, _, errOut := f.run(t); code != exitLivePhaseRefused || !strings.Contains(errOut, "GM mode") {
		t.Fatalf("code %d, %q", code, errOut)
	}
}

func TestPlanScoutNeedsModuleAndToolkit(t *testing.T) {
	f := newLiveFixture(t, 1)
	f.agent.cfg = false
	if code, _, errOut := f.run(t); code != exitLivePhaseRefused || !strings.Contains(errOut, ".bot scout") {
		t.Fatalf("module: code %d, %q", code, errOut)
	}
	f = newLiveFixture(t, 1)
	f.agent.toolkit = false
	f.agent.instCalls = func(int) float64 { return 0 }
	f.agent.cfg = true
	if code, _, errOut := f.run(t); code != exitLivePhaseRefused || !strings.Contains(errOut, "Eluna") {
		t.Fatalf("toolkit: code %d, %q", code, errOut)
	}
}

// 22. At the instance cap the scout waits (or exits 26 with --instance-cap-fail); a reservation is released on a refused run.
func TestPlanScoutInstanceCap(t *testing.T) {
	f := newLiveFixture(t, 1)
	fillCap(t, f.env.dir, 5)
	f.env.agent.cfg = false // refuse after the reservation: the release must be visible
	code, _, errOut := f.run(t, "--instance-cap-wait", "1ms")
	if code != exitLivePhaseRefused {
		t.Fatalf("wait then refuse: code %d, %q", code, errOut)
	}
	if n := capReservations(t, f.env.dir); n != 0 {
		t.Fatalf("%d reservation(s) left", n)
	}
	// Waited past the cap, then went on: the reservation became the scout's entry.
	f = newLiveFixture(t, 1)
	fillCap(t, f.env.dir, 5)
	if code, _, errOut := f.run(t, "--instance-cap-wait", "1ms"); code != exitOK && code != exitPlanScoutFail {
		t.Fatalf("wait then go: code %d, %q", code, errOut)
	}
	if n := capEntries(t, f.env.dir, "scout_live"); n != 1 {
		t.Fatalf("the scout's ledger entry: %d", n)
	}

	f = newLiveFixture(t, 1)
	fillCap(t, f.env.dir, 5)
	if code, _, errOut := f.run(t, "--instance-cap-fail"); code != exitRaidWalkInstanceCap || !strings.Contains(errOut, "instance cap") {
		t.Fatalf("--instance-cap-fail: code %d, %q", code, errOut)
	}
	if len(f.agent.gm) != 0 || f.env.teardownCalls != 0 {
		t.Fatalf("nothing may start: gm %v teardown %d", f.agent.gm, f.env.teardownCalls)
	}
}

func fillCap(t *testing.T, dir string, n int) {
	t.Helper()
	now := newFakeClock().now()
	if _, err := raidwalk.UpdateCapLedger(dir+"/instance-cap.json", func(l *raidwalk.CapLedger) {
		for i := range n {
			l.Confirm("tortoise-test2", "filler", raidwalk.CapEntry{At: now.Add(-time.Duration(i) * time.Minute), Plan: "filler"}, now)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func capEntries(t *testing.T, dir, plan string) int {
	t.Helper()
	n := 0
	if _, err := raidwalk.UpdateCapLedger(dir+"/instance-cap.json", func(l *raidwalk.CapLedger) {
		if s := l.Servers["tortoise-test2"]; s != nil {
			for _, e := range s.Entries {
				if e.Plan == plan {
					n++
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func capReservations(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	if _, err := raidwalk.UpdateCapLedger(dir+"/instance-cap.json", func(l *raidwalk.CapLedger) {
		if s := l.Servers["tortoise-test2"]; s != nil {
			n = len(s.Reservations)
		}
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// 23. Path probes are pipelined by id: a dropped reply is retried once. A reply lost twice leaves the live pass incomplete (28),
// the abort reason names the request and its step, and the hop is queued for the walk pass (walked, budget permitting).
func TestPlanScoutPipelinesPathProbes(t *testing.T) {
	f := newLiveFixture(t, 2)
	f.agent.dropFirst = 1
	f.agent.holdFirstReply = true
	f.agent.dropAt = map[string]int{"250.00 0.00 0.00": 2}
	code, _, errOut := f.run(t)
	if code != exitPlanScoutLiveAborted {
		t.Fatalf("code %d, want %d (a lost path reply is an incomplete live pass): %q", code, exitPlanScoutLiveAborted, errOut)
	}
	if !regexp.MustCompile(`path request\(s\) unanswered after the retry: #\d+ \(step \d+\)`).MatchString(errOut) {
		t.Fatalf("the reason does not name the unanswered request and its step: %q", errOut)
	}
	rep := f.report(t)
	if !hasCheck(rep, "live_path", "skip") {
		t.Fatal("no live_path skip after the lost reply")
	}
	if !hasCheck(rep, "live_path", "ok") {
		t.Fatal("the retried reply did not land")
	}
	queued := false
	for _, line := range f.agent.gm {
		if strings.HasPrefix(line, ".bot scout walk #") && strings.HasSuffix(line, " 250 0 0") {
			queued = true
		}
	}
	if !queued {
		t.Fatalf("the unanswered hop was not queued for the walk pass: %v", f.agent.gm)
	}
}

// hasCheck walks the report's steps for one check verdict.
func hasCheckAny(rep map[string]any, verdict string) bool {
	steps, _ := rep["steps"].([]any)
	for _, raw := range steps {
		st, _ := raw.(map[string]any)
		checks, _ := st["checks"].([]any)
		for _, cr := range checks {
			c, _ := cr.(map[string]any)
			if c["verdict"] == verdict {
				return true
			}
		}
	}
	return false
}

func hasCheck(rep map[string]any, name, verdict string) bool {
	steps, _ := rep["steps"].([]any)
	for _, raw := range steps {
		st, _ := raw.(map[string]any)
		checks, _ := st["checks"].([]any)
		for _, cr := range checks {
			c, _ := cr.(map[string]any)
			if c["name"] == name && c["verdict"] == verdict {
				return true
			}
		}
	}
	return false
}

// 24. A reached time ceiling and a changed instance both write a partial report (28) and tear the slot down.
func TestPlanScoutAbortWritesPartial(t *testing.T) {
	f := newLiveFixture(t, 3)
	f.env.bigSleep = true
	if code, _, errOut := f.run(t, "--max-minutes", "1"); code != exitPlanScoutLiveAborted || !strings.Contains(errOut, "time ceiling") {
		t.Fatalf("ceiling: code %d, %q", code, errOut)
	}
	if f.env.teardownCalls != 1 {
		t.Fatalf("teardown %d", f.env.teardownCalls)
	}
	if !hasCheckAny(f.report(t), "skip") {
		t.Fatal("the partial report has no live skip")
	}

	f = newLiveFixture(t, 26)
	f.agent.instCalls = func(n int) float64 {
		if n > 2 {
			return 7
		}
		return 1
	}
	if code, _, errOut := f.run(t); code != exitPlanScoutLiveAborted || !strings.Contains(errOut, "instance changed") {
		t.Fatalf("instance: code %d, %q", code, errOut)
	}
	if f.env.teardownCalls != 1 {
		t.Fatalf("teardown %d", f.env.teardownCalls)
	}
}

// 25. scout.log carries the commands and replies, never the slot's secrets.
func TestPlanScoutLogHasNoSecrets(t *testing.T) {
	f := newLiveFixture(t, 1)
	if code, _, errOut := f.run(t); code != exitOK && code != exitPlanScoutFail {
		t.Fatalf("code %d, %q", code, errOut)
	}
	b, err := os.ReadFile(filepath.Join(f.out, scoutLogName))
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	for _, secret := range []string{"fixture-pw", "fixture-token", "server_name"} {
		if strings.Contains(log, secret) {
			t.Fatalf("scout.log carries %q:\n%s", secret, log)
		}
	}
	for _, want := range []string{"> .gm on", "> .bot scout cfg", "TBM:SCOUT_CFG", "> .tt q inst"} {
		if !strings.Contains(log, want) {
			t.Fatalf("scout.log lacks %q:\n%s", want, log)
		}
	}
}

// writeChainPlan writes a plan with one leg whose pull groups stand at xs (x, y 0): the scout's stops chain over them.
func writeChainPlan(t *testing.T, dir, name string, xs []float32) {
	t.Helper()
	groups := make([]dungeon.PullGroup, len(xs))
	for i, x := range xs {
		groups[i] = dungeon.PullGroup{Entries: []uint32{100}, Positions: []dungeon.Point{{X: x}},
			Approach: []dungeon.Point{{X: x - 10}}}
	}
	p := &dungeon.Plan{Version: 1, Name: name, Map: 1, Kind: "dungeon",
		Entrance: dungeon.Point{X: xs[0] - 20},
		Portal:   &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 10}},
		Legs:     []dungeon.Leg{{Name: "one", To: dungeon.Point{X: xs[len(xs)-1]}, PullGroups: groups}}}
	b, err := dungeon.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// lastTeleportBefore is the point of the last `.go xyz` sent before trace[i] ("x y z"), or "" when none was.
func lastTeleportBefore(trace []string, i int) string {
	for k := i - 1; k >= 0; k-- {
		if f := strings.Fields(trace[k]); len(f) == 7 && f[0] == "gm" && f[1] == ".go" {
			return strings.Join(f[3:6], " ")
		}
	}
	return ""
}

// 26. A stop's spots chain past the toolkit's read reach (195 yd from the stop's first spot): the GM goes to each far spot
// before its reads, so no read is refused and the live pass does not stop (28). Every `q units` read stands within 120 yd
// of the GM's last teleport.
func TestPlanScoutSpotsChainTeleportBeforeFarReads(t *testing.T) {
	f := newFixtureWithPlan(t, "scout_chain", func(dir, name string) { writeChainPlan(t, dir, name, []float32{0, 98, 195}) })
	f.agent.farLimit = 150
	code, _, errOut := f.run(t)
	if code == exitPlanScoutLiveAborted {
		t.Fatalf("the live pass stopped (28): %q", errOut)
	}
	reads, farTeleports := 0, 0
	for i, line := range f.agent.trace {
		if !strings.HasPrefix(line, "tt q units ") {
			continue
		}
		reads++
		at := strings.Fields(line)[4:7]
		goAt := lastTeleportBefore(f.agent.trace, i)
		if goAt == "" {
			t.Fatalf("read %q with no teleport before it", line)
		}
		gf := strings.Fields(goAt)
		var g, r [3]float64
		for k := range g {
			fmt.Sscan(gf[k], &g[k])
			fmt.Sscan(at[k], &r[k])
		}
		if d := math.Sqrt(math.Pow(g[0]-r[0], 2) + math.Pow(g[1]-r[1], 2) + math.Pow(g[2]-r[2], 2)); d > scoutReadReachYd {
			t.Fatalf("read %q is %.0f yd from the last teleport %q: the GM did not go to the spot", line, d, goAt)
		}
		if strings.Join(at, " ") == strings.Join(gf, " ") {
			farTeleports++
		}
	}
	if reads < 3 {
		t.Fatalf("%d q units reads, want one per spot (3)", reads)
	}
	if farTeleports == 0 {
		t.Fatalf("no spot was reached by its own teleport: %v", f.agent.trace)
	}
}

// 27. A read the toolkit refuses as point_far costs one teleport to its point and one retry; the live pass goes on.
func TestPlanScoutPointFarRetriesOnce(t *testing.T) {
	f := newLiveFixture(t, 1)
	f.agent.refuse = 1
	code, _, errOut := f.run(t)
	if code == exitPlanScoutLiveAborted {
		t.Fatalf("the live pass stopped (28) on one point_far: %q", errOut)
	}
	i := -1
	for k, line := range f.agent.trace {
		if strings.HasPrefix(line, "tt q units ") {
			i = k
			break
		}
	}
	if i < 0 || i+2 >= len(f.agent.trace) {
		t.Fatalf("no toolkit read after the start: %v", f.agent.trace)
	}
	read := f.agent.trace[i]
	if !strings.HasPrefix(f.agent.trace[i+1], "gm .go xyz ") {
		t.Fatalf("the refused read %q is not followed by a teleport: next %q", read, f.agent.trace[i+1])
	}
	if f.agent.trace[i+2] != read {
		t.Fatalf("the refused read %q was not sent again after the teleport: next %q", read, f.agent.trace[i+2])
	}
	want := "gm .go xyz " + strings.Join(strings.Fields(read)[4:7], " ") + " 1" // the plan's map is 1
	if f.agent.trace[i+1] != want {
		t.Fatalf("the teleport %q is not to the refused read's point (want %q)", f.agent.trace[i+1], want)
	}
}

// 28. A teleport the server does not ack (no TeleportSeq move) ends the live pass (28) instead of reading from the wrong place.
func TestPlanScoutTeleportNeedsAck(t *testing.T) {
	f := newFixtureWithPlan(t, "scout_chain", func(dir, name string) { writeChainPlan(t, dir, name, []float32{0, 98, 195}) })
	f.agent.farLimit = 150
	f.agent.noAck = true
	code, _, errOut := f.run(t)
	if code != exitPlanScoutLiveAborted || !strings.Contains(errOut, "no teleport ack") {
		t.Fatalf("code %d, %q: want 28 with the missing ack named", code, errOut)
	}
}
