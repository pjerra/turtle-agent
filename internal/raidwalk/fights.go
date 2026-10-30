package raidwalk

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The dashboard's fight detail (GET /api/v1/combat/fight/<key>, TortoiseBots tools/observability internal/combat):
// {"fight": {...the module's COMBAT_FIGHT fight...}, "warnings": [...]}. The walk saves each one raw; FightDetailOf reads
// the parts a run analysis wants into the summary: who died to what (the death recap's killer and last hit), the bot
// that took the most damage, the debuff uptime on the fought unit, the engaged entries and the red rule warnings.
// Unknown keys are ignored and missing ones stay zero, so an older or newer daemon still gives what it has.

// MaxFightDeaths bounds the death recap kept per fight in the summary (the raw detail file keeps them all).
const MaxFightDeaths = 8

// FightDeath is one bot death of a fight: who, when (ms since the fight's start), by what, and the last hit taken.
type FightDeath struct {
	Name    string `json:"name"`
	TMs     uint32 `json:"t_ms"`
	Killer  string `json:"killer,omitempty"`
	LastHit uint32 `json:"last_hit,omitempty"`
	// Burst is the damage of the hits the recap holds (the last few before the death), the spike that killed.
	Burst uint64 `json:"burst,omitempty"`
}

// FightDetail is the summary's digest of one fight detail.
type FightDetail struct {
	Kind        string             `json:"kind,omitempty"`
	Engaged     []string           `json:"engaged,omitempty"` // "Onyxian Warder x2"
	DeathRecap  []FightDeath       `json:"death_recap,omitempty"`
	TopTaken    string             `json:"top_taken,omitempty"` // the bot that took the most damage
	TopTakenAmt uint64             `json:"top_taken_amount,omitempty"`
	Damage      uint64             `json:"damage,omitempty"` // the bots' damage done, summed
	Healing     uint64             `json:"healing,omitempty"`
	DebuffPct   map[string]float64 `json:"debuff_pct,omitempty"` // uptime % on the fought unit per tracked debuff
	Red         []string           `json:"red,omitempty"`        // red rule warnings: "<bot>: <text>"
	Warnings    int                `json:"warnings,omitempty"`   // every rule warning, any level
}

type fightDetailJSON struct {
	Fight struct {
		Kind    string `json:"kind"`
		Engaged []struct {
			N string `json:"n"`
			C uint32 `json:"c"`
		} `json:"engaged"`
		Bots []struct {
			Name  string `json:"name"`
			Dmg   uint64 `json:"dmg"`
			Heal  uint64 `json:"heal"`
			Taken uint64 `json:"taken"`
		} `json:"bots"`
		Debuffs struct {
			SampledS uint64            `json:"sampled_s"`
			UpS      map[string]uint64 `json:"up_s"`
		} `json:"debuffs"`
		Deaths []struct {
			Name   string `json:"name"`
			TMs    uint32 `json:"t_ms"`
			Killer string `json:"killer"`
			Hits   []struct {
				A string `json:"a"`
				D uint32 `json:"d"`
			} `json:"hits"`
		} `json:"deaths"`
	} `json:"fight"`
	Warnings []struct {
		Level string `json:"level"`
		Bot   string `json:"bot"`
		Text  string `json:"text"`
	} `json:"warnings"`
}

// FightDetailOf reads a saved fight detail; ok is false when it is not one (not JSON, or no fight object).
func FightDetailOf(raw []byte) (FightDetail, bool) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil || len(probe["fight"]) == 0 || string(probe["fight"]) == "null" {
		return FightDetail{}, false
	}
	var in fightDetailJSON
	if json.Unmarshal(raw, &in) != nil {
		return FightDetail{}, false
	}
	f := in.Fight
	d := FightDetail{Kind: f.Kind}
	for _, e := range f.Engaged {
		if e.N == "" {
			continue
		}
		if e.C > 1 {
			d.Engaged = append(d.Engaged, fmt.Sprintf("%s x%d", e.N, e.C))
		} else {
			d.Engaged = append(d.Engaged, e.N)
		}
	}
	for _, b := range f.Bots {
		d.Damage += b.Dmg
		d.Healing += b.Heal
		if b.Taken > d.TopTakenAmt {
			d.TopTaken, d.TopTakenAmt = b.Name, b.Taken
		}
	}
	for _, x := range f.Deaths {
		if len(d.DeathRecap) == MaxFightDeaths {
			break
		}
		fd := FightDeath{Name: x.Name, TMs: x.TMs, Killer: x.Killer}
		for _, h := range x.Hits {
			fd.Burst += uint64(h.D)
		}
		if n := len(x.Hits); n > 0 {
			fd.LastHit = x.Hits[n-1].D
			if fd.Killer == "" {
				fd.Killer = x.Hits[n-1].A
			}
		}
		d.DeathRecap = append(d.DeathRecap, fd)
	}
	if f.Debuffs.SampledS > 0 && len(f.Debuffs.UpS) > 0 {
		d.DebuffPct = map[string]float64{}
		for k, up := range f.Debuffs.UpS {
			d.DebuffPct[k] = round1(100 * float64(min(up, f.Debuffs.SampledS)) / float64(f.Debuffs.SampledS))
		}
	}
	d.Warnings = len(in.Warnings)
	for _, w := range in.Warnings {
		if w.Level == "red" {
			d.Red = append(d.Red, strings.TrimSpace(w.Bot+": "+w.Text))
		}
	}
	return d, true
}

// fightLine is one fight of summary.txt's fights block, or "" for a fight with nothing to say (no deaths, no red
// warning): "step 22 onyxia (move, 39.1 s, end): 2 deaths: Irelanthe by Onyxian Warder (last hit 1369), ...".
func fightLine(f FightSummary) string {
	if f.Detail == nil || (len(f.Detail.DeathRecap) == 0 && len(f.Detail.Red) == 0) {
		return ""
	}
	d := f.Detail
	var b strings.Builder
	head := []string{}
	if d.Kind != "" {
		head = append(head, d.Kind)
	}
	head = append(head, fmt.Sprintf("%.1f s", float64(f.DurMS)/1000))
	if f.Outcome != "" {
		head = append(head, f.Outcome)
	}
	fmt.Fprintf(&b, "step %d", f.Step)
	if f.BossName != "" {
		fmt.Fprintf(&b, " %s", f.BossName)
	} else if f.Leg != "" {
		fmt.Fprintf(&b, " %s", f.Leg)
	}
	fmt.Fprintf(&b, " (%s)", strings.Join(head, ", "))
	if n := max(f.Deaths, len(d.DeathRecap)); n > 0 {
		parts := make([]string, 0, len(d.DeathRecap))
		for _, x := range d.DeathRecap {
			p := x.Name
			if x.Killer != "" {
				p += " by " + x.Killer
			}
			if x.LastHit > 0 {
				p += fmt.Sprintf(" (last hit %d)", x.LastHit)
			}
			parts = append(parts, p)
		}
		word := "deaths"
		if n == 1 {
			word = "death"
		}
		fmt.Fprintf(&b, ": %d %s: %s", n, word, strings.Join(parts, ", "))
		if n > len(d.DeathRecap) {
			b.WriteString(", ...")
		}
	}
	if len(d.Red) > 0 {
		red := slices.Clone(d.Red)
		if len(red) > 3 {
			red = append(red[:3], fmt.Sprintf("%d more", len(d.Red)-3))
		}
		fmt.Fprintf(&b, "; red: %s", strings.Join(red, "; "))
	}
	return b.String()
}
