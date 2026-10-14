package dungeon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// The progress watchdog (README "Run watchdog"). A run that waits on something that never comes (an order nobody answers, a rest gate
// whose mana does not rise, a carrier that stands still, a feed that went quiet) used to sit there until a caller noticed. Both drivers
// feed one watchdog with their progress and ask it at every wait; after warn_s without progress it warns, after recover_s it makes one
// recovery attempt suited to the phase the run is in, after fail_s it fails the run with the reason.
//
// Progress is any of: the step or leg index advanced, a pull started or ended, a kill, a fought mob's health at a new low since the stall
// began (a mob healed back up, or one that evades and comes back, is no progress until it goes below that low), the lead or the leader
// got more than wdMoveYards farther from where the stall began than it had been (net displacement: a bot that runs to and fro is not
// progress), a new order was ACKed, a rest gate finished, an object step ended. Movement in a fight, and anything a stuck run can fake by
// repeating itself after the recovery has fired, is soft (softProgress). The clock is the driver's, so the tests move it by
// hand. The watchdog is looked at from the drivers' own waits (the group driver's sleep, the remote driver's
// tick), never from a goroutine: a single game call that blocks is bounded by its own timeout and is judged at the next wait.

// Default limits of the watchdog, in seconds (WatchdogConfig).
const (
	DefaultWatchdogWarnS    = 120
	DefaultWatchdogRecoverS = 240
	DefaultWatchdogFailS    = 420
	maxWatchdogS            = 24 * 3600
)

// wdFightPhase is the phase of a fight while it goes on: a pull's fight (tracedFight), a fight at a rest gate, a fight on the way. It is
// not the pull step's phase ("pull"), which also holds the step's walks (to a standoff point, through a stealthed pack, to a position to
// look at): those are walks.
const wdFightPhase = "fight"

// wdHealthStep is how many percent of its maximum a fought mob must go below its lowest health of the stall for that to be progress.
const wdHealthStep = 1

// wdMoveYards is how much farther from the stall's start point than ever before in the stall the lead (or the leader) must get for
// that to be progress.
const wdMoveYards = 5.0

// WatchdogConfig sets the three stall levels, in seconds without progress. A zero field is the default (120/240/420); a negative field
// turns that level off. It is the plan's "watchdog" object and dungeon_run's watchdog parameter (which wins field by field).
type WatchdogConfig struct {
	WarnS    int `json:"warn_s,omitempty"`    // a stall trace event and a note naming what the run waits on
	RecoverS int `json:"recover_s,omitempty"` // one recovery attempt suited to the phase
	FailS    int `json:"fail_s,omitempty"`    // the run stops with "stalled: ..."
}

// Merge is c with every non-zero field of over on top.
func (c WatchdogConfig) Merge(over WatchdogConfig) WatchdogConfig {
	if over.WarnS != 0 {
		c.WarnS = over.WarnS
	}
	if over.RecoverS != 0 {
		c.RecoverS = over.RecoverS
	}
	if over.FailS != 0 {
		c.FailS = over.FailS
	}
	return c
}

// wdLimits are the resolved levels; a zero duration is a level that is off.
type wdLimits struct{ warn, recover, fail time.Duration }

func (c WatchdogConfig) limits() wdLimits {
	pick := func(v, def int) time.Duration {
		switch {
		case v < 0:
			return 0
		case v == 0:
			v = def
		}
		return time.Duration(v) * time.Second
	}
	return wdLimits{pick(c.WarnS, DefaultWatchdogWarnS), pick(c.RecoverS, DefaultWatchdogRecoverS), pick(c.FailS, DefaultWatchdogFailS)}
}

