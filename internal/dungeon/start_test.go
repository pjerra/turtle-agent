package dungeon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Starting anywhere (start.go) and the pull test (pulltest.go) against a fake commander. startFake is the fakeRemote of remote_test.go that
// is also the agent's own character: it has an account level, a place, `.appear`, `.go xyz`, `.bot summon` (the lead bot comes to the
// agent after 3 s), `cmd summon` (a bot that stands apart comes to the lead bot after 3 s) and `.respawn`.
type startFake struct {
	*fakeRemote
	level    int
	known    bool
	agent    Point
	agentMap uint32

	appears   []string
	gos       []string // "x y z map"
	botLines  []string
	respawns  int
	respawnFn func(g *startFake) // what a .respawn brings back
	respawnOK bool               // the account may .respawn (a developer): level >= 3 is enough, this is for a Respawner-less game

	apart     map[string]bool      // bots that stand far from the lead bot until a `cmd summon` lands
	arrive    map[string]time.Time // when a summoned bot lands
	stuckBots map[string]bool      // `cmd summon` of these does nothing
	neverLead bool                 // `.bot summon <lead>` does nothing
	leadTo    *Point
	leadToAt  time.Time
	extra     func(g *startFake) // per tick
	// followAgent: after `.bot summon` the module makes the commander the bot's master and puts it on follow (SetBotFollow): the lead bot
	// walks after the agent until a `cmd move` or `cmd stay` is sent. drift moves the agent away when that starts.
	followAgent bool
	drift       *Point
	driftNow    bool
	summonsLog  []string
	// calls is every call of the fake that acts on the game, in order, beside the commands in orders: a refused start
	// must leave both empty (no teleport, unbind, GM line, bot line or move).
	calls []string

	// .gps: gpsFn answers it (nil: the fake has no .gps reply and the call fails); sink puts the agent that far below the point of each
	// `.go xyz`; goPts is every point `.go xyz` was sent.
	gpsFn    func(g *startFake) (string, error)
	gpsCalls int
	sink     float32
	goPts    []Point
}

func (g *startFake) log(format string, a ...any) {
	g.calls = append(g.calls, fmt.Sprintf(format, a...))
}

func newStartFake(t *testing.T) *startFake {
	g := &startFake{fakeRemote: newFakeRemote(t), level: 3, known: true, agentMap: 1, agent: Point{-500, 0, 0}, apart: map[string]bool{}, arrive: map[string]time.Time{}, stuckBots: map[string]bool{}}
	g.onTick = g.tick
	return g
}

func (g *startFake) tick(*fakeRemote) {
	if g.leadTo != nil && !g.now.Before(g.leadToAt) {
		l := g.lead()
		l.X, l.Y, l.Z = g.leadTo.X, g.leadTo.Y, g.leadTo.Z
		g.leadTo = nil
		g.followAgent = true
		g.driftNow = g.drift != nil
		for _, p := range g.party[1:] {
			p.X, p.Y, p.Z = l.X, l.Y, l.Z
		}
	}
	if g.driftNow && g.leadTo == nil && g.now.After(g.leadToAt) {
		g.driftNow, g.agent = false, *g.drift
	}
	if g.followAgent {
		l := g.lead()
		if d := dist3(Point{l.X, l.Y, l.Z}, g.agent); d > 3 {
			step := float32(min(d, 10)) / float32(d)
			l.X, l.Y, l.Z = l.X+(g.agent.X-l.X)*step, l.Y+(g.agent.Y-l.Y)*step, l.Z+(g.agent.Z-l.Z)*step
		}
	}
	for name, at := range g.arrive {
		if !g.now.Before(at) {
			delete(g.apart, name)
			delete(g.arrive, name)
		}
	}
	for _, p := range g.party[1:] {
		if g.apart[p.Name] {
			p.X, p.Y, p.Z = g.lead().X+200, g.lead().Y, g.lead().Z
		}
	}
	if g.extra != nil {
		g.extra(g)
	}
}

func (g *startFake) Command(ctx context.Context, command string) (tbm.Reply, error) {
	g.log("command %s", command)
	f := strings.Fields(command)
	if len(f) > 1 && f[0] == "cmd" && (f[1] == "move" || f[1] == "stay") && len(g.cmdErr[f[1]]) == 0 {
		g.followAgent = false // the order replaces the follow
	}
	if len(f) > 2 && f[0] == "cmd" && f[1] == "summon" {
		g.summonsLog = append(g.summonsLog, f[2])
		if g.apart[f[2]] && !g.stuckBots[f[2]] {
			g.arrive[f[2]] = g.now.Add(3 * time.Second)
		}
	}
	return g.fakeRemote.Command(ctx, command)
}

// unboundByFresh records that prepare fresh disbanded the standing group (see GroupUnbind): the next `commander
// status` answers not-bound.
func (g *startFake) unboundByFresh() { g.fakeRemote.bound = false }

