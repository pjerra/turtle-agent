// Package dungeon holds the dungeon plan format of the dungeon leader (issue #73): a JSON file per dungeon
// (dungeons/<name>.json) that lists, in order, where to walk, which groups to pull, which doors to open and which
// boss to kill; a validating loader; and a pure Stepper that walks a plan against a fake or real World.
//
// The package does no networking. The driver that turns the Stepper's actions into RPC calls is a separate piece.
package dungeon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/tactics"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Version is the plan format this package reads and writes.
const Version = 1

// Limits the loader enforces, so a typo cannot ask for an hour-long wait or a 3-mile radius.
const (
	DefaultTimeoutS    = 60
	MaxTimeoutS        = 600
	DefaultObjectRange = 8   // yards around Near an object is looked for
	MaxRadius          = 300 // yards
	MaxFailText        = 400 // characters of a wait's fail_text
)

// Point is a position in the map's coordinates.
type Point struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// The kinds of plan: a 5-man (or smaller) dungeon, run with a hired party, and a raid, run with a raid group.
const (
	KindDungeon = "dungeon"
	KindRaid    = "raid"
)

// Plan status: how far a plan has been checked. The field is free text after one of these words, so a plan can say what is
// missing ("draft (not mapped, not live-checked)"). Only a plan that is mapped or live-checked has had its route corrected from a
// walk; a draft is a skeleton from the world database export (tools/dungeongen) and is not to be run before its mapping walk.
const (
	StatusDraft       = "draft"
	StatusMapped      = "mapped"
	StatusLiveChecked = "live-checked"

	// DraftStatus is the status line tools/dungeongen writes.
	DraftStatus = "draft (not mapped, not live-checked)"
)

// RaidSpec is a raid plan's group: how many players and, as a placeholder until a raid has been run, the roles they are split into.
// Size is 10, 20 or 40 and Tanks + Healers + DPS must add up to it.
type RaidSpec struct {
	Size    int `json:"size"`
	Tanks   int `json:"tanks"`
	Healers int `json:"healers"`
	DPS     int `json:"dps"`
}

// Plan is a loaded, validated dungeon plan.
type Plan struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	// Kind is "dungeon" (the default when absent) or "raid".
	Kind string `json:"kind,omitempty"`
	// Status says how far the plan has been checked: draft, mapped or live-checked, then free text (see StatusDraft). Absent: not said.
	Status   string `json:"status,omitempty"`
	Map      uint32 `json:"map"`
	Entrance Point  `json:"entrance"`
	// EntranceTrigger is the id of the area trigger of the dungeon's entrance portal (areatrigger_teleport in the world
	// database; Uldaman 286). A ghost whose corpse lies inside sends it at the portal to be taken in and revived (the client
	// does the same on stepping into it). Without it a corpse run into the dungeon is refused; the rest of the run does not need it.
	EntranceTrigger uint32 `json:"entrance_trigger,omitempty"`
	// EntranceTele is the name of the game_tele row that puts a player at the entrance inside the dungeon (Uldaman "ulda", game_tele
	// 169): a remote run's enter phase sends `.tele name <lead bot> <name>` with it when the account is a developer-level GM.
	EntranceTele string `json:"entrance_tele,omitempty"`
	// OutsideTele is the game_tele row that puts a player at the entrance OUTSIDE, on the outer map (Uldaman "uldaman"): the prepare step
	// of a remote run (`fresh`) brings the bots and the agent out of an old instance with it before it unbinds them.
	OutsideTele string `json:"outside_tele,omitempty"`
	// Portal is the entrance portal on the outer map (the area trigger of EntranceTrigger): where a remote run's enter phase walks the
	// lead bot when it has no GM rights.
	Portal *Portal `json:"portal,omitempty"`
	// LevelBand is the character levels the dungeon is meant for, low and high (Uldaman 41-45): the prepare step's hired party takes the
	// middle of it as its level. Empty: the plan gives none.
	LevelBand []int `json:"level_band,omitempty"`
	// HireComposition is the hired party (prepare.party "hire") when prepare.hire.composition gives none, in its form ("class" or
	// "class:role" each). Empty: DefaultHireComposition. Zul'Farrak hires a mage for the Polymorph of its big packs.
	HireComposition []string `json:"hire_composition,omitempty"`
	// Fleers are the creature entries that run from the fight at low health (EventAI "flee at 15 % HP": Uldaman's Stonevault Geomancers,
	// Shadowforge casters, Flameweavers) and come back with the next group. The remote driver kills such a mob before it is out of reach
	// and waits for one that got away before it pulls on (remote_flee.go); any other mob is taken for a runner only when the feed shows it
	// at low health, moving away from the party.
	Fleers []uint32 `json:"fleers,omitempty"`
	// CCPriority lists, best first, the entries the pack CC (remote_cc.go) puts its mark on: the healers and casters of the dungeon
	// that the mage's Polymorph takes. Empty: any pack member but the pull target, the farthest from it first.
	CCPriority []uint32 `json:"cc_priority,omitempty"`
	// CCAlways lists entries that get the CC in any pack they are in, below big_pack too (Zul'Farrak's Sandfury Shadowhunter, whose
	// Hex takes the tank out; owner rule 2026-10-14). They are not picked as the pull target while another member is there.
	CCAlways []uint32 `json:"cc_always,omitempty"`
	// NoCC lists entries the pack and reactive CC never take, besides the plan's bosses and kill_first entries (which are never crowd-controlled
	// either): the undead and the others a Polymorph does not land on (Zul'Farrak's Zombie 7286 and Dead Hero 7276: nine orders, one cast).
	NoCC []uint32 `json:"no_cc,omitempty"`
	// KillFirst lists, best first, the entries the party kills before the rest of a fight, unless one is crowd-controlled (Zul'Farrak's
	// Greater Healing Ward 8179, the Witch Doctor 5650 and the Soul Eater 7247, the healers whose heals outlast the party). A summoned
	// entry (the ward) is a kill target, never a CC target. Empty: the tank's order only (remote_fight.go).
	KillFirst []uint32 `json:"kill_first,omitempty"`
	// SummonedKillFirst names the kill_first entries that are summoned units (Zul'Farrak's Greater Healing Ward 8179): they fight nobody, so
	// the remote driver takes one as part of a fight only with evidence that the fight is there (it stands within 10 yd of a hostile that
	// fights the party), sends the party at it with no threat wait, and never crowd-controls it. Each must also be in kill_first.
	SummonedKillFirst []uint32 `json:"summoned_kill_first,omitempty"`
	// Raid is the raid group of a raid plan (kind "raid"): its size and a placeholder role split.
	Raid  *RaidSpec `json:"raid,omitempty"`
	Notes []string  `json:"notes,omitempty"`
	// Watchdog sets the plan's stall levels (see WatchdogConfig); dungeon_run's watchdog parameter wins field by field. Absent: 120/240/420 s.
	Watchdog *WatchdogConfig `json:"watchdog,omitempty"`
	Legs     []Leg           `json:"legs"`
}

// Band is the plan's level band, low and high; 0, 0 when it gives none.
func (p *Plan) Band() (lo, hi int) {
	if len(p.LevelBand) != 2 {
		return 0, 0
	}
	return p.LevelBand[0], p.LevelBand[1]
}

// Portal is a point of the outer map: the centre of the entrance's area trigger.
type Portal struct {
	Map uint32 `json:"map"`
	Point
}

// Leg is one stretch of the dungeon. The driver walks to To, clears PullGroups in order, runs Steps in order, then
// walks to the Boss and kills it. Every part is optional but a leg must hold at least one of them.
type Leg struct {
	Name string `json:"name,omitempty"`
	To   Point  `json:"to"`
	// Optional marks a leg that needs an item no honest run carries (Zul'Farrak's gong needs the Mallet of Zul'Farrak): at the leg's start the
	// run looks for the item in the lead bot's and the carrier's bags and, when neither has it, skips the whole leg (the leg's result says
	// so) instead of failing. The run-start check for a step's requires_item leaves such a leg out.
	Optional *OptionalLeg `json:"optional,omitempty"`
	// Via are waypoints walked, in order, before To (and before the pull groups). A plain waypoint is walked like To (the navmesh
	// path). A waypoint with Direct set is a straight-line step for a gap the navmesh does not bridge: the remote driver sends
	// `cmd move x y z direct` (TortoiseBots), which walks the lead bot in a line instead of on a path; the others walk to its spot first
	// and take its line (TortoiseBots #948). See Waypoint.
	Via        []Waypoint  `json:"via,omitempty"`
	PullGroups []PullGroup `json:"pull_groups,omitempty"`
	// Skip lists creature entries the leg never waits for: rares, escorts, friendly NPCs, and adds that die with the boss.
	Skip  []uint32 `json:"skip,omitempty"`
	Steps []Step   `json:"steps,omitempty"`
	Boss  *Boss    `json:"boss,omitempty"`
	// CompanionsLoot false has the driver order the party's corpse and object looting off (".bot behavior <bot> loot off") from the start
	// of the leg to its end, and on again after: the Zul'Farrak graveyard, where a bot that loots opens a Shallow Grave chest and wakes
	// what lies in it. It is the leg's whole stretch, not only the boss fight (Boss.CompanionsLoot, which the boss's own loot_item needs).
	// Absent or true leaves the companions as they are.
	CompanionsLoot *bool `json:"companions_loot,omitempty"`
	// LeftoverEntries are summoned creatures a step of the leg leaves standing idle (Zul'Farrak's Zombies 7286 and Dead Heroes 7276 that a grave
	// brings: round 3, 11 of them stood idle after step 16 and all aggroed at once when the carrier went to the next grave). The remote driver
	// pulls the ones within LeftoverRadius of the next batch of chests and fights them before the chests are opened, after each batch, and
	// before an area trigger (remote_batch.go). Empty: no sweep.
	LeftoverEntries []uint32 `json:"leftover_entries,omitempty"`
	// NeedsWalk marks a To that is not on the navmesh and could not be snapped to it (no walkable floor within a few yards: a scripted
	// area, a platform): the mapping walk has to find the real waypoint. The same flag on a pull group or the boss says the same of its position.
	NeedsWalk bool `json:"needs_walk,omitempty"`
	// AfterBoss are steps done once the boss is dead (or skipped for the leader's team): the chest by the camp that a hostile boss
	// guards, the item to combine with what the boss dropped. Same kinds as Steps.
	AfterBoss []Step `json:"after_boss,omitempty"`
	// Rest overrides the rest gates' targets and cap for this leg (see RestSpec); absent: the driver's defaults.
	Rest  *RestSpec `json:"rest,omitempty"`
	Notes []string  `json:"notes,omitempty"`
}

