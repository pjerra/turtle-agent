package raidwalk

import (
	"strings"
	"testing"
	"time"
)

var autoNow = time.Date(2026, 11, 4, 21, 0, 0, 0, time.UTC)

// walk37Targets is a targets file in the shape the autopilot documents.
const walk37Targets = `# ZG walk 37: the raids as the worker drove them by hand
a tortoise-test2 1 zg37-a full -- zul_gurub --dir /plans --hires /roster-best.json --sql /zg.sql
b tortoise-test2 2 zg37-b boss:14515 -- zul_gurub --dir /plans --hires /roster.json --sql /zg.sql --from step 405
c tortoise-test2 5 zg37-c boss:14507 -- zul_gurub --dir /plans --hires /roster-guide.json --sql /zg.sql
`

func TestParseAutoTargets(t *testing.T) {
	ts, err := ParseAutoTargets(strings.NewReader(walk37Targets))
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 3 {
		t.Fatalf("%d targets, want 3", len(ts))
	}
	if ts[0].ID != "a" || ts[0].Goal.String() != "full" || ts[0].Start != "" {
		t.Fatalf("a = %+v", ts[0])
	}
	if ts[1].Goal.Kind != "boss" || ts[1].Goal.Boss != 14515 || ts[1].Start != "step 405" {
		t.Fatalf("b = %+v, goal %+v", ts[1], ts[1].Goal)
	}
	// The managed --from is not in the args; the goal's own start is.
	if strings.Contains(strings.Join(ts[1].Args, " "), "--from") {
		t.Fatalf("--from leaked into the args: %v", ts[1].Args)
	}
	if ts[2].Goal.Until() != "boss-done" {
		t.Fatalf("boss until = %q", ts[2].Goal.Until())
	}
	if ts[0].Goal.Until() != "none" || (AutoGoal{Kind: "leg", Leg: "3"}).Until() != "leg 3" {
		t.Fatal("until clauses wrong")
	}
}

func TestParseAutoTargetsRefusals(t *testing.T) {
	cases := map[string]string{
		"slot 3":       "a test2 3 h full -- zg",
		"unsafe":       "a test2 1 h full -- zg --instance-cap-unsafe",
		"no separator": "a test2 1 h full zg",
		"bad goal":     "a test2 1 h boss -- zg",
		"no plan":      "a test2 1 h full -- --dir /plans",
		"bad from":     "a test2 1 h full -- zg --from 42",
		"bad slot":     "a test2 x h full -- zg",
		"no goals":     "# only a comment",
	}
	for what, text := range cases {
		if _, err := ParseAutoTargets(strings.NewReader(text)); err == nil {
			t.Errorf("%s: parsed, want an error", what)
		}
	}
}

func bossRun(entry uint32, killed bool) AutoObservation {
	return AutoObservation{
		Exited:       true,
		ExitCode:     0,
		StepsReached: 417,
		StepsTotal:   761,
		EndReason:    "stopped: stopped by .bot run stop",
		KilledBosses: map[uint32]bool{entry: killed},
	}
}

// Walk 37 b3-r1: Arlokk was never summoned, the walk's own re-run of the gong ended in its run stop and exit 0. The
// autopilot must re-place, not call the raid complete.
func TestAutoExitZeroAfterRunStopRestarts(t *testing.T) {
	o := bossRun(14515, false)
	d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 14515}, Reuse: true, HasRun: true}, autoNow, 15*time.Minute)
	if d.Action != AutoRestart || !d.Reuse || !strings.Contains(d.Reason, "goal is not met") {
		t.Fatalf("decision %+v, want a reuse restart", d)
	}
	if GoalAchieved(AutoGoal{Kind: "boss", Boss: 14515}, o) {
		t.Fatal("an exit-0 run stop met the boss goal")
	}
}

// Walk 37 e: three supervisor restarts used, the run ended again at step 552. A used-up restart budget is still an
// end, and the autopilot re-places.
func TestAutoRestartsExhaustedEndStillRestarts(t *testing.T) {
	o := AutoObservation{
		Exited: true, ExitCode: 19, StepsReached: 552, StepsTotal: 761,
		EndReason: "wipe at step 552", KilledBosses: map[uint32]bool{},
	}
	d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "full"}, Attempt: 3, Reuse: true, HasRun: true}, autoNow, 15*time.Minute)
	if d.Action != AutoRestart || !d.Reuse || !strings.Contains(d.Reason, "wipe at step 552") {
		t.Fatalf("decision %+v, want a reuse restart", d)
	}
}

