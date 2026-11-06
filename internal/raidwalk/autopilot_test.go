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

// --gm-instance-data holds spaces in its value, but targets.txt is split
// on whitespace with no quoting: the words after the flag are joined back,
// and one `--gm-instance-data=<value>` argv reaches the walk whole.
func TestParseAutoTargetsGMInstanceData(t *testing.T) {
	ts, err := ParseAutoTargets(strings.NewReader("a test2 1 h full -- zul_gurub --sql /zg.sql --gm-instance-data hakkar:1 3;6 3;0 0 --hires /r.json\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || len(ts[0].Args) == 0 {
		t.Fatalf("%+v", ts)
	}
	want := "--gm-instance-data=hakkar:1 3;6 3;0 0"
	found := false
	for _, a := range ts[0].Args {
		if a == want {
			found = true
		}
		if strings.HasPrefix(a, "--gm-instance-data") && a != want {
			t.Fatalf("split value argv %q", a)
		}
	}
	if !found {
		t.Fatalf("no joined argv in %q", ts[0].Args)
	}
	// The = form joins the same way, and a bare flag is refused.
	ts, err = ParseAutoTargets(strings.NewReader("a test2 1 h full -- zul_gurub --sql /zg.sql --gm-instance-data=hakkar:1 3 --hires /r.json\n"))
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, a := range ts[0].Args {
		if a == want[:len("--gm-instance-data=hakkar:1 3")] {
			found = true
		}
	}
	if !found {
		t.Fatalf("no joined = argv in %q", ts[0].Args)
	}
	if _, err := ParseAutoTargets(strings.NewReader("a test2 1 h full -- zul_gurub --sql /zg.sql --gm-instance-data\n")); err == nil {
		t.Fatal("a bare --gm-instance-data parsed")
	}
}

func TestParseAutoTargetsRefusals(t *testing.T) {
	cases := map[string]string{
		"slot 3":       "a test2 3 h full -- zg --sql /zg.sql",
		"unsafe":       "a test2 1 h full -- zg --sql /zg.sql --instance-cap-unsafe",
		"inject":       "a test2 1 h full -- zg --sql /zg.sql --inject kill-mt",
		"no sql":       "a test2 1 h full -- zg",
		"no separator": "a test2 1 h full zg",
		"bad goal":     "a test2 1 h boss -- zg --sql /zg.sql",
		"no plan":      "a test2 1 h full -- --sql /zg.sql --dir /plans",
		"bad from":     "a test2 1 h full -- zg --sql /zg.sql --from 42",
		"bad slot":     "a test2 x h full -- zg --sql /zg.sql",
		"no goals":     "# only a comment",
		// One slot runs one walk at a time: two ids may not name the same server and slot.
		"duplicate slot": "a test2 1 h full -- zg --sql /zg.sql\nb test2 1 h2 full -- zg --sql /zg.sql",
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
	if d.Backoff != time.Minute {
		t.Fatalf("first failed fresh attempt backoff = %s, want 1m (one no-progress attempt)", d.Backoff)
	}
	// The fresh attempt fails too: the backoff grows.
	d = DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 11380}, Reuse: false, HasRun: true, FastFailures: 2}, autoNow, 15*time.Minute)
	if d.Action != AutoRestart || d.Reuse || d.Backoff != 2*time.Minute {
		t.Fatalf("decision %+v, want a fresh restart with a 2m backoff", d)
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
	// The kill counts whatever the exit code: an incident after it (19) still met the goal.
	o.ExitCode = 19
	o.EndReason = "wipe after the boss went down"
	if !GoalAchieved(AutoGoal{Kind: "boss", Boss: 14507}, o) {
		t.Fatal("a boss killed in an exit-19 run did not meet the boss goal")
	}
	if d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 14507}, Reuse: true}, autoNow, 15*time.Minute); d.Action != AutoAdvance {
		t.Fatalf("decision %+v, want advance after the exit-19 kill", d)
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
	// A clear that ended in an incident (19) is still a clear.
	full.ExitCode = 19
	if !GoalAchieved(AutoGoal{Kind: "full"}, full) {
		t.Fatal("an exit-19 full clear did not meet the full goal")
	}
	bad := AutoObservation{Exited: true, ExitCode: 2, EndReason: "bad --sql"}
	if d := DecideAutoAction(bad, AutoRunState{Goal: AutoGoal{Kind: "full"}}, autoNow, 15*time.Minute); d.Action != AutoStop || !d.Escalate {
		t.Fatalf("decision %+v, want a stop with an ESCALATE line", d)
	}
	// Exit 29 on a same-instance placement: ONE automatic fresh-instance retry (walk 40: 45 ids stopped here).
	placeFailed := AutoObservation{Exited: true, ExitCode: 29, EndReason: "place: 4 tries in the same instance, start step 582"}
	d := DecideAutoAction(placeFailed, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 14507}, Reuse: true, HasRun: true}, autoNow, 15*time.Minute)
	if d.Action != AutoRestart || d.Reuse || d.Escalate || !strings.Contains(d.Reason, "fresh-instance retry after exit 29") {
		t.Fatalf("decision %+v, want a restart in a fresh instance", d)
	}
	// A second exit 29, from the fresh-instance attempt (Reuse false), stops the id with an ESCALATE line.
	if d := DecideAutoAction(placeFailed, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 14507}, Reuse: false, HasRun: true}, autoNow, 15*time.Minute); d.Action != AutoStop || !d.Escalate {
		t.Fatalf("decision %+v, want a stop with an ESCALATE line after the fresh retry also failed", d)
	}
}

