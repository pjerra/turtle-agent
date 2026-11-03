package planscout

import "github.com/pjerra/turtle-agent/internal/planlint"

// The run leader's rules and the scout's own, in yards (pinned by TestConstantsMirrorTheRunLeader).
const (
	ReactDistanceYd   = planlint.ReactDistanceYd
	TooFarLegYd       = planlint.TooFarLegYd
	DriftMarginYd     = planlint.DriftMarginYd
	ArriveYd          = planlint.ArriveYd
	StalledArriveYd   = planlint.StalledArriveYd
	ExactArriveYd     = planlint.ExactArriveYd
	StragglerDzYd     = planlint.StragglerDzYd
	ClimbViaDzYd      = planlint.ClimbViaDzYd
	StandZTolYd       = planlint.StandZTolYd
	SeeYd             = planlint.SeeYd
	FightDriftYd      = planlint.FightDriftYd
	DetourMaxYd       = planlint.DetourMaxYd
	StandMinYd        = planlint.StandMinYd
	StandHostileYd    = planlint.StandHostileYd
	StandPullMarginYd = planlint.StandPullMarginYd
	MaxMoveEndOffset  = planlint.MaxMoveEndOffset
	JoinerWarnBodies  = planlint.JoinerWarnBodies
	JoinerFailBodies  = planlint.JoinerFailBodies
)

// Options are the offline pass's choices.
type Options struct {
	// Filter, with FromLeg and UntilLeg, limits the report to these exported legs, inclusive (-1: no limit).
	Filter            bool
	FromLeg, UntilLeg int
	// PartyLevel is the party's level (the aggro radius); 0: the plan's band high end.
	PartyLevel int
	// Raid widens the stand spread; the caller sets it from the plan.
	Raid bool
	// AggroRate is the core's Rate.Creature.Aggro (1 offline).
	AggroRate float32
	// ReactDistance is the run leader's AiPlayerbot.ReactDistance (offline: its default).
	ReactDistance float32
	// KillFirst ranks the live target choice like the run leader (the plan's kill_first).
	KillFirst []uint32
}
