package planexport

import (
	"strings"
	"testing"
)

// TestPlanexportValidateFlagBossAnyOf: FlagBossAnyOf is legal on a boss row only, needs at least 2 entries, and the
// row's Entry (its canonical id) must be one of them. Validate is what Parse and ParseMigration run, so a row the
// module would misread is refused here like the other step flags.
func TestPlanexportValidateFlagBossAnyOf(t *testing.T) {
	boss := func(entry uint32, entries []uint32, flags uint32) Rows {
		return Rows{Steps: []StepRow{{
			Plan: "p", Leg: 0, Seq: 1, Kind: KindBoss, Entry: entry, Entries: entries, Flags: flags,
		}}}
	}
	valid := boss(15082, []uint32{15082, 15083, 15084, 15085}, FlagBossAnyOf)
	if err := Validate(valid); err != nil {
		t.Fatalf("the exported Edge of Madness row was refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		rows Rows
		want string
	}{
		{
			name: "on a non-boss row",
			rows: Rows{Steps: []StepRow{{Plan: "p", Leg: 0, Seq: 1, Kind: KindMove, Flags: FlagBossAnyOf}}},
			want: "the boss-any-of flag",
		},
		{
			name: "one entry",
			rows: boss(15082, []uint32{15082}, FlagBossAnyOf),
			want: "at least 2 entries",
		},
		{
			name: "entry missing",
			rows: boss(15082, []uint32{15083, 15084}, FlagBossAnyOf),
			want: "must be one of its entries",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.rows)
			if err == nil {
				t.Fatalf("Validate accepted %+v", tc.rows.Steps[0])
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not say %q", err, tc.want)
			}
		})
	}
}
