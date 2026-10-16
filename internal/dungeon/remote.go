package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// Remote-leader mode (issue #83). The agent leads a bot group it is not in, through the TortoiseBots remote commander
// (`.bot commander bind`, GUID-targeted `.bot cmd` orders, the TBM:VIEW perception feed). The RemoteDriver walks the same
// Plan with the same Stepper as the Driver, but everything it knows of the dungeon comes from the feed (where the lead bot
// is, who is hurt, which hostiles the bots see and who holds their threat), and everything it does is a `.bot cmd` order.
// The agent's own character is not part of it: it may be anywhere, even outside the instance.

// Runner is what the dungeon runner needs of a run, in group mode (Driver) and remote mode (RemoteDriver).
type Runner interface {
	Run(ctx context.Context, fromLeg int) error
	Report() Report
	Status() (Report, bool)
}

// RemoteGame is what the RemoteDriver does to the game: send commander commands, read the feed. The agent implements it over
// the session (cmd/turtle-agent/commander_methods.go); the tests use a fake with a fake clock. A RemoteGame may also implement
// PathPlanner.
type RemoteGame interface {
	// Command sends ".bot <command>" ("commander bind Tanky", "cmd move 1 2 3") and returns its ACK or ERR reply (Kind
	// timeout when none came in time). The error is for a command that could not be sent.
	Command(ctx context.Context, command string) (tbm.Reply, error)
	// Frame is the newest frame of the feed and the number of frames the feed has published; ok is false when none came yet.
	Frame() (fr tbm.ViewFrame, n uint64, ok bool)
	// ResetView forgets the frame held: a new binding counts its seq from 1.
	ResetView()
	// BindingEnds are the automatic ends of the binding the module announced.
	BindingEnds() []tbm.BindingEnd
	// ViewStats are the feed's counters.
	ViewStats() tbm.ViewStats
	// Emit appends an event of kind to the agent's event log.
	Emit(kind, text string, data map[string]any)
}

// PathPlanner is the optional part of a RemoteGame that plans a navmesh path between two points of a map, wherever the agent
// stands: the lead bot's walks over more than one leg (`move` takes a point within 150 yd) and the approach to a pack follow it.
type PathPlanner interface {
	PlanPath(ctx context.Context, mapID uint32, from, to Point) ([]Point, error)
}

// FloorSnapper is the optional part of a RemoteGame that puts a point on the navmesh floor under it (false: there is none near): the look
// point of a pack (LookPoint) is snapped with it, as the plan check snaps its own.
type FloorSnapper interface {
	SnapToFloor(ctx context.Context, mapID uint32, p Point) (Point, bool)
}

// FloorLister is the optional part of a RemoteGame that lists the walkable floors of the navmesh under (x, y) near height z, highest
// first (false: no planner or no tile there). The walk puts each step on the floor nearest its height with it.
type FloorLister interface {
	FloorsAt(ctx context.Context, mapID uint32, x, y, z float32) ([]float32, bool)
}

// CCRule asks for a mob entry to be crowd-controlled in a fight: `.bot cmd cc <mark> <guid> [bot]` before the pull.
type CCRule struct {
	Entry uint32 `json:"entry"`
	Mark  string `json:"mark"`          // skull, cross, square, moon, triangle, diamond, circle or star
	Bot   string `json:"bot,omitempty"` // the bot that takes the mark; a capable one when empty
}

// RemoteOptions are the run's choices and limits in remote mode. A zero limit takes its default (DefaultRemoteOptions).
type RemoteOptions struct {
	Lead string   // the lead bot: it walks, the rest follow it
	Bots []string // the other bots to bind with it; empty: the lead bot's party as it is
	// Tank names the bot that holds threat. Empty: the bot that holds the first pulled mob's threat is taken for it.
	Tank string
	// Healer names the bot the healer mana target applies to (the feed carries no class). Empty: every caster is held to the healer target.
	Healer string
	CC     []CCRule
	// GravesBatch is a fixed number of chests per batch for the loot_object steps that have a batch id, and GravesRamp a size that grows after
	// each batch without a wipe (remote_batch.go). Both off (0, nil): the plan's own batches. GravesRamp wins when both are given.
	GravesBatch int
	GravesRamp  *GravesRamp
	// CCBot names the bot that takes the big-pack CC mark (remote_cc.go: a pack of BigPack or more gets one member Polymorphed before the
	// pull). A hired party sets it to its mage; empty: no big-pack CC.
	CCBot string
	// Team is the party's team for the team gate (Boss.Team, PullGroup.Team): "alliance" or "horde". Empty: the game's (the agent's own
	// race, as the feed carries none), which is right when the bots share the agent's account faction.
	Team string
	// Preflight is the pre-run kit check: "warn" (the default; `.bot maintenance` and `.bot inv` for each bot, what is missing is reported),
	// "strict" (a warning fails the run) or "off".
	Preflight string
	// OnEnd is what the run does with the bots at its end: "stay" (the default: `cmd stay` before the unbind, so that they do not follow the
	// agent), "logout" (stay, unbind, then `.bot logout` for each) or "none".
	OnEnd string
	// KeepOnFail keeps the binding of a FAILED run (the bots are stood still, no `commander unbind`), so that the next run re-enters the
	// same instance instead of making a new one. Off by default: the group never stays bound after a run (driver rule 4); a finished
	// or stopped run always unbinds, and dungeon_stop with no run going unbinds a kept binding.
	KeepOnFail bool
	// HireMemory is what an earlier run of this agent read of the hires of a kept binding (name, lower case, to class, level, talent
	// points): a bound hire does not answer `who`, so the prepare step takes its class and level from here for the bots that are still
	// in the standing binding.
	HireMemory map[string]tbm.Who
	// ClassMemory is the class (lower case) of every hire the agent has read in any earlier run, kept whether or not that run kept its
	// binding (a class never changes): the cc bot of a run that names its party is the mage found here (remote_cc.go).
	ClassMemory map[string]string
	// Carrier names the bot that does the object steps (loot_object, a boss's loot_item, use_item, use_object): it walks to the chest or
	// corpse, loots, opens and uses items on `cmd open|loot|use` orders (remote_objects.go). Empty: the first living bot that is neither
	// the lead bot, the tank nor the healer.
	Carrier string
	// LootRestore is the loot method put back after a free-for-all loot ("group" by default; the module cannot be asked for the old one).
	LootRestore string
	// ObjectFallback is what an object step does when the carrier's order fails twice (remote_fallback.go): "agent" (the default) lets the
	// agent's own character open the object and hand the item over with a trade, "off" fails the step as the carrier's refusal says.
	ObjectFallback string
	// Enter says how the run gets a party that is not inside the plan's map in (remote_enter.go): "auto" (the default: `.tele name` when
	// the account is a developer-level GM and the plan names a tele, else `cmd enter` at the portal when the plan has an entrance_trigger,
	// else walk the lead bot into the portal), "gm", "cmd", "walk", or "off"
	// (a party outside fails the run). IgnoreEntryLimit lets the run try a 6th new instance within the hour.
	Enter            string
	IgnoreEntryLimit bool
	// Prepare is the optional prepare block (prepare.go): log the bots in, a fresh instance, GM setup lines. It runs before the bind.
	Prepare PrepareSpec
	// FromPullGroup starts the run inside from_leg, at that pull group (start.go): the commander gets the party to the group's gather
	// spot, the legs before the leg and the leg's earlier pull groups are reported skipped. StartHere keeps the old from_leg: no
	// move, the run starts where the lead bot stands. StartWait is the ceiling of each wait of the start phase (0: 30 s).
	FromPullGroup *int
	StartHere     bool
	StartWait     time.Duration
	// HaveItems are item entries the caller says the party already carries: a start past the leg that makes one does not refuse for it.
	HaveItems []uint32
	// GiveItems is the give_items run option: GM `.additem` lines on named bots at the prepare step (prepare_give.go).
	GiveItems []GiveItem
	// Pulltest runs one pull group as a scenario instead of the plan (pulltest.go); it implies FromPullGroup at its group.
	Pulltest *PulltestSpec
	// Observe makes the run a watch of the TortoiseBots run leader instead of a driven one (observe.go): after the same prepare, bind and
	// enter, `.bot run start <plan>` goes out and the agent sends no order, it reads the TBM:RUN status. Nil: a driven (commander) run.
	Observe *ObserveSpec
	// RezWait is how long a dead bot waits for a living caster's resurrection, out of combat, before it is released with `cmd revive`.
	// 0: 30 s; negative: no wait.
	RezWait time.Duration

	EnterVerify time.Duration // after the entry every bot must be on the plan's map within this
	EnterGrace  time.Duration // the followers' time to come in after the lead bot on a walk, before they are summoned
	SummonGap   time.Duration // between two `cmd summon` orders
	// BindSettle is how long a bind is given before the bound party is read back and compared with the named one (remote_party.go).
	BindSettle time.Duration
	// TankBusyWait is how long a pull the module refused as tank-busy (the only tank is in combat) waits for the tank to be free or the
	// mob to be engaged, before the pull is sent again.
	TankBusyWait time.Duration

	FeedWait   time.Duration // the first frame after the bind
	FeedStale  time.Duration // without a new frame this long the feed is "stalled": the loops wait instead of deciding
	FeedLost   time.Duration // without a new frame this long the run fails
	MoveLeg    float32       // yards: the longest single `move` (the server takes 150)
	MoveArrive float32       // yards from the point at which the lead bot has arrived
	MoveStall  time.Duration // a lead bot that gained no ground in this long gets the move again
	// CarefulHop is the longest single `move` of a walk inside the dungeon (remote_gather.go): before each hop the party gathers and a
	// hostile near the hop is pulled. GatherWait bounds the wait for the party to gather (negative: no wait).
	CarefulHop float32
	GatherWait time.Duration
	// LookAhead is how near (yards) the lead bot must be to its current waypoint for the next `cmd move` to go out, so that it never
	// comes to a stop between waypoints; LookAheadETA does the same by time (distance / the bot's speed). Negative LookAhead turns the
	// look-ahead off (each waypoint is waited for). MoveGap is the least time between two `cmd move` orders.
	LookAhead     float32
	LookAheadETA  time.Duration
	MoveGap       time.Duration
	MoveRetries   int           // stalls and refusals of one walk before it fails
	LeadWait      time.Duration // how long an order waits out lead-unavailable (the lead bot between maps, dead, on a taxi)
	ThreatWait    time.Duration // longest wait for the tank to hold a mob's threat before the party is sent in anyway
	ThreatWarnPct int           // a bot that is not the tank at this share of the top threat is a warning
	// OpenerDelay is how long the tank must have held a mob (top of its T record, the mob hitting it) before the party is first sent
	// at it: the tank's lead needs a moment to build. Negative: no delay. AddCooldown is the least time between two tank orders at
	// the same loose add (a mob hitting a bot that is not the tank).
	OpenerDelay time.Duration
	AddCooldown time.Duration
	UnbindWait  time.Duration // the unbind at the end of the run
	// PlanPath is the plan file the run was loaded from, TunePath the tunables file (default: <plan>.tune.json beside it; absent is fine). At
	// each leg start and pull-group start the driver reads both again when they changed (remote_tune.go, README "Live tuning"). NoReload
	// turns the two reloads off; `tune_set` still works. Empty PlanPath: no plan reload and no default tunables file.
	PlanPath, TunePath string
	NoReload           bool
	// EndFightWait is how long the end of a run (done, failed or stopped) keeps the binding and the fight logic going while the party is
	// still in combat, before the stay and the unbind (remote_end.go). 0: DefaultEndFightWait; negative: unbind at once.
	EndFightWait time.Duration
}

// Values of RemoteOptions.ObjectFallback.
const (
	ObjectFallbackAgent = "agent"
	ObjectFallbackOff   = "off"
)

// Values of RemoteOptions.OnEnd.
const (
	OnEndStay   = "stay"
	OnEndLogout = "logout"
	OnEndNone   = "none"
)

// DefaultRemoteOptions are the limits of a live run.
func DefaultRemoteOptions() RemoteOptions {
	return RemoteOptions{FeedWait: 20 * time.Second, FeedStale: 6 * time.Second, FeedLost: 45 * time.Second,
		MoveLeg: 120, CarefulHop: 25, GatherWait: 12 * time.Second, MoveArrive: 5, LookAhead: 10, LookAheadETA: 2 * time.Second, MoveGap: time.Second, MoveStall: 10 * time.Second, MoveRetries: 4, LeadWait: 30 * time.Second,
		ThreatWait: 6 * time.Second, ThreatWarnPct: 90, OpenerDelay: 2 * time.Second, AddCooldown: 8 * time.Second, UnbindWait: 8 * time.Second, RezWait: 30 * time.Second,
		EnterVerify: 30 * time.Second, EnterGrace: 8 * time.Second, SummonGap: 6 * time.Second, BindSettle: DefaultBindSettle, TankBusyWait: 10 * time.Second}
}

func (o RemoteOptions) withDefaults() RemoteOptions {
	d := DefaultRemoteOptions()
	o.Team, o.Preflight, o.OnEnd, o.Enter = strings.ToLower(o.Team), strings.ToLower(o.Preflight), strings.ToLower(o.OnEnd), strings.ToLower(o.Enter)
	if o.ObjectFallback = strings.ToLower(strings.TrimSpace(o.ObjectFallback)); o.ObjectFallback == "" {
		o.ObjectFallback = ObjectFallbackAgent
	}
	if o.Enter == "" {
		o.Enter = EnterAuto
	}
	if o.Preflight == "" {
		o.Preflight = PreflightWarn
	}
	if o.OnEnd == "" {
		o.OnEnd = OnEndStay
	}
	if o.RezWait == 0 {
		o.RezWait = d.RezWait
	}
	fill := func(dst *time.Duration, def time.Duration) {
		if *dst <= 0 {
			*dst = def
		}
	}
	fill(&o.FeedWait, d.FeedWait)
	fill(&o.FeedStale, d.FeedStale)
	fill(&o.FeedLost, d.FeedLost)
	fill(&o.MoveStall, d.MoveStall)
	fill(&o.LeadWait, d.LeadWait)
	fill(&o.ThreatWait, d.ThreatWait)
	fill(&o.AddCooldown, d.AddCooldown)
	if o.OpenerDelay == 0 {
		o.OpenerDelay = d.OpenerDelay
	}
	fill(&o.UnbindWait, d.UnbindWait)
	if o.EndFightWait == 0 {
		o.EndFightWait = DefaultEndFightWait
	}
	fill(&o.StartWait, 30*time.Second)
	fill(&o.EnterVerify, d.EnterVerify)
	fill(&o.EnterGrace, d.EnterGrace)
	fill(&o.SummonGap, d.SummonGap)
	fill(&o.BindSettle, d.BindSettle)
	fill(&o.TankBusyWait, d.TankBusyWait)
	fill(&o.LookAheadETA, d.LookAheadETA)
	fill(&o.MoveGap, d.MoveGap)
	if o.LookAhead == 0 {
		o.LookAhead = d.LookAhead
	}
	if o.MoveLeg <= 0 {
		o.MoveLeg = d.MoveLeg
	}
	if o.MoveArrive <= 0 {
		o.MoveArrive = d.MoveArrive
	}
	if o.CarefulHop <= 0 {
		o.CarefulHop = d.CarefulHop
	}
	if o.GatherWait == 0 {
		o.GatherWait = d.GatherWait
	}
	if o.MoveRetries <= 0 {
		o.MoveRetries = d.MoveRetries
	}
	if o.ThreatWarnPct <= 0 {
		o.ThreatWarnPct = d.ThreatWarnPct
	}
	return o
}

