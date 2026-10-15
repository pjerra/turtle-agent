package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The gossip step with the agent's own character (gossip.go, driver_gossip.go, remote_gossip.go).

const (
	npcWeegli     uint32 = 7607
	guidWeegli           = packet.GUID(0xF130001DB7000001)
	menuBefore    uint32 = 1511
	menuReady     uint32 = 1513
	weegliName           = "Weegli Blastfuse"
	gossipReachYd        = 5.0
)

var weegliAt = Point{30, 0, 0}

// npcTalk is the NPC's side of a gossip: it answers a hello with the next scripted menu (the last one repeats) and nothing from farther than
// reach yards, and a select with sel.
type npcTalk struct {
	menus    []GossipAnswer
	sel      GossipAnswer
	reach    float64
	helloErr error
	hellos   int
	selects  []uint32
	helloAt  []float64 // the agent's distance from the NPC at each hello
	// dist is how far the agent stands from the NPC now; tick moves the fake clock a second per call.
	dist func() float64
	tick func()
}

func menuOf(textID uint32, opts ...GossipLine) GossipAnswer {
	return GossipAnswer{Kind: GossipAnswerMenu, TextID: textID, Options: opts}
}

func (n *npcTalk) GossipHello(ctx context.Context, npc packet.GUID, wait time.Duration) (GossipAnswer, error) {
	n.hellos++
	d := n.dist()
	n.helloAt = append(n.helloAt, d)
	if n.tick != nil {
		n.tick()
	}
	if n.helloErr != nil {
		return GossipAnswer{}, n.helloErr
	}
	if npc != guidWeegli || d > n.reach {
		return GossipAnswer{Kind: GossipAnswerNone}, nil
	}
	i := min(n.hellos, len(n.menus)) - 1
	return n.menus[i], nil
}

func (n *npcTalk) GossipSelect(ctx context.Context, npc packet.GUID, option uint32, wait time.Duration) (GossipAnswer, error) {
	n.selects = append(n.selects, option)
	return n.sel, nil
}

var closeAnswer = GossipAnswer{Kind: GossipAnswerComplete}

func readyMenu() GossipAnswer {
	return menuOf(menuReady, GossipLine{Index: 0, Text: "Weegli, blow the door!"})
}

func eventMenu() GossipAnswer { return menuOf(menuBefore) } // before the event: text 1511, no options

// ---- group mode: the leader is the agent ----

type gossipGroup struct {
	*fakeGame
	talk *npcTalk
}

func (g *gossipGroup) GossipHello(ctx context.Context, npc packet.GUID, w time.Duration) (GossipAnswer, error) {
	return g.talk.GossipHello(ctx, npc, w)
}

func (g *gossipGroup) GossipSelect(ctx context.Context, npc packet.GUID, o uint32, w time.Duration) (GossipAnswer, error) {
	return g.talk.GossipSelect(ctx, npc, o, w)
}

func newGossipGroup(menus ...GossipAnswer) *gossipGroup {
	g := &gossipGroup{fakeGame: newFake()}
	u := g.add(guidWeegli, npcWeegli, weegliAt)
	u.Name, u.Hostile = weegliName, false
	g.talk = &npcTalk{menus: menus, sel: closeAnswer, reach: gossipReachYd,
		dist: func() float64 { return dist3(g.pos, weegliAt) },
		tick: func() { g.now = g.now.Add(time.Second) }}
	return g
}

