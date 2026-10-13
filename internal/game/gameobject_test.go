package game

import (
	"bytes"
	"testing"

	"github.com/pjerra/turtle-agent/internal/capture"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

func TestGameObjectQueryAndUse(t *testing.T) {
	const g packet.GUID = 0xF11001E5D3003839
	if b := BuildGameObjectQuery(124371, g); !bytes.Equal(b, packet.NewWriter().U32(124371).GUID(g).Bytes()) {
		t.Fatalf("% X", b)
	}
	if b := BuildGameObjectUse(g); !bytes.Equal(b, packet.NewWriter().GUID(g).Bytes()) || len(b) != 8 {
		t.Fatalf("the use packet is the full guid: % X", b)
	}
	w := packet.NewWriter().U32(124371).U32(0).U32(1369).CString("Keystone").U8(0).U8(0).U8(0).U8(0) // QueryHandler.cpp:250-256
	for i := 0; i < 24; i++ {
		w.U32(uint32(i + 1))
	}
	info, err := ParseGameObjectQueryResponse(w.Bytes())
	if err != nil || !info.Found || info.Entry != 124371 || info.Name != "Keystone" || info.Type != 0 || info.DisplayID != 1369 || info.Data[0] != 1 || info.Data[23] != 24 {
		t.Fatalf("%+v %v", info, err)
	}
	info, err = ParseGameObjectQueryResponse(packet.NewWriter().U32(124371 | 0x80000000).Bytes()) // :261-268
	if err != nil || info.Found || info.Entry != 124371 {
		t.Fatalf("unknown entry: %+v %v", info, err)
	}
	if _, err := ParseGameObjectQueryResponse(w.Bytes()[:30]); err == nil {
		t.Fatal("a cut-off response parsed")
	}
}

func TestGameObjectAnim(t *testing.T) {
	const g packet.GUID = 0xF11001E5D3003839
	a, err := ParseGameObjectAnim(packet.NewWriter().GUID(g).U32(3).Bytes(), true) // GameObject.cpp:2478-2484
	if err != nil || a != (GameObjectAnim{GUID: g, Anim: 3}) {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = ParseGameObjectAnim(packet.NewWriter().GUID(g).Bytes(), false) // :2486-2491
	if err != nil || a != (GameObjectAnim{GUID: g}) {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := ParseGameObjectAnim(packet.NewWriter().GUID(g).Bytes(), true); err == nil {
		t.Fatal("a custom animation without its id parsed")
	}
	if _, err := ParseGameObjectAnim([]byte{1, 2, 3}, false); err == nil {
		t.Fatal("a short guid parsed")
	}
}

// Uldaman, 2026-10-13 (m910q, build 17): the server answered 140 game object queries, and the
// agent's six uses of game objects went out as the 8-byte guid.
func TestGameObjectPacketsFromCapture(t *testing.T) {
	recs := fixture(t, "m7-gameobjects.twcap")
	names := map[uint32]string{}
	for _, body := range capture.Bodies(recs, opcode.SMSGGameobjectQueryResponse) {
		info, err := ParseGameObjectQueryResponse(body)
		if err != nil || !info.Found || info.Name == "" {
			t.Fatalf("% X: %+v %v", body, info, err)
		}
		names[info.Entry] = info.Name
	}
	if len(names) < 100 || names[124371] != "Keystone" || names[3707] != "Food Crate" || names[131978] != "Large Mithril Bound Chest" {
		t.Fatalf("%d names, Keystone %q", len(names), names[124371])
	}
	uses := 0
	for _, r := range recs {
		if r.Dir == worldconn.ClientToServer && r.Op == opcode.CMSGGameobjUse {
			uses++
			if len(r.Body) != 8 {
				t.Fatalf("use packet % X", r.Body)
			}
		}
		if r.Dir == worldconn.ClientToServer && r.Op == opcode.CMSGGameobjectQuery {
			q := packet.NewReader(r.Body)
			if entry, g := q.U32(), q.GUID(); q.Err() != nil || !g.IsGameObject() || g.Entry() != entry || names[entry] == "" {
				t.Fatalf("query % X", r.Body)
			}
		}
	}
	if uses != 6 {
		t.Fatalf("%d uses", uses)
	}
}
