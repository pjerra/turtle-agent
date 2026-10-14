package dungeon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The enter phase (remote_enter.go) against a fake commander whose bots start outside the plan's map (plan map 70, the bots on map 0).

const insideMap = 70

// enterFake is a fakeRemote that also is an Enterer and an EntryLedger. `.tele name` and `cmd summon` bring a bot to the plan's map after
// a delay; a lead bot that walks within 10 yd of portal goes in by itself (the area trigger). log is the order of what touched the groups
// and the entry.
type enterFake struct {
	*fakeRemote
	plan       *Plan
	inGroup    bool
	bindLeaves bool // the bind takes the agent out of its party (a kept bots+commander party)
	agentMap   uint32
	level      int
	known      bool
	log        []string
	pending    map[packet.GUID]time.Time
	stuck      map[string]bool // bots a summon does not bring
	teleFails  bool            // `.tele name` changes nothing
	portal     *Point
	ledger     []time.Time
	// swallow[name] summons of that bot do nothing before one works; dropOnTele leaves the group when the lead bot is teleported (after
	// the party was checked); rejoinOnSummon: a summon of a bot that is out of the group puts it back (else the module says no-bot).
	swallow        map[string]int
	dropOnTele     string
	rejoinOnSummon bool
}

func newEnterFake(t *testing.T) *enterFake {
	g := &enterFake{fakeRemote: newFakeRemote(t), pending: map[packet.GUID]time.Time{}, stuck: map[string]bool{}, swallow: map[string]int{}, known: true, level: 3}
	g.memberMap = map[packet.GUID]uint32{}
	g.plan = remotePlan(Leg{Name: "a", To: Point{5, 0, 0}})
	g.plan.Map, g.plan.EntranceTele = insideMap, "ulda"
	g.onTick = g.tick
	return g
}

func (g *enterFake) guidOf(name string) packet.GUID {
	for _, p := range g.party {
		if strings.EqualFold(p.Name, name) {
			return p.GUID
		}
	}
	return 0
}

func (g *enterFake) tick(*fakeRemote) {
	for guid, at := range g.pending {
		if !g.now.Before(at) {
			g.memberMap[guid] = insideMap
			delete(g.pending, guid)
		}
	}
	if g.portal != nil && g.memberMap[g.lead().GUID] != insideMap {
		l := g.lead()
		if dist3(Point{l.X, l.Y, l.Z}, *g.portal) <= 10 {
			g.memberMap[l.GUID] = insideMap
			g.log = append(g.log, "portal")
		}
	}
}

func (g *enterFake) Command(ctx context.Context, command string) (tbm.Reply, error) {
	f := strings.Fields(command)
	switch {
	case strings.HasPrefix(command, "commander bind"):
		g.log = append(g.log, "bind")
		if g.bindLeaves {
			g.inGroup = false
		}
	case strings.HasPrefix(command, "cmd summon") && len(f) > 2:
		g.log = append(g.log, "summon "+f[2])
		if g.left[f[2]] {
			if !g.rejoinOnSummon {
				return errReply("cmd summon", "no-bot"), nil
			}
			delete(g.left, f[2])
		}
		if g.swallow[f[2]] > 0 {
			g.swallow[f[2]]--
		} else if !g.stuck[f[2]] {
			g.pending[g.guidOf(f[2])] = g.now.Add(3 * time.Second)
		}
	}
	return g.fakeRemote.Command(ctx, command)
}

