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
func (e *engine) serverPath(Vec3, Vec3) (uint32, []Vec3, Vec3, float32, float32) {
	return 0, nil, Vec3{}, 0, 0
}
