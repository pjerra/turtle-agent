package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

func TestBrainIsOffUntilStarted(t *testing.T) {
	r := newRig(t)
	st := r.call("brain_status", "{}")["result"].(map[string]any)
	if st["running"] != false || len(st["strategies"].([]any)) != 0 || len(st["last_decisions"].([]any)) != 0 || len(st["holds"].([]any)) != 0 || st["ticks"] != float64(0) {
		t.Fatalf("%v", st)
	}
	if st := r.call("brain_stop", "{}")["result"].(map[string]any); st["running"] != false {
		t.Fatalf("stopping a brain that is off: %v", st)
	}
}

func TestBrainStartRefusals(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct{ params, want string }{
		{`{}`, "name at least one"},
		{`{"strategies":[]}`, "name at least one"},
		{`{"strategies":["lock_fire"]}`, `strategy "lock_fire" is unknown (presets: frost_mage; strategies: combat_dps, mage_frost, mage_rest)`},
		{`{"strategies":["frost_mage"],"role":"healer"}`, `role "healer"`},
		{`{"strategies":["mage_frost"]}`, "face_target"},
		{`{"strategies":["frost_mage"],"colour":1}`, "colour"},
		{`{"strategies":"frost_mage"}`, "strategies"},
	} {
		out := r.call("brain_start", c.params)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(out["error"].(map[string]any)["message"].(string), c.want) {
			t.Errorf("%s: %v, want %q", c.params, out, c.want)
		}
	}
	if st := r.call("brain_status", "{}")["result"].(map[string]any); st["running"] != false {
		t.Fatalf("a refused start left a brain on: %v", st)
	}
}

