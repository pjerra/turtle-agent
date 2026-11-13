package main

// The raid-walk --prepull-aura test tool: at the pre-pull rest or gather of the step before each boss row the walk
// pauses the run, puts the flag's lists on the living party with the gm_aura RPC (a confirmed select then
// ".aura <id>"), reads them back into prepull-auras.json and resumes. Once per boss row, never at a pull, a pause is
// always resumed, and an aura on the commander is a miss like any other.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	sent    []string // "<bot name> <id>" per gm_aura call, in order
	verbs   []string // every `run <verb>` the commander asked for
	answers bool     // the unit RPC answers for the party (set by newPrepullAgent)
	pauses  int

	// pauseErr and resumeErr are the commander's own refusals (the RPC itself failing).
	pauseErr  error
	resumeErr error
	// applyToSelf makes every ".aura" land on the commander instead of the target: the selection did not hold.
	applyToSelf bool
	// drop is the ids a bot never shows after its ".aura" (an aura the server refuses, or a selection that did not hold).
	drop map[packet.GUID]map[uint32]bool
}

func newPrepullAgent(t *testing.T) *prepullAgent {
	t.Helper()
	a := &prepullAgent{t: t, clock: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), answers: true,
		classes: map[packet.GUID]uint8{}, auras: map[packet.GUID]map[uint32]bool{}, caster: map[uint32]bool{},
		drop: map[packet.GUID]map[uint32]bool{}}
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
		v = session.PartyInfo{InGroup: true, Members: a.bots}
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
			u := objects.Unit{GUID: g, Type: "player", Name: a.selfName}
			for id := range a.caster {
				u.Auras = append(u.Auras, objects.Aura{SpellID: id})
			}
			v = u
			break
		}
		if _, ok := a.auras[g]; !ok || !a.answers {
			return errors.New("unit: no such unit")
		}
		v = objects.Unit{GUID: g, Type: "player", Name: a.nameOf(g), Class: a.classes[g], Auras: a.aurasOf(g)}
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
			v = commanderReply{Sent: ".bot run pause"}
		case "resume":
			if a.resumeErr != nil {
				return a.resumeErr
			}
			v = commanderReply{Sent: ".bot run resume"}
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

// prepullRows is a plan of 6 steps with boss rows at the steps given (1-based).
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
		rows.Steps = append(rows.Steps, planexport.StepRow{Kind: kind, Entry: 14834, Leg: 1})
	}
	return rows
}

// newPrepullWalk is a walk with the flag set, a fake clock and the agent above. No phase runs: the tests call
// prepullAuraSendPending and prepullAuraFire themselves.
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

