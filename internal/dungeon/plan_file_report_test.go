package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// The run says which plan file it was loaded from and the sha256 of it, so the live dashboard draws exactly that file.
func TestReportCarriesThePlanFileAndItsHash(t *testing.T) {
	g := newFakeRemote(t)
	m := g.addMob(297, Point{40, 0, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{297}, Positions: []Point{m.pos}}}})
	var path string
	rep, err := tuneRun(t, g, plan, "", func(o *RemoteOptions) { path = o.PlanPath })
	if err != nil {
		t.Fatal(err)
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	sum := sha256.Sum256(data)
	if rep.Remote.PlanFile != path || filepath.Base(path) != "rt.json" || rep.Remote.PlanSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("file %q sha %q, want %q %x", rep.Remote.PlanFile, rep.Remote.PlanSHA256, path, sum)
	}
}

// A run made without a plan file reports none.
func TestReportHasNoPlanFileWithoutOne(t *testing.T) {
	g := newFakeRemote(t)
	m := g.addMob(297, Point{40, 0, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{297}, Positions: []Point{m.pos}}}})
	o := opts()
	d := NewRemoteDriver(plan, g, DefaultConfig(), o, g.clock())
	if r := d.Report(); r.Remote.PlanFile != "" || r.Remote.PlanSHA256 != "" {
		t.Fatalf("%+v", r.Remote)
	}
}
