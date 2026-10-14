package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// The prepare step (prepare.go) against a fake game with a roster: `.bot add` logs a bot in after a delay, a tele changes the map of the
// bot (and of the agent), and every GM line and bot line is logged in order.

const (
	outsideMap  = 0
	prepAgent   = "Glacienne"
	prepAgentID = packet.GUID(0x20D)
)

type prepBot struct {
	guid    packet.GUID
	state   string // online, starting, offline
	readyAt time.Time
}

type prepFake struct {
	*enterFake
	bots       map[string]*prepBot
	loginDelay time.Duration
	addReply   string // overrides the answer to `.bot add`
	events     []string
	freezeTele bool // teleports change no map
	// The instance state the core keeps: who has a stale bind, and who is in which group (0: none).
	binds         map[string]bool
	group         map[string]int
	noGroupAnswer bool // the sender gets no reply to the group unbind
}

func newPrepFake(t *testing.T, states map[string]string) *prepFake {
	e := newEnterFake(t)
	e.plan.OutsideTele = "uld"
	g := &prepFake{enterFake: e, bots: map[string]*prepBot{}, loginDelay: 6 * time.Second}
	for _, p := range e.party {
		g.bots[p.Name] = &prepBot{guid: p.GUID, state: "offline"}
	}
	for n, s := range states {
		g.bots[n].state = s
	}
	e.agentMap = outsideMap
	// Every character starts with a stale bind to the old instance; the bots are one group.
	g.binds, g.group = map[string]bool{prepAgent: true}, map[string]int{}
	for n := range g.bots {
		g.binds[n], g.group[n] = true, 1
	}
	return g
}

func (g *prepFake) log(format string, a ...any) {
	g.events = append(g.events, fmt.Sprintf(format, a...))
}

