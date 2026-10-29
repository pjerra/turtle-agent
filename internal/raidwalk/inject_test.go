package raidwalk

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

const (
	gMt = packet.GUID(101)
	gOt = packet.GUID(102)
	gR1 = packet.GUID(201)
	gR2 = packet.GUID(202)
	gR3 = packet.GUID(203)
)

func injectRoster(withOT bool) *Roster {
	batches := []Batch{
		{Role: RoleMT, Hire: "1 warrior:tank"},
		{Role: RoleRest, Hire: "3h"},
	}
	if withOT {
		batches = []Batch{
			{Role: RoleMT, Hire: "1 warrior:tank"},
			{Role: RoleOT, Hire: "1t"},
			{Role: RoleRest, Hire: "3h"},
		}
	}
	return &Roster{Format: "tb-raid-roster", Version: 1, Batches: batches}
}

func mustInjects(t *testing.T, specs ...string) []InjectSpec {
	t.Helper()
	got, err := ParseInjects(specs, injectRoster(true))
	if err != nil {
		t.Fatalf("ParseInjects(%v): %v", specs, err)
	}
	return got
}

func injectNames() RosterNames {
	return RosterNames{MT: "Mt", OT: []string{"Ot"}, Rest: []string{"R1", "R2", "R3"}, Tanks: []string{"Mt", "Ot"}}
}

func injectPlan() *dungeon.Plan {
	return &dungeon.Plan{Name: "onyxias_lair", Kind: dungeon.KindRaid, Map: 249}
}

func injectBase() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }

func injectBots(now time.Time) map[string]BotObs {
	return map[string]BotObs{
		"Mt": {GUID: gMt, Pos: dungeon.Point{}, At: now},
		"Ot": {GUID: gOt, Pos: dungeon.Point{X: 3}, At: now},
		"R1": {GUID: gR1, Pos: dungeon.Point{Y: 4}, At: now},
		"R2": {GUID: gR2, Pos: dungeon.Point{Y: 5}, At: now},
		"R3": {GUID: gR3, Pos: dungeon.Point{Y: 6}, At: now},
	}
}

func runAt(now time.Time, kind string, step int) tbm.RunStatus {
	return tbm.RunStatus{Plan: "onyxias_lair", State: tbm.RunRunning, Step: step, Steps: 29, Leg: "lair", Kind: kind, Phase: "pull"}
}

// TestParseInjects pins spec 1.1's grammar and its refusals.
func TestParseInjects(t *testing.T) {
	withOT := injectRoster(true)
	oks := []struct {
		spec string
		want InjectSpec
	}{
		{"kill-mt", InjectSpec{Kind: InjectKillMT, From: 1, Raw: "kill-mt"}},
		{"runaway,dist=150,hold=90", InjectSpec{Kind: InjectRunaway, From: 1, Dist: 150, HoldS: 90, Raw: "runaway,dist=150,hold=90"}},
		{"add-pack@from=5,max=50", InjectSpec{Kind: InjectAddPack, From: 5, MaxYd: 50, Raw: "add-pack@from=5,max=50"}},
		{"lag,secs=30", InjectSpec{Kind: InjectLag, From: 1, Secs: 30, Raw: "lag,secs=30"}},
		// Defaults of each kind.
		{"runaway", InjectSpec{Kind: InjectRunaway, From: 1, Dist: 150, HoldS: 90, Raw: "runaway"}},
		{"add-pack", InjectSpec{Kind: InjectAddPack, From: 1, MaxYd: 60, Raw: "add-pack"}},
		{"lag", InjectSpec{Kind: InjectLag, From: 1, Secs: 30, Raw: "lag"}},
		{"kill-mt@from=2", InjectSpec{Kind: InjectKillMT, From: 2, Raw: "kill-mt@from=2"}},
	}
	for _, c := range oks {
		got, err := ParseInjects([]string{c.spec}, withOT)
		if err != nil {
			t.Errorf("ParseInjects(%q): %v", c.spec, err)
			continue
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("ParseInjects(%q) = %+v, want %+v", c.spec, got, c.want)
		}
	}
	bad := []string{
		"kill-boss",                 // unknown kind
		"runaway,dist=90",           // out of range
		"runaway,dist=251",          // out of range
		"runaway,hold=29",           // out of range
		"add-pack,max=81",           // out of range
		"lag,secs=5",                // out of range
		"kill-mt,dist=1",            // a key of another kind
		"runaway,secs=30",           // a key of another kind
		"add-pack,hold=90",          // a key of another kind
		"runaway@from=0",            // @from=0
		"runaway@from=x",            // not a number
		"runaway@hold=90",           // @ takes from only
		"runaway,dist",              // not key=value
		"runaway,dist=150,dist=160", // a repeated key
		"lag,lag",                   // a repeated kind
		"",                          // empty
	}
	for _, s := range bad {
		if _, err := ParseInjects([]string{s}, withOT); err == nil {
			t.Errorf("ParseInjects(%q) accepted", s)
		}
	}
	if _, err := ParseInjects([]string{"kill-mt"}, injectRoster(false)); err == nil || !strings.Contains(err.Error(), "kill-mt needs an ot batch: nobody would take over") {
		t.Errorf("kill-mt without an ot batch: err = %v", err)
	}
	if _, err := ParseInjects([]string{"lag", "lag", "lag", "lag", "lag"}, withOT); err == nil {
		t.Error("ParseInjects accepted five specs")
	}
}

func killObsAt(now time.Time, kind string, hp int, mtDead bool, h1, h2 packet.GUID) InjectObs {
	o := InjectObs{Now: now, Run: runAt(now, kind, 10), HaveRun: true, Lead: "Mt", Bots: injectBots(now)}
	o.Bots["Mt"] = BotObs{GUID: gMt, Pos: dungeon.Point{}, At: now, Dead: mtDead}
	if h1 != 0 {
		o.Hostiles = []HostileObs{
			{GUID: 301, Entry: 11, HPPct: hp, Target: h1, Reaction: "h", Kind: "-", Flags: "-", At: now},
			{GUID: 302, Entry: 12, HPPct: hp, Target: h2, Reaction: "h", Kind: "-", Flags: "-", At: now},
		}
	}
	return o
}

