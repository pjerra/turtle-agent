package session

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// wolfNearby puts a named Young Wolf 10 yards east of the player.
func wolfNearby(h *harness) {
	h.t.Helper()
	wolfName(h)
	h.send(opcode.SMSGUpdateObject, unitCreate(wolfGUID, objects.TypeUnit, me.X+10, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	waitFor(h.t, func() bool { return len(h.s.Units(objects.Filter{Name: "young wolf"})) == 1 })
}

func TestTargetAttackStopCast(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.Attack(ctx); err == nil || !strings.Contains(err.Error(), "no target") {
		t.Fatalf("attack without target: %v", err)
	}
	wolfNearby(h)
	u, err := h.s.Target(ctx, 0, "young wolf")
	if err != nil || u.GUID != wolfGUID || u.Name != "Young Wolf" {
		t.Fatalf("%+v %v", u, err)
	}
	if p := h.expect(opcode.CMSGSetSelection); packet.NewReader(p.Body).GUID() != wolfGUID {
		t.Fatalf("% X", p.Body)
	}
	if err := h.s.Attack(ctx); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGAttackswing); packet.NewReader(p.Body).GUID() != wolfGUID {
		t.Fatalf("% X", p.Body)
	}
	if err := h.s.Cast(ctx, 133, 0); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpell(133, wolfGUID)) {
		t.Fatalf("% X", p.Body)
	}
	if err := h.s.StopAttack(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGAttackstop)
	if err := h.s.CancelAutoRepeat(ctx); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGCancelAutoRepeatSpell); len(p.Body) != 0 {
		t.Fatalf("% X", p.Body)
	}
	if h.s.Status().Target != wolfGUID {
		t.Fatal("status target")
	}
	h.send(opcode.SMSGAttackstart, packet.NewWriter().GUID(me.GUID).GUID(wolfGUID).Bytes())
	h.send(opcode.SMSGAttackswingNotinrange, nil)
	h.logout()
	if a := ofKind(h.s.Log(), events.KindAttackStart); len(a) != 1 || a[0].Source != "Harnessb" || a[0].Target != "Young Wolf" {
		t.Fatalf("%+v", a)
	}
	if e := ofKind(h.s.Log(), events.KindAttackError); len(e) != 1 || e[0].Data["reason"] != "not_in_range" {
		t.Fatalf("%+v", e)
	}
}

func TestFaceAndWalk(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	wolfNearby(h)
	if err := h.s.Face(ctx, wolfGUID); err != nil {
		t.Fatal(err)
	}
	if mi := game.ReadMoveInfo(packet.NewReader(h.expect(opcode.MSGMoveSetFacing).Body)); math.Abs(float64(mi.O)) > 1e-4 || mi.X != me.X {
		t.Fatalf("%+v", mi)
	}
	if err := h.s.WalkTo(ctx, me.X+100, me.Y, me.Z); err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("long walk: %v", err)
	}
	if err := h.s.WalkTo(ctx, me.X+3.5, me.Y, me.Z); err != nil {
		t.Fatal(err)
	}
	if mi := game.ReadMoveInfo(packet.NewReader(h.expect(opcode.MSGMoveStartForward).Body)); mi.Flags != game.MoveForward {
		t.Fatalf("start %+v", mi)
	}
	if mi := game.ReadMoveInfo(packet.NewReader(h.expect(opcode.MSGMoveStop).Body)); mi.Flags != 0 || mi.X != me.X+3.5 {
		t.Fatalf("stop %+v", mi)
	}
	if st := h.s.Status(); st.X != me.X+3.5 {
		t.Fatalf("%+v", st)
	}
	h.logout()
}

