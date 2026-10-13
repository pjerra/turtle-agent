package session

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// fakePlanner answers each Plan with the next scripted reply and records what it was asked.
type fakePlanner struct {
	mu      sync.Mutex
	replies []func(from, to navmesh.Vec3) navmesh.Path
	calls   []navmesh.Vec3 // from of each call
	err     error
}

func (f *fakePlanner) Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, from)
	if f.err != nil {
		return navmesh.Path{}, f.err
	}
	r := f.replies[min(len(f.calls), len(f.replies))-1]
	return r(from, to), nil
}

func (f *fakePlanner) froms() []navmesh.Vec3 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]navmesh.Vec3(nil), f.calls...)
}

// via plans start -> corner -> goal, where corner is the goal's x at the start's y.
func via(from, to navmesh.Vec3) navmesh.Path {
	corner := navmesh.Vec3{to[0], from[1], from[2]}
	return navmesh.Path{Status: navmesh.Complete, Engine: "fake", Points: []navmesh.Vec3{from, corner, to},
		Ground: make([][]navmesh.Vec3, 2)}
}

func straight(from, to navmesh.Vec3) navmesh.Path {
	return navmesh.Path{Status: navmesh.Complete, Engine: "fake", Points: []navmesh.Vec3{from, to}, Ground: make([][]navmesh.Vec3, 1)}
}

func walkHarness(t *testing.T, f *fakePlanner) *harness {
	t.Helper()
	h := start(t, Options{Planner: f})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, selfCreate(20)) // 20 yd/s keeps the walks short
	waitFor(t, func() bool { return h.s.Status().MaxHealth == 100 })
	return h
}

func moveInfo(p []byte) game.MoveInfo { return game.ReadMoveInfo(packet.NewReader(p)) }

func kinds(r WalkReport) string {
	var ks []string
	for _, s := range r.Trace {
		ks = append(ks, s.Kind)
	}
	return strings.Join(ks, ",")
}

func TestWalkPathTurnsAtTheCornerAndArrives(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{via}}
	h := walkHarness(t, f)
	goal := navmesh.Vec3{me.X + 4, me.Y + 3, me.Z}
	done := make(chan WalkReport, 1)
	go func() {
		r, err := h.s.WalkPath(context.Background(), goal[0], goal[1], goal[2])
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	// Leg 1 faces east (along +x, orientation 0), leg 2 north (+y, pi/2): one start, a
	// facing on the second leg with Forward still set, one stop at the goal.
	if mi := moveInfo(h.expect(opcode.MSGMoveStartForward).Body); mi.X != me.X || math.Abs(float64(mi.O)) > 1e-3 {
		t.Fatalf("first start %+v", mi)
	}
	mark := len(h.srv.Skipped())
	if mi := moveInfo(h.expect(opcode.MSGMoveSetFacing).Body); mi.Flags&game.MoveForward == 0 || mi.X != me.X+4 ||
		mi.Y < me.Y || math.Abs(float64(mi.O)-math.Pi/2) > 1e-3 {
		t.Fatalf("facing at the corner %+v", mi)
	}
	if mi := moveInfo(h.expect(opcode.MSGMoveStop).Body); mi.X != goal[0] || mi.Y != goal[1] || math.Abs(float64(mi.O)-math.Pi/2) > 1e-3 {
		t.Fatalf("stop at the goal %+v", mi)
	}
	for _, p := range h.srv.Skipped()[mark:] {
		if p.Op == opcode.MSGMoveStop || p.Op == opcode.MSGMoveStartForward {
			t.Fatalf("%s at the corner", opcode.Name(p.Op))
		}
	}
	r := <-done
	if r.Result != "arrived" || r.DistLeft > 0.01 || r.Replans != 0 || math.Abs(float64(r.Walked-7)) > 0.01 ||
		kinds(r) != "plan,walk" || r.Trace[0].Status != "complete" || len(r.Trace[0].Points) != 3 {
		t.Fatalf("%+v", r)
	}
	if st := h.s.Status(); st.X != goal[0] || st.Y != goal[1] {
		t.Fatalf("status %+v", st)
	}
	h.logout()
}