func (g *startFake) AgentPlace() (Point, uint32, bool) { return g.agent, g.agentMap, true }
func (g *startFake) AccountLevel(context.Context) (int, bool, error) {
	return g.level, g.known, nil
}
func (g *startFake) TeleName(_ context.Context, player, location string) error {
	g.log("tele name %s %s", player, location)
	return nil
}
func (g *startFake) AgentName() string      { return "Commander" }
func (g *startFake) AgentGUID() packet.GUID { return packet.GUID(0x1) }
func (g *startFake) AgentMap() uint32       { return g.agentMap }
func (g *startFake) Sees(string) bool       { return true }
func (g *startFake) GMVisible(_ context.Context, on bool) error {
	g.log("gm visible %v", on)
	return nil
}
func (g *startFake) UnbindInstances(_ context.Context, player string, _ packet.GUID) error {
	g.log("instance unbind all %s", player)
	return nil
}
func (g *startFake) GroupUnbind(_ context.Context, player string, _ packet.GUID) (GroupUnbindResult, error) {
	g.log("group unbind %s", player)
	g.unboundByFresh()
	return GroupUnbound, nil
}
func (g *startFake) Appear(_ context.Context, player string) error {
	g.log("appear %s", player)
	g.appears = append(g.appears, player)
	l := g.lead()
	g.agent, g.agentMap = Point{l.X, l.Y, l.Z}, g.mapID
	return nil
}
func (g *startFake) WalkSelf(_ context.Context, at Point) (WalkResult, error) {
	g.log("walk self %v", at)
	return WalkResult{Result: "arrived"}, nil
}
func (g *startFake) GoXYZ(_ context.Context, mapID uint32, at Point) error {
	g.log("go xyz %v %d", at, mapID)
	g.gos = append(g.gos, fmt.Sprintf("%.0f %.0f %.0f %d", at.X, at.Y, at.Z, mapID))
	g.goPts = append(g.goPts, at)
	g.agent, g.agentMap = Point{at.X, at.Y, at.Z - g.sink}, mapID
	return nil
}
func (g *startFake) GPS(context.Context) (string, error) {
	g.log("gps")
	g.gpsCalls++
	if g.gpsFn == nil {
		return "", errors.New("no .gps reply")
	}
	return g.gpsFn(g)
}
func (g *startFake) BotLines(_ context.Context, command string, _ time.Duration, done func(string) bool) ([]string, bool, error) {
	g.log("bot %s", command)
	if command == "roster" {
		// Fresh prepare needs the bots online in the world: report them so (nonzero GUIDs).
		lines := []string{fmt.Sprintf("TBM:ROSTER_BEGIN|%d", len(g.party))}
		for _, p := range g.party {
			lines = append(lines, fmt.Sprintf("TBM:ROSTER|%d|%s|1|online|0|Uldaman", uint32(uint64(p.GUID)&0xFFFFFFFF), p.Name))
		}
		lines = append(lines, "TBM:ROSTER_END")
		var got []string
		for _, l := range lines {
			got = append(got, l)
			if done(l) {
				return got, true, nil
			}
		}
		return got, false, nil
	}
	g.botLines = append(g.botLines, command)
	if command == "summon "+g.lead().Name && !g.neverLead {
		at := g.agent
		g.leadTo, g.leadToAt = &at, g.now.Add(3*time.Second)
		line := "Summoning Tanky to a safe position near you (3s); it will follow on arrival."
		return []string{line}, done(line), nil
	}
	if strings.HasPrefix(command, "summon ") {
		return []string{"Bot is in combat and cannot be summoned."}, true, nil
	}
	return nil, false, nil
}

func (g *startFake) Respawn(context.Context) error {
	g.log("respawn")
	g.respawns++
	if g.respawnFn != nil {
		g.respawnFn(g)
	}
	return nil
}

func startOpts() RemoteOptions {
	o := opts()
	o.Preflight = PreflightOff
	return o
}

func runFrom(t *testing.T, g interface {
	RemoteGame
	clock() Clock
}, plan *Plan, o RemoteOptions, fromLeg int, setup ...string) (Report, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight = 2 * time.Second
	cfg.AddsWait = 4 * time.Second
	cfg.Setup = setup
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	err := d.Run(context.Background(), fromLeg)
	rep, ended := d.Status()
	if err == nil && !ended {
		t.Fatal("the report does not say the run ended")
	}
	return rep, err
}

// runFromResume is runFrom with cfg.Resume on (a resume after a pause/keep_on_fail into the same instance).
func runFromResume(t *testing.T, g interface {
	RemoteGame
	clock() Clock
}, plan *Plan, o RemoteOptions, fromLeg int, setup ...string) (Report, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight = 2 * time.Second
	cfg.AddsWait = 4 * time.Second
	cfg.Setup = setup
	cfg.Resume = true
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	err := d.Run(context.Background(), fromLeg)
	rep, ended := d.Status()
	if err == nil && !ended {
		t.Fatal("the report does not say the run ended")
	}
	return rep, err
}

func twoLegPlan() *Plan {
	return remotePlan(
		Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
		Leg{Name: "second", To: Point{200, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{300}, Positions: []Point{{250, 5, 0}}}}})
}

func (g *startFake) mobAlive(entry uint32) bool {
	for _, m := range g.mobs {
		if m.entry == entry {
			return true
		}
	}
	return false
}

