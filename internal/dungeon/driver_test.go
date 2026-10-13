package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

type fakeUnit struct {
	objects.Unit
	dieAt      time.Time
	noJoin     bool // pulling a neighbour does not bring it into the fight (stationary mobs with no link)
	stuck      bool // pulling it draws no fight at all
	stuckPulls int  // the next n pulls of it draw no fight
}

type lateAura struct {
	at  time.Time
	who packet.GUID
	id  uint32
}

type fakeEvent struct {
	kind string
	data map[string]any
}

// fakeGame is a Game the test steers: time moves only when the Driver sleeps, and a pull (the party chat
// ".bot action pullback") sets the targeted pack fighting and dying a few seconds later.
type fakeGame struct {
	now    time.Time
	pos    Point
	mapID  uint32
	units  []*fakeUnit
	objs   []objects.GameObject
	self   Self
	party  []Member
	dieIn  time.Duration // how long a pulled pack member fights before it dies
	canned []WalkResult  // results of the next walks; after them every walk arrives

	chats, gms         []string
	targets            []packet.GUID
	walks              []Point
	used               []packet.GUID
	teleports          []Point
	events             []fakeEvent
	onChat             func(g *fakeGame, text string)
	onTick             func(g *fakeGame)
	onUse              func(g *fakeGame, guid packet.GUID)
	walkNeverMoves     bool
	noEngage           bool
	healOnRest         bool
	resting            bool
	targeted           packet.GUID
	pathErr            error
	partyInViewNearMax float32 // distance the party members show at, 0: not in view

	partyClass  map[packet.GUID]uint8 // class ids the members' unit rows show (the healer is a priest)
	reviveDelay time.Duration         // a GM revive shows in the party list this long after (negative: never)
	revAt       map[string]time.Time  // revives under way, by member name
	onWalk      func(g *fakeGame, to Point)
	// walkHook replaces WalkPath (it may block until ctx ends, as a long walk does). It must not touch the units: the
	// driver's combat watch reads them from another goroutine meanwhile; aggro is the way to start a fight there.
	walkHook  func(ctx context.Context, g *fakeGame, to Point) (WalkResult, error)
	aggro     atomic.Bool
	aggroGUID packet.GUID       // the unit that is in combat with the leader while aggro is set
	afterGate func(g *fakeGame) // runs as the first rest gate looks at the party
	selfLooks int

	auras       map[packet.GUID][]objects.Aura // auras the party members' unit rows show, put there by ".aura <id>"
	auraRefused bool                           // the server ignores ".aura" (no developer level): nothing lands on the target
	auraErr     error                          // GM fails for ".aura"
	gmOnGUIDs   []packet.GUID                  // the unit each GMOn selected
	hidden      map[packet.GUID]bool           // party members that are not in view

	refusals      []Refusal                 // what the bots refused since the last look (the Driver reads them through RefusalSource)
	refuseTargets map[packet.GUID][]Refusal // a pullback at this target is refused with the first of these (and it is dropped), no fight starts
	auraDelay     time.Duration             // an ".aura" shows on its target only this long after
	lateAuras     []lateAura
	failGMOn      map[packet.GUID]int // the next n ".aura" commands selecting this unit land on the leader instead
	dead          fakeDeath           // the leader's death (fake_death_test.go)
	moveHeld      func() int          // the holds of the brain's move lock now (nil: not recorded)
	moveLog       []string            // "walk:1", "teleport:1" ...: each movement of the leader and the holds standing at it

	onEmit func(kind string, data map[string]any) // sees each event as it is emitted
	bag    []objects.Item                         // what the leader carries (ItemSource)
}

const (
	leader packet.GUID = 0x10
	tank   packet.GUID = 0x11
	healer packet.GUID = 0x12
)

func newFake() *fakeGame {
	g := &fakeGame{now: time.Unix(1_000_000, 0), mapID: 70, dieIn: 8 * time.Second, partyInViewNearMax: 5,
		self:  Self{GUID: leader, Name: "Lead", Health: 1000, MaxHealth: 1000},
		party: []Member{{Name: "Tank", GUID: tank, Online: true, Health: 900, MaxHealth: 900, PowerType: 1, Power: 50, MaxPower: 100}, {Name: "Heal", GUID: healer, Online: true, Health: 500, MaxHealth: 500, PowerType: 0, Power: 400, MaxPower: 400}}}
	g.partyClass = map[packet.GUID]uint8{tank: 1, healer: 5}
	return g
}

