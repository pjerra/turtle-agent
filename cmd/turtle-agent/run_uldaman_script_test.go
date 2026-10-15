package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
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

// The script's leader mode builds the params of a leader run, and refuses what only the commander does; observe mode builds a watch run.
func TestRunUldamanScriptModes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	out, err := exec.Command(bash, "../../tools/run-uldaman.sh", "--mode", "leader", "--brain", "frost_mage", "--rotation", "frost_mage", "--role", "dps", "--from-leg", "2", "--print-params").Output()
	if err != nil {
		t.Fatal(err)
	}
	var p dungeonRunParams
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if remote, err := checkMode(p); remote || err != nil {
		t.Fatalf("checkMode %v %v", remote, err)
	}
	if p.Mode != "leader" || p.Brain != "frost_mage" || p.Rotation != "frost_mage" || p.Role != "dps" || p.FromLeg == nil || p.FromLeg.index != 2 || p.LeadBot != "" {
		t.Errorf("%+v", p)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--mode", "observe", "--from-leg", "2", "--print-params"}, "observe mode starts at the plan's first step"},
		{[]string{"--mode", "observe", "--brain", "frost_mage", "--print-params"}, "go with --mode leader"},
		{[]string{"--mode", "fly", "--print-params"}, "commander, observe or leader"},
		{[]string{"--mode", "leader", "--hire", "--print-params"}, "commander options"},
		{[]string{"--mode", "leader", "--pull-group", "1", "--print-params"}, "commander options"},
		{[]string{"--rotation", "frost_mage", "--print-params"}, "go with --mode leader"},
		{[]string{"--mode", "leader", "--role", "bard", "--print-params"}, "dps, tank or healer"},
	} {
		cmd := exec.Command(bash, append([]string{"../../tools/run-uldaman.sh"}, c.args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%v: %v %q, want %q", c.args, err, stderr.String(), c.want)
		}
	}
	// The pull test of a leader run: the params name the pull and no from_leg.
	out, err = exec.Command(bash, "../../tools/run-uldaman.sh", "--mode", "leader", "--rotation", "frost_mage", "--pulltest", "0:0:4:respawn", "--print-params").Output()
	if err != nil {
		t.Fatal(err)
	}
	var pt dungeonRunParams
	dec = json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pt); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if remote, err := checkMode(pt); remote || err != nil {
		t.Fatalf("checkMode %v %v", remote, err)
	}
	if pt.Pulltest == nil || pt.Pulltest.Tries != 4 || pt.Pulltest.Reset != "respawn" || pt.FromLeg != nil || pt.Rotation != "frost_mage" {
		t.Errorf("%+v", pt)
	}
	// MODE= in the environment is the same option.
	cmd := exec.Command(bash, "../../tools/run-uldaman.sh", "--print-params")
	cmd.Env = append(os.Environ(), "MODE=leader")
	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), `"mode":"leader"`) {
		t.Errorf("MODE=leader: %s %v", out, err)
	}
}

