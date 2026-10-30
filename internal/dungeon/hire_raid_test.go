package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The raid hire flow (hire_raid.go): a raid plan runs raidHire against a fake
// game that behaves like the server for what the flow meets: `.bot hire raid`
// with its TBM:HIRE_RAID answer, hires that arrive in the commander's group,
// `.bot gear raid` with its TBM:GEAR verdict, `commander bind <Lead> raid` with
// its TBM:RAID_LAYOUT line, and `commander status` with the commander outside
// the bound raid.

type hireRaidFake struct {
	*enterFake
	tanks, healers, dps int
	tier                int
	names               []string
	nextName            int
	agentLvl            int
	agentName           string
	hires               []*fhire
	binding             []string // bound names, lead first; nil: not bound
	haveGroup           bool
	hireReply           string // overrides the answer to `.bot hire raid`
	gearPreset          string // what the fake answers `.bot gear raid` with: ok, partial, none
	raidErr             string // the bind's refusal code: raid-off, raid-not-raid, ...
	gearLines           []string
	layoutLine          string
	events              []string
	botCmds             []string
	arriveIn            time.Duration
}

func newHireRaidFake(t *testing.T, tanks, healers, dps, tier int) *hireRaidFake {
	e := newEnterFake(t)
	e.plan.LevelBand = []int{55, 60}
	g := &hireRaidFake{enterFake: e, tanks: tanks, healers: healers, dps: dps, tier: tier,
		agentLvl: 60, agentName: "Glacienne", gearPreset: "ok", arriveIn: 2 * time.Second,
		names: []string{"Rtank", "Rtankb", "Rheala", "Rhealb", "Rhealc", "Rdpsa", "Rdpsb", "Rdpsc", "Rdpsd", "Rdpse",
			"Rdpsf", "Rdpsg", "Rdpsh", "Rdpsi", "Rdpsj", "Rdpsk", "Rdpsl", "Rdpsm", "Rdpsn", "Rdpso"},
		layoutLine: "TBM:RAID_LAYOUT|raid|10|2|main:Rtank|subs:2"}
	g.agentMap = outsideMap
	return g
}

func (g *hireRaidFake) log(format string, a ...any) {
	g.events = append(g.events, fmt.Sprintf(format, a...))
}