// OptionalLeg is Leg.Optional: the item the leg needs (item entry, and its name for the messages).
type OptionalLeg struct {
	Item uint32 `json:"item"`
	Name string `json:"name,omitempty"`
}

// Ref is the item as an ItemRef.
func (o OptionalLeg) Ref() ItemRef { return ItemRef{Entry: o.Item, Name: o.Name} }

// The limits of a direct step, as TortoiseBots enforces them (RemoteCommanderPolicy.h): the lead bot must be within DirectMaxDistance
// yards (horizontally) and DirectMaxVertical yards (up or down) of the point, and the point needs ground within DirectGroundTolerance.
const (
	DirectMaxDistance     = 25.0
	DirectMaxVertical     = 8.0
	DirectGroundTolerance = 3.0
)

// Waypoint is a point of a leg's Via. Direct makes the step to it a straight line (`cmd move ... direct`) instead of a navmesh
// path: only for a short step (see the limits above) over ground the navmesh lacks, such as Uldaman's scorpid-pit ramp. A direct
// waypoint must be preceded by a Via point within the limits, so the party is gathered where the step starts. Only the remote
// driver can take such a step; the agent's own companions walk it as a plain waypoint. In the file:
// {"x": -155.7, "y": 279.6, "z": -46.8, "direct": true}.
type Waypoint struct {
	Point
	Direct bool `json:"direct,omitempty"`
	// CheckSkip exempts the hop from the plan check (plancheck.go) and says why: a hop the navmesh cannot judge on purpose (Uldaman's scorpid
	// pit, a gap the mesh does not link). The check lists it as skipped with this text instead of failing it. Empty: checked.
	CheckSkip string `json:"check_skip,omitempty"`
}

// RestSpec is a leg's say over its rest gates. A zero field keeps the driver's default (health 90, healer_mana 50, mana 35,
// max_s 60). A gate passes once every living member has at least Health percent health, every healer at least HealerMana percent
// mana and every other mana user at least Mana percent; after MaxS seconds it goes on regardless (health floor permitting).
type RestSpec struct {
	Health     int `json:"health,omitempty"`      // percent, 1-100
	HealerMana int `json:"healer_mana,omitempty"` // percent, 1-100
	Mana       int `json:"mana,omitempty"`        // percent, 1-100
	MaxS       int `json:"max_s,omitempty"`       // seconds, 1-MaxTimeoutS
	// WalkYd is the walk length (yards) from which the gate at the leg's start lets the party rest while it walks instead of holding
	// (the pull gates that follow still wait): 0 keeps the driver's 60, -1 makes the gate always hold. WalkHealth, WalkHealerMana and
	// WalkMana are the floors everyone must be above for that (defaults 70, 40, 25; percent, 1-100).
	WalkYd         int `json:"walk_yd,omitempty"`
	WalkHealth     int `json:"walk_health,omitempty"`
	WalkHealerMana int `json:"walk_healer_mana,omitempty"`
	WalkMana       int `json:"walk_mana,omitempty"`
}

// PullGroup is one pack: the entries it holds and where its members stand (one position each, or fewer when they
// stand together; the first is the one to pull).
type PullGroup struct {
	Entries   []uint32 `json:"entries"`
	Positions []Point  `json:"positions"`
	// Ambush marks a pack whose members are stealthed (tools/dungeongen sets it from the spawn's stealth aura): a
	// companion never attacks a stealthed unit, so `.bot action pullback` draws no fight. The driver does not pull
	// such a group: it walks the party through the group's positions and fights whatever shows itself (see
	// Driver.sneakPast). A member in view without a stealth aura is pulled as usual; the flag only decides what
	// happens when none is in view.
	Ambush bool `json:"ambush,omitempty"`
	// Radius is how far from one of Positions a unit of the group's entries may stand and still be a member (yards;
	// 0: the driver's PackRadius, 25). Packs of the same entry that stand closer to each other than that, such as the
	// two rows of Stonevault Ambushers at the Uldaman Keystone, need a smaller one so that each group takes its own.
	Radius float32 `json:"radius,omitempty"`
	// Team limits the group to a leader of that team ("alliance" or "horde"); the driver skips it for the other (see Team).
	Team string `json:"team,omitempty"`
	// Equiv names a set of equivalent packs of the leg (same mobs, same kind of ground): the pull-test scenario (dungeon_run pulltest)
	// pulls the next pack of the set for its next try instead of waiting for the first to respawn. Empty: the pack has no equivalent.
	Equiv string `json:"equiv,omitempty"`
	// Patrol is a patrol that walks near the group (remote_patrol.go): before the group is pulled the driver waits until it is out of
	// patrol_aggro of the pull, or pulls it alone first when it passes the party away from the pack, and counts it in the pack while
	// it stands within patrol_aggro of the pull. Nil: no patrol comes by.
	Patrol *Patrol `json:"patrol,omitempty"`
	// NeedsWalk: the positions are not on the navmesh and could not be snapped to it; see Leg.NeedsWalk.
	NeedsWalk bool `json:"needs_walk,omitempty"`
}

// Patrol is a group of mobs that walks a path (creature_movement waypoints, movement type 2): Entries are its members, Path the
// waypoints in the order it walks them (it goes on from the last to the first, as the core's waypoint movement does), PeriodS how long
// one round takes, in seconds (the path's length at the mobs' walk speed, 2.5 yd/s for speed_walk 1).
type Patrol struct {
	Entries []uint32 `json:"entries"`
	Path    []Point  `json:"path"`
	PeriodS float32  `json:"period_s"`
	// PeriodEstimate says PeriodS is an estimate (the path's length at 2.5 yd/s, not a measured round): the driver then does not predict where
	// the patrol will be (it takes no phase from the period): it locates a member and waits until the patrol is past the pull, see
	// patrolDanger. The mapping walk measures the round and drops the flag. The plan check warns for each such patrol.
	PeriodEstimate bool `json:"period_estimate,omitempty"`
	// Source says where the path comes from (the world export's spawn guid, a live sighting), for people.
	Source string `json:"source,omitempty"`
}

// maxPatrolPeriod bounds a patrol's round (seconds).
const maxPatrolPeriod = 900

// MaxEquivLen is the longest Equiv label.
const MaxEquivLen = 40

// Boss is the leg's boss. First lists the entries of the encounter to kill before the boss itself, in order (an add
// that heals it, the first of a group of brothers); the boss entry is killed last and the leg is done when it dies.
type Boss struct {
	Entry    uint32   `json:"entry"`
	Position Point    `json:"position"`
	First    []uint32 `json:"first,omitempty"`
	// Team limits the fight to a leader of that team ("alliance" or "horde"): the driver skips the boss, with its First entries,
	// for the other one, and the leg goes on without it. Uldaman's Baelog, Eric and Olaf are friendly to the Alliance (faction 122,
	// quest givers) and hostile to the Horde only.
	Team string `json:"team,omitempty"`
	// LootItem is the item the leader takes from the boss's corpse after the kill: the driver walks to the corpse, loots it and fails
	// the leg when the item is not in the bags then. Without it the corpse is left to the companions.
	LootItem *ItemRef `json:"loot_item,omitempty"`
	// CompanionsLoot false has the driver order the party's corpse looting off (".bot action loot off") before the boss is pulled and on again after
	// the leader has looted. The companions loot their kills and, under round-robin or group loot, the item goes to whoever takes it first: a
	// boss whose drop the plan needs (LootItem) should have this off. Absent or true leaves the companions as they are.
	CompanionsLoot *bool `json:"companions_loot,omitempty"`
	// Tactics is what a player does on this fight beyond the class rotation: kill an add first, interrupt a cast, dispel an aura, leave the
	// place of a cast (internal/tactics). The bot brain acts on it for the agent's own character when the agent leads and plays (mode
	// leader, README "Boss tactics"); a commander run carries it in the plan and does not use it (the bots are TortoiseBots'), except for
	// kill_first: the remote driver sends the party at those entries before anything else while the boss fight is on (README "Boss tactics").
	Tactics *tactics.Tactics `json:"tactics,omitempty"`
	// NeedsWalk: the position is not on the navmesh and could not be snapped to it; see Leg.NeedsWalk.
	NeedsWalk bool `json:"needs_walk,omitempty"`
}

// The two teams a Team field can name. The driver learns the leader's from its race (session.Team).
const (
	TeamAlliance = "alliance"
	TeamHorde    = "horde"
)

