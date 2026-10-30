package dungeon

// The player follow mode (spec ta-player-follow): a real player in a bot-led
// TortoiseBots run follows the lead bot behind it on its trail. The run
// leader holds pulls while a player is over 60 yd from the lead bot and a
// regroup waits 60 s for every living player within 20 yd, so the mode keeps
// the player near the lead bot without ever moving ahead of it.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

const (
	FollowTick           = 500 * time.Millisecond // one look per tick (fake clock in tests)
	FollowNear           = 8.0                    // default inner edge of the band behind the lead bot
	FollowFar            = 15.0                   // default outer edge: a walk starts when the lead is farther (3D)
	FollowRetargetYd     = 8.0                    // a walk is cancelled and made again when its target moved this far
	FollowWalkMax        = 30 * time.Second       // ceiling of one walk
	FollowFailStop       = 5                      // walks in a row that did not arrive while the lead is over FollowHoldYd: the mode stops
	FollowHoldYd         = 60.0                   // the run leader's kPlayerPullYd
	TrailStepYd          = 2.0                    // a trail point is added when the lead moved this far from the last one (3D)
	TrailJumpYd          = 30.0                   // a lead that moved this far between two looks was teleported or summoned: the trail starts again
	TrailMaxYd           = 300.0                  // the trail keeps this much length, oldest dropped
	TrailHeadingYd       = 5.0                    // the lead's heading is taken from the trail point this far back
	CombatLeashYd        = 40.0                   // in a fight the player moves only when the lead is farther than this
	CombatStandYd        = 25.0                   // ... and then to this far short of the lead along the path
	SitLeashYd           = 45.0                   // a sitting player (eating, drinking) gets up only when the lead is farther than this
	FallbackAggroYd      = 15.0                   // a fallback way (no trail) passing a hostile this close is not walked
	FallbackLeadYd       = 30.0                   // ... unless the hostile stands this close to the lead bot (its pull, not ours)
	FallbackSampleYd     = 4.0                    // the fallback way is sampled this often for that check
	FallbackHoldMax      = 60 * time.Second       // a fallback held this long stops the mode
	AssistTankYd         = 8.0                    // the lead's target counts as tanked when this close to the lead
	AssistReachYd        = 30.0                   // the player walks to a tanked target only from this close
	AssistMeleeYd        = 3.0                    // ... and stops this far short of it
	ZonePortalYd         = 40.0                   // the lead last seen this close to the portal: it went in (zone in after it)
	ZoneMaxYd            = 300.0                  // the portal farther than this: no zoning, the mode stops
	ZoneTimeout          = 180 * time.Second
	ZoneTriggerTries     = 3
	ZoneTriggerWait      = 10 * time.Second
	FollowMaxDeaths      = 10               // Config.MaxLeaderDeaths of the recovery driver
	FollowHeadingYd      = 6.0              // the lead's heading is known once its trail (and its newest look) is this long
	FollowNoHeadWait     = 20 * time.Second // a lead FollowNoHeadDirectYd or more away with no heading this long: the player walks to it directly
	FollowNoHeadDirectYd = 20.0             // ... (the run leader's kGatherYd: a regroup waits for the player within it)
	FollowNotes          = 50
)

// FollowGame is what the follow mode needs: the leader-mode Game (positions, units, party, walk, path, death calls, area
// trigger, Emit) plus the agent's own unit row and the melee swing. It sends no chat and no GM line: the mode never calls Game.Chat, GM, GMOn or
// Teleport (section 4 "never").
type FollowGame interface {
	Game
	SelfRow() (objects.Unit, bool) // the agent's own unit row: in_combat, stand_state, target
	Attack(ctx context.Context) error
	StopAttack(ctx context.Context) error
}

type FollowConfig struct {
	Plan         *Plan
	Lead         string
	Near, Far    float64 // 0: defaults
	Assist       bool
	LostAfter    time.Duration // 0: 30 s
	Brain        Brain         // the move lock (HoldMove); Start/Stop/Rest are no-ops in the adapter. nil: no lock
	BrainRunning func() bool   // whether a brain runs now (assist is off then); nil: false
	WalkWatch    time.Duration // the walk watcher's real-time period; 0: 500 ms (tests: 2 ms)
}

type FollowStatus struct {
	Running       bool      `json:"running"`
	State         string    `json:"state"`
	Plan          string    `json:"plan"`
	Lead          string    `json:"lead"`
	Assist        bool      `json:"assist"`
	Near          float64   `json:"near"`
	Far           float64   `json:"far"`
	LostS         int       `json:"lost_s"`
	LeadSeen      bool      `json:"lead_seen"`
	LeadYd        float64   `json:"lead_yd"`
	LeadAt        *Point    `json:"lead_at,omitempty"`
	SelfAt        Point     `json:"self_at"`
	Map           uint32    `json:"map"`
	TrailYd       float64   `json:"trail_yd"`
	Walks         int       `json:"walks"`
	WalkFails     int       `json:"walk_fails"`
	Deaths        int       `json:"deaths"`
	Resurrections int       `json:"resurrections"`
	CorpseRuns    int       `json:"corpse_runs"`
	ZoneIns       int       `json:"zone_ins"`
	Assists       int       `json:"assists"`
	MaxLeadYd     float64   `json:"max_lead_yd"`
	SecondsOver60 float64   `json:"seconds_over_60"`
	StoppedReason string    `json:"stopped_reason"`
	Notes         []string  `json:"notes"`
	Started       time.Time `json:"started"`
	Seconds       float64   `json:"seconds"`
}

