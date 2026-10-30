package raidwalk

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSummaryTextNamesTheFaults is the summary file's fault line and injected death count (spec
// raid-walk-addons 1.7): summary.txt names every fault with its verdict, reason and step, and summary.json's
// injections are a list, never null.
func TestSummaryTextNamesTheFaults(t *testing.T) {
	sum := BuildSummary(nil, SummaryMeta{Plan: "onyxias_lair",
		Injections: []InjectSummary{
			{Kind: "kill-mt", Verdict: "pass", Step: 7, Victim: "Bot01"},
			{Kind: "lag", Verdict: "fail", Reason: "no rejoin in 120 s", Step: 5, Victim: "Bot02"},
		},
		InjectedDeaths: 1})
	if sum.Injections == nil || len(sum.Injections) != 2 {
		t.Fatalf("injections %+v, want the two faults", sum.Injections)
	}
	if sum.Deaths.Injected != 1 {
		t.Fatalf("deaths.injected = %d, want 1", sum.Deaths.Injected)
	}
	want := "faults: kill-mt pass (step 7); lag fail: no rejoin in 120 s (step 5)\n"
	if text := sum.Text(); !strings.Contains(text, want) {
		t.Fatalf("summary.txt has no fault line %q:\n%s", want, text)
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"injections":[`) {
		t.Fatalf("summary.json injections are not a list: %s", raw)
	}
	if !strings.Contains(string(raw), `"injected":1`) {
		t.Fatalf("summary.json has no injected death count: %s", raw)
	}
}
