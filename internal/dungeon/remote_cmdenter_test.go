package dungeon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The `cmd enter` entry (remote_cmdenter.go) against a fake commander whose bots start outside the plan's map: a player's account, a plan
// with an entrance_trigger and a portal but no entrance_tele. Entering is the module's order only: a lead bot that stands in the portal
// is NOT taken in (the fake's area trigger is off).

// enterOrders is an enterFake that is also an OrderGame: `cmd enter` is answered from a script (the last answer repeats), an ACK
// brings the whole party inside after 2 s, and every other order is acknowledged.
type enterOrders struct {
	*enterFake
	script []tbm.OrderResult
	sent   []string
	at     []time.Time
	// onEnterErr runs after an answer that is not an ACK (part of the party went through, or an old module took the lead bot in itself).
	onEnterErr func()
}

func (g *enterOrders) Order(ctx context.Context, command string) (tbm.OrderResult, error) {
	g.log = append(g.log, "order "+command)
	g.at = append(g.at, g.now)
	if !strings.HasPrefix(command, "cmd enter") {
		return okOrder(command, "x", "x"), nil
	}
	g.sent = append(g.sent, command)
	i := min(len(g.sent)-1, len(g.script)-1)
	r := g.script[i]
	r.Command = command
	g.advance(1 * time.Second)
	if r.Kind == tbm.KindAck {
		for _, p := range g.party {
			g.pending[p.GUID] = g.now.Add(2 * time.Second)
		}
	}
	if r.Kind != tbm.KindAck && g.onEnterErr != nil {
		g.onEnterErr()
	}
	if r.Pending != nil {
		g.advance(time.Duration(r.Pending.BudgetS) * time.Second / 4)
	}
	return r, nil
}
func (g *enterOrders) Behavior(context.Context, string, string, bool) (tbm.Reply, error) {
	return tbm.Reply{Kind: tbm.KindAck, OK: true, Lines: []string{}}, nil
}
func (g *enterOrders) GameObjects() []objects.GameObject { return nil }

func enterAck(n int) tbm.OrderResult {
	a := tbm.Ack{Intent: "cmd enter", Scope: "party", Count: n, Executor: "entered"}
	p := tbm.Pending{Intent: "cmd enter", Scope: "party", BudgetS: 40, GUID: "924"}
	return tbm.OrderResult{Intent: "cmd enter", Kind: tbm.KindAck, OK: true, Ack: &a, Pending: &p, Entered: true,
		Final: "TBM:ACTION_ACK|cmd enter|party|3|entered", Lines: []string{}}
}

func enterErr(code, msg string) tbm.OrderResult {
	e := tbm.Err{Intent: "cmd enter", Code: code, Message: msg}
	p := tbm.Pending{Intent: "cmd enter", Scope: "party", BudgetS: 40, GUID: "924"}
	return tbm.OrderResult{Intent: "cmd enter", Kind: tbm.KindErr, Err: &e, Pending: &p, Final: "TBM:ACTION_ERR|cmd enter|" + code + "|" + msg, Lines: []string{}}
}

func newEnterOrders(t *testing.T, script ...tbm.OrderResult) *enterOrders {
	g := &enterOrders{enterFake: newEnterFake(t), script: script}
	g.level, g.known = 0, true // a player's account
	g.plan.EntranceTele = ""
	g.plan.EntranceTrigger = 924
	g.plan.Portal = &Portal{Map: 0, Point: Point{300, 0, 0}}
	g.fakeRemote.onTick = g.enterFake.tick
	return g
}

func (g *enterOrders) run(o RemoteOptions) (Report, error) {
	o.Tank = "Tanky"
	o.Lead, o.Bots = "Tanky", []string{"Healy", "Magey"}
	return runRemote(g.t, g, g.plan, o, nil)
}

func TestRemoteEnterByCmdEnter(t *testing.T) {
	g := newEnterOrders(t, enterAck(3))
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	e := rep.Remote.Enter
	if e == nil || e.Method != EnterCmd || e.Tele != "" || rep.Remote.GMCommands != 0 || e.Entries != 1 || e.CmdEnter == nil || e.CmdEnter.Trigger != 924 || e.CmdEnter.Orders != 1 || e.CmdEnter.Entered != 3 {
		t.Fatalf("%+v %+v gm %d", e, e.CmdEnter, rep.Remote.GMCommands)
	}
	if len(g.sent) != 1 || g.sent[0] != "cmd enter 924" || g.idx("tele") >= 0 || g.idx("probe") < 0 {
		t.Fatalf("sent %v log %v", g.sent, g.log)
	}
	// The walk to the door comes first, in legs, then the one order; nobody is summoned (all went through together).
	if n := len(g.ordersOf("cmd move")); n < 3 || len(e.Summoned) != 0 || g.idx("summon") >= 0 {
		t.Fatalf("moves %d summoned %v log %v", n, e.Summoned, g.log)
	}
	if rep.Remote.Orders["enter"] != 1 {
		t.Fatalf("orders %v", rep.Remote.Orders)
	}
	g.noGroupChangeAfter("order cmd enter")
}