// from_leg 1: the commander enters the lead bot's instance, goes to the leg's start, the lead bot and the others come, the first leg is
// reported skipped and its pack is left alone, and the GM use is counted.
func TestStartFromLegMovesTheCommanderAndSummonsTheParty(t *testing.T) {
	g := newStartFake(t)
	g.addMob(299, Point{150, 5, 0})
	g.addMob(300, Point{250, 5, 0})
	g.apart["Healy"] = true
	g.drift = &Point{140, 0, 0} // the commander walks off as soon as the lead bot lands: a bot still on follow would go with it
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if fmt.Sprint(g.appears) != "[Tanky]" || fmt.Sprint(g.gos) != "[100 0 0 0]" {
		t.Fatalf("appears %v gos %v", g.appears, g.gos)
	}
	if fmt.Sprint(g.botLines) != "[summon Tanky]" || fmt.Sprint(g.summonsLog) != "[Healy]" {
		t.Fatalf("bot lines %v, cmd summons %v", g.botLines, g.summonsLog)
	}
	s := rep.Start
	if s == nil || s.Kind != StartLeg || s.Leg != 1 || s.Method != "gm" || s.Point != (Point{100, 0, 0}) || !s.LeadSummoned || fmt.Sprint(s.Summoned) != "[Healy]" {
		t.Fatalf("start %+v", s)
	}
	if fmt.Sprint(s.GMCommands) != "[.appear Tanky .go xyz 100.00 0.00 0.00 0 .gps]" || len(s.Skipped) != 1 || s.Skipped[0] != "leg 0 (first): skipped (from_leg)" {
		t.Fatalf("start %+v", s)
	}
	if rep.Remote.GMCommands != 3 || rep.Remote.RunGMAssists != 0 {
		t.Fatalf("gm %d assists %d", rep.Remote.GMCommands, rep.Remote.RunGMAssists)
	}
	if got := fmt.Sprint(rep.SetupShortcuts); !strings.Contains(got, ".appear Tanky (GM start point)") || !strings.Contains(got, ".go xyz 100.00 0.00 0.00 0 (GM start point)") {
		t.Fatalf("setup shortcuts %v", rep.SetupShortcuts)
	}
	// Rule 1: the first leg is skipped, not cleared: its pack lives, nothing was pulled there, the report says so.
	if !g.mobAlive(299) || g.mobAlive(300) {
		t.Fatalf("mobs %v", g.mobs)
	}
	if len(rep.Legs) != 2 || rep.Legs[0].Result != SkippedFromLeg || rep.Legs[1].Result != "done" || rep.FromLeg != 1 {
		t.Fatalf("legs %+v", rep.Legs)
	}
	if n := len(g.ordersOf("cmd pullback")); n != 1 {
		t.Fatalf("pullbacks %v", g.ordersOf("cmd pullback"))
	}
	// The lead bot was told to hold where it landed, before the others were summoned, and the commander's drift did not take it along.
	hold := g.indexOf("cmd move 100.00 0.00 0.00")
	if hold < 0 || hold > g.indexOf("cmd summon") || g.followAgent {
		t.Fatalf("no hold after .bot summon (or it still follows): %v", g.orders)
	}
	// The GM lines come before any order of the run itself, and none is aimed at another player.
	if g.indexOf("cmd summon") < 0 || g.indexOf("cmd summon") > g.indexOf("cmd move 200.00") {
		t.Fatalf("orders %v", g.orders)
	}
}

// The lead bot that cannot be told to hold fails the run after three orders, instead of chasing the commander through the dungeon.
func TestStartLeadThatKeepsFollowingFails(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.drift = &Point{140, 0, 0}
	g.cmdErr["move"] = []string{"invalid", "invalid", "invalid", "invalid", "invalid"}
	g.cmdErr["stay"] = []string{"invalid", "invalid"}
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err == nil || !strings.Contains(err.Error(), "does not hold still after .bot summon") || rep.State != "failed" || g.unbinds != 1 {
		t.Fatalf("%v %s", err, rep.State)
	}
	// Two moves and a stay were refused; the last stay is the end of the run's.
	if len(g.ordersOf("cmd move")) != 2 || len(g.ordersOf("cmd stay")) != 2 || len(g.summonsLog) != 0 {
		t.Fatalf("orders %v", g.orders)
	}
}

// A party that stands at the start already needs no GM line.
func TestStartFromLegAlreadyThere(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.lead().X = 100
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if len(g.appears)+len(g.gos)+len(g.botLines)+len(g.summonsLog) != 0 || rep.Remote.GMCommands != 0 {
		t.Fatalf("appears %v gos %v lines %v summons %v", g.appears, g.gos, g.botLines, g.summonsLog)
	}
	if rep.Start == nil || rep.Start.Method != "already there" || len(rep.Start.GMCommands) != 0 || rep.Legs[0].Result != SkippedFromLeg {
		t.Fatalf("start %+v legs %+v", rep.Start, rep.Legs)
	}
}

// A resume into a leg whose own ground the party stands in already (the Zum'rah room after the trigger) does not send it back to the previous
// leg's end: no GM move, the leg starts from where the party is.
func TestStartFromLegInTheLegsOwnGround(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.lead().X = 230 // 130 yd past the start point (leg 0's To), 30 yd from leg 1's To
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if len(g.appears)+len(g.gos)+len(g.botLines)+len(g.summonsLog) != 0 || rep.Remote.GMCommands != 0 {
		t.Fatalf("appears %v gos %v lines %v summons %v", g.appears, g.gos, g.botLines, g.summonsLog)
	}
	if rep.Start == nil || rep.Start.Method != "already in the leg" || !strings.Contains(notesOf(rep), "in leg 1's own ground already") {
		t.Fatalf("start %+v", rep.Start)
	}
}

// A player's account cannot start inside: the run fails before it sends anything, and still unbinds.
func TestStartNeedsAGM(t *testing.T) {
	for _, c := range []struct {
		level int
		known bool
	}{{0, true}, {0, false}} {
		g := newStartFake(t)
		g.level, g.known = c.level, c.known
		g.addMob(300, Point{250, 5, 0})
		rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
		if err == nil || !strings.Contains(err.Error(), "needs a GM commander") || !strings.Contains(err.Error(), "nothing was sent") || rep.State != "failed" {
			t.Fatalf("%v %s", err, rep.State)
		}
		if len(g.appears)+len(g.gos)+len(g.botLines)+len(g.summonsLog) != 0 || len(g.ordersOf("cmd move")) != 0 || g.unbinds != 1 || rep.Remote.GMCommands != 0 {
			t.Fatalf("appears %v gos %v, moves %v, unbinds %d", g.appears, g.gos, g.ordersOf("cmd move"), g.unbinds)
		}
	}
}

