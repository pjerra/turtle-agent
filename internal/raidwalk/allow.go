package raidwalk

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	gmNameRe    = regexp.MustCompile(`^[A-Za-z]{2,12}$`)
	teleWordRe  = regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)
	runStopRe   = regexp.MustCompile(`^\.bot run stop(?: time limit reached \(raid-walk ([0-9]{1,3})m\))?$`)
	runResumeRe = regexp.MustCompile(`^\.bot run resume$`)
	tierWordRe  = regexp.MustCompile(`^(?:t|tier)([1-3])$`)
	gearTierRe  = regexp.MustCompile(`^[1-3]$`)
)

// GMVisibleOff is the one `.gm` line raid-walk may send, verbatim: the revive hides the commander before it appears
// next to a hire (review round 2, L2). Besides `.gm on` (the place phase's probe) no other `.gm` form passes.
const GMVisibleOff = ".gm visible off"

// AllowedLine says whether raid-walk may send line through the gm RPC (spec 3.6): every line must match one of
// the forms, with its arguments checked, and a bot name must be one of the walk's hires. mapID is the walk's plan
// map: `.instance unbind` may name that map and no other (spec 7). The command checks every gm line with it before
// sending; there is no second copy of the rule.
func AllowedLine(line string, hires map[string]bool, mapID uint32) bool {
	if strings.ContainsAny(line, "\r\n") {
		return false
	}
	if line == GMVisibleOff {
		return true
	}
	if runResumeRe.MatchString(line) {
		return true
	}
	if m := runStopRe.FindStringSubmatch(line); m != nil {
		if m[1] == "" {
			return true
		}
		n, err := strconv.Atoi(m[1])
		return err == nil && n >= 1 && n <= 720
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case ".gm":
		// The place phase's GM-mode probe (spec 5 step 1, review round 1, item 12): the commander must be in GM mode
		// before the first teleport, and `.gm on` is the only read of that state (it is a no-op already ON). Besides
		// the exact GMVisibleOff line (checked above) nothing else may be typed into the GM console.
		return len(f) == 2 && f[1] == "on"
	case ".go":
		if len(f) != 6 || f[1] != "xyz" {
			return false
		}
		for _, v := range f[2:5] {
			x, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(x) || math.IsInf(x, 0) {
				return false
			}
		}
		_, err := strconv.ParseUint(f[5], 10, 32)
		return err == nil
	case ".tele":
		if len(f) != 4 || f[1] != "name" || !hires[f[2]] {
			return false
		}
		return teleWordRe.MatchString(f[3])
	case ".summon", ".revive":
		return len(f) == 2 && hires[f[1]]
	case ".appear":
		// The place phase's way into the raid's copy (spec 5 step 3), and the revive's (joinHires): a hire, and only a hire.
		return len(f) == 2 && hires[f[1]]
	case ".instance":
		// `.instance unbind <map>` drops the commander's own bind to the walk's plan map (spec 5.1): that map and no other.
		// `.instance listbinds` is read only: the --reuse-instance bind check needs the commander's TTR for the map.
		if len(f) == 2 && f[1] == "listbinds" {
			return true
		}
		return len(f) == 3 && f[1] == "unbind" && f[2] == strconv.FormatUint(uint64(mapID), 10)
	case ".bot":
		if len(f) < 3 {
			return false
		}
		switch {
		case f[1] == "hire" && f[2] == "raid":
			return allowedHireRaid(f[3:])
		case f[1] == "gear" && f[2] == "raid":
			// `.bot gear raid <commander> tier <N>`, the refit after the hire (amendment G1); the speculative
			// `preset <word>` form is gone.
			return len(f) == 6 && gmNameRe.MatchString(f[3]) && f[4] == "tier" && gearTierRe.MatchString(f[5])
		}
	}
	return false
}

// allowedHireRaid checks `<hire> level=<n>[ <tier>]`.
func allowedHireRaid(f []string) bool {
	levelAt := -1
	for i, w := range f {
		if strings.HasPrefix(w, "level=") {
			if levelAt >= 0 {
				return false
			}
			levelAt = i
		}
	}
	if levelAt <= 0 || levelAt > len(f)-1 {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(f[levelAt], "level="))
	if err != nil || n < 1 || n > 60 {
		return false
	}
	tail := f[levelAt+1:]
	if len(tail) > 1 {
		return false
	}
	if len(tail) == 1 && !tierWordRe.MatchString(tail[0]) {
		return false
	}
	_, total, err := validateHire(strings.Join(f[:levelAt], " "))
	return err == nil && total >= 1 && total <= maxRosterBots
}

// AllowedTTMax is the longest toolkit command raid-walk sends: it must fit one chat line with the `.tt` prefix, the
// request id and room to spare (the toolkit's own cap is 200).
const AllowedTTMax = 200