// RemoteReport is the report's part for a run in remote mode (Report.Remote; absent in group mode).
type RemoteReport struct {
	Mode      string   `json:"mode"` // "remote"
	LeadBot   string   `json:"lead_bot"`
	Bots      []string `json:"bots"`
	Tank      string   `json:"tank,omitempty"` // the bot taken for the tank (named, or inferred from the first pull)
	Team      string   `json:"team,omitempty"` // the team the team gate ran with, and where it came from
	Bound     bool     `json:"bound"`          // the bind was acknowledged (or unanswered)
	Unbound   bool     `json:"unbound"`        // the run's end sent the unbind and it was answered
	UnbindVia string   `json:"unbind_via,omitempty"`
	// EndStay is the answer to the `cmd stay` sent before the unbind ("ack", "refused: ...", "no answer"); LoggedOut the bots logged out
	// after it (OnEnd logout).
	EndStay string `json:"end_stay,omitempty"`
	// EndFight is what the end of the run did about a fight still on ("out of combat after 12 s", "still in combat after 90 s; gave up");
	// empty when the party was out of combat.
	EndFight  string   `json:"end_fight,omitempty"`
	LoggedOut []string `json:"logged_out,omitempty"`
	// Preflight is the pre-run kit check (absent with preflight off).
	Preflight *Preflight `json:"preflight,omitempty"`
	// Refills are the `.bot maintenance` orders sent again at rest gates to casters who ran short of drinks (remote_rest.go).
	Refills []Refill `json:"refills"`
	// MaintenanceRuns are the automatic `.bot maintenance` rounds of the whole party during the run (every maintenance_every_legs legs and
	// after a recovered wipe; remote_maint.go; the preflight's is not counted). MaintenanceSkips counts the rounds not done, by reason:
	// "combat" (a leg's slot that never found a calm moment, or a round cut short by a fight), "disabled" (maintenance is off on the
	// server) and "no bot lines" (the game cannot send them).
	MaintenanceRuns  int            `json:"maintenance_runs"`
	MaintenanceSkips map[string]int `json:"maintenance_skips"`
	// EndedByModule is the reason the module gave when it ended the binding itself (lead bot gone, party disbanded, a player joined).
	EndedByModule string `json:"ended_by_module,omitempty"`
	// GMCommands is the GM commands the run sent: 0, or 1 when the enter phase teleported the lead bot (`.tele name`). The run sends no
	// other (no revive, aura or visibility).
	GMCommands int `json:"gm_commands"`
	// RunGMAssists is the GM `.appear <lead bot>` lines sent DURING the run to keep the agent's character near the party (a GM account;
	// remote_near.go). They are GM assists, not part of the party's play: gm_commands does not count them, so a target of "0 GM commands"
	// has to read this counter too. AgentWalks is the walks of the agent's own character behind the party (any other account).
	RunGMAssists int `json:"run_gm_assists"`
	AgentWalks   int `json:"agent_walks"`
	// Enter is the enter phase (absent when the party was already inside the plan's map).
	Enter *EnterReport `json:"enter,omitempty"`

	// The feed during the run.
	FramesReceived  uint64 `json:"frames_received"`
	FramesMissed    uint64 `json:"frames_missed"`
	FramesInvalid   uint64 `json:"frames_invalid"`
	FramesStale     uint64 `json:"frames_stale"`
	FramesTruncated uint64 `json:"frames_truncated"` // frames the module marked as cut short: hostiles may be missing
	FeedStalls      int    `json:"feed_stalls"`      // times the feed went quiet for FeedStale and came back
	LastSeq         uint64 `json:"last_seq"`

	// The orders.
	OrdersSent       int            `json:"orders_sent"`
	OrdersRefused    int            `json:"orders_refused"`
	OrdersUnanswered int            `json:"orders_unanswered"`
	Orders           map[string]int `json:"orders"`  // sent, by verb
	Refused          map[string]int `json:"refused"` // by "<verb>:<code>"

	// Decisions made from the feed.
	MoveStalls      int `json:"move_stalls"`      // moves sent again because the lead bot gained no ground
	WalkAggros      int `json:"walk_aggros"`      // fights that interrupted a walk
	ThreatHolds     int `json:"threat_holds"`     // mobs the party was sent at once the tank held their threat
	ThreatWaits     int `json:"threat_waits"`     // mobs the party was held back from until the tank held their threat
	ThreatOverrides int `json:"threat_overrides"` // mobs the party was sent at after ThreatWait without the tank holding them
	ThreatWarnings  int `json:"threat_warnings"`  // bots that came within ThreatWarnPct of the tank's threat on a mob
	PullFallbacks   int `json:"pull_fallbacks"`   // pulls done as an attack order because the module found no tank
	TankBusyWaits   int `json:"tank_busy_waits"`  // pulls the module refused as tank-busy that waited for the tank, then pulled again
	CCOrders        int `json:"cc_orders"`        // crowd-control orders
	BigPacks        int `json:"big_packs"`        // pulls of a pack of big_pack or more mobs
	BigPackCC       int `json:"big_pack_cc"`      // big-pack CC marks the module accepted
	Revives         int `json:"revives"`          // `.bot cmd revive` orders
	// GraveBatches is one record per batch of chests opened back to back (remote_batch.go; the notes "graves-batch: ..." say the same).
	GraveBatches  []GraveBatch `json:"grave_batches,omitempty"`
	LeftoverPulls int          `json:"leftover_pulls,omitempty"` // idle summons pulled by the leftover sweeps
	// Reactive CC (remote_patrol.go): marks put during a fight on a mob that joined it (a cc_always entry, or the fight grew to big_pack),
	// and marks put again on a mob whose CC lapsed while the fight went on.
	ReactiveCC int `json:"reactive_cc"`
	CCRenews   int `json:"cc_renews"`
	// Patrols (PullGroup.Patrol): pulls that waited for a patrol to walk out of patrol_aggro, the seconds of those waits, the waits that
	// ran out with the patrol still near (the pack was pulled with it counted in), and patrols pulled alone before their group.
	PatrolWaits    int     `json:"patrol_waits"`
	PatrolWaitS    float64 `json:"patrol_wait_s"`
	PatrolLocates  int     `json:"patrol_locates"`  // looks for a patrol none of whose members was in view (remote_patrol.go)
	PatrolLocateS  float64 `json:"patrol_locate_s"` // ... and the seconds they took
	PatrolTimeouts int     `json:"patrol_timeouts"`
	PatrolPulls    int     `json:"patrol_pulls"`
	// TinyMoves are walks whose last stretch was shorter than min_move (the module ignores such a move): the order went to a point
	// min_move+1 yd along the line instead.
	TinyMoves int `json:"tiny_moves"`
	// Tank first (README "Tank first"): TankThreatShare is the mean share of the top threat the tank held on the mobs of the fight, in
	// percent (100: top of every list, 0: not among the top three), over TankThreatSamples frames of fighting; LooseAddOrders are the
	// tank orders at a mob that hit a bot that was not the tank; CCRespected the crowd-controlled mobs the party was not sent at
	// (counted once per mob); TargetSwitches the times the party was sent at another mob while the one before still lived.
	TankThreatShare   float64 `json:"tank_threat_share"`
	TankThreatSamples int     `json:"tank_threat_samples"`
	LooseAddOrders    int     `json:"loose_add_orders"`
	CCRespected       int     `json:"cc_respected"`
	TargetSwitches    int     `json:"target_switches"`
	// AttackResends are the attack orders sent again at the mob the party was already sent at (after AssistEvery, with the tank not holding it).
	AttackResends  int     `json:"attack_resends"`
	UnsafeRezSkips int     `json:"unsafe_rez_skips"` // waits for a resurrection in place skipped for a live hostile near the bodies (remote_flee.go)
	Fled           int     `json:"fled"`             // mobs that ran off at low health and left the feed alive
	FleeOrders     int     `json:"flee_orders"`      // attack rounds at a running mob
	FleeWaits      int     `json:"flee_waits"`       // pulls held for a mob that ran off
	FleeWaitS      float64 `json:"flee_wait_s"`      // ... and the seconds they waited
	RezWaits       int     `json:"rez_waits"`        // times dead bots were left to a living caster's resurrection before a release
	Rezzed         int     `json:"rezzed"`           // bots that got up during those waits
	Rezzes         int     `json:"rezzes"`           // the same count under the trace event's name (`rez`): bots a party caster raised instead of their releasing
	PackLooks      int     `json:"pack_looks"`       // pack positions the lead bot walked to because the bots saw nothing of the pack from afar
	PacksSeenEmpty int     `json:"packs_seen_empty"` // pull groups counted as cleared because the party stood at every position and saw no hostile
	// The careful walk (remote_gather.go): waits for the party to gather on the lead bot before a pull or a hop, their seconds, the waits
	// that ran out with a bot still away, and the hostiles near a hop that were pulled before it.
	Gathers        int     `json:"gathers"`
	GatherWaitS    float64 `json:"gather_wait_s"`
	GatherTimeouts int     `json:"gather_timeouts"`
	GatherGaveUp   int     `json:"gather_gave_up"` // bots a gather stopped waiting for in their leg (no closer during a whole wait)
	AheadPulls     int     `json:"ahead_pulls"`
	AheadBigPulls  int     `json:"ahead_big_pulls"` // ... of which pulled a pack of big_pack or more mobs, or one with a cc_always member (they get the pack CC)
	// BossSearches counts the kill-boss steps that found no boss at his spot and waited for him to walk back (remote_fight.go searchBoss).
	BossSearches int `json:"boss_searches"`
	// RejoinTPs are the `cmd summon <bot>` orders the commander sent to bring a bot back to the lead bot (remote_rejoin.go); each is also a
	// run_gm_assists.
	RejoinTPs int `json:"rejoin_tps"`
	// GMCrosses counts the gm_cross waypoints the run crossed with a GM move of the party (remote_gmcross.go).
	GMCrosses int `json:"gm_crosses"`
	// The gathers around direct steps (remote_gather_direct.go): waits for every bot to stand on the lead bot, their seconds, the waits that
	// ran out, the `cmd move` to the lead bot's spot (never a summon) and the `cmd stay` that holds the party there.
	DirectGathers        int     `json:"direct_gathers"`
	DirectGatherWaitS    float64 `json:"direct_gather_wait_s"`
	DirectGatherTimeouts int     `json:"direct_gather_timeouts"`
	DirectGatherMoves    int     `json:"direct_gather_moves"`
	DirectGatherStays    int     `json:"direct_gather_stays"`
	// Tank orders and pulls held back while the tank is dead, released or far from the mob / the lead bot (remote_tank.go).
	TankOrdersHeld int `json:"tank_orders_held"`
	TankAwayWaits  int `json:"tank_away_waits"`
	Unreachable    int `json:"unreachable"` // pulls and moves the bots answered "unreachable" (each is in the notes and the trace with its point)
	// The walks: WalkStops counts the times the lead bot stood still in the middle of a walk (after it had moved, before the walk's end),
	// WalkStoppedS the seconds it stood, LookAheadMoves the `move` orders sent while the bot was still on its way to the last
	// one, and FightHolds the "stay here" moves sent when a fight ended.
	WalkStops      int     `json:"walk_stops"`
	WalkStoppedS   float64 `json:"walk_stopped_s"`
	LookAheadMoves int     `json:"look_ahead_moves"`
	FightHolds     int     `json:"fight_holds"`

	ObjectStepsAhead int `json:"object_steps_ahead"` // use_object, loot_object, use_item and game-object waits the plan has from the first leg on
	// Carrier is the bot that did (or will do) the object steps; ObjectSteps is what each did (remote_objects.go).
	Carrier     string             `json:"carrier,omitempty"`
	ObjectSteps []ObjectStepReport `json:"object_steps,omitempty"`
	// EntryTargets says whether the object steps name their objects by entry and wait on `cmd state` ("on"), or the module did not answer
	// that probe ("off: <why>", the GUID path and the keep-near of old); empty when the plan has no object step to probe with
	// (remote_entry.go). StatePolls counts the `cmd state` orders of game-object waits.
	EntryTargets string `json:"entry_targets,omitempty"`
	StatePolls   int    `json:"state_polls,omitempty"`

	// AreaTriggers are the area_trigger steps the run took and who took each (remote_trigger.go); AgentTriggers counts the CMSG_AREATRIGGER
	// packets the agent's own character sent (each is a step the lead bot could not take).
	AreaTriggers []TriggerReport `json:"area_triggers,omitempty"`
	// WipesAfterKill counts the wipes that came after the leg's boss was dead (the adds he left killed the party): recovered, not counted in
	// wipes or party_downs, and no failure of the boss step.
	WipesAfterKill int `json:"wipes_after_kill,omitempty"`
	AgentTriggers  int `json:"agent_triggers,omitempty"`

	// Observe is the run leader watch of an observed run (observe.go); absent in a driven run.
	Observe *ObserveReport `json:"observe,omitempty"`

	// Live tuning (tune.go, remote_tune.go): the tunables changed during the run and the reloads of the plan file, each with when, old
	// and new. `turtle-agent tune-diff <trace>` turns them into a patch for the tunables file and the plan file.
	TuneChanges []TuneChange `json:"tune_changes"`
	PlanReloads []PlanReload `json:"plan_reloads"`
}

func (r *RemoteReport) clone() *RemoteReport {
	if r == nil {
		return nil
	}
	c := *r
	c.Bots = slices.Clone(r.Bots)
	c.LoggedOut = slices.Clone(r.LoggedOut)
	c.Preflight = r.Preflight.clone()
	c.Refills = slices.Clone(r.Refills)
	c.MaintenanceSkips = cloneMap(r.MaintenanceSkips)
	c.TuneChanges = slices.Clone(r.TuneChanges)
	c.PlanReloads = slices.Clone(r.PlanReloads)
	c.Enter = r.Enter.clone()
	c.Observe = r.Observe.clone()
	c.ObjectSteps = cloneObjectSteps(r.ObjectSteps)
	c.AreaTriggers = r.cloneTriggers()
	c.Orders = cloneMap(r.Orders)
	c.Refused = cloneMap(r.Refused)
	return &c
}

func cloneMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Errors of the remote run.
var (
	errBindingLost = errors.New("the commander binding ended")
	errFeedLost    = errors.New("the view feed stopped")
)

