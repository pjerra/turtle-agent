package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
	"github.com/pjerra/turtle-agent/internal/worldconn"
	"github.com/pjerra/turtle-agent/internal/worldconn/worldtest"
)

type agentRig struct {
	t   *testing.T
	s   *session.Session
	srv *worldtest.Server
	ts  *httptest.Server
	run chan error
}

// newRig logs a session into a scripted world server and serves its methods over HTTP.
func newRig(t *testing.T) *agentRig {
	t.Helper()
	return newRigAt(t, session.Options{}, [3]float32{1, 2, 3})
}

// newRigAt is newRig with session options and the position the character enters map 0 at.
func newRigAt(t *testing.T, opts session.Options, pos [3]float32) *agentRig {
	t.Helper()
	key := bytes.Repeat([]byte{7}, 40)
	cli, srvConn := net.Pipe()
	t.Cleanup(func() { cli.Close(); srvConn.Close() })
	ch := make(chan *worldtest.Server, 1)
	go func() { srv, _ := worldtest.Accept(srvConn, key, worldtest.Options{}); ch <- srv }()
	conn, err := worldconn.Client(cli, "TESTACC", key, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &agentRig{t: t, srv: <-ch, run: make(chan error, 1)}
	opts.Character = "Harnessb"
	r.s = session.New(conn, opts)
	go func() { r.run <- r.s.Run(context.Background()) }()
	srv := rpc.NewServer()
	registerMethods(srv, r.s)
	r.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(r.ts.Close)
	r.expect(opcode.CMSGCharEnum)
	enum := packet.NewWriter().U8(1).GUID(0x2A).CString("Harnessb").Append(make([]byte, session.CharEnumTailLen)).Bytes()
	r.srv.Send(opcode.SMSGCharEnum, enum)
	r.expect(opcode.CMSGPlayerLogin)
	r.srv.Send(opcode.SMSGLoginVerifyWorld, packet.NewWriter().U32(0).F32(pos[0]).F32(pos[1]).F32(pos[2]).F32(0).Bytes())
	// Ready never closes when login fails, so Done ends the wait too.
	select {
	case <-r.s.Ready():
	case <-r.s.Done():
		t.Fatalf("login failed: %v", <-r.run)
	case <-time.After(2 * time.Second):
		t.Fatal("not in world after 2 s")
	}
	return r
}

func (r *agentRig) expect(op uint16) {
	r.t.Helper()
	if _, err := r.srv.Expect(op, 2*time.Second); err != nil {
		r.t.Fatal(err)
	}
}

// wolfName answers the creature query for entry 299 (QueryHandler.cpp:187-200).
func (r *agentRig) wolfName() {
	r.srv.Send(opcode.SMSGCreatureQueryResponse, packet.NewWriter().U32(299).CString("Young Wolf").U8(0).U8(0).U8(0).CString("").
		U32(0).U32(1).U32(1).U32(0).U32(0).U32(0).U32(161).U8(0).U8(0).Bytes())
}

func (r *agentRig) call(method, params string) map[string]any {
	r.t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	resp, err := http.Post(r.ts.URL+"/rpc", "application/json", strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func TestStatusChatAndChatLog(t *testing.T) {
	r := newRig(t)
	if st := r.call("status", "{}")["result"].(map[string]any); st["state"] != "in_world" || st["character"] != "Harnessb" {
		t.Fatalf("%v", st)
	}
	if out := r.call("chat", `{"type":"say","text":"hi"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGMessagechat)
	if out := r.call("chat", `{"type":"system","text":"hi"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		r.srv.Send(opcode.SMSGNotification, []byte("You are now visible.\x00"))
	}()
	page := r.call("chat_log", `{"since":0,"wait_ms":3000}`)["result"].(map[string]any)
	evs := page["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["text"] != "You are now visible." {
		t.Fatalf("%v", page)
	}
}

// The HeadlessClient (wow-play-harness) reads status.last_seq, sends gm {text} and pages
// events by next/more/dropped/oldest; this pins those names.
func TestHeadlessClientShapes(t *testing.T) {
	r := newRig(t)
	st := r.call("status", "{}")["result"].(map[string]any)
	last, ok := st["last_seq"].(float64)
	if !ok || last < 1 {
		t.Fatalf("last_seq: %v", st)
	}
	if out := r.call("gm", `{"text":"gm visible on"}`); out["error"] != nil || len(out["result"].(map[string]any)) != 0 {
		t.Fatalf("%v", out)
	}
	if p, err := r.srv.Expect(opcode.CMSGMessagechat, 2*time.Second); err != nil || !bytes.Contains(p.Body, []byte(".gm visible on\x00")) {
		t.Fatalf("%v %q", err, p.Body)
	}
	if out := r.call("chat", `{"type":"whisper","to":"Someone","text":"psst"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGMessagechat)
	page := r.call("events", `{"since":0,"kinds":["session"],"limit":5000}`)["result"].(map[string]any)
	for _, k := range []string{"events", "next", "more", "dropped", "oldest"} {
		if _, ok := page[k]; !ok {
			t.Fatalf("page lacks %q: %v", k, page)
		}
	}
	if page["next"] != last || page["more"] != false || page["dropped"] != false || len(page["events"].([]any)) != 1 {
		t.Fatalf("%v", page)
	}
	if out := r.call("events", `[0]`); errCode(out) != rpc.CodeInvalidParams {
		t.Fatalf("positional params: %v", out)
	}
}

// Ruling T7 minor 1: a cursor past the end of the log (a client that outlived an agent
// restart) is clamped, so a wait returns on the next append instead of blocking.
func TestStaleCursorWaitReturnsOnNextAppend(t *testing.T) {
	log := events.New(100)
	for i := 0; i < 3; i++ {
		log.Append(events.Event{Kind: events.KindSystem, Text: "old"})
	}
	got := make(chan events.Page, 1)
	go func() { got <- readEvents(context.Background(), log, events.Query{Since: 1000}, 3000) }()
	time.Sleep(50 * time.Millisecond)
	log.Append(events.Event{Kind: events.KindSystem, Text: "new"})
	select {
	case p := <-got:
		if len(p.Events) != 1 || p.Events[0].Text != "new" || p.Next != 4 {
			t.Fatalf("%+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("stale cursor blocked past the next append")
	}
}

// Ruling T14 minor 6: a cursor ahead of Last() means the agent restarted under the
// client, so the clamped page reports Dropped, with or without a wait.
func TestStaleCursorReportsDropped(t *testing.T) {
	log := events.New(100)
	for i := 0; i < 3; i++ {
		log.Append(events.Event{Kind: events.KindSystem, Text: "old"})
	}
	if p := readEvents(context.Background(), log, events.Query{Since: 3}, 0); p.Dropped || p.Next != 3 {
		t.Fatalf("cursor at Last(): %+v", p)
	}
	if p := readEvents(context.Background(), log, events.Query{Since: 1000}, 0); !p.Dropped || len(p.Events) != 0 || p.Next != 3 {
		t.Fatalf("no wait: %+v", p)
	}
	got := make(chan events.Page, 1)
	go func() { got <- readEvents(context.Background(), log, events.Query{Since: 1000}, 3000) }()
	time.Sleep(50 * time.Millisecond)
	log.Append(events.Event{Kind: events.KindSystem, Text: "new"})
	if p := <-got; len(p.Events) != 1 || p.Events[0].Text != "new" || !p.Dropped {
		t.Fatalf("after wait: %+v", p)
	}
}

func TestReadEventsWaitBoundsAndFilters(t *testing.T) {
	log := events.New(100)
	start := time.Now()
	if p := readEvents(context.Background(), log, events.Query{}, 100); len(p.Events) != 0 || time.Since(start) < 90*time.Millisecond {
		t.Fatalf("%+v after %s", p, time.Since(start))
	}
	got := make(chan events.Page, 1)
	go func() {
		got <- readEvents(context.Background(), log, events.Query{Kinds: []string{events.KindWhisper}}, 3000)
	}()
	time.Sleep(50 * time.Millisecond)
	log.Append(events.Event{Kind: events.KindSystem, Text: "skip"})
	log.Append(events.Event{Kind: events.KindWhisper, Text: "match"})
	if p := <-got; len(p.Events) != 1 || p.Events[0].Text != "match" || p.Next != 2 {
		t.Fatalf("%+v", p)
	}
}

// A gap before the cursor, seen on the first read, is still reported when the wait
// returns a later page (the follow-up reads start past the gap).
func TestReadEventsKeepsDroppedAcrossWait(t *testing.T) {
	log := events.New(10)
	for i := 0; i < 11; i++ { // the 11th append drops the oldest tenth (seq 1)
		log.Append(events.Event{Kind: events.KindSystem})
	}
	got := make(chan events.Page, 1)
	go func() {
		got <- readEvents(context.Background(), log, events.Query{Kinds: []string{events.KindWhisper}}, 3000)
	}()
	time.Sleep(50 * time.Millisecond)
	log.Append(events.Event{Kind: events.KindWhisper, Text: "match"})
	if p := <-got; len(p.Events) != 1 || !p.Dropped {
		t.Fatalf("%+v", p)
	}
}

// The HeadlessClient reads party.members[].{name,health,max_health}; members is [] outside a group.
func TestPartyMethods(t *testing.T) {
	r := newRig(t)
	if p := r.call("party", "{}")["result"].(map[string]any); p["in_group"] != false || p["members"] == nil || len(p["members"].([]any)) != 0 {
		t.Fatalf("no group: %v", p)
	}
	if out := r.call("accept_invite", "{}"); errCode(out) != rpc.CodeGame {
		t.Fatalf("accept without invite: %v", out)
	}
	if out := r.call("leave_group", "{}"); errCode(out) != rpc.CodeGame {
		t.Fatalf("leave without group: %v", out)
	}
	if out := r.call("invite", `{"name":""}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("blank invite: %v", out)
	}
	if out := r.call("invite", `{"name":"Gorm"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGGroupInvite)
	r.srv.Send(opcode.SMSGGroupList, gametest.GroupList(0x2A, game.GroupMember{Name: "Gorm", GUID: 0x30, Status: game.MemberOnline}))
	r.expect(opcode.CMSGRequestPartyMemberStats)
	stats := packet.NewWriter().PackedGUID(0x30).U32(game.GroupUpdateCurHP | game.GroupUpdateMaxHP).U16(500).U16(600).Bytes()
	r.srv.Send(opcode.SMSGPartyMemberStatsFull, stats)
	var m map[string]any
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		members := r.call("party", "{}")["result"].(map[string]any)["members"].([]any)
		if len(members) == 1 && members[0].(map[string]any)["health"] == float64(500) {
			m = members[0].(map[string]any)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("members %v", members)
		}
	}
	if m["name"] != "Gorm" || m["max_health"] != float64(600) || m["online"] != true {
		t.Fatalf("%v", m)
	}
	if out := r.call("leave_group", "{}"); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGGroupDisband)
}

func TestRPCErrMapping(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
	}{
		{session.ErrNotInWorld, rpc.CodeNotInWorld},
		{session.ErrDisconnected, rpc.CodeDisconnected},
		{session.ErrClosed, rpc.CodeDisconnected},
		{context.DeadlineExceeded, rpc.CodeTimeout},
		{context.Canceled, rpc.CodeDisconnected},
		{&session.LogoutError{Reason: 1}, rpc.CodeGame},
		{fmt.Errorf("%w: CMSG_PING: %w", worldconn.ErrWrite, net.ErrClosed), rpc.CodeDisconnected},
		{errors.New("world: CMSG_MESSAGECHAT body is 10300 bytes"), rpc.CodeGame},
	} {
		var re *rpc.Error
		if !errors.As(rpcErr(c.err), &re) || re.Code != c.code {
			t.Errorf("%v: %v, want %d", c.err, rpcErr(c.err), c.code)
		}
	}
	re := rpcErr(&session.LogoutError{Reason: 1}).(*rpc.Error)
	if re.Data.(map[string]any)["reason"] != uint32(1) {
		t.Fatalf("%+v", re)
	}
	if rpcErr(nil) != nil {
		t.Fatal("nil error mapped")
	}
}

func TestLogoutRefusedThenComplete(t *testing.T) {
	r := newRig(t)
	got := make(chan map[string]any, 1)
	go func() { got <- r.call("logout", "{}") }()
	r.expect(opcode.CMSGLogoutRequest)
	r.srv.Send(opcode.SMSGLogoutResponse, packet.NewWriter().U32(1).U8(0).Bytes())
	out := <-got
	if errCode(out) != rpc.CodeGame || out["error"].(map[string]any)["data"].(map[string]any)["reason"] != float64(1) {
		t.Fatalf("refused: %v", out)
	}
	go func() { got <- r.call("logout", "{}") }()
	r.expect(opcode.CMSGLogoutRequest)
	r.srv.Send(opcode.SMSGLogoutResponse, packet.NewWriter().U32(0).U8(1).Bytes())
	r.srv.Send(opcode.SMSGLogoutComplete, nil)
	if out := <-got; out["error"] != nil || len(out["result"].(map[string]any)) != 0 {
		t.Fatalf("logout: %v", out)
	}
	if err := <-r.run; err != nil {
		t.Fatal(err)
	}
	if out := r.call("gm", `{"text":".gm on"}`); errCode(out) != rpc.CodeDisconnected {
		t.Fatalf("after logout: %v", out)
	}
}

// Review Focus 4: after the server drops the connection, calls fail fast with -32002.
func TestRPCAfterDisconnect(t *testing.T) {
	r := newRig(t)
	r.srv.Close()
	<-r.run
	start := time.Now()
	if out := r.call("chat", `{"type":"say","text":"hi"}`); errCode(out) != rpc.CodeDisconnected {
		t.Fatalf("%v", out)
	}
	if time.Since(start) > time.Second {
		t.Fatal("call after disconnect was slow")
	}
	if st := r.call("status", "{}")["result"].(map[string]any); st["state"] != "disconnected" || st["error"] == "" {
		t.Fatalf("%v", st)
	}
}

func errCode(m map[string]any) int {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(float64)
	return int(c)
}

// units is a bare array (never null) of {guid, name, health, max_health, dead, ...}; status
// adds the player's own health and power (T14 contract, read by wowharness/headless.py).
func TestUnitsAndStatusHealth(t *testing.T) {
	r := newRig(t)
	if out := r.call("units", "{}"); out["error"] != nil || out["result"] == nil || len(out["result"].([]any)) != 0 {
		t.Fatalf("no units: %v", out)
	}
	const wolf packet.GUID = 0xF13000012B000001
	w := objtest.Header(2)
	for _, u := range []struct {
		g      packet.GUID
		typ    uint8
		x      float32
		hp, mx uint32
	}{{0x2A, objects.TypePlayer, 1, 700, 800}, {wolf, objects.TypeUnit, 11, 0, 50}} {
		w.U8(uint8(objects.BlockCreate2)).PackedGUID(u.g).U8(u.typ)
		objtest.Living(w, 0x20, game.MoveInfo{X: u.x, Y: 2, Z: 3}, nil)
		objtest.Values(w, 192, map[uint16]uint32{objects.UnitHealth: u.hp, objects.UnitMaxHealth: u.mx, objects.UnitPower1: 10, objects.UnitMaxPower1: 20})
	}
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
	r.expect(opcode.CMSGCreatureQuery)
	r.wolfName()
	var us []any
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		us = r.call("units", `{"name":"wolf","limit":200}`)["result"].([]any)
		if len(us) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("units %v", us)
		}
	}
	u := us[0].(map[string]any)
	if u["guid"] != "0xF13000012B000001" || u["name"] != "Young Wolf" || u["health"] != float64(0) || u["max_health"] != float64(50) || u["dead"] != true {
		t.Fatalf("%v", u)
	}
	if out := r.call("units", `{"name":"bear"}`)["result"].([]any); len(out) != 0 {
		t.Fatalf("bear %v", out)
	}
	if out := r.call("units", `{"hostile":false,"near":5}`)["result"].([]any); len(out) != 0 {
		t.Fatalf("near %v", out)
	}
	st := r.call("status", "{}")["result"].(map[string]any)
	if st["health"] != float64(700) || st["max_health"] != float64(800) || st["power"] != float64(10) || st["max_power"] != float64(20) {
		t.Fatalf("status %v", st)
	}
}

// TestActionMethodShapes sends the params the Task 29 MCP tools and the MVP script send.
func TestActionMethodShapes(t *testing.T) {
	r := newRig(t)
	const wolf packet.GUID = 0xF13000012B000001
	w := objtest.Header(1).U8(uint8(objects.BlockCreate2)).PackedGUID(wolf).U8(objects.TypeUnit)
	objtest.Living(w, 0x20, game.MoveInfo{X: 11, Y: 2, Z: 3}, nil)
	objtest.Values(w, 192, map[uint16]uint32{objects.UnitHealth: 50, objects.UnitMaxHealth: 50})
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
	r.wolfName()
	var u map[string]any
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		out := r.call("target", `{"name":"Young Wolf"}`)
		if res, ok := out["result"].(map[string]any); ok {
			u = res
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("target %v", out)
		}
	}
	if u["guid"] != "0xF13000012B000001" || u["name"] != "Young Wolf" || u["max_health"] != float64(50) {
		t.Fatalf("%v", u)
	}
	if st := r.call("status", "{}")["result"].(map[string]any); st["target"] != u["guid"] {
		t.Fatalf("status target %v", st["target"])
	}
	r.expect(opcode.CMSGSetSelection)
	for _, c := range []struct{ method, params string }{
		{"target", `{"guid":"0xF13000012B000001"}`},
		{"attack", `{}`},
		{"stop_attack", `{}`},
		{"cast", `{"spell_id":133}`},
		{"cast", `{"spell_id":133,"target":"0xF13000012B000001"}`},
		{"face", `{"guid":"0xF13000012B000001"}`},
		{"walk_to", `{"x":1,"y":2,"z":3}`},
	} {
		if out := r.call(c.method, c.params); out["error"] != nil {
			t.Fatalf("%s %s: %v", c.method, c.params, out["error"])
		}
	}
	if out := r.call("target", `{}`); out["error"] == nil {
		t.Fatal("target without guid or name")
	}
	if out := r.call("walk_to", `{"x":1,"y":2,"z":3,"map":0}`); out["error"] == nil {
		t.Fatal("walk_to accepted a map")
	}
	tp := make(chan map[string]any, 1)
	go func() { tp <- r.call("teleport", `{"x":1,"y":2,"z":3,"map":0}`) }()
	p, err := r.srv.Expect(opcode.CMSGMessagechat, 2*time.Second)
	if err != nil || !strings.Contains(string(p.Body), ".go xyz 1.00 2.00 3.00 0") {
		t.Fatalf("% X %v", p.Body, err)
	}
	r.srv.Send(opcode.SMSGNewWorld, packet.NewWriter().U32(0).F32(1).F32(2).F32(3).F32(0).Bytes())
	if out := <-tp; out["error"] != nil {
		t.Fatalf("teleport %v", out["error"])
	}
}

func TestPathMethodsWithoutMmapsDir(t *testing.T) {
	r := newRig(t)
	for _, m := range []string{"path_to", "walk_path"} {
		out := r.call(m, `{"x":1,"y":2,"z":3}`)
		if errCode(out) != rpc.CodeGame || !strings.Contains(out["error"].(map[string]any)["message"].(string), "mmaps_dir") {
			t.Fatalf("%s: %v", m, out)
		}
	}
}
