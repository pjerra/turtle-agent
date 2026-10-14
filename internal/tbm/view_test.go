package tbm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// frameLines builds a frame the way the module writes it (docs/guides/player-controls.md, "Perception feed").
func frameLines(seq uint64, party, hostiles, threats []string) []string {
	pre := fmt.Sprintf("TBM:VIEW|v1|%d|", seq)
	lines := []string{pre + fmt.Sprintf("B|0x2A,%d,%d,%d", len(party), len(hostiles), len(threats))}
	if len(party) > 0 {
		lines = append(lines, pre+"P|"+strings.Join(party, ";"))
	}
	if len(hostiles) > 0 {
		lines = append(lines, pre+"H|"+strings.Join(hostiles, ";"))
	}
	if len(threats) > 0 {
		lines = append(lines, pre+"T|"+strings.Join(threats, ";"))
	}
	return append(lines, pre+fmt.Sprintf("E|%d", len(lines)+1))
}

const (
	tankRec  = "0x2A,Tanky,4000,5000,35,100,1,-8913.2,554.5,93.1,1.50,0,0,0xF130000C5E0012AB,0,0,c,heroic strike"
	healRec  = "0x2B,Healy,2000,3000,500,1000,0,-8915.0,556.0,93.1,1.50,0,0,0,0,0,c,-"
	mobRec   = "0xF130000C5E0012AB,3874,-8900.0,560.0,94.0,64,0x2A,0,-"
	mob2Rec  = "0xF130000C5E0012AC,3875,-8890.0,561.0,94.0,100,0,0,s"
	threatRc = "0xF130000C5E0012AB,0x2A:100,0x2B:41"
)

// The example of the documentation, line for line.
var docExample = []string{
	"TBM:VIEW|v1|12|B|0x2A,1,1,1",
	"TBM:VIEW|v1|12|P|0x2A,Tanky,4000,5000,35,100,1,-8913.2,554.5,93.1,1.50,0,0,0xF130000C5E0012AB,0,0,c,heroic strike",
	"TBM:VIEW|v1|12|H|0xF130000C5E0012AB,3874,-8900.0,560.0,94.0,64,0x2A,0,-",
	"TBM:VIEW|v1|12|T|0xF130000C5E0012AB,0x2A:100,0x2B:41",
	"TBM:VIEW|v1|12|E|5",
}

func feedAll(a *ViewAssembler, lines []string) (frames []*ViewFrame) {
	for _, l := range lines {
		if f, ok := a.Feed(l); ok {
			frames = append(frames, f)
		}
	}
	return
}

func TestAssembleDocumentedExample(t *testing.T) {
	a := NewViewAssembler()
	fr := feedAll(a, docExample)
	if len(fr) != 1 {
		t.Fatalf("frames: %d, stats %+v", len(fr), a.Stats())
	}
	f := fr[0]
	if f.Seq != 12 || f.Lead != 0x2A || f.Lines != 5 || f.Version != "v1" {
		t.Fatalf("frame %+v", f)
	}
	p := f.Party[0]
	if p.Name != "Tanky" || p.HP != 4000 || p.MaxHP != 5000 || p.PowerType != 1 || p.X != -8913.2 || p.O != 1.5 ||
		p.Target != 0xF130000C5E0012AB || p.Dead || p.State != "c" || p.Action != "heroic strike" || p.HPPct() != 80 {
		t.Fatalf("party %+v", p)
	}
	h := f.Hostiles[0]
	if h.Entry != 3874 || h.HPPct != 64 || h.Target != 0x2A || h.Flags != "-" || h.Stealthed() || h.Evading() {
		t.Fatalf("hostile %+v", h)
	}
	g, ok := f.ThreatHolder(h.GUID)
	if !ok || g != 0x2A {
		t.Fatalf("holder %v %v", g, ok)
	}
	if pct, listed := f.ThreatOf(h.GUID, 0x2B); !listed || pct != 41 {
		t.Fatalf("threat of the healer: %d %v", pct, listed)
	}
	if _, listed := f.ThreatOf(h.GUID, 0x2C); listed {
		t.Fatal("an unlisted bot is not listed")
	}
	if !f.PartyInCombat() || len(f.EngagedHostiles()) != 1 || f.AllDead() {
		t.Fatal("combat state")
	}
	if lead, ok := f.LeadRec(); !ok || lead.Name != "Tanky" {
		t.Fatalf("lead %+v", lead)
	}
}

