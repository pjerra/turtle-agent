package dashboard

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The stall thresholds of the card: yellow at two minutes without progress, red at five (the owner's stale-run rule).
const (
	StallYellow = 2 * time.Minute
	StallRed    = 5 * time.Minute
)

// Inputs are the raw answers of one poll of one agent. Raw answers that are nil or failed are simply absent from the card.
type Inputs struct {
	Agent     Agent
	Now       time.Time
	Status    json.RawMessage
	Dungeon   json.RawMessage
	Tune      json.RawMessage
	View      json.RawMessage
	Trace     *TraceSnapshot
	Links     []Link // the run's files under --tests-dir
	Folder    string // the capture folder the run belongs to
	Plan      *dungeon.Plan
	PlanErr   string
	StatusErr string // why the status call failed, when it did
	WasOnline bool
}

// Card is everything the page shows of one commander. It is built from an allow-list of fields (never from a raw reply), so no
// account, password or token can get into it; Scrub runs over it once more before it is served.
type Card struct {
	ID        string `json:"id"`
	RPC       string `json:"rpc"`
	Online    bool   `json:"online"`
	Error     string `json:"error,omitempty"`
	Character string `json:"character,omitempty"`
	Level     int    `json:"level,omitempty"`
	Session   string `json:"session,omitempty"`
	Map       uint32 `json:"map"`
	Pos       *Pos   `json:"pos,omitempty"`

	Label Label     `json:"label"`
	Run   RunInfo   `json:"run"`
	Stall StallInfo `json:"stall"`

	Counters Counters `json:"counters"`
	Orders   []Order  `json:"orders"`
	Changes  []Change `json:"changes"`
	Tunables []Tune   `json:"tunables,omitempty"`

	LastError   string `json:"last_error,omitempty"`
	LastRefusal string `json:"last_refusal,omitempty"`
	SHA         string `json:"sha,omitempty"`
	Links       []Link `json:"links"`

	Feed      Feed         `json:"feed"`
	Bots      []BotView    `json:"bots"`
	Mobs      []MobView    `json:"mobs"`
	Commander *Dot         `json:"commander,omitempty"` // the agent's own character, when it stands on the plan's map
	TrailT    []int64      `json:"trail_t,omitempty"`   // the unix second of each point of Trail (the lead was last seen there)
	Trail     [][2]float64 `json:"trail,omitempty"`     // where the lead bot has been this run ([x, y] in the drawing's plane), oldest first
	Events    []Event      `json:"events"`              // what happened this run (deaths, wipes, boss kills, doors and chests), oldest first; the poller fills it

	ev        evInputs // what the event log reads of the report; never sent
	tracePath string   // the trace file of the run, for the live digest; never sent
}

// Link is a file of the run the page links to, served from --tests-dir.
type Link struct {
	Name string `json:"name"`
	Path string `json:"path"` // relative to --tests-dir
}

// Pos is a position in game coordinates.
type Pos struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// Dot is a position in drawing coordinates.
type Dot struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Name string  `json:"name"`
}

// Label names the run: the plan and the capture folder.
type Label struct {
	Plan   string `json:"plan,omitempty"`
	Folder string `json:"folder,omitempty"`
	Map    uint32 `json:"map,omitempty"`
	RunID  string `json:"run_id,omitempty"`
}

// RunInfo is the state of the run in words and numbers.
type RunInfo struct {
	State    string  `json:"state"` // idle, running, done, failed, stopped
	Phase    string  `json:"phase"` // prepare, enter, walk, pull, boss, rest, step, wait, ... or the final state
	Text     string  `json:"text,omitempty"`
	Leg      int     `json:"leg"` // -1 when no leg has started
	LegName  string  `json:"leg_name,omitempty"`
	LegCount int     `json:"leg_count,omitempty"`
	Group    int     `json:"group,omitempty"`  // the pull group in hand, 1-based; 0 when none
	Groups   int     `json:"groups,omitempty"` // the groups of the leg
	Minutes  float64 `json:"minutes"`
	Waiting  string  `json:"waiting,omitempty"`
	LastStep string  `json:"last_progress,omitempty"`
	Mode     string  `json:"mode,omitempty"`
	PrepareS float64 `json:"prepare_s,omitempty"`
	// Leader is the TortoiseBots run leader's own status in an observed run (mode observe); nil in any other.
	Leader *LeaderInfo `json:"leader,omitempty"`
}

