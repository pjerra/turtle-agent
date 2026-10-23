package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
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
	// ledger is the account's list of instance entries (the enter phase of a remote run); probeWindow is how long ".account" is awaited.
	ledger      *entryLedger
	probeWindow time.Duration

	viewLogEvery atomic.Int64 // 0: no VIEW line is logged; n: every nth frame's lines are (debug)
	viewLines    atomic.Uint64
	viewFrames   atomic.Uint64
}

// newCommanderService wires the feed into the session's event log (the log outlives reconnects).
func newCommanderService(cur func() *session.Session, planner *navmesh.Planner, ledgerPath string, probeWindow time.Duration) *commanderService {
	log := cur().Log()
	emit := func(kind, text string, data map[string]any) {
		log.Append(events.Event{Kind: kind, Text: text, Data: data})
	}
	c := &commanderService{cur: cur, cmdr: tbm.NewCommander(sessionLink{cur}), feed: tbm.NewViewFeed(log, emit, tbm.DefaultEventEvery), planner: planner,
		ledger: &entryLedger{path: ledgerPath}, probeWindow: probeWindow}
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
	Op      string   `json:"op"`                 // bind, unbind, status, view, cmd or run
	LeadBot string   `json:"lead_bot,omitempty"` // bind: the lead bot
	Bots    []string `json:"bots,omitempty"`     // bind: the other bots
	On      *bool    `json:"on,omitempty"`       // view: on or off
	Verb    string   `json:"verb,omitempty"`     // cmd: attack, pull, ... ; run: start, pause, resume, skip, stop, status or list
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
	case "run":
		if p.Verb == "" {
			return commanderResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "run needs a verb: " + strings.Join(tbm.RunVerbs, ", ")}
		}
		command = strings.Join(append([]string{"run", p.Verb}, p.Args...), " ")
	case "cmd":
		if p.Verb == "" {
			return commanderResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "cmd needs a verb: " + strings.Join(tbm.CmdVerbs, ", ")}
		}
		command = strings.Join(append([]string{"cmd", p.Verb}, p.Args...), " ")
	default:
		return commanderResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "op must be bind, unbind, status, view, cmd or run"}
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
var orderVerbs = []string{"loot", "open", "use", "ritual"}

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
func registerCommanderMethods(srv *rpc.Server, cur func() *session.Session, planner *navmesh.Planner, ledgerPath string, probeWindow time.Duration) (*commanderService, func()) {
	c := newCommanderService(cur, planner, ledgerPath, probeWindow)
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

// RunStatus is the newest TBM:RUN line the feed read (dungeon.RunLeaderWatcher).
func (g remoteGame) RunStatus() (tbm.RunStatus, uint64, bool) { return g.c.feed.RunLatest() }
func (g remoteGame) ResetView()                               { g.c.feed.Reset() }
func (g remoteGame) BindingEnds() []tbm.BindingEnd            { return g.c.feed.Ends() }
func (g remoteGame) ViewStats() tbm.ViewStats                 { return g.c.feed.Stats() }

// Team is the agent's own side (dungeon.TeamSource): the feed carries no race, and the owned bots share the account's faction.
func (g remoteGame) Team() string { return g.c.cur().Team() }

// BotLines implements dungeon.BotLiner: a plain `.bot maintenance|inv|logout <bot>` and the lines it was answered with.
func (g remoteGame) BotLines(ctx context.Context, command string, wait time.Duration, done func(line string) bool) ([]string, bool, error) {
	return g.c.cmdr.Lines(ctx, command, wait, done)
}

// BotLinesAll implements dungeon.BotLinesAller: several `.bot` lines sent together and answered in one wait.
func (g remoteGame) BotLinesAll(ctx context.Context, reqs []dungeon.BotLineRequest, wait time.Duration) ([][]string, []bool, error) {
	rs := make([]tbm.LineRequest, len(reqs))
	for i, r := range reqs {
		rs[i] = tbm.LineRequest{Command: r.Command, Done: r.Done}
	}
	res, err := g.c.cmdr.LinesAll(ctx, rs, wait)
	lines, settled := make([][]string, len(res)), make([]bool, len(res))
	for i, r := range res {
		lines[i], settled[i] = r.Lines, r.Settled
	}
	return lines, settled, err
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

var _ dungeon.AvoidMarker = remoteGame{}

// MarkAvoid implements dungeon.AvoidMarker: the plan's avoid boxes are marked on the agent's planner, so its paths and floor snaps keep
// out of them (the session's own walks too: it is the same planner).
func (g remoteGame) MarkAvoid(mapID uint32, boxes []dungeon.AvoidBox) {
	if g.c.planner == nil {
		return
	}
	g.c.planner.SetAvoid(mapID, dungeon.NavBoxes(boxes))
}

// SnapToFloor implements dungeon.FloorSnapper: the navmesh floor point nearest p (within a few yards), false when there is none.
func (g remoteGame) SnapToFloor(ctx context.Context, mapID uint32, p dungeon.Point) (dungeon.Point, bool) {
	if g.c.planner == nil {
		return p, false
	}
	n, ok, err := g.c.planner.Nearest(mapID, navmesh.Vec3{p.X, p.Y, p.Z})
	if err != nil || !ok {
		return p, false
	}
	return dungeon.Point{X: n[0], Y: n[1], Z: n[2]}, true
}

// FloorsAt implements dungeon.FloorLister: the walkable ground floors of the navmesh under (x, y) within 12 yd of z, highest first;
// false when there is no planner, the tile is missing or the lookup fails.
func (g remoteGame) FloorsAt(ctx context.Context, mapID uint32, x, y, z float32) ([]float32, bool) {
	if g.c.planner == nil {
		return nil, false
	}
	fl, err := g.c.planner.Floors(mapID, x, y, z-12, z+12)
	if err != nil || len(fl) == 0 {
		return nil, false
	}
	var out []float32
	for _, f := range fl {
		if f.Flags&navmesh.NavGround != 0 {
			out = append(out, f.Z)
		}
	}
	return out, len(out) > 0
}

// The enter phase of a remote run (dungeon.Enterer): the agent's own character, which stays outside and ungrouped, and the one GM
// command the phase sends.

func (g remoteGame) InGroup() bool    { return g.c.cur().Party().InGroup }
func (g remoteGame) AgentMap() uint32 { return g.c.cur().Status().Map }

func (g remoteGame) LeaveGroup(ctx context.Context) error { return g.c.cur().LeaveGroup(ctx) }

// AccountLevel reads the account's security level with ".account", as the group driver's probe does.
func (g remoteGame) AccountLevel(ctx context.Context) (int, bool, error) {
	return (&sessionGame{cur: g.c.cur, probeWindow: g.c.probeWindow}).ProbeAccount(ctx)
}

// teleLocation is what a game_tele name can be in the line TeleName sends.
var teleLocation = regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)

// TeleName sends ".tele name <bot> <location>": the only GM line the remote run sends. Both words are checked first.
func (g remoteGame) TeleName(ctx context.Context, player, location string) error {
	if !botName.MatchString(player) || !teleLocation.MatchString(location) {
		return fmt.Errorf("tele name: %q %q is not a bot name and a game_tele name", player, location)
	}
	return g.c.cur().GM(ctx, ".tele name "+player+" "+location)
}

// The prepare step (dungeon.Preparer): the agent's own character and the three GM lines it may send beyond .tele name. Each takes the
// text from this file, not from the caller: a name and a GUID are all that vary.

// AgentName and AgentGUID are the agent's own character.
func (g remoteGame) AgentName() string      { return g.c.cur().Status().Character }
func (g remoteGame) AgentGUID() packet.GUID { return g.c.cur().Status().GUID }

// Sees says whether the agent's own object store lists the named player.
func (g remoteGame) Sees(name string) bool {
	for _, u := range g.c.cur().Units(objects.Filter{Limit: 5000}) {
		if u.Type == "player" && strings.EqualFold(u.Name, name) {
			return true
		}
	}
	return false
}

// GMVisible sends ".gm visible on" or ".gm visible off".
func (g remoteGame) GMVisible(ctx context.Context, on bool) error {
	if on {
		return g.c.cur().GM(ctx, ".gm visible on")
	}
	return g.c.cur().GM(ctx, ".gm visible off")
}

// gmSelectConfirmWait is how long the server is given to show a selection before a line that acts on it is given up on.
var gmSelectConfirmWait = 3 * time.Second

// selectConfirmed selects the unit and waits until the SERVER is known to have it in force as the agent's selection
// (session.SelectionConfirmed: the target field of the agent's own object, which the core sends back after it handled CMSG_SET_SELECTION,
// in an echo that cannot be an older one). The core queues that packet (map thread) apart from a GM's chat line (world thread), so a line
// sent right after can run on the selection from before: none (the sender itself) or a stale one, which can be another player. Run 6b:
// `.instance groupunbind all` answered "Glacienne is not in a group" for a selected Lcwarra. Issue #296: an echo still showing the unit from
// an earlier select, with another unit's select pending behind it, is not a confirmation. When an echo of the new select could not be told
// from such an older one (the unit was selected before another unit, and the server has not shown how far it got), a neutral selection
// (none, or the agent itself) is made and confirmed first. An error, with nothing sent, when the server does not show the selection in time.
func (g remoteGame) selectConfirmed(ctx context.Context, guid packet.GUID) error {
	s := g.c.cur()
	timer := time.NewTimer(gmSelectConfirmWait)
	defer timer.Stop()
	if s.SelectionAmbiguous(guid) {
		if n, ok := s.SelectionNeutral(); ok {
			if err := selectUnit(ctx, s, n); err != nil {
				return err
			}
			if err := awaitSelection(ctx, s, n, timer.C); err != nil {
				return fmt.Errorf("%w (the neutral selection made before %v)", err, guid)
			}
		}
	}
	if err := selectUnit(ctx, s, guid); err != nil {
		return err
	}
	return awaitSelection(ctx, s, guid, timer.C)
}

// selectUnit sends the selection of the unit, or of none for 0.
func selectUnit(ctx context.Context, s *session.Session, guid packet.GUID) error {
	if guid == 0 {
		return s.ClearTarget(ctx)
	}
	_, err := s.Target(ctx, guid, "")
	return err
}

// awaitSelection waits until the session says the server has the unit in force as the agent's selection, or until expired fires.
func awaitSelection(ctx context.Context, s *session.Session, guid packet.GUID, expired <-chan time.Time) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if s.SelectionConfirmed(guid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-expired:
			sel, _ := s.ServerSelection()
			return fmt.Errorf("the server did not show %v as the agent's selection within %s (it shows %v): not sending a line that acts on the selection", guid, gmSelectConfirmWait, sel)
		case <-tick.C:
		}
	}
}