// A lead bot that does not come, and a bot that never gathers, fail the run after bounded waits, not a loop.
func TestStartWaitsAreBounded(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.neverLead = true
	began := g.now
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err == nil || !strings.Contains(err.Error(), "did not come to the commander after three .bot summon") || rep.State != "failed" || g.unbinds != 1 {
		t.Fatalf("%v %s unbinds %d", err, rep.State, g.unbinds)
	}
	if len(g.botLines) != 3 || g.now.Sub(began) > 3*time.Minute {
		t.Fatalf("%d summons, %s", len(g.botLines), g.now.Sub(began))
	}

	g = newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.apart["Magey"], g.stuckBots["Magey"] = true, true
	began = g.now
	rep, err = runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err == nil || !strings.Contains(err.Error(), "did not gather at the lead bot: Magey still apart after two summons") || g.unbinds != 1 {
		t.Fatalf("%v unbinds %d", err, g.unbinds)
	}
	if len(g.summonsLog) != 2 || g.now.Sub(began) > 3*time.Minute {
		t.Fatalf("summons %v, %s", g.summonsLog, g.now.Sub(began))
	}
}

// start_here keeps the old from_leg: no move, no GM line, the earlier legs are still reported skipped.
func TestStartHere(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	o := startOpts()
	o.StartHere = true
	rep, err := runFrom(t, g, twoLegPlan(), o, 1)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if len(g.appears)+len(g.gos)+len(g.botLines) != 0 || rep.Start == nil || rep.Start.Method != "here" || rep.Legs[0].Result != SkippedFromLeg {
		t.Fatalf("start %+v legs %+v", rep.Start, rep.Legs)
	}
}

func groupPlan() *Plan {
	return remotePlan(Leg{Name: "pack", To: Point{100, 0, 0}, PullGroups: []PullGroup{
		{Entries: []uint32{299}, Positions: []Point{{150, 0, 0}}},
		{Entries: []uint32{300}, Positions: []Point{{300, 0, 0}}},
	}})
}

// from_pull_group 1: the party is gathered at a safe point short of the second pack, the first pack is skipped, not pulled, and the second is
// killed.
func TestStartFromPullGroup(t *testing.T) {
	g := newStartFake(t)
	g.addMob(299, Point{150, 0, 0})
	g.addMob(300, Point{300, 0, 0})
	o := startOpts()
	one := 1
	o.FromPullGroup = &one
	rep, err := runFrom(t, g, groupPlan(), o, 0)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	s := rep.Start
	if s == nil || s.Kind != StartGather || s.PullGroup == nil || *s.PullGroup != 1 || s.Point != (Point{245, 0, 0}) || s.Warning != "" {
		t.Fatalf("start %+v", s)
	}
	if fmt.Sprint(g.gos) != "[245 0 0 0]" || fmt.Sprint(g.appears) != "[Tanky]" {
		t.Fatalf("gos %v appears %v", g.gos, g.appears)
	}
	if !g.mobAlive(299) || g.mobAlive(300) || len(g.ordersOf("cmd pullback")) != 1 {
		t.Fatalf("mobs %v pullbacks %v", g.mobs, g.ordersOf("cmd pullback"))
	}
	if got := strings.Join(s.Skipped, "|"); !strings.Contains(got, "leg 0 (pack): pull groups 0-0 skipped (from_pull_group)") {
		t.Fatalf("skipped %q", got)
	}
	if len(rep.Legs) != 1 || rep.Legs[0].Result != "done" || rep.Legs[0].Pulls != 1 {
		t.Fatalf("legs %+v", rep.Legs)
	}
}

func TestStartRefusesAPullGroupThePlanLacks(t *testing.T) {
	g := newStartFake(t)
	for _, n := range []int{-1, 2} {
		o := startOpts()
		o.FromPullGroup = &n
		_, err := runFrom(t, g, groupPlan(), o, 0)
		if err == nil || !strings.Contains(err.Error(), "from_pull_group") || len(g.orders) != 0 {
			t.Fatalf("%v orders %v", err, g.orders)
		}
	}
	two := 0
	o := startOpts()
	o.FromPullGroup = &two
	_, err := runFrom(t, g, remotePlan(Leg{Name: "none", To: Point{1, 0, 0}}), o, 0)
	if err == nil || !strings.Contains(err.Error(), "has 0 pull groups") || len(g.orders) != 0 {
		t.Fatalf("%v", err)
	}
}

func TestGatherSpotIsClearOfOtherPacks(t *testing.T) {
	p := remotePlan(Leg{Name: "l", To: Point{0, 0, 0}, PullGroups: []PullGroup{
		{Entries: []uint32{1}, Positions: []Point{{160, 0, 0}}},
		{Entries: []uint32{2}, Positions: []Point{{200, 0, 0}}},
	}})
	// 55 yd short of pack 1 is (145, 0, 0): 15 yd from pack 0. 70 yd short is (130, 0, 0): 30 yd from it, which is clear.
	if at, warn := p.GatherSpot(0, 1, nil); at != (Point{130, 0, 0}) || warn != "" {
		t.Fatalf("%v %q", at, warn)
	}
	// With nothing in the way the first candidate stands.
	q := remotePlan(Leg{Name: "l", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{2}, Positions: []Point{{200, 0, 0}}}}})
	if at, warn := q.GatherSpot(0, 0, nil); at != (Point{145, 0, 0}) || warn != "" {
		t.Fatalf("%v %q", at, warn)
	}
	// A pack at the leg's To: the To is the spot. A path that bends is followed: 55 yd before the end of it.
	r := remotePlan(Leg{Name: "l", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{2}, Positions: []Point{{30, 0, 0}}}}})
	if at, _ := r.GatherSpot(0, 0, nil); at != (Point{0, 0, 0}) {
		t.Fatalf("%v", at)
	}
	bent := func(from, to Point) []Point { return []Point{from, {100, 0, 0}, {100, 100, 0}, to} }
	s := remotePlan(Leg{Name: "l", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{2}, Positions: []Point{{100, 120, 0}}}}})
	if at, warn := s.GatherSpot(0, 0, bent); at != (Point{100, 65, 0}) || warn != "" {
		t.Fatalf("%v %q", at, warn)
	}
	// A boss counts as another pack, and a crowd with no clear spot gets the best one with a warning.
	t2 := remotePlan(Leg{Name: "l", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{2}, Positions: []Point{{200, 0, 0}}}}, Boss: &Boss{Entry: 9, Position: Point{145, 0, 0}}})
	if at, _ := t2.GatherSpot(0, 0, nil); at == (Point{145, 0, 0}) {
		t.Fatalf("the spot is on the boss: %v", at)
	}
	crowd := remotePlan(Leg{Name: "l", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{2}, Positions: []Point{{200, 0, 0}}}, {Entries: []uint32{3}, Positions: []Point{{130, 0, 0}, {115, 0, 0}, {100, 0, 0}}}}})
	if _, warn := crowd.GatherSpot(0, 0, nil); warn == "" {
		t.Fatalf("no warning for a spot among packs")
	}
}