// A path with four corners is run as a client runs it: one MSG_MOVE_START_FORWARD, a
// MSG_MOVE_SET_FACING at each corner with Forward still set and the position on the path,
// heartbeats in between, one MSG_MOVE_STOP at the goal. Every packet keeps within the
// anticheat's bound after the one before, and no gap reaches its 1000 ms limit.
func TestWalkPathRunsThroughCorners(t *testing.T) {
	zig := func(from, to navmesh.Vec3) navmesh.Path {
		pts := []navmesh.Vec3{from, {from[0] + 6, from[1], from[2]}, {from[0] + 6, from[1] + 5, from[2] + 1},
			{from[0] + 1, from[1] + 9, from[2] + 1}, {from[0] + 1, from[1] + 13, from[2]}, to}
		return navmesh.Path{Status: navmesh.Complete, Engine: "fake", Points: pts, Ground: make([][]navmesh.Vec3, len(pts)-1)}
	}
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{zig}}
	h := start(t, Options{Planner: f})
	h.enterWorld() // 7 yd/s: 26 yd in about 3.8 s
	mark := len(h.srv.Skipped())
	goal := navmesh.Vec3{me.X + 5, me.Y + 14, me.Z}
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), goal[0], goal[1], goal[2]); done <- r }()
	stop, err := h.srv.Expect(opcode.MSGMoveStop, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.Result != "arrived" || kinds(r) != "plan,walk" {
		t.Fatalf("%+v", r)
	}
	path := zig(navmesh.Vec3{me.X, me.Y, me.Z}, goal).Points
	pts := make([][3]float32, len(path))
	copy(pts, path)
	type move struct {
		op uint16
		mi game.MoveInfo
	}
	var moves []move
	for _, p := range append(h.srv.Skipped()[mark:], stop) {
		switch p.Op {
		case opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveSetFacing, opcode.MSGMoveStop:
			moves = append(moves, move{p.Op, moveInfo(p.Body)})
		}
	}
	count := map[uint16]int{}
	leg := 0
	for i, m := range moves {
		count[m.op]++
		if (m.op == opcode.MSGMoveStartForward) != (i == 0) || (m.op == opcode.MSGMoveStop) != (i == len(moves)-1) {
			t.Fatalf("packet %d is %s", i, opcode.Name(m.op))
		}
		if m.op != opcode.MSGMoveStop && m.mi.Flags&game.MoveForward == 0 {
			t.Fatalf("packet %d (%s) without MoveForward: %+v", i, opcode.Name(m.op), m.mi)
		}
		p := navmesh.Vec3{m.mi.X, m.mi.Y, m.mi.Z}
		at := along(pts, p)
		if q := pointAlong(pts, at); dist3(p, q) > 0.05 {
			t.Fatalf("packet %d (%s) at %v is off the path", i, opcode.Name(m.op), p)
		}
		if m.op == opcode.MSGMoveSetFacing {
			leg++
			a, b := pts[leg], pts[leg+1]
			if want := game.Facing(a[0], a[1], b[0], b[1]); math.Abs(float64(m.mi.O-want)) > 1e-3 {
				t.Fatalf("facing %d is %.3f, want %.3f along leg %d", count[m.op], m.mi.O, want, leg)
			}
		}
		if i == 0 {
			continue
		}
		prev := moves[i-1].mi
		dt := float64(m.mi.Time-prev.Time) / 1000
		if dt > 0.75 { // the 500 ms beat read on the session's 100 ms tick, plus slack for -race; the bound is 1 s
			t.Fatalf("packet %d (%s) %.3f s after the one before", i, opcode.Name(m.op), dt)
		}
		if dxy := math.Hypot(float64(m.mi.X-prev.X), float64(m.mi.Y-prev.Y)); dxy > 7*dt+stepMargin {
			t.Fatalf("packet %d (%s): %.2f yd in %.3f s", i, opcode.Name(m.op), dxy, dt)
		}
		if dz := math.Abs(float64(m.mi.Z - prev.Z)); dz > 7*dt+stepMargin {
			t.Fatalf("packet %d (%s): z %.2f in %.3f s", i, opcode.Name(m.op), dz, dt)
		}
	}
	if count[opcode.MSGMoveStartForward] != 1 || count[opcode.MSGMoveStop] != 1 || count[opcode.MSGMoveSetFacing] != 4 ||
		count[opcode.MSGMoveHeartbeat] < 3 {
		t.Fatalf("packets %v", count)
	}
	if last := moves[len(moves)-1].mi; last.X != goal[0] || last.Y != goal[1] {
		t.Fatalf("stopped at %+v", last)
	}
	if math.Abs(float64(r.Walked)-float64(along(pts, goal))) > 0.01 {
		t.Fatalf("walked %.2f", r.Walked)
	}
	h.logout()
}

// pointAlong is the point d yards along the polyline pts.
func pointAlong(pts [][3]float32, d float32) navmesh.Vec3 {
	w := newWalk(pts, 1, time.Time{}, nil)
	w.base = float64(d)
	return w.at(time.Time{})
}

