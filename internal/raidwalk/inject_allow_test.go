package raidwalk

import (
	"fmt"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/packet"
)

func allowCtx() InjectCtx {
	return InjectCtx{
		Hires:    map[string]bool{"Mt": true, "Ot": true, "V": true, "H": true},
		Map:      249,
		Lead:     dungeon.Point{},
		HaveLead: true,
	}
}

// TestAllowedInjectLine pins spec 1.4: the exact forms the injector may send through the gm RPC, and nothing else.
func TestAllowedInjectLine(t *testing.T) {
	mk := func(f func(*InjectCtx)) InjectCtx {
		c := allowCtx()
		if f != nil {
			f(&c)
		}
		return c
	}
	cases := []struct {
		name string
		line string
		ctx  InjectCtx
		want bool
	}{
		{"prepare gm visible off", ".gm visible off", mk(nil), true},
		{"gm visible off not in prepare", ".gm visible off", mk(func(c *InjectCtx) { c.Fault = InjectRunaway }), false},
		{"gm off", ".gm off", mk(nil), false},
		{"kick", ".kick X", mk(nil), false},
		{"aura", ".aura 9454", mk(nil), false},
		{"npc", ".npc delete", mk(nil), false},
		{"die with a name", ".die X", mk(func(c *InjectCtx) {
			c.Fault, c.Victim, c.SelectedHire = InjectKillMT, "Mt", "Mt"
		}), false},
		{"die", ".die", mk(func(c *InjectCtx) {
			c.Fault, c.Victim, c.SelectedHire = InjectKillMT, "Mt", "Mt"
		}), true},
		{"die without a selected hire", ".die", mk(func(c *InjectCtx) {
			c.Fault, c.Victim = InjectKillMT, "Mt"
		}), false},
		{"die with an ot selected", ".die", mk(func(c *InjectCtx) {
			c.Fault, c.Victim, c.SelectedHire = InjectKillMT, "Mt", "Ot"
		}), false},
		{"die under another fault", ".die", mk(func(c *InjectCtx) {
			c.Fault, c.Victim, c.SelectedHire = InjectAddPack, "Mt", "Mt"
		}), false},
		{"go in prepare", ".go xyz -1.5 2.25 -3 249", mk(nil), true},
		{"go another map", ".go xyz 0 0 0 1", mk(nil), false},
		{"go 300 yd out", ".go xyz 300 0 0 249", mk(func(c *InjectCtx) { c.Fault = InjectRunaway }), false},
		{"go 200 yd out", ".go xyz 200 0 0 249", mk(func(c *InjectCtx) { c.Fault = InjectRunaway }), true},
		{"go NaN", ".go xyz NaN 0 0 249", mk(nil), false},
		{"go Inf", ".go xyz 0 +Inf 0 249", mk(nil), false},
		{"go without the lead", ".go xyz 0 0 0 249", mk(func(c *InjectCtx) {
			c.Fault, c.HaveLead = InjectRunaway, false
		}), false},
		{"go with too few args", ".go xyz 0 0 249", mk(nil), false},
		{"summon the runaway victim", ".summon V", mk(func(c *InjectCtx) {
			c.Fault, c.Victim = InjectRunaway, "V"
		}), true},
		{"summon under lag", ".summon V", mk(func(c *InjectCtx) {
			c.Fault, c.Victim = InjectLag, "V"
		}), false},
		{"summon another hire", ".summon H", mk(func(c *InjectCtx) {
			c.Fault, c.Victim = InjectRunaway, "V"
		}), false},
		{"summon a non-hire", ".summon X", mk(func(c *InjectCtx) {
			c.Fault, c.Victim = InjectRunaway, "X"
		}), false},
		{"freeze the victim", ".freeze V", mk(func(c *InjectCtx) {
			c.SelfSelected, c.Fault, c.Victim = true, InjectRunaway, "V"
		}), true},
		{"freeze without self selected", ".freeze V", mk(func(c *InjectCtx) {
			c.Fault, c.Victim = InjectRunaway, "V"
		}), false},
		{"freeze without a fault", ".freeze V", mk(func(c *InjectCtx) {
			c.SelfSelected, c.Victim = true, "V"
		}), false},
		{"freeze another hire", ".freeze H", mk(func(c *InjectCtx) {
			c.SelfSelected, c.Fault, c.Victim = true, InjectRunaway, "V"
		}), false},
		{"freeze a non-hire", ".freeze X", mk(func(c *InjectCtx) {
			c.SelfSelected, c.Fault, c.Victim = true, InjectRunaway, "X"
		}), false},
		{"unfreeze any hire", ".unfreeze H", mk(func(c *InjectCtx) { c.SelfSelected = true }), true},
		{"unfreeze without self selected", ".unfreeze H", mk(nil), false},
		{"unfreeze a non-hire", ".unfreeze X", mk(func(c *InjectCtx) { c.SelfSelected = true }), false},
		{"cr", ".freeze V\r", mk(func(c *InjectCtx) {
			c.SelfSelected, c.Fault, c.Victim = true, InjectRunaway, "V"
		}), false},
		{"newline", ".die\n", mk(func(c *InjectCtx) {
			c.Fault, c.Victim, c.SelectedHire = InjectKillMT, "Mt", "Mt"
		}), false},
		{"empty", "", mk(nil), false},
	}
	for _, c := range cases {
		if got := AllowedInjectLine(c.line, c.ctx); got != c.want {
			t.Errorf("%s: AllowedInjectLine(%q) = %v, want %v", c.name, c.line, got, c.want)
		}
	}
}

// TestAllowedInjectOrder pins spec 1.4's commander order: attack the chosen candidate with a hire, under add-pack only.
func TestAllowedInjectOrder(t *testing.T) {
	guid := packet.GUID(0xF1300000001E240)
	hex := fmt.Sprintf("0x%016X", uint64(guid))
	ctx := InjectCtx{
		Hires:     map[string]bool{"R1": true, "R2": true},
		Fault:     InjectAddPack,
		Candidate: guid,
	}
	if !AllowedInjectOrder("attack", []string{hex, "R1"}, ctx) {
		t.Errorf("AllowedInjectOrder refused the candidate order")
	}
	cases := []struct {
		name string
		verb string
		args []string
		ctx  InjectCtx
	}{
		{"another guid", "attack", []string{"0x0000000000000001", "R1"}, ctx},
		{"pull verb", "pull", []string{hex, "R1"}, ctx},
		{"non-hire", "attack", []string{hex, "X"}, ctx},
		{"three args", "attack", []string{hex, "R1", "R2"}, ctx},
		{"one arg", "attack", []string{hex}, ctx},
		{"another fault", "attack", []string{hex, "R1"}, InjectCtx{Hires: ctx.Hires, Fault: InjectRunaway, Candidate: guid}},
		{"no candidate", "attack", []string{hex, "R1"}, InjectCtx{Hires: ctx.Hires, Fault: InjectAddPack}},
	}
	for _, c := range cases {
		if AllowedInjectOrder(c.verb, c.args, c.ctx) {
			t.Errorf("%s: AllowedInjectOrder(%q, %v) accepted", c.name, c.verb, c.args)
		}
	}
}