// nothingSent fails the test unless the refused run sent nothing at all and says why: no command (bind, enter, loot,
// summon), no teleport, unbind, GM line or bot line (calls), no move, and the report failed with the refusal.
func nothingSent(t *testing.T, g *startFake, rep Report, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("not refused as expected:\n got %v\nwant %s", err, want)
	}
	if len(g.orders) != 0 || len(g.calls) != 0 || len(g.gos) != 0 || len(g.appears) != 0 || len(g.botLines) != 0 {
		t.Fatalf("a refused start sent something: orders %q calls %q", g.orders, g.calls)
	}
	if rep.State != "failed" || rep.Error != want || rep.Start != nil || (rep.Remote != nil && rep.Remote.Bound) {
		t.Fatalf("the report does not say the start was refused: state %q error %q start %v", rep.State, rep.Error, rep.Start)
	}
}

func freshOpts(group int) RemoteOptions {
	o := startOpts()
	o.FromPullGroup = &group
	o.Prepare = PrepareSpec{Fresh: true}
	return o
}

// crowdPlan's pull group 1 has its gather spot (145 0 0, and the fallbacks 130 and 115) inside pull group 0.
func crowdPlan() *Plan {
	p := remotePlan(Leg{Name: "l", To: Point{0, 0, 0}, PullGroups: []PullGroup{
		{Entries: []uint32{1}, Positions: []Point{{130, 0, 0}, {115, 0, 0}, {100, 0, 0}}},
		{Entries: []uint32{2}, Positions: []Point{{200, 0, 0}}},
	}})
	p.OutsideTele = "uldaman"
	return p
}

// A from_pull_group start whose gather spot is under the safe distance from a pack of an earlier step is refused in a
// fresh instance (prepare.fresh: the earlier packs are alive) before anything is sent: not the prepare step's teleport
// and unbinds, the bind, the enter or a GM line (zf-g9-6cd0b13: the gather spot was 21 yd from a live pack and the party
// died there twice). force_start runs it anyway. In a non-fresh instance the same start only warns. A clear spot, or a
// near pack of a later step (the run's own fight), is neither refused nor warned about.
func TestStartRefusesAGatherSpotNearAnEarlierPack(t *testing.T) {
	const want = "from_pull_group 1: the gather spot 145 0 0 is 15 yd from pull group 0 of leg 0 (a safe spot needs 30), alive in a fresh instance: pass force_start to start there anyway"
	g := newStartFake(t)
	rep, err := runFrom(t, g, crowdPlan(), freshOpts(1), 0)
	nothingSent(t, g, rep, err, want)

	o := freshOpts(1)
	o.ForceStart = true
	g = newStartFake(t)
	rep, err = runFrom(t, g, crowdPlan(), o, 0)
	if err != nil || rep.State != "done" {
		t.Fatalf("force_start: %v %s\n%s", err, rep.State, notesOf(rep))
	}
	if notes := notesOf(rep); !strings.Contains(notes, "start: warning: from_pull_group 1: the gather spot 145 0 0 is 15 yd from pull group 0 of leg 0 (a safe spot needs 30), alive in a fresh instance; force_start: starting there anyway") {
		t.Fatalf("no force_start note:\n%s", notes)
	}
	if !slices.ContainsFunc(g.calls, func(c string) bool { return strings.HasPrefix(c, "instance unbind all ") }) {
		t.Fatalf("the forced fresh run did no prepare (the refusal test above proves nothing): %q", g.calls)
	}
	// A non-fresh instance: a warning, the run goes on.
	one := 1
	o3 := startOpts()
	o3.FromPullGroup = &one
	rep3, err := runFrom(t, newStartFake(t), crowdPlan(), o3, 0)
	if err != nil || rep3.State != "done" {
		t.Fatalf("non-fresh start refused: %v %s", err, rep3.State)
	}
	if notes := notesOf(rep3); !strings.Contains(notes, "start: warning: from_pull_group 1: the gather spot 145 0 0 is 15 yd from pull group 0 of leg 0 (a safe spot needs 30); the instance is not known fresh") {
		t.Fatalf("no warning in notes:\n%s", notes)
	}
	// A clear spot: no refusal.
	g4 := newStartFake(t)
	g4.addMob(299, Point{150, 0, 0})
	g4.addMob(300, Point{300, 0, 0})
	clear := groupPlan()
	clear.OutsideTele = "uldaman"
	if _, err := runFrom(t, g4, clear, freshOpts(1), 0); err != nil {
		t.Fatalf("clear start refused: %v", err)
	}
	// A pack of a later step near the spot is the run's own fight, not a danger: no refusal, no warning.
	later := remotePlan(Leg{Name: "l", To: Point{0, 0, 0}, PullGroups: []PullGroup{
		{Entries: []uint32{2}, Positions: []Point{{200, 0, 0}}},
		{Entries: []uint32{3}, Positions: []Point{{130, 0, 0}}},
	}})
	later.OutsideTele = "uldaman"
	rep5, err := runFrom(t, newStartFake(t), later, freshOpts(0), 0)
	if err != nil {
		t.Fatalf("later pack refused: %v", err)
	}
	if notes := notesOf(rep5); strings.Contains(notes, "start: warning: from_pull_group") {
		t.Fatalf("warning for a later pack:\n%s", notes)
	}
}