func (g *prepFake) idxOf(prefix string) int {
	for i, l := range g.events {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

func (g *prepFake) count(prefix string) int {
	n := 0
	for _, l := range g.events {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// stateOf is the roster state of a bot as of now: a login in progress turns online at its ready time.
func (g *prepFake) stateOf(b *prepBot) string {
	if b.state == "starting" && !g.now.Before(b.readyAt) {
		b.state = "online"
	}
	return b.state
}

func (g *prepFake) BotLines(_ context.Context, command string, _ time.Duration, done func(string) bool) ([]string, bool, error) {
	f := strings.Fields(command)
	var reply []string
	switch {
	case command == "roster":
		g.log("roster")
		reply = append(reply, fmt.Sprintf("TBM:ROSTER_BEGIN|%d", len(g.bots)))
		for _, p := range g.party {
			b := g.bots[p.Name]
			reply = append(reply, fmt.Sprintf("TBM:ROSTER|%d|%s|1|%s|0|Uldaman", uint32(b.guid), p.Name, g.stateOf(b)))
		}
		reply = append(reply, "TBM:ROSTER_END")
	case f[0] == "add" && len(f) == 2:
		g.log("add %s", f[1])
		b := g.bots[f[1]]
		switch {
		case g.addReply != "":
			reply = []string{g.addReply}
		case b == nil:
			reply = []string{fmt.Sprintf("Character '%s' not found.", f[1])}
		case g.stateOf(b) == "online":
			reply = []string{fmt.Sprintf("Character '%s' is already online and cannot be claimed as a Headless bot.", f[1])}
		default:
			b.state, b.readyAt = "starting", g.now.Add(g.loginDelay)
			reply = []string{fmt.Sprintf("Bot %s queued for login; it will follow %s after entering the world.", f[1], prepAgent)}
		}
	case f[0] == "command" && len(f) == 3 && f[2] == "leave":
		g.log("leave %s", f[1])
		g.group[f[1]] = 0
		reply = []string{"ok"}
	default:
		g.log("line %s", command)
		return nil, false, nil // maintenance, inv, talents: no answer
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

func (g *prepFake) TeleName(ctx context.Context, player, loc string) error {
	g.log("tele %s %s", player, loc)
	if loc != g.plan.OutsideTele {
		return g.enterFake.TeleName(ctx, player, loc)
	}
	if g.freezeTele {
		return nil
	}
	if player == prepAgent {
		g.agentMap = outsideMap
		return nil
	}
	for _, p := range g.party {
		if p.Name == player {
			g.memberMap[p.GUID] = outsideMap
		}
	}
	return nil
}

func (g *prepFake) AgentName() string      { return prepAgent }
func (g *prepFake) AgentGUID() packet.GUID { return prepAgentID }
func (g *prepFake) Sees(name string) bool {
	if g.agentMap == insideMap {
		return false
	}
	for _, p := range g.party {
		if p.Name == name {
			return g.memberMap[p.GUID] != insideMap
		}
	}
	return false
}
func (g *prepFake) GMVisible(_ context.Context, on bool) error {
	if on {
		g.log("visible on")
	} else {
		g.log("visible off")
	}
	return nil
}

// UnbindInstances models HandleInstanceUnbindCommand: the selected player is unbound only from account level 4; below it the SENDER is.
func (g *prepFake) UnbindInstances(_ context.Context, who string, guid packet.GUID) error {
	g.log("unbind %s %d", who, uint32(guid))
	if g.level >= AdministratorLevel {
		g.binds[who] = false
	} else {
		g.binds[prepAgent] = false
	}
	return nil
}

// GroupUnbind models HandleInstanceGroupUnbindCommand: every member of the selected player's group is unbound and the group disbanded,
// whatever the sender's level (it needs a developer); a player in no group gets "is not in a group".
func (g *prepFake) GroupUnbind(_ context.Context, who string, guid packet.GUID) (GroupUnbindResult, error) {
	g.log("groupunbind %s %d", who, uint32(guid))
	if g.noGroupAnswer {
		return GroupNoAnswer, nil
	}
	id := g.group[who]
	if id == 0 {
		return GroupNotGroups, nil
	}
	for n, gid := range g.group {
		if gid == id {
			g.binds[n], g.group[n] = false, 0
		}
	}
	return GroupUnbound, nil
}

// inside puts every bot and the agent in the old instance, as a finished run leaves them.
func (g *prepFake) inside() {
	for _, p := range g.party {
		g.memberMap[p.GUID] = insideMap
	}
	g.agentMap = insideMap
}

func (g *prepFake) run(spec PrepareSpec) (Report, error) {
	o := RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}, Tank: "Tanky", Prepare: spec, Preflight: PreflightOff}
	return runRemote(g.t, g, g.plan, o, nil)
}

func TestPrepareOnlineAddsOnlyTheMissingBots(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Magey": "starting"})
	g.bots["Magey"].readyAt = g.now.Add(3 * time.Second)
	rep, err := g.run(PrepareSpec{Online: true})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if n := g.count("add "); n != 1 || g.idxOf("add Healy") < 0 {
		t.Fatalf("add lines %v", g.events)
	}
	p := rep.Prepare
	if p == nil || len(p.Steps) != 1 || p.Steps[0].Name != "online" || strings.Join(p.Added, ",") != "Healy" || strings.Join(p.AlreadyOnline, ",") != "Tanky" {
		t.Fatalf("%+v", p)
	}
	// Healy needs its 6 s of login: the step waited for it on the roster, and the bind came after.
	if p.Steps[0].Seconds < 6 || p.Seconds < p.Steps[0].Seconds {
		t.Fatalf("seconds %+v", p)
	}
	if g.indexOf("commander bind") < 0 {
		t.Fatalf("no bind: %v", g.orders)
	}
	if rep.SetupS < p.Seconds {
		t.Fatalf("setup_s %.1f < prepare %.1f", rep.SetupS, p.Seconds)
	}
	if rep.Remote.GMCommands != 1 || len(rep.SetupShortcuts) != 0 {
		t.Fatalf("online sends no GM line: gm %d setup %v", rep.Remote.GMCommands, rep.SetupShortcuts)
	}
}

func TestPrepareOnlineWithEveryoneOnlineSendsNoAdd(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	rep, err := g.run(PrepareSpec{Online: true})
	if err != nil || rep.State != "done" || g.count("add ") != 0 || len(rep.Prepare.Added) != 0 || len(rep.Prepare.AlreadyOnline) != 3 {
		t.Fatalf("%v %s %v %+v", err, rep.State, g.events, rep.Prepare)
	}
}

func TestPrepareOnlineWaitHasACeiling(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online"})
	g.loginDelay = time.Hour // Magey never comes
	began := g.now
	rep, err := g.run(PrepareSpec{Online: true, OnlineWaitS: 20})
	if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), "Magey") || !strings.Contains(err.Error(), "not online") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if took := g.now.Sub(began); took > 30*time.Second {
		t.Fatalf("the wait took %v with a 20 s ceiling", took)
	}
	if g.indexOf("commander bind") >= 0 {
		t.Fatalf("the run bound after a failed prepare: %v", g.orders)
	}
	if rep.Prepare == nil || len(rep.Prepare.Steps) != 1 {
		t.Fatalf("the failed step is not in the report: %+v", rep.Prepare)
	}
}

func TestPrepareOnlineReadsTheRefusal(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online"})
	g.addReply = "You may only control characters on your account."
	_, err := g.run(PrepareSpec{Online: true})
	if err == nil || !strings.Contains(err.Error(), "may only control") || !strings.Contains(err.Error(), "Magey") {
		t.Fatalf("%v", err)
	}
	if g.indexOf("commander bind") >= 0 {
		t.Fatal("bound after a refused add")
	}
}

func TestPrepareFreshResetsEachBotByName(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	g.inside()
	g.level = 4 // from this level the core unbinds the selected player
	rep, err := g.run(PrepareSpec{Fresh: true, GMSetup: []string{".GM  visible OFF"}})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	// The bots, then the agent, are teleported out; only then do they leave their group; then each bot is unbound by name with its own
	// GUID, and the agent last; the visible line closes the step.
	want := []string{"roster",
		"tele Tanky uld", "tele Healy uld", "tele Magey uld", "tele Glacienne uld",
		"leave Tanky", "leave Healy", "leave Magey",
		fmt.Sprintf("unbind Tanky %d", uint32(gTank)), fmt.Sprintf("unbind Healy %d", uint32(gHealer)), fmt.Sprintf("unbind Magey %d", uint32(gMage)),
		fmt.Sprintf("unbind Glacienne %d", uint32(prepAgentID)), "visible off"}
	if got := g.events[:len(want)]; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events\n%v\nwant\n%v", got, want)
	}
	p := rep.Prepare
	if p == nil || strings.Join(p.Unbound, ",") != "Tanky,Healy,Magey,Glacienne" || strings.Join(p.TeleportedOut, ",") != "Tanky,Healy,Magey,Glacienne" ||
		strings.Join(p.LeftGroup, ",") != "Tanky,Healy,Magey" || p.GMCommands != 9 || len(p.Steps) != 2 {
		t.Fatalf("%+v", p)
	}
	if p.Steps[0].Name != "fresh" || p.Steps[1].Name != "gm_setup" || strings.Join(p.GMSetup, ",") != "gm visible off" {
		t.Fatalf("%+v", p.Steps)
	}
	// Every GM line is a setup shortcut and a trace-countable one; the run's own gm_commands is the enter phase's tele and no more.
	if len(rep.SetupShortcuts) != 9 || rep.Remote.GMCommands != 1 {
		t.Fatalf("setup %v gm %d", rep.SetupShortcuts, rep.Remote.GMCommands)
	}
	for _, want := range []string{".tele name Tanky uld (prepare fresh)", ".instance unbind all on Healy (prepare fresh)", ".instance unbind all on Glacienne (prepare fresh)", "gm visible off"} {
		found := false
		for _, s := range rep.SetupShortcuts {
			found = found || s == want
		}
		if !found {
			t.Errorf("setup_shortcuts lacks %q: %v", want, rep.SetupShortcuts)
		}
	}
	if len(p.Warnings) != 0 || len(p.GroupUnbound) != 0 || g.count("groupunbind") != 0 {
		t.Fatalf("level 4: %+v %v", p, g.events)
	}
	for n, has := range g.binds {
		if has {
			t.Errorf("%s still has its bind", n)
		}
	}
	// The bind and the enter phase come after the whole step.
	if g.indexOf("commander bind") < 0 || rep.Remote.Enter == nil || rep.Remote.Enter.Tele != "ulda" {
		t.Fatalf("no bind/enter after prepare: %v %+v", g.orders, rep.Remote.Enter)
	}
	if rep.SetupS < p.Seconds {
		t.Fatalf("setup_s %.1f < prepare %.1f", rep.SetupS, p.Seconds)
	}
}

// Below account level 4 the core unbinds the sender whatever is selected: the step sends `.instance groupunbind all` with the lead bot
// selected instead, reads the confirmation, and reports only that. No per-bot unbind goes out (it would clear the agent five times).
func TestPrepareFreshAtLevel3UsesTheGroupUnbind(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	g.inside()
	g.level = 3
	rep, err := g.run(PrepareSpec{Fresh: true})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	want := []string{"roster", "tele Tanky uld", "tele Healy uld", "tele Magey uld", "tele Glacienne uld",
		fmt.Sprintf("groupunbind Tanky %d", uint32(gTank)), fmt.Sprintf("unbind Glacienne %d", uint32(prepAgentID))}
	if got := g.events[:len(want)]; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events\n%v\nwant\n%v", got, want)
	}
	if g.count("unbind Tanky") != 0 || g.count("unbind Healy") != 0 || g.count("unbind Magey") != 0 || g.count("leave ") != 0 {
		t.Fatalf("a per-bot unbind or a leave went out: %v", g.events)
	}
	p := rep.Prepare
	if strings.Join(p.GroupUnbound, ",") != "Tanky" || strings.Join(p.Unbound, ",") != "Glacienne" || len(p.NotGrouped) != 0 || len(p.Warnings) != 0 {
		t.Fatalf("%+v", p)
	}
	// What the fake core did: every bot and the agent are unbound, nobody is grouped any more.
	for n, has := range g.binds {
		if has {
			t.Errorf("%s still has its bind", n)
		}
	}
	for n, id := range g.group {
		if id != 0 {
			t.Errorf("%s is still grouped", n)
		}
	}
	if p.GMCommands != 6 || len(rep.SetupShortcuts) != 6 {
		t.Fatalf("gm %d setup %v", p.GMCommands, rep.SetupShortcuts)
	}
	if g.indexOf("commander bind") < 0 {
		t.Fatalf("no bind: %v", g.orders)
	}
}