// Exit 15 is the slot's lease held by another holder: three consecutive attempts stop the id for a person.
func TestAutoLeaseHeldThreeTimesEscalates(t *testing.T) {
	o := AutoObservation{Exited: true, ExitCode: 15, EndReason: "lease: srv/1 is not leased by zg-a"}
	for i := 0; i < 2; i++ {
		d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "full"}, LeaseFails: i}, autoNow, 15*time.Minute)
		if d.Action != AutoRestart {
			t.Fatalf("exit 15 #%d: decision %+v, want a restart", i+1, d)
		}
	}
	if d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "full"}, LeaseFails: 2}, autoNow, 15*time.Minute); d.Action != AutoStop || !d.Escalate {
		t.Fatalf("third exit 15: decision %+v, want a stop with an ESCALATE line", d)
	}
}

func TestAutoBackoffTwentySecondsToOneMinuteSteps(t *testing.T) {
	for fast, want := range map[int]time.Duration{
		0: 20 * time.Second, 1: time.Minute, 2: 2 * time.Minute, 3: 5 * time.Minute, 4: 10 * time.Minute, 10: 10 * time.Minute,
	} {
		if got := AutoBackoff(fast); got != want {
			t.Errorf("AutoBackoff(%d) = %s, want %s", fast, got, want)
		}
	}
}

// A restart carries the backoff of its no-progress count; the stall clock of a run waiting on the instance cap starts
// when the wait ends, not when the run last moved.
func TestAutoBackoffAndStallAfterTheCapWait(t *testing.T) {
	o := AutoObservation{Exited: true, ExitCode: 5, StepsReached: 51, EndReason: "server closed the connection"}
	if d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "full"}, FastFailures: 2}, autoNow, 15*time.Minute); d.Backoff != 2*time.Minute {
		t.Fatalf("restart backoff = %s, want 2m at two no-progress attempts", d.Backoff)
	}
	// The cap wait ended 10 minutes ago; the run moved 40 minutes ago. The stall clock starts at the wait's end.
	o = AutoObservation{
		Exited: false, StepsReached: 51, LastProgress: autoNow.Add(-40 * time.Minute),
		StallAfter: autoNow.Add(-10 * time.Minute),
	}
	if d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "full"}}, autoNow, 15*time.Minute); d.Action != AutoContinue {
		t.Fatalf("decision %+v, want continue 10m after the cap wait ended", d)
	}
	o.StallAfter = autoNow.Add(-20 * time.Minute)
	if d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "full"}}, autoNow, 15*time.Minute); d.Action != AutoRestart {
		t.Fatalf("decision %+v, want a stall restart 20m after the cap wait ended", d)
	}
}