// along is how far along the polyline pts the point nearest p is (3D yards from pts[0]).
func along(pts [][3]float32, p navmesh.Vec3) float32 {
	best, at, cum := math.Inf(1), 0.0, 0.0
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		seg := dist3f(a, b)
		t := 0.0
		if seg > 1e-9 {
			for k := range 3 {
				t += float64(p[k]-a[k]) * float64(b[k]-a[k])
			}
			t = min(max(t/(seg*seg), 0), 1)
		}
		q := [3]float32{a[0] + (b[0]-a[0])*float32(t), a[1] + (b[1]-a[1])*float32(t), a[2] + (b[2]-a[2])*float32(t)}
		if d := dist3f(q, p); d < best {
			best, at = d, cum+t*seg
		}
		cum += seg
	}
	return float32(at)
}

// legFacing is the facing of the leg of pts that d yards along the path is on.
func legFacing(pts [][3]float32, d float32) float32 {
	var cum float32
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		cum += float32(dist3f(a, b))
		if d <= cum || i == len(pts)-1 {
			return game.Facing(a[0], a[1], b[0], b[1])
		}
	}
	return 0
}

// Turns under minTurn are not sent one by one, but they add up: the facing a heartbeat carries
// stays within minTurn of the leg it is on, so the walk sends a facing once they reach it.
func TestWalkPathKeepsTheFacingWithinATurnOfTheLeg(t *testing.T) {
	// 16 chords of 2 yd, each turning 0.005 rad: 0.075 rad in all, no turn of its own over minTurn
	arc := func(from, to navmesh.Vec3) navmesh.Path {
		pts := []navmesh.Vec3{from}
		x, y := float64(from[0]), float64(from[1])
		for k := range 16 {
			a := float64(k) * 0.005
			x, y = x+2*math.Cos(a), y+2*math.Sin(a)
			pts = append(pts, navmesh.Vec3{float32(x), float32(y), from[2]})
		}
		return navmesh.Path{Status: navmesh.Complete, Engine: "fake", Points: pts, Ground: make([][]navmesh.Vec3, len(pts)-1)}
	}
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{arc}}
	h := walkHarness(t, f) // 20 yd/s
	mark := len(h.srv.Skipped())
	goal := navmesh.Vec3{me.X + 30, me.Y + 2, me.Z}
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), goal[0], goal[1], goal[2]); done <- r }()
	stop, err := h.srv.Expect(opcode.MSGMoveStop, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Result != "arrived" || kinds(r) != "plan,walk" {
		t.Fatalf("%+v", r)
	}
	pts := arc(navmesh.Vec3{me.X, me.Y, me.Z}, goal).Points
	var moves []worldconn.Packet
	for _, p := range append(h.srv.Skipped()[mark:], stop) {
		switch p.Op {
		case opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveSetFacing, opcode.MSGMoveStop:
			moves = append(moves, p)
		}
	}
	facings := 0
	for i, p := range moves {
		mi := moveInfo(p.Body)
		if p.Op == opcode.MSGMoveSetFacing {
			facings++
		}
		if want := legFacing(pts, along(pts, navmesh.Vec3{mi.X, mi.Y, mi.Z})); math.Abs(float64(mi.O-want)) > minTurn+0.002 {
			t.Fatalf("packet %d (%s) faces %.3f, its leg faces %.3f", i, opcode.Name(p.Op), mi.O, want)
		}
	}
	if facings == 0 {
		t.Fatal("no facing sent for 0.075 rad of turns")
	}
	h.logout()
}

// The planner's ground samples set the heartbeat heights between corners.
func TestWalkPathKeepsToTheGround(t *testing.T) {
	hill := func(from, to navmesh.Vec3) navmesh.Path {
		p := straight(from, to)
		p.Ground = [][]navmesh.Vec3{{{from[0] + 4, from[1], from[2] + 4}, {from[0] + 8, from[1], from[2] + 4}}}
		return p
	}
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{hill}}
	h := start(t, Options{Planner: f})
	h.enterWorld() // the default 7 yd/s: heartbeats at 3.5 and 7 yd of the 12
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), me.X+12, me.Y, me.Z); done <- r }()
	h.expect(opcode.MSGMoveStartForward)
	beat := moveInfo(h.expect(opcode.MSGMoveHeartbeat).Body)
	want := min(beat.X-me.X, 4) // up the 1:1 slope to the first sample at 4 yd, then level
	if math.Abs(float64(beat.Z-me.Z-want)) > 0.3 {
		t.Fatalf("heartbeat at +%.2f yd has z %+.2f, want %+.2f", beat.X-me.X, beat.Z-me.Z, want)
	}
	if mi := moveInfo(h.expect(opcode.MSGMoveStop).Body); mi.Z != me.Z {
		t.Fatalf("stop z %v", mi.Z)
	}
	if r := <-done; r.Result != "arrived" {
		t.Fatalf("%+v", r)
	}
	h.logout()
}