func TestAssembleSplitAndFlags(t *testing.T) {
	a := NewViewAssembler()
	// A part may span several lines.
	lines := []string{
		"TBM:VIEW|v1|3|B|0x2A,2,2,0",
		"TBM:VIEW|v1|3|P|" + tankRec,
		"TBM:VIEW|v1|3|P|" + healRec,
		"TBM:VIEW|v1|3|H|" + mobRec,
		"TBM:VIEW|v1|3|H|" + mob2Rec,
		"TBM:VIEW|v1|3|E|6",
	}
	fr := feedAll(a, lines)
	if len(fr) != 1 || len(fr[0].Party) != 2 || len(fr[0].Hostiles) != 2 || len(fr[0].Threats) != 0 {
		t.Fatalf("%+v %+v", fr, a.Stats())
	}
	if !fr[0].Hostiles[1].Stealthed() || fr[0].Hostiles[1].HPPct != 100 {
		t.Fatalf("flags %+v", fr[0].Hostiles[1])
	}
	if !fr[0].Party[1].UsesMana() || fr[0].Party[1].PowerPct() != 50 {
		t.Fatalf("healer %+v", fr[0].Party[1])
	}
}

func TestAssembleEmptyFrame(t *testing.T) {
	a := NewViewAssembler()
	fr := feedAll(a, []string{"TBM:VIEW|v1|1|B|0,0,0,0", "TBM:VIEW|v1|1|E|2"})
	if len(fr) != 1 || len(fr[0].Party) != 0 || fr[0].Party == nil {
		t.Fatalf("%+v", fr)
	}
}

func TestMissedFramesAndStaleLines(t *testing.T) {
	a := NewViewAssembler()
	feedAll(a, frameLines(1, []string{tankRec}, nil, nil))
	// Frames 2-4 never arrive.
	fr := feedAll(a, frameLines(5, []string{tankRec}, nil, nil))
	if len(fr) != 1 || a.Stats().Missed != 3 || a.Stats().LastSeq != 5 {
		t.Fatalf("%+v", a.Stats())
	}
	// Frame 4 arrives late: dropped as stale, never replaces frame 5.
	if fr := feedAll(a, frameLines(4, []string{tankRec}, nil, nil)); len(fr) != 0 {
		t.Fatal("a late frame was published")
	}
	if a.Stats().Stale != 1 {
		t.Fatalf("%+v", a.Stats())
	}
	// A duplicate of the newest frame is stale too.
	if fr := feedAll(a, frameLines(5, []string{tankRec}, nil, nil)); len(fr) != 0 {
		t.Fatal("a duplicate was published")
	}
}

func TestLostBeginAndEnd(t *testing.T) {
	a := NewViewAssembler()
	// No B: the frame never completes, the lines are orphans.
	lines := frameLines(7, []string{tankRec}, []string{mobRec}, nil)
	if fr := feedAll(a, lines[1:]); len(fr) != 0 {
		t.Fatal("a frame without B was published")
	}
	if a.Stats().Orphans == 0 {
		t.Fatalf("%+v", a.Stats())
	}
	// No E: it is dropped when the next frame completes.
	b := NewViewAssembler()
	lines = frameLines(1, []string{tankRec}, nil, nil)
	feedAll(b, lines[:len(lines)-1])
	if fr := feedAll(b, frameLines(2, []string{tankRec}, nil, nil)); len(fr) != 1 || b.Stats().Incomplete != 1 {
		t.Fatalf("%+v", b.Stats())
	}
}

func TestReorderedLines(t *testing.T) {
	a := NewViewAssembler()
	lines := frameLines(9, []string{tankRec}, []string{mobRec}, []string{threatRc})
	// B, E, then the rest: the frame is whole when the last line arrives.
	order := []int{0, 4, 1, 3, 2}
	var got []*ViewFrame
	for _, i := range order {
		if f, ok := a.Feed(lines[i]); ok {
			got = append(got, f)
		}
	}
	if len(got) != 1 || got[0].Seq != 9 || len(got[0].Threats) != 1 {
		t.Fatalf("%+v %+v", got, a.Stats())
	}
}

