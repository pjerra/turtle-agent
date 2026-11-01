// raid_walk_place.go: phase 8b, the `--place` start (spec ~/TortoiseBots-audit/research/raid-walk-place/SPEC.md 5). The
// commander drops his own bind to the plan's map, appears in the raid's copy, clears the corridor of the legs the raid
// skips, quiets the area, moves the raid onto the start point and verifies it by the walk's own frames; the raid then
// starts partway into the instance. Every GM line passes raidwalk.AllowedLine and every toolkit line raidwalk.AllowedTT.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

const (
	// ttCallTimeout is how long one `tt` RPC may take. The agent's client waits for the toolkit's final reply, which for
	// `quiet w=20` arrives about 20 s after the command and for `place` about 4 s later; 45 s leaves room for the
	// event-log poll on top (spec 5 names 30 s).
	ttCallTimeout = 45 * time.Second
	// placeProbeTimeout is how long the toolkit probe waits before the walk calls the server not a test image.
	placeProbeTimeout = 10 * time.Second
	// placeGMTimeout is how long the GM-mode probe waits for the `.gm on` notification (review round 1, item 12).
	placeGMTimeout = 10 * time.Second
	// placeVerifyWait and placeSummonWait are the frame windows of the place verification (spec 5 step 9).
	placeVerifyWait = 15 * time.Second
	placeSummonWait = 10 * time.Second
	// placeNearYd is how close the commander must stand to a circle's point to clear it without a move (spec 5 step 6).
	placeNearYd = 140
	// placeRogueYd is how close to a BEHIND circle a named unit must stand for the one extra clear round (spec 5 step 7).
	placeRogueYd = 6
	// placeExtraRadius is the radius of that extra round.
	placeExtraRadius = 3
	// placeClearDz is the vertical window of the clear, its pre-read and the quiet (the toolkit's dz=).
	placeClearDz = 8
	// placeQuietWaitS is the quiet's w=: the two reads are this many seconds apart (spec 5 step 7).
	placeQuietWaitS = 20
	// placeQuietR is the quiet's radius around the start point (spec 5 step 7).
	placeQuietR = 50
)

// ttBody is one toolkit reply's fields.
type ttBody struct{ raw map[string]json.RawMessage }

