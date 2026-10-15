package dungeon

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

// The pull-test scenario's result (dungeon_run pulltest, pulltest.go): one pull group pulled `tries` times, each try scored, a compact table
// to read and JSON to compare two builds with (`turtle-agent pulltest --compare a.json b.json`). The same pull, the same party, 3+ tries
// each is the A/B rule of the auto-improve loop; this is what makes a try cheap.

// PulltestVersion is the version of the result JSON. The leader fields (mode, brain, character, tries[].leader, summary.leader) were added
// to version 1: a result without them is a commander run's.
const PulltestVersion = 1

// ModeLeader is PulltestReport.Mode of a leader-mode result.
const ModeLeader = "leader"

// The values of PulltestTry.Outcome.
const (
	TryKilled = "killed"  // the pack was pulled and every mob of it is dead
	TryNoPack = "no_pack" // nothing of the pack was found: the reset did not bring it back; the try is not scored
	TryWipe   = "wipe"    // the whole party died
	TryFailed = "failed"  // the pull ended in an error (fight timeout, unreachable, ...)
)

// PulltestTry is one try's score.
type PulltestTry struct {
	Try       int    `json:"try"`
	PullGroup int    `json:"pull_group"` // the pack of this try: the scenario's, or an equivalent one
	Reset     string `json:"reset"`      // how the pack came back: "first spawn", "equivalent pack", "respawn"
	Outcome   string `json:"outcome"`
	Error     string `json:"error,omitempty"`
	Kills     int    `json:"kills"`
	// TimeToKillS runs from the pull order to the last kill of the fight; Seconds is the whole try (pull, fight, rest).
	TimeToKillS float64  `json:"time_to_kill_s"`
	Seconds     float64  `json:"seconds"`
	Deaths      int      `json:"deaths"`
	DeathNames  []string `json:"death_names,omitempty"`
	// ThreatOverrides are the mobs the party was sent at without the tank holding them. OffTankS is the mob-seconds the engaged mobs spent
	// on a bot that was not the tank (a mob on the tank counts nothing), OffTankMobs the mobs that were off the tank at all, MaxOffTankS the
	// longest any one was.
	ThreatOverrides int     `json:"threat_overrides"`
	OffTankS        float64 `json:"off_tank_s"`
	OffTankMobs     int     `json:"off_tank_mobs"`
	MaxOffTankS     float64 `json:"max_off_tank_s"`
	// Adds are the engaged mobs that were not of the pack (a neighbouring pack that joined, a wanderer); AddEntries their entries.
	Adds       int      `json:"adds"`
	AddEntries []uint32 `json:"add_entries,omitempty"`
	// HealerMinManaPct is the lowest mana percent of the healer (else of any mana user) from the pull to the end of the rest; -1: not measured.
	HealerMinManaPct int `json:"healer_min_mana_pct"`
	// RestS is the seconds the try spent in rest gates (the one before the pull and the one after the kill).
	RestS float64 `json:"rest_s"`
	// Leader is the agent's own character's score in the try (leader mode only; pulltest_leader.go).
	Leader *LeaderTry `json:"leader,omitempty"`
}

// LeaderTry is the score of the agent's own character in one try: what it did to the pack and how well it played. Rates are over the try's
// time to kill. Damage and healing are the character's own, from the combat log (melee, spell damage and heals with the character as the
// source); the rest is the brain's counters (brain.Stats). Power is the character's primary power (mana, energy, rage in tenths).
type LeaderTry struct {
	DamageDone         int64          `json:"damage_done"`
	HealingDone        int64          `json:"healing_done"`
	DPS                float64        `json:"dps"`
	HPS                float64        `json:"hps"`
	PowerSpent         uint64         `json:"power_spent"`
	DamagePerPower     float64        `json:"damage_per_power"`
	HealingPerPower    float64        `json:"healing_per_power"`
	Actions            int            `json:"actions"` // actions that ran (casts and swings sent)
	ActionCounts       map[string]int `json:"action_counts,omitempty"`
	Errors             int            `json:"errors"`       // actions whose game call failed
	CastsFailed        int            `json:"casts_failed"` // casts the server refused
	CastsFailedReasons map[string]int `json:"casts_failed_by_reason,omitempty"`
	CombatS            float64        `json:"combat_s"`
	IdleCombatS        float64        `json:"idle_combat_s"` // in combat, nothing running: waiting for a cooldown, power or the tank
	IdlePct            float64        `json:"idle_pct"`
	MobOnMeS           float64        `json:"mob_on_me_s"` // seconds with a mob on the character (the threat it took off the tank)
	MobsOnMe           int            `json:"mobs_on_me"`
	LeaderDeaths       int            `json:"leader_deaths"`
	MinPowerPct        int            `json:"min_power_pct"` // -1: not measured
}

