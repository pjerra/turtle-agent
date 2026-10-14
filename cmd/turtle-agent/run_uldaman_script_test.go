package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// tools/run-uldaman.sh builds its dungeon_run params by hand: they must be what the agent accepts (no unknown key, a mode, a prepare
// block that passes the same checks), or the first live run after a rename fails at the start.
func TestRunUldamanScriptParamsAreWhatDungeonRunTakes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	for _, c := range []struct {
		args  []string
		fresh bool
		from  int
	}{
		{[]string{"--print-params"}, true, 0},
		{[]string{"--no-fresh", "--from-leg", "2", "--print-params"}, false, 2},
	} {
		out, err := exec.Command(bash, append([]string{"../../tools/run-uldaman.sh"}, c.args...)...).Output()
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		var p dungeonRunParams
		dec := json.NewDecoder(bytes.NewReader(out))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("%v: %s: %v", c.args, out, err)
		}
		if remote, err := checkMode(p); !remote || err != nil {
			t.Fatalf("%v: checkMode %v %v", c.args, remote, err)
		}
		if p.Name != "uldaman" || p.LeadBot != "Lcwarra" || len(p.Bots) != 4 || p.Tank != "Lcwarra" || p.Healer != "Lcpriest" {
			t.Errorf("%+v", p)
		}
		if p.Prepare == nil || !p.Prepare.Online || p.Prepare.Fresh != c.fresh || strings.Join(p.Prepare.GMSetup, ",") != "gm visible off" {
			t.Errorf("prepare %+v", p.Prepare)
		}
		if p.FromLeg == nil || p.FromLeg.index != c.from {
			t.Errorf("from_leg %+v", p.FromLeg)
		}
	}
}