// Step is one thing to do between the pulls and the boss: exactly one of its fields is set.
type Step struct {
	UseObject  *UseObject  `json:"use_object,omitempty"`
	WaitFor    *WaitFor    `json:"wait_for,omitempty"`
	LootObject *LootObject `json:"loot_object,omitempty"`
	UseItem    *UseItem    `json:"use_item,omitempty"`
	Ritual     *Ritual     `json:"ritual,omitempty"`
	Gossip     *Gossip     `json:"gossip,omitempty"`
	// AreaTrigger takes an area trigger the way a player's client does (CMSG_AREATRIGGER): see AreaTrigger.
	AreaTrigger *AreaTrigger `json:"area_trigger,omitempty"`
}

// The ways an area_trigger step is taken (AreaTrigger.By).
const (
	TriggerAuto  = "auto"  // the lead bot walks into the zone first; when Expect does not happen in TriggerBotWait, the agent's own character does it
	TriggerBot   = "bot"   // the lead bot only (the module's AiPlayerbot.BotAreaTriggers lists the trigger)
	TriggerAgent = "agent" // the agent's own character only
)

// TriggerBotWait is how long an auto area_trigger waits for Expect after the lead bot has walked into the zone, before the agent takes over.
const TriggerBotWait = 5 * time.Second

// TriggerMaxStandToBoss is the most a boss of the leg may stand from the area_trigger's stand point (yards): the plan check
// (plancheck.go) wants the trigger and the fight in one place.
const TriggerMaxStandToBoss = 30.0

// AreaTrigger takes the area trigger ID: the player steps into its zone (a sphere of Radius yards round Center) and the client sends
// CMSG_AREATRIGGER, whose handler runs the trigger's script. Zul'Farrak's Witch Doctor Zum'rah (7271) is friendly (faction 35) until area
// trigger 962 fires, and a trigger is taken only from a client packet, so a run that must kill him has someone step into the zone.
//
// By "auto" (the default) lets the lead bot walk to Stand first (TortoiseBots' AiPlayerbot.BotAreaTriggers lists the triggers a bot takes
// by itself on entering the zone); when Expect does not hold within TriggerBotWait the agent's own character does it: `.appear <lead bot>`
// when it is far (a GM assist the run already uses), a walk to Stand, the packet, a walk back. "bot" and "agent" take one of the two
// only. The step fails with the reason when Expect never comes, and is never retried: one attempt by each, then it ends the plan.
//
// Expect is what must be true afterwards, "unit <entry>[,<entry>...] <state>" (UnitWait; states seen, hostile, dead, gone): "unit 7271
// hostile". TimeoutS (default DefaultTimeoutS) bounds the wait for it after the agent's packet (and after the lead bot's, for by "bot").
// Stand must lie within Radius of Center, and a boss of the leg within TriggerMaxStandToBoss of Stand (the plan check says so).
type AreaTrigger struct {
	ID     uint32  `json:"id"`
	Center Point   `json:"center"`
	Radius float32 `json:"radius"`
	// Stand is where the one who takes the trigger stands: inside the zone.
	Stand    Point  `json:"stand"`
	By       string `json:"by"`
	Expect   string `json:"expect"`
	TimeoutS int    `json:"timeout_s"`
	// ExpectUnit is Expect parsed.
	ExpectUnit *UnitWait `json:"-"`
}

// Limits of a ritual step's party.
const (
	MinRitualParticipants = 2 // a ritual of one player is a use_object
	MaxRitualParticipants = 5 // the party
)

// Ritual has several bots use a summoning ritual object together (game object type 18; Uldaman's Altar of the Keepers and Altar of
// Archaedas need three different players). Remote runs only: the driver orders `cmd ritual entry:<Entry>@<Position> <carrier> <bot> <bot>`
// (TortoiseBots), which walks the bots to the object, has them use it in turn and keeps them still until the object is active and the
// ritual has run; the step ends with the final answer. A wait_for the object to be active (or whatever the ritual opens) belongs after it.
// In the file, Position is [x, y, z] or {"x":..,"y":..,"z":..}.
type Ritual struct {
	Entry        uint32 `json:"entry"`
	Position     Point  `json:"position"`
	Participants int    `json:"participants"`
	// Stand is where the party is held while the bots walk in (like UseObject.Stand); without it the driver stands the lead bot a few
	// yards short of Position.
	Stand *Point `json:"stand,omitempty"`
}

// Who talks to the NPC in a gossip step.
const (
	GossipByAgent   = "agent"   // the agent's own character (the default)
	GossipByCarrier = "carrier" // the carrier bot: `.bot cmd gossip <npc> <bot> <option>` (TortoiseBots)
	GossipByTank    = "tank"    // the tank bot, the same order
)

// Limits of a gossip step.
const (
	DefaultGossipRetryS = 15  // seconds a menu that lacks the option is looked at again
	MaxGossipRetryS     = 120 // the most retry_s may say
	GossipRange         = 5.0 // yards: the core answers a gossip hello only within INTERACTION_DISTANCE; the stand must lie inside
	MaxGossipOption     = 99  // the highest option index a plan may name
	MaxGossipText       = 80  // characters of a text option
)

// Gossip talks to an NPC and picks one option of its menu (CMSG_GOSSIP_HELLO, then CMSG_GOSSIP_SELECT_OPTION): Weegli Blastfuse blows
// Zul'Farrak's End Door and Sergeant Bly starts his fight this way. NPC is the creature entry and Near where it stands (its spawn): the
// NPC is the one of that entry nearest to Near. The option is Option (the menu's index) or Text (case-insensitive substring of the line),
// exactly one of them. By says who talks: "agent" (the default; the agent's own character goes within 5 yd of the NPC, with `.appear
// <lead bot>` as a GM assist when it is far, remote runs), or "carrier" and "tank" (the bot sends the order `cmd gossip <npc> <bot>
// <option>`; a module that does not know the verb makes the agent take the step once). Stand is where the talker stands (within
// GossipRange of Near), the NPC's own place when absent. A menu that lacks the option (the phase lags one update behind the event that
// adds it) is asked again for RetryS seconds, then the step fails with the menu id it saw. The step ends at SMSG_GOSSIP_COMPLETE (or the
// order's ACK): what happens next is for the following wait_for to check. FailText is added to the failure.
type Gossip struct {
	NPC      uint32  `json:"npc"`
	Near     Point   `json:"near"`
	Option   *uint32 `json:"option,omitempty"`
	Text     string  `json:"text,omitempty"`
	By       string  `json:"by,omitempty"`
	Stand    *Point  `json:"stand,omitempty"`
	RetryS   int     `json:"retry_s,omitempty"`
	FailText string  `json:"fail_text,omitempty"`
}

// Talker is who talks, By or its default.
func (g *Gossip) Talker() string {
	if g.By == "" {
		return GossipByAgent
	}
	return g.By
}

// Retry is how long a menu that lacks the option is asked again.
func (g *Gossip) Retry() time.Duration {
	if g.RetryS <= 0 {
		return DefaultGossipRetryS * time.Second
	}
	return time.Duration(g.RetryS) * time.Second
}

// OptionText says which option the step picks, for logs: "option 1" or `"text"`.
func (g *Gossip) OptionText() string {
	if g.Option != nil {
		return fmt.Sprintf("option %d", *g.Option)
	}
	return fmt.Sprintf("%q", g.Text)
}

// ObjectRef picks a game object: by entry, by position, or both (the nearest object of that entry to Near).
type ObjectRef struct {
	Entry uint32  `json:"entry,omitempty"`
	Near  *Point  `json:"near,omitempty"`
	Range float32 `json:"range,omitempty"` // yards around Near; DefaultObjectRange when 0
	// Spawn is the object's spawn GUID in the world database (gameobject.guid): with Entry it names that exact object. A remote run asks
	// `cmd state` for it before a loot_object step and skips one that is opened already or gone (a resumed run goes on at the first unopened
	// grave), and opens it by GUID, so that a neighbour of the same entry is never opened in its place. 0: not given.
	Spawn uint32 `json:"spawn,omitempty"`
}

// UseObject uses a game object (right-click). Stand is where to stand to do it; without it the driver walks next
// to the object.
type UseObject struct {
	ObjectRef
	Stand *Point `json:"stand,omitempty"`
	// RequiresItem is the item a non-GM leader must carry for the use to do anything (the object's lock names it: the
	// Uldaman Keystone needs the Staff of Prehistoria). Without it the core ignores the use silently, so the driver checks the
	// leader's bags before the run and before the step and fails with this item's name. Only a leader the caller says is a GM
	// (gm: true) is not checked: the core lets a GM through a lock in GM mode only.
	RequiresItem *ItemRef `json:"requires_item,omitempty"`
	// ViaItem says the required item is USED ON the object instead of the object being used: the remote driver orders `cmd use <item> <bot>
	// entry:<object>@<point>` (TortoiseBots walks the bot to the object and casts the item's spell on it). Zul'Farrak's Gong of Zul'Farrak is
	// rung that way: the Mallet of Zul'Farrak's on-use spell 11434 (SPELL_EFFECT_OPEN_LOCK_ITEM, target the game object) opens the gong's lock
	// 459 (Gahz'ridian) and the gong's event 2488 runs; a plain use of the gong object does nothing (live 2026-10-15, round 3: no answer, no
	// Gahz'rilla). It needs requires_item, and a stand within UseRange of the object, since the spell's range is short. Remote runs only.
	ViaItem bool `json:"via_item,omitempty"`
}

