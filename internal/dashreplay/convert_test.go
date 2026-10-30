package dashreplay

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func readObject(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return obj
}

func decodeAny(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

func remarshal(t *testing.T, b []byte) []byte {
	t.Helper()
	out, err := json.Marshal(decodeAny(t, b))
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return out
}

func lineObject(t *testing.T, l Line) map[string]json.RawMessage {
	t.Helper()
	if !json.Valid(l.DG) {
		t.Fatalf("t_ms %d: dg is not JSON: %s", l.TMS, l.DG)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(l.DG, &obj); err != nil {
		t.Fatalf("t_ms %d: dg is not an object: %v", l.TMS, err)
	}
	return obj
}

func dgType(t *testing.T, l Line) string {
	t.Helper()
	var typ string
	if err := json.Unmarshal(lineObject(t, l)["type"], &typ); err != nil {
		t.Fatalf("t_ms %d: type is not a string: %v", l.TMS, err)
	}
	return typ
}

func TestConvertWalk2(t *testing.T) {
	const dir = "testdata/walk2"
	pre := readObject(t, dir+"/commanders-pre.json")
	after := readObject(t, dir+"/commanders-after-stop.json")
	dump := readObject(t, dir+"/combat-fight-5-4.json")
	fight := dump["fight"].(map[string]any)

	preAt := int64(pre["updated_at"].(float64))
	afterAt := int64(after["updated_at"].(float64))
	start := int64(fight["start"].(float64))
	dur := int64(fight["dur_ms"].(float64))

	header, lines, err := ConvertDumps(dir)
	if err != nil {
		t.Fatalf("ConvertDumps: %v", err)
	}
	if header.Source != "convert" {
		t.Errorf("source = %q, want convert", header.Source)
	}
	if len(header.Synthetic) != 1 || header.Synthetic[0] != "HEARTBEAT" {
		t.Errorf("synthetic = %v, want [HEARTBEAT]", header.Synthetic)
	}

	if len(lines) == 0 {
		t.Fatal("no lines")
	}
	if lines[0].TMS != 0 || dgType(t, lines[0]) != "COMMANDERS" {
		t.Fatalf("first line = t_ms %d %s, want a COMMANDERS at t_ms 0", lines[0].TMS, dgType(t, lines[0]))
	}
	var firstCommanders any
	if err := json.Unmarshal(lineObject(t, lines[0])["commanders"], &firstCommanders); err != nil {
		t.Fatalf("first commanders: %v", err)
	}
	if !reflect.DeepEqual(firstCommanders, pre["commanders"]) {
		t.Errorf("first COMMANDERS = %v, want the pre snapshot's %v", firstCommanders, pre["commanders"])
	}

	// Ticks are 2000 ms apart and every tick carries one HEARTBEAT and one COMMANDERS.
	type tickCounts struct{ heartbeat, commanders, fights int }
	ticks := map[int64]*tickCounts{}
	var tickOrder []int64
	fights := 0
	for _, l := range lines {
		if l.TMS%2000 != 0 {
			t.Errorf("t_ms %d is not on the 2000 ms tick grid", l.TMS)
		}
		if _, ok := ticks[l.TMS]; !ok {
			ticks[l.TMS] = &tickCounts{}
			tickOrder = append(tickOrder, l.TMS)
		}
		switch dgType(t, l) {
		case "HEARTBEAT":
			ticks[l.TMS].heartbeat++
		case "COMMANDERS":
			ticks[l.TMS].commanders++
		case "COMBAT_FIGHT":
			ticks[l.TMS].fights++
			fights++
		}
	}
	relS := start - preAt + (dur+999)/1000
	fightSendMS := relS * 1000
	lastEventMS := (afterAt - preAt) * 1000
	if fightSendMS > lastEventMS {
		lastEventMS = fightSendMS
	}
	lastMS := (lastEventMS + 1999) / 2000 * 2000
	for i, ms := range tickOrder {
		if want := int64(i) * 2000; ms != want {
			t.Fatalf("tick %d at t_ms %d, want %d", i, ms, want)
		}
		if c := ticks[ms]; c.heartbeat != 1 || c.commanders != 1 {
			t.Errorf("tick %d: %d HEARTBEAT, %d COMMANDERS, want one of each", ms, c.heartbeat, c.commanders)
		}
	}
	if tickOrder[len(tickOrder)-1] != lastMS {
		t.Errorf("last tick %d, want %d", tickOrder[len(tickOrder)-1], lastMS)
	}

	// The tick at the after-stop time carries the after-stop snapshot.
	var stopped any
	for _, l := range lines {
		if l.TMS == lastMS && dgType(t, l) == "COMMANDERS" {
			var got any
			if err := json.Unmarshal(lineObject(t, l)["commanders"], &got); err != nil {
				t.Fatalf("commanders at %d: %v", lastMS, err)
			}
			stopped = got
		}
	}
	if !reflect.DeepEqual(stopped, after["commanders"]) {
		t.Errorf("COMMANDERS at t_ms %d = %v, want the after-stop snapshot's %v", lastMS, stopped, after["commanders"])
	}

	// Exactly one fight, sent at the tick at or after start + ceil(dur/1000).
	if fights != 1 {
		t.Fatalf("%d COMBAT_FIGHT lines, want 1", fights)
	}
	relS = start - preAt + (dur+999)/1000
	wantFightMS := (relS + 1) / 2 * 2 * 1000
	var gotFight json.RawMessage
	for _, l := range lines {
		if dgType(t, l) == "COMBAT_FIGHT" {
			if l.TMS != wantFightMS {
				t.Errorf("COMBAT_FIGHT at t_ms %d, want %d", l.TMS, wantFightMS)
			}
			gotFight = lineObject(t, l)["fight"]
		}
	}
	var fixtureDump map[string]json.RawMessage
	b, err := os.ReadFile(dir + "/combat-fight-5-4.json")
	if err != nil {
		t.Fatalf("read fight: %v", err)
	}
	if err := json.Unmarshal(b, &fixtureDump); err != nil {
		t.Fatalf("decode fight: %v", err)
	}
	if !bytes.Equal(remarshal(t, gotFight), remarshal(t, fixtureDump["fight"])) {
		t.Errorf("COMBAT_FIGHT fight = %s, want %s", remarshal(t, gotFight), remarshal(t, fixtureDump["fight"]))
	}
}

// The daemon's own types, mirrored with only the fields a replay needs
// (turtle-agent cannot import tools/observability).
type mirrorCommandersPayload struct {
	V          int               `json:"v"`
	Session    uint64            `json:"session"`
	Seq        uint64            `json:"seq"`
	TS         int64             `json:"ts"`
	Type       string            `json:"type"`
	Commanders []json.RawMessage `json:"commanders"`
}

type mirrorFightDatagram struct {
	V       int             `json:"v"`
	Session uint64          `json:"session"`
	Seq     uint64          `json:"seq"`
	TS      int64           `json:"ts"`
	Type    string          `json:"type"`
	Fight   json.RawMessage `json:"fight"`
}

func TestConvertDecodesAsDaemonTypes(t *testing.T) {
	_, lines, err := ConvertDumps("testdata/walk2")
	if err != nil {
		t.Fatalf("ConvertDumps: %v", err)
	}
	var commanders, fights int
	for i, l := range lines {
		switch dgType(t, l) {
		case "COMMANDERS":
			var p mirrorCommandersPayload
			if err := json.Unmarshal(l.DG, &p); err != nil {
				t.Fatalf("line %d: %v", i+2, err)
			}
			if p.V != 12 || p.Type != "COMMANDERS" || p.Session == 0 || p.Seq == 0 || p.TS == 0 {
				t.Errorf("line %d: got v=%d type=%q session=%d seq=%d ts=%d", i+2, p.V, p.Type, p.Session, p.Seq, p.TS)
			}
			if len(p.Commanders) == 0 {
				t.Errorf("line %d: no commanders", i+2)
			}
			commanders++
		case "COMBAT_FIGHT":
			var p mirrorFightDatagram
			if err := json.Unmarshal(l.DG, &p); err != nil {
				t.Fatalf("line %d: %v", i+2, err)
			}
			if p.V != 12 || p.Type != "COMBAT_FIGHT" || p.Session == 0 || p.Seq == 0 || p.TS == 0 || len(p.Fight) == 0 {
				t.Errorf("line %d: got v=%d type=%q session=%d seq=%d ts=%d fight=%s", i+2, p.V, p.Type, p.Session, p.Seq, p.TS, p.Fight)
			}
			fights++
		}
	}
	if commanders == 0 || fights == 0 {
		t.Fatalf("decoded %d COMMANDERS and %d COMBAT_FIGHT, want at least one of each", commanders, fights)
	}
}
