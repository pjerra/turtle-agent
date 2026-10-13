package session

import (
	"errors"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

type rayPlanner struct {
	*fakePlanner
	ray navmesh.Ray
	err error
}

func (p rayPlanner) Raycast(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Ray, error) {
	return p.ray, p.err
}
func (p rayPlanner) Nearest(_ uint32, v navmesh.Vec3) (navmesh.Vec3, bool, error) {
	return navmesh.Vec3{v[0], v[1], 7}, p.ray.Known, p.err
}

func TestLineClearReadsTheMeshRay(t *testing.T) {
	a, b := [3]float32{0, 0, 0}, [3]float32{20, 0, 0}
	for _, c := range []struct {
		name         string
		p            Planner
		clear, known bool
	}{
		{"no planner", nil, false, false},
		{"a planner without rays", &fakePlanner{}, false, false},
		{"clear", rayPlanner{fakePlanner: &fakePlanner{}, ray: navmesh.Ray{Known: true, Clear: true}}, true, true},
		{"blocked halfway", rayPlanner{fakePlanner: &fakePlanner{}, ray: navmesh.Ray{Known: true, Hit: 10}}, false, true},
		{"stops at the target's body", rayPlanner{fakePlanner: &fakePlanner{}, ray: navmesh.Ray{Known: true, Hit: 19}}, true, true},
		{"unknown", rayPlanner{fakePlanner: &fakePlanner{}, ray: navmesh.Ray{}}, false, false},
		{"error", rayPlanner{fakePlanner: &fakePlanner{}, err: errors.New("no tiles")}, false, false},
	} {
		s := &Session{opts: Options{Planner: c.p}}
		clear, known := s.LineClear(a, b)
		if clear != c.clear || known != c.known {
			t.Errorf("%s: clear %v known %v, want %v %v", c.name, clear, known, c.clear, c.known)
		}
	}
	s := &Session{opts: Options{Planner: rayPlanner{fakePlanner: &fakePlanner{}, ray: navmesh.Ray{Known: true}}}}
	if p, ok := s.SnapToMesh([3]float32{1, 2, 3}); !ok || p[2] != 7 {
		t.Fatalf("%v %v", p, ok)
	}
	if _, ok := (&Session{}).SnapToMesh([3]float32{}); ok {
		t.Fatal("snapped without a planner")
	}
}