// gmSelectAttempts is how many times selectThenGM selects and confirms again when the selection changed after the confirmation.
const gmSelectAttempts = 3

// gmAfterConfirm is a test seam: it runs between the server's confirmation of the selection and the line, where another request (the target RPC,
// the addon host) can select another unit.
var gmAfterConfirm func()

// selectThenGM selects the unit, waits for the server to show the selection, and sends the GM line as one session operation that sends NO
// selection of its own (GMOnConfirmed): the line is sent only while the confirmed selection still stands. A selection sent right before a
// chat line would not be in force when the line runs (the core handles the two on different threads). If another request changed the selection
// after the confirmation, nothing is sent and the unit is selected and confirmed again, up to gmSelectAttempts times.
func (g remoteGame) selectThenGM(ctx context.Context, guid packet.GUID, line string) error {
	var err error
	for try := 0; try < gmSelectAttempts; try++ {
		if err = g.selectConfirmed(ctx, guid); err != nil {
			return err
		}
		if gmAfterConfirm != nil {
			gmAfterConfirm()
		}
		if err = g.c.cur().GMOnConfirmed(ctx, guid, line); !errors.Is(err, session.ErrSelectionChanged) {
			return err
		}
	}
	return fmt.Errorf("%w: it changed %d times between the server's confirmation and the line, which was not sent", err, gmSelectAttempts)
}