// A server move (fear) interrupts the walk: walk_path waits for the spline to end, then
// plans again from where the server put the player.
func TestWalkPathReplansAfterAServerMove(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{straight}}
	h := start(t, Options{Planner: f})
	h.enterWorld() // 7 yd/s: the first leg takes over a second
	goal := navmesh.Vec3{me.X + 8, me.Y, me.Z}
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), goal[0], goal[1], goal[2]); done <- r }()
	h.expect(opcode.MSGMoveStartForward)
	fear := packet.NewWriter().PackedGUID(me.GUID).F32(me.X).F32(me.Y).F32(me.Z).U32(1).U8(0).U32(0x100).U32(300).U32(1).
		F32(me.X + 1).F32(me.Y + 2).F32(me.Z).Bytes()
	h.send(opcode.SMSGMonsterMove, fear)
	start := moveInfo(h.expect(opcode.MSGMoveStartForward).Body)
	if start.X != me.X+1 || start.Y != me.Y+2 {
		t.Fatalf("second leg starts at %+v, not where the server put the player", start)
	}
	h.expect(opcode.MSGMoveStop)
	r := <-done
	if r.Result != "arrived" || r.Replans != 1 || kinds(r) != "plan,walk,interrupted,plan,walk" {
		t.Fatalf("%+v", r)
	}
	if froms := f.froms(); len(froms) != 2 || froms[1] != (navmesh.Vec3{me.X + 1, me.Y + 2, me.Z}) {
		t.Fatalf("planned from %v", froms)
	}
	if r.Trace[2].Text != "moved by the server" || r.Trace[3].TMS < 300 {
		t.Fatalf("trace %+v", r.Trace)
	}
	// Walked counts the first leg up to the fear (a few tenths of a yard at most) and the second leg
	// from where the server put the player, not the 2.2 yd jump itself.
	if second := math.Hypot(7, 2); float64(r.Walked) < second-0.01 || float64(r.Walked) > second+1 {
		t.Fatalf("walked %.2f, want about %.2f", r.Walked, second)
	}
	h.logout()
}

func TestWalkPathNoPathDoesNotMove(t *testing.T) {
	none := func(from, to navmesh.Vec3) navmesh.Path {
		return navmesh.Path{Status: navmesh.NoPath, Reason: "goal (1.0, 2.0, 3.0) is not on the navmesh", Points: []navmesh.Vec3{}}
	}
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{none}}
	h := walkHarness(t, f)
	r, err := h.s.WalkPath(context.Background(), 1, 2, 3)
	if err != nil || r.Result != "no_path" || !strings.Contains(r.Reason, "not on the navmesh") || r.Walked != 0 || kinds(r) != "plan" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := h.srv.Expect(opcode.MSGMoveStartForward, 200*time.Millisecond); err == nil {
		t.Fatal("moved without a path")
	}
	h.logout()
}

func TestWalkPathPartialWalksToTheNearestPoint(t *testing.T) {
	part := func(from, to navmesh.Vec3) navmesh.Path {
		end := navmesh.Vec3{from[0] + 3, from[1], from[2]}
		return navmesh.Path{Status: navmesh.Partial, Reason: "no navmesh route to the goal", Points: []navmesh.Vec3{from, end},
			Ground: make([][]navmesh.Vec3, 1)}
	}
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{part}}
	h := walkHarness(t, f)
	r, err := h.s.WalkPath(context.Background(), me.X+10, me.Y, me.Z)
	if err != nil || r.Result != "partial" || r.Reason != "no navmesh route to the goal" || math.Abs(float64(r.DistLeft-7)) > 0.01 {
		t.Fatalf("%+v %v", r, err)
	}
	h.logout()
}

func TestWalkPathStopsOnAnotherMap(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{straight}}
	h := start(t, Options{Planner: f})
	h.enterWorld()
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), me.X+8, me.Y, me.Z); done <- r }()
	h.expect(opcode.MSGMoveStartForward)
	h.send(opcode.SMSGNewWorld, packet.NewWriter().U32(1).F32(10).F32(20).F32(30).F32(0).Bytes())
	r := <-done
	if r.Result != "partial" || !strings.Contains(r.Reason, "another map") || len(f.froms()) != 1 {
		t.Fatalf("%+v", r)
	}
	h.logout()
}

