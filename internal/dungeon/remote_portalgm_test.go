package dungeon

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The GM move of the enter phase (remote_portalgm.go): the portal is on map 0 at 300 0 0 and the bots start 300 yd away; the first Ragefire
// start had the party in Tanaris and no walkable path to Orgrimmar.

// portalFake is an enterOrders that is also StartHands and BotLiner: `.go xyz` moves the commander, `.bot summon <lead>` brings the lead bot to
// it after 3 s, `cmd summon <bot>` puts a bot on the lead bot (the real module also does that across maps).
type portalFake struct {
	*enterOrders
	agentAt          Point
	agentOn          uint32
	gos              []string
	botLines         []string
	planLen          float64         // the agent's navmesh path from the lead bot to the portal (0: the straight line)
	noPlanner        bool            // the planner fails (no tiles of the outer map): the walk falls back to straight-line points
	stuckBot         map[string]bool // bots a `cmd summon` does not bring
	leadTo           *Point
	leadToAt         time.Time
	summoned         []string
	movesBeforeEnter []string
}

func newPortalFake(t *testing.T, script ...tbm.OrderResult) *portalFake {
	g := &portalFake{enterOrders: newEnterOrders(t, script...)}
	g.agentOn = 1 // Kalimdor; the commander stands far from the portal
	g.agentAt = Point{-7000, -3000, 10}
	g.fakeRemote.onTick = g.tick
	return g
}

// lagFollowers keeps the bots behind (the fake's followers otherwise stay with the lead bot wherever it is put).
func (g *portalFake) lagFollowers() {
	g.lagUntil = map[packet.GUID]time.Time{}
	for _, p := range g.party[1:] {
		g.lagUntil[p.GUID] = g.now.Add(24 * time.Hour)
	}
}

// walkMoves are the cmd move orders sent before the first cmd enter: the walk to the portal and the hold on the landing spot.
func (g *portalFake) walkMoves() []string { return g.movesBeforeEnter }

func (g *portalFake) Order(ctx context.Context, command string) (tbm.OrderResult, error) {
	if strings.HasPrefix(command, "cmd enter") && g.movesBeforeEnter == nil {
		g.movesBeforeEnter = append([]string{}, g.ordersOf("cmd move")...)
	}
	return g.enterOrders.Order(ctx, command)
}

func (g *portalFake) tick(r *fakeRemote) {
	g.enterFake.tick(r)
	if g.leadTo != nil && !g.now.Before(g.leadToAt) {
		l := g.lead()
		l.X, l.Y, l.Z = g.leadTo.X, g.leadTo.Y, g.leadTo.Z
		g.memberMap[l.GUID] = g.agentOn
		g.leadTo = nil
	}
}

func (g *portalFake) AgentPlace() (Point, uint32, bool) { return g.agentAt, g.agentOn, true }
func (g *portalFake) Appear(context.Context, string) error {
	return errors.New("not used")
}
func (g *portalFake) WalkSelf(context.Context, Point) (WalkResult, error) {
	return WalkResult{}, errors.New("not used")
}
func (g *portalFake) GoXYZ(_ context.Context, mapID uint32, at Point) error {
	g.gos = append(g.gos, strings.TrimSpace(strings.Join([]string{ftoa(at.X), ftoa(at.Y), ftoa(at.Z), strconv.Itoa(int(mapID))}, " ")))
	g.agentAt, g.agentOn = at, mapID
	return nil
}
func (g *portalFake) BotLines(_ context.Context, command string, _ time.Duration, done func(string) bool) ([]string, bool, error) {
	if strings.HasPrefix(command, "summon ") {
		g.botLines = append(g.botLines, command)
	}
	if command == "summon "+g.lead().Name {
		at := g.agentAt
		g.leadTo, g.leadToAt = &at, g.now.Add(3*time.Second)
		line := "Summoning Tanky to a safe position near you (3s); it will follow on arrival."
		return []string{line}, done(line), nil
	}
	return nil, false, nil
}
func (g *portalFake) PlanPath(_ context.Context, mapID uint32, from, to Point) ([]Point, error) {
	if g.noPlanner || mapID != outsideMap {
		return nil, errors.New("no planner")
	}
	d := dist3(from, to)
	if g.planLen <= 0 || d == 0 {
		return []Point{from, to}, nil
	}
	// A straight line stretched to planLen: a middle corner off the line.
	h := (g.planLen/2)*(g.planLen/2) - (d/2)*(d/2)
	if h < 0 {
		h = 0
	}
	mid := Point{(from.X + to.X) / 2, (from.Y+to.Y)/2 + float32(sqrt(h)), (from.Z + to.Z) / 2}
	return []Point{from, mid, to}, nil
}

