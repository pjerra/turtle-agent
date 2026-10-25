package main

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/tbm"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// botName is what a character name can be (1.12 names are letters; the commander command takes them as words).
var botName = regexp.MustCompile(`^[A-Za-z]{2,12}$`)

// checkMode reads dungeon_run's mode and refuses the options that make no sense in it. It reports whether the run is remote.
func checkMode(p dungeonRunParams) (remote bool, err error) {
	bad := func(format string, a ...any) error {
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf(format, a...)}
	}
	mode, _, merr := runMode(p.Mode)
	if merr != nil {
		return false, merr
	}
	if p.FromStep != nil {
		switch {
		case mode != modeObserve:
			return false, bad(`from_step is an observed run's start step in the run leader's plan: it goes with mode "observe" (a driven run starts from from_leg)`)
		case *p.FromStep < 1:
			return false, bad("from_step %d: a step number from 1 (the number .bot run status shows)", *p.FromStep)
		case p.FromLeg != nil || p.FromPullGroup != nil:
			return false, bad("from_step names the start step itself: leave from_leg and from_pull_group out")
		}
	}
	if mode == modeObserve {
		if err := checkObserve(p, bad); err != nil {
			return true, err
		}
	}
	switch mode {
	case modeLeader:
		if p.FromPullGroup != nil || p.StartHere || len(p.HaveItems) > 0 {
			return false, bad(`from_pull_group, start_here and have_items go with mode "remote" (commander; leader and group mode have teleport)`)
		}
		if len(p.GiveItems) > 0 {
			return false, bad(`give_items goes with mode "remote" (commander): it sends .additem to a hired or named bot at the prepare step`)
		}
		if p.Pulltest != nil && (p.Teleport || p.Resume != nil) {
			return false, bad(`pulltest in leader mode runs where the party stands, at the pack: leave teleport and resume out`)
		}
		if p.ForceStart {
			return false, bad(`force_start passes the unsafe-start gate, which is commander mode only (mode "commander"); leader mode has no such gate, so it would do nothing`)
		}
		if p.Tune != "" || p.LiveReload != nil {
			return false, bad(`tune and live_reload go with mode "remote" (commander)`)
		}
		if p.LeadBot != "" || len(p.Bots) > 0 || p.Tank != "" || p.Healer != "" || len(p.CC) > 0 || p.Team != "" || p.Preflight != "" || p.OnEnd != "" || p.KeepOnFail || p.Carrier != "" || p.LootRestore != "" || p.ObjectFallback != "" || p.GravesBatch != 0 || p.GravesRamp != nil || p.GravesOpenBy != "" || p.Enter != "" || p.IgnoreEntryLimit || p.Prepare != nil {
			return false, bad(`lead_bot, bots, tank, healer, carrier, loot_restore, object_fallback, graves_batch, graves_ramp, graves_open_by, cc, team, preflight, on_end, keep_on_fail, enter, ignore_entry_limit and prepare go with mode "remote" (commander)`)
		}
		if (p.Rotation != "" || p.Role != "") && p.Brain == "" && !strings.EqualFold(p.Mode, modeLeader) {
			return false, bad(`rotation and role go with a brain (brain, or mode "leader", which picks the preset of the character's class)`)
		}
		return false, nil
	}
	if p.Prepare != nil && p.Prepare.HiredParty() {
		if p.LeadBot != "" || len(p.Bots) > 0 || p.Tank != "" || p.Healer != "" {
			return true, bad(`prepare.party "hire" names its own bots: leave lead_bot, bots, tank and healer out (the lead bot is the hired tank)`)
		}
	} else if err := checkNamedParty(p, bad); err != nil {
		return true, err
	}
	if p.Carrier != "" && !botName.MatchString(p.Carrier) {
		return true, bad("carrier: %q is not a bot name", p.Carrier)
	}
	if lr := strings.ToLower(p.LootRestore); lr != "" && !slices.Contains([]string{"group", "rr", "master", "nbg"}, lr) {
		return true, bad("loot_restore: %q is not group, rr, master or nbg", p.LootRestore)
	}
	if v := strings.ToLower(p.ObjectFallback); !slices.Contains([]string{"", dungeon.ObjectFallbackAgent, dungeon.ObjectFallbackOff}, v) {
		return true, bad("object_fallback: %q is neither %q nor %q", p.ObjectFallback, dungeon.ObjectFallbackAgent, dungeon.ObjectFallbackOff)
	}
	if v := strings.ToLower(p.GravesOpenBy); !slices.Contains([]string{"", dungeon.GravesOpenCarrier, dungeon.GravesOpenAgent}, v) {
		return true, bad("graves_open_by: %q is neither %q nor %q", p.GravesOpenBy, dungeon.GravesOpenCarrier, dungeon.GravesOpenAgent)
	}
	if p.GravesBatch < 0 || p.GravesBatch > 20 {
		return true, bad("graves_batch: %d is outside 1-20", p.GravesBatch)
	}
	if r := p.GravesRamp; r != nil {
		switch {
		case p.GravesBatch != 0:
			return true, bad("graves_batch and graves_ramp are two ways to set the batch size: give one")
		case r.Start < 0 || r.Start > 20 || r.Step < 0 || r.Step > 10 || r.Max < 0 || r.Max > 40:
			return true, bad("graves_ramp: start is 0-20 (default %d), step 0-10 (default %d), max 0-40 (0: no limit)", dungeon.GravesRampStart, dungeon.GravesRampStep)
		case r.Max > 0 && r.Start > r.Max:
			return true, bad("graves_ramp: start %d is above max %d", r.Start, r.Max)
		}
	}
	if t := strings.ToLower(p.Team); t != "" && t != dungeon.TeamAlliance && t != dungeon.TeamHorde {
		return true, bad("team: %q is neither %q nor %q", p.Team, dungeon.TeamAlliance, dungeon.TeamHorde)
	}
	if v := strings.ToLower(p.Preflight); !slices.Contains([]string{"", dungeon.PreflightWarn, dungeon.PreflightStrict, dungeon.PreflightOff}, v) {
		return true, bad("preflight: %q is none of %q, %q, %q", p.Preflight, dungeon.PreflightWarn, dungeon.PreflightStrict, dungeon.PreflightOff)
	}
	if v := strings.ToLower(p.OnEnd); !slices.Contains([]string{"", dungeon.OnEndStay, dungeon.OnEndLogout, dungeon.OnEndNone}, v) {
		return true, bad("on_end: %q is none of %q, %q, %q", p.OnEnd, dungeon.OnEndStay, dungeon.OnEndLogout, dungeon.OnEndNone)
	}
	if v := strings.ToLower(p.Enter); !slices.Contains([]string{"", dungeon.EnterAuto, dungeon.EnterGM, dungeon.EnterCmd, dungeon.EnterWalk, dungeon.EnterOff}, v) {
		return true, bad("enter: %q is none of %q, %q, %q, %q, %q", p.Enter, dungeon.EnterAuto, dungeon.EnterGM, dungeon.EnterCmd, dungeon.EnterWalk, dungeon.EnterOff)
	}
	if p.Prepare != nil {
		if err := p.Prepare.Validate(true); err != nil {
			return true, bad("%v", err)
		}
	}
	if err := dungeon.ValidateGiveItems(p.GiveItems); err != nil {
		return true, bad("%v", err)
	}
	for _, c := range p.CC {
		if c.Entry == 0 || !slices.Contains(tbm.CCMarks, strings.ToLower(c.Mark)) || (c.Bot != "" && !botName.MatchString(c.Bot)) {
			return true, bad("cc: each rule needs an entry, a mark (%s) and optionally a bot name", strings.Join(tbm.CCMarks, ", "))
		}
	}
	switch {
	case p.Teleport:
		return true, bad(`teleport is a GM command of group mode; a remote run enters the dungeon itself (enter)`)
	case p.Buffs != nil:
		return true, bad(`buffs are GM commands (.aura); remote mode sends none`)
	case p.Brain != "" || p.Rotation != "" || p.Role != "":
		return true, bad(`brain, rotation and role play the agent's own character; in remote (commander) mode the bots fight`)
	case p.GM != nil || p.GMLevel != nil:
		return true, bad(`gm and gm_level do not apply: a remote run sends no GM command but the one .tele name of its enter phase, which it asks the server about (enter)`)
	case p.WaitSickness:
		return true, bad(`wait_sickness is for the agent's own corpse run; it does not apply in remote mode`)
	}
	return true, nil
}

