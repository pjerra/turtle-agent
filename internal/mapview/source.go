package mapview

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pjerra/turtle-agent/internal/capture"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// SessionFrame is the frame a session shows now: its own position (leader, when given, overrides it with where
// the agent last walked to, which a replay's session does not follow), the party members in view, the hostile
// creatures and the game objects. ok is false before the character is in the world.
func SessionFrame(s *session.Session, t time.Time, leader *Pos) (f Frame, ok bool) {
	st := s.Status()
	if st.State != session.StatusInWorld {
		return Frame{}, false
	}
	f = Frame{T: t, Map: st.Map}
	self := Pos{st.X, st.Y, st.Z}
	if leader != nil {
		self = *leader
	}
	f.Members = append(f.Members, MemberFrame{Name: st.Character, GUID: uint64(st.GUID), Pos: self, Self: true})
	for _, u := range s.Units(objects.Filter{Limit: 5000}) {
		if u.PosAgeMS < 0 {
			continue
		}
		switch {
		case u.Type == "player" && u.InParty:
			f.Members = append(f.Members, MemberFrame{Name: u.Name, GUID: uint64(u.GUID), Pos: Pos{u.X, u.Y, u.Z}})
		case u.Type == "creature" && u.Hostile && !u.InParty:
			f.Hostiles = append(f.Hostiles, UnitFrame{GUID: uint64(u.GUID), Entry: u.Entry, Name: u.Name, Level: u.Level,
				Health: u.Health, MaxHealth: u.MaxHealth, InCombat: u.InCombat, Dead: u.Dead, Pos: Pos{u.X, u.Y, u.Z}})
		}
	}
	for _, g := range s.GameObjects(objects.GOFilter{Limit: 5000}) {
		if g.Distance < 0 { // a transport's: the server never sent where it is
			continue
		}
		f.Objects = append(f.Objects, ObjectFrame{GUID: uint64(g.GUID), Entry: g.Entry, Name: g.Name, Type: g.TypeName, Pos: Pos{g.X, g.Y, g.Z}})
	}
	return f, true
}

// clientMoves are the movement packets the client sends: a MovementInfo with the position it is at.
var clientMoves = map[uint16]bool{
	opcode.MSGMoveStartForward: true, opcode.MSGMoveStartBackward: true, opcode.MSGMoveStop: true,
	opcode.MSGMoveStartStrafeLeft: true, opcode.MSGMoveStartStrafeRight: true, opcode.MSGMoveStopStrafe: true,
	opcode.MSGMoveJump: true, opcode.MSGMoveStartTurnLeft: true, opcode.MSGMoveStartTurnRight: true,
	opcode.MSGMoveStopTurn: true, opcode.MSGMoveSetRunMode: true, opcode.MSGMoveSetWalkMode: true,
	opcode.MSGMoveFallLand: true, opcode.MSGMoveStartSwim: true, opcode.MSGMoveStopSwim: true,
	opcode.MSGMoveSetFacing: true, opcode.MSGMoveHeartbeat: true,
}

// CaptureInfo says what a capture replay read.
type CaptureInfo struct {
	Character string
	Records   int
	Redacted  int
	First     time.Time
	Last      time.Time
	Frames    int
	// Warning is why the capture ended early (a cut-off record), empty for a clean file.
	Warning string
}

// AddCapture replays the capture at path through a session and adds a frame every cfg.FrameStep to b. It returns
// the events the replay built. The leader's route comes from the movement packets the agent sent; everything
// else from what the server told the session.
func AddCapture(b *Builder, path string, cfg Config) ([]events.Event, CaptureInfo, error) {
	recs, perr := capture.ReadFilePartial(path)
	var info CaptureInfo
	if perr != nil {
		if len(recs) == 0 {
			return nil, info, perr
		}
		info.Warning = perr.Error()
	}
	step := cfg.FrameStep
	if step <= 0 {
		step = time.Second
	}
	var leader *Pos
	var lastStatus Pos
	var lastMap uint32
	var lastFrame time.Time
	emit := func(rec capture.Record, s *session.Session) {
		if rec.Dir == worldconn.ClientToServer && clientMoves[rec.Op] {
			r := packet.NewReader(rec.Body)
			mi := game.ReadMoveInfo(r)
			if r.Err() == nil && finite(mi.X, mi.Y, mi.Z) {
				leader = &Pos{mi.X, mi.Y, mi.Z}
			}
		}
		if !lastFrame.IsZero() && rec.Time.Sub(lastFrame) < step {
			return
		}
		st := s.Status()
		if now := (Pos{st.X, st.Y, st.Z}); now != lastStatus || st.Map != lastMap {
			// The server placed the player (login, teleport, a new world, knockback): that beats the last walk,
			// also when it put the player where it was before.
			lastStatus, lastMap = now, st.Map
			leader = &Pos{now.X, now.Y, now.Z}
		}
		if f, ok := SessionFrame(s, rec.Time, leader); ok {
			b.AddFrame(f)
			info.Frames++
			lastFrame = rec.Time
		}
	}
	log, rep, err := session.ReplayWith(recs, session.Options{}, emit)
	if err != nil {
		return nil, info, err
	}
	info.Character, info.Records, info.Redacted, info.First, info.Last = rep.Character, rep.Records, rep.Redacted, rep.First, rep.Last
	var out []events.Event
	for since := uint64(0); ; {
		page := log.Read(events.Query{Since: since, Limit: events.MaxLimit})
		out = append(out, page.Events...)
		since = page.Next
		if !page.More {
			break
		}
	}
	return out, info, nil
}

