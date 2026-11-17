package objects

import (
	"math"
	"sort"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/testfixture"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

// clock is a settable store clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClockStore() (*Store, *clock) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	s := NewStore()
	s.SetClock(c.now)
	return s, c
}
func near(a, b float32) bool          { return math.Abs(float64(a-b)) < 1e-3 }
func at(u Unit, x, y, z float32) bool { return near(u.X, x) && near(u.Y, y) && near(u.Z, z) }
func facing(u Unit, o float64) bool   { return math.Abs(float64(u.O)-o) < 1e-4 }

// A unit on a spline is at the spline's current point, taken at an even speed over its
// duration (MoveSpline's CommonInitializer times each point by its distance), and faces
// along the leg it is on; once the duration has run it stands at the end.
func TestSplineCurrentPoint(t *testing.T) {
	s, c := newClockStore()
	s.Apply([]Block{createBlock(wolf, TypeUnit, 0, 0, nil)})
	c.add(time.Second)
	s.MoveSpline(MonsterMove{GUID: wolf, Start: [3]float32{0, 0, 0}, Dest: [3]float32{10, 10, 0}, Duration: 2000,
		Path: [][3]float32{{0, 0, 0}, {10, 0, 0}, {10, 10, 0}}})
	for _, w := range []struct {
		after   time.Duration
		x, y    float32
		o       float64
		moving  bool
		age     int64
		d2d, d3 float32
	}{
		{0, 0, 0, 0, true, 0, 0, 3},
		{500 * time.Millisecond, 5, 0, 0, true, 500, 5, float32(math.Sqrt(34))},
		{time.Second, 10, 0, 0, true, 1000, 10, float32(math.Sqrt(109))}, // the corner, still on the first leg
		{1500 * time.Millisecond, 10, 5, math.Pi / 2, true, 1500, float32(math.Sqrt(125)), float32(math.Sqrt(134))},
		{5 * time.Second, 10, 10, math.Pi / 2, false, 5000, float32(math.Sqrt(200)), float32(math.Sqrt(209))},
	} {
		at0 := c.t
		c.t = at0.Add(w.after)
		u, _ := s.Get(wolf, 0, 0, 3)
		c.t = at0
		if !at(u, w.x, w.y, 0) || !facing(u, w.o) || u.Moving != w.moving || u.PosAgeMS != w.age || !near(u.Distance2D, w.d2d) || !near(u.Distance, w.d3) {
			t.Fatalf("after %v: %+v", w.after, u)
		}
	}
	// A stop partway puts it where the stop says and keeps the facing it had there.
	c.add(1500 * time.Millisecond)
	s.MoveSpline(MonsterMove{GUID: wolf, Stop: true, Start: [3]float32{10, 5, 0}, Dest: [3]float32{10, 5, 0}})
	c.add(time.Second)
	if u, _ := s.Get(wolf, 0, 0, 0); !at(u, 10, 5, 0) || !facing(u, math.Pi/2) || u.Moving || u.PosAgeMS != 1000 {
		t.Fatalf("stop: %+v", u)
	}
	// A final angle is the facing at the end; a relay ends the spline at once.
	s.MoveSpline(MonsterMove{GUID: wolf, Start: [3]float32{10, 5, 0}, Dest: [3]float32{0, 5, 0}, Duration: 1000, FinalAngle: 1, HasFinalAngle: true})
	c.add(500 * time.Millisecond)
	if u, _ := s.Get(wolf, 0, 0, 0); !at(u, 5, 5, 0) || !facing(u, math.Pi) {
		t.Fatalf("halfway west: %+v", u)
	}
	c.add(time.Second)
	if u, _ := s.Get(wolf, 0, 0, 0); !at(u, 0, 5, 0) || !facing(u, 1) || u.Moving {
		t.Fatalf("final angle: %+v", u)
	}
	s.MoveSpline(MonsterMove{GUID: wolf, Start: [3]float32{0, 5, 0}, Dest: [3]float32{20, 5, 0}, Duration: 4000})
	c.add(time.Second)
	s.MoveTo(wolf, 7, 7, 7, 2)
	c.add(time.Second)
	if u, _ := s.Get(wolf, 0, 0, 0); !at(u, 7, 7, 7) || u.O != 2 || u.Moving || u.PosAgeMS != 1000 {
		t.Fatalf("relay: %+v", u)
	}
}

