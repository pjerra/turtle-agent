package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
)

// The leader's death (issue #80 step 3a). A leader that dies is not a wipe while a member lives: the driver pauses its
// orders and gets the leader back the way a player does. First it takes a resurrection a member offers (a healer bot casts
// one: SMSG_RESURRECT_REQUEST, answered with CMSG_RESURRECT_RESPONSE), waiting ResWait while one could. Else it releases
// the spirit (CMSG_REPOP_REQUEST), runs as a ghost from the graveyard to the corpse along the navmesh, and reclaims it
// (CMSG_RECLAIM_CORPSE) within 39 yd and past the reclaim delay. A corpse in a dungeon is reached through the entrance
// portal: the ghost walks to the entrance and sends the portal's area trigger (CMSG_AREATRIGGER), which revives it on the
// way in (plan.entrance_trigger). Then it gathers the party and rests. The whole party dead is a wipe and ends the run,
// unless RecoverWipe: then everybody does a corpse run and the run goes on from the current step.
// The behaviour follows mod-playerbots' DeadStrategy (ideas only; no code).

// DeathState is the leader's death state as the Game reports it.
type DeathState struct {
	Dead  bool // health 0: the body is where it died
	Ghost bool // the spirit was released
	// Offer is a resurrection a member or NPC offers and the leader may accept; nil when none.
	Offer *Offer
	// ReclaimIn is how long until the corpse may be reclaimed (0: now, or unknown).
	ReclaimIn time.Duration
}

// Offer is an open SMSG_RESURRECT_REQUEST.
type Offer struct {
	Name     string // the caster's name when the server sent one (empty for a player)
	Sickness bool   // accepting costs resurrection sickness
}

// Corpse is where the corpse is (MSG_CORPSE_QUERY). Inside means the corpse lies in a dungeon the leader is not in: Pos
// is then the entrance to run to on map Map; else Pos is the corpse on Map == CorpseMap.
type Corpse struct {
	Found     bool
	Map       uint32
	CorpseMap uint32
	Pos       Point
	Inside    bool
}

// maxAccepts bounds the answers to resurrection offers in one phase of a recovery that did not bring the leader back.
const maxAccepts = 3

// reclaimReach is how close to the corpse the ghost stands before it reclaims: the server accepts 39 yd (CORPSE_RECLAIM_RADIUS),
// the ghost stops a few yards short of that so that a position error does not make the packet ignored.
const reclaimReach = 34

// GMMode says how the Driver learns whether the leader's account is a GM.
type GMMode int

const (
	// GMAssumed (the zero value) takes the account for a GM and sends the GM commands, as the first driver did.
	GMAssumed GMMode = iota
	// GMNone takes the account for a player's: the driver sends no GM command at all.
	GMNone
	// GMProbe sends ".gm visible on" once and reads the server's answer (Game.ProbeGM): a GM account runs it and answers with
	// a notification, a player account has it said aloud in "say" (Chat.cpp:1773-1774). No clear answer counts as GM.
	GMProbe
)

// leaderEvent sends a dungeon_leader event of phase for the current leg.
func (d *Driver) leaderEvent(phase string, extra map[string]any) {
	data := map[string]any{"phase": phase, "leg": max(d.curLeg, 0)}
	for k, v := range extra {
		data[k] = v
	}
	d.g.Emit(events.KindDungeonLeader, "leader "+phase, data)
}

// check looks at the party after a wait: deaths are counted, a dead leader is brought back, and a wipe ends the run (or is
// recovered from with RecoverWipe). Every wait of the driver goes through it.
func (d *Driver) check(ctx context.Context) error {
	err := d.observe()
	if err != nil {
		if errors.Is(err, errWipe) && d.cfg.RecoverWipe && !d.recovering {
			return d.recoverWipe(ctx, err)
		}
		return err
	}
	if !d.recovering && d.g.Self().Dead() {
		err := d.recoverLeader(ctx)
		if errors.Is(err, errWipe) && d.cfg.RecoverWipe {
			return d.recoverWipe(ctx, err)
		}
		return err
	}
	return nil
}

