package tracecheck

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/trace"
)

const (
	gTank   = "0x11"
	gHealer = "0x12"
	gMage   = "0x13"
)

// build is a hand-made trace.
type build struct{ f *trace.File }

func newTrace(secs float64) *build {
	h := trace.Header{Lead: "Tank", Caps: []string{trace.CapFacing, trace.CapMoving}}
	h.Run, h.Src = "r-t", "agent"
	return &build{f: &trace.File{Header: h, Party: []trace.Member{{G: gTank, Name: "Tank", Lead: true}, {G: gHealer, Name: "Healer"}, {G: gMage, Name: "Mage"}},
		End: &trace.End{Reason: "done", Seconds: secs, Counters: map[string]any{}}}}
}

func (b *build) ev(t int64, name string, kv ...any) *build {
	d := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		d[kv[i].(string)] = kv[i+1]
	}
	b.f.Events = append(b.f.Events, trace.Event{Envelope: trace.Envelope{T: t}, Ev: name, D: d})
	return b
}

func (b *build) frame(t int64, via string, bots ...trace.Bot) *trace.Frame {
	b.f.Frames = append(b.f.Frames, trace.Frame{Envelope: trace.Envelope{T: t}, Via: via, Bots: bots})
	return &b.f.Frames[len(b.f.Frames)-1]
}

func get(t *testing.T, cs []trace.Check, id string) trace.Check {
	t.Helper()
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q", id)
	return trace.Check{}
}

func expect(t *testing.T, c trace.Check, verdict string) {
	t.Helper()
	if c.Verdict != verdict {
		t.Fatalf("%s: %s (value %g, threshold %g): %s", c.ID, c.Verdict, c.Value, c.Threshold, c.Detail)
	}
}

func bot(g string, lead bool, x float32) trace.Bot {
	return trace.Bot{G: g, Alive: true, State: "n", HP: 500, MaxHP: 500, X: x, Lead: lead}
}

func TestEveryCheckHasAnIDNameAndAVerdictOnAnEmptyTrace(t *testing.T) {
	cs := Score(newTrace(0).f, Defaults())
	if len(cs) != 11 {
		t.Fatalf("%d checks", len(cs))
	}
	seen := map[string]bool{}
	for _, c := range cs {
		if c.ID == "" || c.Name == "" || seen[c.ID] || (c.Verdict != trace.Pass && c.Verdict != trace.Fail && c.Verdict != trace.NA) {
			t.Errorf("%+v", c)
		}
		seen[c.ID] = true
	}
	// The ids the server's scorer has too keep its words.
	for _, id := range []string{"threat_off_tank", "deaths", "facing", "stuck"} {
		if !seen[id] {
			t.Errorf("no %s check", id)
		}
	}
	// Nothing happened: nothing failed.
	if v, failed := trace.Verdict(cs); v != trace.Pass || len(failed) != 0 {
		t.Fatalf("%s %v", v, failed)
	}
}

func TestRestShare(t *testing.T) {
	ok := newTrace(200).ev(10_000, "rest", "seconds", 30.0).ev(60_000, "rest", "seconds", 20.0)
	c := get(t, Score(ok.f, Defaults()), IDRestShare)
	expect(t, c, trace.Pass)
	if c.Value != 25 || c.Threshold != 40 || c.Unit != "%" || len(c.Evidence) != 2 || c.Count != 0 {
		t.Fatalf("%+v", c)
	}
	bad := newTrace(200).ev(10_000, "rest", "seconds", 60.0).ev(90_000, "rest", "seconds", 40.0)
	c = get(t, Score(bad.f, Defaults()), IDRestShare)
	expect(t, c, trace.Fail)
	if c.Count != 1 || c.FirstMS != 10_000 {
		t.Fatalf("%+v", c)
	}
	expect(t, get(t, Score(newTrace(30).ev(1, "rest", "seconds", 29.0).f, Defaults()), IDRestShare), trace.NA)
	// A threshold changed afterwards re-scores the same trace.
	cfg := Defaults()
	if err := cfg.Set(IDRestShare, 20); err != nil {
		t.Fatal(err)
	}
	expect(t, get(t, Score(ok.f, cfg), IDRestShare), trace.Fail)
}

