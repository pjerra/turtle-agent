package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// talentRoster is an onyxia roster whose first rest batch is one hunter with the given spec word.
func talentRoster(t *testing.T, f *fixture, spec string) {
	t.Helper()
	roster := `{"format":"tb-raid-roster","version":2,"level":60,"tier":2,
 "batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection"},
            {"role":"rest","hire":"1 hunter","spec":"` + spec + `"},
            {"role":"rest","hire":"18d"}]}`
	path := filepath.Join(t.TempDir(), "roster.json")
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	f.opts.hires = path
}

func runLog(t *testing.T, f *fixture) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (a *walkFakeAgent) talentOrder() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.talentLines...)
}

// The send: a hunter batch's spec goes out as `.bot command <bot> talents <path>` with TB's premade path name, then a bare
// `talents` read-back, both before the `.bot role` of the same bot; "sv" is the alias of survival.
func TestRaidWalkSendsTheHunterSpecAsATalentsCommand(t *testing.T) {
	for spec, want := range map[string]string{"mm": "marksmanship", "bm": "beastmastery", "sv": "survival"} {
		f := newFixture(t)
		talentRoster(t, f, spec)
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("spec %s: exit %d, want 0: %s", spec, code, f.stderr.String())
		}
		got := f.agent.talentOrder()
		// Bot02 is the hunter (Bot01 the warrior).
		wantLines := []string{".bot command Bot02 talents " + want, ".bot command Bot02 talents"}
		if len(got) != 2 || got[0] != wantLines[0] || got[1] != wantLines[1] {
			t.Fatalf("spec %s: talent lines %q, want %q", spec, got, wantLines)
		}
		lines := f.agent.gmLines()
		apply, role := -1, -1
		for i, l := range lines {
			if l == wantLines[0] {
				apply = i
			}
			if strings.HasPrefix(l, ".bot role Bot02 ") {
				role = i
			}
		}
		if apply < 0 || role < 0 || apply > role {
			t.Fatalf("spec %s: the talents (%d) must go before the role (%d): %q", spec, apply, role, lines)
		}
	}
}

// Verify ok: the read-back puts the points in the spec's tree, so the run.log says so and nothing is reported as not applied.
func TestRaidWalkHunterSpecVerifiedFromTheTalentReadBack(t *testing.T) {
	f := newFixture(t)
	talentRoster(t, f, "mm")
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	log := runLog(t, f)
	if !strings.Contains(log, "roster: Bot02 hunter mm talents 9/42/0") {
		t.Errorf("run.log lacks the verified talents line:\n%s", log)
	}
	if strings.Contains(log, "not applied") {
		t.Errorf("run.log reports a spec as not applied:\n%s", log)
	}
}

// Verify fail: TB answers but the build stays Survival (a read-back 8/0/43 for an mm spec). The walk logs the reason and
// carries on to the role, the refit and the end of the walk (exit 0).
func TestRaidWalkHunterSpecThatDoesNotTakeIsLoggedAndTheWalkGoesOn(t *testing.T) {
	cases := map[string]string{
		"stuck":    "hire: spec mm not applied to Bot02 (talents read back 8/0/43, want tree 2 on top)",
		"silent":   "hire: spec mm not applied to Bot02 (no talent read-back in 15s)",
		"multiple": "hire: spec mm not applied to Bot02 (TB answered: Found multiple specs",
	}
	for mode, want := range cases {
		f := newFixture(t)
		f.agent.talentMode = mode
		talentRoster(t, f, "mm")
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("%s: exit %d, want 0 (the walk carries on): %s", mode, code, f.stderr.String())
		}
		if log := runLog(t, f); !strings.Contains(log, want) {
			t.Errorf("%s: run.log lacks %q:\n%s", mode, want, log)
		}
		if got := f.agent.roleOrder(); len(got) != 2 || got[1] != ".bot role Bot02 mm" {
			t.Errorf("%s: role lines %q, the hunter's role must still go out", mode, got)
		}
	}
}