// Validate refuses a value above a day and levels that are not in order (warn_s < recover_s < fail_s once the defaults are filled in).
func (c WatchdogConfig) Validate() error {
	for name, v := range map[string]int{"warn_s": c.WarnS, "recover_s": c.RecoverS, "fail_s": c.FailS} {
		if v > maxWatchdogS {
			return fmt.Errorf("watchdog.%s: %d s is above %d (use a negative value to turn the level off)", name, v, maxWatchdogS)
		}
	}
	l := c.limits()
	var prev time.Duration
	prevName := ""
	for _, lv := range []struct {
		name string
		d    time.Duration
	}{{"warn_s", l.warn}, {"recover_s", l.recover}, {"fail_s", l.fail}} {
		if lv.d == 0 {
			continue
		}
		if prev != 0 && lv.d <= prev {
			return fmt.Errorf("watchdog: %s (%.0f) must be above %s (%.0f); a zero field takes the default %d/%d/%d, a negative one turns its level off",
				lv.name, lv.d.Seconds(), prevName, prev.Seconds(), DefaultWatchdogWarnS, DefaultWatchdogRecoverS, DefaultWatchdogFailS)
		}
		prev, prevName = lv.d, lv.name
	}
	return nil
}

// Stall levels, as the report words them.
const (
	stallNone = iota
	stallWarn
	stallRecover
	stallFail
)

var stallNames = [...]string{"none", "warn", "recover", "fail"}

// WatchdogReport is the report's view of the watchdog (Report.Watchdog), for a poller that wants to see at a glance whether a run
// hangs: when the last progress was, how long ago, and which stall level the run is at.
type WatchdogReport struct {
	WarnS    int `json:"warn_s"` // the limits in effect, in seconds; 0: that level is off
	RecoverS int `json:"recover_s"`
	FailS    int `json:"fail_s"`
	// LastProgressAt is when the run last made progress and LastProgress says what it was. StalledForS is the seconds since then (0 while
	// the run waits on something that has its own limit: a death's recovery, a plan wait). StallLevel is none, warn, recover or fail.
	LastProgressAt time.Time `json:"last_progress_at"`
	LastProgress   string    `json:"last_progress"`
	StalledForS    float64   `json:"stalled_for_s"`
	StallLevel     string    `json:"stall_level"`
	// Waiting is what the run waits on now: the phase, the step, the last order and its answer.
	Waiting string `json:"waiting,omitempty"`
	// Totals of the run: stalls warned about, recovery attempts made, and the longest time without progress.
	Warnings     int     `json:"warnings"`
	Recoveries   int     `json:"recoveries"`
	LongestStall float64 `json:"longest_stall_s"`
}

// errStalled is the error of a run the watchdog failed.
var errStalled = errors.New("stalled")

// wdPos is where the Stepper is: the leg, the phase and the index in it.
type wdPos struct {
	leg   int
	phase string
	idx   int
}

// wdHooks are what a driver gives its watchdog.
type wdHooks struct {
	look  func() (pos wdPos, lead Point, ok bool) // the Stepper's position and the lead's (or the leader's); ok false: not known yet
	event func(name string, data map[string]any)  // the trace
	note  func(text string)                       // the report's notes
	// silent, when set, is the recovery of a run whose feed went quiet; it reports whether it handled the stall.
	silent func(ctx context.Context) (what string, handled bool, err error)
}

// wdPhase is a part of the run that may wait: its name, the step it works on, and its recovery (nil: none of its own).
type wdPhase struct {
	name    string
	detail  string
	recover func(ctx context.Context) (string, error)
}

type watchdog struct {
	mu    sync.Mutex
	now   func() time.Time
	lim   wdLimits
	hooks wdHooks

	last   time.Time // the last progress
	what   string
	level  int       // the highest level fired in this stall
	held   int       // > 0: the run waits on something that has its own limit; the stall clock is paused
	holdAt time.Time // when the clock was paused
	off    bool
	// recovering is set while a recovery runs: the orders it sends wait through the same ticks, which must not fire anything.
	recovering bool
	failed     error
	endedAt    time.Time

	phases []wdPhase
	step   string // the plan step the run is on (the Stepper's action label)
	order  string // the last order and its answer, for the note

	pos     wdPos
	havePos bool
	// The movement reference: lead is the lead's position at the last look; ref is where it stood when the stall began (the last
	// progress other than movement), and far the displacement from ref the last counted move (or any uncounted look) reached. Only a
	// look more than wdMoveYards beyond far counts, so a bot going to and fro between two points keeps nothing alive.
	lead      Point
	haveLead  bool
	ref       Point
	haveRef   bool
	far       float64
	recentAck []string // the orders ACKed lately (wdRecentAcks), so that alternating orders are not progress either
	degraded  bool     // the recovery has fired and nothing real has happened since: soft progress is ignored
	fightOn   bool
	fightKill int
	// low is each fought mob's lowest health percent since the stall began (the last progress other than movement or a new low): only a
	// look below it by wdHealthStep is progress, so health that dips and comes back keeps nothing alive.
	low map[packet.GUID]int

	warnings, recoveries int
	longest              time.Duration
}

