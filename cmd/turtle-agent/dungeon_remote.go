package main

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
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
	switch strings.ToLower(p.Mode) {
	case "", "group":
		if p.LeadBot != "" || len(p.Bots) > 0 || p.Tank != "" || p.Healer != "" || len(p.CC) > 0 || p.Team != "" || p.Preflight != "" || p.OnEnd != "" || p.Carrier != "" || p.LootRestore != "" || p.Enter != "" || p.IgnoreEntryLimit {
			return false, bad(`lead_bot, bots, tank, healer, carrier, loot_restore, cc, team, preflight, on_end, enter and ignore_entry_limit go with mode "remote"`)
		}
		return false, nil
	case "remote":
	default:
		return false, bad(`mode must be "group" or "remote"`)
	}
	if !botName.MatchString(p.LeadBot) {
		return true, bad(`mode "remote" needs lead_bot: the name of the bot that walks (letters only)`)
	}
	if len(p.Bots) > 4 {
		return true, bad("bots: at most 4 besides the lead bot (a party holds five)")
	}
	seen := map[string]bool{strings.ToLower(p.LeadBot): true}
	for _, b := range p.Bots {
		if !botName.MatchString(b) || seen[strings.ToLower(b)] {
			return true, bad("bots: %q is not a bot name, or is listed twice", b)
		}
		seen[strings.ToLower(b)] = true
	}
	if p.Tank != "" && !botName.MatchString(p.Tank) {
		return true, bad("tank: %q is not a bot name", p.Tank)
	}
	if p.Healer != "" && !botName.MatchString(p.Healer) {
		return true, bad("healer: %q is not a bot name", p.Healer)
	}
	if p.Carrier != "" && !botName.MatchString(p.Carrier) {
		return true, bad("carrier: %q is not a bot name", p.Carrier)
	}
	if lr := strings.ToLower(p.LootRestore); lr != "" && !slices.Contains([]string{"group", "rr", "master", "nbg"}, lr) {
		return true, bad("loot_restore: %q is not group, rr, master or nbg", p.LootRestore)
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
	if v := strings.ToLower(p.Enter); !slices.Contains([]string{"", dungeon.EnterAuto, dungeon.EnterGM, dungeon.EnterWalk, dungeon.EnterOff}, v) {
		return true, bad("enter: %q is none of %q, %q, %q, %q", p.Enter, dungeon.EnterAuto, dungeon.EnterGM, dungeon.EnterWalk, dungeon.EnterOff)
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
	case p.Brain != "":
		return true, bad(`brain plays the agent's own character; in remote mode the bots fight`)
	case p.GM != nil || p.GMLevel != nil:
		return true, bad(`gm and gm_level do not apply: a remote run sends no GM command but the one .tele name of its enter phase, which it asks the server about (enter)`)
	case p.WaitSickness:
		return true, bad(`wait_sickness is for the agent's own corpse run; it does not apply in remote mode`)
	}
	return true, nil
}

// startRemote starts a run in remote mode. The caller holds r.mu and has checked that no run goes.
func (r *dungeonRunner) startRemote(p dungeonRunParams, tcfg tracerun.Config, plan *dungeon.Plan, setup []string, from int, names []string) (dungeonRunResult, error) {
	if r.remote == nil {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "the remote commander is not available"}
	}
	cfg := dungeon.DefaultConfig()
	cfg.Trace = tcfg
	cfg.Setup, cfg.RecoverWipe = setup, p.RecoverWipe
	cfg.Resume = p.Resume != nil && *p.Resume
	opt := dungeon.RemoteOptions{Lead: p.LeadBot, Bots: p.Bots, Tank: p.Tank, Healer: p.Healer, Carrier: p.Carrier, LootRestore: p.LootRestore, Team: p.Team, Preflight: p.Preflight, OnEnd: p.OnEnd, Enter: p.Enter, IgnoreEntryLimit: p.IgnoreEntryLimit}
	for _, c := range p.CC {
		c.Mark = strings.ToLower(c.Mark)
		opt.CC = append(opt.CC, c)
	}
	drv := dungeon.NewRemoteDriver(plan, remoteGame{r.remote}, cfg, opt, dungeon.RealClock())
	ctx, cancel := context.WithCancel(r.root)
	done := make(chan struct{})
	r.drv, r.cancel, r.done = drv, cancel, done
	go func() {
		defer close(done)
		defer cancel()
		_ = drv.Run(ctx, from) // the outcome is in the report and the events; the unbind is deferred inside Run
	}()
	return dungeonRunResult{Started: true, Plan: plan.Name, Map: plan.Map, FromLeg: from, Legs: names, Buffs: []string{}, LeaderGM: "false", Mode: "remote", LeadBot: p.LeadBot}, nil
}
