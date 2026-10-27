package scenario

import (
	"strings"
	"testing"
)

// TestParseRejectsAnUnknownStepField pins the strict per-kind field check:
// a gm step carrying a matcher-only field is invalid.
func TestParseRejectsAnUnknownStepField(t *testing.T) {
	data := `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"steps":[{"do":"gm","session":"A","text":"x","match":"y"}]}`
	_, err := Parse([]byte(data), nil)
	if err == nil || !strings.Contains(err.Error(), `step 1 (gm): unknown field "match"`) {
		t.Fatalf("got %v", err)
	}
}

// TestParseRejectsAVarNeverSet pins the static use-before-set check.
func TestParseRejectsAVarNeverSet(t *testing.T) {
	data := `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"addon_prefix":"X",
		"steps":[{"do":"addon_send","session":"A","channel":"guild","text":"C2S_GET_GROUP_DETAILS;${gid}"}]}`
	_, err := Parse([]byte(data), nil)
	if err == nil || !strings.Contains(err.Error(), "${gid} is never set before this step") {
		t.Fatalf("got %v", err)
	}
}

// TestParseAcceptsACaptureAndASave pins that a capture group and an rpc save
// make their vars available to later steps.
func TestParseAcceptsACaptureAndASave(t *testing.T) {
	data := `{"scenario":"cap","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"steps":[
			{"do":"expect","session":"A","match":"^id:(\\d+)$","capture":{"gid":1}},
			{"do":"addon_send","session":"A","channel":"guild","prefix":"X","text":"C2S_GET;${gid}"},
			{"do":"rpc","session":"A","method":"party","save":{"ingrp":"in_group"}},
			{"do":"check_rpc","session":"A","method":"party","path":"members.0.name","equals":"${ingrp}"}
		]}`
	if _, err := Parse([]byte(data), nil); err != nil {
		t.Fatalf("got %v", err)
	}
}

// TestParseRejectsANonLoopbackSession pins that a token must never leave the
// machine: only loopback RPC addresses parse.
func TestParseRejectsANonLoopbackSession(t *testing.T) {
	for _, addr := range []string{"10.0.0.5:8774", "example.com:1"} {
		data := `{"scenario":"x","sessions":{"A":{"rpc":"` + addr + `"}},"steps":[{"do":"mark"}]}`
		_, err := Parse([]byte(data), nil)
		if err == nil || !strings.Contains(err.Error(), "is not a loopback address") {
			t.Fatalf("%s: got %v", addr, err)
		}
	}
	data := `{"scenario":"x","sessions":{"A":{"rpc":"[::1]:8774"}},"steps":[{"do":"mark"}]}`
	if _, err := Parse([]byte(data), nil); err != nil {
		t.Fatalf("::1: got %v", err)
	}
}

// TestParseRejectsWorldChangingGM pins the AGENTS.md deny list on gm text
// and on rpc steps calling the gm method.
func TestParseRejectsWorldChangingGM(t *testing.T) {
	denied := []string{
		`{"do":"gm","session":"A","text":".npc add 1"}`,
		`{"do":"gm","session":"A","text":"reload all"}`,
		`{"do":"gm","session":"A","text":".GOBJECT delete 5"}`,
		`{"do":"rpc","session":"A","method":"gm","params":{"text":".ban x"}}`,
	}
	for _, step := range denied {
		data := `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[` + step + `]}`
		_, err := Parse([]byte(data), nil)
		if err == nil || !strings.Contains(err.Error(), "changes the world for everyone (AGENTS.md)") {
			t.Fatalf("%s: got %v", step, err)
		}
	}
	allowed := []string{
		`{"do":"gm","session":"A","text":".go xyz 1 2 3 0"}`,
		`{"do":"gm","session":"A","text":".summon Bob"}`,
		`{"do":"gm","session":"A","text":".npc info"}`,
	}
	for _, step := range allowed {
		data := `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[` + step + `]}`
		if _, err := Parse([]byte(data), nil); err != nil {
			t.Fatalf("%s: got %v", step, err)
		}
	}
}

