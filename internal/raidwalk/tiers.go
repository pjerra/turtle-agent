package raidwalk

import "fmt"

// MinimumHireTier is the minimum hire gear tier per raid, one row each (spec amendment G1, Sagiroth's rule): the tier
// a player holds when the raid is next, from the mercenary NPC unlock quests (TortoiseBots runtime/HireUnlockPolicy.h,
// the sequential chain 90100-90106: each kit unlocks after the raid before it) and the `tortoise_bots_hire_gear` sets.
// A walk hires and refits at its raid's tier; a raid with **0** has none sourced yet, so a walk refuses to run it
// without --tier N, because the tier is the fact a wipe is judged against (a wipe at the right tier is a gear-set
// tuning finding, never fixed by raising the tier).
//
// TODO (0 rows to source, one raid at a time, with that raid's mercenary unlock quest and hire-gear set): molten_core,
// blackwing_lair, zul_gurub, ruins_of_ahnqiraj, temple_of_ahnqiraj, naxxramas, upper_blackrock_spire,
// lower_karazhan_halls, upper_karazhan_halls, timbermaw_hold, emerald_sanctum.
var MinimumHireTier = map[string]int{
	// onyxia-kill-plan.md (c): the tier-3 quest needs Molten Core, Onyxia and Timbermaw kills, so a player who fights
	// Onyxia for the unlock holds tier 2 (the Zul'Gurub kit); tier 3 is her reward, not her entry kit.
	"onyxias_lair": 2,

	"molten_core":           0,
	"blackwing_lair":        0,
	"zul_gurub":             0,
	"ruins_of_ahnqiraj":     0,
	"temple_of_ahnqiraj":    0,
	"naxxramas":             0,
	"upper_blackrock_spire": 0,
	"lower_karazhan_halls":  0,
	"upper_karazhan_halls":  0,
	"timbermaw_hold":        0,
	"emerald_sanctum":       0,
}

// MinimumTier returns the raid's minimum hire tier; known is false for a raid with no sourced tier (a 0 row, or no row).
func MinimumTier(raid string) (tier int, known bool) {
	tier = MinimumHireTier[raid]
	return tier, tier > 0
}

// Where a walk's tier came from; the summary's roster.tier_source.
const (
	TierSourceTable    = "table"           // the committed table's row (the default roster, or a file that repeats it)
	TierSourceRoster   = "roster"          // the --hires file's own tier, for a raid with no sourced row
	TierSourceFlag     = "--tier"          // --tier N, for a raid the table leaves unknown
	TierSourceOverride = "--tier-override" // --tier-override N: a comparison, not a tier proof
)

// ResolveTier picks a walk's hire gear tier and names where it came from: rosterTier is the roster file's tier (0 when
// absent), flagTier --tier, overrideTier --tier-override. The table's row wins over an absent roster tier; a roster tier
// that differs from the row, and --tier on a raid that has a row, are refused (a tier other than the minimum is a
// comparison, and only --tier-override states that). A raid with no row needs --tier or a roster tier.
func ResolveTier(raid string, rosterTier, flagTier, overrideTier int) (tier int, source string, err error) {
	if raw := MinimumHireTier[raid]; raw < 0 || raw > maxHireTier {
		return 0, "", fmt.Errorf("the tier table's %s row is %d, want 0-%d (internal/raidwalk/tiers.go)", raid, raw, maxHireTier)
	}
	min, known := MinimumTier(raid)
	for _, c := range []struct {
		name string
		v    int
	}{{"--tier", flagTier}, {"--tier-override", overrideTier}, {"roster tier", rosterTier}} {
		if c.v != 0 && (c.v < 1 || c.v > maxHireTier) {
			return 0, "", fmt.Errorf("%s %d, want 1-%d", c.name, c.v, maxHireTier)
		}
	}
	if flagTier > 0 && overrideTier > 0 {
		return 0, "", fmt.Errorf("--tier fills the tier of a raid the table leaves unknown and --tier-override replaces a known one; not both")
	}
	if overrideTier > 0 {
		return overrideTier, TierSourceOverride, nil
	}
	if flagTier > 0 {
		if known {
			return 0, "", fmt.Errorf("raid %s's minimum hire tier is %d (internal/raidwalk/tiers.go); --tier is for a raid the table leaves unknown, --tier-override %d is a comparison run",
				raid, min, flagTier)
		}
		return flagTier, TierSourceFlag, nil
	}
	if rosterTier > 0 {
		if known {
			if rosterTier != min {
				return 0, "", fmt.Errorf("the roster's tier %d, but raid %s's minimum hire tier is %d: use --tier-override %d for a comparison run",
					rosterTier, raid, min, rosterTier)
			}
			return min, TierSourceTable, nil
		}
		return rosterTier, TierSourceRoster, nil
	}
	if known {
		return min, TierSourceTable, nil
	}
	return 0, "", fmt.Errorf("raid %s has no minimum hire tier in internal/raidwalk/tiers.go: pass --tier N (1-%d), the kit a player holds when the raid is next",
		raid, maxHireTier)
}
