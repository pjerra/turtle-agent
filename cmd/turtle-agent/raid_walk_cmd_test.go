package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/slots"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The fakes below stand in for the slot agent, the dashboard, the clock and the lease, so every phase runs
// without a network or a process (spec 3.9 A3).

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return ctx.Err()
}

// queuedEvent is one TBM line the fake events RPC delivers; text is what a colour-stripped event carries.
type queuedEvent struct {
	raw  string
	text string
}

type timelineEntry struct {
	Text     string
	Targeted bool
}

type walkFakeAgent struct {
	t     *testing.T
	clock *fakeClock

	mu        sync.Mutex
	calls     []string
	gm        []string
	gmUnits   []int
	timeline  []timelineEntry
	party     []string
	nameSeq   int
	startArgs string

	entered  bool
	status   session.StatusInfo
	lines    []queuedEvent
	next     uint64
	viewN    int
	dead     map[string]bool
	targeted bool

	hostilesFn func(viewN int) []tbm.HostileRec
	unitsFn    func(n int) []objects.Unit
	unitsN     int

	startReply *commanderReply
	startQueue []queuedEvent
	bindLayout string
	enterReply *commanderReply
	onGM       func(line string)

	// pos is where the commander's last .go put it; the quiet-check fakes place units around it.
	x, y, z float32

	// hireCap is the server's hire limit (0: raidwalk.HireCap, the 39 of onyxia-kill-plan C6).
	hireCap int
}

func newFakeAgent(t *testing.T, clock *fakeClock) *walkFakeAgent {
	return &walkFakeAgent{t: t, clock: clock, dead: map[string]bool{},
		status: session.StatusInfo{State: session.StatusInWorld, Map: 1, Character: "Commander"}}
}

func (f *walkFakeAgent) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.mu.Unlock()
	var v any
	switch method {
	case "status":
		f.mu.Lock()
		v = f.status
		f.mu.Unlock()
	case "party":
		f.mu.Lock()
		members := make([]session.PartyMember, 0, len(f.party))
		for i, n := range f.party {
			members = append(members, session.PartyMember{Name: n, GUID: packet.GUID(1000 + i), Online: true})
		}
		f.mu.Unlock()
		v = session.PartyInfo{InGroup: len(members) > 0, Members: members}
	case "gm":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.gm = append(f.gm, p.Text)
		f.gmUnits = append(f.gmUnits, f.unitsN)
		f.timeline = append(f.timeline, timelineEntry{Text: p.Text, Targeted: f.targeted})
		onGM := f.onGM
		f.mu.Unlock()
		f.gmSide(p.Text)
		if onGM != nil {
			onGM(p.Text)
		}
		v = struct{}{}
	case "events":
		v = f.eventsPage()
	case "bot_view":
		v = f.viewPage()
	case "units":
		f.mu.Lock()
		f.unitsN++
		n := f.unitsN
		fn := f.unitsFn
		f.mu.Unlock()
		if fn == nil {
			v = []objects.Unit{}
		} else {
			v = fn(n)
		}
	case "commander":
		var p commanderReq
		_ = json.Unmarshal(raw, &p)
		v = f.commander(&p)
	default:
		f.t.Errorf("unexpected RPC %s", method)
	}
	if out != nil {
		b, _ := json.Marshal(v)
		if err := json.Unmarshal(b, out); err != nil {
			f.t.Errorf("%s result: %v", method, err)
		}
	}
	return nil
}

// gmSide is what the game does for a GM line.
func (f *walkFakeAgent) gmSide(line string) {
	switch {
	case strings.HasPrefix(line, ".bot hire raid "):
		hire := strings.TrimPrefix(line, ".bot hire raid ")
		if i := strings.Index(hire, " level="); i >= 0 {
			hire = hire[:i]
		}
		n := hireCount(hire)
		f.mu.Lock()
		cap := f.hireCap
		if cap == 0 {
			cap = raidwalk.HireCap
		}
		if room := cap - len(f.party); n > room {
			n = room // the server's hire cap: it hires what fits and no more
		}
		names := make([]string, 0, n)
		for range n {
			f.nameSeq++
			names = append(names, fmt.Sprintf("Bot%02d", f.nameSeq))
		}
		f.party = append(f.party, names...)
		f.mu.Unlock()
		f.push("TBM:HIRE_RAID|ok|"+strconv.Itoa(n)+"|"+strconv.Itoa(n)+"|0|0|0|3|0|", "")
	case strings.HasPrefix(line, ".bot gear raid "):
		f.push("TBM:GEAR|ok|39|39|3|60|-|-|-|0|-|0", "")
	case strings.HasPrefix(line, ".go xyz "):
		fields := strings.Fields(line)
		if len(fields) == 6 {
			x, _ := strconv.ParseFloat(fields[2], 32)
			y, _ := strconv.ParseFloat(fields[3], 32)
			z, _ := strconv.ParseFloat(fields[4], 32)
			m, err := strconv.Atoi(fields[5])
			f.mu.Lock()
			f.x, f.y, f.z = float32(x), float32(y), float32(z)
			if err == nil {
				f.status.Map = uint32(m)
			}
			f.mu.Unlock()
		}
	}
}

