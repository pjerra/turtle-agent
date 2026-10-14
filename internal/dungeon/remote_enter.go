package dungeon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// The enter phase of a remote run (issue #83, deploy-19i BLOCKERS fix 1). A dungeon's instance bind belongs to the group, and a
// bot that leaves its group inside the dungeon is sent home about a minute later. So the party that fights must be the bots' own
// group from the first step in: the agent stays outside and ungrouped, `commander bind` forms (or keeps) the bots-only group
// outside, and that group's lead bot is the first to go in, which binds the instance to it. Then the others are summoned to the lead.
//
//	GM (an account of developer level, the plan names a game_tele): `.tele name <lead> <tele>`, then `cmd summon <bot>` for each other
//	bot, SummonGap apart.
//	walk (any account, the plan names the portal): `cmd move` legs to the area trigger on the outer map; the bots that do not follow
//	the lead in by themselves are summoned like the above.
//
// After the entry every bot's feed map must be the plan's within EnterVerify, and the group is not touched again until the run ends:
// no leave, invite or bind (groupLocked; the only group change left is the unbind that ends the run).

// Values of RemoteOptions.Enter.
const (
	EnterAuto = "auto" // the default: GM tele when the account is a developer and the plan names a tele, else the walk
	EnterGM   = "gm"   // `.tele name`, no account probe
	EnterWalk = "walk" // walk the lead bot into the portal
	EnterOff  = "off"  // do not enter: a lead bot outside the plan's map fails the run
)

// MaxInstancesPerHour is the core's limit of new instances one account may enter in an hour (MAX_INSTANCE_PER_ACCOUNT_PER_HOUR,
// Player.h:874; MapManager.cpp:238-244 refuses the next one with TRANSFER_ABORT_TOO_MANY_INSTANCES).
const MaxInstancesPerHour = 5

// Enterer is the optional part of a RemoteGame the enter phase needs: the agent's own character, which must stay outside and
// ungrouped, and the one GM command it sends.
type Enterer interface {
	// InGroup says whether the agent's character is in a group; AgentMap is the map it stands on.
	InGroup() bool
	AgentMap() uint32
	// LeaveGroup sends the agent's character out of its group.
	LeaveGroup(ctx context.Context) error
	// AccountLevel is ProbeAccount of the group driver: the agent's account security level.
	AccountLevel(ctx context.Context) (level int, known bool, err error)
	// TeleName sends `.tele name <player> <location>` (developer level, Chat.cpp:709).
	TeleName(ctx context.Context, player, location string) error
}

// EntryLedger is the optional memory of the instances the account entered, so that a run does not try a 6th within the hour. It is
// kept per account (the owned bots share the agent's account), in a small file.
type EntryLedger interface {
	// Recent are the entries of the last hour before now, oldest first.
	Recent(now time.Time) []time.Time
	// Record adds an entry at now.
	Record(now time.Time) error
}

// EnterReport is the enter phase in the report (RemoteReport.Enter; absent when the party was already inside).
type EnterReport struct {
	Method         string             `json:"method"`                  // gm or walk
	AccountLevel   *int               `json:"account_level,omitempty"` // read by the probe (auto only)
	AgentLeftGroup bool               `json:"agent_left_group"`        // the agent's character had to leave a group first
	Tele           string             `json:"tele,omitempty"`          // the game_tele the lead bot was sent to
	Summoned       []string           `json:"summoned,omitempty"`      // the bots summoned to the lead bot, in order
	Arrived        map[string]float64 `json:"arrived,omitempty"`       // seconds after the phase began at which each bot was seen on the plan's map
	Seconds        float64            `json:"seconds"`
	// Entries is the instances this run entered (1); EntriesLastHour the account's entries in the last hour with it (-1: not tracked);
	// EntryLimit the core's limit per account and hour.
	Entries         int    `json:"entries"`
	EntriesLastHour int    `json:"entries_last_hour"`
	EntryLimit      int    `json:"entry_limit"`
	Warning         string `json:"warning"`
}