func TestBrainStartStopAndEvents(t *testing.T) {
	r := newRig(t)
	out := r.call("brain_start", `{"strategies":["frost_mage"],"tank":"Gorm"}`)
	st, ok := out["result"].(map[string]any)
	if !ok || st["running"] != true || st["role"] != "dps" || st["tank"] != "Gorm" || st["rest_ok"] != true {
		t.Fatalf("%v", out)
	}
	if s := st["strategies"].([]any); len(s) != 4 || s[0] != "base" || s[1] != "combat_dps" || s[2] != "mage_frost" || s[3] != "mage_rest" {
		t.Fatalf("%v", s)
	}
	if out := r.call("brain_start", `{"strategies":["frost_mage"]}`); errCode(out) != rpc.CodeGame || !strings.Contains(out["error"].(map[string]any)["message"].(string), "brain_stop it first") {
		t.Fatalf("a second start: %v", out)
	}
	// The spellbook is not loaded yet: the brain waits, and says why.
	r.eventually("brain_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["idle"] == "not_ready" })
	if st := r.call("brain_stop", "{}")["result"].(map[string]any); st["running"] != false || len(st["strategies"].([]any)) != 4 {
		t.Fatalf("%v", st)
	}
	ev := r.call("events", `{"since":0,"kinds":["brain_state","brain_idle"]}`)["result"].(map[string]any)["events"].([]any)
	var kinds []string
	for _, e := range ev {
		m := e.(map[string]any)
		kinds = append(kinds, m["kind"].(string)+":"+m["data"].(map[string]any)[map[string]string{"brain_state": "state", "brain_idle": "reason"}[m["kind"].(string)]].(string))
	}
	if got := strings.Join(kinds, ","); got != "brain_state:started,brain_idle:not_ready,brain_state:stopped" {
		t.Fatalf("%s", got)
	}
}

// The leader plays: a wolf in combat with the leader stands 10 yards away, the spellbook has Frostbolt, and the brain
// casts it; a decision event names the trigger and the spell, and brain_stop ends the casting.
func TestBrainCastsOnAMobThatAttacksTheLeader(t *testing.T) {
	r := newRig(t)
	const wolf packet.GUID = 0xF13000012B000001
	w := objtest.Header(2)
	for _, u := range []struct {
		g      packet.GUID
		typ    uint8
		x      float32
		hp, mx uint32
		vals   map[uint16]uint32
	}{
		{0x2A, objects.TypePlayer, 1, 800, 800, map[uint16]uint32{objects.UnitBytes0: 8 << 8, objects.UnitPower1: 1000, objects.UnitMaxPower1: 1000, objects.UnitAura: 168, objects.UnitAura + 1: 1459}},
		{wolf, objects.TypeUnit, 11, 50, 50, map[uint16]uint32{objects.UnitFlags: objects.UnitFlagInCombat, objects.UnitTarget: 0x2A}},
	} {
		w.U8(uint8(objects.BlockCreate2)).PackedGUID(u.g).U8(u.typ)
		objtest.Living(w, 0x20, game.MoveInfo{X: u.x, Y: 2, Z: 3}, nil)
		v := map[uint16]uint32{objects.UnitHealth: u.hp, objects.UnitMaxHealth: u.mx}
		for k, x := range u.vals {
			v[k] = x
		}
		objtest.Values(w, 192, v)
	}
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
	r.expect(opcode.CMSGCreatureQuery)
	r.wolfName()
	r.srv.Send(opcode.SMSGInitialSpells, gametest.InitialSpells([]uint32{7322, 2136, 168, 1459}, nil))
	r.eventually("spells", "{}", func(m map[string]any) bool { return len(m["result"].([]any)) == 4 })
	r.eventually("units", `{"name":"wolf"}`, func(m map[string]any) bool { return len(m["result"].([]any)) == 1 })

	if out := r.call("brain_start", `{"strategies":["frost_mage"]}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	p, err := r.srv.Expect(opcode.CMSGCastSpell, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(p.Body); got != 7322 {
		t.Fatalf("cast spell %d, want Frostbolt rank 4 (7322)", got)
	}
	st := r.call("brain_stop", "{}")["result"].(map[string]any)
	if st["actions"].(float64) < 1 {
		t.Fatalf("%v", st)
	}
	last := st["last_decisions"].([]any)
	d := last[len(last)-1].(map[string]any)
	if d["action"] != "frostbolt" || d["trigger"] != "fight_target" || d["result"] != "ok" || d["spell_id"] != float64(7322) || d["relevance"] != float64(10) {
		t.Fatalf("%v", last)
	}
	ev := r.call("events", `{"since":0,"kinds":["brain_decision"]}`)["result"].(map[string]any)["events"].([]any)
	if len(ev) == 0 {
		t.Fatal("no brain_decision event")
	}
	e := ev[0].(map[string]any)
	if e["spell_id"] != float64(7322) || e["data"].(map[string]any)["action"] != "frostbolt" || e["data"].(map[string]any)["result"] != "ok" {
		t.Fatalf("%v", e)
	}
}

func TestDungeonRunBrainOption(t *testing.T) {
	r := dungeonRig(t)
	bad := r.call("dungeon_run", `{"name":"rig","brain":"lock_fire"}`)
	if errCode(bad) != rpc.CodeInvalidParams || !strings.Contains(bad["error"].(map[string]any)["message"].(string), "brain: strategy") {
		t.Fatalf("%v", bad)
	}
	// A brain started by hand blocks a run that wants its own.
	if out := r.call("brain_start", `{"strategies":["frost_mage"]}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	if out := r.call("dungeon_run", `{"name":"rig","brain":"frost_mage"}`); errCode(out) != rpc.CodeGame || !strings.Contains(out["error"].(map[string]any)["message"].(string), "brain_stop it first") {
		t.Fatalf("%v", out)
	}
	r.call("brain_stop", "{}")
	out := r.call("dungeon_run", `{"name":"rig","brain":"frost_mage"}`)
	if out["error"] != nil || out["result"].(map[string]any)["brain"] != "frost_mage" {
		t.Fatalf("%v", out)
	}
	if rep := r.call("dungeon_status", "{}")["result"].(map[string]any)["report"].(map[string]any); rep["brain"] != "frost_mage" {
		t.Fatalf("%v", rep)
	}
	r.call("dungeon_stop", "{}")
	if st := r.call("brain_status", "{}")["result"].(map[string]any); st["running"] != false {
		t.Fatalf("a stopped run leaves the brain off: %v", st)
	}
	// Without the option the run starts no brain and says nothing of one.
	out = r.call("dungeon_run", `{"name":"rig"}`)
	if out["error"] != nil || out["result"].(map[string]any)["brain"] != nil {
		t.Fatalf("%v", out)
	}
	r.call("dungeon_stop", "{}")
}