// A cyclic spline (a patrol loop) goes round and round.
func TestCyclicSpline(t *testing.T) {
	s, c := newClockStore()
	s.Apply([]Block{createBlock(wolf, TypeUnit, 0, 0, nil)})
	s.MoveSpline(MonsterMove{GUID: wolf, Flags: splineCyclic, Start: [3]float32{0, 0, 0}, Dest: [3]float32{0, 0, 0}, Duration: 2000,
		Path: [][3]float32{{0, 0, 0}, {10, 0, 0}, {0, 0, 0}}})
	c.add(5500 * time.Millisecond) // 2 laps and a quarter
	if u, _ := s.Get(wolf, 0, 0, 0); !at(u, 5, 0, 0) || !u.Moving {
		t.Fatalf("%+v", u)
	}
}

// A create block for a unit on a spline carries the route and how far along it the unit
// is (packet_builder.cpp:154-180), in the layout of InitCatmullRom for linear paths too:
// lerp(c0, c1, -1), c0..cN-1, cN-1 again (spline.cpp:49-55, :240-271). This is a
// two-point chase: c0 (0,0,0) to c1 (20,0,0).
func TestCreateBlockSplineCarriesOn(t *testing.T) {
	s, c := newClockStore()
	b := createBlock(wolf, TypeUnit, 10, 0, nil)
	b.Move.Spline = &Spline{TimePassed: 1000, Duration: 2000, Points: [][3]float32{{-20, 0, 0}, {0, 0, 0}, {20, 0, 0}, {20, 0, 0}}, Dest: [3]float32{20, 0, 0}}
	s.Apply([]Block{b})
	if u, _ := s.Get(wolf, 0, 0, 0); !at(u, 10, 0, 0) || !u.Moving || u.PosAgeMS != 0 {
		t.Fatalf("at receipt: %+v", u)
	}
	c.add(500 * time.Millisecond)
	if u, _ := s.Get(wolf, 0, 0, 0); !at(u, 15, 0, 0) || u.PosAgeMS != 500 {
		t.Fatalf("half a second on: %+v", u)
	}
	b.Move.Spline.Flags = splineDone // a finished spline is not followed
	s.Apply([]Block{b})
	c.add(500 * time.Millisecond)
	if u, _ := s.Get(wolf, 0, 0, 0); !at(u, 10, 0, 0) || u.Moving {
		t.Fatalf("done spline: %+v", u)
	}
}

// A unit known only from a values block has no position: age and distances are -1, it
// sorts last and a distance filter never matches it.
func TestUnknownPosition(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{{Type: BlockValues, GUID: 0x31, Values: map[uint16]uint32{UnitHealth: 1}}, createBlock(wolf, TypeUnit, 50, 0, nil)})
	us := s.Units(Filter{}, 0, 0, 0)
	if len(us) != 2 || us[0].GUID != wolf || us[1].PosAgeMS != -1 || us[1].Distance != -1 || us[1].Distance2D != -1 {
		t.Fatalf("%+v", us)
	}
	if us := s.Units(Filter{Near: 100}, 0, 0, 0); len(us) != 1 || us[0].GUID != wolf {
		t.Fatalf("near: %+v", us)
	}
}

