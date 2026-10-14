package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The agent kept near the party (remote_near.go).

// nearGame is a fakeCarrier whose game is also a Follower: the agent's character stands at agent.
type nearGame struct {
	*fakeCarrier
	agent     Point
	agentMap  uint32
	level     int
	known     bool
	appears   []string
	appearErr []error // errors the next appears answer with
	walks     []Point
	walkErr   error
	probes    int
	onAppear  func(g *nearGame)
	blockWalk bool        // WalkSelf waits for its context to end (a long walk)
	walking   atomic.Bool // a walk is under way
}

func newNearGame(t *testing.T, level int) *nearGame {
	return &nearGame{fakeCarrier: newFakeCarrier(t), level: level, known: true}
}

func (n *nearGame) AgentPlace() (Point, uint32, bool) { return n.agent, n.agentMap, true }

func (n *nearGame) AccountLevel(ctx context.Context) (int, bool, error) {
	n.probes++
	return n.level, n.known, nil
}

func (n *nearGame) Appear(ctx context.Context, player string) error {
	n.appears = append(n.appears, player)
	if len(n.appearErr) > 0 {
		err := n.appearErr[0]
		n.appearErr = n.appearErr[1:]
		return err
	}
	l := n.lead()
	n.agent = Point{l.X, l.Y, l.Z}
	if n.onAppear != nil {
		n.onAppear(n)
	}
	return nil
}

func (n *nearGame) WalkSelf(ctx context.Context, to Point) (WalkResult, error) {
	n.walks = append(n.walks, to)
	if n.blockWalk {
		n.walking.Store(true)
		<-ctx.Done()
		n.walking.Store(false)
		return WalkResult{Result: "partial"}, ctx.Err()
	}
	if n.walkErr != nil {
		return WalkResult{}, n.walkErr
	}
	n.agent = to
	return WalkResult{Result: "arrived"}, nil
}

func nearRun(t *testing.T, g *nearGame, plan *Plan) (Report, error) {
	t.Helper()
	g.agent = Point{-300, 0, 0} // far behind the party's start
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	d := NewRemoteDriver(plan, g, cfg, objOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

func TestAgentAppearsAtTheLeadAfterTheLegsWalk(t *testing.T) {
	g := newNearGame(t, 3) // a developer-level GM
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}, Leg{Name: "b", To: Point{220, 0, 0}}))
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	// One appear after leg a's walk; leg b's walk of 20 yd leaves the agent within 40 yd.
	if fmt.Sprint(g.appears) != "[Tanky]" || len(g.walks) != 0 || g.probes != 1 {
		t.Fatalf("appears %v walks %v probes %d", g.appears, g.walks, g.probes)
	}
	r := rep.Remote
	if r.AgentAppears != 1 || r.GMCommands != 0 || fmt.Sprint(rep.SetupShortcuts) != "[.appear Tanky]" {
		t.Fatalf("%+v setup %v", r, rep.SetupShortcuts)
	}
	if !strings.Contains(notesOf(rep), "a setup shortcut, not a run GM command") {
		t.Fatalf("%s", notesOf(rep))
	}
}

func TestAgentThatIsNotAGMWalksBehindTheParty(t *testing.T) {
	g := newNearGame(t, 0)
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}))
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if len(g.appears) != 0 || len(g.walks) != 1 || len(rep.SetupShortcuts) != 0 {
		t.Fatalf("appears %v walks %v setup %v", g.appears, g.walks, rep.SetupShortcuts)
	}
	// 30 yd short of the lead bot, on the line from the agent.
	if w := g.walks[0]; w.X < 169 || w.X > 171 || rep.Remote.AgentWalks != 1 || rep.Remote.GMCommands != 0 {
		t.Fatalf("%v %+v", w, rep.Remote)
	}
}

func TestAgentWithAnUnknownLevelIsNoGM(t *testing.T) {
	g := newNearGame(t, 3)
	g.known = false
	if _, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}})); err != nil || len(g.appears) != 0 || len(g.walks) != 1 {
		t.Fatalf("%v appears %v walks %v", err, g.appears, g.walks)
	}
}

func TestWalkingAgentKeepsOutOfAPack(t *testing.T) {
	g := newNearGame(t, 0)
	g.addMob(299, Point{150, 10, 0}) // idle, on the agent's way and near the stand point
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}))
	if err != nil || len(g.walks) != 0 || !strings.Contains(notesOf(rep), "the agent stays where it is") {
		t.Fatalf("%v walks %v\n%s", err, g.walks, notesOf(rep))
	}
}

func TestWalkingAgentStopsWhenAFightBegins(t *testing.T) {
	g := newNearGame(t, 0)
	g.blockWalk = true
	g.onTick = func(f *fakeRemote) {
		if g.walking.Load() {
			f.party[0].State = "c" // a fight begins while the agent walks
		}
	}
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}))
	if err != nil || len(g.walks) != 1 || !strings.Contains(notesOf(rep), "a fight began: the agent stopped walking") {
		t.Fatalf("%v walks %v\n%s", err, g.walks, notesOf(rep))
	}
}

func TestNoFollowerNoMovement(t *testing.T) {
	g := carrierGame(t) // a game that is no Follower: the run is as before
	if _, err := runCarrier(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}), objOpts()); err != nil {
		t.Fatal(err)
	}
}

// An object step whose chest the agent cannot see: the agent is brought up (once) and the step goes on.
func TestObjectStepBringsTheAgentUp(t *testing.T) {
	g := newNearGame(t, 3)
	g.chestLoot = []tbm.ItemCount{{Entry: itMedallion, Count: 1}}
	g.appearErr = []error{errors.New("not now")} // the walk's own appear fails; the object step's retry works
	g.onAppear = func(n *nearGame) { n.gobjs = []objects.GameObject{chestObj()} }
	plan := remotePlan(Leg{Name: "c", To: Point{200, 0, 0}, Steps: []Step{lootChestStep()}})
	rep, err := nearRun(t, g, plan)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if len(g.appears) != 2 || g.indexOf("cmd open") < 0 || rep.Remote.AgentAppears != 1 {
		t.Fatalf("appears %v %v %+v", g.appears, g.orders, rep.Remote)
	}
}
