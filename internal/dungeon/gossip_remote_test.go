package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The gossip step in remote mode (remote_gossip.go): by the agent, by a bot's `cmd gossip` order, and the agent as the fallback of a module
// that does not know the order.

// gossipRemote is a nearGame (a GM agent that follows the party, sees units and takes orders) with an NPC and its menus.
type gossipRemote struct {
	*nearGame
	talk    *npcTalk
	answers []tbm.OrderResult // answers to the first gossip orders, in order; after them an ACK
	orders  []string          // every gossip order
}

func newGossipRemote(t *testing.T, menus ...GossipAnswer) *gossipRemote {
	g := &gossipRemote{nearGame: newNearGame(t, 3)}
	g.units = []objects.Unit{{GUID: guidWeegli, Type: "creature", Entry: npcWeegli, Name: weegliName, X: weegliAt.X, Y: weegliAt.Y, Z: weegliAt.Z, Health: 100, MaxHealth: 100}}
	g.talk = &npcTalk{menus: menus, sel: closeAnswer, reach: gossipReachYd,
		dist: func() float64 { return dist3(g.agent, weegliAt) },
		tick: func() { g.advance(time.Second) }}
	return g
}

// Units shows the distance from the agent, as the session's store does.
func (g *gossipRemote) Units() []objects.Unit {
	out := append([]objects.Unit(nil), g.units...)
	for i := range out {
		out[i].Distance = float32(dist3(g.agent, Point{out[i].X, out[i].Y, out[i].Z}))
	}
	return out
}

func (g *gossipRemote) GossipHello(ctx context.Context, npc packet.GUID, w time.Duration) (GossipAnswer, error) {
	return g.talk.GossipHello(ctx, npc, w)
}

func (g *gossipRemote) GossipSelect(ctx context.Context, npc packet.GUID, o uint32, w time.Duration) (GossipAnswer, error) {
	return g.talk.GossipSelect(ctx, npc, o, w)
}

func (g *gossipRemote) Order(ctx context.Context, command string) (tbm.OrderResult, error) {
	f := strings.Fields(command)
	if len(f) < 4 || f[1] != "gossip" {
		return g.nearGame.Order(ctx, command)
	}
	g.orders = append(g.orders, command)
	g.nearGame.orders = append(g.nearGame.orders, command)
	g.advance(2 * time.Second)
	if len(g.answers) > 0 {
		a := g.answers[0]
		g.answers = g.answers[1:]
		a.Command = command
		return a, nil
	}
	return okOrder(command, f[3], "done"), nil
}

func gossipRemotePlan(t *testing.T, fields string) *Plan {
	t.Helper()
	return remotePlan(Leg{Name: "end", To: Point{20, 0, 0}, Steps: []Step{gossipPlan(t, fields).Legs[0].Steps[0]}})
}

