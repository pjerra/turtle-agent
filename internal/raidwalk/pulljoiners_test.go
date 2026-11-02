package raidwalk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// A neighbouring pack with the same entry joins: counted, because the plan
// pack is owned by spawn guid and position, not by creature entry.
func TestPullPackMembersCountsSameEntryNeighbour(t *testing.T) {
	row := PackRow{HasRow: true, CX: 0, CY: 0, Radius: 15}
	seed := packet.GUID(0xF130001)
	pos := map[packet.GUID][3]float32{
		seed:      {0, 0, 0},   // pulled unit
		0xF130002: {5, 0, 0},   // pack mate within the radius: owned
		0xF130101: {100, 0, 0}, // neighbour with the same entry, far off: a joiner
		0xF130102: {120, 0, 0}, // neighbour with the same entry, far off: a joiner
	}
	got := PullPackMembers(seed, row, pos)
	if len(got) != 1 || got[0] != 0xF130002 {
		t.Fatalf("pack members %v, want only the mate at (5,0)", got)
	}
}

// The plan pack's other members never count as joiners: the pulled guid's
// pack mates within the row's radius are owned.
func TestPullPackMembersOwnsPackMates(t *testing.T) {
	row := PackRow{HasRow: true, CX: 10, CY: 0, Radius: 15}
	seed := packet.GUID(0xF130001)
	pos := map[packet.GUID][3]float32{
		seed:      {10, 0, 0},
		0xF130002: {14, 0, 0},
		0xF130003: {6, 0, 0},
		0xF130004: {10, 20, 0}, // 20 yd out: another pack
	}
	got := PullPackMembers(seed, row, pos)
	if len(got) != 2 || got[0] != 0xF130002 || got[1] != 0xF130003 {
		t.Fatalf("pack members %v, want the two mates within 15 yd", got)
	}
	if own := PullPackMembers(seed, PackRow{}, pos); own != nil {
		t.Fatalf("no row owns nothing, got %v", own)
	}
}

// The run.log line names the pull step, so a reader ties the count to its pull.
func TestPullJoinersLine(t *testing.T) {
	guids := []packet.GUID{0xF130102, 0xF130101}
	if got, want := PullJoinersLine(5, guids), "step 5: joiners 2 (0x000000000F130101 0x000000000F130102)"; got != want {
		t.Fatalf("line %q, want %q", got, want)
	}
	if PullJoinersLine(5, nil) != "" {
		t.Fatal("no joiners must give no line")
	}
}

// The report attaches the walk's joiner record to its pull step: the count in
// the markdown row, the guids in the JSON, and one fight with joiners even
// when the run log's status lines saw one too.
func TestReportAttachesPullJoiners(t *testing.T) {
	raw := []byte(`[
		{"step":2,"at":"2026-10-30T19:16:55Z","count":2,"guids":["0x000000000F130101","0x000000000F130102"]},
		{"step":5,"at":"2026-10-30T19:20:05Z","count":0,"guids":[]}]`)
	rep := reportFixture(t, ReportOptions{PullJoins: ParsePullJoins(raw)})
	if len(rep.Pulls) != 2 {
		t.Fatalf("pulls %+v", rep.Pulls)
	}
	p := rep.Pulls[0]
	if p.Step != 2 || p.JoinersSeen != 2 || len(p.JoinerGUIDs) != 2 || p.JoinerGUIDs[0] != "0x000000000F130101" {
		t.Fatalf("pull 3-1 joiners: %+v", p)
	}
	if q := rep.Pulls[1]; q.Step != 5 || q.JoinersSeen != 0 || len(q.JoinerGUIDs) != 0 {
		t.Fatalf("pull 3-2 joiners: %+v", q)
	}
	if md := rep.Markdown(); !strings.Contains(md, "frames: joiners 2 (0x000000000F130101 0x000000000F130102)") {
		t.Fatalf("markdown lacks the frames joiners:\n%s", md)
	}
	if rep.Totals.FightsWithJoins != 1 {
		t.Fatalf("totals %+v, want the one pull with joiners counted once", rep.Totals)
	}
	js, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatal(err)
	}
	if back.Pulls[0].JoinersSeen != 2 || len(back.Pulls[0].JoinerGUIDs) != 2 {
		t.Fatalf("json round trip: %+v", back.Pulls[0])
	}
}

