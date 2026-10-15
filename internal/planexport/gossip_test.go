package planexport

import (
	"strings"
	"testing"
)

// The run leader has no gossip step: a plan with one is refused with the reason (the same way as the other steps it cannot carry), not
// exported without the step.
func TestGossipStepIsRefused(t *testing.T) {
	p := parsePlan(t, `{"version":1,"name":"zf","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"end","to":{"x":10,"y":0,"z":0},"steps":[{"gossip":{"npc":7607,"near":{"x":12,"y":0,"z":0},"text":"blow","by":"tank"}}]}]}`)
	_, err := Build(p, Options{Source: "test"})
	if err == nil || !strings.Contains(err.Error(), "gossip (NPC 7607") || !strings.Contains(err.Error(), "cannot be exported") {
		t.Fatalf("%v", err)
	}
}
