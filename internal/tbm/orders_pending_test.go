package tbm

import (
	"testing"
	"time"
)

func TestPendingWaitIsCapped(t *testing.T) {
	for _, c := range []struct {
		budget int
		want   time.Duration
	}{{0, 5 * time.Second}, {20, 25 * time.Second}, {85, 90 * time.Second}, {600, MaxPendingWait}, {1 << 30, MaxPendingWait}, {-5, 5 * time.Second}} {
		if got := pendingWait(c.budget, DefaultPendingSlack); got != c.want {
			t.Errorf("budget %d: %v, want %v", c.budget, got, c.want)
		}
	}
}

// cmd enter may be waited for up to the module's worst case (60 + 30 + 30 s and its own timeout 6 s later), more than any other order.
func TestPendingWaitOfEnter(t *testing.T) {
	for _, c := range []struct {
		budget int
		want   time.Duration
	}{{40, 48 * time.Second}, {120, 128 * time.Second}, {200, EnterMaxPendingWait}} {
		if got := pendingWaitCap(c.budget, EnterPendingSlack, EnterMaxPendingWait); got != c.want {
			t.Errorf("budget %d: %v, want %v", c.budget, got, c.want)
		}
	}
	if EnterMaxPendingWait < (60+30+30+6)*time.Second || EnterMaxPendingWait <= MaxPendingWait {
		t.Errorf("cap %v", EnterMaxPendingWait)
	}
}