type PlayerFollower struct {
	g   FollowGame
	cfg FollowConfig
	d   *Driver
	clk Clock

	mu        sync.Mutex
	status    FollowStatus
	trail     []Point
	trailLen  float64
	lastSeen  time.Time
	leadInPt  bool
	lead      string
	pending   string // a lead switch SetLead asked for; the loop picks it up at the next tick
	runCtx    context.Context
	failRun   int
	holdGUID  packet.GUID
	holdNote  bool
	holdAt    time.Time
	ahead     bool        // the ahead note is out (until the state is near again)
	brain     bool        // the brain note is out
	leash     bool        // the leash note is out
	hitting   bool        // the melee swing is on
	assistID  packet.GUID // the mob the swing is on (no re-target while the row keeps it as the target)
	assistOn  bool        // the player's own row was seen with assistID as its target since the swing began
	waitBack  bool        // after recovery: waiting until the lead is in view again
	zoneAt    time.Time
	zoneN     int
	zoneNote  bool
	noHead    time.Time // when the player first waited for the lead's heading (zero: not waiting)
	headNote  bool      // the no-heading note is out (until the heading is known)
	frontNote bool      // the "way passes in front of the lead" note is out (until the state is near again)
	lostOut   bool      // the lost event is out (until the lead is seen again)
}

func NewFollower(g FollowGame, cfg FollowConfig, clk Clock) *PlayerFollower {
	if clk.Now == nil || clk.Sleep == nil {
		clk = RealClock()
	}
	if cfg.Near == 0 {
		cfg.Near = FollowNear
	}
	if cfg.Far == 0 {
		cfg.Far = FollowFar
	}
	if cfg.LostAfter == 0 {
		cfg.LostAfter = 30 * time.Second
	}
	walkWatch := cfg.WalkWatch
	if walkWatch == 0 {
		walkWatch = 500 * time.Millisecond
	}
	f := &PlayerFollower{g: g, cfg: cfg, clk: clk, lead: cfg.Lead}
	f.d = NewDriver(cfg.Plan, g, Config{Brain: cfg.Brain, MaxLeaderDeaths: FollowMaxDeaths, WalkWatch: walkWatch, EventMode: "follow"}, clk)
	f.status.Running = true
	f.status.State = "starting"
	f.status.Plan = cfg.Plan.Name
	f.status.Lead = cfg.Lead
	f.status.Assist = cfg.Assist
	f.status.Near = cfg.Near
	f.status.Far = cfg.Far
	f.status.LostS = int(cfg.LostAfter / time.Second)
	f.status.Started = clk.Now()
	f.status.Map = g.Map()
	f.lastSeen = clk.Now()
	return f
}

func (f *PlayerFollower) Status() FollowStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.status
	out.Seconds = f.clk.Now().Sub(out.Started).Seconds()
	out.Notes = append([]string(nil), f.status.Notes...)
	return out
}

// SetLead switches the lead bot (3.9); safe from another goroutine (under mu;
// the loop picks it up at the next tick).
func (f *PlayerFollower) SetLead(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = name
}

// WantedLead is the lead bot the loop follows from its next tick on: a switch SetLead asked for, else the current lead.
func (f *PlayerFollower) WantedLead() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending != "" {
		return f.pending
	}
	return f.lead
}

// Run runs until ctx ends or the mode stops itself; sets Running false at the end.
func (f *PlayerFollower) Run(ctx context.Context) {
	f.mu.Lock()
	f.runCtx = ctx
	f.mu.Unlock()
	f.emit("started", map[string]any{"lead": f.lead, "plan": f.cfg.Plan.Name})
	for {
		select {
		case <-ctx.Done():
			// A ctx error ends the loop: state stopped, the reason stays what Stop set.
			f.stopLocked("", "stopped")
			return
		default:
		}
		if reason := f.tick(); reason != "" {
			f.stopLocked(reason, "stopped")
			return
		}
		if err := f.clk.Sleep(ctx, FollowTick); err != nil {
			f.stopLocked("", "stopped")
			return
		}
	}
}

func (f *PlayerFollower) ctx() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runCtx != nil {
		return f.runCtx
	}
	return context.Background()
}

func (f *PlayerFollower) stopLocked(reason, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.status.Running {
		return
	}
	f.status.Running = false
	f.status.State = state
	if reason != "" {
		if f.status.StoppedReason == "" {
			f.status.StoppedReason = reason
		}
		f.noteLocked(reason)
	}
	f.g.Emit(events.KindPlayerFollow, "player_follow stopped",
		map[string]any{"phase": "stopped", "reason": f.status.StoppedReason, "lead": f.status.Lead})
}

func (f *PlayerFollower) noteLocked(s string) {
	f.status.Notes = append(f.status.Notes, s)
	if len(f.status.Notes) > FollowNotes {
		f.status.Notes = append([]string(nil), f.status.Notes[len(f.status.Notes)-FollowNotes:]...)
	}
}