func (g *enterFake) InGroup() bool    { return g.inGroup }
func (g *enterFake) AgentMap() uint32 { return g.agentMap }
func (g *enterFake) LeaveGroup(context.Context) error {
	g.log = append(g.log, "leave")
	g.inGroup = false
	return nil
}
func (g *enterFake) AccountLevel(context.Context) (int, bool, error) {
	g.log = append(g.log, "probe")
	return g.level, g.known, nil
}
func (g *enterFake) TeleName(_ context.Context, player, loc string) error {
	g.log = append(g.log, "tele "+player+" "+loc)
	if g.dropOnTele != "" && loc == g.plan.EntranceTele {
		if g.left == nil {
			g.left = map[string]bool{}
		}
		g.left[g.dropOnTele] = true
		g.dropOnTele = ""
	}
	if !g.teleFails {
		g.pending[g.guidOf(player)] = g.now.Add(2 * time.Second)
	}
	return nil
}
func (g *enterFake) Recent(now time.Time) []time.Time {
	var out []time.Time
	for _, t := range g.ledger {
		if now.Sub(t) < time.Hour {
			out = append(out, t)
		}
	}
	return out
}
func (g *enterFake) Record(now time.Time) error {
	g.log = append(g.log, "record")
	g.ledger = append(g.ledger, now)
	return nil
}

func (g *enterFake) run(o RemoteOptions) (Report, error) {
	o.Tank = "Tanky"
	return runRemote(g.t, g, g.plan, o, nil)
}