func TestRemoteEnterCmdAnswersBusyAndCancelledThenGoes(t *testing.T) {
	g := newEnterOrders(t, enterErr("busy", "Healy: busy"), enterErr("cancelled", "cancelled"), enterAck(3))
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" || len(g.sent) != 3 || rep.Remote.Enter.CmdEnter.Orders != 3 {
		t.Fatalf("%v sent %v\n%s", err, g.sent, notesOf(rep))
	}
	if rep.Remote.Refused["enter:busy"] != 1 {
		t.Fatalf("%v", rep.Remote.Refused)
	}
}

func TestRemoteEnterCmdPartialEntryLeavesTheRestToSummons(t *testing.T) {
	// "Magey: ... (1 of 3 got in)": the lead bot is in, one bot is not; the group is locked, so the phase summons it.
	g := newEnterOrders(t, enterErr("failed", "Magey: teleport refused (2 of 3 got in)"))
	g.script[0].Err.Message = "Magey: teleport refused (2 of 3 got in)"
	g.onEnterErr = func() {
		g.pending[g.lead().GUID] = g.now.Add(time.Second)
		g.pending[g.guidOf("Healy")] = g.now.Add(time.Second)
	}
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if strings.Join(rep.Remote.Enter.Summoned, ",") != "Magey" || len(g.sent) != 1 {
		t.Fatalf("summoned %v sent %v", rep.Remote.Enter.Summoned, g.sent)
	}
}

func TestRemoteEnterCmdFatalRefusalsNameTheCause(t *testing.T) {
	for code, want := range map[string]string{
		"requirement":    "entry requirement",
		"instance-limit": "TRANSFER_ABORT_TOO_MANY_INSTANCES",
		"not-found":      "entrance_trigger",
		"denied":         "not the bots' owner",
	} {
		g := newEnterOrders(t, enterErr(code, "Healy: no"))
		rep, err := g.run(RemoteOptions{})
		if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), "cmd enter refused: "+code) || !strings.Contains(err.Error(), want) || len(g.sent) != 1 {
			t.Fatalf("%s: %v sent %v", code, err, g.sent)
		}
		g.noGroupChangeAfter("order cmd enter")
	}
}

// Rule 3: every wait of the order has a ceiling, whatever the module keeps answering.
func TestRemoteEnterCmdRetriesAreBounded(t *testing.T) {
	for code, max := range map[string]int{"busy": cmdEnterBusyTries + 1, "cancelled": cmdEnterCancelTries + 1, "too-far": cmdEnterFarTries + 1} {
		g := newEnterOrders(t, enterErr(code, "x"))
		start := g.now
		_, err := g.run(RemoteOptions{})
		if err == nil || len(g.sent) != max {
			t.Fatalf("%s: %d orders (want %d) err %v", code, len(g.sent), max, err)
		}
		if g.now.Sub(start) > 10*time.Minute {
			t.Fatalf("%s: took %v", code, g.now.Sub(start))
		}
	}
	// in-combat is waited out for cmdEnterCombatWait of the clock and then the refusal stands.
	g := newEnterOrders(t, enterErr("in-combat", "Healy: in combat"))
	_, err := g.run(RemoteOptions{})
	if err == nil || !strings.Contains(err.Error(), "in-combat") || len(g.sent) < 3 || len(g.sent) > 40 {
		t.Fatalf("%d orders: %v", len(g.sent), err)
	}
	if span := g.at[len(g.at)-1].Sub(g.at[0]); span > 2*time.Minute {
		t.Fatalf("in-combat held the phase for %v", span)
	}
}

func TestRemoteEnterCmdAnOlderModuleFallsBackToTheWalkError(t *testing.T) {
	// No ACTION_PENDING and no answer: the module does not know the verb.
	silent := tbm.OrderResult{Intent: "cmd enter", Kind: tbm.KindTimeout, Lines: []string{}}
	g := newEnterOrders(t, silent)
	rep, err := g.run(RemoteOptions{})
	if err == nil || !strings.Contains(err.Error(), "stood at the portal and did not enter") || !strings.Contains(err.Error(), "#939") || len(g.sent) != 1 {
		t.Fatalf("%v sent %v", err, g.sent)
	}
	if ce := rep.Remote.Enter.CmdEnter; ce == nil || ce.Fallback == "" || !strings.Contains(notesOf(rep), "does not know cmd enter") {
		t.Fatalf("%+v\n%s", ce, notesOf(rep))
	}
	// An unknown-verb refusal is the same.
	g = newEnterOrders(t, enterErr("unknown", "Unknown cmd verb"))
	g.script[0].Pending = nil
	if _, err := g.run(RemoteOptions{}); err == nil || !strings.Contains(err.Error(), "#939") || len(g.sent) != 1 {
		t.Fatalf("%v sent %v", err, g.sent)
	}
	// The fallback still lets a lead bot that was taken in by itself (an old module that did handle the trigger) pass.
	g = newEnterOrders(t, silent)
	g.onEnterErr = func() { g.pending[g.lead().GUID] = g.now.Add(time.Second) }
	if rep, err := g.run(RemoteOptions{}); err != nil || rep.State != "done" {
		t.Fatalf("%v", err)
	}
}