// TestKillMTFiresMidPackOnly pins spec 1.3's kill-mt fire condition.
func TestKillMTFiresMidPackOnly(t *testing.T) {
	base := injectBase()
	newInj := func() *Injector {
		return NewInjector(mustInjects(t, "kill-mt"), injectNames(), planexport.Rows{}, injectPlan())
	}

	// No fight at all.
	in := newInj()
	if acts := in.Tick(killObsAt(base, "pull", 60, false, 0, 0)); len(acts) != 0 {
		t.Fatalf("kill-mt fired without a fight: %+v", acts)
	}
	if acts := in.Tick(killObsAt(base.Add(10*time.Second), "pull", 60, false, 0, 0)); len(acts) != 0 {
		t.Fatalf("kill-mt fired without a fight: %+v", acts)
	}

	// The fight for 3 s is too young.
	in = newInj()
	in.Tick(killObsAt(base, "pull", 60, false, gMt, gR1))
	if acts := in.Tick(killObsAt(base.Add(3*time.Second), "pull", 60, false, gMt, gR1)); len(acts) != 0 {
		t.Fatalf("kill-mt fired after 3 s of fight: %+v", acts)
	}

	// 7 s of fight, two hostiles on the party, the MT targeted, mean hp 60, an OT alive: select the MT.
	in = newInj()
	in.Tick(killObsAt(base, "pull", 60, false, gMt, gR1))
	acts := in.Tick(killObsAt(base.Add(7*time.Second), "pull", 60, false, gMt, gR1))
	if len(acts) != 1 || acts[0].Kind != ActSelect || acts[0].GUID != gMt {
		t.Fatalf("kill-mt fire actions = %+v, want [select the MT]", acts)
	}
	// `.die` follows on the next tick: the allow-list refuses a `.die` until the MT's selection has been read
	// back (spec 1.4), so the injector cannot send both in one Tick.
	in.Ack(acts[0], InjectCtx{SelfSelected: true, SelectedHire: "Mt"}, nil)
	acts = in.Tick(killObsAt(base.Add(8*time.Second), "pull", 60, false, gMt, gR1))
	if len(acts) != 2 || acts[0].Kind != ActGM || acts[0].Line != ".die" || acts[1].Kind != ActSelectSelf {
		t.Fatalf("after the selection: %+v, want [.die, select self]", acts)
	}

	// Never on a boss step, and never with a boss engaged.
	in = newInj()
	in.Tick(killObsAt(base, "boss", 60, false, gMt, gR1))
	if acts := in.Tick(killObsAt(base.Add(7*time.Second), "boss", 60, false, gMt, gR1)); len(acts) != 0 {
		t.Fatalf("kill-mt fired on a boss step: %+v", acts)
	}
	in = newInj()
	o := killObsAt(base, "pull", 60, false, gMt, gR1)
	o.BossEngaged = true
	in.Tick(o)
	o = killObsAt(base.Add(7*time.Second), "pull", 60, false, gMt, gR1)
	o.BossEngaged = true
	if acts := in.Tick(o); len(acts) != 0 {
		t.Fatalf("kill-mt fired with a boss engaged: %+v", acts)
	}

	// No off tank alive.
	in = newInj()
	o = killObsAt(base, "pull", 60, false, gMt, gR1)
	o.Bots["Ot"] = BotObs{GUID: gOt, Dead: true, At: base}
	in.Tick(o)
	o = killObsAt(base.Add(7*time.Second), "pull", 60, false, gMt, gR1)
	o.Bots["Ot"] = BotObs{GUID: gOt, Dead: true, At: o.Now}
	if acts := in.Tick(o); len(acts) != 0 {
		t.Fatalf("kill-mt fired with no OT alive: %+v", acts)
	}

	// The hostiles are not weak enough: kill-mt expects 20-90 %.
	in = newInj()
	in.Tick(killObsAt(base, "pull", 95, false, gMt, gR1))
	if acts := in.Tick(killObsAt(base.Add(7*time.Second), "pull", 95, false, gMt, gR1)); len(acts) != 0 {
		t.Fatalf("kill-mt fired at 95 %% mean health: %+v", acts)
	}
}

// startKillMT fires kill-mt and sends .die: the returned time is the fire time (the .die follows at +1 s).
func startKillMT(t *testing.T) (*Injector, time.Time) {
	t.Helper()
	base := injectBase()
	in := NewInjector(mustInjects(t, "kill-mt"), injectNames(), planexport.Rows{}, injectPlan())
	in.Tick(killObsAt(base, "pull", 60, false, gMt, gR1))
	acts := in.Tick(killObsAt(base.Add(7*time.Second), "pull", 60, false, gMt, gR1))
	if len(acts) != 1 {
		t.Fatalf("kill-mt fire actions = %+v", acts)
	}
	in.Ack(acts[0], InjectCtx{SelfSelected: true, SelectedHire: "Mt"}, nil)
	acts = in.Tick(killObsAt(base.Add(8*time.Second), "pull", 60, false, gMt, gR1))
	for _, a := range acts {
		in.Ack(a, InjectCtx{}, nil)
	}
	return in, base.Add(7 * time.Second)
}

func ackSelect(t *testing.T, in *Injector, acts []InjectAction) {
	t.Helper()
	if len(acts) != 1 || acts[0].Kind != ActSelectSelf {
		t.Fatalf("selection actions = %+v, want [select self]", acts)
	}
	in.Ack(acts[0], InjectCtx{SelfSelected: true}, nil)
}

func ackGM(t *testing.T, in *Injector, acts []InjectAction, want string) {
	t.Helper()
	if len(acts) != 1 || acts[0].Kind != ActGM || acts[0].Line != want {
		t.Fatalf("actions = %+v, want [%s]", acts, want)
	}
	in.Ack(acts[0], InjectCtx{}, nil)
}