func mana(g string, pw uint32) trace.Bot {
	return trace.Bot{G: g, Alive: true, HP: 500, MaxHP: 500, Power: pw, MaxPow: 400}
}

func TestStalePartyData(t *testing.T) {
	// The healer's mana stands still for 100 s while the tank's health moves: stale.
	bad := newTrace(100)
	for s := int64(0); s <= 100; s++ {
		tank := trace.Bot{G: gTank, Alive: true, HP: uint32(900 - s), MaxHP: 900, PType: 1}
		bad.frame(s*1000, "session", tank, mana(gHealer, 200))
	}
	c := get(t, Score(bad.f, Defaults()), IDStaleParty)
	expect(t, c, trace.Fail)
	if c.Value < 99 || !strings.Contains(c.Detail, "Healer") || len(c.Evidence) != 2 || c.Count != 1 {
		t.Fatalf("%+v", c)
	}
	// Mana that moves is fine; mana that stands still while nobody else changes is a quiet party, not stale data.
	ok := newTrace(100)
	quiet := newTrace(100)
	for s := int64(0); s <= 100; s++ {
		tank := trace.Bot{G: gTank, Alive: true, HP: uint32(900 - s), MaxHP: 900, PType: 1}
		ok.frame(s*1000, "session", tank, mana(gHealer, uint32(100+s)))
		quiet.frame(s*1000, "session", trace.Bot{G: gTank, Alive: true, HP: 900, MaxHP: 900, PType: 1}, mana(gHealer, 200))
	}
	expect(t, get(t, Score(ok.f, Defaults()), IDStaleParty), trace.Pass)
	expect(t, get(t, Score(quiet.f, Defaults()), IDStaleParty), trace.Pass)
	// A full mana bar that does not move is not stale; 59 s of stale is under the limit; no mana user: n/a.
	full := newTrace(100)
	short := newTrace(100)
	for s := int64(0); s <= 100; s++ {
		tank := trace.Bot{G: gTank, Alive: true, HP: uint32(900 - s), MaxHP: 900, PType: 1}
		full.frame(s*1000, "session", tank, mana(gHealer, 400))
		pw := uint32(200)
		if s > 59 {
			pw = uint32(200 + s)
		}
		short.frame(s*1000, "session", tank, mana(gHealer, pw))
	}
	expect(t, get(t, Score(full.f, Defaults()), IDStaleParty), trace.Pass)
	expect(t, get(t, Score(short.f, Defaults()), IDStaleParty), trace.Pass)
	rage := newTrace(10)
	rage.frame(0, "session", trace.Bot{G: gTank, Alive: true, PType: 1})
	expect(t, get(t, Score(rage.f, Defaults()), IDStaleParty), trace.NA)
}

// walkTrace walks the lead along x: each pair is (seconds, speed in yd/s); frames once a second.
func walkTrace(legs ...[2]float64) *build {
	b := newTrace(0)
	x, t := 0.0, int64(0)
	b.ev(0, "walk_start")
	b.frame(0, "session", bot(gTank, true, 0))
	for _, l := range legs {
		for s := 0; s < int(l[0]); s++ {
			x += l[1]
			t += 1000
			b.frame(t, "session", bot(gTank, true, float32(x)))
		}
	}
	b.ev(t, "walk_end", "ok", true)
	b.f.End.Seconds = float64(t) / 1000
	return b
}

