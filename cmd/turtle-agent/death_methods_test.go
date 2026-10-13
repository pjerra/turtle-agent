package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

// selfValues creates (or updates) the player's own object with the given values over health 800.
func (r *agentRig) selfValues(vals map[uint16]uint32) {
	r.t.Helper()
	w := objtest.Header(1).U8(uint8(objects.BlockCreate2)).PackedGUID(0x2A).U8(objects.TypePlayer)
	objtest.Living(w, 0x20, game.MoveInfo{X: 1, Y: 2, Z: 3}, nil)
	v := map[uint16]uint32{objects.UnitHealth: 800, objects.UnitMaxHealth: 800}
	for k, x := range vals {
		v[k] = x
	}
	objtest.Values(w, 192, v)
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
}

func (r *agentRig) deathStatus() map[string]any {
	r.t.Helper()
	return r.call("death_status", "{}")["result"].(map[string]any)
}

// The death methods over JSON-RPC: the state, an offered resurrection, the release, the corpse and the reclaim.
func TestDeathMethods(t *testing.T) {
	r := newRigAt(t, session.Options{NamesWait: 50 * time.Millisecond}, [3]float32{1, 2, 3}) // nobody answers for the caster's name
	r.selfValues(nil)
	r.eventually("status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["max_health"] == float64(800) })
	if d := r.deathStatus(); d["dead"] != false || d["ghost"] != false || d["resurrect"] != nil {
		t.Fatalf("%v", d)
	}
	for _, m := range []string{"accept_resurrect", "release_spirit", "reclaim_corpse"} {
		if out := r.call(m, "{}"); errCode(out) != rpc.CodeGame {
			t.Fatalf("%s while alive: %v", m, out)
		}
	}
	if out := r.call("area_trigger", `{"id":0}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("trigger 0: %v", out)
	}

	r.selfValues(map[uint16]uint32{objects.UnitHealth: 0})
	r.eventually("death_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["dead"] == true })
	r.srv.Send(opcode.SMSGResurrectRequest, gametest.ResurrectRequest(0x30, "", false, true))
	d := r.eventually("death_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["resurrect"] != nil })["result"].(map[string]any)
	if o := d["resurrect"].(map[string]any); o["caster"] != "0x0000000000000030" || o["sickness"] != false {
		t.Fatalf("%v", d)
	}
	acc := r.call("accept_resurrect", "{}")
	if acc["error"] != nil {
		t.Fatalf("%v", acc)
	}
	p, err := r.srv.Expect(opcode.CMSGResurrectResponse, 2*time.Second)
	if err != nil || binary.LittleEndian.Uint64(p.Body) != 0x30 || p.Body[8] != 1 {
		t.Fatalf("% X %v", p.Body, err)
	}

	// Still dead, so the spirit can be released.
	if out := r.call("release_spirit", "{}"); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGRepopRequest)
	r.selfValues(map[uint16]uint32{objects.UnitHealth: 1, objects.PlayerFlags: objects.PlayerFlagGhost})
	r.eventually("death_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["ghost"] == true })

	got := make(chan map[string]any, 1)
	go func() { got <- r.call("corpse_query", "{}") }()
	r.expect(opcode.MSGCorpseQuery)
	r.srv.Send(opcode.MSGCorpseQuery, gametest.CorpseQuery(0, -6053.7, -2954.6, 213.7, 70))
	c := (<-got)["result"].(map[string]any)
	if c["found"] != true || c["map"] != float64(0) || c["corpse_map"] != float64(70) {
		t.Fatalf("%v", c)
	}

	r.srv.Send(opcode.SMSGCorpseReclaimDelay, gametest.CorpseReclaimDelay(60000))
	r.eventually("death_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["reclaim_in_ms"].(float64) > 50000 })
	if out := r.call("reclaim_corpse", "{}"); errCode(out) != rpc.CodeGame || !strings.Contains(out["error"].(map[string]any)["message"].(string), "reclaim delay") {
		t.Fatalf("%v", out)
	}

	if out := r.call("area_trigger", `{"id":286}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	p, err = r.srv.Expect(opcode.CMSGAreatrigger, 2*time.Second)
	if err != nil || binary.LittleEndian.Uint32(p.Body) != 286 {
		t.Fatalf("% X %v", p.Body, err)
	}
	evs := r.eventually("events", `{"since":0,"kinds":["resurrect_request","corpse_reclaim_delay"]}`, func(m map[string]any) bool {
		return len(m["result"].(map[string]any)["events"].([]any)) == 2
	})["result"].(map[string]any)["events"].([]any)
	if len(evs) != 2 || evs[0].(map[string]any)["kind"] != events.KindResurrectRequest {
		t.Fatalf("%v", evs)
	}
}

// dungeon_run with gm false is a player's run: it refuses the options that need a GM and reports leader_gm false.
func TestDungeonRunNonGM(t *testing.T) {
	r := dungeonRig(t)
	for _, c := range []struct{ params, want string }{
		{`{"name":"rig","gm":false,"teleport":true}`, "teleport needs a GM account"},
		{`{"name":"rig","gm":false,"buffs":"world"}`, "buffs need a GM account"},
		{`{"name":"rig","gm_level":2}`, "gm_level goes with gm true"},
		{`{"name":"rig","gm":false,"gm_level":3}`, "gm_level goes with gm true"},
		{`{"name":"rig","gm":true,"gm_level":0}`, "gm_level must be 1"},
		{`{"name":"rig","gm":true,"gm_level":7}`, "gm_level must be 1"},
	} {
		out := r.call("dungeon_run", c.params)
		msg, _ := out["error"].(map[string]any)["message"].(string)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %v", c.params, out)
		}
	}
	// A GM may; a probe (no gm) may too, and the result says how the run decides.
	for params, want := range map[string]string{`{"name":"rig","gm":false}`: "false", `{"name":"rig","gm":true,"recover_wipe":true}`: "true", `{"name":"rig"}`: "probe"} {
		res := r.call("dungeon_run", params)["result"].(map[string]any)
		if res["leader_gm"] != want {
			t.Fatalf("%s: %v", params, res)
		}
		r.eventually("dungeon_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["state"] == "failed" })
		if want == "false" {
			// No GM command went out at all: the chat log holds no say line.
			if gms := r.call("events", `{"since":0,"kinds":["chat"]}`)["result"].(map[string]any)["events"].([]any); len(gms) != 0 {
				t.Fatalf("chat %v", gms)
			}
		}
	}
}

// The probe reads the level from the reply to .account, whatever else the server says meanwhile.
func TestClassifyAccountProbe(t *testing.T) {
	const me packet.GUID = 0x2A
	sys := func(text string) events.Event { return events.Event{Kind: events.KindSystem, Text: text} }
	echo := events.Event{Kind: events.KindChat, SourceGUID: me, Text: ".account", Data: map[string]any{"type": "say"}}
	other := events.Event{Kind: events.KindChat, SourceGUID: 0x99, Text: ".account"}
	for _, c := range []struct {
		name  string
		evs   []events.Event
		level int
		known bool
	}{
		{"nothing", nil, 0, false},
		{"a developer", []events.Event{sys("Your account level is: 3")}, 3, true},
		{"a player (PlayerCommands 1)", []events.Event{sys("Your account level is: 0")}, 0, true},
		{"an administrator after noise", []events.Event{sys("You are in combat."), sys("Your account level is: 4")}, 4, true},
		{"the line said aloud (PlayerCommands 0)", []events.Event{echo}, 0, true},
		{"an unrelated system line is no answer", []events.Event{sys("This command is not available to you.")}, 0, false},
		{"a notification is no answer", []events.Event{sys("GM visibility on")}, 0, false},
		{"somebody else's chat", []events.Event{other}, 0, false},
		{"a sentence that only contains the words", []events.Event{sys("Your account level is: 3 and more")}, 0, false},
	} {
		level, known := classifyAccountProbe(c.evs, me, ".account")
		if level != c.level || known != c.known {
			t.Errorf("%s: %d %v", c.name, level, known)
		}
	}
}
