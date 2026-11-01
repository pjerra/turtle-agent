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

func TestParseRunLineLeftPacks(t *testing.T) {
	st, ok := ParseRunLine("TBM:RUN|uldaman|done|41/41|archaedas|boss|pull|plan done|left 2")
	if !ok || st.Left != 2 || st.Note != "plan done" {
		t.Fatalf("%+v %v", st, ok)
	}
	st, ok = ParseRunLine("TBM:RUN|uldaman|running|7/41|baelog|pull|pull|pulling 4851")
	if !ok || st.Left != 0 || st.Note != "pulling 4851" {
		t.Fatalf("no field: %+v %v", st, ok)
	}
	// a note that merely mentions left text is not the trailing field
	for _, line := range []string{
		"TBM:RUN|uldaman|paused|12/41|baelog|boss|begin|left alone: regroup",
		"TBM:RUN|uldaman|paused|12/41|baelog|boss|begin|3 left: regroup|left 0",
		"TBM:RUN|uldaman|done|41/41|archaedas|boss|pull|plan done|left x",
		"TBM:RUN|uldaman|done|41/41|archaedas|boss|pull|plan done|leftover",
	} {
		st, ok = ParseRunLine(line)
		if !ok || st.Left != 0 {
			t.Errorf("%q: Left %d, ok %v", line, st.Left, ok)
		}
	}
	st, ok = ParseRunLine("TBM:RUN|uldaman|done|41/41|archaedas|boss|pull|plan done|left 2|left 3")
	if !ok || st.Left != 3 {
		t.Fatalf("last field wins: %+v %v", st, ok)
	}
}

// TestParseRunLineLabel is the trailing speed/until label (dungeon-run-leader.md): taken off the note, after `left`,
// read into its parts; a field that only looks like one stays note text.
func TestParseRunLineLabel(t *testing.T) {
	// ZG walk 4's start line (tortoise-test2, 2026-10-30)
	st, ok := ParseRunLine("TBM:RUN|zul_gurub|running|1/1104|high_priestess_jeklik|move|begin|started|until boss (step 41)")
	if !ok || st.Note != "started" || st.Label != "until boss (step 41)" || st.Until != "until boss" || st.UntilStep != 41 || st.Speed != 0 {
		t.Fatalf("%+v %v", st, ok)
	}
	st, ok = ParseRunLine("TBM:RUN|windhorn_canyon|done|1890/1890|x|boss|pull|plan done (1 pack left alive)|left 1|speed 2.0 (test)")
	if !ok || st.Left != 1 || st.Note != "plan done (1 pack left alive)" || st.Speed != 2.0 || st.SpeedFellBack || st.Until != "" {
		t.Fatalf("left before the label: %+v %v", st, ok)
	}
	st, ok = ParseRunLine("TBM:RUN|molten_core|running|3/90|magmadar|pull|walk|walking|speed 3.0 (test); until boss (step 38)")
	if !ok || st.Speed != 3.0 || st.Until != "until boss" || st.UntilStep != 38 || st.Note != "walking" {
		t.Fatalf("both parts: %+v %v", st, ok)
	}
	st, ok = ParseRunLine("TBM:RUN|molten_core|running|3/90|magmadar|pull|walk|walking|speed 2.0 (test; 1.0 since a real player joined); until leg gehennas (step 29)")
	if !ok || st.Speed != 2.0 || !st.SpeedFellBack || st.Until != "until leg gehennas" || st.UntilStep != 29 {
		t.Fatalf("fell back: %+v %v", st, ok)
	}
	st, ok = ParseRunLine("TBM:RUN|molten_core|done|38/90|magmadar|boss|begin|stopped before step 38 (until boss): the party stays where it stands")
	if !ok || st.Label != "" || st.Note != "stopped before step 38 (until boss): the party stays where it stands" {
		t.Fatalf("label dropped at the stop: %+v %v", st, ok)
	}
	// a field the parser does not know stays note text, and so does a label-like field with an unknown part
	for _, line := range []string{
		"TBM:RUN|zul_gurub|paused|29/1104|j|pull|pull|15 dead: 2 near wipes within 10 min|the run stops here",
		"TBM:RUN|zul_gurub|paused|29/1104|j|pull|pull|15 dead|until boss (step 41); something new",
		"TBM:RUN|zul_gurub|paused|29/1104|j|pull|pull|15 dead|speed fast",
	} {
		st, ok = ParseRunLine(line)
		want := line[strings.Index(line, "pull|pull|")+len("pull|pull|"):]
		if !ok || st.Label != "" || st.Note != want {
			t.Errorf("%q: note %q label %q", line, st.Note, st.Label)
		}
	}
	a, _ := ParseRunLine("TBM:RUN|p|running|3/9|l|pull|walk|walking|speed 2.0 (test)")
	b, _ := ParseRunLine("TBM:RUN|p|running|3/9|l|pull|walk|walking|speed 2.0 (test; 1.0 since a real player joined)")
	if a.Same(b) {
		t.Error("a label change is a new status")
	}
}

// TestParseRunLineEventPhase is a hold step's event count in the phase field.
func TestParseRunLineEventPhase(t *testing.T) {
	st, ok := ParseRunLine("TBM:RUN|uldaman|running|30/41|archaedas|hold|fight 1/4 down|event: fighting (1 of 4 down, 1 awake)")
	if !ok || st.Phase != "fight 1/4 down" || st.EventDown != 1 || st.EventSeen != 4 || !st.EventFight {
		t.Fatalf("%+v %v", st, ok)
	}
	if got := st.Text(); got != "archaedas · hold 30/41 · event fight (1 of 4 down)" {
		t.Errorf("text %q", got)
	}
	st, _ = ParseRunLine("TBM:RUN|uldaman|running|30/41|archaedas|hold|hold 0/4 down|event: waiting")
	if !st.EventPhase() || st.EventFight || st.EventDown != 0 || st.EventSeen != 4 {
		t.Fatalf("%+v", st)
	}
	if got := st.Text(); got != "archaedas · hold 30/41 · holding for the event (0 of 4 down)" {
		t.Errorf("text %q", got)
	}
	st, _ = ParseRunLine("TBM:RUN|uldaman|running|7/41|baelog|pull|pull|pulling")
	if st.EventPhase() {
		t.Error("a pull phase is no event count")
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
