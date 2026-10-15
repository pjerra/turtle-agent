package dashboard

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A digest is what the map layers need of one run trace: where every bot walked, where the party stood still, which walks were
// refused, where bots died, where it rested, which packs were pulled and how, which objects were used. It is built by one pass over the
// trace lines (the same builder serves a finished file in the tests folder and the live trace, line by line as it grows), and every
// position is in the drawing's plane (Project) so that the page draws it without a transform.

const (
	// stallMinS is how long the lead must stand within stallRadius of one spot to count as a stall (the owner's 30 s).
	stallMinS = 30
	// stallRadius is how far (yards) the lead may wander and still be standing.
	stallRadius = 5.0
	// maxStalls and the other caps bound a digest, so that a long ugly run cannot make a huge answer.
	maxStalls  = 80
	maxMarks   = 200
	maxDeaths  = 400
	maxRests   = 80
	maxPulls   = 400
	maxObjects = 120
)

// DPath is one character's walk. P holds [x, y] in the plane and T the seconds since the run began, in step.
type DPath struct {
	Name string       `json:"name"`
	Role string       `json:"role,omitempty"`
	Lead bool         `json:"lead,omitempty"`
	P    [][2]float64 `json:"p"`
	T    []float64    `json:"t"`
}

// DStall is a place the lead stood for stallMinS seconds or more.
type DStall struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	From   float64 `json:"from"` // seconds since the run began
	To     float64 `json:"to"`
	Secs   float64 `json:"secs"`
	Leg    int     `json:"leg"`
	Reason string  `json:"reason"`
	Open   bool    `json:"open,omitempty"` // the party is still standing there
}

// DMark is a point with a reason: a refused walk (kind refused), a spot the navmesh does not reach (unreachable), a stuck bot (stuck), a
// death, a rest or a gathering.
type DMark struct {
	Kind string  `json:"kind,omitempty"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	T    float64 `json:"t"`
	Leg  int     `json:"leg"`
	Text string  `json:"text,omitempty"`
	Secs float64 `json:"secs,omitempty"`
}

// DPull is one pull of the run: the creature pulled and its place, and how it ended ("" while it goes on): won, evaded, no_engage, error, refused, retarget.
type DPull struct {
	Leg     int     `json:"leg"`
	Entry   uint32  `json:"entry"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	T       float64 `json:"t"`
	Outcome string  `json:"outcome,omitempty"`
	Kills   int     `json:"kills,omitempty"`
}

