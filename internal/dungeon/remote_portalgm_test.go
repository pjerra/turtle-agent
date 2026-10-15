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
	planLen          float64 // the agent's navmesh path from the lead bot to the portal (0: the game plans nothing)
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
	if g.planLen <= 0 || mapID != outsideMap {
		return nil, errors.New("no planner")
	}
	d := dist3(from, to)
	if d == 0 {
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
			if strings.EqualFold(p.Name, name) {
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
