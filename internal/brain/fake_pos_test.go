package brain

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Spells and items of the step-3b tests (ids from internal/spells/meta.tsv.gz and the core's item_template).
const (
	spCounterspell = 2139
	spBlink        = 1953
	spIceBlock     = 11958
	spShoot        = 5019
	spLesserHeal   = 2050 // a heal: interruptible
	spShadowBolt   = 686  // damage: interruptible
	spPolymorph    = 118  // crowd control: mechanic 17
	spRainOfFire   = 5740 // a ground effect: persistent area aura with periodic damage
	spBlizzard     = 10
)

var mageSpells = []uint32{spFrostbolt, spFireBlast, spFrostNova, spCone, spFrostArmor, spIntellect, spCounterspell, spBlink, spIceBlock, spShoot}

// posFake is the fake game with the optional halves of the Game: a walker that moves the player, a navmesh whose walls the test
// draws, and ground effects.
type posFake struct {
	*fakeGame
	blocked func(a, b Vec3) bool // true when a wall stands between the two points
	offMesh func(p Vec3) bool    // true for a point that is not walkable
	walkErr error
	hold    chan struct{} // when set, WalkTo waits on it (or on the context)
	areas   []objects.DynamicObject

	walks    []Vec3
	rays     int
	snaps    int
	walkedAt []time.Time
}

func (f *posFake) WalkTo(ctx context.Context, x, y, z float32) error {
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.fakeGame.mu.Lock()
	defer f.fakeGame.mu.Unlock()
	if f.walkErr != nil {
		return f.walkErr
	}
	f.walks = append(f.walks, Vec3{x, y, z})
	f.walkedAt = append(f.walkedAt, f.clk.Now())
	f.self.X, f.self.Y, f.self.Z = x, y, z
	for i := range f.units {
		f.units[i] = f.place(f.units[i], f.units[i].X, f.units[i].Y)
	}
	return nil
}

func (f *posFake) LineClear(a, b Vec3) (bool, bool) {
	f.fakeGame.mu.Lock()
	f.rays++
	f.fakeGame.mu.Unlock()
	if f.offMesh != nil && f.offMesh(a) {
		return false, false
	}
	return f.blocked == nil || !f.blocked(a, b), true
}

func (f *posFake) SnapToMesh(p Vec3) (Vec3, bool) {
	f.fakeGame.mu.Lock()
	f.snaps++
	f.fakeGame.mu.Unlock()
	return p, f.offMesh == nil || !f.offMesh(p)
}

func (f *posFake) snapCount() int {
	f.fakeGame.mu.Lock()
	defer f.fakeGame.mu.Unlock()
	return f.snaps
}

func (f *posFake) DynamicObjects() []objects.DynamicObject {
	f.fakeGame.mu.Lock()
	defer f.fakeGame.mu.Unlock()
	out := append([]objects.DynamicObject{}, f.areas...)
	for i := range out {
		out[i].Distance = float32(math.Hypot(float64(out[i].X-f.self.X), float64(out[i].Y-f.self.Y)))
	}
	return out
}

func (f *posFake) walkList() []Vec3 {
	f.fakeGame.mu.Lock()
	defer f.fakeGame.mu.Unlock()
	return append([]Vec3{}, f.walks...)
}

func (f *posFake) rayCount() int {
	f.fakeGame.mu.Lock()
	defer f.fakeGame.mu.Unlock()
	return f.rays
}

// wallRect is a blocked region: any line that crosses the rectangle is blocked.
func wallRect(x0, y0, x1, y1 float32) func(a, b Vec3) bool {
	return func(a, b Vec3) bool {
		n := max(1, int(dist2(a, b)/0.25))
		for k := 0; k <= n; k++ {
			f := float32(k) / float32(n)
			x, y := a[0]+(b[0]-a[0])*f, a[1]+(b[1]-a[1])*f
			if x >= x0 && x <= x1 && y >= y0 && y <= y1 {
				return true
			}
		}
		return false
	}
}

// newPosRig is a brain on a posFake that knows the mage spells and is buffed.
func newPosRig(t *testing.T, p Params, known ...uint32) (*rig, *posFake) {
	t.Helper()
	if len(known) == 0 {
		known = mageSpells
	}
	g := newFake(known...)
	buffUp(g)
	pf := &posFake{fakeGame: g}
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	if p.Strategies == nil {
		p.Strategies = []string{"frost_mage"}
	}
	b, err := New(pf, cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, g: g, b: b}, pf
}

// tankFights puts a warrior tank at (tx, 0) and a mob at (mx, 0) that faces mobO, fought by the tank.
func tankFights(g *fakeGame, tx, mx, mobO float32) {
	g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	g.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 900, MaxHealth: 900, Target: mobA}, tx, 0)
	m := mob(mobA, tankG)
	m.O = mobO
	g.addUnit(m, mx, 0)
}

func ms(v int64) *int64 { return &v }

func here(g *fakeGame) Vec3 { return Vec3{g.self.X, g.self.Y, g.self.Z} }

func unitXY(g *fakeGame, id packet.GUID) Vec3 {
	for _, u := range g.units {
		if u.GUID == id {
			return Vec3{u.X, u.Y, u.Z}
		}
	}
	return Vec3{}
}