func newWatchdog(cfg WatchdogConfig, now func() time.Time, h wdHooks) *watchdog {
	w := &watchdog{now: now, lim: cfg.limits(), hooks: h}
	w.last, w.what = now(), "run created"
	return w
}

// start restarts the stall clock at the run's start.
func (w *watchdog) start() {
	if w == nil {
		return
	}
	w.progress("run started")
}

// progress records that the run advanced: the stall is over and the levels can fire again for the next one.
func (w *watchdog) progress(what string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.progressLocked(what)
}

// wdRecentAcks is how many ACKed orders are remembered: an order among them, ACKed again, is not progress.
const wdRecentAcks = 16

// softProgress is progress that a stuck run can fake by repeating itself: a pull started or ended, a new order ACKed. After a recovery
// has fired it does not count until something real happens (a kill, a new health low, a step, a walk getting on, an object step done),
// so that a recovery which re-pulls a mob that is stuck cannot keep the run alive for ever: the next interval without real progress goes
// to the failure.
func (w *watchdog) softProgress(what string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.degraded {
		return
	}
	w.progressLocked(what)
}

// progressLocked ends the stall; the movement reference starts over at where the lead stands now.
func (w *watchdog) progressLocked(what string) {
	if w.failed != nil || w.off {
		return
	}
	w.ref, w.haveRef, w.far = w.lead, w.haveLead, 0
	w.low = nil
	w.clockLocked(what)
}

// clockLocked starts the stall clock over: the run made progress, and the levels can fire again for the next stall.
func (w *watchdog) clockLocked(what string) {
	if w.failed != nil || w.off { // a failed or ended run stays at its level for the report
		return
	}
	w.degraded = false
	now := w.now()
	if el := now.Sub(w.last); el > w.longest && w.held == 0 {
		w.longest = el
	}
	w.last, w.what, w.level = now, what, stallNone
}

// hold pauses the stall clock while the run waits on something with a limit of its own (a leader's corpse run, a plan's wait_for);
// the returned release starts the clock again from that moment.
func (w *watchdog) hold() (release func()) {
	if w == nil {
		return func() {}
	}
	w.mu.Lock()
	if w.held++; w.held == 1 {
		w.holdAt = w.now()
	}
	w.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			// The time spent held is not counted; what was stalled before it still is (a recovery that re-pulls a stuck mob goes through
			// a rest gate each time, and must not start the clock over).
			if w.held--; w.held == 0 {
				if el := w.now().Sub(w.holdAt); el > 0 {
					w.last = w.last.Add(el)
					if now := w.now(); w.last.After(now) {
						w.last = now
					}
				}
			}
		})
	}
}

// gateHold pauses the stall clock for a rest gate while the party is out of combat: a fight inside the gate is watched like any other.
type gateHold struct {
	w   *watchdog
	rel func()
}

// set holds (on) or lets go (off); it does nothing when the state is the one it is in.
func (h *gateHold) set(on bool) {
	switch {
	case on && h.rel == nil:
		h.rel = h.w.hold()
	case !on && h.rel != nil:
		h.rel()
		h.rel = nil
	}
}

// enter marks the run as being in a phase until the returned func is called. recover, when set, is the phase's recovery attempt.
func (w *watchdog) enter(name, detail string, recover func(ctx context.Context) (string, error)) (leave func()) {
	if w == nil {
		return func() {}
	}
	w.mu.Lock()
	w.phases = append(w.phases, wdPhase{name, detail, recover})
	n := len(w.phases)
	w.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			if n <= len(w.phases) {
				w.phases = w.phases[:n-1]
			}
		})
	}
}

