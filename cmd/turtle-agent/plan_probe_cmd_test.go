package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planprobe"
)

// cmdFakeNav is plan-probe's navmesh in the command tests: a neutral answer unless a query is scripted.
type cmdFakeNav struct {
	floors func(x, y, lo, hi float32) ([]navmesh.Floor, error)
}

func (f cmdFakeNav) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	return navmesh.ServerPath{Type: navmesh.PathNormal, ActualEnd: to, Length: float32(dist3f(from, to))}, nil
}

func (f cmdFakeNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}, Length: float32(dist3f(from, to))}, nil
}

func (f cmdFakeNav) Floors(_ uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	if f.floors != nil {
		return f.floors(x, y, lo, hi)
	}
	return []navmesh.Floor{{Z: (lo + hi) / 2, Flags: navmesh.NavGround}}, nil
}

func (f cmdFakeNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	return p, true, nil
}

// useFakeProbeNav swaps the command's navmesh for one test.
func useFakeProbeNav(t *testing.T, nav planprobe.Nav, cut func(uint32, dungeon.Point, dungeon.Point) ([]dungeon.Point, error)) {
	t.Helper()
	old := planProbeOpen
	t.Cleanup(func() { planProbeOpen = old })
	planProbeOpen = func(string) *planProbeNavmesh {
		return &planProbeNavmesh{
			nav: nav,
			polys: func(uint32, float32, float32, float32, float32) ([]navmesh.Polygon, bool, error) {
				return nil, false, nil
			},
			cut:   cut,
			close: func() {},
		}
	}
}

func writeProbeMigration(t *testing.T, rows planexport.Rows) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.sql")
	if err := os.WriteFile(path, []byte(planexport.WriteSQL([]planexport.Rows{rows})), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPlanProbeCmdNeedsOneInput: exactly one input, and --plan only picks a plan inside --sql.
func TestPlanProbeCmdNeedsOneInput(t *testing.T) {
	useFakeProbeNav(t, cmdFakeNav{}, nil)
	cases := [][]string{
		{"--mmaps", "mm"},
		{"--mmaps", "mm", "one", "two"},
		{"--mmaps", "mm", "--plan", "one"},
		{"--mmaps", "mm", "--sql", "file.sql", "one"},
		{"--mmaps", "mm", "--from-step", "0", "one"},
	}
	for _, args := range cases {
		var out, errOut bytes.Buffer
		if code := planProbeCmd(args, &out, &errOut); code != exitUsage {
			t.Fatalf("%v: exit %d, want %d (stderr %q)", args, code, exitUsage, errOut.String())
		}
		if out.Len() != 0 {
			t.Fatalf("%v: printed %q", args, out.String())
		}
	}
}

// TestPlanProbeCmdExit20OnBad: walk 3's under-floor step 26 makes the command exit 20 and names the segment.
func TestPlanProbeCmdExit20OnBad(t *testing.T) {
	useFakeProbeNav(t, cmdFakeNav{floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		if math.Abs(float64(x+149.46112)) < 0.5 && math.Abs(float64(y+210.48434)) < 0.5 {
			return []navmesh.Floor{{Z: -66.7, Flags: navmesh.NavGround}}, nil
		}
		return []navmesh.Floor{{Z: (lo + hi) / 2, Flags: navmesh.NavGround}}, nil
	}}, nil)
	sql := filepath.Join("..", "..", "internal", "planexport", "testdata", "onyxias_lair-20261029120000.sql")
	var out, errOut bytes.Buffer
	code := planProbeCmd([]string{"--mmaps", "mm", "--sql", sql}, &out, &errOut)
	if code != exitPlanProbeBad {
		t.Fatalf("exit %d, want %d (stderr %q, stdout %q)", code, exitPlanProbeBad, errOut.String(), out.String())
	}
	text := out.String()
	for _, want := range []string{"step 25->26", "BAD", "1 bad"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, text)
		}
	}
}

// TestPlanProbeCmdJSONGolden pins the report's JSON (tb-plan-probe v1).
func TestPlanProbeCmdJSONGolden(t *testing.T) {
	useFakeProbeNav(t, cmdFakeNav{}, nil)
	sql := writeProbeMigration(t, planexport.Rows{
		Plan: planexport.PlanRow{Plan: "probe_fixture", Map: 249, Source: "test"},
		Legs: []planexport.LegRow{{Plan: "probe_fixture", Leg: 0, Name: "one"}},
		Steps: []planexport.StepRow{
			{Plan: "probe_fixture", Leg: 0, Seq: 0, Kind: planexport.KindMove, X: 10, Comment: "to"},
			{Plan: "probe_fixture", Leg: 0, Seq: 1, Kind: planexport.KindMove, X: 20, Comment: "path"},
		},
	})
	var out, errOut bytes.Buffer
	if code := planProbeCmd([]string{"--mmaps", "mm", "--json", "--sql", sql}, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitOK, errOut.String())
	}
	got := strings.ReplaceAll(out.String(), sql, "PROBE_SQL")
	want := `{
  "format": "tb-plan-probe",
  "version": 1,
  "plan": "probe_fixture",
  "map": 249,
  "source": "sql PROBE_SQL",
  "mmaps": "mm",
  "steps": 2,
  "segments": [
    {
      "from": {
        "step": 1,
        "leg": 0,
        "seq": 0,
        "kind": "move",
        "comment": "to",
        "x": 10,
        "y": 0,
        "z": 0
      },
      "to": {
        "step": 2,
        "leg": 0,
        "seq": 1,
        "kind": "move",
        "comment": "path",
        "x": 20,
        "y": 0,
        "z": 0
      },
      "straight": 10,
      "server": {
        "type": 1,
        "verdict": "ok",
        "length": 10,
        "end_short": 0,
        "start_poly": 0,
        "end_poly": 0
      },
      "offline": {
        "status": "complete",
        "length": 10
      },
      "ratio": 1,
      "point": {
        "floors": [
          {
            "z": 0,
            "flags": 1
          }
        ],
        "floor_dz": 0,
        "snap": 0,
        "floors_here": 1
      },
      "water_yd": 0,
      "lava_yd": 0,
      "max_climb": 0,
      "max_drop": 0,
      "steep": false,
      "floor_from": 0,
      "floor_to": 0,
      "hop": false,
      "verdict": "ok",
      "reasons": []
    }
  ],
  "notes": [],
  "counts": {
    "ok": 1,
    "warn": 0,
    "bad": 0
  }
}
`
	if got != want {
		t.Fatalf("report JSON differs:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
