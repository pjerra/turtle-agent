package raidwalk

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	gmNameRe   = regexp.MustCompile(`^[A-Za-z]{2,12}$`)
	teleWordRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)
	runStopRe  = regexp.MustCompile(`^\.bot run stop(?: time limit reached \(raid-walk ([0-9]{1,3})m\))?$`)
	tierWordRe = regexp.MustCompile(`^(?:t|tier)([1-3])$`)
)

// AllowedLine says whether raid-walk may send line through the gm RPC (spec 3.6): every line must match one of
// the forms, with its arguments checked, and a bot name must be one of the walk's hires. The command checks every
// gm line with it before sending; there is no second copy of the rule.
func AllowedLine(line string, hires map[string]bool) bool {
	if strings.ContainsAny(line, "\r\n") {
		return false
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
		m, err := strconv.ParseUint(f[5], 10, 32)
		return err == nil && m > 0
	case ".tele":
		if len(f) != 4 || f[1] != "name" || !hires[f[2]] {
			return false
		}
		return teleWordRe.MatchString(f[3])
	case ".summon", ".revive":
		return len(f) == 2 && hires[f[1]]
	case ".bot":
		if len(f) < 3 {
			return false
		}
		switch {
		case f[1] == "hire" && f[2] == "raid":
			return allowedHireRaid(f[3:])
		case f[1] == "gear" && f[2] == "raid":
			return len(f) == 6 && gmNameRe.MatchString(f[3]) && f[4] == "preset" && gearPresetRe.MatchString(f[5])
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