// checkObserve refuses what has no meaning in observe mode: the run leader on the server does the walking, the pulling and the object
// steps, and the agent sends it no order, so the options that steer those (or move or equip the party for a start) are refused instead
// of ignored. from_leg, from_pull_group and from_step only say at which step of the run leader's own plan it starts (#248).
func checkObserve(p dungeonRunParams, bad func(string, ...any) error) error {
	switch {
	case p.StartHere || p.Pulltest != nil || len(p.HaveItems) > 0:
		return bad(`start_here, have_items and pulltest move or equip the party; in observe mode the run leader does that, and from_leg, from_pull_group (or from_step) only say at which of its steps it starts`)
	case len(p.GiveItems) > 0:
		return bad(`give_items puts items in the bags of the driven run's bots; in observe mode the run leader on the server gives the agent no such step (and its plan leaves an optional item leg out)`)
	case len(p.CC) > 0 || p.Carrier != "" || p.LootRestore != "" || p.ObjectFallback != "" || p.Tank != "" || p.Healer != "" || p.RecoverWipe != nil || p.GravesBatch != 0 || p.GravesRamp != nil || p.GravesOpenBy != "":
		return bad(`cc, carrier, loot_restore, object_fallback, graves_batch, graves_ramp, graves_open_by, tank, healer and recover_wipe steer the bots; in observe mode the run leader on the server does that and the agent sends no order`)
	case p.Tune != "" || p.LiveReload != nil:
		return bad(`tune and live_reload belong to the driven run (mode "commander")`)
	case p.ForceStart:
		return bad(`force_start passes the unsafe-start gate, which is commander mode only (mode "commander"); observe mode starts where the run leader's own plan says (from_leg, from_pull_group, from_step), so it would do nothing`)
	}
	if o := p.Observe; o != nil {
		if o.Plan != "" && !planName.MatchString(o.Plan) {
			return bad("observe.plan: %q is not a plan name (a-z, 0-9, _ and -)", o.Plan)
		}
		for _, v := range []struct {
			name string
			s    float64
		}{{"poll_s", o.PollS}, {"max_run_s", o.MaxRunS}, {"stall_s", o.StallS}, {"paused_wait_s", o.PausedWaitS}} {
			if v.s < 0 || v.s > 24*3600 {
				return bad("observe.%s: %v is outside 0-86400 seconds (0: the default)", v.name, v.s)
			}
		}
		if s := o.Speed; s != 0 && (s < 1 || s > 4) {
			return bad("observe.speed: %v is outside 1.0-4.0 (0: omit it, a normal run)", s)
		}
	}
	return nil
}

