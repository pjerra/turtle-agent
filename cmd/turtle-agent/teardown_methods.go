package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// RPC teardown: one call that ends the agent's game presence (run, binding,
// hires, GM, logout) best-effort, step by step. Game-side refusals are steps
// with status fail, never an rpc.Error.

// teardownParams are the RPC params. GM "off" is the default.
type teardownParams struct {
	DryRun bool   `json:"dry_run,omitempty"`
	GM     string `json:"gm,omitempty"` // "off" (default), "on" or "keep"
	WaitMS int    `json:"wait_ms,omitempty"`
}

type teardownStep struct {
	Step   string `json:"step"`
	Status string `json:"status"` // ok, skip, fail; dry_run: "plan"
	Detail string `json:"detail"`
}

type teardownResult struct {
	Character string         `json:"character"`
	Bound     bool           `json:"bound"`
	Lead      string         `json:"lead,omitempty"`
	Listed    []string       `json:"listed"`
	Steps     []teardownStep `json:"steps"`
	Failed    int            `json:"failed"`
	DryRun    bool           `json:"dry_run"`
	Seconds   float64        `json:"seconds"`
}

// teardownGame is the game half teardown drives; the fake in the tests records
// every command it gets.
type teardownGame interface {
	Character() string
	BotCommand(ctx context.Context, cmd string, wait time.Duration) (tbm.Reply, error)
	GMReply(ctx context.Context, text string, match func(string) bool, wait time.Duration) (string, bool, error)
	Party() session.PartyInfo
	VisibleNames() []string
	LeaveGroup(ctx context.Context) error
	Logout(ctx context.Context) (string, error)
	AgentRunActive() bool
	StopAgentRun() string
	BrainFree() bool
	StopBrain()
}

var tdBotNameRe = regexp.MustCompile(`^[A-Za-z]{2,12}$`)

func tdIsBotName(n string) bool { return tdBotNameRe.MatchString(n) }

// sessionTeardownGame is the production teardownGame over the agent's session.
type sessionTeardownGame struct {
	cur    func() *session.Session
	c      *commanderService
	runner *dungeonRunner
	brain  *brain.Controller
	lease  *brainLease
}

func (g sessionTeardownGame) Character() string { return g.cur().Status().Character }

func (g sessionTeardownGame) BotCommand(ctx context.Context, cmd string, wait time.Duration) (tbm.Reply, error) {
	return g.c.cmdr.DoWait(ctx, cmd, wait)
}

// GMReply sends text with cur().GM and returns the first KindSystem line
// appended after the send for which match is true, ok=false after wait.
func (g sessionTeardownGame) GMReply(ctx context.Context, text string, match func(string) bool, wait time.Duration) (string, bool, error) {
	log := g.cur().Log()
	since := log.Last()
	if err := g.cur().GM(ctx, text); err != nil {
		return "", false, err
	}
	tctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	cursor := since
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			if e.Text == "" {
				continue
			}
			if match(e.Text) {
				return e.Text, true, nil
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		if err := log.Wait(tctx, cursor); err != nil {
			if ctx.Err() != nil {
				return "", false, ctx.Err()
			}
			return "", false, nil
		}
	}
}

func (g sessionTeardownGame) Party() session.PartyInfo { return g.cur().Party() }

// VisibleNames are the names of player-type units in view.
func (g sessionTeardownGame) VisibleNames() []string {
	var out []string
	for _, u := range g.cur().Units(objects.Filter{Limit: 2000}) {
		if u.Type == "player" {
			out = append(out, u.Name)
		}
	}
	return out
}

func (g sessionTeardownGame) LeaveGroup(ctx context.Context) error { return g.cur().LeaveGroup(ctx) }

func (g sessionTeardownGame) Logout(ctx context.Context) (string, error) {
	outcome, err := g.cur().Logout(ctx)
	return string(outcome), err
}

func (g sessionTeardownGame) AgentRunActive() bool { return g.runner.running() }

func (g sessionTeardownGame) StopAgentRun() string { return g.runner.stop().State }

// BrainFree is brain running and no dungeon run owns its lease.
func (g sessionTeardownGame) BrainFree() bool {
	if !g.brain.Running() {
		return false
	}
	g.lease.mu.Lock()
	defer g.lease.mu.Unlock()
	return !g.lease.held
}

