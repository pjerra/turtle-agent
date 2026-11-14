package main

// The raid-walk --prepull-aura test tool: at the step before each boss row (whatever its phase), or on the boss row
// itself when the run passed that step too fast to pause, the walk pauses the run, puts the flag's lists on the
// living party with the gm_aura RPC (a confirmed select then ".aura <id>"), reads them back into prepull-auras.json
// and resumes. Once per boss row, never at or after the pull, a pause is always resumed, and an aura on the
// commander is a miss like any other.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// prepullAgent is the agent side of a --prepull-aura test: a party of players whose auras the gm_aura RPC changes,
// and the commander's own auras (where a misfired ".aura" lands).
type prepullAgent struct {
	t     *testing.T
	clock time.Time

	bots    []session.PartyMember
	classes map[packet.GUID]uint8
	auras   map[packet.GUID]map[uint32]bool

	selfGUID packet.GUID
	selfName string
	caster   map[uint32]bool
	// selfCombat is the commander's own combat state (the party's is `combat`): a pull the commander started alone.
	selfCombat bool

	// arenaCombat names the units the fresh `q units` read of a boss arena finds in combat, arenaCBUnknown names the
	// units it lists without a `cb` field (a degraded reply), arenaTrunc makes that reply truncated, and arenaErr
	// makes the `tt` call fail (the toolkit's refusal).
	arenaCombat    []string
	arenaCBUnknown []string
	arenaTrunc     bool
	arenaErr       error
	ttCmds         []string

	sent    []string // "<bot name> <id>" per gm_aura call, in order
	verbs   []string // every `run <verb>` the commander asked for
	answers bool     // the unit RPC answers for the party (set by newPrepullAgent)
	pauses  int
	// paused is the run's state as the fake's `run status` answers it; holdPause=false makes a pause ACK without
	// the run going paused. statusStep is the step the status line names (the rest before the boss row).
	paused     bool
	holdPause  bool
	statusLine string
	statusStep int
	// combat is what the party's unit rows report. combatOnPause starts it when the pause holds; combatAfter
	// starts it after that many gm_aura calls (0: never).
	combat        bool
	combatOnPause bool
	combatAfter   int
	sends         int

	// pauseErr and resumeErr are the commander's own refusals (the RPC itself failing).
	pauseErr  error
	resumeErr error
	// applyToSelf makes every ".aura" land on the commander instead of the target: the selection did not hold.
	applyToSelf bool
	// drop is the ids a bot never shows after its ".aura" (an aura the server refuses, or a selection that did not hold).
	drop map[packet.GUID]map[uint32]bool
	// combatBots are the bots whose own row reports combat whatever `combat` says (a pull from a member the flag's
	// lists do not cover).
	combatBots map[packet.GUID]bool
	// The commander's view (bot_view): noView answers with no frame, hidden leaves named bots out of the party,
	// deadBots marks them dead (in the view and in their unit rows, as the server's would), viewTruncated sets the
	// frame's truncation flag.
	noView        bool
	hidden        map[string]bool
	deadBots      map[string]bool
	viewTruncated bool
}

func newPrepullAgent(t *testing.T) *prepullAgent {
	t.Helper()
	a := &prepullAgent{t: t, clock: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), answers: true, holdPause: true, statusStep: 4,
		classes: map[packet.GUID]uint8{}, auras: map[packet.GUID]map[uint32]bool{}, caster: map[uint32]bool{},
		drop: map[packet.GUID]map[uint32]bool{}, combatBots: map[packet.GUID]bool{},
		hidden: map[string]bool{}, deadBots: map[string]bool{}}
	a.selfGUID, a.selfName = packet.GUID(0x0000000000000AAA), "Commander"
	// Mtbot (warrior mt), Otbot (warrior ot), Rank (rogue), Flitz (hunter), Zap (mage), Holi (priest).
	for i, b := range []struct {
		name  string
		class uint8
	}{{"Mtbot", 1}, {"Otbot", 1}, {"Rank", 4}, {"Flitz", 3}, {"Zap", 8}, {"Holi", 5}} {
		g := packet.GUID(0x100 + i)
		a.classes[g] = b.class
		a.auras[g] = map[uint32]bool{}
		a.bots = append(a.bots, session.PartyMember{Name: b.name, GUID: g, Online: true})
	}
	return a
}

func (a *prepullAgent) advance(d time.Duration) { a.clock = a.clock.Add(d) }

// aurasOf is the auras the unit RPC reports for a bot, in id order.
func (a *prepullAgent) aurasOf(g packet.GUID) []objects.Aura {
	ids := make([]uint32, 0, len(a.auras[g]))
	for id := range a.auras[g] {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]objects.Aura, len(ids))
	for i, id := range ids {
		out[i] = objects.Aura{Slot: i, SpellID: id}
	}
	return out
}