func TestWalkStopAndGo(t *testing.T) {
	// 60 s at 7 yd/s with one 3 s stop halfway: about 0.3 stops per 100 yd.
	ok := walkTrace([2]float64{30, 7}, [2]float64{3, 0}, [2]float64{30, 7})
	c := get(t, Score(ok.f, Defaults()), IDWalkStops)
	expect(t, c, trace.Pass)
	if c.Value <= 0 || c.Value > 1 || !strings.Contains(c.Detail, "1 stops") {
		t.Fatalf("%+v", c)
	}
	// Two seconds of walking, three of standing, again and again: about seven stops per 100 yd.
	var legs [][2]float64
	for i := 0; i < 10; i++ {
		legs = append(legs, [2]float64{2, 7}, [2]float64{3, 0})
	}
	legs = append(legs, [2]float64{3, 7})
	bad := walkTrace(legs...)
	c = get(t, Score(bad.f, Defaults()), IDWalkStops)
	expect(t, c, trace.Fail)
	if c.Value < 4 || len(c.Evidence) == 0 || c.Count < 8 {
		t.Fatalf("%+v", c)
	}
	// Standing at the end of a walk is arriving, not a stop; a short walk and no walk are n/a.
	arrive := walkTrace([2]float64{30, 7}, [2]float64{3, 0})
	expect(t, get(t, Score(arrive.f, Defaults()), IDWalkStops), trace.Pass)
	if c := get(t, Score(arrive.f, Defaults()), IDWalkStops); !strings.Contains(c.Detail, "0 stops") {
		t.Fatalf("%+v", c)
	}
	expect(t, get(t, Score(walkTrace([2]float64{5, 7}).f, Defaults()), IDWalkStops), trace.NA)
	expect(t, get(t, Score(newTrace(10).f, Defaults()), IDWalkStops), trace.NA)
}

func mob(g string, hp int, top string) trace.Mob {
	m := trace.Mob{G: g, HPPct: hp, Combat: true}
	if top != "" {
		m.Threat = []trace.Threat{{G: top, Pct: 100}}
		m.Target = top // the mob is on the member that holds it
	}
	return m
}

// threatTrace is a 40 s traced fight with a pull from 0 s to 30 s: one feed frame a second from Tank, Healer and Mage, with the bots and
// mobs that tick builds for that second.
func threatTrace(tick func(s int64) (bots []trace.Bot, mobs []trace.Mob)) *build {
	b := newTrace(40)
	b.f.Header.Tank = "Tank"
	b.ev(0, "pull_start").ev(30_000, "pull_end")
	for s := int64(0); s <= 40; s++ {
		bots, mobs := tick(s)
		fr := b.frame(s*1000, "feed", bots...)
		fr.Mobs = mobs
	}
	return b
}

// mageHolds is the fixture's off-tank stretch: the mage is top of the mob from 10 s to 20 s.
func mageHolds(s int64) bool { return s >= 10 && s < 20 }

// threatMob is the fixture's mob: the holder is top with 100, and the tank is second with tankPct when that is above zero.
func threatMob(holder string, tankPct int) trace.Mob {
	m := trace.Mob{G: "0xf1", HPPct: 80, Combat: true, Target: holder, Threat: []trace.Threat{{G: holder, Pct: 100}}}
	if tankPct > 0 {
		m.Threat = append(m.Threat, trace.Threat{G: gTank, Pct: tankPct})
	}
	return m
}

func members() []trace.Bot {
	return []trace.Bot{bot(gTank, true, 0), bot(gHealer, false, 0), bot(gMage, false, 0)}
}

func TestThreatOffTheTank(t *testing.T) {
	mk := func(holder func(s int64) string) *build {
		b := newTrace(40)
		b.f.Header.Tank = "Tank"
		b.ev(0, "pull_start").ev(30_000, "pull_end")
		for s := int64(0); s <= 40; s++ {
			fr := b.frame(s*1000, "feed", members()...)
			fr.Mobs = []trace.Mob{mob("0xf1", 80, holder(s))}
		}
		return b
	}
	// The mage holds the mob from 10 s to 20 s.
	bad := mk(func(s int64) string {
		if s >= 10 && s < 20 {
			return gMage
		}
		return gTank
	})
	c := get(t, Score(bad.f, Defaults()), IDThreat)
	expect(t, c, trace.Fail)
	if c.Value != 9 || !strings.Contains(c.Detail, "Mage") || c.Evidence[0] != 10_000 || c.Count != 1 {
		t.Fatalf("%+v", c)
	}
	// Three seconds is under the limit; the tank all the time is a pass.
	blip := mk(func(s int64) string {
		if s == 10 || s == 11 || s == 12 {
			return gHealer
		}
		return gTank
	})
	expect(t, get(t, Score(blip.f, Defaults()), IDThreat), trace.Pass)
	expect(t, get(t, Score(mk(func(int64) string { return gTank }).f, Defaults()), IDThreat), trace.Pass)
	// Outside a pull the threat is not judged: the mage may hold it after the pull ended.
	late := mk(func(s int64) string {
		if s > 31 {
			return gMage
		}
		return gTank
	})
	expect(t, get(t, Score(late.f, Defaults()), IDThreat), trace.Pass)
	// The tank role in the party records names the tank when the header does not.
	role := mk(func(s int64) string {
		if s >= 10 && s < 20 {
			return gMage
		}
		return gTank
	})
	role.f.Header.Tank = ""
	role.f.Party[0].Role = "tank"
	expect(t, get(t, Score(role.f, Defaults()), IDThreat), trace.Fail)
	// No tank named: the one that holds most threat is it, so a mob held by the mage most of the time makes the mage the tank.
	infer := mk(func(s int64) string {
		if s < 25 {
			return gTank
		}
		return gMage
	})
	infer.f.Header.Tank = ""
	expect(t, get(t, Score(infer.f, Defaults()), IDThreat), trace.Fail)
	// Frames with no threat lists (a group run): n/a.
	none := mk(func(int64) string { return "" })
	expect(t, get(t, Score(none.f, Defaults()), IDThreat), trace.NA)
}

