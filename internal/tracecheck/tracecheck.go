// Package tracecheck scores a run trace: pure functions of a trace file (internal/trace), so an old trace can be scored again after a
// threshold or a check changes. Each check returns a trace.Check with its id, a verdict (pass, fail, or na when the trace lacks what it
// needs), the measured value, the threshold, how many times it happened (count), and the times (ms into the run) of the frames or
// events that show it. The ids threat_off_tank, deaths, facing and stuck are the server scorer's (TortoiseBots tools/trace_score.py);
// the others look at what only the agent sees: its orders, rest gates, walks and feed.
package tracecheck

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/trace"
)

// Check ids.
const (
	IDRestShare   = "rest_share"
	IDStaleParty  = "stale_party"
	IDWalkStops   = "walk_stops"
	IDThreat      = "threat_off_tank"
	IDDeaths      = "deaths"
	IDRefusals    = "order_refusals"
	IDFeedStalls  = "feed_stalls"
	IDGMShortcuts = "gm_shortcuts"
	IDStuck       = "stuck"
	IDFacing      = "facing"

	// Thresholds of the spells check (its own id is IDSpells): they make warnings, never a fail.
	IDIdleShare      = "idle_share"
	IDInterruptCover = "interrupt_cover"
	IDCCSuccess      = "cc_success"

	maxEvidence    = 8
	minRunSeconds  = 60
	minOrders      = 3
	moveSpeed      = 1.5 // yd/s: faster is walking
	stillSpeed     = 0.5 // yd/s: slower is standing
	stopSeconds    = 1.5 // a stop is standing this long in a walk
	arriveSeconds  = 3.0 // standing in the last seconds of a walk is arriving, not stopping
	feedStallStart = 5.0 // a gap between two feed frames of more than this many seconds is a stall (counted in the detail)
	meleeYards     = 6.0 // facing: within this distance of its target a bot is in melee
	facingDegrees  = 90  // facing: more than this many degrees off the target is turned away
)

// Config holds the thresholds. Defaults are what a run of ours is held to.
type Config struct {
	RestShareMax   float64 // percent of the run spent in rest gates
	StaleSeconds   float64 // a member's mana unchanged this long while others change
	StopsPer100Max float64 // stops per 100 yd walked
	ThreatOffTankS float64 // seconds a non-tank may hold the top threat of a mob
	RefusalRateMax float64 // percent of orders refused
	FeedGapMax     float64 // seconds without a feed frame
	StuckSeconds   float64 // a member that should move stands still this long
	FacingHoldS    float64 // seconds a member in melee range stands turned away from its target

	// The spells check only warns: these are the limits of its warnings.
	IdleSharePct      float64 // percent of engaged mana-user frames without an executed action (a warning above this)
	InterruptCoverPct float64 // percent of enemy casts that met an interrupt (a warning below this)
	CCSuccessPct      float64 // percent of crowd-control actions whose target showed the cc flag (a warning below this)
}

// Defaults are the thresholds of the first set of checks.
func Defaults() Config {
	return Config{RestShareMax: 40, StaleSeconds: 60, StopsPer100Max: 4, ThreatOffTankS: 4, RefusalRateMax: 20, FeedGapMax: 10, StuckSeconds: 5, FacingHoldS: 2, IdleSharePct: 15, InterruptCoverPct: 30, CCSuccessPct: 50}
}

var thresholdIDs = []string{IDRestShare, IDStaleParty, IDWalkStops, IDThreat, IDRefusals, IDFeedStalls, IDStuck, IDFacing, IDIdleShare, IDInterruptCover, IDCCSuccess}