func (g *fakeGame) clock() Clock {
	return Clock{Now: func() time.Time { return g.now }, Sleep: func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.now = g.now.Add(d)
		for _, u := range g.units {
			if !u.dieAt.IsZero() && !g.now.Before(u.dieAt) {
				u.Dead, u.InCombat, u.dieAt = true, false, time.Time{}
				u.Health = 0
			}
		}
		for i := 0; i < len(g.lateAuras); i++ {
			if l := g.lateAuras[i]; !g.now.Before(l.at) {
				g.auras[l.who] = append(g.auras[l.who], objects.Aura{SpellID: l.id})
				g.lateAuras = append(g.lateAuras[:i], g.lateAuras[i+1:]...)
				i--
			}
		}
		for name, at := range g.revAt {
			if !g.now.Before(at) {
				for i := range g.party {
					if g.party[i].Name == name {
						g.party[i].Dead, g.party[i].Health = false, g.party[i].MaxHealth
					}
				}
				delete(g.revAt, name)
			}
		}
		if g.resting && g.healOnRest {
			for i := range g.party {
				m := &g.party[i]
				m.Health = min(m.MaxHealth, m.Health+m.MaxHealth/5)
				m.Power = min(m.MaxPower, m.Power+m.MaxPower/5)
			}
		}
		if g.onTick != nil {
			g.onTick(g)
		}
		return ctx.Err()
	}}
}

func (g *fakeGame) add(guid packet.GUID, entry uint32, at Point) *fakeUnit {
	u := &fakeUnit{Unit: objects.Unit{GUID: guid, Type: "creature", Entry: entry, Name: fmt.Sprintf("mob%d", entry), X: at.X, Y: at.Y, Z: at.Z,
		Hostile: true, Health: 100, MaxHealth: 100}}
	g.units = append(g.units, u)
	return u
}

func (g *fakeGame) Position() Point { return g.pos }
func (g *fakeGame) Map() uint32     { return g.mapID }
func (g *fakeGame) Self() Self {
	// The first look at the leader is the run's start, the second the first rest gate's: afterGate runs there, so
	// that what it starts is met by the first walk, not by the gate.
	if g.selfLooks++; g.selfLooks == 2 && g.afterGate != nil {
		g.afterGate(g)
	}
	return g.self
}
func (g *fakeGame) Party() []Member { return append([]Member(nil), g.party...) }
func (g *fakeGame) GameObjects() []objects.GameObject {
	out := append([]objects.GameObject(nil), g.objs...)
	for i := range out {
		out[i].Distance = float32(dist3(g.pos, Point{out[i].X, out[i].Y, out[i].Z}))
	}
	return out
}

func (g *fakeGame) Units() []objects.Unit {
	var out []objects.Unit
	for _, u := range g.units {
		c := u.Unit
		if g.aggro.Load() && c.GUID == g.aggroGUID && !c.Dead {
			c.InCombat, c.Target = true, leader
		}
		c.Distance = float32(dist3(g.pos, Point{c.X, c.Y, c.Z}))
		if c.Distance <= 150 {
			out = append(out, c)
		}
	}
	if g.partyInViewNearMax > 0 {
		for _, m := range g.party {
			if g.hidden[m.GUID] {
				continue
			}
			out = append(out, objects.Unit{GUID: m.GUID, Type: "player", Name: m.Name, Distance: g.partyInViewNearMax, Class: g.partyClass[m.GUID],
				Auras: append([]objects.Aura(nil), g.auras[m.GUID]...)})
		}
	}
	return out
}

// moved records how many holds of the brain's move lock stand while the Driver moves the leader (moveHeld reads them).
func (g *fakeGame) moved(kind string) {
	if g.moveHeld != nil {
		g.moveLog = append(g.moveLog, fmt.Sprintf("%s:%d", kind, g.moveHeld()))
	}
}

func (g *fakeGame) WalkPath(ctx context.Context, to Point) (WalkResult, error) {
	g.moved("walk")
	g.walks = append(g.walks, to)
	if g.walkHook != nil {
		return g.walkHook(ctx, g, to)
	}
	if len(g.canned) > 0 {
		r := g.canned[0]
		g.canned = g.canned[1:]
		return r, nil
	}
	if !g.walkNeverMoves {
		g.pos = to
	}
	if g.onWalk != nil {
		g.onWalk(g, to)
	}
	return WalkResult{Result: "arrived"}, nil
}

func (g *fakeGame) PathPoints(ctx context.Context, to Point) ([]Point, error) {
	if g.pathErr != nil {
		return nil, g.pathErr
	}
	return []Point{g.pos, to}, nil
}

func (g *fakeGame) UseObject(ctx context.Context, guid packet.GUID) error {
	g.moved("use")
	g.used = append(g.used, guid)
	if g.onUse != nil {
		g.onUse(g, guid)
	}
	return nil
}

