package main

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/session"
)

// The place-unfreeze safety net (walks 54 and 55, Jin'do): after a `--place` two bots of every raid could stay frozen
// at the placement point, often for the whole first fight (fights with the pair frozen won 22 %, clean ones 50-71 %).
// The cause lives in the server; this is the agent's net. From the first watch poll after a placement move (the
// `.tt place`, or the GM summon wave of a retry or retreat) and for unfreezeWindow after it, the watch loop keeps a
// short position history of every hire, one sample per poll. A hire that is alive, in the instance, not the main tank,
// has not moved for unfreezeStillFor, still stands within spread + unfreezeRingPadYd of the placement point, stands
// more than unfreezeFarFromMTYd from the main tank, and whose main tank has left the point (more than unfreezeMTMovedYd
// away) is put onto the main tank again, at most unfreezeMaxReplace times. The cause (FROZEN-REPRO.md): the `spread=5`
// place drops two seats onto a small disconnected navmesh island beside the step-516 point, where every move fails with
// NOPATH; they stay within about 8 yd of the point while the raid walks on and end up about 50 yd behind the main tank.
// A caster or healer that stands still elsewhere, or near the point within reach of the main tank's fight, is none of
// this and is never touched. `--no-place-unfreeze` turns it off.

const (
	unfreezeFirstCheck  = 15 * time.Second  // the first judgement, after the first watch sample
	unfreezeWindow      = 120 * time.Second // the last judgement, after the first watch sample
	unfreezeStillFor    = 10 * time.Second  // how long a hire must have stood still
	unfreezeStillYd     = 2.0               // "stood still": every sample of that time within this many yards (2D) of the latest
	unfreezeMTMovedYd   = 15.0              // the main tank farther than this from the placement point: the raid has left
	unfreezeFarFromMTYd = 35.0              // a frozen hire stands farther than this (2D) from the main tank; nearer, it is doing its job
	unfreezeRingPadYd   = 6.0               // a frozen hire stands within spread + this of the placement point (11 yd for spread 5)
	unfreezeMaxReplace  = 2                 // successful re-places per hire and placement
	unfreezeMaxFails    = 3                 // failed `.summon`s per hire and placement
	unfreezeFailGap     = 5 * time.Second   // a failed hire's next `.summon` waits at least this long
	unfreezeMaxGoFails  = 3                 // consecutive failed commander `.go xyz` moves per placement, then the check stops
	unfreezeLandWait    = 5 * time.Second   // how long the commander's `.go` onto the main tank may take to land
	unfreezeLandYd      = 5.0               // the landing counts within this many yards (2D) of the target
	unfreezeSampleGap   = time.Second       // at most one position sample per hire per second (in practice one per watch poll, which lasts 2-7 s)
	unfreezeFreshAge    = 15 * time.Second  // a frame older than this (against the poll's own time) says nothing about where a hire is now
	unfreezeAnchorMax   = 20 * time.Second  // the sample that opens the still window may be at most this old
	unfreezeKeep        = 30 * time.Second  // how much history is kept
)

// posSample is one position of a hire.
type posSample struct {
	at      time.Time
	x, y, z float32
}

// unfreezeBot is one hire as frozenBots judges it: its history (oldest first), whether it is alive and on the walk's
// map in the freshest frame, and how often it has been re-placed already.
type unfreezeBot struct {
	name     string
	mt       bool
	alive    bool
	replaced int
	samples  []posSample
}

// unfreezeFlat is the flat distance between two points.
func unfreezeFlat(ax, ay, bx, by float32) float64 {
	dx, dy := float64(ax-bx), float64(ay-by)
	return math.Sqrt(dx*dx + dy*dy)
}