// L5: a boss in boss_kills meets its goal even when the run ended with a "stopped: ..." reason (the 90-minute limit
// firing after the kill). A run stop or a failed placement still does not make a boss goal without the kill.
func TestAutoStoppedEndStillMeetsAKilledBoss(t *testing.T) {
	goal := AutoGoal{Kind: "boss", Boss: 14515}
	o := AutoObservation{
		Exited:       true,
		ExitCode:     0,
		StepsReached: 417,
		StepsTotal:   761,
		EndReason:    "stopped: the 90-minute limit",
		KilledBosses: map[uint32]bool{14515: true},
	}
	if !GoalAchieved(goal, o) {
		t.Fatal("a killed boss did not meet its goal because the run ended stopped")
	}
	d := DecideAutoAction(o, AutoRunState{Goal: goal, Reuse: true, HasRun: true}, autoNow, 15*time.Minute)
	if d.Action != AutoAdvance {
		t.Fatalf("decision %+v, want an advance", d)
	}
	// The kill is the boss's: another boss's goal is not met by it.
	if GoalAchieved(AutoGoal{Kind: "boss", Boss: 14507}, o) {
		t.Fatal("another boss's goal was met by 14515's kill")
	}
	// Without the kill, a stopped end still fails the boss goal.
	o.KilledBosses = map[uint32]bool{}
	if GoalAchieved(goal, o) {
		t.Fatal("a stopped run without the kill met the boss goal")
	}
	// A leg goal is still not met by a stopped end.
	if GoalAchieved(AutoGoal{Kind: "leg", Leg: "high_priestess_marli"}, o) {
		t.Fatal("a stopped run met a leg goal")
	}
}

// Walk 39 t7/m6: the goal was boss:14509 (Thekal's reborn form) and the walk's summary credited the kill to 14599,
// his boss row's entry, with phase_entry 14509. The goal never matched, so the autopilot restarted a raid whose boss
// was already dead (t7 restarted 9 s after the kill and later escalated). A goal boss matches any entry of that boss.
func TestAutoGoalBossMatchesTheBossOtherEntry(t *testing.T) {
	o := AutoObservation{
		Exited: true, ExitCode: 0, StepsReached: 257, StepsTotal: 733, EndReason: "until",
		KilledBosses: map[uint32]bool{14599: true},
	}
	if !GoalAchieved(AutoGoal{Kind: "boss", Boss: 14509}, o) {
		t.Fatal("a kill of Thekal 14599 does not meet the goal boss:14509")
	}
	if d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 14509}}, autoNow, time.Hour); d.Action != AutoAdvance {
		t.Fatalf("decision %+v, want advance", d)
	}
	// The goal's own entry still matches, and a Zulian Guardian (15068, Thekal's phase_entries add) is not the boss.
	o2 := AutoObservation{Exited: true, KilledBosses: map[uint32]bool{15068: true}}
	if GoalAchieved(AutoGoal{Kind: "boss", Boss: 14509}, o2) {
		t.Fatal("a Zulian Guardian's death meets the goal boss:14509")
	}
}

// Walk 39: several big raids on one server starved each other of hires ("No mercenary of that kind is available
// right now", m5 at 03:58/04:12, m1 at 04:20 and t1/t3 in their own dirs), and each refusal counted as a no-progress
// attempt, growing the backoff toward 10 minutes and the ESCALATE thresholds. The refusal is a 5-minute pool wait
// for that raid, not a failure.
func TestAutoHirePoolWaitBacksOffFiveMinutes(t *testing.T) {
	o := AutoObservation{
		Exited: true, ExitCode: 18, StepsReached: 0, StepsTotal: 733,
		EndReason: "hire 2 priest:healer: TBM:HIRE_RAID|partial|1|2|0|1|0|2|0|No mercenary of that kind is available right now. Try again",
	}
	if !HirePoolRefused(o.EndReason) {
		t.Fatalf("the evidence's refusal was not read: %q", o.EndReason)
	}
	o.HirePoolWait = true
	d := DecideAutoAction(o, AutoRunState{Goal: AutoGoal{Kind: "boss", Boss: 11380}, FastFailures: 4}, autoNow, time.Hour)
	if d.Action != "hire-pool-wait" {
		t.Fatalf("action %q, want hire-pool-wait (%+v)", d.Action, d)
	}
	if d.Backoff != 5*time.Minute || d.Escalate {
		t.Fatalf("decision %+v, want a 5m backoff and no escalate", d)
	}
	if d.Action == AutoStop {
		t.Fatal("a hire-pool wait stopped the id")
	}
}

