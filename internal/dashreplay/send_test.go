package dashreplay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCheckTarget(t *testing.T) {
	cases := []struct {
		name    string
		to      string
		iKnow   bool
		ok      bool
		warning bool
	}{
		{"loopback v4", "127.0.0.1:9195", false, true, false},
		{"loopback v6", "[::1]:9195", false, true, false},
		{"the name localhost", "localhost:9195", false, true, false},
		{"a private address", "10.0.0.5:9195", false, false, false},
		{"a tailscale address", "100.86.248.75:8790", false, false, false},
		{"a private address with i-know", "10.0.0.5:9195", true, true, true},
		{"port 0", "127.0.0.1:0", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warn, err := CheckTarget(tc.to, tc.iKnow)
			if tc.ok && err != nil {
				t.Fatalf("CheckTarget(%q, iKnow=%v) = %v, want no error", tc.to, tc.iKnow, err)
			}
			if !tc.ok {
				var refused *TargetRefusedError
				if !errors.As(err, &refused) {
					t.Fatalf("CheckTarget(%q, iKnow=%v) = %v, want a *TargetRefusedError", tc.to, tc.iKnow, err)
				}
				if refused.To != tc.to {
					t.Errorf("refused.To = %q, want %q", refused.To, tc.to)
				}
			}
			if tc.warning && !strings.Contains(warn, "non-loopback "+tc.to) {
				t.Errorf("warning = %q, want it to name non-loopback %s", warn, tc.to)
			}
			if !tc.warning && warn != "" {
				t.Errorf("warning = %q, want none", warn)
			}
		})
	}
}