func (g *fakeGame) Target(ctx context.Context, guid packet.GUID) error {
	g.targets = append(g.targets, guid)
	g.targeted = guid
	return nil
}

func (g *fakeGame) Chat(ctx context.Context, channel, text string) error {
	g.chats = append(g.chats, text)
	switch text {
	case ".bot action rest":
		g.resting = true
	case ".bot action pullback":
		if rs := g.refuseTargets[g.targeted]; len(rs) > 0 {
			g.refusals = append(g.refusals, rs[0])
			g.refuseTargets[g.targeted] = rs[1:]
		} else if !g.noEngage {
			g.pull()
		}
	}
	if g.onChat != nil {
		g.onChat(g, text)
	}
	return nil
}

// pull sets the targeted unit and its neighbours (within 25 yd) fighting, dying one after the other.
func (g *fakeGame) pull() {
	var t *fakeUnit
	for _, u := range g.units {
		if u.GUID == g.targeted {
			t = u
		}
	}
	if t == nil || stealthedFake(t) || t.stuck { // nobody attacks what it cannot see; a stuck mob does not come
		return
	}
	if t.stuckPulls > 0 {
		t.stuckPulls--
		return
	}
	n := 0
	for _, u := range g.units {
		if u != t && u.noJoin {
			continue // a stationary mob that does not link to the pack
		}
		if !u.Dead && !stealthedFake(u) && dist3(Point{u.X, u.Y, u.Z}, Point{t.X, t.Y, t.Z}) <= 25 {
			u.InCombat, u.Health = true, 50
			u.dieAt = g.now.Add(g.dieIn + time.Duration(n)*2*time.Second)
			u.Target = leader
			n++
		}
	}
}

// GMOn selects and sends in one step, as the session does: a selection changed by somebody else in between cannot happen.
func (g *fakeGame) GMOn(ctx context.Context, guid packet.GUID, text string) error {
	g.gmOnGUIDs = append(g.gmOnGUIDs, guid)
	g.targeted = guid
	return g.GM(ctx, text)
}

func (g *fakeGame) GM(ctx context.Context, text string) error {
	g.gms = append(g.gms, text)
	if id, ok := strings.CutPrefix(text, ".aura "); ok {
		if g.auraErr != nil {
			return g.auraErr
		}
		if n, err := strconv.ParseUint(id, 10, 32); err == nil && !g.auraRefused {
			if g.auras == nil {
				g.auras = map[packet.GUID][]objects.Aura{}
			}
			who := g.targeted
			if g.failGMOn[who] > 0 {
				g.failGMOn[who]--
				who = leader
			}
			if g.auraDelay > 0 {
				g.lateAuras = append(g.lateAuras, lateAura{at: g.now.Add(g.auraDelay), who: who, id: uint32(n)})
			} else {
				g.auras[who] = append(g.auras[who], objects.Aura{SpellID: uint32(n)})
			}
		}
	}
	if strings.HasPrefix(text, ".revive ") {
		for i := range g.party {
			if ".revive "+g.party[i].Name == text {
				switch {
				case g.reviveDelay == 0:
					g.party[i].Dead, g.party[i].Health = false, g.party[i].MaxHealth
					delete(g.auras, g.party[i].GUID)
				case g.reviveDelay > 0:
					if g.revAt == nil {
						g.revAt = map[string]time.Time{}
					}
					g.revAt[g.party[i].Name] = g.now.Add(g.reviveDelay)
				}
			}
		}
	}
	return nil
}

func stealthedFake(u *fakeUnit) bool { return Stealthed(u.Unit) }

// stealth gives a unit the Shadowstalker Stealth aura the Uldaman Cave Lurkers carry.
func (u *fakeUnit) stealth() *fakeUnit {
	u.Auras = []objects.Aura{{SpellID: 5916, Name: "Shadowstalker Stealth"}}
	return u
}

func (g *fakeGame) Teleport(ctx context.Context, to Point, mapID uint32) error {
	g.moved("teleport")
	g.teleports = append(g.teleports, to)
	g.pos, g.mapID = to, mapID
	return nil
}

func (g *fakeGame) Emit(kind, text string, data map[string]any) {
	if g.onEmit != nil {
		g.onEmit(kind, data)
	}
	g.events = append(g.events, fakeEvent{kind, data})
}

func (g *fakeGame) kinds() string {
	var ks []string
	for _, e := range g.events {
		ks = append(ks, strings.TrimPrefix(e.kind, "dungeon_"))
	}
	return strings.Join(ks, ",")
}