// UnbindInstances selects the player with the GUID and sends ".instance unbind all" as one operation of the session (GMOn): the core
// runs it on the selected player (from account level 4; below it on the sender), so the target is chosen by GUID and never left to
// whatever the agent had selected. The selection is confirmed by the server first.
func (g remoteGame) UnbindInstances(ctx context.Context, who string, guid packet.GUID) error {
	if !botName.MatchString(who) || guid == 0 {
		return fmt.Errorf("instance unbind: %q (%v) is not a character name and a GUID", who, guid)
	}
	return g.selectThenGM(ctx, guid, ".instance unbind all")
}

// GiveItem implements dungeon.ItemGiver: it selects the bot by GUID, waits until the server shows that selection, and sends ".additem <entry>"
// as one operation of the session (GMOn), so the line acts on that bot and never on whatever the agent had selected. The core runs it on the
// selected player (HandleAddItemCommand), from account level 3; the caller checks the level and the bag afterwards.
func (g remoteGame) GiveItem(ctx context.Context, who string, guid packet.GUID, entry uint32) error {
	if !botName.MatchString(who) || guid == 0 || entry == 0 {
		return fmt.Errorf("additem: %q (%v) item %d is not a character name, a GUID and an item entry", who, guid, entry)
	}
	return g.selectThenGM(ctx, guid, fmt.Sprintf(".additem %d", entry))
}