// CharacterStats is the brain's counters of one window (brain.Stats), as the driver reads them through StatsBrain.
type CharacterStats struct {
	Actions     map[string]int
	Errors      int
	CastFailed  map[string]int
	CombatS     float64
	IdleCombatS float64
	MobOnMeS    float64
	MobsOnMe    int
	PowerSpent  uint64
	MinPowerPct int
}

// LeaderSummary is the totals of the leader's tries; the means are over the killed ones that carry leader numbers.
type LeaderSummary struct {
	Tries               int            `json:"tries"`
	DPSMean             float64        `json:"dps_mean"`
	HPSMean             float64        `json:"hps_mean"`
	DamagePerPowerMean  float64        `json:"damage_per_power_mean"`
	HealingPerPowerMean float64        `json:"healing_per_power_mean"`
	ActionsMean         float64        `json:"actions_mean"`
	CastsFailed         int            `json:"casts_failed"`
	CastsFailedPerTry   float64        `json:"casts_failed_per_try"`
	CastsFailedByReason map[string]int `json:"casts_failed_by_reason,omitempty"`
	IdlePctMean         float64        `json:"idle_pct_mean"`
	MobsOnMe            int            `json:"mobs_on_me"`
	MobOnMeMeanS        float64        `json:"mob_on_me_mean_s"`
	LeaderDeaths        int            `json:"leader_deaths"`
	MinPowerPct         int            `json:"min_power_pct"` // the lowest over the tries; -1: not measured
}

// PulltestSummary is the totals of a result, the numbers two results are compared by. The sums are over the scored tries (every outcome but
// no_pack), the means over the killed ones.
type PulltestSummary struct {
	Tries            int     `json:"tries"` // scored
	Killed           int     `json:"killed"`
	TimeToKillMeanS  float64 `json:"time_to_kill_mean_s"`
	TimeToKillMaxS   float64 `json:"time_to_kill_max_s"`
	Deaths           int     `json:"deaths"`
	ThreatOverrides  int     `json:"threat_overrides"`
	OffTankMeanS     float64 `json:"off_tank_mean_s"`
	Adds             int     `json:"adds"`
	HealerMinManaPct int     `json:"healer_min_mana_pct"` // the lowest over the tries; -1: not measured
	RestMeanS        float64 `json:"rest_mean_s"`
	// Leader is the agent's own character's totals (leader mode).
	Leader *LeaderSummary `json:"leader,omitempty"`
}

// PulltestReport is the scenario's result (Report.Pulltest and the <run id>.pulltest.json file).
type PulltestReport struct {
	Version    int      `json:"version"`
	Plan       string   `json:"plan"`
	Leg        int      `json:"leg"`
	LegName    string   `json:"leg_name,omitempty"`
	PullGroup  int      `json:"pull_group"`
	Entries    []uint32 `json:"entries"`
	TriesAsked int      `json:"tries_asked"`
	Reset      string   `json:"reset"`
	Healer     string   `json:"healer"` // the bot the mana is read from, or "all mana users"
	Tank       string   `json:"tank,omitempty"`
	RunID      string   `json:"run_id,omitempty"`
	// Mode is "leader" when the agent's own character played the try (the run mode); empty: a commander run, where the bots did.
	Mode string `json:"mode,omitempty"`
	// Brain is what played the character: the preset, and the rotation with its file's digest ("frost_mage+rot:frost_mage@32c7144315b5"):
	// the arm of the A/B. Character is its name.
	Brain     string        `json:"brain,omitempty"`
	Character string        `json:"character,omitempty"`
	Tries     []PulltestTry `json:"tries"`
	// Stopped says why there are fewer tries than asked ("one try per spawn: ..."); empty when all were run.
	Stopped string `json:"stopped,omitempty"`
	// GMCommands are the `.respawn` lines the resets sent (also run_gm_assists and setup_shortcuts in the report).
	GMCommands []string        `json:"gm_commands"`
	Summary    PulltestSummary `json:"summary"`
	// File is where the JSON was written (next to the run's trace); empty when the run is not traced.
	File string `json:"file,omitempty"`
}

