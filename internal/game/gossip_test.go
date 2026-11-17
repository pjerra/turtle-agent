package game_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const recruiter packet.GUID = 0xF130001F4A000123

// HandleGossipHelloOpcode reads the NPC GUID (NPCHandler.cpp:368-373);
// HandleGossipSelectOptionOpcode reads GUID, option, and the code only when the server's
// menu marks that option coded (NPCHandler.cpp:405-420).
func TestBuildGossip(t *testing.T) {
	if b := game.BuildGossipHello(recruiter); !bytes.Equal(b, packet.NewWriter().GUID(recruiter).Bytes()) {
		t.Fatalf("hello % X", b)
	}
	if b := game.BuildGossipSelectOption(recruiter, 2, "", false); !bytes.Equal(b, packet.NewWriter().GUID(recruiter).U32(2).Bytes()) {
		t.Fatalf("select % X", b)
	}
	if b := game.BuildGossipSelectOption(recruiter, 1, "1234", true); !bytes.Equal(b, packet.NewWriter().GUID(recruiter).U32(1).CString("1234").Bytes()) {
		t.Fatalf("coded % X", b)
	}
	// A coded option always carries the string, even an empty one: the core reads it.
	if b := game.BuildGossipSelectOption(recruiter, 1, "", true); !bytes.Equal(b, packet.NewWriter().GUID(recruiter).U32(1).CString("").Bytes()) {
		t.Fatalf("coded empty % X", b)
	}
}

func TestParseGossipMessage(t *testing.T) {
	want := game.GossipMenu{NPC: recruiter, TextID: 907,
		Options: []game.GossipOption{{Index: 0, Icon: 0, Text: "Hire a companion"}, {Index: 1, Icon: 6, Coded: true, Text: "Enter a code"}},
		Quests:  []game.GossipQuest{{ID: 7, Icon: 2, Level: 10, Title: "Kobold Camp Cleanup"}}}
	got, err := game.ParseGossipMessage(gametest.GossipMessage(recruiter, 907, want.Options, want.Quests))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v\nwant %+v", got, err, want)
	}
	empty, err := game.ParseGossipMessage(gametest.GossipMessage(recruiter, 1, nil, nil))
	if err != nil || empty.NPC != recruiter || len(empty.Options) != 0 || len(empty.Quests) != 0 {
		t.Fatalf("%+v %v", empty, err)
	}
	// A count the body cannot hold fails instead of looping.
	if _, err := game.ParseGossipMessage(packet.NewWriter().GUID(recruiter).U32(1).U32(0xFFFFFFFF).Bytes()); err == nil {
		t.Fatal("truncated menu accepted")
	}
	if o, ok := want.Option(1); !ok || !o.Coded {
		t.Fatal("Option(1)")
	}
	if _, ok := want.Option(5); ok {
		t.Fatal("Option(5)")
	}
}
