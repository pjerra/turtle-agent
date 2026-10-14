package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The remote commander (README "Remote leader"): the agent binds a TortoiseBots bot party it is not in (`.bot commander bind`),
// gives GUID-targeted orders (`.bot cmd ...`) and reads the TBM:VIEW perception feed. commander and bot_view are the manual
// methods; dungeon_run with mode "remote" drives the same service.

// sessionLink is the commander's way to the session: commands go out as say chat, replies come back in the event log.
type sessionLink struct{ cur func() *session.Session }

func (l sessionLink) Say(ctx context.Context, text string) error { return l.cur().GM(ctx, text) }
func (l sessionLink) Log() *events.Log                           { return l.cur().Log() }

// commanderService holds the one commander of the agent: the command sender and the feed. The feed is wired into the event log
// (events.Log.SetIntercept): every TBM:VIEW line goes to the feed and, unless the debug sampling says otherwise, not into the log;
// the unbind ACKs the module sends when it ends a binding are shown to the feed and stay in the log.
type commanderService struct {
	cur     func() *session.Session
	cmdr    *tbm.Commander
	feed    *tbm.ViewFeed
	planner *navmesh.Planner

	viewLogEvery atomic.Int64 // 0: no VIEW line is logged; n: every nth frame's lines are (debug)
	viewLines    atomic.Uint64
	viewFrames   atomic.Uint64
}

// newCommanderService wires the feed into the session's event log (the log outlives reconnects).
func newCommanderService(cur func() *session.Session, planner *navmesh.Planner) *commanderService {
	log := cur().Log()
	emit := func(kind, text string, data map[string]any) {
		log.Append(events.Event{Kind: kind, Text: text, Data: data})
	}
	c := &commanderService{cur: cur, cmdr: tbm.NewCommander(sessionLink{cur}), feed: tbm.NewViewFeed(log, emit, tbm.DefaultEventEvery), planner: planner}
	log.SetIntercept(c.intercept)
	return c
}

// intercept shows the feed each system and addon event; it consumes the TBM:VIEW lines.
func (c *commanderService) intercept(e events.Event) bool {
	if e.Kind != events.KindSystem && e.Kind != events.KindAddon {
		return false
	}
	line, ok := tbm.LineOf(e)
	if !ok || !strings.HasPrefix(line, "TBM:") {
		return false
	}
	if !strings.HasPrefix(line, tbm.ViewPrefix) {
		c.feed.ProcessLine(line) // the unbind ACK that ends a binding; the event stays in the log
		return false
	}
	c.feed.ProcessLine(line)
	c.viewLines.Add(1)
	if vl, err := tbm.ParseViewLine(line); err == nil && vl.Part == 'B' {
		c.viewFrames.Add(1)
	}
	every := c.viewLogEvery.Load()
	// Debug sampling by frame: the lines of every nth frame are logged.
	return every <= 0 || (c.viewFrames.Load()-1)%uint64(every) != 0
}

type commanderParams struct {
	Op      string   `json:"op"`                 // bind, unbind, status, view or cmd
	LeadBot string   `json:"lead_bot,omitempty"` // bind: the lead bot
	Bots    []string `json:"bots,omitempty"`     // bind: the other bots
	On      *bool    `json:"on,omitempty"`       // view: on or off
	Verb    string   `json:"verb,omitempty"`     // cmd: attack, pull, pullback, cc, focus, move, stay, follow, rest, revive or summon
	Args    []string `json:"args,omitempty"`     // cmd: the arguments of the verb (a GUID is "0x" + hex)
	WaitMS  int      `json:"wait_ms,omitempty"`  // how long to wait for the reply; default 4000
}

type commanderResult struct {
	Sent  string    `json:"sent"`
	Reply tbm.Reply `json:"reply"`
	// Order is the typed outcome of a loot, open, use or lootmethod order, which waits for its final line (through ACTION_PENDING).
	Order *tbm.OrderResult `json:"order,omitempty"`
}