// RemoteDriver runs a Plan with a bot group it leads from outside (see the package comment above). Run blocks; Report can be
// read from another goroutine while it runs.
type RemoteDriver struct {
	plan *Plan
	g    RemoteGame
	cfg  Config
	opt  RemoteOptions
	clk  Clock
	rep  reporter

	st     *Stepper
	curLeg int

	// The feed as the driver last looked at it.
	fr      tbm.ViewFrame
	haveFr  bool
	n       uint64    // frames the feed had published at the last look
	nAt     time.Time // when the driver saw n change
	stalled bool

	bound     bool
	unbinding bool
	// groupLocked is set when the enter phase begins to send the party in: no leave, invite or bind may follow until the run ends.
	groupLocked   bool
	entryRecorded bool      // the instance the lead bot entered is in the ledger
	enterCmd      bool      // the portal walk ends in `cmd enter` instead of a wait for the trigger
	enterCmdEnd   time.Time // the ceiling of the cmd enter phase, walk included
	outside       *uint32   // the map a walk before the entry plans its paths on (nil: the plan's)
	aheadPulling  bool      // a pull of a hostile near a walk's next hop is going on (remote_gather.go)
	downSaid      bool      // partyDown has stopped the leg for this round of deaths
	gatherSt      gatherState
	// aheadPassedLeg: hostiles a pull on the way passed over (no fight, evaded, refused), with the leg it happened in
	aheadPassedLeg map[packet.GUID]int
	// deadly is the avoid state (remote_avoid.go): the spots where bots died with no wound and the last frame of each living member.
	deadly         deadlyState
	endsAtBind     int
	stats0         tbm.ViewStats
	statsMu        sync.Mutex // guards stats0: Status reads it from another goroutine while begin writes it
	prevDead       map[packet.GUID]bool
	deadSeen       bool
	revived        map[packet.GUID]int
	lastRevive     map[packet.GUID]time.Time
	wipeRecover    int
	wipeRecoverLeg map[int]int // recovered wipes per leg
	batch          *batchRun   // the grave batch that is being opened and fought (remote_batch.go)
	gb             gravesState
	sweepWiped     bool // a wipe ended a leftover sweep (sweepErr); stepFailedRecover reads and clears it
	healerCapped   int  // leg+1 of the leg whose rest gates stopped waiting for the healer's mana alone
	bossFight      bool // a boss fight (doKill) is on
	recoveries     int

	tank        packet.GUID
	engagedAt   map[packet.GUID]time.Time // when a mob was first seen fighting the party
	attacked    map[packet.GUID]bool      // mobs the party was sent at
	heldNoted   map[packet.GUID]bool
	waitNoted   map[packet.GUID]bool
	warned      map[string]bool
	ccDone      map[packet.GUID]bool
	ccOrdered   map[packet.GUID]time.Time  // when a big-pack CC order went out for a mob (remote_cc.go)
	rccLast     time.Time                  // the last reactive CC order (remote_patrol.go)
	midStart    bool                       // the run began mid-plan (from_leg, from_pull_group, start_here, resume) ...
	midStartLeg int                        // ... in this leg
	rccN        map[packet.GUID]int        // reactive CC orders per mob
	patrolSeen  map[packet.GUID]*patrolObs // units of patrol entries seen on a patrol path (remote_patrol.go)
	// patrolWalkedAt is when a member of each patrol was last seen walking (the next group of the leg does not look for it again at once).
	patrolWalkedAt map[*Patrol]time.Time
	// wantParty is how many bots the party has when the run starts (the lead bot and the named bots; the feed's party when none are
	// named); shortSince is when the feed first showed fewer, partyShort whether the stop has been said (remote_party.go).
	ccLookAt   time.Time // the last look for the party's mage (ensureCCBot)
	wantParty  int
	shortSince time.Time
	shortSaid  bool
	// paused: Pause was called; the run ends stopped and keeps its binding (dungeon_pause).
	paused       atomic.Bool
	skipped      map[packet.GUID]string
	lastAssist   time.Time
	lastMove     time.Time // when the last `cmd move` went out (MoveGap keeps the orders apart)
	moveTo       Point     // the point of the last `cmd move` the module took: the lead bot's stay spot
	haveMoveTo   bool
	tf           tankFirst                 // the tank-first state of the fights (remote_tank.go)
	outSince     map[packet.GUID]time.Time // since when each living member stands on another map than the plan's (outsideAlive)
	offSince     map[packet.GUID]time.Time // when a bot dead outside the dungeon was first left to run back (no GM account)
	gmRevived    map[packet.GUID]int       // `.revive` lines sent for a bot dead outside the dungeon (reviveOffMap)
	lastGMRevive map[packet.GUID]time.Time //
	unitMem      map[*UnitWait]*unitMemory // what each "gone" unit wait has seen (remote_trigger.go)
	home         *agentHome                // the agent's safe point after an area trigger it took itself (remote_trigger.go)
	legLoot      bool                      // the leg under way has companions_loot false and it was turned off (legMuted: for whom)
	legMuted     []string                  // the bots whose looting the leg's companions_loot false turned off (remote_trigger.go)
	kit          restKit                   // the bots' drink counts and refills (remote_rest.go)
	maint        maintState                // the automatic `.bot maintenance` that is due (remote_maint.go)
	// objectTouched is set when the Stepper asked for game objects, i.e. it is on an object step.
	objectTouched bool
	obj           objState                   // the object steps' carrier, corpses and step reports (remote_objects.go)
	team          string                     // the party's team for the team gate ("" when unknown: everything runs)
	killedAt      map[uint32][]Point         // where mobs of an entry were killed in this run (evidence that a pack is gone)
	ledger        map[packet.GUID]*ledgerMob // every hostile seen fighting the party in this run (remote_ledger.go)
	near          nearState                  // the agent kept near the party (remote_near.go)
	gmCrossing    bool                       // a gm_cross is being done: the start phase's helpers book their notes and GM lines as the crossing's (remote_gmcross.go)
	rejoin        rejoinState                // bots brought back to the lead bot with cmd summon (remote_rejoin.go)
	entry         entryState                 // object steps by entry and game-object waits on cmd state (remote_entry.go)
	seenHostile   map[packet.GUID]uint32     // units listed alive in the feed at some point of the run, with their entry (the team gate's evidence)
	killedGUID    map[packet.GUID]bool       // units a won pull killed

	pts       *ptSampler         // the pull-test try being scored (pulltest.go); nil outside one
	tr        *tracerun.Recorder // the run id and the trace (nil-safe; no file when tracing is off)
	trSeen    uint64             // the feed frame count the trace last took
	trRoster  bool
	trStalled bool

	// The progress watchdog (watchdog.go) and the requests its recoveries leave for the loops they interrupt.
	wd         *watchdog
	wdRepick   flagOnce // end the fight, to pick the target again
	wdObject   flagOnce // give up the carrier order's retries
	viewResent bool     // `commander view on` went out again since the feed was last heard from

	obs *observeState // the run leader watch of an observed run (observe.go); nil otherwise

	fl   fleeState // the mobs that run at low health and the ones that got away (remote_flee.go)
	tx   tuneExtra // the thresholds that were constants (tune.go)
	live liveTune  // live tuning: the tunables file, the plan file and tune_set (remote_tune.go)
}

// NewRemoteDriver prepares a run of plan in remote mode. A zero clock is the wall clock.
func NewRemoteDriver(plan *Plan, g RemoteGame, cfg Config, opt RemoteOptions, clk Clock) *RemoteDriver {
	if clk.Now == nil || clk.Sleep == nil {
		clk = RealClock()
	}
	d := &RemoteDriver{plan: plan, g: g, cfg: cfg.withDefaults(), opt: opt.withDefaults(), clk: clk, curLeg: -1,
		prevDead: map[packet.GUID]bool{}, revived: map[packet.GUID]int{}, lastRevive: map[packet.GUID]time.Time{},
		engagedAt: map[packet.GUID]time.Time{}, attacked: map[packet.GUID]bool{}, heldNoted: map[packet.GUID]bool{}, waitNoted: map[packet.GUID]bool{},
		warned: map[string]bool{}, ccDone: map[packet.GUID]bool{}, ccOrdered: map[packet.GUID]time.Time{}, skipped: map[packet.GUID]string{}, killedAt: map[uint32][]Point{}, patrolWalkedAt: map[*Patrol]time.Time{}}
	d.tx = defaultTuneExtra()
	d.initLive()
	d.rep.init(plan, clk.Now)
	d.initWatchdog()
	d.rep.r.SetupShortcuts = slices.Clone(cfg.Setup)
	mode := "remote"
	if opt.Observe != nil {
		mode = "observe"
	}
	d.rep.r.Remote = &RemoteReport{Mode: mode, LeadBot: opt.Lead, Bots: slices.Clone(opt.Bots), Tank: opt.Tank,
		Orders: map[string]int{}, Refused: map[string]int{}, Refills: []Refill{}, MaintenanceSkips: map[string]int{}, TuneChanges: []TuneChange{}, PlanReloads: []PlanReload{}}
	d.initTrace()
	return d
}

// Report is a copy of the run report, usable while the run goes on.
func (d *RemoteDriver) Report() Report { r, _ := d.Status(); return r }

// Status is the report and whether the run has ended, read together under one lock.
func (d *RemoteDriver) Status() (Report, bool) {
	r, ended := d.rep.snapshotEnded()
	if r.Remote != nil && !ended {
		d.fillFeed(r.Remote)
	}
	return r, ended
}

// fillFeed copies the feed's counters since the bind into the remote report.
func (d *RemoteDriver) fillFeed(r *RemoteReport) {
	s := d.g.ViewStats()
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	r.FramesReceived, r.FramesMissed, r.FramesInvalid, r.FramesStale = s.Frames-d.stats0.Frames, s.Missed-d.stats0.Missed, s.Invalid-d.stats0.Invalid, s.Stale-d.stats0.Stale
	r.FramesTruncated = s.Truncated - d.stats0.Truncated
	r.LastSeq = s.LastSeq
}

func (d *RemoteDriver) remote(f func(*RemoteReport)) {
	d.rep.update(func(r *Report) {
		if r.Remote != nil {
			f(r.Remote)
		}
	})
}

// Run does the plan from leg fromLeg. The commander binding is made first and always ended, whatever way the run ends (done,
// failed, stopped or a panic): the unbind is deferred. It returns nil when the plan is done.
func (d *RemoteDriver) Run(ctx context.Context, fromLeg int) error {
	if fromLeg < 0 || fromLeg >= len(d.plan.Legs) {
		return fmt.Errorf("from_leg %d: the plan has legs 0-%d", fromLeg, len(d.plan.Legs)-1)
	}
	if err := d.checkStart(fromLeg); err != nil {
		return err
	}
	if err := d.checkGMCross(ctx, fromLeg); err != nil {
		return err
	}
	d.markAvoid()
	d.rep.start(fromLeg)
	d.traceBegin(fromLeg)
	d.wd.start()
	err := d.runRecovering(ctx, fromLeg)
	d.finish(err)
	return err
}

// Pause marks the run as paused: the caller then cancels its context, and the run ends "stopped" with the bots stood still and the
// binding kept (no `commander unbind`), so that a dungeon_run with resume re-enters the same group and instance. Without it a stopped
// run unbinds, which returns the hires to the commander's raid and loses the instance (ZF-2 run 3b).
func (d *RemoteDriver) Pause() { d.paused.Store(true) }

// runRecovering is run with a panic turned into a failed run: the deferred unbind has run by the time the panic gets here, and the
// report says what happened instead of the process going down with the binding half undone.
func (d *RemoteDriver) runRecovering(ctx context.Context, fromLeg int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
			d.rep.note(fmt.Sprintf("the remote run panicked: %v\n%s", r, debug.Stack()))
		}
	}()
	return d.run(ctx, fromLeg)
}

func (d *RemoteDriver) finish(err error) {
	d.wd.stop()
	leg := d.curLeg
	var state, errText, kind, text string
	switch {
	case err == nil:
		state, kind, text = "done", events.KindDungeonDone, "dungeon "+d.plan.Name+" done (remote)"
		if deg := d.rep.snapshot().Degraded; len(deg) > 0 {
			text += " (degraded: " + strings.Join(deg, "; ") + ")"
		}
	case ctxDone(err):
		state, errText, kind, text = "stopped", "stopped", events.KindDungeonFailed, "dungeon "+d.plan.Name+" stopped (remote)"
	default:
		state, errText, kind, text = "failed", err.Error(), events.KindDungeonFailed, "dungeon "+d.plan.Name+" failed (remote): "+err.Error()
	}
	d.endLeg(state)
	at := d.rep.now()
	d.remote(d.fillFeed)
	data := d.rep.summaryEnded(state, errText, at)
	if err != nil {
		data["error"], data["leg"] = errText, leg
	}
	if deg := d.rep.snapshot().Degraded; len(deg) > 0 {
		data["degraded"] = deg
	}
	d.emit(kind, text, data)
	d.traceEnd(state, errText, at)
	d.rep.end(state, errText, at)
}

func (d *RemoteDriver) run(ctx context.Context, fromLeg int) (err error) {
	// Every way out: done, failed, stopped, a panic. Only a failed run with KeepOnFail keeps its binding (unbind).
	// A paused run (dungeon_pause) is stopped on purpose to be resumed: it keeps its binding whatever keep_on_fail says.
	defer func() {
		d.unbind((err != nil && !ctxDone(err) && d.opt.KeepOnFail) || (d.paused.Load() && ctxDone(err)))
	}()
	// Declared after the unbind, so it runs before it: the bots' area spells that a grave batch turned off go back on while the binding lasts.
	defer d.legAoEOn()
	// The tunables file (and tune_set requests made already) count from the start: the entry walk and the preflight read them too. The
	// plan is not read again here, it was loaded a moment ago.
	d.applyTunables(liveBoundary{leg: fromLeg, kind: boundaryRun})
	setupAt := d.clk.Now()
	if d.observing() {
		// A server without the run leader (or without the plan) is refused before a single bot is hired.
		if err = d.probeRunLeader(ctx); err != nil {
			return err
		}
	}
	err = d.begin(ctx, fromLeg)
	setupS := d.clk.Now().Sub(setupAt).Seconds()
	d.rep.update(func(r *Report) {
		r.SetupS = setupS
		if r.Ready != nil {
			r.Ready.SetupS = setupS
		}
	})
	if err != nil {
		return err
	}
	d.resolveTeam()
	if d.observing() {
		return d.observe(ctx)
	}
	d.st = NewStepperAt(d.plan, remoteWorld{d}, d.clk.Now, fromLeg)
	if g := d.startGroup(); g >= 0 {
		d.st = NewStepperAtGroup(d.plan, remoteWorld{d}, d.clk.Now, fromLeg, g)
	}
	if d.opt.Pulltest != nil {
		return d.runPulltest(ctx)
	}
	resumed := false
	if d.cfg.Resume {
		if r, ok := d.st.ResumeAt(); ok {
			resumed = true
			d.rep.update(func(rp *Report) { rp.Resumed = &r })
			d.rep.note(r.String())
		}
	}
	if step, ok := d.st.SkipToFiredTrigger(); ok {
		resumed = true
		d.rep.note(fmt.Sprintf("resume: the area trigger of leg %d (step %d) was taken already: the party is in the room, the walk, the pulls and the steps before it are not run again", d.st.leg, step))
	}
	d.midStart, d.midStartLeg = fromLeg > 0 || d.opt.StartHere || d.opt.FromPullGroup != nil || resumed, fromLeg
	waiting := [2]int{-1, -1}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.legLootSync(ctx); err != nil {
			d.stepFailedRecover(ctx, err)
			continue
		}
		if err := d.nearBeforeObjectStep(ctx); err != nil {
			d.stepFailedRecover(ctx, err)
			continue
		}
		d.objectTouched = false
		a := d.liveStep(d.st.Next()) // a leg start or a pull-group start: the tunables and the plan are read again (remote_tune.go)
		d.setProgress(a)
		d.noteSkipped()
		d.rep.setCurrent(a.Label)
		d.wd.setStep(a.Label)
		if d.objectTouched {
			if err, handled := d.objectLook(ctx, a); handled {
				if err != nil {
					return err
				}
				continue
			}
		}
		if a.Kind != ActFailed && a.Leg != d.curLeg {
			d.endLeg("done")
			if a.Kind != ActDone {
				if skipped, err := d.skipOptionalLeg(ctx, a.Leg); err != nil {
					d.stepFailedRecover(ctx, err)
					continue
				} else if skipped {
					continue
				}
				if err := d.startLeg(ctx, a.Leg, a); err != nil {
					if d.stepFailedRecover(ctx, err) {
						continue
					}
					continue
				}
			}
			if a.Kind == ActPull {
				a = d.liveStep(a) // the leg's start gate took a while: the first group is a pull-group start as well
			}
		}
		switch a.Kind {
		case ActDone:
			return nil
		case ActFailed:
			return errors.New(a.Err)
		case ActWait:
			if waiting != [2]int{a.Leg, a.Step} {
				waiting = [2]int{a.Leg, a.Step}
				d.emit(events.KindDungeonStep, a.Label, map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind), "mode": "remote"})
			}
			// A plan's wait has its own timeout_s: the watchdog does not count it.
			release := d.wd.hold()
			err := d.tick(ctx, min(d.cfg.WaitPoll, a.Remaining))
			release()
			if err != nil {
				d.stepFailedRecover(ctx, err)
			}
			continue
		}
		began := d.clk.Now()
		var err error
		d.wdObject.clear() // a request left by an earlier step is not this step's
		leave := d.wd.enter(wdPhaseName(a.Kind), a.Label, d.wdRecoverStep(a.Kind))
		switch a.Kind {
		case ActWalk:
			err = d.agentGoHome(ctx, false) // an agent left in a trigger's zone goes back before the party walks on
			if err == nil {
				if a.GMCross {
					err = d.gmCross(ctx, a.To)
				} else {
					err = d.tracedWalkAs(ctx, a.To, a.Direct)
				}
			}
			if err == nil {
				d.obj.unchecked = ""     // the party walked on: whatever a wait passed unchecked let it through
				_, err = d.keepNear(ctx) // the leg's walk is over: bring the agent up
			} else if d.obj.unchecked != "" {
				err = fmt.Errorf("%w; the earlier wait for %s passed unchecked (the agent could not see it): a door that stayed closed would fail this walk", err, d.obj.unchecked)
			}
		case ActPull:
			err = d.doPull(ctx, a)
		case ActKill:
			err = d.doBoss(ctx, a)
			if err == nil {
				err = d.fightEngagedOut(ctx, a.Label) // the adds the boss left are fought before the leg ends or anyone rests
			}
			if err == nil {
				err = d.agentGoHome(ctx, true) // the boss (Zum'rah) is dead: the agent leaves the zone it stayed in; a failed walk is no failure now
			}
		case ActLoot, ActUse, ActItem, ActRitual:
			err = d.doObjectStep(ctx, a)
		case ActTrigger:
			err = d.doAreaTrigger(ctx, a)
		case ActGossip:
			err = d.doGossip(ctx, a)
		default:
			err = fmt.Errorf("remote mode cannot do %q", a.Kind)
		}
		leave()
		data := map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind), "ok": err == nil, "seconds": d.clk.Now().Sub(began).Seconds(), "mode": "remote"}
		if err != nil {
			data["error"] = err.Error()
		}
		d.emit(events.KindDungeonStep, a.Label, data)
		if err != nil {
			d.stepFailedRecover(ctx, err)
			continue
		}
		d.st.Complete()
		d.wd.progress("step done: " + a.Label)
	}
}

