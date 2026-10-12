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

func TestWalkPathWalksEachSegmentAndArrives(t *testing.T) {
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
	// Segment 1 faces east (along +x, orientation 0), segment 2 north (+y, pi/2).
	if mi := moveInfo(h.expect(opcode.MSGMoveStartForward).Body); mi.X != me.X || math.Abs(float64(mi.O)) > 1e-3 {
		t.Fatalf("first start %+v", mi)
	}
	if mi := moveInfo(h.expect(opcode.MSGMoveStop).Body); mi.X != me.X+4 || mi.Y != me.Y {
		t.Fatalf("stop at the corner %+v", mi)
	}
	if mi := moveInfo(h.expect(opcode.MSGMoveStartForward).Body); mi.X != me.X+4 || math.Abs(float64(mi.O)-math.Pi/2) > 1e-3 {
		t.Fatalf("second start %+v", mi)
	}
	if mi := moveInfo(h.expect(opcode.MSGMoveStop).Body); mi.X != goal[0] || mi.Y != goal[1] {
		t.Fatalf("stop at the goal %+v", mi)
	}
	r := <-done
	if r.Result != "arrived" || r.DistLeft > 0.01 || r.Replans != 0 || math.Abs(float64(r.Walked-7)) > 0.01 ||
		kinds(r) != "plan,walk,walk" || r.Trace[0].Status != "complete" || len(r.Trace[0].Points) != 3 {
		t.Fatalf("%+v", r)
	}
	if st := h.s.Status(); st.X != goal[0] || st.Y != goal[1] {
		t.Fatalf("status %+v", st)
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
		if dz > 7*dt+0.05 || dz > 7*dt+0.5 {
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
	w := newWalk([][3]float32{{0, 0, 0}, {10, 0, 0}}, 7, time.Now(), nil)
	w.sentZ, w.sentT = 0, 1000
	if z := w.clampZ(6, 1500); z != 3.5 {
		t.Fatalf("z %v, want 3.5", z)
	}
	if z := w.clampZ(-6, 2000); z != 0 {
		t.Fatalf("z %v, want 0", z)
	}
	if z := w.clampZ(1, 2500); z != 1 {
		t.Fatalf("z %v, want 1", z)
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

// A partial path that gained ground is planned on from its end (the tiles round the new
// start may reach further); one that gained nothing ends the walk.
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
	h.logout()
}