// observeSpec is the dungeon.ObserveSpec of the run's observe parameters.
func observeSpec(o *observeParam) *dungeon.ObserveSpec {
	spec := &dungeon.ObserveSpec{}
	if o != nil {
		sec := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
		spec.Plan, spec.Poll, spec.MaxRun, spec.Stall, spec.PausedWait, spec.Maintenance, spec.AllowFarStart, spec.KeepLootSetting = o.Plan, sec(o.PollS), sec(o.MaxRunS), sec(o.StallS), sec(o.PausedWaitS), o.Maintenance, o.AllowFarStart, o.KeepLootSetting
		spec.Speed = o.Speed
	}
	return spec
}

// checkNamedParty checks the lead bot, the other bots, the tank and the healer of a run that names its party.
func checkNamedParty(p dungeonRunParams, bad func(string, ...any) error) error {
	if !botName.MatchString(p.LeadBot) {
		return bad(`mode "remote" needs lead_bot: the name of the bot that walks (letters only)`)
	}
	if len(p.Bots) > 4 {
		return bad("bots: at most 4 besides the lead bot (a party holds five)")
	}
	seen := map[string]bool{strings.ToLower(p.LeadBot): true}
	for _, b := range p.Bots {
		if !botName.MatchString(b) || seen[strings.ToLower(b)] {
			return bad("bots: %q is not a bot name, or is listed twice", b)
		}
		seen[strings.ToLower(b)] = true
	}
	if p.Tank != "" && !botName.MatchString(p.Tank) {
		return bad("tank: %q is not a bot name", p.Tank)
	}
	if p.Healer != "" && !botName.MatchString(p.Healer) {
		return bad("healer: %q is not a bot name", p.Healer)
	}
	return nil
}

// remoteRecoverWipe is the recover_wipe setting of a run that goes through startRemote: the caller's word, else on in a commander run and off
// in an observed one (the agent steers nothing there; checkObserve refuses the option, so only the default is left).
func remoteRecoverWipe(p dungeonRunParams) bool {
	mode, _, _ := runMode(p.Mode)
	return recoverWipeOn(p.RecoverWipe, mode != modeObserve)
}

