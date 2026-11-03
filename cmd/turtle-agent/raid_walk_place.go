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
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
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
	// placeRogueYd is how close to a BEHIND circle a named unit must stand for the one extra clear round (spec 5 step 7):
	// the same slack a BEHIND pull row gives its own home (raidwalk.PlaceOwnYd).
	placeRogueYd = raidwalk.PlaceOwnYd
	// placeExtraRadius is the radius of that extra round.
	placeExtraRadius = 3
	// placeClearDz is the vertical window of the clear, its pre-read and the quiet (the toolkit's dz=).
	placeClearDz = 8
	// placeQuietWaitS is the quiet's w=: the two reads are this many seconds apart (spec 5 step 7).
	placeQuietWaitS = 20
	// placeQuietR is the quiet's radius around the start point (spec 5 step 7).
	placeQuietR = 50
	// placeReachYd is how close to the start point any unit blocks the placement: the reach of `.tt place` with its
	// spread-5 ring. Beyond it only a BEHIND-owned unit matters (item 3 of the proof findings).
	placeReachYd = 25
	// placeRoamScopeYd is the radius of the join read that gives the quiet's roamers a position: the toolkit's own
	// roamer scope (CFG quiet_roam_r 80), so every roamer the toolkit names can be found (item 4).
	placeRoamScopeYd = 80
	// placeQuietRereads is how many times the quiet is read again (placeQuietWaitS apart) when the only units blocking
	// it are moving patrols, before the placement fails (item 5).
	placeQuietRereads = 2
	// placeQuietExtraRounds is how many extra clear rounds the quiet may run (walk 24: one round left the bats that
	// flew in after it).
	placeQuietExtraRounds = 2
	// placeLandingReadYd and placeLandingDz bound the M1b landing read (PLACE-W26): what `q units` radius the walk
	// re-reads at the end of a clean quiet, and how far off the point's floor a unit may stand to be taken. On Mar'li
	// at the end of the quiet (the `.go` +22 s) the Berserker is about 36 yd out and 13 yd below.
	placeLandingReadYd = 45
	placeLandingDz     = 30
	// placeCritterHP is the top of a critter's max-HP band: a rank-0 unit with at most this many hit points (a level-1
	// critter) never blocks the clear or the quiet (item 2).
	placeCritterHP = 100
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

