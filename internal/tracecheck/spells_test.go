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

// An older trace has the action name but no verdict letter: everything that needs the letter says "not recorded" or counts the action
// as unknown (never as executed), and nothing fails.
func TestSpellsOnAnOlderTraceWithoutVerdicts(t *testing.T) {
	b := spellTrace(40, func(s int) ([]trace.Bot, []trace.Mob) {
		m := sb(gMage, "frostbolt", "")
		m.Cast = spFrostbolt
		tank := sb(gTank, "taunt", "")
		return []trace.Bot{tank, m}, []trace.Mob{onParty()}
	})
	r := Spells(b.f, Defaults())
	if r.VerdictSeen || r.Idle.Recorded || r.Idle.Unknown == 0 || !r.ActsSeen || !r.CastsSeen {
		t.Fatalf("%+v", r)
	}
	// With no verdict letter an action is unknown: it is not counted as executed.
	if r.Taunts.Done != 0 || r.Taunts.Unknown != 1 || r.Taunts.Tried != 1 {
		t.Fatalf("taunts %+v", r.Taunts)
	}
	text := strings.Join(r.Lines(), "\n")
	for _, want := range []string{"action verdicts not recorded", "idle share: not recorded", "0 executed (verdict O) of 1 tried, 1 with no verdict letter (unknown)"} {
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
	if in.Casts != 7 || in.Near != 2 || in.Confirmed != 2 || in.CutShort != 2 || in.Pct != 28.6 || len(in.TopMissed) != 1 || in.TopMissed[0].Name != "Shadow Bolt" || in.TopMissed[0].N != 5 {
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
	if in := Spells(mk(1).f, Defaults()).Interrupt; in.Near != 1 || in.CutShort != 1 {
		t.Fatalf("%+v", in)
	}
	if in := Spells(mk(3).f, Defaults()).Interrupt; in.Near != 1 || in.CutShort != 0 {
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

func TestSpellsCrowdControlIsAnUncorrelatedObservation(t *testing.T) {
	// Four polymorph actions by a mage targeting 0xf1: the first is followed by a new cc flag on 0xf1; the second by none; the third is a
	// failed cast (F, not counted); the fourth is cast while 0xf3 was already crowd-controlled before it, which is no new flag.
	poly := map[int]string{10: "O", 20: "O", 30: "F", 40: "O"}
	b := spellTrace(60, func(s int) ([]trace.Bot, []trace.Mob) {
		m := sb(gMage, "frostbolt", "O")
		if res, ok := poly[s]; ok {
			m = sb(gMage, "polymorph", res)
		}
		m.Target = "0xf1"
		mob := onParty()
		if s >= 11 && s <= 14 { // the flag appears a second after the first polymorph
			mob.CC = true
		}
		other := trace.Mob{G: "0xf3", HPPct: 90, Combat: true, Target: gTank, CC: s >= 38} // already flagged when the fourth is cast
		return []trace.Bot{m}, []trace.Mob{mob, other}
	})
	r := Spells(b.f, Defaults())
	cc := r.CC
	if cc.Actions != 3 || cc.Unknown != 0 || cc.WithMobs != 3 || cc.FlagSeen != 1 || cc.TargetSeen != 3 || cc.Attributed != 1 || cc.BySpell[0].Name != "polymorph" {
		t.Fatalf("%+v", cc)
	}
	// No warning is raised from it, and nothing in the text calls it a success.
	if len(r.Warnings) != 0 {
		t.Fatalf("%q", r.Warnings)
	}
	text := strings.Join(r.Lines(), "\n") + get(t, Score(b.f, Defaults()), IDSpells).Detail
	if !strings.Contains(text, "observation, not a success rate") || !strings.Contains(text, "uncorrelated") || strings.Contains(text, "landed") {
		t.Fatalf("\n%s", text)
	}
	if err := (&Config{}).Set("cc_success", 50); err == nil {
		t.Fatal("cc_success is no threshold any more")
	}
	// A flag on another mob than the caster's target is seen, but not attributed.
	o := spellTrace(30, func(s int) ([]trace.Bot, []trace.Mob) {
		m := sb(gMage, "frostbolt", "O")
		if s == 10 {
			m = sb(gMage, "polymorph", "O")
		}
		m.Target = "0xf1"
		sheep := trace.Mob{G: "0xf5", HPPct: 90, Combat: true, Target: gTank, CC: s >= 12}
		return []trace.Bot{m}, []trace.Mob{onParty(), sheep}
	})
	if cc := Spells(o.f, Defaults()).CC; cc.FlagSeen != 1 || cc.TargetSeen != 1 || cc.Attributed != 0 {
		t.Fatalf("%+v", cc)
	}
	// An area crowd control (frost nova) is not judged by the flag.
	n := spellTrace(20, func(s int) ([]trace.Bot, []trace.Mob) {
		return []trace.Bot{sb(gMage, "frost nova", "O")}, []trace.Mob{onParty()}
	})
	if r := Spells(n.f, Defaults()); r.CC.Actions != 0 {
		t.Fatalf("%+v", r.CC)
	}
}

// The spells check reports and warns; it never fails a run by itself, whatever the numbers.
func TestSpellsNeverFailsARun(t *testing.T) {
	b := idleTrace(40)
	b.ev(5000, "note", "text", "x")
	cfg := Defaults()
	cfg.IdleSharePct, cfg.InterruptCoverPct = 0, 100
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

func TestSpellsSetKnowsTheTwoLimits(t *testing.T) {
	cfg := Defaults()
	for id, field := range map[string]*float64{IDIdleShare: &cfg.IdleSharePct, IDInterruptCover: &cfg.InterruptCoverPct} {
		if err := cfg.Set(id, 7); err != nil || *field != 7 {
			t.Errorf("%s: %v %g", id, err, *field)
		}
	}
	if d := Defaults(); d.IdleSharePct != 15 || d.InterruptCoverPct != 30 {
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

// An interrupt action is during a cast from a second before its first frame to its last frame. In the frame right after the last one it
// is counted apart (an interrupt that worked takes the cast out of the next frame, so does a kick that came too late); later, nothing.
func TestSpellsInterruptWindowIsASecondBeforeToTheCastsEnd(t *testing.T) {
	at := func(kickAt int) [2]int {
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
		in := Spells(b.f, Defaults()).Interrupt
		return [2]int{in.Near, in.After}
	}
	for kickAt, want := range map[int][2]int{9: {1, 0}, 10: {1, 0}, 11: {0, 1}, 8: {0, 0}, 12: {0, 0}} {
		if got := at(kickAt); got != want {
			t.Errorf("kick at %d s: during/after %v, want %v", kickAt, got, want)
		}
	}
	// The warning is held to the favourable reading: six casts each followed by a kick in the next frame raise none.
	b := spellTrace(80, func(s int) ([]trace.Bot, []trace.Mob) {
		act := sb(gMage, "sinister strike", "O")
		if s%10 == 1 && s >= 11 {
			act = sb(gMage, "kick", "O")
		}
		if s%10 == 0 && s >= 10 && s < 70 {
			return []trace.Bot{act}, []trace.Mob{mobCasting(spShadowBolt)}
		}
		return []trace.Bot{act}, []trace.Mob{onParty()}
	})
	if in := Spells(b.f, Defaults()); in.Interrupt.Casts != 6 || in.Interrupt.Near != 0 || in.Interrupt.After != 6 || len(in.Warnings) != 0 {
		t.Fatalf("%+v %q", in.Interrupt, in.Warnings)
	}
}

// Frames are samples: a sustained cs is several casts back to back. With the cast time known the run is ceil(duration / cast time);
// without it, one, and the report says it is a lower bound.
func TestSpellsCastEpisodesFromARunOfFrames(t *testing.T) {
	const frostbolt2s, arcaneMissiles, serpentSting = 17503, 5143, 13549 // a 2.0 s cast; a channel with no cast time; a spell the table gives a negative cast time
	if castTimeMS(frostbolt2s) != 2000 || castTimeMS(arcaneMissiles) != 0 || castTimeMS(serpentSting) != 0 {
		t.Fatalf("the spell table changed: %d %d %d", castTimeMS(frostbolt2s), castTimeMS(arcaneMissiles), castTimeMS(serpentSting))
	}
	run := func(id uint32, frames int) ClassSpell {
		b := spellTrace(frames+4, func(s int) ([]trace.Bot, []trace.Mob) {
			m := sb(gMage, "", "")
			if s >= 2 && s < 2+frames {
				m.Cast = id
			}
			return []trace.Bot{m}, nil
		})
		return Spells(b.f, Defaults()).Classes[0]
	}
	if c := run(frostbolt2s, 6); c.Casts != 3 || c.CastsLB != 0 { // 6 s of a 2 s cast
		t.Fatalf("%+v", c)
	}
	if c := run(frostbolt2s, 1); c.Casts != 1 {
		t.Fatalf("%+v", c)
	}
	if c := run(frostbolt2s, 3); c.Casts != 2 { // 3 s: two casts at least, never more than the frames
		t.Fatalf("%+v", c)
	}
	c := run(arcaneMissiles, 6)
	if c.Casts != 1 || c.CastsLB != 1 {
		t.Fatalf("%+v", c)
	}
	if c := run(serpentSting, 4); c.Casts != 1 || c.CastsLB != 1 { // a negative table value is no cast time
		t.Fatalf("%+v", c)
	}
	lines := strings.Join(Spells(spellTrace(10, func(s int) ([]trace.Bot, []trace.Mob) {
		m := sb(gMage, "", "")
		m.Cast = arcaneMissiles
		return []trace.Bot{m}, nil
	}).f, Defaults()).Lines(), "\n")
	if !strings.Contains(lines, "about 1 cast episodes") || !strings.Contains(lines, "a lower bound: 1 run(s)") {
		t.Fatalf("\n%s", lines)
	}
}

func twoCasts() []trace.Mob {
	return []trace.Mob{
		{G: "0xf2", HPPct: 90, Combat: true, Target: gTank, Cast: spShadowBolt},
		{G: "0xf4", HPPct: 90, Combat: true, Target: gTank, Cast: 403}, // Lightning Bolt
	}
}

// One kick covers one cast: two enemy casts at the same moment are not both covered by it.
func TestSpellsOneInterruptActionCoversOneCast(t *testing.T) {
	mk := func(target string, kicks ...int) InterruptUse {
		b := spellTrace(30, func(s int) ([]trace.Bot, []trace.Mob) {
			act := sb(gMage, "sinister strike", "O")
			act.Target = target
			for i, k := range kicks {
				if s == k {
					act = sb(gMage, []string{"kick", "pummel"}[i%2], "O") // a different name, so that two in a row are two actions
					act.Target = target
				}
			}
			if s == 10 {
				return []trace.Bot{act}, twoCasts()
			}
			return []trace.Bot{act}, []trace.Mob{onParty()}
		})
		return Spells(b.f, Defaults()).Interrupt
	}
	in := mk("", 10)
	if in.Casts != 2 || in.Near != 1 || len(in.TopMissed) != 1 {
		t.Fatalf("%+v", in)
	}
	// Two kicks, two casts: both covered, one each.
	if in := mk("", 9, 10); in.Near != 2 {
		t.Fatalf("%+v", in)
	}
	// The interrupter's target must be the casting mob when both are known: a kick on 0xf4 covers the Lightning Bolt, not the Shadow Bolt.
	in = mk("0xf4", 10)
	if in.Near != 1 || len(in.TopMissed) != 1 || in.TopMissed[0].Name != "Shadow Bolt" || in.ByBot[0].Name != "Mage" {
		t.Fatalf("%+v", in)
	}
	// A kick on a mob that casts nothing covers nothing.
	if in := mk("0xf9", 10); in.Near != 0 {
		t.Fatalf("%+v", in)
	}
}

// A trace in which only some bots sent verdict letters: a blank verdict from a bot that sent none is unknown, not "not executed" and
// not "executed". It is left out of the idle denominator and out of the executed counts.
func TestSpellsMixedVersionTrace(t *testing.T) {
	b := spellTrace(40, func(s int) ([]trace.Bot, []trace.Mob) {
		healer := sb(gHealer, "heal on party", "O") // sends verdicts
		mage := sb(gMage, "frostbolt", "")          // an older module: none
		tank := sb(gTank, "sunder armor", "")
		if s == 20 {
			tank = sb(gTank, "taunt", "")
			mage = sb(gMage, "kick", "")
		}
		return []trace.Bot{healer, mage, tank}, []trace.Mob{onParty()}
	})
	r := Spells(b.f, Defaults())
	if !r.VerdictSeen || !r.Idle.Recorded || r.Idle.Engaged != 40 || r.Idle.Idle != 0 || r.Idle.Unknown != 80 {
		t.Fatalf("%+v", r.Idle) // the healer's 40 frames count; the mage's and the tank's 80 mana-user frames are unknown
	}
	if r.Taunts.Done != 0 || r.Taunts.Unknown != 1 || r.Taunts.Tried != 1 {
		t.Fatalf("%+v", r.Taunts)
	}
	// A blank verdict from the mage with no enemy cast near: nothing to match, and nothing counted as executed.
	if r.Interrupt.Near != 0 {
		t.Fatalf("%+v", r.Interrupt)
	}
	// With a cast near it, the unknown kick is matched but not confirmed.
	c := spellTrace(40, func(s int) ([]trace.Bot, []trace.Mob) {
		healer := sb(gHealer, "heal on party", "O")
		mage := sb(gMage, "frostbolt", "")
		if s == 20 {
			mage = sb(gMage, "kick", "")
		}
		mobs := []trace.Mob{onParty()}
		if s == 20 {
			mobs = []trace.Mob{mobCasting(spShadowBolt)}
		}
		return []trace.Bot{healer, mage}, mobs
	})
	in := Spells(c.f, Defaults()).Interrupt
	if in.Casts != 1 || in.Near != 1 || in.Confirmed != 0 {
		t.Fatalf("%+v", in)
	}
	// A bot that does send verdict letters but sent none for one action has a missing verdict there: it is not "not known to have failed".
	e := spellTrace(40, func(s int) ([]trace.Bot, []trace.Mob) {
		m := sb(gMage, "sinister strike", "O")
		if s == 20 {
			m = sb(gMage, "kick", "")
		}
		if s == 20 {
			return []trace.Bot{m}, []trace.Mob{mobCasting(spShadowBolt)}
		}
		return []trace.Bot{m}, []trace.Mob{onParty()}
	})
	if in := Spells(e.f, Defaults()).Interrupt; in.Casts != 1 || in.Near != 0 || in.After != 0 {
		t.Fatalf("%+v", in)
	}
	// The healer's own failed verdicts are failures, not unknowns: its engaged idle frames are counted.
	d := spellTrace(40, func(s int) ([]trace.Bot, []trace.Mob) {
		h := sb(gHealer, "heal on party", "I")
		if s < 20 {
			h.Res = "O"
		}
		return []trace.Bot{h, sb(gMage, "frostbolt", "")}, []trace.Mob{onParty()}
	})
	if r := Spells(d.f, Defaults()); r.Idle.Engaged != 40 || r.Idle.Idle != 20 || r.Idle.Pct != 50 {
		t.Fatalf("%+v", r.Idle)
	}
}

// tauntTrace: a hostile on the tank that is switched to the healer for [start, start+lat] of episode k (start 10+25k), with the
// tank taunting at start+lat when taunt is set. No tank is named in the header: the caller sets it.
func tauntTrace(lats []int, taunt bool) *build {
	return spellTrace(200, func(s int) ([]trace.Bot, []trace.Mob) {
		mob := trace.Mob{G: "0xf1", HPPct: 80, Combat: true, Target: gTank, Threat: []trace.Threat{{G: gTank, Pct: 100}}}
		tank := sb(gTank, "sunder armor", "O")
		for k, lat := range lats {
			start := 10 + 25*k
			if s >= start && s <= start+lat {
				mob.Target = gHealer
				mob.Threat = []trace.Threat{{G: gHealer, Pct: 100}}
			}
			if taunt && s == start+lat {
				tank = sb(gTank, "taunt", "O")
			}
		}
		return []trace.Bot{tank, sb(gHealer, "flash heal", "O")}, []trace.Mob{mob}
	})
}

// The median time from a non-tank victim to the tank's taunt, or to the hostile going back to the tank.
func TestSpellsTauntLatencyMedian(t *testing.T) {
	// Answered by a taunt: the latency is the frame time of the taunt.
	b := tauntTrace([]int{2, 2, 2, 6}, true)
	b.f.Header.Tank = "Tank"
	r := Spells(b.f, Defaults())
	if t1 := r.TauntLatency; t1.Episodes != 4 || t1.ByTaunt != 4 || t1.MedianS != 2 || t1.WorstS != 6 {
		t.Fatalf("%+v", t1)
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "taunt_latency") {
			t.Fatalf("median 2 s is within the limit: %q", w)
		}
	}
	// A median over the limit warns, and the check still passes with the warning in its detail.
	b = tauntTrace([]int{6, 7, 8, 2}, true)
	b.f.Header.Tank = "Tank"
	r = Spells(b.f, Defaults())
	if t1 := r.TauntLatency; t1.MedianS != 6.5 {
		t.Fatalf("%+v", t1)
	}
	warned := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "taunt_latency") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no warning: %q", r.Warnings)
	}
	c := get(t, Score(b.f, Defaults()), IDSpells)
	if c.Verdict != trace.Pass || !strings.Contains(c.Detail, "taunt_latency") {
		t.Fatalf("%+v", c)
	}
	// Returned to the tank without a taunt: the latency is the return frame's time minus the start (lat+1).
	b = tauntTrace([]int{2, 2, 4, 4}, false)
	b.f.Header.Tank = "Tank"
	if t1 := Spells(b.f, Defaults()).TauntLatency; t1.Episodes != 4 || t1.ByTaunt != 0 || t1.Returned != 4 || t1.MedianS != 4 {
		t.Fatalf("%+v", t1)
	}
	// Fewer than three episodes: no warning.
	b = tauntTrace([]int{9, 9}, true)
	b.f.Header.Tank = "Tank"
	r = Spells(b.f, Defaults())
	if r.TauntLatency.Episodes != 2 || len(r.Warnings) != 0 {
		t.Fatalf("%+v %q", r.TauntLatency, r.Warnings)
	}
	// No tank named anywhere: not measured.
	b = tauntTrace([]int{2, 2, 2, 6}, true)
	if t1 := Spells(b.f, Defaults()).TauntLatency; t1.Recorded || t1.Episodes != 0 {
		t.Fatalf("%+v", t1)
	}
}

// The crowd-control hold: an episode is a run of consecutive frames with the mob flagged, joined to a single-target cc action.
func TestSpellsCCHoldBrokenEpisodes(t *testing.T) {
	const polyEvery = 20
	ccTrace := func(lens []int, extra int, goneFrom int) *build {
		return spellTrace(100, func(s int) ([]trace.Bot, []trace.Mob) {
			m := sb(gMage, "frostbolt", "O")
			if s >= 10 && s <= 70 && s%polyEvery == 10 {
				m = sb(gMage, "polymorph", "O")
				m.Target = "0xf1"
			}
			flagged := false
			for k, n := range lens {
				p := 10 + polyEvery*k
				if s >= p+1 && s <= p+n {
					flagged = true
				}
			}
			if extra > 0 && s >= 85 && s < 85+extra {
				flagged = true
			}
			var mobs []trace.Mob
			if goneFrom < 0 || s < goneFrom {
				mobs = append(mobs, trace.Mob{G: "0xf2", HPPct: 80, Combat: true, Target: gTank, CC: flagged})
			}
			return []trace.Bot{m}, mobs
		})
	}
	// Four joined episodes, two of them shorter than 5 s: half held, no warning at exactly 50 %.
	r := Spells(ccTrace([]int{10, 10, 2, 2}, 0, -1).f, Defaults())
	if h := r.CCHold; h.Joined != 4 || h.Broken != 2 || h.HeldPct != 50 {
		t.Fatalf("%+v", h)
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "cc_hold") {
			t.Fatalf("50 %% is not under the limit: %q", w)
		}
	}
	// Three of four broken warns.
	r = Spells(ccTrace([]int{10, 2, 2, 2}, 0, -1).f, Defaults())
	if h := r.CCHold; h.Broken != 3 || h.HeldPct != 25 {
		t.Fatalf("%+v", h)
	}
	warned := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "cc_hold") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no warning: %q", r.Warnings)
	}
	// A flagged run with no cc action before it is not joined: left out.
	r = Spells(ccTrace([]int{10, 10, 2, 2}, 2, -1).f, Defaults())
	if h := r.CCHold; h.Joined != 4 {
		t.Fatalf("%+v", h)
	}
	// A mob that leaves the frames after a short joined episode was not seen unflagged: held, not broken.
	r = Spells(ccTrace([]int{10, 10, 2, 2}, 0, 73).f, Defaults())
	if h := r.CCHold; h.Joined != 4 || h.Broken != 1 {
		t.Fatalf("%+v", h)
	}
}

// The share of mob-frames whose top threat holder is a pet (guid prefix 0xf140).
func TestSpellsPetThreatShare(t *testing.T) {
	const pet = "0xf140000000000009"
	petTrace := func(secs int, petFrame func(s int) bool) *build {
		return spellTrace(secs, func(s int) ([]trace.Bot, []trace.Mob) {
			thr := []trace.Threat{{G: gTank, Pct: 100}}
			if petFrame(s) {
				thr = []trace.Threat{{G: pet, Pct: 100}}
			}
			return []trace.Bot{sb(gTank, "sunder armor", "O")}, []trace.Mob{{G: "0xf1", HPPct: 80, Combat: true, Target: gTank, Threat: thr}}
		})
	}
	r := Spells(petTrace(60, func(s int) bool { return s < 6 }).f, Defaults())
	if p := r.PetThreat; p.MobFrames != 60 || p.PetTop != 6 || p.Pct != 10 {
		t.Fatalf("%+v", p)
	}
	warned := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "pet_threat") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no warning: %q", r.Warnings)
	}
	r = Spells(petTrace(60, func(s int) bool { return s == 0 }).f, Defaults())
	if p := r.PetThreat; p.Pct != 1.7 || len(r.Warnings) != 0 {
		t.Fatalf("%+v %q", p, r.Warnings)
	}
	// Half the frames, but under 30 mob-frames: no warning.
	r = Spells(petTrace(20, func(s int) bool { return s < 10 }).f, Defaults())
	if p := r.PetThreat; p.MobFrames != 20 || p.Pct != 50 || len(r.Warnings) != 0 {
		t.Fatalf("%+v %q", p, r.Warnings)
	}
	// A dead mob, one out of combat and one with no threat list are not mob-frames.
	b := spellTrace(5, func(s int) ([]trace.Bot, []trace.Mob) {
		return []trace.Bot{sb(gTank, "sunder armor", "O")}, []trace.Mob{
			{G: "0xf1", HPPct: 0, Combat: true, Threat: []trace.Threat{{G: pet, Pct: 100}}},
			{G: "0xf2", HPPct: 90, Combat: false, Threat: []trace.Threat{{G: pet, Pct: 100}}},
			{G: "0xf3", HPPct: 90, Combat: true},
		}
	})
	if p := Spells(b.f, Defaults()).PetThreat; p.MobFrames != 0 || p.PetTop != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestSpellsNewLimitsAreSettable(t *testing.T) {
	d := Defaults()
	if d.TauntLatencyS != 3 || d.CCHoldPct != 50 || d.PetThreatPct != 5 {
		t.Fatalf("%+v", d)
	}
	cfg := Defaults()
	for id, field := range map[string]*float64{IDTauntLatency: &cfg.TauntLatencyS, IDCCHold: &cfg.CCHoldPct, IDPetThreat: &cfg.PetThreatPct} {
		if err := cfg.Set(id, 7); err != nil || *field != 7 {
			t.Errorf("%s: %v %g", id, err, *field)
		}
	}
	err := (&Config{}).Set("bogus", 1)
	if err == nil {
		t.Fatal("bogus is no threshold")
	}
	for _, id := range []string{IDTauntLatency, IDCCHold, IDPetThreat} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("%q is not in %q", id, err)
		}
	}
}

func TestSpellsLinesNameTheNewReadings(t *testing.T) {
	b := tauntTrace([]int{6, 7, 8, 2}, true)
	b.f.Header.Tank = "Tank"
	text := strings.Join(Spells(b.f, Defaults()).Lines(), "\n")
	for _, want := range []string{"taunt latency", "cc hold", "pet threat", "1 Hz"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q is missing in\n%s", want, text)
		}
	}
}