func (g *enterFake) idx(prefix string) int {
	for i, l := range g.log {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

// noGroupChangeAfter fails when the agent left a group, or a bind went out, after the first log entry that starts with marker.
func (g *enterFake) noGroupChangeAfter(marker string) {
	g.t.Helper()
	i := g.idx(marker)
	if i < 0 {
		g.t.Fatalf("no %q in %v", marker, g.log)
	}
	for _, l := range g.log[i:] {
		if l == "leave" || l == "bind" {
			g.t.Fatalf("the group changed after %q: %v", marker, g.log)
		}
	}
	if n := len(g.ordersOf("commander bind")); n != 1 {
		g.t.Fatalf("%d binds: %v", n, g.orders)
	}
}

func TestRemoteEnterByGMTele(t *testing.T) {
	g := newEnterFake(t)
	g.inGroup = true // the agent starts in a group: it leaves it once, before the entry
	rep, err := g.run(opts())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	e := rep.Remote.Enter
	if e == nil || e.Method != EnterGM || e.Tele != "ulda" || e.Entries != 1 || !e.AgentLeftGroup || e.AccountLevel == nil || *e.AccountLevel != 3 || e.EntryLimit != 5 {
		t.Fatalf("%+v", e)
	}
	if strings.Join(e.Summoned, ",") != "Healy,Magey" || len(e.Arrived) != 3 || !strings.Contains(e.Warning, "limit of 5") {
		t.Fatalf("%+v", e)
	}
	if rep.Remote.GMCommands != 1 || !strings.Contains(notesOf(rep), "every new instance counts toward the account's limit of 5") {
		t.Fatalf("gm %d\n%s", rep.Remote.GMCommands, notesOf(rep))
	}
	// Order: bind, the agent leaves, probe, the lead bot is teleported, the others are summoned, the entry is recorded.
	want := []string{"bind", "leave", "probe", "tele Tanky ulda", "record", "summon Healy", "summon Magey"}
	var got []string
	for _, l := range g.log {
		got = append(got, l)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("log %v", g.log)
	}
	g.noGroupChangeAfter("tele")
	if len(g.ledger) != 1 {
		t.Fatalf("ledger %v", g.ledger)
	}
	// The summons are SummonGap (6 s) apart.
	var at []time.Time
	for i, o := range g.orders {
		if strings.HasPrefix(o, "cmd summon") {
			at = append(at, g.orderAt[i])
		}
	}
	if len(at) != 2 || at[1].Sub(at[0]) < 6*time.Second {
		t.Fatalf("summons at %v", at)
	}
	// The enter phase comes before leg 0: no move before the lead bot was teleported.
	if mv := g.indexOf("cmd move"); mv >= 0 && mv < g.indexOf("cmd summon Magey") {
		t.Fatalf("a move before the entry was complete: %v", g.orders)
	}
}

func TestRemoteEnterBindTakesTheAgentOutOfItsParty(t *testing.T) {
	g := newEnterFake(t)
	g.inGroup, g.bindLeaves = true, true // a kept party: the bind removes the commander itself
	rep, err := g.run(opts())
	if err != nil || g.idx("leave") >= 0 || rep.Remote.Enter.AgentLeftGroup {
		t.Fatalf("%v %v %+v", err, g.log, rep.Remote.Enter)
	}
}

func TestRemoteEnterByWalkingIntoThePortal(t *testing.T) {
	g := newEnterFake(t)
	g.level, g.known = 0, true // a player's account: no .tele
	g.plan.Portal = &Portal{Map: 0, Point: Point{300, 0, 0}}
	g.portal = &Point{300, 0, 0}
	rep, err := g.run(opts())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	e := rep.Remote.Enter
	if e == nil || e.Method != EnterWalk || e.Tele != "" || rep.Remote.GMCommands != 0 || e.Entries != 1 {
		t.Fatalf("%+v gm %d", e, rep.Remote.GMCommands)
	}
	if g.idx("tele") >= 0 {
		t.Fatalf("a player's account sent a GM command: %v", g.log)
	}
	// The walk was in legs of at most 120 yd, before the summons; the lead bot got in at the portal; the followers were summoned.
	if n := len(g.ordersOf("cmd move")); n < 3 || g.idx("portal") < 0 || g.idx("portal") > g.idx("summon Healy") {
		t.Fatalf("log %v orders %v", g.log, g.orders)
	}
	if strings.Join(e.Summoned, ",") != "Healy,Magey" {
		t.Fatalf("%+v", e)
	}
	g.noGroupChangeAfter("portal")
}

func TestRemoteEnterWalkNeedsAPortal(t *testing.T) {
	g := newEnterFake(t)
	g.level = 0
	_, err := g.run(opts())
	if err == nil || !strings.Contains(err.Error(), "no way in") || !strings.Contains(err.Error(), "has no portal") || g.idx("tele") >= 0 {
		t.Fatalf("%v %v", err, g.log)
	}
	// An account whose level cannot be read is a player's.
	g = newEnterFake(t)
	g.known = false
	g.plan.Portal = &Portal{Map: 0, Point: Point{40, 0, 0}}
	g.portal = &Point{40, 0, 0}
	if rep, err := g.run(opts()); err != nil || rep.Remote.Enter.Method != EnterWalk || g.idx("tele") >= 0 {
		t.Fatalf("%v %v", err, g.log)
	}
}

func TestRemoteEnterAGMWithoutATeleWalks(t *testing.T) {
	g := newEnterFake(t)
	g.plan.EntranceTele = ""
	g.plan.Portal = &Portal{Map: 0, Point: Point{40, 0, 0}}
	g.portal = &Point{40, 0, 0}
	rep, err := g.run(opts())
	if err != nil || rep.Remote.Enter.Method != EnterWalk || !strings.Contains(notesOf(rep), "has no entrance_tele") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

func TestRemoteEnterFailsClearlyForABotOnTheWrongMap(t *testing.T) {
	g := newEnterFake(t)
	g.stuck["Magey"] = true // the summon does not bring it
	rep, err := g.run(opts())
	if err == nil || !strings.Contains(err.Error(), "Magey (map 0) not on map 70") || strings.Contains(err.Error(), "Healy") || rep.State != "failed" {
		t.Fatalf("%v", err)
	}
	// Failing does not touch the group: no leave, one bind, the run unbinds (the end of the run).
	g.noGroupChangeAfter("tele")
	if g.unbinds != 1 || len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("unbinds %d %v", g.unbinds, g.orders)
	}
	// A bot still outside after the wait is summoned once more, by name, and then the run fails: two summons, not an endless wait.
	n := 0
	for _, l := range g.log {
		if l == "summon Magey" {
			n++
		}
	}
	if n != 2 || strings.Join(rep.Remote.Enter.Resummoned, ",") != "Magey" || !strings.Contains(err.Error(), "second summon") {
		t.Fatalf("summons of Magey: %d, resummoned %v, %v", n, rep.Remote.Enter.Resummoned, err)
	}
}

// A summon that does nothing the first time (the bot was busy, or a teleport swallowed it) is repeated once after the wait, and the run goes on.
func TestRemoteEnterSummonsAgainABotThatStayedOutside(t *testing.T) {
	g := newEnterFake(t)
	g.swallow["Magey"] = 1
	rep, err := g.run(opts())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if strings.Join(rep.Remote.Enter.Resummoned, ",") != "Magey" || len(rep.Remote.Enter.Arrived) != 3 {
		t.Fatalf("%+v", rep.Remote.Enter)
	}
}

// Run 6b: the hunter left the group after the bind, so the feed no longer listed it, and the enter phase, which summoned only the bots
// of the feed, never summoned it. The party was checked and whole when the lead bot went in; the bot is named by the run, so it is
// summoned by name.
func TestRemoteEnterSummonsANamedBotTheFeedNoLongerLists(t *testing.T) {
	g := newEnterFake(t)
	g.dropOnTele, g.rejoinOnSummon = "Magey", true
	rep, err := g.run(opts())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if g.idx("summon Magey") < 0 || strings.Join(rep.Remote.Enter.Summoned, ",") != "Healy,Magey" {
		t.Fatalf("%v %+v", g.log, rep.Remote.Enter)
	}
	// Without a module that takes the bot back, the summon is refused and the run fails naming the bot, not "not in the feed" 30 s later.
	g = newEnterFake(t)
	g.dropOnTele = "Magey"
	_, err = g.run(opts())
	if err == nil || !strings.Contains(err.Error(), "cmd summon Magey was refused") || !strings.Contains(err.Error(), "no-bot") {
		t.Fatalf("%v", err)
	}
}

func (g *enterFake) count(prefix string) int {
	n := 0
	for _, l := range g.log {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// A bot the bind left out (it dropped out of the group right after the ACK) is invited again by one unbind and bind, and only then does
// the lead bot go in: the entry happens with the whole party in the group.
func TestRemoteEnterBindsAgainForABotThatLeftTheGroup(t *testing.T) {
	g := newEnterFake(t)
	g.leaveOnBind = []string{"Magey"}
	rep, err := g.run(opts())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	var head []string
	for _, l := range g.log {
		if l == "tele Tanky ulda" {
			break
		}
		head = append(head, l)
	}
	var cmds []string
	for _, o := range g.orders {
		if strings.HasPrefix(o, "commander bind") || o == "commander unbind" {
			cmds = append(cmds, strings.Fields(o)[1])
		}
	}
	if strings.Join(head, "|") != "bind|bind|probe" || strings.Join(cmds[:3], ",") != "bind,unbind,bind" {
		t.Fatalf("before the entry %v, orders %v: want bind, unbind, bind, then the probe", head, g.orders)
	}
	if !strings.Contains(notesOf(rep), "not in commander status's listing") || !strings.Contains(notesOf(rep), "bound party checked") {
		t.Fatalf("%s", notesOf(rep))
	}
	if len(rep.Remote.Enter.Arrived) != 3 {
		t.Fatalf("%+v", rep.Remote.Enter)
	}
	// After the entry the group is not touched: no bind, no leave.
	i := g.idx("tele")
	for _, l := range g.log[i:] {
		if l == "leave" || l == "bind" {
			t.Fatalf("the group changed after the entry: %v", g.log)
		}
	}
}

// A bot that is loading or teleporting joins the group a few seconds after the bind: the party is read back until it matches, so it is not
// a failure and no second bind goes out.
func TestRemoteEnterWaitsForABotThatJoinsLate(t *testing.T) {
	g := newEnterFake(t)
	g.leaveOnBind = []string{"Magey"}
	g.lateJoin = map[string]time.Time{"Magey": g.now.Add(7 * time.Second)}
	rep, err := g.run(opts())
	if err != nil || rep.State != "done" || g.count("bind") != 1 || g.unbinds != 1 {
		t.Fatalf("%v %s binds %d unbinds %d\n%s", err, rep.State, g.count("bind"), g.unbinds, notesOf(rep))
	}
}

// A bot that is still out after the second bind fails the run before anything is teleported or summoned, and the failure names it.
func TestRemoteEnterNeverEntersWithAPartialParty(t *testing.T) {
	g := newEnterFake(t)
	g.leaveOnBind, g.stubborn = []string{"Magey"}, true
	rep, err := g.run(opts())
	if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), "Magey") || !strings.Contains(err.Error(), "partial party") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.idx("tele") >= 0 || g.idx("summon") >= 0 || g.idx("record") >= 0 || g.count("bind") != 2 {
		t.Fatalf("something went in or a third bind went out: %v", g.log)
	}
	if g.unbinds != 2 { // the unbind before the second bind, and the run's own at its end
		t.Fatalf("unbinds %d", g.unbinds)
	}
}

// The second bind is refused (a hired companion cannot be bound, the bot is offline, ...): the failure carries the module's reason.
func TestRemoteEnterNamesTheReasonABotCannotBeInvited(t *testing.T) {
	g := newEnterFake(t)
	g.leaveOnBind = []string{"Magey"}
	g.joinErr = map[string]string{"Magey": "hired"}
	_, err := g.run(opts())
	if err == nil || !strings.Contains(err.Error(), "hired") || !strings.Contains(err.Error(), "Magey") || g.idx("tele") >= 0 {
		t.Fatalf("%v %v", err, g.log)
	}
}

// A bound group that holds a bot the run did not name is not changed to fit: the run fails before the entry.
func TestRemoteEnterRefusesAnUnnamedBotInTheGroup(t *testing.T) {
	g := newEnterFake(t)
	_, err := g.run(RemoteOptions{Lead: "Tanky", Bots: []string{"Healy"}})
	if err == nil || !strings.Contains(err.Error(), "Magey") || !strings.Contains(err.Error(), "did not name") || g.idx("tele") >= 0 {
		t.Fatalf("%v %v", err, g.log)
	}
}

func TestRemoteEnterFailsWhenTheLeadBotNeverArrives(t *testing.T) {
	g := newEnterFake(t)
	g.teleFails = true
	_, err := g.run(opts())
	if err == nil || !strings.Contains(err.Error(), "still on map 0") || !strings.Contains(err.Error(), "TRANSFER_ABORT_TOO_MANY_INSTANCES") || g.idx("summon") >= 0 {
		t.Fatalf("%v %v", err, g.log)
	}
	if g.idx("record") >= 0 {
		t.Fatalf("an entry that did not happen was recorded: %v", g.log)
	}
}

func TestRemoteEnterSummonsOnlyWhoIsOutside(t *testing.T) {
	g := newEnterFake(t)
	g.mapID = insideMap // the party is inside ...
	g.memberMap[gMage] = 0
	rep, err := g.run(opts()) // ... but the mage
	if err != nil || g.idx("tele") >= 0 || strings.Join(rep.Remote.Enter.Summoned, ",") != "Magey" {
		t.Fatalf("%v %v %+v", err, g.log, rep.Remote.Enter)
	}
	// A party that is all inside has no enter phase at all.
	g = newEnterFake(t)
	g.mapID = insideMap
	rep, err = g.run(opts())
	if err != nil || rep.Remote.Enter != nil || g.idx("tele") >= 0 || g.idx("probe") >= 0 || g.idx("summon") >= 0 || g.idx("record") >= 0 {
		t.Fatalf("%v %v %+v", err, g.log, rep.Remote.Enter)
	}
}

func TestRemoteEnterKeepsTheAgentOutside(t *testing.T) {
	g := newEnterFake(t)
	g.agentMap = insideMap
	_, err := g.run(opts())
	if err == nil || !strings.Contains(err.Error(), "must stay outside") || g.idx("tele") >= 0 {
		t.Fatalf("%v %v", err, g.log)
	}
}

func TestRemoteEnterOffAndNoEnterer(t *testing.T) {
	g := newEnterFake(t)
	_, err := g.run(RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}, Enter: EnterOff})
	if err == nil || !strings.Contains(err.Error(), "bring the group into the dungeon first") || g.idx("tele") >= 0 || g.idx("probe") >= 0 {
		t.Fatalf("%v %v", err, g.log)
	}
	// A game that cannot enter says so.
	f := newFakeRemote(t)
	plan := remotePlan(Leg{Name: "a", To: Point{5, 0, 0}})
	plan.Map = insideMap
	_, err = runRemote(t, f, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "cannot enter a dungeon") || f.unbinds != 1 {
		t.Fatalf("%v", err)
	}
}