// A lead bot in no group: the group unbind did nothing for it, so the report says so (a warning, nothing under group_unbound) and the
// bots still leave whatever group they are in.
func TestPrepareFreshAtLevel3WithTheLeadUngrouped(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	g.level = 3
	g.group["Tanky"] = 0
	rep, err := g.run(PrepareSpec{Fresh: true})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s", err, rep.State)
	}
	p := rep.Prepare
	if len(p.GroupUnbound) != 0 || strings.Join(p.NotGrouped, ",") != "Tanky" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "no group") {
		t.Fatalf("%+v", p)
	}
	if g.count("leave ") != 3 || g.group["Healy"] != 0 || !g.binds["Tanky"] {
		t.Fatalf("%v %v", g.events, g.binds)
	}
}

// No reply to the group unbound: the step does not claim it, and fails before the agent's own unbind and before the bind.
func TestPrepareFreshAtLevel3NeedsTheConfirmation(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	g.level, g.noGroupAnswer = 3, true
	rep, err := g.run(PrepareSpec{Fresh: true})
	if err == nil || !strings.Contains(err.Error(), "not confirmed") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if len(rep.Prepare.GroupUnbound) != 0 || len(rep.Prepare.Unbound) != 0 || g.count("unbind ") != 0 || g.indexOf("commander bind") >= 0 {
		t.Fatalf("%+v %v", rep.Prepare, g.events)
	}
}

