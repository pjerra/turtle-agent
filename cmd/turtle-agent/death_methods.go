package main

import (
	"context"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

type areaTriggerParams struct {
	ID uint32 `json:"id"`
}

// registerDeathMethods wires the player's death: death_status, accept_resurrect, release_spirit, corpse_query,
// reclaim_corpse and area_trigger (README "Death and the corpse run").
func registerDeathMethods(srv *rpc.Server, cur func() *session.Session) {
	srv.Register("death_status", rpc.Typed(func(ctx context.Context, _ empty) (session.DeathInfo, error) {
		return cur().Death(), nil
	}))
	srv.Register("accept_resurrect", rpc.Typed(func(ctx context.Context, _ empty) (game.ResurrectRequest, error) {
		r, err := cur().AcceptResurrect(ctx)
		return r, rpcErr(err)
	}))
	srv.Register("release_spirit", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(cur().ReleaseSpirit(ctx))
	}))
	srv.Register("corpse_query", rpc.Typed(func(ctx context.Context, _ empty) (game.Corpse, error) {
		c, err := cur().CorpseQuery(ctx)
		return c, rpcErr(err)
	}))
	srv.Register("reclaim_corpse", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(cur().ReclaimCorpse(ctx))
	}))
	srv.Register("area_trigger", rpc.Typed(func(ctx context.Context, p areaTriggerParams) (empty, error) {
		return empty{}, rpcErr(cur().AreaTrigger(ctx, p.ID))
	}))
}