func (g *fakeGame) count(chat string) int {
	n := 0
	for _, c := range g.chats {
		if c == chat {
			n++
		}
	}
	return n
}

// A one-leg plan: a pack of two at 30 yd, a keystone door at 20 yd that opens a seal, and a boss with a brother
// 90 yd away (so the leader has to approach).
const runPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "pull_groups":[{"entries":[1,2],"positions":[{"x":30,"y":0,"z":0},{"x":32,"y":0,"z":0}]}],
  "steps":[{"use_object":{"entry":124371}},{"wait_for":{"gameobject":{"entry":124372,"state":"active"},"timeout_s":20}}],
  "boss":{"entry":7,"position":{"x":90,"y":0,"z":0},"first":[8]}},
 {"name":"b","to":{"x":95,"y":0,"z":0},"pull_groups":[{"entries":[3],"positions":[{"x":130,"y":0,"z":0}]}]}]}`

func runFixture(t *testing.T) (*fakeGame, *Plan) {
	t.Helper()
	g := newFake()
	g.add(101, 1, Point{30, 0, 0})
	g.add(102, 2, Point{32, 0, 0})
	g.add(108, 8, Point{88, 0, 0})
	g.add(107, 7, Point{90, 0, 0})
	g.add(103, 3, Point{130, 0, 0})
	g.objs = []objects.GameObject{{GUID: keystone, Entry: 124371, X: 20, State: "ready"}, {GUID: seal, Entry: 124372, X: 30, State: "ready"}}
	g.onUse = func(g *fakeGame, guid packet.GUID) {
		if guid == keystone {
			g.objs[1].State = "active"
		}
	}
	return g, mustPlan(t, runPlan)
}

func TestDriverRunsAPlan(t *testing.T) {
	g, p := runFixture(t)
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	if rep.State != "done" || rep.Wipes != 0 || len(rep.Deaths) != 0 || len(rep.Shortcuts) != 0 || len(rep.Legs) != 2 || rep.Error != "" || rep.Ended == nil {
		t.Fatalf("%+v", rep)
	}
	// Pack (101 is the nearest), then the boss's brother (the boss stands next to it and joins: it dies with it),
	// then leg b's single mob.
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{101, 108, 103}) {
		t.Fatalf("targets %v", g.targets)
	}
	if g.count(".bot action pullback") != 3 || len(g.used) != 1 || g.used[0] != keystone || g.gms[0] != ".gm visible on" || g.count(".bot action rest") != 0 {
		t.Fatalf("chats %q gms %q used %v", g.chats, g.gms, g.used)
	}
	if l := rep.Legs[0]; l.Name != "a" || l.Result != "done" || l.Pulls != 2 || l.PullRetries != 0 || l.Seconds <= 0 {
		t.Fatalf("%+v", l)
	}
	if l := rep.Legs[1]; l.Result != "done" || l.Pulls != 1 {
		t.Fatalf("%+v", l)
	}
	// The leg's walk, the walk next to the keystone, the approach to the brother 38 yd short of it (it is 70 yd away),
	// the boss is then in range.
	want := []Point{{10, 0, 0}, {20, 0, 0}, {50, 0, 0}, {95, 0, 0}}
	if fmt.Sprint(g.walks) != fmt.Sprint(want) {
		t.Fatalf("walks %v, want %v", g.walks, want)
	}
	if k := g.kinds(); !strings.HasPrefix(k, "leg,step") || !strings.HasSuffix(k, "leg,done") || strings.Contains(k, "failed") {
		t.Fatalf("events %s", k)
	}
	if last := g.events[len(g.events)-1]; last.kind != events.KindDungeonDone || last.data["state"] != "done" || last.data["wipes"] != 0 {
		t.Fatalf("%+v", last)
	}
}

func TestDriverRunsFromALeg(t *testing.T) {
	g, p := runFixture(t)
	g.mapID = 1 // outside
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "enter the dungeon first, or run with teleport") {
		t.Fatalf("%v", err)
	}

	// With teleport the leader is put at the end of leg a (the boss's position), the party summoned and the run flagged.
	g, p = runFixture(t)
	g.mapID = 1
	d = NewDriver(p, g, Config{Teleport: true}, g.clock())
	if err := d.Run(context.Background(), 1); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	if fmt.Sprint(g.teleports) != "[{90 0 0}]" || len(rep.Shortcuts) != 1 || rep.Shortcuts[0].Kind != "teleport_to_leg_start" || rep.Shortcuts[0].Leg != 1 ||
		g.count(".bot action follow") != 1 || len(rep.Legs) != 1 || rep.Legs[0].Leg != 1 || rep.FromLeg != 1 {
		t.Fatalf("%v %+v", g.teleports, rep)
	}
	if strings.Join(g.gms, "|") != ".gm visible on|.bot summon Tank|.bot summon Heal" {
		t.Fatalf("%q", g.gms)
	}

	// From the start, the shortcut is named after the entrance.
	g, p = runFixture(t)
	g.mapID = 1
	d = NewDriver(p, g, Config{Teleport: true}, g.clock())
	_ = d.Run(context.Background(), 0)
	if s := d.Report().Shortcuts; len(s) < 1 || s[0].Kind != "teleport_to_entrance" {
		t.Fatalf("%+v", s)
	}

	if err := NewDriver(p, g, Config{}, g.clock()).Run(context.Background(), 5); err == nil || !strings.Contains(err.Error(), "legs 0-1") {
		t.Fatalf("%v", err)
	}
}

func TestDriverWithoutAParty(t *testing.T) {
	g, p := runFixture(t)
	g.party = nil
	d := NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "no party") || d.Report().State != "failed" {
		t.Fatalf("%v %+v", err, d.Report())
	}
	if g.kinds() != "failed" {
		t.Fatalf("%s", g.kinds())
	}
}

func TestDriverWipeEndsTheRun(t *testing.T) {
	// The leader and every member dead is a wipe: the run ends.
	g, p := runFixture(t)
	g.dieIn = 30 * time.Second
	g.onTick = func(g *fakeGame) {
		if g.targeted == 101 && len(g.targets) == 1 && g.now.After(time.Unix(1_000_000, 0).Add(5*time.Second)) && g.units[0].InCombat {
			g.self.Health = 0
			g.party[0].Dead, g.party[1].Dead = true, true
		}
	}
	d := NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	rep := d.Report()
	if err == nil || err.Error() != "wipe" || rep.State != "failed" || rep.Error != "wipe" || rep.Wipes != 1 || len(rep.Deaths) != 3 || rep.Deaths[0].Name != "Lead" ||
		rep.Legs[0].Result != "failed" || rep.Legs[0].Deaths != 3 {
		t.Fatalf("%v %+v", err, rep)
	}
	last := g.events[len(g.events)-1]
	if last.kind != events.KindDungeonFailed || last.data["error"] != "wipe" || last.data["wipes"] != 1 {
		t.Fatalf("%+v", last)
	}

	// The whole party dead is a wipe too; one member dead is not.
	g, p = runFixture(t)
	g.onTick = func(g *fakeGame) {
		if len(g.targets) > 0 {
			g.party[0].Dead = true
			if g.now.After(time.Unix(1_000_000, 0).Add(12 * time.Second)) {
				g.party[1].Dead = true
			}
		}
	}
	d = NewDriver(p, g, Config{}, g.clock())
	err = d.Run(context.Background(), 0)
	if err == nil || err.Error() != "wipe" || len(d.Report().Deaths) != 2 || d.Report().Wipes != 1 {
		t.Fatalf("%v %+v", err, d.Report())
	}
}

func TestDriverBoundsWalks(t *testing.T) {
	g, p := runFixture(t)
	g.canned = []WalkResult{{Result: "partial", Reason: "rooted", DistLeft: 6}}
	g.walkNeverMoves = false
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("a retry after a partial walk: %v", err)
	}
	if g.walks[0] != g.walks[1] {
		t.Fatalf("the same walk is tried again: %v", g.walks)
	}

	g, p = runFixture(t)
	g.canned = []WalkResult{{Result: "no_path", Reason: "blocked"}, {Result: "no_path", Reason: "blocked"}, {Result: "no_path", Reason: "blocked"}}
	d = NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "walk to 10.0 0.0 0.0 failed after 3 tries (no_path: blocked") || d.Report().Wipes != 0 {
		t.Fatalf("%v", err)
	}
}

// A use_object step whose walks "arrive" without getting next to the object ends the plan after MaxStepWalks.
func TestDriverEndsAStepThatNeverGetsInReach(t *testing.T) {
	g, p := runFixture(t)
	g.walkNeverMoves = true
	g.pos = Point{10, 0, 0}
	d := NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "leg 0 (a): step 0: game object 124371 is still out of reach after 4 walks") {
		t.Fatalf("%v", err)
	}
	if n := len(g.walks); n != MaxStepWalks {
		t.Fatalf("%d walks: %v", n, g.walks)
	}
}

func TestDriverWaitTimesOut(t *testing.T) {
	g, p := runFixture(t)
	g.onUse = nil // the seal never opens
	d := NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || err.Error() != "leg 0 (a): step 1: waited 20 s for game object 124372 to be active" {
		t.Fatalf("%v", err)
	}
	if g.count(".bot action pullback") != 1 {
		t.Fatalf("the boss is not pulled after a failed step: %q", g.chats)
	}
}

func TestDriverPullThatDrawsNoFightIsRetriedThenFails(t *testing.T) {
	g, p := runFixture(t)
	g.noEngage = true
	d := NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	rep := d.Report()
	if err == nil || !strings.Contains(err.Error(), "3 pulls of [1 2] failed") || rep.Legs[0].Pulls != 3 || rep.Legs[0].PullRetries != 2 {
		t.Fatalf("%v %+v", err, rep)
	}
	if g.count(".bot action pullback") != 3 || g.count(".bot action follow") != 3 {
		t.Fatalf("%q", g.chats)
	}
	var drew int
	for _, n := range rep.Notes {
		if strings.Contains(n, "drew no fight") {
			drew++
		}
	}
	if drew != 3 {
		t.Fatalf("%q", rep.Notes)
	}
}

func TestDriverMobsThatEvadeEndThePull(t *testing.T) {
	g, p := runFixture(t)
	g.dieIn = time.Hour
	g.onTick = func(g *fakeGame) { // the pack is hit once, then drops out of combat
		for _, u := range g.units {
			if u.InCombat && g.now.After(time.Unix(1_000_000, 0).Add(10*time.Second)) {
				u.InCombat, u.Health, u.dieAt = false, 100, time.Time{}
			}
		}
	}
	d := NewDriver(p, g, Config{PullAttempts: 1}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "1 pulls of [1 2] failed") {
		t.Fatalf("%v", err)
	}
	if n := d.Report().Notes; !strings.Contains(strings.Join(n, "|"), "evaded") {
		t.Fatalf("%q", n)
	}
}

func TestDriverRestGate(t *testing.T) {
	g, p := runFixture(t)
	g.healOnRest = true
	g.party[0].Health, g.party[1].Power = 300, 100 // the tank at 33% health, the healer at 25% mana
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if g.count(".bot action rest") != 1 || d.Report().Legs[0].RestSeconds < 3 {
		t.Fatalf("%q %+v", g.chats, d.Report().Legs[0])
	}

	// A party that never rests: a poor party stops the run, a nearly rested one goes on with a note.
	g, p = runFixture(t)
	g.party[0].Health = 300
	d = NewDriver(p, g, Config{RestTimeout: 20 * time.Second}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "the party is not rested after 20 s: [Tank health 33%]") {
		t.Fatalf("%v", err)
	}
	g, p = runFixture(t)
	g.party[0].Health = 700 // 77%: below 90, above the 50 floor
	d = NewDriver(p, g, Config{RestTimeout: 20 * time.Second}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if n := strings.Join(d.Report().Notes, "|"); !strings.Contains(n, "rest gate gave up after 20 s") {
		t.Fatalf("%q", n)
	}

	// Members who are not near the leader are waited for too (and named when the gate gives up).
	g, p = runFixture(t)
	g.partyInViewNearMax = 0
	d = NewDriver(p, g, Config{RestTimeout: 10 * time.Second}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if n := strings.Join(d.Report().Notes, "|"); !strings.Contains(n, "rest gate gave up after 10 s ([not near the leader: [Tank Heal]])") {
		t.Fatalf("%q", n)
	}
}

func TestDriverRevivesADeadMemberAndFlagsIt(t *testing.T) {
	g, p := runFixture(t)
	g.party[1].Dead = true // dead before the run: not a death of the run
	d := NewDriver(p, g, Config{Shortcuts: true}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	rep := d.Report()
	if len(rep.Shortcuts) != 1 || rep.Shortcuts[0].Kind != "revive" || rep.Shortcuts[0].Detail != "GM .revive Heal" || len(rep.Deaths) != 0 {
		t.Fatalf("%+v", rep)
	}
	if !strings.Contains(strings.Join(g.gms, "|"), ".revive Heal") {
		t.Fatalf("%q", g.gms)
	}

	// A death during the run is counted, and without shortcuts the next rest gate stops the run.
	g, p = runFixture(t)
	g.onTick = func(g *fakeGame) {
		if len(g.targets) > 0 && !g.party[1].Dead {
			g.party[1].Dead = true
		}
	}
	d = NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "Heal is dead and GM shortcuts are off") || len(d.Report().Deaths) != 1 || len(d.Report().Shortcuts) != 0 {
		t.Fatalf("%v %+v", err, d.Report())
	}
}

func TestDriverStops(t *testing.T) {
	g, p := runFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action pullback" {
			cancel()
		}
	}
	d := NewDriver(p, g, Config{}, g.clock())
	err := d.Run(ctx, 0)
	rep := d.Report()
	if !errors.Is(err, context.Canceled) || rep.State != "stopped" || rep.Legs[0].Result != "stopped" || rep.Wipes != 0 {
		t.Fatalf("%v %+v", err, rep)
	}
	if last := g.events[len(g.events)-1]; last.kind != events.KindDungeonFailed || last.data["error"] != "stopped" {
		t.Fatalf("%+v", last)
	}
}

func TestDriverBossMustBeFound(t *testing.T) {
	g, p := runFixture(t)
	g.units = append(g.units[:3:3], g.units[4:]...) // no unit 107 (the boss)
	d := NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "no living [7] in view") {
		t.Fatalf("%v", err)
	}
	// A pack that is not there (already dead) is not an error: it is noted.
	g, p = runFixture(t)
	g.units = g.units[2:]
	d = NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(strings.Join(d.Report().Notes, "|"), "taken as already dead") {
		t.Fatalf("%q", d.Report().Notes)
	}
}

func TestDriverIgnoresSkippedEntries(t *testing.T) {
	g := newFake()
	g.add(201, 5, Point{20, 0, 0})
	g.add(202, 6, Point{22, 0, 0}) // skipped: never targeted
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"to":{"x":0,"y":0,"z":0},"skip":[6],"pull_groups":[{"entries":[5],"positions":[{"x":20,"y":0,"z":0}]}]}]}`)
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{201}) {
		t.Fatalf("%v", g.targets)
	}
}

