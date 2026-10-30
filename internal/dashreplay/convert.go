package dashreplay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// The module's snapshot cadence; convert ticks and the daemon's
	// commanders staleness both assume it.
	tickMS = 2000
	// replayV is the module protocol version convert synthesizes.
	replayV = 12
)

type snapshot struct {
	at         int64
	commanders json.RawMessage
	bots       int
}

type fightDump struct {
	id    string
	start int64
	durMS int64
	fight json.RawMessage
}

type fightSummary struct {
	ID      string `json:"id"`
	Session uint64 `json:"session"`
	Start   int64  `json:"start"`
	Dur     int64  `json:"dur"`
}

// ConvertDumps turns the dashboard API dumps of one run directory
// (commanders-*.json snapshots and combat-fight-*.json fights, optionally
// combat-fights.json for the session and fight order) into a replay timeline:
// a COMMANDERS and a synthetic HEARTBEAT every 2 s, and each fight once when
// the module would have sent it.
func ConvertDumps(dir string) (Header, []Line, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Header{}, nil, err
	}
	var snapshots []snapshot
	var fights []fightDump
	var summaries []fightSummary
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name)
		switch {
		case strings.HasPrefix(name, "commanders-") && strings.HasSuffix(name, ".json"):
			s, err := readSnapshot(path)
			if err != nil {
				return Header{}, nil, fmt.Errorf("%s: %v", name, err)
			}
			snapshots = append(snapshots, s)
		case strings.HasPrefix(name, "combat-fight-") && strings.HasSuffix(name, ".json"):
			f, err := readFight(path)
			if err != nil {
				return Header{}, nil, fmt.Errorf("%s: %v", name, err)
			}
			fights = append(fights, f)
		case name == "combat-fights.json":
			rows, err := readFightSummaries(path)
			if err != nil {
				return Header{}, nil, fmt.Errorf("%s: %v", name, err)
			}
			summaries = rows
		}
	}
	if len(snapshots) == 0 && len(fights) == 0 {
		return Header{}, nil, fmt.Errorf("no API dumps in %s", dir)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].at < snapshots[j].at })

	var session uint64
	var order map[string]int
	if len(summaries) > 0 {
		session = summaries[0].Session
		order = make(map[string]int, len(summaries))
		for i, row := range summaries {
			order[row.ID] = i
		}
	} else if len(snapshots) > 0 {
		session = uint64(snapshots[0].at)
	}

	seen := make(map[string]bool, len(fights))
	unique := fights[:0]
	for _, f := range fights {
		if seen[f.id] {
			continue
		}
		seen[f.id] = true
		unique = append(unique, f)
	}
	fights = unique
	sort.SliceStable(fights, func(i, j int) bool {
		oi, iok := order[fights[i].id]
		oj, jok := order[fights[j].id]
		if iok && jok {
			return oi < oj
		}
		if iok != jok {
			return iok
		}
		return fights[i].start < fights[j].start
	})

	first := int64(0)
	haveFirst := false
	last := int64(0)
	note := func(at int64) {
		if !haveFirst || at < first {
			first, haveFirst = at, true
		}
		if at > last {
			last = at
		}
	}
	for _, s := range snapshots {
		note(s.at)
	}
	for _, f := range fights {
		note(fightSentAt(f))
	}

	fightsAt := make(map[int64][]fightDump, len(fights))
	for _, f := range fights {
		tick := roundUp((fightSentAt(f)-first)*1000, tickMS)
		fightsAt[tick] = append(fightsAt[tick], f)
	}
	lastMS := (last - first) * 1000
	lastTick := roundUp(lastMS, tickMS)

	var lines []Line
	seq := uint64(0)
	add := func(t int64, dg string) error {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(dg)); err != nil {
			return err
		}
		dg = compact.String()
		if len(dg) > MaxDatagram {
			return fmt.Errorf("datagram at t_ms %d is %d bytes, over the %d-byte limit", t, len(dg), MaxDatagram)
		}
		lines = append(lines, Line{TMS: t, DG: json.RawMessage(dg)})
		return nil
	}
	for tick := int64(0); tick <= lastTick; tick += tickMS {
		ts := first + tick/1000
		held, ok := latestSnapshot(snapshots, ts)
		if ok {
			seq++
			dg := fmt.Sprintf(`{"v":%d,"session":%d,"seq":%d,"ts":%d,"type":"COMMANDERS","commanders":%s}`,
				replayV, session, seq, ts, held.commanders)
			if err := add(tick, dg); err != nil {
				return Header{}, nil, err
			}
		}
		seq++
		bots := 0
		if ok {
			bots = held.bots
		}
		dg := fmt.Sprintf(`{"v":%d,"session":%d,"seq":%d,"ts":%d,"type":"HEARTBEAT","uptime":%d,"diff":50,"humans":1,"bots":%d,"states":{}}`,
			replayV, session, seq, ts, tick/1000, bots)
		if err := add(tick, dg); err != nil {
			return Header{}, nil, err
		}
		for _, f := range fightsAt[tick] {
			seq++
			dg := fmt.Sprintf(`{"v":%d,"session":%d,"seq":%d,"ts":%d,"type":"COMBAT_FIGHT","fight":%s}`,
				replayV, session, seq, ts, f.fight)
			if err := add(tick, dg); err != nil {
				return Header{}, nil, err
			}
		}
	}

	h := Header{
		Format:    FormatName,
		Version:   FormatVersion,
		Source:    "convert",
		Created:   time.Now().UTC().Format(time.RFC3339),
		ProtocolV: replayV,
		From:      "api dumps " + filepath.Base(dir),
		Synthetic: []string{"HEARTBEAT"},
	}
	if err := fillHeader(&h, lines); err != nil {
		return Header{}, nil, err
	}
	return h, lines, nil
}