// GroupUnbind selects the player with the GUID, waits for the server to show the selection, sends ".instance groupunbind all" as one
// operation (GMOn), then reads the reply the sender gets: "Group unbound. Disbanding." or "<name> is not in a group."
// (Commands.cpp, HandleInstanceGroupUnbindCommand). A "not in a group" that names the agent is GroupWrongTarget (the line ran on the sender),
// one that names a third player is GroupThirdPlayer (it ran on somebody else's selection).
func (g remoteGame) GroupUnbind(ctx context.Context, who string, guid packet.GUID) (dungeon.GroupUnbindResult, error) {
	if !botName.MatchString(who) || guid == 0 {
		return "", fmt.Errorf("instance groupunbind: %q (%v) is not a character name and a GUID", who, guid)
	}
	log := g.c.cur().Log()
	since := log.Last()
	if err := g.selectThenGM(ctx, guid, ".instance groupunbind all"); err != nil {
		return "", err
	}
	res, subject := g.awaitGroupUnbind(ctx, log, since)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return classifyGroupUnbind(res, subject, who, g.AgentName()), nil
}

// classifyGroupUnbind tells whose "not in a group" a reply was: the lead bot's (the bot is in no group), the agent's (the line ran on the
// sender) or a third player's (it ran on somebody else's selection). Any other reply is returned as it is.
func classifyGroupUnbind(res dungeon.GroupUnbindResult, subject, who, agent string) dungeon.GroupUnbindResult {
	if res != dungeon.GroupNotGroups || subject == "" || strings.EqualFold(subject, who) {
		return res
	}
	if strings.EqualFold(subject, agent) {
		return dungeon.GroupWrongTarget
	}
	return dungeon.GroupThirdPlayer
}

// awaitGroupUnbind reads the log after since for the answer to the group unbind: its result and, for "not in a group", whose name it
// carries.
func (g remoteGame) awaitGroupUnbind(ctx context.Context, log *events.Log, since uint64) (dungeon.GroupUnbindResult, string) {
	wctx, cancel := context.WithTimeout(ctx, groupUnbindWait)
	defer cancel()
	cursor := since
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			if r := groupUnbindReply(e.Text); r != "" {
				return r, groupUnbindSubject(e.Text)
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		if log.Wait(wctx, cursor) != nil {
			return dungeon.GroupNoAnswer, ""
		}
	}
}

