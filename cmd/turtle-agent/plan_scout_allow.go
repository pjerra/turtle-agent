// plan_scout_allow.go: the strict allow-list of the scout's live pass (spec 5.6). Every GM line and every toolkit line
// is checked here before it is sent: the scout moves its own GM, teleports nowhere else, hires nothing, orders no bot.
package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

var (
	// scoutIDRe is the module's request id: #<1-9 digits>.
	scoutIDRe = regexp.MustCompile(`^#[1-9][0-9]{0,8}$`)
	// scoutGUIDRe is a guid as the toolkit's findByHex takes it: 0x and 1-16 hex digits.
	scoutGUIDRe = regexp.MustCompile(`^0[xX][0-9A-Fa-f]{1,16}$`)
)

// scoutAllowedLine says whether the scout may send line through the gm RPC: `.gm on`, `.gm visible off`, a `.go xyz` on
// the plan's map or the portal's map, `.instance unbind <plan map>` and `.bot scout cfg|path|walk|where` with an id and
// finite numbers. Everything else (`.die`, `.tele`, `.bot run`, `.bot cmd`, `.appear`, `.summon`, a hire) is refused.
func scoutAllowedLine(line string, planMap, portalMap uint32) bool {
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return false
	}
	switch line {
	case ".gm on", raidwalk.GMVisibleOff:
		return true
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case ".go":
		if len(f) != 6 || f[1] != "xyz" || !scoutNumbers(f[2:5]) {
			return false
		}
		m, err := strconv.ParseUint(f[5], 10, 32)
		return err == nil && (uint32(m) == planMap || uint32(m) == portalMap)
	case ".instance":
		return len(f) == 3 && f[1] == "unbind" && f[2] == strconv.FormatUint(uint64(planMap), 10)
	case ".bot":
		return scoutBotForm(f[1:])
	}
	return false
}

// scoutBotForm checks `.bot scout <form>`.
func scoutBotForm(f []string) bool {
	if len(f) < 3 || f[0] != "scout" {
		return false
	}
	switch f[1] {
	case "cfg", "where":
		return len(f) == 3 && scoutIDRe.MatchString(f[2])
	case "walk":
		return len(f) == 6 && scoutIDRe.MatchString(f[2]) && scoutNumbers(f[3:6])
	case "path":
		if len(f) != 6 && len(f) != 10 {
			return false
		}
		if !scoutIDRe.MatchString(f[2]) || !scoutNumbers(f[3:6]) {
			return false
		}
		if len(f) == 6 {
			return true
		}
		return f[6] == "from" && scoutNumbers(f[7:10])
	}
	return false
}

// scoutNumbers says every word is a finite number.
func scoutNumbers(ss []string) bool {
	for _, s := range ss {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// scoutAllowedTT says whether the scout may send cmd to the Eluna test toolkit: `q inst`, a `q units` read of at most 60
// yd around a point, or `q los` with 1-8 hex guids. One line, no `#` (the agent adds the id), at most the raid-walk cap.
func scoutAllowedTT(cmd string, planMap uint32) bool {
	if cmd == "" || len(cmd) > raidwalk.AllowedTTMax || strings.ContainsAny(cmd, "\r\n#") {
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
		case "units":
			// q units <r<=60> <x> <y> <z> all
			if len(f) != 7 || f[6] != "all" || !scoutNumbers(f[3:6]) {
				return false
			}
			r, err := strconv.ParseFloat(f[2], 64)
			return err == nil && !math.IsNaN(r) && r >= 1 && r <= 60
		case "los":
			// q los <x> <y> <z> <0xGUID>... (1-8 guids)
			if len(f) < 6 || len(f) > 13 || !scoutNumbers(f[2:5]) {
				return false
			}
			for _, g := range f[5:] {
				if !scoutGUIDRe.MatchString(g) {
					return false
				}
			}
			return true
		}
	}
	_ = planMap
	return false
}