func TestTeleportNearFarAndSpeed(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	done := make(chan error, 1)
	go func() { done <- h.s.Teleport(ctx, 1, 2, 3, -1) }()
	if p := h.expect(opcode.CMSGMessagechat); !strings.Contains(string(p.Body), ".go xyz 1.00 2.00 3.00") {
		t.Fatalf("% X", p.Body)
	}
	w := packet.NewWriter().PackedGUID(me.GUID).U32(7)
	game.WriteMoveInfo(w, game.MoveInfo{X: 1, Y: 2, Z: 3})
	h.send(opcode.MSGMoveTeleportAck, w.Bytes())
	r := packet.NewReader(h.expect(opcode.MSGMoveTeleportAck).Body)
	if r.GUID() != me.GUID || r.U32() != 7 {
		t.Fatal("teleport ack")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.send(opcode.SMSGTransferPending, packet.NewWriter().U32(1).Bytes())
	h.send(opcode.SMSGNewWorld, packet.NewWriter().U32(1).F32(10).F32(20).F32(30).F32(0).Bytes())
	h.expect(opcode.MSGMoveWorldportAck)
	h.expect(opcode.CMSGSetActiveMover)
	h.send(opcode.SMSGForceRunSpeedChange, packet.NewWriter().PackedGUID(me.GUID).U32(3).F32(14).Bytes())
	ack := packet.NewReader(h.expect(opcode.CMSGForceRunSpeedChangeAck).Body)
	if ack.GUID() != me.GUID || ack.U32() != 3 {
		t.Fatal("speed ack header")
	}
	game.ReadMoveInfo(ack)
	if ack.F32() != 14 || ack.Remaining() != 0 {
		t.Fatal("speed ack tail")
	}
	time.Sleep(50 * time.Millisecond)
	if st := h.s.Status(); st.Map != 1 || st.X != 10 {
		t.Fatalf("%+v", st)
	}
	h.logout()
}

// TestRootStopsWalkAndBlocksIt pins ruling T23: a root mid-walk stops with MoveRoot, the
// acks carry MoveRoot while rooted and a non-zero, non-decreasing client time, and walk_to
// refuses until the unroot.
func TestRootStopsWalkAndBlocksIt(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	walked := make(chan error, 1)
	go func() { walked <- h.s.WalkTo(ctx, me.X+20, me.Y, me.Z) }()
	start := game.ReadMoveInfo(packet.NewReader(h.expect(opcode.MSGMoveStartForward).Body))
	if start.Time == 0 {
		t.Fatal("client time 0")
	}
	time.Sleep(300 * time.Millisecond)
	h.send(opcode.SMSGForceMoveRoot, packet.NewWriter().PackedGUID(me.GUID).U32(5).Bytes())
	stop := game.ReadMoveInfo(packet.NewReader(h.expect(opcode.MSGMoveStop).Body))
	if stop.Flags != game.MoveRoot || stop.X <= me.X || stop.X >= me.X+20 || stop.Time < start.Time {
		t.Fatalf("stop %+v (start %+v)", stop, start)
	}
	r := packet.NewReader(h.expect(opcode.CMSGForceMoveRootAck).Body)
	if r.GUID() != me.GUID || r.U32() != 5 {
		t.Fatal("root ack header")
	}
	if mi := game.ReadMoveInfo(r); mi.Flags != game.MoveRoot || mi.X != stop.X || mi.Time < stop.Time || r.Remaining() != 0 {
		t.Fatalf("root ack %+v", mi)
	}
	if err := <-walked; err == nil || !strings.Contains(err.Error(), "rooted") {
		t.Fatalf("walk under root: %v", err)
	}
	if st := h.s.Status(); st.X != stop.X {
		t.Fatalf("status %+v, stop at %v", st, stop.X)
	}
	if err := h.s.WalkTo(ctx, stop.X+1, me.Y, me.Z); err == nil || !strings.Contains(err.Error(), "rooted") {
		t.Fatalf("walk while rooted: %v", err)
	}
	h.send(opcode.SMSGForceMoveUnroot, packet.NewWriter().PackedGUID(me.GUID).U32(6).Bytes())
	r = packet.NewReader(h.expect(opcode.CMSGForceMoveUnrootAck).Body)
	if r.GUID() != me.GUID || r.U32() != 6 {
		t.Fatal("unroot ack header")
	}
	if mi := game.ReadMoveInfo(r); mi.Flags != 0 || mi.Time < stop.Time {
		t.Fatalf("unroot ack %+v", mi)
	}
	if err := h.s.WalkTo(ctx, stop.X, me.Y, me.Z); err != nil {
		t.Fatalf("walk after unroot: %v", err)
	}
	h.logout()
}

// selfCreate is the player's own create block carrying run speed run (Object.cpp:436-441).
func selfCreate(run float32) []byte { return selfCreateFlags(run, 0) }

// selfCreateFlags is selfCreate with movement flags in its MovementInfo.
func selfCreateFlags(run float32, flags uint32) []byte {
	w := objtest.Header(1).U8(uint8(objects.BlockCreate2)).PackedGUID(me.GUID).U8(objects.TypePlayer)
	w.U8(0x20)
	game.WriteMoveInfo(w, game.MoveInfo{Flags: flags, X: me.X, Y: me.Y, Z: me.Z})
	w.F32(2.5).F32(run).F32(4.5).F32(4.72).F32(2.5).F32(3.14)
	objtest.Values(w, 192, map[uint16]uint32{objects.UnitHealth: 100, objects.UnitMaxHealth: 100, objects.UnitLevel: 61})
	return w.Bytes()
}

// TestWalkStepsKeepToTheSpeedInForce collects every movement packet of a 10-yard walk at
// the seeded run speed of 10 that the server slows to 3.5 halfway: no step may cover
// more than the speed in force allows for its client-time gap, and the arrival must fall
// when the rest of the walk at 3.5 yd/s ends.
func TestWalkStepsKeepToTheSpeedInForce(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, selfCreate(10))
	waitFor(t, func() bool { return h.s.Status().MaxHealth == 100 })
	walked := make(chan error, 1)
	go func() { walked <- h.s.WalkTo(ctx, me.X+10, me.Y, me.Z) }()
	read := func(p []byte) game.MoveInfo { return game.ReadMoveInfo(packet.NewReader(p)) }
	start := read(h.expect(opcode.MSGMoveStartForward).Body)
	beat := read(h.expect(opcode.MSGMoveHeartbeat).Body)
	if v := (beat.X - start.X) / float32(beat.Time-start.Time) * 1000; v < 9 {
		t.Fatalf("first step at %.2f yd/s, not the seeded 10", v)
	}
	h.send(opcode.SMSGForceRunSpeedChange, packet.NewWriter().PackedGUID(me.GUID).U32(4).F32(3.5).Bytes())
	mark := len(h.srv.Skipped())
	stopPkt, err := h.srv.Expect(opcode.MSGMoveStop, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-walked; err != nil {
		t.Fatal(err)
	}
	type step struct {
		mi  game.MoveInfo
		ack bool
	}
	steps := []step{{mi: start}, {mi: beat}}
	ackAt := -1
	for _, p := range h.srv.Skipped()[mark:] {
		switch p.Op {
		case opcode.MSGMoveHeartbeat:
			steps = append(steps, step{mi: read(p.Body)})
		case opcode.CMSGForceRunSpeedChangeAck:
			r := packet.NewReader(p.Body)
			r.GUID()
			r.U32()
			ackAt = len(steps)
			steps = append(steps, step{mi: game.ReadMoveInfo(r), ack: true})
		}
	}
	stop := read(stopPkt.Body)
	steps = append(steps, step{mi: stop})
	if ackAt < 0 {
		t.Fatal("no speed ack")
	}
	for i := 1; i < len(steps); i++ {
		a, b := steps[i-1].mi, steps[i].mi
		speed := float32(10)
		if i > ackAt {
			speed = 3.5
		}
		dt := float32(b.Time-a.Time) / 1000
		if d := float32(math.Hypot(float64(b.X-a.X), float64(b.Y-a.Y))); b.Time < a.Time || d > speed*dt+0.5 {
			t.Fatalf("step %d: %.2f yd in %.3f s at %.1f yd/s (%+v -> %+v)", i, d, dt, speed, a, b)
		}
	}
	ack := steps[ackAt].mi
	want := ack.Time + uint32((me.X+10-ack.X)/3.5*1000)
	if stop.X != me.X+10 || stop.Time+20 < want || stop.Time > want+300 {
		t.Fatalf("arrived at x=%v ctime %d, want x=%v ctime ~%d", stop.X, stop.Time, me.X+10, want)
	}
	h.logout()
}

// Review I4: a name picks the nearest living match over a nearer corpse, and the corpse
// only when nothing of that name is alive.
func TestTargetByNamePrefersTheLiving(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	const live packet.GUID = 0xF13000012B000002
	wolfName(h)
	h.send(opcode.SMSGUpdateObject, unitCreate(wolfGUID, objects.TypeUnit, me.X+2, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 0, objects.UnitMaxHealth: 50}))
	h.send(opcode.SMSGUpdateObject, unitCreate(live, objects.TypeUnit, me.X+9, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	waitFor(t, func() bool { return len(h.s.Units(objects.Filter{Name: "young wolf"})) == 2 })
	if u, err := h.s.Target(ctx, 0, "young wolf"); err != nil || u.GUID != live || u.Dead {
		t.Fatalf("%+v %v", u, err)
	}
	if p := h.expect(opcode.CMSGSetSelection); packet.NewReader(p.Body).GUID() != live {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(live, map[uint16]uint32{objects.UnitHealth: 0}))
	waitFor(t, func() bool { u, _ := h.s.Store().Get(live, 0, 0, 0); return u.Dead })
	if u, err := h.s.Target(ctx, 0, "young wolf"); err != nil || u.GUID != wolfGUID || !u.Dead {
		t.Fatalf("only corpses: %+v %v", u, err)
	}
	h.expect(opcode.CMSGSetSelection)
	h.logout()
}

// Review M3: a server spline on the player (fear) ends a running walk, moves the player's
// own position to its destination, and walk_to waits until it has run; a relayed move of
// the player does the same without the wait.
func TestServerMoveRefreshesOwnPosition(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	walked := make(chan error, 1)
	go func() { walked <- h.s.WalkTo(ctx, me.X+20, me.Y, me.Z) }()
	h.expect(opcode.MSGMoveStartForward)
	fear := packet.NewWriter().PackedGUID(me.GUID).F32(me.X).F32(me.Y).F32(me.Z).U32(1).U8(0).U32(0x100).U32(300).U32(1).
		F32(me.X - 5).F32(me.Y + 6).F32(me.Z).Bytes()
	h.send(opcode.SMSGMonsterMove, fear)
	if err := <-walked; err == nil || !strings.Contains(err.Error(), "moved by the server") {
		t.Fatalf("walk: %v", err)
	}
	if st := h.s.Status(); st.X != me.X-5 || st.Y != me.Y+6 {
		t.Fatalf("status %+v", st)
	}
	if err := h.s.WalkTo(ctx, me.X, me.Y, me.Z); err == nil || !strings.Contains(err.Error(), "waits") {
		t.Fatalf("walk during the spline: %v", err)
	}
	time.Sleep(350 * time.Millisecond)
	w := packet.NewWriter().PackedGUID(me.GUID)
	game.WriteMoveInfo(w, game.MoveInfo{X: me.X - 4, Y: me.Y + 6, Z: me.Z})
	h.send(opcode.MSGMoveTeleport, w.Bytes())
	waitFor(t, func() bool { return h.s.Status().X == me.X-4 })
	if err := h.s.WalkTo(ctx, me.X-3, me.Y+6, me.Z); err != nil {
		t.Fatalf("walk after the spline: %v", err)
	}
	if mi := game.ReadMoveInfo(packet.NewReader(h.expect(opcode.MSGMoveStartForward).Body)); mi.X != me.X-4 || mi.Y != me.Y+6 {
		t.Fatalf("walk starts at %+v", mi)
	}
	h.expect(opcode.MSGMoveStop)
	h.logout()
}

// Status shows the live level from the store, not the one from the character list.
func TestStatusLevelFollowsTheStore(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	if got := h.s.Status().Level; got != me.Level {
		t.Fatalf("before the create block: level %d", got)
	}
	h.send(opcode.SMSGUpdateObject, selfCreate(7))
	waitFor(t, func() bool { return h.s.Status().Level == 61 })
	h.logout()
}

// A Teleport that gives up leaves no waiter behind.
func TestTimedOutTeleportDropsItsWaiter(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := h.s.Teleport(ctx, 1, 2, 3, -1); err == nil {
		t.Fatal("teleport without a server answer succeeded")
	}
	n, err := h.s.do(context.Background(), func() (any, error) { return len(h.s.teleportWaiters), nil })
	if err != nil || n.(int) != 0 {
		t.Fatalf("waiters left: %v %v", n, err)
	}
	h.logout()
}