func (f *PlayerFollower) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noteLocked(s)
}

func (f *PlayerFollower) emit(phase string, data map[string]any) {
	f.mu.Lock()
	lead := f.status.Lead
	f.mu.Unlock()
	if data == nil {
		data = map[string]any{}
	}
	data["phase"] = phase
	data["lead"] = lead
	f.g.Emit(events.KindPlayerFollow, "player_follow "+phase, data)
}

// tick runs one look; a non-empty return is the stop reason.
func (f *PlayerFollower) tick() string {
	now := f.clk.Now()
	f.mu.Lock()
	if f.pending != "" && !strings.EqualFold(f.pending, f.status.Lead) {
		old := f.status.Lead
		f.status.Lead = f.pending
		f.lead = f.pending
		f.pending = ""
		f.trail, f.trailLen = nil, 0
		f.noHead, f.headNote = time.Time{}, false // the trail starts again: so does the wait for a heading
		f.status.TrailYd = 0
		f.lastSeen = now
		f.status.LeadAt = nil
		f.noteLocked(fmt.Sprintf("the lead bot is now %s (was %s)", f.lead, old))
		f.ahead = false
	} else {
		f.pending = ""
	}
	lead := f.lead
	f.mu.Unlock()

	// 1. Self dead.
	if f.g.Self().Dead() {
		f.mu.Lock()
		f.status.State = "dead"
		f.status.Deaths++
		f.mu.Unlock()
		f.emit("dead", nil)
		how, err := f.d.RecoverSelf(f.ctx())
		if err != nil {
			if ctxDone(err) {
				return ""
			}
			return fmt.Sprintf("the player could not come back: %v", err)
		}
		back := f.clk.Now()
		f.mu.Lock()
		if how == "resurrected" {
			f.status.Resurrections++
		} else {
			f.status.CorpseRuns++
		}
		f.noteLocked(fmt.Sprintf("back after %.0f s (%s): waiting for the lead bot, then following behind it", back.Sub(now).Seconds(), how))
		f.trail, f.trailLen = nil, 0
		f.noHead, f.headNote = time.Time{}, false // the trail starts again: so does the wait for a heading
		f.status.TrailYd = 0
		f.lastSeen = back
		f.waitBack = true
		f.status.State = "waiting"
		f.mu.Unlock()
		f.emit("back", map[string]any{"how": how, "seconds": back.Sub(now).Seconds()})
		return ""
	}

	// 2. Party.
	ms := f.g.Party()
	if len(ms) == 0 {
		return "the player is not in a group"
	}
	inParty := false
	for _, m := range ms {
		if strings.EqualFold(m.Name, lead) {
			inParty = true
			break
		}
	}
	f.mu.Lock()
	f.leadInPt = inParty
	f.mu.Unlock()

	// 3. Lead row.
	var leadRow *objects.Unit
	for _, u := range f.g.Units() {
		if u.Type == "player" && strings.EqualFold(u.Name, lead) {
			c := u
			leadRow = &c
			break
		}
	}
	self := f.g.Position()
	f.mu.Lock()
	f.status.SelfAt = self
	f.status.Map = f.g.Map()
	f.mu.Unlock()

	// 4. Seen.
	seen := leadRow != nil && inParty
	if seen {
		l := Point{X: leadRow.X, Y: leadRow.Y, Z: leadRow.Z}
		f.trailSeen(l)
		f.mu.Lock()
		f.lastSeen = now
		f.status.LeadSeen = true
		f.status.LeadYd = dist3(self, l)
		f.status.LeadAt = &Point{X: l.X, Y: l.Y, Z: l.Z}
		f.waitBack = false
		f.lostOut = false
		f.mu.Unlock()
	}

	if seen && f.headingKnown() {
		f.mu.Lock()
		f.noHead, f.headNote = time.Time{}, false
		f.mu.Unlock()
	}

	// 5. Off the plan's map and not seen → zoning.
	if f.g.Map() != f.cfg.Plan.Map && !seen {
		return f.zoneTick(now, self)
	}

	// 6. Not seen.
	if !seen {
		return f.lostTick(now, self)
	}

	// 7. Book-keeping.
	l := Point{X: leadRow.X, Y: leadRow.Y, Z: leadRow.Z}
	dist := dist3(self, l)
	alive := !f.g.Self().Dead()
	onMap := f.g.Map() == f.cfg.Plan.Map
	f.mu.Lock()
	if dist > f.status.MaxLeadYd {
		f.status.MaxLeadYd = dist
	}
	f.status.LeadYd = dist
	f.mu.Unlock()

	// 8. Combat.
	if f.partyCombat(lead) {
		return f.combatTick(dist, l, self)
	}
	f.mu.Lock()
	hitting := f.hitting
	if hitting {
		f.hitting = false
	}
	f.mu.Unlock()
	if hitting {
		_ = f.g.StopAttack(f.ctx())
	}
	if alive && onMap && dist > FollowHoldYd {
		f.mu.Lock()
		f.status.SecondsOver60 += FollowTick.Seconds()
		f.mu.Unlock()
	}

	// 9. Sitting.
	if row, ok := f.g.SelfRow(); ok && row.StandState != "" && row.StandState != "stand" {
		if dist <= SitLeashYd {
			f.mu.Lock()
			f.status.State = "sitting"
			f.mu.Unlock()
			return ""
		}
	}

	// 10. In the band.
	if dist <= f.cfg.Far && !f.isAhead(self) {
		f.mu.Lock()
		f.status.State = "near"
		f.failRun = 0
		f.ahead = false
		f.frontNote = false
		f.holdAt = time.Time{}
		f.mu.Unlock()
		return ""
	}
	if f.isAhead(self) {
		f.mu.Lock()
		once := !f.ahead
		f.ahead = true
		f.mu.Unlock()
		if once {
			f.note(fmt.Sprintf("the player stands ahead of the lead bot (%.0f yd): walking back behind it", dist))
			f.emit("ahead", nil)
		}
	}

	// 11. Target.
	at := f.cfg.Near + (f.cfg.Far-f.cfg.Near)/2
	t, ok := f.trailPoint(at)
	if !ok {
		// No trail target: with no heading of the lead yet the player may stand in front of it, and the way to it would go
		// through its front, so it stands still (a lead FollowNoHeadDirectYd or more away: walked to directly after FollowNoHeadWait).
		if !f.noHeadingWalk(now, dist) {
			f.mu.Lock()
			f.status.State = "holding"
			f.holdAt = time.Time{}
			f.mu.Unlock()
			return ""
		}
		tp, ok2, hold := f.fallbackTarget(self, l, now)
		if hold == frontHoldText {
			f.mu.Lock()
			f.status.State = "holding"
			f.holdAt = time.Time{}
			f.mu.Unlock()
			return ""
		}
		if hold != "" {
			f.mu.Lock()
			f.status.State = "holding"
			if f.holdAt.IsZero() {
				f.holdAt = now
			}
			hs := f.holdAt
			f.mu.Unlock()
			if now.Sub(hs) >= FallbackHoldMax {
				return fmt.Sprintf("the way to the lead bot stayed blocked for %.0f s: %s", now.Sub(hs).Seconds(), hold)
			}
			return ""
		}
		f.mu.Lock()
		f.holdAt = time.Time{}
		f.mu.Unlock()
		if !ok2 {
			f.mu.Lock()
			f.status.State = "near"
			f.mu.Unlock()
			return ""
		}
		t = tp
	} else {
		f.mu.Lock()
		f.holdAt = time.Time{}
		f.mu.Unlock()
	}

	// 12. Walk.
	return f.walkToTick(t, dist, "follow", func(s *FollowStatus) { s.State = "walking" })
}