func (a *prepullAgent) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	raw, _ := json.Marshal(params)
	var v any
	switch method {
	case "party":
		// The player's own group: empty in every TurtleBots walk (the hired bots raid without the player), so a fire
		// that reads it sees 0 bots. The tests below prove the walk does not.
		v = session.PartyInfo{}
	case "bot_view":
		if a.noView {
			v = botViewResult{Have: false}
			break
		}
		fr := tbm.ViewFrame{Seq: 1, Lines: 1}
		for _, b := range a.bots {
			if a.hidden[b.Name] {
				continue
			}
			dead := a.deadBots[b.Name]
			state := "n"
			if dead {
				state = "d"
			}
			fr.Party = append(fr.Party, tbm.PartyRec{GUID: b.GUID, Name: b.Name, HP: 6000, MaxHP: 6000, Map: 309, State: state, Dead: dead})
		}
		fr.Truncated = a.viewTruncated
		v = botViewResult{Have: true, N: 1, Frame: &fr}
	case "status":
		v = session.StatusInfo{Character: a.selfName, GUID: a.selfGUID, State: session.StatusInWorld}
	case "unit":
		var p struct {
			GUID string `json:"guid"`
		}
		_ = json.Unmarshal(raw, &p)
		g, err := packet.ParseGUID(p.GUID)
		if err != nil {
			return err
		}
		if g == a.selfGUID {
			u := objects.Unit{GUID: g, Type: "player", Name: a.selfName, InCombat: a.selfCombat}
			for id := range a.caster {
				u.Auras = append(u.Auras, objects.Aura{SpellID: id})
			}
			v = u
			break
		}
		if _, ok := a.auras[g]; !ok || !a.answers {
			return errors.New("unit: no such unit")
		}
		v = objects.Unit{GUID: g, Type: "player", Name: a.nameOf(g), Class: a.classes[g],
			InCombat: a.combat || a.combatBots[g], Dead: a.deadBots[a.nameOf(g)], Auras: a.aurasOf(g)}
	case "gm_aura":
		var p struct {
			GUID    string `json:"guid"`
			SpellID uint32 `json:"spell_id"`
		}
		_ = json.Unmarshal(raw, &p)
		g, err := packet.ParseGUID(p.GUID)
		if err != nil {
			return err
		}
		a.sent = append(a.sent, a.nameOf(g)+" "+itoaU(uint32(p.SpellID)))
		a.sends++
		if a.combatAfter > 0 && a.sends >= a.combatAfter {
			a.combat = true
		}
		if a.applyToSelf {
			a.caster[p.SpellID] = true
			break
		}
		if _, ok := a.auras[g]; !ok {
			return errors.New("gm aura: no such player")
		}
		if a.drop[g][p.SpellID] {
			break
		}
		a.auras[g][p.SpellID] = true
	case "tt":
		var p struct {
			Cmd string `json:"cmd"`
		}
		_ = json.Unmarshal(raw, &p)
		a.ttCmds = append(a.ttCmds, p.Cmd)
		if a.arenaErr != nil {
			return a.arenaErr
		}
		var units []any
		for i, name := range a.arenaCombat {
			units = append(units, map[string]any{"g": fmt.Sprintf("0xF1300000000%03X", 0x200+i), "n": name, "e": 14834, "cb": true})
		}
		for i, name := range a.arenaCBUnknown {
			units = append(units, map[string]any{"g": fmt.Sprintf("0xF1300000000%03X", 0x300+i), "n": name, "e": 14834})
		}
		v = ttReplyOf(a.t, map[string]any{"units": units, "n": len(units), "trunc": a.arenaTrunc})
	case "commander":
		var p struct {
			Op   string `json:"op"`
			Verb string `json:"verb"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Op != "run" {
			a.t.Errorf("unexpected commander op %q", p.Op)
			break
		}
		a.verbs = append(a.verbs, p.Verb)
		switch p.Verb {
		case "pause":
			a.pauses++
			if a.pauseErr != nil {
				return a.pauseErr
			}
			if a.holdPause {
				a.paused = true
				if a.combatOnPause {
					a.combat = true
				}
			}
			v = commanderReply{Sent: ".bot run pause", Reply: tbm.Reply{Kind: tbm.KindAck, OK: true, Line: "TBM:ACTION_ACK|run pause"}}
		case "resume":
			if a.resumeErr != nil {
				return a.resumeErr
			}
			a.paused = false
			v = commanderReply{Sent: ".bot run resume", Reply: tbm.Reply{Kind: tbm.KindAck, OK: true, Line: "TBM:ACTION_ACK|run resume"}}
		case "status":
			if a.statusLine != "" {
				v = commanderReply{Sent: ".bot run status", Reply: tbm.Reply{OK: true, Kind: tbm.KindAck, Lines: []string{a.statusLine}}}
				break
			}
			state := "running"
			if a.paused {
				state = "paused"
			}
			v = commanderReply{Sent: ".bot run status", Reply: tbm.Reply{OK: true, Kind: tbm.KindAck,
				Lines: []string{fmt.Sprintf("TBM:RUN|zul_gurub|%s|%d/6|here|move|rest|paused by .bot run pause", state, a.statusStep)}}}
		default:
			a.t.Errorf("unexpected run verb %q", p.Verb)
		}
	default:
		a.t.Errorf("unexpected RPC %s", method)
	}
	if out != nil {
		b, _ := json.Marshal(v)
		if err := json.Unmarshal(b, out); err != nil {
			a.t.Errorf("%s result: %v", method, err)
		}
	}
	return nil
}

func (a *prepullAgent) nameOf(g packet.GUID) string {
	if g == a.selfGUID {
		return a.selfName
	}
	for _, b := range a.bots {
		if b.GUID == g {
			return b.Name
		}
	}
	return g.String()
}

// prepullHoldVerbs is the run verbs minus the `status` confirmations: the pause/resume sequence the walk asked for.
func prepullHoldVerbs(a *prepullAgent) []string {
	out := make([]string, 0, len(a.verbs))
	for _, v := range a.verbs {
		if v != "status" {
			out = append(out, v)
		}
	}
	return out
}

// itoaU is a small decimal formatter for the sent log.
func itoaU(n uint32) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// prepullRows is a plan of 6 steps with boss rows at the steps given (1-based). Every row carries an anchor, so a boss
// row's arena can be read fresh.
func prepullRows(bossAt ...int) planexport.Rows {
	isBoss := map[int]bool{}
	for _, s := range bossAt {
		isBoss[s] = true
	}
	rows := planexport.Rows{}
	for step := 1; step <= 6; step++ {
		kind := planexport.KindMove
		if isBoss[step] {
			kind = planexport.KindBoss
		}
		rows.Steps = append(rows.Steps, planexport.StepRow{Kind: kind, Entry: 14834, Leg: 1, X: 100, Y: 200, Z: 10})
	}
	return rows
}

// newPrepullWalk is a walk with the flag set, a fake clock and the agent above. No phase runs: the tests call
// prepullAuraSendPending and prepullAuraFire themselves. The roster is the fake agent's six bots (mt, ot, rest), so
// every one of them is a hire the fire's coverage check expects a list for.
func newPrepullWalk(t *testing.T, a *prepullAgent, flags ...string) *walk {
	t.Helper()
	w := &walk{
		opts:   raidWalkOptions{prepullAura: stringList(flags), server: "m910q"},
		env:    walkEnv{now: func() time.Time { return a.clock }, sleep: func(_ context.Context, d time.Duration) error { a.advance(d); return nil }},
		agent:  a,
		outDir: t.TempDir(),
		plan:   &dungeon.Plan{Name: "zul_gurub"},
		rows:   prepullRows(2, 5),
		mt:     "Mtbot",
		ots:    []string{"Otbot"},
		rest:   []string{"Rank", "Flitz", "Zap", "Holi"},
	}
	if err := w.validatePrepullAura(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return w
}

// prepullRest is the out-of-combat rest of step step.
func prepullRest(step int) tbm.RunStatus {
	return tbm.RunStatus{State: tbm.RunRunning, Step: step, Kind: planexport.KindMove, Phase: "rest", Note: "resting"}
}

// prepullRestAuras is the flag a test starts from: one id per class role plus the world list.
var prepullRestAuras = []string{
	"role=tank:17626", "role=melee:17538", "role=ranged:18192", "role=caster:17628", "role=healer:17627",
	"role=all:22888",
}

// The gate: the auras go out before the pull, and never at or after it in the same page. The named case is the
// spec's TestPrepullAuraNotAtPull.
func TestPrepullAuraNotAtPull(t *testing.T) {
	for _, tc := range []struct {
		name  string
		st    tbm.RunStatus
		later bool
	}{
		{"pull phase", tbm.RunStatus{State: tbm.RunRunning, Step: 4, Kind: planexport.KindMove, Phase: "pull", Note: "pulling 11338"}, false},
		{"pulling note at rest", tbm.RunStatus{State: tbm.RunRunning, Step: 4, Kind: planexport.KindMove, Phase: "rest", Note: "pulling Hakkar"}, false},
		{"later pull line", prepullRest(4), true},
		{"move step, later pull line", tbm.RunStatus{State: tbm.RunRunning, Step: 4, Kind: planexport.KindMove, Phase: "walk", Note: "walking"}, true},
	} {
		a := newPrepullAgent(t)
		w := newPrepullWalk(t, a, prepullRestAuras...)
		if fired, err := w.prepullAuraSendPending(context.Background(), tc.st, tc.later); err != nil || fired {
			t.Errorf("%s: fired %v, err %v", tc.name, fired, err)
		}
		if len(a.sent) != 0 || a.pauses != 0 {
			t.Errorf("%s: sent %v pauses %d", tc.name, a.sent, a.pauses)
		}
	}
	// The control: the same rest, no pull line behind it, fires.
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, prepullRestAuras...)
	if fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false); err != nil || !fired {
		t.Fatalf("control: fired %v, err %v", fired, err)
	}
	if len(a.sent) == 0 {
		t.Fatal("the control sent nothing")
	}
}

// Walk 101b: the ZG Thekal plan's step before boss row 255 is a plain move, so the rest/gather-only gate never fired.
// A move step before a boss row now fires like a rest, pauses, verifies and resumes.
func TestPrepullAuraFiresOnAPlainMoveStepBeforeTheBoss(t *testing.T) {
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, "role=all:22888")
	st := tbm.RunStatus{State: tbm.RunRunning, Step: 4, Kind: planexport.KindMove, Phase: "walk", Note: "walking to Thekal"}
	fired, err := w.prepullAuraSendPending(context.Background(), st, false)
	if err != nil || !fired {
		t.Fatalf("move step: fired %v err %v", fired, err)
	}
	if len(a.sent) == 0 {
		t.Fatal("the move step before the boss row sent nothing")
	}
	if got := strings.Join(prepullHoldVerbs(a), " "); got != "pause resume" {
		t.Fatalf("verbs %q", got)
	}
	if !w.prepullAuraSent[5] {
		t.Fatal("the row the move step stands before was not recorded as done")
	}
	f := readPrepullAuraFile(t, w)
	if len(f.BossRows) != 1 || f.BossRows[0] != 5 {
		t.Fatalf("boss rows %v", f.BossRows)
	}
}

// The run passed the step before the boss too fast for the pause: the pause lands on the boss row itself, before the
// pull, so the auras still go out and verify.
func TestPrepullAuraAdvancedPauseAtTheBossRowFires(t *testing.T) {
	a := newPrepullAgent(t)
	a.statusStep = 5 // the pause confirms the run already stands on boss row 5
	w := newPrepullWalk(t, a, "role=all:22888")
	st := tbm.RunStatus{State: tbm.RunRunning, Step: 4, Kind: planexport.KindMove, Phase: "walk", Note: "walking to Thekal"}
	fired, err := w.prepullAuraSendPending(context.Background(), st, false)
	if err != nil || !fired {
		t.Fatalf("advanced pause: fired %v err %v", fired, err)
	}
	if len(a.sent) == 0 {
		t.Fatal("the advanced pause sent nothing")
	}
	if got := strings.Join(prepullHoldVerbs(a), " "); got != "pause resume" {
		t.Fatalf("verbs %q", got)
	}
	if !w.prepullAuraSent[5] {
		t.Fatal("the row was not recorded as done")
	}
}

// The boss row became current before any fire: still before the pull (begin, no combat, the boss's arena read fresh
// out of combat), so the walk pauses on the row, fires, verifies and resumes.
func TestPrepullAuraBossRowCurrentPausesFiresAndResumes(t *testing.T) {
	a := newPrepullAgent(t)
	a.statusStep = 5
	w := newPrepullWalk(t, a, "role=all:22888")
	// The tracker holds the boss unengaged: alive at 100 %, no target, no live target. The fresh read is the one that
	// decides, and it is clean too.
	w.bossSet = map[uint32]bool{14834: true}
	w.bossRows = map[uint32]hostileState{14834: {entry: 14834, hppct: 100, at: a.clock}}
	w.bossSeenAt = a.clock
	boss := tbm.RunStatus{State: tbm.RunRunning, Step: 5, Kind: planexport.KindBoss, Phase: "begin", Note: "starting"}
	fired, err := w.prepullAuraSendPending(context.Background(), boss, false)
	if err != nil || !fired {
		t.Fatalf("boss row current: fired %v err %v", fired, err)
	}
	// The gate reads the arena once, the confirmed pause on the boss row reads it again.
	if len(a.ttCmds) != 2 || a.ttCmds[0] != "q units 60 100 200 10 all" || a.ttCmds[1] != "q units 60 100 200 10 all" {
		t.Fatalf("arena reads %v", a.ttCmds)
	}
	if len(a.sent) == 0 {
		t.Fatal("the boss row sent nothing")
	}
	if got := strings.Join(prepullHoldVerbs(a), " "); got != "pause resume" {
		t.Fatalf("verbs %q", got)
	}
	if !w.prepullAuraSent[5] {
		t.Fatal("the row was not recorded as done")
	}
	f := readPrepullAuraFile(t, w)
	if len(f.BossRows) != 1 || f.BossRows[0] != 5 {
		t.Fatalf("boss rows %v", f.BossRows)
	}
}

// The boss row became current with the boss already engaged: the pull has started, so the walk fails as before with
// no aura sent and no pause. The arena's fresh read is what says so (the cached frame agrees here).
func TestPrepullAuraBossRowEngagedFails(t *testing.T) {
	a := newPrepullAgent(t)
	a.statusStep = 5
	a.arenaCombat = []string{"Hakkar"}
	w := newPrepullWalk(t, a, "role=all:22888")
	w.bossSet = map[uint32]bool{14834: true}
	w.bossRows = map[uint32]hostileState{14834: {entry: 14834, hppct: 40, target: 0x103, at: a.clock}}
	w.bossSeenAt = a.clock
	boss := tbm.RunStatus{State: tbm.RunRunning, Step: 5, Kind: planexport.KindBoss, Phase: "begin", Note: "starting"}
	fired, err := w.prepullAuraSendPending(context.Background(), boss, false)
	code, reason := stopInfo(err)
	if fired || code != exitRaidWalkFailed || !strings.Contains(reason, "boss row 5") || !strings.Contains(reason, "never sent") {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if len(a.ttCmds) != 1 {
		t.Fatalf("arena reads %v (want the gate's one, no pause)", a.ttCmds)
	}
	if len(a.sent) != 0 || len(a.verbs) != 0 {
		t.Fatalf("the engaged boss row sent %v / %v", a.sent, a.verbs)
	}
	if w.prepullAuraSent[5] {
		t.Fatal("the engaged row was recorded as done")
	}
}

// The boss-row fallback reads the boss FRESH, not from the cached hostile frames: the frame can still show him alive at
// 100 % with no target after the commander pulled, because the next view frame has not arrived. A stale frame is no
// evidence, and a read that cannot be made fails closed: neither sends an aura. This is the r1 HIGH.
func TestPrepullAuraBossRowFreshReadBeatsAStaleFrame(t *testing.T) {
	boss := tbm.RunStatus{State: tbm.RunRunning, Step: 5, Kind: planexport.KindBoss, Phase: "begin", Note: "starting"}
	for _, tc := range []struct {
		name   string
		setup  func(*prepullAgent)
		reason string
	}{
		{"engaged in the fresh read", func(a *prepullAgent) { a.arenaCombat = []string{"Hakkar"} }, "never sent"},
		{"the fresh read fails", func(a *prepullAgent) { a.arenaErr = errors.New("point_far") }, "arena read"},
	} {
		a := newPrepullAgent(t)
		tc.setup(a)
		a.statusStep = 5
		w := newPrepullWalk(t, a, "role=all:22888")
		// The cached frame is the r1 gate's evidence: seen now, alive at 100 %, no target, no live target — unengaged.
		w.bossSet = map[uint32]bool{14834: true}
		w.bossRows = map[uint32]hostileState{14834: {entry: 14834, hppct: 100, at: a.clock}}
		w.bossSeenAt = a.clock
		fired, err := w.prepullAuraSendPending(context.Background(), boss, false)
		code, reason := stopInfo(err)
		if fired || code != exitRaidWalkFailed || !strings.Contains(reason, "boss row 5") || !strings.Contains(reason, tc.reason) {
			t.Errorf("%s: fired %v, code %d, reason %q", tc.name, fired, code, reason)
		}
		if len(a.sent) != 0 || len(a.verbs) != 0 {
			t.Errorf("%s: sent %v / paused %v", tc.name, a.sent, a.verbs)
		}
		if w.prepullAuraSent[5] {
			t.Errorf("%s: the row was recorded as done", tc.name)
		}
	}
}

// A unit row without a combat-state field is no evidence of peace: a degraded or version-skewed toolkit reply still
// carries a valid envelope and no trunc flag, so ttUnitOf's tolerant InCombat=false read made the arena look clean and
// r2 fired the auras. The prepull arena read counts the unknown state as combat: no pause, no aura. This is the r2
// HIGH.
func TestPrepullAuraBossRowMissingCombatStateFails(t *testing.T) {
	a := newPrepullAgent(t)
	a.statusStep = 5
	a.arenaCBUnknown = []string{"Hakkar"} // the arena lists the boss, but its row has no `cb`
	w := newPrepullWalk(t, a, "role=all:22888")
	// The cached frames say unengaged too: the missing field is the only warning, and it must stop the fire.
	w.bossSet = map[uint32]bool{14834: true}
	w.bossRows = map[uint32]hostileState{14834: {entry: 14834, hppct: 100, at: a.clock}}
	w.bossSeenAt = a.clock
	boss := tbm.RunStatus{State: tbm.RunRunning, Step: 5, Kind: planexport.KindBoss, Phase: "begin", Note: "starting"}
	fired, err := w.prepullAuraSendPending(context.Background(), boss, false)
	code, reason := stopInfo(err)
	if fired || code != exitRaidWalkFailed || !strings.Contains(reason, "boss row 5") || !strings.Contains(reason, "never sent") {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if len(a.ttCmds) != 1 {
		t.Fatalf("arena reads %v (want the gate's one, no pause)", a.ttCmds)
	}
	if len(a.sent) != 0 || len(a.verbs) != 0 {
		t.Fatalf("an arena row without `cb` sent %v / %v", a.sent, a.verbs)
	}
	if w.prepullAuraSent[5] {
		t.Fatal("the row was recorded as done")
	}
}

// One fire per boss row: a second rest of the same step fires nothing, the next boss row's rest fires again (and only
// tops up, since the party still carries the auras).
func TestPrepullAuraOncePerBossRow(t *testing.T) {
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, prepullRestAuras...)
	ctx := context.Background()
	if fired, err := w.prepullAuraSendPending(ctx, prepullRest(4), false); err != nil || !fired {
		t.Fatalf("first fire: %v %v", fired, err)
	}
	first := len(a.sent)
	// 6 bots: 2 tanks (1 + world 1 = 2 each), rogue 2, hunter 2, mage 2, priest 2.
	if first != 12 {
		t.Fatalf("%d lines sent, want 12 (%v)", first, a.sent)
	}
	if got := strings.Join(prepullHoldVerbs(a), " "); got != "pause resume" {
		t.Fatalf("verbs %q", got)
	}
	if fired, err := w.prepullAuraSendPending(ctx, prepullRest(4), false); err != nil || fired {
		t.Fatalf("same step again: %v %v", fired, err)
	}
	if len(a.sent) != first {
		t.Fatalf("the same boss row sent again: %v", a.sent[first:])
	}
	// The next boss row's rest: the party still carries everything, so nothing is sent.
	a.statusStep = 1
	if fired, err := w.prepullAuraSendPending(ctx, prepullRest(1), false); err != nil || !fired {
		t.Fatalf("second boss row: %v %v", fired, err)
	}
	if len(a.sent) != first {
		t.Fatalf("a top-up with nothing missing sent %v", a.sent[first:])
	}
}

// A pause that does not hold is a blocking failure: no aura may go out on a run the walk cannot show paused. A
// refused pause, an ACK without the paused state, and a pull during the pause all stop the batch before the first
// `.aura`, and the resume still goes out. This is the r1 HIGH.
func TestPrepullAuraDoesNotFireWithoutAHeldPause(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		setup  func(*prepullAgent)
		reason string
	}{
		{"pause refused", func(a *prepullAgent) { a.pauseErr = errors.New("the run is busy") }, "run pause"},
		{"pause not held", func(a *prepullAgent) { a.holdPause = false }, "not paused"},
		{"combat during the pause", func(a *prepullAgent) { a.combatOnPause = true }, "combat"},
	} {
		a := newPrepullAgent(t)
		tc.setup(a)
		w := newPrepullWalk(t, a, prepullRestAuras...)
		fired, err := w.prepullAuraSendPending(ctx, prepullRest(4), false)
		code, reason := stopInfo(err)
		if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, tc.reason) {
			t.Fatalf("%s: fired %v, code %d, reason %q", tc.name, fired, code, reason)
		}
		if len(a.sent) != 0 {
			t.Fatalf("%s: %d aura(s) went out without a held pause: %v", tc.name, len(a.sent), a.sent)
		}
		if got := strings.Join(prepullHoldVerbs(a), " "); got != "pause resume" {
			t.Fatalf("%s: verbs %q, want pause then resume", tc.name, got)
		}
		if w.prepullAuraSent[5] {
			t.Fatalf("%s: the boss row was recorded as done", tc.name)
		}
	}
}

// A pull that starts mid-batch stops the auras: the party is re-read before every send, so no `.aura` follows the
// combat. The batch fails (exit 18) and the missing list is still on disk.
func TestPrepullAuraCombatMidBatchStopsTheAuras(t *testing.T) {
	a := newPrepullAgent(t)
	a.combatAfter = 1 // the first gm_aura lands; every later send would follow a pull
	w := newPrepullWalk(t, a, "role=all:22888,23768,15366")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "combat") {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if len(a.sent) != 1 {
		t.Fatalf("%d aura(s) went out, want 1 before the combat check stops the batch: %v", len(a.sent), a.sent)
	}
	if got := strings.Join(prepullHoldVerbs(a), " "); got != "pause resume" {
		t.Fatalf("verbs %q", got)
	}
	f := readPrepullAuraFile(t, w)
	seen := false
	for _, b := range f.Bots {
		if len(b.Missing) == 3 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("the missing list is not on disk: %+v", f.Bots)
	}
}

// The combat recheck watches the whole living party, not only the members the flag gives a list: a pull from a member
// the lists do not cover stops the batch before the first `.aura`. This is the r2 HIGH.
func TestPrepullAuraCombatOnAMemberWithoutAListStopsTheBatch(t *testing.T) {
	a := newPrepullAgent(t)
	a.combatBots[a.bots[3].GUID] = true // Flitz, a hunter: role=tank gives it no list
	w := newPrepullWalk(t, a, "role=tank:17626")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "combat") || !strings.Contains(reason, "Flitz") {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if len(a.sent) != 0 {
		t.Fatalf("%d aura(s) went out with a party member in combat: %v", len(a.sent), a.sent)
	}
	if got := strings.Join(prepullHoldVerbs(a), " "); got != "pause resume" {
		t.Fatalf("verbs %q", got)
	}
	if w.prepullAuraSent[5] {
		t.Fatal("the boss row was recorded as done")
	}
	f := readPrepullAuraFile(t, w)
	if len(f.BossRows) != 0 || len(f.Attempts) != 1 || f.Attempts[0].BossRow != 5 {
		t.Fatalf("boss rows %v, attempts %+v", f.BossRows, f.Attempts)
	}
}

// The commander is the one unit PartyInfo.Members leaves out, so a pull it started alone must stop the batch exactly
// like a member's: no `.aura` goes out and the row is an attempt, not a boss row. This is the r1 HIGH.
func TestPrepullAuraCommanderInCombatStopsTheBatch(t *testing.T) {
	a := newPrepullAgent(t)
	a.selfCombat = true // the commander pulled; every party bot is still out of combat
	w := newPrepullWalk(t, a, "role=tank:17626")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "combat") || !strings.Contains(reason, "Commander") {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if len(a.sent) != 0 {
		t.Fatalf("%d aura(s) went out with the commander in combat: %v", len(a.sent), a.sent)
	}
	if got := strings.Join(prepullHoldVerbs(a), " "); got != "pause resume" {
		t.Fatalf("verbs %q", got)
	}
	if w.prepullAuraSent[5] {
		t.Fatal("the boss row was recorded as done")
	}
	f := readPrepullAuraFile(t, w)
	if len(f.BossRows) != 0 || len(f.Attempts) != 1 || f.Attempts[0].BossRow != 5 {
		t.Fatalf("boss rows %v, attempts %+v", f.BossRows, f.Attempts)
	}
}

// A unit read the walk cannot complete fails closed: the batch stops before any `.aura` and the row is an attempt,
// not a boss row. The r2 code treated the unreadable rows as absent and reported a fire with nothing to send.
func TestPrepullAuraUnreadableUnitStopsTheBatch(t *testing.T) {
	a := newPrepullAgent(t)
	a.answers = false
	w := newPrepullWalk(t, a, "role=tank:17626")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "unit read") {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if len(a.sent) != 0 {
		t.Fatalf("%d aura(s) went out on an unreadable party: %v", len(a.sent), a.sent)
	}
	if w.prepullAuraSent[5] {
		t.Fatal("the boss row was recorded as done on an unreadable party")
	}
	f := readPrepullAuraFile(t, w)
	if len(f.BossRows) != 0 || len(f.Attempts) != 1 || !strings.Contains(f.Attempts[0].Reason, "unit read") {
		t.Fatalf("boss rows %v, attempts %+v", f.BossRows, f.Attempts)
	}
}

// A fire that misses beyond the tolerance is an attempt, not a boss row: boss_rows stays empty and the attempt
// carries the reason. This is the r2 MEDIUM behind the row appended before verification.
func TestPrepullAuraFailedAttemptIsNotABossRow(t *testing.T) {
	a := newPrepullAgent(t)
	a.drop[a.bots[3].GUID] = map[uint32]bool{22888: true} // Flitz never shows the id
	w := newPrepullWalk(t, a, "role=all:22888")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "beyond the tolerance of 0") {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if w.prepullAuraSent[5] {
		t.Fatal("the failed row was recorded as sent")
	}
	f := readPrepullAuraFile(t, w)
	if len(f.BossRows) != 0 {
		t.Fatalf("a failed attempt is on disk as a boss row: %v", f.BossRows)
	}
	if len(f.Attempts) != 1 || f.Attempts[0].BossRow != 5 || !strings.Contains(f.Attempts[0].Reason, "22888") {
		t.Fatalf("attempts %+v", f.Attempts)
	}
}

// A resume that fails after a successful fire still fails the walk (the raid would stand paused), and the fire's
// error wins over the resume's when both fail.
func TestPrepullAuraResumeFailures(t *testing.T) {
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, "role=all:22888")
	a.resumeErr = errors.New("no run is going")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "run resume") {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	// Already paused (not by the walk): no resume, and the confirmation still passes.
	a = newPrepullAgent(t)
	w = newPrepullWalk(t, a, "role=all:22888")
	st := prepullRest(4)
	st.State = tbm.RunPaused
	a.paused = true
	if fired, err := w.prepullAuraSendPending(context.Background(), st, false); err != nil || !fired {
		t.Fatalf("paused status: %v %v", fired, err)
	}
	if got := prepullHoldVerbs(a); len(got) != 0 {
		t.Fatalf("a paused status sent %v", got)
	}
}

// An aura whose selection does not hold lands on the commander: it is a miss, and with the default tolerance of 0
// even one missing bot fails the walk (exit 18).
func TestPrepullAuraOnTheCommanderIsAMiss(t *testing.T) {
	a := newPrepullAgent(t)
	a.applyToSelf = true
	w := newPrepullWalk(t, a, "role=all:22888,23768,15366")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if !strings.Contains(reason, "beyond the tolerance of 0") || !strings.Contains(reason, "Mtbot") {
		t.Fatalf("reason %q", reason)
	}
	// The resume still went out.
	if got := strings.Join(prepullHoldVerbs(a), " "); got != "pause resume" {
		t.Fatalf("verbs %q", got)
	}
	// The evidence is on disk, with the misses.
	f := readPrepullAuraFile(t, w)
	if len(f.Bots) == 0 {
		t.Fatal("prepull-auras.json holds no bot")
	}
	for _, b := range f.Bots {
		// Only the batch's first aura is ever sent before the miss is read back: the kernel stops that target there.
		if len(b.Missing) != 3 || len(b.Seen) != 0 || len(b.Sent) != 1 || b.Sent[0] != 22888 {
			t.Fatalf("%s: sent %v seen %v missing %v", b.Bot, b.Sent, b.Seen, b.Missing)
		}
	}
}

// By default every requested aura must verify: one missing id fails the walk, the file carries the missing list and
// the log never claims the bot carries its list. --prepull-aura-tolerance N is the only way to accept a miss, and
// the log then says so. A dropped id behind the batch's first aura still shows in the read-back.
func TestPrepullAuraTolerance(t *testing.T) {
	// One miss with the default tolerance of 0: the walk fails and names the id.
	a := newPrepullAgent(t)
	a.drop[a.bots[3].GUID] = map[uint32]bool{15366: true}
	w := newPrepullWalk(t, a, "role=all:22888,23768,15366")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "Flitz (missing 15366)") {
		t.Fatalf("one miss: fired %v code %d reason %q", fired, code, reason)
	}
	f := readPrepullAuraFile(t, w)
	byBot := map[string]prepullAuraRecord{}
	for _, b := range f.Bots {
		byBot[b.Bot] = b
	}
	if got := byBot["Flitz"]; len(got.Missing) != 1 || got.Missing[0] != 15366 || len(got.Seen) != 2 {
		t.Fatalf("Flitz's record %+v", got)
	}
	if f.Tolerance != 0 {
		t.Fatalf("tolerance %d in the file", f.Tolerance)
	}

	// The same miss with --prepull-aura-tolerance 1 passes; the missing list is still on disk and the log says the
	// bot misses an aura within the tolerance (it never claims every bot carries its list).
	a = newPrepullAgent(t)
	a.drop[a.bots[3].GUID] = map[uint32]bool{15366: true}
	w = newPrepullWalk(t, a, "role=all:22888,23768,15366")
	w.opts.prepullAuraTolerance = 1
	logFile, err := os.Create(filepath.Join(w.outDir, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	w.logF = logFile
	if fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false); err != nil || !fired {
		t.Fatalf("tolerated miss: fired %v err %v", fired, err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(w.outDir, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "miss auras within the tolerance of 1") || !strings.Contains(string(raw), "Flitz (missing 15366)") {
		t.Fatalf("run.log %s", raw)
	}
	if strings.Contains(string(raw), "carry their lists\n") {
		t.Fatalf("the log claims the lists while one is missing: %s", raw)
	}
	f = readPrepullAuraFile(t, w)
	if f.Tolerance != 1 || len(f.Bots) == 0 {
		t.Fatalf("file %+v", f)
	}
	for _, b := range f.Bots {
		if b.Bot == "Flitz" && (len(b.Missing) != 1 || b.Missing[0] != 15366) {
			t.Fatalf("Flitz's record %+v", b)
		}
	}

	// Two misses with a tolerance of 1 still fail: the bound is per bot, not per batch.
	a = newPrepullAgent(t)
	a.drop[a.bots[3].GUID] = map[uint32]bool{23768: true, 15366: true}
	w = newPrepullWalk(t, a, "role=all:22888,23768,15366")
	w.opts.prepullAuraTolerance = 1
	fired, err = w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason = stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "beyond the tolerance of 1") {
		t.Fatalf("two misses over the tolerance: fired %v code %d reason %q", fired, code, reason)
	}
}

// A row that did fire at the step before its boss row is done: the boss row's own status is no failure and sends
// nothing more (the fire happened exactly once).
func TestPrepullAuraBossRowFiredOnceIsNotRefired(t *testing.T) {
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, "role=all:22888")
	a.statusStep = 1
	if fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(1), false); err != nil || !fired {
		t.Fatalf("rest before row 2: fired %v err %v", fired, err)
	}
	if !w.prepullAuraSent[2] {
		t.Fatal("a verified row was not recorded as done")
	}
	before := len(a.sent)
	boss := tbm.RunStatus{State: tbm.RunRunning, Step: 2, Kind: planexport.KindBoss, Phase: "begin", Note: "starting"}
	fired, err := w.prepullAuraSendPending(context.Background(), boss, false)
	if fired || err != nil {
		t.Fatalf("fired row's boss status: fired %v err %v", fired, err)
	}
	if len(a.sent) != before {
		t.Fatalf("the boss status sent %v", a.sent[before:])
	}
}

// The flag's shape: repeatable, one value per role, and a refusal for a bad role, an unknown preset, spell id 0, a
// role named twice and a production server.
func TestParsePrepullAuraFlags(t *testing.T) {
	var stderr bytes.Buffer
	o, code := parseRaidWalkArgs([]string{"zul_gurub", "--prepull-aura", "role=tank:17626,17538", "--prepull-aura", "role=all:world,23768"}, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if len(o.prepullAura) != 2 || o.prepullAura[0] != "role=tank:17626,17538" || o.prepullAura[1] != "role=all:world,23768" {
		t.Fatalf("options %v", o.prepullAura)
	}
	// The tolerance is an explicit opt-in, 0 by default.
	if o.prepullAuraTolerance != 0 {
		t.Fatalf("default tolerance %d", o.prepullAuraTolerance)
	}
	o, code = parseRaidWalkArgs([]string{"zul_gurub", "--prepull-aura", "role=tank:17626", "--prepull-aura-tolerance", "2"}, &stderr)
	if code != exitOK || o.prepullAuraTolerance != 2 {
		t.Fatalf("tolerance options %v exit %d: %s", o.prepullAuraTolerance, code, stderr.String())
	}
	// The flag takes the sample file itself (a value that is not a role= list is a tb-prepull-auras file), alone or
	// beside the role= lists it does not name.
	o, code = parseRaidWalkArgs([]string{"zul_gurub", "--prepull-aura", "../../dungeons/prepull/zg-preraid.json"}, &stderr)
	if code != exitOK || len(o.prepullAura) != 1 || o.prepullAura[0] != "../../dungeons/prepull/zg-preraid.json" {
		t.Fatalf("file option %v exit %d: %s", o.prepullAura, code, stderr.String())
	}
	// Off by default.
	o, code = parseRaidWalkArgs([]string{"zul_gurub"}, &stderr)
	if code != exitOK || len(o.prepullAura) != 0 {
		t.Fatalf("default options %v exit %d", o.prepullAura, code)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"production server", []string{"zul_gurub", "--server", "tortoise", "--prepull-aura", "role=tank:17626"}, "test servers only"},
		{"bad role", []string{"zul_gurub", "--prepull-aura", "role=wizard:17626"}, "is not one of"},
		{"unknown preset", []string{"zul_gurub", "--prepull-aura", "role=tank:nope"}, "unknown preset"},
		{"id 0", []string{"zul_gurub", "--prepull-aura", "role=tank:0"}, "spell id 0"},
		{"twice", []string{"zul_gurub", "--prepull-aura", "role=tank:17626", "--prepull-aura", "role=tank:17538"}, "named twice"},
		{"no role", []string{"zul_gurub", "--prepull-aura", "17626"}, "no such file"},
		{"missing file", []string{"zul_gurub", "--prepull-aura", "no-such-prepull.json"}, "no such file"},
		{"the same file twice", []string{"zul_gurub", "--prepull-aura", "../../dungeons/prepull/zg-preraid.json", "--prepull-aura", "../../dungeons/prepull/zg-preraid.json"}, "named twice"},
		{"file and its role", []string{"zul_gurub", "--prepull-aura", "../../dungeons/prepull/zg-preraid.json", "--prepull-aura", "role=tank:17626"}, "named twice"},
		{"negative tolerance", []string{"zul_gurub", "--prepull-aura", "role=tank:17626", "--prepull-aura-tolerance", "-1"}, "0 or more"},
		{"tolerance alone", []string{"zul_gurub", "--prepull-aura-tolerance", "1"}, "needs --prepull-aura"},
	} {
		var errBuf bytes.Buffer
		_, code := parseRaidWalkArgs(tc.args, &errBuf)
		if code != exitUsage || !strings.Contains(errBuf.String(), tc.want) {
			t.Errorf("%s: exit %d, stderr %q (want %q)", tc.name, code, errBuf.String(), tc.want)
		}
	}
}

// A wipe recovery revives the raid, and a death strips every aura: the clear forgets the fired rows, so the rest
// before the boss row the recovery retries sends the lists again.
func TestPrepullAuraRefiresAfterAWipe(t *testing.T) {
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, "role=all:22888")
	ctx := context.Background()
	if fired, err := w.prepullAuraSendPending(ctx, prepullRest(4), false); err != nil || !fired {
		t.Fatalf("first fire: %v %v", fired, err)
	}
	first := len(a.sent)
	// The same rest, the party still carrying everything: nothing is sent.
	if fired, err := w.prepullAuraSendPending(ctx, prepullRest(4), false); err != nil || fired {
		t.Fatalf("the same row again: %v %v", fired, err)
	}
	// The wipe: every aura is gone, the recovery clears the fired rows, and the retried row's rest sends again.
	for g := range a.auras {
		a.auras[g] = map[uint32]bool{}
	}
	w.prepullAuraClear()
	if fired, err := w.prepullAuraSendPending(ctx, prepullRest(4), false); err != nil || !fired {
		t.Fatalf("after the wipe: %v %v", fired, err)
	}
	if len(a.sent) != first*2 {
		t.Fatalf("sent %v (want the lists twice: %d lines)", a.sent, first*2)
	}
}

// prepull-auras.json carries the bots, their roles and the ids sent and seen.
func TestPrepullAuraWritesTheFile(t *testing.T) {
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, "role=tank:17626", "role=melee:17538", "role=all:22888")
	if fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false); err != nil || !fired {
		t.Fatalf("fire: %v %v", fired, err)
	}
	f := readPrepullAuraFile(t, w)
	if len(f.Flag) != 3 || f.Flag[0] != "role=tank:17626" {
		t.Fatalf("flag %v", f.Flag)
	}
	if len(f.BossRows) != 1 || f.BossRows[0] != 5 {
		t.Fatalf("boss rows %v", f.BossRows)
	}
	byBot := map[string]prepullAuraRecord{}
	for _, b := range f.Bots {
		byBot[b.Bot] = b
	}
	if got := byBot["Mtbot"]; got.Role != "tank" || len(got.Seen) != 2 || len(got.Missing) != 0 {
		t.Fatalf("Mtbot %+v", got)
	}
	// Otbot is the second tank batch: its class says melee, the walk's own tanks win.
	if got := byBot["Otbot"]; got.Role != "tank" {
		t.Fatalf("Otbot %+v", got)
	}
	if got := byBot["Rank"]; got.Role != "melee" || len(got.Seen) != 2 {
		t.Fatalf("Rank %+v", got)
	}
	if got := byBot["Flitz"]; got.Role != "ranged" || len(got.Seen) != 1 {
		t.Fatalf("Flitz %+v", got)
	}
}

// Walk 101's bug: the walk's own `party` RPC is the PLAYER's group, and the hired bots raid without the player
// (in_group=false, 0 members), so the fire put nothing on anyone and still recorded boss row 691. The targets come
// from the commander's view instead, and every bot of the sample's role names carries its own list.
func TestPrepullAuraTargetsTheRaidsBotsNotThePlayersParty(t *testing.T) {
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, prepullRestAuras...)
	if fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false); err != nil || !fired {
		t.Fatalf("fire: fired %v err %v", fired, err)
	}
	want := map[string][]string{
		"Mtbot": {"Mtbot 17626", "Mtbot 22888"},
		"Otbot": {"Otbot 17626", "Otbot 22888"},
		"Rank":  {"Rank 17538", "Rank 22888"},
		"Flitz": {"Flitz 18192", "Flitz 22888"},
		"Zap":   {"Zap 17628", "Zap 22888"},
		"Holi":  {"Holi 17627", "Holi 22888"},
	}
	for _, b := range a.bots {
		var got []string
		for _, line := range a.sent {
			if strings.HasPrefix(line, b.Name+" ") {
				got = append(got, line)
			}
		}
		if strings.Join(got, ",") != strings.Join(want[b.Name], ",") {
			t.Fatalf("%s: sent %v, want %v", b.Name, got, want[b.Name])
		}
	}
	f := readPrepullAuraFile(t, w)
	if len(f.Bots) != 6 {
		t.Fatalf("prepull-auras.json holds %d bot(s), want the raid's 6: %+v", len(f.Bots), f.Bots)
	}
	if len(f.BossRows) != 1 || f.BossRows[0] != 5 || len(f.Attempts) != 0 {
		t.Fatalf("boss rows %v, attempts %+v", f.BossRows, f.Attempts)
	}
	roles := map[string]string{}
	for _, rec := range f.Bots {
		roles[rec.Bot] = rec.Role
	}
	for name, role := range map[string]string{"Mtbot": "tank", "Otbot": "tank", "Rank": "melee", "Flitz": "ranged", "Zap": "caster", "Holi": "healer"} {
		if roles[name] != role {
			t.Fatalf("%s's role %q, want %q (%v)", name, roles[name], role, roles)
		}
	}
}

// The flag takes the sample file itself (--prepull-aura <file.json>): its role lists are the walk's lists, and every
// bot of the raid carries its class's list plus the "all" list of the file, id for id.
func TestPrepullAuraSampleFileCoversTheRaid(t *testing.T) {
	const sample = "../../dungeons/prepull/zg-preraid.json"
	raw, err := os.ReadFile(sample)
	if err != nil {
		t.Fatalf("%s: %v", sample, err)
	}
	var doc struct {
		Roles map[string]struct {
			IDs []uint32 `json:"ids"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", sample, err)
	}
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, sample)
	if fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false); err != nil || !fired {
		t.Fatalf("fire: fired %v err %v", fired, err)
	}
	classes := map[string]string{"Mtbot": "tank", "Otbot": "tank", "Rank": "melee", "Flitz": "ranged", "Zap": "caster", "Holi": "healer"}
	total := 0
	for name, role := range classes {
		var want, got []string
		for _, id := range append(append([]uint32{}, doc.Roles[role].IDs...), doc.Roles["all"].IDs...) {
			want = append(want, fmt.Sprintf("%s %d", name, id))
		}
		for _, line := range a.sent {
			if strings.HasPrefix(line, name+" ") {
				got = append(got, line)
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s (%s): sent %v, want the file's %s + all lists %v", name, role, got, role, want)
		}
		total += len(want)
	}
	if len(a.sent) != total {
		t.Fatalf("%d lines sent, want %d: %v", len(a.sent), total, a.sent)
	}
	if f := readPrepullAuraFile(t, w); len(f.Bots) != 6 || len(f.BossRows) != 1 || len(f.Flag) != 1 || f.Flag[0] != sample {
		t.Fatalf("file %+v", f)
	}
}