// A mob that keeps swinging at the tank is not threat lost to the mage on top of its list.
func TestThreatIgnoresAMobThatIsNotOnTheHolder(t *testing.T) {
	b := threatTrace(func(s int64) ([]trace.Bot, []trace.Mob) {
		m := threatMob(gTank, 0)
		if mageHolds(s) {
			m = threatMob(gMage, 0)
		}
		m.Target = gTank
		return members(), []trace.Mob{m}
	})
	c := get(t, Score(b.f, Defaults()), IDThreat)
	expect(t, c, trace.Pass)
	if c.Value != 0 || c.Count != 0 {
		t.Fatalf("%+v", c)
	}
}

// A mob with no target (polymorphed, fleeing, evading) is not on the mage either.
func TestThreatIgnoresAMobWithNoTarget(t *testing.T) {
	b := threatTrace(func(s int64) ([]trace.Bot, []trace.Mob) {
		m := threatMob(gTank, 0)
		if mageHolds(s) {
			m = threatMob(gMage, 0)
			m.Target = ""
		}
		return members(), []trace.Mob{m}
	})
	expect(t, get(t, Score(b.f, Defaults()), IDThreat), trace.Pass)
}

// A mob out of combat does not hit anyone.
func TestThreatIgnoresAMobThatIsNotInCombat(t *testing.T) {
	b := threatTrace(func(s int64) ([]trace.Bot, []trace.Mob) {
		m := threatMob(gTank, 0)
		if mageHolds(s) {
			m = threatMob(gMage, 0)
			m.Combat = false
		}
		return members(), []trace.Mob{m}
	})
	expect(t, get(t, Score(b.f, Defaults()), IDThreat), trace.Pass)
}

// A dead tank loses no threat: its death is not a span. The same stretch with the tank alive fails, so the frames are counted.
func TestThreatIgnoresTheFramesOfADeadTank(t *testing.T) {
	mk := func(dead func(s int64) bool) *build {
		return threatTrace(func(s int64) ([]trace.Bot, []trace.Mob) {
			bots := members()
			if dead(s) {
				bots[0].Alive = false
			}
			m := threatMob(gTank, 0)
			if mageHolds(s) {
				m = threatMob(gMage, 0)
			}
			return bots, []trace.Mob{m}
		})
	}
	// The tank dies at 10 s and is back at 20 s: the mage held the mob on the way out, not while the tank lay dead.
	dead := func(s int64) bool { return mageHolds(s) }
	expect(t, get(t, Score(mk(dead).f, Defaults()), IDThreat), trace.Pass)
	alive := get(t, Score(mk(func(int64) bool { return false }).f, Defaults()), IDThreat)
	expect(t, alive, trace.Fail)
	if alive.Value != 9 || alive.Count != 1 {
		t.Fatalf("%+v", alive)
	}
}

