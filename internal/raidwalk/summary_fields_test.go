package raidwalk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSummaryHireSettleAbsentIsNotZero: a summary written before hire_settle_s existed decodes the field as nil
// (absent), never as 0; a run that writes the field keeps 0 for --hire-settle 0 and 20 for the default.
func TestSummaryHireSettleAbsentIsNotZero(t *testing.T) {
	var old Summary
	if err := json.Unmarshal([]byte(`{"roster":{"tier":3,"settle_short":false}}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Roster.HireSettleS != nil {
		t.Fatalf("summary without the field reads hire_settle_s as %v, want absent", *old.Roster.HireSettleS)
	}
	for _, tc := range []struct {
		name   string
		settle time.Duration
		want   string
	}{
		{"zero", 0, `"hire_settle_s":0`},
		{"default", 20 * time.Second, `"hire_settle_s":20`},
	} {
		sum := BuildSummary(nil, SummaryMeta{Plan: "onyxias_lair", HireSettle: tc.settle, SettleShort: tc.settle < 20*time.Second})
		raw, err := json.Marshal(sum)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), tc.want) {
			t.Fatalf("%s: summary.json lacks %s: %s", tc.name, tc.want, raw)
		}
		var back Summary
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if back.Roster.HireSettleS == nil || *back.Roster.HireSettleS != tc.settle.Seconds() {
			t.Fatalf("%s: round trip reads hire_settle_s %v, want %v", tc.name, back.Roster.HireSettleS, tc.settle.Seconds())
		}
	}
}
