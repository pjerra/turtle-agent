package dungeon

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// ---- zf-r5 findings ----

// ---- the key of the Troll Cage (plan_keys.go) ----

const keyedLeg = `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},%s}]}`

const cageStep = `{"use_object":{"entry":141073,"near":{"x":1,"y":0,"z":0}%s}}`

// A use_object on an object whose lock is known to need an item must say so in requires_item; and the item must be made before the step.
// The plan check reports it (KeyGaps), not the loader.
func TestKeyedObjectNeedsItsKeyAndASourceForIt(t *testing.T) {
	const key = `"requires_item":{"entry":8444,"name":"Executioner's Key"}`
	boss := `"boss":{"entry":7274,"position":{"x":1,"y":0,"z":0},"loot_item":{"entry":8444,"name":"Executioner's Key"}}`
	for name, c := range map[string]struct{ leg, want string }{
		"no requires_item":           {`"steps":[` + fmt.Sprintf(cageStep, "") + `]`, "object 141073 has a lock that needs Executioner's Key (item 8444): add requires_item"},
		"a wrong item":               {`"steps":[` + fmt.Sprintf(cageStep, `,"requires_item":{"entry":9}`) + `]`, "object 141073 has a lock that needs Executioner's Key"},
		"no source":                  {`"steps":[` + fmt.Sprintf(cageStep, ","+key) + `]`, "needs Executioner's Key (item 8444), which no step before it makes"},
		"the boss is after the step": {`"steps":[` + fmt.Sprintf(cageStep, ","+key) + `],` + boss, "which no step before it makes"},
		"boss loot_item first":       {boss + `,"after_boss":[` + fmt.Sprintf(cageStep, ","+key) + `]`, ""},
		"a chest's expect_item":      {`"steps":[{"loot_object":{"entry":5,"near":{"x":1,"y":0,"z":0},"expect_item":{"entry":8444}}},` + fmt.Sprintf(cageStep, ","+key) + `]`, ""},
		"an optional leg":            {`"optional":{"item":8444,"name":"Executioner's Key"},"steps":[` + fmt.Sprintf(cageStep, ","+key) + `]`, ""},
		"an unlocked object":         {`"steps":[{"use_object":{"entry":5,"near":{"x":1,"y":0,"z":0}}}]`, ""},
	} {
		p, err := Parse("t.json", []byte(fmt.Sprintf(keyedLeg, c.leg)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		gaps := p.KeyGaps(0)
		switch {
		case c.want == "" && len(gaps) != 0:
			t.Errorf("%s: %v", name, gaps)
		case c.want != "" && (len(gaps) == 0 || !strings.Contains(gaps[0].String(), c.want)):
			t.Errorf("%s: %v, want %q", name, gaps, c.want)
		}
	}
}

// The plan check puts a key gap among its issues (reason "key"), and a plan without one passes.
func TestPlanCheckReportsAKeyGap(t *testing.T) {
	p, err := Parse("t.json", []byte(fmt.Sprintf(keyedLeg, `"steps":[`+fmt.Sprintf(cageStep, `,"requires_item":{"entry":8444,"name":"Executioner's Key"}`)+`]`)))
	if err != nil {
		t.Fatal(err)
	}
	res, err := CheckPlan(p, fakeNav{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var keys int
	for _, is := range res.Issues {
		if is.Reason == "key" && strings.Contains(is.String(), "needs Executioner's Key (item 8444), which no step before it makes") {
			keys++
		}
	}
	if res.OK() || keys != 1 {
		t.Fatalf("%d key issues: %s", keys, res.Summary())
	}
	// From a later leg on the gap of an earlier leg is not reported.
	if gaps := p.KeyGaps(1); len(gaps) != 0 {
		t.Fatalf("%v", gaps)
	}
}

// The item of a use_item's requires_item needs a source too, in an earlier leg as well.
func TestUseItemRequiresItemNeedsASource(t *testing.T) {
	const use = `{"use_item":{"entry":7,"requires_item":{"entry":8}}}`
	const mk = `{"to":{"x":0,"y":0,"z":0},"steps":[{"loot_object":{"entry":5,"near":{"x":1,"y":0,"z":0},"expect_item":{"entry":8}}}]}`
	plan := func(legs string) *Plan {
		p, err := Parse("t.json", []byte(`{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[`+legs+`]}`))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if gaps := plan(`{"to":{"x":0,"y":0,"z":0},"steps":[` + use + `]}`).KeyGaps(0); len(gaps) != 1 || !strings.Contains(gaps[0].String(), "needs item 8, which no step before it makes") {
		t.Fatalf("%v", gaps)
	}
	if gaps := plan(mk + `,{"to":{"x":0,"y":0,"z":0},"steps":[` + use + `]}`).KeyGaps(0); len(gaps) != 0 {
		t.Fatalf("%v", gaps)
	}
}

// Every shipped plan provides the keys of its locked objects and the items its steps need.
func TestShippedPlansProvideTheirKeys(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(plansDir, "*.json"))
	if err != nil || len(files) < 40 {
		t.Fatalf("%d plan files: %v", len(files), err)
	}
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range p.KeyGaps(0) {
			t.Errorf("%s: %s", p.Name, g)
		}
	}
}

// The shipped Zul'Farrak stairs leg kills the Sandfury Executioner as its boss, has the carrier loot the Executioner's Key from him (a 100 %
// drop) and uses the cage only after that (zf-r5: the cage open was refused "no-item").
func TestZulfarrakStairsLegLootsTheCageKey(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	l := zfLeg(t, p, "stairs")
	if l.Boss == nil || l.Boss.Entry != 7274 || l.Boss.LootItem == nil || l.Boss.LootItem.Entry != 8444 {
		t.Fatalf("boss %+v", l.Boss)
	}
	if len(l.Steps) != 0 || len(l.AfterBoss) < 2 || l.AfterBoss[0].UseObject == nil {
		t.Fatalf("steps %d after_boss %+v", len(l.Steps), l.AfterBoss)
	}
	u := l.AfterBoss[0].UseObject
	if u.Entry != 141073 || u.RequiresItem == nil || u.RequiresItem.Entry != 8444 {
		t.Fatalf("%+v", u)
	}
	if l.CompanionsLoot == nil || *l.CompanionsLoot {
		t.Fatalf("the leg's mute must stay on for the graveyard walk")
	}
	for _, g := range l.PullGroups {
		for _, e := range g.Entries {
			if e == 7274 {
				t.Errorf("7274 is the boss now, not also a pull group: %+v", g)
			}
		}
	}
}

// ---- Velratha's pack (leg velratha_b, pull group 1) ----

// Velratha patrols the strip (1658.6 1153.6) - (1650.4 1161.4) - (1636.5 1174.7) (the leg's note). The leg's stand, where the pack of five is
// pulled to and fought, stays out of her aggro range (zf-r5: the stand was 16.6 yd from the strip, she joined, the party wiped).
func TestVelrathaStandIsFarFromHerStrip(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	l := zfLeg(t, p, "velratha_b")
	strip := []Point{{1658.6, 1153.6, 9.8}, {1650.4, 1161.4, 9.9}, {1636.5, 1174.7, 9.8}}
	near := math.MaxFloat64
	for _, s := range strip {
		near = math.Min(near, math.Hypot(float64(l.To.X-s.X), float64(l.To.Y-s.Y)))
	}
	if near < 30 {
		t.Errorf("the stand %v is %.1f yd from Velratha's strip: her aggro range (about 20 yd, more with the pack's pull) reaches it", l.To, near)
	}
	// ... and still within the pull range of the first pack (pull_range 45), which it pulls from there.
	g := l.PullGroups[0]
	far := 0.0
	for _, at := range g.Positions {
		far = math.Max(far, math.Hypot(float64(l.To.X-at.X), float64(l.To.Y-at.Y)))
	}
	if far > 45 || fmt.Sprint(g.Entries) != "[5649 5649 5649 5650 7246]" {
		t.Errorf("group 0 %v is %.1f yd from the stand", g.Entries, far)
	}
}

// ---- a party released outside the instance in a `cmd enter` run ----

// outsideGame is an enterOrders whose agent stands at the door OUTSIDE the instance (map 1), as in a `cmd enter` run, with a GM account:
// `cmd summon` lands a bot at the commander (on map 1), `cmd enter` takes the party in.
type outsideGame struct {
	*enterOrders
	door Point
}

func (g *outsideGame) AgentPlace() (Point, uint32, bool)    { return g.door, 1, true }
func (g *outsideGame) Appear(context.Context, string) error { return nil }
func (g *outsideGame) WalkSelf(context.Context, Point) (WalkResult, error) {
	return WalkResult{Result: "arrived"}, nil
}

func (g *outsideGame) Command(ctx context.Context, command string) (tbm.Reply, error) {
	f := strings.Fields(command)
	if len(f) > 2 && f[0] == "cmd" && f[1] == "summon" {
		g.log = append(g.log, "summon "+f[2])
		r, err := g.fakeRemote.Command(ctx, command) // the base fake brings the bot to the lead, on the plan's map: put it with the commander instead
		for _, p := range g.party {
			if strings.EqualFold(p.Name, f[2]) {
				p.X, p.Y, p.Z = g.door.X, g.door.Y, g.door.Z
				g.memberMap[p.GUID] = 1
			}
		}
		return r, err
	}
	return g.enterFake.Command(ctx, command)
}

func newOutsideGame(t *testing.T, level int) *outsideGame {
	g := &outsideGame{enterOrders: newEnterOrders(t, enterAck(3)), door: Point{-100, 0, 0}}
	g.level = level
	g.mapID = insideMap // the bots start inside
	g.plan.Portal = &Portal{Map: 1, Point: g.door}
	g.plan.Legs = []Leg{{Name: "a", To: Point{300, 0, 0}}}
	return g
}

// The tank dies last and is spirit-healed at a graveyard on map 1 and the others follow it out (zf-r5, the Velratha wipe): all alive, all
// outside. The commander stands at the door outside, so `.summon` into the instance is impossible: every bot is brought to the commander with
// `cmd summon` (GM assists), put on follow, and `cmd enter` takes the party in again; the leg then goes on.
func TestWipeRecoveryBringsAPartyReleasedOutsideBackIn(t *testing.T) {
	g := newOutsideGame(t, 3)
	gy := Point{-6978, -3825, 12}
	done := false
	g.onTick = func(f *fakeRemote) {
		g.enterFake.tick(f)
		if !done && f.lead().X > 5 {
			done = true
			for _, p := range f.party {
				p.X, p.Y, p.Z = gy.X, gy.Y, gy.Z
				f.memberMap[p.GUID] = 1
			}
		}
	}
	cfg := DefaultConfig()
	cfg.RecoverWipe = true
	cfg.Setup = []string{"gm visible off"}
	o := RemoteOptions{Tank: "Tanky", Lead: "Tanky", Bots: []string{"Healy", "Magey"}, Enter: EnterCmd}
	d := NewRemoteDriver(g.plan, g, cfg, o, g.clock())
	err := d.Run(context.Background(), 0)
	rep, _ := d.Status()
	n := notesOf(rep)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, n)
	}
	if !done {
		t.Fatal("the party was never released")
	}
	for _, name := range []string{"Tanky", "Healy", "Magey"} {
		if g.idx("summon "+name) < 0 {
			t.Errorf("no cmd summon %s: %v", name, g.log)
		}
	}
	// The party started inside, so the one cmd enter is the re-entry, and it came after the summons; the party is on the plan's map again.
	if len(g.sent) != 1 || g.sent[0] != "cmd enter 924" || g.idx("summon Magey") > g.idx("order cmd enter 924") {
		t.Fatalf("cmd enter sent %v\n%s", g.sent, n)
	}
	for _, p := range g.party {
		if m, ok := g.memberMap[p.GUID]; !ok || m != insideMap {
			t.Errorf("%s is on map %v", p.Name, m)
		}
	}
	if !strings.Contains(n, "cmd summon to the commander, then in again") || !strings.Contains(n, "every bot is on map 70 again") {
		t.Fatalf("%s", n)
	}
	if rep.Remote.RunGMAssists < 3 || rep.Remote.RejoinTPs < 3 {
		t.Fatalf("gm assists %d rejoin tps %d", rep.Remote.RunGMAssists, rep.Remote.RejoinTPs)
	}
	var shortcuts int
	for _, s := range rep.SetupShortcuts {
		if strings.HasPrefix(s, "cmd summon ") {
			shortcuts++
		}
	}
	if shortcuts != 3 {
		t.Fatalf("setup shortcuts %v", rep.SetupShortcuts)
	}
	_ = packet.GUID(0)
}

// Without a GM account nothing is summoned: the party is waited for outside for a bounded time, then the recovery fails and says why.
func TestWipeRecoveryOutsideWithoutAGMFailsAfterABoundedWait(t *testing.T) {
	g := newOutsideGame(t, 0)
	done := false
	g.onTick = func(f *fakeRemote) {
		g.enterFake.tick(f)
		if !done && f.lead().X > 5 {
			done = true
			for _, p := range f.party {
				f.memberMap[p.GUID] = 1
			}
		}
	}
	cfg := DefaultConfig()
	cfg.RecoverWipe = true
	o := RemoteOptions{Tank: "Tanky", Lead: "Tanky", Bots: []string{"Healy", "Magey"}, Enter: EnterCmd}
	d := NewRemoteDriver(g.plan, g, cfg, o, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "outside the dungeon") || g.idx("summon") >= 0 {
		t.Fatalf("%v %v", err, g.log)
	}
}
