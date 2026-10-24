package tracecheck

import (
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/trace"
)

// The three rate readings of the spells report: how fast a mob off the tank was answered, whether a crowd-control episode held,
// and whose threat was on top. All three are warnings only and need a minimum sample.

const (
	minTauntEpisodes = 3
	minCCEpisodes    = 3
	minPetMobFrames  = 30
	ccBrokenS        = 5.0 // a joined cc episode shorter than this, ended by the mob still being in view unflagged, was broken
)

// TauntLatency is how long a hostile whose top victim was a party member that is not the tank took to be answered: by the tank's
// taunt, or by the hostile going back to the tank. The frames are samples, so a latency can be about one frame interval off.
type TauntLatency struct {
	Recorded bool    `json:"recorded"` // a tank is known and threat or targets are in the frames
	Episodes int     `json:"episodes"`
	ByTaunt  int     `json:"answered_by_taunt"`
	Returned int     `json:"returned_without_taunt"`
	Open     int     `json:"dropped_open"` // episodes cut off by the mob's death, leaving the frames, the tank's death or the end of the trace
	MedianS  float64 `json:"median_s"`
	WorstS   float64 `json:"worst_s"`
}

// CCHold is the crowd-control episodes joined to a single-target long cc action (polymorph, sap and the like; the short stuns are
// left out) and whether they held. The flags are not tied to the caster: the feed does not say who a polymorph went to.
type CCHold struct {
	Joined  int     `json:"joined_episodes"`
	Broken  int     `json:"broken"`
	HeldPct float64 `json:"held_pct"`
	MedianS float64 `json:"median_length_s"`
}

// PetThreat is the share of mob-frames whose top threat holder is a pet (guid prefix 0xf140).
type PetThreat struct {
	MobFrames int     `json:"mob_frames"`
	PetTop    int     `json:"pet_top"`
	Pct       float64 `json:"pct"`
}

// headerTank is the tank named in the header, else the first party member with the tank role ("" when neither says).
func headerTank(f *trace.File) string {
	if m, ok := f.Member(f.Header.Tank); ok {
		return m.G
	}
	for _, m := range f.Party {
		if m.Role == "tank" {
			return m.G
		}
	}
	return ""
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	if len(v)%2 == 1 {
		return v[len(v)/2]
	}
	return (v[len(v)/2-1] + v[len(v)/2]) / 2
}

// tauntLatency measures the time from "a hostile's top victim became a party member that is not the tank" to "the tank taunted, or
// the hostile went back to the tank". A victim that is not a party member (a pet, an unknown unit) neither opens nor closes an
// episode; a mob that dies, leaves the frames or the tank that dies cuts its episode off (Recorded and Open).
func tauntLatency(f *trace.File, acts []actEv, ivS float64) TauntLatency {
	_ = ivS // the text of the reading carries the caveat; the frames are the resolution
	var r TauntLatency
	tank := headerTank(f)
	if tank == "" {
		return r
	}
	party := map[string]bool{}
	for _, m := range f.Party {
		party[m.G] = true
	}
	for _, fr := range f.Frames {
		for _, b := range fr.Bots {
			party[b.G] = true
		}
	}
	var taunts []actEv
	for _, a := range acts {
		if cat, _ := CategoryOf(a.name); cat == CatTaunt && a.g == tank && a.plausible() {
			taunts = append(taunts, a)
		}
	}
	sort.SliceStable(taunts, func(i, j int) bool { return taunts[i].t < taunts[j].t })
	open := map[string]int64{} // guid -> the frame time the episode began
	var lats []float64
	closeEp := func(g string, st, at int64) {
		delete(open, g)
		tt := int64(-1)
		for _, ta := range taunts {
			// A taunt with a target answers only that mob; without one the feed does not say, so any taunt in the episode counts.
			if ta.target != "" && ta.target != g {
				continue
			}
			if ta.t >= st && ta.t <= at {
				tt = ta.t
				break
			}
		}
		lat := float64(at-st) / 1000
		if tt >= 0 {
			lat = float64(tt-st) / 1000
			r.ByTaunt++
		} else {
			r.Returned++
		}
		r.Episodes++
		lats = append(lats, lat)
	}
	for _, fr := range f.Frames {
		tankAlive := false
		for _, b := range fr.Bots {
			if b.G == tank {
				tankAlive = b.Alive
				break
			}
		}
		if !tankAlive {
			r.Open += len(open)
			open = map[string]int64{}
			continue
		}
		inFrame := map[string]trace.Mob{}
		for _, m := range fr.Mobs {
			inFrame[m.G] = m
			if m.HPPct <= 0 || !m.Combat {
				continue
			}
			victim := m.Target
			if len(m.Threat) > 0 {
				victim = m.Threat[0].G
			}
			if victim == "" || !party[victim] {
				continue
			}
			r.Recorded = true
			st, isOpen := open[m.G]
			switch {
			case victim != tank && !isOpen:
				open[m.G] = fr.T
			case victim == tank && isOpen:
				closeEp(m.G, st, fr.T)
			}
		}
		for g := range open {
			if m, ok := inFrame[g]; !ok || m.HPPct <= 0 || !m.Combat {
				r.Open++
				delete(open, g)
			}
		}
	}
	r.Open += len(open)
	if len(lats) > 0 {
		sort.Float64s(lats)
		r.MedianS = round1(median(lats))
		r.WorstS = round1(lats[len(lats)-1])
	}
	return r
}