// LeaderInfo is the TBM:RUN status of an observed run: what the run leader on the server is doing, as the agent last read it.
type LeaderInfo struct {
	State string `json:"state"` // running, paused, done or none
	Leg   string `json:"leg,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Phase string `json:"phase,omitempty"`
	Note  string `json:"note,omitempty"`
	Step  int    `json:"step"`
	Steps int    `json:"steps"`
	// Text is the status in plain words: "Run leader: Sentinel · pull 7/41 · pulling".
	Text string `json:"text"`
}

// StallInfo is the stall timer: the seconds since the run last made progress and how loud to show it.
type StallInfo struct {
	Seconds float64 `json:"seconds"`
	Level   string  `json:"level"` // none, yellow (2 min), red (5 min)
}

// Counters are the run's totals.
type Counters struct {
	Deaths           int     `json:"deaths"`
	LeaderDeaths     int     `json:"leader_deaths"`
	Wipes            int     `json:"wipes"`
	PartyDowns       int     `json:"party_downs"`
	ThreatOverrides  int     `json:"threat_overrides"`
	RestPct          float64 `json:"rest_pct"`
	RestGates        int     `json:"rest_gates"`
	Gathers          int     `json:"gathers"`
	WalkStops        int     `json:"walk_stops"`
	GMCommands       int     `json:"gm_commands"`
	GMAssists        int     `json:"gm_assists"`
	AgentWalks       int     `json:"agent_walks"`
	OrdersSent       int     `json:"orders_sent"`
	OrdersRefused    int     `json:"orders_refused"`
	OrdersUnanswered int     `json:"orders_unanswered"`
	Pulls            int     `json:"pulls"`
	Unreachable      int     `json:"unreachable"`
}

// Change is an on-the-spot change of the run: a tunable changed or the plan reloaded.
type Change struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // tune_change or plan_reload
	Text string    `json:"text"`
}

// Tune is a tunable whose value is not the base value.
type Tune struct {
	Key    string  `json:"key"`
	Value  float64 `json:"value"`
	Base   float64 `json:"base"`
	Source string  `json:"source"`
}

// Feed says how fresh the bots' view is.
type Feed struct {
	Have   bool   `json:"have"`
	Seq    uint64 `json:"seq,omitempty"`
	AgeMS  int64  `json:"age_ms,omitempty"`
	Stale  bool   `json:"stale,omitempty"`
	Frames uint64 `json:"frames,omitempty"`
}

// BotView is one bot of the party.
type BotView struct {
	GUID       string   `json:"guid,omitempty"`        // the feed's guid of the bot (what a mob's or a bot's target_guid names)
	TargetGUID string   `json:"target_guid,omitempty"` // the guid of the unit the bot has targeted, when it has one
	Name       string   `json:"name"`
	Class      string   `json:"class,omitempty"`
	Role       string   `json:"role,omitempty"`
	Lead       bool     `json:"lead,omitempty"`
	HPPct      int      `json:"hp_pct"`
	PowerPct   int      `json:"power_pct"`
	PowerType  string   `json:"power_type,omitempty"`
	Alive      bool     `json:"alive"`
	State      string   `json:"state,omitempty"`
	Action     string   `json:"action,omitempty"`
	Cast       string   `json:"cast,omitempty"` // the spell it casts or channels now (its name, else "spell <id>"), when the feed says
	Result     string   `json:"result,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Target     string   `json:"target,omitempty"`
	DistTank   *float64 `json:"dist_tank,omitempty"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Known      bool     `json:"known"` // x and y are a position (the drawing plane)
	InMap      bool     `json:"in_map"`
}

// MobView is one creature of the feed.
type MobView struct {
	TargetGUID string  `json:"target_guid,omitempty"` // the guid of the unit it targets (a bot of the party, as a rule)
	GUID       string  `json:"guid"`
	Entry      uint32  `json:"entry"`
	Kind       string  `json:"kind"` // hostile, neutral, friendly or critter
	HPPct      int     `json:"hp_pct"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Engaged    bool    `json:"engaged,omitempty"`
	Target     string  `json:"target,omitempty"`
	CC         bool    `json:"cc,omitempty"`
	Cast       string  `json:"cast,omitempty"` // the spell it casts or channels now (its name, else "spell <id>"), when the feed says
	Evade      bool    `json:"evade,omitempty"`
	Stealth    bool    `json:"stealth,omitempty"`
	Leg        int     `json:"leg"`   // the plan leg whose group it belongs to, -1 for none
	Group      int     `json:"group"` // the group's index in the leg, -1 for none
	Boss       bool    `json:"boss,omitempty"`
	Cleared    bool    `json:"cleared,omitempty"` // its group is behind the run: shown dimmed
}