func TestDriverReportSnapshotWhileRunning(t *testing.T) {
	g, p := runFixture(t)
	d := NewDriver(p, g, Config{}, g.clock())
	if r := d.Report(); r.State != "idle" || len(r.Legs) != 0 || r.Deaths == nil || r.Shortcuts == nil || r.Notes == nil {
		t.Fatalf("%+v", r)
	}
	var mid Report
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action pullback" && mid.State == "" {
			mid = d.Report()
		}
	}
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if mid.State != "running" || len(mid.Legs) != 1 || mid.Legs[0].Result != "running" || mid.Current == "" {
		t.Fatalf("%+v", mid)
	}
}

func TestStandoffPoint(t *testing.T) {
	path := []Point{{0, 0, 0}, {10, 0, 0}, {10, 30, 0}}
	for _, c := range []struct {
		standoff float32
		want     Point
		ok       bool
	}{
		{10, Point{10, 20, 0}, true},   // on the last leg
		{30, Point{10, 0, 0}, true},    // exactly at the corner
		{35, Point{5, 0, 0}, true},     // round the corner: 5 yd up the first leg
		{39.5, Point{0.5, 0, 0}, true}, // nearly the whole path
		{40, Point{}, false},           // as long as the path: nothing to walk
		{41, Point{}, false},           // the path is shorter than the standoff: nothing to walk
		{0, Point{10, 30, 0}, true},    // zero is the target itself
	} {
		got, ok := StandoffPoint(path, c.standoff)
		if ok != c.ok || (ok && dist3(got, c.want) > 1e-3) {
			t.Errorf("standoff %v: %v %v, want %v %v", c.standoff, got, ok, c.want, c.ok)
		}
	}
	if _, ok := StandoffPoint([]Point{{1, 1, 1}}, 5); ok {
		t.Error("a path of one point")
	}
}