// ccHold follows the cc flag of every mob: an episode is a run of consecutive frames with the mob present and flagged. Only the
// episodes that start within the cc window of a single-target long cc action are joined (Joined; ccs holds those, the short stuns
// and disorients are left out by longCC); a joined episode under ccBrokenS that ends with the mob still in view, alive and unflagged
// was broken, and every other one held.
func ccHold(f *trace.File, ccs []actEv, ivS float64) CCHold {
	var r CCHold
	type episode struct {
		start, last int64
	}
	open := map[string]*episode{}
	var lens []float64
	end := func(g string, e *episode, unflagged bool) {
		delete(open, g)
		joined := false
		for _, a := range ccs {
			if e.start >= a.t && e.start <= a.t+int64(ccWindowS*1000) {
				joined = true
				break
			}
		}
		if !joined {
			return
		}
		length := float64(e.last-e.start)/1000 + ivS
		r.Joined++
		if unflagged && length < ccBrokenS {
			r.Broken++
		}
		lens = append(lens, length)
	}
	for _, fr := range f.Frames {
		present := map[string]bool{}
		for _, m := range fr.Mobs {
			present[m.G] = true
			e := open[m.G]
			switch {
			case e == nil:
				if m.CC && m.HPPct > 0 {
					open[m.G] = &episode{start: fr.T, last: fr.T}
				}
			case m.HPPct == 0:
				end(m.G, e, false) // dead: the mob was killed, which is not the hold breaking
			case !m.CC:
				end(m.G, e, true)
			default:
				e.last = fr.T
			}
		}
		for g, e := range open {
			if !present[g] {
				end(g, e, false) // gone from the frames: its end was not seen
			}
		}
	}
	for g, e := range open {
		end(g, e, false) // the trace ended first
	}
	if r.Joined > 0 {
		r.HeldPct = round1(float64(r.Joined-r.Broken) * 100 / float64(r.Joined))
	}
	if len(lens) > 0 {
		sort.Float64s(lens)
		r.MedianS = round1(median(lens))
	}
	return r
}

// petThreat counts the mob-frames whose top threat holder is a pet. The trace writes guids lower case (0x%x), so the prefix test
// lower-cases the guid too.
func petThreat(f *trace.File) PetThreat {
	var r PetThreat
	for _, fr := range f.Frames {
		for _, m := range fr.Mobs {
			if m.HPPct <= 0 || !m.Combat || len(m.Threat) == 0 {
				continue
			}
			r.MobFrames++
			if strings.HasPrefix(strings.ToLower(m.Threat[0].G), "0xf140") {
				r.PetTop++
			}
		}
	}
	if r.MobFrames > 0 {
		r.Pct = round1(float64(r.PetTop) * 100 / float64(r.MobFrames))
	}
	return r
}
