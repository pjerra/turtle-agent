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

// The remote Uldaman gaps (issue #83): the team gate, packs the bots do not see, the wait for a resurrection, the kit check and the end
// of the run.

// teamFake is a RemoteGame that knows the agent's team (TeamSource).
type teamFake struct {
	*fakeRemote
	team string
}

func (g teamFake) Team() string { return g.team }

// botFake is a RemoteGame that also answers plain `.bot` lines (BotLiner) from a script: replies maps "maintenance Name" or "inv Name"
// to the lines that come back.
type botFake struct {
	*fakeRemote
	replies map[string][]string
	lines   []string // the lines sent
}

func (g *botFake) BotLines(_ context.Context, command string, _ time.Duration, done func(string) bool) ([]string, bool, error) {
	if command != "roster" { // the start of a run reads the roster for its cc bot (resolveCCBot); what these tests count are the other lines
		g.lines = append(g.lines, command)
	}
	var got []string
	for _, l := range g.replies[command] {
		got = append(got, l)
		if done(l) {
			return got, true, nil
		}
	}
	return got, false, nil
}

func (g *botFake) sent(prefix string) []string {
	var out []string
	for _, l := range g.lines {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func notesOf(rep Report) string { return strings.Join(rep.Notes, "\n") }

// G1: Baelog is friendly to the Alliance and never in the feed. A team-gated boss is skipped for the party's team the way group mode
// does it, instead of failing the run with "no living [6906] seen".
func TestRemoteTeamGateSkipsTheFriendlyBoss(t *testing.T) {
	plan := remotePlan(Leg{Name: "baelog", To: Point{20, 0, 0},
		PullGroups: []PullGroup{{Entries: []uint32{1}, Positions: []Point{{30, 0, 0}}, Team: TeamHorde}},
		Boss:       &Boss{Entry: 6906, Position: Point{60, 0, 0}, First: []uint32{6907}, Team: TeamHorde}})

	// The team option.
	g := newFakeRemote(t)
	o := opts()
	o.Team = "Alliance"
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %v", err, rep.State, rep.Notes)
	}
	if rep.Remote.Team != TeamAlliance || !strings.Contains(notesOf(rep), "is for team horde, the leader is alliance: skipped") || !strings.Contains(notesOf(rep), "boss 6906 is for team horde") {
		t.Fatalf("%+v\n%s", rep.Remote, notesOf(rep))
	}
	if len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("a gated group was pulled: %v", g.orders)
	}

	// The agent's own team, when the run says none.
	rep, err = runRemote(t, teamFake{newFakeRemote(t), TeamAlliance}, plan, opts(), nil)
	if err != nil || rep.Remote.Team != TeamAlliance || !strings.Contains(notesOf(rep), "the agent's own race") {
		t.Fatalf("%v %+v\n%s", err, rep.Remote, notesOf(rep))
	}

	// The option beats the agent's team.
	o.Team = "horde"
	g = newFakeRemote(t)
	rep, err = runRemote(t, teamFake{g, TeamAlliance}, plan, o, nil)
	if rep.Remote.Team != TeamHorde {
		t.Fatalf("%+v", rep.Remote)
	}
	// ... and for the Horde the boss is not gated: the run fights it, and fails because the (hostile) boss is not there to be seen.
	if err == nil || !strings.Contains(err.Error(), "no living") {
		t.Fatalf("a horde party skipped the boss: %v\n%s", err, notesOf(rep))
	}

	// No team known: nothing is gated, and the report says so.
	g = newFakeRemote(t)
	rep, _ = runRemote(t, g, plan, opts(), nil)
	if rep.Remote.Team != "" || !strings.Contains(notesOf(rep), "team is unknown") {
		t.Fatalf("%+v\n%s", rep.Remote, notesOf(rep))
	}
}

func packPlan(at Point) *Plan {
	return remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{at}}}})
}