func TestPathToAndNoPlanner(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{via}}
	h := walkHarness(t, f)
	r, err := h.s.PathTo(context.Background(), me.X+4, me.Y+3, me.Z)
	if err != nil || r.Status != navmesh.Complete || len(r.Points) != 3 || r.Map != me.Map || r.From != (navmesh.Vec3{me.X, me.Y, me.Z}) {
		t.Fatalf("%+v %v", r, err)
	}
	f.err = errors.New("navmesh: no 000.mmap")
	if _, err := h.s.PathTo(context.Background(), 1, 2, 3); err == nil || !strings.Contains(err.Error(), "000.mmap") {
		t.Fatalf("planner error: %v", err)
	}
	h.logout()

	h2 := start(t, Options{})
	h2.enterWorld()
	if _, err := h2.s.PathTo(context.Background(), 1, 2, 3); !errors.Is(err, ErrNoNavmesh) {
		t.Fatalf("path_to: %v", err)
	}
	if _, err := h2.s.WalkPath(context.Background(), 1, 2, 3); !errors.Is(err, ErrNoNavmesh) {
		t.Fatalf("walk_path: %v", err)
	}
	h2.logout()
}

// collectMoves gathers the player's movement packets (start, heartbeats, stop) until the
// walk ends.
func collectMoves(t *testing.T, h *harness, mark int, stop worldconn.Packet) []game.MoveInfo {
	t.Helper()
	var out []game.MoveInfo
	for _, p := range h.srv.Skipped()[mark:] {
		switch p.Op {
		case opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat:
			out = append(out, moveInfo(p.Body))
		}
	}
	return append(out, moveInfo(stop.Body))
}

// A leg that runs 8 yd level, then climbs a 50° slope (9.53 yd up over 8 yd): every beat
// keeps its height change within the run speed times its client-time gap, the most the
// anticheat allows (plus its 0.5 yd margin, which the walk does not need).
func TestWalkPathClimbsA50DegreeSlopeWithinTheZSpeed(t *testing.T) {
	rise := float32(8 * math.Tan(50*math.Pi/180))
	slope := func(from, to navmesh.Vec3) navmesh.Path {
		p := straight(from, to)
		p.Ground = [][]navmesh.Vec3{{{from[0] + 4, from[1], from[2]}, {from[0] + 8, from[1], from[2]},
			{from[0] + 12, from[1], from[2] + rise/2}}}
		return p
	}
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{slope}}
	h := start(t, Options{Planner: f})
	h.enterWorld() // 7 yd/s
	mark := len(h.srv.Skipped())
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), me.X+16, me.Y, me.Z+rise); done <- r }()
	stop, err := h.srv.Expect(opcode.MSGMoveStop, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Result != "arrived" {
		t.Fatalf("%+v", r)
	}
	moves := collectMoves(t, h, mark, stop)
	if len(moves) < 5 {
		t.Fatalf("%d packets", len(moves))
	}
	for i := 1; i < len(moves); i++ {
		a, b := moves[i-1], moves[i]
		dt := float64(b.Time-a.Time) / 1000
		dz := math.Abs(float64(b.Z - a.Z))
		if dz > 7*dt+0.05 {
			t.Fatalf("beat %d: z %+.2f in %.3f s, over 7 yd/s (%+v -> %+v)", i, b.Z-a.Z, dt, a, b)
		}
	}
	if last := moves[len(moves)-1]; last.Z != me.Z+rise || last.X != me.X+16 {
		t.Fatalf("stopped at %+v", last)
	}
	h.logout()
}

// A ground sample that snapped to a floor 6 yd up between two beats cannot lift a packet
// more than speed*dt above the last one sent.
func TestClampZHoldsTheZSpeed(t *testing.T) {
	last := lastMove{pos: [3]float32{0, 0, 0}, ctime: 1000}
	for _, c := range []struct {
		z     float32
		ctime uint32
		want  float32
	}{{6, 1500, 3.5}, {-6, 1500, -3.5}, {1, 1500, 1}, {6, 900, 0}} {
		if z := clampZ(c.z, last, c.ctime, 7); z != c.want {
			t.Errorf("z %v at %d: %v, want %v", c.z, c.ctime, z, c.want)
		}
	}
}