// sleepQuiet waits dur and counts the deaths, without bringing anyone back: the recovery's own waits.
func (d *Driver) sleepQuiet(ctx context.Context, dur time.Duration) error {
	if err := d.clk.Sleep(ctx, dur); err != nil {
		return err
	}
	return d.look()
}

// look counts the deaths; it reports a wipe unless one is being recovered from (everybody being dead is what that is for).
func (d *Driver) look() error {
	if d.inWipe {
		d.countDeaths()
		return nil
	}
	return d.observe()
}

// healerAlive is true when a living online member could resurrect the leader: a healer class by its unit row, or a member
// whose class is not known (out of view).
func (d *Driver) healerAlive() bool {
	classes := map[uint64]uint8{}
	for _, u := range d.g.Units() {
		if u.Type == "player" && u.Class != 0 {
			classes[uint64(u.GUID)] = u.Class
		}
	}
	for _, m := range d.g.Party() {
		if !m.Online || m.Dead {
			continue
		}
		if c := classes[uint64(m.GUID)]; c == 0 || HealerClasses[c] {
			return true
		}
	}
	return false
}

// recoverLeader gets a dead leader back (see the top of this file), then gathers the party and rests. It returns nil when
// the run can go on, errWipe when the whole party died meanwhile, or why the leader could not come back.
func (d *Driver) recoverLeader(ctx context.Context) error {
	d.leaderDeaths++
	if d.leaderDeaths > d.cfg.MaxLeaderDeaths {
		return fmt.Errorf("the leader has died %d times: giving up", d.leaderDeaths)
	}
	if err := d.bringBack(ctx, nil); err != nil {
		return err
	}
	return d.afterBack(ctx)
}

// bringBack is recoverLeader and the leader's part of recoverWipe. orders, when set, is called at every poll to keep the
// members' own recovery going (recoverWipe).
func (d *Driver) bringBack(ctx context.Context, orders func(context.Context) error) error {
	began := d.clk.Now()
	prev := d.recovering
	d.recovering = true
	leg := max(d.curLeg, 0)
	if !d.deathCounted { // a wipe that overtakes a leader's recovery goes on with the same death
		d.deathCounted = true
		d.rep.update(func(r *Report) { r.LeaderDeaths++ })
		d.legStat(func(l *LegReport) { l.LeaderDeaths++ })
		d.leaderEvent("died", map[string]any{"healer_alive": d.healerAlive()})
		d.rep.note(fmt.Sprintf("leg %d: the leader died", leg))
	}
	how, err := d.leaderBack(ctx, orders == nil, orders)
	d.recovering = prev
	secs := d.clk.Now().Sub(began).Seconds()
	d.rep.update(func(r *Report) { r.SecondsDead += secs })
	d.legStat(func(l *LegReport) { l.SecondsDead += secs })
	if err != nil {
		if errors.Is(err, errWipe) || ctxDone(err) {
			return err
		}
		d.leaderEvent("gave_up", map[string]any{"error": err.Error(), "seconds": secs})
		return fmt.Errorf("the leader could not come back: %w", err)
	}
	d.recoveries++
	d.deathCounted = false
	switch how {
	case "resurrected":
		d.rep.update(func(r *Report) { r.Resurrections++ })
		d.legStat(func(l *LegReport) { l.Resurrections++ })
	default:
		d.rep.update(func(r *Report) { r.CorpseRuns++ })
		d.legStat(func(l *LegReport) { l.CorpseRuns++ })
	}
	d.leaderEvent("back", map[string]any{"how": how, "seconds": secs})
	d.rep.note(fmt.Sprintf("leg %d: the leader was back after %.0f s (%s)", leg, secs, how))
	if err := d.noteSickness(ctx); err != nil {
		return err
	}
	return nil
}

// afterBack gathers the party round the leader and rests, the way a leg starts.
func (d *Driver) afterBack(ctx context.Context) error {
	if err := d.g.Chat(ctx, "party", ".bot action follow"); err != nil {
		return err
	}
	return d.restGate(ctx)
}