// startRemote starts a run in remote mode. The caller holds r.mu and has checked that no run goes.
func (r *dungeonRunner) startRemote(p dungeonRunParams, tcfg tracerun.Config, plan *dungeon.Plan, planPath, tunePath string, setup []string, from int, names []string) (dungeonRunResult, error) {
	if r.remote == nil {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "the remote commander is not available"}
	}
	cfg := dungeon.DefaultConfig()
	cfg.Trace = tcfg
	cfg.Setup = setup
	cfg.Resume = p.Resume != nil && *p.Resume
	if p.Watchdog != nil {
		cfg.Watchdog = *p.Watchdog
	}
	var observe *dungeon.ObserveSpec
	mode, _, _ := runMode(p.Mode)
	var startFrom dungeon.StartFromResolver
	if mode == modeObserve {
		observe = observeSpec(p.Observe)
		var planner *navmesh.Planner
		if r.remote != nil {
			planner = r.remote.planner
		}
		step := 0
		if p.FromStep != nil {
			step = *p.FromStep
		}
		startFrom = observeStartFrom(plan, from, p.FromPullGroup, step, planner)
	}
	// recover_wipe is on by default in a commander run only: an observed run steers nothing (and refuses the option), so its trace
	// options say off.
	cfg.RecoverWipe = remoteRecoverWipe(p)
	opt := dungeon.RemoteOptions{RunMeta: resolveRunMeta(p.RunMeta, r.runMeta), ForceStart: p.ForceStart, Observe: observe, StartFrom: startFrom, PlanPath: planPath, TunePath: tunePath, NoReload: p.LiveReload != nil && !*p.LiveReload, StartHere: p.StartHere, HaveItems: p.HaveItems, GiveItems: p.GiveItems, Lead: p.LeadBot, Bots: p.Bots, Tank: p.Tank, Healer: p.Healer, Carrier: p.Carrier, CCBot: p.CCBot, LootRestore: p.LootRestore, ObjectFallback: p.ObjectFallback, GravesBatch: p.GravesBatch, GravesRamp: p.GravesRamp, GravesOpenBy: strings.ToLower(p.GravesOpenBy), Team: p.Team, Preflight: p.Preflight, OnEnd: p.OnEnd, KeepOnFail: p.KeepOnFail, HireMemory: r.keptHires(), ClassMemory: maps.Clone(r.classes), Enter: p.Enter, IgnoreEntryLimit: p.IgnoreEntryLimit}
	if p.Prepare != nil {
		opt.Prepare = *p.Prepare
	}
	if p.FromPullGroup != nil {
		g := *p.FromPullGroup
		opt.FromPullGroup = &g
	}
	if pt := p.Pulltest; pt != nil && pt.PullGroup != nil {
		g := *pt.PullGroup
		opt.FromPullGroup = &g
		spec := dungeon.PulltestSpec{Leg: from, PullGroup: g, Tries: pt.Tries, Reset: pt.Reset}.Normalized()
		opt.Pulltest = &spec
	}
	for _, c := range p.CC {
		c.Mark = strings.ToLower(c.Mark)
		opt.CC = append(opt.CC, c)
	}
	// The abilities the options need are checked before the driver starts, so that a run that cannot give items fails before it hires anyone.
	if err := dungeon.RemoteAbilities(remoteGame{r.remote}, opt, plan); err != nil {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: err.Error()}
	}
	drv := dungeon.NewRemoteDriver(plan, remoteGame{r.remote}, cfg, opt, dungeon.RealClock())
	ctx, cancel := context.WithCancel(r.root)
	done := make(chan struct{})
	r.drv, r.cancel, r.done = drv, cancel, done
	go func() {
		defer close(done)
		defer cancel()
		_ = drv.Run(ctx, from)
		r.keptFrom(drv.Report()) // the outcome is in the report and the events; the unbind is deferred inside Run
	}()
	res := dungeonRunResult{Started: true, Plan: plan.Name, Map: plan.Map, FromLeg: from, FromPullGroup: opt.FromPullGroup, Legs: names, Buffs: []string{}, LeaderGM: "false", Mode: "remote", RunMode: modeCommander, LeadBot: p.LeadBot}
	if observe != nil {
		res.Mode, res.RunMode = modeObserve, modeObserve
	}
	return res, nil
}

// keptHires is a copy of what the run that kept a binding read of its hires (nil when none was kept). The caller holds r.mu.
func (r *dungeonRunner) keptHires() map[string]tbm.Who {
	if r.kept == nil {
		return nil
	}
	return maps.Clone(r.kept)
}