// A from_leg start at a point under the safe distance from a pack of an earlier leg is refused in a fresh instance
// unless force_start is passed. A resume is never refused: only a warning.
func TestStartRefusesAFromLegNearAnEarlierPack(t *testing.T) {
	p := remotePlan(
		Leg{Name: "a", To: Point{10, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{1}, Positions: []Point{{12, 0, 0}}}}},
		Leg{Name: "b", To: Point{100, 0, 0}},
	)
	p.OutsideTele = "uldaman"
	const want = "from_leg 1: the start point 10 0 0 is 2 yd from pull group 0 of leg 0 (a safe spot needs 30), alive in a fresh instance: pass force_start to start there anyway"
	o := startOpts()
	o.Prepare = PrepareSpec{Fresh: true}
	g := newStartFake(t)
	rep, err := runFrom(t, g, p, o, 1)
	nothingSent(t, g, rep, err, want)
	o.ForceStart = true
	rep, err = runFrom(t, newStartFake(t), p, o, 1)
	if err != nil || rep.State != "done" {
		t.Fatalf("force_start: %v %s\n%s", err, rep.State, notesOf(rep))
	}
	// A resume (fresh or not): a warning only.
	for _, fresh := range []bool{false, true} {
		o2 := startOpts()
		o2.Prepare.Fresh = fresh
		rep2, err := runFromResume(t, newStartFake(t), p, o2, 1)
		if err != nil || rep2.State != "done" {
			t.Fatalf("resume (fresh %v) refused: %v %s", fresh, err, rep2.State)
		}
		if notes := notesOf(rep2); !strings.Contains(notes, "start: warning: from_leg 1: the start point 10 0 0 is 2 yd from pull group 0 of leg 0") || !strings.Contains(notes, "not known fresh") {
			t.Fatalf("no warning in notes:\n%s", notes)
		}
	}
}

