package brain

import (
	"context"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/spellstate"
)

// The spells of the tests, with their template numbers (internal/spells/meta.tsv.gz).
const (
	spFrostbolt  = 7322  // rank 4: 2.6 s cast, 65 mana, 30 yd
	spFireBlast  = 2136  // instant, 40 mana, 20 yd, 8 s
	spFrostNova  = 122   // instant, 55 mana, 25 s
	spCone       = 120   // instant, 210 mana, 10 s
	spEvocation  = 12051 // channel, 8 min
	spFrostArmor = 168
	spIceArmor   = 7302
	spIntellect  = 1459
	spConjure    = 5504 // Conjure Water rank 1
	itemWater    = 5350
	selfG        = packet.GUID(1)
	tankG        = packet.GUID(2)
	mobA, mobB   = packet.GUID(100), packet.GUID(101)
	manaFull     = 1000
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type castCall struct {
	spell  uint32
	target packet.GUID
}

// fakeGame is a scripted world: the test sets the units, the party and the spellbook, and reads back what the
// brain did. A cast goes off at once (the spell state sees SPELL_START and SPELL_GO) unless slow is set.
type fakeGame struct {
	mu       sync.Mutex
	clk      *fakeClock
	st       *spellstate.State
	self     objects.Unit
	selfOK   bool
	units    []objects.Unit
	party    []Member
	selected packet.GUID
	items    []objects.Item
	castErr  error
	slow     bool // a cast stays in progress (SPELL_START without SPELL_GO)
	block    chan struct{}

	casts   []castCall
	faces   []packet.GUID
	used    []uint32
	targets []packet.GUID
	attacks int
	stops   int
	cancels int
	stands  int
	evs     []events.Event
}

func newFake(known ...uint32) *fakeGame {
	clk := newClock()
	st := spellstate.New(clk.Now)
	st.SetInitial(game.InitialSpells{Spells: known})
	return &fakeGame{clk: clk, st: st, selfOK: true,
		self: objects.Unit{GUID: selfG, Type: "player", Name: "Leader", Class: 8, Level: 30, Health: 800, MaxHealth: 800,
			PowerType: 0, Power: manaFull, MaxPower: manaFull, StandState: "stand", Auras: []objects.Aura{}}}
}

func (f *fakeGame) Self() (objects.Unit, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.self, f.selfOK
}

func (f *fakeGame) Units() []objects.Unit {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]objects.Unit{}, f.units...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	return out
}

func (f *fakeGame) Party() []Member {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Member{}, f.party...)
}

func (f *fakeGame) Selected() packet.GUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.selected
}

func (f *fakeGame) Spells() *spellstate.State { return f.st }

func (f *fakeGame) Items() []objects.Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]objects.Item{}, f.items...)
}

func (f *fakeGame) Cast(ctx context.Context, spell uint32, target packet.GUID) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.castErr != nil {
		return f.castErr
	}
	f.casts = append(f.casts, castCall{spell, target})
	m, _ := spells.Lookup(spell)
	f.st.CastStarted(spell, m.CastMS, target)
	if !f.slow {
		f.st.CastWent(spell)
	}
	return nil
}

func (f *fakeGame) Face(ctx context.Context, g packet.GUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faces = append(f.faces, g)
	for _, u := range f.units {
		if u.GUID == g {
			f.self.O = game.Facing(f.self.X, f.self.Y, u.X, u.Y)
		}
	}
	return nil
}

func (f *fakeGame) Target(ctx context.Context, g packet.GUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, g)
	f.selected = g
	return nil
}

func (f *fakeGame) Attack(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attacks++
	return nil
}

func (f *fakeGame) StopAttack(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}

// CancelAutoRepeat switches the auto-repeat spell off.
func (f *fakeGame) CancelAutoRepeat(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	return nil
}

// Stand stands the player up: the seat and the meal auras go, as the server's update would show.
func (f *fakeGame) Stand(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stands++
	f.self.StandState = "stand"
	var keep []objects.Aura
	for _, a := range f.self.Auras {
		if a.Name != "Drink" && a.Name != "Food" {
			keep = append(keep, a)
		}
	}
	f.self.Auras = keep
	return nil
}