// DObject is a step on a game object (a door, chest, altar, keystone) with how it ended.
type DObject struct {
	Leg    int     `json:"leg"`
	Action string  `json:"action"` // use_object, loot_object, wait, open
	Entry  uint32  `json:"entry,omitempty"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	OK     bool    `json:"ok"`
	Step   int     `json:"step"`
	After  bool    `json:"after,omitempty"` // a step after the boss
	T      float64 `json:"t"`
	Text   string  `json:"text,omitempty"`
}

// DLeg is one section of the run: when it started and ended in seconds since the run began.
type DLeg struct {
	Leg    int     `json:"leg"`
	Name   string  `json:"name,omitempty"`
	StartS float64 `json:"start"`
	EndS   float64 `json:"end,omitempty"`
	Result string  `json:"result,omitempty"`
}

// RunDigest is the whole of it. Seconds is the length of the run so far (or in total when Ended).
type RunDigest struct {
	Run       string    `json:"run"`
	Plan      string    `json:"plan"`
	Map       uint32    `json:"map"`
	Mode      string    `json:"mode,omitempty"`
	Lead      string    `json:"lead,omitempty"`
	StartUnix int64     `json:"start_unix"`
	Seconds   float64   `json:"seconds"`
	Ended     bool      `json:"ended"`
	Reason    string    `json:"reason,omitempty"`
	LegsDone  int       `json:"legs_done"`
	Legs      []DLeg    `json:"legs"`
	Paths     []DPath   `json:"paths"`
	Stalls    []DStall  `json:"stalls"`
	Refusals  []DMark   `json:"refusals"`
	Deaths    []DMark   `json:"deaths"`
	Rests     []DMark   `json:"rests"`
	Gathers   []DMark   `json:"gathers"`
	Pulls     []DPull   `json:"pulls"`
	Objects   []DObject `json:"objects"`
	Truncated []string  `json:"truncated,omitempty"` // lists that hit their cap
}

type dParty struct{ name, role string }

// digestBuilder reads trace lines in file order.
type digestBuilder struct {
	run, plan, mode, lead string
	mapID                 uint32
	start                 time.Time
	startUnix             int64
	party                 map[string]dParty
	trails                map[string]*Trail
	lastPos               map[string][2]float64
	leadG                 string
	leadPos               [2]float64
	haveLead              bool
	lastT                 int64 // ms of the latest line
	lastFrame             int64 // ms of the latest frame
	ended                 bool
	reason                string
	endSeconds            float64
	legs                  []DLeg
	stalls                []DStall
	refusals, deaths      []DMark
	rests, gathers        []DMark
	pulls                 []DPull
	objects               []DObject
	// the stall under way
	anchor     [2]float64
	anchorT    int64
	anchorN    int
	anchorCB   int
	anchorAct  map[string]int
	anchorLeg  int
	haveAnchor bool
	curLeg     int
	trunc      map[string]bool
	version    uint64
}

func newDigestBuilder() *digestBuilder {
	return &digestBuilder{party: map[string]dParty{}, trails: map[string]*Trail{}, lastPos: map[string][2]float64{}, anchorAct: map[string]int{}, curLeg: -1, trunc: map[string]bool{}}
}

var (
	nearRe   = regexp.MustCompile(`near (-?\d+(?:\.\d+)?) (-?\d+(?:\.\d+)?) (-?\d+(?:\.\d+)?)`)
	objRe    = regexp.MustCompile(`game object (\d+)`)
	moveRe   = regexp.MustCompile(`^cmd move (-?\d+(?:\.\d+)?) (-?\d+(?:\.\d+)?)`)
	gatherRe = regexp.MustCompile(`(?i)\bgathers?\b`)
	legNumRe = regexp.MustCompile(`\bleg (\d+)\b`)
	stepNoRe = regexp.MustCompile(`step (\d+)`)
)

// plane maps a game position to the drawing's plane, rounded to a tenth of a yard (and never a negative zero, which JSON would print as -0).
func plane(x, y float64) (float64, float64) {
	px, py := Project(float32(x), float32(y))
	return round1(px) + 0, round1(py) + 0
}

func (b *digestBuilder) sec(ms int64) float64 { return round1(float64(ms) / 1000) }

// Line takes one trace line. A line it cannot read is ignored.
func (b *digestBuilder) Line(line []byte) {
	if len(line) == 0 {
		return
	}
	if strings.HasPrefix(string(line[:min(len(line), 20)]), `{"v":1,"k":"frame"`) {
		b.frame(line)
		return
	}
	if strings.HasPrefix(string(line[:min(len(line), 20)]), `{"v":1,"k":"mobs"`) {
		return
	}
	var env struct {
		K  string `json:"k"`
		Ev string `json:"ev"`
		T  int64  `json:"t"`
	}
	if json.Unmarshal(line, &env) != nil {
		return
	}
	if env.T > b.lastT {
		b.lastT = env.T
	}
	b.version++
	switch env.K {
	case "header":
		var h struct {
			Run       string `json:"run"`
			Plan      string `json:"plan"`
			Mode      string `json:"mode"`
			Lead      string `json:"lead"`
			Map       uint32 `json:"map"`
			Start     string `json:"start"`
			StartUnix int64  `json:"start_unix"`
		}
		if json.Unmarshal(line, &h) != nil {
			return
		}
		b.run, b.plan, b.mode, b.lead, b.mapID, b.startUnix = h.Run, h.Plan, h.Mode, h.Lead, h.Map, h.StartUnix
		if t, err := time.Parse(time.RFC3339Nano, h.Start); err == nil {
			b.start = t
		} else {
			b.start = time.Unix(h.StartUnix, 0)
		}
		if b.startUnix == 0 {
			b.startUnix = b.start.Unix()
		}
	case "party":
		var p struct {
			G    string `json:"g"`
			Name string `json:"name"`
			Role string `json:"role"`
			Lead bool   `json:"lead"`
		}
		if json.Unmarshal(line, &p) == nil && p.G != "" {
			b.party[p.G] = dParty{p.Name, p.Role}
			if p.Lead {
				b.leadG, b.lead = p.G, p.Name
			}
		}
	case "end":
		var e struct {
			Reason  string  `json:"reason"`
			Seconds float64 `json:"seconds"`
		}
		if json.Unmarshal(line, &e) == nil {
			b.ended, b.reason, b.endSeconds = true, e.Reason, e.Seconds
		}
	case "event":
		b.event(env.Ev, env.T, line)
	}
}

type frameBot struct {
	G    string  `json:"g"`
	CB   bool    `json:"cb"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Act  string  `json:"act"`
	Lead bool    `json:"lead"`
}

func (b *digestBuilder) frame(line []byte) {
	var f struct {
		T    int64      `json:"t"`
		Bots []frameBot `json:"bots"`
	}
	if json.Unmarshal(line, &f) != nil {
		return
	}
	if f.T > b.lastT {
		b.lastT = f.T
	}
	b.lastFrame = f.T
	b.version++
	at := b.start.Add(time.Duration(f.T) * time.Millisecond)
	flagged := false
	for _, bot := range f.Bots {
		flagged = flagged || bot.Lead
	}
	for _, bot := range f.Bots {
		if bot.G == "" || (bot.X == 0 && bot.Y == 0) {
			continue
		}
		x, y := plane(bot.X, bot.Y)
		t := b.trails[bot.G]
		if t == nil {
			t = &Trail{}
			b.trails[bot.G] = t
		}
		t.AddAt(b.run, x, y, at)
		b.lastPos[bot.G] = [2]float64{x, y}
		if bot.Lead || (!flagged && bot.G == b.leadG) {
			b.leadG = bot.G
			b.leadPos, b.haveLead = [2]float64{x, y}, true
			b.watchStall(x, y, f.T, bot.CB, bot.Act)
		}
	}
}

// watchStall follows the lead frame by frame: it stands while it stays within stallRadius of the spot it came to.
func (b *digestBuilder) watchStall(x, y float64, t int64, cb bool, act string) {
	if b.haveAnchor && math.Hypot(x-b.anchor[0], y-b.anchor[1]) <= stallRadius {
		b.anchorN++
		if cb {
			b.anchorCB++
		}
		b.anchorAct[act]++
		return
	}
	b.closeStall(false)
	b.anchor, b.anchorT, b.haveAnchor = [2]float64{x, y}, t, true
	b.anchorN, b.anchorCB, b.anchorLeg = 1, 0, b.curLeg
	if cb {
		b.anchorCB = 1
	}
	b.anchorAct = map[string]int{act: 1}
}

// closeStall ends the stall under way; it is kept when it lasted stallMinS. open says the party is still there.
func (b *digestBuilder) closeStall(open bool) {
	if s, ok := b.currentStall(open); ok {
		if len(b.stalls) >= maxStalls {
			b.trunc["stalls"] = true
			// keep the longest
			sort.SliceStable(b.stalls, func(i, j int) bool { return b.stalls[i].Secs > b.stalls[j].Secs })
			b.stalls = b.stalls[:maxStalls-1]
		}
		b.stalls = append(b.stalls, s)
	}
}

// currentStall is the stall under way, when it has lasted long enough.
func (b *digestBuilder) currentStall(open bool) (DStall, bool) {
	if !b.haveAnchor || b.lastFrameT()-b.anchorT < stallMinS*1000 {
		return DStall{}, false
	}
	to := b.lastFrameT()
	s := DStall{X: b.anchor[0], Y: b.anchor[1], From: b.sec(b.anchorT), To: b.sec(to), Secs: b.sec(to - b.anchorT), Leg: b.anchorLeg, Open: open}
	s.Reason = b.stallReason(b.anchorT, to)
	return s, true
}

func (b *digestBuilder) lastFrameT() int64 { return b.lastFrame }

// stallReason says why the party stood: fighting, resting (a rest gate ran meanwhile), or what the lead was doing.
func (b *digestBuilder) stallReason(from, to int64) string {
	if b.anchorN > 0 && float64(b.anchorCB)/float64(b.anchorN) > 0.5 {
		return "in a fight"
	}
	for _, r := range b.rests {
		if r.Secs >= 3 && int64(r.T*1000) >= from-2000 && int64(r.T*1000) <= to+2000 {
			return "resting before the next pull"
		}
	}
	best, n := "", 0
	for a, c := range b.anchorAct {
		if c > n {
			best, n = a, c
		}
	}
	switch {
	case strings.Contains(best, "stay"):
		return "holding position (stay)"
	case strings.Contains(best, "pull"):
		return "waiting to pull"
	case best != "":
		return "idle (" + best + ")"
	}
	return "waiting"
}

func addCapped[T any](list []T, v T, limit int, name string, trunc map[string]bool) []T {
	if len(list) >= limit {
		trunc[name] = true
		return append(list[1:limit:limit], v) // the oldest goes
	}
	return append(list, v)
}

func atoiSafe(s string) int { n, _ := strconv.Atoi(s); return n }

func atofSafe(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

// refusalCodes are the answers to a walk or a pull that say the ground is the problem; the others (the tank is busy, the target is dead)
// are the run's own timing and would only clutter the map.
var refusalCodes = map[string]bool{"unreachable": true, "off-mesh": true, "no-los": true, "too-far": true, "failed": true, "unavailable": true}

func (b *digestBuilder) event(ev string, t int64, line []byte) {
	ts := b.sec(t)
	switch ev {
	case "leg":
		var e struct {
			Leg    int    `json:"leg"`
			Name   string `json:"name"`
			Phase  string `json:"phase"`
			Result string `json:"result"`
		}
		if json.Unmarshal(line, &e) != nil {
			return
		}
		switch e.Phase {
		case "start":
			b.curLeg = e.Leg
			b.legs = append(b.legs, DLeg{Leg: e.Leg, Name: e.Name, StartS: ts})
		case "end":
			for i := len(b.legs) - 1; i >= 0; i-- {
				if b.legs[i].Leg == e.Leg {
					b.legs[i].EndS, b.legs[i].Result = ts, e.Result
					break
				}
			}
		}
	case "rest":
		var e struct {
			Leg     int     `json:"leg"`
			Seconds float64 `json:"seconds"`
		}
		if json.Unmarshal(line, &e) != nil || e.Seconds < 3 || !b.haveLead {
			return
		}
		b.rests = addCapped(b.rests, DMark{Kind: "rest", X: b.leadPos[0], Y: b.leadPos[1], T: ts, Leg: e.Leg, Secs: round1(e.Seconds)}, maxRests, "rests", b.trunc)
	case "note":
		var e struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(line, &e) != nil || !b.haveLead || !gatherRe.MatchString(e.Text) {
			return
		}
		leg := b.curLeg
		if m := legNumRe.FindStringSubmatch(e.Text); m != nil {
			leg = atoiSafe(m[1])
		}
		if n := len(b.gathers); n > 0 {
			g := b.gathers[n-1]
			if ts-g.T < 20 && math.Hypot(g.X-b.leadPos[0], g.Y-b.leadPos[1]) < 8 {
				return
			}
		}
		b.gathers = addCapped(b.gathers, DMark{Kind: "gather", X: b.leadPos[0], Y: b.leadPos[1], T: ts, Leg: leg, Text: firstLine(e.Text)}, maxRests, "gathers", b.trunc)
	case "order":
		var e struct {
			Reply   string `json:"reply"`
			Code    string `json:"code"`
			Message string `json:"message"`
			Text    string `json:"text"`
			Verb    string `json:"verb"`
		}
		if json.Unmarshal(line, &e) != nil || !strings.EqualFold(e.Reply, "err") {
			return
		}
		if e.Verb != "move" && !refusalCodes[e.Code] {
			return
		}
		x, y, ok := b.leadPos[0], b.leadPos[1], b.haveLead
		if m := moveRe.FindStringSubmatch(e.Text); m != nil {
			x, y = plane(atofSafe(m[1]), atofSafe(m[2]))
			ok = true
		}
		if !ok {
			return
		}
		text := strings.TrimSpace(e.Code + ": " + e.Message)
		b.refusals = addCapped(b.refusals, DMark{Kind: "refused", X: x, Y: y, T: ts, Leg: b.curLeg, Text: firstLine(text)}, maxMarks, "refusals", b.trunc)
	case "unreachable":
		var e struct {
			Leg  int     `json:"leg"`
			Kind string  `json:"kind"`
			What string  `json:"what"`
			X    float64 `json:"x"`
			Y    float64 `json:"y"`
		}
		if json.Unmarshal(line, &e) != nil {
			return
		}
		x, y := plane(e.X, e.Y)
		b.refusals = addCapped(b.refusals, DMark{Kind: "unreachable", X: x, Y: y, T: ts, Leg: e.Leg, Text: firstLine(strings.TrimSpace("unreachable " + e.Kind + ": " + e.What))}, maxMarks, "refusals", b.trunc)
	case "stuck":
		var e struct {
			Name    string  `json:"name"`
			Seconds float64 `json:"seconds"`
			Leg     int     `json:"leg"`
			X       float64 `json:"x"`
			Y       float64 `json:"y"`
		}
		if json.Unmarshal(line, &e) != nil {
			return
		}
		x, y := plane(e.X, e.Y)
		if n := len(b.refusals); n > 0 {
			r := b.refusals[n-1]
			if r.Kind == "stuck" && ts-r.T < 15 && math.Hypot(r.X-x, r.Y-y) < 5 {
				return
			}
		}
		b.refusals = addCapped(b.refusals, DMark{Kind: "stuck", X: x, Y: y, T: ts, Leg: e.Leg, Text: e.Name + " stuck", Secs: round1(e.Seconds)}, maxMarks, "refusals", b.trunc)
	case "death":
		var e struct {
			A    string `json:"a"`
			Name string `json:"name"`
			Leg  int    `json:"leg"`
		}
		if json.Unmarshal(line, &e) != nil {
			return
		}
		p, ok := b.lastPos[e.A]
		if !ok {
			p, ok = b.leadPos, b.haveLead
		}
		if !ok {
			return
		}
		b.deaths = addCapped(b.deaths, DMark{Kind: "death", X: p[0], Y: p[1], T: ts, Leg: e.Leg, Text: e.Name + " died"}, maxDeaths, "deaths", b.trunc)
	case "pull_start":
		var e struct {
			Entry uint32  `json:"entry"`
			Leg   int     `json:"leg"`
			X     float64 `json:"x"`
			Y     float64 `json:"y"`
		}
		if json.Unmarshal(line, &e) != nil {
			return
		}
		x, y := plane(e.X, e.Y)
		b.pulls = addCapped(b.pulls, DPull{Leg: e.Leg, Entry: e.Entry, X: x, Y: y, T: ts}, maxPulls, "pulls", b.trunc)
	case "pull_end":
		var e struct {
			Leg     int    `json:"leg"`
			Outcome string `json:"outcome"`
			Kills   int    `json:"kills"`
		}
		if json.Unmarshal(line, &e) != nil {
			return
		}
		for i := len(b.pulls) - 1; i >= 0; i-- {
			if b.pulls[i].Outcome == "" && b.pulls[i].Leg == e.Leg {
				b.pulls[i].Outcome, b.pulls[i].Kills = e.Outcome, e.Kills
				break
			}
		}
	case "step":
		var e struct {
			Action string `json:"action"`
			OK     *bool  `json:"ok"`
			Leg    int    `json:"leg"`
			Step   int    `json:"step"`
			Text   string `json:"text"`
		}
		if json.Unmarshal(line, &e) != nil {
			return
		}
		switch e.Action {
		case "use_object", "loot_object", "wait", "open", "wait_object":
		default:
			return
		}
		m := nearRe.FindStringSubmatch(e.Text)
		if m == nil {
			return
		}
		x, y := plane(atofSafe(m[1]), atofSafe(m[2]))
		o := DObject{Leg: e.Leg, Action: e.Action, X: x, Y: y, OK: e.OK != nil && *e.OK, Step: e.Step, After: strings.Contains(e.Text, "after_boss"), T: ts, Text: firstLine(e.Text)}
		if mm := objRe.FindStringSubmatch(e.Text); mm != nil {
			o.Entry = uint32(atoiSafe(mm[1]))
		}
		b.objects = addCapped(b.objects, o, maxObjects, "objects", b.trunc)
	}
}

// Snapshot is the digest so far. minutes > 0 keeps only the last minutes of every walk (the stalls, marks and the rest are not cut).
func (b *digestBuilder) Snapshot(minutes int) *RunDigest {
	d := &RunDigest{Run: b.run, Plan: b.plan, Map: b.mapID, Mode: b.mode, Lead: b.lead, StartUnix: b.startUnix, Ended: b.ended, Reason: b.reason,
		Seconds: round1(float64(b.lastT) / 1000), Legs: append([]DLeg{}, b.legs...), Stalls: append([]DStall{}, b.stalls...),
		Refusals: append([]DMark{}, b.refusals...), Deaths: append([]DMark{}, b.deaths...), Rests: append([]DMark{}, b.rests...), Gathers: append([]DMark{}, b.gathers...),
		Pulls: append([]DPull{}, b.pulls...), Objects: append([]DObject{}, b.objects...), Paths: []DPath{}}
	if b.ended && b.endSeconds > 0 {
		d.Seconds = round1(b.endSeconds)
	}
	for _, l := range d.Legs {
		if l.Result == "done" {
			d.LegsDone++
		}
	}
	if s, ok := b.currentStall(!b.ended); ok {
		d.Stalls = append(d.Stalls, s)
	}
	if len(b.trunc) > 0 {
		for k := range b.trunc {
			d.Truncated = append(d.Truncated, k)
		}
		sort.Strings(d.Truncated)
	}
	cut := -1.0
	if minutes > 0 {
		cut = float64(b.lastT)/1000 - float64(minutes*60)
	}
	for g, t := range b.trails {
		pts, ts := t.Points(), t.Times()
		p := DPath{Name: g, P: [][2]float64{}, T: []float64{}, Lead: g == b.leadG}
		if pp, ok := b.party[g]; ok {
			p.Name, p.Role = pp.name, pp.role
		}
		for i := range pts {
			rel := float64(ts[i] - b.startUnix)
			if rel < cut {
				continue
			}
			p.P, p.T = append(p.P, pts[i]), append(p.T, rel)
		}
		if len(p.P) > 0 {
			d.Paths = append(d.Paths, p)
		}
	}
	sort.SliceStable(d.Paths, func(i, j int) bool {
		if d.Paths[i].Lead != d.Paths[j].Lead {
			return d.Paths[i].Lead
		}
		return d.Paths[i].Name < d.Paths[j].Name
	})
	return d
}

// DigestBytes reads a whole trace file into a digest (the file is only read).
func digestFile(data []byte) *digestBuilder {
	b := newDigestBuilder()
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		var line []byte
		if i < 0 {
			line, data = data, nil
		} else {
			line, data = data[:i], data[i+1:]
		}
		if len(line) <= maxLine {
			b.Line(line)
		}
	}
	return b
}