// The ninth server move ends the walk: walk_path plans again up to maxReplans times.
func TestWalkPathGivesUpAfterMaxReplans(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{straight}}
	h := start(t, Options{Planner: f})
	h.enterWorld()
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), me.X+30, me.Y, me.Z); done <- r }()
	for range maxReplans + 1 {
		h.expect(opcode.MSGMoveStartForward)
		push := packet.NewWriter().PackedGUID(me.GUID).F32(me.X).F32(me.Y).F32(me.Z).U32(1).U8(0).U32(0x100).U32(10).U32(1).
			F32(me.X).F32(me.Y).F32(me.Z).Bytes()
		h.send(opcode.SMSGMonsterMove, push)
	}
	r := <-done
	if r.Result != "partial" || r.Replans != maxReplans || !strings.Contains(r.Reason, "9 times") || len(f.froms()) != maxReplans+1 {
		t.Fatalf("%+v (%d plans)", r, len(f.froms()))
	}
	h.logout()
}

// A root stops the leg; walk_path waits for the unroot, plans again and arrives.
func TestWalkPathWaitsOutARoot(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{straight}}
	h := start(t, Options{Planner: f})
	h.enterWorld()
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), me.X+5, me.Y, me.Z); done <- r }()
	h.expect(opcode.MSGMoveStartForward)
	h.send(opcode.SMSGForceMoveRoot, packet.NewWriter().PackedGUID(me.GUID).U32(5).Bytes())
	h.expect(opcode.MSGMoveStop)
	h.expect(opcode.CMSGForceMoveRootAck)
	time.Sleep(300 * time.Millisecond)
	if _, err := h.srv.Expect(opcode.MSGMoveStartForward, 10*time.Millisecond); err == nil {
		t.Fatal("walked on while rooted")
	}
	h.send(opcode.SMSGForceMoveUnroot, packet.NewWriter().PackedGUID(me.GUID).U32(6).Bytes())
	h.expect(opcode.MSGMoveStartForward)
	h.expect(opcode.MSGMoveStop)
	r := <-done
	if r.Result != "arrived" || r.Replans != 1 || kinds(r) != "plan,walk,interrupted,plan,walk" ||
		r.Trace[2].Text != "rooted by the server; walk stopped" || r.Trace[3].TMS < 300 {
		t.Fatalf("%+v", r)
	}
	h.logout()
}

// A root that outlasts rootWait ends the walk as partial.
func TestWalkPathRootOutlastsTheWait(t *testing.T) {
	old := rootWait
	rootWait = 300 * time.Millisecond
	t.Cleanup(func() { rootWait = old })
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{straight}}
	h := start(t, Options{Planner: f})
	h.enterWorld()
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), me.X+5, me.Y, me.Z); done <- r }()
	h.expect(opcode.MSGMoveStartForward)
	h.send(opcode.SMSGForceMoveRoot, packet.NewWriter().PackedGUID(me.GUID).U32(5).Bytes())
	select {
	case r := <-done:
		if r.Result != "partial" || !strings.Contains(r.Reason, "still rooted") {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("walk_path still waiting")
	}
	h.logout()
}

// A root after the first corner of a two-leg path: walked counts the first leg and the part of
// the second walked before the root, along the path, not the straight line from the start.
// The root is sent when a heartbeat shows the walk well up the second leg (not after a sleep), and
// the second leg is long enough that the walk is still going when the root reaches it, so a slow
// machine cannot make the walk arrive first or the root come before the corner.
func TestWalkPathCountsAlongThePathWhenRootedAfterACorner(t *testing.T) {
	old := rootWait
	rootWait = 300 * time.Millisecond
	t.Cleanup(func() { rootWait = old })
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{via}}
	h := start(t, Options{Planner: f})
	h.enterWorld()                                  // 7 yd/s
	goal := navmesh.Vec3{me.X + 7, me.Y + 40, me.Z} // 7 yd east, then 40 yd north: a walk of 6.7 s
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), goal[0], goal[1], goal[2]); done <- r }()
	h.expect(opcode.MSGMoveStartForward)
	h.expect(opcode.MSGMoveSetFacing) // the corner
	if beat := moveInfo(h.expect(opcode.MSGMoveHeartbeat).Body); beat.Y <= me.Y+1 {
		t.Fatalf("heartbeat %+v is not up the second leg", beat)
	}
	h.send(opcode.SMSGForceMoveRoot, packet.NewWriter().PackedGUID(me.GUID).U32(5).Bytes())
	stop := moveInfo(h.expect(opcode.MSGMoveStop).Body)
	h.expect(opcode.CMSGForceMoveRootAck)
	r := <-done
	e := float64(stop.Y - me.Y) // how far up the second leg the root caught the walk
	if r.Result != "partial" || !strings.Contains(r.Reason, "still rooted") || kinds(r) != "plan,walk,interrupted" ||
		stop.X != goal[0] || e < 1 {
		t.Fatalf("%+v, stopped at %+v", r, stop)
	}
	if want := 7 + e; math.Abs(float64(r.Walked)-want) > 0.01 {
		t.Fatalf("walked %.2f, want %.2f along the path (the straight line is %.2f)", r.Walked, want, math.Hypot(7, e))
	}
	h.logout()
}