func TestRemoteEnterRefusesASixthInstanceInTheHour(t *testing.T) {
	g := newEnterFake(t)
	for i := 1; i <= 5; i++ {
		g.ledger = append(g.ledger, g.now.Add(-time.Duration(i*10)*time.Minute))
	}
	_, err := g.run(opts())
	if err == nil || !strings.Contains(err.Error(), "5 new instances in the last hour") || !strings.Contains(err.Error(), "limit 5") || g.idx("tele") >= 0 || g.unbinds != 1 {
		t.Fatalf("%v %v", err, g.log)
	}
	// An entry older than the hour does not count; four recent ones leave room, and the entry is added.
	g = newEnterFake(t)
	g.ledger = []time.Time{g.now.Add(-90 * time.Minute), g.now.Add(-50 * time.Minute), g.now.Add(-40 * time.Minute), g.now.Add(-30 * time.Minute), g.now.Add(-20 * time.Minute)}
	rep, err := g.run(opts())
	if err != nil || len(g.ledger) != 6 || rep.Remote.Enter.EntriesLastHour != 5 {
		t.Fatalf("%v ledger %d %+v", err, len(g.ledger), rep.Remote.Enter)
	}
	// ignore_entry_limit tries anyway.
	g = newEnterFake(t)
	for i := 1; i <= 5; i++ {
		g.ledger = append(g.ledger, g.now.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := g.run(RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}, IgnoreEntryLimit: true}); err != nil || g.idx("tele") < 0 {
		t.Fatalf("%v %v", err, g.log)
	}
}