// LootObject opens a chest or a node and takes what is in it: the leader walks next to it (to Stand when given), casts the Opening
// spell at it (session.OpenObject: CMSG_GAMEOBJ_USE alone does not open a chest in this core), takes every item and closes the
// window. ExpectItem is the item that must be in the bags afterwards; the step fails when it is not. A leader that already carries
// it has nothing to fetch and the step is passed (noted), so a run can be resumed.
type LootObject struct {
	ObjectRef
	Stand      *Point   `json:"stand,omitempty"`
	ExpectItem *ItemRef `json:"expect_item,omitempty"`
	// Batch groups consecutive loot_object steps of one leg that are opened back to back (Zum'rah's graves): the party gathers at the
	// batch's centre, the carrier opens every chest of the batch with no fight between the opens, then the party fights out everything
	// that came out. A batch has BatchMinSize to BatchMaxSize steps, all within BatchRadius yards of the batch's centre. The run's
	// graves_batch and graves_ramp settings regroup the same steps into batches of their own size (remote_batch.go). Empty: no batch.
	Batch string `json:"batch,omitempty"`
}

// Limits of a loot_object batch in a plan (the run's graves_batch and graves_ramp are not bound by the size limits).
const (
	BatchMinSize = 3
	BatchMaxSize = 6
	// BatchRadius is how far from the batch's centre (the mean of its chests) a chest of a plan batch may lie: the summons of the batch
	// stand close enough together for the party's area spells to hit them all.
	BatchRadius = 15.0
	// LeftoverRadius is how far from a batch's centre (or the trigger's stand point) an idle summon of the leg's leftover_entries is swept.
	LeftoverRadius = 35.0
)

// UseItem uses an item from the leader's bags (a right-click: CMSG_USE_ITEM) and waits for its effect. ExpectItem is the item the use
// must put in the bags (the Gni'kiv Medallion's spell 9594 makes the Staff of Prehistoria); without it the step ends after the
// cast has had time to finish. RequiresItem is a second item the use consumes (spell 9594 takes the Shaft of Tsol as a reagent), checked
// before the use so that the failure names it. An ExpectItem the leader already carries passes the step without a use.
type UseItem struct {
	Entry        uint32   `json:"entry"`
	Name         string   `json:"name,omitempty"`
	RequiresItem *ItemRef `json:"requires_item,omitempty"`
	ExpectItem   *ItemRef `json:"expect_item,omitempty"`
}

// ItemRef names an item: its entry and, for the messages, its name.
type ItemRef struct {
	Entry uint32 `json:"entry"`
	Name  string `json:"name,omitempty"`
}

// String is "Staff of Prehistoria (item 7733)", or "item 7733" without a name.
func (i ItemRef) String() string {
	if i.Name == "" {
		return fmt.Sprintf("item %d", i.Entry)
	}
	return fmt.Sprintf("%s (item %d)", i.Name, i.Entry)
}

// WaitFor waits for a condition, up to TimeoutS seconds, and fails the plan when it does not come. Exactly one of
// GameObject, HostilesGone and Unit is set.
type WaitFor struct {
	GameObject   *ObjectState  `json:"gameobject,omitempty"`
	HostilesGone *HostilesGone `json:"hostiles_gone,omitempty"`
	Unit         *UnitWait     `json:"unit,omitempty"`
	TimeoutS     int           `json:"timeout_s,omitempty"`
	// FailText is added to the failure when the wait times out: the reason a person should read (Uldaman's altar waits say that the
	// ritual needs three participants).
	FailText string `json:"fail_text,omitempty"`
}

// ObjectState waits for a game object to be in a state: ready (a door closed), active (open or used) or active_alt.
type ObjectState struct {
	ObjectRef
	State string `json:"state"`
}

// HostilesGone waits until no hostile unit is within Radius yards of Center (the player when nil).
type HostilesGone struct {
	Radius float32 `json:"radius"`
	Center *Point  `json:"center,omitempty"`
}

// UnitWait waits for a creature: a living or dead unit of one of Entries, in the bots' feed or in the agent's own view (its object store,
// when the agent stands near). The states:
//
//	seen     a living unit of the entries is in view (any reaction)
//	hostile  a living unit of the entries is in view and hostile to the party (the feed lists it hostile, or the agent's store does)
//	dead     a unit of the entries is dead (a corpse in view, or the kill ledger saw it die), and none of them is alive in view
//	gone     no unit of the entries is in view (despawned, or never there)
//
// Near and Radius narrow it to units within Radius yards of Near (DefaultUnitRadius without a radius); without Near a unit anywhere in view
// counts. In the file, from a wait_for: {"unit": {"entries": [7271], "near": {..}, "radius": 40, "state": "hostile"}}.
type UnitWait struct {
	Entries []uint32 `json:"entries"`
	Near    *Point   `json:"near,omitempty"`
	Radius  float32  `json:"radius,omitempty"`
	State   string   `json:"state"`
}

// The states of a UnitWait, and the radius round Near when none is given.
const (
	UnitSeen          = "seen"
	UnitHostile       = "hostile"
	UnitDead          = "dead"
	UnitGone          = "gone"
	DefaultUnitRadius = 50
)

// UnitStates are the values of UnitWait.State.
var UnitStates = []string{UnitSeen, UnitHostile, UnitDead, UnitGone}

// String is the unit wait as an expect text: "unit 7271 hostile".
func (u UnitWait) String() string {
	es := make([]string, len(u.Entries))
	for i, e := range u.Entries {
		es[i] = strconv.FormatUint(uint64(e), 10)
	}
	return "unit " + strings.Join(es, ",") + " " + u.State
}

// parseExpect reads "unit <entry>[,<entry>...] <state>".
func parseExpect(s string) (*UnitWait, error) {
	f := strings.Fields(s)
	if len(f) != 3 || f[0] != "unit" {
		return nil, fmt.Errorf("%q: write it as \"unit <entry>[,<entry>...] <state>\" (states %s)", s, strings.Join(UnitStates, ", "))
	}
	u := &UnitWait{State: f[2]}
	for _, e := range strings.Split(f[1], ",") {
		n, err := strconv.ParseUint(e, 10, 32)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("%q: %q is no creature entry", s, e)
		}
		u.Entries = append(u.Entries, uint32(n))
	}
	if !slices.Contains(UnitStates, u.State) {
		return nil, fmt.Errorf("%q: %q is none of %s", s, u.State, strings.Join(UnitStates, ", "))
	}
	return u, nil
}

// ObjectStates are the values of ObjectState.State.
var ObjectStates = []string{"ready", "active", "active_alt"}

// raw layer: what the JSON file says before it is checked. Pointers tell a missing field from a zero.
type rawPoint struct {
	X, Y, Z *float32
}