// A path that doubles back and ends on its first leg: walked counts the whole way, 10 yd out and
// 4 back, not the 6 yd from the start to the goal along the first leg.
func TestWalkPathCountsAPathThatDoublesBack(t *testing.T) {
	back := func(from, to navmesh.Vec3) navmesh.Path {
		pts := []navmesh.Vec3{from, {from[0] + 10, from[1], from[2]}, {from[0] + 6, from[1], from[2]}}
		return navmesh.Path{Status: navmesh.Complete, Engine: "fake", Points: pts, Ground: make([][]navmesh.Vec3, 2)}
	}
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{back}}
	h := walkHarness(t, f) // 20 yd/s
	r, err := h.s.WalkPath(context.Background(), me.X+6, me.Y, me.Z)
	if err != nil || r.Result != "arrived" || kinds(r) != "plan,walk" || math.Abs(float64(r.Walked)-14) > 0.01 {
		t.Fatalf("%+v %v", r, err)
	}
	h.logout()
}

// setCheckSpeed sets the run speed checkStep holds each step to and leaves the walk's own
// speed alone: the walk then steps faster than the bound allows, and the check refuses its
// next packet.
func setCheckSpeed(t *testing.T, h *harness, speed float32) {
	t.Helper()
	if _, err := h.s.do(context.Background(), func() (any, error) {
		h.s.runSpeed = speed
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The check refuses a heartbeat: after the first one the bound drops to 3.5 yd/s while the walk
// still moves at 7, so the next 3.5 yd step is over the 2 yd allowed in 0.5 s. The walk stops
// where the server last had the player, at that first heartbeat, and walked counts the way there.
func TestWalkPathCountsToTheLastHeartbeatWhenTheCheckRefuses(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{straight}}
	h := start(t, Options{Planner: f})
	h.enterWorld() // 7 yd/s: a heartbeat every 500 ms, 3.5 yd apart
	var r WalkReport
	var err error
	done := make(chan struct{})
	go func() {
		r, err = h.s.WalkPath(context.Background(), me.X+30, me.Y, me.Z)
		close(done)
	}()
	h.expect(opcode.MSGMoveStartForward)
	beat := moveInfo(h.expect(opcode.MSGMoveHeartbeat).Body)
	setCheckSpeed(t, h, 3.5)
	stop := moveInfo(h.expect(opcode.MSGMoveStop).Body)
	<-done
	if err == nil || !strings.Contains(err.Error(), "walk stopped") || r.Result != "partial" || kinds(r) != "plan,walk" {
		t.Fatalf("%+v %v", r, err)
	}
	if stop.X != beat.X || stop.Y != beat.Y {
		t.Fatalf("stopped at %+v, not at the last heartbeat %+v", stop, beat)
	}
	if want := beat.X - me.X; math.Abs(float64(r.Walked-want)) > 0.01 {
		t.Fatalf("walked %.2f, want %.2f to the last heartbeat", r.Walked, want)
	}
	h.logout()
}

// The check refuses the arrival: the bound drops to 3.5 yd/s just after the start of a 3 yd walk
// at 7 yd/s, which arrives 0.43 s in, before the first heartbeat at 0.5 s. The stop at the goal is
// the step refused, so the walk stops where the server last had the player, at the start, and
// walked counts none of the path.
func TestWalkPathDoesNotCountAnArrivalTheCheckRefuses(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{straight}}
	h := start(t, Options{Planner: f})
	h.enterWorld() // 7 yd/s
	var r WalkReport
	var err error
	done := make(chan struct{})
	go func() {
		r, err = h.s.WalkPath(context.Background(), me.X+3, me.Y, me.Z)
		close(done)
	}()
	h.expect(opcode.MSGMoveStartForward)
	setCheckSpeed(t, h, 3.5)
	stop := moveInfo(h.expect(opcode.MSGMoveStop).Body)
	<-done
	if err == nil || !strings.Contains(err.Error(), "walk stopped") || r.Result != "partial" {
		t.Fatalf("%+v %v", r, err)
	}
	if stop.X != me.X || stop.Y != me.Y {
		t.Fatalf("stopped at %+v, not where the server had the player", stop)
	}
	if r.Walked > 0.01 {
		t.Fatalf("walked %.2f counts the refused arrival: no packet past the start was accepted", r.Walked)
	}
	h.logout()
}

// A partial path that gained ground is planned on from its end (the tiles round the new
// start may reach further); one that gained under 10 yd ends the walk.
func TestWalkPathPlansOnFromAPartialEnd(t *testing.T) {
	part := func(from, to navmesh.Vec3) navmesh.Path {
		end := navmesh.Vec3{from[0] + 12, from[1], from[2]}
		return navmesh.Path{Status: navmesh.Partial, Reason: "edge of the loaded tiles", Points: []navmesh.Vec3{from, end},
			Ground: make([][]navmesh.Vec3, 1)}
	}
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{part, straight}}
	h := walkHarness(t, f)
	r, err := h.s.WalkPath(context.Background(), me.X+20, me.Y, me.Z)
	if err != nil || r.Result != "arrived" || r.Replans != 0 || kinds(r) != "plan,walk,plan,walk" {
		t.Fatalf("%+v %v", r, err)
	}

	// 12 yd, then 4: the second partial gained under 10 yd, so the walk ends there.
	short := func(from, to navmesh.Vec3) navmesh.Path {
		p := part(from, to)
		p.Points[1][0] = from[0] + 4
		return p
	}
	f.mu.Lock()
	f.replies, f.calls = []func(from, to navmesh.Vec3) navmesh.Path{part, short}, nil
	f.mu.Unlock()
	x, _, _, _ := h.s.selfPos()
	r, err = h.s.WalkPath(context.Background(), x+30, me.Y, me.Z)
	if err != nil || r.Result != "partial" || r.Reason != "edge of the loaded tiles" || kinds(r) != "plan,walk,plan,walk" ||
		math.Abs(float64(r.Walked-16)) > 0.01 {
		t.Fatalf("%+v %v", r, err)
	}
	h.logout()
}

