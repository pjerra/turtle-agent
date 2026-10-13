package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/navmesh"
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

// commanderService holds the one commander of the agent: the command sender and the feed that follows the event log.
type commanderService struct {
	cur     func() *session.Session
	cmdr    *tbm.Commander
	feed    *tbm.ViewFeed
	planner *navmesh.Planner
}

// newCommanderService starts the feed on the session's event log (the log outlives reconnects) until ctx ends.
func newCommanderService(ctx context.Context, cur func() *session.Session, planner *navmesh.Planner, wg *sync.WaitGroup) *commanderService {
	log := cur().Log()
	emit := func(kind, text string, data map[string]any) {
		log.Append(events.Event{Kind: kind, Text: text, Data: data})
	}
	c := &commanderService{cur: cur, cmdr: tbm.NewCommander(sessionLink{cur}), feed: tbm.NewViewFeed(log, emit, tbm.DefaultEventEvery), planner: planner}
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.feed.Run(ctx)
	}()
	return c
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
	r, err := c.cmdr.DoWait(ctx, command, wait)
	return commanderResult{Sent: ".bot " + command, Reply: r}, rpcErr(err)
}

type botViewParams struct {
	AfterN uint64 `json:"after_n,omitempty"` // wait for a frame newer than this counter (the n of the last result)
	WaitMS int    `json:"wait_ms,omitempty"` // how long to wait for it; 0: return what there is
}

type botViewResult struct {
	Have        bool             `json:"have"`
	N           uint64           `json:"n"` // frames the feed has published; pass it as after_n to wait for the next
	AgeMS       int64            `json:"age_ms"`
	Frame       *tbm.ViewFrame   `json:"frame,omitempty"`
	Stats       tbm.ViewStats    `json:"stats"`
	BindingEnds []tbm.BindingEnd `json:"binding_ends"`
}

func (c *commanderService) view(ctx context.Context, p botViewParams) (botViewResult, error) {
	if p.WaitMS > 0 {
		wctx, cancel := context.WithTimeout(ctx, waitBound(p.WaitMS, 0))
		_, _, err := c.feed.WaitNew(wctx, p.AfterN)
		cancel()
		if err != nil && ctx.Err() != nil {
			return botViewResult{}, rpcErr(ctx.Err())
		}
	}
	res := botViewResult{Stats: c.feed.Stats(), BindingEnds: c.feed.Ends()}
	if fr, n, ok := c.feed.Latest(); ok {
		res.Have, res.N, res.Frame = true, n, &fr
		res.AgeMS = time.Since(fr.At).Milliseconds()
	}
	return res, nil
}

// registerCommanderMethods wires commander and bot_view. The returned func stops the feed.
func registerCommanderMethods(srv *rpc.Server, cur func() *session.Session, planner *navmesh.Planner) (*commanderService, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	c := newCommanderService(ctx, cur, planner, &wg)
	srv.Register("commander", rpc.Typed(func(ctx context.Context, p commanderParams) (commanderResult, error) {
		return c.do(ctx, p)
	}))
	srv.Register("bot_view", rpc.Typed(func(ctx context.Context, p botViewParams) (botViewResult, error) {
		return c.view(ctx, p)
	}))
	return c, func() { cancel(); wg.Wait() }
}

// remoteGame is the dungeon.RemoteGame over the commander service and the session.
type remoteGame struct{ c *commanderService }

func (g remoteGame) Command(ctx context.Context, command string) (tbm.Reply, error) {
	return g.c.cmdr.Do(ctx, command)
}

func (g remoteGame) Frame() (tbm.ViewFrame, uint64, bool) { return g.c.feed.Latest() }
func (g remoteGame) ResetView()                           { g.c.feed.Reset() }
func (g remoteGame) BindingEnds() []tbm.BindingEnd        { return g.c.feed.Ends() }
func (g remoteGame) ViewStats() tbm.ViewStats             { return g.c.feed.Stats() }

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