func finite(v ...float32) bool {
	for _, x := range v {
		if x != x || x > 100000 || x < -100000 {
			return false
		}
	}
	return true
}

// ReadEvents reads events saved from the events method or from `capture events`: a JSON array, the page the
// events method returns, or one event per line.
func ReadEvents(path string) ([]events.Event, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseEvents(raw)
}

// ParseEvents is ReadEvents on the file's bytes.
func ParseEvents(raw []byte) ([]events.Event, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	switch raw[0] {
	case '[':
		var evs []events.Event
		if err := json.Unmarshal(raw, &evs); err != nil {
			return nil, fmt.Errorf("events: %w", err)
		}
		return evs, nil
	case '{':
		dec := json.NewDecoder(bytes.NewReader(raw))
		var out []events.Event
		for {
			var one json.RawMessage
			if err := dec.Decode(&one); err != nil {
				if errors.Is(err, io.EOF) {
					return out, nil
				}
				return nil, fmt.Errorf("events: %w", err)
			}
			var page struct {
				Events []events.Event `json:"events"`
			}
			if err := json.Unmarshal(one, &page); err != nil {
				return nil, fmt.Errorf("events: %w", err)
			}
			if page.Events != nil {
				out = append(out, page.Events...)
				continue
			}
			var e events.Event
			if err := json.Unmarshal(one, &e); err != nil {
				return nil, fmt.Errorf("events: %w", err)
			}
			out = append(out, e)
		}
	}
	return nil, errors.New("events: not a JSON array, an events page or JSON lines")
}

// AddBotwatch adds the positions of a botwatch file (one JSON object per line: t in epoch seconds, bots with
// name, map, x, y, z) as members, only those in names when it is not empty.
func AddBotwatch(b *Builder, path string, names []string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	rd := bufio.NewReaderSize(f, 1<<20)
	n := 0
	for {
		line, err := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var rec struct {
				T    float64 `json:"t"`
				Bots []struct {
					Name string  `json:"name"`
					Map  uint32  `json:"map"`
					X    float32 `json:"x"`
					Y    float32 `json:"y"`
					Z    float32 `json:"z"`
				} `json:"bots"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.T > 0 {
				at := time.Unix(0, int64(rec.T*1e9))
				for _, bot := range rec.Bots {
					if bot.Name == "" || (len(want) > 0 && !want[bot.Name]) || !finite(bot.X, bot.Y, bot.Z) {
						continue
					}
					b.AddFrame(Frame{T: at, Map: bot.Map, Members: []MemberFrame{{Name: bot.Name, Pos: Pos{bot.X, bot.Y, bot.Z}}}})
					n++
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, err
		}
	}
}

// ReadReport reads a dungeon_status report: the bare report, or the dungeon_status result (or a JSON-RPC reply)
// that holds it.
func ReadReport(path string) (*dungeon.Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Report *dungeon.Report `json:"report"`
		Result *struct {
			Report *dungeon.Report `json:"report"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}
	switch {
	case wrap.Report != nil:
		return wrap.Report, nil
	case wrap.Result != nil && wrap.Result.Report != nil:
		return wrap.Result.Report, nil
	}
	var rep dungeon.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}
	if rep.Plan == "" && rep.Started.IsZero() {
		return nil, errors.New("report: no dungeon report in this file")
	}
	return &rep, nil
}