// hireCount reads the bot count out of a hire grammar.
func hireCount(hire string) int {
	n := 0
	for _, tok := range strings.Fields(hire) {
		if strings.HasPrefix(tok, "t") && len(tok) == 2 {
			continue // the tier word t1/t2/t3
		}
		i := 0
		for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
			i++
		}
		if i > 0 {
			c, _ := strconv.Atoi(tok[:i])
			n += c
		}
	}
	return n
}

func (f *walkFakeAgent) push(raw, text string) {
	f.mu.Lock()
	f.lines = append(f.lines, queuedEvent{raw: raw, text: text})
	f.mu.Unlock()
}

func (f *walkFakeAgent) eventsPage() events.Page {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.lines) == 0 {
		return events.Page{Next: f.next}
	}
	q := f.lines[0]
	f.lines = f.lines[1:]
	f.next++
	ev := events.Event{Seq: f.next, Kind: events.KindSystem, Text: q.raw, Data: map[string]any{"raw": q.raw}}
	if q.text != "" {
		ev.Text = q.text
	}
	return events.Page{Events: []events.Event{ev}, Next: f.next}
}

type viewReply struct {
	Have  bool           `json:"have"`
	N     uint64         `json:"n"`
	Frame *tbm.ViewFrame `json:"frame"`
}

func (f *walkFakeAgent) viewPage() any {
	f.mu.Lock()
	f.viewN++
	n := f.viewN
	m := uint32(1)
	if f.entered {
		m = 249
	}
	party := append([]string{}, f.party...)
	dead := map[string]bool{}
	for k, v := range f.dead {
		dead[k] = v
	}
	fn := f.hostilesFn
	f.mu.Unlock()
	frame := &tbm.ViewFrame{Seq: uint64(n)}
	for i, name := range party {
		state := "n"
		if dead[name] {
			state = "d"
		}
		frame.Party = append(frame.Party, tbm.PartyRec{GUID: packet.GUID(1000 + i), Name: name, HP: 1, MaxHP: 1, Map: m, Dead: dead[name], State: state})
	}
	if fn != nil {
		frame.Hostiles = fn(n)
	}
	return viewReply{Have: true, N: uint64(n), Frame: frame}
}

func (f *walkFakeAgent) commander(p *commanderReq) commanderReply {
	ack := func(intent string) commanderReply {
		f.mu.Lock()
		count := len(f.party)
		f.mu.Unlock()
		return commanderReply{Sent: ".bot " + intent, Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: intent, Scope: "party", Count: count, Executor: "-"},
			Line:  "TBM:ACTION_ACK|" + intent + "|party|" + strconv.Itoa(count) + "|-",
			Lines: []string{"TBM:ACTION_ACK|" + intent + "|party|" + strconv.Itoa(count) + "|-"}}}
	}
	switch p.Op {
	case "bind":
		layout := f.bindLayout
		if layout == "" {
			f.mu.Lock()
			mt := "Bot01"
			if len(f.party) > 0 {
				mt = f.party[0]
			}
			n := len(f.party)
			f.mu.Unlock()
			layout = "TBM:RAID_LAYOUT|" + strconv.Itoa(n) + "|" + mt + "|" + mt + "|Bot02|"
		}
		f.push(layout, "")
		return ack("commander bind")
	case "view":
		return ack("commander view")
	case "cmd":
		if p.Verb == "enter" && f.enterReply != nil {
			return *f.enterReply
		}
		if p.Verb == "enter" {
			f.mu.Lock()
			f.entered = true
			f.mu.Unlock()
		}
		return ack("cmd " + p.Verb)
	case "run":
		if p.Verb == "start" {
			f.mu.Lock()
			f.startArgs = strings.Join(p.Args, "|")
			q := f.startQueue
			f.startQueue = nil
			f.mu.Unlock()
			for _, e := range q {
				f.push(e.raw, e.text)
			}
			if f.startReply != nil {
				return *f.startReply
			}
			rep := ack("run start")
			run := "TBM:RUN|" + p.Args[0] + "|running|1/29|onyxia|move|walk|walking|until boss (step 29)"
			rep.Reply.Lines = append([]string{run}, rep.Reply.Lines...)
			return rep
		}
		if p.Verb == "status" {
			rep := ack("commander status")
			f.mu.Lock()
			names := strings.Join(f.party, ", ")
			f.mu.Unlock()
			rep.Reply.Lines = append([]string{fmt.Sprintf("You command Bot01's group: %s.", names)}, rep.Reply.Lines...)
			return rep
		}
		return ack("run " + p.Verb)
	}
	return ack(p.Op)
}