type rawPlan struct {
	Version    *int              `json:"version"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Status     string            `json:"status"`
	Fleers     []uint32          `json:"fleers"`
	Raid       *RaidSpec         `json:"raid"`
	Map        *uint32           `json:"map"`
	Entrance   *rawPoint         `json:"entrance"`
	Trigger    uint32            `json:"entrance_trigger"`
	Tele       string            `json:"entrance_tele"`
	Outside    string            `json:"outside_tele"`
	Portal     *rawPortal        `json:"portal"`
	Band       []int             `json:"level_band"`
	Hire       []string          `json:"hire_composition"`
	CCPrio     []uint32          `json:"cc_priority"`
	CCAlways   []uint32          `json:"cc_always"`
	NoCC       []uint32          `json:"no_cc"`
	KillFirst  []uint32          `json:"kill_first"`
	SummonedKF []uint32          `json:"summoned_kill_first"`
	Notes      []string          `json:"notes"`
	Watchdog   *WatchdogConfig   `json:"watchdog"`
	Legs       []json.RawMessage `json:"legs"`
}

type rawPortal struct {
	Map     *uint32 `json:"map"`
	X, Y, Z *float32
}

type rawWaypoint struct {
	X, Y, Z   *float32
	Direct    bool   `json:"direct"`
	CheckSkip string `json:"check_skip"`
}

type rawLeg struct {
	Name       string            `json:"name"`
	To         *rawPoint         `json:"to"`
	Via        []rawWaypoint     `json:"via"`
	PullGroups []rawPullGroup    `json:"pull_groups"`
	Skip       []uint32          `json:"skip"`
	Steps      []json.RawMessage `json:"steps"`
	Boss       *rawBoss          `json:"boss"`
	AfterBoss  []json.RawMessage `json:"after_boss"`
	// CompanionsLoot is Leg.CompanionsLoot.
	CompanionsLoot *bool        `json:"companions_loot"`
	Leftover       []uint32     `json:"leftover_entries"`
	Optional       *OptionalLeg `json:"optional"`
	Rest           *RestSpec    `json:"rest"`
	Notes          []string     `json:"notes"`
	NeedsWalk      bool         `json:"needs_walk"`
}

type rawPullGroup struct {
	Entries   []uint32   `json:"entries"`
	Positions []rawPoint `json:"positions"`
	Ambush    bool       `json:"ambush"`
	Radius    float32    `json:"radius"`
	Team      string     `json:"team"`
	Equiv     string     `json:"equiv"`
	NeedsWalk bool       `json:"needs_walk"`
	Patrol    *rawPatrol `json:"patrol"`
}

type rawPatrol struct {
	Entries []uint32   `json:"entries"`
	Path    []rawPoint `json:"path"`
	PeriodS float32    `json:"period_s"`
	// PeriodEstimate is Patrol.PeriodEstimate.
	PeriodEstimate bool   `json:"period_estimate"`
	Source         string `json:"source"`
}

type rawBoss struct {
	Entry    uint32    `json:"entry"`
	Position *rawPoint `json:"position"`
	First    []uint32  `json:"first"`
	Team     string    `json:"team"`
	LootItem *rawItem  `json:"loot_item"`
	// CompanionsLoot is Boss.CompanionsLoot.
	CompanionsLoot *bool `json:"companions_loot"`
	// Tactics is Boss.Tactics.
	Tactics   *tactics.Tactics `json:"tactics"`
	NeedsWalk bool             `json:"needs_walk"`
}

type rawStep struct {
	UseObject   *rawUse     `json:"use_object"`
	WaitFor     *rawWait    `json:"wait_for"`
	LootObject  *rawLoot    `json:"loot_object"`
	UseItem     *rawUseItem `json:"use_item"`
	Ritual      *rawRitual  `json:"ritual"`
	AreaTrigger *rawTrigger `json:"area_trigger"`
	Gossip      *rawGossip  `json:"gossip"`
}

type rawTrigger struct {
	ID       uint32    `json:"id"`
	Center   *rawPoint `json:"center"`
	Radius   *float32  `json:"radius"`
	Stand    *rawPoint `json:"stand"`
	By       string    `json:"by"`
	Expect   string    `json:"expect"`
	TimeoutS *int      `json:"timeout_s"`
}

type rawGossip struct {
	NPC      uint32    `json:"npc"`
	Near     *rawPoint `json:"near"`
	Option   *int      `json:"option"`
	Text     string    `json:"text"`
	By       string    `json:"by"`
	Stand    *rawPoint `json:"stand"`
	RetryS   *int      `json:"retry_s"`
	FailText string    `json:"fail_text"`
}

type rawUnitWait struct {
	Entries []uint32  `json:"entries"`
	Near    *rawPoint `json:"near"`
	Radius  *float32  `json:"radius"`
	State   string    `json:"state"`
}

type rawRitual struct {
	Entry        uint32    `json:"entry"`
	Position     *rawPos   `json:"position"`
	Participants *int      `json:"participants"`
	Stand        *rawPoint `json:"stand"`
}

// rawPos is a point written as [x, y, z] or as {"x":..,"y":..,"z":..}.
type rawPos struct{ rawPoint }

func (p *rawPos) UnmarshalJSON(b []byte) error {
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '[' {
		var a []*float32
		if err := json.Unmarshal(t, &a); err != nil {
			return err
		}
		if len(a) != 3 || a[0] == nil || a[1] == nil || a[2] == nil {
			return errors.New("a position array is [x, y, z]: three numbers")
		}
		p.X, p.Y, p.Z = a[0], a[1], a[2]
		return nil
	}
	return strict(b, &p.rawPoint)
}

type rawLoot struct {
	rawRef
	Stand      *rawPoint `json:"stand"`
	ExpectItem *rawItem  `json:"expect_item"`
	Batch      string    `json:"batch"`
}

type rawUseItem struct {
	Entry        uint32   `json:"entry"`
	Name         string   `json:"name"`
	RequiresItem *rawItem `json:"requires_item"`
	ExpectItem   *rawItem `json:"expect_item"`
}

type rawRef struct {
	Entry uint32    `json:"entry"`
	Near  *rawPoint `json:"near"`
	Range *float32  `json:"range"`
	Spawn uint32    `json:"spawn"`
}

type rawUse struct {
	rawRef
	Stand        *rawPoint `json:"stand"`
	RequiresItem *rawItem  `json:"requires_item"`
	ViaItem      bool      `json:"via_item"`
}

type rawItem struct {
	Entry uint32 `json:"entry"`
	Name  string `json:"name"`
}

type rawWait struct {
	GameObject   *rawObjState `json:"gameobject"`
	HostilesGone *rawHostiles `json:"hostiles_gone"`
	Unit         *rawUnitWait `json:"unit"`
	TimeoutS     *int         `json:"timeout_s"`
	FailText     string       `json:"fail_text"`
}

type rawObjState struct {
	rawRef
	State string `json:"state"`
}

type rawHostiles struct {
	Radius *float32  `json:"radius"`
	Center *rawPoint `json:"center"`
}

// checker collects the errors of one file, each with the path of the field.
type checker struct {
	file string
	errs []error
}

func (c *checker) fail(path, format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf("%s: %s: %s", c.file, path, fmt.Sprintf(format, args...)))
}

// Load reads and validates the plan file at path.
func Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, data)
}

// Parse validates a plan; name is used in the error messages. The error lists every problem found, one per line,
// each with the path of the field (legs[2].steps[0].use_object.entry) so the file can be fixed in one pass.
func Parse(name string, data []byte) (*Plan, error) {
	c := &checker{file: name}
	var rp rawPlan
	if err := strict(data, &rp); err != nil {
		if !strings.HasPrefix(err.Error(), "json: unknown field ") {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		// An unknown field stops the strict decoder where it stands: report it, then read the file again
		// without the check so the other mistakes are listed with it.
		c.errs = append(c.errs, fmt.Errorf("%s: %w", name, err))
		rp = rawPlan{}
		if err := json.Unmarshal(data, &rp); err != nil {
			return nil, errors.Join(c.errs...)
		}
	}
	p := &Plan{Name: rp.Name, Notes: rp.Notes}
	switch {
	case rp.Version == nil:
		c.fail("version", "required (this build reads version %d)", Version)
	case *rp.Version != Version:
		c.fail("version", "%d is not supported (this build reads version %d)", *rp.Version, Version)
	default:
		p.Version = *rp.Version
	}
	if strings.TrimSpace(p.Name) == "" {
		c.fail("name", "required")
	}
	if rp.Map == nil {
		c.fail("map", "required (the instance map id)")
	} else {
		p.Map = *rp.Map
	}
	c.kindStatus(p, &rp)
	p.Entrance = c.point("entrance", rp.Entrance)
	p.EntranceTrigger = rp.Trigger
	if rp.Tele != "" && !teleName.MatchString(rp.Tele) {
		c.fail("entrance_tele", "%q is not a game_tele name (letters, digits and _ only)", rp.Tele)
	} else {
		p.EntranceTele = rp.Tele
	}
	if rp.Outside != "" && !teleName.MatchString(rp.Outside) {
		c.fail("outside_tele", "%q is not a game_tele name (letters, digits and _ only)", rp.Outside)
	} else {
		p.OutsideTele = rp.Outside
	}
	switch {
	case len(rp.Band) == 0:
	case len(rp.Band) != 2 || rp.Band[0] < 1 || rp.Band[1] > 60 || rp.Band[0] > rp.Band[1]:
		c.fail("level_band", "%v: use [low, high] with 1 <= low <= high <= 60", rp.Band)
	default:
		p.LevelBand = []int{rp.Band[0], rp.Band[1]}
	}
	if len(rp.CCPrio) > 0 {
		p.CCPriority = slices.Clone(rp.CCPrio)
	}
	if len(rp.CCAlways) > 0 {
		p.CCAlways = slices.Clone(rp.CCAlways)
	}
	for _, e := range append(slices.Clone(rp.CCPrio), rp.CCAlways...) {
		if slices.Contains(rp.KillFirst, e) {
			c.fail("kill_first", "%d is also in cc_priority or cc_always: a kill_first entry is killed first and never crowd-controlled; take it out of the CC lists", e)
		}
	}
	c.entries("no_cc", rp.NoCC, false)
	if len(rp.NoCC) > 0 {
		p.NoCC = slices.Clone(rp.NoCC)
	}
	if len(rp.KillFirst) > 0 {
		p.KillFirst = slices.Clone(rp.KillFirst)
	}
	for _, l := range []struct {
		name string
		es   []uint32
	}{{"kill_first", rp.KillFirst}, {"summoned_kill_first", rp.SummonedKF}} {
		seen := map[uint32]bool{}
		for _, e := range l.es {
			if e == 0 {
				c.fail(l.name, "0 is not a creature entry")
			} else if seen[e] {
				c.fail(l.name, "%d is listed twice", e)
			}
			seen[e] = true
		}
	}
	for _, e := range rp.SummonedKF {
		if !slices.Contains(rp.KillFirst, e) {
			c.fail("summoned_kill_first", "%d is not in kill_first", e)
		}
	}
	if len(rp.SummonedKF) > 0 {
		p.SummonedKillFirst = slices.Clone(rp.SummonedKF)
	}
	if rp.Portal != nil {
		if rp.Portal.Map == nil {
			c.fail("portal.map", "required (the outer map id)")
		} else {
			p.Portal = &Portal{Map: *rp.Portal.Map, Point: c.point("portal", &rawPoint{rp.Portal.X, rp.Portal.Y, rp.Portal.Z})}
		}
	}
	if rp.Watchdog != nil {
		if err := rp.Watchdog.Validate(); err != nil {
			c.fail("watchdog", "%v", err)
		} else {
			p.Watchdog = rp.Watchdog
		}
	}
	if len(rp.Legs) == 0 {
		c.fail("legs", "needs at least one leg")
	}
	for i, rl := range rp.Legs {
		path := fmt.Sprintf("legs[%d]", i)
		var l rawLeg
		if err := strict(rl, &l); err != nil {
			c.fail(path, "%v", err)
			continue
		}
		p.Legs = append(p.Legs, c.leg(path, &l))
	}
	if len(c.errs) > 0 {
		return nil, errors.Join(c.errs...)
	}
	return p, nil
}

// kindStatus checks and copies the kind, status, hire_composition and raid fields.
func (c *checker) kindStatus(p *Plan, rp *rawPlan) {
	switch rp.Kind {
	case "", KindDungeon, KindRaid:
		p.Kind = rp.Kind
	default:
		c.fail("kind", "%q: use %q or %q", rp.Kind, KindDungeon, KindRaid)
	}
	if rp.Status != "" {
		word, _, _ := strings.Cut(rp.Status, " ")
		if !slices.Contains([]string{StatusDraft, StatusMapped, StatusLiveChecked}, word) {
			c.fail("status", "%q: start with %s, %s or %s", rp.Status, StatusDraft, StatusMapped, StatusLiveChecked)
		} else {
			p.Status = rp.Status
		}
	}
	c.entries("fleers", rp.Fleers, false)
	p.Fleers = rp.Fleers
	if len(rp.Hire) > 0 {
		if rp.Kind == KindRaid {
			c.fail("hire_composition", "a raid plan has a raid block instead")
		} else if _, err := (&HireSpec{Composition: rp.Hire}).slots(); err != nil {
			c.fail("hire_composition", "%v", strings.TrimPrefix(err.Error(), "prepare.hire.composition: "))
		} else {
			p.HireComposition = rp.Hire
		}
	}
	switch {
	case rp.Raid == nil && rp.Kind == KindRaid:
		c.fail("raid", "required for a raid plan: {\"size\": 40, \"tanks\": .., \"healers\": .., \"dps\": ..}")
	case rp.Raid != nil && rp.Kind != KindRaid:
		c.fail("raid", "only a plan of kind %q has a raid block", KindRaid)
	case rp.Raid != nil:
		r := *rp.Raid
		switch {
		case !slices.Contains([]int{10, 20, 40}, r.Size):
			c.fail("raid.size", "%d: use 10, 20 or 40", r.Size)
		case r.Tanks < 1 || r.Healers < 1 || r.DPS < 1:
			c.fail("raid", "needs at least one tank, one healer and one damage dealer")
		case r.Tanks+r.Healers+r.DPS != r.Size:
			c.fail("raid", "%d tanks + %d healers + %d dps is %d, the size is %d", r.Tanks, r.Healers, r.DPS, r.Tanks+r.Healers+r.DPS, r.Size)
		default:
			p.Raid = &r
		}
	}
}

// IsRaid says whether the plan is a raid plan.
func (p *Plan) IsRaid() bool { return p.Kind == KindRaid }

// strict decodes JSON that must hold exactly one value of v's shape and no unknown field.
func strict(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("more than one JSON value")
	}
	return nil
}

// teleName is what a game_tele name may be here: it goes into a GM command line.
var teleName = regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)

func (c *checker) point(path string, p *rawPoint) Point {
	if p == nil {
		c.fail(path, "required ({\"x\":..,\"y\":..,\"z\":..})")
		return Point{}
	}
	var out Point
	for _, f := range []struct {
		name string
		in   *float32
		out  *float32
	}{{"x", p.X, &out.X}, {"y", p.Y, &out.Y}, {"z", p.Z, &out.Z}} {
		switch {
		case f.in == nil:
			c.fail(path+"."+f.name, "required")
		case math.IsNaN(float64(*f.in)) || math.IsInf(float64(*f.in), 0) || math.Abs(float64(*f.in)) > 20000:
			c.fail(path+"."+f.name, "%v is not a map coordinate", *f.in)
		default:
			*f.out = *f.in
		}
	}
	return out
}

func (c *checker) entries(path string, es []uint32, need bool) {
	if need && len(es) == 0 {
		c.fail(path, "needs at least one entry")
	}
	for i, e := range es {
		if e == 0 {
			c.fail(fmt.Sprintf("%s[%d]", path, i), "0 is no creature entry")
		}
	}
}

func (c *checker) leg(path string, r *rawLeg) Leg {
	l := Leg{Name: r.Name, Skip: r.Skip, Notes: r.Notes, NeedsWalk: r.NeedsWalk, CompanionsLoot: r.CompanionsLoot, LeftoverEntries: r.Leftover}
	for i, e := range r.Leftover {
		if e == 0 {
			c.fail(fmt.Sprintf("%s.leftover_entries[%d]", path, i), "0 is not a creature entry")
		}
	}
	l.To = c.point(path+".to", r.To)
	if o := r.Optional; o != nil {
		l.Optional = &OptionalLeg{Item: o.Item, Name: o.Name}
		if o.Item == 0 {
			c.fail(path+".optional.item", "required (the item entry the leg needs)")
		}
	}
	for i, w := range r.Via {
		wp := fmt.Sprintf("%s.via[%d]", path, i)
		pt := c.point(wp, &rawPoint{X: w.X, Y: w.Y, Z: w.Z})
		l.Via = append(l.Via, Waypoint{Point: pt, Direct: w.Direct, CheckSkip: w.CheckSkip})
		if !w.Direct {
			continue
		}
		if i == 0 {
			c.fail(wp, "a direct step needs a via point before it, where the party gathers")
			continue
		}
		if prev := l.Via[i-1].Point; !DirectStepOK(prev, pt) {
			c.fail(wp, "the direct step from via[%d] is %.1f yd across and %.1f yd up or down; the limits are %g and %g", i-1, hdist(prev, pt), math.Abs(float64(pt.Z-prev.Z)), DirectMaxDistance, DirectMaxVertical)
		}
	}
	for i, g := range r.PullGroups {
		gp := fmt.Sprintf("%s.pull_groups[%d]", path, i)
		pg := PullGroup{Entries: g.Entries, Ambush: g.Ambush, Radius: g.Radius, Team: g.Team, Equiv: g.Equiv, NeedsWalk: g.NeedsWalk}
		c.team(gp+".team", g.Team)
		if len(g.Equiv) > MaxEquivLen || strings.TrimSpace(g.Equiv) != g.Equiv {
			c.fail(gp+".equiv", "%q: a label of at most %d characters, with no space around it", g.Equiv, MaxEquivLen)
		}
		if g.Radius < 0 || g.Radius > 60 {
			c.fail(gp+".radius", "%g: use 0 (the default) or 1-60 yards", g.Radius)
		}
		c.entries(gp+".entries", g.Entries, true)
		if len(g.Positions) == 0 {
			c.fail(gp+".positions", "needs at least one position")
		}
		if len(g.Positions) > len(g.Entries) && len(g.Entries) > 0 {
			c.fail(gp+".positions", "%d positions for %d entries: one per member at most", len(g.Positions), len(g.Entries))
		}
		for j := range g.Positions {
			pg.Positions = append(pg.Positions, c.point(fmt.Sprintf("%s.positions[%d]", gp, j), &g.Positions[j]))
		}
		if rp := g.Patrol; rp != nil {
			pp := gp + ".patrol"
			pat := &Patrol{Entries: rp.Entries, PeriodS: rp.PeriodS, PeriodEstimate: rp.PeriodEstimate, Source: rp.Source}
			c.entries(pp+".entries", rp.Entries, true)
			if len(rp.Path) < 2 {
				c.fail(pp+".path", "needs at least two points")
			}
			for j := range rp.Path {
				pat.Path = append(pat.Path, c.point(fmt.Sprintf("%s.path[%d]", pp, j), &rp.Path[j]))
			}
			if rp.PeriodS <= 0 || rp.PeriodS > maxPatrolPeriod {
				c.fail(pp+".period_s", "%g: one round of the path takes 1-%d seconds", rp.PeriodS, maxPatrolPeriod)
			}
			pg.Patrol = pat
		}
		l.PullGroups = append(l.PullGroups, pg)
	}
	c.entries(path+".skip", r.Skip, false)
	for i, e := range r.Skip {
		for j, g := range r.PullGroups {
			if slices.Contains(g.Entries, e) {
				c.fail(fmt.Sprintf("%s.skip[%d]", path, i), "%d is also in pull_groups[%d].entries: the leg would pull it and never wait for it", e, j)
			}
		}
		if r.Boss != nil && slices.Contains(r.Boss.First, e) {
			c.fail(fmt.Sprintf("%s.skip[%d]", path, i), "%d is also in boss.first: the leg would kill it and never wait for it", e)
		}
	}
	if r.Boss != nil {
		bp := path + ".boss"
		b := Boss{Entry: r.Boss.Entry, First: r.Boss.First, Team: r.Boss.Team, CompanionsLoot: r.Boss.CompanionsLoot, NeedsWalk: r.Boss.NeedsWalk}
		c.team(bp+".team", r.Boss.Team)
		b.LootItem = c.item(bp+".loot_item", r.Boss.LootItem)
		if b.Entry == 0 {
			c.fail(bp+".entry", "required")
		}
		b.Position = c.point(bp+".position", r.Boss.Position)
		c.entries(bp+".first", b.First, false)
		for i, e := range b.First {
			if e == b.Entry && e != 0 {
				c.fail(fmt.Sprintf("%s.first[%d]", bp, i), "the boss (%d) is killed last; leave it out of first", e)
			}
		}
		for i, e := range l.Skip {
			if e == b.Entry && e != 0 {
				c.fail(fmt.Sprintf("%s.skip[%d]", path, i), "skips the boss (%d)", e)
			}
		}
		if r.Boss.Tactics != nil {
			if err := r.Boss.Tactics.Validate(spells.HasName); err != nil {
				c.fail(bp+".tactics", "%v", err)
			}
			if !r.Boss.Tactics.Empty() {
				b.Tactics = r.Boss.Tactics
			}
		}
		if r.CompanionsLoot != nil && !*r.CompanionsLoot && b.LootItem != nil {
			c.fail(bp+".loot_item", "the leg has companions_loot false (every bot's looting off for the whole leg) and the leader must loot the boss's corpse for it: the two cannot be run together; use the boss's own companions_loot false instead")
		}
		l.Boss = &b
	}
	for i, rs := range r.Steps {
		sp := fmt.Sprintf("%s.steps[%d]", path, i)
		var s rawStep
		if err := strict(rs, &s); err != nil {
			c.fail(sp, "%v", err)
			continue
		}
		l.Steps = append(l.Steps, c.step(sp, &s))
	}
	c.batches(path, l.Steps)
	for i, rs := range r.AfterBoss {
		sp := fmt.Sprintf("%s.after_boss[%d]", path, i)
		var s rawStep
		if err := strict(rs, &s); err != nil {
			c.fail(sp, "%v", err)
			continue
		}
		l.AfterBoss = append(l.AfterBoss, c.step(sp, &s))
	}
	if o := l.Optional; o != nil && o.Item != 0 && !slices.ContainsFunc(append(slices.Clone(l.Steps), l.AfterBoss...), func(st Step) bool {
		return st.UseObject != nil && st.UseObject.RequiresItem != nil && st.UseObject.RequiresItem.Entry == o.Item
	}) {
		c.fail(path+".optional.item", "%d is required by no use_object step of the leg (requires_item): the leg would be skipped for an item it never uses", o.Item)
	}
	if rs := r.Rest; rs != nil {
		for _, f := range []struct {
			name string
			v    int
		}{{"health", rs.Health}, {"healer_mana", rs.HealerMana}, {"mana", rs.Mana}, {"walk_health", rs.WalkHealth}, {"walk_healer_mana", rs.WalkHealerMana}, {"walk_mana", rs.WalkMana}} {
			if f.v < 0 || f.v > 100 {
				c.fail(path+".rest."+f.name, "%d is outside 1-100 percent (omit it for the default)", f.v)
			}
		}
		if rs.MaxS < 0 || rs.MaxS > MaxTimeoutS {
			c.fail(path+".rest.max_s", "%d s is outside 1-%d (omit it for the default)", rs.MaxS, MaxTimeoutS)
		}
		if rs.WalkYd < -1 || rs.WalkYd > 5000 {
			c.fail(path+".rest.walk_yd", "%d yd is outside 1-5000 (-1 turns walking rests off, omit it for the default)", rs.WalkYd)
		}
		v := *rs
		l.Rest = &v
	}
	if len(l.PullGroups) == 0 && len(l.Steps) == 0 && len(l.AfterBoss) == 0 && l.Boss == nil {
		c.fail(path, "has no pull_groups, steps, boss or after_boss: it would only walk to \"to\"; fold it into the next leg or add one")
	}
	return l
}

func (c *checker) ref(path string, r *rawRef) ObjectRef {
	o := ObjectRef{Entry: r.Entry, Spawn: r.Spawn}
	if r.Spawn != 0 && r.Entry == 0 {
		c.fail(path+".spawn", "needs the entry as well (the spawn guid is of an object of that entry)")
	}
	if r.Near != nil {
		p := c.point(path+".near", r.Near)
		o.Near = &p
	}
	if r.Entry == 0 && r.Near == nil {
		c.fail(path, "needs an entry, a near position, or both")
	}
	if r.Range != nil {
		if r.Near == nil {
			c.fail(path+".range", "only means something with near")
		}
		if *r.Range <= 0 || *r.Range > MaxRadius {
			c.fail(path+".range", "%v yards is outside 0-%d", *r.Range, MaxRadius)
		} else {
			o.Range = *r.Range
		}
	}
	return o
}

// team checks a team field: empty, "alliance" or "horde".
func (c *checker) team(path, team string) {
	if team != "" && team != TeamAlliance && team != TeamHorde {
		c.fail(path, "%q is neither %q nor %q", team, TeamAlliance, TeamHorde)
	}
}

// item checks an optional item reference and returns it; nil when absent.
func (c *checker) item(path string, r *rawItem) *ItemRef {
	if r == nil {
		return nil
	}
	if r.Entry == 0 {
		c.fail(path+".entry", "required (the item entry)")
	}
	return &ItemRef{Entry: r.Entry, Name: r.Name}
}

func (c *checker) step(path string, r *rawStep) Step {
	var s Step
	n := 0
	for _, set := range []bool{r.UseObject != nil, r.WaitFor != nil, r.LootObject != nil, r.UseItem != nil, r.Ritual != nil, r.AreaTrigger != nil, r.Gossip != nil} {
		if set {
			n++
		}
	}
	switch {
	case n > 1:
		c.fail(path, "has more than one of use_object, wait_for, loot_object, use_item, ritual, area_trigger and gossip: one step does one thing")
	case n == 0:
		c.fail(path, "needs use_object, wait_for, loot_object, use_item, ritual, area_trigger or gossip")
	case r.UseObject != nil:
		u := &UseObject{ObjectRef: c.ref(path+".use_object", &r.UseObject.rawRef)}
		if r.UseObject.Stand != nil {
			p := c.point(path+".use_object.stand", r.UseObject.Stand)
			u.Stand = &p
		}
		u.RequiresItem = c.item(path+".use_object.requires_item", r.UseObject.RequiresItem)
		if u.ViaItem = r.UseObject.ViaItem; u.ViaItem {
			switch {
			case u.RequiresItem == nil:
				c.fail(path+".use_object.via_item", "needs requires_item: the item that is used on the object")
			case u.Stand == nil || u.Near == nil:
				c.fail(path+".use_object.via_item", "needs near and stand: the item's spell has a short range, so the stand must be within %g yd of the object", UseRange)
			case dist3(*u.Stand, *u.Near) > UseRange:
				c.fail(path+".use_object.stand", "is %.1f yd from near; with via_item the stand must be within %g yd (the item's spell range)", dist3(*u.Stand, *u.Near), UseRange)
			}
		}
		s.UseObject = u
	case r.LootObject != nil:
		l := &LootObject{ObjectRef: c.ref(path+".loot_object", &r.LootObject.rawRef)}
		if r.LootObject.Stand != nil {
			p := c.point(path+".loot_object.stand", r.LootObject.Stand)
			l.Stand = &p
		}
		l.ExpectItem = c.item(path+".loot_object.expect_item", r.LootObject.ExpectItem)
		l.Batch = r.LootObject.Batch
		s.LootObject = l
	case r.UseItem != nil:
		u := &UseItem{Entry: r.UseItem.Entry, Name: r.UseItem.Name}
		if u.Entry == 0 {
			c.fail(path+".use_item.entry", "required (the item entry)")
		}
		u.RequiresItem = c.item(path+".use_item.requires_item", r.UseItem.RequiresItem)
		u.ExpectItem = c.item(path+".use_item.expect_item", r.UseItem.ExpectItem)
		s.UseItem = u
	case r.Ritual != nil:
		s.Ritual = c.ritual(path+".ritual", r.Ritual)
	case r.AreaTrigger != nil:
		s.AreaTrigger = c.trigger(path+".area_trigger", r.AreaTrigger)
	case r.Gossip != nil:
		s.Gossip = c.gossip(path+".gossip", r.Gossip)
	default:
		s.WaitFor = c.wait(path+".wait_for", r.WaitFor)
	}
	return s
}

func (c *checker) trigger(path string, r *rawTrigger) *AreaTrigger {
	t := &AreaTrigger{ID: r.ID, By: r.By, TimeoutS: DefaultTimeoutS}
	if r.ID == 0 {
		c.fail(path+".id", "required (the area trigger's id, as in areatrigger_teleport or the script's table)")
	}
	t.Center = c.point(path+".center", r.Center)
	t.Stand = c.point(path+".stand", r.Stand)
	switch {
	case r.Radius == nil:
		c.fail(path+".radius", "required (yards)")
	case *r.Radius <= 0 || *r.Radius > MaxRadius:
		c.fail(path+".radius", "%v yards is outside 0-%d", *r.Radius, MaxRadius)
	default:
		t.Radius = *r.Radius
	}
	switch r.By {
	case "":
		t.By = TriggerAuto
	case TriggerAuto, TriggerBot, TriggerAgent:
	default:
		c.fail(path+".by", "%q is none of %s, %s, %s", r.By, TriggerAuto, TriggerBot, TriggerAgent)
	}
	if r.Expect == "" {
		c.fail(path+".expect", "required (what the trigger changes, so that the step can tell it worked: \"unit 7271 hostile\")")
	} else if u, err := parseExpect(r.Expect); err != nil {
		c.fail(path+".expect", "%v", err)
	} else {
		t.Expect, t.ExpectUnit = u.String(), u
	}
	if r.TimeoutS != nil {
		if *r.TimeoutS <= 0 || *r.TimeoutS > MaxTimeoutS {
			c.fail(path+".timeout_s", "%d s is outside 1-%d", *r.TimeoutS, MaxTimeoutS)
		} else {
			t.TimeoutS = *r.TimeoutS
		}
	}
	// A stand point outside the zone takes nothing: the loader can say so from the numbers alone.
	if t.Radius > 0 && r.Center != nil && r.Stand != nil {
		if d := dist3(t.Stand, t.Center); d > float64(t.Radius) {
			c.fail(path+".stand", "is %.1f yd from the centre, outside the radius %g: standing there takes no trigger", d, t.Radius)
		}
	}
	return t
}

func (c *checker) ritual(path string, r *rawRitual) *Ritual {
	out := &Ritual{Entry: r.Entry}
	if r.Entry == 0 {
		c.fail(path+".entry", "required (the entry of the summoning ritual object)")
	}
	if r.Position == nil {
		c.fail(path+".position", "required ([x, y, z]: the server finds the object by its entry near this point)")
	} else {
		out.Position = c.point(path+".position", &r.Position.rawPoint)
	}
	switch {
	case r.Participants == nil:
		c.fail(path+".participants", "required (the bots that use the object: %d-%d; the object's own number is in its template)", MinRitualParticipants, MaxRitualParticipants)
	case *r.Participants < MinRitualParticipants || *r.Participants > MaxRitualParticipants:
		c.fail(path+".participants", "%d is outside %d-%d", *r.Participants, MinRitualParticipants, MaxRitualParticipants)
	default:
		out.Participants = *r.Participants
	}
	if r.Stand != nil {
		p := c.point(path+".stand", r.Stand)
		out.Stand = &p
	}
	return out
}

func (c *checker) gossip(path string, r *rawGossip) *Gossip {
	out := &Gossip{NPC: r.NPC, By: r.By}
	if r.NPC == 0 {
		c.fail(path+".npc", "required (the entry of the creature to talk to)")
	}
	if r.Near == nil {
		c.fail(path+".near", "required ({\"x\":..,\"y\":..,\"z\":..}: where the NPC stands; the one of that entry nearest to it is talked to)")
	} else {
		out.Near = c.point(path+".near", r.Near)
	}
	switch {
	case r.Option != nil && r.Text != "":
		c.fail(path, "has both option and text: one step picks one option")
	case r.Option == nil && r.Text == "":
		c.fail(path, "needs option (the menu's index) or text (a part of the option's line)")
	case r.Option != nil:
		if *r.Option < 0 || *r.Option > MaxGossipOption {
			c.fail(path+".option", "%d is outside 0-%d", *r.Option, MaxGossipOption)
		} else {
			o := uint32(*r.Option)
			out.Option = &o
		}
	default:
		if len(r.Text) > MaxGossipText || strings.TrimSpace(r.Text) != r.Text || strings.ContainsAny(r.Text, "\r\n\t") {
			c.fail(path+".text", "is longer than %d characters, has a line break, or has spaces around it", MaxGossipText)
		} else {
			out.Text = r.Text
		}
	}
	switch r.By {
	case "", GossipByAgent:
	case GossipByCarrier, GossipByTank:
		// The order is one command line: every word of a text option must be one argument the commander accepts.
		if out.Text != "" {
			if bad := tbm.BadOrderWords(out.Text); bad != "" {
				c.fail(path+".text", "%q cannot be carried in the order `cmd gossip` (by %s): %s; use option (the index) or by \"agent\"", out.Text, r.By, bad)
			} else if strings.Trim(out.Text, "0123456789") == "" {
				c.fail(path+".text", "%q is a number: the order `cmd gossip` would read it as the option's index; use option", out.Text)
			}
		}
	default:
		c.fail(path+".by", "%q: use %q, %q or %q", r.By, GossipByAgent, GossipByCarrier, GossipByTank)
	}
	if r.Stand != nil {
		p := c.point(path+".stand", r.Stand)
		out.Stand = &p
	}
	if r.RetryS != nil {
		if *r.RetryS <= 0 || *r.RetryS > MaxGossipRetryS {
			c.fail(path+".retry_s", "%d s is outside 1-%d (omit it for %d)", *r.RetryS, MaxGossipRetryS, DefaultGossipRetryS)
		} else {
			out.RetryS = *r.RetryS
		}
	}
	if len(r.FailText) > MaxFailText || strings.ContainsAny(r.FailText, "\r\n\t") {
		c.fail(path+".fail_text", "is longer than %d characters or has a line break", MaxFailText)
	} else {
		out.FailText = r.FailText
	}
	return out
}

func (c *checker) wait(path string, r *rawWait) *WaitFor {
	w := &WaitFor{TimeoutS: DefaultTimeoutS}
	if len(r.FailText) > MaxFailText || strings.ContainsAny(r.FailText, "\r\n\t") {
		c.fail(path+".fail_text", "is longer than %d characters or has a line break", MaxFailText)
	} else {
		w.FailText = r.FailText
	}
	if r.TimeoutS != nil {
		if *r.TimeoutS <= 0 || *r.TimeoutS > MaxTimeoutS {
			c.fail(path+".timeout_s", "%d s is outside 1-%d", *r.TimeoutS, MaxTimeoutS)
		} else {
			w.TimeoutS = *r.TimeoutS
		}
	}
	switch {
	case r.GameObject != nil && r.HostilesGone != nil:
		c.fail(path, "has both gameobject and hostiles_gone: one wait waits for one thing")
	case r.Unit != nil && (r.GameObject != nil || r.HostilesGone != nil):
		c.fail(path, "has unit and another condition: one wait waits for one thing")
	case r.GameObject == nil && r.HostilesGone == nil && r.Unit == nil:
		c.fail(path, "needs gameobject, hostiles_gone or unit")
	case r.Unit != nil:
		w.Unit = c.unitWait(path+".unit", r.Unit)
	case r.GameObject != nil:
		gp := path + ".gameobject"
		g := &ObjectState{ObjectRef: c.ref(gp, &r.GameObject.rawRef), State: r.GameObject.State}
		if !validState(g.State) {
			c.fail(gp+".state", "%q is none of %s", g.State, strings.Join(ObjectStates, ", "))
		}
		w.GameObject = g
	default:
		hp := path + ".hostiles_gone"
		h := &HostilesGone{}
		switch {
		case r.HostilesGone.Radius == nil:
			c.fail(hp+".radius", "required")
		case *r.HostilesGone.Radius <= 0 || *r.HostilesGone.Radius > MaxRadius:
			c.fail(hp+".radius", "%v yards is outside 0-%d", *r.HostilesGone.Radius, MaxRadius)
		default:
			h.Radius = *r.HostilesGone.Radius
		}
		if r.HostilesGone.Center != nil {
			p := c.point(hp+".center", r.HostilesGone.Center)
			h.Center = &p
		}
		w.HostilesGone = h
	}
	return w
}

func (c *checker) unitWait(path string, r *rawUnitWait) *UnitWait {
	u := &UnitWait{Entries: r.Entries, State: r.State}
	c.entries(path+".entries", r.Entries, true)
	if !slices.Contains(UnitStates, r.State) {
		c.fail(path+".state", "%q is none of %s", r.State, strings.Join(UnitStates, ", "))
	}
	if r.Near != nil {
		p := c.point(path+".near", r.Near)
		u.Near = &p
	}
	if r.Radius != nil {
		switch {
		case r.Near == nil:
			c.fail(path+".radius", "only means something with near")
		case *r.Radius <= 0 || *r.Radius > MaxRadius:
			c.fail(path+".radius", "%v yards is outside 0-%d", *r.Radius, MaxRadius)
		default:
			u.Radius = *r.Radius
		}
	}
	return u
}

func validState(s string) bool {
	for _, v := range ObjectStates {
		if v == s {
			return true
		}
	}
	return false
}

// batchPoint is where a loot_object step's chest lies: its near position, else its stand.
func batchPoint(l *LootObject) (Point, bool) {
	switch {
	case l.Near != nil:
		return *l.Near, true
	case l.Stand != nil:
		return *l.Stand, true
	}
	return Point{}, false
}

// BatchCentre is the mean of the points.
func BatchCentre(pts []Point) Point {
	var c Point
	for _, p := range pts {
		c.X += p.X
		c.Y += p.Y
		c.Z += p.Z
	}
	if n := float32(len(pts)); n > 0 {
		c.X, c.Y, c.Z = c.X/n, c.Y/n, c.Z/n
	}
	return c
}

// BatchSpread is the largest distance (in the plane) from the points' centre to one of them.
func BatchSpread(pts []Point) float64 {
	c := BatchCentre(pts)
	var worst float64
	for _, p := range pts {
		worst = max(worst, math.Hypot(float64(p.X-c.X), float64(p.Y-c.Y)))
	}
	return worst
}

// batches checks the loot_object batches of one step list: a batch is a run of consecutive steps with the same id, of BatchMinSize to
// BatchMaxSize chests that all lie within BatchRadius yards of their centre; an id does not come back after another step.
func (c *checker) batches(path string, steps []Step) {
	closed := map[string]bool{}
	for i := 0; i < len(steps); {
		l := steps[i].LootObject
		if l == nil || l.Batch == "" {
			i++
			continue
		}
		id, j := l.Batch, i
		var pts []Point
		for ; j < len(steps) && steps[j].LootObject != nil && steps[j].LootObject.Batch == id; j++ {
			p, ok := batchPoint(steps[j].LootObject)
			if !ok {
				c.fail(fmt.Sprintf("%s.steps[%d].loot_object", path, j), "a batch step needs near or stand (the batch's centre is made of them)")
			}
			pts = append(pts, p)
		}
		bp := fmt.Sprintf("%s.steps[%d].loot_object.batch", path, i)
		switch {
		case closed[id]:
			c.fail(bp, "batch %q comes back after other steps: the steps of one batch are next to each other", id)
		case j-i < BatchMinSize || j-i > BatchMaxSize:
			c.fail(bp, "batch %q has %d chests: a batch has %d to %d", id, j-i, BatchMinSize, BatchMaxSize)
		default:
			if w := BatchSpread(pts); w > BatchRadius {
				c.fail(bp, "batch %q reaches %.1f yd from its centre: the chests of a batch lie within %.0f yd of it, so that the summons stack for area spells", id, w, BatchRadius)
			}
		}
		closed[id] = true
		i = j
	}
}
