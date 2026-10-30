package tracecheck

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/kbspells"
	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/trace"
)

// The spells oracle check scores what each party bot cast against the Turtle WoW knowledge-base spec lists
// (internal/kbspells). It warns: a core spell of the bot's inferred spec with no cast, or a spell its file says never to use.
// Like the spells report it reads 1 Hz samples, so instants and short casts are not seen. A pet is not a party member and is not
// judged.
const (
	IDSpellOracle = "spell_oracle"
	IDOOMInFight  = "oom_in_fight"

	oomManaPct = 5.0 // percent: under this a mana user is dry
)

// oracleBot is a party bot between frames while its casts are collected.
type oracleBot struct {
	seen      bool
	lastT     int64
	runSpell  uint32
	runFrames int
	prevAct   string
	counts    map[string]int
	combat    int // combat frames
}

// SpellOracle judges every party bot of the trace against the oracle. ActiveS is the bot's combat frames times the frame
// interval; the casts come from cs episodes and from changes of act (instants, which only the action line shows), a name cut to
// 16 bytes matched by prefix against the bot's class names.
func SpellOracle(f *trace.File, c Config) []kbspells.Finding {
	interval := f.Header.IntervalMS
	if interval <= 0 {
		interval = 1000
	}
	ivS := float64(interval) / 1000
	o := kbspells.Default()
	members := map[string]trace.Member{}
	for _, m := range f.Party {
		members[m.G] = m
	}
	known := map[string]bool{}
	for _, fr := range f.Frames {
		for _, b := range fr.Bots {
			if b.Res != "" {
				known[b.G] = true
			}
		}
	}
	names := map[string][]string{}
	for class, specs := range o.Classes {
		seen := map[string]bool{}
		var all []string
		add := func(l []string) {
			for _, n := range l {
				if !seen[strings.ToLower(n)] {
					seen[strings.ToLower(n)] = true
					all = append(all, n)
				}
			}
		}
		for _, s := range specs {
			add(s.Core)
			add(s.Situational)
			add(s.Never)
			add(s.Signature)
			add(s.Taunt)
		}
		add(o.Common.Generic[class])
		names[class] = all
	}
	st := map[string]*oracleBot{}
	flush := func(s *oracleBot) {
		if s.runSpell == 0 {
			return
		}
		if name := spells.Name(s.runSpell); name != "" {
			s.counts[name] += castEpisodes(s.runSpell, s.runFrames, ivS)
		}
		s.runSpell, s.runFrames = 0, 0
	}
	for _, fr := range f.Frames {
		present := map[string]bool{}
		for _, b := range fr.Bots {
			m, ok := members[b.G]
			if !ok {
				continue // a pet or a unit that is not in the party
			}
			present[b.G] = true
			s := st[b.G]
			if s == nil {
				s = &oracleBot{counts: map[string]int{}}
				st[b.G] = s
			}
			gap := s.seen && fr.T-s.lastT > int64(3*interval)
			if b.Cast != 0 && b.Cast == s.runSpell && !gap {
				s.runFrames++
			} else {
				flush(s)
				if b.Cast != 0 {
					s.runSpell, s.runFrames = b.Cast, 1
				}
			}
			plausible := b.Res == "O" || (b.Res == "" && !known[b.G])
			if b.Action != "" && (s.prevAct != b.Action || gap) && plausible {
				if name := matchAct(b.Action, names[strings.ToLower(m.Class)]); name != "" {
					s.counts[name]++
				}
			}
			if b.Action != "" && (s.prevAct != b.Action || gap) {
				s.prevAct = b.Action
			}
			if b.Alive && (b.Combat || b.State == "c") {
				s.combat++
			}
			s.seen, s.lastT = true, fr.T
		}
		for g, s := range st {
			if !present[g] {
				flush(s)
			}
		}
	}
	var out []kbspells.Finding
	for _, m := range f.Party {
		s := st[m.G]
		if s == nil {
			continue
		}
		flush(s)
		name := m.Name
		if name == "" {
			name = f.Name(m.G)
		}
		if name == "" {
			name = m.G
		}
		out = append(out, o.Judge(kbspells.BotCasts{
			Name: name, Class: m.Class, Role: m.Role, Level: m.Level,
			Casts: s.counts, ActiveS: float64(s.combat) * ivS,
		}, c.SpellOracleActiveS))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bot < out[j].Bot })
	return out
}

