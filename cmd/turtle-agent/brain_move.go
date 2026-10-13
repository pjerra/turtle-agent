package main

import (
	"context"
	"fmt"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/objects"
)

// The movement and sight half of the brain's Game (brain.Mover, brain.Sight, brain.Ground) over the agent's session. The brain
// asks for them by type assertion, so the session needs no more than the navmesh: without mmaps_dir the walks fail with
// ErrNoNavmesh, the sight answers "unknown", and the positioning triggers stay quiet.
var (
	_ brain.Mover  = (*sessionBrainGame)(nil)
	_ brain.Sight  = (*sessionBrainGame)(nil)
	_ brain.Ground = (*sessionBrainGame)(nil)
)

// WalkTo walks to a point along the navmesh (walk_path). Cancelling ctx stops the walk. A walk that ends short is not an error;
// no route is.
func (g *sessionBrainGame) WalkTo(ctx context.Context, x, y, z float32) error {
	r, err := g.cur().WalkPath(ctx, x, y, z)
	if err != nil {
		return err
	}
	switch r.Result {
	case "arrived", "partial":
		return nil
	}
	return fmt.Errorf("walk %s: %s", r.Result, r.Reason)
}

func (g *sessionBrainGame) LineClear(a, b brain.Vec3) (clear, known bool) {
	return g.cur().LineClear(a, b)
}

func (g *sessionBrainGame) SnapToMesh(p brain.Vec3) (brain.Vec3, bool) { return g.cur().SnapToMesh(p) }

func (g *sessionBrainGame) DynamicObjects() []objects.DynamicObject { return g.cur().DynamicObjects() }