const gossipLegJSON = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"end","to":{"x":10,"y":0,"z":0},"steps":[{"gossip":{"npc":7607,"near":{"x":30,"y":0,"z":0},%s}}]}]}`

func gossipPlan(t *testing.T, fields string) *Plan {
	t.Helper()
	return mustPlan(t, fmt.Sprintf(gossipLegJSON, fields))
}

func runGossipGroup(t *testing.T, g *gossipGroup, plan *Plan) (*Driver, error) {
	t.Helper()
	d := NewDriver(plan, g, Config{}, g.clock())
	return d, d.Run(context.Background(), 0)
}

func TestGroupGossipWalksNextToTheNPCAndPicksTheOption(t *testing.T) {
	g := newGossipGroup(readyMenu())
	d, err := runGossipGroup(t, g, gossipPlan(t, `"option":0`))
	if err != nil {
		t.Fatalf("%v\n%s", err, notes(d))
	}
	if fmt.Sprint(g.talk.selects) != "[0]" || g.talk.hellos != 1 {
		t.Fatalf("hellos %d selects %v", g.talk.hellos, g.talk.selects)
	}
	// The hello came from within the server's range: the leader walked to a spot short of the NPC (2.5 yd), not onto it.
	if dd := g.talk.helloAt[0]; dd > gossipReach || dd < 1 {
		t.Fatalf("the leader said hello from %.1f yd", dd)
	}
	if d.Report().State != "done" || !strings.Contains(notes(d), "option 0 (\"Weegli, blow the door!\") of menu 1513 picked") {
		t.Fatalf("%s", notes(d))
	}
}

func TestGroupGossipRetriesAMenuThatLacksTheOptionThenPicksIt(t *testing.T) {
	g := newGossipGroup(eventMenu(), eventMenu(), readyMenu())
	d, err := runGossipGroup(t, g, gossipPlan(t, `"text":"BLOW THE DOOR","retry_s":15`))
	if err != nil {
		t.Fatalf("%v\n%s", err, notes(d))
	}
	// Case-insensitive substring; the third hello had the option.
	if g.talk.hellos != 3 || fmt.Sprint(g.talk.selects) != "[0]" {
		t.Fatalf("hellos %d selects %v", g.talk.hellos, g.talk.selects)
	}
}

func TestGroupGossipFailsWithTheMenuItSaw(t *testing.T) {
	g := newGossipGroup(eventMenu())
	d, err := runGossipGroup(t, g, gossipPlan(t, `"option":0,"retry_s":6,"fail_text":"the event is not over"`))
	if err == nil || !strings.Contains(err.Error(), "Weegli Blastfuse shows menu 1511: the event is not over") {
		t.Fatalf("%v", err)
	}
	// Asked again every 2 s for 6 s, then given up: four hellos, nothing selected.
	if g.talk.hellos < 3 || g.talk.hellos > 5 || len(g.talk.selects) != 0 {
		t.Fatalf("hellos %d selects %v", g.talk.hellos, g.talk.selects)
	}
	if d.Report().State != "failed" {
		t.Fatalf("%+v", d.Report())
	}
	// Without a fail_text the failure says what the menu offers.
	g = newGossipGroup(menuOf(menuBefore, GossipLine{Index: 3, Text: "I need a vendor"}))
	_, err = runGossipGroup(t, g, gossipPlan(t, `"text":"door","retry_s":2`))
	if err == nil || !strings.Contains(err.Error(), `shows menu 1511, which lacks "door" (it offers: 3 "I need a vendor")`) {
		t.Fatalf("%v", err)
	}
}

func TestGroupGossipFailsAtOnceForAMissingOrDeadNPC(t *testing.T) {
	g := newGossipGroup(readyMenu())
	g.units = nil // not in view, also after the walk to its spawn
	_, err := runGossipGroup(t, g, gossipPlan(t, `"option":0`))
	if err == nil || !strings.Contains(err.Error(), "NPC 7607 is not in view within 15 yd of 30.0 0.0 0.0: it is missing") || g.talk.hellos != 0 {
		t.Fatalf("%v hellos %d", err, g.talk.hellos)
	}
	g = newGossipGroup(readyMenu())
	g.units[0].Dead, g.units[0].Health = true, 0
	_, err = runGossipGroup(t, g, gossipPlan(t, `"option":0`))
	if err == nil || !strings.Contains(err.Error(), "Weegli Blastfuse (NPC 7607) is dead") || g.talk.hellos != 0 || len(g.walks) > 1 {
		t.Fatalf("%v hellos %d walks %v", err, g.talk.hellos, g.walks)
	}
}

func TestGroupGossipNoAnswerWhenOutOfReach(t *testing.T) {
	// A stand 9 yd from the NPC: the server answers nothing (probe 2026-10-15: nothing at 9-10 yd, text 1511 within 3.6).
	g := newGossipGroup(readyMenu())
	_, err := runGossipGroup(t, g, gossipPlan(t, `"option":0,"retry_s":4,"stand":{"x":21,"y":0,"z":0}`))
	if err == nil || !strings.Contains(err.Error(), "did not answer the hello (the agent stands 9.0 yd from it") || len(g.talk.selects) != 0 {
		t.Fatalf("%v", err)
	}
}

func TestGroupGossipOptionRules(t *testing.T) {
	two := menuOf(menuReady, GossipLine{Index: 0, Text: "Blow the door"}, GossipLine{Index: 1, Text: "Blow it up"})
	g := newGossipGroup(two)
	_, err := runGossipGroup(t, g, gossipPlan(t, `"text":"blow"`))
	if err == nil || !strings.Contains(err.Error(), `the text "blow" matches 2 options`) || len(g.talk.selects) != 0 {
		t.Fatalf("an ambiguous text: %v", err)
	}
	g = newGossipGroup(two)
	if _, err = runGossipGroup(t, g, gossipPlan(t, `"option":1`)); err != nil || fmt.Sprint(g.talk.selects) != "[1]" {
		t.Fatalf("the index is the menu's, not a position: %v %v", err, g.talk.selects)
	}
	g = newGossipGroup(menuOf(menuReady, GossipLine{Index: 0, Text: "Enter code", Coded: true}))
	_, err = runGossipGroup(t, g, gossipPlan(t, `"option":0`))
	if err == nil || !strings.Contains(err.Error(), "asks for a code") || len(g.talk.selects) != 0 {
		t.Fatalf("a coded option: %v", err)
	}
}

func TestGroupGossipEndsOnTheCloseOnly(t *testing.T) {
	for name, ans := range map[string]GossipAnswer{
		"another menu": menuOf(1600, GossipLine{Index: 0, Text: "more"}),
		"a panel":      {Kind: GossipAnswerOther, Other: "vendor_list"},
		"nothing":      {Kind: GossipAnswerNone},
	} {
		g := newGossipGroup(readyMenu())
		g.talk.sel = ans
		_, err := runGossipGroup(t, g, gossipPlan(t, `"option":0`))
		if err == nil || len(g.talk.selects) != 1 {
			t.Errorf("%s: %v %v", name, err, g.talk.selects)
		}
	}
	g := newGossipGroup(readyMenu())
	g.talk.sel = menuOf(1600)
	_, err := runGossipGroup(t, g, gossipPlan(t, `"option":0`))
	if err == nil || !strings.Contains(err.Error(), "with another menu (1600), not a close") {
		t.Fatalf("%v", err)
	}
}

func TestGroupGossipByTankIsTheAgentsOwnTalk(t *testing.T) {
	g := newGossipGroup(readyMenu())
	d, err := runGossipGroup(t, g, gossipPlan(t, `"option":0,"by":"tank"`))
	if err != nil || !strings.Contains(notes(d), "by tank: the agent leads this run and talks to the NPC itself") || g.talk.hellos != 1 {
		t.Fatalf("%v\n%s", err, notes(d))
	}
}

func TestGroupGossipNeedsHands(t *testing.T) {
	g := newFake() // no GossipTalker
	g.add(guidWeegli, npcWeegli, weegliAt)
	d := NewDriver(gossipPlan(t, `"option":0`), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "cannot talk to an NPC") {
		t.Fatalf("%v", err)
	}
}

var _ = objects.Unit{}