// Walk 37 b2: a 65-minute no-progress fight (no step, no boss HP change) must be caught at the 15-minute stall, not
// waited out.
func TestAutoStallAtFifteenMinutes(t *testing.T) {
	o := AutoObservation{
		Exited: false, StepsReached: 417, StepsTotal: 761,
		LastProgress: autoNow.Add(-65 * time.Minute),
	}
	d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 14515}, Reuse: true, HasRun: true}, autoNow, 15*time.Minute)
	if d.Action != AutoRestart || !strings.Contains(d.Reason, "stalled") {
		t.Fatalf("decision %+v, want a stall restart", d)
	}
	// A run that showed progress 14 minutes ago keeps running.
	o.LastProgress = autoNow.Add(-14 * time.Minute)
	if d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 14515}, Reuse: true}, autoNow, 15*time.Minute); d.Action != AutoContinue {
		t.Fatalf("decision %+v, want continue", d)
	}
}

// Walk 37 e3: eight failed placements, all "not at the start", burned fresh instances. After a reuse attempt failed,
// the next attempt asks for a fresh instance; a fresh one that fails backs off instead of hammering the server.
func TestAutoFailedPlacementSwitchesToFresh(t *testing.T) {
	o := AutoObservation{
		Exited: true, ExitCode: 18, StepsReached: 0,
		EndReason: "place: not at the start: Quibblee, Grimvale, Prayie, Taelis, Fireblade",
	}
	d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 11380}, Reuse: true, HasRun: true, FastFailures: 1}, autoNow, 15*time.Minute)
	if d.Action != AutoRestart || d.Reuse {
		t.Fatalf("decision %+v, want a restart without --reuse-instance", d)
	}
	if d.Backoff != 0 {
		t.Fatalf("first failed fresh attempt backoff = %s, want 0 (the switch itself is the next try)", d.Backoff)
	}
	// The fresh attempt fails too: back off.
	d = DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 11380}, Reuse: false, HasRun: true, FastFailures: 2}, autoNow, 15*time.Minute)
	if d.Action != AutoRestart || d.Reuse || d.Backoff != 80*time.Second {
		t.Fatalf("decision %+v, want a fresh restart with an 80 s backoff", d)
	}
}

// Walk 37 d3: Jeklik and Venoxis went down. A boss goal advances only on that boss.
func TestAutoBossKillAdvances(t *testing.T) {
	o := AutoObservation{
		Exited: true, ExitCode: 0, StepsReached: 193, StepsTotal: 761,
		EndReason:    "boss down",
		KilledBosses: map[uint32]bool{14517: true, 14507: true},
	}
	d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 14507}, Reuse: true}, autoNow, 15*time.Minute)
	if d.Action != AutoAdvance {
		t.Fatalf("decision %+v, want advance", d)
	}
	// A different boss is not the goal's.
	d = DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 14515}, Reuse: true}, autoNow, 15*time.Minute)
	if d.Action != AutoRestart {
		t.Fatalf("decision %+v, want restart for the unkilled boss", d)
	}
}

func TestAutoFullClearAdvancesAndBadArgsStop(t *testing.T) {
	full := AutoObservation{Exited: true, ExitCode: 0, StepsReached: 761, StepsTotal: 761, EndReason: "boss down"}
	if !GoalAchieved(AutoGoal{Kind: "full"}, full) {
		t.Fatal("a full clear did not meet the full goal")
	}
	if d := DecideAutoAction(full, AutoRunState{Goal: AutoGoal{Kind: "full"}}, autoNow, 15*time.Minute); d.Action != AutoAdvance {
		t.Fatalf("decision %+v, want advance", d)
	}
	bad := AutoObservation{Exited: true, ExitCode: 2, EndReason: "bad --sql"}
	if d := DecideAutoAction(bad, AutoRunState{Goal: AutoGoal{Kind: "full"}}, autoNow, 15*time.Minute); d.Action != AutoStop {
		t.Fatalf("decision %+v, want stop", d)
	}
}

func TestAutoBackoffDoublesToTenMinutes(t *testing.T) {
	for fast, want := range map[int]time.Duration{0: 20 * time.Second, 1: 40 * time.Second, 2: 80 * time.Second, 10: 10 * time.Minute} {
		if got := AutoBackoff(fast); got != want {
			t.Errorf("AutoBackoff(%d) = %s, want %s", fast, got, want)
		}
	}
}