// remoteObjectShort is the failure of an object step in remote mode; remoteObjectMsg adds why.
const remoteObjectShort = "remote mode: object step needs the agent nearby"

// objectStepError is the failure of an object step: using or looting a game object, using an item, waiting for a game object's
// state. The leader of a remote run is a bot, and a bot does not open chests for the agent.
func (d *RemoteDriver) objectStepError(a Action) error {
	kind := "object step"
	if a.Leg >= 0 && a.Leg < len(d.plan.Legs) && a.Step >= 0 {
		l := d.plan.Legs[a.Leg]
		steps := l.Steps
		if strings.Contains(a.Label, "after_boss step") {
			steps = l.AfterBoss
		}
		if a.Step < len(steps) && objectStepKind(steps[a.Step]) != "" {
			kind = objectStepKind(steps[a.Step])
		}
	}
	label := a.Label
	if a.Kind == ActFailed {
		label = a.Err
	}
	return fmt.Errorf("%s: %s: %s (%s)", remoteObjectShort, kind, label, "the agent's character is not with the group; run that leg in group mode")
}

func objectStepKind(st Step) string {
	switch {
	case st.UseObject != nil:
		return "use_object"
	case st.LootObject != nil:
		return "loot_object"
	case st.UseItem != nil:
		return "use_item"
	case st.Ritual != nil:
		return "ritual"
	case st.WaitFor != nil && st.WaitFor.GameObject != nil:
		return "wait_for game_object"
	}
	return ""
}

// stepFailedRecover ends the plan with err, or, for a wipe with RecoverWipe on, recovers the party and lets the step be tried again.
// It reports whether the run goes on.
func (d *RemoteDriver) stepFailedRecover(ctx context.Context, err error) bool {
	if errors.Is(err, errWipe) {
		d.ledgerClearAbsences()
		down := errors.Is(err, errPartyDown)
		// A wipe in a leg whose boss is dead already (the party died to the adds the boss left: Zum'rah's zombies and Dead Heroes) is no
		// failure of the boss step: it is recovered like any wipe (also with recover_wipe off: the boss is down) and the leg goes on, and the
		// report counts it apart ("wipe after the kill").
		after := d.legBossDead()
		inBatch := d.batch != nil || d.sweepWiped // a wipe in a grave batch (batchWiped clears it) or in a leftover sweep
		d.sweepWiped = false
		d.rep.update(func(r *Report) {
			switch {
			case after:
			case down:
				r.PartyDowns++
			default:
				r.Wipes++
			}
		})
		if after {
			d.remote(func(r *RemoteReport) { r.WipesAfterKill++ })
			d.rep.note(fmt.Sprintf("wipe after the kill: the boss of leg %d is dead; recovering the party, the leg goes on (%v)", max(d.curLeg, 0), err))
		}
		d.batchWiped()
		d.tr.Event("wipe", map[string]any{"leg": d.curLeg, "error": err.Error(), "party_down": down, "after_kill": after})
		d.rep.note(err.Error())
		// The cap counts per leg (MaxWipeRecoveries recoveries in one leg), with a run-wide ceiling of wipeRecoverRunFactor times that: a run
		// with a wipe in each of several legs recovers each of them (live, zf-zum2-s2: wipes in legs 5 and 8 recovered, the third, in the
		// Zum'rah leg, hit the run-wide cap of 2 and failed "wipe").
		if d.wipeRecoverLeg == nil {
			d.wipeRecoverLeg = map[int]int{}
		}
		if inBatch && !after && d.slotReserved() {
			// The leg's last recovery slot is kept for the boss: a wipe in the graves that would use it is not recovered. The party is dead and
			// cannot go on to the boss without a recovery, so the run ends here; a run started again at this leg has a cap of its own, and the
			// graves already opened are skipped (cmd state).
			d.rep.note(fmt.Sprintf("wipe in the grave batches or a leftover sweep with %d of %d recovery slot(s) of the leg left: the last slot is kept for the boss, so this wipe is not recovered and the run stops here", d.recoverySlotsLeft(), d.cfg.MaxWipeRecoveries))
			d.st.Fail("wipe in the graves: not recovered, the last recovery slot of the leg is kept for the boss")
			return false
		}
		if (d.cfg.RecoverWipe || after) && d.wipeRecoverLeg[d.curLeg] < d.cfg.MaxWipeRecoveries && d.wipeRecover < d.cfg.MaxWipeRecoveries*wipeRecoverRunFactor && ctx.Err() == nil {
			d.wipeRecover++
			d.wipeRecoverLeg[d.curLeg]++
			if rerr := d.recoverWipe(ctx); rerr == nil {
				d.rep.update(func(r *Report) { r.WipeRecoveries++ })
				_ = d.maintWipeRecovered(ctx) // once the party is calm; a context error ends the run at the next look
				d.obj.waitAt = time.Time{}    // the step comes back to its game-object wait: the settle time starts again (remote_objects.go)
				return true
			} else {
				d.st.Fail("wipe: " + rerr.Error())
				return false
			}
		}
		d.st.Fail("wipe")
		return false
	}
	d.st.Fail(err.Error())
	return false
}

// wipeRecoverRunFactor times MaxWipeRecoveries is the run-wide ceiling of recovered wipes (the cap itself counts per leg).
const wipeRecoverRunFactor = 5

// remoteWorld is the Stepper's World over the feed: the lead bot's position and the hostiles the bots see. It has no game
// objects: the agent does not see the dungeon.
type remoteWorld struct{ d *RemoteDriver }

func (w remoteWorld) Position() Point {
	p, _ := w.d.leadPos()
	return p
}

// EntryTargets implements EntryWorld: the module takes objects by entry and answers cmd state (remote_entry.go). Only the object steps
// and game-object waits ask, so it counts as touching the objects.
func (w remoteWorld) EntryTargets() bool {
	if !w.d.entry.ok {
		return false
	}
	w.d.objectTouched = true
	return true
}

func (w remoteWorld) GameObjects() []objects.GameObject {
	w.d.objectTouched = true // only the object steps and game-object waits ask
	return w.d.objectsInView()
}

// Team makes the world a TeamWorld: the team gate skips what is for the other team (a boss friendly to the party never shows in the feed).
func (w remoteWorld) Team() string { return w.d.team }

// HostileSeen makes the world a HostileWorld: the team gate fights a part gated to the other team when the feed lists its mobs hostile.
func (w remoteWorld) HostileSeen(entries []uint32) bool { return w.d.hostileSeen(entries) }

// TeamGateLook makes the world a TeamGateLook: a gated part is looked at before it is skipped.
func (w remoteWorld) TeamGateLook() bool { return true }

func (w remoteWorld) PartyNear(c Point, r float32) bool {
	for _, m := range w.d.fr.Party {
		if !m.Dead && dist3(partyPoint(m), c) <= float64(r) {
			return true
		}
	}
	return false
}

func (w remoteWorld) HostilesWithin(c Point, r float32) int {
	n := 0
	for _, h := range w.d.fr.Hostiles {
		if h.HPPct > 0 && dist3(hostilePoint(h), c) <= float64(r) {
			n++
		}
	}
	return n
}

func hostilePoint(h tbm.HostileRec) Point { return Point{h.X, h.Y, h.Z} }
func partyPoint(p tbm.PartyRec) Point     { return Point{p.X, p.Y, p.Z} }

// leadPos is where the lead bot is in the newest frame.
func (d *RemoteDriver) leadPos() (Point, bool) {
	if !d.haveFr {
		return Point{}, false
	}
	l, ok := d.fr.LeadRec()
	if !ok {
		return Point{}, false
	}
	return partyPoint(l), true
}

// resolveTeam takes the party's team for the team gate: the run's team option, else the game's (the agent's own race; the feed carries
// no race, and the owned bots share the account's faction). Without either, nothing is gated and the report says so.
func (d *RemoteDriver) resolveTeam() {
	src := ""
	switch {
	case d.opt.Team != "":
		d.team, src = d.opt.Team, "the team option"
	default:
		if ts, ok := d.g.(TeamSource); ok {
			if t := ts.Team(); t != "" {
				d.team, src = t, "the agent's own race (the bots are taken to share it; pass team to override)"
			}
		}
	}
	if d.team == "" {
		d.rep.note("team gate: the party's team is unknown (no team option, the agent's race is not known); every team-gated step runs")
		return
	}
	d.remote(func(r *RemoteReport) { r.Team = d.team })
	d.rep.note(fmt.Sprintf("team gate: %s, from %s", d.team, src))
}

// noteSkipped reports what the team gate passed over since the last look, in the report and as dungeon_step events.
func (d *RemoteDriver) noteSkipped() {
	for _, text := range d.st.Skipped() {
		d.rep.note(text)
		action := "skip_team"
		if strings.HasSuffix(text, ": fought") {
			action = "team_override"
		}
		d.emit(events.KindDungeonStep, text, map[string]any{"action": action, "ok": true, "mode": "remote"})
	}
}