func (g *portalFake) Command(ctx context.Context, command string) (tbm.Reply, error) {
	if strings.HasPrefix(command, "cmd summon ") && g.memberMap[g.lead().GUID] != insideMap {
		name := strings.Fields(command)[2]
		g.summoned = append(g.summoned, name)
		g.log = append(g.log, "summon "+name)
		g.orders = append(g.orders, command)
		for _, p := range g.party {
			if strings.EqualFold(p.Name, name) && !g.stuckBot[name] {
				delete(g.left, name)
				l := g.lead()
				p.X, p.Y, p.Z = l.X, l.Y, l.Z
				g.memberMap[p.GUID] = g.memberMap[l.GUID]
				delete(g.lagUntil, p.GUID)
			}
		}
		return ack("cmd summon"), nil
	}
	return g.enterOrders.Command(ctx, command)
}

func (g *portalFake) run(o RemoteOptions) (Report, error) {
	o.Tank = "Tanky"
	o.Lead, o.Bots = "Tanky", []string{"Healy", "Magey"}
	return runRemote(g.t, g, g.plan, o, nil)
}

func ftoa(f float32) string { return strconv.FormatFloat(float64(f), 'f', -1, 32) }

// An unreachable first move and a GM account: the party is moved to the portal, no further move is sent, then cmd enter.
func TestRemoteEnterUnreachableAndGMMovesThePartyToThePortal(t *testing.T) {
	g := newPortalFake(t, enterAck(3))
	g.level = 1 // an observer: .go xyz, not .tele name
	g.lagFollowers()
	g.moveErrs = []string{"unreachable", "unreachable", "unreachable"}
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	e := rep.Remote.Enter
	if e == nil || e.Method != EnterCmd || !strings.Contains(e.PartyMoved, "party moved to the portal by GM, 2 bots") {
		t.Fatalf("%+v", e)
	}
	if len(g.gos) != 1 || g.gos[0] != "300 0 0 0" {
		t.Fatalf("go xyz %v", g.gos)
	}
	if strings.Join(g.botLines, "|") != "summon Tanky" || strings.Join(g.summoned, ",") != "Healy,Magey" {
		t.Fatalf("bot lines %v summoned %v", g.botLines, g.summoned)
	}
	// The refused move, then only the holds on the landing spot: no walking order after the GM move.
	m := g.walkMoves()
	for _, o := range m[1:] {
		if o != "cmd move 300.00 0.00 0.00" {
			t.Fatalf("cmd move: %v", m)
		}
	}
	if len(m) < 2 {
		t.Fatalf("cmd move: %v", m)
	}
	if len(g.sent) != 1 || g.sent[0] != "cmd enter 924" || len(g.ordersOf("cmd follow")) != 1 {
		t.Fatalf("sent %v follow %v", g.sent, g.ordersOf("cmd follow"))
	}
	if rep.Remote.GMCommands != 1 || rep.Remote.RunGMAssists != 2 || !strings.Contains(notesOf(rep), "party moved to the portal by GM, 2 bots") {
		t.Fatalf("gm %d assists %d\n%s", rep.Remote.GMCommands, rep.Remote.RunGMAssists, notesOf(rep))
	}
}