// leaderBack returns how the leader came back: "resurrected" (a resurrection was accepted, or somebody revived it) or
// "corpse_run". wait says that a living healer is worth waiting for. orders is called at every poll.
func (d *Driver) leaderBack(ctx context.Context, wait bool, orders func(context.Context) error) (string, error) {
	poll := func() error {
		if orders != nil {
			if err := orders(ctx); err != nil {
				return err
			}
		}
		return d.sleepQuiet(ctx, d.cfg.Poll)
	}
	alive := func(dt DeathState) bool { return !dt.Dead && !dt.Ghost }

	// 1. A resurrection a member offers, while one could. Three answers that do not bring the leader back end the hope.
	resEnd := d.clk.Now().Add(d.cfg.ResWait)
	tries := 0
	for {
		dt := d.g.Death()
		if alive(dt) {
			return "resurrected", nil
		}
		if dt.Offer != nil && tries < maxAccepts {
			tries++
			ok, err := d.acceptOffer(ctx, dt.Offer)
			if err != nil {
				return "", err
			}
			if ok {
				return "resurrected", nil
			}
			continue
		}
		if dt.Ghost || !wait || !d.healerAlive() || !d.clk.Now().Before(resEnd) {
			break
		}
		if err := poll(); err != nil {
			return "", err
		}
	}

	// 2. Release the spirit.
	if dt := d.g.Death(); dt.Dead && !dt.Ghost {
		if err := d.release(ctx, orders); err != nil {
			return "", err
		}
	}

	// 3. The corpse run.
	return d.corpseRun(ctx, orders)
}

// acceptOffer answers an offered resurrection with yes and waits for the leader to be alive. false means it did not come
// (the offer ran out); the caller looks again.
func (d *Driver) acceptOffer(ctx context.Context, o *Offer) (bool, error) {
	if err := d.g.AcceptResurrect(ctx); err != nil {
		d.rep.note("could not accept the resurrection: " + err.Error())
		return false, d.sleepQuiet(ctx, d.cfg.Poll)
	}
	d.leaderEvent("resurrect_accepted", map[string]any{"from": o.Name, "sickness": o.Sickness})
	end := d.clk.Now().Add(d.cfg.ResAcceptWait)
	for d.clk.Now().Before(end) {
		if err := d.sleepQuiet(ctx, d.cfg.Poll/2+time.Millisecond); err != nil {
			return false, err
		}
		if dt := d.g.Death(); !dt.Dead && !dt.Ghost {
			return true, nil
		}
	}
	d.rep.note("a resurrection was accepted but the leader stayed dead for " + d.cfg.ResAcceptWait.String())
	return false, nil
}

// release sends the release and waits for the ghost (the server moves it to the graveyard).
func (d *Driver) release(ctx context.Context, orders func(context.Context) error) error {
	for try := 1; try <= 3; try++ {
		if err := d.g.ReleaseSpirit(ctx); err != nil {
			if dt := d.g.Death(); dt.Ghost || !dt.Dead {
				return nil
			}
			return fmt.Errorf("release the spirit: %w", err)
		}
		d.leaderEvent("released", nil)
		end := d.clk.Now().Add(d.cfg.ReleaseWait)
		for d.clk.Now().Before(end) {
			if orders != nil {
				if err := orders(ctx); err != nil {
					return err
				}
			}
			if err := d.sleepQuiet(ctx, d.cfg.Poll); err != nil {
				return err
			}
			dt := d.g.Death()
			if dt.Ghost || !dt.Dead {
				return nil
			}
			if dt.Offer != nil { // a healer could be quicker than the release
				return nil
			}
		}
	}
	return fmt.Errorf("the spirit was not released: still dead after 3 tries of %s", d.cfg.ReleaseWait)
}