// TestKillMTVerdicts pins spec 1.3's kill-mt measurements and verdict.
func TestKillMTVerdicts(t *testing.T) {
	t.Run("pickup pass", func(t *testing.T) {
		in, fire := startKillMT(t)
		in.Tick(killObsAt(fire.Add(2*time.Second), "pull", 60, true, gMt, gR1)) // the MT dies at +2 s
		in.Tick(killObsAt(fire.Add(3*time.Second), "pull", 60, true, gOt, gOt)) // the OT picks both up at +3 s
		in.Tick(killObsAt(fire.Add(23*time.Second), "pull", 60, true, 0, 0))    // fight off; it ends at +23 s
		in.Tick(killObsAt(fire.Add(83*time.Second), "pull", 60, true, 0, 0))    // +60 s after the fight end
		res := in.Results()[0]
		if res.Verdict != VerdictPass {
			t.Fatalf("verdict = %q (%s), want pass", res.Verdict, res.Reason)
		}
		if got := res.Measured["pickup_s"]; got != 3.0 {
			t.Errorf("pickup_s = %v, want 3.0", got)
		}
		if got := res.Measured["pickup_by"]; got != "Ot" {
			t.Errorf("pickup_by = %v, want Ot", got)
		}
		if got := res.Measured["killed_at"]; got != 2.0 {
			t.Errorf("killed_at = %v, want 2.0", got)
		}
		if got := res.Measured["fight_end_s"]; got != 23.0 {
			t.Errorf("fight_end_s = %v, want 23.0", got)
		}
	})

	t.Run("no pickup in ten seconds", func(t *testing.T) {
		in, fire := startKillMT(t)
		in.Tick(killObsAt(fire.Add(2*time.Second), "pull", 60, true, gMt, gMt))
		in.Tick(killObsAt(fire.Add(11*time.Second), "pull", 60, true, gMt, gMt))
		res := in.Results()[0]
		if res.Verdict != VerdictFail || !strings.Contains(res.Reason, "no tank picked up the MT's attackers in 10 s") {
			t.Fatalf("verdict = %q (%s), want the no-pickup fail", res.Verdict, res.Reason)
		}
	})

	t.Run("wipe", func(t *testing.T) {
		in, fire := startKillMT(t)
		o := killObsAt(fire.Add(2*time.Second), "pull", 60, true, gMt, gMt)
		o.Wipe = true
		in.Tick(o)
		res := in.Results()[0]
		if res.Verdict != VerdictFail || !strings.Contains(res.Reason, "wipe after the MT died") {
			t.Fatalf("verdict = %q (%s), want the wipe fail", res.Verdict, res.Reason)
		}
	})

	t.Run("the MT does not die", func(t *testing.T) {
		in, fire := startKillMT(t)
		in.Tick(killObsAt(fire.Add(1*time.Second), "pull", 60, false, gMt, gR1))
		in.Tick(killObsAt(fire.Add(7*time.Second), "pull", 60, false, gMt, gR1))
		res := in.Results()[0]
		if res.Verdict != VerdictInconclusive || !strings.Contains(res.Reason, "the MT did not die") {
			t.Fatalf("verdict = %q (%s), want the did-not-die inconclusive", res.Verdict, res.Reason)
		}
	})

	t.Run("selection not confirmed", func(t *testing.T) {
		base := injectBase()
		in := NewInjector(mustInjects(t, "kill-mt"), injectNames(), planexport.Rows{}, injectPlan())
		in.Tick(killObsAt(base, "pull", 60, false, gMt, gR1))
		acts := in.Tick(killObsAt(base.Add(7*time.Second), "pull", 60, false, gMt, gR1))
		if len(acts) != 1 || acts[0].Kind != ActSelect {
			t.Fatalf("fire actions = %+v", acts)
		}
		in.Ack(acts[0], InjectCtx{}, nil) // status.target did not read back as the MT
		if acts := in.Tick(killObsAt(base.Add(8*time.Second), "pull", 60, false, gMt, gR1)); len(acts) != 0 {
			t.Fatalf("a .die followed an unconfirmed selection: %+v", acts)
		}
		res := in.Results()[0]
		if res.Verdict != VerdictInconclusive || !strings.Contains(res.Reason, "selection not confirmed") {
			t.Fatalf("verdict = %q (%s), want selection not confirmed", res.Verdict, res.Reason)
		}
	})
}

func runawayRows() planexport.Rows {
	return planexport.Rows{Steps: []planexport.StepRow{
		{Leg: 0, Seq: 1, Kind: "move", X: -60},
		{Leg: 0, Seq: 2, Kind: "move", X: -140},
		{Leg: 0, Seq: 3, Kind: "move", X: -155},
		{Leg: 0, Seq: 4, Kind: "move", X: -300},
		{Leg: 0, Seq: 5, Kind: "move"},         // unpositioned
		{Leg: 0, Seq: 6, Kind: "move", X: 150}, // ahead: beyond the current step
	}}
}

func runawayObs(now time.Time, step int) InjectObs {
	return InjectObs{Now: now, Run: runAt(now, "move", step), HaveRun: true, Lead: "Mt", Bots: injectBots(now)}
}

func atQ(o InjectObs) InjectObs {
	o.Bots["R3"] = BotObs{GUID: gR3, Pos: dungeon.Point{X: -155}, At: o.Now}
	return o
}

func nearLead(o InjectObs) InjectObs {
	o.Bots["R3"] = BotObs{GUID: gR3, Pos: dungeon.Point{Y: 3}, At: o.Now}
	return o
}

