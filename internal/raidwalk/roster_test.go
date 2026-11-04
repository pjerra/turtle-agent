package raidwalk

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

const exampleRoster = `{"format":"tb-raid-roster","version":1,"level":60,"tier":2,
 "batches":[{"role":"mt","hire":"1 warrior:tank"},
            {"role":"ot","hire":"1 druid:tank 1 paladin:tank"},
            {"role":"rest","hire":"10h 27d"}]}`

func loadRaidPlan(t *testing.T, name string) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsRaid() {
		t.Fatalf("%s is not a raid plan", name)
	}
	return p
}

func TestRosterGrammar(t *testing.T) {
	ony := loadRaidPlan(t, "onyxias_lair")
	r, err := ParseRoster([]byte(exampleRoster))
	if err != nil {
		t.Fatalf("the example roster: %v", err)
	}
	if err := r.Validate(ony); err != nil {
		t.Fatalf("the example roster against onyxia: %v", err)
	}
	if r.Total() != 40 {
		t.Fatalf("total = %d, want 40 (every raid slot, the commander outside)", r.Total())
	}
	lines := r.HireLines()
	want := []string{
		".bot hire raid 1 warrior:tank level=60 t2",
		".bot hire raid 1 druid:tank 1 paladin:tank level=60 t2",
		".bot hire raid 10h 27d level=60 t2",
	}
	if len(lines) != len(want) {
		t.Fatalf("HireLines = %d lines (%v), want %d", len(lines), lines, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}

	refuse := []struct {
		name string
		json string
		text string
	}{
		{"preset", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1 warrior:tank"},{"role":"rest","hire":"onyxia"}]}`, "preset fills the raid in one go"},
		{"two mt", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"mt","hire":"1t"},{"role":"rest","hire":"10h 27d"}]}`, "one mt"},
		{"mt hires two", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"2 warrior:tank"},{"role":"rest","hire":"10h 27d"}]}`, "exactly 1"},
		{"ot dps", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"ot","hire":"1t 1d"},{"role":"rest","hire":"10h 26d"}]}`, "tank"},
		{"total 41", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"ot","hire":"1t"},{"role":"rest","hire":"39d"}]}`, "40"},
		{"removed gear_preset", `{"format":"tb-raid-roster","version":1,"level":60,"gear_preset":"onyxia","batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"1d"}]}`, "unknown field"},
		{"bad class", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"1 bard"}]}`, "class"},
		{"bad format", `{"format":"other","version":1,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"1d"}]}`, "format"},
		{"bad version", `{"format":"tb-raid-roster","version":3,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"1d"}]}`, "version"},
	}
	for _, c := range refuse {
		if _, err := ParseRoster([]byte(c.json)); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.text)) {
			t.Errorf("%s: ParseRoster error = %v, want it to name %q", c.name, err, c.text)
		}
	}

	three := `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"3 warrior:tank 36d"}]}`
	r, err = ParseRoster([]byte(three))
	if err != nil {
		t.Fatalf("3 warrior:tank: %v", err)
	}
	if err := r.Validate(ony); err != nil {
		t.Fatalf("3 warrior:tank against onyxia: %v", err)
	}
	if r.Total() != 40 {
		t.Fatalf("3 warrior:tank total = %d, want 40", r.Total())
	}
}

func TestDefaultRosterFromPlanRaid(t *testing.T) {
	cases := []struct {
		plan  string
		total int
		ot    string
		rest  string
		tier  int
	}{
		// Every raid slot: the plan's tank count and healers, the dps make up the rest (amendment G2).
		{"onyxias_lair", 40, "3t", "10h 26d", 2},
		{"zul_gurub", 20, "2t", "5h 12d", 1},
		{"lower_karazhan_halls", 10, "1t", "3h 5d", 0},
	}
	for _, c := range cases {
		p := loadRaidPlan(t, c.plan)
		r, err := DefaultRoster(p)
		if err != nil {
			t.Fatalf("%s: %v", c.plan, err)
		}
		if r.Total() != c.total {
			t.Errorf("%s: total = %d, want %d", c.plan, r.Total(), c.total)
		}
		if got := r.Batches[0].Hire; got != "1 warrior:tank" {
			t.Errorf("%s: mt = %q", c.plan, got)
		}
		if len(r.Batches) < 2 || r.Batches[1].Role != "ot" || r.Batches[1].Hire != c.ot {
			t.Errorf("%s: ot batch = %+v, want %q", c.plan, r.Batches[1], c.ot)
		}
		if got := r.Batches[len(r.Batches)-1].Hire; got != c.rest {
			t.Errorf("%s: rest = %q, want %q", c.plan, got, c.rest)
		}
		// The roster's tier comes from the committed table; a raid the table leaves unknown stays 0 and the walk
		// refuses it without --tier.
		if r.Tier != c.tier {
			t.Errorf("%s: tier = %d, want %d", c.plan, r.Tier, c.tier)
		}
		if r.Level != 60 {
			t.Errorf("%s: level = %d", c.plan, r.Level)
		}
		if err := r.Validate(p); err != nil {
			t.Errorf("%s: default roster does not validate: %v", c.plan, err)
		}
	}
}

func TestRosterPlanBoundAndDefaults(t *testing.T) {
	ony := loadRaidPlan(t, "onyxias_lair")
	noLevel := `{"format":"tb-raid-roster","version":1,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"10h 27d"}]}`
	r, err := ParseRoster([]byte(noLevel))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(ony); err != nil {
		t.Fatal(err)
	}
	if r.Level != 60 {
		t.Fatalf("level default = %d, want the plan band's max 60", r.Level)
	}
	if r.Tier != 0 {
		t.Fatalf("an absent tier must stay absent (the walk resolves it): %d", r.Tier)
	}

	// A 38-bot roster is fine on a 40-player raid but not on a 20-player one.
	big := `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"37d"}]}`
	r, err = ParseRoster([]byte(big))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(loadRaidPlan(t, "zul_gurub")); err == nil {
		t.Fatal("a 38-bot roster validated against a 20-player raid")
	}

	// The hires fill the raid's every seat (the commander stands outside): 40 is the most a roster may ask for.
	full := `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"39d"}]}`
	r, err = ParseRoster([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(ony); err != nil {
		t.Fatalf("a full 40-bot roster against onyxia: %v", err)
	}
	if err := r.Validate(loadRaidPlan(t, "zul_gurub")); err == nil {
		t.Fatal("a 40-bot roster validated against a 20-player raid")
	}
}

// A v2 roster sets a spec per batch: the spec must be a `.bot role` id of the batch's single class
// and agree with the role the hire names; count, when given, must be the batch's size.
func TestRosterV2Specs(t *testing.T) {
	ony := loadRaidPlan(t, "onyxias_lair")
	const v2 = `{"format":"tb-raid-roster","version":2,"level":60,"tier":2,
 "batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection","count":1},
            {"role":"ot","hire":"1 paladin:tank","spec":"protection"},
            {"role":"rest","hire":"2 priest:healer","spec":"holy","count":2},
            {"role":"rest","hire":"1 druid:tank","spec":"bear"},
            {"role":"rest","hire":"1 mage","spec":"fire"},
            {"role":"rest","hire":"34d"}]}`
	r, err := ParseRoster([]byte(v2))
	if err != nil {
		t.Fatalf("the v2 roster: %v", err)
	}
	if err := r.Validate(ony); err != nil {
		t.Fatalf("the v2 roster against onyxia: %v", err)
	}
	if r.Total() != 40 {
		t.Fatalf("total = %d, want 40", r.Total())
	}
	if got := r.Batches[0].Spec; got != "protection" {
		t.Errorf("mt spec = %q, want protection", got)
	}
	if got := r.Batches[3].Class(); got != "druid" {
		t.Errorf("the bear batch's class = %q, want druid", got)
	}
	if got := r.Batches[5].Class(); got != "" {
		t.Errorf("a role count's class = %q, want none", got)
	}
	// A v1 roster is still read and hired the same way.
	if _, err := ParseRoster([]byte(exampleRoster)); err != nil {
		t.Fatalf("v1: %v", err)
	}

	refuse := []struct {
		name string
		json string
		text string
	}{
		{"v1 spec", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection"},{"role":"rest","hire":"10h 26d"}]}`, "version 2"},
		{"v1 count", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t","count":1},{"role":"rest","hire":"10h 26d"}]}`, "version 2"},
		{"role count spec", `{"format":"tb-raid-roster","version":2,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"5h","spec":"holy"}]}`, "class batch"},
		{"mixed class spec", `{"format":"tb-raid-roster","version":2,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"1 priest 1 mage","spec":"holy"}]}`, "class batch"},
		{"wrong class spec", `{"format":"tb-raid-roster","version":2,"level":60,"batches":[{"role":"mt","hire":"1 warrior:tank","spec":"holy"},{"role":"rest","hire":"10h 26d"}]}`, "not a warrior spec"},
		{"wrong role spec", `{"format":"tb-raid-roster","version":2,"level":60,"batches":[{"role":"mt","hire":"1 warrior:tank","spec":"arms"},{"role":"rest","hire":"10h 26d"}]}`, "the hire says tank"},
		{"default role spec", `{"format":"tb-raid-roster","version":2,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"1 druid","spec":"bear"}]}`, "the hire says healer"},
		{"count mismatch", `{"format":"tb-raid-roster","version":2,"level":60,"batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection","count":2},{"role":"rest","hire":"10h 26d"}]}`, "count 2"},
		{"bad spec", `{"format":"tb-raid-roster","version":2,"level":60,"batches":[{"role":"mt","hire":"1 warrior:tank","spec":"prot"},{"role":"rest","hire":"10h 26d"}]}`, "not a warrior spec"},
	}
	for _, c := range refuse {
		if _, err := ParseRoster([]byte(c.json)); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.text)) {
			t.Errorf("%s: ParseRoster error = %v, want it to name %q", c.name, err, c.text)
		}
	}
}
