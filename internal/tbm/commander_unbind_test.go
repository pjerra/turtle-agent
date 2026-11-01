package tbm

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/events"
)

func TestUnbindAckDropsTheHeldFrame(t *testing.T) {
	f := NewViewFeed(events.New(8), nil, 0)
	f.mu.Lock()
	f.have = true
	f.mu.Unlock()
	f.ProcessLine("TBM:ACTION_ACK|commander unbind|party|5|binding ended")
	if _, _, ok := f.Latest(); ok {
		t.Fatal("the frame of an ended binding is still held")
	}
}