// The two replies of ".instance groupunbind all" (Commands.cpp, HandleInstanceGroupUnbindCommand; mangos_string 117) are told apart,
// whatever else the server says meanwhile.
func TestGroupUnbindReply(t *testing.T) {
	for text, want := range map[string]dungeon.GroupUnbindResult{
		"Group unbound. Disbanding.":                       dungeon.GroupUnbound,
		"|Hplayer:Lcwarra|h[Lcwarra]|h is not in a group.": dungeon.GroupNotGroups,
		"Lcwarra is not in a group.":                       dungeon.GroupNotGroups,
		"instances unbound: 1":                             "",
		"You are in combat.":                               "",
		"":                                                 "",
	} {
		if got := groupUnbindReply(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}

// The player a "not in a group" reply names tells whether the selection landed: run 6b answered "Glacienne is not in a group" for a
// selected Lcwarra, because the core had run the line before the selection packet.
func TestGroupUnbindSubject(t *testing.T) {
	for text, want := range map[string]string{
		"|cffffffff|Hplayer:Glacienne|h[Glacienne]|h|r is not in a group.": "Glacienne",
		"|Hplayer:Lcwarra|h[Lcwarra]|h is not in a group.":                 "Lcwarra",
		"Lcwarra is not in a group.":                                       "Lcwarra",
		"Group unbound. Disbanding.":                                       "",
		"":                                                                 "",
	} {
		if got := groupUnbindSubject(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}

// A "not in a group" reply is told by whose it is: the lead's, the agent's (resend), or a third player's (stop).
func TestClassifyGroupUnbind(t *testing.T) {
	for _, c := range []struct {
		res           dungeon.GroupUnbindResult
		subject, want string
	}{
		{dungeon.GroupNotGroups, "Lcwarra", string(dungeon.GroupNotGroups)},
		{dungeon.GroupNotGroups, "Glacienne", string(dungeon.GroupWrongTarget)},
		{dungeon.GroupNotGroups, "glacienne", string(dungeon.GroupWrongTarget)},
		{dungeon.GroupNotGroups, "Somebody", string(dungeon.GroupThirdPlayer)},
		{dungeon.GroupNotGroups, "", string(dungeon.GroupNotGroups)},
		{dungeon.GroupUnbound, "", string(dungeon.GroupUnbound)},
		{dungeon.GroupNoAnswer, "", string(dungeon.GroupNoAnswer)},
	} {
		if got := classifyGroupUnbind(c.res, c.subject, "Lcwarra", "Glacienne"); string(got) != c.want {
			t.Errorf("%v %q: %q, want %q", c.res, c.subject, got, c.want)
		}
	}
}

// run-uldaman.sh --hire builds the hired-party params: no lead_bot, bots, tank or healer, the prepare block names the party, and the same
// checks that dungeon_run makes pass on it (a rename of a key would otherwise fail the first live run).
func TestRunUldamanScriptHireParamsAreWhatDungeonRunTakes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	for _, c := range []struct {
		args    []string
		env     []string
		rehire  bool
		level   int
		comp    string
		fromLeg int
	}{
		{[]string{"--hire", "--print-params"}, nil, false, 0, "", 0},
		{[]string{"--hire", "--rehire", "--level", "44", "--from-leg", "3", "--print-params"}, nil, true, 44, "", 3},
		{[]string{"--hire", "--no-fresh", "--print-params"}, []string{"COMPOSITION=warrior:tank,priest,mage"}, false, 0, "warrior:tank,priest,mage", 0},
	} {
		cmd := exec.Command(bash, append([]string{"../../tools/run-uldaman.sh"}, c.args...)...)
		cmd.Env = append(os.Environ(), c.env...)
		out, err := cmd.Output()
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
		if p.LeadBot != "" || len(p.Bots) != 0 || p.Tank != "" || p.Healer != "" {
			t.Errorf("a hired party names no bots: %+v", p)
		}
		pr := p.Prepare
		if pr == nil || !pr.HiredParty() || pr.Online || pr.Fresh || strings.Join(pr.GMSetup, ",") != "gm visible off" || pr.Hire == nil {
			t.Fatalf("prepare %+v", pr)
		}
		if pr.Hire.Rehire != c.rehire || pr.Hire.Level != c.level || strings.Join(pr.Hire.Composition, ",") != c.comp {
			t.Errorf("hire %+v", pr.Hire)
		}
		if p.FromLeg == nil || p.FromLeg.index != c.fromLeg {
			t.Errorf("from_leg %+v", p.FromLeg)
		}
	}
	for _, args := range [][]string{{"--rehire", "--print-params"}, {"--level", "40", "--print-params"}, {"--hire", "--level", "99", "--print-params"}} {
		if err := exec.Command(bash, append([]string{"../../tools/run-uldaman.sh"}, args...)...).Run(); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

// A hired party takes its names from the prepare step; naming bots as well is refused, and so is a group run with the block.
func TestCheckModeHiredParty(t *testing.T) {
	hire := &dungeon.PrepareSpec{Party: "hire"}
	if remote, err := checkMode(dungeonRunParams{Mode: "remote", Prepare: hire}); !remote || err != nil {
		t.Fatalf("a hired party needs no lead_bot: %v %v", remote, err)
	}
	for _, p := range []dungeonRunParams{
		{Mode: "remote", Prepare: hire, LeadBot: "Lcwarra"},
		{Mode: "remote", Prepare: hire, Bots: []string{"Lcpriest"}},
		{Mode: "remote", Prepare: hire, Tank: "Lcwarra"},
		{Mode: "remote", Prepare: hire, Healer: "Lcpriest"},
		{Mode: "remote", Prepare: &dungeon.PrepareSpec{Party: "hire", Online: true}},
		{Mode: "remote", Prepare: &dungeon.PrepareSpec{Party: "hire", Hire: &dungeon.HireSpec{Composition: []string{"warrior"}}}},
		{Mode: "remote", Prepare: &dungeon.PrepareSpec{Party: "alts"}, LeadBot: "Lcwarra"},
		{Prepare: hire},
	} {
		if _, err := checkMode(p); err == nil {
			t.Errorf("%+v was accepted", p)
		}
	}
	// Without the block a remote run still needs its lead bot.
	if _, err := checkMode(dungeonRunParams{Mode: "remote", Prepare: &dungeon.PrepareSpec{Online: true}}); err == nil {
		t.Error("a named party needs lead_bot")
	}
}

// The agent's classes are the names the hire command takes.
func TestClassNamesAreHireClasses(t *testing.T) {
	for id, name := range classNames {
		if err := (&dungeon.HireSpec{Composition: []string{name + ":dps", "warrior:tank", "priest:healer"}}).Validate(); err != nil {
			t.Errorf("class %d %q is no hire class", id, name)
		}
	}
	if len(classNames) != 9 || classNames[5] != "priest" || classNames[9] != "warlock" {
		t.Errorf("%v", classNames)
	}
}

// --from-leg N --pull-group M, --start-here and --pulltest build params the agent accepts, including the checks of the start options; the
// combinations the script refuses never reach the agent.
func TestRunUldamanScriptStartAndPulltestParams(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	plan, err := dungeon.Load("../../dungeons/uldaman.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args      []string
		fromLeg   int
		pullGroup int // -1: none
		startHere bool
		pulltest  string
	}{
		{[]string{"--from-leg", "3", "--print-params"}, 3, -1, false, ""},
		{[]string{"--from-leg", "1", "--pull-group", "0", "--print-params"}, 1, 0, false, ""},
		{[]string{"--from-leg", "4", "--start-here", "--print-params"}, 4, -1, true, ""},
		{[]string{"--from-leg", "2", "--have-items", "7733,7740", "--print-params"}, 2, -1, false, ""},
		{[]string{"--hire", "--from-leg", "1", "--pull-group", "1", "--print-params"}, 1, 1, false, ""},
		{[]string{"--pulltest", "1:0", "--print-params"}, 1, 0, false, "1:0:0:"},
		{[]string{"--pulltest", "1:1:5:respawn", "--hire", "--print-params"}, 1, 1, false, "1:1:5:respawn"},
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
		if c.pulltest == "" {
			if p.Pulltest != nil || p.FromLeg == nil || p.FromLeg.index != c.fromLeg || p.StartHere != c.startHere || (p.FromPullGroup == nil) != (c.pullGroup < 0) || (p.FromPullGroup != nil && *p.FromPullGroup != c.pullGroup) {
				t.Errorf("%v: %+v", c.args, p)
			}
			if err := checkStartParams(p, plan, c.fromLeg); err != nil {
				t.Errorf("%v: %v", c.args, err)
			}
			continue
		}
		if p.FromLeg != nil || p.FromPullGroup != nil || p.Pulltest == nil || p.Pulltest.Leg.index != c.fromLeg || *p.Pulltest.PullGroup != c.pullGroup {
			t.Errorf("%v: %+v", c.args, p)
		}
		if err := checkStartParams(p, plan, c.fromLeg); err != nil {
			t.Errorf("%v: %v", c.args, err)
		}
		if strings.HasSuffix(c.pulltest, "respawn") && (p.Pulltest.Tries != 5 || p.Pulltest.Reset != "respawn") {
			t.Errorf("%v: %+v", c.args, p.Pulltest)
		}
	}
	for _, args := range [][]string{
		{"--pull-group", "x", "--print-params"},
		{"--have-items", "7733;7740", "--print-params"},
		{"--from-leg", "2", "--pull-group", "1", "--start-here", "--print-params"},
		{"--start-here", "--print-params"},
		{"--pulltest", "2", "--print-params"},
		{"--pulltest", "1:x", "--print-params"},
		{"--pulltest", "1:0:11", "--print-params"},
		{"--pulltest", "1:0:3:fast", "--print-params"},
		{"--pulltest", "1:0", "--from-leg", "2", "--print-params"},
		{"--pulltest", "1:0", "--pull-group", "1", "--print-params"},
	} {
		if err := exec.Command(bash, append([]string{"../../tools/run-uldaman.sh"}, args...)...).Run(); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}
