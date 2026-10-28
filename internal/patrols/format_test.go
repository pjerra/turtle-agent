package patrols

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func sampleFile() *File {
	return &File{
		Format: Format, Version: Version, Map: 249, Source: "record",
		Created: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC),
		Area:    Area{X: -100, Y: -150, R: 120}, Minutes: 15, SampleMS: 1000,
		Patrols: []Patrol{{
			ID: "52050", GUIDs: []string{"0xF130002F61000ABC"}, SpawnGUIDs: []uint32{52050},
			Entries: []uint32{12129}, Name: "Onyxian Warder",
			Path:    []dungeon.Point{{X: 49.8, Y: -114.6, Z: -24.3}, {X: 60, Y: -114.6, Z: -24.3}, {X: 60, Y: -100, Z: -24.3}},
			WaitsMS: []int{0, 2000, 0}, PeriodS: 33.4, PeriodEstimate: false, CyclesSeen: 3,
			PhaseEpochMS: 1793223608123, Complete: true, Samples: 900, Source: "record m910q 2026-10-29",
		}},
		Static:    []Static{{GUID: "0xF130000000000002", Entry: 12129, X: 1, Y: 2, Z: 3}},
		Wanderers: []Wanderer{{GUID: "0xF130000000000003", Entry: 12129, Center: dungeon.Point{X: 1, Y: 2, Z: 3}, Radius: 4.2}},
	}
}

func TestFileRoundTripAndRefusals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patrols.json")
	want := sampleFile()
	if err := Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode %04o, want 0600", mode)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip differs:\ngot  %+v\nwant %+v", got, want)
	}
	if err := Write(path, want); err == nil {
		t.Error("Write overwrote an existing file")
	}

	d := got.Patrols[0].ToDungeon()
	if d.PeriodS != want.Patrols[0].PeriodS || d.PeriodEstimate || !reflect.DeepEqual(d.Entries, []uint32{12129}) ||
		!reflect.DeepEqual(d.Path, want.Patrols[0].Path) || d.Source != want.Patrols[0].Source {
		t.Errorf("ToDungeon: %+v", d)
	}

	raw := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name string
		body string
	}{
		{"format", `{"format":"tb-other","version":1}`},
		{"version", `{"format":"tb-patrols","version":2}`},
		{"shortpath", `{"format":"tb-patrols","version":1,"patrols":[{"id":"1","path":[{"x":0,"y":0,"z":0}],"period_s":10}]}`},
		{"zeroperiod", `{"format":"tb-patrols","version":1,"patrols":[{"id":"1","path":[{"x":0,"y":0,"z":0},{"x":1,"y":0,"z":0}],"period_s":0}]}`},
		{"longperiod", `{"format":"tb-patrols","version":1,"patrols":[{"id":"1","path":[{"x":0,"y":0,"z":0},{"x":1,"y":0,"z":0}],"period_s":901}]}`},
	}
	for _, c := range cases {
		if _, err := Read(raw(c.name+".json", c.body)); err == nil {
			t.Errorf("%s: Read accepted %s", c.name, c.body)
		}
	}
}