const entryWarning = "every new instance counts toward the account's limit of 5 new instances per hour (the 6th is refused with TRANSFER_ABORT_TOO_MANY_INSTANCES); a GM teleport counts too, and only a mangosd restart clears the list"

// offMap are the bots of the run that are not on the plan's map in the newest frame: the party of the feed, and with missing any bot
// the run named that the feed does not list.
func (d *RemoteDriver) offMap(missing bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range d.fr.Party {
		seen[strings.ToLower(m.Name)] = true
		if m.Map != d.plan.Map {
			out = append(out, fmt.Sprintf("%s (map %d)", m.Name, m.Map))
		}
	}
	for _, n := range append([]string{d.opt.Lead}, d.opt.Bots...) {
		if missing && n != "" && !seen[strings.ToLower(n)] {
			out = append(out, n+" (not in the feed)")
		}
	}
	return out
}

// needsEntry says whether the party, as the first frame shows it, is not inside the plan's map.
func (d *RemoteDriver) needsEntry() bool { return len(d.offMap(false)) > 0 }

// enterNote records a step of the enter phase as a note and a dungeon_step event.
func (d *RemoteDriver) enterNote(text string) {
	d.rep.note("enter: " + text)
	d.g.Emit(events.KindDungeonStep, "enter: "+text, map[string]any{"action": "enter", "ok": true, "mode": "remote"})
}

func (d *RemoteDriver) enterReport(f func(*EnterReport)) {
	d.remote(func(r *RemoteReport) {
		if r.Enter != nil {
			f(r.Enter)
		}
	})
}

// enter gets the bound party into the plan's map as its own group (see the top of this file). The caller has the first frame.
func (d *RemoteDriver) enter(ctx context.Context) error {
	began := d.clk.Now()
	e, ok := d.g.(Enterer)
	if !ok {
		return fmt.Errorf("the party is not inside map %d (%s) and this game cannot enter a dungeon: bring the group in first, or pass enter \"off\"", d.plan.Map, strings.Join(d.offMap(false), ", "))
	}
	er := &EnterReport{EntryLimit: MaxInstancesPerHour, EntriesLastHour: -1, Warning: entryWarning}
	d.remote(func(r *RemoteReport) { r.Enter = er })
	d.enterNote(fmt.Sprintf("the party is not inside map %d (%s)", d.plan.Map, strings.Join(d.offMap(false), ", ")))

	if m := e.AgentMap(); m == d.plan.Map {
		return fmt.Errorf("enter: the agent's own character is on map %d, inside the dungeon: it must stay outside (and ungrouped) while the bots enter as their own group", m)
	}
	if err := d.agentUngrouped(ctx, e); err != nil {
		return err
	}
	method, err := d.entryMethod(ctx, e)
	if err != nil {
		return err
	}
	d.enterReport(func(r *EnterReport) { r.Method = method })
	if err := d.checkEntryLimit(); err != nil {
		return err
	}

	// From here the group is not changed any more.
	d.groupLocked = true
	switch method {
	case EnterGM:
		if err := d.enterByTele(ctx, e, began); err != nil {
			return err
		}
	default:
		if err := d.enterByWalk(ctx, began); err != nil {
			return err
		}
	}
	// The others: summoned to the lead bot, which is inside (and so are the ones that came by themselves).
	if err := d.summonRest(ctx, began); err != nil {
		return err
	}
	if err := d.verifyInside(ctx, began); err != nil {
		return err
	}
	secs := d.clk.Now().Sub(began).Seconds()
	d.enterReport(func(r *EnterReport) { r.Seconds = secs })
	d.enterNote(fmt.Sprintf("every bot is on map %d, %.0f s after the phase began; the group is not changed until the run ends", d.plan.Map, secs))
	return nil
}