// setStep records the plan step the run is on.
func (w *watchdog) setStep(label string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.step = label
	w.mu.Unlock()
}

// ordered records the last order and the answer to it, for the stall note.
func (w *watchdog) ordered(text string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.order = text
	w.mu.Unlock()
}

// acked counts an ACKed order as progress when it is not the order that was ACKed last: the same order sent again and again (a move
// to a point the bot never reaches, a rest order every 45 s) does not keep a stuck run alive.
func (w *watchdog) acked(key string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if slices.Contains(w.recentAck, key) {
		return
	}
	if w.recentAck = append(w.recentAck, key); len(w.recentAck) > wdRecentAcks {
		w.recentAck = w.recentAck[1:]
	}
	if !w.recovering && !w.degraded { // the recovery's own orders are not the run moving again
		w.progressLocked("order acked: " + key)
	}
}

// fightBegin starts the look at a fight: the first fightSignal after it only takes the state.
func (w *watchdog) fightBegin() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.fightOn = false
	w.mu.Unlock()
}

// fightSignal takes a pull's fight state at a look: the kills so far and the health percent of each mob being fought. A kill, or a mob
// at a new low (lowLocked), is progress: a long boss fight that wears the boss down never stalls.
func (w *watchdog) fightSignal(kills int, hp map[packet.GUID]int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.fightOn {
		w.fightOn, w.fightKill = true, kills
	} else if kills > w.fightKill {
		w.fightKill = kills
		w.progressLocked("kill")
	}
	w.lowLocked(hp)
}

// mobHealth is fightSignal for a fight with no kill count of its own (one at a rest gate, one on the way): the mobs' health percent.
func (w *watchdog) mobHealth(hp map[packet.GUID]int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lowLocked(hp)
}

// lowLocked takes the mobs' health at a look. A mob seen for the first time in the stall only sets its low; one at least wdHealthStep
// below its low is progress. The lows are kept across the look (a mob that is out of the fight for a while, evading, keeps its low) and
// across a new low; real progress of another kind starts them over (progressLocked).
func (w *watchdog) lowLocked(hp map[packet.GUID]int) {
	lower := false
	for g, p := range hp {
		low, ok := w.low[g]
		switch {
		case !ok:
			if w.low == nil {
				w.low = map[packet.GUID]int{}
			}
			w.low[g] = p
		case p <= low-wdHealthStep:
			w.low[g], lower = p, true
		}
	}
	if lower {
		w.clockLocked("a fought mob's health fell to a new low")
	}
}

// inPhaseLocked says whether a phase of that name is open (innermost or not).
func (w *watchdog) inPhaseLocked(name string) bool {
	return slices.ContainsFunc(w.phases, func(p wdPhase) bool { return p.name == name })
}

// movedLocked takes the lead's position at a look. Movement is progress only as net displacement: more than wdMoveYards farther from
// the stall's start point than any counted look before (to and fro is nothing). In a fight (wdFightPhase open) it is soft progress: the
// pull's own recovery (`cmd follow`) runs the tank away and the re-pull runs it back, and a fight is judged by kills and health. After
// the recovery has fired it counts only in a walk that is not inside a fight (the walk's recovery plans it again, and the walk getting on
// is what that recovery is for; a walk of the pull step, to a standoff point or through a stealthed pack, is such a walk); anywhere else
// only a kill, a new health low, a step or an object step result is real then.
func (w *watchdog) movedLocked(lead Point) {
	w.lead, w.haveLead = lead, true
	if !w.haveRef {
		w.ref, w.haveRef, w.far = lead, true, 0
		return
	}
	d := dist3(w.ref, lead)
	if d <= w.far+wdMoveYards {
		return
	}
	fight := w.inPhaseLocked(wdFightPhase)
	walk := len(w.phases) > 0 && w.phases[len(w.phases)-1].name == "walk"
	if w.degraded && (fight || !walk) {
		w.far = max(w.far, d) // the run must get beyond where it already was before movement counts again
		return
	}
	w.far = d
	w.clockLocked("the lead moved")
}