// The values are raw threat as a share of the top, and a tie between the mage and the tank is the tank's: the core keeps the tie in
// insertion order. One point below the mage is not a tie.
func TestThreatCountsATieWithTheTankAsTheTanks(t *testing.T) {
	mk := func(tankPct int) *build {
		return threatTrace(func(s int64) ([]trace.Bot, []trace.Mob) {
			m := threatMob(gTank, 0)
			if mageHolds(s) {
				m = threatMob(gMage, tankPct)
			}
			return members(), []trace.Mob{m}
		})
	}
	expect(t, get(t, Score(mk(100).f, Defaults()), IDThreat), trace.Pass)
	c := get(t, Score(mk(90).f, Defaults()), IDThreat)
	expect(t, c, trace.Fail)
	if c.Value != 9 || c.Count != 1 {
		t.Fatalf("%+v", c)
	}
}

// The detail carries the episodes per hour the tank spends in combat: one 9 s span in 41 s of combat is 88 per hour.
func TestThreatReportsTheRatePerTankCombatHour(t *testing.T) {
	b := threatTrace(func(s int64) ([]trace.Bot, []trace.Mob) {
		bots := members()
		bots[0].Combat = true
		m := threatMob(gTank, 0)
		if mageHolds(s) {
			m = threatMob(gMage, 0)
		}
		return bots, []trace.Mob{m}
	})
	c := get(t, Score(b.f, Defaults()), IDThreat)
	expect(t, c, trace.Fail)
	if !strings.Contains(c.Detail, "1 spans in 41 s of tank combat, 88 per tank-combat-hour") {
		t.Fatalf("%+v", c)
	}
}

func TestDeaths(t *testing.T) {
	expect(t, get(t, Score(newTrace(100).f, Defaults()), IDDeaths), trace.Pass)
	c := get(t, Score(newTrace(100).ev(5000, "death", "name", "Mage").f, Defaults()), IDDeaths)
	expect(t, c, trace.Fail)
	if c.Value != 1 || c.Count != 1 || !strings.Contains(c.Detail, "Mage") || c.Evidence[0] != 5000 || c.FirstMS != 5000 {
		t.Fatalf("%+v", c)
	}
	// The server's events name the bot by guid in a.
	c = get(t, Score(newTrace(100).ev(5000, "death", "a", gHealer).f, Defaults()), IDDeaths)
	if !strings.Contains(c.Detail, "Healer") {
		t.Fatalf("%+v", c)
	}
	w := get(t, Score(newTrace(100).ev(5000, "wipe").f, Defaults()), IDDeaths)
	expect(t, w, trace.Fail)
	if !strings.Contains(w.Detail, "1 wipes") {
		t.Fatalf("%+v", w)
	}
}

func orderTrace(sent, refused int, refusalEvents int) *build {
	b := newTrace(100)
	for i := 0; i < sent; i++ {
		reply := "ack"
		if i < refused {
			reply = "err"
		}
		b.ev(int64(i)*1000, "order", "reply", reply, "code", "unreachable")
	}
	for i := 0; i < refusalEvents; i++ {
		b.ev(int64(i)*1000, "refusal", "code", "dead")
	}
	return b
}

func TestOrderRefusals(t *testing.T) {
	c := get(t, Score(orderTrace(20, 2, 0).f, Defaults()), IDRefusals)
	expect(t, c, trace.Pass)
	if c.Value != 10 || !strings.Contains(c.Detail, "2 of 20") {
		t.Fatalf("%+v", c)
	}
	expect(t, get(t, Score(orderTrace(20, 8, 0).f, Defaults()), IDRefusals), trace.Fail)
	// A group run sees a refusal as its own event.
	expect(t, get(t, Score(orderTrace(10, 0, 5).f, Defaults()), IDRefusals), trace.Fail)
	expect(t, get(t, Score(orderTrace(2, 2, 0).f, Defaults()), IDRefusals), trace.NA)
}

func feedTrace(gapAt, gap int64) *build {
	b := newTrace(60)
	for s := int64(0); s <= 60; s++ {
		t := s * 1000
		if gapAt > 0 && s > gapAt {
			t += gap
		}
		b.frame(t, "feed", members()...)
	}
	return b
}

