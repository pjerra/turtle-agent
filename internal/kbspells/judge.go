package kbspells

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/spells"
)

// BotCasts is one bot's casts, from a trace (1 Hz samples) or a finished fight (the module's exact counts, capped at 8 spell
// lines). Truncated says the list may be capped; Level 0 means the level is not known.
type BotCasts struct {
	Name      string         `json:"name"`
	Class     string         `json:"class"`
	Role      string         `json:"role"`
	Level     int            `json:"level,omitempty"`
	Casts     map[string]int `json:"casts,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	ActiveS   float64        `json:"active_s"`
}

// Finding is the judgment of one bot. Level is warn (a missing core spell or a never spell), na (not judged: too little active
// time, a capped list, or no oracle data for the class) or ok.
type Finding struct {
	Bot     string   `json:"bot"`
	Class   string   `json:"class"`
	Role    string   `json:"role"`
	Spec    string   `json:"spec"`
	SpecHow string   `json:"spec_how"`
	Level   string   `json:"level"`
	Missing []string `json:"missing,omitempty"`
	Unknown []string `json:"unknown,omitempty"`
	Never   []string `json:"never,omitempty"`
	OffList []string `json:"off_list,omitempty"`
	Text    string   `json:"text"`
}

// InferSpec guesses the spec of a bot that carries no spec on the wire: the role picks a tank or healer spec, and a dps bot is
// the spec of its class whose signature talents were cast most. No signature (or a tie) is the class-wide lists. how says which
// reading it was, for the report.
func (o *Oracle) InferSpec(class, role string, casts map[string]int) (spec, how string) {
	c := normClass(class)
	specs := o.Classes[c]
	if len(specs) == 0 {
		return "unknown", "no oracle data for " + orClass(c)
	}
	switch normRole(role) {
	case "tank":
		switch c {
		case "warrior":
			if s, ok := o.spec(c, "fury"); ok {
				if name, n := sigCasts(s, casts); n > 0 {
					return "fury_tank", fmt.Sprintf("signature %s %d", name, n)
				}
			}
			if _, ok := o.spec(c, "protection"); ok {
				return "protection", "role tank"
			}
		case "druid":
			if _, ok := o.spec(c, "feral_bear"); ok {
				return "feral_bear", "role tank"
			}
		case "paladin":
			if _, ok := o.spec(c, "protection"); ok {
				return "protection", "role tank"
			}
		case "shaman":
			if _, ok := o.spec(c, "enhancement_tank"); ok {
				return "enhancement_tank", "role tank"
			}
		}
		return "unknown", "no tank spec for " + c
	case "heal":
		healer := map[string]string{"priest": "holy", "paladin": "holy", "druid": "restoration", "shaman": "restoration"}[c]
		if healer != "" {
			if _, ok := o.spec(c, healer); ok {
				return healer, "role heal"
			}
		}
		return "unknown", "no healer spec for " + c
	}
	var dps []SpecOracle
	for _, s := range specs {
		if normRole(s.Role) == "dps" {
			dps = append(dps, s)
		}
	}
	if len(dps) == 0 {
		return "unknown", "no dps spec for " + c
	}
	bestN, bestName, best := 0, "", ""
	tied := false
	for _, s := range dps {
		name, n := sigCasts(s, casts)
		switch {
		case n > bestN:
			bestN, bestName, best, tied = n, name, s.Spec, false
		case n == bestN && n > 0:
			tied = true
		}
	}
	if bestN == 0 || tied {
		return "class", "no signature cast"
	}
	return best, fmt.Sprintf("signature %s %d", bestName, bestN)
}

// sigCasts is the most-cast signature spell of a spec and how often it was cast.
func sigCasts(s SpecOracle, casts map[string]int) (name string, n int) {
	for _, sig := range s.Signature {
		if c := castCount(casts, sig); c > n {
			name, n = sig, c
		}
	}
	return name, n
}

func castCount(casts map[string]int, name string) int {
	if n, ok := casts[name]; ok {
		return n
	}
	lower := strings.ToLower(name)
	for k, n := range casts {
		if strings.ToLower(strings.TrimSpace(k)) == lower {
			return n
		}
	}
	return 0
}

// Judge reads one bot against its spec's lists. A core spell with no cast is a warning unless the bot is too young for its first
// rank or the list is capped; a never spell is always a warning. Spells outside every list are reported as off-list, information
// only. It never fails anything: a trace's other checks keep the exit code.
func (o *Oracle) Judge(b BotCasts, minActiveS float64) Finding {
	f := Finding{Bot: b.Name, Class: normClass(b.Class), Role: normRole(b.Role)}
	specs := o.Classes[f.Class]
	if len(specs) == 0 {
		f.Spec = "unknown"
		f.SpecHow = "no oracle data for " + orClass(f.Class)
		f.Level = "na"
		f.Text = f.SpecHow
		return f
	}
	f.Spec, f.SpecHow = o.InferSpec(b.Class, b.Role, b.Casts)
	var core, situational []string
	switch f.Spec {
	case "unknown":
		f.Level = "na"
		f.Text = f.SpecHow
		return f
	case "class":
		core, situational = classWide(specs)
	default:
		s, ok := o.spec(f.Class, f.Spec)
		if !ok {
			f.Level = "na"
			f.Text = f.SpecHow
			return f
		}
		core, situational = s.Core, s.Situational
	}

	// A bot seen for less than the threshold is not judged on its core list: a fight it joined late, or a trace cut short, says
	// nothing about an every-fight spell yet.
	if b.ActiveS >= minActiveS {
		for _, name := range core {
			if castCount(b.Casts, name) > 0 {
				continue
			}
			if b.Level > 0 {
				if lvl, ok := firstRankLevel(name); ok && lvl > b.Level {
					continue // the bot cannot have learnt it yet
				}
			}
			if b.Truncated {
				f.Unknown = append(f.Unknown, name)
				continue
			}
			f.Missing = append(f.Missing, name)
		}
	}
	for _, name := range neverOf(o, f.Class, f.Spec) {
		if castCount(b.Casts, name) > 0 {
			f.Never = append(f.Never, name)
		}
	}
	known := map[string]bool{}
	for _, l := range [][]string{core, situational, neverOf(o, f.Class, f.Spec), o.Common.Generic[f.Class], o.Common.Ignore} {
		for _, n := range l {
			known[strings.ToLower(n)] = true
		}
	}
	for name, n := range b.Casts {
		if n > 0 && !known[strings.ToLower(strings.TrimSpace(name))] {
			f.OffList = append(f.OffList, name)
		}
	}
	sort.Strings(f.OffList)

	switch {
	case len(f.Missing) > 0 || len(f.Never) > 0:
		f.Level = "warn"
	case b.ActiveS < minActiveS || len(f.Unknown) > 0:
		f.Level = "na"
	default:
		f.Level = "ok"
	}

	var parts []string
	if f.Spec == "class" {
		parts = append(parts, "class-wide ("+f.SpecHow+")")
	} else {
		parts = append(parts, "spec "+f.Spec+" ("+f.SpecHow+")")
	}
	if len(f.Missing) > 0 {
		parts = append(parts, "missing core: "+strings.Join(f.Missing, ", "))
	}
	if len(f.Unknown) > 0 {
		parts = append(parts, "unknown (list capped at 8): "+strings.Join(f.Unknown, ", "))
	}
	if len(f.Never) > 0 {
		parts = append(parts, "never: "+strings.Join(f.Never, ", "))
	}
	if len(f.OffList) > 0 {
		parts = append(parts, "off-list: "+strings.Join(f.OffList, ", "))
	}
	switch {
	case f.Level == "ok":
		parts = append(parts, "ok")
	case f.Level == "na" && len(f.Missing) == 0 && len(f.Never) == 0:
		parts = append(parts, fmt.Sprintf("not judged: %.0f s active of %.0f s", b.ActiveS, minActiveS))
	}
	f.Text = strings.Join(parts, "; ")
	return f
}

// neverOf is the never list of the judged spec, or of every dps spec when the spec is not known.
func neverOf(o *Oracle, class, spec string) []string {
	if s, ok := o.spec(class, spec); ok {
		return s.Never
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range o.Classes[class] {
		for _, n := range s.Never {
			if !seen[strings.ToLower(n)] {
				seen[strings.ToLower(n)] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// classWide judges a dps bot whose spec the casts do not tell: its core is the intersection of the class's dps specs' cores (a
// spell every one of them presses) and its situational list is their union.
func classWide(specs []SpecOracle) (core, situational []string) {
	var dps []SpecOracle
	for _, s := range specs {
		if normRole(s.Role) == "dps" {
			dps = append(dps, s)
		}
	}
	if len(dps) == 0 {
		return nil, nil
	}
	for _, n := range dps[0].Core {
		in := true
		for _, s := range dps[1:] {
			if !slices.Contains(s.Core, n) {
				in = false
				break
			}
		}
		if in {
			core = append(core, n)
		}
	}
	seen := map[string]bool{}
	for _, s := range dps {
		for _, n := range s.Situational {
			if !seen[n] {
				seen[n] = true
				situational = append(situational, n)
			}
		}
	}
	return core, situational
}

// firstRankLevel is the character level at which the first rank of a spell is learnt: the lowest level of the spells of that name
// with "Rank 1" in the data, or of the rankless ones when no rank is listed (the rule of brain.FirstRankLevels). The oracle
// cannot import the brain, so the table is read here.
func firstRankLevel(name string) (int, bool) {
	var ranked, rankless []int
	for _, id := range spells.IDsByName(name) {
		m, ok := spells.Lookup(id)
		if !ok || m.Level == 0 {
			continue
		}
		switch m.Rank {
		case 1:
			ranked = append(ranked, int(m.Level))
		case 0:
			rankless = append(rankless, int(m.Level))
		}
	}
	lvls := ranked
	if len(lvls) == 0 {
		lvls = rankless
	}
	if len(lvls) == 0 {
		return 0, false
	}
	return slices.Min(lvls), true
}

func orClass(class string) string {
	if class == "" {
		return "an unknown class"
	}
	return class
}
