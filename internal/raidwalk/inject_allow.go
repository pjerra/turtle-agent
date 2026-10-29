package raidwalk

import (
	"math"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// AllowedInjectLine says whether the fault injector may send line through the gm RPC (spec raid-walk-addons 1.4).
// The command checks every injector line with it before sending; a refused line is a programming error and stops the
// walk, as raid-walk's own AllowedLine does. The walk's own lines keep going through AllowedLine.
func AllowedInjectLine(line string, c InjectCtx) bool {
	if strings.ContainsAny(line, "\r\n") {
		return false
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case ".gm":
		// Only the invisible switch of Prepare; nothing else may be typed into the GM console.
		return c.Fault == "" && len(f) == 3 && f[1] == "visible" && f[2] == "off"
	case ".go":
		if len(f) != 6 || f[1] != "xyz" {
			return false
		}
		var p [3]float64
		for i, v := range f[2:5] {
			x, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(x) || math.IsInf(x, 0) {
				return false
			}
			p[i] = x
		}
		m, err := strconv.ParseUint(f[5], 10, 32)
		if err != nil || uint32(m) != c.Map {
			return false
		}
		if c.Fault == "" {
			return true
		}
		if !c.HaveLead {
			return false
		}
		dx, dy, dz := p[0]-float64(c.Lead.X), p[1]-float64(c.Lead.Y), p[2]-float64(c.Lead.Z)
		return math.Sqrt(dx*dx+dy*dy+dz*dz) <= 260
	case ".summon":
		return c.Fault == InjectRunaway && len(f) == 2 && c.Hires[f[1]] && f[1] == c.Victim
	case ".freeze":
		if !c.SelfSelected || len(f) != 2 || !c.Hires[f[1]] {
			return false
		}
		return (c.Fault == InjectRunaway || c.Fault == InjectLag) && f[1] == c.Victim
	case ".unfreeze":
		// Cleanup releases any hire the injector froze; the selection must be the GM itself.
		return c.SelfSelected && len(f) == 2 && c.Hires[f[1]]
	case ".die":
		return len(f) == 1 && c.Fault == InjectKillMT && c.SelectedHire != "" && c.SelectedHire == c.Victim
	}
	return false
}

// AllowedInjectOrder says whether the fault injector may send this commander order (spec 1.4): add-pack's attack on
// its chosen candidate, pulled by a hire. Nothing else.
func AllowedInjectOrder(verb string, args []string, c InjectCtx) bool {
	if c.Fault != InjectAddPack || verb != "attack" || len(args) != 2 {
		return false
	}
	if args[0] != candidateHex(c.Candidate) || !c.Hires[args[1]] {
		return false
	}
	return c.Candidate != 0
}

// candidateHex is the GUID form the commander's `cmd attack` takes: 0x and 16 uppercase hex digits.
func candidateHex(g packet.GUID) string { return g.String() }