func readSnapshot(path string) (snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return snapshot{}, err
	}
	var view struct {
		Commanders json.RawMessage `json:"commanders"`
		UpdatedAt  int64           `json:"updated_at"`
	}
	if err := json.Unmarshal(b, &view); err != nil {
		return snapshot{}, err
	}
	if len(view.Commanders) == 0 || string(view.Commanders) == "null" {
		return snapshot{}, errors.New("no commanders")
	}
	if view.UpdatedAt <= 0 {
		return snapshot{}, errors.New("no updated_at")
	}
	var bindings []struct {
		Bots []json.RawMessage `json:"bots"`
	}
	if err := json.Unmarshal(view.Commanders, &bindings); err != nil {
		return snapshot{}, fmt.Errorf("commanders: %v", err)
	}
	bots := 0
	for _, b := range bindings {
		bots += len(b.Bots)
	}
	return snapshot{at: view.UpdatedAt, commanders: view.Commanders, bots: bots}, nil
}

func readFight(path string) (fightDump, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return fightDump{}, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return fightDump{}, err
	}
	raw := obj["fight"]
	if len(raw) == 0 {
		if _, ok := obj["id"]; !ok {
			return fightDump{}, errors.New("no fight")
		}
		raw = json.RawMessage(b)
	}
	var meta struct {
		ID    string `json:"id"`
		Start int64  `json:"start"`
		DurMS int64  `json:"dur_ms"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fightDump{}, fmt.Errorf("fight: %v", err)
	}
	return fightDump{id: meta.ID, start: meta.Start, durMS: meta.DurMS, fight: raw}, nil
}

func readFightSummaries(path string) ([]fightSummary, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []fightSummary
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// fightSentAt is the second the module would have sent the fight: its end,
// rounded up to whole seconds.
func fightSentAt(f fightDump) int64 {
	return f.start + (f.durMS+999)/1000
}

func roundUp(n, step int64) int64 {
	if n <= 0 {
		return 0
	}
	return (n + step - 1) / step * step
}

func latestSnapshot(snapshots []snapshot, ts int64) (snapshot, bool) {
	i := sort.Search(len(snapshots), func(i int) bool { return snapshots[i].at > ts })
	if i == 0 {
		return snapshot{}, false
	}
	return snapshots[i-1], true
}