// matchAct finds the oracle name an action is: the names are matched case-insensitively by prefix, because the module cuts an
// action name to 16 bytes ("challenging shou"). An exact name wins; otherwise the shortest prefix match, then the first in
// name order, so a reading is deterministic.
func matchAct(act string, names []string) string {
	a := strings.ToLower(strings.TrimSpace(act))
	if a == "" {
		return ""
	}
	best := ""
	for _, n := range names {
		ln := strings.ToLower(n)
		if ln == a {
			return n
		}
		if strings.HasPrefix(ln, a) && (best == "" || len(n) < len(best) || (len(n) == len(best) && n < best)) {
			best = n
		}
	}
	return best
}

// oracleText is the finding as a person reads it.
func oracleText(f kbspells.Finding) string {
	return fmt.Sprintf("%s (%s %s): %s", f.Bot, f.Class, f.Role, f.Text)
}

// spellOracleCheck is the report as a check: it never fails. A trace whose bots were all active for less than the threshold has
// nothing to judge yet and is n/a.
func spellOracleCheck(f *trace.File, c Config) trace.Check {
	const id, name = IDSpellOracle, "Spell oracle (report only)"
	fs := SpellOracle(f, c)
	if len(fs) == 0 {
		return na(id, name, "not recorded: no party bot with casts or actions")
	}
	var warns []string
	judged := 0
	for _, x := range fs {
		switch x.Level {
		case "warn":
			judged++
			warns = append(warns, oracleText(x))
		case "ok":
			judged++
		}
	}
	if judged == 0 {
		return na(id, name, fmt.Sprintf("no bot active for %.0f s or more (threshold spell_oracle=%.0f s)", c.SpellOracleActiveS, c.SpellOracleActiveS))
	}
	d := fmt.Sprintf("report only; 1 Hz frames, so instants and short casts are not seen; %d bot(s) judged against their spec's core list, %d warned", judged, len(warns))
	if len(warns) > 0 {
		d += "; WARN " + strings.Join(warns, "; ")
	}
	return trace.Check{ID: id, Name: name, Verdict: trace.Pass, Count: len(warns), Value: float64(len(warns)), Unit: "warnings", Detail: d}
}

// oomCheck warns per mana user that sat under 5 % mana for the threshold or more while in combat. It never fails: a low mana
// pool can be the fight's fault, not the bot's.
func oomCheck(f *trace.File, c Config) trace.Check {
	const id, name = IDOOMInFight, "Mana dry in a fight (report only)"
	interval := f.Header.IntervalMS
	if interval <= 0 {
		interval = 1000
	}
	ivS := float64(interval) / 1000
	members := map[string]bool{}
	for _, m := range f.Party {
		members[m.G] = true
	}
	type dry struct {
		frames int
		warned bool
	}
	st := map[string]*dry{}
	manaCombat := false
	var warns []string
	for _, fr := range f.Frames {
		present := map[string]bool{}
		for _, b := range fr.Bots {
			if !members[b.G] || !b.Alive || !(b.Combat || b.State == "c") || b.PType != 0 || b.MaxPow == 0 {
				continue
			}
			present[b.G] = true
			manaCombat = true
			d := st[b.G]
			if d == nil {
				d = &dry{}
				st[b.G] = d
			}
			if float64(b.Power)*100 >= oomManaPct*float64(b.MaxPow) {
				*d = dry{}
				continue
			}
			d.frames++
			if !d.warned && float64(d.frames)*ivS >= c.OomFightS {
				d.warned = true
				warns = append(warns, fmt.Sprintf("%s (%.0f s under %.0f %% mana)", f.Name(b.G), float64(d.frames)*ivS, oomManaPct))
			}
		}
		for g, d := range st {
			if !present[g] {
				*d = dry{} // not in this frame, dead or out of combat: the run ends
			}
		}
	}
	if !manaCombat {
		return na(id, name, "not recorded: no mana user in combat")
	}
	sort.Strings(warns)
	d := fmt.Sprintf("no mana user sat under %.0f %% mana for %g s or more in combat", oomManaPct, c.OomFightS)
	if len(warns) > 0 {
		d = fmt.Sprintf("%d mana user(s) sat under %.0f %% mana for %g s or more in combat: %s", len(warns), oomManaPct, c.OomFightS, strings.Join(warns, ", "))
	}
	return trace.Check{ID: id, Name: name, Verdict: trace.Pass, Count: len(warns), Value: float64(len(warns)), Unit: "warnings", Threshold: c.OomFightS, Detail: d}
}