// corpseRun runs the ghost to the corpse (through the dungeon's entrance when the corpse is inside) and reclaims it, taking
// any resurrection offered on the way. It gives up after CorpseRunTimeout.
func (d *Driver) corpseRun(ctx context.Context, orders func(context.Context) error) (string, error) {
	began := d.clk.Now()
	d.leaderEvent("corpse_run", nil)
	noCorpse := 0
	portalTries := 0
	accepts := 0
	ownWay := false // the ghost reclaimed its corpse or went in through the portal: not a resurrection
	for {
		dt := d.g.Death()
		if !dt.Dead && !dt.Ghost {
			if ownWay {
				return "corpse_run", nil
			}
			return "resurrected", nil
		}
		if dt.Offer != nil && accepts < maxAccepts {
			accepts++
			ok, err := d.acceptOffer(ctx, dt.Offer)
			if err != nil {
				return "", err
			}
			if ok {
				return "resurrected", nil
			}
			continue
		}
		if el := d.clk.Now().Sub(began); el > d.cfg.CorpseRunTimeout {
			return "", fmt.Errorf("corpse run: gave up after %.0f min", d.cfg.CorpseRunTimeout.Minutes())
		}
		if orders != nil {
			if err := orders(ctx); err != nil {
				return "", err
			}
		}
		if !dt.Ghost { // released a moment ago, the ghost has not shown yet
			if err := d.sleepQuiet(ctx, d.cfg.Poll); err != nil {
				return "", err
			}
			continue
		}
		c, err := d.g.CorpseQuery(ctx)
		if err != nil || !c.Found {
			if noCorpse++; noCorpse >= 5 {
				if err != nil {
					return "", fmt.Errorf("corpse run: %w", err)
				}
				return "", errors.New("corpse run: the server knows no corpse (it turned to bones)")
			}
			if err := d.sleepQuiet(ctx, d.cfg.Poll*2); err != nil {
				return "", err
			}
			continue
		}
		noCorpse = 0
		here := d.g.Map()
		switch {
		case c.Map == here && !c.Inside: // the corpse is on this map
			if dist3(d.g.Position(), c.Pos) <= reclaimReach {
				if dt.ReclaimIn > 0 {
					d.leaderEvent("waiting_reclaim_delay", map[string]any{"ms": dt.ReclaimIn.Milliseconds()})
					if err := d.sleepQuiet(ctx, min(dt.ReclaimIn+250*time.Millisecond, 5*time.Second)); err != nil {
						return "", err
					}
					continue
				}
				if err := d.g.ReclaimCorpse(ctx); err != nil {
					d.rep.note("reclaim: " + err.Error())
				} else {
					ownWay = true
					d.leaderEvent("reclaimed", nil)
				}
				if err := d.sleepQuiet(ctx, d.cfg.Poll*2); err != nil {
					return "", err
				}
				continue
			}
			if err := d.ghostWalk(ctx, c.Pos); err != nil {
				return "", err
			}
		case c.Inside && c.Map == here: // the corpse is in a dungeon: through the entrance portal
			if dist3(d.g.Position(), c.Pos) > ArriveRadius+2 {
				if err := d.ghostWalk(ctx, c.Pos); err != nil {
					return "", err
				}
				continue
			}
			if d.plan.EntranceTrigger == 0 {
				return "", fmt.Errorf("the corpse lies inside map %d and the plan %q has no entrance_trigger to enter it as a ghost", c.CorpseMap, d.plan.Name)
			}
			if portalTries++; portalTries > 4 {
				return "", fmt.Errorf("area trigger %d did not take the ghost into map %d (4 tries)", d.plan.EntranceTrigger, c.CorpseMap)
			}
			ownWay = true
			d.leaderEvent("portal", map[string]any{"trigger": d.plan.EntranceTrigger, "try": portalTries})
			if err := d.g.AreaTrigger(ctx, d.plan.EntranceTrigger); err != nil {
				return "", fmt.Errorf("area trigger %d: %w", d.plan.EntranceTrigger, err)
			}
			if err := d.sleepQuiet(ctx, 3*time.Second); err != nil {
				return "", err
			}
		default: // another map than the corpse's or its entrance's: the release teleport is under way, or the graveyard is elsewhere
			if err := d.sleepQuiet(ctx, d.cfg.Poll*2); err != nil {
				return "", err
			}
			if d.clk.Now().Sub(began) > time.Minute && here != c.Map {
				return "", fmt.Errorf("corpse run: the ghost is on map %d, the corpse on map %d (entrance map %d)", here, c.CorpseMap, c.Map)
			}
		}
	}
}