// A retried pull at one step leaves a record per attempt: none replaces the
// attempt before it.
func TestParsePullJoinsKeepsEveryAttempt(t *testing.T) {
	raw := []byte(`[{"step":2,"at":"2026-11-02T03:00:02Z","count":1},{"step":2,"at":"2026-11-02T03:01:02Z","count":2}]`)
	recs := ParsePullJoins(raw)
	if len(recs) != 2 || recs[0].Step != 2 || recs[1].Step != 2 || !recs[0].At.Before(recs[1].At) {
		t.Fatalf("records %+v, want both attempts of step 2", recs)
	}
}

// Two fights and two pulls at one step: each fight takes the attempt whose
// pull time its own window holds, never the step's latest record. A record
// outside every window stays off the table and is counted in a note.
func TestReportAttachesJoinersPerAttempt(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	fight := func(id string, start time.Time, durS int) ReportFight {
		return ReportFight{ID: id, Step: 2, Kind: "pull", Start: float64(start.Unix()), DurMS: int64(durS) * 1000}
	}
	f1 := fight("f1", t0, 30)
	f2 := fight("f2", t0.Add(60*time.Second), 30)
	raw := []byte(`[
		{"step":2,"at":"2026-11-02T03:00:02Z","count":1,"guids":["0x000000000F130101"]},
		{"step":2,"at":"2026-11-02T03:01:02Z","count":2,"guids":["0x000000000F130201","0x000000000F130202"]},
		{"step":2,"at":"2026-11-02T03:05:00Z","count":9,"guids":["0x000000000F130901"]}]`)
	rep := BuildReport([]ReportFight{f2, f1}, nil, ReportOptions{PullJoins: ParsePullJoins(raw)})
	if len(rep.Pulls) != 2 {
		t.Fatalf("pulls %+v", rep.Pulls)
	}
	if p := rep.Pulls[0]; p.Step != 2 || p.JoinersSeen != 1 || len(p.JoinerGUIDs) != 1 || p.JoinerGUIDs[0] != "0x000000000F130101" {
		t.Fatalf("first attempt: %+v", p)
	}
	if p := rep.Pulls[1]; p.Step != 2 || p.JoinersSeen != 2 || len(p.JoinerGUIDs) != 2 || p.JoinerGUIDs[0] != "0x000000000F130201" || p.JoinerGUIDs[1] != "0x000000000F130202" {
		t.Fatalf("retried attempt: %+v", p)
	}
	if rep.Totals.FightsWithJoins != 2 {
		t.Fatalf("totals %+v, want both fights with joiners", rep.Totals)
	}
	var unmatched []string
	for _, n := range rep.Notes {
		if strings.Contains(n, "joiners unmatched:") {
			unmatched = append(unmatched, n)
		}
	}
	if len(unmatched) != 1 || !strings.Contains(unmatched[0], "joiners unmatched: 1") {
		t.Fatalf("notes %q, want one joiners unmatched note for the one stray record", rep.Notes)
	}
	if md := rep.Markdown(); strings.Contains(md, "joiners 9") {
		t.Fatalf("markdown shows a record outside every window:\n%s", md)
	}
}

// A folder from before the record (or a damaged one) yields no joiners, not an error.
func TestParsePullJoinsEmptyAndDamaged(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("not json"), []byte(`[{"step":0,"count":3}]`)} {
		if got := ParsePullJoins(raw); len(got) != 0 {
			t.Fatalf("%q: %+v, want no records", raw, got)
		}
	}
}