func (f *fakeGame) UseItem(ctx context.Context, entry uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.used = append(f.used, entry)
	return nil
}

func (f *fakeGame) Emit(e events.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evs = append(f.evs, e)
}

func (f *fakeGame) events(kind string) []events.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []events.Event
	for _, e := range f.evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeGame) castList() []castCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]castCall{}, f.casts...)
}

func (f *fakeGame) lastCast(t *testing.T) castCall {
	t.Helper()
	c := f.castList()
	if len(c) == 0 {
		t.Fatal("no cast was sent")
	}
	return c[len(c)-1]
}

// place puts a unit at x, y and gives it its distance from the player.
func (f *fakeGame) place(u objects.Unit, x, y float32) objects.Unit {
	u.X, u.Y = x, y
	u.Distance = float32(math.Hypot(float64(x-f.self.X), float64(y-f.self.Y)))
	u.Distance2D = u.Distance
	if u.Auras == nil {
		u.Auras = []objects.Aura{}
	}
	return u
}

func (f *fakeGame) addUnit(u objects.Unit, x, y float32) *objects.Unit {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.units = append(f.units, f.place(u, x, y))
	return &f.units[len(f.units)-1]
}

// mutate changes a unit of the scene by guid.
func (f *fakeGame) mutate(g packet.GUID, fn func(u *objects.Unit)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.units {
		if f.units[i].GUID == g {
			fn(&f.units[i])
			f.units[i] = f.place(f.units[i], f.units[i].X, f.units[i].Y)
		}
	}
}

func (f *fakeGame) setSelf(fn func(u *objects.Unit)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.self)
}

// A party of the leader and a warrior tank 25 yards east, with the tank's mob 30 yards east: the standard fight.
func (f *fakeGame) partyWithTank() {
	f.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	f.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 900, MaxHealth: 900, Target: mobA}, 25, 0)
}

func mob(g packet.GUID, target packet.GUID) objects.Unit {
	return objects.Unit{GUID: g, Type: "creature", Name: "Mob", Hostile: true, InCombat: true, Health: 500, MaxHealth: 500, Target: target}
}

// codeOf finds the SpellCastResult code of a reason name.
func codeOf(t *testing.T, name string) uint8 {
	t.Helper()
	for i := 0; i < 200; i++ {
		if game.CastFailName(uint8(i)) == name {
			return uint8(i)
		}
	}
	t.Fatalf("no cast result named %q", name)
	return 0
}

// rig is a brain on a fake game with its clock.
type rig struct {
	t *testing.T
	g *fakeGame
	b *Brain
}

func newRig(t *testing.T, g *fakeGame, p Params) *rig {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	b, err := New(g, cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, g: g, b: b}
}

// tick lets d pass and runs one tick.
func (r *rig) tick(d time.Duration) *Decision {
	r.t.Helper()
	r.g.clk.advance(d)
	dec := r.b.Tick(context.Background())
	r.settleStep(200 * time.Millisecond)
	return dec
}

// settleStep waits (in real time, up to max) for the brain's walk step to end: the fake walker answers at once, so a step started by
// the tick is over a moment later. A walker held on purpose (posFake.hold) outlasts the wait, which is what those tests want.
func (r *rig) settleStep(max time.Duration) {
	r.t.Helper()
	for end := time.Now().Add(max); r.b.lock.stepping() && time.Now().Before(end); {
		time.Sleep(time.Millisecond)
	}
}

// recent is the decisions the brain kept, oldest first.
func (r *rig) recent() []Decision {
	r.b.mem.mu.Lock()
	defer r.b.mem.mu.Unlock()
	return append([]Decision{}, r.b.mem.recent...)
}

func (r *rig) idle() string {
	r.b.mem.mu.Lock()
	defer r.b.mem.mu.Unlock()
	return r.b.mem.idle
}