// do runs one commander call.
func (c *commanderService) do(ctx context.Context, p commanderParams) (commanderResult, error) {
	var command string
	switch strings.ToLower(p.Op) {
	case "bind":
		if strings.TrimSpace(p.LeadBot) == "" {
			return commanderResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "bind needs lead_bot"}
		}
		command = strings.Join(append([]string{"commander", "bind", p.LeadBot}, p.Bots...), " ")
		c.feed.Reset() // a new binding counts its frames from seq 1
	case "unbind", "status":
		command = "commander " + strings.ToLower(p.Op)
	case "view":
		if p.On == nil {
			return commanderResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "view needs on true or false"}
		}
		command = "commander view off"
		if *p.On {
			command = "commander view on"
		}
	case "cmd":
		if p.Verb == "" {
			return commanderResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "cmd needs a verb: " + strings.Join(tbm.CmdVerbs, ", ")}
		}
		command = strings.Join(append([]string{"cmd", p.Verb}, p.Args...), " ")
	default:
		return commanderResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "op must be bind, unbind, status, view or cmd"}
	}
	if err := tbm.CheckCommand(command); err != nil {
		return commanderResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	}
	var wait time.Duration
	if p.WaitMS > 0 {
		wait = waitBound(p.WaitMS, 4000)
	}
	if verb := strings.Fields(command); len(verb) > 1 && strings.EqualFold(verb[0], "cmd") && slices.Contains(orderVerbs, strings.ToLower(verb[1])) {
		o, err := c.cmdr.OrderWait(ctx, command, wait)
		rep := tbm.Reply{Kind: o.Kind, OK: o.OK, Line: o.Final, Ack: o.Ack, Err: o.Err, Lines: o.Lines}
		return commanderResult{Sent: ".bot " + command, Reply: rep, Order: &o}, rpcErr(err)
	}
	r, err := c.cmdr.DoWait(ctx, command, wait)
	return commanderResult{Sent: ".bot " + command, Reply: r}, rpcErr(err)
}

// orderVerbs are the `cmd` verbs that run for a while and answer ACTION_PENDING before their final line.
var orderVerbs = []string{"loot", "open", "use"}

type botViewParams struct {
	AfterN uint64 `json:"after_n,omitempty"` // wait for a frame newer than this counter (the n of the last result)
	WaitMS int    `json:"wait_ms,omitempty"` // how long to wait for it; 0: return what there is
	// LogEvery is a debug switch: with n above 0, every nth frame's TBM:VIEW lines are also written to the event log (by default none
	// are, so a long run does not push other events out); 0 turns it off again. Omitted: unchanged.
	LogEvery *int `json:"log_every,omitempty"`
}

type botViewResult struct {
	Have        bool             `json:"have"`
	N           uint64           `json:"n"` // frames the feed has published; pass it as after_n to wait for the next
	AgeMS       int64            `json:"age_ms"`
	Frame       *tbm.ViewFrame   `json:"frame,omitempty"`
	Stats       tbm.ViewStats    `json:"stats"`
	BindingEnds []tbm.BindingEnd `json:"binding_ends"`
	LogEvery    int64            `json:"log_every"`
	ViewLines   uint64           `json:"view_lines"` // TBM:VIEW lines read, logged or not
}

func (c *commanderService) view(ctx context.Context, p botViewParams) (botViewResult, error) {
	if p.LogEvery != nil {
		if *p.LogEvery < 0 || *p.LogEvery > 10000 {
			return botViewResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "log_every must be 0 to 10000"}
		}
		c.viewLogEvery.Store(int64(*p.LogEvery))
	}
	if p.WaitMS > 0 {
		wctx, cancel := context.WithTimeout(ctx, waitBound(p.WaitMS, 0))
		_, _, err := c.feed.WaitNew(wctx, p.AfterN)
		cancel()
		if err != nil && ctx.Err() != nil {
			return botViewResult{}, rpcErr(ctx.Err())
		}
	}
	res := botViewResult{Stats: c.feed.Stats(), BindingEnds: c.feed.Ends(), LogEvery: c.viewLogEvery.Load(), ViewLines: c.viewLines.Load()}
	if fr, n, ok := c.feed.Latest(); ok {
		res.Have, res.N, res.Frame = true, n, &fr
		res.AgeMS = time.Since(fr.At).Milliseconds()
	}
	return res, nil
}