// A fire whose lists are set but whose raid reads empty fails closed: exit 18 with the read named, no aura sent and no
// boss row written (walk 101 wrote one for 0 bots).
func TestPrepullAuraZeroTargetsFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*prepullAgent)
		reason string
	}{
		{"no view frame", func(a *prepullAgent) { a.noView = true }, "holds no frame"},
		{"no party bot", func(a *prepullAgent) {
			for _, b := range a.bots {
				a.hidden[b.Name] = true
			}
		}, "shows no party bot"},
		{"truncated view", func(a *prepullAgent) { a.viewTruncated = true }, "truncated"},
	} {
		a := newPrepullAgent(t)
		tc.setup(a)
		w := newPrepullWalk(t, a, "role=all:22888")
		fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
		code, reason := stopInfo(err)
		if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, tc.reason) {
			t.Fatalf("%s: fired %v, code %d, reason %q", tc.name, fired, code, reason)
		}
		if len(a.sent) != 0 {
			t.Fatalf("%s: %d aura(s) went out: %v", tc.name, len(a.sent), a.sent)
		}
		if w.prepullAuraSent[5] {
			t.Fatalf("%s: the boss row was recorded as done", tc.name)
		}
		f := readPrepullAuraFile(t, w)
		if len(f.BossRows) != 0 || len(f.Attempts) != 1 || f.Attempts[0].BossRow != 5 || !strings.Contains(f.Attempts[0].Reason, tc.reason) {
			t.Fatalf("%s: boss rows %v, attempts %+v", tc.name, f.BossRows, f.Attempts)
		}
	}
}

