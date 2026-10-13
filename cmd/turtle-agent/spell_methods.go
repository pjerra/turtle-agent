package main

import (
	"context"

	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

type spellsParams struct {
	SpellID uint32 `json:"spell_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Highest bool   `json:"highest,omitempty"`
	Passive bool   `json:"passive,omitempty"`
}

// registerSpellMethods wires the agent's own spell state (README "Agent state") to the session
// cur returns at each call (see registerMethodsFor).
func registerSpellMethods(srv *rpc.Server, cur func() *session.Session) {
	srv.Register("spells", rpc.Typed(func(ctx context.Context, p spellsParams) ([]session.SpellRow, error) {
		rows, err := cur().Spells(session.SpellsFilter{SpellID: p.SpellID, Name: p.Name, Highest: p.Highest, Passive: p.Passive})
		return rows, rpcErr(err)
	}))
	srv.Register("cast_state", rpc.Typed(func(ctx context.Context, _ empty) (session.CastStateInfo, error) {
		info, err := cur().CastState()
		return info, rpcErr(err)
	}))
}