// groupUnbindWait is how long the reply to the group unbind is awaited.
const groupUnbindWait = 4 * time.Second

// playerLinkName finds the name in a chat player link ("|Hplayer:Name|h[Name]|h") or, without a link, the first word.
var playerLinkName = regexp.MustCompile(`\[([A-Za-z]{2,12})\]|^\s*([A-Za-z]{2,12}) is not in a group`)

// groupUnbindSubject is the player a "... is not in a group." line names ("" for any other line).
func groupUnbindSubject(text string) string {
	if groupUnbindReply(text) != dungeon.GroupNotGroups {
		return ""
	}
	m := playerLinkName.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

// groupUnbindReply classifies a system line as the answer to ".instance groupunbind all" ("" for any other line).
func groupUnbindReply(text string) dungeon.GroupUnbindResult {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "group unbound"):
		return dungeon.GroupUnbound
	case strings.Contains(low, "is not in a group"):
		return dungeon.GroupNotGroups
	}
	return ""
}

// Recent and Record implement dungeon.EntryLedger over the account's ledger file.
func (g remoteGame) Recent(now time.Time) []time.Time { return g.c.ledger.Recent(now) }
func (g remoteGame) Record(now time.Time) error       { return g.c.ledger.Record(now) }

// Keeping the agent near the party (dungeon.Follower): its own character follows the bots after each leg's walk and before the object
// steps. `.appear` is a GM line; it is a setup shortcut of the run (the driver reports it so), not a run GM command.

func (g remoteGame) AgentPlace() (dungeon.Point, uint32, bool) {
	st := g.c.cur().Status()
	return dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}, st.Map, st.State == session.StatusInWorld
}

// Appear sends ".appear <player>"; the name is checked first.
func (g remoteGame) Appear(ctx context.Context, player string) error {
	if !botName.MatchString(player) {
		return fmt.Errorf("appear: %q is not a bot name", player)
	}
	return g.c.cur().GM(ctx, ".appear "+player)
}

// GoXYZ sends ".go xyz x y z map" for the agent's own character (dungeon.StartHands) and waits for its teleport. The numbers are formatted
// here; nothing the caller wrote goes into the line.
func (g remoteGame) GoXYZ(ctx context.Context, mapID uint32, at dungeon.Point) error {
	return g.c.cur().Teleport(ctx, at.X, at.Y, at.Z, int(mapID))
}

// Respawn sends the commander's own ".respawn" with nothing selected (dungeon.Respawner): the core respawns the creatures and game objects
// around the commander. The selection is cleared first and the server is waited for to show it cleared, else the line would act on that
// one unit alone (or fail on a player).
func (g remoteGame) Respawn(ctx context.Context) error {
	return respawnUnselected(ctx, g.c.cur())
}

// respawnUnselected clears the selection, waits until the server is known to have it cleared (selectConfirmed with no unit, so a stale echo
// of an earlier clear does not count), and sends ".respawn".
func respawnUnselected(ctx context.Context, s *session.Session) error {
	g := remoteGame{c: &commanderService{cur: func() *session.Session { return s }}}
	if err := g.selectConfirmed(ctx, 0); err != nil {
		return fmt.Errorf("the selection was not cleared, not sending .respawn: %w", err)
	}
	return s.GM(ctx, ".respawn")
}

// WalkSelf is walk_path for the agent's character.
func (g remoteGame) WalkSelf(ctx context.Context, to dungeon.Point) (dungeon.WalkResult, error) {
	r, err := g.c.cur().WalkPath(ctx, to.X, to.Y, to.Z)
	return dungeon.WalkResult{Result: r.Result, Reason: r.Reason, DistLeft: r.DistLeft}, err
}

