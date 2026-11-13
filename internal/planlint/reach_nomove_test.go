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

// A loot, use_item, wait_object or wait_clear row works where the party already stands, wherever its point is: the reach lint
// must not ask the server to path to it (route audit 2026-11-12, finding 4: these kinds were flagged NOPATH; none is a hop).
func TestReachSkipsALootOrWaitRowWhereverItStands(t *testing.T) {
	for _, kind := range []string{planexport.KindWaitObject, planexport.KindWaitClear, planexport.KindLoot, planexport.KindUseItem} {
		row := planexport.StepRow{Kind: kind, X: 50, Y: 0, Z: 0, Entry: 1}
		for _, f := range lintOne(t, rowsOf(move(0, 0, 1), row), pathNav{}, nil) {
			if f.Lint == LintReach {
				t.Errorf("%s row: %s", kind, f.String())
			}
		}
	}
}

// A hold or gossip row is skipped only when it stands on the spot the row before it reached: the audit's 15 rows are like
// that, and a hold the party must walk to is a hop the server judges.
func TestReachSkipsAHoldOrGossipOnlyOnTheSpot(t *testing.T) {
	for _, kind := range []string{planexport.KindHold, planexport.KindGossip} {
		var near []string
		for _, f := range lintOne(t, rowsOf(move(0, 0, 1), planexport.StepRow{Kind: kind, X: 1, Y: 0, Z: 1, Entry: 1}), pathNav{}, nil) {
			if f.Lint == LintReach {
				near = append(near, f.String())
			}
		}
		if len(near) != 0 {
			t.Errorf("a %s row on the party's spot got %v, want no reach finding", kind, near)
		}
		var far []string
		for _, f := range lintOne(t, rowsOf(move(0, 0, 1), planexport.StepRow{Kind: kind, X: 80, Y: 0, Z: 0, Entry: 1}), pathNav{}, nil) {
			if f.Lint == LintReach {
				far = append(far, f.String())
			}
		}
		if len(far) != 1 || !strings.Contains(far[0], "server NOPATH") {
			t.Errorf("a %s row 80 yd away got %v, want one server NOPATH", kind, far)
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
	// A rest row's point is still judged.
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
