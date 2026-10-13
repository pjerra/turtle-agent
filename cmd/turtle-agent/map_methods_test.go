package main

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/rpc"
)

func TestMapSnapshotDrawsTheSessionSoFar(t *testing.T) {
	r := dungeonRig(t)
	res := r.call("map_snapshot", "{}")["result"].(map[string]any)
	svg, _ := res["svg"].(string)
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "Harnessb") || res["map"] != float64(0) || res["floor"] != float64(0) ||
		res["members"] != float64(1) || res["background"] != false || res["html"] != nil {
		t.Fatalf("%v", res)
	}
	if floors := res["floors"].([]any); len(floors) != 1 {
		t.Errorf("floors %v", floors)
	}
	if notes := res["notes"].([]any); !strings.Contains(strings.Join(anyStrings(notes), "|"), `"mmaps_dir" is not set`) {
		t.Errorf("notes %v", notes)
	}
	// The plan, the replay and the options.
	res = r.call("map_snapshot", `{"plan":"rig","html":true,"background":false}`)["result"].(map[string]any)
	if !strings.Contains(res["svg"].(string), "planned walk through the legs") || !strings.Contains(res["html"].(string), `id="run-data"`) {
		t.Fatalf("%.200v", res)
	}
}

func TestMapSnapshotRefusals(t *testing.T) {
	r := dungeonRig(t)
	for _, c := range []struct{ params, want string }{
		{`{"plan":"../rig"}`, "plan must be a plan file name"},
		{`{"plan":"nowhere"}`, "dungeon plan: "},
		{`{"floor":3}`, "floor: this map has floors 0 to 0"},
		{`{"floor":-1}`, "floor: this map has floors"},
		{`{"floors":"five"}`, "is not a height"},
		{`{"colour":1}`, "colour"},
	} {
		out := r.call("map_snapshot", c.params)
		msg, _ := out["error"].(map[string]any)["message"].(string)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %v", c.params, out)
		}
	}
}

func anyStrings(in []any) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i], _ = v.(string)
	}
	return out
}