// A knockback mid-leg: walk_path waits for the landing, plans again from where the player
// came down and walks on; arriving on the mesh floor settles the landing's z_unknown.
func TestWalkPathReplansAfterAKnockback(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{straight}}
	h := start(t, Options{Planner: f})
	h.enterWorld()
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), me.X+8, me.Y, me.Z); done <- r }()
	h.expect(opcode.MSGMoveStartForward)
	time.Sleep(200 * time.Millisecond)
	h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 3, 0, 1, 5, -4)) // north, 5 yd/s, up 4
	h.expect(opcode.CMSGMoveKnockBackAck)
	h.expect(opcode.MSGMoveFallLand)
	if !h.s.Status().ZUnknown {
		t.Fatal("the landing's z is a guess")
	}
	h.expect(opcode.MSGMoveStartForward)
	h.expect(opcode.MSGMoveStop)
	r := <-done
	if r.Result != "arrived" || r.Replans != 1 || kinds(r) != "plan,walk,interrupted,plan,walk" ||
		r.Trace[2].Text != "knocked back by the server; walk stopped" {
		t.Fatalf("%+v", r)
	}
	if froms := f.froms(); len(froms) != 2 || froms[1][1] <= me.Y {
		t.Fatalf("planned from %v, not from the landing north of the start", froms)
	}
	if st := h.s.Status(); st.ZUnknown || st.X != me.X+8 {
		t.Fatalf("status %+v", st)
	}
	h.logout()
}

func TestWalkPathReportsTheTeleportPosition(t *testing.T) {
	f := &fakePlanner{replies: []func(from, to navmesh.Vec3) navmesh.Path{straight}}
	h := start(t, Options{Planner: f})
	h.enterWorld()
	done := make(chan WalkReport, 1)
	go func() { r, _ := h.s.WalkPath(context.Background(), me.X+8, me.Y, me.Z); done <- r }()
	h.expect(opcode.MSGMoveStartForward)
	w := packet.NewWriter().PackedGUID(me.GUID).U32(7)
	game.WriteMoveInfo(w, game.MoveInfo{X: me.X + 12, Y: me.Y, Z: me.Z})
	h.send(opcode.MSGMoveTeleportAck, w.Bytes())
	h.expect(opcode.MSGMoveTeleportAck)
	r := <-done
	h.logout()
	if r.Replans != 1 || r.Trace[2].Kind != "interrupted" || r.Trace[2].From[0] != me.X+12 {
		t.Fatalf("%+v", r)
	}
	// 4 yd back to the goal; the 12 yd teleport is not walked.
	if r.Walked > 5 {
		t.Fatalf("walked %.2f counts the teleport", r.Walked)
	}
}