// Walk 40: a done id kept its slot, so a new line naming it made the whole file invalid. An inactive id holds no slot;
// an active id does, and claims it before a new line whatever the file order (a new line inserted ABOVE the running
// id's line is the skipped one).
func TestParseAutoTargetsLenientClaimsDecideTheSlot(t *testing.T) {
	text := "mh5 test2 6 h boss:14515 -- zg --sql /zg.sql\nnew test2 6 h2 full -- zg --sql /zg.sql\n"
	ts, issues, err := ParseAutoTargetsLenient(strings.NewReader(text), nil)
	if err != nil || len(issues) != 1 || issues[0].ID != "new" || issues[0].Line != 2 || len(ts) != 1 || ts[0].ID != "mh5" {
		t.Fatalf("no claims: targets %v issues %+v err %v, want the first line kept and the second skipped", ts, issues, err)
	}
	claim := func(m map[string]AutoSlotClaim) func(string) AutoSlotClaim {
		return func(id string) AutoSlotClaim { return m[id] }
	}
	ts, issues, err = ParseAutoTargetsLenient(strings.NewReader(text), claim(map[string]AutoSlotClaim{"mh5": AutoSlotInactive}))
	if err != nil || len(issues) != 0 || len(ts) != 2 {
		t.Fatalf("inactive id: targets %v issues %+v err %v, want both lines accepted", ts, issues, err)
	}
	// The new line is FIRST in the file, the running id second: the running id keeps the slot.
	above := "new test2 6 h2 full -- zg --sql /zg.sql\nmh5 test2 6 h boss:14515 -- zg --sql /zg.sql\n"
	ts, issues, err = ParseAutoTargetsLenient(strings.NewReader(above), claim(map[string]AutoSlotClaim{"mh5": AutoSlotActive}))
	if err != nil || len(issues) != 1 || issues[0].ID != "new" || issues[0].Line != 1 || len(ts) != 1 || ts[0].ID != "mh5" {
		t.Fatalf("active id below a new line: targets %v issues %+v err %v, want the new line skipped", ts, issues, err)
	}
}

// Codex round 1: a rejected id released nothing, so the slots it had claimed on its other lines invalidated later ids.
// x claims slot 2 on its first line and is rejected on its second (slot 1 belongs to the active a): z may take slot 2.
func TestParseAutoTargetsLenientRejectedIDReleasesItsClaims(t *testing.T) {
	text := "x test2 2 h full -- zg --sql /zg.sql\n" +
		"x test2 1 h full -- zg --sql /zg.sql\n" +
		"z test2 2 h full -- zg --sql /zg.sql\n" +
		"a test2 1 h full -- zg --sql /zg.sql\n"
	claim := func(id string) AutoSlotClaim {
		if id == "a" {
			return AutoSlotActive
		}
		return AutoSlotNew
	}
	ts, issues, err := ParseAutoTargetsLenient(strings.NewReader(text), claim)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range ts {
		ids = append(ids, x.ID)
	}
	if len(issues) != 1 || issues[0].ID != "x" || strings.Join(ids, ",") != "z,a" {
		t.Fatalf("targets %v issues %+v, want z and a kept and only x skipped", ids, issues)
	}
}

// The claim callback is asked once per id, not once per goal line.
func TestParseAutoTargetsLenientAsksOncePerID(t *testing.T) {
	text := "a test2 1 h boss:1 -- zg --sql /zg.sql\na test2 1 h boss:2 -- zg --sql /zg.sql\na test2 1 h boss:3 -- zg --sql /zg.sql\n"
	asked := 0
	if _, _, err := ParseAutoTargetsLenient(strings.NewReader(text), func(string) AutoSlotClaim { asked++; return AutoSlotActive }); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Fatalf("claim asked %d times for one id", asked)
	}
}

// Exit 29 is retried in a fresh instance once per GOAL: a goal that already made its retry stops the id on the next 29,
// whichever placement it came from.
func TestAutoExit29RetryOncePerGoalRule(t *testing.T) {
	placeFailed := AutoObservation{Exited: true, ExitCode: 29, EndReason: "place: 4 tries in the same instance, start step 582"}
	g := AutoGoal{Kind: "boss", Boss: 14507}
	d := DecideAutoAction(placeFailed, AutoRunState{Goal: g, Reuse: true, HasRun: true}, autoNow, 15*time.Minute)
	if d.Action != AutoRestart || !d.Exit29Retry {
		t.Fatalf("first 29 of a goal: %+v, want the counted fresh retry", d)
	}
	if d := DecideAutoAction(placeFailed, AutoRunState{Goal: g, Reuse: true, HasRun: true, Exit29Retries: 1}, autoNow, 15*time.Minute); d.Action != AutoStop || !d.Escalate {
		t.Fatalf("second 29 of the same goal: %+v, want a stop with an ESCALATE line", d)
	}
}

