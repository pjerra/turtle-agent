//go:build !cgo

package navmesh

import "testing"

func TestRaycastWithoutCgoFails(t *testing.T) {
	p := NewPlanner(t.TempDir())
	defer p.Close()
	if _, err := p.Raycast(1, Vec3{}, Vec3{1, 1, 0}); err == nil {
		t.Fatal("no engine, no error")
	}
}
