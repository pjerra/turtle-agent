package main

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

type empty struct{}

type chatParams struct {
	Type string `json:"type"`
	Text string `json:"text"`
	To   string `json:"to,omitempty"`
}

type gmParams struct {
	Text string `json:"text"`
}

type eventsParams struct {
	Since     uint64   `json:"since"`
	Kinds     []string `json:"kinds,omitempty"`
	PartyOnly bool     `json:"party_only,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	WaitMS    int      `json:"wait_ms,omitempty"`
}

type nameParams struct {
	Name string `json:"name"`
}

type chatLogParams struct {
	Since  uint64 `json:"since"`
	Limit  int    `json:"limit,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

type unitsParams struct {
	Within  float32 `json:"within,omitempty"` // yards, 3D
	Near    float32 `json:"near,omitempty"`   // the older name of within
	Entry   uint32  `json:"entry,omitempty"`
	Hostile *bool   `json:"hostile,omitempty"`
	Name    string  `json:"name,omitempty"`
	Limit   int     `json:"limit,omitempty"`
}

type targetParams struct {
	GUID packet.GUID `json:"guid,omitempty"`
	Name string      `json:"name,omitempty"`
}

type castParams struct {
	SpellID uint32      `json:"spell_id"`
	Target  packet.GUID `json:"target,omitempty"`
}

type cancelCastParams struct {
	SpellID uint32 `json:"spell_id,omitempty"` // 0: whatever is being cast
}

type guidParams struct {
	GUID packet.GUID `json:"guid"`
}

type walkParams struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

type pointParams struct {
	X   float32 `json:"x"`
	Y   float32 `json:"y"`
	Z   float32 `json:"z"`
	Map *int    `json:"map,omitempty"`
}

type markParams struct {
	GUID packet.GUID `json:"guid,omitempty"`
	Name string      `json:"name,omitempty"`
	Icon string      `json:"icon"` // skull, cross, square, moon, triangle, diamond, circle, star or clear
}

type rollParams struct {
	RollID   uint64      `json:"roll_id,omitempty"`
	ItemSlot *uint32     `json:"item_slot,omitempty"`
	Looted   packet.GUID `json:"looted,omitempty"`
	Choice   string      `json:"choice"` // need, greed or pass
}

type inventoryParams struct {
	Bags bool `json:"bags,omitempty"` // the items in the four bags too
}

type equipParams struct {
	ItemEntry uint32 `json:"item_entry,omitempty"`
	Bag       *uint8 `json:"bag,omitempty"`
	Slot      *uint8 `json:"slot,omitempty"`
}

// logoutResult says how the logout ended: "complete" (SMSG_LOGOUT_COMPLETE) or
// "closed_by_server" (the server accepted it, then closed the socket; issues #9, #18).
// "lost" and "timeout" are errors (-32002, -32004) carrying data.outcome.
type logoutResult struct {
	Outcome string `json:"outcome"`
}

// methodOptions are the settings some methods need.
type methodOptions struct {
	// AddonDir is the TortoiseBotsManager checkout addon_start loads (settings tbm_addon_dir);
	// empty means the pinned copy tools/tbmcontract/fetch.sh keeps under the working directory.
	AddonDir string
	// DungeonsDir holds the plans dungeon_run reads (settings dungeons_dir); empty means "dungeons".
	DungeonsDir string
	// TunePath is the login's --tune: the tunables file of dungeon runs that name none (empty: <plan>.tune.json beside the plan).
	TunePath string
	// TracesDir is where run traces go (settings traces_dir); empty means "traces".
	TracesDir string
	// RunMeta is the default AI driver of the slot's runs (settings run_meta): dungeon_run's run_meta wins when it names one.
	RunMeta dungeon.RunMeta
	// Planner is the navmesh path_to uses; map_snapshot draws its polygons as the background. Nil: no background.
	Planner *navmesh.Planner
	// PlanCheckNav makes the navmesh dungeon_run's plan check walks, and closes it (nil: a scratch copy of Planner). Tests give a fake.
	PlanCheckNav func() (dungeon.Navmesh, func(), error)
	// AccountProbeWindow is how long a dungeon run waits for the server's answer to its ".account" probe; 0 means 2 s. The
	// tests shorten it: their server never answers, and the wait would race their own.
	AccountProbeWindow time.Duration
	// EntryLedgerPath is the file that keeps the account's instance entries of the last hour (a remote run's enter phase refuses a 6th);
	// empty keeps them in memory only.
	EntryLedgerPath string
	// Follow starts the player follow at once (login --follow); nil starts nothing.
	Follow *followStartParams
	// Stderr is where the at-once follow start reports; nil discards it.
	Stderr io.Writer
}

// registerMethodsFor wires the spec's JSON-RPC methods, and every later milestone's, to whatever
// session cur returns at each call, so
// --reconnect can swap a new session in behind the same address. The sessions share one
// event log, so event cursors stay valid across a reconnect. The returned func stops what
// the methods started (the addon host); call it before the process ends.
func registerMethodsFor(srv *rpc.Server, cur func() *session.Session, opts methodOptions) func() {
	log := cur().Log()
	srv.Register("status", rpc.Typed(func(ctx context.Context, _ empty) (session.StatusInfo, error) {
		return cur().Status(), nil
	}))
	srv.Register("chat", rpc.Typed(func(ctx context.Context, p chatParams) (empty, error) {
		return empty{}, rpcErr(cur().Chat(ctx, p.Type, p.Text, p.To))
	}))
	srv.Register("gm", rpc.Typed(func(ctx context.Context, p gmParams) (empty, error) {
		return empty{}, rpcErr(cur().GM(ctx, p.Text))
	}))
	srv.Register("events", rpc.Typed(func(ctx context.Context, p eventsParams) (events.Page, error) {
		q := events.Query{Since: p.Since, Kinds: p.Kinds, PartyOnly: p.PartyOnly, Limit: p.Limit}
		return readEvents(ctx, log, q, p.WaitMS), nil
	}))
	srv.Register("chat_log", rpc.Typed(func(ctx context.Context, p chatLogParams) (events.Page, error) {
		return readEvents(ctx, log, events.Query{Since: p.Since, Kinds: events.ChatKinds, Limit: p.Limit}, p.WaitMS), nil
	}))
	srv.Register("logout", rpc.Typed(func(ctx context.Context, _ empty) (logoutResult, error) {
		outcome, err := cur().Logout(ctx)
		return logoutResult{Outcome: string(outcome)}, rpcErr(err)
	}))
	srv.Register("invite", rpc.Typed(func(ctx context.Context, p nameParams) (empty, error) {
		return empty{}, rpcErr(cur().Invite(ctx, p.Name))
	}))
	srv.Register("accept_invite", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(cur().AcceptInvite(ctx))
	}))
	srv.Register("leave_group", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(cur().LeaveGroup(ctx))
	}))
	srv.Register("party", rpc.Typed(func(ctx context.Context, _ empty) (session.PartyInfo, error) {
		return cur().Party(), nil
	}))
	srv.Register("units", rpc.Typed(func(ctx context.Context, p unitsParams) ([]objects.Unit, error) {
		if p.Within == 0 {
			p.Within = p.Near
		}
		us := cur().Units(objects.Filter{Near: p.Within, Entry: p.Entry, Hostile: p.Hostile, Name: p.Name, Limit: p.Limit})
		if us == nil {
			us = []objects.Unit{}
		}
		return us, nil
	}))
	srv.Register("unit", rpc.Typed(func(ctx context.Context, p targetParams) (objects.Unit, error) {
		u, err := cur().Unit(p.GUID, p.Name)
		return u, rpcErr(err)
	}))
	srv.Register("target", rpc.Typed(func(ctx context.Context, p targetParams) (objects.Unit, error) {
		u, err := cur().Target(ctx, p.GUID, p.Name)
		return u, rpcErr(err)
	}))
	srv.Register("attack", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) { return empty{}, rpcErr(cur().Attack(ctx)) }))
	srv.Register("stop_attack", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) { return empty{}, rpcErr(cur().StopAttack(ctx)) }))
	srv.Register("cast", rpc.Typed(func(ctx context.Context, p castParams) (empty, error) {
		return empty{}, rpcErr(cur().Cast(ctx, p.SpellID, p.Target))
	}))
	srv.Register("cancel_cast", rpc.Typed(func(ctx context.Context, p cancelCastParams) (empty, error) {
		return empty{}, rpcErr(cur().CancelCast(ctx, p.SpellID))
	}))
	srv.Register("face", rpc.Typed(func(ctx context.Context, p guidParams) (empty, error) {
		return empty{}, rpcErr(cur().Face(ctx, p.GUID))
	}))
	srv.Register("walk_to", rpc.Typed(func(ctx context.Context, p walkParams) (empty, error) {
		return empty{}, rpcErr(cur().WalkTo(ctx, p.X, p.Y, p.Z))
	}))
	srv.Register("path_to", rpc.Typed(func(ctx context.Context, p walkParams) (session.PathInfo, error) {
		info, err := cur().PathTo(ctx, p.X, p.Y, p.Z)
		return info, rpcErr(err)
	}))
	srv.Register("walk_path", rpc.Typed(func(ctx context.Context, p walkParams) (session.WalkReport, error) {
		r, err := cur().WalkPath(ctx, p.X, p.Y, p.Z)
		return r, rpcErr(err)
	}))
	srv.Register("teleport", rpc.Typed(func(ctx context.Context, p pointParams) (empty, error) {
		m := -1
		if p.Map != nil {
			m = *p.Map
		}
		return empty{}, rpcErr(cur().Teleport(ctx, p.X, p.Y, p.Z, m))
	}))
	srv.Register("mark", rpc.Typed(func(ctx context.Context, p markParams) (session.MarkResult, error) {
		res, err := cur().Mark(ctx, p.GUID, p.Name, p.Icon)
		return res, rpcErr(err)
	}))
	srv.Register("roll", rpc.Typed(func(ctx context.Context, p rollParams) (session.RollInfo, error) {
		res, err := cur().Roll(ctx, session.RollRef{ID: p.RollID, Slot: p.ItemSlot, Looted: p.Looted}, p.Choice)
		return res, rpcErr(err)
	}))
	srv.Register("inventory", rpc.Typed(func(ctx context.Context, p inventoryParams) ([]objects.Item, error) {
		inv := cur().Inventory()
		if p.Bags {
			inv = cur().Items()
		}
		if inv == nil {
			inv = []objects.Item{}
		}
		return inv, nil
	}))
	srv.Register("equip", rpc.Typed(func(ctx context.Context, p equipParams) (objects.Item, error) {
		it, err := cur().Equip(ctx, session.EquipRef{Entry: p.ItemEntry, Bag: p.Bag, Slot: p.Slot})
		return it, rpcErr(err)
	}))
	registerSocialMethods(srv, cur)
	stopLFG := registerLFGMethods(srv, cur)
	registerGroupMethods(srv, cur)
	registerItemMethods(srv, cur)
	registerTradeMethods(srv, cur)
	registerGameObjectMethods(srv, cur)
	registerNPCMethods(srv, cur)
	registerSpellMethods(srv, cur)
	registerDeathMethods(srv, cur)
	registerPetMethods(srv, cur)
	stopAddons := registerAddonHost(srv, cur, opts.AddonDir)
	brainCtl, lease, stopBrain := registerBrainMethods(srv, cur, opts.TracesDir)
	cmdrSvc, stopCommander := registerCommanderMethods(srv, cur, opts.Planner, opts.EntryLedgerPath, opts.AccountProbeWindow)
	stopDungeon, dungeonRun := registerDungeonMethods(srv, cur, opts.DungeonsDir, brainCtl, lease, opts.AccountProbeWindow, cmdrSvc)
	dungeonRun.tracesDir = opts.TracesDir
	dungeonRun.runMeta = opts.RunMeta
	dungeonRun.tunePath = opts.TunePath
	dungeonRun.checkNav = opts.PlanCheckNav
	stopMap := registerMapMethods(srv, cur, opts.Planner, opts.DungeonsDir, dungeonRun)
	registerStageMethods(srv, cmdrSvc, dungeonRun, opts.Planner)
	// The follow service shares the runner's movement mode (dungeonRun.moves): dungeon_run and follow_start refuse each other.
	followSvc, stopFollow := registerFollowMethods(srv, cur, opts.DungeonsDir, opts.Planner != nil, brainCtl, dungeonRun, followOptions{Follow: opts.Follow, Stderr: opts.Stderr})
	registerTeardownMethods(srv, cur, cmdrSvc, dungeonRun, brainCtl, lease, followSvc)
	return func() {
		stopFollow()
		stopMap()
		stopDungeon()
		stopCommander()
		stopBrain()
		stopLFG()
		stopAddons()
	}
}