// TestRunawayPicksPointBehind pins spec 1.3's placement point Q.
func TestRunawayPicksPointBehind(t *testing.T) {
	base := injectBase()
	in := NewInjector(mustInjects(t, "runaway"), injectNames(), runawayRows(), injectPlan())
	in.Tick(runawayObs(base, 5))
	acts := in.Tick(runawayObs(base.Add(10*time.Second), 5))
	if len(acts) < 1 || acts[0].Kind != ActGM || !strings.HasPrefix(acts[0].Line, ".go xyz -155 0 0 249") {
		t.Fatalf("fire actions = %+v, want .go xyz at the 155 yd point behind", acts)
	}

	// No point in the band: no fire, still eligible, and the reason is kept.
	base = injectBase()
	rows := planexport.Rows{Steps: []planexport.StepRow{
		{Leg: 0, Seq: 1, Kind: "move", X: -60},
		{Leg: 0, Seq: 2, Kind: "move", X: -300},
	}}
	in = NewInjector(mustInjects(t, "runaway"), injectNames(), rows, injectPlan())
	in.Tick(runawayObs(base, 5))
	if acts := in.Tick(runawayObs(base.Add(10*time.Second), 5)); len(acts) != 0 {
		t.Fatalf("runaway fired without a point in range: %+v", acts)
	}
	in.Finish(runawayObs(base.Add(11*time.Second), 5))
	res := in.Results()[0]
	if res.Verdict != VerdictNotFired || !strings.Contains(res.Reason, "plan point") {
		t.Fatalf("verdict = %q (%s), want not-fired with the missing point", res.Verdict, res.Reason)
	}
}

// frozenRunaway freezes R3 at Q; the returned time is the freeze (t0).
func frozenRunaway(t *testing.T) (*Injector, time.Time) {
	t.Helper()
	base := injectBase()
	in := NewInjector(mustInjects(t, "runaway"), injectNames(), runawayRows(), injectPlan())
	in.Tick(runawayObs(base, 5))
	acts := in.Tick(runawayObs(base.Add(10*time.Second), 5))
	if len(acts) != 2 || acts[0].Kind != ActGM || acts[1].Kind != ActWait || acts[1].For != 2*time.Second {
		t.Fatalf("fire actions = %+v, want [.go, wait 2 s]", acts)
	}
	acts = in.Tick(runawayObs(base.Add(12*time.Second), 5))
	ackGM(t, in, acts, ".summon R3")
	acts = in.Tick(atQ(runawayObs(base.Add(13*time.Second), 5)))
	ackSelect(t, in, acts)
	acts = in.Tick(atQ(runawayObs(base.Add(14*time.Second), 5)))
	ackGM(t, in, acts, ".freeze R3")
	return in, base.Add(14 * time.Second)
}

// TestRunawayPlacesThenFreezes pins the placement and freeze order, and the no-arrival case.
func TestRunawayPlacesThenFreezes(t *testing.T) {
	in, _ := frozenRunaway(t)
	res := in.Results()[0]
	var text []string
	for _, l := range res.Lines {
		text = append(text, l.Text)
	}
	joined := strings.Join(text, " | ")
	for _, want := range []string{".go xyz -155 0 0 249", ".summon R3", ".freeze R3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lines %q lack %q", joined, want)
		}
	}
	if strings.Index(joined, ".summon R3") > strings.Index(joined, ".freeze R3") {
		t.Errorf("the freeze came before the summon: %q", joined)
	}

	// The victim never arrives: inconclusive and nothing frozen.
	base := injectBase()
	in = NewInjector(mustInjects(t, "runaway"), injectNames(), runawayRows(), injectPlan())
	in.Tick(runawayObs(base, 5))
	in.Tick(runawayObs(base.Add(10*time.Second), 5))
	acts := in.Tick(runawayObs(base.Add(12*time.Second), 5))
	ackGM(t, in, acts, ".summon R3")
	if acts := in.Tick(runawayObs(base.Add(22*time.Second), 5)); len(acts) != 0 {
		t.Fatalf("actions after a missing arrival: %+v", acts)
	}
	res = in.Results()[0]
	if res.Verdict != VerdictInconclusive || !strings.Contains(res.Reason, "did not arrive at Q") {
		t.Fatalf("verdict = %q (%s), want the no-arrival inconclusive", res.Verdict, res.Reason)
	}
	for _, a := range in.Cleanup() {
		if a.Kind == ActGM && strings.HasPrefix(a.Line, ".unfreeze") {
			t.Errorf("cleanup unfroze a bot that was never frozen: %+v", a)
		}
	}
}

