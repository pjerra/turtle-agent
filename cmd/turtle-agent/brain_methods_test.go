package main

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
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
		{`{"strategies":["lock_fire"]}`, `strategy "lock_fire" is unknown (presets: frost_mage; strategies: avoid_ground, combat_consumables, combat_dps, mage_defense, mage_frost, mage_interrupt, mage_rest, ranged_position, wand)`},
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
	if s := st["strategies"].([]any); len(s) != 10 || s[0] != "base" || s[1] != "combat_dps" || s[2] != "mage_frost" || s[3] != "mage_rest" || s[9] != "wand" {
		t.Fatalf("%v", s)
	}
	if out := r.call("brain_start", `{"strategies":["frost_mage"]}`); errCode(out) != rpc.CodeGame || !strings.Contains(out["error"].(map[string]any)["message"].(string), "brain_stop it first") {
		t.Fatalf("a second start: %v", out)
	}
	// The spellbook is not loaded yet: the brain waits, and says why.
	r.eventually("brain_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["idle"] == "not_ready" })
	if st := r.call("brain_stop", "{}")["result"].(map[string]any); st["running"] != false || len(st["strategies"].([]any)) != 10 {
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
	r.groupWith("Gorm") // a party that is not in view: the first rest gate waits for it, so the run goes on
	out := r.call("dungeon_run", `{"name":"rig","brain":"frost_mage"}`)
	if out["error"] != nil || out["result"].(map[string]any)["brain"] != "frost_mage" {
		t.Fatalf("%v", out)
	}
	r.eventually("dungeon_status", "{}", func(m map[string]any) bool {
		rep, _ := m["result"].(map[string]any)["report"].(map[string]any)
		return rep != nil && len(rep["legs"].([]any)) == 1
	})
	// The rest gate started the brain, with upkeep waiting for the gate.
	st := r.eventually("brain_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["running"] == true })["result"].(map[string]any)
	if st["rest_gate"] != true {
		t.Fatalf("%v", st)
	}
	if rep := r.call("dungeon_status", "{}")["result"].(map[string]any)["report"].(map[string]any); rep["brain"] != "frost_mage" {
		t.Fatalf("%v", rep)
	}
	// While the run owns the brain, starting or stopping it by hand is refused (status still answers).
	for _, m := range []string{"brain_start", "brain_stop"} {
		params := "{}"
		if m == "brain_start" {
			params = `{"strategies":["frost_mage"]}`
		}
		if out := r.call(m, params); errCode(out) != rpc.CodeGame || !strings.Contains(out["error"].(map[string]any)["message"].(string), "a dungeon run owns the brain") {
			t.Fatalf("%s during a run: %v", m, out)
		}
	}
	if out := r.call("brain_status", "{}"); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.call("dungeon_stop", "{}")
	if st := r.call("brain_status", "{}")["result"].(map[string]any); st["running"] != false {
		t.Fatalf("a stopped run leaves the brain off: %v", st)
	}
	// The lease ends with the run: the brain can be started by hand again.
	if out := r.call("brain_start", `{"strategies":["frost_mage"]}`); out["error"] != nil {
		t.Fatalf("after the run: %v", out)
	}
	r.call("brain_stop", "{}")
	// Without the option the run starts no brain and says nothing of one.
	out = r.call("dungeon_run", `{"name":"rig"}`)
	if out["error"] != nil || out["result"].(map[string]any)["brain"] != nil {
		t.Fatalf("%v", out)
	}
	r.call("dungeon_stop", "{}")
}

func TestDriverBrainStartsAndStopsOnlyWhatItStarted(t *testing.T) {
	r := newRig(t)
	cfg := brain.Config{NewTicker: func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }}
	ctl := brain.NewController(context.Background(), &sessionBrainGame{cur: func() *session.Session { return r.s }}, cfg)
	defer ctl.Close()
	d := &driverBrain{ctl: ctl, strategies: []string{"frost_mage"}}

	d.Rest(true) // not started: ignored
	if ctl.Running() {
		t.Fatal("Rest started the brain")
	}
	if err := d.Start(); err != nil || !ctl.Running() {
		t.Fatalf("%v", err)
	}
	if err := d.Start(); err != nil { // a second Start is a no-op
		t.Fatal(err)
	}
	if st := ctl.Status(); !st.RestGate || st.RestOK {
		t.Fatalf("upkeep must wait for a rest gate: %+v", st)
	}
	d.Rest(true)
	if !ctl.Status().RestOK {
		t.Fatal("Rest(true) did not allow upkeep")
	}
	d.Rest(false)
	if ctl.Status().RestOK {
		t.Fatal("Rest(false) did not forbid upkeep")
	}
	d.Stop()
	if ctl.Running() {
		t.Fatal("Stop left the brain on")
	}
	d.Stop()                                            // stopping twice is fine
	if err := d.Start(); err != nil || !ctl.Running() { // and it can start again, as in the next leg
		t.Fatalf("%v", err)
	}
	d.Stop()

	// A brain started by hand is neither taken over nor stopped.
	if _, err := ctl.Start(brain.Params{Strategies: []string{"frost_mage"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.Start(); err == nil || !strings.Contains(err.Error(), "started by hand") {
		t.Fatalf("%v", err)
	}
	d.Stop()
	d.Rest(true)
	if !ctl.Running() || ctl.Status().RestOK != true {
		t.Fatal("the hand-started brain was touched")
	}
}

func TestBrainStartCanSwitchMovementOff(t *testing.T) {
	r := newRig(t)
	st := r.call("brain_start", `{"strategies":["frost_mage"],"no_move":true}`)["result"].(map[string]any)
	if st["running"] != true || st["no_move"] != true {
		t.Fatalf("%v", st)
	}
	r.call("brain_stop", "{}")
	st = r.call("brain_start", `{"strategies":["frost_mage"]}`)["result"].(map[string]any)
	if st["no_move"] != nil || st["move_held"] != nil {
		t.Fatalf("movement is on by default: %v", st)
	}
	r.call("brain_stop", "{}")
}

// The driver's brain does not walk the player until the driver holds the move lock around its own walks (lane A wires
// HoldMove into the driver and then drops NoMove); HoldMove itself works at once.
func TestDriverBrainKeepsMovementOffAndOffersTheMoveLock(t *testing.T) {
	r := newRig(t)
	cfg := brain.Config{NewTicker: func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }}
	ctl := brain.NewController(context.Background(), &sessionBrainGame{cur: func() *session.Session { return r.s }}, cfg)
	defer ctl.Close()
	d := &driverBrain{ctl: ctl, strategies: []string{"frost_mage"}}
	release, err := d.HoldMove(context.Background()) // before the brain runs: a free lock
	if err != nil || !ctl.Status().MoveHeld {
		t.Fatalf("%v %+v", err, ctl.Status())
	}
	release()
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	if st := ctl.Status(); !st.NoMove || st.MoveHeld {
		t.Fatalf("%+v", st)
	}
	release, err = d.HoldMove(context.Background())
	if err != nil || !ctl.Status().MoveHeld {
		t.Fatalf("%v %+v", err, ctl.Status())
	}
	release()
	if ctl.Status().MoveHeld {
		t.Fatal("the lock was not let go")
	}
}

func TestSessionBrainGameNavmeshHalfWithoutAPlanner(t *testing.T) {
	r := newRig(t)
	g := &sessionBrainGame{cur: func() *session.Session { return r.s }}
	if err := g.WalkTo(context.Background(), 1, 2, 3); err == nil || !strings.Contains(err.Error(), "mmaps_dir") {
		t.Fatalf("%v", err)
	}
	if clear, known := g.LineClear(brain.Vec3{}, brain.Vec3{5, 5, 0}); clear || known {
		t.Fatalf("clear %v known %v without a navmesh", clear, known)
	}
	if _, ok := g.SnapToMesh(brain.Vec3{}); ok {
		t.Fatal("snapped without a navmesh")
	}
	if len(g.DynamicObjects()) != 0 {
		t.Fatal("ground effects out of nowhere")
	}
}