// registerCommanderMethods wires commander and bot_view. The returned func removes the log's intercept.
func registerCommanderMethods(srv *rpc.Server, cur func() *session.Session, planner *navmesh.Planner) (*commanderService, func()) {
	c := newCommanderService(cur, planner)
	srv.Register("commander", rpc.Typed(func(ctx context.Context, p commanderParams) (commanderResult, error) {
		return c.do(ctx, p)
	}))
	srv.Register("bot_view", rpc.Typed(func(ctx context.Context, p botViewParams) (botViewResult, error) {
		return c.view(ctx, p)
	}))
	return c, func() { cur().Log().SetIntercept(nil) }
}

// remoteGame is the dungeon.RemoteGame over the commander service and the session.
type remoteGame struct{ c *commanderService }

func (g remoteGame) Command(ctx context.Context, command string) (tbm.Reply, error) {
	return g.c.cmdr.Do(ctx, command)
}

// Order implements dungeon.OrderGame: a `cmd loot|open|use|lootmethod` order, waited for through ACTION_PENDING to its final line.
func (g remoteGame) Order(ctx context.Context, command string) (tbm.OrderResult, error) {
	return g.c.cmdr.Order(ctx, command)
}

// Behavior implements dungeon.OrderGame: `.bot behavior <bot> <key> on|off`.
func (g remoteGame) Behavior(ctx context.Context, bot, key string, on bool) (tbm.Reply, error) {
	return g.c.cmdr.Behavior(ctx, bot, key, on)
}

// GameObjects implements dungeon.OrderGame: the game objects the agent's own character has in view.
func (g remoteGame) GameObjects() []objects.GameObject {
	return g.c.cur().GameObjects(objects.GOFilter{Limit: 5000})
}

func (g remoteGame) Frame() (tbm.ViewFrame, uint64, bool) { return g.c.feed.Latest() }
func (g remoteGame) ResetView()                           { g.c.feed.Reset() }
func (g remoteGame) BindingEnds() []tbm.BindingEnd        { return g.c.feed.Ends() }
func (g remoteGame) ViewStats() tbm.ViewStats             { return g.c.feed.Stats() }

// Team is the agent's own side (dungeon.TeamSource): the feed carries no race, and the owned bots share the account's faction.
func (g remoteGame) Team() string { return g.c.cur().Team() }

// BotLines implements dungeon.BotLiner: a plain `.bot maintenance|inv|logout <bot>` and the lines it was answered with.
func (g remoteGame) BotLines(ctx context.Context, command string, wait time.Duration, done func(line string) bool) ([]string, bool, error) {
	return g.c.cmdr.Lines(ctx, command, wait, done)
}

func (g remoteGame) Emit(kind, text string, data map[string]any) {
	g.c.cur().Log().Append(events.Event{Kind: kind, Text: text, Data: data})
}

var errNoPlanner = errors.New("no navmesh planner (set mmaps_dir)")

// PlanPath implements dungeon.PathPlanner: the navmesh path between two points of a map, from wherever the agent stands.
func (g remoteGame) PlanPath(ctx context.Context, mapID uint32, from, to dungeon.Point) ([]dungeon.Point, error) {
	if g.c.planner == nil {
		return nil, errNoPlanner
	}
	p, err := g.c.planner.Plan(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
	if err != nil {
		return nil, err
	}
	if len(p.Points) < 2 {
		return nil, fmt.Errorf("no path (%s): %s", p.Status, p.Reason)
	}
	out := make([]dungeon.Point, len(p.Points))
	for i, v := range p.Points {
		out[i] = dungeon.Point{X: v[0], Y: v[1], Z: v[2]}
	}
	return out, nil
}
