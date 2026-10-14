package tbm

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/events"
)

func TestParseRunLine(t *testing.T) {
	st, ok := ParseRunLine("TBM:RUN|uldaman|running|7/41|baelog|pull|pull|pulling 4851")
	if !ok || st.Plan != "uldaman" || st.State != RunRunning || st.Step != 7 || st.Steps != 41 || st.Leg != "baelog" || st.Kind != "pull" || st.Phase != "pull" || st.Note != "pulling 4851" {
		t.Fatalf("%+v %v", st, ok)
	}
	if got := st.Text(); got != "baelog · pull 7/41 · pulling" {
		t.Errorf("text %q", got)
	}
	st, ok = ParseRunLine("TBM:RUN|uldaman|paused|12/41|baelog|boss|begin|3 dead: regroup (cmd revive), then .bot run resume")
	if !ok || st.State != RunPaused || st.Note != "3 dead: regroup (cmd revive), then .bot run resume" {
		t.Fatalf("%+v", st)
	}
	if got := st.Text(); !strings.Contains(got, "boss 12/41") || !strings.Contains(got, "3 dead") {
		t.Errorf("paused text %q", got)
	}
	st, ok = ParseRunLine("TBM:RUN|-|none|0/0|-|-|-|no run")
	if !ok || st.Plan != "" || st.State != RunNone || st.Leg != "" || st.Kind != "" || st.Phase != "" || st.Text() != "no run" {
		t.Fatalf("%+v", st)
	}
	for _, bad := range []string{"", "TBM:RUN|", "TBM:RUN|a|running|7|x|y|z|w", "TBM:RUN|a|running|x/y|l|k|p|n", "TBM:RUN|a|running|1/2|l|k|p", "TBM:RUN|a|running|-1/2|l|k|p|n", "TBM:ACTION_ACK|run start|party|1|x"} {
		if _, ok := ParseRunLine(bad); ok {
			t.Errorf("%q was taken for a status line", bad)
		}
	}
	// a leg that has no name is its number; underscores read as spaces
	st, _ = ParseRunLine("TBM:RUN|uldaman|running|3/9|ancient_stone_keeper|wait_object|wait|waiting")
	if got := st.Text(); got != "ancient stone keeper · wait object 3/9 · waiting" {
		t.Errorf("text %q", got)
	}
}

func TestRunCommandsPassTheCheck(t *testing.T) {
	for _, c := range []string{"run start uldaman", "run start", "run pause", "run resume", "run skip", "run stop", "run status", "run list"} {
		if err := CheckCommand(c); err != nil {
			t.Errorf("%q: %v", c, err)
		}
	}
	for _, c := range []string{"run fly", "run start bad;plan", "run start ../x"} {
		if err := CheckCommand(c); err == nil {
			t.Errorf("%q passed", c)
		}
	}
	if Intent("run start uldaman") != "run start" {
		t.Error("intent")
	}
}

func TestViewFeedKeepsTheNewestRunLine(t *testing.T) {
	f := NewViewFeed(events.New(100), nil, 0)
	if _, n, ok := f.RunLatest(); ok || n != 0 {
		t.Fatal("a run line before any")
	}
	f.ProcessLine("TBM:RUN|uldaman|running|1/41|baelog|move|walk|walking")
	f.ProcessLine("TBM:RUN|uldaman|running|2/41|baelog|pull|gather|gathering")
	f.ProcessLine("TBM:ACTION_ACK|run pause|party|2|uldaman")
	st, n, ok := f.RunLatest()
	if !ok || n != 2 || st.Step != 2 || st.Phase != "gather" {
		t.Fatalf("%+v %d %v", st, n, ok)
	}
	f.Reset()
	if _, n, ok := f.RunLatest(); !ok || n != 2 {
		t.Errorf("a new binding forgets the counter: %d %v", n, ok)
	}
}

func TestLooseAckSettlesRunList(t *testing.T) {
	// `.bot run list` answers with the scope "plans", which the addon's own parser would drop; the reply still settles on it.
	log := events.New(100)
	since := log.Last()
	log.Append(events.Event{Kind: events.KindSystem, Text: "Dungeon plans: uldaman (map 70, 41 steps)"})
	log.Append(events.Event{Kind: events.KindSystem, Text: "TBM:ACTION_ACK|run list|plans|1|-"})
	r := ReadReply(context.Background(), log, since, "run list", 0)
	if r.Kind != KindAck || r.Ack == nil || r.Ack.Scope != "plans" || len(r.Lines) != 2 || !strings.HasPrefix(r.Lines[0], "Dungeon plans: ") {
		t.Fatalf("%+v", r)
	}
}