func TestPrepareFreshRefusesAPlayerAccount(t *testing.T) {
	for name, set := range map[string]func(g *prepFake){
		"player":   func(g *prepFake) { g.level = 0 },
		"observer": func(g *prepFake) { g.level = 1 },
		"unknown":  func(g *prepFake) { g.known = false },
	} {
		t.Run(name, func(t *testing.T) {
			g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
			g.inside()
			set(g)
			rep, err := g.run(PrepareSpec{Fresh: true})
			if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), "fresh") {
				t.Fatalf("%v %s", err, rep.State)
			}
			if g.count("tele ") != 0 || g.count("unbind ") != 0 || g.count("groupunbind ") != 0 || g.count("leave ") != 0 || g.indexOf("commander bind") >= 0 || len(rep.SetupShortcuts) != 0 {
				t.Fatalf("something was sent: %v %v", g.events, g.orders)
			}
		})
	}
}

func TestPrepareFreshChecksTheEntryLedgerFirst(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	g.inside()
	for i := 0; i < MaxInstancesPerHour; i++ {
		g.ledger = append(g.ledger, g.now.Add(-time.Duration(10+i)*time.Minute))
	}
	rep, err := g.run(PrepareSpec{Fresh: true})
	if err == nil || !strings.Contains(err.Error(), "last hour") {
		t.Fatalf("%v", err)
	}
	if g.count("tele ") != 0 || g.count("unbind ") != 0 || g.indexOf("commander bind") >= 0 || rep.Prepare == nil || len(rep.Prepare.Unbound) != 0 {
		t.Fatalf("something was sent before the ledger check: %v %v", g.events, g.orders)
	}
	// ignore_entry_limit goes on.
	g = newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	for i := 0; i < MaxInstancesPerHour; i++ {
		g.ledger = append(g.ledger, g.now.Add(-time.Duration(10+i)*time.Minute))
	}
	o := RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}, Tank: "Tanky", Preflight: PreflightOff, IgnoreEntryLimit: true, Prepare: PrepareSpec{Fresh: true}}
	if rep, err := runRemote(t, g, g.plan, o, nil); err != nil || rep.State != "done" || g.count("unbind ") != 1 || g.count("groupunbind ") != 1 {
		t.Fatalf("%v %s %v", err, rep.State, g.events)
	}
}

