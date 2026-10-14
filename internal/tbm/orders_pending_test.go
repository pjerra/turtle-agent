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