// begin binds the party, switches the feed on, waits for the first frame and checks the lead bot is where the plan starts.
func (d *RemoteDriver) begin(ctx context.Context, fromLeg int) error {
	if strings.TrimSpace(d.opt.Lead) == "" && !d.opt.Prepare.HiredParty() {
		return errors.New("remote mode needs a lead_bot")
	}
	if err := RemoteAbilities(d.g, d.opt, d.plan); err != nil {
		return err
	}
	if err := d.prepare(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(d.opt.Lead) == "" {
		return errors.New("the prepare step named no lead bot")
	}
	d.countObjectSteps(fromLeg)
	d.g.ResetView()
	d.setStats0(d.g.ViewStats())
	d.endsAtBind = len(d.g.BindingEnds())
	r, err := d.g.Command(ctx, bindCommand(d.opt.Lead, d.opt.Bots))
	if err != nil {
		return fmt.Errorf("commander bind: %w", err)
	}
	adopted := false
	switch r.Kind {
	case tbm.KindErr:
		if r.Err == nil || r.Err.Code != "already-bound" {
			return fmt.Errorf("commander bind refused: %s", describeBindErr(r.Err))
		}
		// The account's commander already commands a party: that is this run's party, or another's.
		if err := d.adoptBinding(ctx, r.Err); err != nil {
			return err
		}
		adopted = true
	case tbm.KindTimeout:
		// No answer: it may have gone through. The unbind at the end clears it either way.
		d.rep.note("commander bind: no ACK or ERR came; going on with the feed (the module may be an older build without the remote commander)")
	}
	d.bound = true
	d.remote(func(r *RemoteReport) { r.Bound = true })
	d.rep.note(fmt.Sprintf("remote mode: bound lead bot %s%s", d.opt.Lead, botsSuffix(d.opt.Bots)))
	// Every new binding starts with the view on; say so anyway, a binding that survived an earlier run may have it off.
	if vr, err := d.g.Command(ctx, "commander view on"); err != nil {
		d.rep.note("commander view on: " + err.Error())
	} else if vr.Kind == tbm.KindErr {
		d.rep.note("commander view on refused: " + describeBindErr(vr.Err))
	}
	if err := d.waitFirstFrame(ctx); err != nil {
		return err
	}
	if adopted {
		if err := d.checkAdopted(); err != nil {
			d.bound = false // not this run's binding: leave it as it is, but the feed this run switched on is put back off
			if _, verr := d.g.Command(ctx, "commander view off"); verr != nil {
				d.rep.note("commander view off (the other group's binding): " + verr.Error())
			}
			d.remote(func(r *RemoteReport) { r.Bound = false })
			return err
		}
	}
	if _, ok := d.fr.LeadRec(); !ok {
		return fmt.Errorf("the first frame does not list the lead bot %s (lead guid %s)", d.opt.Lead, d.fr.Lead)
	}
	if d.opt.Enter != EnterOff && d.needsEntry() {
		// Nothing goes in before the whole named party is in the bound group: a bot left out is invited again, or the run fails here.
		if err := d.ensureWholeParty(ctx); err != nil {
			return err
		}
		if err := d.enter(ctx); err != nil {
			return err
		}
	}
	if l, ok := d.fr.LeadRec(); !ok {
		return fmt.Errorf("the lead bot %s is not in the feed any more (lead guid %s)", d.opt.Lead, d.fr.Lead)
	} else if d.plan.Map != l.Map {
		return fmt.Errorf("the lead bot %s is on map %d and the plan needs map %d: bring the group into the dungeon first (or let the run enter it: enter \"auto\")", l.Name, l.Map, d.plan.Map)
	} else if !strings.EqualFold(l.Name, d.opt.Lead) {
		d.rep.note(fmt.Sprintf("the feed's lead bot is %s, not %s", l.Name, d.opt.Lead))
	}
	if len(d.fr.Party) == 0 {
		return errors.New("the first frame has an empty party")
	}
	d.observeInit()
	if d.opt.Tank != "" {
		if m, ok := d.memberByName(d.opt.Tank); ok {
			d.tank = m.GUID
		} else {
			d.rep.note(fmt.Sprintf("tank %s is not in the party of the feed; the tank will be taken from the first pull", d.opt.Tank))
		}
	}
	d.wantParty = max(len(d.opt.Bots)+1, 0)
	if len(d.opt.Bots) == 0 {
		d.wantParty = len(d.fr.Party)
	}
	d.resolveCCBot(ctx)
	d.lootOnAtStart(ctx)
	d.probeEntryTargets(ctx, fromLeg)
	if err := d.preflight(ctx); err != nil {
		return err
	}
	return d.startAnywhere(ctx, fromLeg)
}

// adoptBinding answers a bind refused as already-bound: the binding the account holds is reused when it is this run's own (the same lead
// bot, and the same number of bots when the run names them; the names are checked against the first frame), else the run is refused
// with a message that names the binding in the way. refusal is the bind's own refusal.
func (d *RemoteDriver) adoptBinding(ctx context.Context, refusal *tbm.Err) error {
	sr, err := d.g.Command(ctx, "commander status")
	if err != nil {
		return fmt.Errorf("commander bind refused: %s; commander status: %w", describeBindErr(refusal), err)
	}
	if sr.Kind != tbm.KindAck || sr.Ack == nil {
		return fmt.Errorf("commander bind refused: %s (and commander status gave no answer to compare it with)", describeBindErr(refusal))
	}
	lead, n, want := strings.TrimSpace(sr.Ack.Executor), sr.Ack.Count, 1+len(d.opt.Bots)
	if !strings.EqualFold(lead, d.opt.Lead) || (len(d.opt.Bots) > 0 && n != want) {
		return fmt.Errorf("the commander is already bound to another group: lead bot %s with %d bots, not %s%s; unbind it first (commander op unbind)", lead, n, d.opt.Lead, botsSuffix(d.opt.Bots))
	}
	d.rep.note(fmt.Sprintf("commander bind: already bound to %s with %d bots, the same group; reusing the binding", lead, n))
	return nil
}

// checkAdopted checks the first frame of a reused binding: the lead bot leads it and the named bots are in its party.
func (d *RemoteDriver) checkAdopted() error {
	if l, ok := d.fr.LeadRec(); !ok || !strings.EqualFold(l.Name, d.opt.Lead) {
		return fmt.Errorf("the commander is already bound to another group: its lead guid is %s, not %s; unbind it first (commander op unbind)", d.fr.Lead, d.opt.Lead)
	}
	for _, b := range d.opt.Bots {
		if _, ok := d.memberByName(b); !ok {
			return fmt.Errorf("the commander is already bound to a group that does not hold %s: unbind it first (commander op unbind)", b)
		}
	}
	return nil
}

func botsSuffix(bots []string) string {
	if len(bots) == 0 {
		return ""
	}
	return " with " + strings.Join(bots, ", ")
}

// describeBindErr explains the refusals of `.bot commander bind` (player-controls.md section 1a).
func describeBindErr(e *tbm.Err) string {
	if e == nil {
		return "no reason"
	}
	hint := map[string]string{
		"disabled":         "AiPlayerbot.RemoteCommander is off on the server (needs a TortoiseBots build with the remote commander and the switch on)",
		"already-bound":    "this account already commands a party: unbind it first",
		"no-bot":           "not an online bot this account controls",
		"hired":            "another player's hired companion cannot be bound (your own can, from TortoiseBots #935: an older build refuses them too)",
		"group-raid":       "the lead bot is in a raid",
		"group-has-player": "a player is in the lead bot's party",
		"group-bound":      "another commander holds that party",
		"no-members":       "a lead bot without a party needs at least one more bot",
		"group-too-large":  "more than five bots",
		"bot-bound":        "a bot is still in another commander's group",
		"member-grouped":   "a named bot is in some other group",
		"invite-failed":    "a named bot could not be invited",
		"party-unnamed":    "the party holds a bot the bind did not name: name every bot in it (bots), or the bind would drop the party's instance bind",
		"lead-bound":       "the lead bot holds a saved instance of a different instance than the group's",
	}[e.Code]
	if hint != "" {
		return fmt.Sprintf("%s (%s): %s", e.Code, e.Message, hint)
	}
	return fmt.Sprintf("%s (%s)", e.Code, e.Message)
}

func (d *RemoteDriver) countObjectSteps(fromLeg int) {
	n := 0
	for li := fromLeg; li < len(d.plan.Legs); li++ {
		l := d.plan.Legs[li]
		for _, st := range append(slices.Clone(l.Steps), l.AfterBoss...) {
			if objectStepKind(st) != "" {
				n++
			}
		}
	}
	d.remote(func(r *RemoteReport) { r.ObjectStepsAhead = n })
	if n > 0 {
		d.rep.note(d.objectStepsNote(n, fromLeg))
	}
}

func (d *RemoteDriver) waitFirstFrame(ctx context.Context) error {
	deadline := d.clk.Now().Add(d.opt.FeedWait)
	for {
		d.look()
		if d.haveFr {
			return nil
		}
		if err := d.endedByModule(); err != nil {
			return err
		}
		if d.clk.Now().After(deadline) {
			return fmt.Errorf("no TBM:VIEW frame came in %.0f s after the bind: the view is off, or the server build has no perception feed (TortoiseBots phase 2)", d.opt.FeedWait.Seconds())
		}
		if err := d.clk.Sleep(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// unbind ends the binding with its own context, so that a stopped run unbinds too. keep is a run that FAILED (not stopped, not done):
// the bots are stood still, but the binding stays, so that the next run's bind finds the same group and re-enters the same instance
// (an unbind returns the hires to the commander's raid, and the bind after it makes a new group, so a new instance: ZF-2 lost its
// half-cleared instance that way and the 5 new instances an hour of the account with it). on_end logout still unbinds.
func (d *RemoteDriver) unbind(keep bool) {
	d.wd.stop() // the closing fight handling and the unbind wait through the same ticks: the watchdog has had its say
	if !d.bound {
		return
	}
	d.finishFights() // before unbinding is set: a binding the module ended is noticed
	d.unbinding = true
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), d.opt.UnbindWait+5*time.Second)
	defer cancel()
	if d.opt.OnEnd != OnEndNone {
		// Once the commander lets go the bots follow their master, the agent, wherever it is: stand them still first.
		d.endStay(ctx)
	}
	if keep && d.opt.OnEnd != OnEndLogout {
		why := "the run failed"
		if d.paused.Load() {
			why = "the run was paused"
		}
		d.remote(func(rr *RemoteReport) {
			rr.UnbindVia = "kept (" + why + "; a resume reuses the group and its instance)"
		})
		d.rep.note("commander unbind: not sent, " + why + "; the binding is kept so that a resume re-enters the same instance (dungeon_stop or a finished run unbinds)")
		return
	}
	r, err := d.g.Command(ctx, "commander unbind")
	via := ""
	switch {
	case err != nil:
		via = "error: " + err.Error()
	case r.Kind == tbm.KindAck:
		via = "ack"
	case r.Kind == tbm.KindErr && r.Err != nil && r.Err.Code == "not-bound":
		via = "already unbound"
	case r.Kind == tbm.KindErr && r.Err != nil:
		via = "refused: " + r.Err.Code
	default:
		via = "no answer"
	}
	d.remote(func(rr *RemoteReport) {
		rr.Unbound, rr.UnbindVia = err == nil && (r.Kind == tbm.KindAck || via == "already unbound"), via
	})
	d.rep.note("commander unbind: " + via)
	d.bound = false
	if d.opt.OnEnd == OnEndLogout {
		lctx, lcancel := context.WithTimeout(context.WithoutCancel(context.Background()), 30*time.Second)
		defer lcancel()
		d.endLogout(lctx)
	}
}

func (d *RemoteDriver) setStats0(s tbm.ViewStats) {
	d.statsMu.Lock()
	d.stats0 = s
	d.statsMu.Unlock()
}

// look reads the feed. It never waits.
func (d *RemoteDriver) look() {
	defer d.traceLook()
	fr, n, ok := d.g.Frame()
	if !ok {
		return
	}
	if !d.haveFr || n != d.n {
		d.fr, d.n, d.haveFr = fr, n, true
		d.noteLedger()
		d.noteFleeing()
		d.sampleBatch()
		d.ptObserve()
		if d.stalled {
			d.remote(func(r *RemoteReport) { r.FeedStalls++ })
		}
		d.nAt, d.stalled, d.viewResent = d.clk.Now(), false, false
	} else if d.clk.Now().Sub(d.nAt) > d.opt.FeedStale {
		d.stalled = true
	}
}

// endedByModule is the error for a binding the module ended on its own.
func (d *RemoteDriver) endedByModule() error {
	if d.unbinding {
		return nil
	}
	if ends := d.g.BindingEnds(); len(ends) > d.endsAtBind {
		reason := ends[len(ends)-1].Reason
		d.remote(func(r *RemoteReport) { r.EndedByModule = reason })
		return fmt.Errorf("%w: %s", errBindingLost, reason)
	}
	return nil
}

// tick waits dur, then looks: the feed, the binding and the party's deaths (a wipe ends the wait with errWipe).
func (d *RemoteDriver) tick(ctx context.Context, dur time.Duration) error {
	if err := d.clk.Sleep(ctx, dur); err != nil {
		return err
	}
	if err := d.check(); err != nil {
		return err
	}
	d.resendViewIfSilent(ctx)
	return d.wd.watch(ctx)
}

func (d *RemoteDriver) check() error {
	d.look()
	if err := d.endedByModule(); err != nil {
		return err
	}
	if d.haveFr && d.clk.Now().Sub(d.nAt) > d.opt.FeedLost {
		return fmt.Errorf("%w: no new frame for %.0f s", errFeedLost, d.opt.FeedLost.Seconds())
	}
	if d.countDeaths() {
		return fmt.Errorf("%w: the whole party is dead", errWipe)
	}
	if n, all := d.partyDown(); n > 0 {
		return fmt.Errorf("%w: %w: %d of %d bots are dead or outside the dungeon", errWipe, errPartyDown, n, all)
	}
	return d.partyIntact()
}

// partyDown reports, once, that three bots or more are dead inside the dungeon: the leg stops there as at a wipe (ZF-1: after
// four deaths the driver walked the lone tank back into the pack and it died too). With RecoverWipe the dead are revived and the step
// goes on with the whole party; without, the run ends. It is said once until fewer than three are dead, so that what follows the stop
// (the revive, the end's fight) is not stopped again.
// errPartyDown marks a wipe that is a stop at three dead bots, not the whole party (Report.PartyDowns, not Wipes).
var errPartyDown = errors.New("party down")

// partyDownDead is how many dead bots stop the leg (the owner, ZF-1: "on a wipe or 3 dead stop the leg").
const partyDownDead = 3

func (d *RemoteDriver) partyDown() (dead, all int) {
	if d.curLeg < 0 || !d.haveFr {
		return 0, 0
	}
	all = len(d.fr.Party)
	// Dead in the instance, and alive on another map: a party that released to a graveyard outside and was spirit-healed there (live,
	// Antu'sul: all alive on map 1, the in-instance count never fired) is down as much as a dead one.
	dead = len(d.deadMembers()) + d.outsideAlive()
	if dead < d.tx.PartyDownDead {
		d.downSaid = false
		return 0, all
	}
	if d.downSaid {
		return 0, all
	}
	d.downSaid = true
	return dead, all
}

// outsideGrace is how long a living member must stand on another map than the plan's before it counts as outside (a bot that is taking
// the portal, or arriving after a summon, is for a moment on the old map).
const outsideGrace = 8 * time.Second

// outsideAlive counts the living members that have stood on another map than the plan's for outsideGrace, inside a leg.
func (d *RemoteDriver) outsideAlive() int {
	n := 0
	now := d.clk.Now()
	if d.outSince == nil {
		d.outSince = map[packet.GUID]time.Time{}
	}
	for _, p := range d.fr.Party {
		if p.Dead || p.Map == d.plan.Map || d.curLeg < 0 {
			delete(d.outSince, p.GUID)
			continue
		}
		since, ok := d.outSince[p.GUID]
		if !ok {
			d.outSince[p.GUID] = now
			continue
		}
		if now.Sub(since) >= outsideGrace {
			if p.GUID == d.fr.Lead {
				n += d.tx.PartyDownDead // a lead outside cannot walk the party anywhere: it is party down by itself (live, zum2-s1: the walk was refused 8 times)
			} else {
				n++
			}
		}
	}
	return n
}

func (d *RemoteDriver) observeInit() {
	for _, p := range d.fr.Party {
		d.prevDead[p.GUID] = p.Dead
	}
	d.deadSeen = true
}

// countDeaths counts each death once and reports whether the whole party is dead.
func (d *RemoteDriver) countDeaths() bool {
	if !d.haveFr {
		return false
	}
	if !d.deadSeen {
		d.observeInit()
	}
	for _, p := range d.fr.Party {
		d.observeDeadly(p, p.Dead && !d.prevDead[p.GUID])
		if p.Dead && !d.prevDead[p.GUID] {
			d.rep.update(func(r *Report) {
				r.Deaths = append(r.Deaths, Death{Name: p.Name, Leg: d.curLeg, At: d.clk.Now()})
				if n := len(r.Legs); n > 0 && d.curLeg >= 0 {
					r.Legs[n-1].Deaths++
				}
			})
			d.tr.Event("death", map[string]any{"name": p.Name, "a": guidText(p.GUID), "leg": d.curLeg})
		}
		d.prevDead[p.GUID] = p.Dead
	}
	d.deadlyFrame()
	return d.fr.AllDead()
}

func (d *RemoteDriver) memberByName(name string) (tbm.PartyRec, bool) {
	for _, p := range d.fr.Party {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return tbm.PartyRec{}, false
}

func (d *RemoteDriver) memberByGUID(g packet.GUID) (tbm.PartyRec, bool) {
	for _, p := range d.fr.Party {
		if p.GUID == g {
			return p, true
		}
	}
	return tbm.PartyRec{}, false
}

func (d *RemoteDriver) startLeg(ctx context.Context, i int, next Action) error {
	d.curLeg = i
	name := d.plan.Legs[i].Name
	d.rep.update(func(r *Report) {
		r.Legs = append(r.Legs, LegReport{Leg: i, Name: name, Started: d.clk.Now(), Result: "running"})
	})
	d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: start", i, name), map[string]any{"leg": i, "name": name, "phase": "start", "mode": "remote"})
	// companions_loot false: off from here to the leg's end; a leg that cannot have it does not start (before the walk, the rest gate, anything).
	if err := d.legLootOff(ctx, i); err != nil {
		return err
	}
	// Every few legs the bots get `.bot maintenance` again (remote_maint.go), before the gate so that the gate sees the full bags.
	if err := d.maintLegStart(ctx, i); err != nil {
		return err
	}
	// The gate at a leg's start waits for health and mana but not for the followers: they catch up on the walk, and the gate before a
	// pull gathers them. When a long walk comes first, the party may rest on the way (walkAhead).
	return d.restGateWalk(ctx, false, d.walkAhead(next))
}

func (d *RemoteDriver) endLeg(result string) {
	i := d.curLeg
	if i < 0 {
		return
	}
	d.maintLegEnd()
	d.legAoEOn()
	d.legLootOn()
	d.curLeg = -1
	var l LegReport
	d.rep.update(func(r *Report) {
		if n := len(r.Legs); n > 0 && r.Legs[n-1].Leg == i {
			x := &r.Legs[n-1]
			x.Seconds, x.Result = d.clk.Now().Sub(x.Started).Seconds(), result
			l = *x
		}
	})
	d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: %s in %.0f s", i, l.Name, result, l.Seconds), map[string]any{
		"leg": i, "name": l.Name, "phase": "end", "result": result, "seconds": l.Seconds, "deaths": l.Deaths,
		"pulls": l.Pulls, "pull_retries": l.PullRetries, "rest_seconds": l.RestSeconds, "mode": "remote"})
}

func (d *RemoteDriver) legStat(f func(*LegReport)) {
	d.rep.update(func(r *Report) {
		if n := len(r.Legs); n > 0 {
			f(&r.Legs[n-1])
		}
	})
}

// order sends `.bot cmd <verb> <args...>` and counts it. A refusal is recorded in the report; an answer that says the binding is
// gone (not-bound, disabled) ends the run. lead-unavailable is waited out up to LeadWait. The reply is returned for the caller to
// judge (an ERR is not an error here).
func (d *RemoteDriver) order(ctx context.Context, verb string, args ...string) (tbm.Reply, error) {
	cmd := strings.Join(append([]string{"cmd", verb}, args...), " ")
	if verb == "move" {
		defer func() { d.lastMove = d.clk.Now() }()
	}
	switch verb {
	case "pull", "pullback", "follow", "stay", "move", "rest", "revive":
		d.tf.sentGUID = 0 // the bots were told something else: an attack order for the same mob is a new order (attackAllowed)
	}
	deadline := d.clk.Now().Add(d.opt.LeadWait)
	for {
		sentAt := d.clk.Now()
		r, err := d.g.Command(ctx, cmd)
		if err != nil {
			return r, fmt.Errorf("cmd %s: %w", verb, err)
		}
		d.traceOrder(verb, cmd, r, d.clk.Now().Sub(sentAt).Milliseconds())
		d.wd.ordered(cmd + " -> " + replyText(r))
		if r.Kind == tbm.KindAck {
			d.wd.acked(cmd)
		}
		d.remote(func(rr *RemoteReport) {
			rr.OrdersSent++
			rr.Orders[verb]++
			switch r.Kind {
			case tbm.KindErr:
				rr.OrdersRefused++
				if r.Err != nil {
					rr.Refused[verb+":"+r.Err.Code]++
				}
			case tbm.KindTimeout:
				rr.OrdersUnanswered++
			}
		})
		if r.Kind != tbm.KindErr || r.Err == nil {
			if verb == "move" && r.Kind == tbm.KindAck {
				if at, ok := moveArgsPoint(args); ok {
					d.moveTo, d.haveMoveTo = at, true
				}
			}
			return r, nil
		}
		d.recordRefusal(Refusal{Intent: "cmd " + verb, Code: r.Err.Code, Message: r.Err.Message})
		switch r.Err.Code {
		case "not-bound", "disabled":
			return r, fmt.Errorf("%w: cmd %s answered %s (%s)", errBindingLost, verb, r.Err.Code, r.Err.Message)
		case "lead-unavailable":
			if d.clk.Now().Before(deadline) {
				if err := d.tick(ctx, d.cfg.Poll*2); err != nil {
					return r, err
				}
				continue
			}
			return r, fmt.Errorf("cmd %s: the lead bot was unavailable for %.0f s (%s)", verb, d.opt.LeadWait.Seconds(), r.Err.Message)
		}
		return r, nil
	}
}

func (d *RemoteDriver) recordRefusal(r Refusal) {
	d.rep.update(func(rep *Report) {
		for i := range rep.Refusals {
			x := &rep.Refusals[i]
			if x.Intent == r.Intent && x.Code == r.Code && x.Message == r.Message && x.Leg == d.curLeg {
				x.Count++
				return
			}
		}
		if len(rep.Refusals) < maxRefusalKinds {
			rep.Refusals = append(rep.Refusals, RefusalReport{Intent: r.Intent, Code: r.Code, Message: r.Message, Leg: d.curLeg, Count: 1, First: d.clk.Now()})
		}
	})
}

// refused turns an ERR reply into the Refusal the shared classifier reads.
func refusedOf(r tbm.Reply) (Refusal, bool) {
	if r.Kind != tbm.KindErr || r.Err == nil {
		return Refusal{}, false
	}
	return Refusal{Intent: r.Err.Intent, Code: r.Err.Code, Message: r.Err.Message}, true
}

// ---- walking ----------------------------------------------------------------------------------------------------------

// moveArg formats the coordinates of a `.bot cmd move`.
func moveArgs(p Point) []string {
	return []string{tbm.CoordArg(p.X), tbm.CoordArg(p.Y), tbm.CoordArg(p.Z)}
}

// moveArgsPoint reads the point back out of the arguments of a `cmd move` (moveArgs, with or without the direct flag).
func moveArgsPoint(args []string) (Point, bool) {
	if len(args) < 3 {
		return Point{}, false
	}
	var v [3]float32
	for i := range v {
		f, err := strconv.ParseFloat(args[i], 32)
		if err != nil {
			return Point{}, false
		}
		v[i] = float32(f)
	}
	return Point{v[0], v[1], v[2]}, true
}

// waypoint is the point to send the lead bot to on its way from `from` to `to`: `to` itself when within MoveLeg, else a point on
// the navmesh path (when the game plans one), else on the straight line. A point cut out of a segment is put on the navmesh floor
// under it (onFloor), among the floors in the height band of the part of the path being cut: the server drops a floor more than 3 yd
// above the asked point, and in a sloping tunnel the segment's own height runs under the floor.
func (d *RemoteDriver) waypoint(ctx context.Context, from, to Point, leg float32) Point {
	p, _ := d.waypointSrc(ctx, from, to, leg)
	return p
}

// waypointSrc is waypoint with the source of the point: real is true when it is `to` itself or a point of a path the game's navmesh planned,
// false for the straight-line fallback (no planner, no tiles, a planner error): a module refusal of a straight-line point says nothing about
// the target (the enter phase's unreachable portal, remote_portalgm.go).
func (d *RemoteDriver) waypointSrc(ctx context.Context, from, to Point, leg float32) (p Point, real bool) {
	if dist3(from, to) <= float64(leg) {
		return to, true
	}
	if pl, ok := d.g.(PathPlanner); ok {
		if pts, err := pl.PlanPath(ctx, d.stepMap(), from, to); err == nil && len(pts) >= 2 {
			along := alongPath
			if boxes := d.stepAvoid(); len(boxes) > 0 {
				along = func(pts []Point, from Point, leg float32) (Point, bool) { return alongPathAvoid(pts, from, leg, boxes) }
			}
			if p, ok := along(pts, from, leg); ok {
				lo, hi := from.Z, from.Z
				for i := 1; i < len(pts); i++ {
					lo, hi = min(lo, pts[i].Z), max(hi, pts[i].Z)
					if dist3(from, pts[i]) > float64(leg) {
						break
					}
				}
				return d.onFloor(ctx, p, lo, hi), true
			}
		}
	}
	return d.onFloor(ctx, alongLine(from, to, float64(leg)), min(from.Z, to.Z), max(from.Z, to.Z)), false
}

// stepMap is the map the lead bot walks on.
func (d *RemoteDriver) stepMap() uint32 {
	if d.outside != nil {
		return *d.outside
	}
	return d.plan.Map
}

// floorBand is how far (yd) outside the heights of the path being cut a navmesh floor may lie and still be the floor under a step: a floor
// farther off is another level (a deck below, a bridge above) that the path does not go to.
const floorBand = 3

// floorWindow is how far (yd) from a refused step's height a floor counts as the one it missed (the live refusals were 3.1 to 5.5 under).
const floorWindow = 8

// nearestFloor is the floor of zs nearest z within [lo, hi]; on a tie the one above.
func nearestFloor(zs []float32, z, lo, hi float32) (float32, bool) {
	best, found := float32(0), false
	for _, f := range zs {
		if f < lo || f > hi {
			continue
		}
		df, db := math.Abs(float64(f-z)), math.Abs(float64(best-z))
		if !found || df < db-0.01 || (df < db+0.01 && f > best) {
			best, found = f, true
		}
	}
	return best, found
}

// StepOnFloor is p with the height of the navmesh floor at its x, y nearest p's height among those between lo-floorBand and hi+floorBand
// (the heights of the path being cut); p itself when fl lists no floors (no planner, no tile) or none lies in the band.
func StepOnFloor(ctx context.Context, fl FloorLister, mapID uint32, p Point, lo, hi float32) Point {
	zs, ok := fl.FloorsAt(ctx, mapID, p.X, p.Y, p.Z)
	if !ok {
		return p
	}
	if z, ok := nearestFloor(zs, p.Z, lo-floorBand, hi+floorBand); ok {
		p.Z = z
	}
	return p
}

// onFloor is StepOnFloor for the game of the run, when it lists floors at all.
func (d *RemoteDriver) onFloor(ctx context.Context, p Point, lo, hi float32) Point {
	if fl, ok := d.g.(FloorLister); ok {
		return StepOnFloor(ctx, fl, d.stepMap(), p, lo, hi)
	}
	return p
}

// refusedStepFix is the point to send again for a step the server answered `unreachable`, same x and y: at the height of the floor the
// step missed by more than 2.5 yd (the server accepts a floor up to 3 yd above the asked point), or, when the game knows no floor there at
// all, raised floorRaise yd. false when the step is on a floor already: it is refused for another reason and only a shorter leg may help.
func (d *RemoteDriver) refusedStepFix(ctx context.Context, sent Point) (Point, bool) {
	fl, ok := d.g.(FloorLister)
	if !ok {
		return Point{sent.X, sent.Y, sent.Z + floorRaise}, true
	}
	zs, ok := fl.FloorsAt(ctx, d.stepMap(), sent.X, sent.Y, sent.Z)
	if !ok || len(zs) == 0 {
		return Point{sent.X, sent.Y, sent.Z + floorRaise}, true
	}
	z, ok := nearestFloor(zs, sent.Z, sent.Z-floorWindow, sent.Z+floorWindow)
	if !ok || math.Abs(float64(z-sent.Z)) <= 2.5 {
		return Point{}, false
	}
	return Point{sent.X, sent.Y, z}, true
}

const floorRaise = 3

// alongPath is the farthest point of the path within leg yards of from, following the corners in order.
func alongPath(pts []Point, from Point, leg float32) (Point, bool) {
	best := from
	found := false
	for i := 1; i < len(pts); i++ {
		a := pts[i-1]
		if i == 1 {
			a = from
		}
		b := pts[i]
		if dist3(from, b) <= float64(leg) {
			best, found = b, true
			continue
		}
		// The corner is beyond: cut the segment where it leaves the circle.
		seg := dist3(a, b)
		if seg == 0 {
			break
		}
		lo, hi := 0.0, 1.0
		for k := 0; k < 24; k++ {
			mid := (lo + hi) / 2
			p := lerp(a, b, mid)
			if dist3(from, p) <= float64(leg) {
				lo = mid
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			best, found = lerp(a, b, lo), true
		}
		break
	}
	return best, found
}

func lerp(a, b Point, t float64) Point {
	return Point{X: float32(float64(a.X) + (float64(b.X)-float64(a.X))*t), Y: float32(float64(a.Y) + (float64(b.Y)-float64(a.Y))*t), Z: float32(float64(a.Z) + (float64(b.Z)-float64(a.Z))*t)}
}

func alongLine(from, to Point, leg float64) Point {
	d := dist3(from, to)
	if d <= leg || d == 0 {
		return to
	}
	return lerp(from, to, leg/d)
}

// moveTimeout bounds one leg of a walk: bots run about 7 yd/s, and a path is longer than the straight line.
func moveTimeout(dist float64) time.Duration {
	return time.Duration(30+dist/3) * time.Second
}

// doWalk sends the lead bot to p and waits for it to get there, in legs of at most MoveLeg yards. The lead bot's position comes
// from the feed. A pack that aggroes on the way is fought (walkAggro) and the walk goes on; a lead bot that gains no ground in
// MoveStall is told again; a refused move is tried again with a shorter leg. WalkAttempts bounds the refusals of one walk. Inside the
// dungeon the walk is careful (remote_gather.go): hops of at most CarefulHop yards, the party gathered before each, and a hostile near the
// next hop pulled before it.
func (d *RemoteDriver) doWalk(ctx context.Context, p Point) error {
	return d.walk(ctx, p, false)
}

// walkArrive is doWalk with its own arrival distance.
func (d *RemoteDriver) walkArrive(ctx context.Context, p Point, arriveAt float32) error {
	return d.walkTo(ctx, p, false, arriveAt)
}

// directMoveOrder is the order a direct step sends: `cmd move x y z direct` (TortoiseBots), a straight-line walk for a gap the navmesh
// does not bridge. Short steps only; the module checks the limits and the ground and refuses with too-far, no-ground or no-los.
func directMoveArgs(p Point) []string { return append(moveArgs(p), "direct") }

// errDirectUnsupported is a direct step on a server that does not know the flag: it answers `invalid` (the usage line), as it does to
// any `cmd move` it cannot read.
func errDirectUnsupported(p Point, ref Refusal) error {
	return fmt.Errorf("direct step to %.1f %.1f %.1f: the server refused `cmd move ... direct` (%s: %s); it needs a TortoiseBots with the direct flag (cmd move <x> <y> <z> direct)", p.X, p.Y, p.Z, ref.Code, ref.Message)
}

// walk is doWalk, or with direct set a direct step to p: one `cmd move ... direct` (no legs, no planning), repeated when the module
// answers too-far (the lead bot is out of the step's limits, or a follower has no way to the lead bot's spot yet: TortoiseBots #948) and
// waited for like a walk. A refusal of `invalid` is a server without the flag; no-ground is the plan's point being off the ground and no-los the lead bot not seeing it (the step would cut through a wall).
// A direct step is never split into the careful walk's hops (it bridges a gap the navmesh does not); the party still gathers before it
// and a hostile near its line is still pulled first.
func (d *RemoteDriver) walk(ctx context.Context, p Point, direct bool) error {
	return d.walkTo(ctx, p, direct, d.opt.MoveArrive)
}

// directArrive is how close (yd) the lead bot must be to a direct step's point for the step to count as done, or as not needed.
const directArrive = 1.5

func (d *RemoteDriver) walkTo(ctx context.Context, p Point, direct bool, arriveAt float32) error {
	defer d.wd.enter("walk", fmt.Sprintf("to %.0f %.0f %.0f", p.X, p.Y, p.Z), func(ctx context.Context) (string, error) {
		if direct {
			// A plain move would plan the navmesh path again, which is the very thing a direct step goes around.
			return "a direct step: no plain move sent (the walk sends the step again itself when the lead bot stalls)", nil
		}
		return d.wdReplanWalk(ctx, p)
	})()
	if direct {
		// A direct step is short on purpose (the plan's 4 yd hop under the overhang): the usual 5 yd "arrived" would count it as done
		// without sending it (live, uldaman hires-2 17:20: the lip was 3.97 yd from the first hop, so only the 12 yd hop was sent and
		// the module refused it no-los).
		arriveAt = min(arriveAt, directArrive)
	}
	refusals, stalls, aggros, aheads := 0, 0, 0, 0
	var resend *Point // the step to send again, put on the floor, after the server called it unreachable
	fixed := false    // the unreachable step has had its resend (until a move is taken)
	leg := d.opt.MoveLeg
	var last string
	var meter walkMeter
	named, regathered, gaveUp := "", false, false // a bot the module's too-far named: brought at once, once per step; a gather that ran out
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.check(); err != nil {
			return err
		}
		if err := d.ensureAlive(ctx); err != nil {
			return err
		}
		if err := d.requireLiving(); err != nil {
			return err
		}
		here, ok := d.leadPos()
		if !ok {
			resend = nil
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			continue
		}
		if dist3(here, p) <= float64(arriveAt) {
			return nil
		}
		if d.fr.PartyInCombat() && len(d.fr.EngagedHostiles()) > 0 && !d.batchOpening() {
			resend = nil
			if aggros++; aggros > maxWalkAggros {
				return fmt.Errorf("walk to %.1f %.1f %.1f: the party was pulled into a fight %d times on the way", p.X, p.Y, p.Z, maxWalkAggros)
			}
			if err := d.walkAggro(ctx); err != nil {
				return err
			}
			meter = walkMeter{} // the fight is not a stop of the walk
			continue
		}
		if err := d.rejoinFar(ctx); err != nil {
			return err
		}
		careful := d.careful()
		hop := leg
		if careful {
			hop = min(leg, d.opt.CarefulHop)
		}
		wp := d.waypoint(ctx, here, p, hop)
		if resend != nil && !direct {
			wp, resend = *resend, nil
		}
		args := moveArgs(wp)
		if direct {
			wp, args = p, directMoveArgs(p) // one step, never hops
		}
		if careful {
			if h, ok := d.hostileAhead(here, wp); ok {
				if aheads++; aheads > d.tx.MaxAheadPulls {
					return fmt.Errorf("walk to %.1f %.1f %.1f: %d hostiles near the way were pulled and still more stand there", p.X, p.Y, p.Z, d.tx.MaxAheadPulls)
				}
				if err := d.pullAhead(ctx, h); err != nil {
					return err
				}
				meter = walkMeter{}
				continue
			}
			if err := d.gather(ctx, "the next hop"); err != nil {
				return err
			}
		}
		if direct && (!gaveUp || named != "") {
			// The module takes the step only with every follower on the lead bot (#948): the party stands on it first.
			fight, timedOut, err := d.gatherDirect(ctx, "a direct step", named)
			if err != nil {
				return err
			}
			named, gaveUp = "", gaveUp || timedOut
			if fight {
				continue // the loop's combat branch takes it
			}
		}
		// The module carries out no move shorter than about 6 yd (ZF-1 run 4: a last stretch of 5.2 yd was ignored five times and failed
		// the walk). Such a stretch is sent as a move to min_move+1 yd along the same line: the lead bot walks through the point and stops
		// just past it, and the arrival counts within that overshoot.
		tiny := 0.0
		if dd := dist3(here, wp); !direct && d.tx.MinMove > 0 && dd > 0.1 && dd < float64(d.tx.MinMove) && !d.overshootDeadly(ctx, here, wp, dd) {
			past := d.onFloor(ctx, lerp(here, wp, (float64(d.tx.MinMove)+1)/dd), min(here.Z, wp.Z), max(here.Z, wp.Z))
			args = moveArgs(past)
			tiny = float64(d.tx.MinMove) + 1 - dd
			d.remote(func(r *RemoteReport) { r.TinyMoves++ })
		}
		if err := d.moveGap(ctx); err != nil {
			return err
		}
		r, err := d.order(ctx, "move", args...)
		if err != nil {
			return err
		}
		if ref, bad := refusedOf(r); bad {
			last = ref.Code + ": " + ref.Message
			if direct {
				if ref.Code == "invalid" {
					return errDirectUnsupported(p, ref)
				}
				if bot := farBotName(ref.Message); ref.Code == "too-far" && bot != "" && !regathered {
					// A follower fell behind after the gather: bring that bot (follow, then summon), and send the step again.
					regathered, named = true, bot
					d.rep.note(fmt.Sprintf("direct step to %.1f %.1f %.1f: %s; regathering %s", p.X, p.Y, p.Z, last, bot))
					continue
				}
				if refusals++; refusals > d.cfg.WalkAttempts+d.opt.MoveRetries || ref.Code == "no-ground" || ref.Code == "no-los" {
					return fmt.Errorf("direct step to %.1f %.1f %.1f: the lead bot was refused (%s)", p.X, p.Y, p.Z, last)
				}
				if err := d.tick(ctx, d.cfg.WalkRetryDelay); err != nil {
					return err
				}
				continue
			}
			if ref.Code == "unreachable" {
				d.noteUnreachable("move", wp, fmt.Sprintf("walk to %.1f %.1f %.1f, leg of %.0f yd from %.1f %.1f %.1f", p.X, p.Y, p.Z, leg, here.X, here.Y, here.Z))
				// A step under its floor is the usual cause (the server drops a floor more than 3 yd above the asked point): the same
				// step on its floor goes first, and a refusal the resend cures is not counted. Never the plan's own end point.
				if sent, ok := moveArgsPoint(args); ok && !fixed && wp != p {
					if q, ok := d.refusedStepFix(ctx, sent); ok {
						fixed, resend = true, &q
						d.rep.note(fmt.Sprintf("walk to %.1f %.1f %.1f: step %.1f %.1f %.1f unreachable; sent again at z %.1f", p.X, p.Y, p.Z, sent.X, sent.Y, sent.Z, q.Z))
						if err := d.tick(ctx, d.cfg.WalkRetryDelay); err != nil {
							return err
						}
						continue
					}
				}
			}
			if refusals++; refusals > d.cfg.WalkAttempts+d.opt.MoveRetries {
				return fmt.Errorf("walk to %.1f %.1f %.1f: the lead bot was refused %d times (%s)", p.X, p.Y, p.Z, refusals, last)
			}
			if ref.Code == "too-far" || ref.Code == "off-mesh" || ref.Code == "unreachable" {
				leg = max(min(leg, hop)/2, 15) // a shorter step, actually: closer, and nearer the mesh
			}
			if err := d.tick(ctx, d.cfg.WalkRetryDelay); err != nil {
				return err
			}
			continue
		}
		fixed = false
		arrive := arriveAt
		if wp != p {
			arrive = max(arrive, 8)
		}
		if tiny > 0 {
			arrive = max(arrive, float32(tiny)+0.5)
		}
		ahead := wp != p && d.opt.LookAhead > 0
		res, err := d.waitArrival(ctx, wp, arrive, ahead, &meter)
		if err != nil {
			return err
		}
		switch res {
		case nearAt:
			// The lead bot is about to reach the waypoint: the next order goes out from here, while it is still running.
			d.remote(func(r *RemoteReport) { r.LookAheadMoves++ })
		case arrivedAt:
			if wp == p {
				if direct {
					// The next step (or the pull after the crossing) starts with the party up: wait for the followers now.
					if _, _, err := d.gatherDirect(ctx, "the next step", ""); err != nil {
						return err
					}
				}
				return nil
			}
		case stalledAt:
			if stalls++; stalls > d.opt.MoveRetries {
				return fmt.Errorf("walk to %.1f %.1f %.1f: the lead bot gained no ground %d times (stuck at %.1f %.1f %.1f)", p.X, p.Y, p.Z, stalls, here.X, here.Y, here.Z)
			}
			d.remote(func(r *RemoteReport) { r.MoveStalls++ })
			leg = max(leg/2, 30)
		case fightAt:
			// the loop's combat branch takes it
		}
	}
}

type arrival int

const (
	arrivedAt arrival = iota
	stalledAt
	fightAt
	nearAt // with look-ahead: close enough to the waypoint for the next order
)

// walkSpeed is the speed (yd/s) a lead bot is taken to run at until the feed has shown it.
const walkSpeed = 6.6

// walkMeter watches the lead bot during a walk: how fast it runs, and the times it stood still after it had started (a stop in the
// middle of the walk), for the report.
type walkMeter struct {
	prev    Point
	prevAt  time.Time
	have    bool
	moved   bool    // the bot has been seen moving in this walk
	stopped bool    // the last look found it standing: one stop, however long
	speed   float64 // yd/s, as of the last look that found it moving
}

// look takes the bot's position at now.
func (m *walkMeter) look(d *RemoteDriver, here Point, now time.Time) {
	if !m.have {
		m.prev, m.prevAt, m.have = here, now, true
		return
	}
	dt := now.Sub(m.prevAt).Seconds()
	if dt <= 0 {
		return
	}
	v := dist3(here, m.prev) / dt
	m.prev, m.prevAt = here, now
	if v > 0.5 {
		m.moved, m.stopped = true, false
		m.speed = min(max(v, 3), 12)
		return
	}
	if !m.moved {
		return
	}
	d.remote(func(r *RemoteReport) {
		r.WalkStoppedS += dt
		if !m.stopped {
			r.WalkStops++
		}
	})
	m.stopped = true
}

// near says whether the bot at here is close enough to wp for the next order: within LookAhead yards or LookAheadETA of running.
func (m *walkMeter) near(d *RemoteDriver, here, wp Point) bool {
	dist := dist3(here, wp)
	if dist <= float64(d.opt.LookAhead) {
		return true
	}
	v := m.speed
	if v <= 0 {
		v = walkSpeed
	}
	return dist/v <= d.opt.LookAheadETA.Seconds()
}

// moveGap keeps two `cmd move` orders MoveGap apart.
func (d *RemoteDriver) moveGap(ctx context.Context) error {
	if d.lastMove.IsZero() {
		return nil
	}
	if left := d.opt.MoveGap - d.clk.Now().Sub(d.lastMove); left > 0 {
		return d.tick(ctx, left)
	}
	return nil
}

// waitArrival waits for the lead bot to come within arrive yards of wp. It ends early when the party is in a fight, when the
// lead bot gained no ground in MoveStall, or at the leg's timeout (reported as a stall). With ahead it also ends as soon as the bot
// is near wp (nearAt), so that the caller can send the next order before the bot stops.
func (d *RemoteDriver) waitArrival(ctx context.Context, wp Point, arrive float32, ahead bool, meter *walkMeter) (arrival, error) {
	start, _ := d.leadPos()
	began := d.clk.Now()
	limit := moveTimeout(dist3(start, wp))
	winAt, winPos := began, start
	for {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return 0, err
		}
		now := d.clk.Now()
		here, ok := d.leadPos()
		if ok && !d.stalled {
			meter.look(d, here, now)
		}
		if ok && dist3(here, wp) <= float64(arrive) {
			return arrivedAt, nil
		}
		if len(d.fr.EngagedHostiles()) > 0 && d.fr.PartyInCombat() {
			return fightAt, nil
		}
		if ahead && ok && !d.stalled && meter.near(d, here, wp) {
			return nearAt, nil
		}
		if d.stalled {
			winAt = now // a quiet feed proves nothing about the lead bot
			continue
		}
		if ok && dist3(here, winPos) > 2 {
			winAt, winPos = now, here
		}
		if now.Sub(winAt) >= d.opt.MoveStall || now.Sub(began) > limit {
			return stalledAt, nil
		}
	}
}