// readEvents long-polls up to waitMS (at most 25 s) until a matching event exists. A
// cursor past the end of the log (a client that outlived an agent restart) is clamped to
// Last(), so the wait ends on the next append rather than when the log catches up, and the
// page reports Dropped, since the cursor belongs to an earlier log (ruling T14 minor 6).
func readEvents(ctx context.Context, log *events.Log, q events.Query, waitMS int) events.Page {
	ahead := false
	if last := log.Last(); q.Since > last {
		q.Since, ahead = last, true
	}
	p := log.Read(q)
	p.Dropped = p.Dropped || ahead
	if waitMS <= 0 || len(p.Events) > 0 {
		return p
	}
	if waitMS > 25000 {
		waitMS = 25000
	}
	wctx, cancel := context.WithTimeout(ctx, time.Duration(waitMS)*time.Millisecond)
	defer cancel()
	// A gap seen on the first read stays reported: the later reads start past it.
	dropped := p.Dropped
	for len(p.Events) == 0 {
		if err := log.Wait(wctx, p.Next); err != nil {
			break
		}
		q.Since = p.Next
		p = log.Read(q)
	}
	p.Dropped = p.Dropped || dropped
	return p
}

// rpcErr maps session errors onto the JSON-RPC codes the Python wrapper branches on.
func rpcErr(err error) error {
	var le *session.LogoutError
	var te *session.TaughtError
	var tf *session.TradeFailed
	switch {
	case err == nil:
		return nil
	case errors.Is(err, session.ErrNotInWorld):
		return &rpc.Error{Code: rpc.CodeNotInWorld, Message: err.Error()}
	case errors.Is(err, session.ErrLostAtLogout):
		return &rpc.Error{Code: rpc.CodeDisconnected, Message: err.Error(), Data: map[string]any{"outcome": string(session.LogoutLost)}}
	case errors.Is(err, session.ErrLogoutTimeout):
		return &rpc.Error{Code: rpc.CodeTimeout, Message: err.Error(), Data: map[string]any{"outcome": string(session.LogoutTimedOut)}}
	case errors.Is(err, session.ErrDisconnected), errors.Is(err, session.ErrClosed), errors.Is(err, context.Canceled),
		errors.Is(err, worldconn.ErrWrite):
		// context.Canceled: the client went away or the agent is shutting down. ErrWrite: the
		// socket died under a write, before Run has noticed the close (review M1).
		return &rpc.Error{Code: rpc.CodeDisconnected, Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return &rpc.Error{Code: rpc.CodeTimeout, Message: err.Error()}
	case errors.As(err, &te):
		// A bridle taught a spell the agent did not cast: the spell, when there is one.
		var data map[string]any
		if te.Spell != 0 {
			data = map[string]any{"learned_spell": te.Spell}
		}
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error(), Data: data}
	case errors.As(err, &tf):
		// A trade that did not complete: the result (how far it got, what the other side had) rides along.
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error(), Data: tf.Result}
	case errors.As(err, &le):
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error(), Data: map[string]any{"reason": le.Reason}}
	default:
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error()}
	}
}