// Units implements dungeon.UnitSource: the units in the agent's own view (a boss corpse the feed does not list).
func (g remoteGame) Units() []objects.Unit { return g.c.cur().Units(objects.Filter{Limit: 5000}) }

// The hire step of the prepare block (dungeon.HireGame): the agent's own group, the players in its view, their pets and the item
// prototypes of what they wear.

// classNames maps a unit's class id (UNIT_FIELD_BYTES_0, 1.12) to the name the hire command takes.
var classNames = map[uint8]string{1: "warrior", 2: "paladin", 3: "hunter", 4: "rogue", 5: "priest", 7: "shaman", 8: "mage", 9: "warlock", 11: "druid"}

func (g remoteGame) AgentLevel() int { return int(g.c.cur().Status().Level) }

// Members are the other members of the agent's group; the class comes from the object store when the player is in view.
func (g remoteGame) Members() []dungeon.HireMember {
	s := g.c.cur()
	party := s.Party()
	units := map[packet.GUID]objects.Unit{}
	for _, u := range s.Units(objects.Filter{Limit: 5000}) {
		if u.Type == "player" {
			units[u.GUID] = u
		}
	}
	var out []dungeon.HireMember
	for _, m := range party.Members {
		hm := dungeon.HireMember{Name: m.Name, GUID: m.GUID, Level: int(m.Level), InGroup: true, Online: m.Online, Dead: m.Dead, MaxHP: uint32(m.MaxHealth)}
		if u, ok := units[m.GUID]; ok {
			hm.Class = classNames[u.Class]
			if u.Level != 0 {
				hm.Level = int(u.Level)
			}
		}
		out = append(out, hm)
	}
	return out
}

// Player looks a player up by name: the group first, then the players in view.
func (g remoteGame) Player(name string) (dungeon.HireMember, bool) {
	for _, m := range g.Members() {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	for _, u := range g.c.cur().Units(objects.Filter{Limit: 5000}) {
		if u.Type == "player" && strings.EqualFold(u.Name, name) {
			return dungeon.HireMember{Name: u.Name, GUID: u.GUID, Level: int(u.Level), Class: classNames[u.Class], Online: true, Dead: u.Dead, MaxHP: u.MaxHealth}, true
		}
	}
	return dungeon.HireMember{}, false
}

// PetOf is the name of the pet the player has out in the agent's view.
func (g remoteGame) PetOf(guid packet.GUID) (string, bool) {
	u, ok := g.c.cur().PetOfPlayer(guid)
	if !ok {
		return "", false
	}
	if u.Name == "" {
		return "pet", true
	}
	return u.Name, true
}

// BotBurst implements dungeon.HireGame: a plain `.bot` line whose answer is a run of lines (a bot's spell list).
func (g remoteGame) BotBurst(ctx context.Context, command string, wait, quiet time.Duration, begin func(line string) bool) ([]string, bool, error) {
	return g.c.cmdr.Burst(ctx, command, wait, quiet, begin)
}

// ConvertRaid turns the agent's party into a raid.
func (g remoteGame) ConvertRaid(ctx context.Context) error { return g.c.cur().ConvertRaid(ctx) }

// ItemInfos asks the server for the item prototypes.
func (g remoteGame) ItemInfos(ctx context.Context, entries []uint32, wait time.Duration) (map[uint32]dungeon.ItemInfo, error) {
	m, err := g.c.cur().ItemInfos(ctx, entries, wait)
	out := make(map[uint32]dungeon.ItemInfo, len(m))
	for e, it := range m {
		out[e] = dungeon.ItemInfo{Found: it.Found, Name: it.Name, Class: int(it.Class), SubClass: int(it.SubClass), Quality: int(it.Quality), InventoryType: int(it.InventoryType), ItemLevel: int(it.ItemLevel), RequiredLevel: int(it.RequiredLevel)}
	}
	return out, err
}