// TestParseRejectsCheckDBWithoutDB pins that a check_db step needs the
// top-level db target, and that the table must be on the read-only allowlist.
func TestParseRejectsCheckDBWithoutDB(t *testing.T) {
	data := `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"steps":[{"do":"check_db","table":"tw_world.lft_user_groups","rows":0}]}`
	_, err := Parse([]byte(data), nil)
	if err == nil || !strings.Contains(err.Error(), "has no db target") {
		t.Fatalf("got %v", err)
	}
	data = `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"db":{"host":"m910q","container":"tortoise-db"},
		"steps":[{"do":"check_db","table":"tw_logon.account","rows":0}]}`
	_, err = Parse([]byte(data), nil)
	if err == nil || !strings.Contains(err.Error(), "is not on the read-only allowlist (internal/scenario/db.go)") {
		t.Fatalf("got %v", err)
	}
}

// TestParseRejectsACheckInFinally pins that finally holds actions only.
func TestParseRejectsACheckInFinally(t *testing.T) {
	data := `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"steps":[{"do":"mark"}],
		"finally":[{"do":"expect","session":"A","match":"x"}]}`
	_, err := Parse([]byte(data), nil)
	if err == nil || !strings.Contains(err.Error(), "finally step 1 (expect)") {
		t.Fatalf("got %v", err)
	}
}

// TestParseRejectsBadRegexAndCaptureGroup pins match compilation and the
// capture group range check at load.
func TestParseRejectsBadRegexAndCaptureGroup(t *testing.T) {
	data := `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"steps":[{"do":"expect","session":"A","match":"("}]}`
	_, err := Parse([]byte(data), nil)
	if err == nil || !strings.Contains(err.Error(), "bad match regexp") {
		t.Fatalf("got %v", err)
	}
	data = `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"steps":[{"do":"expect","session":"A","match":"^(a)$","capture":{"x":2}}]}`
	_, err = Parse([]byte(data), nil)
	if err == nil || !strings.Contains(err.Error(), "capture group 2 out of range") {
		t.Fatalf("got %v", err)
	}
}

// TestParseRejectsBadDBValues pins the value allowlists: no quotes, spaces or
// option-looking strings anywhere near the built SQL.
func TestParseRejectsBadDBValues(t *testing.T) {
	steps := []string{
		`{"do":"check_db","table":"tw_world.lft_user_groups","where":{"a;b":1},"rows":0}`,
		`{"do":"check_db","table":"tw_world.lft_user_groups","where":{"a":"x'"},"rows":0}`,
		`{"do":"check_db","item":{"char":"Bob'--","entry":9240},"count":0}`,
	}
	for _, step := range steps {
		data := `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
			"db":{"host":"m910q","container":"tortoise-db"},"steps":[` + step + `]}`
		if _, err := Parse([]byte(data), nil); err == nil {
			t.Fatalf("%s: accepted", step)
		}
	}
	for _, db := range []string{
		`{"host":"-oProxyCommand=x","container":"tortoise-db"}`,
		`{"host":"m910q","container":"a b"}`,
	} {
		data := `{"scenario":"x","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
			"db":` + db + `,"steps":[{"do":"mark"}]}`
		if _, err := Parse([]byte(data), nil); err == nil {
			t.Fatalf("%s: accepted", db)
		}
	}
}

// TestParseExampleScenariosParse pins that both shipped files parse with
// their own vars: LFT has 8 sections, the item file 4.
func TestParseExampleScenariosParse(t *testing.T) {
	for _, tc := range []struct {
		path     string
		sections int
	}{
		{"../../scenarios/lft-addon-path.json", 8},
		{"../../scenarios/itemleg3-g1-items.json", 4},
	} {
		f, err := Load(tc.path, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		sections := 0
		for _, s := range f.Steps {
			if s.Do == "section" {
				sections++
			}
		}
		if sections != tc.sections {
			t.Fatalf("%s: %d sections, want %d", tc.path, sections, tc.sections)
		}
	}
}
