// Package dashreplay reads, writes and converts TortoiseBots observability
// dashboard replay files: a header line and one JSON datagram per line, as
// the dashboard's UDP port would have received them. See README.md,
// "Dashboard replay".
package dashreplay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	FormatName    = "tb-dash-replay"
	FormatVersion = 1
	// MaxDatagram is the largest UDP payload (IPv4); a bigger datagram
	// cannot be replayed.
	MaxDatagram = 65507
)

// Header is line 1 of a replay file.
type Header struct {
	Format    string         `json:"format"`
	Version   int            `json:"version"`
	Source    string         `json:"source"`
	Created   string         `json:"created"`
	ProtocolV int            `json:"protocol_v"`
	From      string         `json:"from"`
	Synthetic []string       `json:"synthetic"`
	Datagrams int            `json:"datagrams"`
	SpanMS    int64          `json:"span_ms"`
	ByType    map[string]int `json:"by_type"`
}

// Line is one datagram of a replay file.
type Line struct {
	TMS int64           `json:"t_ms"`
	DG  json.RawMessage `json:"dg"`
}

// Read parses a replay file. It refuses a header that is not this format and
// version, a datagram that is not a JSON object with a string "type", and a
// t_ms that goes back; each error names the line.
func Read(r io.Reader) (Header, []Line, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return Header{}, nil, err
		}
		return Header{}, nil, errors.New("empty replay file")
	}
	var h Header
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
		return Header{}, nil, fmt.Errorf("line 1: header: %v", err)
	}
	if h.Format != FormatName {
		return Header{}, nil, fmt.Errorf("line 1: format %q, want %q", h.Format, FormatName)
	}
	if h.Version != FormatVersion {
		return Header{}, nil, fmt.Errorf("line 1: version %d, want %d", h.Version, FormatVersion)
	}
	var lines []Line
	lineNumber := 1
	for sc.Scan() {
		lineNumber++
		var l Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return Header{}, nil, fmt.Errorf("line %d: %v", lineNumber, err)
		}
		if _, err := datagramType(l.DG); err != nil {
			return Header{}, nil, fmt.Errorf("line %d: %v", lineNumber, err)
		}
		if len(lines) > 0 && l.TMS < lines[len(lines)-1].TMS {
			return Header{}, nil, fmt.Errorf("line %d: t_ms %d goes back from %d", lineNumber, l.TMS, lines[len(lines)-1].TMS)
		}
		lines = append(lines, l)
	}
	if err := sc.Err(); err != nil {
		return Header{}, nil, err
	}
	return h, lines, nil
}

// Write writes a replay file, filling Datagrams, SpanMS and ByType from the
// lines.
func Write(w io.Writer, h Header, lines []Line) error {
	if err := fillHeader(&h, lines); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(h); err != nil {
		return err
	}
	for i, l := range lines {
		if err := enc.Encode(l); err != nil {
			return fmt.Errorf("line %d: %v", i+2, err)
		}
	}
	return nil
}

func fillHeader(h *Header, lines []Line) error {
	h.Datagrams = len(lines)
	h.ByType = make(map[string]int, 4)
	h.SpanMS = 0
	if len(lines) > 0 {
		h.SpanMS = lines[len(lines)-1].TMS - lines[0].TMS
	}
	for i, l := range lines {
		typ, err := datagramType(l.DG)
		if err != nil {
			return fmt.Errorf("line %d: %v", i+2, err)
		}
		h.ByType[typ]++
	}
	return nil
}

func datagramType(dg json.RawMessage) (string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(dg, &obj); err != nil {
		return "", fmt.Errorf("dg is not a JSON object: %v", err)
	}
	if obj == nil {
		return "", errors.New("dg is not a JSON object")
	}
	raw, ok := obj["type"]
	if !ok {
		return "", errors.New(`dg has no "type"`)
	}
	var typ string
	if len(raw) == 0 || raw[0] != '"' {
		return "", errors.New(`dg "type" is not a string`)
	}
	if err := json.Unmarshal(raw, &typ); err != nil {
		return "", errors.New(`dg "type" is not a string`)
	}
	return typ, nil
}