// An unreachable first move and a player's account: one clear failure after one refused order, no GM line, no repeated orders.
func TestRemoteEnterUnreachableWithoutGMFailsAtOnce(t *testing.T) {
	g := newPortalFake(t, enterAck(3))
	g.level = 0
	g.moveErrs = []string{"unreachable", "unreachable", "unreachable", "unreachable", "unreachable", "unreachable", "unreachable", "unreachable", "unreachable", "unreachable"}
	rep, err := g.run(RemoteOptions{})
	if err == nil || rep.State == "done" {
		t.Fatalf("%v %s", err, rep.State)
	}
	for _, want := range []string{"the lead bot has no walkable path to the portal", "unreachable", "hire the party near it or use a GM account"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q not in %v", want, err)
		}
	}
	if n := len(g.ordersOf("cmd move")); n != 1 || len(g.sent) != 0 || len(g.gos) != 0 || len(g.botLines) != 0 {
		t.Fatalf("moves %d enter %v go %v lines %v", n, g.sent, g.gos, g.botLines)
	}
	if strings.Contains(err.Error(), "refused 8 times") {
		t.Fatalf("%v", err)
	}
}

// A reachable portal is walked as before: a GM account sends no .go xyz and no summon.
func TestRemoteEnterReachableWithGMWalksAsBefore(t *testing.T) {
	g := newPortalFake(t, enterAck(3))
	g.level = 1
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.gos) != 0 || len(g.botLines) != 0 || len(g.summoned) != 0 || rep.Remote.Enter.PartyMoved != "" || rep.Remote.GMCommands != 0 {
		t.Fatalf("go %v lines %v summoned %v moved %q", g.gos, g.botLines, g.summoned, rep.Remote.Enter.PartyMoved)
	}
	if n := len(g.walkMoves()); n < 3 || len(g.sent) != 1 {
		t.Fatalf("moves %d sent %v", n, g.sent)
	}
}

// A path of more than 300 yd and a GM account: the party is moved without a single walking order; without a GM it is walked.
func TestRemoteEnterVeryLongPathMovesTheParty(t *testing.T) {
	g := newPortalFake(t, enterAck(3))
	g.level = 1
	g.planLen = 500
	g.lagFollowers()
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if m := g.walkMoves(); len(g.gos) != 1 || len(m) == 0 || m[0] != "cmd move 300.00 0.00 0.00" /* only the hold on the landing spot */ || !strings.Contains(notesOf(rep), "the path to the portal is 5") {
		t.Fatalf("go %v moves %v\n%s", g.gos, m, notesOf(rep))
	}

	p := newPortalFake(t, enterAck(3))
	p.level = 0
	p.planLen = 500
	rep, _ = p.run(RemoteOptions{}) // the fake's detour is slow: only that no GM line went out and the walk was chosen is looked at
	if len(p.gos) != 0 || len(p.botLines) != 0 || !strings.Contains(notesOf(rep), "the path to the portal is 500 yd; walking it") || len(p.ordersOf("cmd move")) == 0 {
		t.Fatalf("go %v lines %v\n%s", p.gos, p.botLines, notesOf(rep))
	}
}

// The lead bot on another map than the portal: a GM account moves the party (the commander is teleported, the bots summoned); a player's
// account fails as before.
func TestRemoteEnterPartyOnAnotherMapIsMovedByGM(t *testing.T) {
	g := newPortalFake(t, enterAck(3))
	g.level = 1
	g.lagFollowers()
	for _, p := range g.party {
		g.memberMap[p.GUID] = 1
	}
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.gos) != 1 || strings.Join(g.summoned, ",") != "Healy,Magey" || len(g.sent) != 1 {
		t.Fatalf("go %v summoned %v sent %v", g.gos, g.summoned, g.sent)
	}

	p := newPortalFake(t, enterAck(3))
	p.level = 0
	for _, m := range p.party {
		p.memberMap[m.GUID] = 1
	}
	if _, err = p.run(RemoteOptions{}); err == nil || !strings.Contains(err.Error(), "is on map 1; the portal is on map 0") || len(p.gos) != 0 {
		t.Fatalf("%v go %v", err, p.gos)
	}
}