// ttLast is the last reply of a toolkit command (the final read).
func ttLast(out ttResult) (ttBody, bool) {
	if len(out.Replies) == 0 {
		return ttBody{}, false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(out.Replies[len(out.Replies)-1], &body); err != nil || body == nil {
		return ttBody{}, false
	}
	return ttBody{raw: body}, true
}

// num, str, flag and list read one field of a reply.
func (b ttBody) num(key string) (float64, bool) {
	raw, ok := b.raw[key]
	if !ok {
		return 0, false
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

func (b ttBody) flag(key string) bool {
	var v bool
	if raw, ok := b.raw[key]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

func (b ttBody) list(key string) []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	if raw, ok := b.raw[key]; ok {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// numOf, strOf and posOf read a field of a list element.
func numOf(m map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

func strOf(m map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := m[key]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func posOf(m map[string]json.RawMessage, key string) (x, y, z float32, ok bool) {
	raw, found := m[key]
	if !found {
		return 0, 0, 0, false
	}
	var p []float32
	if err := json.Unmarshal(raw, &p); err != nil || len(p) < 3 {
		return 0, 0, 0, false
	}
	return p[0], p[1], p[2], true
}

// ttUnit is one unit of a `q units` reply, reduced to what the pre-read needs.
type ttUnit struct {
	GUID  string
	Entry uint32
	Name  string
	X     float32
	Y     float32
	Z     float32
	Dz    float32
	Dist  float32
}

// tt is one Eluna test toolkit command (spec 5): the line passes raidwalk.AllowedTT first, and the answer is the `tt`
// RPC's replies. A refusal by the toolkit comes back as an error that carries its code.
func (w *walk) tt(ctx context.Context, cmd string) (ttResult, error) {
	if !raidwalk.AllowedTT(cmd, w.hires, w.plan.Map) {
		return ttResult{}, stopf(exitRaidWalkFailed, "raid-walk refused to send a toolkit line outside the allow-list: %s", cmd)
	}
	w.logLine(w.env.now(), "> .tt "+cmd)
	var out ttResult
	err := w.agent.Call(ctx, "tt", ttParams{Cmd: cmd}, &out, ttCallTimeout)
	return out, err
}

// ttInst reads the caller's instance id from a `q inst` reply.
func (w *walk) ttInst(ctx context.Context, why string) (float64, error) {
	out, err := w.tt(ctx, "q inst")
	if err != nil {
		return 0, stopf(exitRaidWalkFailed, "place: %s: q inst: %v", why, err)
	}
	body, ok := ttLast(out)
	if !ok {
		return 0, stopf(exitRaidWalkFailed, "place: %s: q inst gave no reply", why)
	}
	inst, ok := body.num("inst")
	if !ok {
		return 0, stopf(exitRaidWalkFailed, "place: %s: q inst gave no instance id", why)
	}
	return inst, nil
}

// phaseUnbindSelf is phase 5b (spec 5.1): with --place, the commander drops his own personal bind to the plan's map
// right after the portal (he is outside the instance then, and nothing is selected), so that `.appear` later lands him
// in the raid's copy and not in the copy of an older walk.
func (w *walk) phaseUnbindSelf(ctx context.Context) error {
	if w.place == nil {
		return nil
	}
	return w.gm(ctx, fmt.Sprintf(".instance unbind %d", w.plan.Map))
}

// placeGM is spec 5 step 1 (review round 1, item 12): before the first teleport the commander must be in GM mode, or
// `.appear` and the clears would fail late, with a hire already spent. `.gm on` is a no-op when already on and its
// reply (the server's notification "GM mode is ON", or an error for an account with no GM rights) is the only read of
// that state the walk has. A refusal ends the walk before anything moves.
func (w *walk) placeGM(ctx context.Context) error {
	if err := w.gm(ctx, ".gm on"); err != nil {
		return err
	}
	deadline := w.env.now().Add(placeGMTimeout)
	for {
		var page events.Page
		params := map[string]any{"since": w.eventsAt, "kinds": []string{"system"}, "wait_ms": 1000}
		if err := w.agent.Call(ctx, "events", params, &page, 15*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "place: the GM-mode check: events: %v", err)
		}
		w.eventsAt = page.Next
		for _, e := range page.Events {
			if line, ok := tbm.Line(e); ok {
				w.logLine(w.env.now(), line)
				continue
			}
			if e.Kind != events.KindSystem || e.Text == "" {
				continue
			}
			text := raidwalk.CleanLine(e.Text)
			switch {
			case strings.Contains(text, "GM mode is ON"):
				return nil
			case strings.Contains(text, "GM mode is OFF"), strings.Contains(text, "There is no such command"):
				return stopf(exitRaidWalkFailed, "place: the commander is not in GM mode (%s)", text)
			}
		}
		if !w.env.now().Before(deadline) {
			return stopf(exitRaidWalkFailed, "place: no answer to .gm on in %s: the commander's GM mode is unknown", placeGMTimeout)
		}
		if err := w.env.sleep(ctx, 200*time.Millisecond); err != nil {
			return stopf(exitRaidWalkFailed, "place: interrupted")
		}
	}
}

// placeSleep waits d, or ends the walk when the context does.
func (w *walk) placeSleep(ctx context.Context, d time.Duration) error {
	if err := w.env.sleep(ctx, d); err != nil {
		return stopf(exitRaidWalkFailed, "place: interrupted")
	}
	return nil
}

// goPlace sends a `.go xyz` inside the walk's map and remembers where the commander stands (the clear's distance rule).
func (w *walk) goPlace(ctx context.Context, x, y, z float32) error {
	if err := w.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(x), num32(y), num32(z), w.plan.Map)); err != nil {
		return err
	}
	w.placePos, w.placeHavePos = [3]float32{x, y, z}, true
	return nil
}

// placeCommanderFar says the commander is farther than placeNearYd (2D) from a point, so the clear must move him first.
func (w *walk) placeCommanderFar(x, y float32) bool {
	if !w.placeHavePos {
		return true
	}
	dx, dy := float64(w.placePos[0]-x), float64(w.placePos[1]-y)
	return dx*dx+dy*dy > placeNearYd*placeNearYd
}

// placeInstance is spec 5 step 4: the commander's `q inst` and the raid's `q group of=<MT>` must name one instance. The
// list may be truncated, so at least one hire on the walk's map must be listed and every listed one must share its
// instance. Nothing is cleared before this passes.
func (w *walk) placeInstance(ctx context.Context) (float64, error) {
	inst, err := w.ttInst(ctx, "the instance check")
	if err != nil {
		return 0, err
	}
	out, err := w.tt(ctx, "q group of="+w.mt)
	if err != nil {
		return 0, stopf(exitRaidWalkFailed, "place: q group of=%s: %v", w.mt, err)
	}
	body, ok := ttLast(out)
	if !ok {
		return 0, stopf(exitRaidWalkFailed, "place: q group of=%s gave no reply", w.mt)
	}
	members := body.list("members")
	if len(members) == 0 {
		return 0, stopf(exitRaidWalkFailed, "place: q group of=%s lists no member: the raid is not in %s's group", w.mt, w.mt)
	}
	raid, have := 0.0, false
	for _, m := range members {
		mp, ok := numOf(m, "map")
		if !ok || uint32(mp) != w.plan.Map {
			continue
		}
		i, ok := numOf(m, "inst")
		if !ok {
			continue
		}
		switch {
		case !have:
			raid, have = i, true
		case i != raid:
			return 0, stopf(exitRaidWalkFailed, "place: the raid is listed in two instances (%.0f and %.0f)", raid, i)
		}
	}
	if !have {
		return 0, stopf(exitRaidWalkFailed, "place: q group of=%s lists no member on map %d", w.mt, w.plan.Map)
	}
	if inst != raid {
		return 0, stopf(exitRaidWalkFailed, "place: the commander is in instance %.0f, the raid in %.0f (a stale personal bind: see .instance unbind)", inst, raid)
	}
	return raid, nil
}

// placeInstanceIs re-reads the commander's instance and fails when it moved (spec 5 steps 5 and 6).
func (w *walk) placeInstanceIs(ctx context.Context, want float64) error {
	got, err := w.ttInst(ctx, "the instance check")
	if err != nil {
		return err
	}
	if got != want {
		return stopf(exitRaidWalkFailed, "place: the commander is in instance %.0f, not the raid's %.0f", got, want)
	}
	return nil
}

// placeFloor checks the toolkit's floor under the start point (spec 5 step 5).
func (w *walk) placeFloor(ctx context.Context, p dungeon.Point) error {
	out, err := w.tt(ctx, fmt.Sprintf("q floor %s %s %s", num32(p.X), num32(p.Y), num32(p.Z)))
	if err != nil {
		return stopf(exitRaidWalkFailed, "place: q floor: %v", err)
	}
	body, ok := ttLast(out)
	if !ok {
		return stopf(exitRaidWalkFailed, "place: q floor gave no reply")
	}
	floor, ok := body.num("floor")
	if !ok {
		return stopf(exitRaidWalkFailed, "place: the start step's floor was not found (q floor gave none)")
	}
	if d := floor - float64(p.Z); d > raidwalk.PlaceFloorYd || d < -raidwalk.PlaceFloorYd {
		return stopf(exitRaidWalkFailed, "place: the start step's floor is %.1f, not %.1f", floor, p.Z)
	}
	return nil
}

// placeUnits is the pre-read of one circle (spec 5 step 6): the units the toolkit sees, and whether the reply was cut.
func (w *walk) placeUnits(ctx context.Context, r, x, y, z float32) ([]ttUnit, bool, error) {
	out, err := w.tt(ctx, fmt.Sprintf("q units %s %s %s %s all", num32(r), num32(x), num32(y), num32(z)))
	if err != nil {
		return nil, false, stopf(exitRaidWalkFailed, "place: q units at %s %s %s: %v", num32(x), num32(y), num32(z), err)
	}
	body, ok := ttLast(out)
	if !ok {
		return nil, false, stopf(exitRaidWalkFailed, "place: q units at %s %s %s gave no reply", num32(x), num32(y), num32(z))
	}
	var units []ttUnit
	for _, m := range body.list("units") {
		u := ttUnit{GUID: strOf(m, "g"), Name: strOf(m, "n")}
		if e, ok := numOf(m, "e"); ok {
			u.Entry = uint32(e)
		}
		if dz, ok := numOf(m, "dz"); ok {
			u.Dz = float32(dz)
		}
		u.X, u.Y, u.Z, _ = posOf(m, "p")
		units = append(units, u)
	}
	return units, body.flag("trunc"), nil
}

// placeCircleBlocked says why a circle must not be cleared ("" when it may): a unit the row does not own, a unit that
// stands in an AHEAD row's circle, or a pre-read the toolkit had to cut.
func (w *walk) placeCircleBlocked(c raidwalk.ClearCircle, units []ttUnit, trunc bool) string {
	if trunc {
		return "the reply lists more units than it can carry (trunc)"
	}
	for _, u := range units {
		if u.Dz > placeClearDz || u.Dz < -placeClearDz {
			continue
		}
		if inEntries(w.place.Skips, u.Entry) {
			// The clearto carries skip=<those entries>, so the toolkit never touches them: a skip entry in the circle
			// cannot block the clear (review round 1, item 2).
			continue
		}
		if !inEntries(c.Entries, u.Entry) {
			return fmt.Sprintf("unit %d %s stands in the circle", u.Entry, u.Name)
		}
		if k, inside := w.place.AheadIn(u.X, u.Y, u.Z); inside {
			return fmt.Sprintf("unit %d %s stands in AHEAD row %d", u.Entry, u.Name, k)
		}
	}
	return ""
}

// inEntries says a row owns an entry.
func inEntries(entries []uint32, e uint32) bool {
	for _, x := range entries {
		if x == e {
			return true
		}
	}
	return false
}

// placeClear is spec 5 step 6: the corridor's circles, in route order, each with its pre-read and its despawn.
func (w *walk) placeClear(ctx context.Context) error {
	skip := skipArg(w.place.Skips)
	for _, c := range w.place.Circles {
		moved := false
		if w.placeCommanderFar(c.X, c.Y) {
			if err := w.goPlace(ctx, c.X, c.Y, c.Z+2); err != nil {
				return err
			}
			if err := w.placeSleep(ctx, 2*time.Second); err != nil {
				return err
			}
			if err := w.placeInstanceIs(ctx, w.placeInst); err != nil {
				return err
			}
			moved = true
		}
		units, trunc, err := w.placeUnits(ctx, c.Radius, c.X, c.Y, c.Z)
		if err != nil {
			return err
		}
		summary := raidwalk.PlaceCircleSummary{Step: c.Step, Leg: c.Leg, X: c.X, Y: c.Y, Z: c.Z, Radius: c.Radius,
			PerUnit: c.PerUnit, Unit: c.Unit, Moved: moved}
		if why := w.placeCircleBlocked(c, units, trunc); why != "" {
			w.note("place: not cleared row %d: %s", c.Step, why)
			w.placeSum.NotCleared = append(w.placeSum.NotCleared, fmt.Sprintf("row %d: %s", c.Step, why))
			w.placeSum.Circles = append(w.placeSum.Circles, summary)
			continue
		}
		cmd := fmt.Sprintf("clearto %d %s %s %s %s dz=%s all=1%s", w.plan.Map, num32(c.X), num32(c.Y), num32(c.Z), num32(c.Radius), num32(placeClearDz), skip)
		out, err := w.tt(ctx, cmd)
		if err != nil {
			return stopf(exitRaidWalkFailed, "place: clearto row %d: %v", c.Step, err)
		}
		body, ok := ttLast(out)
		if !ok {
			return stopf(exitRaidWalkFailed, "place: clearto row %d gave no reply", c.Step)
		}
		removed, ok := body.num("n")
		if !ok {
			return stopf(exitRaidWalkFailed, "place: clearto row %d gave no count", c.Step)
		}
		summary.Removed = int(removed)
		for _, s := range body.list("skipped") {
			summary.Skipped = append(summary.Skipped, strOf(s, "why"))
		}
		w.placeSum.Circles = append(w.placeSum.Circles, summary)
		w.placeRemoved += int(removed)
		w.note("place: cleared row %d: %d removed%s", c.Step, int(removed), skippedWords(summary.Skipped))
		if int(removed) < len(units) {
			w.note("place: row %d removed %d of the %d units the pre-read listed", c.Step, int(removed), len(units))
		}
	}
	if w.placeHavePos && (w.placePos[0] != w.place.Point.X || w.placePos[1] != w.place.Point.Y) {
		if err := w.goPlace(ctx, w.place.Point.X, w.place.Point.Y, w.place.Point.Z+2); err != nil {
			return err
		}
		if err := w.placeSleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// skippedWords is the " (skipped: why, why)" tail of a clear note.
func skippedWords(why []string) string {
	if len(why) == 0 {
		return ""
	}
	return " (skipped: " + strings.Join(why, ", ") + ")"
}

// skipArg is the toolkit's ` skip=<ids>` argument ("" for none).
func skipArg(ids []uint32) string {
	if w := raidwalk.SkipWords(ids); w != "" {
		return " skip=" + w
	}
	return ""
}

// placeQuiet is spec 5 step 7: the toolkit's quiet rule around the start point, with one extra clear round for a named
// unit that stands by a BEHIND circle, then a refusal.
func (w *walk) placeQuiet(ctx context.Context) error {
	p := w.place.Point
	skip := skipArg(w.place.Skips)
	quiet, why, near, roamers, err := w.placeQuietOnce(ctx, p, skip)
	if err != nil {
		return err
	}
	if !quiet {
		cleared := 0
		for _, u := range near {
			if !w.placeRogue(u) {
				continue
			}
			// The extra round is a clear like any other (review round 1, item 1): it keeps the pre-read's answer and
			// runs the same guard before it acts. A unit no BEHIND row owns is never cleared, and a circle holding a
			// unit of another row, a unit in an AHEAD circle, or a truncated reply is not cleared at all.
			owner, ok := w.placeRogueOwner(u)
			if !ok {
				w.note("place: not cleared %d %s in the extra round: no BEHIND row owns the unit", u.Entry, u.Name)
				continue
			}
			c := raidwalk.ClearCircle{Step: owner.Step, Leg: owner.Leg, X: u.X, Y: u.Y, Z: u.Z,
				Radius: placeExtraRadius, Entries: owner.Entries}
			units, trunc, err := w.placeUnits(ctx, placeExtraRadius, u.X, u.Y, u.Z)
			if err != nil {
				return err
			}
			if why := w.placeCircleBlocked(c, units, trunc); why != "" {
				w.note("place: not cleared %d %s in the extra round: %s", u.Entry, u.Name, why)
				continue
			}
			cmd := fmt.Sprintf("clearto %d %s %s %s %d dz=%s all=1%s", w.plan.Map, num32(u.X), num32(u.Y), num32(u.Z), placeExtraRadius, num32(placeClearDz), skip)
			if _, err := w.tt(ctx, cmd); err != nil {
				w.note("place: the extra clear of %d %s: %v", u.Entry, u.Name, err)
				continue
			}
			cleared++
		}
		if len(roamers) > 0 {
			w.note("place: the quiet read lists %d roamer(s) with no position in the reply: %s", len(roamers), strings.Join(roamers, ", "))
		}
		if cleared > 0 {
			quiet, why, _, _, err = w.placeQuietOnce(ctx, p, skip)
			if err != nil {
				return err
			}
		}
	}
	w.placeSum.Quiet = quiet
	if !quiet {
		return stopf(exitRaidWalkFailed, "place: the area is not quiet: %s", why)
	}
	return nil
}

// placeQuietOnce runs one `quiet` and answers with its near list and roamers (the extra round reads them).
func (w *walk) placeQuietOnce(ctx context.Context, p dungeon.Point, skip string) (bool, string, []ttUnit, []string, error) {
	cmd := fmt.Sprintf("quiet %d %s %s %s w=%d dz=%s%s", placeQuietR, num32(p.X), num32(p.Y), num32(p.Z), placeQuietWaitS, num32(placeClearDz), skip)
	out, err := w.tt(ctx, cmd)
	if err != nil {
		return false, "", nil, nil, stopf(exitRaidWalkFailed, "place: quiet: %v", err)
	}
	body, ok := ttLast(out)
	if !ok {
		return false, "", nil, nil, stopf(exitRaidWalkFailed, "place: quiet gave no reply")
	}
	var near []ttUnit
	var names []string
	for _, m := range body.list("near") {
		u := ttUnit{GUID: strOf(m, "g"), Name: strOf(m, "n")}
		if e, ok := numOf(m, "e"); ok {
			u.Entry = uint32(e)
		}
		if d, ok := numOf(m, "d"); ok {
			u.Dist = float32(d)
		}
		u.X, u.Y, u.Z, _ = posOf(m, "p")
		near = append(near, u)
		names = append(names, fmt.Sprintf("%d %s %.1f yd", u.Entry, u.Name, u.Dist))
	}
	var roamers []string
	for _, m := range body.list("roamers") {
		e, _ := numOf(m, "e")
		names = append(names, fmt.Sprintf("%d (%s)", uint32(e), strOf(m, "why")))
		roamers = append(roamers, fmt.Sprintf("%d %s", uint32(e), strOf(m, "why")))
	}
	if body.flag("quiet") {
		return true, "", near, roamers, nil
	}
	if len(names) == 0 {
		names = append(names, "the toolkit named no unit")
	}
	return false, strings.Join(names, ", "), near, roamers, nil
}

// placeRogue says whether a unit of the quiet list gets the one extra clear round: it stands within placeRogueYd of a
// BEHIND circle, or its entry is one of the --patrols file's.
func (w *walk) placeRogue(u ttUnit) bool {
	for _, c := range w.place.Circles {
		if d := placeDist3(u.X, u.Y, u.Z, c.X, c.Y, c.Z); d <= placeRogueYd {
			return true
		}
	}
	for _, set := range w.patrolGUIDS {
		if set[u.GUID] || set[strings.ToUpper(u.GUID)] {
			return true
		}
	}
	for _, pat := range w.quietPatrols {
		for _, p := range pat.Path {
			if placeDist3(u.X, u.Y, u.Z, p.X, p.Y, p.Z) <= placeRogueYd {
				return true
			}
		}
	}
	return false
}

// placeRogueOwner is the BEHIND circle that owns a unit of the quiet list (review round 1, item 1): among the circles
// whose entries hold the unit's entry, the nearest to it. A patrol unit whose entry no BEHIND row owns gets no owner,
// and the extra clear never runs for it; the quiet then fails and the placement stops cleanly.
func (w *walk) placeRogueOwner(u ttUnit) (raidwalk.ClearCircle, bool) {
	var best raidwalk.ClearCircle
	bestD := math.Inf(1)
	for _, c := range w.place.Circles {
		if !inEntries(c.Entries, u.Entry) {
			continue
		}
		if d := placeDist3(u.X, u.Y, u.Z, c.X, c.Y, c.Z); d < bestD {
			best, bestD = c, d
		}
	}
	return best, bestD < math.Inf(1)
}

// placeMove is spec 5 step 8: the toolkit moves the raid's group onto the point.
func (w *walk) placeMove(ctx context.Context) error {
	p := w.place.Point
	cmd := fmt.Sprintf("place %s %s %s spread=5 of=%s", num32(p.X), num32(p.Y), num32(p.Z), w.mt)
	out, err := w.tt(ctx, cmd)
	if err != nil {
		if strings.Contains(err.Error(), "in_combat") {
			return stopf(exitRaidWalkFailed, "place: a hire is in combat: the raid is not moved (never force)")
		}
		return stopf(exitRaidWalkFailed, "place: %v", err)
	}
	if body, ok := ttLast(out); ok {
		members := len(body.list("members"))
		if !body.flag("placed") {
			w.note("place: the toolkit's second read did not report the party placed (%d members listed)", members)
		} else {
			w.note("place: the toolkit's second read reports the party placed (%d members listed)", members)
		}
	}
	return nil
}

// placeMissing are the hires whose freshest frame is not on the walk's map, alive and within
// raidwalk.PlaceReviveYd of the start point.
func (w *walk) placeMissing(now time.Time, p dungeon.Point) []string {
	var out []string
	for _, name := range w.hireList() {
		b, ok := w.botAt[name]
		if !ok || now.Sub(b.at) > 10*time.Second || b.mapID != w.plan.Map || b.dead {
			out = append(out, name)
			continue
		}
		if placeDist3(b.x, b.y, b.z, p.X, p.Y, p.Z) > raidwalk.PlaceReviveYd {
			out = append(out, name)
		}
	}
	return out
}

// placeFighting are the hires the freshest frames show in combat.
func (w *walk) placeFighting(now time.Time) []string {
	var out []string
	for _, name := range w.hireList() {
		if b, ok := w.botAt[name]; ok && now.Sub(b.at) <= 10*time.Second && b.inCombat {
			out = append(out, name)
		}
	}
	return out
}

// placeView polls one frame into the walk's state (the same read waitEntered does).
func (w *walk) placeView(ctx context.Context) {
	if n, frame, err := w.view(ctx, w.afterN, 2000); err == nil && frame != nil {
		w.afterN = n
		w.trackFrame(w.env.now(), frame)
	}
}

// placeVerify is spec 5 step 9: the walk's own frames must show every hire on the point. A missing hire gets one
// `.summon` (it lands at the commander's x/y/z, in the raid's copy); still missing is a failure. The toolkit's own
// member lists are never judged: they truncate near a full raid.
func (w *walk) placeVerify(ctx context.Context) error {
	p := w.place.Point
	deadline := w.env.now().Add(placeVerifyWait)
	for {
		w.placeView(ctx)
		now := w.env.now()
		if missing := w.placeMissing(now, p); len(missing) == 0 {
			return w.placeCombatCheck(now)
		}
		if !now.Before(deadline) {
			break
		}
		if err := w.placeSleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	missing := w.placeMissing(w.env.now(), p)
	for _, name := range missing {
		if err := w.gm(ctx, ".summon "+name); err != nil {
			return err
		}
		if err := w.placeSleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
		w.placeSummoned = append(w.placeSummoned, name)
		w.note("place: summoned %s (not on the point by the frames)", name)
	}
	deadline = w.env.now().Add(placeSummonWait)
	for {
		w.placeView(ctx)
		now := w.env.now()
		missing = w.placeMissing(now, p)
		if len(missing) == 0 {
			return w.placeCombatCheck(now)
		}
		if !now.Before(deadline) {
			break
		}
		if err := w.placeSleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return stopf(exitRaidWalkFailed, "place: not at the start: %s", strings.Join(missing, ", "))
}

// placeCombatCheck fails when the frames show a hire fighting after the place (spec 5 step 9).
func (w *walk) placeCombatCheck(now time.Time) error {
	if fighting := w.placeFighting(now); len(fighting) > 0 {
		return stopf(exitRaidWalkFailed, "place: fighting after the place: %s", strings.Join(fighting, ", "))
	}
	return nil
}

// placeHold is the commander's own `cmd stay` (Q2). A refusal is not fatal: the place-to-start gap is a few seconds, so
// both stays are dropped with a note.
func (w *walk) placeHold(ctx context.Context) bool {
	rep, err := w.commander(ctx, commanderReq{Op: "cmd", Verb: "stay", WaitMS: 10000})
	if err != nil || rep.Reply.Err != nil {
		w.note("place: cmd stay was refused (%s): the raid is not held at the point", replyProblem(rep, err))
		return false
	}
	return true
}

// placeRelease sends `cmd follow` (spec 5 step 10); a refusal is fatal: the raid must walk on with the main tank.
func (w *walk) placeRelease(ctx context.Context) error {
	rep, err := w.commander(ctx, commanderReq{Op: "cmd", Verb: "follow", WaitMS: 10000})
	if err != nil {
		return stopf(exitRaidWalkFailed, "place: cmd follow: %v", err)
	}
	if rep.Reply.Err != nil {
		return stopf(exitRaidWalkFailed, "place: cmd follow: %s", rep.Reply.Err.Message)
	}
	return nil
}

// placeMTReady is the check before `run start` (spec 5.2): the main tank's freshest frame must stand within
// raidwalk.PlaceMTYd of the start point. It replaces the 5-man 80-yd check, which the raid walk never had.
func (w *walk) placeMTReady(ctx context.Context) error {
	p := w.place.Point
	deadline := w.env.now().Add(10 * time.Second)
	for {
		w.placeView(ctx)
		now := w.env.now()
		if b, ok := w.botAt[w.mt]; ok && now.Sub(b.at) <= 10*time.Second && b.mapID == w.plan.Map && !b.dead &&
			placeDist3(b.x, b.y, b.z, p.X, p.Y, p.Z) <= raidwalk.PlaceMTYd {
			return nil
		}
		if !now.Before(deadline) {
			return stopf(exitRaidWalkFailed, "place: the main tank %s is not within %d yd of the start point", w.mt, raidwalk.PlaceMTYd)
		}
		if err := w.placeSleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// placeReviveInstance is the instance check before a revive's summons (spec 6): a commander in another copy would
// summon the hires out of the raid's instance.
func (w *walk) placeReviveInstance(ctx context.Context) error {
	inst, err := w.ttInst(ctx, "the revive's instance check")
	if err != nil {
		return stopf(exitRaidWalkIncident, "not revived: the toolkit did not answer the instance check")
	}
	if inst != w.placeInst {
		return stopf(exitRaidWalkIncident, "not revived: the commander is in instance %.0f, not the raid's %.0f", inst, w.placeInst)
	}
	return nil
}

// phasePlace is phase 8b (spec 5), between the enter and the start. It ends with the raid standing on the start point
// and released, so phaseStart runs at once.
func (w *walk) phasePlace(ctx context.Context) error {
	if w.place == nil {
		return nil
	}
	p := w.place.Point
	w.placeSum = &raidwalk.PlaceSummary{Start: w.place.Start, Leg: w.place.Leg, Stop: w.place.Stop,
		Point: []float32{p.X, p.Y, p.Z}, Circles: []raidwalk.PlaceCircleSummary{}, NotCleared: []string{},
		Hires: len(w.hireList())}
	// 1. the toolkit must answer at all, before anything moves.
	probeCtx, cancel := context.WithTimeout(ctx, placeProbeTimeout)
	_, err := w.tt(probeCtx, "q inst")
	cancel()
	if err != nil {
		return stopf(exitRaidWalkFailed, "place: the Eluna test toolkit does not answer on %s (not a test image?): %v", w.opts.server, err)
	}
	// 1b. the commander must be in GM mode, before the first teleport (review round 1, item 12).
	if err := w.placeGM(ctx); err != nil {
		return err
	}
	// 2. hold the raid where it stands while the commander steps out.
	w.placeStayed = w.placeHold(ctx)
	// 3. into the raid's copy.
	if err := w.gm(ctx, ".appear "+w.mt); err != nil {
		return err
	}
	if err := w.waitMap(ctx, w.plan.Map, 30*time.Second, time.Time{}); err != nil {
		return err
	}
	if err := w.placeSleep(ctx, 2*time.Second); err != nil {
		return err
	}
	// 4. the commander and the raid must be in one instance.
	inst, err := w.placeInstance(ctx)
	if err != nil {
		return err
	}
	w.placeInst = inst
	w.placeSum.Instance = inst
	// 5. to the start point, on its floor.
	if err := w.goPlace(ctx, p.X, p.Y, p.Z+2); err != nil {
		return err
	}
	if err := w.placeSleep(ctx, 2*time.Second); err != nil {
		return err
	}
	if err := w.placeInstanceIs(ctx, w.placeInst); err != nil {
		return err
	}
	if err := w.placeFloor(ctx, p); err != nil {
		return err
	}
	// 6. the corridor of the legs the raid skips.
	if err := w.placeClear(ctx); err != nil {
		return err
	}
	// 7. the area must be quiet.
	if err := w.placeQuiet(ctx); err != nil {
		return err
	}
	// 8. the raid onto the point.
	if err := w.placeMove(ctx); err != nil {
		return err
	}
	// 9. the walk's own frames must show it.
	if err := w.placeVerify(ctx); err != nil {
		return err
	}
	// 10. re-anchor here and release.
	if w.placeStayed {
		w.placeStayed = w.placeHold(ctx)
	}
	if err := w.placeRelease(ctx); err != nil {
		return err
	}
	w.placeSum.Placed = len(w.hireList()) - len(w.placeSummoned)
	w.placeSum.Summoned = w.placeSummoned
	w.note("place: start step %d%s at %s %s %s; cleared %d rows (%d units); quiet; placed %d/%d",
		w.place.Start, legWords(w.place.Leg), num32(p.X), num32(p.Y), num32(p.Z),
		len(w.place.Circles), w.placeRemoved, w.placeSum.Placed, w.placeSum.Hires)
	return nil
}

// legWords is the " (leg <name>)" tail of the place note ("" for an explicit step).
func legWords(leg string) string {
	if leg == "" {
		return ""
	}
	return " (leg " + leg + ")"
}

// placeLeave sends the teardown's own unbind: out to the portal and `.instance unbind <plan map>`, so the commander
// does not carry a bind into the next walk. The external teardown ends the commander's agent, so these lines go out
// before it, not after (spec 5.1).
func (w *walk) placeLeave(ctx context.Context) {
	if w.plan == nil || w.plan.Portal == nil {
		return
	}
	if err := w.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(w.plan.Portal.X), num32(w.plan.Portal.Y), num32(w.plan.Portal.Z), w.plan.Portal.Map)); err != nil {
		w.note("place: the teardown's .go to the portal: %v", err)
		return
	}
	// Wait for the portal map rather than sleeping a fixed second: the unbind is a GM line whose target is the
	// commander's current position, and a `.go xyz` that has not landed yet would unbind in the instance
	// (review round 1, item 7). A failure is only a note here, as the teardown still runs.
	if err := w.waitMap(ctx, w.plan.Portal.Map, 30*time.Second, time.Time{}); err != nil {
		w.note("place: the teardown's wait for the portal map: %v", err)
	}
	if err := w.gm(ctx, fmt.Sprintf(".instance unbind %d", w.plan.Map)); err != nil {
		w.note("place: the teardown's .instance unbind: %v", err)
	}
}

// placeTTMax is the longest toolkit line the place phase will build for this plan (with the plan's map), so validate
// can refuse a plan whose skip list or geometry would overflow one chat line before any GM command runs
// (review round 1, item 11).
func placeTTMax(p *raidwalk.PlacePlan, planMap uint32) (string, int) {
	long, n := "", 0
	for _, l := range placeTTLines(p, planMap) {
		if len(l) > n {
			long, n = l, len(l)
		}
	}
	return long, n
}

// placeTTLines is every toolkit line the place phase sends, with a placeholder main-tank name (a hire's name is at most
// 12 characters, gmNameRe), so the check is a true upper bound.
func placeTTLines(p *raidwalk.PlacePlan, planMap uint32) []string {
	const mt = "MMMMMMMMMMMM"
	tail := ""
	if w := raidwalk.SkipWords(p.Skips); w != "" {
		tail = " skip=" + w
	}
	px, py, pz := num32(p.Point.X), num32(p.Point.Y), num32(p.Point.Z)
	out := []string{
		"q inst",
		"q group of=" + mt,
		fmt.Sprintf("q floor %s %s %s", px, py, pz),
		fmt.Sprintf("quiet %d %s %s %s w=%d dz=%d%s", placeQuietR, px, py, pz, placeQuietWaitS, placeClearDz, tail),
		fmt.Sprintf("place %s %s %s spread=5 of=%s", px, py, pz, mt),
	}
	for _, c := range p.Circles {
		out = append(out,
			fmt.Sprintf("q units %s %s %s %s all", num32(c.Radius), num32(c.X), num32(c.Y), num32(c.Z)),
			fmt.Sprintf("clearto %d %s %s %s %s dz=%d all=1%s", planMap, num32(c.X), num32(c.Y), num32(c.Z), num32(c.Radius), placeClearDz, tail),
		)
	}
	return out
}

// placeDist3 is the 3D distance between two points.
func placeDist3(ax, ay, az, bx, by, bz float32) float64 {
	dx, dy, dz := float64(ax-bx), float64(ay-by), float64(az-bz)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
