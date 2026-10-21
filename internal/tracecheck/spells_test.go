package tracecheck

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/trace"
)

// Spell ids as the real traces carry them.
const (
	spPolymorph  = 118
	spFrostbolt  = 837
	spShadowBolt = 686 // an enemy cast of 1.7 s
	spHeal       = 2054
)

// sb is a party bot in combat with an action; the power type is mana unless set.
func sb(g string, act, res string) trace.Bot {
	return trace.Bot{G: g, Alive: true, Combat: true, State: "c", HP: 500, MaxHP: 500, Power: 800, MaxPow: 1000, Action: act, Res: res}
}

// onParty is a mob in combat on the tank (so a hostile targets the party).
func onParty() trace.Mob {
	return trace.Mob{G: "0xf1", HPPct: 80, Combat: true, Target: gTank}
}

// spellTrace puts one frame per second of the given bots per second.
func spellTrace(secs int, per func(s int) ([]trace.Bot, []trace.Mob)) *build {
	b := newTrace(float64(secs))
	b.f.Header.IntervalMS = 1000
	for s := 0; s < secs; s++ {
		bots, mobs := per(s)
		fr := b.frame(int64(s)*1000, "feed", bots...)
		fr.Mobs = mobs
	}
	return b
}

func TestActionCategoryTable(t *testing.T) {
	for name, want := range map[string]string{
		"heal on party": CatHeal, "flash heal on party": CatHeal, "lesser heal on p": CatHeal, "renew on party": CatHeal,
		"power word_ shie": CatHeal, "frostbolt": CatDamage, "auto shot": CatDamage, "melee": CatDamage, "sinister strike": CatDamage,
		"polymorph": CatCC, "polymorph on cc": CatCC, "kick": CatInterrupt, "shield bash": CatInterrupt, "counterspell": CatInterrupt,
		"taunt": CatTaunt, "challenging shout": CatTaunt, "sunder armor": CatThreat, "demoralizing sho": CatThreat, "fade": CatDefense,
		"drink": CatRest, "food": CatRest, "stay": CatStay, "follow": CatMove, "reach melee": CatMove, "release pull hol": CatMove,
		"add gathering lo": CatAdmin, "check values": CatAdmin, "battle shout": CatBuff, "": CatOther, "no such action": CatOther,
	} {
		if got, _ := CategoryOf(name); got != want {
			t.Errorf("CategoryOf(%q) = %s, want %s", name, got, want)
		}
	}
	// Only a single-target crowd control tells by its target whether it landed.
	if _, single := CategoryOf("polymorph"); !single {
		t.Error("polymorph is single target")
	}
	if _, single := CategoryOf("frost nova"); single {
		t.Error("frost nova is not single target")
	}
}