func (f *walkFakeAgent) gmLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.gm...)
}

// fakeDash is the dashboard client.
type fakeDash struct {
	mu        sync.Mutex
	fights    []walkFightRow
	fightKeys []string
}

func (d *fakeDash) Login(ctx context.Context) error { return nil }

func (d *fakeDash) Commanders(ctx context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"commanders":[]}`), nil
}

func (d *fakeDash) Fights(ctx context.Context, commander string) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	raw, err := json.Marshal(d.fights)
	return raw, err
}

func (d *fakeDash) Fight(ctx context.Context, key string) (json.RawMessage, error) {
	d.mu.Lock()
	d.fightKeys = append(d.fightKeys, key)
	d.mu.Unlock()
	return json.RawMessage(`{"fight":{"id":"4-5"}}`), nil
}

// envRecord is what the fake environment saw.
type envRecord struct {
	acquires  int
	releases  int
	setupArgs []string
	teardown  []string
	started   int
}

// fixture is one raid walk with fakes.
type fixture struct {
	t      *testing.T
	clock  *fakeClock
	agent  *walkFakeAgent
	dash   *fakeDash
	rec    *envRecord
	env    walkEnv
	deps   walkDeps
	opts   raidWalkOptions
	out    string
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("TBDASH_URL", "")
	t.Setenv("TBDASH_PASS_FILE", "")
	t.Setenv("TBDASH_USER", "")
	clock := newFakeClock()
	agent := newFakeAgent(t, clock)
	rec := &envRecord{}
	out := filepath.Join(t.TempDir(), "out")
	f := &fixture{t: t, clock: clock, agent: agent, dash: &fakeDash{}, rec: rec, out: out}
	f.opts = raidWalkOptions{
		plan: "onyxias_lair", server: "m910q", holder: "raid-walk-onyxias_lair", dir: "../../dungeons",
		until: "boss", speed: 1.0, enter: "auto", maxRunMinutes: 90, pausedWait: 10 * time.Minute,
		resetWait: 5 * time.Minute, out: out,
	}
	f.env = walkEnv{
		now:   clock.now,
		sleep: clock.sleep,
		startAgent: func(m teardownSlotManifest) (int, error) {
			rec.started++
			return 4242, nil
		},
		acquire: func(server, slot string, any bool, holder string, ttl time.Duration) (slots.Acquired, error) {
			rec.acquires++
			if slot == "" {
				slot = "2"
			}
			now := clock.now()
			return slots.Acquired{Lease: slots.Lease{Server: server, Slot: slot, Holder: holder, LeaseID: "lease-1", Acquired: now, Expires: now.Add(ttl)},
				Slot: slots.Slot{ID: slot}}, nil
		},
		renew: func(server, slot, holder, leaseID string, ttl time.Duration) (slots.Lease, error) {
			return slots.Lease{}, nil
		},
		release: func(server, slot, holder, leaseID string, force bool) (slots.Lease, error) {
			rec.releases++
			return slots.Lease{}, nil
		},
		setup: func(args []string, stdout, stderr io.Writer) int {
			rec.setupArgs = append([]string{}, args...)
			dir := argValue(args, "--dir")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				f.t.Fatal(err)
			}
			m := teardownSlotManifest{Server: args[0], Slot: args[1], Holder: argValue(args, "--holder"), Character: "Commander",
				Port: 1234, Dir: dir, Binary: filepath.Join(dir, "turtle-agent"), Settings: filepath.Join(dir, "settings.json"),
				PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: filepath.Join(dir, "rpc-1234.token")}
			raw, _ := json.Marshal(m)
			if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
				f.t.Fatal(err)
			}
			fmt.Fprintf(stdout, "slot %s/%s ready in %s\n", args[0], args[1], dir)
			return 0
		},
		teardown: func(args []string, stdout, stderr io.Writer) int {
			rec.teardown = append([]string{}, args...)
			fmt.Fprint(stdout, `{"server":"m910q","slot":"2","steps":[],"failed":0}`)
			return 0
		},
	}
	f.deps = walkDeps{
		openAgent: func(dir string) (walkAgent, teardownSlotManifest, error) {
			m, err := readWalkManifest(dir)
			return agent, m, err
		},
		openDash: func(url, user, passFile string) (walkDash, error) { return f.dash, nil },
	}
	return f
}

func argValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// run performs the walk.
func (f *fixture) run(ctx context.Context) int {
	return raidWalkRun(ctx, f.opts, f.env, f.deps, &f.stdout, &f.stderr)
}

// summary reads <out>/summary.json.
func (f *fixture) summary() raidwalk.Summary {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.out, "summary.json"))
	if err != nil {
		f.t.Fatalf("summary.json: %v (stdout %q, stderr %q)", err, f.stdout.String(), f.stderr.String())
	}
	var s raidwalk.Summary
	if err := json.Unmarshal(raw, &s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

// defaultRun queues the run lines of a walk that stops before Onyxia (2/29 .. 28/29, then the until done); they
// arrive once the run start has been answered, as on the server.
func (f *fixture) defaultRun(plan string) {
	var q []queuedEvent
	for step := 2; step <= 28; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|%s|running|%d/29|onyxia|move|walk|walking", plan, step)})
	}
	q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|%s|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands", plan)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
}

// startAt is a start reply whose first RUN line is at step with the until label.
func startAt(plan string, step int) *commanderReply {
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{
			fmt.Sprintf("TBM:RUN|%s|running|%d/29|onyxia|move|walk|walking|until boss (step 29)", plan, step),
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
}

func TestRaidWalkRefusesSlot3(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := raidWalkCmd(context.Background(), []string{"--slot", "3", "onyxias_lair"}, &stdout, &stderr)
	if code != exitSlotRefused {
		t.Fatalf("--slot 3: exit %d, want %d (stderr %q)", code, exitSlotRefused, stderr.String())
	}
	if !strings.Contains(stderr.String(), "never uses slot 3") {
		t.Fatalf("the refusal does not name the rule: %q", stderr.String())
	}
}

func TestRaidWalkDryRunPrintsThePhasePlan(t *testing.T) {
	f := newFixture(t)
	f.opts.dryRun = true
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("dry run exit %d: %s", code, f.stderr.String())
	}
	if f.rec.acquires != 0 || f.rec.setupArgs != nil {
		t.Fatalf("the dry run touched the lease or the slot: %+v", f.rec)
	}
	if _, err := os.Stat(f.out); !os.IsNotExist(err) {
		t.Fatalf("the dry run created the out dir")
	}
	got := f.stdout.String()
	want := `raid-walk onyxias_lair: dry run, nothing is sent
