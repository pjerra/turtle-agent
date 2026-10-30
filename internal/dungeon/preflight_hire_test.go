package dungeon

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// 5a: the preflight takes the talents read at hire time instead of asking
// again in the instance, where the ask gets no answer.

func TestRemotePreflightTalentsFromTheHireMemory(t *testing.T) {
	o := kitOpts()
	o.HireMemory = map[string]tbm.Who{"healy": {Class: "priest", Level: 40, Trees: [3]int{0, 5, 31}}}
	g := &multiFake{botFake: kitFake(t), silent: map[string]bool{"Healy": true}}
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	talents := map[string]string{}
	for _, b := range rep.Remote.Preflight.Bots {
		talents[b.Name] = b.Talents
	}
	if talents["Healy"] != "0/5/31 (hire)" {
		t.Fatalf("talents %v", talents)
	}
	if all := strings.Join(rep.Remote.Preflight.Warnings, "\n"); strings.Contains(all, "Healy: talents") {
		t.Fatalf("warnings %v", rep.Remote.Preflight.Warnings)
	}
	if len(g.allCmds) != 2 {
		t.Fatalf("talents asked %d times, want 2 (Tanky, Magey): %v", len(g.allCmds), g.allCmds)
	}
}

func TestRemotePreflightTalentsAllKnownSendsNothing(t *testing.T) {
	o := kitOpts()
	o.HireMemory = map[string]tbm.Who{
		"tanky": {Class: "warrior", Level: 40, Trees: [3]int{0, 5, 31}},
		"healy": {Class: "priest", Level: 40, Trees: [3]int{0, 5, 31}},
		"magey": {Class: "mage", Level: 40, Trees: [3]int{1, 2, 3}},
	}
	g := &multiFake{botFake: kitFake(t), silent: map[string]bool{"Tanky": true, "Healy": true, "Magey": true}}
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if g.calls != 0 {
		t.Fatalf("BotLinesAll called %d times, want 0", g.calls)
	}
	for _, b := range rep.Remote.Preflight.Bots {
		if !strings.HasSuffix(b.Talents, " (hire)") {
			t.Errorf("%+v", b)
		}
	}
}

func TestKnownTalentsPrefersTheReadyBlock(t *testing.T) {
	ready := &ReadyReport{Bots: []ReadyBot{
		{Name: "Tanky", Talents: "1/2/37"},
		{Name: "Magey", Talents: "no answer"},
	}}
	for _, c := range []struct {
		name  string
		bot   string
		ready *ReadyReport
		mem   map[string]tbm.Who
		want  string
		ok    bool
	}{
		{"ready wins over memory", "Tanky", ready, map[string]tbm.Who{"tanky": {Class: "warrior", Level: 40, Trees: [3]int{0, 5, 31}}}, "1/2/37", true},
		{"memory", "Healy", nil, map[string]tbm.Who{"healy": {Class: "priest", Level: 40, Trees: [3]int{0, 5, 31}}}, "0/5/31", true},
		{"case differs", "HEALY", nil, map[string]tbm.Who{"healy": {Class: "priest", Level: 40, Trees: [3]int{0, 5, 31}}}, "0/5/31", true},
		{"memory 0/0/0 unknown", "Healy", nil, map[string]tbm.Who{"healy": {Class: "priest", Level: 40, Trees: [3]int{}}}, "", false},
		{"ready no answer falls back to memory", "Magey", ready, map[string]tbm.Who{"magey": {Class: "mage", Level: 40, Trees: [3]int{1, 2, 3}}}, "1/2/3", true},
		{"no entry unknown", "Healy", nil, nil, "", false},
	} {
		if got, ok := knownTalents(c.ready, c.mem, c.bot); got != c.want || ok != c.ok {
			t.Errorf("%s: knownTalents = %q, %v, want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// A mixed party maps each answer back to its bot: Tanky's answer lands on Tanky, and Magey keeps the memory talents.
func TestRemotePreflightTalentsMixedKnownAndAsked(t *testing.T) {
	o := kitOpts()
	o.HireMemory = map[string]tbm.Who{"magey": {Class: "mage", Level: 40, Trees: [3]int{1, 2, 3}}}
	g := &multiFake{botFake: kitFake(t), silent: map[string]bool{"Healy": true, "Magey": true}}
	g.replies["command Tanky talents"] = []string{"My current talent spec is: Protection (0/5/31) Link: 05-31"}
	rep, err := runRemote(t, g, plan1(), o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	talents := map[string]string{}
	for _, b := range rep.Remote.Preflight.Bots {
		talents[b.Name] = b.Talents
	}
	if talents["Tanky"] != "0/5/31" || talents["Magey"] != "1/2/3 (hire)" || talents["Healy"] != "no answer" {
		t.Fatalf("talents %v", talents)
	}
	if len(g.allCmds) != 2 {
		t.Fatalf("asked %v, want Tanky and Healy only", g.allCmds)
	}
}

// An unknown ready entry ("no answer", malformed, 0/0/0) falls back to the
// hire memory instead of asking the bot again.
func TestKnownTalentsUnknownReadyFallsBackToMemory(t *testing.T) {
	mem := map[string]tbm.Who{"magey": {Class: "mage", Level: 40, Trees: [3]int{1, 2, 3}}}
	for _, talents := range []string{"no answer", "garbage", "1/2", "0/0/0", "error: timeout"} {
		ready := &ReadyReport{Bots: []ReadyBot{{Name: "Magey", Talents: talents}}}
		if got, ok := knownTalents(ready, mem, "Magey"); got != "1/2/3" || !ok {
			t.Errorf("%q: knownTalents = %q, %v, want %q, %v", talents, got, ok, "1/2/3", true)
		}
	}
}