func TestPrepareFreshNeedsThePlansOutsideTeleAndOnlineBots(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	g.plan.OutsideTele = ""
	if _, err := g.run(PrepareSpec{Fresh: true}); err == nil || !strings.Contains(err.Error(), "outside_tele") || g.count("tele ") != 0 {
		t.Fatalf("%v %v", err, g.events)
	}
	// A bot that is not online cannot be teleported: the step names it and sends nothing (fresh alone does not log anybody in).
	g = newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online"})
	_, err := g.run(PrepareSpec{Fresh: true})
	if err == nil || !strings.Contains(err.Error(), "Magey (offline)") || g.count("tele ") != 0 || g.count("unbind ") != 0 {
		t.Fatalf("%v %v", err, g.events)
	}
	// online + fresh together: the bot is logged in first, then reset.
	g = newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online"})
	rep, err := g.run(PrepareSpec{Online: true, Fresh: true})
	if err != nil || rep.State != "done" || g.idxOf("add Magey") > g.idxOf("tele Tanky") || g.count("groupunbind ") != 1 {
		t.Fatalf("%v %s %v", err, rep.State, g.events)
	}
	if len(rep.Prepare.Steps) != 2 || rep.Prepare.Steps[0].Name != "online" || rep.Prepare.Steps[1].Name != "fresh" {
		t.Fatalf("%+v", rep.Prepare.Steps)
	}
}

func TestPrepareFreshUnbindsOnlyAfterEveryoneIsOutside(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	g.inside()
	g.freezeTele = true // the teleports do nothing: nobody leaves the instance
	began := g.now
	_, err := g.run(PrepareSpec{Fresh: true})
	if err == nil || !strings.Contains(err.Error(), "not outside") {
		t.Fatalf("%v", err)
	}
	if g.count("unbind ") != 0 || g.count("groupunbind ") != 0 || g.count("leave ") != 0 {
		t.Fatalf("unbound while the bots were still inside (the core skips the map a player stands on): %v", g.events)
	}
	if took := g.now.Sub(began); took > 40*time.Second {
		t.Fatalf("the wait took %v", took)
	}
}