func TestFeedStalls(t *testing.T) {
	expect(t, get(t, Score(feedTrace(0, 0).f, Defaults()), IDFeedStalls), trace.Pass)
	c := get(t, Score(feedTrace(30, 14_000).f, Defaults()), IDFeedStalls)
	expect(t, c, trace.Fail)
	if c.Value != 15 || len(c.Evidence) != 1 || c.Evidence[0] != 45_000 || c.Count != 1 {
		t.Fatalf("%+v", c)
	}
	// A gap of 7 s is counted in the detail but is under the limit.
	c = get(t, Score(feedTrace(30, 6000).f, Defaults()), IDFeedStalls)
	expect(t, c, trace.Pass)
	if !strings.Contains(c.Detail, "1 gaps") {
		t.Fatalf("%+v", c)
	}
	grp := newTrace(10)
	grp.frame(0, "session", members()...)
	grp.frame(30_000, "session", members()...)
	expect(t, get(t, Score(grp.f, Defaults()), IDFeedStalls), trace.NA)
}

func TestGMShortcuts(t *testing.T) {
	c := get(t, Score(newTrace(10).f, Defaults()), IDGMShortcuts)
	expect(t, c, trace.Pass)
	if !strings.Contains(c.Detail, "player") {
		t.Fatalf("%+v", c)
	}
	c = get(t, Score(newTrace(10).ev(1000, "gm_shortcut", "kind", "revive").ev(2000, "gm_shortcut", "kind", "revive").ev(3000, "gm_shortcut", "kind", "teleport_to_entrance").f, Defaults()), IDGMShortcuts)
	expect(t, c, trace.Fail)
	if c.Value != 3 || c.Count != 3 || !strings.Contains(c.Detail, "revive x2") {
		t.Fatalf("%+v", c)
	}
	setup := newTrace(10)
	setup.f.Header.Options = map[string]any{"setup": []any{"go entrance"}}
	expect(t, get(t, Score(setup.f, Defaults()), IDGMShortcuts), trace.Fail)
}