// ensureAlive brings a dead lead bot back before it is asked to walk: a revive order (owner or GM), then the wait for it to run
// back to its corpse and revive.
func (d *RemoteDriver) ensureAlive(ctx context.Context) error {
	l, ok := d.fr.LeadRec()
	if !ok || !l.Dead {
		return nil
	}
	return d.reviveDead(ctx, "the lead bot is dead")
}

// reviveDead sends a revive order for every dead bot (the lead bot first) and waits for them to show alive. A member that stays
// dead after two orders fails the run.
func (d *RemoteDriver) reviveDead(ctx context.Context, why string) error {
	defer d.wd.hold()() // the waits and the corpse runs have limits of their own
	if err := d.waitForRez(ctx, why); err != nil {
		return err
	}
	deadline := d.clk.Now().Add(d.cfg.WipeRecoverTimeout)
	for {
		dead := d.deadMembers()
		if len(dead) == 0 {
			if len(d.gmRevived) > 0 || len(d.offSince) > 0 || len(d.offMapMembers()) > 0 {
				return d.bringPartyIn(ctx) // the lead into the instance first, the others to it; every member on the plan's map
			}
			return nil
		}
		if d.clk.Now().After(deadline) {
			return fmt.Errorf("%s and still %d bots dead after %.0f s", why, len(dead), d.cfg.WipeRecoverTimeout.Seconds())
		}
		for _, m := range dead {
			if last, ok := d.lastRevive[m.GUID]; ok && d.clk.Now().Sub(last) < 20*time.Second {
				continue
			}
			// A bot that released to a graveyard outside the dungeon (another map than the plan's) does not come back by `cmd revive`: the
			// module acknowledges it and the ghost stays out (live 2026-10-15, Velratha group 9 wipe: "still dead after 3 revive orders").
			// A GM account revives it where it is (`.revive`; the next rejoin summons it into the instance); any other account leaves it to
			// run back through the entrance, for as long as the recovery's own limit.
			if m.Map != d.plan.Map && d.revived[m.GUID] >= 1 {
				if handled, err := d.reviveOffMap(ctx, m); err != nil {
					return err
				} else if handled {
					continue
				}
			}
			if d.revived[m.GUID] >= 3 {
				return fmt.Errorf("%s is still dead after %d revive orders", m.Name, d.revived[m.GUID])
			}
			d.revived[m.GUID]++
			d.lastRevive[m.GUID] = d.clk.Now()
			d.remote(func(r *RemoteReport) { r.Revives++ })
			r, err := d.order(ctx, "revive", m.Name)
			if err != nil {
				return err
			}
			if ref, bad := refusedOf(r); bad && ref.Code != "not-dead" {
				return fmt.Errorf("%s is dead and the revive order was refused: %s (%s)", m.Name, ref.Code, ref.Message)
			}
		}
		if err := d.tick(ctx, d.cfg.Poll*3); err != nil && !errors.Is(err, errWipe) {
			return err
		}
	}
}