// agentUngrouped makes sure the agent's character is in no group. A bind that keeps the bots' party removes the agent from it last,
// so it waits a moment for that before it leaves by itself; the bots do not leave with their master once bound.
func (d *RemoteDriver) agentUngrouped(ctx context.Context, e Enterer) error {
	wait := func() error {
		for i := 0; i < 4 && e.InGroup(); i++ {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
		}
		return nil
	}
	if err := wait(); err != nil {
		return err
	}
	if !e.InGroup() {
		return nil
	}
	if d.groupLocked {
		return errors.New("enter: the agent is in a group again after the entry; the group is not changed until the run ends")
	}
	if err := e.LeaveGroup(ctx); err != nil {
		return fmt.Errorf("enter: the agent's character could not leave its group: %w", err)
	}
	if err := wait(); err != nil {
		return err
	}
	if e.InGroup() {
		return errors.New("enter: the agent's character is still in a group after leaving it")
	}
	d.enterReport(func(r *EnterReport) { r.AgentLeftGroup = true })
	d.enterNote("the agent's character left its group (the bound bots stay together)")
	return nil
}

// entryMethod picks the way in. auto asks the server for the account level once (the group driver's probe): a developer with a
// tele in the plan teleports the lead bot, anyone else with a portal in the plan walks it.
func (d *RemoteDriver) entryMethod(ctx context.Context, e Enterer) (string, error) {
	p := d.plan
	switch strings.ToLower(d.opt.Enter) {
	case EnterGM:
		if p.EntranceTele == "" {
			return "", fmt.Errorf("enter gm: plan %s has no entrance_tele (the game_tele name of its entrance)", p.Name)
		}
		return EnterGM, nil
	case EnterWalk:
		if p.Portal == nil {
			return "", fmt.Errorf("enter walk: plan %s has no portal (the entrance's area trigger on the outer map)", p.Name)
		}
		return EnterWalk, nil
	}
	level, known, err := e.AccountLevel(ctx)
	why := ""
	switch {
	case err != nil:
		why = "the account level could not be read (" + err.Error() + ")"
	case !known:
		why = "the server did not answer .account"
	default:
		lv := level
		d.enterReport(func(r *EnterReport) { r.AccountLevel = &lv })
		if level >= DeveloperLevel && p.EntranceTele != "" {
			d.enterNote(fmt.Sprintf("account level %d: a GM teleport (.tele name needs level %d)", level, DeveloperLevel))
			return EnterGM, nil
		}
		if level >= DeveloperLevel {
			why = fmt.Sprintf("account level %d, but plan %s has no entrance_tele", level, p.Name)
		} else {
			why = fmt.Sprintf("account level %d (.tele name needs %d)", level, DeveloperLevel)
		}
	}
	if p.Portal == nil {
		return "", fmt.Errorf("enter: no way in: %s, and plan %s has no portal to walk the bots to", why, p.Name)
	}
	d.enterNote(why + ": walking the lead bot into the portal")
	return EnterWalk, nil
}

// checkEntryLimit refuses a 6th new instance within the hour when the ledger knows five, and warns about the limit otherwise.
func (d *RemoteDriver) checkEntryLimit() error {
	l, ok := d.g.(EntryLedger)
	if !ok {
		d.enterNote("warning: " + entryWarning + " (entries are not tracked in this build)")
		return nil
	}
	recent := l.Recent(d.clk.Now())
	d.enterReport(func(r *EnterReport) { r.EntriesLastHour = len(recent) })
	if len(recent) >= MaxInstancesPerHour && !d.opt.IgnoreEntryLimit {
		return fmt.Errorf("enter: the account entered %d new instances in the last hour (limit %d); the oldest was at %s, so the next entry is free after %s (a mangosd restart clears the list; ignore_entry_limit tries anyway)",
			len(recent), MaxInstancesPerHour, recent[0].Local().Format("15:04:05"), recent[0].Add(time.Hour).Local().Format("15:04:05"))
	}
	d.enterNote(fmt.Sprintf("warning: this entry is a new instance, %d already this hour; %s", len(recent), entryWarning))
	return nil
}