// The planner gave a real path and the module still has none to its first point: the portal is unreachable (the tests above). A planner that
// failed made a straight-line point, and a refusal of that says nothing about the portal: the leg is halved and the move asked again first.
func TestRemoteEnterPlannerFailureHalvesBeforeAnyGMMove(t *testing.T) {
	g := newPortalFake(t, enterAck(3))
	g.level = 1
	g.noPlanner = true
	g.moveErrs = []string{"unreachable"}
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.gos) != 0 || len(g.botLines) != 0 || len(g.summoned) != 0 || len(g.sent) != 1 || rep.Remote.Enter.PartyMoved != "" {
		t.Fatalf("go %v lines %v summoned %v sent %v", g.gos, g.botLines, g.summoned, g.sent)
	}
	// An off-mesh first point is the same: halve and replan, never a GM move.
	o := newPortalFake(t, enterAck(3))
	o.level = 1
	o.moveErrs = []string{"off-mesh"}
	rep, err = o.run(RemoteOptions{})
	if err != nil || rep.State != "done" || len(o.gos) != 0 || len(o.sent) != 1 {
		t.Fatalf("%v %s go %v sent %v", err, rep.State, o.gos, o.sent)
	}
}

// Only "unreachable" answers for the whole budget with no planner: the portal is given up after the walk's tries, by a GM move (late) or the clear failure.
func TestRemoteEnterNoPlannerUnreachableAfterTheBudget(t *testing.T) {
	errs := []string{}
	for i := 0; i < 12; i++ {
		errs = append(errs, "unreachable")
	}
	g := newPortalFake(t, enterAck(3))
	g.level = 1
	g.noPlanner = true
	g.lagFollowers()
	g.moveErrs = append([]string{}, errs...)
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" || len(g.gos) != 1 || len(g.sent) != 1 || len(g.botLines) != 1 {
		t.Fatalf("%v %s go %v sent %v\n%s", err, rep.State, g.gos, g.sent, notesOf(rep))
	}
	if n := len(g.walkMoves()); n < 8 {
		t.Fatalf("the budget was not spent first: %d moves %v", n, g.walkMoves())
	}
	p := newPortalFake(t, enterAck(3))
	p.level = 0
	p.noPlanner = true
	p.moveErrs = append([]string{}, errs...)
	_, err = p.run(RemoteOptions{})
	if err == nil || !strings.Contains(err.Error(), "no walkable path to the portal") || len(p.gos) != 0 || len(p.sent) != 0 || len(p.ordersOf("cmd move")) < 8 {
		t.Fatalf("%v go %v sent %v moves %d", err, p.gos, p.sent, len(p.ordersOf("cmd move")))
	}
}

