package main

import (
	"context"
	"errors"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
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
	Near    float32 `json:"near,omitempty"`
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

// registerMethods wires the spec's JSON-RPC methods (v1) to the session; later
// milestones add their methods at the end of this function.
func registerMethods(srv *rpc.Server, s *session.Session) {
	log := s.Log()
	srv.Register("status", rpc.Typed(func(ctx context.Context, _ empty) (session.StatusInfo, error) {
		return s.Status(), nil
	}))
	srv.Register("chat", rpc.Typed(func(ctx context.Context, p chatParams) (empty, error) {
		return empty{}, rpcErr(s.Chat(ctx, p.Type, p.Text, p.To))
	}))
	srv.Register("gm", rpc.Typed(func(ctx context.Context, p gmParams) (empty, error) {
		return empty{}, rpcErr(s.GM(ctx, p.Text))
	}))
	srv.Register("events", rpc.Typed(func(ctx context.Context, p eventsParams) (events.Page, error) {
		q := events.Query{Since: p.Since, Kinds: p.Kinds, PartyOnly: p.PartyOnly, Limit: p.Limit}
		return readEvents(ctx, log, q, p.WaitMS), nil
	}))
	srv.Register("chat_log", rpc.Typed(func(ctx context.Context, p chatLogParams) (events.Page, error) {
		return readEvents(ctx, log, events.Query{Since: p.Since, Kinds: events.ChatKinds, Limit: p.Limit}, p.WaitMS), nil
	}))
	srv.Register("logout", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(s.Logout(ctx))
	}))
	srv.Register("invite", rpc.Typed(func(ctx context.Context, p nameParams) (empty, error) {
		return empty{}, rpcErr(s.Invite(ctx, p.Name))
	}))
	srv.Register("accept_invite", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(s.AcceptInvite(ctx))
	}))
	srv.Register("leave_group", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(s.LeaveGroup(ctx))
	}))
	srv.Register("party", rpc.Typed(func(ctx context.Context, _ empty) (session.PartyInfo, error) {
		return s.Party(), nil
	}))
	srv.Register("units", rpc.Typed(func(ctx context.Context, p unitsParams) ([]objects.Unit, error) {
		us := s.Units(objects.Filter{Near: p.Near, Entry: p.Entry, Hostile: p.Hostile, Name: p.Name, Limit: p.Limit})
		if us == nil {
			us = []objects.Unit{}
		}
		return us, nil
	}))
	srv.Register("target", rpc.Typed(func(ctx context.Context, p targetParams) (objects.Unit, error) {
		u, err := s.Target(ctx, p.GUID, p.Name)
		return u, rpcErr(err)
	}))
	srv.Register("attack", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) { return empty{}, rpcErr(s.Attack(ctx)) }))
	srv.Register("stop_attack", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) { return empty{}, rpcErr(s.StopAttack(ctx)) }))
	srv.Register("cast", rpc.Typed(func(ctx context.Context, p castParams) (empty, error) {
		return empty{}, rpcErr(s.Cast(ctx, p.SpellID, p.Target))
	}))
	srv.Register("face", rpc.Typed(func(ctx context.Context, p guidParams) (empty, error) { return empty{}, rpcErr(s.Face(ctx, p.GUID)) }))
	srv.Register("walk_to", rpc.Typed(func(ctx context.Context, p walkParams) (empty, error) {
		return empty{}, rpcErr(s.WalkTo(ctx, p.X, p.Y, p.Z))
	}))
	srv.Register("path_to", rpc.Typed(func(ctx context.Context, p walkParams) (session.PathInfo, error) {
		info, err := s.PathTo(ctx, p.X, p.Y, p.Z)
		return info, rpcErr(err)
	}))
	srv.Register("walk_path", rpc.Typed(func(ctx context.Context, p walkParams) (session.WalkReport, error) {
		r, err := s.WalkPath(ctx, p.X, p.Y, p.Z)
		return r, rpcErr(err)
	}))
	srv.Register("teleport", rpc.Typed(func(ctx context.Context, p pointParams) (empty, error) {
		m := -1
		if p.Map != nil {
			m = *p.Map
		}
		return empty{}, rpcErr(s.Teleport(ctx, p.X, p.Y, p.Z, m))
	}))
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
	switch {
	case err == nil:
		return nil
	case errors.Is(err, session.ErrNotInWorld):
		return &rpc.Error{Code: rpc.CodeNotInWorld, Message: err.Error()}
	case errors.Is(err, session.ErrDisconnected), errors.Is(err, session.ErrClosed), errors.Is(err, context.Canceled),
		errors.Is(err, worldconn.ErrWrite):
		// context.Canceled: the client went away or the agent is shutting down. ErrWrite: the
		// socket died under a write, before Run has noticed the close (review M1).
		return &rpc.Error{Code: rpc.CodeDisconnected, Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return &rpc.Error{Code: rpc.CodeTimeout, Message: err.Error()}
	case errors.As(err, &le):
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error(), Data: map[string]any{"reason": le.Reason}}
	default:
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error()}
	}
}
