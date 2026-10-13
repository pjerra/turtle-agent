package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// chatBody transcribes ChatHandler::BuildChatPacket's default and say branches (Chat.cpp:2251-2314).
func chatBody(t game.ChatType, sender packet.GUID, msg string) []byte {
	w := packet.NewWriter().U8(uint8(t)).U32(0).GUID(sender)
	if t == game.ChatSay || t == game.ChatParty || t == game.ChatYell {
		w.GUID(sender)
	}
	return w.U32(uint32(len(msg) + 1)).CString(msg).U8(0).Bytes()
}

func nameResponse(g packet.GUID, name string) []byte {
	return packet.NewWriter().GUID(g).CString(name).U8(0).U32(1).U32(0).U32(1).Bytes()
}

// wolfGUID is a Young Wolf (entry 299) spawn.
const wolfGUID packet.GUID = 0xF13000012B000001

// creatureResponse answers a creature query for entry the way QueryHandler.cpp:187-200 writes it.
func creatureResponse(entry uint32, name string) []byte {
	return packet.NewWriter().U32(entry).CString(name).U8(0).U8(0).U8(0).CString("").
		U32(0).U32(1).U32(1).U32(0).U32(0).U32(0).U32(161).U8(0).U8(0).Bytes()
}

// wolfName answers a creature query for entry 299.
func wolfName(h *harness) {
	h.t.Helper()
	h.send(opcode.SMSGCreatureQueryResponse, creatureResponse(299, "Young Wolf"))
}

func TestChatAndGMSendMessagechat(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.Chat(context.Background(), "party", "hello", ""); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGMessagechat); !bytes.Equal(p.Body, packet.NewWriter().U32(1).U32(0).CString("hello").Bytes()) {
		t.Fatalf("% X", p.Body)
	}
	if err := h.s.GM(context.Background(), "gm visible on"); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGMessagechat); !bytes.Equal(p.Body, packet.NewWriter().U32(0).U32(0).CString(".gm visible on").Bytes()) {
		t.Fatalf("% X", p.Body)
	}
	// The server says these aloud instead of running them (Chat.cpp:1769-1779).
	for _, bad := range []string{"", "  ", ".", " . ", "..gm on"} {
		if err := h.s.GM(context.Background(), bad); err == nil {
			t.Errorf("GM(%q) accepted", bad)
		}
	}
	h.logout()
	for _, p := range h.srv.Skipped() { // packets Expect passed over: a refused GM line would be one
		if p.Op == opcode.CMSGMessagechat {
			t.Fatalf("refused GM text was sent: % X", p.Body)
		}
	}
}

func TestChatBeforeWorldFails(t *testing.T) {
	h := start(t, Options{})
	h.expect(opcode.CMSGCharEnum)
	if err := h.s.Chat(context.Background(), "say", "x", ""); !errors.Is(err, ErrNotInWorld) {
		t.Fatalf("err = %v", err)
	}
}

func TestWhisperNameComesFromQuery(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatWhisper, 0x99, "attacking"))
	if p := h.expect(opcode.CMSGNameQuery); packet.NewReader(p.Body).GUID() != 0x99 {
		t.Fatalf("query % X", p.Body)
	}
	h.send(opcode.SMSGNameQueryResponse, nameResponse(0x99, "Gorm"))
	h.send(opcode.SMSGNotification, []byte("Hello\x00"))
	h.logout()
	w := ofKind(h.s.Log(), events.KindWhisper)
	if len(w) != 1 || w[0].Source != "Gorm" || w[0].Text != "attacking" || w[0].Data["type"] != "whisper" {
		t.Fatalf("%+v", w)
	}
	if sys := ofKind(h.s.Log(), events.KindSystem); len(sys) != 1 || sys[0].Text != "Hello" {
		t.Fatalf("%+v", sys)
	}
}

// Review Focus 5: an event waiting for a name is not overtaken, and it is logged
// with what is known once the name wait runs out.
func TestNameWaitKeepsOrder(t *testing.T) {
	h := start(t, Options{NamesWait: 150 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatWhisper, 0x77, "first"))
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatSystem, 0, "second"))
	h.expect(opcode.CMSGNameQuery) // never answered
	// The wait ran out on a tick, not at logout: both are already logged, in order.
	waitFor(t, func() bool { return len(h.s.Log().Read(events.Query{Kinds: events.ChatKinds}).Events) == 2 })
	if p := h.s.Log().Read(events.Query{Kinds: events.ChatKinds}); len(p.Events) != 2 ||
		p.Events[0].Text != "first" || p.Events[0].Source != "" || p.Events[1].Text != "second" {
		t.Fatalf("before logout: %+v", p.Events)
	}
	h.send(opcode.SMSGChatPlayerNotFound, []byte("Nobody\x00"))
	h.logout()
	p := h.s.Log().Read(events.Query{Kinds: events.ChatKinds})
	if len(p.Events) != 3 || p.Events[0].Text != "first" || p.Events[0].Source != "" ||
		p.Events[1].Text != "second" || p.Events[2].Data["type"] != "player_not_found" {
		t.Fatalf("%+v", p.Events)
	}
	for i := 1; i < len(p.Events); i++ {
		if p.Events[i].Seq <= p.Events[i-1].Seq {
			t.Fatal("sequence not increasing")
		}
	}
}