// Replaying the server's monster moves from the captures: a new spline starts where the
// server has the unit at that moment (MoveSplineInit::Launch takes the running spline's
// ComputePosition, MoveSplineInit.cpp:63-84). Where that start is partway along the
// unit's last spline (it was cut short: a mob turning to chase, a pet called back), the
// store's current point must be there too. The m4-m6 runs hold some 100 such pairs, of
// mobs, pets and bots; 90 % are within 0.75 yd. Not counted are the moves that start at
// the old destination while the store still has the unit 1-1.7 s short of it: wandering
// and pathing mobs in those runs reach their ends that much before the duration the
// server sent, and the captures do not show why.
func TestCapturedSplinesPredictTheNextStart(t *testing.T) {
	var errs, destErrs []float64
	for _, name := range []string{"m4-world.twcap", "m5-attack.twcap", "m6-fight.twcap"} {
		st, c := newClockStore()
		move := func(body []byte) {
			m, err := ParseMonsterMove(body)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			st.mu.RLock()
			if o, ok := st.objs[m.GUID]; ok && o.track != nil {
				p, _, moving := o.pos(c.t)
				if d := dist3(o.track.pts[len(o.track.pts)-1], m.Start); moving && !m.Stop && d > 0.5 {
					errs = append(errs, dist3(p, m.Start))
					destErrs = append(destErrs, d)
				}
			}
			st.mu.RUnlock()
			st.MoveSpline(m)
		}
		for _, r := range testfixture.Load(t, name) {
			if r.Dir != worldconn.ServerToClient || r.Redacted {
				continue
			}
			c.t = r.Time
			switch r.Op {
			case opcode.SMSGUpdateObject, opcode.SMSGCompressedUpdateObject:
				body := r.Body
				if r.Op == opcode.SMSGCompressedUpdateObject {
					var err error
					if body, err = Inflate(body); err != nil {
						t.Fatal(err)
					}
				}
				b, err := ParseUpdate(body)
				if err != nil {
					t.Fatal(err)
				}
				st.Apply(b)
			case opcode.SMSGMonsterMove:
				move(r.Body)
			case opcode.SMSGCompressedMoves:
				subs, err := SplitCompressedMoves(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				for _, sp := range subs {
					if sp.Op == opcode.SMSGMonsterMove {
						move(sp.Body)
					}
				}
			}
		}
	}
	p90 := func(e []float64) float64 { sort.Float64s(e); return e[len(e)*9/10] }
	if len(errs) < 50 {
		t.Fatalf("only %d spline pairs in the captures", len(errs))
	}
	t.Logf("%d cut-short splines: 90th percentile %.2f yd off, the destination %.2f yd", len(errs), p90(errs), p90(destErrs))
	if e, d := p90(errs), p90(destErrs); e > 1 || d < 5 {
		t.Fatalf("90th percentile off by %.2f yd (the destination: %.2f yd) over %d splines", e, d, len(errs))
	}
}

// packXYZ is ByteBuffer::appendPackXYZ.
func packXYZ(x, y, z float32) uint32 {
	return uint32(int32(x/0.25))&0x7FF | (uint32(int32(y/0.25))&0x7FF)<<11 | (uint32(int32(z/0.25))&0x3FF)<<22
}

// WriteLinearPath writes the destination, then destination - point for the points before
// it as packed quarter yards (packet_builder.cpp:78-112); signs are kept.
func TestMonsterMoveLinearPathOffsets(t *testing.T) {
	w := packet.NewWriter().PackedGUID(wolf).F32(0).F32(0).F32(0).U32(9).U8(0).U32(0x100).U32(3000).U32(3).
		F32(10).F32(-10).F32(2).U32(packXYZ(10-4, -10-(-3), 2-1)).U32(packXYZ(10-12, -10-5.5, 2-(-1)))
	m, err := ParseMonsterMove(w.Bytes())
	want := [][3]float32{{0, 0, 0}, {4, -3, 1}, {12, 5.5, -1}, {10, -10, 2}}
	if err != nil || len(m.Path) != len(want) {
		t.Fatalf("%+v %v", m, err)
	}
	for i := range want {
		if m.Path[i] != want[i] {
			t.Fatalf("point %d: %v, want %v", i, m.Path[i], want[i])
		}
	}
	// No offsets (a two-point path): start and destination.
	if m, _ := ParseMonsterMove(linearMove(wolf)[:len(linearMove(wolf))-4]); len(m.Path) != 2 || m.Path[1] != [3]float32{11, 12, 13} {
		t.Fatalf("%+v", m.Path)
	}
	// An offset past its bits wraps (300 yd reads as -212); the point nearest the one
	// before it is taken.
	w = packet.NewWriter().PackedGUID(wolf).F32(0).F32(0).F32(0).U32(9).U8(0).U32(0x100).U32(3000).U32(3).
		F32(300).F32(0).F32(-200).U32(packXYZ(300-10, 0, -200-(-60))).U32(packXYZ(300-150, 0, -200-(-100)))
	want = [][3]float32{{0, 0, 0}, {10, 0, -60}, {150, 0, -100}, {300, 0, -200}}
	if m, _ := ParseMonsterMove(w.Bytes()); len(m.Path) != 4 || m.Path[1] != want[1] || m.Path[2] != want[2] {
		t.Fatalf("wrapped offsets: %+v", m.Path)
	}
}

// Every create block for a unit on a spline in the captures (167, linear paths of mobs,
// pets and bots) has the virtual first point lerp(c0, c1, -1) and the last point twice,
// and the store's current point at receipt is the block's own position.
func TestCapturedCreateSplines(t *testing.T) {
	n := 0
	for _, name := range []string{"m1-login.twcap", "m3-party.twcap", "m4-world.twcap", "m5-attack.twcap", "m6-fight.twcap"} {
		for _, r := range testfixture.Load(t, name) {
			if r.Dir != worldconn.ServerToClient || r.Redacted || (r.Op != opcode.SMSGUpdateObject && r.Op != opcode.SMSGCompressedUpdateObject) {
				continue
			}
			body := r.Body
			if r.Op == opcode.SMSGCompressedUpdateObject {
				var err error
				if body, err = Inflate(body); err != nil {
					t.Fatal(err)
				}
			}
			bs, err := ParseUpdate(body)
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range bs {
				if b.Move == nil || b.Move.Spline == nil || b.Move.Spline.Flags&splineDone != 0 {
					continue
				}
				p := b.Move.Spline.Points
				if len(p) < 3 {
					t.Fatalf("%s: spline of %d points", name, len(p))
				}
				virt := [3]float32{2*p[1][0] - p[2][0], 2*p[1][1] - p[2][1], 2*p[1][2] - p[2][2]}
				if dist3(p[0], virt) > 0.01 || p[len(p)-1] != p[len(p)-2] {
					t.Fatalf("%s: not the Catmull-Rom layout: %v", name, p)
				}
				st, _ := newClockStore()
				st.Apply([]Block{b})
				if u, _ := st.Get(b.GUID, 0, 0, 0); dist3([3]float32{u.X, u.Y, u.Z}, [3]float32{b.Move.X, b.Move.Y, b.Move.Z}) > 0.1 {
					t.Fatalf("%s: current point %v %v %v, block at %v %v %v (%+v)", name, u.X, u.Y, u.Z, b.Move.X, b.Move.Y, b.Move.Z, b.Move.Spline)
				}
				n++
			}
		}
	}
	if n < 100 {
		t.Fatalf("only %d create-block splines in the captures", n)
	}
}

// A path of more than 80 points goes out in parts; a later part of the same spline
// repeats the first point and carries the points after those sent, with the time left
// (Unit.cpp:11437-11442). The unit goes on from where it is, through the points of the
// earlier part still ahead.
func TestSplineLaterPart(t *testing.T) {
	s, c := newClockStore()
	s.Apply([]Block{createBlock(wolf, TypeUnit, 0, 0, nil)})
	s.MoveSpline(MonsterMove{GUID: wolf, ID: 7, Start: [3]float32{0, 0, 0}, Dest: [3]float32{20, 0, 0}, Duration: 2000,
		Path: [][3]float32{{0, 0, 0}, {10, 0, 0}, {20, 0, 0}}})
	c.add(500 * time.Millisecond) // at (5,0,0)
	s.MoveSpline(MonsterMove{GUID: wolf, ID: 7, Start: [3]float32{0, 0, 0}, Dest: [3]float32{20, 10, 0}, Duration: 2500,
		Path: [][3]float32{{0, 0, 0}, {20, 10, 0}}})
	for _, w := range []struct {
		after time.Duration
		x, y  float32
	}{{0, 5, 0}, {1000, 15, 0}, {2000, 20, 5}, {3000, 20, 10}} {
		at0 := c.t
		c.t = at0.Add(w.after * time.Millisecond)
		u, _ := s.Get(wolf, 0, 0, 0)
		c.t = at0
		if !at(u, w.x, w.y, 0) {
			t.Fatalf("after %d ms: %+v", w.after, u)
		}
	}
	// A new spline (another id) starts from its own first point.
	s.MoveSpline(MonsterMove{GUID: wolf, ID: 8, Start: [3]float32{5, 0, 0}, Dest: [3]float32{5, 10, 0}, Duration: 1000})
	c.add(500 * time.Millisecond)
	if u, _ := s.Get(wolf, 0, 0, 0); !at(u, 5, 5, 0) {
		t.Fatalf("new spline: %+v", u)
	}
}