// Set changes the threshold of the check with the given id (the CLI's --set id=value). deaths and gm_shortcuts have none.
func (c *Config) Set(id string, v float64) error {
	switch id {
	case IDRestShare:
		c.RestShareMax = v
	case IDStaleParty:
		c.StaleSeconds = v
	case IDWalkStops:
		c.StopsPer100Max = v
	case IDThreat:
		c.ThreatOffTankS = v
	case IDRefusals:
		c.RefusalRateMax = v
	case IDFeedStalls:
		c.FeedGapMax = v
	case IDStuck:
		c.StuckSeconds = v
	case IDFacing:
		c.FacingHoldS = v
	case IDIdleShare:
		c.IdleSharePct = v
	case IDInterruptCover:
		c.InterruptCoverPct = v
	case IDCCSuccess:
		c.CCSuccessPct = v
	default:
		return fmt.Errorf("no threshold for check %q (have %s)", id, strings.Join(thresholdIDs, ", "))
	}
	return nil
}

// Score runs every check over the trace.
func Score(f *trace.File, c Config) []trace.Check {
	cs := []trace.Check{restShare(f, c), staleParty(f, c), walkStops(f, c), threatOffTank(f, c), deathsWipes(f), orderRefusals(f, c), feedStalls(f, c), gmShortcuts(f), stuck(f, c), facing(f, c), spellsCheck(f, c)}
	for i := range cs {
		if cs[i].FirstMS == 0 && len(cs[i].Evidence) > 0 {
			cs[i].FirstMS = cs[i].Evidence[0]
		}
	}
	return cs
}

func verdict(fail bool) string {
	if fail {
		return trace.Fail
	}
	return trace.Pass
}

func na(id, name, why string) trace.Check {
	return trace.Check{ID: id, Name: name, Verdict: trace.NA, Detail: why}
}