// The gate: the auras go out at the rest before a boss row, and never at or after the pull. The named case is the
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
		{"boss row itself", tbm.RunStatus{State: tbm.RunRunning, Step: 5, Kind: planexport.KindBoss, Phase: "begin", Note: "starting"}, false},
		{"no rest phase", tbm.RunStatus{State: tbm.RunRunning, Step: 4, Kind: planexport.KindMove, Phase: "walk", Note: "walking"}, false},
	} {
		a := newPrepullAgent(t)
		w := newPrepullWalk(t, a, prepullRestAuras...)
		fired, err := w.prepullAuraSendPending(context.Background(), tc.st, tc.later)
		if err != nil || fired {
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
	if got := a.verbs; len(got) != 2 || got[0] != "pause" || got[1] != "resume" {
		t.Fatalf("verbs %v", got)
	}
	if fired, err := w.prepullAuraSendPending(ctx, prepullRest(4), false); err != nil || fired {
		t.Fatalf("same step again: %v %v", fired, err)
	}
	if len(a.sent) != first {
		t.Fatalf("the same boss row sent again: %v", a.sent[first:])
	}
	// The next boss row's rest: the party still carries everything, so nothing is sent and no pause is needed.
	if fired, err := w.prepullAuraSendPending(ctx, prepullRest(1), false); err != nil || !fired {
		t.Fatalf("second boss row: %v %v", fired, err)
	}
	if len(a.sent) != first {
		t.Fatalf("a top-up with nothing missing sent %v", a.sent[first:])
	}
	// A boss row that became current with no rest before it is handled with one note and never paused.
	if fired, err := w.prepullAuraSendPending(ctx, tbm.RunStatus{State: tbm.RunRunning, Step: 2, Kind: planexport.KindBoss, Phase: "begin", Note: "starting"}, false); err != nil || fired {
		t.Fatalf("boss row: %v %v", fired, err)
	}
}

// A pause that the commander refuses still gets its resume, and the auras still go out.
func TestPrepullAuraResumesAfterAPauseError(t *testing.T) {
	a := newPrepullAgent(t)
	a.pauseErr = errors.New("the run is busy")
	w := newPrepullWalk(t, a, prepullRestAuras...)
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	if err != nil || !fired {
		t.Fatalf("fired %v, err %v", fired, err)
	}
	if len(a.sent) == 0 {
		t.Fatal("a refused pause must not skip the auras")
	}
	if got := a.verbs; len(got) != 2 || got[0] != "pause" || got[1] != "resume" {
		t.Fatalf("verbs %v, want pause then resume", got)
	}
	// A refused resume is a failed walk (the raid would stand paused).
	a = newPrepullAgent(t)
	w = newPrepullWalk(t, a, "role=all:22888")
	a.resumeErr = errors.New("no run is going")
	fired, err = w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "run resume") {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	// A status that was already paused sends no resume.
	a = newPrepullAgent(t)
	w = newPrepullWalk(t, a, "role=all:22888")
	st := prepullRest(4)
	st.State = tbm.RunPaused
	if fired, err := w.prepullAuraSendPending(context.Background(), st, false); err != nil || !fired {
		t.Fatalf("paused status: %v %v", fired, err)
	}
	if got := a.verbs; len(got) != 0 {
		t.Fatalf("a paused status sent %v", got)
	}
}

// An aura whose selection does not hold lands on the commander: it is a miss, and a bot missing more than two ids
// fails the walk (exit 18).
func TestPrepullAuraOnTheCommanderIsAMiss(t *testing.T) {
	a := newPrepullAgent(t)
	a.applyToSelf = true
	w := newPrepullWalk(t, a, "role=all:22888,23768,15366")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed {
		t.Fatalf("fired %v, code %d, reason %q", fired, code, reason)
	}
	if !strings.Contains(reason, "miss more than 2") || !strings.Contains(reason, "Mtbot") {
		t.Fatalf("reason %q", reason)
	}
	// The resume still went out.
	if got := a.verbs; len(got) != 2 || got[1] != "resume" {
		t.Fatalf("verbs %v", got)
	}
	// The evidence is on disk, with the misses.
	f := readPrepullAuraFile(t, w)
	if len(f.Bots) == 0 {
		t.Fatal("prepull-auras.json holds no bot")
	}
	for _, b := range f.Bots {
		if len(b.Missing) != 3 || len(b.Seen) != 0 || len(b.Sent) != 3 {
			t.Fatalf("%s: sent %v seen %v missing %v", b.Bot, b.Sent, b.Seen, b.Missing)
		}
	}
}

// Two missing ids are tolerated, three fail the walk: a bot's first aura is the one the batch checks, so a dropped
// id behind it still shows up in the read-back.
func TestPrepullAuraTwoMissesPassThreeFail(t *testing.T) {
	a := newPrepullAgent(t)
	// Flitz (the hunter) never shows 23768 and 15366: two misses, tolerated.
	flitz := a.bots[3].GUID
	a.drop[flitz] = map[uint32]bool{23768: true, 15366: true}
	w := newPrepullWalk(t, a, "role=all:22888,23768,15366")
	fired, err := w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	if !fired || err != nil {
		t.Fatalf("two misses: fired %v err %v", fired, err)
	}
	rec := w.prepullAuraBots["Flitz"]
	if rec == nil || len(rec.Missing) != 2 || len(rec.Seen) != 1 || rec.Seen[0] != 22888 {
		t.Fatalf("Flitz's record %+v", rec)
	}
	// Three: the batch's first aura never lands either, the batch stops and the walk fails naming the bot.
	a = newPrepullAgent(t)
	a.drop[a.bots[3].GUID] = map[uint32]bool{22888: true, 23768: true, 15366: true}
	w = newPrepullWalk(t, a, "role=all:22888,23768,15366")
	fired, err = w.prepullAuraSendPending(context.Background(), prepullRest(4), false)
	code, reason := stopInfo(err)
	if !fired || code != exitRaidWalkFailed || !strings.Contains(reason, "Flitz (missing") {
		t.Fatalf("three misses: fired %v code %d reason %q", fired, code, reason)
	}
	// The other bots are intact: only Flitz is named.
	if strings.Contains(reason, "Mtbot") {
		t.Fatalf("a carrying bot was named: %q", reason)
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
		{"no role", []string{"zul_gurub", "--prepull-aura", "17626"}, `want "role=<role>:<ids>"`},
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
