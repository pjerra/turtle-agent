package main

import (
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// The group control methods over JSON-RPC: errors outside a group, a convert_raid answered by
// a raid list, and `party` reporting the subgroups and assistant flags it carries.
func TestGroupControlMethods(t *testing.T) {
	r := newRig(t)
	for m, params := range map[string]string{"promote": `{"name":"Gorm"}`, "assistant": `{"name":"Gorm"}`,
		"convert_raid": `{}`, "raid_move": `{"name":"Gorm","subgroup":2}`, "disband": `{}`} {
		if out := r.call(m, params); errCode(out) != rpc.CodeGame {
			t.Fatalf("%s outside a group: %v", m, out)
		}
	}
	gorm := game.GroupMember{Name: "Gorm", GUID: 0x30, Status: game.MemberOnline}
	r.srv.Send(opcode.SMSGGroupList, gametest.GroupList(0x2A, gorm))
	r.expect(opcode.CMSGRequestPartyMemberStats)

	res := make(chan map[string]any, 1)
	go func() { res <- r.call("convert_raid", "{}") }()
	r.expect(opcode.CMSGGroupRaidConvert)
	gorm.Flags = game.GroupAssistant | 1
	r.srv.Send(opcode.SMSGGroupList, gametest.GroupListAs(game.GroupRaid, 0, 0x2A, gorm))
	if out := <-res; out["error"] != nil {
		t.Fatalf("convert_raid: %v", out)
	}
	r.expect(opcode.MSGRaidTargetUpdate)

	p := r.call("party", "{}")["result"].(map[string]any)
	m := p["members"].([]any)[0].(map[string]any)
	if p["raid"] != true || p["subgroup"] != float64(1) || m["subgroup"] != float64(2) || m["assistant"] != true {
		t.Fatalf("party %v", p)
	}
	if out := r.call("raid_move", `{"name":"Gorm","subgroup":9}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("subgroup 9: %v", out)
	}
}

// loot_method sends CMSG_LOOT_METHOD as the leader and returns when the group list shows the change; party reports the
// group's loot rules.
func TestLootMethodMethod(t *testing.T) {
	r := newRig(t)
	if out := r.call("loot_method", `{"method":"free_for_all"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("outside a group: %v", out)
	}
	gorm := game.GroupMember{Name: "Gorm", GUID: 0x30, Status: game.MemberOnline}
	r.srv.Send(opcode.SMSGGroupList, gametest.GroupList(0x2A, gorm))
	r.expect(opcode.CMSGRequestPartyMemberStats)
	if p := r.call("party", "{}")["result"].(map[string]any); p["loot_method"] != "group_loot" || p["loot_threshold"] != float64(2) {
		t.Fatalf("party %v", p)
	}
	if out := r.call("loot_method", `{"method":"everyone"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("unknown method: %v", out)
	}
	res := make(chan map[string]any, 1)
	go func() { res <- r.call("loot_method", `{"method":"free_for_all"}`) }()
	if p, err := r.srv.Expect(opcode.CMSGLootMethod, 2*time.Second); err != nil || string(p.Body) != string(game.BuildLootMethod(game.LootFreeForAll, 0, 2)) {
		t.Fatalf("%v %+v", err, p)
	}
	r.srv.Send(opcode.SMSGGroupList, gametest.GroupListLoot(0x2A, game.LootFreeForAll, 0, 2, gorm))
	if out := <-res; out["error"] != nil {
		t.Fatalf("loot_method: %v", out)
	}
	if p := r.call("party", "{}")["result"].(map[string]any); p["loot_method"] != "free_for_all" {
		t.Fatalf("party %v", p)
	}
}