// The exact refusal text the command prints and the README quotes.
func TestCheckTargetRefusalText(t *testing.T) {
	_, err := CheckTarget("10.0.0.5:9195", false)
	if err == nil {
		t.Fatal("CheckTarget accepted a private address")
	}
	want := "refusing to replay to 10.0.0.5:9195: not a loopback address; pass --i-know if this daemon is a test dashboard"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestRewriteKeepsOtherKeys(t *testing.T) {
	dg := json.RawMessage(`{"v":12,"session":3,"seq":9,"ts":5,"type":"COMMANDERS","commanders":[{"commander":"Glacienne","bots":[{"name":"Alpha"},{"name":"Beta"}]}]}`)
	out, err := Rewrite(dg, 1793209269, 2, 1793209999)
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("rewritten datagram is not an object: %v (%s)", err, out)
	}
	vars := []struct {
		key  string
		want string
	}{
		{"session", "1793209269"},
		{"seq", "2"},
		{"ts", "1793209999"},
		{"replay", "true"},
		{"v", "12"},
		{"type", `"COMMANDERS"`},
	}
	for _, v := range vars {
		if string(got[v.key]) != v.want {
			t.Errorf("%s = %s, want %s", v.key, got[v.key], v.want)
		}
	}
	var orig, kept map[string]json.RawMessage
	if err := json.Unmarshal(dg, &orig); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"commanders":`+string(got["commanders"])+`}`), &kept); err != nil {
		t.Fatalf("commanders: %v", err)
	}
	if !jsonEqual(t, kept["commanders"], orig["commanders"]) {
		t.Errorf("commanders = %s, want the recorded %s", kept["commanders"], orig["commanders"])
	}
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

func receiveDatagrams(t *testing.T, pc net.PacketConn, n int) []map[string]json.RawMessage {
	t.Helper()
	got := make([]map[string]json.RawMessage, 0, n)
	buf := make([]byte, 65536)
	for i := range n {
		if err := pc.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		size, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("datagram %d of %d: %v", i+1, n, err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(append([]byte(nil), buf[:size]...), &obj); err != nil {
			t.Fatalf("datagram %d: not a JSON object: %v (%q)", i+1, err, buf[:size])
		}
		got = append(got, obj)
	}
	return got
}

func rawUint(t *testing.T, obj map[string]json.RawMessage, key string) uint64 {
	t.Helper()
	var n uint64
	if err := json.Unmarshal(obj[key], &n); err != nil {
		t.Fatalf("%s = %s, not a number: %v", key, obj[key], err)
	}
	return n
}

func rawString(t *testing.T, obj map[string]json.RawMessage, key string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(obj[key], &s); err != nil {
		t.Fatalf("%s = %s, not a string: %v", key, obj[key], err)
	}
	return s
}

func rawBool(t *testing.T, obj map[string]json.RawMessage, key string) bool {
	t.Helper()
	var b bool
	if err := json.Unmarshal(obj[key], &b); err != nil {
		t.Fatalf("%s = %s, not a bool: %v", key, obj[key], err)
	}
	return b
}

// Five datagrams, three BOT_BATCH sharing a seq (a snapshot cycle), at speed
// 1000 with a 50 ms cap: the datagrams carry a rewritten session, seq 1 from
// the smallest recorded seq 7, the fake clock's time and replay: true, and
// the sleeps are the capped gaps.
func TestPlayToLocalUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	lines := []Line{
		{TMS: 0, DG: json.RawMessage(`{"v":12,"session":3,"seq":7,"ts":100,"type":"BOT_BATCH","part":1,"bots":[]}`)},
		{TMS: 20000, DG: json.RawMessage(`{"v":12,"session":3,"seq":7,"ts":100,"type":"BOT_BATCH","part":2,"bots":[]}`)},
		{TMS: 40000, DG: json.RawMessage(`{"v":12,"session":3,"seq":8,"ts":100,"type":"BOT_BATCH","part":1,"bots":[]}`)},
		{TMS: 100000, DG: json.RawMessage(`{"v":12,"session":3,"seq":7,"ts":100,"type":"COMMANDERS","commanders":[]}`)},
		{TMS: 100000, DG: json.RawMessage(`{"v":12,"session":3,"seq":7,"ts":100,"type":"COMBAT_FIGHT","fight":{"id":"1-1"}}`)},
	}

	clock := time.Unix(1793209831, 0)
	var slept []time.Duration
	var progress bytes.Buffer
	opts := Options{
		To:     pc.LocalAddr().String(),
		Speed:  1000,
		MaxGap: 50 * time.Millisecond,
		Now:    func() time.Time { return clock },
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}
	stats, err := Play(context.Background(), lines, opts, &progress)
	if err != nil {
		t.Fatalf("Play: %v", err)
	}
	if stats.Sent != 5 || stats.Failed != 0 || stats.Skipped != 0 {
		t.Errorf("stats = sent %d, failed %d, skipped %d, want 5, 0, 0", stats.Sent, stats.Failed, stats.Skipped)
	}
	wantByType := map[string]int{"BOT_BATCH": 3, "COMMANDERS": 1, "COMBAT_FIGHT": 1}
	if !reflect.DeepEqual(stats.ByType, wantByType) {
		t.Errorf("by type = %v, want %v", stats.ByType, wantByType)
	}
	wantSleeps := []time.Duration{20 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond}
	if !reflect.DeepEqual(slept, wantSleeps) {
		t.Errorf("sleeps = %v, want %v (gap/speed, capped at max-gap)", slept, wantSleeps)
	}

	got := receiveDatagrams(t, pc, 5)
	wantSeq := []uint64{1, 1, 2, 1, 1}
	wantTypes := []string{"BOT_BATCH", "BOT_BATCH", "BOT_BATCH", "COMMANDERS", "COMBAT_FIGHT"}
	for i, obj := range got {
		if got := rawString(t, obj, "type"); got != wantTypes[i] {
			t.Errorf("datagram %d type = %q, want %q (the recorded order)", i+1, got, wantTypes[i])
		}
		if got := rawUint(t, obj, "session"); got != uint64(clock.Unix()) {
			t.Errorf("datagram %d session = %d, want the replay session %d", i+1, got, clock.Unix())
		}
		if got := rawUint(t, obj, "seq"); got != wantSeq[i] {
			t.Errorf("datagram %d seq = %d, want %d", i+1, got, wantSeq[i])
		}
		if got := rawUint(t, obj, "ts"); got != uint64(clock.Unix()) {
			t.Errorf("datagram %d ts = %d, want the fake clock's %d", i+1, got, clock.Unix())
		}
		if !rawBool(t, obj, "replay") {
			t.Errorf("datagram %d has no replay: true", i+1)
		}
	}
}

func TestLoopNewSession(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	lines := []Line{
		{TMS: 0, DG: json.RawMessage(`{"v":12,"session":3,"seq":1,"ts":100,"type":"HEARTBEAT","diff":50}`)},
		{TMS: 1000, DG: json.RawMessage(`{"v":12,"session":3,"seq":2,"ts":101,"type":"HEARTBEAT","diff":50}`)},
	}
	clock := time.Unix(1793209831, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sleeps := 0
	opts := Options{
		To:     pc.LocalAddr().String(),
		Speed:  1,
		MaxGap: 10 * time.Second,
		Loop:   true,
		Now:    func() time.Time { return clock },
		Sleep: func(_ context.Context, _ time.Duration) error {
			sleeps++
			if sleeps == 3 {
				cancel()
				return context.Canceled
			}
			return nil
		},
	}
	stats, err := Play(ctx, lines, opts, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Play = %v, want the cancel that stopped the loop", err)
	}
	if stats.Loops < 2 {
		t.Fatalf("loops = %d, want at least 2", stats.Loops)
	}

	var sessions []uint64
	buf := make([]byte, 65536)
	for {
		pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		size, _, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(append([]byte(nil), buf[:size]...), &obj); err != nil {
			t.Fatalf("datagram is not a JSON object: %v", err)
		}
		sessions = append(sessions, rawUint(t, obj, "session"))
	}
	if len(sessions) < 3 {
		t.Fatalf("%d datagrams received, want the first loop and the start of the second", len(sessions))
	}
	if sessions[0] != uint64(clock.Unix()) {
		t.Errorf("first session = %d, want the frozen clock's %d", sessions[0], clock.Unix())
	}
	if sessions[2] <= sessions[0] {
		t.Errorf("second loop's session %d is not newer than the first loop's %d (a reused session is deduped away)", sessions[2], sessions[0])
	}
	if sessions[1] != sessions[0] {
		t.Errorf("session changed inside one loop: %d then %d", sessions[0], sessions[1])
	}
}

// One progress line per 10 s of played time.
func TestPlayPrintsProgress(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	lines := []Line{
		{TMS: 0, DG: json.RawMessage(`{"v":12,"session":3,"seq":1,"ts":100,"type":"BOT_BATCH","bots":[]}`)},
		{TMS: 25000, DG: json.RawMessage(`{"v":12,"session":3,"seq":1,"ts":125,"type":"BOT_BATCH","bots":[]}`)},
		{TMS: 30000, DG: json.RawMessage(`{"v":12,"session":3,"seq":2,"ts":130,"type":"BOT_BATCH","bots":[]}`)},
	}
	var out bytes.Buffer
	opts := Options{
		To:     pc.LocalAddr().String(),
		Speed:  1,
		MaxGap: time.Minute,
		Sleep:  func(context.Context, time.Duration) error { return nil },
	}
	if _, err := Play(context.Background(), lines, opts, &out); err != nil {
		t.Fatalf("Play: %v", err)
	}
	got := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(got) != 2 {
		t.Fatalf("progress lines = %q, want 2 (after 25 s and after 30 s of played time; a wait that jumps past a boundary prints once)", out.String())
	}
	if got[0] != "sent 1 (BOT_BATCH 1)" {
		t.Errorf("first progress line = %q, want %q", got[0], "sent 1 (BOT_BATCH 1)")
	}
	if got[1] != "sent 2 (BOT_BATCH 2)" {
		t.Errorf("last progress line = %q, want %q", got[1], "sent 2 (BOT_BATCH 2)")
	}
}

func TestPlan(t *testing.T) {
	lines := []Line{
		{TMS: 0, DG: json.RawMessage(`{"type":"COMMANDERS"}`)},
		{TMS: 20000, DG: json.RawMessage(`{"type":"HEARTBEAT"}`)},
		{TMS: 80000, DG: json.RawMessage(`{"type":"HEARTBEAT"}`)},
	}
	total, byType := Plan(lines, 10, 5*time.Second)
	if want := 7 * time.Second; total != want {
		t.Errorf("plan = %s, want %s (the 20 s gap is 2 s at speed 10, the 60 s gap 6 s capped at 5 s)", total, want)
	}
	want := map[string]int{"COMMANDERS": 1, "HEARTBEAT": 2}
	if !reflect.DeepEqual(byType, want) {
		t.Errorf("by type = %v, want %v", byType, want)
	}
	if got, want := TypeSummary(byType), "COMMANDERS 1, HEARTBEAT 2"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}