// trailSeen updates the trail at a tick where the lead row is seen at l.
func (f *PlayerFollower) trailSeen(l Point) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.trail) == 0 {
		f.trail = []Point{l}
		f.trailLen = 0
		f.status.TrailYd = 0
		return
	}
	last := f.trail[len(f.trail)-1]
	if dist3(l, last) >= TrailJumpYd {
		f.trail = []Point{l}
		f.noHead, f.headNote = time.Time{}, false // the trail starts again: so does the wait for a heading
		f.trailLen = 0
		f.status.TrailYd = 0
		f.noteLocked(fmt.Sprintf("the lead bot jumped %.0f yd (a teleport or a summon): the trail starts again", dist3(l, last)))
		return
	}
	if dist3(l, last) >= TrailStepYd {
		f.trail = append(f.trail, l)
		f.trailLen += dist3(l, last)
		for len(f.trail) >= 2 && f.trailLen-segLen(f.trail[0], f.trail[1]) >= TrailMaxYd {
			f.trailLen -= segLen(f.trail[0], f.trail[1])
			f.trail = f.trail[1:]
		}
	}
	f.status.TrailYd = f.trailLen
}

func segLen(a, b Point) float64 { return dist3(a, b) }

// trailPoint walks the polyline (trail..., newest look) backwards `back` yards.
func (f *PlayerFollower) trailPoint(back float64) (Point, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pts := append([]Point(nil), f.trail...)
	if l := f.status.LeadAt; l != nil && (len(pts) == 0 || dist3(*l, pts[len(pts)-1]) > 1e-9) {
		pts = append(pts, *l)
	}
	return trailBackPoint(pts, back)
}

func trailBackPoint(pts []Point, back float64) (Point, bool) {
	left := back
	for i := len(pts) - 1; i > 0; i-- {
		seg := dist3(pts[i], pts[i-1])
		if seg > left {
			t := left / seg
			if seg == 0 {
				t = 0
			}
			return Point{
				X: float32(float64(pts[i].X) + (float64(pts[i-1].X)-float64(pts[i].X))*t),
				Y: float32(float64(pts[i].Y) + (float64(pts[i-1].Y)-float64(pts[i].Y))*t),
				Z: float32(float64(pts[i].Z) + (float64(pts[i-1].Z)-float64(pts[i].Z))*t),
			}, true
		}
		left -= seg
	}
	return Point{}, false
}

// isAhead reports whether self stands ahead of the lead bot.
func (f *PlayerFollower) isAhead(self Point) bool {
	f.mu.Lock()
	lead := f.status.LeadAt
	trail := append([]Point(nil), f.trail...)
	near := f.cfg.Near
	f.mu.Unlock()
	if lead == nil {
		return false
	}
	h, ok := headingFrom(trail, *lead)
	if !ok {
		return false
	}
	return aheadOf(self, *lead, h, near)
}