// stillSince reads a hire's history at now: where it stands, and for how long it has stood there (the span from the
// oldest sample of its still run to the latest one). ok is false when the history says nothing yet: no fresh latest
// sample, or none old enough to open an unfreezeStillFor window. A hire that moved within the window is not still.
func stillSince(samples []posSample, now time.Time) (x, y, z float32, span time.Duration, ok bool) {
	if len(samples) == 0 {
		return 0, 0, 0, 0, false
	}
	last := samples[len(samples)-1]
	if now.Sub(last.at) > unfreezeFreshAge {
		return 0, 0, 0, 0, false
	}
	// The anchor: the latest sample at or before now-unfreezeStillFor. The loop below checks it and everything after.
	anchor := -1
	for i := len(samples) - 1; i >= 0; i-- {
		if !samples[i].at.After(now.Add(-unfreezeStillFor)) {
			anchor = i
			break
		}
	}
	if anchor < 0 || now.Sub(samples[anchor].at) > unfreezeAnchorMax {
		return 0, 0, 0, 0, false
	}
	for _, s := range samples[anchor:] {
		if unfreezeFlat(s.x, s.y, last.x, last.y) >= unfreezeStillYd {
			return 0, 0, 0, 0, false
		}
	}
	// The whole still run, for the log line: walk back from the anchor while the hire stayed put.
	first := samples[anchor]
	for i := anchor - 1; i >= 0; i-- {
		if unfreezeFlat(samples[i].x, samples[i].y, last.x, last.y) >= unfreezeStillYd {
			break
		}
		first = samples[i]
	}
	return last.x, last.y, last.z, last.at.Sub(first.at), true
}

// frozenBots is the pure judgement: the names of the hires to re-place at now, in input order. A hire is frozen when
// it is alive, not the main tank, re-placed fewer than unfreezeMaxReplace times, has moved less than unfreezeStillYd
// over the last unfreezeStillFor, stands within spread + unfreezeRingPadYd of the placement point and more than
// unfreezeFarFromMTYd from the main tank (2D); and the main tank stands more than unfreezeMTMovedYd from the placement
// point. Combat is no part of it: a healer or ranged hire stands still while it casts, and the 35 yd rule keeps one
// that holds back near the point while the main tank fights just out of it.
func frozenBots(bots []unfreezeBot, mtPos, placePos [3]float32, spread float32, now time.Time) []string {
	if unfreezeFlat(mtPos[0], mtPos[1], placePos[0], placePos[1]) <= unfreezeMTMovedYd {
		return nil
	}
	ringYd := float64(spread) + unfreezeRingPadYd
	var out []string
	for _, b := range bots {
		if b.mt || !b.alive || b.replaced >= unfreezeMaxReplace {
			continue
		}
		x, y, _, _, ok := stillSince(b.samples, now)
		if !ok || unfreezeFlat(x, y, placePos[0], placePos[1]) > ringYd {
			continue
		}
		if d := unfreezeFlat(x, y, mtPos[0], mtPos[1]); d <= unfreezeFarFromMTYd {
			continue
		}
		out = append(out, b.name)
	}
	return out
}

// placeUnfreeze is the net's state for the walk's current placement.
type placeUnfreeze struct {
	active   bool
	started  time.Time // the first watch sample after the placement move; zero until then
	point    [3]float32
	spread   float32 // the spread of the placement move that armed the check (0: a GM summon wave)
	hist     map[string][]posSample
	replaced map[string]int       // successful re-places
	failed   map[string]int       // failed `.summon`s
	failAt   map[string]time.Time // the last failed `.summon`
	flagged  map[string]bool      // judged frozen at least once
	goFails  int                  // failed commander moves onto the main tank
	sent     int                  // re-places that went out and succeeded
}