// TestRunawayPassOnF4Note pins the F4 note match, the unfreeze triggers and the runaway verdicts.
func TestRunawayPassOnF4Note(t *testing.T) {
	t.Run("pass", func(t *testing.T) {
		in, t0 := frozenRunaway(t)
		o := atQ(runawayObs(t0.Add(9*time.Second), 5))
		o.Notes = []string{"R3 is 152.3 yd from the lead bot (8 s out of reach): summoned"}
		if acts := in.Tick(o); len(acts) != 0 {
			t.Fatalf("actions at the F4 note: %+v", acts)
		}
		acts := in.Tick(nearLead(runawayObs(t0.Add(12*time.Second), 5)))
		ackSelect(t, in, acts)
		acts = in.Tick(nearLead(runawayObs(t0.Add(13*time.Second), 5)))
		ackGM(t, in, acts, ".unfreeze R3")
		res := in.Results()[0]
		if res.Verdict != VerdictPass {
			t.Fatalf("verdict = %q (%s), want pass", res.Verdict, res.Reason)
		}
		// Times are from the fire time (spec 1.3): the freeze came 4 s after it.
		if got := res.Measured["f4_at_s"]; got != 13.0 {
			t.Errorf("f4_at_s = %v, want 13.0", got)
		}
		if got := res.Measured["back_s"]; got != 16.0 {
			t.Errorf("back_s = %v, want 16.0", got)
		}
		if got := res.Measured["f4_note"]; got != "R3 is 152.3 yd from the lead bot (8 s out of reach): summoned" {
			t.Errorf("f4_note = %v", got)
		}
	})

	t.Run("another bot's note is ignored", func(t *testing.T) {
		in, t0 := frozenRunaway(t)
		o := atQ(runawayObs(t0.Add(9*time.Second), 5))
		o.Notes = []string{"R2 is 152.3 yd from the lead bot (8 s out of reach): summoned"}
		in.Tick(o)
		acts := in.Tick(atQ(runawayObs(t0.Add(90*time.Second), 5)))
		ackSelect(t, in, acts)
		acts = in.Tick(atQ(runawayObs(t0.Add(91*time.Second), 5)))
		ackGM(t, in, acts, ".unfreeze R3")
		res := in.Results()[0]
		if res.Verdict != VerdictFail || !strings.Contains(res.Reason, "no runaway summon in 90 s") {
			t.Fatalf("verdict = %q (%s), want the no-summon fail", res.Verdict, res.Reason)
		}
	})

	t.Run("straggler first", func(t *testing.T) {
		in, t0 := frozenRunaway(t)
		o := atQ(runawayObs(t0.Add(9*time.Second), 5))
		o.Notes = []string{"R3 is 41.0 yd from the lead bot: summoned"}
		acts := in.Tick(o)
		ackSelect(t, in, acts)
		acts = in.Tick(atQ(runawayObs(t0.Add(10*time.Second), 5)))
		ackGM(t, in, acts, ".unfreeze R3")
		res := in.Results()[0]
		if res.Verdict != VerdictFail || !strings.Contains(res.Reason, "straggler ladder") {
			t.Fatalf("verdict = %q (%s), want the straggler fail", res.Verdict, res.Reason)
		}
	})

	t.Run("a fight most of the hold", func(t *testing.T) {
		in, t0 := frozenRunaway(t)
		fight := HostileObs{GUID: 401, Entry: 21, HPPct: 100, Target: gMt, Reaction: "h", Kind: "-", Flags: "-"}
		o := atQ(runawayObs(t0.Add(1*time.Second), 5))
		o.Hostiles = []HostileObs{fight}
		in.Tick(o)
		o = atQ(runawayObs(t0.Add(61*time.Second), 5))
		o.Hostiles = []HostileObs{fight}
		in.Tick(o)
		acts := in.Tick(atQ(runawayObs(t0.Add(90*time.Second), 5)))
		ackSelect(t, in, acts)
		acts = in.Tick(atQ(runawayObs(t0.Add(91*time.Second), 5)))
		ackGM(t, in, acts, ".unfreeze R3")
		res := in.Results()[0]
		if res.Verdict != VerdictInconclusive {
			t.Fatalf("verdict = %q (%s), want inconclusive (F4 waits for no fight)", res.Verdict, res.Reason)
		}
	})
}

// addPackPlan names a boss entry for add-pack's filter.
func addPackPlan() *dungeon.Plan {
	return &dungeon.Plan{Name: "onyxias_lair", Kind: dungeon.KindRaid, Map: 249,
		Legs: []dungeon.Leg{{Boss: &dungeon.Boss{Entry: 500}}}}
}

func addPackObs(now time.Time, hostiles []HostileObs) InjectObs {
	o := InjectObs{Now: now, Run: runAt(now, "pull", 10), HaveRun: true, Lead: "Mt", Bots: injectBots(now), Hostiles: hostiles}
	o.Bots["R1"] = BotObs{GUID: gR1, Pos: dungeon.Point{Y: 50}, At: now}
	o.Bots["R2"] = BotObs{GUID: gR2, Pos: dungeon.Point{Y: 41}, At: now}
	o.Bots["R3"] = BotObs{GUID: gR3, Pos: dungeon.Point{Y: 100}, At: now}
	return o
}

func partyHostile() HostileObs {
	return HostileObs{GUID: 401, Entry: 501, HPPct: 100, Pos: dungeon.Point{Y: 10}, Target: gR1, Reaction: "h", Kind: "-", Flags: "-"}
}

func idleHostile(guid packet.GUID, x, y float32) HostileObs {
	return HostileObs{GUID: guid, Entry: 502, HPPct: 100, Pos: dungeon.Point{X: x, Y: y}, Reaction: "h", Kind: "-", Flags: "-"}
}