// ghostWalk walks the ghost along the navmesh toward p, stopping the walk when a resurrection is offered or the leader
// lives again. A walk that could not arrive is not an error: the corpse run looks again after a pause.
func (d *Driver) ghostWalk(ctx context.Context, p Point) error {
	wctx, cancel := context.WithCancel(ctx)
	stop := d.watchUntil(wctx, cancel, func() bool {
		dt := d.g.Death()
		return dt.Offer != nil || (!dt.Dead && !dt.Ghost)
	})
	r, err := d.g.WalkPath(wctx, p)
	fired := stop()
	cancel()
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if oerr := d.look(); oerr != nil {
		return oerr
	}
	if fired {
		return nil
	}
	if err != nil || r.Result != "arrived" {
		why := r.Result + ": " + r.Reason
		if err != nil {
			why = err.Error()
		}
		d.leaderEvent("walk_incomplete", map[string]any{"why": why, "left": r.DistLeft})
		return d.sleepQuiet(ctx, d.cfg.Poll*2)
	}
	// Arrived, or close enough: look again, after a beat so that a walk that reports arrival without getting anywhere
	// cannot make the corpse run spin.
	return d.sleepQuiet(ctx, d.cfg.Poll)
}

// watchUntil looks, in real time every WalkWatch, for cond to hold while a walk goes on and calls cancel when it does. The
// returned func ends the look and says whether it fired.
func (d *Driver) watchUntil(ctx context.Context, cancel context.CancelFunc, cond func() bool) func() bool {
	done, finished := make(chan struct{}), make(chan struct{})
	fired := false
	go func() {
		defer close(finished)
		t := time.NewTicker(d.cfg.WalkWatch)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if cond() {
					fired = true
					cancel()
					return
				}
			}
		}
	}()
	return func() bool {
		close(done)
		<-finished
		return fired
	}
}

// noteSickness reports resurrection sickness on the leader after it came back, and with WaitSickness waits it out.
func (d *Driver) noteSickness(ctx context.Context) error {
	src, _ := d.g.(SelfAuraSource)
	find := func() (objects.Aura, bool) {
		if src == nil {
			return objects.Aura{}, false
		}
		auras, _ := src.SelfAuras()
		for _, a := range auras {
			if strings.Contains(strings.ToLower(a.Name), "resurrection sickness") {
				return a, true
			}
		}
		return objects.Aura{}, false
	}
	// The aura shows a moment after the revive.
	a, ok := find()
	for i := 0; !ok && i < 3; i++ {
		if err := d.sleepQuiet(ctx, d.cfg.Poll); err != nil {
			return err
		}
		a, ok = find()
	}
	if !ok {
		return nil
	}
	left := "?"
	if a.RemainingMS != nil {
		left = fmt.Sprintf("%.0f", float64(*a.RemainingMS)/1000)
	}
	if !d.cfg.WaitSickness {
		d.rep.note(fmt.Sprintf("the leader has resurrection sickness (%s s left); not waited out (wait_sickness is off)", left))
		return nil
	}
	end := d.clk.Now().Add(d.cfg.SicknessMax)
	for d.clk.Now().Before(end) {
		if _, ok := find(); !ok {
			d.rep.note("resurrection sickness waited out")
			return nil
		}
		if err := d.sleepQuiet(ctx, d.cfg.RestPoll); err != nil {
			return err
		}
	}
	d.rep.note("resurrection sickness still on after " + d.cfg.SicknessMax.String() + "; went on")
	return nil
}

// ---- the whole party dead ----

// memberOrders sends the members their own recovery: after startAfter of being dead they are told to release
// (".bot action release"), a few seconds later to run to their corpses (".bot action corpse run"), and that again every 30 s
// while any is still dead. It is the orders func of a recovery.
type memberOrders struct {
	d          *Driver
	startAfter time.Duration
	began      time.Time
	released   bool
	lastRun    time.Time
}