// placeUnfreezeStart arms the net at a placement move that sets a new point: the `.tt place` (spread placeSpread) and
// the GM summon wave of a retry or a retreat (spread 0: the hires land on the commander, a 6 yd ring). Every such move
// arms it again from scratch with that move's own point and spread; the clock starts at the next watch sample.
func (w *walk) placeUnfreezeStart(p dungeon.Point, spread float32) {
	if w.opts.noPlaceUnfreeze {
		return
	}
	w.placeUnfreezeFinish() // an arm still running ends with its own summary first
	w.unfreeze = placeUnfreeze{active: true, point: [3]float32{p.X, p.Y, p.Z}, spread: spread,
		hist: map[string][]posSample{}, replaced: map[string]int{}, failed: map[string]int{}, failAt: map[string]time.Time{},
		flagged: map[string]bool{}}
	w.note("placement: unfreeze check armed at %s %s %s (ring %.0f yd)", num32(p.X), num32(p.Y), num32(p.Z), float64(spread)+unfreezeRingPadYd)
}

// unfreezeFresh says a hire's last frame is recent against the poll's own time and on the walk's map.
func (w *walk) unfreezeFresh(b botPos, now time.Time) bool {
	return now.Sub(b.at) <= unfreezeFreshAge && b.mapID == w.plan.Map
}

// unfreezeAlive says a hire's last frame is recent, alive and on the walk's map.
func (w *walk) unfreezeAlive(b botPos, now time.Time) bool {
	return w.unfreezeFresh(b, now) && !b.dead
}

// placeUnfreezeTick runs once per watch poll, right after pollOnce, with that poll's own time (w.pollAt, the now that
// stamped its frames, not the clock after the poll's waits): it takes one position sample per hire per frame, and from
// unfreezeFirstCheck after the first sample on re-places the hires frozen at the placement point. A failed GM line is a
// note, never the walk's end.
func (w *walk) placeUnfreezeTick(ctx context.Context) {
	u := &w.unfreeze
	if !u.active {
		return
	}
	now := w.pollAt
	if u.started.IsZero() {
		u.started = now
	}
	for name := range w.hires {
		b, ok := w.botAt[name]
		if !ok || !w.unfreezeAlive(b, now) {
			delete(u.hist, name) // dead, gone or stale: a corpse's rest is no history
			continue
		}
		s := u.hist[name]
		if n := len(s); n > 0 && b.at.Sub(s[n-1].at) < unfreezeSampleGap {
			continue // no newer frame since the last sample
		}
		s = append(s, posSample{at: b.at, x: b.x, y: b.y, z: b.z})
		for len(s) > 0 && now.Sub(s[0].at) > unfreezeKeep {
			s = s[1:]
		}
		u.hist[name] = s
	}
	if now.Sub(u.started) > unfreezeWindow {
		w.placeUnfreezeFinish()
		return
	}
	if w.allDead(now) {
		w.placeUnfreezeStop("the raid wiped") // the wipe recovery's own handleWipe stops it too
		return
	}
	mt, ok := w.botAt[w.mt]
	if ok && w.unfreezeFresh(mt, now) && mt.dead {
		w.placeUnfreezeStop("the main tank died")
		return
	}
	if now.Sub(u.started) < unfreezeFirstCheck {
		return
	}
	if !ok || !w.unfreezeAlive(mt, now) {
		return // no main tank to judge by or to move onto
	}
	var bots []unfreezeBot
	for _, name := range w.hireList() {
		b, ok := w.botAt[name]
		if !ok || !w.unfreezeAlive(b, now) {
			continue
		}
		if u.failed[name] >= unfreezeMaxFails {
			continue // its summons kept failing: left alone for this placement
		}
		if at, failed := u.failAt[name]; failed && now.Sub(at) < unfreezeFailGap {
			continue
		}
		bots = append(bots, unfreezeBot{name: name, mt: name == w.mt, alive: true, replaced: u.replaced[name], samples: u.hist[name]})
	}
	frozen := frozenBots(bots, [3]float32{mt.x, mt.y, mt.z}, u.point, u.spread, now)
	if len(frozen) == 0 {
		return
	}
	for _, name := range frozen {
		u.flagged[name] = true
	}
	// One commander move onto the main tank, then one summon per frozen hire: the per-hire GM path the placement already
	// uses (`.summon` lands the hire at the commander). `.tt place` has no per-hire form, only the whole group. The move
	// lands 2 yd above the main tank's floor, as every placement `.go` does.
	if err := w.unfreezeGoOntoMT(ctx, dungeon.Point{X: mt.x, Y: mt.y, Z: mt.z + 2}); err != nil {
		u.goFails++
		w.note("placement: re-place skipped, the commander's .go onto %s (%d of %d): %v", w.mt, u.goFails, unfreezeMaxGoFails, err)
		if u.goFails >= unfreezeMaxGoFails {
			w.placeUnfreezeStop(fmt.Sprintf("the commander's .go onto %s failed %d times", w.mt, u.goFails))
		}
		return
	}
	u.goFails = 0 // the cap counts consecutive failures
	for _, name := range frozen {
		x, y, z, span, _ := stillSince(u.hist[name], now)
		if err := w.gm(ctx, ".summon "+name); err != nil {
			// The hire stays eligible: its history is kept and the next poll at least unfreezeFailGap later tries again.
			u.failed[name]++
			u.failAt[name] = now
			line := fmt.Sprintf("placement: re-place failed %s (%v)", name, err)
			w.logLine(w.env.now(), line)
			w.note("%s", line)
			continue
		}
		u.replaced[name]++
		u.sent++
		delete(u.hist, name) // judged again only after a new still window at the new spot
		line := fmt.Sprintf("placement: re-placed %s (frozen at %.1f,%.1f,%.1f for %.0fs)", name, x, y, z, span.Seconds())
		w.logLine(w.env.now(), line)
		w.note("%s", line)
		if err := w.env.sleep(ctx, 100*time.Millisecond); err != nil {
			return
		}
	}
}

