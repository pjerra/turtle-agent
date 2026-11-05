package tbm

import (
	"math"
	"strconv"
	"strings"
)

// The scout's wire lines (plan-scout spec 4.2, RemoteCommanderPolicy.h ScoutCfgLine and its siblings). Every field is a
// number in fixed %.2f form, a fixed word or a hex path type, so none needs unescaping.

// The path verdict words (RemoteCommander::MoveVerdictCode).
const (
	ScoutVerdictOK          = "ok"
	ScoutVerdictTooFar      = "too-far"
	ScoutVerdictUnreachable = "unreachable"
	ScoutVerdictOffMesh     = "off-mesh"
)

// ScoutCfg is a TBM:SCOUT_CFG reply: the live server's run-leader rules.
type ScoutCfg struct {
	ID uint32
	// ReactDistance is AiPlayerbot.ReactDistance, RateCreatureAggro the core's Rate.Creature.Aggro.
	ReactDistance      float32
	RateCreatureAggro  float32
	FamilyAssistRadius float32
}

// ScoutPath is a TBM:SCOUT_PATH reply: one lead-move check from a hop's origin to a point.
type ScoutPath struct {
	ID       uint32
	Verdict  string
	PathType uint32
	Length   float32
	End      [3]float32
	// EndOffset is how far the path's actual end lies from the point (CheckMove's off-mesh rule).
	EndOffset float32
	// SnappedZ is the point's z after ChooseFloorZ.
	SnappedZ float32
	// StartSwim and EndSwim are Terrain::IsSwimmable at the origin and the snapped point.
	StartSwim bool
	EndSwim   bool
}

// Underwater is the core's PATHFIND_UNDERWATER bit (0x80): the straight two-point swim line.
const pathfindUnderwater = 0x80

// Underwater says the path is the core's swim line (type bit 0x80).
func (p ScoutPath) Underwater() bool { return p.PathType&pathfindUnderwater != 0 }

// ScoutWalk is a TBM:SCOUT_WALK reply: the walk started, with the path type the module computed.
type ScoutWalk struct {
	ID       uint32
	PathType uint32
}

// ScoutPos is a TBM:SCOUT_POS reply: where the server has the GM.
type ScoutPos struct {
	ID                        uint32
	X, Y, Z                   float32
	Moving, InWater, Swimming bool
}

// ParseScoutCfg reads TBM:SCOUT_CFG|<id>|<reactDistance>|<rateCreatureAggro>|<familyAssistRadius>.
func ParseScoutCfg(line string) (ScoutCfg, bool) {
	rest, ok := strings.CutPrefix(line, "TBM:SCOUT_CFG|")
	if !ok {
		return ScoutCfg{}, false
	}
	f := strings.Split(rest, "|")
	if len(f) != 4 {
		return ScoutCfg{}, false
	}
	id, ok := scoutID(f[0])
	if !ok {
		return ScoutCfg{}, false
	}
	v := make([]float32, 3)
	for i := range v {
		x, ok := scoutNumber(f[i+1])
		if !ok {
			return ScoutCfg{}, false
		}
		v[i] = x
	}
	return ScoutCfg{ID: id, ReactDistance: v[0], RateCreatureAggro: v[1], FamilyAssistRadius: v[2]}, true
}

