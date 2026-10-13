package dungeon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spellstate"
)

// rezGame is the leader's priest as the real brain sees it, over the driver's fake: the tank alive, the healer's body 5 yd away while
// it is dead. A resurrection cast takes its 10 s; the member accepts (is alive) a second after it goes off.
type rezGame struct {
	g     *fakeGame
	st    *spellstate.State
	casts []packet.GUID
	endAt time.Time
	spell uint32
}

func (r *rezGame) Self() (objects.Unit, bool) {
	return objects.Unit{GUID: leader, Type: "player", Name: "Lead", Class: 5, Level: 30, Health: 1000, MaxHealth: 1000, PowerType: 0,
		Power: 1000, MaxPower: 1000, StandState: "stand", Auras: []objects.Aura{}}, true
}

func (r *rezGame) Units() []objects.Unit {
	var out []objects.Unit
	for _, m := range r.g.party {
		class := uint8(1)
		if m.GUID == healer {
			class = 5
		}
		out = append(out, objects.Unit{GUID: m.GUID, Type: "player", Name: m.Name, Class: class, InParty: true, Dead: m.Dead, Health: m.Health,
			MaxHealth: m.MaxHealth, X: 5, Distance: 5, Distance2D: 5, Auras: []objects.Aura{}})
	}
	return out
}

func (r *rezGame) Party() []brain.Member {
	var out []brain.Member
	for _, m := range r.g.party {
		out = append(out, brain.Member{Name: m.Name, GUID: m.GUID, Online: m.Online, Dead: m.Dead})
	}
	return out
}

func (r *rezGame) Selected() packet.GUID                     { return 0 }
func (r *rezGame) Spells() *spellstate.State                 { return r.st }
func (r *rezGame) Items() []objects.Item                     { return nil }
func (r *rezGame) Face(context.Context, packet.GUID) error   { return nil }
func (r *rezGame) Target(context.Context, packet.GUID) error { return nil }
func (r *rezGame) Attack(context.Context) error              { return nil }
func (r *rezGame) StopAttack(context.Context) error          { return nil }
func (r *rezGame) UseItem(context.Context, uint32) error     { return nil }
func (r *rezGame) Emit(events.Event)                         {}

func (r *rezGame) Cast(_ context.Context, spell uint32, target packet.GUID) error {
	r.casts = append(r.casts, target)
	r.st.CastStarted(spell, 10000, target)
	r.spell, r.endAt = spell, r.g.now.Add(10*time.Second)
	return nil
}

// step is called after each slice of time: a finished cast goes off and the member accepts a second later.
func (r *rezGame) step() {
	if !r.endAt.IsZero() && !r.g.now.Before(r.endAt) {
		r.st.CastWent(r.spell)
		r.endAt = time.Time{}
		r.g.revAt = map[string]time.Time{"Heal": r.g.now.Add(time.Second)}
	}
}

// realRezBrain is a dungeon.Brain over the real brain.Brain, ticked every 200 ms of the fake clock.
type realRezBrain struct {
	*fakeBrain
	b *brain.Brain
}

func (b *realRezBrain) CanRez() bool { return b.b.CanRez() }

func newRealRezBrain(t *testing.T, g *fakeGame) (*realRezBrain, *rezGame, Clock) {
	t.Helper()
	st := spellstate.New(func() time.Time { return g.now })
	st.SetInitial(game.InitialSpells{Spells: []uint32{2010, 2054}}) // Resurrection rank 2, Heal rank 1
	rg := &rezGame{g: g, st: st}
	cfg := brain.DefaultConfig()
	cfg.Now = func() time.Time { return g.now }
	b, err := brain.New(rg, cfg, brain.Params{Strategies: []string{"holy_priest"}})
	if err != nil {
		t.Fatal(err)
	}
	clk := g.clock()
	orig := clk.Sleep
	clk.Sleep = func(ctx context.Context, d time.Duration) error {
		for left := d; left > 0; {
			step := min(200*time.Millisecond, left)
			if err := orig(ctx, step); err != nil {
				return err
			}
			left -= step
			rg.step()
			b.Tick(ctx)
		}
		return nil
	}
	return &realRezBrain{fakeBrain: &fakeBrain{}, b: b}, rg, clk
}

// The rest gate keeps waiting through the whole 10 s cast and the member's accept, with the real brain's CanRez: the brain skips the member it
// has sent a resurrection to (its hold), which must not read as "nothing to resurrect".
func TestRestGateWaitsThroughTheRealBrainsCastWithGMShortcutsOn(t *testing.T) {
	for name, cfg := range map[string]Config{"shortcuts": {Shortcuts: true}, "recover_wipe": {RecoverWipe: true}, "neither": {}} {
		g, p := runFixture(t)
		g.party[1].Dead, g.party[1].Health = true, 0
		br, rg, clk := newRealRezBrain(t, g)
		cfg.Brain, cfg.BrainName = br, "holy_priest"
		d := NewDriver(p, g, cfg, clk)
		if err := d.Run(context.Background(), 0); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rep := d.Report()
		if len(rg.casts) != 1 || rg.casts[0] != healer {
			t.Errorf("%s: casts %v", name, rg.casts)
		}
		if len(rep.Shortcuts) != 0 || strings.Contains(strings.Join(g.gms, "|"), ".revive") {
			t.Errorf("%s: the gate gave up mid-cast: %+v %q", name, rep.Shortcuts, g.gms)
		}
		for _, c := range g.chats {
			if strings.Contains(c, "release") || strings.Contains(c, "corpse run") {
				t.Errorf("%s: the member was told %q while its rez was in flight", name, c)
			}
		}
		if g.party[1].Dead {
			t.Errorf("%s: still dead", name)
		}
	}
}