func (g sessionTeardownGame) StopBrain() { g.brain.Stop() }

// tdRunStatus is the TBM:RUN line of a reply, or state none.
func tdRunStatus(r tbm.Reply) tbm.RunStatus {
	for _, l := range r.Lines {
		if rs, ok := tbm.ParseRunLine(l); ok {
			return rs
		}
	}
	return tbm.RunStatus{State: tbm.RunNone}
}

func tdContainsFold(hay, needle string) bool {
	return strings.Contains(strings.ToLower(hay), strings.ToLower(needle))
}

// tdClassifyRemove maps a matched .bot remove reply line onto its outcome.
func tdClassifyRemove(line, name string) (string, string) {
	switch {
	case tdContainsFold(line, "Hire ended for "+name),
		tdContainsFold(line, "Removal requested for bot "+name):
		return "ok", line
	case tdContainsFold(line, "Character '"+name+"' not found."):
		return "ok", "already gone"
	default:
		return "fail", line
	}
}

// runTeardown runs the teardown steps in order; sleep is time.Sleep in
// production and a fake clock in the tests. Every step runs even after a
// failed one.
func runTeardown(ctx context.Context, g teardownGame, p teardownParams, sleep func(time.Duration)) (teardownResult, error) {
	t0 := time.Now()
	wait := time.Duration(p.WaitMS) * time.Millisecond
	if p.GM == "" {
		p.GM = "off"
	}
	res := teardownResult{DryRun: p.DryRun}
	add := func(step, status, detail string) {
		res.Steps = append(res.Steps, teardownStep{Step: step, Status: status, Detail: detail})
		if status == "fail" {
			res.Failed++
		}
	}

	// 1. snapshot: the listing is read BEFORE agent_run, because the agent-side
	// stop unbinds a kept binding and the names would be lost.
	own := g.Character()
	res.Character = own
	bp, err := dungeon.ReadBoundParty(ctx, func(ctx context.Context, cmd string) (tbm.Reply, error) {
		return g.BotCommand(ctx, cmd, wait)
	})
	party := g.Party()
	rs := tdRunStatus(func() tbm.Reply {
		r, rerr := g.BotCommand(ctx, "run status", wait)
		if rerr != nil {
			return tbm.Reply{}
		}
		return r
	}())
	if err != nil {
		add("snapshot", "fail", err.Error())
	} else {
		var pieces []string
		if bp.Answered {
			pieces = append(pieces, fmt.Sprintf("bound to %s's group: %s", bp.Lead, strings.Join(bp.Names, ", ")))
		} else {
			pieces = append(pieces, "not bound")
		}
		if rs.State == tbm.RunNone || rs.State == "" {
			pieces = append(pieces, "no run")
		} else {
			pieces = append(pieces, fmt.Sprintf("run %s %s %d/%d", rs.Plan, rs.State, rs.Step, rs.Steps))
		}
		if party.InGroup {
			pieces = append(pieces, fmt.Sprintf("in a group of %d", len(party.Members)+1))
		} else {
			pieces = append(pieces, "not in a group")
		}
		add("snapshot", "ok", strings.Join(pieces, "; "))
	}
	if err == nil {
		res.Bound = bp.Answered
		res.Lead = bp.Lead
		for _, n := range bp.Names {
			if strings.EqualFold(n, own) {
				continue
			}
			res.Listed = append(res.Listed, n)
		}
	}
	listed := res.Listed
	dry := p.DryRun
	plan := func(step, detail string) { add(step, "plan", detail) }

	// 2. agent_run
	if g.AgentRunActive() {
		if dry {
			plan("agent_run", "would stop the agent-side dungeon_run")
		} else {
			add("agent_run", "ok", fmt.Sprintf("agent-side dungeon_run stopped (%s)", g.StopAgentRun()))
		}
	} else {
		add("agent_run", "skip", "no agent-side run")
	}

	// 3. brain
	if g.BrainFree() {
		if dry {
			plan("brain", "would stop the brain")
		} else {
			g.StopBrain()
			add("brain", "ok", "brain stopped")
		}
	} else {
		add("brain", "skip", "brain not running")
	}

	// 4. run_stop
	switch rs.State {
	case tbm.RunRunning, tbm.RunPaused:
		if dry {
			plan("run_stop", "would send run stop")
		} else {
			r, rerr := g.BotCommand(ctx, "run stop", wait)
			done := ""
			if rerr == nil {
				for _, l := range r.Lines {
					if s, ok := tbm.ParseRunLine(l); ok && (s.State == tbm.RunDone || s.State == tbm.RunNone) {
						done = s.Line
						break
					}
				}
				if done == "" && (r.Kind == tbm.KindAck || r.Ack != nil) {
					done = r.Line
				}
			}
			if done != "" {
				add("run_stop", "ok", done)
			} else if rerr != nil {
				add("run_stop", "fail", fmt.Sprintf("run stop: %v", rerr))
			} else if r.Line != "" {
				add("run_stop", "fail", fmt.Sprintf("run stop: %s", r.Line))
			} else {
				add("run_stop", "fail", fmt.Sprintf("run stop: %s", strings.Join(r.Lines, "\n")))
			}
		}
	default:
		add("run_stop", "skip", fmt.Sprintf("no server run (%s)", rs.State))
	}

	// 5. unbind
	if bp.Answered && err == nil {
		if dry {
			plan("unbind", "would send commander unbind")
		} else {
			r, rerr := g.BotCommand(ctx, "commander unbind", wait)
			switch {
			case rerr != nil:
				add("unbind", "fail", fmt.Sprintf("unbind: %v", rerr))
			case r.Ack != nil || r.Kind == tbm.KindAck:
				line := r.Line
				for _, l := range r.Lines {
					if strings.Contains(l, "You no longer command") {
						line = l
						break
					}
				}
				add("unbind", "ok", line)
			case r.Err != nil && r.Err.Code == "not-bound":
				add("unbind", "ok", "already unbound")
			default:
				detail := r.Line
				if detail == "" {
					detail = strings.Join(r.Lines, "\n")
				}
				add("unbind", "fail", fmt.Sprintf("unbind: %s", detail))
			}
		}
	} else {
		add("unbind", "skip", "not bound")
	}

	// 6. remove: never another slot's bots. A mixed group (a party member
	// that is neither own nor listed) gets no .bot remove at all.
	mixed := ""
	if err == nil {
		inListing := map[string]bool{}
		for _, n := range bp.Names {
			inListing[strings.ToLower(n)] = true
		}
		for _, m := range party.Members {
			if strings.EqualFold(m.Name, own) || inListing[strings.ToLower(m.Name)] {
				continue
			}
			mixed = m.Name
			break
		}
	}
	switch {
	case mixed != "":
		add("remove", "skip", fmt.Sprintf("%s (not a bot of commander status) is in the group: no .bot remove (a GM removes any bot); this character's hires end when it leaves the group", mixed))
	case len(listed) == 0:
		add("remove", "skip", "commander status listed no bots")
	default:
		names := []string{}
		for _, n := range listed {
			if tdIsBotName(n) {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			add("remove", "skip", "commander status listed no bots")
			break
		}
		for _, n := range names {
			name := n
			if dry {
				plan("remove "+name, fmt.Sprintf("would send .bot remove %s", name))
				continue
			}
			match := func(l string) bool {
				return tdContainsFold(l, name) || strings.Contains(l, "You may only control characters on your account.")
			}
			line, ok, merr := g.GMReply(ctx, ".bot remove "+name, match, wait)
			if merr != nil {
				add("remove "+name, "fail", merr.Error())
				continue
			}
			if !ok {
				add("remove "+name, "fail", fmt.Sprintf("no answer in %d s", p.WaitMS/1000))
				continue
			}
			st, detail := tdClassifyRemove(line, name)
			add("remove "+name, st, detail)
		}
	}

	// 7. leave_group: the party is read again now.
	if g.Party().InGroup {
		if dry {
			plan("leave_group", "would leave the group")
		} else if lerr := g.LeaveGroup(ctx); lerr != nil {
			add("leave_group", "fail", lerr.Error())
		} else {
			add("leave_group", "ok", "left the group (the server ends this character's hires)")
		}
	} else {
		add("leave_group", "skip", "not in a group")
	}

	// 8. gone: up to 10 s, polled every 500 ms, until no listed name is in view.
	if len(listed) == 0 {
		add("gone", "skip", "no listed bots")
	} else if dry {
		plan("gone", "would wait until the listed bots are out of view")
	} else {
		inView := func() []string {
			var still []string
			vis := map[string]bool{}
			for _, v := range g.VisibleNames() {
				vis[strings.ToLower(v)] = true
			}
			for _, n := range listed {
				if vis[strings.ToLower(n)] {
					still = append(still, n)
				}
			}
			return still
		}
		var still []string
		for i := 0; ; i++ {
			still = inView()
			if len(still) == 0 || i >= 20 {
				break
			}
			sleep(500 * time.Millisecond)
		}
		if len(still) == 0 {
			add("gone", "ok", "none of the listed bots is in view")
		} else {
			add("gone", "fail", fmt.Sprintf("still in view after 10 s: %s", strings.Join(still, ", ")))
		}
	}

	// 9. gm
	switch p.GM {
	case "keep":
		add("gm", "skip", "left as it is")
	default: // "off" (or "" defaulted by the RPC handler) and "on"
		want := "GM mode is OFF"
		if p.GM == "on" {
			want = "GM mode is ON"
		}
		if dry {
			plan("gm", fmt.Sprintf("would send .gm %s", p.GM))
			break
		}
		match := func(l string) bool {
			return tdContainsFold(l, want) || strings.Contains(l, "There is no such command")
		}
		line, ok, merr := g.GMReply(ctx, ".gm "+p.GM, match, wait)
		if merr != nil {
			add("gm", "fail", merr.Error())
		} else if !ok {
			add("gm", "skip", fmt.Sprintf("no answer to .gm %s", p.GM))
		} else if strings.Contains(line, "There is no such command") {
			add("gm", "skip", "not a GM account")
		} else {
			add("gm", "ok", line)
		}
	}

	// 10. logout: a refused logout (in combat) is retried every logoutRetryEvery
	// for up to logoutRetryFor.
	if dry {
		plan("logout", "would log out")
	} else {
		var outcome string
		var lerr error
		elapsed := time.Duration(0)
		for {
			var oc string
			oc, lerr = g.Logout(ctx)
			if lerr == nil {
				outcome = oc
				break
			}
			var le *session.LogoutError
			if !errors.As(lerr, &le) {
				break
			}
			if elapsed+logoutRetryEvery > logoutRetryFor {
				break
			}
			sleep(logoutRetryEvery)
			elapsed += logoutRetryEvery
		}
		if lerr != nil {
			add("logout", "fail", lerr.Error())
		} else {
			add("logout", "ok", fmt.Sprintf("logged out (%s)", outcome))
		}
	}

	res.DryRun = dry
	res.Seconds = time.Since(t0).Seconds()
	return res, nil
}

var tdMu sync.Mutex

func registerTeardownMethods(srv *rpc.Server, cur func() *session.Session, cmdrSvc *commanderService, dungeonRun *dungeonRunner, brainCtl *brain.Controller, lease *brainLease) {
	srv.Register("teardown", rpc.Typed(func(ctx context.Context, p teardownParams) (teardownResult, error) {
		gm := p.GM
		if gm == "" {
			gm = "off"
		}
		switch gm {
		case "off", "on", "keep":
		default:
			return teardownResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "gm must be off, on or keep"}
		}
		wait := p.WaitMS
		if wait == 0 {
			wait = 4000
		}
		if wait < 1000 || wait > 10000 {
			return teardownResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("wait_ms %d: 1000 to 10000", p.WaitMS)}
		}
		if !tdMu.TryLock() {
			return teardownResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "a teardown is going"}
		}
		defer tdMu.Unlock()
		g := sessionTeardownGame{cur: cur, c: cmdrSvc, runner: dungeonRun, brain: brainCtl, lease: lease}
		return runTeardown(ctx, g, teardownParams{DryRun: p.DryRun, GM: gm, WaitMS: wait}, time.Sleep)
	}))
}