// waitingLocked names what the run waits on: the phase and the plan step, then what the phase works on and the last order.
func (w *watchdog) waitingLocked() (phase, step, text string) {
	phase, step = "run", w.step
	detail := ""
	if n := len(w.phases); n > 0 {
		phase = w.phases[n-1].name
	}
	for i := len(w.phases) - 1; i >= 0; i-- {
		if w.phases[i].detail != "" {
			detail = w.phases[i].detail
			break
		}
	}
	if step == "" {
		step = detail
	}
	text = phase
	if step != "" {
		text += " " + step
	}
	if detail != "" && detail != step {
		text += " (" + detail + ")"
	}
	if w.order != "" {
		text += "; last order " + w.order
	}
	return phase, step, text
}

// stallError is the failed run's error text.
func (w *watchdog) stallErrorLocked(stalled time.Duration) error {
	phase, step, _ := w.waitingLocked()
	where := phase
	if step != "" {
		where += " " + step
	}
	return fmt.Errorf("%w: %s for %.0f s, last progress %s", errStalled, where, stalled.Seconds(), w.what)
}

type wdFire struct {
	level   int
	stalled time.Duration
	what    string // the last progress
	waiting string
	recover func(ctx context.Context) (string, error)
	phase   string
	err     error
}

// watch is called from every wait of the driver. It takes in what the Stepper and the lead did, and fires the levels that are due: a
// warning, a recovery attempt, the failure. It returns the failure (and every call after it), or an error of a recovery that ends the
// run (a wipe, a lost binding).
func (w *watchdog) watch(ctx context.Context) error {
	if w == nil {
		return nil
	}
	var (
		pos  wdPos
		lead Point
		ok   bool
	)
	if w.hooks.look != nil {
		pos, lead, ok = w.hooks.look()
	}
	w.mu.Lock()
	if w.off {
		w.mu.Unlock()
		return nil
	}
	if w.failed != nil {
		err := w.failed
		w.mu.Unlock()
		return err
	}
	if w.recovering {
		w.mu.Unlock()
		return nil
	}
	if ok {
		if w.havePos && pos != w.pos {
			w.progressLocked(fmt.Sprintf("step advanced to leg %d %s %d", pos.leg, pos.phase, pos.idx))
		}
		w.pos, w.havePos = pos, true
		w.movedLocked(lead)
	}
	var fires []wdFire
	if w.held == 0 {
		el := w.now().Sub(w.last)
		for lvl := w.level + 1; lvl <= stallFail; lvl++ {
			limit := [...]time.Duration{0, w.lim.warn, w.lim.recover, w.lim.fail}[lvl]
			if limit == 0 || el < limit {
				continue
			}
			f := wdFire{level: lvl, stalled: el, what: w.what}
			f.phase, _, f.waiting = w.waitingLocked()
			switch lvl {
			case stallWarn:
				w.warnings++
			case stallRecover:
				w.recoveries++
				w.degraded = true
				for i := len(w.phases) - 1; i >= 0; i-- {
					if w.phases[i].recover != nil {
						f.recover, f.phase = w.phases[i].recover, w.phases[i].name
						break
					}
				}
			case stallFail:
				f.err = w.stallErrorLocked(el)
				w.failed = f.err
			}
			w.level = lvl
			fires = append(fires, f)
			if lvl == stallFail {
				break
			}
		}
	}
	w.mu.Unlock()

	for _, f := range fires {
		if err := w.fire(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

func (w *watchdog) emit(name string, data map[string]any) {
	if w.hooks.event != nil {
		w.hooks.event(name, data)
	}
}

func (w *watchdog) note(text string) {
	if w.hooks.note != nil {
		w.hooks.note(text)
	}
}

// fightToggle opens the fight phase while a fight goes on inside a loop that waits on something else (a rest gate): set(true) at each
// look that finds the party fighting, set(false) at one that does not and when the loop ends.
type fightToggle struct {
	w      *watchdog
	detail string
	leave  func()
}

func (f *fightToggle) set(on bool) {
	switch {
	case on && f.leave == nil:
		f.leave = f.w.enter(wdFightPhase, f.detail, wdWaitFight)
	case !on && f.leave != nil:
		f.leave()
		f.leave = nil
	}
}

// wdWaitFight is the recovery of a fight at a rest gate or on the way: it sends nothing. The fight has its own bound (FightTimeout), and an
// order of an enclosing phase's recovery (a walk's `cmd move`) would drag the party off its fight.
func wdWaitFight(context.Context) (string, error) {
	return "the party is fighting: nothing sent (the fight has its own limit, and the walk goes on after it)", nil
}

// fire does what one level says.
func (w *watchdog) fire(ctx context.Context, f wdFire) error {
	data := map[string]any{"level": stallNames[f.level], "stalled_s": f.stalled.Seconds(), "last_progress": f.what, "waiting": f.waiting, "phase": f.phase}
	switch f.level {
	case stallWarn:
		w.emit("stall", data)
		w.note(fmt.Sprintf("stall warning: no progress for %.0f s (last progress: %s); waiting on %s", f.stalled.Seconds(), f.what, f.waiting))
	case stallRecover:
		return w.recoverStall(ctx, f, data)
	case stallFail:
		data["error"] = f.err.Error()
		w.emit("stall", data)
		w.note(f.err.Error())
		return f.err
	}
	return nil
}

// recoverStall makes the one recovery attempt of a stall: the feed's when the feed went quiet, else the innermost phase's own.
func (w *watchdog) recoverStall(ctx context.Context, f wdFire, data map[string]any) error {
	w.mu.Lock()
	w.recovering = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.recovering = false
		w.mu.Unlock()
	}()
	var (
		did     string
		err     error
		handled bool
	)
	if w.hooks.silent != nil {
		did, handled, err = w.hooks.silent(ctx)
	}
	if !handled && f.recover != nil {
		did, err = f.recover(ctx)
		handled = true
	}
	if !handled {
		did = "no recovery for the " + f.phase + " phase"
	}
	data["recovery"] = did
	if err != nil {
		data["recovery_error"] = err.Error()
	}
	w.emit("stall", data)
	if err != nil {
		w.note(fmt.Sprintf("stall recovery after %.0f s without progress (%s) failed: %v", f.stalled.Seconds(), did, err))
		if runEnded(ctx, err) {
			return err
		}
		return nil
	}
	w.note(fmt.Sprintf("stall recovery after %.0f s without progress (waiting on %s): %s", f.stalled.Seconds(), f.waiting, did))
	return nil
}

// stop ends the watching: a failed run's closing fight handling and unbind wait through the same ticks and must not be failed again.
func (w *watchdog) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.off = true
	if w.endedAt.IsZero() {
		w.endedAt = w.now()
	}
	w.mu.Unlock()
}

// report is the watchdog's part of the run report, as of now (or of the run's end).
func (w *watchdog) report() *WatchdogReport {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if !w.endedAt.IsZero() {
		now = w.endedAt
	}
	el := now.Sub(w.last)
	if w.held > 0 || el < 0 {
		el = 0
	}
	_, _, waiting := w.waitingLocked()
	r := &WatchdogReport{
		WarnS: int(w.lim.warn / time.Second), RecoverS: int(w.lim.recover / time.Second), FailS: int(w.lim.fail / time.Second),
		LastProgressAt: w.last, LastProgress: w.what, StalledForS: el.Seconds(), StallLevel: stallNames[w.level],
		Waiting: waiting, Warnings: w.warnings, Recoveries: w.recoveries, LongestStall: max(w.longest, el).Seconds(),
	}
	if w.held > 0 {
		r.Waiting = strings.TrimSpace(r.Waiting + " (a wait with its own limit)")
	}
	return r
}

// flagOnce is a one-shot request a recovery leaves for the loop it interrupts: take reports whether it was set, and clears it.
type flagOnce struct {
	mu sync.Mutex
	on bool
}

func (f *flagOnce) set() { f.mu.Lock(); f.on = true; f.mu.Unlock() }
func (f *flagOnce) clear() {
	f.mu.Lock()
	f.on = false
	f.mu.Unlock()
}
func (f *flagOnce) take() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	was := f.on
	f.on = false
	return was
}
