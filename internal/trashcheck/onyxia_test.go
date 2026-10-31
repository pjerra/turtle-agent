package trashcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The real Onyxia data: the deployed 130000 rows and C0's map 249 export. The four patrolling warders walk loops the route
// crosses, each is pulled at its own pull step, and the loop of 52050 reaches the entrance (step 1, the revive point), which
// is walk 4's revive incident.
func TestOnyxiaWardersArePatrolsCrossingTheRoute(t *testing.T) {
	const spawns = "../../tools/dungeongen/testdata/map249"
	if _, err := os.Stat(filepath.Join(spawns, "movement.tsv")); err != nil {
		t.Skipf("no C0 export: %v", err)
	}
	sql, err := os.ReadFile("../planexport/testdata/onyxias_lair-20261029130000.sql")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := planexport.ParseMigration(string(sql))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d plans, want 1", len(rows))
	}
	w, _, err := worldspawn.Load(spawns, 249)
	if err != nil {
		t.Fatal(err)
	}
	rep := Check(rows[0], w, Options{Level: 60, Spread: 10, AggroRate: 1, AssistYd: 10})

	// Four loops, each pulled at its own pull step: 52050 at step 2, 52049 at 17, 52052 at 21, 52048 at 23; 52053 does not
	// patrol and is pulled at 25.
	//
	// The periods are worldspawn's (spec 5.2: the loop at 2.5*speed_walk yd/s, the warders' speed_walk 0.888888). Spec 5.3
	// lists "~32/44/64/39 s: the prep's numbers", which are the same loops at 2.5 yd/s with speed_walk left out of the
	// divisor (11 % short); C1's formula is the one the two tools share, so the test pins it.
	want := map[string]struct {
		period float32
		target int
	}{
		"52048": {44.24, 23},
		"52049": {49.74, 17},
		"52050": {36.21, 2},
		"52052": {71.49, 21},
	}
	seen := map[string]bool{}
	for _, p := range rep.Patrols {
		c, ok := want[p.ID]
		if !ok {
			continue
		}
		seen[p.ID] = true
		if !p.Estimate {
			t.Errorf("patrol %s: estimate false, a DB loop's phase was never recorded", p.ID)
		}
		if p.PeriodS < c.period-0.3 || p.PeriodS > c.period+0.3 {
			t.Errorf("patrol %s: period %.2f s, want %.2f s (loop at 2.5*speed_walk)", p.ID, p.PeriodS, c.period)
		}
		if p.TargetedAt == nil || *p.TargetedAt != c.target {
			t.Errorf("patrol %s: targeted %v, want step %d", p.ID, p.TargetedAt, c.target)
		}
		if p.Points < 2 || p.LengthYd <= 0 {
			t.Errorf("patrol %s: %d points, %.1f yd loop", p.ID, p.Points, p.LengthYd)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("patrol %s missing from the report: %+v", id, rep.Patrols)
		}
	}

	// The start has a window: 52050's loop reaches the entrance (7.6 yd from step 1's point), so the revive point of spec
	// 3.5 is not quiet at every phase of that loop.
	first := segTo(t, rep, 2)
	if len(first.Patrols) != 1 || first.Patrols[0].ID != "52050" {
		t.Fatalf("step 1->2 patrols %+v, want 52050's window", first.Patrols)
	}
	if first.Patrols[0].At != "start" || len(first.Patrols[0].Windows) == 0 {
		t.Errorf("step 1->2: at %q windows %+v, want a window at the start stand", first.Patrols[0].At, first.Patrols[0].Windows)
	}
	if first.Verdict != VerdictOwn {
		t.Errorf("step 1->2: verdict %q, want own (the step 2 pull is 52050's)", first.Verdict)
	}

	// Every warder and the boss are pulled at their own row: walk 3's run was clear of body pulls.
	if rep.Counts.Danger != 0 {
		var bad []string
		for _, f := range rep.Findings() {
			if f.Class == VerdictDanger {
				bad = append(bad, f.Text)
			}
		}
		t.Errorf("%d danger segments on the cleared plan:\n%s", rep.Counts.Danger, strings.Join(bad, "\n"))
	}
	if got := rep.Counts.Clear + rep.Counts.Own + rep.Counts.Patrol + rep.Counts.Danger; got != len(rep.Segments) {
		t.Errorf("counts %+v sum to %d, want %d segments", rep.Counts, got, len(rep.Segments))
	}
}