func TestRemoteEnterExplicitModes(t *testing.T) {
	// gm: no probe.
	g := newEnterFake(t)
	if rep, err := g.run(RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}, Enter: EnterGM}); err != nil || g.idx("probe") >= 0 || rep.Remote.Enter.AccountLevel != nil {
		t.Fatalf("%v %v", err, g.log)
	}
	// gm without a tele in the plan, walk without a portal: the plan says what is missing.
	g = newEnterFake(t)
	g.plan.EntranceTele = ""
	if _, err := g.run(RemoteOptions{Lead: "Tanky", Enter: EnterGM, Bots: []string{"Healy", "Magey"}}); err == nil || !strings.Contains(err.Error(), "no entrance_tele") {
		t.Fatalf("%v", err)
	}
	g = newEnterFake(t)
	if _, err := g.run(RemoteOptions{Lead: "Tanky", Enter: EnterWalk, Bots: []string{"Healy", "Magey"}}); err == nil || !strings.Contains(err.Error(), "no portal") {
		t.Fatalf("%v", err)
	}
}

// The lead bot stands at the portal and the trigger does not take it in: the run says what it saw.
func TestRemoteEnterWalkThatDoesNotEnterFails(t *testing.T) {
	g := newEnterFake(t)
	g.level = 0
	g.plan.Portal = &Portal{Map: 0, Point: Point{60, 0, 0}} // g.portal stays nil: nothing happens at the portal
	_, err := g.run(opts())
	if err == nil || !strings.Contains(err.Error(), "stood at the portal and did not enter") {
		t.Fatalf("%v", err)
	}
	g.noGroupChangeAfter("probe")
}

