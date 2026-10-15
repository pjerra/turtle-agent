package dashboard

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The run's events: what the owner wants to see as pins on the map and as a short feed under it. They are worked out from the reports
// the dashboard already reads (the run report's deaths, wipes and finished legs, and the orders of the trace), by an EventLog the
// poller keeps per agent, so the list survives a page reload and is the same for every viewer.

// EventCap is the most events kept per agent (the oldest go first).
const EventCap = 80

// Event is one thing that happened in the run. Pos says X and Y (the drawing's plane) are a place for a pin.
type Event struct {
	At   time.Time `json:"at,omitzero"` // zero when the time is not known (the dashboard started mid-run)
	Kind string    `json:"kind"`        // death, wipe, boss, section, open, loot, use
	Text string    `json:"text"`
	Pos  bool      `json:"pos,omitempty"`
	X    float64   `json:"x,omitempty"`
	Y    float64   `json:"y,omitempty"`
}

// evDeath is one entry of the report's death list.
type evDeath struct {
	Name string    `json:"name"`
	Leg  int       `json:"leg"`
	At   time.Time `json:"at"`
}

// evInputs is what the event log reads of one report; BuildCard fills it, it is never sent to the page.
type evInputs struct {
	deaths   []evDeath
	legsDone map[int]string     // finished legs: leg -> its words ("Sentinel room")
	bossLeg  map[int]string     // legs with a boss: leg -> the boss's name
	bossAt   map[int][2]float64 // leg -> the boss's place
	legEnd   map[int][2]float64 // leg -> where it ends
}

// fillEventInputs reads the death list, the finished legs and the plan's bosses out of the report.
func fillEventInputs(c *Card, rep *rawReport, in Inputs) {
	e := evInputs{legsDone: map[int]string{}, bossLeg: map[int]string{}, bossAt: map[int][2]float64{}, legEnd: map[int][2]float64{}}
	for _, raw := range rep.Deaths {
		var d evDeath
		if json.Unmarshal(raw, &d) == nil && d.Name != "" {
			e.deaths = append(e.deaths, d)
		}
	}
	for _, l := range rep.Legs {
		if l.Result == "done" {
			e.legsDone[l.Leg] = legLabel(l.Name, l.Leg)
		}
	}
	if in.Plan != nil {
		names := EntryNames(in.Plan)
		for i, l := range in.Plan.Legs {
			x, y := Project(l.To.X, l.To.Y)
			e.legEnd[i] = [2]float64{x, y}
			if b := l.Boss; b != nil {
				name := names[fmt.Sprint(b.Entry)]
				if name == "" {
					name = fmt.Sprintf("boss %d", b.Entry)
				}
				bx, by := Project(b.Position.X, b.Position.Y)
				e.bossLeg[i], e.bossAt[i] = name, [2]float64{bx, by}
			}
		}
	}
	c.ev = e
}

// EventLog is one agent's event list. It starts again when a new run starts (another run id or plan).
type EventLog struct {
	run     string
	started bool // the first report of this run has been read (what it already holds is history, with no time of its own)
	deaths  int
	wipes   int
	legs    map[int]bool
	orders  map[string]bool
	list    []Event
}

var orderVerbRe = regexp.MustCompile(`(?i)\bcmd (open|loot|use)\b`)

// Observe reads one card and adds the events it holds that are new. now stamps the ones that happen between two polls.
func (l *EventLog) Observe(c *Card, now time.Time) []Event {
	run := c.Label.RunID
	if run == "" {
		run = c.Label.Plan
	}
	if run != l.run || l.legs == nil {
		*l = EventLog{run: run, legs: map[int]bool{}, orders: map[string]bool{}}
	}
	first := !l.started
	l.started = true
	lx, ly, haveLead := c.LeadPosition()
	place := func(e *Event, x, y float64, ok bool) {
		if ok {
			e.Pos, e.X, e.Y = true, round1(x), round1(y)
		}
	}
	for ; l.deaths < len(c.ev.deaths); l.deaths++ {
		d := c.ev.deaths[l.deaths]
		e := Event{At: d.At, Kind: "death", Text: d.Name + " died"}
		for _, b := range c.Bots {
			if b.Name == d.Name && b.Known {
				place(&e, b.X, b.Y, true)
			}
		}
		if !e.Pos {
			place(&e, lx, ly, haveLead)
		}
		l.add(e)
	}
	if c.Counters.Wipes > l.wipes {
		if !first || l.wipes > 0 {
			e := Event{At: now, Kind: "wipe", Text: "The whole party died"}
			place(&e, lx, ly, haveLead)
			l.add(e)
		}
		l.wipes = c.Counters.Wipes
	}
	legs := make([]int, 0, len(c.ev.legsDone))
	for leg := range c.ev.legsDone {
		legs = append(legs, leg)
	}
	sort.Ints(legs)
	for _, leg := range legs {
		label := c.ev.legsDone[leg]
		if l.legs[leg] {
			continue
		}
		l.legs[leg] = true
		at := now
		if first {
			at = time.Time{}
		}
		if boss, ok := c.ev.bossLeg[leg]; ok {
			e := Event{At: at, Kind: "boss", Text: boss + " killed"}
			if p, ok := c.ev.bossAt[leg]; ok {
				place(&e, p[0], p[1], true)
			}
			l.add(e)
		} else {
			e := Event{At: at, Kind: "section", Text: label + " done"}
			l.add(e)
		}
	}
	for _, o := range c.Orders {
		if strings.ToUpper(o.Reply) != "ACK" {
			continue
		}
		verb := strings.ToLower(o.Verb)
		if verb == "" {
			if m := orderVerbRe.FindStringSubmatch(o.Text); m != nil {
				verb = strings.ToLower(m[1])
			}
		}
		var kind, text string
		switch verb {
		case "open":
			kind, text = "open", "Door or chest opened"
		case "loot":
			kind, text = "loot", "Looted"
		case "use":
			kind, text = "use", "Object used"
		default:
			continue
		}
		key := o.At.Format(time.RFC3339Nano) + "|" + o.Text
		if l.orders[key] {
			continue
		}
		l.orders[key] = true
		e := Event{At: o.At, Kind: kind, Text: text}
		place(&e, lx, ly, haveLead)
		l.add(e)
	}
	return append([]Event{}, l.list...)
}

// add keeps the list in time order (an event without a time stays where it came) and within EventCap.
func (l *EventLog) add(e Event) {
	l.list = append(l.list, e)
	sort.SliceStable(l.list, func(i, j int) bool { return l.list[i].At.Before(l.list[j].At) })
	if n := len(l.list); n > EventCap {
		l.list = append([]Event(nil), l.list[n-EventCap:]...)
	}
}
