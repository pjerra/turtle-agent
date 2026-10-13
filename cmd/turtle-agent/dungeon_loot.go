package main

import (
	"context"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/session"
)

// The loot_object, use_item and team parts of the dungeon driver's Game (dungeon.ObjectActor, dungeon.TeamSource) over the session.

func lootedOf(r session.LootedObject) dungeon.Looted {
	conv := func(slots []session.LootSlot) []dungeon.ItemCount {
		var out []dungeon.ItemCount
		for _, s := range slots {
			out = append(out, dungeon.ItemCount{Entry: s.Item, Count: s.Count})
		}
		return out
	}
	return dungeon.Looted{Items: conv(r.Taken), Money: r.Money, Left: conv(r.Left)}
}

func (g *sessionGame) LootObject(ctx context.Context, guid packet.GUID) (dungeon.Looted, error) {
	r, err := g.cur().LootObject(ctx, guid, 0, 15*time.Second, 5*time.Second)
	return lootedOf(r), err
}

func (g *sessionGame) LootCorpse(ctx context.Context, guid packet.GUID) (dungeon.Looted, error) {
	r, err := g.cur().LootCorpse(ctx, guid, 5*time.Second)
	return lootedOf(r), err
}

func (g *sessionGame) UseItem(ctx context.Context, entry uint32) error {
	_, err := g.cur().UseItem(ctx, session.EquipRef{Entry: entry}, 0)
	return err
}

// Team is the leader's side from its race (session.Team); "" until the race is known.
func (g *sessionGame) Team() string { return g.cur().Team() }

// LootRules is the group's loot method from the session's group list (dungeon.LootRuler); false outside a group.
func (g *sessionGame) LootRules() (dungeon.LootRules, bool) {
	s := g.cur()
	p := s.Party()
	if !p.InGroup || p.LootMethod == "" {
		return dungeon.LootRules{}, false
	}
	return dungeon.LootRules{Method: p.LootMethod, Master: p.LootMaster, Threshold: p.LootThreshold, Leader: p.Leader != 0 && p.Leader == s.Status().GUID}, true
}

// SetLootRules sets the group's loot method (session.SetLootMethod); the leader must lead the group.
func (g *sessionGame) SetLootRules(ctx context.Context, r dungeon.LootRules) error {
	return g.cur().SetLootMethod(ctx, r.Method, r.Master, "", r.Threshold)
}