func (r *PulltestReport) clone() *PulltestReport {
	if r == nil {
		return nil
	}
	c := *r
	c.Entries = slices.Clone(r.Entries)
	c.GMCommands = slices.Clone(r.GMCommands)
	c.Tries = slices.Clone(r.Tries)
	for i := range c.Tries {
		c.Tries[i].DeathNames = slices.Clone(c.Tries[i].DeathNames)
		c.Tries[i].AddEntries = slices.Clone(c.Tries[i].AddEntries)
		if l := c.Tries[i].Leader; l != nil {
			cp := *l
			cp.ActionCounts, cp.CastsFailedReasons = maps.Clone(l.ActionCounts), maps.Clone(l.CastsFailedReasons)
			c.Tries[i].Leader = &cp
		}
	}
	return &c
}

// Summarize fills Summary from the tries.
func (r *PulltestReport) Summarize() {
	s := PulltestSummary{HealerMinManaPct: -1}
	var ttk, off, rest float64
	for _, t := range r.Tries {
		if t.Outcome == TryNoPack {
			continue
		}
		s.Tries++
		s.Deaths += t.Deaths
		s.ThreatOverrides += t.ThreatOverrides
		s.Adds += t.Adds
		if t.HealerMinManaPct >= 0 && (s.HealerMinManaPct < 0 || t.HealerMinManaPct < s.HealerMinManaPct) {
			s.HealerMinManaPct = t.HealerMinManaPct
		}
		if t.Outcome != TryKilled {
			continue
		}
		s.Killed++
		ttk += t.TimeToKillS
		off += t.OffTankS
		rest += t.RestS
		s.TimeToKillMaxS = math.Max(s.TimeToKillMaxS, t.TimeToKillS)
	}
	if s.Killed > 0 {
		n := float64(s.Killed)
		s.TimeToKillMeanS, s.OffTankMeanS, s.RestMeanS = round1(ttk/n), round1(off/n), round1(rest/n)
	}
	s.TimeToKillMaxS = round1(s.TimeToKillMaxS)
	s.Leader = summarizeLeader(r.Tries)
	r.Summary = s
}

