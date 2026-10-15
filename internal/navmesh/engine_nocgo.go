//go:build !cgo

package navmesh

const engineName = "none"

// engine is the stand-in of a build without cgo: no map loads, so every plan fails with
// ErrNoEngine.
type engine struct{}

func newEngine([]byte) (*engine, error)         { return nil, ErrNoEngine }
func (*engine) close()                          {}
func (*engine) addTile([]byte) (uint64, error)  { return 0, ErrNoEngine }
func (*engine) removeTile(uint64) error         { return ErrNoEngine }
func (*engine) nearest(Vec3, Vec3) (Vec3, bool) { return Vec3{}, false }
func (*engine) floors(float32, float32, float32, float32) ([]Floor, error) {
	return nil, ErrNoEngine
}
func (*engine) polys([4]float32) ([]Polygon, error) { return nil, ErrNoEngine }
func (*engine) findPath(Vec3, Vec3) (int, []Vec3, Vec3) {
	return -3, nil, Vec3{}
}
func (*engine) raycast(Vec3, Vec3) (float32, int) { return 0, -3 }
func (*engine) markBox(Box, uint16) int           { return 0 }