// G3: a pack the bots do not see from the 38 yd standoff is not "already dead": the lead bot walks to it, sees it and pulls it.
func TestRemoteUnseenPackIsWalkedToAndPulled(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 30
	g.addMob(299, Point{300, 0, 0})
	rep, err := runRemote(t, g, packPlan(Point{300, 0, 0}), opts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if g.mobsKilled != 1 || len(g.ordersOf("cmd pullback")) != 1 {
		t.Fatalf("the pack was skipped: killed %d, orders %v\n%s", g.mobsKilled, g.orders, notesOf(rep))
	}
	if strings.Contains(notesOf(rep), "taken as already dead") {
		t.Fatalf("%s", notesOf(rep))
	}
	if rep.Remote.PackLooks != 1 {
		t.Fatalf("looks %d", rep.Remote.PackLooks)
	}
}

// G3: a pack that really is gone is believed after the lead bot stood at its position and two whole frames showed none: it counts as
// seen empty, and the party really went there.
func TestRemoteEmptyPackIsLookedAt(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 30
	rep, err := runRemote(t, g, packPlan(Point{300, 0, 0}), opts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if rep.Remote.PackLooks != 1 || rep.Remote.PacksSeenEmpty != 1 || !strings.Contains(notesOf(rep), "counted as cleared") {
		t.Fatalf("%+v\n%s", rep.Remote, notesOf(rep))
	}
	near := 1e9
	for _, p := range g.trace {
		near = min(near, dist3(p, Point{300, 0, 0}))
	}
	if near > ClearSeeRadius {
		t.Fatalf("the lead bot got no nearer than %.0f yd to the pack's position", near)
	}
	if len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("%v", g.orders)
	}
}

// G3: a party member that already stands at the position and sees none is evidence enough: no walk.
func TestRemoteEmptyPackSeenFromWhereTheyStand(t *testing.T) {
	g := newFakeRemote(t)
	g.lead().X = 290
	for _, p := range g.party[1:] {
		p.X = 290
	}
	plan := remotePlan(Leg{Name: "a", To: Point{290, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{300, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.Remote.PackLooks != 0 || !strings.Contains(notesOf(rep), "a party member stood within 25 yd and saw none") {
		t.Fatalf("%v %+v\n%s", err, rep.Remote, notesOf(rep))
	}
}

// G3: mobs killed earlier in the run are evidence for a group at the same spot (a step tried again after a wipe recovery).
func TestRemoteKilledPackIsNotLookedForAgain(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 30
	g.addMob(299, Point{300, 0, 0})
	at := []Point{{300, 0, 0}}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: at}, {Entries: []uint32{299}, Positions: at}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || g.mobsKilled != 1 || rep.Remote.PackLooks > 1 || !strings.Contains(notesOf(rep), "killed earlier in this run") {
		t.Fatalf("%v killed %d %+v\n%s", err, g.mobsKilled, rep.Remote, notesOf(rep))
	}
}

// G3: the resume rule believes an empty view on the same terms: a member must have looked at every position of the group.
func TestStepperResumeNeedsEveryPositionSeen(t *testing.T) {
	plan := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"to":{"x":100,"y":0,"z":0},"pull_groups":[
	   {"entries":[1,1],"positions":[{"x":150,"y":0,"z":0},{"x":150,"y":80,"z":0}]},
	   {"entries":[2],"positions":[{"x":250,"y":0,"z":0}]}]}]}`)
	// The party stands at 170,0: group 1's first position is 20 yd away, its second 80 yd.
	w := &resumeWorld{fakeWorld: fakeWorld{pos: Point{170, 0, 0}}}
	w.near = func(c Point) bool { return dist3(Point{170, 0, 0}, c) <= ResumeSeeRadius }
	s := NewStepperAt(plan, w, nil, 0)
	if r, ok := s.ResumeAt(); !ok || r.SkippedGroups != 0 {
		t.Fatalf("a group with an unseen position was skipped: %+v %v", r, ok)
	}
	// Both positions in sight: skipped.
	w.near = func(Point) bool { return true }
	s = NewStepperAt(plan, w, nil, 0)
	if r, ok := s.ResumeAt(); !ok || r.SkippedGroups != 1 {
		t.Fatalf("%+v %v", r, ok)
	}
}

// G8: a dead bot with a living caster out of combat is left to the resurrection; nobody is released.
func TestRemoteRezWaitLetsTheHealerRaise(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
	raised := false
	g.onTick = func(g *fakeRemote) {
		if !raised && g.now.Sub(time.Unix(1_000_000, 0)) > 12*time.Second {
			raised = true
			p := g.party[2]
			p.Dead, p.HP, p.State = false, p.MaxHP/2, "n"
		}
	}
	o := opts()
	o.Healer = "Healy"
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if len(g.ordersOf("cmd revive")) != 0 || rep.Remote.Revives != 0 || rep.Remote.RezWaits != 1 || rep.Remote.Rezzed != 1 || rep.Remote.Rezzes != 1 {
		t.Fatalf("orders %v %+v\n%s", g.ordersOf("cmd revive"), rep.Remote, notesOf(rep))
	}
}

func plan1() *Plan {
	return remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
}

// A body lying far from every living caster (rez_reach_yd) is not waited for: the caster would have to run to it, and the existing revive
// path takes it at once. A body near the caster is left to it (the test above).
func TestRemoteRezWaitSkippedForAFarCorpse(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
	g.party[2].X = -100 // its body lies 100 yd from the healer
	g.lagUntil = map[packet.GUID]time.Time{gMage: g.now.Add(time.Hour)}
	o := opts()
	o.Healer = "Healy"
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if len(g.ordersOf("cmd revive")) != 1 || rep.Remote.RezWaits != 0 || rep.Remote.Rezzes != 0 {
		t.Fatalf("%v %+v", g.orders, rep.Remote)
	}
}

// G8: no caster left to raise it (the dead bot is the only one with mana besides none): released at once, no wait.
func TestRemoteRezWaitSkippedWithoutAResurrector(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	for _, i := range []int{1, 2} { // both mana users dead; the warrior alone is left
		g.party[i].Dead, g.party[i].HP, g.party[i].State = true, 0, "d"
	}
	rep, err := runRemote(t, g, plan1(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if len(g.ordersOf("cmd revive")) != 2 || rep.Remote.RezWaits != 0 {
		t.Fatalf("%v %+v", g.orders, rep.Remote)
	}
}

// G8: a healer that does not raise them in RezWait does not hold the run for ever: the bot is released with the revive order.
func TestRemoteRezWaitIsBounded(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
	began := g.now
	var releasedAt time.Time
	g.onTick = func(g *fakeRemote) {
		if releasedAt.IsZero() && len(g.revived) > 0 {
			releasedAt = g.now
		}
	}
	o := opts()
	o.RezWait = 20 * time.Second
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if len(g.ordersOf("cmd revive")) != 1 || releasedAt.Sub(began) < 20*time.Second || releasedAt.Sub(began) > 45*time.Second {
		t.Fatalf("revived at +%v: %v", releasedAt.Sub(began), g.orders)
	}
	// Off: no wait.
	g = newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
	o.RezWait = -1
	if rep, err = runRemote(t, g, plan1(), o, nil); err != nil || rep.Remote.RezWaits != 0 || len(g.ordersOf("cmd revive")) != 1 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
}

// ---- G4: the kit check ----

const (
	invOf = "TBM:INV_BEGIN|%s|0"
)

func invLines(name string, eq map[int]uint32, items map[uint32]int) []string {
	out := []string{fmt.Sprintf(invOf, name)}
	for slot, e := range eq {
		out = append(out, fmt.Sprintf("TBM:INV_EQ|%d|%d|10|10", slot, e))
	}
	i := 0
	for e, n := range items {
		out = append(out, fmt.Sprintf("TBM:INV_ITEM|255|%d|%d|%d|-", 23+i, e, n))
		i++
	}
	return append(out, fmt.Sprintf("TBM:INV_END|%s|10|16", name))
}

func kitFake(t *testing.T) *botFake {
	g := &botFake{fakeRemote: newFakeRemote(t), replies: map[string][]string{}}
	for _, n := range []string{"Tanky", "Healy", "Magey"} {
		g.replies["maintenance "+n] = []string{"Maintained " + n + " (consumables, reagents, ammo, repair)."}
		g.replies["command "+n+" talents"] = []string{"Forwarded command for " + n + ": talents; action success is not guaranteed.", "My current talent spec is: Protection (0/5/31) Link: 05-31"}
	}
	water, bow, gun, arrow, shield := drinkEntries[0], arrowWeaponEntries[0], bulletWeaponEntries[0], arrowEntries[0], shieldEntries[0]
	_ = bow
	g.replies["inv Tanky"] = invLines("Tanky", map[int]uint32{tbm.SlotOffHand: shield}, nil)
	g.replies["inv Healy"] = invLines("Healy", nil, map[uint32]int{water: 40})
	g.replies["inv Magey"] = invLines("Magey", map[int]uint32{tbm.SlotRanged: gun}, map[uint32]int{water: 12, arrow: 300}) // a gun, arrows
	return g
}

func kitOpts() RemoteOptions {
	o := opts()
	o.Tank, o.Healer = "Tanky", "Healy"
	return o
}

func TestRemotePreflightReportsWhatIsMissing(t *testing.T) {
	g := kitFake(t)
	// A healer with too few drinks, a gun with arrows only, a tank without a shield.
	g.replies["inv Healy"] = invLines("Healy", nil, map[uint32]int{drinkEntries[0]: 5})
	g.replies["inv Tanky"] = invLines("Tanky", nil, nil)
	rep, err := runRemote(t, g, plan1(), kitOpts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("a warning failed the run: %v %v", err, rep.Notes)
	}
	pf := rep.Remote.Preflight
	if pf == nil || pf.OK || pf.Mode != PreflightWarn || len(pf.Bots) != 3 {
		t.Fatalf("%+v", pf)
	}
	all := strings.Join(pf.Warnings, "\n")
	for _, want := range []string{"Healy: 5 drinks in the bags, 20 wanted", "Magey: its ranged weapon takes bullets and the bags hold the other kind (300)", "Tanky: the tank wears no shield"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	for _, b := range pf.Bots {
		if b.Maintenance != "maintained" || b.Inventory != "read" {
			t.Errorf("%+v", b)
		}
	}
	if got := g.sent("maintenance"); len(got) != 3 || len(g.sent("inv")) != 3 {
		t.Fatalf("%v", g.lines)
	}
	// Maintenance goes out before any inventory is read, and before the first order of the run.
	if g.lines[0] != "maintenance Tanky" || g.lines[3] != "command Tanky talents" || g.lines[5] != "command Magey talents" || g.lines[6] != "inv Tanky" {
		t.Fatalf("%v", g.lines)
	}
}

func TestRemotePreflightCleanKit(t *testing.T) {
	g := kitFake(t)
	g.replies["inv Magey"] = invLines("Magey", map[int]uint32{tbm.SlotRanged: bulletWeaponEntries[0]}, map[uint32]int{drinkEntries[0]: 12, bulletEntries[0]: 400})
	rep, err := runRemote(t, g, plan1(), kitOpts(), nil)
	pf := rep.Remote.Preflight
	if err != nil || pf == nil || !pf.OK || len(pf.Warnings) != 0 {
		t.Fatalf("%v %+v", err, pf)
	}
	if pf.Bots[2].Ammo != "bullets 400" || pf.Bots[0].Shield == nil || !*pf.Bots[0].Shield || pf.Bots[1].Drinks != 40 {
		t.Fatalf("%+v", pf.Bots)
	}
}

func TestRemotePreflightStrictFailsTheRun(t *testing.T) {
	g := kitFake(t)
	g.replies["inv Tanky"] = invLines("Tanky", nil, nil)
	o := kitOpts()
	o.Preflight = "STRICT"
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err == nil || !strings.Contains(err.Error(), "preflight (strict)") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.unbinds != 1 || len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("the run went on or did not unbind: %v", g.orders)
	}
}

func TestRemotePreflightMaintenanceOffOrRefused(t *testing.T) {
	g := kitFake(t)
	g.replies["maintenance Tanky"] = []string{"Owned-bot maintenance is disabled (AiPlayerbot.OwnedBotMaintenanceEnabled = 0)."}
	rep, err := runRemote(t, g, plan1(), kitOpts(), nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	pf := rep.Remote.Preflight
	if pf.Bots[0].Maintenance != "disabled" || pf.Bots[1].Maintenance != "not sent" || len(g.sent("maintenance")) != 1 || len(g.sent("inv")) != 3 {
		t.Fatalf("%+v %v", pf.Bots, g.lines)
	}
	if !strings.Contains(strings.Join(pf.Warnings, "|"), "OwnedBotMaintenanceEnabled") {
		t.Fatalf("%v", pf.Warnings)
	}

	// A refusal of one bot is recorded; the rest go on. An unanswered inventory is a warning, not a failure.
	g = kitFake(t)
	g.replies["maintenance Healy"] = []string{"Bot 'Healy' must be alive."}
	g.replies["inv Magey"] = nil
	rep, err = runRemote(t, g, plan1(), kitOpts(), nil)
	pf = rep.Remote.Preflight
	if err != nil || pf.Bots[1].Maintenance != "refused: the bot is dead" || pf.Bots[2].Maintenance != "maintained" || pf.Bots[2].Inventory != "no answer" || pf.OK {
		t.Fatalf("%v %+v", err, pf)
	}
}

func TestRemotePreflightOffAndNoBotLiner(t *testing.T) {
	g := kitFake(t)
	o := kitOpts()
	o.Preflight = PreflightOff
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err != nil || len(g.lines) != 0 || rep.Remote.Preflight != nil {
		t.Fatalf("%v %v %+v", err, g.lines, rep.Remote.Preflight)
	}
	// A game that cannot send plain bot lines skips the check with a note.
	rep, err = runRemote(t, newFakeRemote(t), plan1(), kitOpts(), nil)
	if err != nil || rep.Remote.Preflight != nil || !strings.Contains(notesOf(rep), "preflight: skipped") {
		t.Fatalf("%v %s", err, notesOf(rep))
	}
}

// ---- the end of the run ----

func TestRemoteEndStaysBeforeTheUnbind(t *testing.T) {
	g := newFakeRemote(t)
	rep, err := runRemote(t, g, plan1(), opts(), nil)
	if err != nil {
		t.Fatal(err)
	}
	n := len(g.orders)
	if g.orders[n-1] != "commander unbind" || g.orders[n-2] != "cmd stay" || rep.Remote.EndStay != "ack" {
		t.Fatalf("%v %+v", g.orders[max(0, n-4):], rep.Remote)
	}
	// A failed run stands the bots still too.
	g = newFakeRemote(t)
	g.mapID = 70
	_, err = runRemote(t, g, plan1(), opts(), nil)
	if err == nil || g.orders[len(g.orders)-2] != "cmd stay" {
		t.Fatalf("%v %v", err, g.orders)
	}
	// on_end none: the unbind only.
	g = newFakeRemote(t)
	o := opts()
	o.OnEnd = OnEndNone
	if rep, err = runRemote(t, g, plan1(), o, nil); err != nil || len(g.ordersOf("cmd stay")) != 0 || rep.Remote.EndStay != "" {
		t.Fatalf("%v %v", err, g.orders)
	}
	// A refused stay is recorded and the unbind still goes.
	g = newFakeRemote(t)
	g.cmdErr["stay"] = []string{"not-owner"}
	rep, err = runRemote(t, g, plan1(), opts(), nil)
	if err != nil || rep.Remote.EndStay != "refused: not-owner" || g.unbinds != 1 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
}

func TestRemoteEndLogsTheBotsOut(t *testing.T) {
	g := &botFake{fakeRemote: newFakeRemote(t), replies: map[string][]string{
		"logout Tanky": {"Bot Tanky logout requested."},
		"logout Healy": {"Bot Healy logout requested."},
		"logout Magey": {"Bot 'Magey' not found"},
	}}
	o := opts()
	o.OnEnd, o.Preflight = OnEndLogout, PreflightOff
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rep.Remote.LoggedOut, ","); got != "Tanky,Healy" || len(g.sent("logout")) != 3 {
		t.Fatalf("%q %v", got, g.lines)
	}
	n := len(g.orders)
	if g.orders[n-1] != "commander unbind" || g.orders[n-2] != "cmd stay" {
		t.Fatalf("the logout must come after stay and unbind: %v", g.orders[n-3:])
	}
	if !strings.Contains(notesOf(rep), "logout Magey: refused") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// G3: a kill clears only the position it happened at. Positions A and B of one group, B round a corner: A is marked killed, so the lead
// bot does not walk there, but it does walk to B and finds the pack that stands there.
func TestRemoteKillAtOnePositionDoesNotClearTheOther(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 30
	a, b := Point{300, 0, 0}, Point{300, 120, 0}
	g.addMob(299, b)
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299, 299}, Positions: []Point{a, b}}}})
	cfg := DefaultConfig()
	d := NewRemoteDriver(plan, g, cfg, opts(), g.clock())
	ctx := context.Background()
	if err := d.begin(ctx, 0); err != nil {
		t.Fatal(err)
	}
	d.curLeg = 0
	sp := fightSpec{pick: []uint32{299}, track: []uint32{299}, anchors: []Point{a, b}}
	d.noteKilled(fightResult{out: won, kills: 1, dead: []deadAt{{entry: 299, at: a}}}) // the pack at A died
	cleared, why, err := d.confirmGone(ctx, sp, newClearing())
	if err != nil || cleared {
		t.Fatalf("a kill at A cleared B: cleared %v (%s) %v", cleared, why, err)
	}
	if rep := d.Report(); rep.Remote.PackLooks != 1 {
		t.Fatalf("looks %d: %v", rep.Remote.PackLooks, rep.Notes)
	}
	if l := g.lead(); dist3(Point{l.X, l.Y, l.Z}, b) > 30 || dist3(Point{l.X, l.Y, l.Z}, a) < 100 {
		t.Fatalf("the lead bot stands at %.0f %.0f: it should have gone to B, not A", l.X, l.Y)
	}
	// B's mob really is there, now in view; once it is gone and B is looked at, the group is cleared.
	g.mobs = nil
	if cleared, why, err = d.confirmGone(ctx, sp, newClearing()); err != nil || !cleared {
		t.Fatalf("%v %s %v", cleared, why, err)
	}
}

// G8: Rezzed counts the bots actually raised, also when another one falls during the wait.
func TestRemoteRezWaitCountsTheBotsRaised(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
	start := g.now
	g.onTick = func(g *fakeRemote) {
		el := g.now.Sub(start)
		if el > 6*time.Second && el < 8*time.Second { // the tank falls while the healer is at it
			g.party[0].Dead, g.party[0].HP, g.party[0].State = true, 0, "d"
		}
		if el > 12*time.Second { // both are raised
			for _, p := range g.party[:3] {
				if p.Dead {
					p.Dead, p.HP, p.State = false, p.MaxHP/2, "n"
				}
			}
		}
	}
	o := opts()
	o.Healer = "Healy"
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err != nil || rep.Remote.Rezzed != 2 || len(g.ordersOf("cmd revive")) != 0 {
		t.Fatalf("%v %+v %v", err, rep.Remote, g.orders)
	}
}

// G3 at the engage level: positions A and B of one group, B out of the bots' sight from A. The pull at A ends won; the fight tracked
// only A's mob, so the driver must not stop there: it walks to B and pulls the pack that stands there.
func TestRemoteWonPullAtAWalksToB(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 30
	a, b := Point{300, 0, 0}, Point{300, 120, 0}
	g.addMob(299, a)
	g.addMob(299, b)
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{a, b}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if g.mobsKilled != 2 || len(g.ordersOf("cmd pullback")) != 2 {
		t.Fatalf("B's pack was left: killed %d, orders %v\n%s", g.mobsKilled, g.orders, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "2 kills; nothing more of [299]") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// The same with B empty: after the won pull at A the lead bot walks to B, sees nothing there and the group is done (no endless loop, no
// failure).
func TestRemoteWonPullAtAConfirmsEmptyB(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 30
	a, b := Point{300, 0, 0}, Point{300, 120, 0}
	g.addMob(299, a)
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{a, b}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if g.mobsKilled != 1 || len(g.ordersOf("cmd pullback")) != 1 || rep.Remote.PacksSeenEmpty != 1 {
		t.Fatalf("killed %d, orders %v, %+v\n%s", g.mobsKilled, g.orders, rep.Remote, notesOf(rep))
	}
	near := 1e9
	for _, p := range g.trace {
		near = min(near, dist3(p, b))
	}
	if near > ClearSeeRadius {
		t.Fatalf("the lead bot got no nearer than %.0f yd to B", near)
	}
	if !strings.Contains(notesOf(rep), "1 kills; nothing more of [299] at its positions: killed earlier in this run; the lead bot stood there and saw none") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// Round 3 of the G3 review, item 1: a boss whose First add is already dead, the boss standing at its position. The First add's step
// judges the position by the entries it can pick (the add), as the group driver does: the boss in view there is no add, and it shows
// the bots have the spot in sight, so the step ends without a walk and the boss's own step pulls the boss.
func TestRemoteBossWhoseFirstAddIsDead(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(6906, Point{60, 0, 0})
	plan := remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, Boss: &Boss{Entry: 6906, Position: Point{60, 0, 0}, First: []uint32{6907}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || g.mobsKilled != 1 || len(g.ordersOf("cmd pullback")) != 1 {
		t.Fatalf("%v killed %d orders %v\n%s", err, g.mobsKilled, g.orders, notesOf(rep))
	}
	if rep.Remote.PackLooks != 0 || !strings.Contains(notesOf(rep), "the bots see 6906 standing there and none of [6907]") {
		t.Fatalf("%+v\n%s", rep.Remote, notesOf(rep))
	}
}

// Item 2: a group that gave up on a mob still has every position confirmed. At A one mob draws no fight and the other dies; B, out of
// the bots' sight, holds a pack: the lead bot walks to B and pulls it instead of "gave up with 1 kills ... counted as cleared".
func TestRemoteGaveUpStillConfirmsTheOtherPositions(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 30
	a, b := Point{300, 0, 0}, Point{300, 120, 0}
	g.addMob(299, Point{297, 0, 0}).inert = true
	g.addMob(299, Point{303, 0, 0})
	g.addMob(299, b)
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{a, b}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if g.mobsKilled != 2 || len(g.mobs) != 1 || !g.mobs[0].inert {
		t.Fatalf("B's pack was left: killed %d, orders %v\n%s", g.mobsKilled, g.orders, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "gave up with 2 kills") || strings.Contains(notesOf(rep), "gave up with 1 kills") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// Item 3: at the cap (the pulls used up) a group with kills counts as cleared only when every position has evidence. A's mob dies, A's
// other mob never comes, and B was never looked at: the run fails instead of counting B cleared.
func TestRemoteCapNeedsEveryPositionSeen(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 60 // A's two mobs are in view together; B, 120 yd on, is not
	a, b := Point{300, 0, 0}, Point{300, 120, 0}
	g.addMob(299, Point{290, 0, 0})
	g.addMob(299, Point{316, 0, 0}).inert = true // 26 yd from the first: not in its fight
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{a, b}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "were never seen") || !strings.Contains(err.Error(), "300 120 0") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if g.mobsKilled != 1 || len(g.ordersOf("cmd pullback")) != 4 || !strings.Contains(notesOf(rep), "drew no fight") {
		t.Fatalf("killed %d, orders %v\n%s", g.mobsKilled, g.orders, notesOf(rep))
	}
	// With the second position next to the first (the kill and the failed pull are evidence for it), the same pulls count as cleared.
	g = newFakeRemote(t)
	g.sight = 60
	g.addMob(299, Point{290, 0, 0})
	g.addMob(299, Point{316, 0, 0}).inert = true
	plan = remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{a, {300, 10, 0}}}}})
	rep, err = runRemote(t, g, plan, opts(), nil)
	if err != nil || !strings.Contains(notesOf(rep), "gave up with 1 kills") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

// Item 4: only the units that died in a won pull mark their spot. The boss is pulled, its add joins and dies, the boss never comes and
// then walks off out of sight: its spot is not "killed earlier", and the boss is not counted dead.
func TestRemoteBossThatEvadesAfterItsAddIsNotDead(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 45
	boss := g.addMob(777, Point{50, 0, 0})
	boss.inert = true
	add := g.addMob(778, Point{55, 0, 0})
	plan := remotePlan(Leg{Name: "b", To: Point{0, 0, 0}})
	d := NewRemoteDriver(plan, g, DefaultConfig(), opts(), g.clock())
	ctx := context.Background()
	if err := d.begin(ctx, 0); err != nil {
		t.Fatal(err)
	}
	d.curLeg = 0
	follows := len(g.ordersOf("cmd follow"))
	g.onTick = func(g *fakeRemote) {
		if add.hp > 0 && !add.pulled && len(g.ordersOf("cmd pullback "+tbm.GUIDArg(boss.guid))) > 0 {
			add.pulled, add.pullAt = true, g.now // the boss's add answers the pull
		}
		if len(g.ordersOf("cmd follow")) > follows {
			boss.pos = Point{50, 300, 0} // the boss walked off after the fight
		}
	}
	sp := fightSpec{pick: []uint32{777}, track: []uint32{778, 777}, anchors: []Point{{50, 0, 0}}, limit: d.cfg.BossTimeout, required: true, wide: true}
	err := d.engage(ctx, "boss", sp)
	if err == nil || !strings.Contains(err.Error(), "no living [777]") {
		t.Fatalf("a boss that never died was counted dead: %v\n%s", err, notesOf(d.Report()))
	}
	if add.hp > 0 {
		t.Fatal("the add did not die")
	}
}

// Item 5: a boss that dies in its First add's won pull is killed: its step does not fail with "no living [777] seen".
func TestRemoteBossKilledInItsAddsPull(t *testing.T) {
	g := newFakeRemote(t)
	boss := g.addMob(777, Point{60, 0, 0})
	add := g.addMob(778, Point{63, 0, 0})
	g.onTick = func(g *fakeRemote) {
		if add.pulled && !boss.pulled {
			boss.pulled, boss.pullAt = true, g.now // linked: the boss comes with its add
		}
	}
	plan := remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, Boss: &Boss{Entry: 777, Position: Point{60, 0, 0}, First: []uint32{778}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || g.mobsKilled != 2 || len(g.ordersOf("cmd pullback")) != 1 {
		t.Fatalf("%v killed %d orders %v\n%s", err, g.mobsKilled, g.orders, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "[777] is dead (killed in this run)") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// multiFake answers BotLinesAll too (the real game does, tbm.Commander.LinesAll): one call for the party's talents, its wait recorded.
type multiFake struct {
	*botFake
	calls   int
	waits   []time.Duration
	silent  map[string]bool // bots that never answer `talents`
	allCmds []string
}

func (g *multiFake) BotLinesAll(_ context.Context, reqs []BotLineRequest, wait time.Duration) ([][]string, []bool, error) {
	g.calls++
	g.waits = append(g.waits, wait)
	lines, settled := make([][]string, len(reqs)), make([]bool, len(reqs))
	for i, r := range reqs {
		g.allCmds = append(g.allCmds, r.Command)
		for _, l := range g.replies[r.Command] {
			if g.silent[strings.Fields(r.Command)[1]] && strings.Contains(l, "talent spec") {
				continue
			}
			lines[i] = append(lines[i], l)
			if r.Done(l) {
				settled[i] = true
				break
			}
		}
	}
	return lines, settled, nil
}

// The talents of the whole party are asked together and read in one wait of at most 7 s (it was 6 s per bot, one after another: 31 s per
// start in the 2026-10-15 ZF runs); a bot that does not answer is "no answer" and a warning, the others are read.
func TestRemotePreflightTalentsAreAskedTogether(t *testing.T) {
	g := &multiFake{botFake: kitFake(t), silent: map[string]bool{"Healy": true}}
	rep, err := runRemote(t, g, plan1(), kitOpts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if g.calls != 1 || len(g.waits) != 1 || g.waits[0] > 7*time.Second || len(g.allCmds) != 3 {
		t.Fatalf("calls %d waits %v commands %v", g.calls, g.waits, g.allCmds)
	}
	if got := g.sent("command"); len(got) != 0 {
		t.Fatalf("talents were also asked one by one: %v", got)
	}
	pf := rep.Remote.Preflight
	talents := map[string]string{}
	for _, b := range pf.Bots {
		talents[b.Name] = b.Talents
	}
	if talents["Tanky"] != "0/5/31" || talents["Magey"] != "0/5/31" || talents["Healy"] != "no answer" {
		t.Fatalf("talents %v", talents)
	}
	if all := strings.Join(pf.Warnings, "\n"); !strings.Contains(all, "Healy: talents: no answer") || strings.Contains(all, "Tanky: talents") {
		t.Fatalf("warnings %v", pf.Warnings)
	}
}

func TestRemotePreflightTalentsAllAnswered(t *testing.T) {
	g := &multiFake{botFake: kitFake(t)}
	rep, err := runRemote(t, g, plan1(), kitOpts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	for _, b := range rep.Remote.Preflight.Bots {
		if b.Talents != "0/5/31" {
			t.Errorf("%+v", b)
		}
	}
	for _, w := range rep.Remote.Preflight.Warnings {
		if strings.Contains(w, "talents") {
			t.Errorf("warning %q", w)
		}
	}
}