func TestInterleavedFrames(t *testing.T) {
	a := NewViewAssembler()
	f1 := frameLines(1, []string{tankRec}, nil, nil)
	f2 := frameLines(2, []string{tankRec, healRec}, nil, nil)
	var got []*ViewFrame
	for _, l := range []string{f1[0], f2[0], f1[1], f2[1], f1[2], f2[2]} {
		if f, ok := a.Feed(l); ok {
			got = append(got, f)
		}
	}
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 || len(got[1].Party) != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestValidation(t *testing.T) {
	for name, lines := range map[string][]string{
		"E counts more lines than the frame had, with an extra": {
			"TBM:VIEW|v1|1|B|0x2A,1,0,0", "TBM:VIEW|v1|1|P|" + tankRec, "TBM:VIEW|v1|1|P|", "TBM:VIEW|v1|1|E|2"},
		"B counts one party record, the frame has none": {
			"TBM:VIEW|v1|1|B|0x2A,1,0,0", "TBM:VIEW|v1|1|E|2"},
		"B counts no hostile, the frame has one": {
			"TBM:VIEW|v1|1|B|0x2A,0,0,0", "TBM:VIEW|v1|1|H|" + mobRec, "TBM:VIEW|v1|1|E|3"},
		"short party record": {
			"TBM:VIEW|v1|1|B|0x2A,1,0,0", "TBM:VIEW|v1|1|P|0x2A,Tanky,4000", "TBM:VIEW|v1|1|E|3"},
		"bad number in a hostile": {
			"TBM:VIEW|v1|1|B|0x2A,0,1,0", "TBM:VIEW|v1|1|H|0xF1,3874,x,560.0,94.0,64,0x2A,0,-", "TBM:VIEW|v1|1|E|3"},
		"bad threat entry": {
			"TBM:VIEW|v1|1|B|0x2A,0,0,1", "TBM:VIEW|v1|1|T|0xF1,nonsense", "TBM:VIEW|v1|1|E|3"},
		"bad E": {
			"TBM:VIEW|v1|1|B|0x2A,0,0,0", "TBM:VIEW|v1|1|E|x"},
		"bad B": {
			"TBM:VIEW|v1|1|B|zz,0,0,0", "TBM:VIEW|v1|1|E|2"},
		"E total off": {
			"TBM:VIEW|v1|1|B|0x2A,0,0,0", "TBM:VIEW|v1|1|E|9"},
	} {
		a := NewViewAssembler()
		fr := feedAll(a, lines)
		// "E total off" and "E more lines" are held as incomplete rather than invalid when the total is above the lines read.
		if len(fr) != 0 {
			t.Errorf("%s: a bad frame was published: %+v", name, fr[0])
		}
	}
	a := NewViewAssembler()
	feedAll(a, []string{"TBM:VIEW|v1|1|B|0x2A,1,0,0", "TBM:VIEW|v1|1|E|2"})
	if a.Stats().Invalid != 1 || a.Stats().LastInvalid == "" {
		t.Fatalf("%+v", a.Stats())
	}
}

func TestVersionAndGarbage(t *testing.T) {
	a := NewViewAssembler()
	for _, l := range []string{
		"TBM:VIEW|v2|1|B|0x2A,0,0,0", "TBM:VIEW|v2|1|E|2", // another version: counted, skipped
		"TBM:VIEW|v1|x|B|1", "TBM:VIEW|v1|1|Q|1", "TBM:VIEW|v1|1|BB|1", "TBM:VIEW|v1|1", "TBM:VIEW|", // malformed
		"TBM:ACTION_ACK|cmd move|party|1|Tanky", "hello", // not the feed
	} {
		if _, ok := a.Feed(l); ok {
			t.Fatalf("%q published a frame", l)
		}
	}
	s := a.Stats()
	if s.Unsupported != 2 || s.Malformed != 5 || s.Lines != 7 {
		t.Fatalf("%+v", s)
	}
	// A v1 frame after a v2 one still assembles.
	if fr := feedAll(a, frameLines(1, nil, nil, nil)); len(fr) != 1 {
		t.Fatal("v1 after v2")
	}
}

func TestRestartAfterRebind(t *testing.T) {
	a := NewViewAssembler()
	for s := uint64(10); s < 14; s++ {
		feedAll(a, frameLines(s, []string{tankRec}, nil, nil))
	}
	// A new binding counts from 1 again: far behind the newest frame, so it is a restart, not a late frame.
	fr := feedAll(a, frameLines(1, []string{tankRec}, nil, nil))
	if len(fr) != 1 || a.Stats().Restarts != 1 || a.Stats().LastSeq != 1 {
		t.Fatalf("%+v", a.Stats())
	}
	a.Reset()
	if fr := feedAll(a, frameLines(2, []string{tankRec}, nil, nil)); len(fr) != 1 {
		t.Fatal("after Reset")
	}
}

func TestPendingBound(t *testing.T) {
	a := NewViewAssembler()
	for s := uint64(1); s <= 20; s++ { // 20 frames that never get their E
		a.Feed(fmt.Sprintf("TBM:VIEW|v1|%d|B|0x2A,0,0,0", s))
	}
	if len(a.pend) > maxPendingFrames {
		t.Fatalf("%d frames held", len(a.pend))
	}
}

func TestParseGUIDField(t *testing.T) {
	for in, want := range map[string]packet.GUID{"0": 0, "0x2A": 0x2A, "0xF130000C5E0012AB": 0xF130000C5E0012AB, "42": 42} {
		if g, err := ParseGUIDField(in); err != nil || g != want {
			t.Errorf("%q: %v %v", in, g, err)
		}
	}
	for _, in := range []string{"", "0x", "zz", "-1", "0xFFFFFFFFFFFFFFFFF"} {
		if _, err := ParseGUIDField(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// Any line, in any order, gives a frame or nothing: never a panic or a frame that disagrees with its own counts.
func FuzzViewAssembler(f *testing.F) {
	for _, l := range docExample {
		f.Add(l, "")
	}
	f.Add("TBM:VIEW|v1|1|B|0x2A,1,1,0", "TBM:VIEW|v1|1|E|2")
	f.Add("TBM:VIEW|v1|99999999999999999999|B|1", "TBM:VIEW|v1|1|P|,,,;;;,")
	f.Add("TBM:VIEW|v1|1|T|0x1,0x2:5;0x3:7", "TBM:VIEW|v1|1|H|;;;")
	f.Fuzz(func(t *testing.T, a, b string) {
		asm := NewViewAssembler()
		for _, l := range []string{a, b, a, b} {
			if fr, ok := asm.Feed(l); ok && fr == nil {
				t.Fatal("ok with no frame")
			}
		}
		_, _ = ParsePartyRec(a)
		_, _ = ParseHostileRec(a)
		_, _ = ParseThreatRec(a)
		_, _ = ParseViewLine(a)
	})
}

func TestBeginTruncatedField(t *testing.T) {
	a := NewViewAssembler()
	// TortoiseBots #901: B may carry a fifth field, truncated 0 or 1; four fields still parse.
	for i, tc := range []struct {
		b    string
		want bool
		ok   bool
	}{{"0x2A,0,0,0,1", true, true}, {"0x2A,0,0,0,0", false, true}, {"0x2A,0,0,0", false, true}, {"0x2A,0,0,0,2", false, true}, {"0x2A,0,0,0,1,9,x", true, true}, {"0x2A,0,0", false, false}} {
		seq := i + 1
		fr := feedAll(a, []string{fmt.Sprintf("TBM:VIEW|v1|%d|B|%s", seq, tc.b), fmt.Sprintf("TBM:VIEW|v1|%d|E|2", seq)})
		if tc.ok != (len(fr) == 1) || (tc.ok && fr[0].Truncated != tc.want) {
			t.Errorf("%s: %+v", tc.b, fr)
		}
	}
	if a.Stats().Truncated != 2 {
		t.Fatalf("%+v", a.Stats())
	}
}

// TB #924: the party record's 19th and 20th fields are the result letter and the failure reason of the bot's last action; an older
// module sends 18 fields and the two stay empty.
func TestParsePartyRecResultAndReason(t *testing.T) {
	base := "0x2A,Priesty,4000,5000,35,100,0,-8913.2,554.5,93.1,1.50,0,0,0,0,0,n,drink"
	p, err := ParsePartyRec(base + ",F,no-item")
	if err != nil || p.Action != "drink" || p.Result != "F" || p.Reason != "no-item" || !p.DrinkFailedNoItem() {
		t.Fatalf("%v %+v", err, p)
	}
	p, err = ParsePartyRec(base + ",O,-")
	if err != nil || p.Result != "O" || p.Reason != "" || p.DrinkFailedNoItem() {
		t.Fatalf("%v %+v", err, p)
	}
	p, err = ParsePartyRec(base)
	if err != nil || p.Result != "" || p.Reason != "" {
		t.Fatalf("%v %+v", err, p)
	}
	// A failure of another action, or of a drink for another reason, is not "no drink in the bags".
	if p, _ := ParsePartyRec("0x2A,Priesty,4000,5000,35,100,0,1,2,3,0,0,0,0,0,0,n,heal,F,no-item"); p.DrinkFailedNoItem() {
		t.Fatalf("%+v", p)
	}
	if p, _ := ParsePartyRec(base + ",F,oom"); p.DrinkFailedNoItem() {
		t.Fatalf("%+v", p)
	}
}
