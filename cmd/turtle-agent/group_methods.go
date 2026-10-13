package main

import (
	"context"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

// memberParams names a group member by guid or by name; the player itself counts.
type memberParams struct {
	GUID packet.GUID `json:"guid,omitempty"`
	Name string      `json:"name,omitempty"`
}

type assistantParams struct {
	GUID  packet.GUID `json:"guid,omitempty"`
	Name  string      `json:"name,omitempty"`
	Clear bool        `json:"clear,omitempty"` // take the flag away instead of setting it
}

type raidMoveParams struct {
	GUID     packet.GUID `json:"guid,omitempty"`
	Name     string      `json:"name,omitempty"`
	Subgroup int         `json:"subgroup"`            // 1-8
	SwapWith string      `json:"swap_with,omitempty"` // trade places with this member of the target subgroup
}

type lootMethodParams struct {
	Method     string      `json:"method"`                // free_for_all, round_robin, master_loot, group_loot, need_before_greed
	Master     packet.GUID `json:"master,omitempty"`      // master_loot: the master looter's guid ...
	MasterName string      `json:"master_name,omitempty"` // ... or name; the player when neither is given
	Threshold  uint8       `json:"threshold,omitempty"`   // item quality 1-5; 0 keeps the group's
}

// registerGroupMethods wires group control: promote, assistant, convert_raid, raid_move, disband, loot_method.
func registerGroupMethods(srv *rpc.Server, cur func() *session.Session) {
	srv.Register("promote", rpc.Typed(func(ctx context.Context, p memberParams) (empty, error) {
		return empty{}, rpcErr(cur().Promote(ctx, p.GUID, p.Name))
	}))
	srv.Register("assistant", rpc.Typed(func(ctx context.Context, p assistantParams) (empty, error) {
		return empty{}, rpcErr(cur().Assistant(ctx, p.GUID, p.Name, p.Clear))
	}))
	srv.Register("convert_raid", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(cur().ConvertRaid(ctx))
	}))
	srv.Register("raid_move", rpc.Typed(func(ctx context.Context, p raidMoveParams) (empty, error) {
		return empty{}, rpcErr(cur().RaidMove(ctx, p.GUID, p.Name, p.Subgroup, p.SwapWith))
	}))
	srv.Register("loot_method", rpc.Typed(func(ctx context.Context, p lootMethodParams) (empty, error) {
		return empty{}, rpcErr(cur().SetLootMethod(ctx, p.Method, p.Master, p.MasterName, p.Threshold))
	}))
	srv.Register("disband", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(cur().Disband(ctx))
	}))
}