func TestClassOfName(t *testing.T) {
	for name, want := range map[string]string{
		"frostbolt": "mage", "Polymorph": "mage", "sunder armor": "warrior", "Shoot Crossbow": "", "heal on party": "priest", "healing touch": "druid",
		"power word_ shie": "priest", "arcane shot": "hunter", "arcane missiles": "mage", "sinister strike": "rogue", "shadow bolt": "warlock",
		"seal of righteousness": "paladin", "lightning bolt": "shaman", "stay": "", "": "",
	} {
		if got := ClassOfName(name); got != want {
			t.Errorf("ClassOfName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSpellsNotRecordedOnATraceWithoutCastsOrActions(t *testing.T) {
	b := spellTrace(10, func(s int) ([]trace.Bot, []trace.Mob) { return members(), nil })
	c := get(t, Score(b.f, Defaults()), IDSpells)
	expect(t, c, trace.NA)
	if !strings.Contains(c.Detail, "not recorded") {
		t.Fatal(c.Detail)
	}
	if l := Spells(b.f, Defaults()).Lines(); len(l) != 1 || !strings.Contains(l[0], "not recorded") {
		t.Fatalf("%q", l)
	}
	// An empty trace does not crash either.
	expect(t, get(t, Score(newTrace(0).f, Defaults()), IDSpells), trace.NA)
}

// An older trace has the action name but no verdict letter: everything that needs the letter says "not recorded", the counts that
// do not still work, and nothing fails.
func TestSpellsOnAnOlderTraceWithoutVerdicts(t *testing.T) {
	b := spellTrace(40, func(s int) ([]trace.Bot, []trace.Mob) {
		m := sb(gMage, "frostbolt", "")
		m.Cast = spFrostbolt
		tank := sb(gTank, "taunt", "")
		return []trace.Bot{tank, m}, []trace.Mob{onParty()}
	})
	r := Spells(b.f, Defaults())
	if r.VerdictSeen || r.Idle.Recorded || !r.ActsSeen || !r.CastsSeen {
		t.Fatalf("%+v", r)
	}
	if r.Taunts.Done != 1 || r.Taunts.Tried != 1 {
		t.Fatalf("taunts %+v", r.Taunts)
	}
	text := strings.Join(r.Lines(), "\n")
	for _, want := range []string{"action verdicts not recorded", "idle share: not recorded", "verdict not recorded"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	c := get(t, Score(b.f, Defaults()), IDSpells)
	expect(t, c, trace.Pass)
	if !strings.Contains(c.Detail, "verdict letters not recorded") {
		t.Fatal(c.Detail)
	}
}

func TestSpellsCountCastsPerClassFromSpellIDs(t *testing.T) {
	// The mage casts frostbolt in seconds 0-1 (one cast: the same id twice in a row), nothing in 2, frostbolt again in 3, polymorph in 5.
	cast := map[int]uint32{0: spFrostbolt, 1: spFrostbolt, 3: spFrostbolt, 5: spPolymorph}
	b := spellTrace(8, func(s int) ([]trace.Bot, []trace.Mob) {
		m := sb(gMage, "", "")
		m.Cast = cast[s]
		h := sb(gHealer, "", "")
		if s == 2 {
			h.Cast = spHeal
		}
		return []trace.Bot{m, h}, nil
	})
	r := Spells(b.f, Defaults())
	got := map[string]ClassSpell{}
	for _, c := range r.Classes {
		got[c.Class] = c
	}
	mage := got["mage"]
	if mage.Casts != 3 || len(mage.TopCasts) != 2 || mage.TopCasts[0].Name != "Frostbolt" || mage.TopCasts[0].N != 2 || mage.TopCasts[1].Name != "Polymorph" {
		t.Fatalf("mage %+v", mage)
	}
	if got["priest"].Casts != 1 || got["priest"].Bots[0] != "Healer" {
		t.Fatalf("priest %+v", got)
	}
}

func TestSpellsKeepTheVerdictLettersAndReasonsPerClass(t *testing.T) {
	b := spellTrace(10, func(s int) ([]trace.Bot, []trace.Mob) {
		m := sb(gMage, "frostbolt", "O")
		if s%2 == 1 {
			m = sb(gMage, "frostbolt", "F")
			m.Why = "oom"
		}
		return []trace.Bot{m}, nil
	})
	r := Spells(b.f, Defaults())
	if !r.VerdictSeen || len(r.Classes) != 1 {
		t.Fatalf("%+v", r)
	}
	c := r.Classes[0]
	if c.Verdicts["O"] != 5 || c.Verdicts["F"] != 5 || len(c.Whys) != 1 || c.Whys[0].Name != "oom" || c.Whys[0].N != 5 {
		t.Fatalf("%+v", c)
	}
	if text := strings.Join(r.Lines(), "\n"); !strings.Contains(text, "why not: oom 5") || !strings.Contains(text, "O 50%, F 50%") {
		t.Fatalf("\n%s", text)
	}
}

func idleTrace(idleSecs int) *build {
	// The healer is engaged for 40 s; it is idle (verdict I) for idleSecs of them and acts (O) otherwise.
	return spellTrace(40, func(s int) ([]trace.Bot, []trace.Mob) {
		h := sb(gHealer, "heal on party", "O")
		if s >= 10 && s < 10+idleSecs {
			h = sb(gHealer, "heal on party", "I")
			h.Why = "oom"
		}
		return []trace.Bot{h}, []trace.Mob{onParty()}
	})
}

func TestSpellsIdleShare(t *testing.T) {
	// 20 idle seconds of 40 engaged: 50 %, over the 15 % limit, a warning that does not fail the check.
	b := idleTrace(20)
	r := Spells(b.f, Defaults())
	if !r.Idle.Recorded || r.Idle.Engaged != 40 || r.Idle.Idle != 20 || r.Idle.Pct != 50 || r.Idle.ByClass["priest"] != 50 {
		t.Fatalf("%+v", r.Idle)
	}
	if len(r.Warnings) != 1 || !strings.HasPrefix(r.Warnings[0], "idle_share") {
		t.Fatalf("%q", r.Warnings)
	}
	c := get(t, Score(b.f, Defaults()), IDSpells)
	expect(t, c, trace.Pass)
	if c.Count != 1 || !strings.Contains(c.Detail, "WARN idle_share") {
		t.Fatalf("%+v", c)
	}
	// The limit is a threshold: --set idle_share=60 takes the warning away.
	cfg := Defaults()
	if err := cfg.Set(IDIdleShare, 60); err != nil {
		t.Fatal(err)
	}
	if r := Spells(b.f, cfg); len(r.Warnings) != 0 || r.Idle.Pct != 50 {
		t.Fatalf("%+v", r)
	}
	// Three idle seconds are a pause, not an idle run (it takes four).
	if r := Spells(idleTrace(3).f, Defaults()); r.Idle.Idle != 0 || len(r.Warnings) != 0 {
		t.Fatalf("%+v", r.Idle)
	}
	// With no hostile on the party the healer is not engaged, so waiting is not idle.
	quiet := spellTrace(40, func(s int) ([]trace.Bot, []trace.Mob) { return []trace.Bot{sb(gHealer, "stay", "I")}, nil })
	if r := Spells(quiet.f, Defaults()); r.Idle.Engaged != 0 || r.Idle.Idle != 0 {
		t.Fatalf("%+v", r.Idle)
	}
}

func TestSpellsPowerAtDeath(t *testing.T) {
	// The healer fights 0-19 s with mana falling from 90 % to 10 %, dies at 20 s; it is revived and fights again from 40 s at 60 %,
	// dying at 50 s at 40 %: each death reads its own fight.
	b := spellTrace(60, func(s int) ([]trace.Bot, []trace.Mob) {
		h := sb(gHealer, "heal on party", "O")
		switch {
		case s < 20:
			h.Power = uint32(900 - s*40)
		case s < 40:
			h.Alive, h.Combat, h.State, h.HP = false, false, "d", 0
		case s < 50:
			h.Power = 600 - uint32(s-40)*20
		default:
			h.Alive, h.Combat, h.State, h.HP = false, false, "d", 0
		}
		return []trace.Bot{h}, nil
	})
	b.ev(20_000, "death", "a", gHealer, "name", "Healer").ev(50_000, "death", "a", gHealer, "name", "Healer")
	r := Spells(b.f, Defaults())
	if len(r.Deaths) != 2 {
		t.Fatalf("%+v", r.Deaths)
	}
	d1, d2 := r.Deaths[0], r.Deaths[1]
	if !d1.Seen || d1.Power != "mana" || d1.AtDeath != 14 || d1.AtStart != 90 || d1.Min != 14 || d1.Class != "priest" {
		t.Fatalf("first %+v", d1)
	}
	if d2.AtDeath != 42 || d2.AtStart != 60 || d2.Min != 42 {
		t.Fatalf("second %+v", d2)
	}
	if r.DryBots != 0 { // 14 % is the lowest: nobody went under 10 %
		t.Fatalf("dry %d", r.DryBots)
	}
	if text := strings.Join(r.Lines(), "\n"); !strings.Contains(text, "power at death: Healer (priest) at 20 s: mana 14 % (fight start 90 %, lowest 14 %)") {
		t.Fatalf("\n%s", text)
	}
	// A death with no frame of the bot before it says so.
	n := newTrace(5)
	n.frame(1000, "feed", sb(gMage, "frostbolt", "O"))
	n.ev(2000, "death", "a", "0x99", "name", "Ghost")
	if r := Spells(n.f, Defaults()); len(r.Deaths) != 1 || r.Deaths[0].Seen {
		t.Fatalf("%+v", r.Deaths)
	}
}

// mobCasting is a hostile casting spell id sp.
func mobCasting(sp uint32) trace.Mob {
	return trace.Mob{G: "0xf2", HPPct: 90, Combat: true, Target: gTank, Cast: sp}
}

func TestSpellsInterruptCover(t *testing.T) {
	// Six enemy casts, one frame each, ten seconds apart; the rogue kicks (verdict O) on the first two, fails a kick on the third.
	kick := map[int]string{10: "O", 20: "O", 30: "F"}
	b := spellTrace(80, func(s int) ([]trace.Bot, []trace.Mob) {
		rg := sb(gMage, "sinister strike", "O")
		if res, ok := kick[s]; ok {
			rg = sb(gMage, "kick", res)
		}
		var mobs []trace.Mob
		if s%10 == 0 && s >= 10 {
			mobs = []trace.Mob{mobCasting(spShadowBolt)}
		} else {
			mobs = []trace.Mob{onParty()}
		}
		return []trace.Bot{rg}, mobs
	})
	r := Spells(b.f, Defaults())
	in := r.Interrupt
	if in.Casts != 7 || in.Covered != 2 || in.CutShort != 2 || in.Pct != 28.6 || len(in.TopMissed) != 1 || in.TopMissed[0].Name != "Shadow Bolt" || in.TopMissed[0].N != 5 {
		t.Fatalf("%+v", in)
	}
	if len(r.Warnings) != 1 || !strings.HasPrefix(r.Warnings[0], "interrupt_cover") {
		t.Fatalf("%q", r.Warnings)
	}
	cfg := Defaults()
	if err := cfg.Set(IDInterruptCover, 20); err != nil {
		t.Fatal(err)
	}
	if r := Spells(b.f, cfg); len(r.Warnings) != 0 {
		t.Fatalf("%q", r.Warnings)
	}
	// A trace with fewer than five enemy casts has no rate to warn about.
	few := spellTrace(30, func(s int) ([]trace.Bot, []trace.Mob) {
		if s == 10 {
			return []trace.Bot{sb(gMage, "sinister strike", "O")}, []trace.Mob{mobCasting(spShadowBolt)}
		}
		return []trace.Bot{sb(gMage, "sinister strike", "O")}, []trace.Mob{onParty()}
	})
	if r := Spells(few.f, Defaults()); r.Interrupt.Casts != 1 || len(r.Warnings) != 0 {
		t.Fatalf("%+v %q", r.Interrupt, r.Warnings)
	}
}

func TestSpellsInterruptThatCutACastShort(t *testing.T) {
	// A 1.7 s cast seen for 1 frame (1 s) with a kick in the window was cut short; seen for 3 frames it ran its time.
	mk := func(frames int) *build {
		return spellTrace(20, func(s int) ([]trace.Bot, []trace.Mob) {
			act := sb(gMage, "sinister strike", "O")
			if s == 5 {
				act = sb(gMage, "kick", "O")
			}
			if s >= 5 && s < 5+frames {
				return []trace.Bot{act}, []trace.Mob{mobCasting(spShadowBolt)}
			}
			return []trace.Bot{act}, []trace.Mob{onParty()}
		})
	}
	if in := Spells(mk(1).f, Defaults()).Interrupt; in.Covered != 1 || in.CutShort != 1 {
		t.Fatalf("%+v", in)
	}
	if in := Spells(mk(3).f, Defaults()).Interrupt; in.Covered != 1 || in.CutShort != 0 {
		t.Fatalf("%+v", in)
	}
}

func TestSpellsTauntsAreCountedPerBot(t *testing.T) {
	// Three taunts by the tank: two executed (O), one refused (I); a taunt repeated in consecutive frames is one.
	acts := map[int][2]string{5: {"taunt", "O"}, 6: {"taunt", "O"}, 15: {"challenging shout", "O"}, 25: {"taunt", "I"}}
	b := spellTrace(40, func(s int) ([]trace.Bot, []trace.Mob) {
		t := sb(gTank, "sunder armor", "O")
		if a, ok := acts[s]; ok {
			t = sb(gTank, a[0], a[1])
		}
		return []trace.Bot{t}, []trace.Mob{onParty()}
	})
	r := Spells(b.f, Defaults())
	if r.Taunts.Done != 2 || r.Taunts.Tried != 3 || len(r.Taunts.ByBot) != 1 || r.Taunts.ByBot[0].Name != "Tank" || r.Taunts.ByBot[0].N != 2 {
		t.Fatalf("%+v", r.Taunts)
	}
}

func TestSpellsCrowdControlSuccess(t *testing.T) {
	// Four polymorph actions: the first puts a cc flag on a mob; the second does nothing; the third is a failed cast (F, not counted);
	// the fourth is cast on a mob that was already crowd-controlled before it, which is no new flag.
	poly := map[int]string{10: "O", 20: "O", 30: "F", 40: "O"}
	b := spellTrace(60, func(s int) ([]trace.Bot, []trace.Mob) {
		m := sb(gMage, "frostbolt", "O")
		if res, ok := poly[s]; ok {
			m = sb(gMage, "polymorph", res)
		}
		mob := onParty()
		if s >= 11 && s <= 14 { // the flag appears a second after the first polymorph
			mob.CC = true
		}
		other := trace.Mob{G: "0xf3", HPPct: 90, Combat: true, Target: gTank, CC: s >= 38} // already flagged when the fourth is cast
		return []trace.Bot{m}, []trace.Mob{mob, other}
	})
	r := Spells(b.f, Defaults())
	cc := r.CC
	if cc.Casts != 3 || cc.Evaluable != 3 || cc.Landed != 1 || cc.Pct != 33.3 || cc.BySpell[0].Name != "polymorph" {
		t.Fatalf("%+v", cc)
	}
	if len(r.Warnings) != 1 || !strings.HasPrefix(r.Warnings[0], "cc_success") {
		t.Fatalf("%q", r.Warnings)
	}
	cfg := Defaults()
	if err := cfg.Set(IDCCSuccess, 30); err != nil {
		t.Fatal(err)
	}
	if r := Spells(b.f, cfg); len(r.Warnings) != 0 {
		t.Fatalf("%q", r.Warnings)
	}
	// An area crowd control (frost nova) is not judged by the flag.
	n := spellTrace(20, func(s int) ([]trace.Bot, []trace.Mob) {
		return []trace.Bot{sb(gMage, "frost nova", "O")}, []trace.Mob{onParty()}
	})
	if r := Spells(n.f, Defaults()); r.CC.Casts != 0 {
		t.Fatalf("%+v", r.CC)
	}
}

// The spells check reports and warns; it never fails a run by itself, whatever the numbers.
func TestSpellsNeverFailsARun(t *testing.T) {
	b := idleTrace(40)
	b.ev(5000, "note", "text", "x")
	cfg := Defaults()
	cfg.IdleSharePct, cfg.InterruptCoverPct, cfg.CCSuccessPct = 0, 100, 100
	cs := Score(b.f, cfg)
	c := get(t, cs, IDSpells)
	if c.Verdict != trace.Pass || c.Count == 0 {
		t.Fatalf("%+v", c)
	}
	v, failed := trace.Verdict([]trace.Check{c})
	if v != trace.Pass || len(failed) != 0 {
		t.Fatalf("%s %v", v, failed)
	}
}

func TestSpellsSetKnowsTheThreeLimits(t *testing.T) {
	cfg := Defaults()
	for id, field := range map[string]*float64{IDIdleShare: &cfg.IdleSharePct, IDInterruptCover: &cfg.InterruptCoverPct, IDCCSuccess: &cfg.CCSuccessPct} {
		if err := cfg.Set(id, 7); err != nil || *field != 7 {
			t.Errorf("%s: %v %g", id, err, *field)
		}
	}
	if d := Defaults(); d.IdleSharePct != 15 || d.InterruptCoverPct != 30 || d.CCSuccessPct != 50 {
		t.Fatalf("%+v", d)
	}
}

// A trace written before the verdict was kept reads, and one with the verdict writes and reads it back.
func TestBotVerdictFieldsAreAdditive(t *testing.T) {
	old := `{"g":"0x1","al":true,"cb":true,"st":"c","hp":1,"mhp":2,"pw":3,"mpw":4,"pt":0,"x":0,"y":0,"z":0,"mv":false,"cs":837,"act":"frostbolt"}`
	var b trace.Bot
	if err := json.Unmarshal([]byte(old), &b); err != nil || b.Res != "" || b.Why != "" || b.Action != "frostbolt" {
		t.Fatalf("%v %+v", err, b)
	}
	b.Res, b.Why = "F", "oom"
	out, _ := json.Marshal(b)
	if !strings.Contains(string(out), `"res":"F"`) || !strings.Contains(string(out), `"why":"oom"`) {
		t.Fatalf("%s", out)
	}
	b.Res, b.Why = "", ""
	out, _ = json.Marshal(b)
	if strings.Contains(string(out), `"res"`) || strings.Contains(string(out), `"why"`) {
		t.Fatalf("empty fields are left out: %s", out)
	}
}

// An interrupt counts when it runs within a second before the cast's first frame or after its last, not further away.
func TestSpellsInterruptWindowIsOneSecond(t *testing.T) {
	at := func(kickAt int) int {
		b := spellTrace(30, func(s int) ([]trace.Bot, []trace.Mob) {
			act := sb(gMage, "sinister strike", "O")
			if s == kickAt {
				act = sb(gMage, "kick", "O")
			}
			if s == 10 {
				return []trace.Bot{act}, []trace.Mob{mobCasting(spShadowBolt)}
			}
			return []trace.Bot{act}, []trace.Mob{onParty()}
		})
		return Spells(b.f, Defaults()).Interrupt.Covered
	}
	for kickAt, want := range map[int]int{9: 1, 10: 1, 11: 1, 8: 0, 12: 0} {
		if got := at(kickAt); got != want {
			t.Errorf("kick at %d s: covered %d, want %d", kickAt, got, want)
		}
	}
}
