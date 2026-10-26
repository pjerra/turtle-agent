package main

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// 6a: the from_pull_group range refusal names both numberings.

func TestFromPullGroupRangeNamesBothNumberings(t *testing.T) {
	plan := startTestPlan(t)
	if len(plan.Legs) < 1 {
		t.Fatal("no legs")
	}
	plan.Legs[0].PullGroups = make([]dungeon.PullGroup, 12)
	group := 12
	err := checkStartParams(dungeonRunParams{FromPullGroup: &group}, plan, 0)
	if err == nil {
		t.Fatal("group 12 of 12 accepted")
	}
	for _, want := range []string{"from_pull_group 0-11", "1-12"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
}

// 3b: observe.auto_resume outside 0-3 is refused, 2 passes through.

func TestCheckObserveAutoResumeRange(t *testing.T) {
	badOf := func(p dungeonRunParams) error {
		return checkObserve(p, func(format string, args ...any) error {
			return errBadTest(format)
		})
	}
	for _, auto := range []int{-1, 4} {
		if err := badOf(dungeonRunParams{Mode: "observe", Observe: &observeParam{AutoResume: auto}}); err == nil {
			t.Errorf("auto_resume %d accepted", auto)
		}
	}
	if err := badOf(dungeonRunParams{Mode: "observe", Observe: &observeParam{AutoResume: 2}}); err != nil {
		t.Errorf("auto_resume 2 refused: %v", err)
	}
	if spec := observeSpec(&observeParam{AutoResume: 2}); spec.AutoResume != 2 {
		t.Errorf("observeSpec dropped auto_resume 2: %+v", spec)
	}
}

type errBadTest string

func (e errBadTest) Error() string { return string(e) }