// flagOf reads a boolean field of a list element (absent or not a bool: false).
func flagOf(m map[string]json.RawMessage, key string) bool {
	var v bool
	if raw, ok := m[key]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v
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

// ttUnit is one unit of a `q units` or `quiet` reply, reduced to what the place decisions need. The toolkit sends the
// unit's home (its spawn point, `home`), its spawn guid (`db`), its health pair (`hpv`) and its rank (`rk`) for every
// creature (tt_lib.ext unitSnap), and `d`/`dz` when it was asked about a point.
type ttUnit struct {
	GUID  string
	Entry uint32
	Name  string
	X     float32
	Y     float32
	Z     float32
	Dz    float32
	Dist  float32
	// Home is the unit's home position (its spawn point) and HomeOK says the reply carried one: the BEHIND owner check
	// gives a unit without one no owner (fail closed).
	Home   [3]float32
	HomeOK bool
	// Spawn is the spawn's guid low (`db`): the notes carry it so a live run can match a unit against the spawn export.
	Spawn uint32
	// Dead is the toolkit's `dead` field: a corpse (bossKilled's toolkit read asks for the dead units too).
	Dead  bool
	MaxHP float64
	Rank  int
}

// ttUnitOf reduces one element of a reply's unit list.
func ttUnitOf(m map[string]json.RawMessage) ttUnit {
	u := ttUnit{GUID: strOf(m, "g"), Name: strOf(m, "n")}
	if e, ok := numOf(m, "e"); ok {
		u.Entry = uint32(e)
	}
	if dz, ok := numOf(m, "dz"); ok {
		u.Dz = float32(dz)
	}
	if d, ok := numOf(m, "d"); ok {
		u.Dist = float32(d)
	}
	if db, ok := numOf(m, "db"); ok {
		u.Spawn = uint32(db)
	}
	if rk, ok := numOf(m, "rk"); ok {
		u.Rank = int(rk)
	}
	u.Dead = flagOf(m, "dead")
	if hpv := numList(m, "hpv"); len(hpv) >= 2 {
		u.MaxHP = hpv[1]
	}
	u.X, u.Y, u.Z, _ = posOf(m, "p")
	u.Home[0], u.Home[1], u.Home[2], u.HomeOK = posOf(m, "home")
	return u
}

// numList reads a list-of-numbers field of a reply element (the toolkit's `hpv` is {hp, maxhp}).
func numList(m map[string]json.RawMessage, key string) []float64 {
	raw, found := m[key]
	if !found {
		return nil
	}
	var out []float64
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// critter says the unit is a level-1 critter: rank 0 with at most placeCritterHP hit points. A critter never blocks the
// clear or the quiet (item 2): it is not what the toolkit's quiet rule means to protect. A reply without `hpv` is never
// a critter.
func (u ttUnit) critter() bool {
	return u.MaxHP > 0 && u.MaxHP <= placeCritterHP && u.Rank == 0
}

// placeHome is the position the refusal reason judges a unit by: the unit's home (its spawn point) when the reply
// carried one, else where it stands. Ownership never falls back: a reply without a home gives no owner (fail closed).
func (u ttUnit) placeHome() (x, y, z float32) {
	if u.HomeOK {
		return u.Home[0], u.Home[1], u.Home[2]
	}
	return u.X, u.Y, u.Z
}

// placeWhere describes a unit for the notes: the spawn guid the reply carried, where it stands now and, when it carried
// one, its home. The first live run confirms `home` is the spawn point by matching these against the spawn export.
func (u ttUnit) placeWhere() string {
	where := fmt.Sprintf("db %d at %s %s %s, ", u.Spawn, num32(u.X), num32(u.Y), num32(u.Z))
	if !u.HomeOK {
		return where + "home none"
	}
	return where + fmt.Sprintf("home %s %s %s", num32(u.Home[0]), num32(u.Home[1]), num32(u.Home[2]))
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
		params := map[string]any{"since": w.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": 1000}
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
		units = append(units, ttUnitOf(m))
	}
	return units, body.flag("trunc"), nil
}

// placeCircleBlocked says why a circle must not be cleared ("" when it may): a unit the row does not own (unless it is
// BEHIND trash, D5), a unit that stands in an AHEAD row's circle, or a pre-read the toolkit had to cut.
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
		if u.critter() {
			// A level-1 critter is not what the corridor protects (item 2): the clear may take it along, as it would
			// have before the toolkit listed it.
			continue
		}
		if !inEntries(c.Entries, u.Entry) && !w.placeBehindTrash(u) {
			return fmt.Sprintf("unit %d %s stands in the circle (%s)", u.Entry, u.Name, u.placeWhere())
		}
		if k, inside := w.place.AheadIn(u.X, u.Y, u.Z); inside {
			return fmt.Sprintf("unit %d %s stands in AHEAD row %d (%s)", u.Entry, u.Name, k, u.placeWhere())
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
			PerUnit: c.PerUnit, Unit: c.Unit, Sweep: c.Sweep, Moved: moved}
		if why := w.placeCircleBlocked(c, units, trunc); why != "" {
			sweep := ""
			if c.Sweep {
				sweep = " (sweep)"
			}
			w.note("place: not cleared row %d%s: %s", c.Step, sweep, why)
			w.placeSum.NotCleared = append(w.placeSum.NotCleared, fmt.Sprintf("row %d: %s", c.Step, why))
			w.placeSum.Circles = append(w.placeSum.Circles, summary)
			// The quiet step's extra round must not act near a circle the corridor refused (review round 2, MEDIUM-A).
			// A refused sweep circle blocks extra clears near it like a corridor circle.
			w.placeSkipped = append(w.placeSkipped, c)
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
		sweep := ""
		if c.Sweep {
			sweep = " (sweep)"
		}
		w.note("place: cleared row %d%s: %d removed%s", c.Step, sweep, int(removed), skippedWords(summary.Skipped))
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

// placeQuiet is spec 5 step 7: the area around the start point must be clear before the raid lands in it. The walk
// judges it itself (item 3): the toolkit's `quiet` reply is the raw material, not the verdict. A unit blocks the
// placement when it is not a skip entry, not a critter and
//
//	(a) it stands within placeReachYd of the start point (the opening the raid lands in), or
//	(b) a BEHIND row owns its home: a corridor circle within its radius + placeRogueYd, or, as BEHIND trash inside
//	    placeQuietR, a pull row before the start within its radius + 6 yd whose entries hold the unit (then the extra
//	    clear rounds take it: placeQuietExtraRounds of them).
//
// AHEAD-owned and unowned units beyond placeReachYd never block: a door-to-end run at this step has them alive too, so
// counting them would make the test stricter than the walk it stands in for. A cut near list whose farthest listed unit
// still stands inside placeReachYd refuses at once (D3). A unit no extra round could take and that stands beyond
// placeReachYd is left alive with a note (D6): only one inside the reach still blocks. When only moving patrols are
// left, the quiet is read again up to placeQuietRereads times (item 5) before the placement fails.
func (w *walk) placeQuiet(ctx context.Context) error {
	p := w.place.Point
	skip := skipArg(w.place.Skips)
	var j placeQuietJudge
	rounds, rereads := 0, 0
	for {
		read, err := w.placeQuietOnce(ctx, p, skip)
		if err != nil {
			return err
		}
		if read.NearCut {
			w.placeSum.Quiet = false
			return stopf(exitRaidWalkFailed, "place: the area is not quiet: the quiet's near list was cut at %.1f yd, inside the %d yd landing reach", read.NearCutYd, placeReachYd)
		}
		j = w.placeQuietJudge(read, p)
		var left []ttUnit // units a round could not take, or that no round is left for
		if len(j.Clear) > 0 && rounds < placeQuietExtraRounds {
			rounds++
			cleared := 0
			for _, u := range j.Clear {
				took, err := w.placeQuietClear(ctx, u, skip)
				if err != nil {
					return err
				}
				if took {
					cleared++
				} else {
					left = append(left, u)
				}
			}
			if cleared > 0 {
				w.note("place: the extra round (%d of %d) cleared %d unit(s); reading the quiet again", rounds, placeQuietExtraRounds, cleared)
				continue
			}
		} else {
			left = j.Clear
		}
		j.Clear = nil
		for _, u := range left {
			if float64(u.Dist) <= placeReachYd {
				j.Block = append(j.Block, u)
				continue
			}
			// D6: beyond the landing reach a unit no round could take is left alive (a door-to-end run at this step
			// would have it alive too).
			w.note("place: the quiet leaves %d %s %.1f yd out alive: no extra round can take it and it is beyond the %d yd landing reach (%s)",
				u.Entry, u.Name, u.Dist, placeReachYd, u.placeWhere())
		}
		if len(j.Block) == 0 {
			w.placeSum.Quiet = true
			if !read.Toolkit {
				w.note("place: the toolkit's own quiet flag is false, but every unit it listed is a critter, a skip entry, "+
					"a BEHIND leftover the extra round took, or beyond %.0f yd, a roamer beyond it that the cut join read missed: "+
					"the walk places the raid", float64(placeReachYd))
			}
			// M1b: the quiet's window (dz 8) misses a patroller between its spawn and the landing. The landing read
			// (r 45, no floor window) nets BEHIND trash off the floor after a clean quiet.
			return w.placeLandingRead(ctx, p, skip)
		}
		if rereads >= placeQuietRereads || !read.roamersOnly(j.Block) {
			w.placeSum.Quiet = false
			return stopf(exitRaidWalkFailed, "place: the area is not quiet: %s", strings.Join(placeQuietNames(j.Block), ", "))
		}
		rereads++
		w.note("place: the quiet's only blockers are moving patrols; reading it again (%d of %d)", rereads, placeQuietRereads)
		if err := w.placeSleep(ctx, placeQuietWaitS*time.Second); err != nil {
			return err
		}
	}
}

// placeLandingRead is the M1b net for BEHIND trash off the quiet's floor window (PLACE-W26): a `q units 45 <p> all`
// after a clean quiet. For each unit: skip it when the quiet judged it (|Dz| <= placeClearDz), when it stands
// farther than placeLandingDz off the floor, skip entries/critters/dead units and anything beyond 45 yd. Otherwise,
// when placeRogueOwner owns it or it is placeBehindTrash, run the existing extra clear (placeQuietClear), which
// pre-reads, guards and clears 3 yd at the unit's position. Any other unit is left with no note (an AHEAD or unowned
// unit off the floor is what a door run meets too). A cut read is fail-open: the quiet's own verdict stands. The read
// does not count against placeQuietExtraRounds.
func (w *walk) placeLandingRead(ctx context.Context, p dungeon.Point, skip string) error {
	units, trunc, err := w.placeUnits(ctx, placeLandingReadYd, p.X, p.Y, p.Z)
	if err != nil {
		return err
	}
	if trunc {
		w.note("place: the landing read (r %d) was cut", placeLandingReadYd)
		return nil
	}
	for _, u := range units {
		if math.Abs(float64(u.Dz)) <= placeClearDz {
			continue
		}
		if math.Abs(float64(u.Dz)) > placeLandingDz {
			continue
		}
		if placeDist3(u.X, u.Y, u.Z, p.X, p.Y, p.Z) > placeLandingReadYd {
			continue
		}
		if inEntries(w.place.Skips, u.Entry) || u.critter() || u.Dead {
			continue
		}
		if _, owned := w.placeRogueOwner(u); !owned && !w.placeBehindTrash(u) {
			continue
		}
		took, err := w.placeQuietClear(ctx, u, skip)
		if err != nil {
			return err
		}
		if !took {
			continue
		}
		below := "below"
		if u.Dz > 0 {
			below = "above"
		}
		w.note("place: the landing read takes %d %s (%.1f yd, %.1f yd %s, %s)", u.Entry, u.Name,
			placeDist3(u.X, u.Y, u.Z, p.X, p.Y, p.Z), math.Abs(float64(u.Dz)), below, u.placeWhere())
	}
	return nil
}

// placeQuietNames names the units that block the quiet, as the refusal and the notes read them.
func placeQuietNames(units []ttUnit) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		name := u.Name
		if name == "" {
			name = "roamer"
		}
		out = append(out, fmt.Sprintf("%d %s %.1f yd", u.Entry, name, u.Dist))
	}
	return out
}

// placeQuietRead is one `quiet` answer: the toolkit's own verdict, its near list, and its roamers joined to their
// positions (item 4). The walk judges the near list and the join itself; the toolkit's flag is not the verdict.
type placeQuietRead struct {
	// Toolkit is the toolkit's own quiet flag (`#near1 == 0 and #near2 == 0 and #roamers == 0`), kept for the notes only.
	Toolkit bool
	// Near is the near list of the final read followed by read 1's (placeQuietR yd, the clear's dz window): both are
	// judged, a unit of both keeps the final read's position.
	Near    []ttUnit
	Roam    []ttUnit   // the roamers, with the position, home and vitals the join read gave them
	Missing []ttRoamer // roamers the join read did not list (they block: the walk cannot judge them)
	JoinCut bool       // the join read was truncated: it may have missed roamers
	// NearCut says the quiet's near list was cut (len(near) < n_near) and the farthest unit still listed stood inside
	// the landing reach: the units it dropped may stand where the raid lands, so the placement is refused (D3).
	NearCut bool
	// NearCutYd is the toolkit's own distance of that farthest listed unit (0 when the cut list was empty).
	NearCutYd float64
}

// ttRoamer is one element of a quiet reply's `roamers` list: the toolkit sends g, e, why, d and moved, and no position
// (tt_commands.lua's quiet).
type ttRoamer struct {
	GUID  string
	Entry uint32
	Why   string
	Moved float32
	Dist  float32
}

// roamersOnly says every blocker is one of the read's roamers: a unit that moved between the two reads (a patrol walking
// through), which a re-read may find gone (item 5). A roamer the join read missed counts too (D4).
func (r *placeQuietRead) roamersOnly(block []ttUnit) bool {
	if len(block) == 0 {
		return false
	}
	moved := map[string]bool{}
	for _, u := range r.Roam {
		moved[strings.ToUpper(u.GUID)] = true
	}
	// D4: a roamer the join read missed counts as a roamer too: inside the reach it is a unit that moved, so it
	// qualifies for the re-reads (a patrol walking through gets its two re-reads).
	for _, m := range r.Missing {
		moved[strings.ToUpper(m.GUID)] = true
	}
	for _, u := range block {
		if !moved[strings.ToUpper(u.GUID)] {
			return false
		}
	}
	return true
}

// nearUnitsOf reduces a reply's near list to the units the judgement sees.
func nearUnitsOf(b ttBody) []ttUnit {
	var out []ttUnit
	for _, m := range b.list("near") {
		out = append(out, ttUnitOf(m))
	}
	return out
}

// nearCutOf records a cut near list (F2, D3): n_near is the toolkit's own count before its line cap dropped the far
// end. The near list is distance-sorted, so the last listed unit is the nearest of the dropped ones; when it stands
// inside the landing reach (placeReachYd itself counts as inside) the placement is refused (the dropped units may
// stand where the raid lands).
func (r *placeQuietRead) nearCutOf(b ttBody, units []ttUnit) {
	n, ok := b.num("n_near")
	if !ok || n <= float64(len(units)) {
		return
	}
	d := 0.0
	if len(units) > 0 {
		d = float64(units[len(units)-1].Dist)
	}
	// The reach itself counts as inside: a unit listed at exactly placeReachYd still stands where the raid lands.
	if d > placeReachYd {
		return
	}
	if !r.NearCut || d < r.NearCutYd {
		r.NearCutYd = d
	}
	r.NearCut = true
}

// placeQuietOnce runs one `quiet` and, when it names roamers, one `q units` read around the start point that gives them
// the position, home and vitals the toolkit's roamer list leaves out (item 4). A roamer the join read missed is also
// looked up in the quiet's own near lists (D2): only one found in neither is Missing.
func (w *walk) placeQuietOnce(ctx context.Context, p dungeon.Point, skip string) (*placeQuietRead, error) {
	cmd := fmt.Sprintf("quiet %d %s %s %s w=%d dz=%s%s", placeQuietR, num32(p.X), num32(p.Y), num32(p.Z), placeQuietWaitS, num32(placeClearDz), skip)
	out, err := w.tt(ctx, cmd)
	if err != nil {
		return nil, stopf(exitRaidWalkFailed, "place: quiet: %v", err)
	}
	body, ok := ttLast(out)
	if !ok {
		return nil, stopf(exitRaidWalkFailed, "place: quiet gave no reply")
	}
	read := &placeQuietRead{Toolkit: body.flag("quiet")}
	final := nearUnitsOf(body)
	read.Near = append(read.Near, final...)
	read.nearCutOf(body, final)
	// A `quiet w=20` answers twice: read 1 (the state at the command) and read 2 (placeQuietWaitS later, the final
	// reply). The walk judges the units of both reads, as the toolkit's own rule counts both: a unit that stood in the
	// landing reach 20 s before the raid lands is judged by read 1's position (a unit of both reads keeps the fresher,
	// final one: the judgement dedupes by guid).
	if len(out.Replies) > 1 {
		var first map[string]json.RawMessage
		if err := json.Unmarshal(out.Replies[0], &first); err == nil && first != nil {
			fb := ttBody{raw: first}
			one := nearUnitsOf(fb)
			read.Near = append(read.Near, one...)
			read.nearCutOf(fb, one)
		}
	}
	var roamers []ttRoamer
	for _, m := range body.list("roamers") {
		r := ttRoamer{GUID: strOf(m, "g"), Why: strOf(m, "why")}
		if e, ok := numOf(m, "e"); ok {
			r.Entry = uint32(e)
		}
		if d, ok := numOf(m, "d"); ok {
			r.Dist = float32(d)
		}
		if mv, ok := numOf(m, "moved"); ok {
			r.Moved = float32(mv)
		}
		roamers = append(roamers, r)
	}
	if len(roamers) == 0 {
		return read, nil
	}
	units, cut, err := w.placeUnits(ctx, placeRoamScopeYd, p.X, p.Y, p.Z)
	if err != nil {
		return nil, err
	}
	byGUID := make(map[string]ttUnit, len(units)+len(read.Near))
	// The join read first: its position, home and vitals are fresher than the quiet's own near list.
	for _, u := range units {
		byGUID[strings.ToUpper(u.GUID)] = u
	}
	// D2: a roamer the cut join read missed is also looked up in the quiet's own near lists (they carry position and
	// home). Only a roamer found nowhere is Missing.
	for _, u := range read.Near {
		if k := strings.ToUpper(u.GUID); k != "" {
			if _, ok := byGUID[k]; !ok {
				byGUID[k] = u
			}
		}
	}
	read.JoinCut = cut
	for _, r := range roamers {
		if u, ok := byGUID[strings.ToUpper(r.GUID)]; ok {
			read.Roam = append(read.Roam, u)
			continue
		}
		read.Missing = append(read.Missing, r)
	}
	return read, nil
}

// placeQuietJudge is the walk's own verdict over one quiet read (item 3): the units the one extra clear round may take
// (their home lies in a BEHIND row's circle) and the units that block the placement.
type placeQuietJudge struct {
	Clear []ttUnit
	Block []ttUnit
}

// placeQuietSight is one unit the judgement sees: a unit of the read's near list or a roamer, with the distance the walk
// measured itself and whether the toolkit listed it as one that moved.
type placeQuietSight struct {
	unit ttUnit
	dist float64
	roam bool
}

// placeQuietJudge decides which units block the placement and which the extra round may clear (item 3). Every unit it
// acts on or refuses is logged with its home and where it stands, so the next live run can confirm `home` is the unit's
// spawn point; a roamer the join read did not list blocks (the walk cannot judge it: item 4). A unit without a home
// beyond the landing reach is ignored but still logged, so a missing `home` shows in the notes.
func (w *walk) placeQuietJudge(read *placeQuietRead, p dungeon.Point) placeQuietJudge {
	var j placeQuietJudge
	if read.JoinCut {
		w.note("place: the quiet's join read (r %d) was cut: it may have missed roamers", placeRoamScopeYd)
	}
	for _, r := range read.Missing {
		if r.Dist > placeReachYd {
			// D1: the cut join read dropped the farthest units; a missing roamer beyond the landing reach is ignored
			// (a door-to-end run at this step would have it alive too), with a note.
			w.note("place: the quiet ignores roamer %d %.1f yd out (%s, moved %.1f): the cut join read missed it, and it is beyond the %d yd landing reach",
				r.Entry, r.Dist, r.Why, r.Moved, placeReachYd)
			continue
		}
		w.note("place: the quiet's roamer %d (%s, %.1f yd out, moved %.1f) is not in the join read: it blocks",
			r.Entry, r.Why, r.Dist, r.Moved)
		j.Block = append(j.Block, ttUnit{GUID: r.GUID, Entry: r.Entry, Dist: r.Dist})
	}
	var sights []placeQuietSight
	at := map[string]int{}
	add := func(u ttUnit, roam bool) {
		key := strings.ToUpper(u.GUID)
		if i, ok := at[key]; ok {
			sights[i].roam = sights[i].roam || roam
			if roam {
				// The join read is fresher than the quiet's near list: a roamer that moved into the landing
				// reach between the reads is judged at its new position.
				sights[i].unit = u
				sights[i].dist = placeDist2(u.X, u.Y, p.X, p.Y)
			}
			return
		}
		at[key] = len(sights)
		sights = append(sights, placeQuietSight{unit: u, dist: placeDist2(u.X, u.Y, p.X, p.Y), roam: roam})
	}
	for _, u := range read.Near {
		add(u, false)
	}
	for _, u := range read.Roam {
		add(u, true)
	}
	for _, s := range sights {
		u := s.unit
		u.Dist = float32(s.dist)
		switch {
		case inEntries(w.place.Skips, u.Entry):
			// The quiet and every clearto carry skip=<those entries>: the toolkit neither counts nor touches them.
		case u.critter():
			w.note("place: the quiet ignores the critter %d %s (%s)", u.Entry, u.Name, u.placeWhere())
		default:
			if _, owned := w.placeRogueOwner(u); owned {
				w.note("place: the quiet's extra round takes %d %s (%.1f yd%s, %s)", u.Entry, u.Name, s.dist, roamWords(s.roam), u.placeWhere())
				j.Clear = append(j.Clear, u)
				continue
			}
			if w.placeBehindTrash(u) && s.dist <= placeQuietR {
				// D5: trash of a skipped leg inside the quiet's own radius: the extra round may take it (walk 24
				// test2-arlokk's Cub and Tiger, m910q-ven's bats).
				w.note("place: the quiet's extra round takes %d %s (%.1f yd%s, trash of a skipped leg, %s)", u.Entry, u.Name, s.dist, roamWords(s.roam), u.placeWhere())
				j.Clear = append(j.Clear, u)
				continue
			}
			if s.dist <= placeReachYd {
				w.note("place: the quiet refuses %d %s: %s (%s)", u.Entry, u.Name, w.placeQuietBlock(u, s.dist), u.placeWhere())
				j.Block = append(j.Block, u)
			} else if !u.HomeOK {
				// Beyond the landing reach an unowned unit is ignored, but a missing home is still noted: the extra
				// round never clears a unit without a spawn (fail closed), and the live run can tell the two apart.
				w.note("place: the quiet ignores %d %s %.1f yd out: no BEHIND row owns a unit with no home (%s)",
					u.Entry, u.Name, s.dist, u.placeWhere())
			}
		}
	}
	return j
}

// roamWords says how the toolkit reported a unit of the quiet list that moved between the two reads.
func roamWords(roam bool) string {
	if roam {
		return ", moved between the reads"
	}
	return ""
}

// placeQuietBlock is the refusal's reason for a unit inside the landing reach: the row its home lies in, so the live run
// can tell an AHEAD pack (a risk the door-to-end run takes too) from a leftover of a leg the raid skips.
func (w *walk) placeQuietBlock(u ttUnit, dist float64) string {
	hx, hy, hz := u.placeHome()
	if k, own := w.place.AheadOwns(u.Entry, hx, hy, hz); own {
		return fmt.Sprintf("it stands %.1f yd from the start point and its home is in AHEAD row %d", dist, k)
	}
	return fmt.Sprintf("it stands %.1f yd from the start point and no BEHIND row owns the unit", dist)
}

// placeQuietClear is the quiet's one extra clear round for a BEHIND-owned unit (review round 1, item 1). It is a clear
// like any other: it keeps the pre-read's answer and runs the same guard before it acts. A unit within a skipped
// corridor circle's radius + placeRogueYd is not cleared (review round 2, MEDIUM-A): the corridor refused that circle
// for a reason the extra round cannot see from its r 3 read. A unit no circle owns by home but that is BEHIND trash
// (D5) is cleared where it stands. took says the clearto went out.
func (w *walk) placeQuietClear(ctx context.Context, u ttUnit, skip string) (took bool, err error) {
	owner, owned := w.placeRogueOwner(u)
	trash := false
	if !owned {
		if !w.placeBehindTrash(u) {
			w.note("place: not cleared %d %s in the extra round: no BEHIND row owns the unit (%s)", u.Entry, u.Name, u.placeWhere())
			return false, nil
		}
		// D5: BEHIND trash no circle owns by home: the extra round clears it where it stands, a BEHIND pull row
		// holding its entry and owning its home outside every AHEAD row (review round 3: positive ownership).
		trash = true
	}
	if sc, near := w.placeNearSkipped(u); near {
		w.note("place: not cleared %d %s in the extra round: it stands within %d yd of the skipped circle of row %d (%s)",
			u.Entry, u.Name, placeRogueYd, sc.Step, u.placeWhere())
		return false, nil
	}
	c := raidwalk.ClearCircle{Step: owner.Step, Leg: owner.Leg, X: u.X, Y: u.Y, Z: u.Z,
		Radius: placeExtraRadius, Entries: owner.Entries}
	if trash {
		c = raidwalk.ClearCircle{Step: 0, X: u.X, Y: u.Y, Z: u.Z, Radius: placeExtraRadius, Entries: []uint32{u.Entry}}
	}
	units, trunc, err := w.placeUnits(ctx, placeExtraRadius, u.X, u.Y, u.Z)
	if err != nil {
		return false, err
	}
	if why := w.placeCircleBlocked(c, units, trunc); why != "" {
		w.note("place: not cleared %d %s in the extra round: %s", u.Entry, u.Name, why)
		return false, nil
	}
	cmd := fmt.Sprintf("clearto %d %s %s %s %d dz=%s all=1%s", w.plan.Map, num32(u.X), num32(u.Y), num32(u.Z), placeExtraRadius, num32(placeClearDz), skip)
	if _, err := w.tt(ctx, cmd); err != nil {
		w.note("place: the extra clear of %d %s: %v", u.Entry, u.Name, err)
		return false, nil
	}
	if trash {
		w.note("place: cleared %d %s in the extra round: trash of a skipped leg (a BEHIND row holds its entry and owns its home, outside every AHEAD row) (%s)", u.Entry, u.Name, u.placeWhere())
	} else {
		w.note("place: cleared %d %s in the extra round: its home is in the circle of row %d (%s)", u.Entry, u.Name, owner.Step, u.placeWhere())
	}
	return true, nil
}

// placeBehindTrash says the unit is trash of a leg the raid skips (walk 24, Venoxis and Arlokk). All four must hold:
//   - the reply carried its home (fail closed without one);
//   - a BEHIND pull row holds its entry AND owns that home (PlacePlan.BehindOwns: any pull row before the start step,
//     within its radius + 6 yd, not only the corridor's circles). An entry alone is not ownership: entries are shared,
//     so a later leg's patrol can carry a corridor row's entry while spawning outside every BEHIND row's area;
//   - it is not boss evidence where it stands or at its home (BossRows.Counts: Thekal's 11361 tigers count only within
//     40 yd of Thekal);
//   - its home lies in no AHEAD row's area (PlacePlan.AheadIn): a wanderer of the packs the run fights next is never trash.
//
// A door-to-end run has killed such a unit, or meets it on the way to the start point.
func (w *walk) placeBehindTrash(u ttUnit) bool {
	if !u.HomeOK || w.place == nil {
		return false
	}
	if _, owned := w.place.BehindOwns(u.Entry, u.Home[0], u.Home[1], u.Home[2]); !owned {
		return false
	}
	if w.bossRule != nil && (w.bossRule.Counts(u.Entry, u.X, u.Y) || w.bossRule.Counts(u.Entry, u.Home[0], u.Home[1])) {
		return false
	}
	if _, in := w.place.AheadIn(u.Home[0], u.Home[1], u.Home[2]); in {
		return false
	}
	return true
}

// placeRogueOwner is the BEHIND circle that owns a unit of the quiet list (review round 1, item 1): among the circles
// whose entries hold the unit's entry and whose area (radius + placeRogueYd) holds the unit's home (item 1: the spawn
// point; a reply without a home gives no owner), the nearest to it. A unit whose entry no BEHIND row owns,
// or whose home lies outside every circle that does, gets no owner: the extra clear never runs for it, and the quiet
// then fails unless the unit is beyond the landing reach.
func (w *walk) placeRogueOwner(u ttUnit) (raidwalk.ClearCircle, bool) {
	// No home in the reply: the unit's spawn is unknown, so no BEHIND row owns it (fail closed). Its current position
	// could be a later leg's patrol standing in a BEHIND circle, and ownership is what authorises a clearto.
	if !u.HomeOK {
		return raidwalk.ClearCircle{}, false
	}
	hx, hy, hz := u.placeHome()
	var best raidwalk.ClearCircle
	bestD := math.Inf(1)
	for _, c := range w.place.Circles {
		if !inEntries(c.Entries, u.Entry) {
			continue
		}
		d := placeDist3(hx, hy, hz, c.X, c.Y, c.Z)
		if d > float64(c.Radius)+placeRogueYd {
			continue
		}
		if d < bestD {
			best, bestD = c, d
		}
	}
	return best, bestD < math.Inf(1)
}

// placeDist2 is the 2D distance between two points.
func placeDist2(ax, ay, bx, by float32) float64 {
	dx, dy := float64(ax-bx), float64(ay-by)
	return math.Sqrt(dx*dx + dy*dy)
}

// placeNearSkipped says whether a unit stands within a skipped corridor circle's radius + placeRogueYd, and which one.
func (w *walk) placeNearSkipped(u ttUnit) (raidwalk.ClearCircle, bool) {
	for _, c := range w.placeSkipped {
		if placeDist3(u.X, u.Y, u.Z, c.X, c.Y, c.Z) <= float64(c.Radius)+placeRogueYd {
			return c, true
		}
	}
	return raidwalk.ClearCircle{}, false
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

// placeView polls one frame into the walk's state (the same read waitEntered does), and saves it, so frames.jsonl
// holds the place phase (M2: walk 26's place frames were empty, and the summary could only guess the fight).
func (w *walk) placeView(ctx context.Context) {
	if n, frame, err := w.view(ctx, w.afterN, 2000); err == nil && frame != nil {
		w.afterN = n
		w.trackFrame(w.env.now(), frame)
		w.saveFrame(w.env.now(), frame)
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

// placeCombatCheck fails when the frames show a hire fighting after the place (spec 5 step 9). M2 names the enemy:
// every hostile seen within 10 s that targets a hire (else every hostile with hppct < 100 or a target), with its
// entry, spawn guid low and position, each with its BEHIND/AHEAD verdict by position (frames carry no home).
func (w *walk) placeCombatCheck(now time.Time) error {
	if fighting := w.placeFighting(now); len(fighting) > 0 {
		return stopf(exitRaidWalkFailed, "place: fighting after the place: %s%s", strings.Join(fighting, ", "), w.placeEnemy(now))
	}
	return nil
}

// placeEnemy names the hostiles the frames show around the post-place fight: those seen within 10 s that target a
// hire, else those seen within 10 s with hppct < 100 or a target. Each reads "<entry> (db <guid low>) at <x> <y> <z>
// (<verdict>)", where the verdict is the AHEAD/BEHIND judgement by position (frames carry no home). Unknown enemies
// read "against an unknown unit (no hostile in the frames)".
func (w *walk) placeEnemy(now time.Time) string {
	hire := map[uint64]bool{}
	for _, b := range w.botAt {
		if b.guid != 0 {
			hire[uint64(b.guid)] = true
		}
	}
	type foe struct {
		guid  string
		state hostileState
	}
	var onHire, engaged []foe
	for guid, h := range w.hostiles {
		if now.Sub(h.at) > 10*time.Second {
			continue
		}
		if h.target != 0 && hire[h.target] {
			onHire = append(onHire, foe{guid, h})
			continue
		}
		if h.target != 0 || h.hppct < 100 {
			engaged = append(engaged, foe{guid, h})
		}
	}
	foes := onHire
	if len(foes) == 0 {
		foes = engaged
	}
	if len(foes) == 0 {
		return "; against an unknown unit (no hostile in the frames)"
	}
	sort.Slice(foes, func(i, j int) bool {
		if foes[i].state.entry != foes[j].state.entry {
			return foes[i].state.entry < foes[j].state.entry
		}
		return foes[i].guid < foes[j].guid
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range foes {
		low := uint32(0)
		if g, err := packet.ParseGUID(f.guid); err == nil {
			low = uint32(uint64(g) & 0xFFFFFF)
		}
		key := fmt.Sprintf("%d/%d", f.state.entry, low)
		if seen[key] {
			continue
		}
		seen[key] = true
		verdict := "unowned"
		if _, own := w.place.AheadOwns(f.state.entry, f.state.x, f.state.y, f.state.z); own {
			verdict = "AHEAD"
		} else if _, own := w.place.BehindOwns(f.state.entry, f.state.x, f.state.y, f.state.z); own {
			verdict = "BEHIND"
		}
		out = append(out, fmt.Sprintf("%d (db %d) at %s %s %s (%s)", f.state.entry, low,
			num32(f.state.x), num32(f.state.y), num32(f.state.z), verdict))
	}
	return "; against " + strings.Join(out, ", ")
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

// placeStillJoined says the placed walk's commander is still in the raid's copy: map and toolkit instance both match
// what the placement read (walk 24 C2). A read error, a moved map or a moved instance is a no. Any failure to prove
// the copy counts as not joined: the appear path then runs and the landing rule is the near one, so an off-map or
// other-copy commander is not assumed to be inside.
func (w *walk) placeStillJoined(ctx context.Context) bool {
	st, err := w.status(ctx)
	if err != nil {
		w.note("revive: the commander status is unreadable; the appear path")
		return false
	}
	inst, err := w.ttInst(ctx, "the revive's copy check")
	if err != nil {
		w.note("revive: the instance check did not answer; the appear path")
		return false
	}
	if st.Map != w.plan.Map || inst != w.placeInst {
		w.note("revive: the commander is on map %d / in instance %.0f, not the raid's: the appear path", st.Map, inst)
		return false
	}
	return true
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
	// --reuse-instance: the raid's copy is already open and alive, so there is no corridor clear, no quiet check and
	// no `.tt place`: the commander joins the raid's copy and summons every hire to the start point.
	if w.opts.reuseInstance {
		return w.placeReuse(ctx)
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
	// 6b. --gm-clear (test tool): the named packs die by GM command before the quiet check and the raid's move. A
	// group the plan skipped (a boss's or an event's units) gets its WARN line here, where run.log exists.
	for _, warn := range w.gmClearWarn {
		w.logLine(w.env.now(), warn)
	}
	if err := w.placeGMClear(ctx); err != nil {
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

// reuseReady is --reuse-instance's precondition, run instead of the enter order: every hired bot must be alive and on
// the plan's map in a fresh frame, so the placement can keep the copy the raid is already in. A bot missing, dead or
// elsewhere refuses the walk (exit 2) instead of opening a fresh instance and burning a cap slot.
func (w *walk) reuseReady(ctx context.Context) error {
	deadline := w.env.now().Add(30 * time.Second)
	for {
		w.placeView(ctx)
		now := w.env.now()
		var missing, dead, elsewhere []string
		for _, name := range w.hireList() {
			b, ok := w.botAt[name]
			if !ok || now.Sub(b.at) > 10*time.Second {
				missing = append(missing, name)
				continue
			}
			if b.dead {
				dead = append(dead, name)
				continue
			}
			if b.mapID != w.plan.Map {
				elsewhere = append(elsewhere, name)
			}
		}
		if len(missing)+len(dead)+len(elsewhere) == 0 {
			w.note("place: --reuse-instance: the raid is alive in map %d; the run keeps its instance", w.plan.Map)
			return nil
		}
		if !now.Before(deadline) {
			return stopf(exitUsage, "--reuse-instance: the raid is not alive in map %d (dead: %s; not seen: %s; elsewhere: %s)",
				w.plan.Map, namesOrNone(dead), namesOrNone(missing), namesOrNone(elsewhere))
		}
		if err := w.placeSleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// namesOrNone is a note's name list ("none" when empty).
func namesOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// placeReuse is the --reuse-instance placement: bind the commander to the main tank's copy (`.appear` opens no copy of
// its own), go to the start point in it, and teleport every hire there with the GM `.summon` (the line the revive and
// placeVerify use). The area is not cleared or quiet-checked: the copy is the one the raid is already playing in.
func (w *walk) placeReuse(ctx context.Context) error {
	p := w.place.Point
	if err := w.gm(ctx, ".appear "+w.mt); err != nil {
		return err
	}
	if err := w.waitMap(ctx, w.plan.Map, 30*time.Second, time.Time{}); err != nil {
		return err
	}
	if err := w.placeSleep(ctx, 2*time.Second); err != nil {
		return err
	}
	inst, err := w.placeInstance(ctx)
	if err != nil {
		return err
	}
	w.placeInst = inst
	w.placeSum.Instance = inst
	w.note("place: --reuse-instance: keeping the raid's instance %.0f (no fresh instance is entered or counted)", inst)
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
	for _, name := range w.hireList() {
		if err := w.gm(ctx, ".summon "+name); err != nil {
			return err
		}
		if err := w.placeSleep(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
	w.note("place: teleported %d hires to the start point with the GM path (.summon); the area was not cleared or quiet-checked (the raid's own instance)",
		len(w.hireList()))
	if err := w.placeVerify(ctx); err != nil {
		return err
	}
	w.placeStayed = w.placeHold(ctx)
	if err := w.placeRelease(ctx); err != nil {
		return err
	}
	w.placeSum.Placed = len(w.hireList()) - len(w.placeSummoned)
	w.placeSum.Summoned = w.placeSummoned
	w.note("place: start step %d%s at %s %s %s; reused instance %.0f; placed %d/%d",
		w.place.Start, legWords(w.place.Leg), num32(p.X), num32(p.Y), num32(p.Z), inst, w.placeSum.Placed, w.placeSum.Hires)
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
	// The unbind gets a context of its own: a slow landing may have used up (or cancelled) the one the wait ran in, and
	// the unbind is the line the commander's next walk depends on (review round 2, LOW-3).
	unbindCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := w.gm(unbindCtx, fmt.Sprintf(".instance unbind %d", w.plan.Map)); err != nil {
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
// 12 characters, gmNameRe). The extra clear round of the quiet step (review round 2, LOW-4) is counted with a
// placeholder position of the longest text a float32 coordinate of 0.1 or more takes (nine significant digits), so the
// check is an upper bound for every line the phase builds from the plan; a unit sitting at a coordinate under 0.1
// could print a longer text, and the toolkit's own cap then refuses that one line.
func placeTTLines(p *raidwalk.PlacePlan, planMap uint32) []string {
	const mt = "MMMMMMMMMMMM"
	tail := ""
	if w := raidwalk.SkipWords(p.Skips); w != "" {
		tail = " skip=" + w
	}
	px, py, pz := num32(p.Point.X), num32(p.Point.Y), num32(p.Point.Z)
	const exLong = "-0.123456789" // nine significant digits with a sign (the longest a float32 of 0.1 or more prints)
	out := []string{
		"q inst",
		"q group of=" + mt,
		fmt.Sprintf("q floor %s %s %s", px, py, pz),
		fmt.Sprintf("quiet %d %s %s %s w=%d dz=%d%s", placeQuietR, px, py, pz, placeQuietWaitS, placeClearDz, tail),
		fmt.Sprintf("q units %d %s %s %s all", placeLandingReadYd, px, py, pz), // M1b: the landing read after a clean quiet
		fmt.Sprintf("place %s %s %s spread=5 of=%s", px, py, pz, mt),
	}
	if len(p.Circles) > 0 {
		out = append(out,
			fmt.Sprintf("q units %d %s %s %s all", placeRoamScopeYd, px, py, pz), // the quiet's roamer join (item 4)
			fmt.Sprintf("q units %d %s %s %s all", placeExtraRadius, exLong, exLong, exLong),
			fmt.Sprintf("clearto %d %s %s %s %d dz=%d all=1%s", planMap, exLong, exLong, exLong, placeExtraRadius, placeClearDz, tail),
		)
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