func (g *hireRaidFake) idxOf(prefix string) int {
	for i, l := range g.events {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

func (g *hireRaidFake) byName(name string) *fhire {
	for _, h := range g.hires {
		if strings.EqualFold(h.name, name) {
			return h
		}
	}
	return nil
}

func (g *hireRaidFake) arrived(h *fhire) bool { return !g.now.Before(h.hiredAt.Add(g.arriveIn)) }

func (g *hireRaidFake) BotLines(_ context.Context, command string, _ time.Duration, done func(string) bool) ([]string, bool, error) {
	g.botCmds = append(g.botCmds, command)
	reply, handled := g.raidReply(command)
	if !handled {
		return nil, false, nil
	}
	var got []string
	for _, l := range reply {
		got = append(got, l)
		if done(l) {
			return got, true, nil
		}
	}
	return got, false, nil
}

func (g *hireRaidFake) BotBurst(_ context.Context, command string, _, _ time.Duration, begin func(string) bool) ([]string, bool, error) {
	reply, _ := g.raidReply(command)
	for i, l := range reply {
		if begin(l) {
			return reply[i:], true, nil
		}
	}
	return nil, false, nil
}

func (g *hireRaidFake) raidReply(command string) ([]string, bool) {
	f := strings.Fields(command)
	switch {
	case f[0] == "hire" && len(f) > 1 && f[1] == "raid":
		g.log("%s", command)
		if g.hireReply != "" {
			return []string{g.hireReply}, true
		}
		n := g.tanks + g.healers + g.dps
		for i := range n {
			class, role := "mage", "dps"
			switch {
			case i < g.tanks:
				class, role = "warrior", "tank"
			case i < g.tanks+g.healers:
				class, role = "priest", "healer"
			}
			h := &fhire{name: g.names[g.nextName], class: class, role: role, level: 60,
				hiredAt: g.now, equip: map[int]uint32{}, bag: map[uint32]int{}, inView: true,
				trees: hireTrees[class+":"+role]}
			g.nextName++
			g.hires = append(g.hires, h)
		}
		return []string{fmt.Sprintf("TBM:HIRE_RAID|ok|%d|%d|%d|%d|%d|%d|t%d", n, n, g.tanks, g.healers, g.dps, g.tier, g.tier)}, true
	case f[0] == "gear" && len(f) > 2 && f[1] == "raid":
		g.log("%s", command)
		g.gearLines = append(g.gearLines, command)
		n := g.tanks + g.healers + g.dps
		switch g.gearPreset {
		case "none":
			return []string{fmt.Sprintf("TBM:GEAR|none|0|%d|%d|63|-|-|-|0||0", n, 2)}, true
		case "partial":
			return []string{fmt.Sprintf("TBM:GEAR|partial|%d|%d|%d|63|60.0|-|60.0|0||0", n-1, n, 2)}, true
		default:
			return []string{fmt.Sprintf("TBM:GEAR|ok|%d|%d|%d|63|60.0|61.0|60.0|0||0", n, n, 2)}, true
		}
	case f[0] == "command" && len(f) == 3 && f[2] == "who":
		if h := g.byName(f[1]); h != nil {
			g.log("who %s", f[1])
			return []string{fmt.Sprintf("Forwarded command for %s: who; action success is not guaranteed.", h.name),
				fmt.Sprintf("|h|cffffffffspec (|h|cff00ff00%d|h|cffffffff/%d|h|cffffffff/|h|cff00ff00%d|h|cffffffff)|r %s (|h|cff00ff0060|h|cffffffff lvl), |h|cff00ff00812|h|cffffffff GS (|h|cff8080ff5|h|cffffffff), playing with Glacienne",
					h.trees[0], h.trees[1], h.trees[2], h.class)}, true
		}
	case f[0] == "command" && len(f) == 3 && f[2] == "talents":
		if h := g.byName(f[1]); h != nil {
			g.log("talents %s", f[1])
			return []string{fmt.Sprintf("Forwarded command for %s: talents; action success is not guaranteed.", h.name),
				fmt.Sprintf("My current talent spec is: |hspec (%d|h/%d|h/|h%d|h) Link: 0-0-1", h.trees[0], h.trees[1], h.trees[2])}, true
		}
	}
	return nil, false
}

func (g *hireRaidFake) AgentLevel() int   { return g.agentLvl }
func (g *hireRaidFake) AgentName() string { return g.agentName }
func (g *hireRaidFake) AgentGUID() packet.GUID {
	return packet.GUID(0x20D)
}
func (g *hireRaidFake) Sees(string) bool { return true }
func (g *hireRaidFake) GMVisible(_ context.Context, on bool) error {
	g.log("visible %v", on)
	return nil
}
func (g *hireRaidFake) UnbindInstances(_ context.Context, who string, _ packet.GUID) error {
	g.t.Errorf("the raid hire step sent .instance unbind all on %s", who)
	return nil
}
func (g *hireRaidFake) GroupUnbind(_ context.Context, who string, _ packet.GUID) (GroupUnbindResult, error) {
	g.t.Errorf("the raid hire step sent .instance groupunbind all on %s", who)
	return GroupNoAnswer, nil
}

func (g *hireRaidFake) Members() []HireMember {
	var out []HireMember
	for _, h := range g.hires {
		if g.arrived(h) {
			out = append(out, HireMember{Name: h.name, GUID: packet.GUID(0x900 + len(h.name) + int(h.name[0])), Level: h.level, Class: h.class, InGroup: true, Online: true, MaxHP: 2100})
		}
	}
	return out
}

func (g *hireRaidFake) Player(name string) (HireMember, bool) {
	for _, m := range g.Members() {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	if h := g.byName(name); h != nil && g.arrived(h) {
		return HireMember{Name: h.name, Level: h.level, Class: h.class, Online: true, MaxHP: 2100}, true
	}
	return HireMember{}, false
}

func (g *hireRaidFake) PetOf(packet.GUID) (string, bool) { return "", false }

func (g *hireRaidFake) ConvertRaid(context.Context) error { return nil }

func (g *hireRaidFake) InGroup() bool { return g.haveGroup }

func (g *hireRaidFake) ItemInfos(_ context.Context, _ []uint32, _ time.Duration) (map[uint32]ItemInfo, error) {
	return map[uint32]ItemInfo{}, nil
}

func (g *hireRaidFake) AccountLevel(context.Context) (int, bool, error) { return 3, true, nil }

func (g *hireRaidFake) Command(ctx context.Context, command string) (tbm.Reply, error) {
	f := strings.Fields(command)
	switch {
	case command == "commander status":
		if g.binding == nil {
			return errReply("commander status", "not-bound"), nil
		}
		r := ack("commander status")
		r.Ack.Count, r.Ack.Executor = len(g.binding), g.binding[0]
		r.Lines = []string{fmt.Sprintf("You command %s's group: %s.", g.binding[0], strings.Join(g.binding, ", "))}
		return r, nil
	case command == "commander unbind":
		g.events = append(g.events, "cunbind")
		g.binding = nil
		return ack("commander unbind"), nil
	case strings.HasPrefix(command, "commander bind"):
		g.events = append(g.events, "cbind "+strings.Join(f[2:], " "))
		if g.raidErr != "" {
			return errReply("commander bind", g.raidErr), nil
		}
		g.binding = append([]string{}, f[2:]...)
		r := ack("commander bind")
		r.Ack.Count, r.Ack.Executor = len(g.binding), g.binding[0]
		r.Lines = []string{g.layoutLine}
		g.haveGroup = false
		return r, nil
	}
	return g.enterFake.Command(ctx, command)
}

func raidPlan10(t *testing.T) *Plan {
	t.Helper()
	p, err := Parse("lkh.json", []byte(`{"version":1,"name":"lower_karazhan_halls","kind":"raid","status":"live-checked","map":532,
 "entrance":{"x":0,"y":0,"z":0},"raid":{"size":10,"tanks":2,"healers":3,"dps":5},"legs":[{"to":{"x":0,"y":0,"z":0},
 "pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func raidHireEnv(t *testing.T, g *hireRaidFake, plan *Plan, spec *HireSpec) *prepEnv {
	t.Helper()
	clk := g.clock()
	return &prepEnv{g: g, plan: plan, spec: PrepareSpec{Party: PartyHire, Hire: spec},
		now: clk.Now, sleep: clk.Sleep, poll: time.Second,
		note: func(string) {}, command: g.Command, rep: PrepareReport{}}
}

// A 10-man LKH plan hires, gears and binds in order: the hire line, the gear
// line with the LKH preset, then the raid bind of the tank the talents name.
func TestRaidHireFlowOrder(t *testing.T) {
	g := newHireRaidFake(t, 2, 3, 5, 2)
	e := raidHireEnv(t, g, raidPlan10(t), &HireSpec{})
	detail, err := e.raidHire(context.Background())
	if err != nil {
		t.Fatalf("%v: %s", err, detail)
	}
	hire := g.idxOf("hire raid 2t 3h 5d t2")
	gear := g.idxOf("gear raid Glacienne preset lkh")
	bind := g.idxOf("cbind Rtank raid")
	if hire < 0 || gear < 0 || bind < 0 || !(hire < gear && gear < bind) {
		t.Fatalf("lines out of order: %v", g.events)
	}
	if e.hired == nil || e.hired.Lead != "Rtank" || len(e.hired.Bots) != 9 || e.hired.Tank != "Rtank" || e.hired.Healer == "" {
		t.Fatalf("hired: %+v", e.hired)
	}
	r := e.rep.Ready
	if r == nil || !r.Ready || len(r.Bots) != 10 || r.Level != 60 {
		t.Fatalf("ready: %+v", r)
	}
	for _, b := range r.Bots {
		if !b.Ready || b.Level != 60 || b.Class == "" || b.Role == "" {
			t.Errorf("%+v", b)
		}
	}
	if r.Bots[0].Name != "Rtank" || r.Bots[0].Role != "tank" {
		t.Errorf("lead: %+v", r.Bots[0])
	}
}

// An explicit tier and gear preset win over the plan's defaults.
func TestRaidHireTierAndPresetOverride(t *testing.T) {
	g := newHireRaidFake(t, 2, 3, 5, 2)
	e := raidHireEnv(t, g, raidPlan10(t), &HireSpec{Tier: 4, GearPreset: "mc"})
	if _, err := e.raidHire(context.Background()); err != nil {
		t.Fatalf("%v", err)
	}
	if g.idxOf("hire raid 2t 3h 5d t4") < 0 || g.idxOf("gear raid Glacienne preset mc") < 0 {
		t.Fatalf("override lines missing: %v %v", g.events, g.gearLines)
	}
}

// A partial raid hire reply fails with that text.
func TestRaidHirePartialFails(t *testing.T) {
	g := newHireRaidFake(t, 2, 3, 5, 2)
	g.hireReply = "TBM:HIRE_RAID|partial|9|10|2|3|5|t2"
	e := raidHireEnv(t, g, raidPlan10(t), &HireSpec{})
	_, err := e.raidHire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "TBM:HIRE_RAID|partial") {
		t.Fatalf("%v", err)
	}
}

// A TBM:GEAR|none reply fails the step.
func TestRaidGearNoneFails(t *testing.T) {
	g := newHireRaidFake(t, 2, 3, 5, 2)
	g.gearPreset = "none"
	e := raidHireEnv(t, g, raidPlan10(t), &HireSpec{})
	_, err := e.raidHire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "TBM:GEAR|none") {
		t.Fatalf("%v", err)
	}
}

// A partial gear verdict is a warning in the ready block, not a failure.
func TestRaidGearPartialWarns(t *testing.T) {
	g := newHireRaidFake(t, 2, 3, 5, 2)
	g.gearPreset = "partial"
	e := raidHireEnv(t, g, raidPlan10(t), &HireSpec{})
	if _, err := e.raidHire(context.Background()); err != nil {
		t.Fatalf("%v", err)
	}
	if e.rep.Ready == nil || len(e.rep.Ready.Warnings) == 0 || !strings.Contains(strings.Join(e.rep.Ready.Warnings, "|"), "partial") {
		t.Fatalf("ready: %+v", e.rep.Ready)
	}
}

// A raid-off refusal from the bind fails with raid-off in the error.
func TestRaidBindRaidOffFails(t *testing.T) {
	g := newHireRaidFake(t, 2, 3, 5, 2)
	g.raidErr = "raid-off"
	e := raidHireEnv(t, g, raidPlan10(t), &HireSpec{})
	_, err := e.raidHire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "raid-off") {
		t.Fatalf("%v", err)
	}
}

// A commander below 60 fails before any line goes out.
func TestRaidHireRefusesBelow60(t *testing.T) {
	g := newHireRaidFake(t, 2, 3, 5, 2)
	g.agentLvl = 59
	e := raidHireEnv(t, g, raidPlan10(t), &HireSpec{})
	_, err := e.raidHire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "level 60") {
		t.Fatalf("%v", err)
	}
	if len(g.events) != 0 || len(g.botCmds) != 0 {
		t.Fatalf("lines sent before the level check: %v %v", g.events, g.botCmds)
	}
}