// TestAddPackChoosesIdleHostile pins each filter of spec 1.3's candidate and the nearest valid one.
func TestAddPackChoosesIdleHostile(t *testing.T) {
	base := injectBase()
	candidate := idleHostile(402, 0, 40)
	nearerValid := idleHostile(403, 0, 55)

	hostiles := []HostileObs{
		partyHostile(), // the fight
		candidate,
		nearerValid,
		{GUID: 411, Entry: 511, HPPct: 100, Target: gMt, Reaction: "h", Kind: "-", Flags: "-"},                // has a target
		{GUID: 412, Entry: 512, HPPct: 99, Reaction: "h", Kind: "-", Flags: "-"},                              // hurt
		{GUID: 413, Entry: 513, HPPct: 100, Reaction: "h", Kind: "k", Flags: "-"},                             // critter
		{GUID: 414, Entry: 514, HPPct: 100, Reaction: "h", Kind: "-", Flags: "e"},                             // evading
		{GUID: 415, Entry: 500, HPPct: 100, Reaction: "h", Kind: "-", Flags: "-"},                             // a boss entry
		{GUID: 416, Entry: 516, HPPct: 100, Pos: dungeon.Point{Y: 25}, Reaction: "h", Kind: "-", Flags: "-"},  // 15 yd from the party's hostile
		{GUID: 417, Entry: 517, HPPct: 100, Pos: dungeon.Point{X: -19}, Reaction: "h", Kind: "-", Flags: "-"}, // 19 yd from the lead
		{GUID: 418, Entry: 518, HPPct: 100, Pos: dungeon.Point{Y: 75}, Reaction: "h", Kind: "-", Flags: "-"},  // beyond max 60
	}
	// Put the one-target-per-rule hostiles at 30 yd from the lead so only their own rule excludes them.
	distractors := []struct {
		name string
		h    HostileObs
	}{
		{"targeted", HostileObs{GUID: 421, Entry: 521, HPPct: 100, Target: gMt, Pos: dungeon.Point{X: 30}, Reaction: "h", Kind: "-", Flags: "-"}},
		{"hurt", HostileObs{GUID: 422, Entry: 522, HPPct: 99, Pos: dungeon.Point{X: -30}, Reaction: "h", Kind: "-", Flags: "-"}},
		{"critter", HostileObs{GUID: 423, Entry: 523, HPPct: 100, Pos: dungeon.Point{Y: -30}, Reaction: "h", Kind: "k", Flags: "-"}},
		{"evading", HostileObs{GUID: 424, Entry: 524, HPPct: 100, Pos: dungeon.Point{X: 21, Y: 21}, Reaction: "h", Kind: "-", Flags: "e"}},
		{"boss entry", HostileObs{GUID: 425, Entry: 500, HPPct: 100, Pos: dungeon.Point{X: -21, Y: 21}, Reaction: "h", Kind: "-", Flags: "-"}},
		{"within 20 yd of the party's", HostileObs{GUID: 426, Entry: 526, HPPct: 100, Pos: dungeon.Point{Y: 25}, Reaction: "h", Kind: "-", Flags: "-"}},
		{"within 20 yd of the lead", HostileObs{GUID: 427, Entry: 527, HPPct: 100, Pos: dungeon.Point{X: -19}, Reaction: "h", Kind: "-", Flags: "-"}},
		{"beyond max", HostileObs{GUID: 428, Entry: 528, HPPct: 100, Pos: dungeon.Point{Y: 75}, Reaction: "h", Kind: "-", Flags: "-"}},
	}
	for _, d := range distractors {
		t.Run(d.name, func(t *testing.T) {
			in := NewInjector(mustInjects(t, "add-pack"), injectNames(), planexport.Rows{}, addPackPlan())
			in.Tick(addPackObs(base, []HostileObs{partyHostile(), d.h}))
			if acts := in.Tick(addPackObs(base.Add(3*time.Second), []HostileObs{partyHostile(), d.h})); len(acts) != 0 {
				t.Fatalf("add-pack fired with only the %s hostile to pick: %+v", d.name, acts)
			}
		})
	}

	in := NewInjector(mustInjects(t, "add-pack"), injectNames(), planexport.Rows{}, addPackPlan())
	in.Tick(addPackObs(base, hostiles))
	acts := in.Tick(addPackObs(base.Add(3*time.Second), hostiles))
	if len(acts) != 1 || acts[0].Kind != ActOrder {
		t.Fatalf("fire actions = %+v, want [order attack ...]", acts)
	}
	if acts[0].Verb != "attack" || len(acts[0].Args) != 2 {
		t.Fatalf("order = %+v", acts[0])
	}
	if acts[0].Args[0] != candidate.GUID.String() {
		t.Errorf("the pack is %s, want the nearest idle hostile %s", acts[0].Args[0], candidate.GUID)
	}
	if acts[0].Args[1] != "R2" {
		t.Errorf("the puller is %s, want the nearest rest hire R2", acts[0].Args[1])
	}
}

// TestAddPackVerdicts pins spec 1.3's add-pack measurements and verdict.
func TestAddPackVerdicts(t *testing.T) {
	base := injectBase()
	fight := []HostileObs{partyHostile(), idleHostile(402, 0, 40)}
	fireAddPack := func(t *testing.T, in *Injector) time.Time {
		t.Helper()
		in.Tick(addPackObs(base, fight))
		acts := in.Tick(addPackObs(base.Add(3*time.Second), fight))
		if len(acts) != 1 || acts[0].Kind != ActOrder {
			t.Fatalf("fire actions = %+v", acts)
		}
		in.Ack(acts[0], InjectCtx{}, nil)
		return base.Add(3 * time.Second)
	}
	newInj := func() *Injector {
		return NewInjector(mustInjects(t, "add-pack"), injectNames(), planexport.Rows{}, addPackPlan())
	}

	t.Run("pass", func(t *testing.T) {
		in := newInj()
		fire := fireAddPack(t, in)
		pack := []HostileObs{partyHostile(), {GUID: 402, Entry: 502, HPPct: 100, Target: gR2, Reaction: "h", Kind: "-", Flags: "-"}}
		in.Tick(addPackObs(fire.Add(1*time.Second), pack)) // the extra pack engages at +1 s
		in.Tick(addPackObs(fire.Add(17*time.Second), nil)) // the fight is off; it ends at +17 s
		o := addPackObs(fire.Add(22*time.Second), nil)
		o.Run.Step = 11
		in.Tick(o)                                          // the run walked on
		in.Tick(addPackObs(fire.Add(137*time.Second), nil)) // fight end + 120 s
		res := in.Results()[0]
		if res.Verdict != VerdictPass {
			t.Fatalf("verdict = %q (%s), want pass", res.Verdict, res.Reason)
		}
		if got := res.Measured["engaged_s"]; got != 1.0 {
			t.Errorf("engaged_s = %v, want 1.0", got)
		}
		if got := res.Measured["fight_end_s"]; got != 17.0 {
			t.Errorf("fight_end_s = %v, want 17.0", got)
		}
		if got := res.Measured["run_continued_s"]; got != 5.0 {
			t.Errorf("run_continued_s = %v, want 5.0", got)
		}
		if got := res.Measured["pack_dead"]; got != true {
			t.Errorf("pack_dead = %v, want true", got)
		}
	})

	t.Run("not engaged", func(t *testing.T) {
		in := newInj()
		fire := fireAddPack(t, in)
		o := addPackObs(fire.Add(11*time.Second), fight)
		in.Tick(o)
		res := in.Results()[0]
		if res.Verdict != VerdictInconclusive || !strings.Contains(res.Reason, "did not engage") {
			t.Fatalf("verdict = %q (%s), want the not-engaged inconclusive", res.Verdict, res.Reason)
		}
	})

	t.Run("wipe", func(t *testing.T) {
		in := newInj()
		fire := fireAddPack(t, in)
		pack := []HostileObs{partyHostile(), {GUID: 402, Entry: 502, HPPct: 100, Target: gR2, Reaction: "h", Kind: "-", Flags: "-"}}
		o := addPackObs(fire.Add(1*time.Second), pack)
		o.Wipe = true
		in.Tick(o)
		res := in.Results()[0]
		if res.Verdict != VerdictFail || !strings.Contains(res.Reason, "wipe") {
			t.Fatalf("verdict = %q (%s), want the wipe fail", res.Verdict, res.Reason)
		}
	})

	t.Run("fight over 240 s", func(t *testing.T) {
		in := newInj()
		fire := fireAddPack(t, in)
		pack := []HostileObs{partyHostile(), {GUID: 402, Entry: 502, HPPct: 100, Target: gR2, Reaction: "h", Kind: "-", Flags: "-"}}
		in.Tick(addPackObs(fire.Add(1*time.Second), pack))
		in.Tick(addPackObs(fire.Add(241*time.Second), pack))
		res := in.Results()[0]
		if res.Verdict != VerdictFail || !strings.Contains(res.Reason, "240") {
			t.Fatalf("verdict = %q (%s), want the 240 s fail", res.Verdict, res.Reason)
		}
	})
}

