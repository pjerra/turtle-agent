package main

import (
	"context"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

type petActionParams struct {
	// Action is attack, follow, stay, passive, defensive, aggressive, cast, autocast_on or autocast_off.
	Action  string      `json:"action"`
	Target  packet.GUID `json:"target,omitempty"`
	Slot    *int        `json:"slot,omitempty"`
	SpellID uint32      `json:"spell_id,omitempty"`
	Name    string      `json:"name,omitempty"`
}

// registerPetMethods wires the pet: `pet` (status) and `pet_action` (README "Pets").
func registerPetMethods(srv *rpc.Server, cur func() *session.Session) {
	srv.Register("pet", rpc.Typed(func(ctx context.Context, _ empty) (session.PetInfo, error) {
		return cur().Pet(), nil
	}))
	srv.Register("pet_action", rpc.Typed(func(ctx context.Context, p petActionParams) (session.PetInfo, error) {
		err := cur().PetAction(ctx, session.PetOrder{Action: p.Action, Target: p.Target, Slot: p.Slot, SpellID: p.SpellID, Name: p.Name})
		return cur().Pet(), rpcErr(err)
	}))
}