func TestRemoteEnterCmdPendingWithoutAFinalLineEnds(t *testing.T) {
	running := tbm.OrderResult{Intent: "cmd enter", Kind: tbm.KindTimeout, Pending: &tbm.Pending{Intent: "cmd enter", Scope: "party", BudgetS: 40, GUID: "924"}, Lines: []string{}}
	g := newEnterOrders(t, running)
	_, err := g.run(RemoteOptions{})
	if err == nil || !strings.Contains(err.Error(), "gave no final answer") || len(g.sent) != 1 {
		t.Fatalf("%v sent %v", err, g.sent)
	}
}

func TestRemoteEnterAutoKeepsTheGMTeleAndWalksWithoutATrigger(t *testing.T) {
	// A developer account with a tele: .tele name, as before, even with a trigger in the plan.
	g := newEnterOrders(t, enterAck(3))
	g.level, g.known, g.plan.EntranceTele = 3, true, "ulda"
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.Remote.Enter.Method != EnterGM || len(g.sent) != 0 {
		t.Fatalf("%v %+v sent %v", err, rep.Remote.Enter, g.sent)
	}
	// A plan without a trigger walks (and a game without orders walks too).
	g = newEnterOrders(t, enterAck(3))
	g.plan.EntranceTrigger = 0
	g.portal = &Point{300, 0, 0}
	rep, err = g.run(RemoteOptions{})
	if err != nil || rep.Remote.Enter.Method != EnterWalk || len(g.sent) != 0 {
		t.Fatalf("%v %+v sent %v", err, rep.Remote.Enter, g.sent)
	}
	// The explicit cmd needs a trigger.
	g = newEnterOrders(t, enterAck(3))
	g.plan.EntranceTrigger = 0
	if _, err := g.run(RemoteOptions{Enter: EnterCmd}); err == nil || !strings.Contains(err.Error(), "no entrance_trigger") {
		t.Fatalf("%v", err)
	}
}

// "cancelled (1 of 3 got in)" with the lead bot already on the plan's map: no second order (it would answer not-found or too-far to a
// party that is half inside and fail the run); the rest are summoned.
func TestRemoteEnterCmdCancelledWithTheLeadInsideIsNotSentAgain(t *testing.T) {
	g := newEnterOrders(t, enterErr("cancelled", "Magey: cancelled (1 of 3 got in)"), enterErr("not-found", "no trigger"))
	g.onEnterErr = func() { g.pending[g.lead().GUID] = g.now.Add(time.Second) }
	rep, err := g.run(RemoteOptions{})
	if err != nil || rep.State != "done" || len(g.sent) != 1 {
		t.Fatalf("%v sent %v\n%s", err, g.sent, notesOf(rep))
	}
	if strings.Join(rep.Remote.Enter.Summoned, ",") != "Healy,Magey" || !strings.Contains(notesOf(rep), "not sent again") {
		t.Fatalf("summoned %v\n%s", rep.Remote.Enter.Summoned, notesOf(rep))
	}
	// The same for busy, and for an answer with no count at all once the lead bot shows inside.
	for _, code := range []string{"busy", "too-far"} {
		g = newEnterOrders(t, enterErr(code, "Healy: x"), enterErr("not-found", "no trigger"))
		g.onEnterErr = func() { g.pending[g.lead().GUID] = g.now.Add(time.Millisecond) }
		g.script[0].Err.Message = "Healy: x"
		if rep, err := g.run(RemoteOptions{}); err != nil || rep.State != "done" || len(g.sent) != 1 {
			t.Fatalf("%s: %v sent %v", code, err, g.sent)
		}
	}
}

// The 4-minute ceiling counts from the start of the walk to the door.
func TestRemoteEnterCmdCeilingIncludesTheWalk(t *testing.T) {
	g := newEnterOrders(t, enterAck(3))
	g.plan.Portal = &Portal{Map: 0, Point: Point{3000, 0, 0}} // 300 s of walking at the fake's 10 yd/s
	start := g.now
	_, err := g.run(RemoteOptions{})
	if err == nil || !strings.Contains(err.Error(), "did not get the party in within 240 s") || len(g.sent) != 0 {
		t.Fatalf("%v sent %v", err, g.sent)
	}
	if span := g.now.Sub(start); span > 6*time.Minute {
		t.Fatalf("the phase took %v", span)
	}
}
