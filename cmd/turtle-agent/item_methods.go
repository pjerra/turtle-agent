package main

import (
	"context"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

type useItemParams struct {
	ItemEntry uint32      `json:"item_entry,omitempty"`
	Bag       *uint8      `json:"bag,omitempty"`
	Slot      *uint8      `json:"slot,omitempty"`
	Target    packet.GUID `json:"target,omitempty"`
}

type lootParams struct {
	GUID   packet.GUID `json:"guid,omitempty"`
	Name   string      `json:"name,omitempty"`
	WaitMS int         `json:"wait_ms,omitempty"`
}

type lootTakeParams struct {
	Slot   *uint8 `json:"slot,omitempty"`
	All    bool   `json:"all,omitempty"`
	Money  bool   `json:"money,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

type openObjectParams struct {
	GUID    packet.GUID `json:"guid"`
	SpellID uint32      `json:"spell_id,omitempty"` // the Opening spell; 6477 when omitted
	Take    bool        `json:"take,omitempty"`     // take everything and close the window
	WaitMS  int         `json:"wait_ms,omitempty"`  // for the cast and the window; 15 s when omitted
}

type waitParams struct {
	WaitMS int `json:"wait_ms,omitempty"`
}

type mountParams struct {
	ItemEntry uint32 `json:"item_entry,omitempty"`
	Bag       *uint8 `json:"bag,omitempty"`
	Slot      *uint8 `json:"slot,omitempty"`
	SpellID   uint32 `json:"spell_id,omitempty"`
	WaitMS    int    `json:"wait_ms,omitempty"`
}

type dismountParams struct {
	SpellID uint32 `json:"spell_id,omitempty"`
	WaitMS  int    `json:"wait_ms,omitempty"`
}

// registerItemMethods wires item use, corpse loot and mounts to the session cur returns at
// each call (see registerMethodsFor).
func registerItemMethods(srv *rpc.Server, cur func() *session.Session) {
	srv.Register("use_item", rpc.Typed(func(ctx context.Context, p useItemParams) (objects.Item, error) {
		it, err := cur().UseItem(ctx, session.EquipRef{Entry: p.ItemEntry, Bag: p.Bag, Slot: p.Slot}, p.Target)
		return it, rpcErr(err)
	}))
	srv.Register("loot", rpc.Typed(func(ctx context.Context, p lootParams) (*session.LootWindow, error) {
		if p.GUID == 0 && p.Name == "" { // no corpse named: the window that is open now
			w, err := cur().LootWindow(ctx)
			return w, rpcErr(err)
		}
		w, err := cur().OpenLoot(ctx, p.GUID, p.Name, waitBound(p.WaitMS, 3000))
		return w, rpcErr(err)
	}))
	srv.Register("open_object", rpc.Typed(func(ctx context.Context, p openObjectParams) (session.LootedObject, error) {
		wait := waitBound(p.WaitMS, 15000)
		if p.Take {
			r, err := cur().LootObject(ctx, p.GUID, p.SpellID, wait, 5*time.Second)
			return r, rpcErr(err)
		}
		w, err := cur().OpenObject(ctx, p.GUID, p.SpellID, wait)
		return session.LootedObject{Window: w, Taken: []session.LootSlot{}}, rpcErr(err)
	}))
	srv.Register("loot_take", rpc.Typed(func(ctx context.Context, p lootTakeParams) (session.LootTaken, error) {
		r, err := cur().TakeLoot(ctx, session.LootTake{Slot: p.Slot, All: p.All, Money: p.Money}, waitBound(p.WaitMS, 3000))
		return r, rpcErr(err)
	}))
	srv.Register("loot_close", rpc.Typed(func(ctx context.Context, p waitParams) (empty, error) {
		return empty{}, rpcErr(cur().CloseLoot(ctx, waitBound(p.WaitMS, 3000)))
	}))
	srv.Register("mount", rpc.Typed(func(ctx context.Context, p mountParams) (session.MountInfo, error) {
		ref := session.MountRef{Item: session.EquipRef{Entry: p.ItemEntry, Bag: p.Bag, Slot: p.Slot}, Spell: p.SpellID}
		// 12 s: a bridle's use is a cast of about 4 s, and the mount spell it teaches another 3 s.
		info, err := cur().Mount(ctx, ref, waitBound(p.WaitMS, 12000))
		return info, rpcErr(err)
	}))
	srv.Register("dismount", rpc.Typed(func(ctx context.Context, p dismountParams) (session.MountInfo, error) {
		info, err := cur().Dismount(ctx, p.SpellID, waitBound(p.WaitMS, 3000))
		return info, rpcErr(err)
	}))
}
