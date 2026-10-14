package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The progress watchdog (README "Run watchdog"). A run that waits on something that never comes (an order nobody answers, a rest gate
// whose mana does not rise, a carrier that stands still, a feed that went quiet) used to sit there until a caller noticed. Both drivers
// feed one watchdog with their progress and ask it at every wait; after warn_s without progress it warns, after recover_s it makes one
// recovery attempt suited to the phase the run is in, after fail_s it fails the run with the reason.
//
// Progress is any of: the step or leg index advanced, a pull started or ended, a kill (or the fought mobs lost health), the lead or the
// leader moved more than wdMoveYards, a new order was ACKed, a rest gate finished, an object step ended. The clock is the driver's, so
// the tests move it by hand. The watchdog is looked at from the drivers' own waits (the group driver's sleep, the remote driver's
// tick), never from a goroutine: a single game call that blocks is bounded by its own timeout and is judged at the next wait.

// Default limits of the watchdog, in seconds (WatchdogConfig).
const (
	DefaultWatchdogWarnS    = 120
	DefaultWatchdogRecoverS = 240
	DefaultWatchdogFailS    = 420
	maxWatchdogS            = 24 * 3600
)

// wdMoveYards is how far the lead (or the leader) must move from where it was last counted for that to be progress.
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

	last  time.Time // the last progress
	what  string
	level int // the highest level fired in this stall
	held  int // > 0: the run waits on something that has its own limit; the stall clock is paused
	off   bool
	// recovering is set while a recovery runs: the orders it sends wait through the same ticks, which must not fire anything.
	recovering bool
	failed     error
	endedAt    time.Time

	phases []wdPhase
	step   string // the plan step the run is on (the Stepper's action label)
	order  string // the last order and its answer, for the note

	pos       wdPos
	havePos   bool
	anchor    Point
	haveAnch  bool
	lastAck   string
	fightOn   bool
	fightKill int
	fightHP   int

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

func (w *watchdog) progressLocked(what string) {
	if w.failed != nil || w.off { // a failed or ended run stays at its level for the report
		return
	}
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
	w.held++
	w.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.held--; w.held == 0 {
				w.progressLocked("a wait with its own limit ended")
			}
		})
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
	if key != w.lastAck {
		w.lastAck = key
		if !w.recovering { // the recovery's own orders are not the run moving again
			w.progressLocked("order acked: " + key)
		}
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

// fightSignal takes a fight's state at a look: the kills so far and the summed health percent of the mobs being fought. A kill, or
// less health than at the last look, is progress: a long boss fight that wears the boss down never stalls.
func (w *watchdog) fightSignal(kills, hp int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.fightOn {
		w.fightOn, w.fightKill, w.fightHP = true, kills, hp
		return
	}
	if kills > w.fightKill {
		w.progressLocked("kill")
	} else if hp < w.fightHP {
		w.progressLocked("the fought mobs lost health")
	}
	w.fightKill, w.fightHP = kills, hp
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
		if !w.haveAnch {
			w.anchor, w.haveAnch = lead, true
		} else if dist3(w.anchor, lead) > wdMoveYards {
			w.anchor = lead
			w.progressLocked("the lead moved")
		}
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