// headingFrom is the trail point TrailHeadingYd behind the lead at l, when the polyline (trail..., l) is FollowHeadingYd long.
func headingFrom(trail []Point, l Point) (Point, bool) {
	pts := append(append([]Point(nil), trail...), l)
	var n float64
	for i := 0; i+1 < len(pts); i++ {
		n += dist3(pts[i], pts[i+1])
	}
	if n < FollowHeadingYd {
		return Point{}, false
	}
	return trailBackPoint(pts, TrailHeadingYd)
}

// aheadOf reports whether p stands in front of the lead at l, whose heading comes from the point h behind it: on the front
// side (2D) and farther than near.
func aheadOf(p, l, h Point, near float64) bool {
	hx, hy := l.X-h.X, l.Y-h.Y
	sx, sy := p.X-l.X, p.Y-l.Y
	if sx*hx+sy*hy <= 0 {
		return false
	}
	return hdist(p, l) > near
}

// headingKnown reports whether the lead's heading is known now.
func (f *PlayerFollower) headingKnown() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status.LeadAt == nil {
		return false
	}
	_, ok := headingFrom(f.trail, *f.status.LeadAt)
	return ok
}

// noHeadingWalk decides a walk toward the lead that is not on its trail: with the heading known it may go (the way is checked
// against it, wayInFront); with none it waits, except that a lead FollowNoHeadDirectYd or more away is walked to directly once
// the wait (since the trail last started) has lasted FollowNoHeadWait (note "no heading: direct walk").
func (f *PlayerFollower) noHeadingWalk(now time.Time, dist float64) bool {
	if f.headingKnown() {
		f.mu.Lock()
		f.noHead, f.headNote = time.Time{}, false
		f.mu.Unlock()
		return true
	}
	f.mu.Lock()
	if f.noHead.IsZero() {
		f.noHead = now
	}
	since := f.noHead
	note := !f.headNote
	f.headNote = true
	f.mu.Unlock()
	if dist >= FollowNoHeadDirectYd && now.Sub(since) >= FollowNoHeadWait {
		f.note(fmt.Sprintf("no heading: direct walk (the lead bot is %.0f yd away and has not moved for %.0f s)", dist, now.Sub(since).Seconds()))
		return true
	}
	if note {
		f.note(fmt.Sprintf("the lead bot's heading is not known yet: standing still until it has moved %.0f yd (it is %.0f yd away)", FollowHeadingYd, dist))
	}
	return false
}

// wayInFront reports whether a sample of the way pts (from the player to its goal) lies in front of the lead at l, by the
// lead's heading; false when the heading is not known (noHeadingWalk decided that case).
func (f *PlayerFollower) wayInFront(pts []Point, l Point) bool {
	f.mu.Lock()
	h, ok := headingFrom(f.trail, l)
	near := f.cfg.Near
	f.mu.Unlock()
	if !ok {
		return false
	}
	for _, s := range samplePath(pts, FallbackSampleYd) {
		if aheadOf(s, l, h, near) {
			return true
		}
	}
	return false
}

// frontHold notes once that the way to the lead passes in front of it.
func (f *PlayerFollower) frontHold(dist float64) {
	f.mu.Lock()
	note := !f.frontNote
	f.frontNote = true
	f.mu.Unlock()
	if note {
		f.note(fmt.Sprintf("the way to the lead bot (%.0f yd) passes in front of it: standing still until it has walked past", dist))
	}
}

// partyCombat reports whether the party is in a fight.
func (f *PlayerFollower) partyCombat(lead string) bool {
	selfGUID := f.g.Self().GUID
	byGUID := map[packet.GUID]bool{selfGUID: true}
	byName := map[string]bool{}
	for _, m := range f.g.Party() {
		byGUID[m.GUID] = true
		byName[strings.ToLower(m.Name)] = true
	}
	leadCombat := false
	for _, u := range f.g.Units() {
		switch {
		case u.Type == "player" && (byGUID[u.GUID] || byName[strings.ToLower(u.Name)]) && u.InCombat:
			return true
		case u.Type == "creature" && u.Hostile && !u.Dead && u.InCombat && byGUID[u.Target]:
			return true
		}
		if u.Type == "player" && strings.EqualFold(u.Name, lead) && u.InCombat {
			leadCombat = true
		}
	}
	return leadCombat
}

// walkTo runs one walk under the move lock with the watch-until cancellation.
func (f *PlayerFollower) walkTo(ctx context.Context, t Point, kind string) (WalkResult, bool, error) {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	start := time.Now()
	cond := func() bool {
		if kind != "leash" && kind != "assist" && f.partyCombat(f.leadName()) {
			return true
		}
		if f.g.Self().Dead() {
			return true
		}
		if kind == "follow" {
			if nt, ok := f.freshTrailTarget(); ok && dist3(nt, t) > FollowRetargetYd {
				return true
			}
		}
		return time.Since(start) > FollowWalkMax
	}
	stop := f.d.watchUntil(wctx, cancel, cond)
	r, err := f.d.walkPath(wctx, t)
	fired := stop()
	if cerr := ctx.Err(); cerr != nil {
		return WalkResult{}, false, cerr
	}
	if err != nil {
		f.emitWalk(kind, t, "partial")
		return WalkResult{Result: "partial", Reason: err.Error()}, fired, nil
	}
	if r.Result != "arrived" {
		f.emitWalk(kind, t, r.Result)
	}
	return r, fired, nil
}

