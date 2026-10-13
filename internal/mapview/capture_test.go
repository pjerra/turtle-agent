package mapview

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const fixtures = "../../testdata/captures/"

func sceneOfCapture(t *testing.T, name string) (*Scene, CaptureInfo) {
	t.Helper()
	cfg := DefaultConfig()
	b := NewBuilder(cfg)
	evs, info, err := AddCapture(b, filepath.Join(fixtures, name), cfg)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := NewScene(b.Build(evs, nil), SceneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return sc, info
}

// m6-fight.twcap: a live fight in the open (Tagentone, 7.7 minutes, wolves and a kill).
func TestGoldenSVGOfTheFightCapture(t *testing.T) {
	sc, info := sceneOfCapture(t, "m6-fight.twcap")
	if info.Character != "Tagentone" || info.Records != 2756 || info.Frames < 300 || info.Warning != "" {
		t.Fatalf("%+v", info)
	}
	run := sc.Run
	if run.Map != 1 || len(run.Tracks) < 2 || run.Tracks[0].Name != "Tagentone" || !run.Tracks[0].Leader || len(run.Creatures) < 50 {
		t.Fatalf("map %d, %d tracks, %d creatures", run.Map, len(run.Tracks), len(run.Creatures))
	}
	killed, pulls := 0, 0
	for _, c := range run.Creatures {
		if c.Fate == FateKilled {
			killed++
		}
	}
	for _, m := range run.Markers {
		if m.Kind == MarkPull {
			pulls++
		}
	}
	if killed != 1 || pulls < 1 {
		t.Fatalf("%d killed, %d pulls", killed, pulls)
	}
	svg := sc.SVG(0)
	wellFormed(t, svg)
	golden(t, "m6-fight.golden.svg", svg)
	again, _ := sceneOfCapture(t, "m6-fight.twcap")
	if !bytes.Equal(svg, again.SVG(0)) {
		t.Fatal("two replays of one capture drew different images")
	}
}

// m7-gameobjects.twcap: a login with game object changes (its record times are all within a millisecond).
func TestGoldenSVGOfTheGameObjectCapture(t *testing.T) {
	sc, info := sceneOfCapture(t, "m7-gameobjects.twcap")
	if info.Character != "Harnessc" || info.Records != 526 {
		t.Fatalf("%+v", info)
	}
	svg := sc.SVG(0)
	wellFormed(t, svg)
	golden(t, "m7-gameobjects.golden.svg", svg)
	html, err := sc.HTML()
	if err != nil || len(html) < 5000 {
		t.Fatalf("%v %d", err, len(html))
	}
}

func TestACutOffCaptureStillDraws(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "m6-fight.twcap"))
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(t.TempDir(), "cut.twcap")
	if err := os.WriteFile(cut, raw[:len(raw)-7], 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	_, info, err := AddCapture(NewBuilder(cfg), cut, cfg)
	if err != nil || info.Warning == "" || info.Frames == 0 {
		t.Fatalf("%v %+v", err, info)
	}
	if _, _, err := AddCapture(NewBuilder(cfg), filepath.Join(t.TempDir(), "missing"), cfg); err == nil {
		t.Fatal("a missing capture read")
	}
}