func TestPrepareGMSetupAllowsOnlyVisibleLines(t *testing.T) {
	for _, ok := range []string{"gm visible off", ".gm visible on", "  .GM Visible   Off ", "gm visible ON"} {
		if _, _, err := ParseGMSetup(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "gm on", "gm off", "gm visible", "gm visible maybe", "gm visible off now", "instance unbind all", ".tele name Tanky ulda",
		"gm visible off; gm on", "gm visible off\n.gm on", "gmvisible off", "gm fly on", "revive", ".instance"} {
		if _, _, err := ParseGMSetup(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if err := (PrepareSpec{GMSetup: []string{bad}}).Validate(true); err == nil {
			t.Errorf("%q passed Validate", bad)
		}
	}
	// A run with such a line is refused before it sends anything.
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	rep, err := g.run(PrepareSpec{GMSetup: []string{"gm visible off", "instance unbind all"}})
	if err == nil || rep.State != "failed" || len(g.events) != 0 || g.indexOf("commander bind") >= 0 {
		t.Fatalf("%v %s %v", err, rep.State, g.events)
	}
}

func TestPrepareGMSetupIsBookedAndNeedsAModerator(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	rep, err := g.run(PrepareSpec{GMSetup: []string{"gm visible off"}})
	if err != nil || rep.State != "done" || g.count("visible off") != 1 {
		t.Fatalf("%v %s %v", err, rep.State, g.events)
	}
	if len(rep.SetupShortcuts) != 1 || rep.SetupShortcuts[0] != "gm visible off" || rep.Prepare.GMCommands != 1 || rep.Remote.GMCommands != 1 {
		t.Fatalf("%v %+v", rep.SetupShortcuts, rep.Prepare)
	}
	g = newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	g.level = 1
	_, err = g.run(PrepareSpec{GMSetup: []string{"gm visible off"}})
	if err == nil || g.count("visible") != 0 || !strings.Contains(err.Error(), "needs 2") {
		t.Fatalf("%v %v", err, g.events)
	}
}

func TestPrepareValidateGroupModeAndLimits(t *testing.T) {
	if err := (PrepareSpec{}).Validate(false); err != nil {
		t.Fatalf("an empty block is no block: %v", err)
	}
	for _, s := range []PrepareSpec{{Online: true}, {Fresh: true}, {GMSetup: []string{"gm visible off"}}} {
		if err := s.Validate(false); err == nil || !strings.Contains(err.Error(), "remote") {
			t.Errorf("%+v: %v", s, err)
		}
	}
	if err := (PrepareSpec{Online: true, OnlineWaitS: 181}).Validate(true); err == nil {
		t.Error("online_wait_s 181 accepted")
	}
	if err := (PrepareSpec{Online: true, OnlineWaitS: -1}).Validate(true); err == nil {
		t.Error("online_wait_s -1 accepted")
	}
	if err := (PrepareSpec{Online: true, Fresh: true, GMSetup: []string{"gm visible off"}, OnlineWaitS: 60}).Validate(true); err != nil {
		t.Errorf("a full block: %v", err)
	}
}

// A game that cannot prepare refuses a block that asks for something, and ignores an empty one.
func TestPrepareNeedsAPreparerGame(t *testing.T) {
	g := newFakeRemote(t)
	o := opts()
	o.Prepare = PrepareSpec{Online: true}
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{5, 0, 0}}), o, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot log bots in") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	g = newFakeRemote(t)
	o.Prepare = PrepareSpec{}
	if rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{5, 0, 0}}), o, nil); err != nil || rep.Prepare != nil {
		t.Fatalf("%v %+v", err, rep.Prepare)
	}
}

// The setup is back to back: after the step the bind, the enter phase and the preflight follow with no wait of their own beyond the
// phases' own work, and setup_s is the whole of it.
func TestPrepareSetupSecondsCoverPrepareEnterAndPreflight(t *testing.T) {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	g.inside()
	o := RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}, Tank: "Tanky", Prepare: PrepareSpec{Fresh: true}}
	rep, err := runRemote(t, g, g.plan, o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	pf, en := rep.Remote.Preflight, rep.Remote.Enter
	if pf == nil || en == nil || en.Seconds <= 0 {
		t.Fatalf("%+v %+v", pf, en)
	}
	if sum := rep.Prepare.Seconds + en.Seconds + pf.Seconds; rep.SetupS < sum {
		t.Fatalf("setup_s %.1f is less than prepare %.1f + enter %.1f + preflight %.1f", rep.SetupS, rep.Prepare.Seconds, en.Seconds, pf.Seconds)
	}
}
