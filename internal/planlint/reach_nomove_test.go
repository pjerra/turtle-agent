package planlint

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// pathNav is fakeNav plus the server's own query: every hop answers NOPATH, as the server does for a goal it cannot path to.
type pathNav struct{ fakeNav }

func (pathNav) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	return navmesh.ServerPath{Type: navmesh.PathNoPath, Points: []navmesh.Vec3{from}, ActualEnd: from}, nil
}

func (pathNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}}, nil
}

// A row that moves nobody has its point for the module's own use — the party already stands there — so the reach lint must not
// ask the server to path to it. Route audit 2026-11-12, finding 4: 15 rows (wait_object, wait_clear, hold, gossip, loot,
// use_item) were flagged NOPATH; none of them is a hop.
func TestReachSkipsARowThatMovesNobody(t *testing.T) {
	for _, kind := range []string{planexport.KindWaitObject, planexport.KindWaitClear, planexport.KindLoot,
		planexport.KindUseItem, planexport.KindGossip, planexport.KindHold} {
		row := planexport.StepRow{Kind: kind, X: 50, Y: 0, Z: 0, Entry: 1}
		for _, f := range lintOne(t, rowsOf(move(0, 0, 1), row), pathNav{}, nil) {
			if f.Lint == LintReach {
				t.Errorf("%s row: %s", kind, f.String())
			}
		}
	}
	// A move row at the same point is a hop, and the server cannot reach it: the finding stays.
	var reach []string
	for _, f := range lintOne(t, rowsOf(move(0, 0, 1), move(50, 0, 0)), pathNav{}, nil) {
		if f.Lint == LintReach {
			reach = append(reach, f.String())
		}
	}
	if len(reach) != 1 || !strings.Contains(reach[0], "server NOPATH") {
		t.Fatalf("a move row the server cannot reach got %v, want one server NOPATH", reach)
	}
	// Only the six kinds the audit saw are skipped: a rest row's point is still judged.
	rest := planexport.StepRow{Kind: planexport.KindRest, X: 50, Y: 0, Z: 0, TimeoutS: 30}
	reach = nil
	for _, f := range lintOne(t, rowsOf(move(0, 0, 1), rest), pathNav{}, nil) {
		if f.Lint == LintReach {
			reach = append(reach, f.String())
		}
	}
	if len(reach) != 1 {
		t.Fatalf("a rest row got %v, want the reach finding kept", reach)
	}
}
