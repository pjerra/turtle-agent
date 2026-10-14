package session

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

func itemResponse(entry, quality, ilvl uint32) []byte {
	return packet.NewWriter().U32(entry).U32(4).U32(0).CString("Thing").U8(0).U8(0).U8(0).
		U32(1).U32(quality).U32(0).U32(0).U32(0).U32(5).U32(0xFFFFFFFF).U32(0xFFFFFFFF).U32(ilvl).U32(40).Bytes()
}

// ItemInfos asks the server once per entry, answers from the cache after, and gives an entry the server never answers back as missing
// within the wait.
func TestItemInfosAskOnceAndCache(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	type res struct {
		m   map[uint32]struct{ q, l uint32 }
		err error
	}
	done := make(chan res, 1)
	go func() {
		m, err := h.s.ItemInfos(context.Background(), []uint32{58092, 9641, 58092, 0, 777}, 400*time.Millisecond)
		out := map[uint32]struct{ q, l uint32 }{}
		for e, it := range m {
			out[e] = struct{ q, l uint32 }{it.Quality, it.ItemLevel}
		}
		done <- res{out, err}
	}()
	asked := map[uint32]bool{}
	for i := 0; i < 3; i++ {
		p := h.expect(opcode.CMSGItemQuerySingle)
		asked[packet.NewReader(p.Body).U32()] = true
	}
	if !asked[58092] || !asked[9641] || !asked[777] || len(asked) != 3 {
		t.Fatalf("asked %v", asked)
	}
	h.send(opcode.SMSGItemQuerySingleResponse, itemResponse(58092, 4, 60))
	h.send(opcode.SMSGItemQuerySingleResponse, packet.NewWriter().U32(9641|0x80000000).Bytes())
	r := <-done // 777 never answers: the call returns after its wait
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := r.m[58092]; got.q != 4 || got.l != 60 || len(r.m) != 2 {
		t.Fatalf("%v", r.m)
	}
	// Both answered entries are cached: a second call sends nothing and answers at once.
	m, err := h.s.ItemInfos(context.Background(), []uint32{58092, 9641}, time.Second)
	if err != nil || len(m) != 2 || !m[58092].Found || m[9641].Found {
		t.Fatalf("%+v %v", m, err)
	}
}