plan ../../dungeons/onyxias_lair.json, map 249, 1 legs, 7 steps, raid 40
roster 40 bots: mt 1 warrior:tank, ot 3t, rest 10h 26d, level 60, tier 2 (table)
until boss; from -; enter auto; server m910q slot any holder raid-walk-onyxias_lair
out ` + f.out + `
phases:
  1 validate: plan, roster, until, 7 steps
  2 lease: m910q any --holder raid-walk-onyxias_lair --ttl 120m
  3 slot setup: slot setup m910q <slot> --holder raid-walk-onyxias_lair --dir ` + f.out + `/slot --binary <bin> --dungeons-from ../../dungeons
  4 login: ` + f.out + `/slot/turtle-agent login --config ` + f.out + `/slot/settings.json --pid-file ` + f.out + `/slot/agent.pid <commander>
  5 gm: .go xyz -4768.61 -3752.11 53.4317 1
  6 gm: .bot hire raid 1 warrior:tank level=60 t2
  6 gm: .bot hire raid 3t level=60 t2
  6 gm: .bot hire raid 10h 26d level=60 t2
  6 party: before and after every batch (the new names are the mt, ot and rest)
  6 hire cap: 39 (the server hires 39 of the 40 asked; the summary notes it)
  6 gm: .bot gear raid <commander> tier 2
  7 commander: bind <hire> raid
  7 commander: view on
  8 commander: cmd enter near
  9 commander: run start onyxias_lair until boss
 10 watch: events 5s, bot_view 2s, dashboard off (no --dash-url/--dash-pass-file), ceiling 90m, paused wait 10m0s, auto-resume 0
 11 hold: 0m
 12 commander: status; teardown --json ` + f.out + `/slot
 13 summary: ` + f.out + `/summary.json, ` + f.out + `/summary.txt
