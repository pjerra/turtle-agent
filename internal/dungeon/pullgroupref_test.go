package dungeon

import (
	"testing"
)

// 6a: pull group texts name both numberings: "pull group 9 of 12
// (from_pull_group 8)".

func TestPullGroupRefNamesBothNumbers(t *testing.T) {
	if got, want := PullGroupRef(8, 12), "pull group 9 of 12 (from_pull_group 8)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := PullGroupRef(0, 0), "pull group 1 (from_pull_group 0)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