// The start point of from_leg N is where the boss of leg N-1 stands (LegStart). In a fresh instance that boss is
// alive: the start is refused, and the refusal says so and points to from_pull_group or a cleared or kept instance.
func TestStartRefusesAFromLegOnTheLiveBossOfTheLegBefore(t *testing.T) {
	p := remotePlan(
		Leg{Name: "a", To: Point{10, 0, 0}, Boss: &Boss{Entry: 5, Position: Point{40, 0, 0}}},
		Leg{Name: "b", To: Point{100, 0, 0}},
	)
	p.OutsideTele = "uldaman"
	const want = "from_leg 1: the boss of leg 0 is alive at the start point 40 0 0 in a fresh instance (entry 5, 0 yd away; a safe spot needs 30). Start at a pull group of leg 1 (from_pull_group), or in a cleared or kept instance (prepare.fresh false, or keep_on_fail and resume), or pass force_start to start there anyway"
	o := startOpts()
	o.Prepare = PrepareSpec{Fresh: true}
	g := newStartFake(t)
	rep, err := runFrom(t, g, p, o, 1)
	nothingSent(t, g, rep, err, want)
	// The same start in an instance not known fresh: a warning that names the boss.
	rep, err = runFrom(t, newStartFake(t), p, startOpts(), 1)
	if err != nil || !strings.Contains(notesOf(rep), "start: warning: from_leg 1: the start point 40 0 0 is 0 yd from the boss of leg 0 (entry 5) (a safe spot needs 30); the instance is not known fresh") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

// A hired party (prepare.party "hire", which takes no fresh) starts in a fresh instance: new hires have no saved one.
// The exception is a hire that takes over the binding an earlier run kept (HireMemory) without rehire: that party is
// still in its instance, so the start only warns.
func TestStartGateTreatsAHiredPartyAsFresh(t *testing.T) {
	p := crowdPlan()
	const want = "from_pull_group 1: the gather spot 145 0 0 is 15 yd from pull group 0 of leg 0 (a safe spot needs 30), alive in a fresh instance: pass force_start to start there anyway"
	hired := func(rehire bool, kept map[string]tbm.Who) RemoteOptions {
		one := 1
		o := startOpts()
		o.Lead, o.Bots = "", nil
		o.FromPullGroup = &one
		o.Prepare = PrepareSpec{Party: PartyHire, Hire: &HireSpec{Rehire: rehire}}
		o.HireMemory = kept
		return o
	}
	if err := hired(false, nil).Prepare.Validate(true); err != nil {
		t.Fatalf("the hire spec of the test is not one dungeon_run takes: %v", err)
	}
	g := newStartFake(t)
	rep, err := runFrom(t, g, p, hired(false, nil), 0)
	nothingSent(t, g, rep, err, want)
	kept := map[string]tbm.Who{"tanky": {Class: "warrior", Level: 43}}
	g = newStartFake(t)
	rep, err = runFrom(t, g, p, hired(true, kept), 0)
	nothingSent(t, g, rep, err, want)
	// A kept binding taken over without rehire: not known fresh, a warning only (the run then fails on this fake, which
	// cannot hire, but not at the gate).
	rep, err = runFrom(t, newStartFake(t), p, hired(false, kept), 0)
	if err == nil || strings.Contains(err.Error(), "force_start") || !strings.Contains(notesOf(rep), "start: warning: from_pull_group 1: the gather spot 145 0 0 is 15 yd from pull group 0 of leg 0 (a safe spot needs 30); the instance is not known fresh") {
		t.Fatalf("kept binding: %v\n%s", err, notesOf(rep))
	}
}

// bentFake plans a path that bends: from the leg's To (0 0 0) up to 0 200 0, across to 200 200 0 and down to the pack
// at 200 0 0. The gather spot on it (200 55 0, the fallbacks 200 70 0 and 200 85 0) is inside pull group 0 at 220 70 0;
// the straight line's (145 0 0) is 100 yd clear of it. The gate must read the same path as the start.
type bentFake struct{ *startFake }

func (g bentFake) PlanPath(_ context.Context, _ uint32, from, to Point) ([]Point, error) {
	if from == (Point{0, 0, 0}) && to == (Point{200, 0, 0}) {
		return []Point{from, {0, 200, 0}, {200, 200, 0}, to}, nil
	}
	return []Point{from, to}, nil
}

func TestStartGateReadsTheNavmeshPath(t *testing.T) {
	p := remotePlan(Leg{Name: "l", To: Point{0, 0, 0}, PullGroups: []PullGroup{
		{Entries: []uint32{1}, Positions: []Point{{220, 70, 0}}},
		{Entries: []uint32{2}, Positions: []Point{{200, 0, 0}}},
	}})
	p.OutsideTele = "uldaman"
	g := newStartFake(t)
	rep, err := runFrom(t, bentFake{g}, p, freshOpts(1), 0)
	nothingSent(t, g, rep, err, "from_pull_group 1: the gather spot 200 55 0 is 25 yd from pull group 0 of leg 0 (a safe spot needs 30), alive in a fresh instance: pass force_start to start there anyway")
	// The straight line (no planner) gathers at 145 0 0: clear, not refused.
	if _, err := runFrom(t, newStartFake(t), p, freshOpts(1), 0); err != nil && strings.Contains(err.Error(), "force_start") {
		t.Fatalf("the straight-line start was refused: %v", err)
	}
}

// A pulltest start whose gather spot is under the safe distance from a pack of an earlier step is refused in a fresh
// instance before anything is sent, unless force_start is passed.
func TestPulltestRefusesAGatherSpotNearAnEarlierPack(t *testing.T) {
	o := ptOpts(PulltestSpec{Leg: 0, PullGroup: 1, Tries: 1})
	o.Prepare = PrepareSpec{Fresh: true}
	const want = "pulltest: the gather spot 145 0 0 is 15 yd from pull group 0 of leg 0 (a safe spot needs 30), alive in a fresh instance: pass force_start to start there anyway"
	g := newStartFake(t)
	rep, err := runFrom(t, g, crowdPlan(), o, 0)
	nothingSent(t, g, rep, err, want)
	o.ForceStart = true
	g = newStartFake(t)
	g.addMob(2, Point{200, 0, 0})
	if _, err := runFrom(t, g, crowdPlan(), o, 0); err != nil {
		t.Fatalf("force_start: %v", err)
	}
}

func TestLegStartIsWhereTheLegBeforeEnded(t *testing.T) {
	p := remotePlan(
		Leg{Name: "a", To: Point{10, 0, 0}},
		Leg{Name: "b", To: Point{20, 0, 0}, Boss: &Boss{Entry: 5, Position: Point{30, 0, 0}}},
		Leg{Name: "c", To: Point{40, 0, 0}})
	p.Entrance = Point{1, 2, 3}
	for i, want := range []struct {
		at   Point
		kind string
	}{{Point{1, 2, 3}, StartEntrance}, {Point{10, 0, 0}, StartLeg}, {Point{30, 0, 0}, StartLeg}} {
		if at, kind := p.LegStart(i); at != want.at || kind != want.kind {
			t.Fatalf("leg %d: %v %s", i, at, kind)
		}
	}
}

func TestStepperAtGroupSkipsTheWalkAndTheEarlierGroups(t *testing.T) {
	p := groupPlan()
	s := NewStepperAtGroup(p, &fakeWorld{}, nil, 0, 1)
	a := s.Next()
	if a.Kind != ActPull || a.Leg != 0 || len(a.Entries) != 1 || a.Entries[0] != 300 {
		t.Fatalf("%+v", a)
	}
	s.Complete()
	if a := s.Next(); a.Kind != ActDone {
		t.Fatalf("%+v", a)
	}
	if a := NewStepperAtGroup(p, &fakeWorld{}, nil, 0, 7).Next(); a.Kind != ActWalk {
		t.Fatalf("a group the leg lacks starts the leg: %+v", a)
	}
}

func TestPlanEquivLabel(t *testing.T) {
	good := `{"version":1,"name":"e","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":1,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":5,"y":0,"z":0}],"equiv":"pair"}]}]}`
	p, err := Parse("e", []byte(good))
	if err != nil || p.Legs[0].PullGroups[0].Equiv != "pair" {
		t.Fatalf("%v %+v", err, p)
	}
	bad := strings.Replace(good, `"pair"`, `" pair"`, 1)
	if _, err := Parse("e", []byte(bad)); err == nil || !strings.Contains(err.Error(), "equiv") {
		t.Fatalf("%v", err)
	}
	long := strings.Replace(good, `"pair"`, `"`+strings.Repeat("x", MaxEquivLen+1)+`"`, 1)
	if _, err := Parse("e", []byte(long)); err == nil || !strings.Contains(err.Error(), "equiv") {
		t.Fatalf("%v", err)
	}
}

var _ = packet.GUID(0)

// A step that needs an item only a skipped leg makes fails the start at once, naming the item and the leg: nothing is sent, the party is not
// moved, and the step does not wait out its timeout for an item that cannot come.
func TestStartRefusesWhatASkippedLegWasToProvide(t *testing.T) {
	plan := remotePlan(
		Leg{Name: "boss", To: Point{10, 0, 0}, Boss: &Boss{Entry: 5, Position: Point{20, 0, 0}, LootItem: &ItemRef{Entry: 777, Name: "Odd Key"}}},
		Leg{Name: "door", To: Point{30, 0, 0}, Steps: []Step{{UseObject: &UseObject{ObjectRef: ObjectRef{Entry: 900}, RequiresItem: &ItemRef{Entry: 777, Name: "Odd Key"}}}}})
	g := newStartFake(t)
	_, err := runFrom(t, g, plan, startOpts(), 1)
	if err == nil || !strings.Contains(err.Error(), "needs Odd Key (item 777) from skipped leg 0 (boss)") || !strings.Contains(err.Error(), "have_items [777]") {
		t.Fatalf("%v", err)
	}
	if len(g.orders) != 0 || len(g.appears)+len(g.gos) != 0 {
		t.Fatalf("something was sent: %v %v %v", g.orders, g.appears, g.gos)
	}
	// From the leg that makes it, or with the item declared, or for a pull test (no steps run), the start is allowed.
	if err := plan.SkippedNeeds(0, nil); err != nil {
		t.Fatal(err)
	}
	if err := plan.SkippedNeeds(1, []uint32{777}); err != nil {
		t.Fatal(err)
	}
	o := startOpts()
	o.Pulltest = &PulltestSpec{}
	if err := NewRemoteDriver(plan, g, DefaultConfig(), o, g.clock()).checkStart(1); err != nil {
		t.Fatalf("a pull test runs no step: %v", err)
	}
}

// The real Uldaman plan: the keystone (leg 2) needs the Staff of Prehistoria that leg 1's use_item makes, and that needs the Medallion of leg 0
// and the Shaft of leg 1's boss.
func TestSkippedNeedsOnUldaman(t *testing.T) {
	p, err := Load("../../dungeons/uldaman.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		from int
		have []uint32
		want string
	}{
		{0, nil, ""},
		{1, nil, "needs Gni'kiv Medallion (item 7740) from skipped leg 0 (baelog)"},
		{1, []uint32{7740}, ""},
		{2, nil, "needs Staff of Prehistoria (item 7733) from skipped leg 1 (revelosh)"},
		{2, []uint32{7733}, ""},
		{3, nil, ""},
		{9, nil, ""},
	} {
		err := p.SkippedNeeds(c.from, c.have)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("from %d have %v: %v, want %q", c.from, c.have, err, c.want)
		}
	}
}