// TestLagFreezesThenReleases pins lag's freeze/release and verdict.
func TestLagFreezesThenReleases(t *testing.T) {
	base := injectBase()
	newInj := func() *Injector {
		return NewInjector(mustInjects(t, "lag,secs=30"), injectNames(), planexport.Rows{}, injectPlan())
	}
	fireLag := func(t *testing.T, in *Injector) (freeze time.Time) {
		t.Helper()
		in.Tick(runawayObs(base, 5))
		acts := in.Tick(runawayObs(base.Add(10*time.Second), 5))
		ackSelect(t, in, acts)
		acts = in.Tick(runawayObs(base.Add(11*time.Second), 5))
		ackGM(t, in, acts, ".freeze R3")
		return base.Add(11 * time.Second)
	}

	t.Run("pass", func(t *testing.T) {
		in := newInj()
		t0 := fireLag(t, in)
		// The victim falls behind, then is released at t0+30 s and comes back 6 s later.
		o := runawayObs(t0.Add(10*time.Second), 5)
		o.Bots["R3"] = BotObs{GUID: gR3, Pos: dungeon.Point{Y: 100}, At: o.Now}
		in.Tick(o)
		acts := in.Tick(runawayObs(t0.Add(30*time.Second), 5))
		ackSelect(t, in, acts)
		acts = in.Tick(runawayObs(t0.Add(31*time.Second), 5))
		ackGM(t, in, acts, ".unfreeze R3")
		in.Tick(nearLead(runawayObs(t0.Add(37*time.Second), 5)))
		in.Tick(nearLead(runawayObs(t0.Add(67*time.Second), 5)))
		res := in.Results()[0]
		if res.Verdict != VerdictPass {
			t.Fatalf("verdict = %q (%s), want pass", res.Verdict, res.Reason)
		}
		if got := res.Measured["rejoin_s"]; got != 6.0 {
			t.Errorf("rejoin_s = %v, want 6.0", got)
		}
		if got := res.Measured["max_gap_yd"]; got != 100.0 {
			t.Errorf("max_gap_yd = %v, want 100.0", got)
		}
	})

	t.Run("no rejoin", func(t *testing.T) {
		in := newInj()
		t0 := fireLag(t, in)
		far := func(d time.Duration) InjectObs {
			o := runawayObs(t0.Add(d), 5)
			o.Bots["R3"] = BotObs{GUID: gR3, Pos: dungeon.Point{Y: 100}, At: o.Now}
			return o
		}
		acts := in.Tick(far(30 * time.Second))
		ackSelect(t, in, acts)
		acts = in.Tick(far(31 * time.Second))
		ackGM(t, in, acts, ".unfreeze R3")
		in.Tick(far(152 * time.Second))
		res := in.Results()[0]
		if res.Verdict != VerdictFail || !strings.Contains(res.Reason, "did not rejoin") {
			t.Fatalf("verdict = %q (%s), want the no-rejoin fail", res.Verdict, res.Reason)
		}
	})
}

// TestOneFaultAtATimeAndGap pins spec 1.2's one-active and 60 s rules.
func TestOneFaultAtATimeAndGap(t *testing.T) {
	base := injectBase()
	in := NewInjector(mustInjects(t, "lag,secs=10", "runaway"), injectNames(), runawayRows(), injectPlan())
	in.Tick(runawayObs(base, 5))
	acts := in.Tick(runawayObs(base.Add(10*time.Second), 5))
	ackSelect(t, in, acts)
	acts = in.Tick(runawayObs(base.Add(11*time.Second), 5))
	ackGM(t, in, acts, ".freeze R3")
	acts = in.Tick(runawayObs(base.Add(21*time.Second), 5))
	ackSelect(t, in, acts)
	acts = in.Tick(runawayObs(base.Add(22*time.Second), 5))
	ackGM(t, in, acts, ".unfreeze R3")
	in.Tick(nearLead(runawayObs(base.Add(23*time.Second), 5))) // rejoin at +2 s
	in.Tick(nearLead(runawayObs(base.Add(53*time.Second), 5))) // rejoin + 30 s: the verdict, at +53 s
	if res := in.Results()[0]; res.Verdict != VerdictPass {
		t.Fatalf("lag verdict = %q (%s)", res.Verdict, res.Reason)
	}
	// 59 s after the verdict is too early; the next tick (60 s) fires the runaway.
	if acts := in.Tick(runawayObs(base.Add(112*time.Second), 5)); len(acts) != 0 {
		t.Fatalf("a second fault fired inside the 60 s gap: %+v", acts)
	}
	acts = in.Tick(runawayObs(base.Add(113*time.Second), 5))
	if len(acts) < 1 || acts[0].Kind != ActGM || !strings.HasPrefix(acts[0].Line, ".go xyz") {
		t.Fatalf("after the gap: %+v, want the runaway .go", acts)
	}
}