// The first of a boss's entries is fought before the boss, each pulled on its own when they stand apart.
func TestDriverKillsFirstThenBoss(t *testing.T) {
	g := newFake()
	g.add(308, 8, Point{60, 0, 0})
	g.add(307, 7, Point{90, 0, 0})
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"name":"boss","to":{"x":0,"y":0,"z":0},"boss":{"entry":7,"position":{"x":90,"y":0,"z":0},"first":[8]}}]}`)
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{308, 307}) || fmt.Sprint(g.walks) != "[{22 0 0} {52 0 0}]" {
		t.Fatalf("targets %v walks %v", g.targets, g.walks)
	}
	if l := d.Report().Legs[0]; l.Pulls != 2 || l.Result != "done" {
		t.Fatalf("%+v", l)
	}
}

// fakeBrain records what the Driver tells the leader's brain.
type fakeBrain struct {
	log      []string
	running  bool
	startErr error
	held     int   // holds of the move lock standing now
	holds    int   // holds taken in all
	maxHeld  int   // most holds at once
	holdErr  error // HoldMove fails with this
}

func (b *fakeBrain) HoldMove(ctx context.Context) (func(), error) {
	if b.holdErr != nil {
		return func() {}, b.holdErr
	}
	b.held++
	b.holds++
	b.maxHeld = max(b.maxHeld, b.held)
	var once bool
	return func() {
		if !once {
			once = true
			b.held--
		}
	}, nil
}

func (b *fakeBrain) Start() error {
	if b.startErr != nil {
		return b.startErr
	}
	if !b.running {
		b.log = append(b.log, "start")
	}
	b.running = true
	return nil
}

func (b *fakeBrain) Stop() {
	if b.running {
		b.log = append(b.log, "stop")
	}
	b.running = false
}

func (b *fakeBrain) Rest(on bool) { b.log = append(b.log, fmt.Sprintf("rest=%v", on)) }

func TestDriverRunsTheLeadersBrainFromTheFirstRestGateToTheEndOfTheLeg(t *testing.T) {
	g, p := runFixture(t)
	br := &fakeBrain{}
	d := NewDriver(p, g, Config{Brain: br, BrainName: "frost_mage"}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if d.Report().Brain != "frost_mage" {
		t.Fatalf("%+v", d.Report())
	}
	// Each leg: the brain starts at its first rest gate, upkeep is allowed inside every gate only, the leg ends with it off.
	log := strings.Join(br.log, ",")
	start := strings.Index(log, "start")
	if start != 0 || strings.Count(log, "start") != 2 || strings.Count(log, "stop") != 2 || br.running {
		t.Fatalf("log %s running %v", log, br.running)
	}
	if strings.Count(log, "rest=true") != strings.Count(log, "rest=false") || !strings.HasPrefix(log, "start,rest=true,rest=false") {
		t.Fatalf("every gate opens and closes upkeep: %s", log)
	}
	if i, j := strings.Index(log, "stop"), strings.LastIndex(log, "start"); i > j {
		t.Fatalf("leg 0 stops before leg 1 starts: %s", log)
	}
}

func TestDriverStopsTheBrainWhenARunIsStoppedOrFails(t *testing.T) {
	g, p := runFixture(t)
	br := &fakeBrain{}
	ctx, cancel := context.WithCancel(context.Background())
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action pullback" {
			cancel()
		}
	}
	d := NewDriver(p, g, Config{Brain: br}, g.clock())
	_ = d.Run(ctx, 0)
	if br.running {
		t.Fatalf("a stopped run left the brain on: %v", br.log)
	}
}

func TestDriverNotesABrainThatWillNotStartAndGoesOn(t *testing.T) {
	g, p := runFixture(t)
	br := &fakeBrain{startErr: errors.New("a brain is running")}
	d := NewDriver(p, g, Config{Brain: br}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	notes := strings.Join(d.Report().Notes, "|")
	if strings.Count(notes, "bot brain did not start") != 1 {
		t.Fatalf("%q", notes)
	}
}

func TestDriverWithoutABrainIsUnchanged(t *testing.T) {
	g, p := runFixture(t)
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil || d.Report().Brain != "" {
		t.Fatalf("%v %+v", err, d.Report())
	}
}

// Refusals implements RefusalSource.
func (g *fakeGame) Refusals() []Refusal {
	out := g.refusals
	g.refusals = nil
	return out
}

// SelfAuras implements SelfAuraSource.
func (g *fakeGame) SelfAuras() ([]objects.Aura, bool) { return g.auras[leader], true }

// A boss out of view at the end of the leg's walk is not taken as dead: the leader walks toward its position, sees it and kills it.
// A boss whose corpse lies there is dead already, and the note says the corpse was seen.
func TestDriverWalksToAnOutOfViewBoss(t *testing.T) {
	plan := `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"name":"far","to":{"x":10,"y":0,"z":0},"boss":{"entry":7,"position":{"x":240,"y":0,"z":0}}}]}`
	g := newFake()
	g.add(107, 7, Point{240, 0, 0}) // 230 yd from the leg's end: beyond the 150 yd the fake shows
	d := NewDriver(mustPlan(t, plan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{107}) || strings.Contains(notes(d), "dead already") || strings.Contains(notes(d), "taken as already dead") {
		t.Fatalf("targets %v notes %q", g.targets, notes(d))
	}

	g = newFake()
	u := g.add(107, 7, Point{240, 0, 0})
	u.Dead, u.Health = true, 0
	d = NewDriver(mustPlan(t, plan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.targets) != 0 || !strings.Contains(notes(d), "[7] is dead already: its corpse is in view") {
		t.Fatalf("targets %v notes %q", g.targets, notes(d))
	}
}