// The follow after the summons must be acknowledged: a refusal fails the phase before cmd enter; a busy answer is asked again.
func TestRemoteEnterFollowAfterTheSummonsMustBeConfirmed(t *testing.T) {
	g := newPortalFake(t, enterAck(3))
	g.level = 1
	g.lagFollowers()
	g.moveErrs = []string{"unreachable"}
	g.cmdErr["follow"] = []string{"denied"}
	rep, err := g.run(RemoteOptions{})
	if err == nil || !strings.Contains(err.Error(), "cmd follow after the summons was not confirmed (denied") || len(g.sent) != 0 || rep.State == "done" {
		t.Fatalf("%v sent %v", err, g.sent)
	}
	if n := len(g.ordersOf("cmd follow")); n != 1 {
		t.Fatalf("a refusal that is not transient was asked %d times", n)
	}

	b := newPortalFake(t, enterAck(3))
	b.level = 1
	b.lagFollowers()
	b.moveErrs = []string{"unreachable"}
	b.cmdErr["follow"] = []string{"busy", "busy"}
	rep, err = b.run(RemoteOptions{})
	if err != nil || rep.State != "done" || len(b.sent) != 1 || len(b.ordersOf("cmd follow")) != 3 || !strings.Contains(notesOf(rep), "acknowledged at try 3") {
		t.Fatalf("%v %s follow %v sent %v", err, rep.State, b.ordersOf("cmd follow"), b.sent)
	}

	c := newPortalFake(t, enterAck(3))
	c.level = 1
	c.lagFollowers()
	c.moveErrs = []string{"unreachable"}
	c.cmdErr["follow"] = []string{"busy", "busy", "busy", "busy"}
	_, err = c.run(RemoteOptions{})
	if err == nil || !strings.Contains(err.Error(), "not confirmed (busy") || len(c.sent) != 0 || len(c.ordersOf("cmd follow")) != 3 {
		t.Fatalf("%v follow %d sent %v", err, len(c.ordersOf("cmd follow")), c.sent)
	}
}

// Every configured bot counts as apart until the feed shows it alive within 15 yd of the portal: one missing from the feed, on another map or
// dead is summoned (a dead one revived first) and waited for; one that never comes fails the phase before PartyMoved and cmd enter.
func TestRemoteEnterGatherCountsMissingDeadAndFarBotsAsApart(t *testing.T) {
	moved := func(t *testing.T, tweak func(g *portalFake)) (*portalFake, Report, error) {
		g := newPortalFake(t, enterAck(3))
		g.level = 1
		g.lagFollowers()
		g.moveErrs = []string{"unreachable"}
		tweak(g)
		rep, err := g.run(RemoteOptions{})
		return g, rep, err
	}
	// missing from the feed
	g, rep, err := moved(t, func(g *portalFake) { g.left = map[string]bool{"Healy": true} })
	if err != nil || rep.State != "done" || !strings.Contains(strings.Join(g.summoned, ","), "Healy") || len(g.sent) != 1 {
		t.Fatalf("missing: %v %s summoned %v\n%s", err, rep.State, g.summoned, notesOf(rep))
	}
	// on another map
	g, rep, err = moved(t, func(g *portalFake) { g.memberMap[g.party[1].GUID] = 1 })
	if err != nil || rep.State != "done" || !strings.Contains(strings.Join(g.summoned, ","), "Healy") {
		t.Fatalf("other map: %v %s", err, rep.State)
	}
	// dead: revived, then summoned
	g, rep, err = moved(t, func(g *portalFake) { g.party[1].Dead, g.party[1].HP = true, 0 })
	if err != nil || rep.State != "done" || len(g.revived) != 1 || g.revived[0] != "Healy" || !strings.Contains(strings.Join(g.summoned, ","), "Healy") || len(g.sent) != 1 {
		t.Fatalf("dead: %v %s revived %v summoned %v\n%s", err, rep.State, g.revived, g.summoned, notesOf(rep))
	}
	// a bot a summon does not bring fails the phase before cmd enter, whichever way it was apart
	for name, tweak := range map[string]func(g *portalFake){
		"missing": func(g *portalFake) {
			g.left = map[string]bool{"Healy": true}
			g.stuckBot = map[string]bool{"Healy": true}
		},
		"other map": func(g *portalFake) { g.memberMap[g.party[1].GUID] = 1; g.stuckBot = map[string]bool{"Healy": true} },
	} {
		g, rep, err = moved(t, tweak)
		if err == nil || !strings.Contains(err.Error(), "the party did not gather at the portal: Healy") || len(g.sent) != 0 || (rep.Remote != nil && rep.Remote.Enter != nil && rep.Remote.Enter.PartyMoved != "") {
			t.Fatalf("%s: %v sent %v", name, err, g.sent)
		}
	}
}
