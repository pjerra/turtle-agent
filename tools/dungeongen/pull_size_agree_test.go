package main

import (
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// TestScoutAndPullSizeAgree: dungeongen's pull lint and plan-check's joiner lint score every ZG pull row with the same bodies.
// The estimator lives once (planlint.PullSizes); this pins that the wrapper and the shared function cannot drift apart.
func TestScoutAndPullSizeAgree(t *testing.T) {
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := worldspawn.Load(filepath.Join("testdata", "map309"), 309)
	if err != nil {
		t.Fatal(err)
	}
	pts, err := routePoints(p)
	if err != nil {
		t.Fatal(err)
	}
	rows := pullRows(pts)
	spawns := dungeonSpawns(cs)
	keep := pullKeep(PullFilter{})
	opt := planlint.PullOptions{PartyLevel: routePartyLevel(p), Raid: p.IsRaid(), Ceiling: p.PullCeiling, KillFirst: p.KillFirst, All: true}
	plain := planlint.PullSizes(rows, spawns, w, keep, opt)
	// The dungeongen lint itself reports the over-ceiling rows of the same estimate.
	found, err := PullSizes(p, cs, w, PullFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byRow := map[int]planlint.PullFinding{}
	for _, f := range plain {
		byRow[f.Row] = f
	}
	for _, f := range found {
		if byRow[f.Row].Bodies != f.Bodies {
			t.Errorf("leg %d pull %d: dungeongen %d bodies, planlint %d", f.Leg, f.Pull, f.Bodies, byRow[f.Row].Bodies)
		}
	}
}