func gossipRun(t *testing.T, g *gossipRemote, plan *Plan, mod func(*RemoteOptions)) (Report, error) {
	t.Helper()
	g.agent = Point{-300, 0, 0} // far behind the party
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	cfg.Setup = []string{"gm visible off"}
	o := objOpts()
	if mod != nil {
		mod(&o)
	}
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

func TestRemoteGossipByTheAgentBringsItNextToTheNPC(t *testing.T) {
	g := newGossipRemote(t, readyMenu())
	rep, err := gossipRun(t, g, gossipRemotePlan(t, `"option":0`), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	// `.appear <lead bot> (a GM assist), then a walk next to the NPC; the hello came from within the server's range; no order went out.
	if fmt.Sprint(g.appears) != "[Tanky]" || len(g.walks) == 0 || g.talk.helloAt[0] > gossipReach || len(g.orders) != 0 {
		t.Fatalf("appears %v walks %v hello at %v orders %v", g.appears, g.walks, g.talk.helloAt, g.orders)
	}
	if fmt.Sprint(g.talk.selects) != "[0]" || rep.State != "done" {
		t.Fatalf("selects %v state %s", g.talk.selects, rep.State)
	}
	r := rep.Remote
	s := r.ObjectSteps[0]
	if s.Kind != "gossip" || s.Carrier != "agent" || s.Result != "done" || r.RunGMAssists != 1 || r.Carrier != "" {
		t.Fatalf("%+v\n%+v", s, r)
	}
}

func TestRemoteGossipRetriesThenFailsWithTheMenuId(t *testing.T) {
	g := newGossipRemote(t, eventMenu(), eventMenu(), readyMenu())
	rep, err := gossipRun(t, g, gossipRemotePlan(t, `"text":"blow the door"`), nil)
	if err != nil || g.talk.hellos != 3 || fmt.Sprint(g.talk.selects) != "[0]" {
		t.Fatalf("%v hellos %d\n%s", err, g.talk.hellos, notesOf(rep))
	}
	g = newGossipRemote(t, eventMenu())
	rep, err = gossipRun(t, g, gossipRemotePlan(t, `"option":0,"retry_s":6,"fail_text":"the event is not over"`), nil)
	if err == nil || !strings.Contains(err.Error(), "Weegli Blastfuse shows menu 1511: the event is not over") || len(g.talk.selects) != 0 || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if s := rep.Remote.ObjectSteps[0]; s.Result != "failed" || !strings.Contains(s.Error, "menu 1511") {
		t.Fatalf("%+v", s)
	}
}

func TestRemoteGossipFailsAtOnceForAMissingOrDeadNPC(t *testing.T) {
	g := newGossipRemote(t, readyMenu())
	g.units = nil
	_, err := gossipRun(t, g, gossipRemotePlan(t, `"option":0`), nil)
	if err == nil || !strings.Contains(err.Error(), "NPC 7607 is not in view") || g.talk.hellos != 0 {
		t.Fatalf("%v", err)
	}
	for _, by := range []string{"agent", "carrier"} {
		g = newGossipRemote(t, readyMenu())
		g.units[0].Dead, g.units[0].Health = true, 0
		_, err = gossipRun(t, g, gossipRemotePlan(t, `"option":0,"by":"`+by+`"`), nil)
		if err == nil || !strings.Contains(err.Error(), "Weegli Blastfuse (NPC 7607) is dead") || g.talk.hellos != 0 || len(g.orders) != 0 {
			t.Fatalf("by %s: %v hellos %d orders %v", by, err, g.talk.hellos, g.orders)
		}
	}
}

func TestRemoteGossipByTheCarrierSendsTheOrderAndTheAckEndsIt(t *testing.T) {
	g := newGossipRemote(t, readyMenu())
	rep, err := gossipRun(t, g, gossipRemotePlan(t, `"option":0,"by":"carrier"`), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	// The carrier is the first bot that is neither the lead, the tank nor the healer. The plan's option 0 (the menu's first line) is the
	// order's 1: `cmd gossip` counts from 1 and refuses 0 (TortoiseBots GossipOrderPolicy.h).
	if fmt.Sprint(g.orders) != "[cmd gossip 7607 Magey 1]" || g.talk.hellos != 0 || len(g.talk.selects) != 0 {
		t.Fatalf("orders %v hellos %d selects %v", g.orders, g.talk.hellos, g.talk.selects)
	}
	s := rep.Remote.ObjectSteps[0]
	if s.Kind != "gossip" || s.Carrier != "Magey" || s.Result != "done" || len(s.Orders) != 1 || s.Orders[0].Result != "ack" || s.FallbackUsed {
		t.Fatalf("%+v", s)
	}
	if rep.Remote.Orders["gossip"] != 1 {
		t.Fatalf("%+v", rep.Remote.Orders)
	}
	// The party was held at the NPC for it and let go after.
	if g.nearGame.indexOf("cmd stay") < 0 || g.nearGame.lastIndexOf("cmd follow") < g.nearGame.indexOf("cmd gossip") {
		t.Fatalf("%v", g.nearGame.orders)
	}
}

func TestRemoteGossipByTheTankOrdersTheTankWithTheText(t *testing.T) {
	g := newGossipRemote(t)
	rep, err := gossipRun(t, g, gossipRemotePlan(t, `"text":"blow the door","by":"tank"`), nil)
	if err != nil || fmt.Sprint(g.orders) != "[cmd gossip 7607 Tanky blow the door]" {
		t.Fatalf("%v %v\n%s", err, g.orders, notesOf(rep))
	}
	if s := rep.Remote.ObjectSteps[0]; s.Carrier != "Tanky" || s.Result != "done" {
		t.Fatalf("%+v", s)
	}
	// Without a tank option and before any pull shows one, there is none to order.
	g = newGossipRemote(t)
	_, err = gossipRun(t, g, gossipRemotePlan(t, `"option":0,"by":"tank"`), func(o *RemoteOptions) { o.Tank = "" })
	if err == nil || !strings.Contains(err.Error(), "by tank: no tank is known") || len(g.orders) != 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
}

func TestRemoteGossipOrderRefusedIsSentAgainThenFails(t *testing.T) {
	g := newGossipRemote(t)
	g.answers = []tbm.OrderResult{errOrder("cmd gossip", "no-option"), errOrder("cmd gossip", "no-option"), errOrder("cmd gossip", "no-option")}
	rep, err := gossipRun(t, g, gossipRemotePlan(t, `"option":0,"by":"carrier"`), nil)
	if err != nil || len(g.orders) != 4 || rep.State != "done" {
		t.Fatalf("a refusal that clears within retry_s: %v orders %d\n%s", err, len(g.orders), notesOf(rep))
	}
	g = newGossipRemote(t)
	for i := 0; i < 20; i++ {
		g.answers = append(g.answers, errOrder("cmd gossip", "no-option"))
	}
	rep, err = gossipRun(t, g, gossipRemotePlan(t, `"option":0,"by":"carrier","retry_s":5,"fail_text":"the event is not over"`), nil)
	if err == nil || !strings.Contains(err.Error(), "no-option") || !strings.Contains(err.Error(), "the event is not over") || len(g.orders) < 2 || len(g.orders) > 5 || g.talk.hellos != 0 {
		t.Fatalf("%v orders %d", err, len(g.orders))
	}
	if s := rep.Remote.ObjectSteps[0]; s.Result != "failed" || s.FallbackUsed {
		t.Fatalf("a refusal is no reason for the agent to take over: %+v", s)
	}
}

func TestRemoteGossipFallsBackToTheAgentOnceWhenTheModuleLacksTheVerb(t *testing.T) {
	for name, ans := range map[string]tbm.OrderResult{
		"unknown verb": errOrder("cmd gossip", "unknown-verb"),
		"no answer":    {Intent: "cmd gossip", Kind: tbm.KindTimeout, Lines: []string{}},
	} {
		g := newGossipRemote(t, readyMenu())
		g.answers = []tbm.OrderResult{ans}
		rep, err := gossipRun(t, g, gossipRemotePlan(t, `"option":0,"by":"carrier"`), nil)
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, notesOf(rep))
		}
		if len(g.orders) != 1 || g.talk.hellos != 1 || fmt.Sprint(g.talk.selects) != "[0]" {
			t.Fatalf("%s: orders %v hellos %d selects %v", name, g.orders, g.talk.hellos, g.talk.selects)
		}
		s := rep.Remote.ObjectSteps[0]
		if !s.FallbackUsed || s.Fallback == nil || s.Fallback.Result != "done" || s.Result != "done" || !hasNote(rep, "the module has no `cmd gossip`") || !hasNote(rep, "the agent talks to the NPC itself") {
			t.Fatalf("%s: %+v\n%s", name, s, notesOf(rep))
		}
	}
	// The fallback is one attempt: when the agent cannot do it either, the step fails with both reasons.
	g := newGossipRemote(t, eventMenu())
	g.answers = []tbm.OrderResult{errOrder("cmd gossip", "unknown-verb")}
	rep, err := gossipRun(t, g, gossipRemotePlan(t, `"option":0,"by":"carrier","retry_s":4`), nil)
	if err == nil || !strings.Contains(err.Error(), "unknown-verb") || !strings.Contains(err.Error(), "the agent's fallback failed too") || !strings.Contains(err.Error(), "menu 1511") || len(g.orders) != 1 {
		t.Fatalf("%v", err)
	}
	if s := rep.Remote.ObjectSteps[0]; s.Fallback == nil || s.Fallback.Result != "failed" {
		t.Fatalf("%+v", s)
	}
}

func TestRemoteGossipByTheAgentNeedsHands(t *testing.T) {
	// A game that cannot talk (the nearGame has no GossipTalker) fails the agent's step with the reason, before it moves anything.
	g := newGossipRemote(t, readyMenu())
	d := &RemoteDriver{g: g.nearGame}
	if err := d.gossipByAgent(context.Background(), Action{Gossip: &Gossip{NPC: 1}}); err == nil || !strings.Contains(err.Error(), "the agent has no hands for a gossip step") {
		t.Fatalf("%v", err)
	}
}

// The order counts the menu from 1 (TortoiseBots GossipOrderPolicy.h refuses 0), the plan's option is the menu's own index from 0: the order
// carries option + 1, and a text goes as it is.
func TestGossipOrderCountsFromOne(t *testing.T) {
	two, zero := uint32(2), uint32(0)
	for _, tc := range []struct {
		g    Gossip
		want string
	}{
		{Gossip{NPC: 7607, Option: &zero}, "cmd gossip 7607 Tanky 1"},
		{Gossip{NPC: 7607, Option: &two}, "cmd gossip 7607 Tanky 3"},
		{Gossip{NPC: 7604, Text: "settled"}, "cmd gossip 7604 Tanky settled"},
	} {
		if got := gossipOrder(&tc.g, "Tanky"); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.g, got, tc.want)
		}
	}
}
