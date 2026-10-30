package dashreplay

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const validHeader = `{"format":"tb-dash-replay","version":1,"source":"convert","created":"2026-10-28T22:10:00Z","protocol_v":12,"from":"test","synthetic":[],"datagrams":0,"span_ms":0,"by_type":{}}`

func TestReadRefusesBadFiles(t *testing.T) {
	cases := []struct {
		name string
		data string
		line string
		want string
	}{
		{"wrong format", `{"format":"tb-dash-other","version":1}` + "\n", "line 1", "format"},
		{"version 2", `{"format":"tb-dash-replay","version":2}` + "\n", "line 1", "version"},
		{"dg array", validHeader + "\n" + `{"t_ms":0,"dg":[]}` + "\n", "line 2", "object"},
		{"dg without type", validHeader + "\n" + `{"t_ms":0,"dg":{"v":12}}` + "\n", "line 2", "type"},
		{"dg with null type", validHeader + "\n" + `{"t_ms":0,"dg":{"type":null}}` + "\n", "line 2", "type"},
		{"t_ms going back", validHeader + "\n" + `{"t_ms":5,"dg":{"type":"HEARTBEAT"}}` + "\n" + `{"t_ms":4,"dg":{"type":"HEARTBEAT"}}` + "\n", "line 3", "t_ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Read(strings.NewReader(tc.data))
			if err == nil {
				t.Fatalf("Read accepted:\n%s", tc.data)
			}
			if !strings.Contains(err.Error(), tc.line) {
				t.Errorf("error %q does not name %s", err, tc.line)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	lines := []Line{
		{TMS: 0, DG: json.RawMessage(`{"v":12,"session":7,"seq":1,"ts":100,"type":"COMMANDERS","commanders":[]}`)},
		{TMS: 100, DG: json.RawMessage(`{"v":12,"session":7,"seq":2,"ts":100,"type":"HEARTBEAT","diff":50}`)},
		{TMS: 900, DG: json.RawMessage(`{"v":12,"session":7,"seq":3,"ts":101,"type":"COMBAT_FIGHT","fight":{"id":"1-1"}}`)},
	}
	var buf bytes.Buffer
	if err := Write(&buf, Header{Format: FormatName, Version: FormatVersion, Source: "convert", Created: "2026-10-28T22:10:00Z", ProtocolV: 12, From: "test"}, lines); err != nil {
		t.Fatalf("Write: %v", err)
	}
	gotHeader, gotLines, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if gotHeader.Datagrams != 3 {
		t.Errorf("datagrams = %d, want 3", gotHeader.Datagrams)
	}
	if gotHeader.SpanMS != 900 {
		t.Errorf("span_ms = %d, want 900", gotHeader.SpanMS)
	}
	wantByType := map[string]int{"COMMANDERS": 1, "HEARTBEAT": 1, "COMBAT_FIGHT": 1}
	if !reflect.DeepEqual(gotHeader.ByType, wantByType) {
		t.Errorf("by_type = %v, want %v", gotHeader.ByType, wantByType)
	}
	if len(gotLines) != len(lines) {
		t.Fatalf("lines = %d, want %d", len(gotLines), len(lines))
	}
	for i := range lines {
		if gotLines[i].TMS != lines[i].TMS || !bytes.Equal(gotLines[i].DG, lines[i].DG) {
			t.Errorf("line %d = %d %s, want %d %s", i+2, gotLines[i].TMS, gotLines[i].DG, lines[i].TMS, lines[i].DG)
		}
	}
}