// recordEntry notes the instance the lead bot entered, once.
func (d *RemoteDriver) recordEntry(now time.Time) {
	if d.entryRecorded {
		return
	}
	d.entryRecorded = true
	n := -1
	if l, ok := d.g.(EntryLedger); ok {
		if err := l.Record(now); err != nil {
			d.enterNote("could not record the entry: " + err.Error())
		}
		n = len(l.Recent(now))
	}
	d.enterReport(func(r *EnterReport) {
		r.Entries = 1
		if n >= 0 {
			r.EntriesLastHour = n
		}
	})
}

// leadInside says whether the lead bot is on the plan's map in the newest frame.
func (d *RemoteDriver) leadInside() bool {
	l, ok := d.fr.LeadRec()
	return ok && l.Map == d.plan.Map
}

// noteArrivals records when each bot was first seen on the plan's map.
func (d *RemoteDriver) noteArrivals(began time.Time) {
	for _, m := range d.fr.Party {
		if m.Map != d.plan.Map {
			continue
		}
		name := m.Name
		secs := d.clk.Now().Sub(began).Seconds()
		d.enterReport(func(r *EnterReport) {
			if r.Arrived == nil {
				r.Arrived = map[string]float64{}
			}
			if _, done := r.Arrived[name]; !done {
				r.Arrived[name] = secs
			}
		})
	}
	if d.leadInside() {
		d.recordEntry(d.clk.Now())
	}
}

