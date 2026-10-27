package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// The stage-check command needs the real navmesh: like zulfarrakMmaps it
// looks in TURTLE_AGENT_MMAPS, ~/tb-ci/mmaps-turtle209 and the server
// host's folder, and this file's own finder also looks in
// ~/turtle-agent-mmaps.
func stageCheckMmaps() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle209"),
		filepath.Join(home, "tortoise-wow-server", "data", "mmaps"), filepath.Join(home, "turtle-agent-mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "209.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, "209*.mmtile")); len(tiles) > 0 {
			return d
		}
	}
	return ""
}

// 29. No args: exit 2 and the usage line.
func TestStageCheckUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"zulfarrak"}, {"zulfarrak", "gahzrilla"}} {
		var out, errOut bytes.Buffer
		if code := stageCheckCmd(args, &out, &errOut); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
			continue
		}
		if !strings.Contains(errOut.String(), "usage: turtle-agent stage-check") {
			t.Errorf("%v: no usage line: %q", args, errOut.String())
		}
	}
}

// 27. The G1 case on the real mesh: gahzrilla refuses in a fresh instance,
// naming the leg 8 boss, proposing velratha_b.
func TestStageCheckZulfarrakGahzrillaRefused(t *testing.T) {
	dir := stageCheckMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	var out, errOut bytes.Buffer
	args := []string{"--dir", "../../dungeons", "--mmaps", dir, "zulfarrak", "gahzrilla"}
	if code := stageCheckCmd(args, &out, &errOut); code != exitStageRefused {
		t.Fatalf("exit %d, want %d (%s) (%s)", code, exitStageRefused, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"cannot be staged in a fresh instance",
		"the boss of leg 8 velratha_b (entry 7795)",
		"proposal: earlier_leg leg 8 velratha_b",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in %q", want, text)
		}
	}
}

// 28. Velratha_b on the real mesh: exit 0 or 13. When 13 the refusal is NOT
// R1/R2 (the outcome is reported, not pinned: the lead decides whether to
// pin it).
func TestStageCheckZulfarrakVelrathaB(t *testing.T) {
	dir := stageCheckMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	var out, errOut bytes.Buffer
	args := []string{"--dir", "../../dungeons", "--mmaps", dir, "zulfarrak", "velratha_b"}
	code := stageCheckCmd(args, &out, &errOut)
	t.Logf("velratha_b: exit %d: %s%s", code, out.String(), errOut.String())
	if code != exitOK && code != exitStageRefused {
		t.Fatalf("exit %d, want %d or %d", code, exitOK, exitStageRefused)
	}
	if code == exitStageRefused {
		text := out.String()
		if strings.Contains(text, "cannot be staged in a fresh instance") || strings.Contains(text, "does not reach its next point") {
			t.Fatalf("velratha_b refused R1/R2: %s", text)
		}
	}
}

func zulfarrakOptionalPlan(t *testing.T) *dungeon.Plan {
	t.Helper()
	plan, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func optionalLegIndex(plan *dungeon.Plan) int {
	for i, l := range plan.Legs {
		if l.Optional != nil {
			return i
		}
	}
	return -1
}

func stageFirstRowNote(t *testing.T, plan *dungeon.Plan, leg int) (string, bool) {
	if leg < 0 {
		return "", false
	}
	p := navmesh.NewPlanner(t.TempDir())
	defer p.Close()
	_, _, note := stageFirstRow(plan, leg, p, 0)
	if !strings.Contains(note, "is optional") {
		return note, false
	}
	return note, true
}

// 30. A plan with an optional leg: stageFirstRow falls back with the
// optional note.
func TestStageFirstRowFallsBackForAnOptionalLeg(t *testing.T) {
	plan := zulfarrakOptionalPlan(t)
	if _, ok := stageFirstRowNote(t, plan, optionalLegIndex(plan)); !ok {
		t.Fatal("want the optional fallback")
	}
}