func (m *memberOrders) send(ctx context.Context) error {
	d := m.d
	now := d.clk.Now()
	if now.Sub(m.began) < m.startAfter {
		return nil
	}
	if !m.released {
		m.released = true
		return d.g.Chat(ctx, "party", ".bot action release")
	}
	dead := false
	for _, mb := range d.g.Party() {
		if mb.Online && mb.Dead {
			dead = true
		}
	}
	if dead && (m.lastRun.IsZero() && now.Sub(m.began) >= m.startAfter+5*time.Second || !m.lastRun.IsZero() && now.Sub(m.lastRun) >= 30*time.Second) {
		m.lastRun = now
		return d.g.Chat(ctx, "party", ".bot action corpse run")
	}
	return nil
}

// membersDead lists the online members that are dead.
func (d *Driver) membersDead() []string {
	var out []string
	for _, m := range d.g.Party() {
		if m.Online && m.Dead {
			out = append(out, m.Name)
		}
	}
	return out
}

// recoverWipe handles the whole party being dead with RecoverWipe on: it counts the wipe, tells the members to release and
// run to their corpses, brings the leader back (a corpse run: nobody is left to resurrect it), waits for the members, and
// rests. The run goes on from the step it was on. Bounded by MaxWipeRecoveries and WipeRecoverTimeout.
func (d *Driver) recoverWipe(ctx context.Context, cause error) error {
	if d.wipeRecoveries >= d.cfg.MaxWipeRecoveries {
		return fmt.Errorf("%w (after %d recoveries)", cause, d.wipeRecoveries)
	}
	d.wipeRecoveries++
	d.countWipe(cause)
	d.rep.note(fmt.Sprintf("leg %d: wipe %d; recovering by corpse run (recover_wipe)", max(d.curLeg, 0), d.wipeRecoveries))
	d.leaderEvent("wipe_recovery", map[string]any{"n": d.wipeRecoveries})
	began := d.clk.Now()
	mo := &memberOrders{d: d, began: began}
	d.inWipe = true
	err := d.wipeRun(ctx, mo, began)
	d.inWipe = false
	if err != nil {
		return err
	}
	d.rep.note(fmt.Sprintf("wipe recovered in %.0f s", d.clk.Now().Sub(began).Seconds()))
	return d.afterBack(ctx)
}

// wipeRun is the recovery proper: the leader's way back if it is dead, then the wait for the members.
func (d *Driver) wipeRun(ctx context.Context, mo *memberOrders, began time.Time) error {
	if d.g.Self().Dead() {
		if err := d.bringBack(ctx, mo.send); err != nil {
			return err
		}
	}
	prev := d.recovering
	d.recovering = true
	defer func() { d.recovering = prev }()
	return d.waitMembers(ctx, mo, began)
}

// waitMembers keeps the members' recovery going until none is dead, for WipeRecoverTimeout from began.
func (d *Driver) waitMembers(ctx context.Context, mo *memberOrders, began time.Time) error {
	for {
		dead := d.membersDead()
		if len(dead) == 0 {
			return nil
		}
		if d.clk.Now().Sub(began) > d.cfg.WipeRecoverTimeout {
			return fmt.Errorf("%d members are still dead after %.0f min: %s", len(dead), d.cfg.WipeRecoverTimeout.Minutes(), strings.Join(dead, ", "))
		}
		if err := mo.send(ctx); err != nil {
			return err
		}
		// Everybody being dead is the case here, so this wait only counts the deaths: it is no new wipe.
		if err := d.clk.Sleep(ctx, d.cfg.Poll*2); err != nil {
			return err
		}
		d.countDeaths()
		if d.g.Self().Dead() { // the leader died again while waiting
			if err := d.bringBack(ctx, mo.send); err != nil {
				return err
			}
		}
	}
}

// recoverDeadMembers is the rest gate's answer to dead members when no GM revive is possible and RecoverWipe is on: a
// healer may resurrect them (ResWait), else they release and run to their corpses, as in a wipe.
func (d *Driver) recoverDeadMembers(ctx context.Context) error {
	began := d.clk.Now()
	d.rep.note(fmt.Sprintf("leg %d: dead members %v; waiting for a resurrection, then they release and run to their corpses", max(d.curLeg, 0), d.membersDead()))
	mo := &memberOrders{d: d, began: began, startAfter: d.cfg.ResWait}
	prev := d.recovering
	d.recovering = true
	defer func() { d.recovering = prev }()
	return d.waitMembers(ctx, mo, began)
}