// summarizeLeader totals the tries that carry the leader's score (nil without any).
func summarizeLeader(tries []PulltestTry) *LeaderSummary {
	s := LeaderSummary{MinPowerPct: -1, CastsFailedByReason: map[string]int{}}
	var dps, hps, dpp, hpp, acts, idle, onMe float64
	var killed int
	for _, t := range tries {
		l := t.Leader
		if l == nil || t.Outcome == TryNoPack {
			continue
		}
		s.Tries++
		s.CastsFailed += l.CastsFailed
		s.MobsOnMe += l.MobsOnMe
		s.LeaderDeaths += l.LeaderDeaths
		for k, v := range l.CastsFailedReasons {
			s.CastsFailedByReason[k] += v
		}
		if l.MinPowerPct >= 0 && (s.MinPowerPct < 0 || l.MinPowerPct < s.MinPowerPct) {
			s.MinPowerPct = l.MinPowerPct
		}
		if t.Outcome != TryKilled {
			continue
		}
		killed++
		dps, hps, dpp, hpp, acts, idle, onMe = dps+l.DPS, hps+l.HPS, dpp+l.DamagePerPower, hpp+l.HealingPerPower, acts+float64(l.Actions), idle+l.IdlePct, onMe+l.MobOnMeS
	}
	if s.Tries == 0 {
		return nil
	}
	s.CastsFailedPerTry = round1(float64(s.CastsFailed) / float64(s.Tries))
	if killed > 0 {
		n := float64(killed)
		s.DPSMean, s.HPSMean, s.DamagePerPowerMean, s.HealingPerPowerMean = round1(dps/n), round1(hps/n), round2(dpp/n), round2(hpp/n)
		s.ActionsMean, s.IdlePctMean, s.MobOnMeMeanS = round1(acts/n), round1(idle/n), round1(onMe/n)
	}
	if len(s.CastsFailedByReason) == 0 {
		s.CastsFailedByReason = nil
	}
	return &s
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Scenario names the pull the result is of: two results compare only when it is the same.
func (r *PulltestReport) Scenario() string {
	return fmt.Sprintf("%s leg %d pull group %d %v", r.Plan, r.Leg, r.PullGroup, r.Entries)
}

// LoadPulltest reads a result: the file the run wrote, a dungeon_status answer (result.report.pulltest) or a report (pulltest).
func LoadPulltest(data []byte) (*PulltestReport, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	node := raw
	if pt := findKey(raw, "pulltest", 0); pt != nil {
		node = pt
	}
	b, err := json.Marshal(node)
	if err != nil {
		return nil, err
	}
	var r PulltestReport
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("not a pulltest result: %w", err)
	}
	if r.Version == 0 || r.Tries == nil {
		return nil, fmt.Errorf("not a pulltest result (no version and tries)")
	}
	if r.Version != PulltestVersion {
		return nil, fmt.Errorf("pulltest result version %d: this build reads version %d", r.Version, PulltestVersion)
	}
	return &r, nil
}

// findKey looks for key in nested objects, down to depth 4.
func findKey(v any, key string, depth int) any {
	m, ok := v.(map[string]any)
	if !ok || depth > 4 {
		return nil
	}
	if x, ok := m[key]; ok && x != nil {
		return x
	}
	for _, k := range []string{"result", "report"} {
		if x := findKey(m[k], key, depth+1); x != nil {
			return x
		}
	}
	return nil
}

// FormatPulltest is the compact table of a result.
func FormatPulltest(r *PulltestReport) string {
	var b strings.Builder
	name := r.LegName
	if name != "" {
		name = " (" + name + ")"
	}
	fmt.Fprintf(&b, "pulltest %s leg %d%s pull group %d %v  tries %d/%d  reset %s  healer %s  tank %s", r.Plan, r.Leg, name, r.PullGroup, r.Entries, len(r.Tries), r.TriesAsked, r.Reset, r.Healer, dashIfEmpty(r.Tank))
	if r.RunID != "" {
		fmt.Fprintf(&b, "  run %s", r.RunID)
	}
	if r.Mode == ModeLeader {
		fmt.Fprintf(&b, "  leader %s  brain %s", dashIfEmpty(r.Character), dashIfEmpty(r.Brain))
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "%-4s %-4s %-16s %-8s %7s %6s %7s %14s %5s %7s %6s %7s\n", "try", "pack", "reset", "result", "ttk_s", "deaths", "thr_ovr", "off_tank_s", "adds", "heal_%", "rest_s", "total_s")
	for _, t := range r.Tries {
		heal := "-"
		if t.HealerMinManaPct >= 0 {
			heal = fmt.Sprint(t.HealerMinManaPct)
		}
		fmt.Fprintf(&b, "%-4d %-4d %-16s %-8s %7.1f %6d %7d %9.1f (%d) %5d %7s %6.1f %7.1f\n", t.Try, t.PullGroup, t.Reset, t.Outcome, t.TimeToKillS, t.Deaths, t.ThreatOverrides, t.OffTankS, t.OffTankMobs, t.Adds, heal, t.RestS, t.Seconds)
		if l := t.Leader; l != nil {
			fmt.Fprintf(&b, "     me: dps %.1f hps %.1f dmg/power %.2f heal/power %.2f  actions %d failed %d%s  idle %.0f%% of %.1f s  mobs on me %d (%.1f s)  deaths %d\n",
				l.DPS, l.HPS, l.DamagePerPower, l.HealingPerPower, l.Actions, l.CastsFailed, reasonsText(l.CastsFailedReasons), l.IdlePct, l.CombatS, l.MobsOnMe, l.MobOnMeS, l.LeaderDeaths)
		}
		if t.Error != "" {
			fmt.Fprintf(&b, "     error: %s\n", t.Error)
		}
	}
	s := r.Summary
	fmt.Fprintf(&b, "sum  killed %d/%d  ttk mean %.1f max %.1f  deaths %d  threat overrides %d  off-tank mean %.1f s  adds %d  healer min %s  rest mean %.1f s\n",
		s.Killed, s.Tries, s.TimeToKillMeanS, s.TimeToKillMaxS, s.Deaths, s.ThreatOverrides, s.OffTankMeanS, s.Adds, pctOrDash(s.HealerMinManaPct), s.RestMeanS)
	if l := s.Leader; l != nil {
		fmt.Fprintf(&b, "me   tries %d  dps mean %.1f  hps mean %.1f  dmg/power %.2f  heal/power %.2f  actions mean %.1f  casts failed %d (%.1f per try)%s  idle mean %.1f%%  mobs on me %d  mob-on-me mean %.1f s  deaths %d  min power %s\n",
			l.Tries, l.DPSMean, l.HPSMean, l.DamagePerPowerMean, l.HealingPerPowerMean, l.ActionsMean, l.CastsFailed, l.CastsFailedPerTry, reasonsText(l.CastsFailedByReason), l.IdlePctMean, l.MobsOnMe, l.MobOnMeMeanS, l.LeaderDeaths, pctOrDash(l.MinPowerPct))
	}
	gm := "none"
	if len(r.GMCommands) > 0 {
		gm = fmt.Sprintf("%d (%s)", len(r.GMCommands), strings.Join(slices.Compact(slices.Clone(r.GMCommands)), ", "))
	}
	fmt.Fprintf(&b, "GM resets: %s", gm)
	if r.Stopped != "" {
		fmt.Fprintf(&b, "  stopped: %s", r.Stopped)
	}
	b.WriteByte('\n')
	return b.String()
}

