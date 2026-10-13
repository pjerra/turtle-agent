package session

import (
	"math"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/objects"
)

// Raycaster answers walkability rays and nearest-point queries on the navmesh (navmesh.Planner is one). A Planner that is one
// gives the brain its line of sight and its standing spots; one that is not (a test's fake) gives it none.
type Raycaster interface {
	Raycast(mapID uint32, from, to navmesh.Vec3) (navmesh.Ray, error)
	Nearest(mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error)
}

var _ Raycaster = (*navmesh.Planner)(nil)

// rayEndSlack is how far short of the end a ray may stop and still count as reaching it: a unit stands on the edge of the mesh
// as often as inside it, and its body is more than a point.
const rayEndSlack = 1.5

func (s *Session) raycaster() Raycaster {
	if s.opts.Planner == nil {
		return nil
	}
	r, _ := s.opts.Planner.(Raycaster)
	return r
}

// LineClear tells whether the straight line between two points of the player's map stays on the navmesh (see
// navmesh.Planner.Raycast: the agent's stand-in for line of sight, and for a straight walk being possible). known is false when
// there is no navmesh, no tile or the start is off the mesh; the caller then should not act on the answer.
func (s *Session) LineClear(a, b [3]float32) (clear, known bool) {
	r := s.raycaster()
	if r == nil {
		return false, false
	}
	m, _ := s.where()
	ray, err := r.Raycast(m, a, b)
	if err != nil || !ray.Known {
		return false, false
	}
	if ray.Clear {
		return true, true
	}
	d := math.Hypot(float64(b[0]-a[0]), float64(b[1]-a[1]))
	return ray.Hit >= float32(d)-rayEndSlack, true
}

// SnapToMesh is the walkable point of the navmesh nearest to p (within a few yards); false when there is none.
func (s *Session) SnapToMesh(p [3]float32) ([3]float32, bool) {
	r := s.raycaster()
	if r == nil {
		return p, false
	}
	m, _ := s.where()
	out, ok, err := r.Nearest(m, p)
	if err != nil || !ok {
		return p, false
	}
	return out, true
}

// DynamicObjects lists the ground areas in view (Blizzard, fire patches), nearest first.
func (s *Session) DynamicObjects() []objects.DynamicObject {
	x, y, _, _ := s.selfPos()
	return s.store.DynamicObjects(x, y)
}