// resurrectors are the living bots out of combat that may raise a dead one: the named healer, else any bot that uses mana (the feed
// carries no class, so a mage counts; the wait is bounded and a mage's absence of a resurrection costs RezWait at most). The module's own
// AI raises a dead party member out of combat (priest Resurrection, paladin Redemption, shaman Ancestral Spirit; the commander has no
// cast order, `cmd` verbs are listed in tbm.CmdVerbs), so the driver only leaves it the time. A caster counts only with a dead body within
// RezReach yards of it (rez_reach_yd; a corpse farther off is released at once), or on the same map as it.
func (d *RemoteDriver) resurrectors() []tbm.PartyRec {
	var out []tbm.PartyRec
	dead := d.deadMembers()
	for _, p := range d.fr.Party {
		if p.Dead || !p.UsesMana() || p.InCombat() {
			continue
		}
		if d.opt.Healer != "" && !strings.EqualFold(d.opt.Healer, p.Name) {
			continue
		}
		if !slices.ContainsFunc(dead, func(m tbm.PartyRec) bool {
			return m.Map == p.Map && dist3(partyPoint(m), partyPoint(p)) <= float64(d.tx.RezReach)
		}) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// waitForRez leaves dead bots to a living healer's resurrection before they are released: `cmd revive` makes a bot release its spirit
// and run back (or revives it at once for a GM), and a released bot cannot be raised in place any more. It waits while a resurrector
// is alive out of combat, up to RezWait of calm (fights in the meantime are helped and do not count), and ends at once when none is
// left: a wipe goes straight to the revive orders. It returns when nobody is dead or the wait is over.
func (d *RemoteDriver) waitForRez(ctx context.Context, why string) error {
	if d.opt.RezWait <= 0 {
		return nil
	}
	began := d.clk.Now()
	last := began
	var calm time.Duration
	noted := false
	seenDead := map[packet.GUID]bool{} // every bot seen dead during the wait, including ones that fell meanwhile
	defer func() {
		raised := 0
		for g := range seenDead {
			if m, ok := d.fr.Member(g); ok && !m.Dead {
				raised++
			}
		}
		for g := range seenDead {
			if m, ok := d.fr.Member(g); ok {
				d.tr.Event("rez", map[string]any{"leg": max(d.curLeg, 0), "bot": m.Name, "raised": !m.Dead, "seconds": d.clk.Now().Sub(began).Seconds(), "why": why})
			}
		}
		if raised > 0 {
			d.remote(func(r *RemoteReport) { r.Rezzed += raised; r.Rezzes += raised })
			d.rep.note(fmt.Sprintf("leg %d: %d dead bots were raised within %.0f s of waiting", max(d.curLeg, 0), raised, d.clk.Now().Sub(began).Seconds()))
		}
	}()
	for {
		dead := d.deadMembers()
		for _, m := range dead {
			seenDead[m.GUID] = true
		}
		if len(dead) == 0 {
			return nil
		}
		now := d.clk.Now()
		if bad := d.corpsesOnDeadly(dead); len(bad) > 0 {
			// A body on ground that kills is raised into it (rfc-live-1e: the bodies lay in the lava): release them to run back instead.
			d.remote(func(r *RemoteReport) { r.UnsafeRezSkips++ })
			d.rep.note(fmt.Sprintf("leg %d: %d dead bots (%s); bodies on deadly ground (%s): no resurrection in place, releasing them (cmd revive: they run back from the graveyard)", max(d.curLeg, 0), len(dead), why, strings.Join(bad, "; ")))
			return nil
		}
		if near := d.hostilesNearCorpses(); len(near) > 0 {
			// A resurrection in place next to mobs that have not reset puts the party back on the floor (Uldaman A, 2026-10-14): the dead
			// are released and run back from the graveyard, which gives the mobs time to leave.
			d.remote(func(r *RemoteReport) { r.UnsafeRezSkips++ })
			d.rep.note(fmt.Sprintf("leg %d: %d dead bots (%s); %s stands within %.0f yd of the bodies: no resurrection in place, releasing them (cmd revive: they run back from the graveyard)", max(d.curLeg, 0), len(dead), why, d.hostileName(near[0]), d.tx.ReviveSafeYd))
			return nil
		}
		if len(d.resurrectors()) == 0 {
			if noted {
				d.rep.note(fmt.Sprintf("leg %d: no living caster out of combat to raise %d dead bots (%s); releasing them", max(d.curLeg, 0), len(dead), why))
			}
			return nil
		}
		if calm >= d.opt.RezWait || now.Sub(began) > d.opt.RezWait+d.cfg.FightTimeout {
			d.rep.note(fmt.Sprintf("leg %d: %d dead bots were not raised in %.0f s (%s); releasing them", max(d.curLeg, 0), len(dead), d.opt.RezWait.Seconds(), why))
			return nil
		}
		if !noted {
			noted = true
			d.remote(func(r *RemoteReport) { r.RezWaits++ })
			d.rep.note(fmt.Sprintf("leg %d: %d dead bots (%s); waiting up to %.0f s for a living caster to raise them before releasing", max(d.curLeg, 0), len(dead), why, d.opt.RezWait.Seconds()))
		}
		if eng := d.fr.EngagedHostiles(); len(eng) > 0 {
			if err := d.assist(ctx, eng); err != nil {
				return err
			}
		} else {
			calm += now.Sub(last)
		}
		last = now
		if err := d.tick(ctx, d.cfg.Poll*2); err != nil && !errors.Is(err, errWipe) {
			return err
		}
	}
}

func (d *RemoteDriver) deadMembers() []tbm.PartyRec {
	var out []tbm.PartyRec
	for _, p := range d.fr.Party {
		if p.Dead {
			out = append(out, p)
		}
	}
	// The lead bot first: nothing walks without it.
	slices.SortStableFunc(out, func(a, b tbm.PartyRec) int {
		switch {
		case a.GUID == d.fr.Lead && b.GUID != d.fr.Lead:
			return -1
		case b.GUID == d.fr.Lead && a.GUID != d.fr.Lead:
			return 1
		}
		return 0
	})
	return out
}

// recoverWipe brings a dead party back: revive orders for every bot (the bots release and run to their corpses), then the rest gate
// runs when the step goes on. It needs RecoverWipe.
func (d *RemoteDriver) recoverWipe(ctx context.Context) error {
	d.rep.note(fmt.Sprintf("wipe %d: reviving the party with .bot cmd revive (recover_wipe)", d.wipeRecover))
	d.rollbackHistory()
	// The wipe is being recovered from: the deaths already counted stay counted.
	if err := d.reviveDead(ctx, "the party wiped"); err != nil {
		return err
	}
	return d.waitRevivedRejoin(ctx)
}

// healerManaCap is the most a rest gate waits when the healer's mana is the only thing under its line.
const healerManaCap = 120 * time.Second

// revivedRejoinWait bounds waitRevivedRejoin.
const revivedRejoinWait = 120 * time.Second

// waitRevivedRejoin waits, after a wipe's revives, until every member is alive and within GatherNear of the lead bot (a revived bot runs back
// from the graveyard), summoning the far ones when the agent is a GM (the rejoin), so that the next pull is not made with dead or distant
// bots (live, zum2-s1: three packs pulled with dead bots, four more bots lost in the 6 minutes after a wipe). Past the bound a member that is
// still dead fails the recovery (one error, so one wipe uses one recovery slot); members that are alive but far give a degraded success.
// A wipe seen during the wait (the revived bots dying again) is the same unresolved wipe: it is not counted again, the wait runs to its bound.
func (d *RemoteDriver) waitRevivedRejoin(ctx context.Context) error {
	until := d.clk.Now().Add(revivedRejoinWait)
	noted := false
	for {
		d.look()
		missing := d.awayFromLead(float64(d.tx.GatherNear))
		dead := d.deadMembers()
		if len(missing) == 0 && len(dead) == 0 {
			if noted {
				d.rep.note(fmt.Sprintf("recovery: every member is alive and with the lead bot again (%.0f s after the revives)", d.clk.Now().Sub(until.Add(-revivedRejoinWait)).Seconds()))
			}
			return nil
		}
		if !d.clk.Now().Before(until) {
			var names, deadNames []string
			for _, b := range missing {
				names = append(names, fmt.Sprintf("%s %.0f yd", b.name, b.dist))
			}
			for _, m := range dead {
				names = append(names, m.Name+" dead")
				deadNames = append(deadNames, m.Name)
			}
			note := fmt.Sprintf("recovery: %s not back with the lead bot after %.0f s", strings.Join(names, ", "), revivedRejoinWait.Seconds())
			if len(dead) > 0 {
				// A member that is dead at the bound is an unresolved wipe: going on would retry the step, and the next party check would count
				// the same wipe again and use up the leg's recovery cap without a second wipe. The recovery fails once here instead.
				d.rep.note(note + "; the wipe is not recovered")
				return fmt.Errorf("the wipe is not recovered: %s still dead %.0f s after the revives", strings.Join(deadNames, ", "), revivedRejoinWait.Seconds())
			}
			// Alive but far from the lead bot: the run goes on with fewer bots at hand, and the report says so.
			d.rep.note(note + "; going on")
			d.degrade(fmt.Sprintf("recovery: %s not back with the lead bot after %.0f s", strings.Join(names, ", "), revivedRejoinWait.Seconds()))
			return nil
		}
		if !noted {
			noted = true
			d.rep.note(fmt.Sprintf("recovery: waiting for %d revived bot(s) to be alive and with the lead bot before the next pull", len(missing)+len(dead)))
		}
		if err := d.rejoinFar(ctx); err != nil {
			return err
		}
		if err := d.tick(ctx, d.cfg.Poll*2); err != nil && !errors.Is(err, errWipe) {
			return err
		}
	}
}

// ---- resting ----------------------------------------------------------------------------------------------------------

type remoteRest struct {
	ok        bool
	why       string
	short     []string
	minHealth int
	dead      []tbm.PartyRec
	stalled   []string
	nudge     []shortMana // members whose mana has not risen at all in DrinkNudge and have had no order of their own yet
}

// restState looks at the party in the newest frame, like Driver.restState: health and mana at the thresholds and everyone
// near the lead bot. A caster whose mana did not rise ManaStallPct in ManaStall (it does not drink) is let pass, unless it is
// below HealerManaMin (the feed carries no class, so every caster that low is waited for). The healer is the bot RemoteOptions.Healer
// names, held to the healer mana target; without one every caster is.
func (d *RemoteDriver) restState(rw *restWatch) remoteRest {
	r := remoteRest{ok: true, minHealth: 100}
	var low []string
	now := d.clk.Now()
	tgt := rw.tgt
	lead, haveLead := d.leadPos()
	for _, m := range d.fr.Party {
		if m.Dead {
			r.dead = append(r.dead, m)
			r.ok = false
			continue
		}
		h := m.HPPct()
		r.minHealth = min(r.minHealth, h)
		if h < tgt.health {
			r.ok = false
			low = append(low, fmt.Sprintf("%s health %d%%", m.Name, h))
		}
		if rw.gather && haveLead && m.GUID != d.fr.Lead && dist3(partyPoint(m), lead) > float64(d.cfg.GatherRadius) {
			if d.gatherIgnored()[m.GUID] {
				// A gather of this leg gave up on it (it came no closer in a whole wait): the rest gate does not wait 60 s for it either.
				if !d.gatherSt.restNoted[m.GUID] {
					d.gatherSt.restNoted[m.GUID] = true
					d.rep.note(fmt.Sprintf("leg %d: rest gate: %s is not near the lead bot, but the gathers of this leg gave up on it; not waiting for it", d.curLeg, m.Name))
				}
			} else {
				r.ok = false
				low = append(low, fmt.Sprintf("%s not near the lead bot", m.Name))
			}
		}
		if !m.UsesMana() {
			continue
		}
		p := m.PowerPct()
		healer := d.opt.Healer == "" || strings.EqualFold(d.opt.Healer, m.Name)
		if p >= tgt.manaNeed(healer) {
			delete(rw.mana, m.GUID)
			continue
		}
		w, nudge := rw.look(d.cfg, now, m.GUID, m.Power)
		sm := shortMana{GUID: m.GUID, Name: m.Name, Pct: p}
		if nudge {
			r.nudge = append(r.nudge, sm)
		}
		if p >= d.cfg.HealerManaMin && rw.stalled(d.cfg, now, m.GUID, w, m.Power, m.MaxPower) {
			r.stalled = append(r.stalled, fmt.Sprintf("%s not drinking (mana %d%%)", m.Name, p))
		} else {
			r.ok = false
			low = append(low, sm.String())
		}
	}
	low = append(low, r.stalled...)
	r.short = low
	r.why = fmt.Sprint(low)
	return r
}

// restGate waits until every living bot is rested (`.bot cmd rest` makes them eat and drink). It starts only once the party is out
// of combat: a fight is helped with attack orders, and the timeout counts from its end. A dead bot is revived with `.bot cmd revive`
// (owner or GM) and the wait for it; with RecoverWipe off a revive that is refused fails the run, like a dead member without GM
// shortcuts in group mode.
func (d *RemoteDriver) restGate(ctx context.Context, gather bool) error {
	return d.restGateWalk(ctx, gather, nil)
}

// restGateWalk is the gate below followed by the automatic maintenance that may be due: a gate that passed is a calm moment.
func (d *RemoteDriver) restGateWalk(ctx context.Context, gather bool, walk *walkRest) error {
	if err := d.restGateRun(ctx, gather, walk); err != nil {
		return err
	}
	return d.maintainIfDue(ctx)
}

// restGateWalk is restGate with the walking-rest rule of the gate that opens a leg: when the party is not at the targets on the gate's
// first frame but everyone is above walk's floors, the gate does not hold (Result "walking"); the party rests on the way and the
// gate before the first pull waits for the targets. A gate that finds everyone at the targets on its first frame is counted as
// skipped (it costs no time).
func (d *RemoteDriver) restGateRun(ctx context.Context, gather bool, walk *walkRest) (err error) {
	defer d.traceRest(d.clk.Now())
	began := d.clk.Now()
	tgt := restTargetFor(d.cfg, d.plan, d.curLeg)
	rec := newGateRec(max(d.curLeg, 0), tgt, began)
	defer func() { rec.finish(&d.rep, d.clk.Now(), err) }()
	// The gate has limits of its own (the cap, the fight timeout, the whole-gate bound): the watchdog's clock waits for it.
	defer d.wd.enter("rest gate", "", nil)()
	gh := &gateHold{w: d.wd}
	defer gh.set(false)
	ft := &fightToggle{w: d.wd, detail: "at a rest gate"} // a fight in the gate is watched by its mobs' health
	defer ft.set(false)
	ordered := false
	held := false // the gate has waited for something (a fight, a revive, the party): its first frame did not pass it
	var lastRest, healerFrom time.Time
	calm := began
	var fightBegan time.Time
	rw := newRestWatch(tgt)
	rw.gather = gather
	limit := tgt.max + 2*d.cfg.FightTimeout
	d.look()
	for {
		if el := d.clk.Now().Sub(began); el > limit {
			return fmt.Errorf("the rest gate has gone on for more than %.0f s (fights keep interrupting it): the party is not rested", limit.Seconds())
		}
		if err := d.check(); err != nil {
			return err
		}
		if eng := d.fr.EngagedHostiles(); len(eng) > 0 {
			now := d.clk.Now()
			gh.set(false)
			ft.set(true)
			d.wd.mobHealth(hostilesHP(eng))
			if fightBegan.IsZero() {
				fightBegan = now
			}
			if now.Sub(fightBegan) > d.cfg.FightTimeout {
				return fmt.Errorf("the party is still in combat after %.0f s at a rest gate", d.cfg.FightTimeout.Seconds())
			}
			if err := d.assist(ctx, eng); err != nil {
				return err
			}
			ordered, calm, held = false, now, true
			clear(rw.mana)
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			continue
		}
		fightBegan = time.Time{}
		ft.set(false)
		gh.set(true)
		if err := d.rejoinFar(ctx); err != nil {
			return err
		}
		st := d.restState(rw)
		rec.g.Short = st.short
		if len(st.dead) > 0 {
			held = true
			if err := d.reviveDead(ctx, "dead bots at a rest gate"); err != nil {
				return err
			}
			calm = d.clk.Now()
			continue
		}
		if !st.ok && !held && walk != nil {
			if fs := d.restState(newRestWatch(walk.floor)); fs.ok && len(fs.dead) == 0 {
				rec.g.Result, rec.g.Skipped = "walking", true
				d.rep.note(fmt.Sprintf("leg %d: rest gate passed for a walk of %.0f yd or more (%s): resting on the way", d.curLeg, walk.yd, st.why))
				return nil
			}
		}
		if st.ok {
			rec.g.Skipped = !held
			if len(st.stalled) > 0 {
				rec.g.Result = "stalled"
				d.rep.note(fmt.Sprintf("leg %d: rest gate passed with %s; mana did not rise in %.0f s", d.curLeg, strings.Join(st.stalled, ", "), d.cfg.ManaStall.Seconds()))
			}
			return nil
		}
		held = true
		// The commander has no order for one bot (`.bot cmd rest` is the whole party's): a member whose mana does not rise at all gets
		// the rest order once more, and a refused one is told apart in the record.
		if fresh := slices.DeleteFunc(slices.Clone(st.nudge), func(m shortMana) bool { return rw.nudged[m.GUID] }); len(fresh) > 0 {
			for _, m := range fresh {
				rw.nudged[m.GUID] = true
				rec.nudged(m.Name)
			}
			ordered, lastRest = true, d.clk.Now()
			r, err := d.order(ctx, "rest")
			if err != nil {
				return err
			}
			if r.Kind == tbm.KindErr {
				rec.g.PartyRestRefused = true
			}
			d.rep.note(fmt.Sprintf("leg %d: %s mana %d%% did not rise in %.0f s; sent cmd rest again (%s)", d.curLeg, fresh[0].Name, fresh[0].Pct, d.cfg.DrinkNudge.Seconds(), replyText(r)))
		}
		// The healer alone under its mana line is waited for healerManaCap at most (live, zum2-s1: a gate sat 359 s for a healer at 23-24 % mana
		// that did not rise); the others' lines and every health line keep the gate's own cap.
		if len(st.short) == 1 && d.opt.Healer != "" && strings.HasPrefix(st.short[0], d.opt.Healer+" mana ") {
			if healerFrom.IsZero() {
				healerFrom = d.clk.Now()
			}
			// Once the cap was hit in a leg, the later gates of that leg do not wait for the healer's mana at all (three gates of 120 s each
			// would be the same 360 s).
			if (d.healerCapped == d.curLeg+1 || d.clk.Now().Sub(healerFrom) > healerManaCap) && st.minHealth >= d.cfg.RestMinHealth {
				d.healerCapped = d.curLeg + 1
				rec.g.Result = "gave_up"
				d.rep.note(fmt.Sprintf("leg %d: rest gate does not wait for the healer's mana any longer in this leg (%.0f s cap: %s); went on with at least %d%% health", d.curLeg, healerManaCap.Seconds(), st.why, st.minHealth))
				return nil
			}
		} else {
			healerFrom = time.Time{}
		}
		if d.clk.Now().Sub(calm) > tgt.max {
			if st.minHealth >= d.cfg.RestMinHealth {
				rec.g.Result = "gave_up"
				d.rep.note(fmt.Sprintf("leg %d: rest gate gave up after %.0f s (%s); went on with at least %d%% health", d.curLeg, tgt.max.Seconds(), st.why, st.minHealth))
				return nil
			}
			return fmt.Errorf("the party is not rested after %.0f s: %s", tgt.max.Seconds(), st.why)
		}
		if !ordered || d.clk.Now().Sub(lastRest) > 45*time.Second {
			ordered, lastRest = true, d.clk.Now()
			r, err := d.order(ctx, "rest")
			if err != nil {
				return err
			}
			if r.Kind == tbm.KindErr {
				rec.g.PartyRestRefused = true
			}
		}
		// A caster running out of drinks gets `.bot maintenance` again (remote_rest.go) and the party is told to rest anew.
		refilled, err := d.refill(ctx, rw, rec)
		if err != nil {
			return err
		}
		if len(refilled) > 0 {
			ordered = false
		}
		if err := d.tick(ctx, d.cfg.RestPoll); err != nil {
			return err
		}
	}
}

// stale says whether the feed has gone quiet: the loops that decide from it wait instead.
func (d *RemoteDriver) stale() bool { return d.stalled }

func distToLead(d *RemoteDriver, p Point) float64 {
	l, ok := d.leadPos()
	if !ok {
		return math.MaxFloat64
	}
	return dist3(l, p)
}

// legBossDead says whether the leg under way has a boss and the run knows him dead (a kill, the ledger, or his corpse in view).
func (d *RemoteDriver) legBossDead() bool {
	if d.curLeg < 0 || d.curLeg >= len(d.plan.Legs) {
		return false
	}
	b := d.plan.Legs[d.curLeg].Boss
	if b == nil {
		return false
	}
	dead, _ := d.bossDead(fightSpec{pick: []uint32{b.Entry}, wide: true})
	return dead
}