// reasonsText is " (out_of_range 3, no_power 1)" for the refusal reasons, most frequent first; "" without any.
func reasonsText(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := slices.Collect(maps.Keys(m))
	slices.SortFunc(keys, func(a, b string) int {
		if m[a] != m[b] {
			return m[b] - m[a]
		}
		return strings.Compare(a, b)
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pctOrDash(p int) string {
	if p < 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", p)
}

// ComparePulltest prints b against a: each summary number with both values and the change, marked better or worse. It warns when the two
// are not the same pull or have fewer than three scored tries.
func ComparePulltest(a, b *PulltestReport) string {
	var out strings.Builder
	fmt.Fprintf(&out, "A: %s  (%d tries, run %s)\nB: %s  (%d tries, run %s)\n", a.Scenario(), a.Summary.Tries, dashIfEmpty(a.RunID), b.Scenario(), b.Summary.Tries, dashIfEmpty(b.RunID))
	if a.Mode == ModeLeader || b.Mode == ModeLeader {
		fmt.Fprintf(&out, "A plays: %s (%s)\nB plays: %s (%s)\n", dashIfEmpty(a.Brain), dashIfEmpty(a.Character), dashIfEmpty(b.Brain), dashIfEmpty(b.Character))
		if a.Mode != b.Mode {
			out.WriteString("WARNING: one result is a leader run and the other a commander run: the numbers of the character exist on one side only.\n")
		} else if a.Brain == b.Brain {
			out.WriteString("NOTE: both sides name the same brain (and rotation digest): this compares two runs of one arm, which is the noise.\n")
		}
	}
	if a.Scenario() != b.Scenario() {
		out.WriteString("WARNING: not the same pull: the numbers do not compare.\n")
	}
	if a.Summary.Tries < 3 || b.Summary.Tries < 3 {
		out.WriteString("WARNING: fewer than 3 scored tries on a side: the difference may be noise.\n")
	}
	if a.Summary.Killed != a.Summary.Tries || b.Summary.Killed != b.Summary.Tries {
		out.WriteString("NOTE: a try that was not a kill (wipe, error) is in the sums, not in the means.\n")
	}
	fmt.Fprintf(&out, "%-24s %10s %10s %10s  %s\n", "", "A", "B", "B - A", "")
	row := func(name string, av, bv float64, lowerIsBetter bool, unit string, known bool) {
		if !known {
			fmt.Fprintf(&out, "%-24s %10s %10s %10s\n", name, "-", "-", "-")
			return
		}
		d := bv - av
		verdict := ""
		switch {
		case math.Abs(d) < 0.05:
			verdict = "same"
		case (d < 0) == lowerIsBetter:
			verdict = "better"
		default:
			verdict = "worse"
		}
		fmt.Fprintf(&out, "%-24s %10.1f %10.1f %+10.1f  %s%s\n", name, av, bv, d, verdict, unit)
	}
	sa, sb := a.Summary, b.Summary
	row("killed", float64(sa.Killed), float64(sb.Killed), false, "", true)
	row("time to kill, mean s", sa.TimeToKillMeanS, sb.TimeToKillMeanS, true, "", sa.Killed > 0 && sb.Killed > 0)
	row("time to kill, max s", sa.TimeToKillMaxS, sb.TimeToKillMaxS, true, "", sa.Killed > 0 && sb.Killed > 0)
	row("deaths", float64(sa.Deaths), float64(sb.Deaths), true, "", true)
	row("threat overrides", float64(sa.ThreatOverrides), float64(sb.ThreatOverrides), true, "", true)
	row("off-tank, mean s", sa.OffTankMeanS, sb.OffTankMeanS, true, "", sa.Killed > 0 && sb.Killed > 0)
	row("adds", float64(sa.Adds), float64(sb.Adds), true, "", true)
	row("healer min mana %", float64(sa.HealerMinManaPct), float64(sb.HealerMinManaPct), false, "", sa.HealerMinManaPct >= 0 && sb.HealerMinManaPct >= 0)
	row("rest, mean s", sa.RestMeanS, sb.RestMeanS, true, "", sa.Killed > 0 && sb.Killed > 0)
	if la, lb := sa.Leader, sb.Leader; la != nil && lb != nil {
		out.WriteString("the character:\n")
		both := sa.Killed > 0 && sb.Killed > 0
		row("  damage per s, mean", la.DPSMean, lb.DPSMean, false, "", both)
		row("  healing per s, mean", la.HPSMean, lb.HPSMean, false, "", both)
		row("  damage per power", la.DamagePerPowerMean, lb.DamagePerPowerMean, false, "", both && (la.DamagePerPowerMean > 0 || lb.DamagePerPowerMean > 0))
		row("  healing per power", la.HealingPerPowerMean, lb.HealingPerPowerMean, false, "", both && (la.HealingPerPowerMean > 0 || lb.HealingPerPowerMean > 0))
		row("  actions per try", la.ActionsMean, lb.ActionsMean, false, "", both)
		row("  casts failed per try", la.CastsFailedPerTry, lb.CastsFailedPerTry, true, "", true)
		reasons := slices.Collect(maps.Keys(la.CastsFailedByReason))
		for k := range lb.CastsFailedByReason {
			if !slices.Contains(reasons, k) {
				reasons = append(reasons, k)
			}
		}
		slices.Sort(reasons)
		for _, k := range reasons {
			row("    "+k+" per try", round1(float64(la.CastsFailedByReason[k])/float64(la.Tries)), round1(float64(lb.CastsFailedByReason[k])/float64(lb.Tries)), true, "", true)
		}
		row("  idle in combat, mean %", la.IdlePctMean, lb.IdlePctMean, true, "", both)
		row("  mobs on me", float64(la.MobsOnMe), float64(lb.MobsOnMe), true, "", true)
		row("  mob on me, mean s", la.MobOnMeMeanS, lb.MobOnMeMeanS, true, "", both)
		row("  character deaths", float64(la.LeaderDeaths), float64(lb.LeaderDeaths), true, "", true)
		row("  lowest power %", float64(la.MinPowerPct), float64(lb.MinPowerPct), false, "", la.MinPowerPct >= 0 && lb.MinPowerPct >= 0)
	} else if la != nil || lb != nil {
		out.WriteString("the character: numbers on one side only.\n")
	}
	return out.String()
}