`
	if got != want {
		t.Fatalf("dry-run plan:\n%s\nwant:\n%s", got, want)
	}
}

func TestRaidWalkHappyPathOnyxia(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.EndReason != "until" || sum.StepsReached != 28 || sum.StepsTotal != 29 {
		t.Fatalf("summary end %q steps %d/%d", sum.EndReason, sum.StepsReached, sum.StepsTotal)
	}
	if sum.ExitCode != exitOK || sum.Teardown.Failed {
		t.Fatalf("summary exit %d teardown %+v", sum.ExitCode, sum.Teardown)
	}
	if len(f.rec.teardown) != 2 || f.rec.teardown[0] != "--json" || f.rec.teardown[1] != filepath.Join(f.out, "slot") {
		t.Fatalf("teardown args %q", f.rec.teardown)
	}
	gm := f.agent.gmLines()
	if len(gm) < 5 || !strings.HasPrefix(gm[0], ".go xyz ") || !strings.HasPrefix(gm[1], ".bot hire raid 1 warrior:tank") ||
		!strings.HasPrefix(gm[4], ".bot gear raid Commander tier 2") {
		t.Fatalf("GM lines out of order: %q", gm)
	}
	hires := map[string]bool{}
	f.agent.mu.Lock()
	for _, n := range f.agent.party {
		hires[n] = true
	}
	f.agent.mu.Unlock()
	for _, line := range gm {
		if !raidwalk.AllowedLine(line, hires) {
			t.Fatalf("a GM line fails the allow-list: %q", line)
		}
	}
	if f.agent.startArgs != "onyxias_lair|until|boss" {
		t.Fatalf("run start args %q", f.agent.startArgs)
	}
	if !strings.Contains(f.stdout.String(), "raid-walk onyxias_lair: until; steps 28/29") {
		t.Fatalf("one-line result: %q", f.stdout.String())
	}
	// The tier of the committed table, in the summary and in every dump's meta (amendment G1).
	if sum.Roster.Tier != 2 || sum.Roster.TierSource != raidwalk.TierSourceTable || sum.Roster.TierComparison {
		t.Fatalf("summary roster tier %d %q comparison %v, want 2 table false",
			sum.Roster.Tier, sum.Roster.TierSource, sum.Roster.TierComparison)
	}
	if !hasNote(sum, "hire cap 39: asked for 40 bots, the server hired 39") {
		t.Fatalf("summary notes %q, want the hire cap note", sum.Notes)
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Tier int `json:"tier"`
	}
	if err := json.Unmarshal(bytes.SplitN(raw, []byte("\n"), 2)[0], &frame); err != nil || frame.Tier != 2 {
		t.Fatalf("frames.jsonl tier = %d (%v), want 2", frame.Tier, err)
	}
	text, err := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "tier 2 (table)\n") {
		t.Fatalf("summary.txt has no tier line: %q", text)
	}
}

// hasNote says whether the summary carries a note holding text.
func hasNote(s raidwalk.Summary, text string) bool {
	for _, n := range s.Notes {
		if strings.Contains(n, text) {
			return true
		}
	}
	return false
}

// TestRaidWalkReadsDeployedOnyxiaMigration is the smoke of the deployed migrations (B1's lenient reader): the
// ai_dungeon_boss block beside the rows is skipped with a note, and the rows are the walk's (1 leg, 29 steps).
func TestRaidWalkReadsDeployedOnyxiaMigration(t *testing.T) {
	for _, c := range []struct {
		file string
		z26  float32
	}{
		{"onyxias_lair-20261029120000.sql", -74.62397},
		{"onyxias_lair-20261029130000.sql", -66.7},
	} {
		path := filepath.Join("../../internal/planexport/testdata", c.file)
		rows, notes, err := readRaidMigration(path, "onyxias_lair")
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if len(rows.Legs) != 1 || len(rows.Steps) != 29 {
			t.Fatalf("%s: %d legs, %d steps, want 1 and 29", c.file, len(rows.Legs), len(rows.Steps))
		}
		if z := rows.Steps[25].Z; z != c.z26 {
			t.Fatalf("%s: step 26 z %v, want %v", c.file, z, c.z26)
		}
		if len(notes) != 1 || !strings.HasPrefix(notes[0], "skipped ai_dungeon_boss:") {
			t.Fatalf("%s: notes %q, want the skipped boss block", c.file, notes)
		}
		if _, _, err := readRaidMigration(path, "molten_core"); err == nil || !strings.Contains(err.Error(), "no plan") {
			t.Fatalf("%s as molten_core: %v, want the no-plan refusal", c.file, err)
		}
	}
}

// TestRaidWalkUsesTheDeployedMigration runs the walk with --sql and carries the reader's notes into the summary.
func TestRaidWalkUsesTheDeployedMigration(t *testing.T) {
	f := newFixture(t)
	f.opts.sql = "../../internal/planexport/testdata/onyxias_lair-20261029130000.sql"
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if sum.StepsTotal != 29 {
		t.Fatalf("steps total %d, want 29 from the migration", sum.StepsTotal)
	}
	if !hasNote(sum, "skipped ai_dungeon_boss") {
		t.Fatalf("summary notes %q, want the skipped-table note", sum.Notes)
	}
	if hasNote(sum, "step points from the plan JSON") {
		t.Fatalf("a walk with --sql used the plan JSON: %q", sum.Notes)
	}
}

// TestRaidWalkReadsTbPatrolsFile reads a real D1 file (numeric spawn_guids, period, waits): the fallback reader of
// the patrols format cannot, internal/patrols can.
func TestRaidWalkReadsTbPatrolsFile(t *testing.T) {
	doc := `{"format":"tb-patrols","version":1,"map":249,"source":"record","created":"2026-10-29T10:00:00Z",
 "area":{"x":-100,"y":-150,"r":120},"minutes":15,"sample_ms":1000,
 "patrols":[{"id":"52050","guids":["0xF130002F61000ABC"],"spawn_guids":[52050],"entries":[12129],"name":"Onyxian Warder",
   "path":[{"x":49.8,"y":-114.6,"z":-24.3},{"x":30,"y":-114.6,"z":-24.3}],"waits_ms":[0,2000],"period_s":33.4,
   "period_estimate":false,"cycles_seen":3,"phase_epoch_ms":1793223608123,"complete":true,"samples":900,
   "source":"record m910q 2026-10-29"}],
 "static":[],"wanderers":[],"stopped":""}`
	path := filepath.Join(t.TempDir(), "patrols-249.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	patrols, guids, mapID, err := readPatrolsFile(path)
	if err != nil {
		t.Fatalf("a real tb-patrols file: %v", err)
	}
	if mapID != 249 || len(patrols) != 1 || len(patrols[0].Path) != 2 || patrols[0].ID != "52050" {
		t.Fatalf("patrols %+v map %d", patrols, mapID)
	}
	for _, key := range []string{"0XF130002F61000ABC", "52050", "0X000000000000CB52"} {
		if !guids[0][key] {
			t.Errorf("the guid set does not hold %q: %v", key, guids[0])
		}
	}

	// The reader's own refusals reach the walk (a patrol path with one point).
	short := `{"format":"tb-patrols","version":1,"map":249,"patrols":[{"id":"52050",
 "path":[{"x":49.8,"y":-114.6,"z":-24.3}],"period_s":33.4}]}`
	shortPath := filepath.Join(t.TempDir(), "short.json")
	if err := os.WriteFile(shortPath, []byte(short), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readPatrolsFile(shortPath); err == nil || !strings.Contains(err.Error(), "need 2") {
		t.Fatalf("a one-point path: %v, want the reader's refusal", err)
	}

	f := newFixture(t)
	f.opts.dryRun = true
	f.opts.patrols = path
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("dry run with --patrols: exit %d, %s", code, f.stderr.String())
	}

	// A file of another map is a usage error (the plan's map check stays in the walk).
	other := strings.Replace(doc, `"map":249`, `"map":1`, 1)
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := f.run(context.Background()); code != exitUsage {
		t.Fatalf("--patrols of map 1: exit %d, want %d", code, exitUsage)
	}
}

// TestRaidWalkTierFlags: --tier fills a raid the table leaves unknown, --tier-override marks a comparison, and both
// are refused where they do not apply.
func TestRaidWalkTierFlags(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		f := newFixture(t)
		f.opts.tierOverride = 3
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		sum := f.summary()
		if sum.Roster.Tier != 3 || sum.Roster.TierSource != raidwalk.TierSourceOverride || !sum.Roster.TierComparison {
			t.Fatalf("roster %+v, want tier 3 by --tier-override and the comparison mark", sum.Roster)
		}
		if !hasNote(sum, "comparison, not a tier proof") {
			t.Fatalf("notes %q, want the comparison mark", sum.Notes)
		}
		want := ".bot gear raid Commander tier 3"
		if !strings.Contains(strings.Join(f.agent.gmLines(), "\n"), want) {
			t.Fatalf("no refit %q in %q", want, f.agent.gmLines())
		}
	})
	t.Run("unknown raid without a tier", func(t *testing.T) {
		f := newFixture(t)
		f.opts.plan, f.opts.holder, f.opts.dryRun = "molten_core", "raid-walk-molten_core", true
		if code := f.run(context.Background()); code != exitUsage {
			t.Fatalf("exit %d, want 2 (stderr %q)", code, f.stderr.String())
		}
		if !strings.Contains(f.stderr.String(), "--tier") {
			t.Fatalf("the refusal does not name --tier: %q", f.stderr.String())
		}
	})
	t.Run("unknown raid with --tier", func(t *testing.T) {
		f := newFixture(t)
		f.opts.plan, f.opts.holder, f.opts.dryRun = "molten_core", "raid-walk-molten_core", true
		f.opts.tier = 1
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if !strings.Contains(f.stdout.String(), "tier 1 (--tier)") {
			t.Fatalf("the dry run does not name the flag tier: %q", f.stdout.String())
		}
	})
	t.Run("--tier on a known raid", func(t *testing.T) {
		f := newFixture(t)
		f.opts.tier, f.opts.dryRun = 3, true
		if code := f.run(context.Background()); code != exitUsage {
			t.Fatalf("exit %d, want 2", code)
		}
		if !strings.Contains(f.stderr.String(), "--tier-override") {
			t.Fatalf("the refusal does not offer --tier-override: %q", f.stderr.String())
		}
	})
	t.Run("both flags", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := raidWalkCmd(context.Background(), []string{"--tier", "1", "--tier-override", "2", "onyxias_lair"}, &stdout, &stderr)
		if code != exitUsage || !strings.Contains(stderr.String(), "not both") {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
	})
	t.Run("out of range", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := raidWalkCmd(context.Background(), []string{"--tier", "4", "molten_core"}, &stdout, &stderr)
		if code != exitUsage || !strings.Contains(stderr.String(), "1, 2 or 3") {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
	})
}

// TestRaidWalkShortHireIsAnError: below the server's cap a short hire is a failed walk, not a note.
func TestRaidWalkShortHireIsAnError(t *testing.T) {
	f := newFixture(t)
	f.agent.hireCap = 30
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "asked for 40 bots, the party has 30") {
		t.Fatalf("summary end %q", sum.EndReason)
	}
	if hasNote(f.summary(), "hire cap 39") {
		t.Fatalf("a short hire below the cap was noted away: %q", f.summary().Notes)
	}
}

func TestRaidWalkRefusesAServerWithoutUntil(t *testing.T) {
	t.Run("err", func(t *testing.T) {
		f := newFixture(t)
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindErr, OK: false,
			Err: &tbm.Err{Intent: "run start", Code: "invalid", Message: "unknown word until"}, Line: "TBM:ACTION_ERR|run start|invalid|unknown word until"}}
		code := f.run(context.Background())
		if code != exitRaidWalkFailed {
			t.Fatalf("exit %d, want 18", code)
		}
		if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
			t.Fatalf("%d run stops after a refused start", n)
		}
		if !strings.Contains(f.summary().EndReason, "has no `until`") {
			t.Fatalf("the refusal does not name run-until: %q", f.summary().EndReason)
		}
	})
	t.Run("no label", func(t *testing.T) {
		f := newFixture(t)
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
			Line: "TBM:ACTION_ACK|run start|party|1|-",
			Lines: []string{
				"TBM:RUN|onyxias_lair|running|1/29|onyxia|move|walk|walking",
				"TBM:ACTION_ACK|run start|party|1|-",
			}}}
		code := f.run(context.Background())
		if code != exitRaidWalkFailed {
			t.Fatalf("exit %d, want 18", code)
		}
		if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
			t.Fatalf("%d run stops, want exactly 1", n)
		}
		if !strings.Contains(f.summary().EndReason, "has no `until`") {
			t.Fatalf("summary reason %q", f.summary().EndReason)
		}
	})
}

func countPrefix(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func TestRaidWalkReadsRawLinesNotStrippedText(t *testing.T) {
	f := newFixture(t)
	var q []queuedEvent
	for step := 2; step <= 28; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|running|%d/29|onyxia|move|walk|walking", step)})
	}
	// The walk-3 lesson (spec 0.1): this event's text lost the "|r" of the plan name, the raw line did not.
	q = append(q, queuedEvent{
		raw:  "TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands",
		text: "TBM:RUN|onyxias_lairunning|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands",
	})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if sum := f.summary(); sum.EndReason != "until" || sum.StepsReached != 28 {
		t.Fatalf("the walk did not read the raw done line: end %q steps %d", sum.EndReason, sum.StepsReached)
	}
}

// allDeadFromHires makes every hire dead once the roster has been hired.
func (f *fixture) allDeadFromHires() {
	f.agent.onGM = func(line string) {
		if !strings.HasPrefix(line, ".bot hire raid ") {
			return
		}
		f.agent.mu.Lock()
		if len(f.agent.party) >= 39 {
			for _, n := range f.agent.party {
				f.agent.dead[n] = true
			}
		}
		f.agent.mu.Unlock()
	}
}

func TestRaidWalkWipeRevivesOnlyWhenQuiet(t *testing.T) {
	run := func(t *testing.T, neverQuiet bool) (*fixture, raidwalk.Summary) {
		f := newFixture(t)
		f.agent.startReply = startAt("onyxias_lair", 23)
		f.agent.unitsFn = func(n int) []objects.Unit {
			if neverQuiet || n <= 3 {
				f.agent.mu.Lock()
				x, y, z := f.agent.x, f.agent.y, f.agent.z
				f.agent.mu.Unlock()
				return []objects.Unit{{Type: "creature", Name: "Onyxian Warder", Hostile: true, Moving: true, X: x + 60, Y: y, Z: z}}
			}
			return []objects.Unit{}
		}
		f.allDeadFromHires()
		code := f.run(context.Background())
		if code != exitRaidWalkIncident {
			t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		return f, f.summary()
	}
	t.Run("quiet after three checks", func(t *testing.T) {
		f, sum := run(t, false)
		gm := f.agent.gmLines()
		firstSummon, firstRevive := -1, -1
		for i, line := range gm {
			if firstSummon < 0 && strings.HasPrefix(line, ".summon ") {
				firstSummon = i
			}
			if firstRevive < 0 && strings.HasPrefix(line, ".revive ") {
				firstRevive = i
			}
		}
		if firstSummon < 0 || firstRevive < 0 || firstSummon > firstRevive {
			t.Fatalf("no summon/revive in order: %q", gm)
		}
		f.agent.mu.Lock()
		unitsAtSummon := f.agent.gmUnits[firstSummon]
		f.agent.mu.Unlock()
		if unitsAtSummon < 3 {
			t.Fatalf("the first summon came after %d units calls, before the warder left", unitsAtSummon)
		}
		if sum.EndReason != "wipe at step 23" || sum.ExitCode != exitRaidWalkIncident {
			t.Fatalf("summary end %q exit %d", sum.EndReason, sum.ExitCode)
		}
	})
	t.Run("never quiet", func(t *testing.T) {
		f, sum := run(t, true)
		if n := countPrefix(f.agent.gmLines(), ".summon "); n != 0 {
			t.Fatalf("%d summons while the warder never left", n)
		}
		if !strings.Contains(sum.EndReason, "not revived") {
			t.Fatalf("summary end %q", sum.EndReason)
		}
	})
}

func TestRaidWalkNeverRevivesWhileTheBossHasATarget(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		targeted := n < 8
		f.agent.mu.Lock()
		f.agent.targeted = targeted
		f.agent.mu.Unlock()
		if targeted {
			return []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100, Target: packet.GUID(1000)}}
		}
		return []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100}}
	}
	f.allDeadFromHires()
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s", code, f.stderr.String())
	}
	f.agent.mu.Lock()
	timeline := append([]timelineEntry{}, f.agent.timeline...)
	f.agent.mu.Unlock()
	revives := 0
	for _, e := range timeline {
		if strings.HasPrefix(e.Text, ".revive ") {
			revives++
			if e.Targeted {
				t.Fatalf("a .revive went out while the boss row still had a target")
			}
		}
	}
	if revives == 0 {
		t.Fatalf("the wipe at the boss was never revived after the reset")
	}
}

func TestRaidWalkLayoutMainTankMismatchFails(t *testing.T) {
	f := newFixture(t)
	f.agent.bindLayout = "TBM:RAID_LAYOUT|39|Somebody,Bot02|Bot01|Bot02|"
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.summary().EndReason, "main tank is Somebody") {
		t.Fatalf("summary reason %q", f.summary().EndReason)
	}
	if len(f.rec.teardown) == 0 {
		t.Fatal("no teardown after the layout refusal")
	}
}

func TestRaidWalkDashboardOffStillWalks(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if sum := f.summary(); sum.Dashboard != "off (no --dash-url/--dash-pass-file)" {
		t.Fatalf("dashboard %q", sum.Dashboard)
	}
}

func TestRaidWalkSavesFightsOncePerKey(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "win", BossName: "Onyxia", Leg: "onyxia", Step: 3, Deaths: 1}}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if sum := f.summary(); sum.Dashboard != "on" || len(sum.Fights) != 1 {
		t.Fatalf("dashboard %q fights %+v", sum.Dashboard, sum.Fights)
	}
	for _, name := range []string{"combat-fights.json", "combat-fight-4-5.json"} {
		if _, err := os.Stat(filepath.Join(f.out, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(f.dash.fightKeys) != 1 || f.dash.fightKeys[0] != "Commander\x001793219890\x004-5" {
		t.Fatalf("fight details fetched %q", f.dash.fightKeys)
	}
	matches, _ := filepath.Glob(filepath.Join(f.out, "commanders-0001-walk.json"))
	if len(matches) != 1 {
		t.Fatalf("no commanders snapshot: %v", matches)
	}
}

func TestRaidWalkInterruptStopsRunAndTearsDown(t *testing.T) {
	f := newFixture(t)
	var q []queuedEvent
	for step := 2; step <= 28; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|running|%d/29|onyxia|move|walk|walking", step)})
	}
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n >= 3 {
			once.Do(cancel)
		}
		return nil
	}
	code := f.run(ctx)
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18", code)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops on an interrupt", n)
	}
	if len(f.rec.teardown) == 0 {
		t.Fatal("no teardown after the interrupt")
	}
	if sum := f.summary(); sum.EndReason != "interrupted" {
		t.Fatalf("summary end %q", sum.EndReason)
	}
}
