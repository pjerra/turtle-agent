package dashreplay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultTarget is the address the TortoiseBots observability daemon listens
// on by default.
const DefaultTarget = "127.0.0.1:9195"

// progressEvery is the played time one progress line in Play covers.
const progressEvery = 10 * time.Second

// defaultMaxGap caps the pause a recording can put between two datagrams: the
// daemon calls a commanders list stale after 12 s, so a longer pause in the
// recording would show as a gap the live module never has.
const defaultMaxGap = 10 * time.Second

// Options is one replay.
type Options struct {
	// To is the daemon's UDP address (default DefaultTarget).
	To string
	// Speed plays the recording this many times faster than recorded; 0 or
	// less means 1.
	Speed float64
	// MaxGap is the longest pause between two datagrams (default 10 s).
	MaxGap time.Duration
	// Loop starts over after the last line, with a new session, until ctx
	// ends.
	Loop bool
	// IKnow allows a non-loopback target (CheckTarget's refusal is skipped
	// and a warning returned instead).
	IKnow bool
	// DryRun counts what one pass would send without opening a socket and
	// without sleeping. It ignores Loop.
	DryRun bool
	// Now is the clock (default time.Now): it sets every datagram's ts and
	// each loop's session.
	Now func() time.Time
	// Sleep waits between two datagrams (default: a timer that ctx can cut
	// short).
	Sleep func(context.Context, time.Duration) error
}

// Stats counts a replay.
type Stats struct {
	Sent    int            // datagrams the socket took
	Failed  int            // writes the socket refused
	Skipped int            // datagrams too big to send
	Loops   int            // passes over the file
	ByType  map[string]int // sent datagrams per type
}

// TargetRefusedError is a replay target CheckTarget will not send to: not a
// loopback address (without --i-know), or no usable host and port. The
// command turns it into exit code 17.
type TargetRefusedError struct {
	To     string
	Reason string
}

func (e *TargetRefusedError) Error() string {
	return "refusing to replay to " + e.To + ": " + e.Reason
}

// CheckTarget vets the address a replay would send to. It returns a warning
// (non-empty only for a non-loopback target that --i-know allowed) and an
// error: a *TargetRefusedError for anything but a loopback address, a name
// that resolves to a non-loopback address, or a port outside 1-65535.
func CheckTarget(to string, iKnow bool) (warn string, err error) {
	if to == "" {
		to = DefaultTarget
	}
	addr, err := net.ResolveUDPAddr("udp", to)
	if err != nil {
		return "", &TargetRefusedError{To: to, Reason: err.Error()}
	}
	if addr.Port <= 0 || addr.Port > 65535 {
		return "", &TargetRefusedError{To: to, Reason: "port must be 1-65535"}
	}
	host, _, err := net.SplitHostPort(to)
	if err != nil {
		return "", &TargetRefusedError{To: to, Reason: err.Error()}
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		// A name: every address it resolves to has to be loopback, or the
		// replay may leave the machine after all.
		addrs, err := net.LookupHost(host)
		if err != nil {
			return "", &TargetRefusedError{To: to, Reason: err.Error()}
		}
		for _, a := range addrs {
			ip := net.ParseIP(a)
			if ip == nil {
				return "", &TargetRefusedError{To: to, Reason: "unreadable address " + a}
			}
			ips = append(ips, ip)
		}
		if len(ips) == 0 {
			return "", &TargetRefusedError{To: to, Reason: "the name resolves to no address"}
		}
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			if !iKnow {
				return "", &TargetRefusedError{To: to, Reason: "not a loopback address; pass --i-know if this daemon is a test dashboard"}
			}
			return "WARNING: replaying to non-loopback " + to, nil
		}
	}
	return "", nil
}