// The skipped legs are in the report's legs, but not in the run's "legs" totals (the summary event and the trace's counters).
func TestSkippedLegsAreNotInTheLegsTotal(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.lead().X = 100
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Legs) != 2 || traceCounters(rep)["legs"] != 1 || summaryBase(rep)["legs"] != 1 {
		t.Fatalf("legs %d, counters %v, summary %v", len(rep.Legs), traceCounters(rep)["legs"], summaryBase(rep)["legs"])
	}
}

// rfc-live-2: a resume into a leg whose party stands in the middle of it (by one of its packs, or on the way to its `to`) must not send the
// party back to the leg's first point and summon it again. The pack positions count as the leg's own ground, and with Config.Resume the
// leg's route does within ResumeMaxOff.
func TestStartResumeInsideALegDoesNotMoveOrSummonTheParty(t *testing.T) {
	plan := func() *Plan {
		return remotePlan(
			Leg{Name: "first", To: Point{100, 0, 0}},
			Leg{Name: "second", To: Point{200, 0, 0}, PullGroups: []PullGroup{
				{Entries: []uint32{300}, Positions: []Point{{260, 30, 0}}},
				{Entries: []uint32{301}, Positions: []Point{{320, 30, 0}}},
			}})
	}
	run := func(resume bool, x, y float32) (*startFake, Report, error) {
		g := newStartFake(t)
		g.lead().X, g.lead().Y = x, y
		for _, p := range g.party[1:] {
			p.X, p.Y = x, y
		}
		cfg := DefaultConfig()
		cfg.PostFight, cfg.AddsWait, cfg.Resume = 2*time.Second, 4*time.Second, resume
		d := NewRemoteDriver(plan(), g, cfg, startOpts(), g.clock())
		err := d.Run(context.Background(), 1)
		rep, _ := d.Status()
		return g, rep, err
	}
	untouched := func(name string, g *startFake, rep Report, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, notesOf(rep))
		}
		if len(g.appears)+len(g.gos)+len(g.botLines)+len(g.summonsLog) != 0 || rep.Remote.GMCommands != 0 {
			t.Fatalf("%s: appears %v gos %v lines %v summons %v", name, g.appears, g.gos, g.botLines, g.summonsLog)
		}
		if rep.Start == nil || rep.Start.Method != "already in the leg" {
			t.Fatalf("%s: start %+v\n%s", name, rep.Start, notesOf(rep))
		}
	}
	// By the second pack, 100 yd from the leg's `to` (more than legRoomYd): in the leg through the pack's position, resume or not.
	for _, resume := range []bool{false, true} {
		g, rep, err := run(resume, 305, 25)
		untouched(fmt.Sprintf("by a pack, resume %v", resume), g, rep, err)
	}
	// On the way between the previous leg's end and the `to`, 64 yd from the `to`: in the leg for a resume only.
	g, rep, err := run(true, 150, 40)
	untouched("on the route", g, rep, err)
	g, rep, err = run(false, 150, 40)
	if err != nil || rep.Start == nil || rep.Start.Method != "gm" || len(g.gos) != 1 {
		t.Fatalf("no resume: %v start %+v gos %v", err, rep.Start, g.gos)
	}
	// Far off the route even a resume is sent to the start point.
	g, rep, err = run(true, 150, 400)
	if err != nil || rep.Start == nil || rep.Start.Method != "gm" || len(g.gos) != 1 {
		t.Fatalf("far off: %v start %+v gos %v", err, rep.Start, g.gos)
	}
}
