package trace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// File is a trace read back.
type File struct {
	Path    string
	Header  Header
	Party   []Member
	Mobs    map[string]MobInfo
	Frames  []Frame
	Events  []Event
	End     *End
	Oracles []Check
	Skipped int // lines that were not valid records (a line cut short by a crash, say)
}

// ErrNoHeader is returned for a file with no header record.
var ErrNoHeader = errors.New("trace: no header record")

// Read parses a trace file. Lines it cannot parse are counted in Skipped; a file of a newer schema version is an error.
func Read(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, b)
}

// Parse is Read over bytes.
func Parse(path string, b []byte) (*File, error) {
	f := &File{Path: path, Mobs: map[string]MobInfo{}}
	haveHeader := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var probe Envelope
		if err := json.Unmarshal([]byte(line), &probe); err != nil || probe.K == "" {
			f.Skipped++
			continue
		}
		if probe.V > Version {
			return nil, fmt.Errorf("trace: %s is schema version %d; this reader knows %d", path, probe.V, Version)
		}
		var err error
		switch probe.K {
		case KindHeader:
			if err = json.Unmarshal([]byte(line), &f.Header); err == nil {
				haveHeader = true
			}
		case KindParty:
			var p PartyRecord
			if err = json.Unmarshal([]byte(line), &p); err == nil {
				f.Party = append(f.Party, p.Member)
			}
		case KindMobs:
			var m MobRecord
			if err = json.Unmarshal([]byte(line), &m); err == nil {
				f.Mobs[m.G] = m.MobInfo
			}
		case KindFrame:
			var fr Frame
			if err = json.Unmarshal([]byte(line), &fr); err == nil {
				f.Frames = append(f.Frames, fr)
			}
		case KindEvent:
			var ev Event
			if err = json.Unmarshal([]byte(line), &ev); err == nil {
				f.Events = append(f.Events, ev)
			}
		case KindEnd:
			var e End
			if err = json.Unmarshal([]byte(line), &e); err == nil {
				f.End = &e
			}
		case KindOracle:
			var o Oracle
			if err = json.Unmarshal([]byte(line), &o); err == nil {
				f.Oracles = append(f.Oracles, o.Check)
			}
		}
		if err != nil {
			f.Skipped++
		}
	}
	if !haveHeader {
		return nil, ErrNoHeader
	}
	return f, nil
}

// Seconds is the length of the run: the end record's, else the last record's time.
func (f *File) Seconds() float64 {
	if f.End != nil && f.End.Seconds > 0 {
		return f.End.Seconds
	}
	var last int64
	if n := len(f.Frames); n > 0 {
		last = f.Frames[n-1].T
	}
	if n := len(f.Events); n > 0 && f.Events[n-1].T > last {
		last = f.Events[n-1].T
	}
	return float64(last) / 1000
}

// Name is the name of the party member with guid g (the guid itself when unknown).
func (f *File) Name(g string) string {
	for _, m := range f.Party {
		if m.G == g {
			return m.Name
		}
	}
	return g
}

// Member finds a party member by name (case-insensitive).
func (f *File) Member(name string) (Member, bool) {
	for _, m := range f.Party {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return Member{}, false
}
