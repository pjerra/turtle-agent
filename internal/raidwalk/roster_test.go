package raidwalk

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

const exampleRoster = `{"format":"tb-raid-roster","version":1,"level":60,"tier":3,"gear_preset":"onyxia",
 "batches":[{"role":"mt","hire":"1 warrior:tank"},
            {"role":"ot","hire":"1 druid:tank 1 paladin:tank"},
            {"role":"rest","hire":"10h 26d"}]}`

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
		t.Fatalf("the spec's example roster: %v", err)
	}
	if err := r.Validate(ony); err != nil {
		t.Fatalf("the spec's example roster against onyxia: %v", err)
	}
	if r.Total() != 39 {
		t.Fatalf("total = %d, want 39", r.Total())
	}
	lines := r.HireLines()
	want := []string{
		".bot hire raid 1 warrior:tank level=60 t3",
		".bot hire raid 1 druid:tank 1 paladin:tank level=60 t3",
		".bot hire raid 10h 26d level=60 t3",
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
		{"total 40", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"ot","hire":"1t"},{"role":"rest","hire":"38d"}]}`, "39"},
		{"bad class", `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"1 bard"}]}`, "class"},
		{"bad format", `{"format":"other","version":1,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"1d"}]}`, "format"},
		{"bad version", `{"format":"tb-raid-roster","version":2,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"1d"}]}`, "version"},
	}
	for _, c := range refuse {
		if _, err := ParseRoster([]byte(c.json)); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.text)) {
			t.Errorf("%s: ParseRoster error = %v, want it to name %q", c.name, err, c.text)
		}
	}

	three := `{"format":"tb-raid-roster","version":1,"level":60,"batches":[{"role":"mt","hire":"1t"},{"role":"rest","hire":"3 warrior:tank 35d"}]}`
	r, err = ParseRoster([]byte(three))
	if err != nil {
		t.Fatalf("3 warrior:tank: %v", err)
	}
	if err := r.Validate(ony); err != nil {
		t.Fatalf("3 warrior:tank against onyxia: %v", err)
	}
}

func TestDefaultRosterFromPlanRaid(t *testing.T) {
	cases := []struct {
		plan       string
		total      int
		ot, rest   string
		gearPreset string
	}{
		{"onyxias_lair", 39, "3t", "10h 25d", "onyxia"},
		{"zul_gurub", 19, "2t", "5h 11d", "zg"},
		{"lower_karazhan_halls", 9, "1t", "3h 4d", ""},
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
		if r.GearPreset != c.gearPreset {
			t.Errorf("%s: gear preset = %q, want %q", c.plan, r.GearPreset, c.gearPreset)
		}
		if r.Level != 60 || r.Tier != 3 {
			t.Errorf("%s: level/tier = %d/%d", c.plan, r.Level, r.Tier)
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
	if r.Tier != 0 || r.GearPreset != "" {
		t.Fatalf("absent tier/gear must stay absent: %d %q", r.Tier, r.GearPreset)
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
}