func num(d map[string]any, key string) float64 {
	switch v := d[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

func str(d map[string]any, key string) string {
	s, _ := d[key].(string)
	return s
}

func capEvidence(ev []int64) []int64 {
	if len(ev) > maxEvidence {
		ev = ev[:maxEvidence]
	}
	return ev
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func countIf(fail bool, n int) int {
	if !fail {
		return 0
	}
	return max(n, 1)
}

// ---- rest share ----

func restShare(f *trace.File, c Config) trace.Check {
	const id, name = IDRestShare, "Time in rest gates"
	total := f.Seconds()
	if total < minRunSeconds {
		return na(id, name, fmt.Sprintf("the run is %.0f s; a share needs at least %d s", total, minRunSeconds))
	}
	var rest float64
	var ev []int64
	n := 0
	for _, e := range f.Events {
		if e.Ev == "rest" {
			rest += num(e.D, "seconds")
			n++
			ev = append(ev, e.T)
		}
	}
	share := rest / total * 100
	fail := share > c.RestShareMax
	return trace.Check{ID: id, Name: name, Verdict: verdict(fail), Count: countIf(fail, 1), Value: round1(share), Threshold: c.RestShareMax, Unit: "%",
		Detail: fmt.Sprintf("%.0f s of %.0f s in %d rest gates", rest, total, n), Evidence: capEvidence(ev)}
}

// ---- stale party data ----

// staleParty finds a mana user whose power did not change for StaleSeconds while someone else's health or power did: the party
// list (or the feed) stopped updating it.
func staleParty(f *trace.File, c Config) trace.Check {
	const id, name = IDStaleParty, "Stale party data"
	type track struct {
		pw        uint32
		since     int64
		othersNow int // changes among the others since the power last changed
	}
	tr := map[string]*track{}
	prev := map[string]trace.Bot{}
	var best float64
	var bestWho string
	var bestAt, bestFrom int64
	tracked := map[string]bool{}
	spans := 0
	counted := map[string]bool{}
	for _, fr := range f.Frames {
		changed := map[string]bool{}
		for _, m := range fr.Bots {
			if p, ok := prev[m.G]; ok && (p.HP != m.HP || p.Power != m.Power) {
				changed[m.G] = true
			}
		}
		for _, m := range fr.Bots {
			t := tr[m.G]
			if m.PType != 0 || m.MaxPow == 0 || !m.Alive {
				delete(tr, m.G)
				continue
			}
			if t == nil || t.pw != m.Power {
				tracked[m.G] = true
				tr[m.G] = &track{pw: m.Power, since: fr.T}
				continue
			}
			for o := range changed {
				if o != m.G {
					t.othersNow++
					break
				}
			}
			if t.othersNow > 0 && m.Power < m.MaxPow {
				s := float64(fr.T-t.since) / 1000
				if s > best {
					best, bestWho, bestAt, bestFrom = s, f.Name(m.G), fr.T, t.since
				}
				if s > c.StaleSeconds && !counted[fmt.Sprint(m.G, t.since)] {
					counted[fmt.Sprint(m.G, t.since)] = true
					spans++
				}
			}
		}
		for _, m := range fr.Bots {
			prev[m.G] = m
		}
	}
	if len(tracked) == 0 || len(f.Frames) == 0 {
		return na(id, name, "no frames with a mana user")
	}
	d := "no member's mana stood still while others changed"
	var ev []int64
	if best > 0 {
		d = fmt.Sprintf("%s: mana unchanged for %.0f s (from %.0f s to %.0f s) while others changed", bestWho, best, float64(bestFrom)/1000, float64(bestAt)/1000)
		ev = []int64{bestFrom, bestAt}
	}
	fail := best > c.StaleSeconds
	return trace.Check{ID: id, Name: name, Verdict: verdict(fail), Count: countIf(fail, spans), Value: round1(best), Threshold: c.StaleSeconds, Unit: "s", Detail: d, Evidence: ev}
}

// ---- walk stop-and-go ----

type window struct{ from, to int64 }

// walkWindows pairs walk_start with walk_end; a walk with no end runs to the end of the trace.
func walkWindows(f *trace.File) []window {
	var out []window
	open := int64(-1)
	var last int64
	for _, e := range f.Events {
		last = max(last, e.T)
		switch e.Ev {
		case "walk_start":
			if open >= 0 {
				out = append(out, window{open, e.T})
			}
			open = e.T
		case "walk_end":
			if open >= 0 {
				out = append(out, window{open, e.T})
				open = -1
			}
		}
	}
	if open >= 0 {
		out = append(out, window{open, max(last, open)})
	}
	return out
}

// leadOf finds the member that walks in a frame: the bot marked lead, else the one named in the header.
func leadOf(f *trace.File, fr trace.Frame) (trace.Bot, bool) {
	var named string
	if m, ok := f.Member(f.Header.Lead); ok {
		named = m.G
	}
	for _, b := range fr.Bots {
		if b.Lead || (named != "" && b.G == named) {
			return b, !b.NoPos
		}
	}
	return trace.Bot{}, false
}

func dist2(a, b trace.Bot) float64 {
	return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y))
}

// walkStops counts the times the lead stood still in the middle of a walk, per 100 yd walked.
func walkStops(f *trace.File, c Config) trace.Check {
	const id, name = IDWalkStops, "Walk stop-and-go"
	wins := walkWindows(f)
	var dist float64
	stops := 0
	var ev []int64
	for _, w := range wins {
		var prev trace.Bot
		var prevT int64
		have, moving, counted := false, false, false
		var still float64
		for _, fr := range f.Frames {
			if fr.T < w.from || fr.T > w.to {
				continue
			}
			m, ok := leadOf(f, fr)
			if !ok {
				have = false
				continue
			}
			if have {
				dt := float64(fr.T-prevT) / 1000
				if dt > 0 && dt <= 5 {
					d := dist2(prev, m)
					dist += d
					speed := d / dt
					arriving := float64(w.to-fr.T)/1000 < arriveSeconds
					switch {
					case speed >= moveSpeed:
						moving, counted, still = true, false, 0
					case speed < stillSpeed && moving && !arriving:
						if still += dt; still >= stopSeconds && !counted {
							counted = true
							stops++
							ev = append(ev, fr.T)
						}
					}
				}
			}
			prev, prevT, have = m, fr.T, true
		}
	}
	if len(wins) == 0 || dist < 100 {
		return na(id, name, fmt.Sprintf("%.0f yd walked in %d walks; a rate needs at least 100 yd", dist, len(wins)))
	}
	rate := float64(stops) * 100 / dist
	fail := rate > c.StopsPer100Max
	return trace.Check{ID: id, Name: name, Verdict: verdict(fail), Count: stops, Value: round1(rate), Threshold: c.StopsPer100Max, Unit: "stops/100 yd",
		Detail: fmt.Sprintf("%d stops in %.0f yd of %d walks", stops, dist, len(wins)), Evidence: capEvidence(ev)}
}

// ---- threat off the tank ----

func threatOffTank(f *trace.File, c Config) trace.Check {
	const id, name = IDThreat, "Threat held by a non-tank"
	// Pull windows: a frame counts inside a pull; with no pull events every frame counts.
	var pulls []window
	open := int64(-1)
	for _, e := range f.Events {
		switch e.Ev {
		case "pull_start":
			if open < 0 {
				open = e.T
			}
		case "pull_end":
			if open >= 0 {
				pulls = append(pulls, window{open, e.T})
				open = -1
			}
		}
	}
	if open >= 0 {
		pulls = append(pulls, window{open, math.MaxInt64})
	}
	inPull := func(t int64) bool {
		if len(pulls) == 0 {
			return true
		}
		for _, w := range pulls {
			if t >= w.from && t <= w.to {
				return true
			}
		}
		return false
	}
	// The tank: named in the header, or the party member with the tank role, or the one that holds the top threat most often.
	tank := ""
	if m, ok := f.Member(f.Header.Tank); ok {
		tank = m.G
	}
	if tank == "" {
		for _, m := range f.Party {
			if m.Role == "tank" {
				tank = m.G
			}
		}
	}
	member := map[string]bool{}
	for _, m := range f.Party {
		member[m.G] = true
	}
	if tank == "" {
		held := map[string]int{}
		for _, fr := range f.Frames {
			if !inPull(fr.T) {
				continue
			}
			for _, h := range fr.Mobs {
				if len(h.Threat) > 0 && h.HPPct > 0 && member[h.Threat[0].G] {
					held[h.Threat[0].G]++
				}
			}
		}
		best := 0
		for g, k := range held {
			if k > best || (k == best && g < tank) {
				tank, best = g, k
			}
		}
	}
	haveThreat := false
	open2 := map[string]int64{}
	var worst float64
	var worstWho string
	var ev []int64
	spans := map[string]bool{}
	combatFrames := 0
	for _, fr := range f.Frames {
		alive, inCombat := tankState(fr, tank)
		if inCombat {
			combatFrames++
		}
		if !inPull(fr.T) {
			open2 = map[string]int64{}
			continue
		}
		// A tank that is dead (or not in the frame) holds nothing: its death is not threat lost to a non-tank.
		if tank != "" && !alive {
			open2 = map[string]int64{}
			continue
		}
		seen := map[string]bool{}
		for _, h := range fr.Mobs {
			if len(h.Threat) == 0 || h.HPPct <= 0 {
				continue
			}
			haveThreat = true
			holder := h.Threat[0].G
			if tank == "" || !member[holder] || holder == tank {
				continue
			}
			// The mob must be on the holder: a mob that still swings at the tank, or that has no target (polymorphed, fleeing,
			// evading), is not threat lost to a non-tank. A tie with the tank is the tank's: the core keeps the tie in insertion
			// order, so the holder is listed first although the mob is still the tank's within the 110 %/130 % rule.
			if !h.Combat || h.Target != holder || tiedWithTank(h.Threat, tank) {
				continue
			}
			seen[h.G] = true
			from, ok := open2[h.G]
			if !ok {
				from = fr.T
				open2[h.G] = from
			}
			s := float64(fr.T-from) / 1000
			if s > c.ThreatOffTankS {
				spans[fmt.Sprint(h.G, from)] = true
			}
			if s > worst {
				worst, worstWho = s, f.Name(holder)
				ev = []int64{from, fr.T}
			}
		}
		for g := range open2 {
			if !seen[g] {
				delete(open2, g)
			}
		}
	}
	if !haveThreat {
		return na(id, name, "the trace has no threat data (group runs do not see threat lists)")
	}
	d := "the tank held the top threat of every mob"
	if worstWho != "" {
		d = fmt.Sprintf("%s held the top threat of a mob for %.0f s (tank %s)", worstWho, worst, f.Name(tank))
	}
	// The rate makes runs of different lengths comparable: the episodes per hour the tank spends in combat.
	interval := f.Header.IntervalMS
	if interval <= 0 {
		interval = 1000
	}
	combatS := float64(combatFrames) * float64(interval) / 1000
	if combatS > 0 {
		d += fmt.Sprintf("; %d spans in %.0f s of tank combat, %.0f per tank-combat-hour", len(spans), combatS, float64(len(spans))*3600/combatS)
	}
	fail := worst > c.ThreatOffTankS
	return trace.Check{ID: id, Name: name, Verdict: verdict(fail), Count: countIf(fail, len(spans)), Value: round1(worst), Threshold: c.ThreatOffTankS, Unit: "s", Detail: d, Evidence: ev}
}

// tankState reads the tank in a frame: whether it is alive and whether it is in combat. A frame that does not list the tank says neither.
func tankState(fr trace.Frame, tank string) (alive, combat bool) {
	if tank == "" {
		return false, false
	}
	for _, b := range fr.Bots {
		if b.G == tank {
			return b.Alive, b.Alive && b.Combat
		}
	}
	return false, false
}

// tiedWithTank says whether the tank sits level with the top of a threat list: the values are raw threat as a share of the top, so an
// equal share is a tie, and a tie between the tank and the holder counts as the tank's.
func tiedWithTank(thr []trace.Threat, tank string) bool {
	if tank == "" || len(thr) < 2 {
		return false
	}
	for _, t := range thr[1:] {
		if t.G == tank {
			return t.Pct == thr[0].Pct
		}
	}
	return false
}

// ---- deaths and wipes ----

func deathsWipes(f *trace.File) trace.Check {
	const id, name = IDDeaths, "Deaths and wipes"
	deaths, wipes := 0, 0
	var ev []int64
	var who []string
	for _, e := range f.Events {
		switch e.Ev {
		case "death":
			deaths++
			ev = append(ev, e.T)
			n := str(e.D, "name")
			if n == "" {
				n = f.Name(str(e.D, "a"))
			}
			who = append(who, n)
		case "wipe":
			wipes++
			ev = append(ev, e.T)
		}
	}
	d := "no death"
	if deaths+wipes > 0 {
		d = fmt.Sprintf("%d deaths (%s), %d wipes", deaths, strings.Join(who, ", "), wipes)
	}
	return trace.Check{ID: id, Name: name, Verdict: verdict(deaths+wipes > 0), Count: deaths + wipes, Value: float64(deaths), Threshold: 0, Unit: "deaths", Detail: d, Evidence: capEvidence(ev)}
}

// ---- order refusals ----

func orderRefusals(f *trace.File, c Config) trace.Check {
	const id, name = IDRefusals, "Order refusals"
	sent, refused, unanswered := 0, 0, 0
	var ev []int64
	codes := map[string]int{}
	for _, e := range f.Events {
		switch e.Ev {
		case "order":
			sent++
			switch str(e.D, "reply") {
			case "err":
				refused++
				codes[str(e.D, "code")]++
				ev = append(ev, e.T)
			case "timeout":
				unanswered++
			}
		case "refusal":
			refused++
			codes[str(e.D, "code")]++
			ev = append(ev, e.T)
		}
	}
	if sent < minOrders {
		return na(id, name, fmt.Sprintf("%d orders sent; a rate needs at least %d", sent, minOrders))
	}
	rate := min(float64(refused)*100/float64(sent), 100)
	var cs []string
	for k, n := range codes {
		cs = append(cs, fmt.Sprintf("%s x%d", k, n))
	}
	sort.Strings(cs)
	fail := rate > c.RefusalRateMax
	return trace.Check{ID: id, Name: name, Verdict: verdict(fail), Count: refused, Value: round1(rate), Threshold: c.RefusalRateMax, Unit: "%",
		Detail: fmt.Sprintf("%d of %d orders refused (%s), %d unanswered", refused, sent, strings.Join(cs, ", "), unanswered), Evidence: capEvidence(ev)}
}

// ---- feed stalls ----

func feedStalls(f *trace.File, c Config) trace.Check {
	const id, name = IDFeedStalls, "Feed stalls"
	var times []int64
	for _, fr := range f.Frames {
		if fr.Via == "feed" {
			times = append(times, fr.T)
		}
	}
	if len(times) < 2 {
		return na(id, name, "no feed frames (a group run has none)")
	}
	var worst float64
	stalls := 0
	var ev []int64
	for i := 1; i < len(times); i++ {
		g := float64(times[i]-times[i-1]) / 1000
		if g > feedStallStart {
			stalls++
			ev = append(ev, times[i])
		}
		worst = max(worst, g)
	}
	missed := 0.0
	if f.End != nil {
		missed = f.End.Counter("frames_missed")
	}
	fail := worst > c.FeedGapMax
	return trace.Check{ID: id, Name: name, Verdict: verdict(fail), Count: stalls, Value: round1(worst), Threshold: c.FeedGapMax, Unit: "s",
		Detail: fmt.Sprintf("longest gap between feed frames %.1f s; %d gaps over %.0f s; %.0f frames reported missed", worst, stalls, feedStallStart, missed), Evidence: capEvidence(ev)}
}

// ---- GM shortcuts ----

// gmShortcuts is the honest-run flag: a run that used a GM command to teleport, revive or buff, or was set up with one, is not what a
// player could have done.
func gmShortcuts(f *trace.File) trace.Check {
	const id, name = IDGMShortcuts, "GM shortcuts (honest run)"
	n := 0
	kinds := map[string]int{}
	var ev []int64
	for _, e := range f.Events {
		if e.Ev == "gm_shortcut" {
			n++
			kinds[str(e.D, "kind")]++
			ev = append(ev, e.T)
		}
	}
	setup := 0
	if l, ok := f.Header.Options["setup"].([]any); ok {
		setup = len(l)
	} else if l, ok := f.Header.Options["setup"].([]string); ok {
		setup = len(l)
	}
	var ks []string
	for k, v := range kinds {
		ks = append(ks, fmt.Sprintf("%s x%d", k, v))
	}
	sort.Strings(ks)
	d := "no GM command: a player could have done this run"
	if n+setup > 0 {
		d = fmt.Sprintf("%d GM shortcuts (%s), %d setup commands: not a player's run", n, strings.Join(ks, ", "), setup)
	}
	return trace.Check{ID: id, Name: name, Verdict: verdict(n+setup > 0), Count: n + setup, Value: float64(n + setup), Threshold: 0, Unit: "commands", Detail: d, Evidence: capEvidence(ev)}
}

// ---- stuck ----

// stuck counts the times a member that should have been moving stood still for StuckSeconds, with where. The same detector writes the
// stuck events of a live run; here it runs over the frames, so an old trace is scored again.
func stuck(f *trace.File, c Config) trace.Check {
	const id, name = IDStuck, "Stuck members"
	if len(walkWindows(f)) == 0 || len(f.Frames) == 0 {
		return na(id, name, "no walk of the plan with frames in the trace")
	}
	var ev []int64
	var where []string
	n := 0
	for _, e := range DetectStuck(f, c.StuckSeconds) {
		if e.Ev != "stuck" {
			continue
		}
		n++
		ev = append(ev, e.T)
		if len(where) < 5 {
			who := "following"
			if e.Lead {
				who = "leading"
			}
			where = append(where, fmt.Sprintf("%s (%s) at %.0f %.0f %.0f for %.0f s, %.0f yd from the lead", f.Name(e.G), who, e.X, e.Y, e.Z, e.Seconds, e.DistLead))
		}
	}
	d := "nobody stood still while it should move"
	if n > 0 {
		d = fmt.Sprintf("%d stalls: %s", n, strings.Join(where, "; "))
	}
	return trace.Check{ID: id, Name: name, Verdict: verdict(n > 0), Count: n, Value: float64(n), Threshold: 0, Unit: "stalls", Detail: d, Evidence: capEvidence(ev)}
}

// ---- facing ----

// facing finds a bot in combat that stands within melee range (6 yd) of its hostile target for FacingHoldS seconds while turned more
// than 90 degrees from it (the server scorer's rule, without its casting case: the agent does not see casts). The angle is the
// bot's fa; na when the producer did not list the facing capability.
func facing(f *trace.File, c Config) trace.Check {
	const id, name = IDFacing, "Facing the target"
	if !f.Header.HasCap(trace.CapFacing) {
		return na(id, name, "the producer does not record facing")
	}
	type run struct {
		from, last int64
		g          string
	}
	open := map[string]*run{}
	prev := map[string]trace.Bot{}
	samples, holds := 0, 0
	var worst float64
	var worstWho string
	var ev []int64
	counted := map[string]bool{}
	for _, fr := range f.Frames {
		mobs := map[string]trace.Mob{}
		for _, m := range fr.Mobs {
			mobs[m.G] = m
		}
		for _, b := range fr.Bots {
			p, hadPrev := prev[b.G]
			prev[b.G] = b
			t, ok := mobs[b.Target]
			if !ok || b.Facing == nil || !b.Combat || !b.Alive || b.NoPos || t.HPPct <= 0 || math.Hypot(float64(t.X-b.X), float64(t.Y-b.Y)) > meleeYards {
				delete(open, b.G)
				continue
			}
			samples++
			standing := !b.Moving && (!hadPrev || math.Hypot(float64(b.X-p.X), float64(b.Y-p.Y)) < 1)
			if !standing || *b.Facing <= facingDegrees {
				delete(open, b.G)
				continue
			}
			r := open[b.G]
			if r == nil || fr.T-r.last > 3000 {
				r = &run{from: fr.T, g: b.G}
				open[b.G] = r
			}
			r.last = fr.T
			s := float64(fr.T-r.from) / 1000
			if s > c.FacingHoldS && !counted[fmt.Sprint(b.G, r.from)] {
				counted[fmt.Sprint(b.G, r.from)] = true
				holds++
				ev = append(ev, r.from)
			}
			if s > worst {
				worst, worstWho = s, f.Name(b.G)
			}
		}
	}
	if samples == 0 {
		return na(id, name, "no frame had a bot in melee range of its target with a facing")
	}
	d := fmt.Sprintf("%d melee frames; no bot stood turned away from its target for long", samples)
	if worstWho != "" {
		d = fmt.Sprintf("%d melee frames; %s stood more than %d degrees off its target for %.0f s", samples, worstWho, facingDegrees, worst)
	}
	fail := worst > c.FacingHoldS
	return trace.Check{ID: id, Name: name, Verdict: verdict(fail), Count: holds, Value: round1(worst), Threshold: c.FacingHoldS, Unit: "s", Detail: d, Evidence: capEvidence(ev)}
}