// waitLead waits up to within for the lead bot to be on the plan's map.
func (d *RemoteDriver) waitLead(ctx context.Context, began time.Time, within time.Duration) error {
	deadline := d.clk.Now().Add(within)
	for {
		d.look()
		d.noteArrivals(began)
		if d.leadInside() {
			return nil
		}
		if err := d.endedByModule(); err != nil {
			return err
		}
		if !d.clk.Now().Before(deadline) {
			l, _ := d.fr.LeadRec()
			return fmt.Errorf("enter: the lead bot %s is still on map %d, %.0f s after it was sent in (the plan needs map %d). Likely causes: the account's limit of %d new instances per hour (TRANSFER_ABORT_TOO_MANY_INSTANCES), the bot's level is under the dungeon's minimum, the bot is dead or in combat, or the account is not a developer-level GM",
				d.opt.Lead, l.Map, within.Seconds(), d.plan.Map, MaxInstancesPerHour)
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// enterByTele sends the lead bot to the dungeon's entrance with `.tele name`: it goes into its group's instance, which the first
// entry binds to the group. The agent never enters, so it spends none of the account's instances.
func (d *RemoteDriver) enterByTele(ctx context.Context, e Enterer, began time.Time) error {
	tele := d.plan.EntranceTele
	if d.leadInside() {
		d.enterNote("the lead bot is already inside")
		return nil
	}
	d.enterNote(fmt.Sprintf(".tele name %s %s", d.opt.Lead, tele))
	d.remote(func(r *RemoteReport) { r.GMCommands++ })
	d.enterReport(func(r *EnterReport) { r.Tele = tele })
	if err := e.TeleName(ctx, d.opt.Lead, tele); err != nil {
		return fmt.Errorf("enter: .tele name: %w", err)
	}
	return d.waitLead(ctx, began, d.opt.EnterVerify)
}

// enterByWalk walks the lead bot to the entrance portal on the outer map, in `cmd move` legs; the area trigger takes it in.
func (d *RemoteDriver) enterByWalk(ctx context.Context, began time.Time) error {
	if d.leadInside() {
		d.enterNote("the lead bot is already inside")
		return nil
	}
	pt := d.plan.Portal
	d.enterNote(fmt.Sprintf("walking the lead bot to the portal at %.1f %.1f %.1f on map %d", pt.X, pt.Y, pt.Z, pt.Map))
	if err := d.walkToPortal(ctx, pt, began); err != nil {
		return err
	}
	// The followers come after the lead bot by themselves if the module walks them into the trigger too: give them a moment.
	deadline := d.clk.Now().Add(d.opt.EnterGrace)
	for len(d.offMap(false)) > 0 && d.clk.Now().Before(deadline) {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
		d.noteArrivals(began)
	}
	return nil
}

// walkToPortal sends the lead bot to the portal in legs of at most MoveLeg yards and returns when it is on the plan's map.
func (d *RemoteDriver) walkToPortal(ctx context.Context, pt *Portal, began time.Time) error {
	d.outside = &pt.Map
	defer func() { d.outside = nil }()
	refusals, stalls, aggros := 0, 0, 0
	leg := d.opt.MoveLeg
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.check(); err != nil {
			return err
		}
		d.noteArrivals(began)
		l, ok := d.fr.LeadRec()
		if !ok {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			continue
		}
		if l.Map == d.plan.Map {
			return nil
		}
		if l.Map != pt.Map {
			return fmt.Errorf("enter: the lead bot %s is on map %d; the portal is on map %d", l.Name, l.Map, pt.Map)
		}
		here := partyPoint(l)
		if dist3(here, pt.Point) <= float64(d.opt.MoveArrive) {
			// At the portal: the trigger should take the lead bot in within a few seconds.
			if err := d.waitLead(ctx, began, d.opt.EnterGrace); err != nil {
				return fmt.Errorf("enter: the lead bot stood at the portal and did not enter (does a bot walking into the area trigger go in? the module may need `cmd enter`): %w", err)
			}
			return nil
		}
		if d.fr.PartyInCombat() && len(d.fr.EngagedHostiles()) > 0 {
			if aggros++; aggros > maxWalkAggros {
				return fmt.Errorf("enter: the party was pulled into a fight %d times on the way to the portal", maxWalkAggros)
			}
			if err := d.walkAggro(ctx); err != nil {
				return err
			}
			continue
		}
		wp := d.waypoint(ctx, here, pt.Point, leg)
		if err := d.moveGap(ctx); err != nil {
			return err
		}
		r, err := d.order(ctx, "move", moveArgs(wp)...)
		if err != nil {
			return err
		}
		if ref, bad := refusedOf(r); bad {
			if refusals++; refusals > d.cfg.WalkAttempts+d.opt.MoveRetries {
				return fmt.Errorf("enter: the lead bot was refused %d times on the way to the portal (%s: %s)", refusals, ref.Code, ref.Message)
			}
			if ref.Code == "too-far" || ref.Code == "off-mesh" || ref.Code == "unreachable" {
				leg = max(leg/2, 15)
			}
			if err := d.tick(ctx, d.cfg.WalkRetryDelay); err != nil {
				return err
			}
			continue
		}
		arrive := d.opt.MoveArrive
		if wp != pt.Point {
			arrive = max(arrive, 8)
		}
		res, err := d.waitPortalLeg(ctx, wp, arrive)
		if err != nil {
			return err
		}
		if res == stalledAt {
			if stalls++; stalls > d.opt.MoveRetries {
				return fmt.Errorf("enter: the lead bot gained no ground %d times on the way to the portal (stuck at %.1f %.1f %.1f)", stalls, here.X, here.Y, here.Z)
			}
			d.remote(func(r *RemoteReport) { r.MoveStalls++ })
			leg = max(leg/2, 30)
		}
	}
}

// waitPortalLeg waits for the lead bot to come within arrive yards of wp, to be on the plan's map, to be in a fight, or to stall.
func (d *RemoteDriver) waitPortalLeg(ctx context.Context, wp Point, arrive float32) (arrival, error) {
	start, _ := d.leadPos()
	began := d.clk.Now()
	limit := moveTimeout(dist3(start, wp))
	winAt, winPos := began, start
	for {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return 0, err
		}
		now := d.clk.Now()
		if d.leadInside() {
			return arrivedAt, nil
		}
		here, ok := d.leadPos()
		if ok && dist3(here, wp) <= float64(arrive) {
			return arrivedAt, nil
		}
		if len(d.fr.EngagedHostiles()) > 0 && d.fr.PartyInCombat() {
			return fightAt, nil
		}
		if d.stalled {
			winAt = now
			continue
		}
		if ok && dist3(here, winPos) > 2 {
			winAt, winPos = now, here
		}
		if now.Sub(winAt) >= d.opt.MoveStall || now.Sub(began) > limit {
			return stalledAt, nil
		}
	}
}

// summonRest summons every bot that is not on the plan's map to the lead bot, one at a time, SummonGap apart (a summon takes about
// 3 s, and the core counts an entry per bot that arrives).
func (d *RemoteDriver) summonRest(ctx context.Context, began time.Time) error {
	d.look()
	var todo []string
	for _, m := range d.fr.Party {
		if m.Map != d.plan.Map && !strings.EqualFold(m.Name, d.opt.Lead) {
			todo = append(todo, m.Name)
		}
	}
	for i, name := range todo {
		if i > 0 {
			if err := d.tick(ctx, d.opt.SummonGap); err != nil {
				return err
			}
		}
		d.look()
		if m, ok := d.memberByName(name); ok && m.Map == d.plan.Map {
			continue // it came by itself while the others were summoned
		}
		if err := d.summon(ctx, name); err != nil {
			return err
		}
		d.enterReport(func(r *EnterReport) { r.Summoned = append(r.Summoned, name) })
		d.noteArrivals(began)
	}
	return nil
}

// summon sends `cmd summon <bot>`, again after SummonGap when the module is busy (the lead bot between maps, the bot in combat).
func (d *RemoteDriver) summon(ctx context.Context, name string) error {
	var last string
	for try := 0; try < 3; try++ {
		if try > 0 {
			if err := d.tick(ctx, d.opt.SummonGap); err != nil {
				return err
			}
		}
		r, err := d.order(ctx, "summon", name)
		if err != nil {
			return err
		}
		ref, bad := refusedOf(r)
		if !bad {
			d.enterNote("cmd summon " + name)
			return nil
		}
		last = ref.Code + ": " + ref.Message
		if slices.Contains([]string{"denied", "no-bot", "invalid"}, ref.Code) {
			break
		}
	}
	return fmt.Errorf("enter: cmd summon %s was refused (%s)", name, last)
}

// verifyInside waits up to EnterVerify for every bot to be on the plan's map and fails, naming the ones that are not.
func (d *RemoteDriver) verifyInside(ctx context.Context, began time.Time) error {
	deadline := d.clk.Now().Add(d.opt.EnterVerify)
	for {
		d.look()
		d.noteArrivals(began)
		off := d.offMap(true)
		if len(off) == 0 && d.haveFr {
			return nil
		}
		if err := d.endedByModule(); err != nil {
			return err
		}
		if !d.clk.Now().Before(deadline) {
			return fmt.Errorf("enter: %s not on map %d %.0f s after the entry (the plan needs the whole party inside; the group is not changed to fix it)", strings.Join(off, ", "), d.plan.Map, d.opt.EnterVerify.Seconds())
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

func (e *EnterReport) clone() *EnterReport {
	if e == nil {
		return nil
	}
	c := *e
	c.Summoned = slices.Clone(e.Summoned)
	c.Arrived = cloneFloats(e.Arrived)
	if e.AccountLevel != nil {
		l := *e.AccountLevel
		c.AccountLevel = &l
	}
	return &c
}

func cloneFloats(m map[string]float64) map[string]float64 {
	if m == nil {
		return nil
	}
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