func TestSetKnowsTheThresholds(t *testing.T) {
	cfg := Defaults()
	for _, id := range thresholdIDs {
		if err := cfg.Set(id, 1); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	if err := cfg.Set(IDDeaths, 1); err == nil || !strings.Contains(err.Error(), "rest_share") {
		t.Fatalf("%v", err)
	}
	if cfg.RestShareMax != 1 || cfg.FeedGapMax != 1 || cfg.FacingHoldS != 1 {
		t.Fatalf("%+v", cfg)
	}
}

// An old trace, as it was written, scores the same every time: the scorer reads nothing but the file.
func TestScoringIsAPureFunctionOfTheTrace(t *testing.T) {
	b := walkTrace([2]float64{30, 7}, [2]float64{3, 0}, [2]float64{30, 7}).ev(100, "death", "name", "x")
	a := fmt.Sprint(Score(b.f, Defaults()))
	if a != fmt.Sprint(Score(b.f, Defaults())) {
		t.Fatal("two scores differ")
	}
}

// stuckTrace walks a lead and a follower along x. The follower stands still from t=20 s for stuckFor seconds; the walk lasts 60 s.
func stuckTrace(stuckFor int, combat bool) *build {
	b := newTrace(60)
	b.ev(0, "walk_start")
	for s := int64(0); s <= 60; s++ {
		lead := bot(gTank, true, float32(s)*7)
		fx := float32(s)*7 - 30
		if s >= 20 && s < 20+int64(stuckFor) {
			fx = 20*7 - 30
		} else if s >= 20+int64(stuckFor) {
			fx = float32(s-int64(stuckFor))*7 - 30
		}
		f := bot(gMage, false, fx)
		f.Combat = combat
		b.frame(s*1000, "session", lead, f)
	}
	b.ev(60_000, "walk_end", "ok", true)
	return b
}

func TestStuckMembers(t *testing.T) {
	c := get(t, Score(stuckTrace(12, false).f, Defaults()), IDStuck)
	expect(t, c, trace.Fail)
	if c.Value != 1 || c.Count != 1 || !strings.Contains(c.Detail, "Mage (following) at 110 0 0") || len(c.Evidence) != 1 || c.Evidence[0] != 25_000 {
		t.Fatalf("%+v", c)
	}
	// Four seconds is under the limit; a member in combat is not expected to move; no walk is n/a.
	expect(t, get(t, Score(stuckTrace(4, false).f, Defaults()), IDStuck), trace.Pass)
	expect(t, get(t, Score(stuckTrace(12, true).f, Defaults()), IDStuck), trace.Pass)
	expect(t, get(t, Score(newTrace(10).f, Defaults()), IDStuck), trace.NA)
	cfg := Defaults()
	_ = cfg.Set(IDStuck, 3)
	expect(t, get(t, Score(stuckTrace(4, false).f, cfg), IDStuck), trace.Fail)
	// A member next to the lead is where it should be...
	near := newTrace(60)
	near.ev(0, "walk_start")
	for s := int64(0); s <= 30; s++ {
		near.frame(s*1000, "session", bot(gTank, true, 0), bot(gMage, false, 3))
	}
	// ...but the lead standing still in a walk is stuck.
	c = get(t, Score(near.f, Defaults()), IDStuck)
	expect(t, c, trace.Fail)
	if c.Value != 1 || !strings.Contains(c.Detail, "Tank (leading)") {
		t.Fatalf("%+v", c)
	}
}

func TestStuckDetectorReportsTheEndOfAStall(t *testing.T) {
	evs := DetectStuck(stuckTrace(12, false).f, 5)
	if len(evs) != 2 || evs[0].Ev != "stuck" || evs[0].T != 25_000 || evs[1].Ev != "unstuck" || evs[1].T != 33_000 || evs[1].Seconds != 13 {
		t.Fatalf("%+v", evs)
	}
	d := evs[0].Data("Mage")
	if d["name"] != "Mage" || d["a"] != gMage || d["x"] != float32(110) || d["lead"] != false || d["dist_lead"] != 65.0 {
		t.Fatalf("%v", d)
	}
}

// facingTrace has the mage fighting the mob that stands 4 yd east of it, turned fa degrees away, for n seconds.
func facingTrace(n int, fa int, moving bool) *build {
	b := newTrace(float64(n))
	for s := 0; s < n; s++ {
		m := bot(gMage, false, 0)
		m.Combat, m.Target, m.Moving = true, "0xf1", moving
		d := fa
		m.Facing = &d
		if moving {
			m.X = float32(s) * 7 // walking past
		}
		fr := b.frame(int64(s)*1000, "feed", bot(gTank, true, 100), m)
		fr.Mobs = []trace.Mob{{G: "0xf1", HPPct: 80, X: m.X + 4, Combat: true}}
	}
	return b
}

func TestFacing(t *testing.T) {
	// In melee range of its target and turned 150 degrees away for 10 s: fail. On the target: pass.
	c := get(t, Score(facingTrace(10, 150, false).f, Defaults()), IDFacing)
	expect(t, c, trace.Fail)
	if c.Value != 9 || c.Count != 1 || !strings.Contains(c.Detail, "Mage") || len(c.Evidence) != 1 {
		t.Fatalf("%+v", c)
	}
	expect(t, get(t, Score(facingTrace(10, 20, false).f, Defaults()), IDFacing), trace.Pass)
	// 90 degrees is the limit; a bot that is moving is repositioning, not standing turned away; two seconds of it is under the limit.
	expect(t, get(t, Score(facingTrace(10, 90, false).f, Defaults()), IDFacing), trace.Pass)
	expect(t, get(t, Score(facingTrace(10, 150, true).f, Defaults()), IDFacing), trace.Pass)
	expect(t, get(t, Score(facingTrace(3, 150, false).f, Defaults()), IDFacing), trace.Pass)
	// Not in melee range (the mob is 30 yd away): nothing to judge. A producer without the capability: n/a.
	far := facingTrace(10, 150, false)
	for i := range far.f.Frames {
		far.f.Frames[i].Mobs[0].X += 30
	}
	expect(t, get(t, Score(far.f, Defaults()), IDFacing), trace.NA)
	nocap := facingTrace(10, 150, false)
	nocap.f.Header.Caps = nil
	expect(t, get(t, Score(nocap.f, Defaults()), IDFacing), trace.NA)
}