// Rewrite sets the fields a replay overwrites on one datagram (session, seq,
// ts and replay: true) and leaves every other key as recorded. Key order is
// not preserved.
func Rewrite(dg json.RawMessage, session, seq uint64, ts int64) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(dg, &obj); err != nil {
		return nil, fmt.Errorf("datagram is not a JSON object: %v", err)
	}
	if obj == nil {
		return nil, errors.New("datagram is not a JSON object")
	}
	obj["session"] = json.RawMessage(strconv.FormatUint(session, 10))
	obj["seq"] = json.RawMessage(strconv.FormatUint(seq, 10))
	obj["ts"] = json.RawMessage(strconv.FormatInt(ts, 10))
	obj["replay"] = json.RawMessage("true")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Plan says how long one pass over lines plays at speed (gaps capped at
// maxGap) and how many datagrams of each type it sends.
func Plan(lines []Line, speed float64, maxGap time.Duration) (time.Duration, map[string]int) {
	var total time.Duration
	for _, gap := range planGaps(lines, speed, maxGap) {
		total += gap
	}
	byType := make(map[string]int, 4)
	for _, l := range lines {
		typ, err := datagramType(l.DG)
		if err != nil {
			continue
		}
		if len(l.DG) > MaxDatagram {
			continue
		}
		byType[typ]++
	}
	return total, byType
}

// TypeSummary renders a ByType map as "COMMANDERS 4, HEARTBEAT 4", types in
// alphabetical order.
func TypeSummary(byType map[string]int) string {
	types := make([]string, 0, len(byType))
	for typ := range byType {
		types = append(types, typ)
	}
	sort.Strings(types)
	parts := make([]string, 0, len(types))
	for _, typ := range types {
		parts = append(parts, fmt.Sprintf("%s %d", typ, byType[typ]))
	}
	return strings.Join(parts, ", ")
}

// Play sends the datagrams of a replay to opts.To over one UDP socket, at the
// recorded pace divided by opts.Speed, and prints a progress line every 10 s
// of played time. A datagram the socket refuses is counted, not fatal. Each
// loop gets a newer session than the one before, so the daemon does not
// dedupe a second pass away.
func Play(ctx context.Context, lines []Line, opts Options, out io.Writer) (Stats, error) {
	stats := Stats{ByType: make(map[string]int, 4)}
	if out == nil {
		out = io.Discard
	}
	speed, maxGap, now, sleep := playDefaults(opts)
	if len(lines) == 0 {
		return stats, nil
	}
	gaps := planGaps(lines, speed, maxGap)
	minSeq := minimalSeq(lines)

	var conn *net.UDPConn
	if !opts.DryRun {
		to := opts.To
		if to == "" {
			to = DefaultTarget
		}
		addr, err := net.ResolveUDPAddr("udp", to)
		if err != nil {
			return stats, err
		}
		conn, err = net.DialUDP("udp", nil, addr)
		if err != nil {
			return stats, fmt.Errorf("replay to %s: %v", to, err)
		}
		defer conn.Close()
	}

	type queued struct {
		typ  string
		seq  uint64
		data json.RawMessage
	}
	queue := make([]queued, len(lines))
	for i, l := range lines {
		typ, err := datagramType(l.DG)
		if err != nil {
			return stats, fmt.Errorf("t_ms %d: %v", l.TMS, err)
		}
		queue[i] = queued{typ: typ, seq: newSeq(l.DG, minSeq), data: l.DG}
	}

	var session uint64
	played := time.Duration(0)
	reportAt := progressEvery
	for {
		session = nextSession(now(), session)
		stats.Loops++
		for i, q := range queue {
			if i > 0 {
				gap := gaps[i]
				if gap > 0 {
					if err := sleep(ctx, gap); err != nil {
						return stats, err
					}
					played += gap
					if played >= reportAt {
						// One line per wait that passed a whole 10 s of
						// played time, not one per boundary crossed: a wait
						// over several boundaries prints once.
						fmt.Fprintf(out, "sent %d (%s)\n", stats.Sent, TypeSummary(stats.ByType))
						for played >= reportAt {
							reportAt += progressEvery
						}
					}
				}
			}
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			if opts.DryRun {
				stats.Sent++
				stats.ByType[q.typ]++
				continue
			}
			b, err := Rewrite(q.data, session, q.seq, now().Unix())
			if err != nil {
				stats.Skipped++
				continue
			}
			if len(b) > MaxDatagram {
				stats.Skipped++
				continue
			}
			if _, err := conn.Write(b); err != nil {
				stats.Failed++
				continue
			}
			stats.Sent++
			stats.ByType[q.typ]++
		}
		if opts.DryRun || !opts.Loop {
			return stats, nil
		}
	}
}

func playDefaults(opts Options) (speed float64, maxGap time.Duration, now func() time.Time,
	sleep func(context.Context, time.Duration) error) {
	speed = opts.Speed
	if speed <= 0 {
		speed = 1
	}
	maxGap = opts.MaxGap
	if maxGap <= 0 {
		maxGap = defaultMaxGap
	}
	now = opts.Now
	if now == nil {
		now = time.Now
	}
	sleep = opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	return speed, maxGap, now, sleep
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// planGaps is the pause before each line: the recorded gap divided by the
// speed, capped at maxGap. The first line has none.
func planGaps(lines []Line, speed float64, maxGap time.Duration) []time.Duration {
	gaps := make([]time.Duration, len(lines))
	if speed <= 0 {
		speed = 1
	}
	if maxGap <= 0 {
		maxGap = defaultMaxGap
	}
	for i := 1; i < len(lines); i++ {
		delta := time.Duration(lines[i].TMS-lines[i-1].TMS) * time.Millisecond
		gap := time.Duration(float64(delta) / speed)
		if gap > maxGap {
			gap = maxGap
		}
		if gap < 0 {
			gap = 0
		}
		gaps[i] = gap
	}
	return gaps
}

// nextSession is the session a loop sends with: the clock's seconds, but
// always newer than the loop before (a repeated session is deduped away by
// the daemon).
func nextSession(now time.Time, previous uint64) uint64 {
	s := uint64(now.Unix())
	if s <= previous {
		return previous + 1
	}
	return s
}

// minimalSeq is the smallest recorded seq in the file.
func minimalSeq(lines []Line) uint64 {
	var min uint64
	have := false
	for _, l := range lines {
		n, ok := recordedSeq(l.DG)
		if !ok {
			continue
		}
		if !have || n < min {
			min, have = n, true
		}
	}
	return min
}

// newSeq maps a recorded seq to the replay's: the smallest becomes 1 and
// every datagram keeps its place in the recorded order, so a BOT_BATCH cycle
// still assembles. A datagram without a seq counts as the smallest.
func newSeq(dg json.RawMessage, min uint64) uint64 {
	n, ok := recordedSeq(dg)
	if !ok {
		n = min
	}
	return n - min + 1
}

func recordedSeq(dg json.RawMessage) (uint64, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(dg, &obj); err != nil || obj == nil {
		return 0, false
	}
	raw, ok := obj["seq"]
	if !ok {
		return 0, false
	}
	var n uint64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}