// ParseScoutPath reads
// TBM:SCOUT_PATH|<id>|<verdict>|0x<type>|<length>|<ex> <ey> <ez>|<endOffset>|<snappedZ>|<startSwim>|<endSwim>.
func ParseScoutPath(line string) (ScoutPath, bool) {
	rest, ok := strings.CutPrefix(line, "TBM:SCOUT_PATH|")
	if !ok {
		return ScoutPath{}, false
	}
	f := strings.Split(rest, "|")
	if len(f) != 9 {
		return ScoutPath{}, false
	}
	id, ok := scoutID(f[0])
	if !ok {
		return ScoutPath{}, false
	}
	switch f[1] {
	case ScoutVerdictOK, ScoutVerdictTooFar, ScoutVerdictUnreachable, ScoutVerdictOffMesh:
	default:
		return ScoutPath{}, false
	}
	typ, ok := scoutHex(f[2])
	if !ok {
		return ScoutPath{}, false
	}
	length, ok := scoutNumber(f[3])
	if !ok {
		return ScoutPath{}, false
	}
	end, ok := scoutPoint(f[4])
	if !ok {
		return ScoutPath{}, false
	}
	offset, ok := scoutNumber(f[5])
	if !ok {
		return ScoutPath{}, false
	}
	snapped, ok := scoutNumber(f[6])
	if !ok {
		return ScoutPath{}, false
	}
	startSwim, ok := scoutFlag(f[7])
	if !ok {
		return ScoutPath{}, false
	}
	endSwim, ok := scoutFlag(f[8])
	if !ok {
		return ScoutPath{}, false
	}
	return ScoutPath{ID: id, Verdict: f[1], PathType: typ, Length: length, End: end, EndOffset: offset,
		SnappedZ: snapped, StartSwim: startSwim, EndSwim: endSwim}, true
}

// ParseScoutWalk reads TBM:SCOUT_WALK|<id>|started|0x<type>.
func ParseScoutWalk(line string) (ScoutWalk, bool) {
	rest, ok := strings.CutPrefix(line, "TBM:SCOUT_WALK|")
	if !ok {
		return ScoutWalk{}, false
	}
	f := strings.Split(rest, "|")
	if len(f) != 3 || f[1] != "started" {
		return ScoutWalk{}, false
	}
	id, ok := scoutID(f[0])
	if !ok {
		return ScoutWalk{}, false
	}
	typ, ok := scoutHex(f[2])
	if !ok {
		return ScoutWalk{}, false
	}
	return ScoutWalk{ID: id, PathType: typ}, true
}

// ParseScoutPos reads TBM:SCOUT_POS|<id>|<x> <y> <z>|<moving>|<inWater>|<swimming>.
func ParseScoutPos(line string) (ScoutPos, bool) {
	rest, ok := strings.CutPrefix(line, "TBM:SCOUT_POS|")
	if !ok {
		return ScoutPos{}, false
	}
	f := strings.Split(rest, "|")
	if len(f) != 5 {
		return ScoutPos{}, false
	}
	id, ok := scoutID(f[0])
	if !ok {
		return ScoutPos{}, false
	}
	p, ok := scoutPoint(f[1])
	if !ok {
		return ScoutPos{}, false
	}
	moving, ok := scoutFlag(f[2])
	if !ok {
		return ScoutPos{}, false
	}
	inWater, ok := scoutFlag(f[3])
	if !ok {
		return ScoutPos{}, false
	}
	swimming, ok := scoutFlag(f[4])
	if !ok {
		return ScoutPos{}, false
	}
	return ScoutPos{ID: id, X: p[0], Y: p[1], Z: p[2], Moving: moving, InWater: inWater, Swimming: swimming}, true
}

// scoutID reads the request id: 1-9 digits, not zero.
func scoutID(s string) (uint32, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil || v == 0 {
		return 0, false
	}
	return uint32(v), true
}

// scoutNumber reads a finite decimal.
func scoutNumber(s string) (float32, bool) {
	v, err := strconv.ParseFloat(s, 32)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return float32(v), true
}

// scoutPoint reads "x y z".
func scoutPoint(s string) ([3]float32, bool) {
	f := strings.Fields(s)
	if len(f) != 3 {
		return [3]float32{}, false
	}
	var out [3]float32
	for i := range f {
		v, ok := scoutNumber(f[i])
		if !ok {
			return [3]float32{}, false
		}
		out[i] = v
	}
	return out, true
}

// scoutHex reads a 0x-prefixed path type.
func scoutHex(s string) (uint32, bool) {
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return 0, false
	}
	v, err := strconv.ParseUint(s[2:], 16, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// scoutFlag reads 0 or 1.
func scoutFlag(s string) (bool, bool) {
	switch s {
	case "0":
		return false, true
	case "1":
		return true, true
	}
	return false, false
}
