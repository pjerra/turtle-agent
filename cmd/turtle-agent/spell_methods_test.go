package main

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

const rigSelf = 0x2A

func TestSpellsAndCastStateMethods(t *testing.T) {
	r := newRig(t)
	// An empty spellbook is an empty list, not null; nothing is being cast.
	out := r.call("spells", "{}")
	if l, ok := out["result"].([]any); !ok || len(l) != 0 {
		t.Fatalf("spells %v", out)
	}
	cs := r.call("cast_state", "{}")["result"].(map[string]any)
	if c := cs["cast"].(map[string]any); c["state"] != "idle" || cs["last_cast"] != nil || cs["last_failure"] != nil || cs["combo_points"] != float64(0) {
		t.Fatalf("cast_state %v", cs)
	}
	if g := cs["gcd"].(map[string]any); g["active"] != false || g["remaining_ms"] != float64(0) {
		t.Fatalf("gcd %v", g)
	}

	// Frostbolt ranks 1 and 2 and Fire Blast: the superseded rank is gone, the top one is "highest".
	r.srv.Send(opcode.SMSGInitialSpells, gametest.InitialSpells([]uint32{116, 2136}, nil))
	r.srv.Send(opcode.SMSGLearnedSpell, gametest.LearnedSpell(205))
	r.srv.Send(opcode.SMSGSupercededSpell, gametest.SupercededSpell(116, 205))
	out = r.eventually("spells", `{"name":"frostbolt","highest":true}`, func(m map[string]any) bool {
		l, ok := m["result"].([]any)
		return ok && len(l) == 1 && l[0].(map[string]any)["spell_id"] == float64(205)
	})
	row := out["result"].([]any)[0].(map[string]any)
	if row["name"] != "Frostbolt" || row["rank"] != float64(2) || row["highest"] != true || row["cast_ms"] != float64(1800) || row["cost"] != float64(35) ||
		row["power"] != "mana" || row["school"] != "frost" || row["ready"] != true || row["range_max"] != float64(30) || row["gcd_ms"] != float64(1500) {
		t.Fatalf("row %v", row)
	}
	if fx := row["effects"].([]any); len(fx) != 2 || fx[0].(map[string]any)["aura"] != float64(33) {
		t.Fatalf("effects %v", fx)
	}
	flags := map[string]bool{}
	for _, f := range row["flags"].([]any) {
		flags[f.(string)] = true
	}
	if !flags["damage"] || !flags["aura"] || !flags["interruptible"] {
		t.Fatalf("flags %v", flags)
	}
	if l := r.call("spells", `{"spell_id":2136}`)["result"].([]any); len(l) != 1 {
		t.Fatalf("by id %v", l)
	}

	// Fire Blast goes off: the cooldown shows in spells, the GCD in cast_state and status.
	r.srv.Send(opcode.SMSGSpellStart, gametest.SpellStart(rigSelf, 2136, 0, 0x77))
	r.srv.Send(opcode.SMSGSpellGo, gametest.SpellGo(rigSelf, 2136, nil, nil, nil, 0x77))
	out = r.eventually("spells", `{"spell_id":2136}`, func(m map[string]any) bool {
		l, ok := m["result"].([]any)
		return ok && len(l) == 1 && l[0].(map[string]any)["ready"] == false
	})
	if fb := out["result"].([]any)[0].(map[string]any); fb["why"] != "cooldown" || fb["remaining_ms"].(float64) < 7000 {
		t.Fatalf("fire blast %v", fb)
	}
	cs = r.call("cast_state", "{}")["result"].(map[string]any)
	if lc := cs["last_cast"].(map[string]any); lc["spell_id"] != float64(2136) || lc["name"] != "Fire Blast" {
		t.Fatalf("last cast %v", cs)
	}
	if st := r.call("status", "{}")["result"].(map[string]any); st["gcd_ms"].(float64) <= 0 || st["combo_points"] != float64(0) {
		t.Fatalf("status %v", st)
	}

	// A refusal reaches cast_state with its name.
	r.srv.Send(opcode.SMSGCastResult, gametest.CastResult(116, 89))
	cs = r.eventually("cast_state", "{}", func(m map[string]any) bool {
		return m["result"].(map[string]any)["last_failure"] != nil
	})["result"].(map[string]any)
	if lf := cs["last_failure"].(map[string]any); lf["reason_name"] != "out_of_range" || lf["reason"] != float64(89) || lf["spell_id"] != float64(116) {
		t.Fatalf("last failure %v", lf)
	}
	if out := r.call("spells", `{"unknown":1}`); errCode(out) != rpc.CodeInvalidParams {
		t.Fatalf("unknown field: %v", out)
	}
}