// raw shapes, read leniently: a field the agent adds later is ignored.

type rawStatus struct {
	State     string  `json:"state"`
	Character string  `json:"character"`
	Level     int     `json:"level"`
	Map       uint32  `json:"map"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
	Z         float32 `json:"z"`
	Error     string  `json:"error"`
}

type rawDungeon struct {
	Running        bool       `json:"running"`
	State          string     `json:"state"`
	LastProgressAt *time.Time `json:"last_progress_at"`
	StallLevel     string     `json:"stall_level"`
	Report         *rawReport `json:"report"`
}

type rawReport struct {
	Plan         string            `json:"plan"`
	Map          uint32            `json:"map"`
	State        string            `json:"state"`
	Started      time.Time         `json:"started"`
	Ended        *time.Time        `json:"ended"`
	Seconds      float64           `json:"seconds"`
	Current      string            `json:"current"`
	Error        string            `json:"error"`
	Wipes        int               `json:"wipes"`
	PartyDowns   int               `json:"party_downs"`
	LeaderDeaths int               `json:"leader_deaths"`
	Deaths       []json.RawMessage `json:"deaths"`
	Legs         []struct {
		Leg    int    `json:"leg"`
		Name   string `json:"name"`
		Result string `json:"result"`
		Pulls  int    `json:"pulls"`
	} `json:"legs"`
	RestShare float64           `json:"rest_share"`
	RestGates []json.RawMessage `json:"rest_gates"`
	RunID     string            `json:"run_id"`
	TraceFile string            `json:"trace_file"`
	SetupS    float64           `json:"setup_s"`
	Watchdog  *struct {
		LastProgressAt time.Time `json:"last_progress_at"`
		LastProgress   string    `json:"last_progress"`
		Waiting        string    `json:"waiting"`
	} `json:"watchdog"`
	Remote *struct {
		Mode             string   `json:"mode"`
		LeadBot          string   `json:"lead_bot"`
		Bots             []string `json:"bots"`
		Tank             string   `json:"tank"`
		GMCommands       int      `json:"gm_commands"`
		RunGMAssists     int      `json:"run_gm_assists"`
		AgentWalks       int      `json:"agent_walks"`
		ThreatOverrides  int      `json:"threat_overrides"`
		Gathers          int      `json:"gathers"`
		WalkStops        int      `json:"walk_stops"`
		OrdersSent       int      `json:"orders_sent"`
		OrdersRefused    int      `json:"orders_refused"`
		OrdersUnanswered int      `json:"orders_unanswered"`
		Unreachable      int      `json:"unreachable"`
		TuneChanges      []struct {
			Key      string    `json:"key"`
			Old      float64   `json:"old"`
			New      float64   `json:"new"`
			Source   string    `json:"source"`
			At       time.Time `json:"at"`
			Leg      int       `json:"leg"`
			Boundary string    `json:"boundary"`
		} `json:"tune_changes"`
		Observe *struct {
			Started bool   `json:"started"`
			State   string `json:"state"`
			Step    int    `json:"step"`
			Steps   int    `json:"steps"`
			Leg     string `json:"leg"`
			Kind    string `json:"kind"`
			Phase   string `json:"phase"`
			Note    string `json:"note"`
			Lines   int    `json:"lines"`
		} `json:"observe"`
		PlanReloads []struct {
			OK       bool              `json:"ok"`
			At       time.Time         `json:"at"`
			Leg      int               `json:"leg"`
			Boundary string            `json:"boundary"`
			File     string            `json:"file"`
			Error    string            `json:"error"`
			Ops      []json.RawMessage `json:"ops"`
		} `json:"plan_reloads"`
	} `json:"remote"`
	Ready *struct {
		Bots []struct {
			Name  string `json:"name"`
			Class string `json:"class"`
			Role  string `json:"role"`
		} `json:"bots"`
	} `json:"ready"`
}

type rawTune struct {
	Tunables []struct {
		Key    string  `json:"key"`
		Value  float64 `json:"value"`
		Base   float64 `json:"base"`
		Source string  `json:"source"`
	} `json:"tunables"`
}

type rawView struct {
	Have  bool   `json:"have"`
	N     uint64 `json:"n"`
	AgeMS int64  `json:"age_ms"`
	Frame *struct {
		Seq   uint64 `json:"seq"`
		Lead  string `json:"lead"`
		Party []struct {
			GUID     string  `json:"guid"`
			Name     string  `json:"name"`
			HP       float64 `json:"hp"`
			MaxHP    float64 `json:"max_hp"`
			Power    float64 `json:"power"`
			MaxPower float64 `json:"max_power"`
			PType    int     `json:"power_type"`
			X        float32 `json:"x"`
			Y        float32 `json:"y"`
			Map      uint32  `json:"map"`
			Target   string  `json:"target"`
			Dead     bool    `json:"dead"`
			State    string  `json:"state"`
			Action   string  `json:"action"`
			Spell    uint32  `json:"spell"`
			Result   string  `json:"result"`
			Reason   string  `json:"reason"`
		} `json:"party"`
		Hostiles []struct {
			GUID     string  `json:"guid"`
			Entry    uint32  `json:"entry"`
			X        float32 `json:"x"`
			Y        float32 `json:"y"`
			HPPct    int     `json:"hp_pct"`
			Target   string  `json:"target"`
			Spell    uint32  `json:"spell"`
			Flags    string  `json:"flags"`
			Reaction string  `json:"reaction"`
			Kind     string  `json:"kind"`
		} `json:"hostiles"`
	} `json:"frame"`
	Stats struct {
		Frames uint64 `json:"frames"`
	} `json:"stats"`
}

var legRe = regexp.MustCompile(`^leg (\d+) \(([^)]*)\): (.*)$`)
var groupRe = regexp.MustCompile(`pull group (\d+) of (\d+)`)

// FeedStale is the age past which the bots' view is shown as stale.
const FeedStale = 6 * time.Second

var powerNames = [...]string{"mana", "rage", "focus", "energy"}

// BuildCard turns the answers of one poll into the card. It is pure: the same inputs give the same card.
func BuildCard(in Inputs) Card {
	c := Card{ID: in.Agent.ID, RPC: in.Agent.Addr, Orders: []Order{}, Changes: []Change{}, Bots: []BotView{}, Mobs: []MobView{}, Events: []Event{}}
	c.Run = RunInfo{State: "idle", Phase: "idle", Leg: -1}
	c.Stall.Level = "none"
	var st rawStatus
	if len(in.Status) > 0 && json.Unmarshal(in.Status, &st) == nil {
		c.Online = true
		c.Character, c.Level, c.Session, c.Map = st.Character, st.Level, st.State, st.Map
		if st.State == "in_world" {
			c.Pos = &Pos{X: st.X, Y: st.Y, Z: st.Z}
		}
		c.LastError = st.Error
	} else {
		c.Error = in.StatusErr
		if c.Error == "" {
			c.Error = "no answer"
		}
		return c
	}
	var ds rawDungeon
	var rep *rawReport
	if len(in.Dungeon) > 0 && json.Unmarshal(in.Dungeon, &ds) == nil {
		rep = ds.Report
		c.Run.State = ds.State
		if c.Run.State == "" {
			c.Run.State = "idle"
		}
	}
	if rep != nil {
		fillRun(&c, rep, ds, in)
		fillEventInputs(&c, rep, in)
	}
	if in.Trace != nil {
		c.SHA = in.Trace.SHA
		c.Orders = append(c.Orders, in.Trace.Orders...)
		for i := len(c.Orders) - 1; i >= 0; i-- {
			if c.Orders[i].Reply == "ERR" {
				c.LastRefusal = strings.TrimSpace(c.Orders[i].Code + " " + c.Orders[i].Message)
				break
			}
		}
	}
	c.Links = append([]Link{}, in.Links...)
	c.Label.Folder = in.Folder
	if in.PlanErr != "" && c.LastError == "" {
		c.LastError = "plan file: " + in.PlanErr
	}
	var tune rawTune
	if len(in.Tune) > 0 && json.Unmarshal(in.Tune, &tune) == nil {
		for _, t := range tune.Tunables {
			if t.Source != "" && t.Source != "base" && len(c.Tunables) < 16 {
				c.Tunables = append(c.Tunables, Tune{Key: t.Key, Value: t.Value, Base: t.Base, Source: t.Source})
			}
		}
	}
	fillStall(&c, ds, rep, in.Now)
	fillView(&c, rep, in)
	return c
}

func fillRun(c *Card, rep *rawReport, ds rawDungeon, in Inputs) {
	c.Label.Plan, c.Label.Map, c.Label.RunID = rep.Plan, rep.Map, rep.RunID
	if rep.State != "" {
		c.Run.State = rep.State
	}
	c.Run.Text = rep.Current
	c.Run.Minutes = round1(rep.Seconds / 60)
	if c.Run.State == "running" && !rep.Started.IsZero() && rep.Ended == nil {
		if s := in.Now.Sub(rep.Started).Seconds(); s > rep.Seconds {
			c.Run.Minutes = round1(s / 60)
		}
	}
	c.Run.PrepareS = rep.SetupS
	if in.Plan != nil {
		c.Run.LegCount = len(in.Plan.Legs)
	}
	if m := legRe.FindStringSubmatch(rep.Current); m != nil {
		c.Run.Leg, _ = strconv.Atoi(m[1])
		c.Run.LegName = m[2]
		if g := groupRe.FindStringSubmatch(m[3]); g != nil {
			c.Run.Group, _ = strconv.Atoi(g[1])
			c.Run.Groups, _ = strconv.Atoi(g[2])
		}
	} else if n := len(rep.Legs); n > 0 {
		c.Run.Leg, c.Run.LegName = rep.Legs[n-1].Leg, rep.Legs[n-1].Name
	}
	if wd := rep.Watchdog; wd != nil {
		c.Run.Waiting, c.Run.LastStep = wd.Waiting, wd.LastProgress
	}
	c.Run.Phase = phaseOf(c.Run.State, rep.Current, c.Run.Waiting, c.Run.Group)
	for _, l := range rep.Legs {
		c.Counters.Pulls += l.Pulls
	}
	c.Counters.Deaths = len(rep.Deaths)
	c.Counters.LeaderDeaths, c.Counters.Wipes, c.Counters.PartyDowns = rep.LeaderDeaths, rep.Wipes, rep.PartyDowns
	c.Counters.RestPct, c.Counters.RestGates = rep.RestShare, len(rep.RestGates)
	if r := rep.Remote; r != nil {
		c.Run.Mode = r.Mode
		c.Counters.ThreatOverrides, c.Counters.Gathers, c.Counters.WalkStops = r.ThreatOverrides, r.Gathers, r.WalkStops
		c.Counters.GMCommands, c.Counters.GMAssists, c.Counters.AgentWalks = r.GMCommands, r.RunGMAssists, r.AgentWalks
		c.Counters.OrdersSent, c.Counters.OrdersRefused, c.Counters.OrdersUnanswered = r.OrdersSent, r.OrdersRefused, r.OrdersUnanswered
		c.Counters.Unreachable = r.Unreachable
		if o := r.Observe; o != nil && o.Lines > 0 {
			c.Run.Leader = leaderInfo(tbm.RunStatus{State: o.State, Step: o.Step, Steps: o.Steps, Leg: o.Leg, Kind: o.Kind, Phase: o.Phase, Note: o.Note})
			if c.Run.State == "running" {
				c.Run.Phase = "leader"
			}
		}
		for _, t := range r.TuneChanges {
			c.Changes = append(c.Changes, Change{At: t.At, Kind: "tune_change",
				Text: fmt.Sprintf("%s %s -> %s (%s, before %s of leg %d)", t.Key, num(t.Old), num(t.New), t.Source, strings.ReplaceAll(t.Boundary, "_", " "), t.Leg)})
		}
		for _, p := range r.PlanReloads {
			text := fmt.Sprintf("plan reload: %d change(s) before %s of leg %d", len(p.Ops), strings.ReplaceAll(p.Boundary, "_", " "), p.Leg)
			if !p.OK {
				text = "plan reload refused: " + firstLine(p.Error)
			}
			if p.File != "" {
				text += " [" + filepath.Base(p.File) + "]"
			}
			c.Changes = append(c.Changes, Change{At: p.At, Kind: "plan_reload", Text: text})
		}
		sort.SliceStable(c.Changes, func(i, j int) bool { return c.Changes[i].At.Before(c.Changes[j].At) })
		if n := len(c.Changes); n > 10 {
			c.Changes = c.Changes[n-10:]
		}
	}
	if rep.Error != "" {
		c.LastError = rep.Error
	}
}

// leaderInfo is the card's view of a run leader status, with its plain-words line.
func leaderInfo(st tbm.RunStatus) *LeaderInfo {
	li := &LeaderInfo{State: st.State, Leg: st.Leg, Kind: st.Kind, Phase: st.Phase, Note: st.Note, Step: st.Step, Steps: st.Steps}
	text := st.Text()
	if text != "" {
		text = strings.ToUpper(text[:1]) + text[1:]
	}
	switch st.State {
	case tbm.RunPaused:
		li.Text = "Run leader paused: " + text
	case tbm.RunDone:
		if st.Steps > 0 && st.Step >= st.Steps && !strings.HasPrefix(st.Note, "stopped") {
			li.Text = fmt.Sprintf("Run leader: finished all %d steps", st.Steps)
		} else {
			li.Text = "Run leader stopped: " + text
			if st.Note != "" {
				li.Text += " · " + st.Note
			}
		}
	case tbm.RunNone:
		li.Text = "Run leader: no run"
	default:
		li.Text = "Run leader: " + text
	}
	return li
}

// phaseOf names what the run is doing: the final state of an ended run, else the first word of what the watchdog says it
// waits on (prepare, enter, walk, pull, rest, ...), else the label of the action in progress.
func phaseOf(state, current, waiting string, group int) string {
	if state != "running" {
		return state
	}
	if w := strings.Fields(waiting); len(w) > 0 && w[0] != "run" {
		return strings.ToLower(w[0])
	}
	switch {
	case strings.HasPrefix(current, "prepare"):
		return "prepare"
	case strings.HasPrefix(current, "enter"):
		return "enter"
	case strings.Contains(current, "kill boss"):
		return "boss"
	case group > 0:
		return "pull"
	case strings.Contains(current, "walk to"), strings.Contains(current, "via to"), strings.Contains(current, "direct to"):
		return "walk"
	case strings.Contains(current, "step "):
		return "step"
	case current == "":
		return "starting"
	}
	return "run"
}

// fillStall sets the stall timer. A run that is going counts the time since its last progress; an ended one has none.
func fillStall(c *Card, ds rawDungeon, rep *rawReport, now time.Time) {
	if c.Run.State != "running" {
		return
	}
	last := time.Time{}
	if ds.LastProgressAt != nil {
		last = *ds.LastProgressAt
	} else if rep != nil && rep.Watchdog != nil {
		last = rep.Watchdog.LastProgressAt
	}
	if last.IsZero() {
		return
	}
	s := now.Sub(last)
	if s < 0 {
		s = 0
	}
	c.Stall.Seconds = round1(s.Seconds())
	switch {
	case s >= StallRed:
		c.Stall.Level = "red"
	case s >= StallYellow:
		c.Stall.Level = "yellow"
	}
}

func fillView(c *Card, rep *rawReport, in Inputs) {
	var v rawView
	haveView := len(in.View) > 0 && json.Unmarshal(in.View, &v) == nil && v.Have && v.Frame != nil
	class, role := map[string]string{}, map[string]string{}
	var lead, tank string
	var names []string
	if rep != nil {
		if rep.Ready != nil {
			for _, b := range rep.Ready.Bots {
				class[b.Name], role[b.Name] = b.Class, b.Role
			}
		}
		if r := rep.Remote; r != nil {
			lead, tank = r.LeadBot, r.Tank
			names = append(names, r.LeadBot)
			names = append(names, r.Bots...)
		}
	}
	if in.Trace != nil {
		for _, p := range in.Trace.Party {
			if role[p.Name] == "" {
				role[p.Name] = p.Role
			}
		}
	}
	if tank != "" && role[tank] == "" {
		role[tank] = "tank"
	}
	c.Feed.Frames = v.Stats.Frames
	byGUID := map[uint64]string{}
	var dot *Dot
	planMap := c.Label.Map
	if in.Plan != nil {
		planMap = in.Plan.Map
	}
	if planMap != 0 && c.Pos != nil && c.Map == planMap {
		x, y := Project(c.Pos.X, c.Pos.Y)
		dot = &Dot{X: x, Y: y, Name: c.Character}
	}
	c.Commander = dot
	if !haveView {
		for _, n := range names {
			if n != "" {
				c.Bots = append(c.Bots, BotView{Name: n, Class: class[n], Role: role[n], Lead: n == lead, Alive: true})
			}
		}
		return
	}
	f := v.Frame
	c.Feed = Feed{Have: true, Seq: f.Seq, AgeMS: v.AgeMS, Stale: time.Duration(v.AgeMS)*time.Millisecond > FeedStale, Frames: v.Stats.Frames}
	type pos struct{ x, y float32 }
	where := map[string]pos{}
	for _, p := range f.Party {
		byGUID[guidNum(p.GUID)] = p.Name
		where[p.Name] = pos{p.X, p.Y}
		if tank == "" && role[p.Name] == "tank" {
			tank = p.Name
		}
	}
	for _, p := range f.Party {
		b := BotView{GUID: p.GUID, Name: p.Name, Class: class[p.Name], Role: role[p.Name], Lead: guidNum(p.GUID) == guidNum(f.Lead) && f.Lead != "",
			HPPct: pct(p.HP, p.MaxHP), PowerPct: pct(p.Power, p.MaxPower), Alive: !p.Dead, State: p.State, Action: p.Action, Cast: spellText(p.Spell), Result: p.Result, Reason: p.Reason}
		if p.PType >= 0 && p.PType < len(powerNames) {
			b.PowerType = powerNames[p.PType]
		}
		b.InMap = planMap == 0 || p.Map == planMap
		b.X, b.Y = Project(p.X, p.Y)
		b.Known = b.InMap && !(p.X == 0 && p.Y == 0)
		if tp, ok := where[tank]; ok && tank != "" && b.Known {
			d := round1(math.Hypot(float64(p.X-tp.x), float64(p.Y-tp.y)))
			b.DistTank = &d
		}
		c.Bots = append(c.Bots, b)
	}
	sort.SliceStable(c.Bots, func(i, j int) bool { return c.Bots[i].Lead && !c.Bots[j].Lead })
	for _, h := range f.Hostiles {
		byGUID[guidNum(h.GUID)] = fmt.Sprintf("entry %d", h.Entry)
	}
	tname := func(g string) string {
		if g == "" || guidNum(g) == 0 {
			return ""
		}
		return byGUID[guidNum(g)]
	}
	for i := range c.Bots {
		for _, p := range f.Party {
			if p.Name == c.Bots[i].Name {
				c.Bots[i].Target = tname(p.Target)
				if guidNum(p.Target) != 0 {
					c.Bots[i].TargetGUID = p.Target
				}
			}
		}
	}
	for _, h := range f.Hostiles {
		m := MobView{GUID: h.GUID, TargetGUID: targetGUID(h.Target), Entry: h.Entry, HPPct: h.HPPct, Leg: -1, Group: -1, Target: tname(h.Target), Cast: spellText(h.Spell), Engaged: h.Target != "" && guidNum(h.Target) != 0,
			CC: strings.Contains(h.Flags, "c"), Evade: strings.Contains(h.Flags, "e"), Stealth: strings.Contains(h.Flags, "s")}
		m.X, m.Y = Project(h.X, h.Y)
		switch {
		case h.Reaction == "f":
			m.Kind = "friendly"
		case strings.Contains(h.Kind, "k"):
			m.Kind = "critter"
		case h.Reaction == "n" || strings.ContainsAny(h.Kind, "vgu"):
			m.Kind = "neutral"
		default:
			m.Kind = "hostile"
		}
		if in.Plan != nil {
			m.Leg, m.Group, m.Boss = GroupOf(in.Plan, c.Run.Leg, h.Entry, h.X, h.Y)
			m.Cleared = clearedGroup(m.Leg, m.Group, c.Run.Leg, c.Run.Group)
		}
		c.Mobs = append(c.Mobs, m)
	}
}

// clearedGroup says whether a pack lies behind the run: in an earlier leg, or an earlier group of the current one.
// spellText is a spell's name for the page, "spell <id>" for one the name table lacks, and "" for none.
func spellText(id uint32) string {
	if id == 0 {
		return ""
	}
	if n := spells.Name(id); n != "" {
		return n
	}
	return "spell " + strconv.FormatUint(uint64(id), 10)
}

func clearedGroup(mobLeg, mobGroup, leg, group int) bool {
	if mobLeg < 0 || leg < 0 {
		return false
	}
	if mobLeg != leg {
		return mobLeg < leg
	}
	return mobGroup >= 0 && group > 0 && mobGroup < group-1
}

// targetGUID is the guid string of a target, or "" for none (an empty or all-zero guid).
func targetGUID(g string) string {
	if g == "" || guidNum(g) == 0 {
		return ""
	}
	return g
}

func guidNum(s string) uint64 {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"), 16, 64)
	if err != nil {
		return 0
	}
	return v
}

func pct(v, full float64) int {
	if full <= 0 {
		return 100
	}
	return min(max(int(math.Round(v/full*100)), 0), 100)
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// LeadPosition is where the party's lead stands in the drawing's plane: the lead bot, else the first bot with a position, else
// the commander.
func (c Card) LeadPosition() (x, y float64, ok bool) {
	for _, b := range c.Bots {
		if b.Lead && b.Known {
			return b.X, b.Y, true
		}
	}
	for _, b := range c.Bots {
		if b.Known {
			return b.X, b.Y, true
		}
	}
	if c.Commander != nil {
		return c.Commander.X, c.Commander.Y, true
	}
	return 0, 0, false
}