// MaxTTEntry is the largest creature entry a toolkit line may name: the toolkit's own number reader refuses
// anything at or above 1e7 (tt_commands.lua num), so a bigger one is a bug on our side.
const MaxTTEntry = 9999999

// AllowedTT says whether raid-walk may send cmd to the Eluna test toolkit (the `tt` RPC, spec 7): one line, no `#`
// (the agent adds the request id), at most AllowedTTMax characters, and one of the forms the place phase and the
// boss-kill read need. Never: kill, boss, force, me, reset, here, a clearto without dz=, another map, a name that
// is not a hire.
func AllowedTT(cmd string, hires map[string]bool, mapID uint32) bool {
	if cmd == "" || len(cmd) > AllowedTTMax || strings.ContainsAny(cmd, "\r\n#") {
		return false
	}
	f := strings.Fields(cmd)
	switch f[0] {
	case "q":
		if len(f) < 2 {
			return false
		}
		switch f[1] {
		case "inst":
			return len(f) == 2
		case "floor":
			return len(f) == 5 && ttNumbers(f[2:5])
		case "group":
			return len(f) == 3 && ttHire(f[2], hires)
		case "units":
			// The radius cap is the quiet's roamer scope (80): the walk joins the quiet's roamers to a `q units` read of
			// their own (item 4). The corridor's pre-reads stay far inside it. The boss-kill read adds `dead` and
			// `e=<entry>` (review round 1, item 1): the corpses too, one entry, near the boss row.
			if len(f) < 7 || f[6] != "all" || !ttRadius(f[2], 80) || !ttNumbers(f[3:6]) {
				return false
			}
			for _, extra := range f[7:] {
				if extra != "dead" && !ttKVNum(extra, "e", 1, MaxTTEntry) {
					return false
				}
			}
			return true
		}
	case "quiet":
		// quiet <r<=50> <x> <y> <z> w=<0..30> dz=<1..15> [skip=<ids>]
		if len(f) != 7 && len(f) != 8 {
			return false
		}
		if !ttRadius(f[1], 50) || !ttNumbers(f[2:5]) {
			return false
		}
		if !ttKVNum(f[5], "w", 0, 30) || !ttKVNum(f[6], "dz", 1, 15) {
			return false
		}
		return len(f) == 7 || ttSkip(f[7])
	case "place":
		// place <x> <y> <z> spread=<0..8> of=<hire>
		return len(f) == 6 && ttNumbers(f[1:4]) && ttKVNum(f[4], "spread", 0, 8) && ttHire(f[5], hires)
	case "clearto":
		// clearto <mapID> <x> <y> <z> <r<=30> dz=<1..15> all=1 [skip=<ids>]
		if len(f) != 8 && len(f) != 9 {
			return false
		}
		if f[1] != strconv.FormatUint(uint64(mapID), 10) {
			return false
		}
		if !ttNumbers(f[2:5]) || !ttRadius(f[5], 30) || !ttKVNum(f[6], "dz", 1, 15) || f[7] != "all=1" {
			return false
		}
		return len(f) == 8 || ttSkip(f[8])
	}
	return false
}

// ttNumbers says every word is a finite number.
func ttNumbers(ss []string) bool {
	for _, s := range ss {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// ttRadius says s is a radius from 1 to max (the toolkit clamps below 1, so a smaller one is a bug on our side).
func ttRadius(s string, max float64) bool {
	v, err := strconv.ParseFloat(s, 64)
	return err == nil && !math.IsNaN(v) && v >= 1 && v <= max
}

// ttKVNum says s is key=<number> with the number in [lo, hi].
func ttKVNum(s, key string, lo, hi float64) bool {
	rest, ok := strings.CutPrefix(s, key+"=")
	if !ok {
		return false
	}
	v, err := strconv.ParseFloat(rest, 64)
	return err == nil && !math.IsNaN(v) && v >= lo && v <= hi
}

// ttHire says s is of=<a hire>.
func ttHire(s string, hires map[string]bool) bool {
	name, ok := strings.CutPrefix(s, "of=")
	return ok && hires[name]
}

// MaxSkipEntries is the toolkit's own skip= cap (ttSkip): a plan whose legs' skip union is longer would make every
// clearto and quiet line unusable, so validate refuses it before any GM line (review round 1, item 11).
const MaxSkipEntries = 16

// ttSkip says s is skip=<id,id,...>: at least one entry id, at most the toolkit's MaxSkipEntries, digits only.
func ttSkip(s string) bool {
	rest, ok := strings.CutPrefix(s, "skip=")
	if !ok || rest == "" {
		return false
	}
	parts := strings.Split(rest, ",")
	if len(parts) > MaxSkipEntries {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		if _, err := strconv.ParseUint(p, 10, 32); err != nil {
			return false
		}
	}
	return true
}