// One bad line is skipped with its line number and id; the other lines still parse, and the bad id's other lines
// are left out too (its caller keeps the old state whole).
func TestParseAutoTargetsLenientSkipsOnlyTheBadLine(t *testing.T) {
	text := "a test2 1 h full -- zg --sql /zg.sql\n" +
		"b test2 2 h full -- zg\n" + // no --sql
		"b test2 2 h full -- zg --sql /zg.sql --from step 5\n" +
		"c test2 4 h full -- zg --sql /zg.sql\n"
	ts, issues, err := ParseAutoTargetsLenient(strings.NewReader(text), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Line != 2 || issues[0].ID != "b" || !strings.Contains(issues[0].Err.Error(), "line 2") {
		t.Fatalf("issues %+v, want one for line 2 of b", issues)
	}
	if len(ts) != 2 || ts[0].ID != "a" || ts[1].ID != "c" {
		t.Fatalf("targets %v, want a and c only", ts)
	}
	// Strict read: the same file fails as a whole, naming the line.
	if _, err := ParseAutoTargets(strings.NewReader(text)); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("strict read err %v, want a line 2 error", err)
	}
	// Nothing valid at all is an error, as before.
	if _, _, err := ParseAutoTargetsLenient(strings.NewReader("garbage\n"), nil); err == nil {
		t.Fatal("a file with no valid line parsed")
	}
}

// Opus round 2 (LOW-1): an id is claimed as a unit. P (active) holds srv/2; O (new) names srv/1 and srv/2; X (new)
// names srv/1. O cannot have srv/2, so it is rejected whole and X gets srv/1: a rejected id never takes a valid one
// down with it. The issue names the owner that holds the slot (P), not an id that was dropped later.
func TestParseAutoTargetsLenientClaimsPerIDAsAUnit(t *testing.T) {
	text := "O test2 1 h full -- zg --sql /zg.sql\n" +
		"X test2 1 h full -- zg --sql /zg.sql\n" +
		"O test2 2 h full -- zg --sql /zg.sql\n" +
		"P test2 2 h full -- zg --sql /zg.sql\n"
	claim := func(id string) AutoSlotClaim {
		if id == "P" {
			return AutoSlotActive
		}
		return AutoSlotNew
	}
	ts, issues, err := ParseAutoTargetsLenient(strings.NewReader(text), claim)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range ts {
		ids = append(ids, x.ID+"@"+x.Slot)
	}
	if strings.Join(ids, ",") != "X@1,P@2" {
		t.Fatalf("targets %v issues %+v, want X on srv/1 and P on srv/2", ids, issues)
	}
	if len(issues) != 1 || issues[0].ID != "O" || issues[0].Line != 3 || !strings.Contains(issues[0].Err.Error(), "already P's") {
		t.Fatalf("issues %+v, want one for O's line 3 naming P", issues)
	}
}

// Codex round 2 (HIGH): a slot the caller's live state holds is that id's before any line is read, even when the
// holder's own lines are skipped as malformed: a new id on it is rejected.
func TestParseAutoTargetsHeldSlotStaysWithABadLinesID(t *testing.T) {
	text := "a test2 1 h full -- zg --sql /zg.sql\n" +
		"a test2 1 h leg:2 -- zg\n" + // no --sql: every line of a is left out
		"b test2 1 h full -- zg --sql /zg.sql\n" +
		"c test2 2 h full -- zg --sql /zg.sql\n"
	claim := func(id string) AutoSlotClaim {
		if id == "a" {
			return AutoSlotActive
		}
		return AutoSlotNew
	}
	ts, issues, err := ParseAutoTargetsHeld(strings.NewReader(text), claim, map[string]string{AutoSlotKey("test2", "1"): "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].ID != "c" {
		t.Fatalf("targets %v, want only c", ts)
	}
	if len(issues) != 2 || issues[0].ID != "a" || issues[1].ID != "b" || !strings.Contains(issues[1].Err.Error(), "already a's") {
		t.Fatalf("issues %+v, want a's bad line and b's slot taken by a", issues)
	}
}