// unfreezeGoOntoMT moves the commander onto p and waits up to unfreezeLandWait until he has LANDED within
// unfreezeLandYd of it: a `.go xyz` answers at once and the server lands the character a moment later, and a summon
// sent before the landing drops the hire at the OLD spot (walk 22 test2 s4, Q3). The landing rule is the revive's
// (reviveLanded: the acked-teleport count or a settled position on the plan's map, read through waitStatus), with the
// commander already inside the instance, plus the tighter radius. A timeout or a landing too far away is an error.
func (w *walk) unfreezeGoOntoMT(ctx context.Context, p dungeon.Point) error {
	beforeSeq, haveSeq := uint64(0), false
	if st, err := w.status(ctx); err == nil {
		beforeSeq, haveSeq = st.TeleportSeq, true
	}
	if err := w.goPlace(ctx, p.X, p.Y, p.Z); err != nil {
		return err
	}
	return w.waitStatus(ctx, unfreezeLandWait, time.Time{},
		fmt.Sprintf("the commander did not land within %.0f yd of %s %s %s", unfreezeLandYd, num32(p.X), num32(p.Y), num32(p.Z)),
		func(st session.StatusInfo) bool {
			return reviveLanded(p, w.plan.Map, w.plan.Map, st, beforeSeq, haveSeq) && unfreezeFlat(st.X, st.Y, p.X, p.Y) <= unfreezeLandYd
		})
}

// placeUnfreezeStop ends the check for this placement early with a note, then writes the summary.
func (w *walk) placeUnfreezeStop(reason string) {
	if !w.unfreeze.active {
		return
	}
	line := fmt.Sprintf("placement: unfreeze check stopped (%s)", reason)
	w.logLine(w.env.now(), line)
	w.note("%s", line)
	w.placeUnfreezeFinish()
}

// placeUnfreezeFinish ends the net and writes the summary line, once.
func (w *walk) placeUnfreezeFinish() {
	u := &w.unfreeze
	if !u.active {
		return
	}
	u.active = false
	line := fmt.Sprintf("placement: frozen=%d re-placed=%d", len(u.flagged), u.sent)
	w.logLine(w.env.now(), line)
	w.note("%s", line)
}