func (f *PlayerFollower) leadName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lead
}

func (f *PlayerFollower) emitWalk(kind string, to Point, result string) {
	f.emit("walk", map[string]any{"kind": kind, "to": map[string]any{"x": to.X, "y": to.Y, "z": to.Z}, "result": result})
}

func (f *PlayerFollower) freshTrailTarget() (Point, bool) {
	var l *Point
	for _, u := range f.g.Units() {
		if u.Type == "player" && strings.EqualFold(u.Name, f.leadName()) {
			l = &Point{X: u.X, Y: u.Y, Z: u.Z}
			break
		}
	}
	if l == nil {
		return Point{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	pts := append([]Point(nil), f.trail...)
	pts = append(pts, *l)
	return trailBackPoint(pts, f.cfg.Near+(f.cfg.Far-f.cfg.Near)/2)
}

func (f *PlayerFollower) walkToTick(t Point, dist float64, kind string, set func(*FollowStatus)) string {
	f.mu.Lock()
	set(&f.status)
	f.status.Walks++
	f.mu.Unlock()
	r, cancelled, err := f.walkTo(f.ctx(), t, kind)
	if err != nil {
		if ctxDone(err) {
			return ""
		}
		return err.Error()
	}
	if r.Result == "arrived" || cancelled {
		f.mu.Lock()
		f.failRun = 0
		f.mu.Unlock()
		return ""
	}
	f.note(fmt.Sprintf("walk to %.1f %.1f %.1f ended %s: %s (%.1f yd left)", t.X, t.Y, t.Z, r.Result, r.Reason, r.DistLeft))
	f.mu.Lock()
	f.status.WalkFails++
	f.failRun++
	failRun := f.failRun
	f.mu.Unlock()
	if failRun >= FollowFailStop && dist > FollowHoldYd {
		return fmt.Sprintf("the player cannot reach the lead bot: %d walks in a row did not arrive (last: %s); it is %.0f yd away", failRun, r.Reason, dist)
	}
	return ""
}

// frontHoldText is fallbackTarget's hold when the way passes in front of the lead: the player stands still, with no time limit.
const frontHoldText = "front"

// fallbackTarget finds the standoff way toward the lead when there is no trail.
func (f *PlayerFollower) fallbackTarget(self, l Point, now time.Time) (Point, bool, string) {
	pts, err := f.g.PathPoints(f.ctx(), l)
	if err != nil || len(pts) < 2 {
		pts = []Point{self, l}
	}
	t, ok := StandoffPoint(pts, float32(f.cfg.Near))
	if !ok {
		return Point{}, false, ""
	}
	if f.wayInFront(pts, l) {
		f.frontHold(dist3(self, l))
		return Point{}, false, frontHoldText
	}
	samples := samplePath(pts, FallbackSampleYd)
	for _, u := range f.g.Units() {
		if u.Type != "creature" || !u.Hostile || u.Dead || u.InCombat {
			continue
		}
		up := Point{X: u.X, Y: u.Y, Z: u.Z}
		near := false
		for _, s := range samples {
			if dist3(s, up) <= FallbackAggroYd {
				near = true
				break
			}
		}
		if !near || dist3(up, l) <= FallbackLeadYd {
			continue
		}
		f.mu.Lock()
		first := f.holdGUID != u.GUID
		if first {
			f.holdGUID = u.GUID
			f.holdNote = false
		}
		note := !f.holdNote
		if note {
			f.holdNote = true
		}
		f.mu.Unlock()
		hold := fmt.Sprintf("%s (entry %d) stands %.0f yd from the way to the lead bot", u.Name, u.Entry, dist3(up, l))
		if note {
			f.note(hold)
		}
		return Point{}, false, hold
	}
	f.mu.Lock()
	f.holdGUID, f.holdNote = 0, false
	f.mu.Unlock()
	return t, true, ""
}

func samplePath(pts []Point, every float64) []Point {
	var total float64
	for i := 0; i+1 < len(pts); i++ {
		total += dist3(pts[i], pts[i+1])
	}
	n := int(total / every)
	if n < 1 {
		n = 1
	}
	var out []Point
	for i := 0; i <= n; i++ {
		d := total * float64(i) / float64(n)
		left := d
		for j := 0; j+1 < len(pts); j++ {
			seg := dist3(pts[j], pts[j+1])
			if seg >= left {
				tt := 0.0
				if seg > 0 {
					tt = left / seg
				}
				out = append(out, Point{
					X: float32(float64(pts[j].X) + (float64(pts[j+1].X)-float64(pts[j].X))*tt),
					Y: float32(float64(pts[j].Y) + (float64(pts[j+1].Y)-float64(pts[j].Y))*tt),
					Z: float32(float64(pts[j].Z) + (float64(pts[j+1].Z)-float64(pts[j].Z))*tt),
				})
				break
			}
			left -= seg
		}
	}
	return out
}

// combatTick decides one tick of a fight.
func (f *PlayerFollower) combatTick(dist float64, l, self Point) string {
	cfg := f.cfg
	f.mu.Lock()
	f.holdAt = time.Time{} // a fight ends a fallback hold: its 60 s start again after it
	f.mu.Unlock()
	brainRuns := cfg.BrainRunning != nil && cfg.BrainRunning()
	if cfg.Assist && !brainRuns {
		var leadRow *objects.Unit
		for _, u := range f.g.Units() {
			if u.Type == "player" && strings.EqualFold(u.Name, f.leadName()) {
				c := u
				leadRow = &c
				break
			}
		}
		var mob *objects.Unit
		if leadRow != nil && leadRow.Target != 0 {
			for _, u := range f.g.Units() {
				if u.GUID == leadRow.Target && u.Type == "creature" && u.Hostile && !u.Dead && u.InCombat &&
					dist3(Point{X: u.X, Y: u.Y, Z: u.Z}, l) <= AssistTankYd {
					c := u
					mob = &c
					break
				}
			}
		}
		if mob != nil {
			mp := Point{X: mob.X, Y: mob.Y, Z: mob.Z}
			row, _ := f.g.SelfRow()
			f.mu.Lock()
			same := f.assistID == mob.GUID
			if same && row.Target == mob.GUID {
				f.assistOn = true // the swing is on: the row shows it
			}
			// A new mob, or the same one whose swing the row showed and no longer does (the core writes 0 into the
			// target field at AttackStop): target and swing again. The same mob not yet shown on the row is the
			// update still on its way: no second swing.
			again := same && f.assistOn && row.Target != mob.GUID
			swing := (!same && row.Target != mob.GUID) || again
			f.mu.Unlock()
			if swing {
				if err := f.g.Target(f.ctx(), mob.GUID); err != nil {
					f.note(fmt.Sprintf("assist: %v", err))
				} else if err := f.g.Attack(f.ctx()); err != nil {
					f.note(fmt.Sprintf("assist: %v", err))
				} else {
					f.mu.Lock()
					f.status.Assists++
					f.hitting = true
					f.assistID = mob.GUID
					f.assistOn = false
					f.mu.Unlock()
					if again {
						f.note(fmt.Sprintf("assist: the swing at %s (entry %d) stopped: attacking it again", mob.Name, mob.Entry))
					} else {
						f.note(fmt.Sprintf("assist: attacking %s (entry %d), the lead bot's target", mob.Name, mob.Entry))
					}
				}
			}
			if d := dist3(self, mp); d > AssistMeleeYd+2 && d <= AssistReachYd {
				if st, ok := StandoffPoint([]Point{self, mp}, AssistMeleeYd); ok {
					return f.walkToTick(st, dist, "assist", func(s *FollowStatus) { s.State = "assisting" })
				}
			}
		}
	} else if cfg.Assist && brainRuns {
		f.mu.Lock()
		if !f.brain {
			f.brain = true
			f.noteLocked("assist: the brain runs and fights by itself; the follow mode only keeps range")
		}
		f.mu.Unlock()
	}
	if dist > CombatLeashYd {
		if t, ok := f.leashTarget(dist, l, self); ok {
			f.mu.Lock()
			if !f.leash {
				f.leash = true
				f.noteLocked(fmt.Sprintf("in a fight %.0f yd from the lead bot: closing to %.0f yd", dist, float64(CombatStandYd)))
			}
			f.mu.Unlock()
			return f.walkToTick(t, dist, "leash", func(s *FollowStatus) { s.State = "combat_leash" })
		}
	}
	f.mu.Lock()
	if f.hitting {
		f.status.State = "assisting"
	} else {
		f.status.State = "combat"
	}
	f.mu.Unlock()
	return ""
}

// leashTarget is the combat leash's goal, CombatStandYd short of the lead along the way to it, or false when the player must
// not walk: the lead's heading is not known yet (noHeadingWalk), or the way passes in front of the lead (the fight is there;
// a player standing in front of the lead is on that way's first point, and the lead comes past it).
func (f *PlayerFollower) leashTarget(dist float64, l, self Point) (Point, bool) {
	if !f.noHeadingWalk(f.clk.Now(), dist) {
		return Point{}, false
	}
	pts, err := f.g.PathPoints(f.ctx(), l)
	if err != nil || len(pts) < 2 {
		pts = []Point{self, l}
	}
	t, ok := StandoffPoint(pts, CombatStandYd)
	if !ok {
		return Point{}, false
	}
	if f.wayInFront(pts, l) {
		f.frontHold(dist)
		return Point{}, false
	}
	return t, true
}

// zoneTick handles the player off the plan's map with the lead not seen.
func (f *PlayerFollower) zoneTick(now time.Time, self Point) string {
	p := f.cfg.Plan.Portal
	trig := f.cfg.Plan.EntranceTrigger
	if p == nil || trig == 0 || p.Map != f.g.Map() {
		var pm uint32
		if p != nil {
			pm = p.Map
		}
		return fmt.Sprintf("the player is on map %d and the lead bot is not in view; the plan %s has no portal on this map (portal map %d, trigger %d): bring the character to the party first",
			f.g.Map(), f.cfg.Plan.Name, pm, trig)
	}
	// Go-ahead guard: zone only when the lead was last seen within ZonePortalYd
	// of the portal (the newest trail point before the trail was lost) or the
	// lead was never seen since start/recovery.
	f.mu.Lock()
	var newest *Point
	if len(f.trail) > 0 {
		last := f.trail[len(f.trail)-1]
		newest = &Point{X: last.X, Y: last.Y, Z: last.Z}
	} else {
		newest = f.status.LeadAt
	}
	f.mu.Unlock()
	pp := Point{X: p.X, Y: p.Y, Z: p.Z}
	if newest != nil && dist3(*newest, pp) > ZonePortalYd {
		return f.lostTick(now, self)
	}
	if dist3(self, pp) > ZoneMaxYd {
		return fmt.Sprintf("the plan's portal is %.0f yd away (at most %.0f): bring the character nearer first", dist3(self, pp), float64(ZoneMaxYd))
	}
	f.mu.Lock()
	if f.zoneAt.IsZero() {
		f.zoneAt = now
		f.zoneNote = true
	}
	zs := f.zoneAt
	note := f.zoneNote
	f.zoneNote = false
	f.mu.Unlock()
	if note {
		f.note(fmt.Sprintf("zoning: the lead bot went into the instance; walking to the portal %.1f %.1f %.1f (trigger %d)", p.X, p.Y, p.Z, trig))
		f.emit("zoning", map[string]any{"trigger": trig})
	}
	if now.Sub(zs) > ZoneTimeout {
		return fmt.Sprintf("zoning in took longer than %.0f s", ZoneTimeout.Seconds())
	}
	if f.partyCombat(f.leadName()) {
		f.mu.Lock()
		f.status.State = "zoning"
		f.mu.Unlock()
		return ""
	}
	if dist3(self, pp) > 5 {
		return f.walkToTick(pp, dist3(self, pp), "zone", func(s *FollowStatus) { s.State = "zoning" })
	}
	if err := f.d.areaTrigger(f.ctx(), trig); err != nil {
		if ctxDone(err) {
			return ""
		}
		return f.zoneTried(trig)
	}
	f.mu.Lock()
	f.zoneN++
	zn := f.zoneN
	f.mu.Unlock()
	// The map is looked at every FollowTick until it changes or ZoneTriggerWait has passed (the injected clock).
	end := f.clk.Now().Add(ZoneTriggerWait)
	for f.g.Map() != f.cfg.Plan.Map && f.clk.Now().Before(end) {
		if err := f.clk.Sleep(f.ctx(), FollowTick); err != nil {
			return ""
		}
	}
	if f.g.Map() == f.cfg.Plan.Map {
		f.mu.Lock()
		f.status.ZoneIns++
		f.trail, f.trailLen = nil, 0
		f.noHead, f.headNote = time.Time{}, false // the trail starts again: so does the wait for a heading
		f.status.TrailYd = 0
		f.lastSeen = f.clk.Now()
		f.zoneAt = time.Time{}
		f.zoneN = 0
		f.mu.Unlock()
		f.note(fmt.Sprintf("zoned into map %d (trigger %d)", f.cfg.Plan.Map, trig))
		f.emit("zoned", map[string]any{"map": f.cfg.Plan.Map, "trigger": trig})
		return ""
	}
	if zn >= ZoneTriggerTries {
		return fmt.Sprintf("area trigger %d did not take the player into map %d (%d tries)", trig, f.cfg.Plan.Map, zn)
	}
	f.mu.Lock()
	f.status.State = "zoning"
	f.mu.Unlock()
	return ""
}

func (f *PlayerFollower) zoneTried(trig uint32) string {
	f.mu.Lock()
	f.zoneN++
	zn := f.zoneN
	f.mu.Unlock()
	if zn >= ZoneTriggerTries {
		return fmt.Sprintf("area trigger %d did not take the player into map %d (%d tries)", trig, f.cfg.Plan.Map, zn)
	}
	return ""
}

// lostTick is step 6 of 3.4 (plain loss, no zoning): no walk, never search ahead.
func (f *PlayerFollower) lostTick(now time.Time, self Point) string {
	f.mu.Lock()
	f.status.LeadSeen = false
	if !f.waitBack {
		f.status.State = "lost"
	}
	lastSeen := f.lastSeen
	leadAt := f.status.LeadAt
	lead := f.status.Lead
	lostAfter := f.cfg.LostAfter
	inParty := f.leadInPt
	lostOnce := !f.lostOut
	f.lostOut = true
	f.mu.Unlock()
	if lostOnce {
		f.emit("lost", nil)
	}
	if now.Sub(lastSeen) > lostAfter {
		if !inParty {
			return fmt.Sprintf("the lead bot %s left the party (%.0f s): the mode stops", lead, now.Sub(lastSeen).Seconds())
		}
		if leadAt != nil {
			return fmt.Sprintf("the lead bot %s was not in view for %.0f s (last seen at %.1f %.1f %.1f, %.0f yd away): the mode stops",
				lead, now.Sub(lastSeen).Seconds(), leadAt.X, leadAt.Y, leadAt.Z, dist3(self, *leadAt))
		}
		return fmt.Sprintf("the lead bot %s was not in view for %.0f s, never seen: the mode stops", lead, now.Sub(lastSeen).Seconds())
	}
	return ""
}
