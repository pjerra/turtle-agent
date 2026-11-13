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
	if sum := f.summary(); len(sum.SpecFailed) != 0 || strings.Contains(f.stdout.String(), "WARN") {
		t.Errorf("a verified spec is reported as failed: %+v", sum.SpecFailed)
	}
}

// Verify fail: TB answers but the build stays Survival (a read-back 8/0/43 for an mm spec). The walk logs the reason and
// carries on to the role, the refit and the end of the walk (exit 0).
func TestRaidWalkHunterSpecThatDoesNotTakeIsLoggedAndTheWalkGoesOn(t *testing.T) {
	cases := map[string]string{
		"stuck":    "hire: spec mm not applied to Bot02 (talents read back 8/0/43, want tree 2 on top)",
		"silent":   "hire: spec mm not applied to Bot02 (no talent read-back in 15s)",
		"multiple": "hire: spec mm not applied to Bot02 (apply refused: Found multiple specs",
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
		// The failure is in the walk's result too, not only run.log; the exit code stays 0.
		sum := f.summary()
		if len(sum.SpecFailed) != 1 || sum.SpecFailed[0].Bot != "Bot02" || sum.SpecFailed[0].Spec != "mm" || sum.SpecFailed[0].Reason == "" {
			t.Errorf("%s: summary spec_failed %+v, want one entry for Bot02 mm", mode, sum.SpecFailed)
		}
		if txt := f.summaryText(); !strings.Contains(txt, "WARN: hunter spec not applied: 1 (Bot02)") {
			t.Errorf("%s: summary.txt lacks the WARN line:\n%s", mode, txt)
		}
		if !strings.Contains(f.stdout.String(), "WARN: hunter spec not applied: 1 (Bot02)") {
			t.Errorf("%s: stdout lacks the WARN line:\n%s", mode, f.stdout.String())
		}
	}
}

// The apply is refused ("talent link is invalid") while the hunter already stands in the requested tree: it is still not applied.
// Today's code did not know that refusal, read the (41/10/0) back and certified bm. The refusal is in the summary too.
func TestRaidWalkHunterSpecApplyRefusedIsNotAppliedEvenIfTheReadBackLooksRight(t *testing.T) {
	f := newFixture(t)
	f.agent.talentMode = "refused"
	f.agent.talentTrees = map[string][3]int{"Bot02": {41, 10, 0}}
	talentRoster(t, f, "bm")
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	log := runLog(t, f)
	if !strings.Contains(log, "hire: spec bm not applied to Bot02 (apply refused: Apply talents [beastmastery] talent link is invalid)") {
		t.Errorf("run.log lacks the refusal:\n%s", log)
	}
	if strings.Contains(log, "roster: Bot02 hunter bm talents") {
		t.Errorf("run.log certifies a refused build:\n%s", log)
	}
	if sum := f.summary(); len(sum.SpecFailed) != 1 || !strings.Contains(sum.SpecFailed[0].Reason, "apply refused") {
		t.Errorf("summary spec_failed %+v", sum.SpecFailed)
	}
}

// The other refusals: not your bot, no AI yet.
func TestRaidWalkTalentRefusalWords(t *testing.T) {
	for _, s := range []string{"Found multiple specs: a, b", "talents are invalid. Needs atleast one number.", "Apply talents [x] talent link is invalid",
		"You may only command an online bot on your account.", "Bot Bot02 has no AI yet."} {
		if !talentRefused(s) {
			t.Errorf("%q is not read as a refusal", s)
		}
	}
	if talentRefused("Apply spec |h|cffffffffmarksmanship 9/42/0") {
		t.Error("an apply answer is not a refusal")
	}
}

// L4: TB's own colouring of the read-back, as the module writes it, parses.
func TestRaidWalkHunterSpecReadBackInTBsColouredText(t *testing.T) {
	f := newFixture(t)
	f.agent.talentMode = "tbtext"
	talentRoster(t, f, "bm")
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	if log := runLog(t, f); !strings.Contains(log, "roster: Bot02 hunter bm talents 45/6/0") || strings.Contains(log, "not applied") {
		t.Errorf("the coloured read-back was not accepted:\n%s", log)
	}
}

// L5: the read-back counts only from the bot's own chat or whisper. Another bot's whisper and a system line (the forwarding ack, or a
// reply that came as a system message) are ignored, so the spec reads as unverified.
func TestRaidWalkHunterSpecReadBackOnlyFromThatBotsChat(t *testing.T) {
	for _, mode := range []string{"otherbot", "systemonly"} {
		f := newFixture(t)
		f.agent.talentMode = mode
		talentRoster(t, f, "mm")
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("%s: exit %d, want 0: %s", mode, code, f.stderr.String())
		}
		if log := runLog(t, f); !strings.Contains(log, "hire: spec mm not applied to Bot02 (no talent read-back in 15s)") {
			t.Errorf("%s: the read-back from outside the bot's chat was accepted:\n%s", mode, log)
		}
	}
}

// M3: a line queued behind the talent read-back in the same events page is not lost: here the game's answer to the next command
// (`.bot role`) came in that page, and the role wait finds it instead of failing after 30 s with "no answer".
func TestRaidWalkTalentWaitKeepsTheRestOfTheEventPage(t *testing.T) {
	f := newFixture(t)
	f.agent.pageSize = 10
	f.agent.roleSilentFor = "Bot02" // the hunter's answer is the line queued behind the read-back
	f.agent.talentReadExtra = "Bot Bot02 role set to dps."
	talentRoster(t, f, "mm")
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (the role answer sat behind the read-back): %s\n%s\n%s", code, f.stderr.String(), f.summary().EndReason, runLog(t, f))
	}
}
