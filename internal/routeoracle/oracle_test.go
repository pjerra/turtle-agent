package routeoracle

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

func TestCheckName(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
	}{
		{"wailing_caverns", true},
		{"a", true},
		{"0", true},
		{"a-b_c9", true},
		{"../escape", false},
		{"../../escape", false},
		{"Wailing", false},
		{"", false},
		{"with space", false},
		{strings.Repeat("a", 41), false},
		{"/etc/passwd", false},
	}
	for _, c := range cases {
		err := CheckName(c.name)
		if (err == nil) != c.valid {
			t.Errorf("%q: err %v, want valid %v", c.name, err, c.valid)
		}
	}
}

// The oracle refuses a plan whose own name is not a plan name, before anything is planned
// and before any caller could use the name as a path. (A name that passes the check but
// differs from the requested plan is refused by the command, which owns the pairing.)
func TestOracleRejectsABadName(t *testing.T) {
	p := navmesh.NewPlanner("")
	defer p.Close()
	for _, name := range []string{"../escape", "../../etc/hosts", "Bad Name"} {
		if _, err := Oracle(p, "", &dungeon.Plan{Name: name}); err == nil || !strings.Contains(err.Error(), "not a plan name") {
			t.Errorf("%q: err %v, want a plan-name refusal", name, err)
		}
	}
}

// A valid name the report is built from is the one the report carries.
func TestOracleCarriesThePlanName(t *testing.T) {
	p := navmesh.NewPlanner("")
	defer p.Close()
	_, err := Oracle(p, "", &dungeon.Plan{Name: "wailing_caverns"})
	// No mmaps folder: the oracle fails on the first plan, but the refusal is not the name's.
	if err == nil || strings.Contains(err.Error(), "not a plan name") {
		t.Errorf("err %v", err)
	}
}