// A name query for an event queued behind a waiting one goes out at once, so the
// second event does not lose its name to the first one's wait.
func TestQueuedEventAsksForItsNameAtOnce(t *testing.T) {
	h := start(t, Options{NamesWait: 300 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatWhisper, 0x77, "first"))
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatWhisper, 0x88, "second"))
	for _, want := range []packet.GUID{0x77, 0x88} {
		if p := h.expect(opcode.CMSGNameQuery); packet.NewReader(p.Body).GUID() != want {
			t.Fatalf("query % X, want %v", p.Body, want)
		}
	}
	h.send(opcode.SMSGNameQueryResponse, nameResponse(0x88, "Bree"))
	h.logout()
	w := ofKind(h.s.Log(), events.KindWhisper)
	if len(w) != 2 || w[0].Text != "first" || w[0].Source != "" || w[1].Text != "second" || w[1].Source != "Bree" {
		t.Fatalf("%+v", w)
	}
}

// Creature names come from CMSG_CREATURE_QUERY, sent once per entry; an unknown entry
// gets a placeholder name.
func TestCreatureTargetNameComesFromQuery(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	// CHAT_MSG_MONSTER_EMOTE: name, then the target GUID (Chat.cpp:2262-2270).
	emote := func(target packet.GUID, msg string) []byte {
		return packet.NewWriter().U8(uint8(game.ChatMonsterEmote)).U32(0).U32(6).CString("Guard").GUID(target).
			U32(uint32(len(msg) + 1)).CString(msg).U8(0).Bytes()
	}
	h.send(opcode.SMSGMessagechat, emote(wolfGUID, "%s glares."))
	if p := h.expect(opcode.CMSGCreatureQuery); !bytes.Equal(p.Body, game.BuildCreatureQuery(299, wolfGUID)) {
		t.Fatalf("query % X", p.Body)
	}
	h.send(opcode.SMSGMessagechat, emote(wolfGUID+1, "%s glares again.")) // same entry: no second query
	wolfName(h)
	h.send(opcode.SMSGMessagechat, emote(0xF13000012C000002, "%s stares."))
	if p := h.expect(opcode.CMSGCreatureQuery); packet.NewReader(p.Body).U32() != 300 {
		t.Fatalf("second query % X, want entry 300 only", p.Body)
	}
	h.send(opcode.SMSGCreatureQueryResponse, packet.NewWriter().U32(300|0x80000000).Bytes())
	h.logout()
	c := ofKind(h.s.Log(), events.KindChat)
	if len(c) != 3 || c[0].Source != "Guard" || c[0].Target != "Young Wolf" || c[1].Target != "Young Wolf" ||
		c[2].Target != "creature 300" || c[0].Data["type"] != "monster_emote" {
		t.Fatalf("%+v", c)
	}
}

// waitText polls the log until an event with text appears or within runs out.
func waitText(h *harness, text string, within time.Duration) (events.Event, bool) {
	deadline := time.Now().Add(within)
	for {
		for _, e := range h.s.Log().Read(events.Query{Kinds: events.ChatKinds, Limit: events.MaxLimit}).Events {
			if e.Text == text {
				return e, true
			}
		}
		if time.Now().After(deadline) {
			return events.Event{}, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A name query that went unanswered for a whole wait does not stall the next event
// from that GUID again until the query may be repeated.
func TestUnansweredNameDoesNotStallAgain(t *testing.T) {
	h := start(t, Options{NamesWait: time.Second})
	h.enterWorld()
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatWhisper, 0x77, "a"))
	h.expect(opcode.CMSGNameQuery) // never answered
	if _, ok := waitText(h, "a", 3*time.Second); !ok {
		t.Fatal(`"a" never logged`)
	}
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatWhisper, 0x77, "b"))
	if e, ok := waitText(h, "b", 500*time.Millisecond); !ok || e.Source != "" {
		t.Fatalf(`"b" stalled behind the unanswered name: %+v %v`, e, ok)
	}
	h.logout()
}

// GMOn selects the unit and sends the command in one step of the session: however many target calls run at the same
// time, the packet before each command is the selection of the unit it was meant for.
func TestGMOnSelectsAndSendsAtomically(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	const mine, other, n = packet.GUID(0x31), packet.GUID(0x99), 40

	var got []worldconn.Packet
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		chats := 0
		for chats < n {
			p, err := h.srv.Next(3 * time.Second)
			if err != nil {
				return
			}
			got = append(got, p)
			if p.Op == opcode.CMSGMessagechat {
				chats++
			}
		}
	}()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // somebody else keeps selecting another unit (the target RPC, the addon host)
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = h.s.Target(ctx, other, "")
			}
		}
	}()
	for i := 0; i < n; i++ {
		if err := h.s.GMOn(ctx, mine, fmt.Sprintf("aura %d", 1000+i)); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	<-collected

	cmds := 0
	for i, p := range got {
		if p.Op != opcode.CMSGMessagechat {
			continue
		}
		cmds++
		if i == 0 || got[i-1].Op != opcode.CMSGSetSelection || packet.NewReader(got[i-1].Body).GUID() != mine {
			t.Fatalf("command %d (%q) was not right after the selection of its unit", cmds, p.Body)
		}
		if !bytes.Contains(p.Body, []byte(fmt.Sprintf(".aura %d", 1000+cmds-1))) {
			t.Fatalf("command %d: % X", cmds, p.Body)
		}
	}
	if cmds != n {
		t.Fatalf("%d commands seen", cmds)
	}
	if h.s.Status().Target == 0 {
		t.Fatal("no target")
	}
	for _, bad := range []string{"", "..gm"} {
		if err := h.s.GMOn(ctx, mine, bad); err == nil {
			t.Errorf("GMOn(%q) accepted", bad)
		}
	}
	if err := h.s.GMOn(ctx, 0, "aura 1"); err == nil {
		t.Error("GMOn without a unit accepted")
	}
}