// TestNeverFiresNearUntilStopOrPaused pins spec 1.2's global fire blocks.
func TestNeverFiresNearUntilStopOrPaused(t *testing.T) {
	base := injectBase()
	fireAt := func(t *testing.T, o InjectObs, step int) []InjectAction {
		t.Helper()
		in := NewInjector(mustInjects(t, "runaway"), injectNames(), runawayRows(), injectPlan())
		o.UntilStep = 29
		o.Run.Step = step
		o.Now = base
		in.Tick(o)
		o.Now = base.Add(10 * time.Second)
		return in.Tick(o)
	}
	if acts := fireAt(t, runawayObs(base, 27), 27); len(acts) != 0 {
		t.Fatalf("runaway fired at step 27 with until step 29: %+v", acts)
	}
	if acts := fireAt(t, runawayObs(base, 28), 28); len(acts) != 0 {
		t.Fatalf("runaway fired at step 28 with until step 29: %+v", acts)
	}
	in := NewInjector(mustInjects(t, "runaway"), injectNames(), runawayRows(), injectPlan())
	in.Tick(runawayObs(base, 26))
	o := runawayObs(base.Add(10*time.Second), 26)
	o.UntilStep = 29
	if acts := in.Tick(o); len(acts) < 1 || acts[0].Kind != ActGM {
		t.Fatalf("runaway did not fire at step 26 (until 29): %+v", acts)
	}

	// Paused, boss step, boss engaged.
	paused := runawayObs(base, 10)
	paused.Run.State = tbm.RunPaused
	if acts := fireAt(t, paused, 10); len(acts) != 0 {
		t.Fatalf("runaway fired while paused: %+v", acts)
	}
	boss := runawayObs(base, 10)
	boss.Run.Kind = "boss"
	if acts := fireAt(t, boss, 10); len(acts) != 0 {
		t.Fatalf("runaway fired on a boss step: %+v", acts)
	}
	engaged := runawayObs(base, 10)
	engaged.BossEngaged = true
	if acts := fireAt(t, engaged, 10); len(acts) != 0 {
		t.Fatalf("runaway fired with a boss engaged: %+v", acts)
	}
}

// TestCleanupUnfreezesFrozenOnly pins spec 1.2's idempotent cleanup.
func TestCleanupUnfreezesFrozenOnly(t *testing.T) {
	base := injectBase()
	fireLag := func(t *testing.T) (*Injector, time.Time) {
		t.Helper()
		in := NewInjector(mustInjects(t, "lag,secs=30"), injectNames(), planexport.Rows{}, injectPlan())
		in.Tick(runawayObs(base, 5))
		acts := in.Tick(runawayObs(base.Add(10*time.Second), 5))
		ackSelect(t, in, acts)
		acts = in.Tick(runawayObs(base.Add(11*time.Second), 5))
		ackGM(t, in, acts, ".freeze R3")
		return in, base.Add(11 * time.Second)
	}

	in, _ := fireLag(t)
	acts := in.Cleanup()
	if len(acts) != 3 || acts[0].Kind != ActSelectSelf || acts[1].Kind != ActGM || acts[1].Line != ".unfreeze R3" || acts[2].Kind != ActSelectSelf {
		t.Fatalf("cleanup actions = %+v, want [select self, .unfreeze R3, select self]", acts)
	}
	if again := in.Cleanup(); len(again) != 0 {
		t.Fatalf("a second cleanup did more: %+v", again)
	}

	// A bot already released is not unfrozen again.
	in, t0 := fireLag(t)
	acts = in.Tick(runawayObs(t0.Add(30*time.Second), 5))
	ackSelect(t, in, acts)
	acts = in.Tick(runawayObs(t0.Add(31*time.Second), 5))
	ackGM(t, in, acts, ".unfreeze R3")
	if acts := in.Cleanup(); len(acts) != 0 {
		t.Fatalf("cleanup froze/unfroze a released bot: %+v", acts)
	}
}

// TestFinishVerdicts pins spec 1.2's end-of-watch verdicts.
func TestFinishVerdicts(t *testing.T) {
	base := injectBase()
	// Never fired: the last unmet condition is the reason.
	rows := planexport.Rows{Steps: []planexport.StepRow{{Leg: 0, Seq: 1, Kind: "move", X: -60}}}
	in := NewInjector(mustInjects(t, "runaway"), injectNames(), rows, injectPlan())
	in.Tick(runawayObs(base, 5))
	in.Finish(runawayObs(base.Add(11*time.Second), 5))
	res := in.Results()[0]
	if res.Verdict != VerdictNotFired || res.Reason == "" || !strings.Contains(res.Reason, "plan point") {
		t.Fatalf("verdict = %q (%s), want not-fired with a reason", res.Verdict, res.Reason)
	}

	// An active kill-mt at a wipe: fail.
	in, fire := startKillMT(t)
	in.Tick(killObsAt(fire.Add(2*time.Second), "pull", 60, true, gMt, gMt))
	o := killObsAt(fire.Add(3*time.Second), "pull", 60, true, gMt, gMt)
	o.Wipe = true
	in.Finish(o)
	res = in.Results()[0]
	if res.Verdict != VerdictFail || !strings.Contains(res.Reason, "wipe after the MT died") {
		t.Fatalf("verdict = %q (%s), want the wipe fail", res.Verdict, res.Reason)
	}

	// An active kill-mt without a wipe: inconclusive.
	in, fire = startKillMT(t)
	in.Tick(killObsAt(fire.Add(2*time.Second), "pull", 60, true, gMt, gMt))
	in.Finish(killObsAt(fire.Add(3*time.Second), "pull", 60, true, gMt, gMt))
	res = in.Results()[0]
	if res.Verdict != VerdictInconclusive || !strings.Contains(res.Reason, "ended before the fault resolved") {
		t.Fatalf("verdict = %q (%s), want the unresolved inconclusive", res.Verdict, res.Reason)
	}
}