// ---- tank-busy ----

func tankBusyPlan() *Plan {
	return remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
}

func tankOpts() RemoteOptions {
	o := opts()
	o.Tank = "Tanky"
	return o
}

// tank-busy: the pull waits for the tank to leave its fight, then goes again; the party is never sent in with an attack order instead.
func TestRemotePullWaitsOutABusyTank(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.cmdErr["pullback"] = []string{"tank-busy"}
	g.busyFor = 5 * time.Second
	rep, err := runRemote(t, g, tankBusyPlan(), tankOpts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	pulls := g.ordersOf("cmd pullback")
	if len(pulls) != 2 || rep.Remote.TankBusyWaits != 1 || rep.Remote.PullFallbacks != 0 {
		t.Fatalf("pulls %v waits %d fallbacks %d", pulls, rep.Remote.TankBusyWaits, rep.Remote.PullFallbacks)
	}
	var at []time.Time
	for i, o := range g.orders {
		if strings.HasPrefix(o, "cmd pullback") {
			at = append(at, g.orderAt[i])
		}
	}
	if wait := at[1].Sub(at[0]); wait < 4*time.Second || wait > 11*time.Second {
		t.Fatalf("the second pull came %v after the refused one", wait)
	}
	second, seen := -1, 0
	for i, o := range g.orders {
		if strings.HasPrefix(o, "cmd pullback") {
			if seen++; seen == 2 {
				second = i
			}
		}
	}
	if first := g.indexOf("cmd attack"); first >= 0 && first < second {
		t.Fatalf("an attack order stood in for the pull: %v", g.orders)
	}
	if !strings.Contains(notesOf(rep), "tank-busy") || rep.Remote.Refused["pullback:tank-busy"] != 1 {
		t.Fatalf("%v\n%s", rep.Remote.Refused, notesOf(rep))
	}
}

// tank-busy: the mob comes to the party by itself during the wait; no second pull is sent.
func TestRemotePullBusyTankMobComesAnyway(t *testing.T) {
	g := newFakeRemote(t)
	m := g.addMob(299, Point{30, 0, 0})
	g.cmdErr["pullback"] = []string{"tank-busy"}
	g.busyFor = 20 * time.Second
	g.onTick = func(g *fakeRemote) {
		if len(g.cmdErr["pullback"]) == 0 && !m.pulled {
			m.pulled, m.pullAt = true, g.now
		}
	}
	rep, err := runRemote(t, g, tankBusyPlan(), tankOpts(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if n := len(g.ordersOf("cmd pullback")); n != 1 || rep.Remote.TankBusyWaits != 1 {
		t.Fatalf("pullbacks %d: %v", n, g.orders)
	}
}

// tank-busy for good: the waits are bounded and the party never goes in without its tank.
func TestRemotePullBusyTankIsBounded(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.cmdErr["pullback"] = []string{"tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy", "tank-busy"}
	g.busyFor = time.Hour
	o := tankOpts()
	o.TankBusyWait = 3 * time.Second
	rep, _ := runRemote(t, g, tankBusyPlan(), o, nil)
	if g.indexOf("cmd attack") >= 0 || rep.Remote.PullFallbacks != 0 || rep.Remote.TankBusyWaits == 0 {
		t.Fatalf("waits %d fallbacks %d: %v", rep.Remote.TankBusyWaits, rep.Remote.PullFallbacks, g.orders)
	}
	if rep.Remote.TankBusyWaits > 3*(tankBusyPlanPullAttempts+1) {
		t.Fatalf("unbounded waits: %d", rep.Remote.TankBusyWaits)
	}
}

const tankBusyPlanPullAttempts = 3

// no-tank stays a real refusal: the party is sent in as it is (the module found no tank at all).
func TestRemoteNoTankIsNotWaitedFor(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.cmdErr["pullback"] = []string{"no-tank"}
	rep, _ := runRemote(t, g, tankBusyPlan(), tankOpts(), func(c *Config) { c.EngageTimeout = 10 * time.Second })
	if rep.Remote.TankBusyWaits != 0 || rep.Remote.PullFallbacks != 1 {
		t.Fatalf("%+v", rep.Remote)
	}
}

// ---- the talents read-back ----

func talentsFake(t *testing.T, reply map[string][]string) *botFake {
	g := kitFake(t)
	for n, l := range reply {
		g.replies["command "+n+" talents"] = l
	}
	return g
}

func TestRemotePreflightFlagsAResetTank(t *testing.T) {
	g := talentsFake(t, map[string][]string{"Tanky": {"My current talent spec is: Warrior (0/0/0) Link: 0"}})
	rep, err := runRemote(t, g, plan1(), kitOpts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("a warning failed the run: %v", err)
	}
	pf := rep.Remote.Preflight
	if pf == nil || pf.OK || pf.Bots[0].Talents != "0/0/0" || pf.Bots[1].Talents != "0/5/31" {
		t.Fatalf("%+v", pf)
	}
	if !strings.Contains(strings.Join(pf.Warnings, "|"), "Tanky: no talent points spent (0/0/0)") {
		t.Fatalf("%v", pf.Warnings)
	}
	// strict: the run fails before the bind's first order past the preflight.
	o := kitOpts()
	o.Preflight = PreflightStrict
	g = talentsFake(t, map[string][]string{"Tanky": {"My current talent spec is: Warrior (0/0/0) Link: 0"}})
	rep, err = runRemote(t, g, plan1(), o, nil)
	if err == nil || !strings.Contains(err.Error(), "no talent points spent") || rep.State != "failed" || len(g.ordersOf("cmd pullback")) != 0 || g.unbinds != 1 {
		t.Fatalf("%v %s", err, rep.State)
	}
}

func TestRemotePreflightTalentsNoAnswerAndMarkup(t *testing.T) {
	// The bot's text carries colour and link codes ("|h|cff00ff00" before a non-zero count): they are read through.
	g := talentsFake(t, map[string][]string{
		"Tanky": {"My current talent spec is: |h|cffffffffProtection (|h|cff00ff005|h|cffffffff/|h|cff00ff0031|h|cffffffff/0|h|cffffffff) Link: 5-31"},
		"Healy": nil,
	})
	rep, err := runRemote(t, g, plan1(), kitOpts(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pf := rep.Remote.Preflight
	if pf.Bots[0].Talents != "5/31/0" || pf.Bots[1].Talents != "no answer" {
		t.Fatalf("%+v", pf.Bots)
	}
	if got := strings.Join(pf.Warnings, "|"); !strings.Contains(got, "Healy: talents: no answer") || strings.Contains(got, "Tanky") {
		t.Fatalf("%v", pf.Warnings)
	}
	// Only the talents line is sent as a `.bot command`.
	for _, l := range g.sent("command") {
		if !strings.HasSuffix(l, " talents") {
			t.Fatalf("%q", l)
		}
	}
}

// KeepOnFail: a failed run keeps the binding, so that a resume's bind finds the same group and instance (ZF-2: the unbind returned the
// hires to the commander's raid and the next bind made a new group). Off (the default), the failed run unbinds like any other end.
func TestRemoteFailedRunKeepsTheBinding(t *testing.T) {
	for _, c := range []struct {
		keep    bool
		unbinds int
	}{{false, 1}, {true, 0}} {
		g := newEnterFake(t)
		g.stuck["Magey"] = true
		o := opts()
		o.KeepOnFail = c.keep
		rep, err := g.run(o)
		if err == nil || rep.State != "failed" || g.unbinds != c.unbinds {
			t.Fatalf("keep_on_fail %v: %v state %s unbinds %d, want %d", c.keep, err, rep.State, g.unbinds, c.unbinds)
		}
		if c.keep && (rep.Remote == nil || !strings.HasPrefix(rep.Remote.UnbindVia, "kept")) {
			t.Fatalf("report %+v", rep.Remote)
		}
	}
}