// Fewer bots than the walk hired must receive a list: a bot missing from the raid view and a dead bot both fail the
// fire, named, with no aura sent and no boss row.
func TestPrepullAuraShortRaidFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*prepullAgent)
		reason string
	}{
		{"bot missing from the view", func(a *prepullAgent) { a.hidden["Flitz"] = true }, "Flitz is not in the commander view"},
		{"dead bot", func(a *prepullAgent) { a.deadBots["Zap"] = true }, "Zap is dead"},
	} {
		a := newPrepullAgent(t)
		tc.setup(a)
		w := newPrepullWalk(t, a, "role=all:22888")
		fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
		code, reason := stopInfo(err)
		if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "1 of the walk's 6 hire(s)") || !strings.Contains(reason, tc.reason) {
			t.Fatalf("%s: fired %v, code %d, reason %q", tc.name, fired, code, reason)
		}
		if len(a.sent) != 0 {
			t.Fatalf("%s: %d aura(s) went out: %v", tc.name, len(a.sent), a.sent)
		}
		if w.prepullAuraSent[5] {
			t.Fatalf("%s: the boss row was recorded as done", tc.name)
		}
		if f := readPrepullAuraFile(t, w); len(f.BossRows) != 0 || len(f.Attempts) != 1 {
			t.Fatalf("%s: boss rows %v, attempts %+v", tc.name, f.BossRows, f.Attempts)
		}
	}
}

// A role word the raid's classes never produce leaves those bots without a list (walk 101's first suspicion): the
// fire fails naming them instead of recording a boss row the raid was not prepared for.
func TestPrepullAuraRoleMismatchFails(t *testing.T) {
	a := newPrepullAgent(t)
	w := newPrepullWalk(t, a, "role=tank:17626")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "4 of the walk's 6 hire(s)") ||
		!strings.Contains(reason, `Rank has no list (role "melee"`) {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if len(a.sent) != 0 {
		t.Fatalf("%d aura(s) went out to a partly listed raid: %v", len(a.sent), a.sent)
	}
	if w.prepullAuraSent[5] {
		t.Fatal("the row was recorded as done")
	}
}

func readPrepullAuraFile(t *testing.T, w *walk) prepullAuraFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.outDir, prepullAuraFileName))
	if err != nil {
		t.Fatalf("%v", err)
	}
	var f prepullAuraFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return f
}
