package lft

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestBuilders pins the exact addon text. Turtle's LFT addon reads every field after
// "TW_LFG\t" and splits it on ';' itself (LFTMgr.cpp:47-51), so a stray space or a role in
// the wrong order is a different request.
func TestBuilders(t *testing.T) {
	text, err := QueueJoin([]string{"ragefirechasm", "wc", "ragefirechasm"}, "dT")
	if err != nil {
		t.Fatalf("QueueJoin: %v", err)
	}
	if want := "C2S_QUEUE_JOIN;ragefirechasm:wc;td"; text != want {
		t.Errorf("QueueJoin = %q, want %q", text, want)
	}
	if text, err := QueueJoin([]string{"rfc"}, "t"); err != nil || text != "C2S_QUEUE_JOIN;rfc;t" {
		t.Errorf("QueueJoin single = %q, %v", text, err)
	}

	cases := []struct {
		name, got, want string
	}{
		{"leave", QueueLeave(), "C2S_QUEUE_LEAVE"},
		{"status", QueueStatus(), "C2S_GET_QUEUE_STATUS"},
		{"rolecheck", RolecheckResponse("dT"), "C2S_ROLECHECK_RESPONSE;td"},
		{"offer", OfferAccept(), "C2S_OFFER_ACCEPT"},
		{"list", GroupsList(), "C2S_GET_GROUPS_LIST"},
		{"details", GroupDetails(7), "C2S_GET_GROUP_DETAILS;7"},
		{"listing role t", ListingRole("T"), "t"},
		{"listing role 0", ListingRole("0"), "0"},
		{"listing role empty", ListingRole(""), "0"},
		{"listing role unknown", ListingRole("x"), "0"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	refused := []struct {
		name      string
		instances []string
		roles     string
	}{
		{"no instances", nil, "t"},
		{"empty roles", []string{"rfc"}, ""},
		{"unknown role", []string{"rfc"}, "x"},
		{"bad instance", []string{"Rage;fire"}, "t"},
		{"uppercase instance", []string{"Ragefirechasm"}, "t"},
		{"eleven instances", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}, "t"},
	}
	for _, c := range refused {
		if _, err := QueueJoin(c.instances, c.roles); err == nil {
			t.Errorf("QueueJoin(%v, %q) accepted, want an error", c.instances, c.roles)
		}
	}

	// Eight 32-byte instances make a 280-byte join; the addon message (prefix, tab and text)
	// must fit in 255 bytes, so the text limit is 248 (LFTMgr.cpp:41-51 reads the prefix+tab).
	long := make([]string, 8)
	for i := range long {
		long[i] = strings.Repeat("a", 31) + string(rune('a'+i))
	}
	if _, err := QueueJoin(long, "d"); err == nil {
		t.Errorf("QueueJoin accepted a %d-byte text, want an error", 15+len(strings.Join(long, ":"))+2)
	}
}

// TestParseQueueLines pins every S2C line the queue protocol has, with its field order.
// Source: src/game/LFT/LFTQeueue.cpp on feat/lft-listing-api @ 6c5c7a2: HandleQueueJoin
// (lines 64-91), HandleRolecheckResponse + S2C_ROLECHECK_INFO (112-140), SendQueueStatus/
// SendQueuedStatus/SendQueueJoined/SendQueueLeft (183-231), StartRolecheck (233-276),
// TryBuildOfferForInstance (387-514), CompleteOffer (520-570).
func TestParseQueueLines(t *testing.T) {
	cases := []struct {
		line   string
		kind   string
		fields []string
	}{
		{"S2C_QUEUE_ERROR;raid", "S2C_QUEUE_ERROR", []string{"raid"}},
		{"S2C_QUEUE_ERROR;not_leader", "S2C_QUEUE_ERROR", []string{"not_leader"}},
		{"S2C_QUEUE_ERROR;invalid", "S2C_QUEUE_ERROR", []string{"invalid"}},
		{"S2C_ROLECHECK_START;ragefirechasm:wc", "S2C_ROLECHECK_START", []string{"ragefirechasm:wc"}},
		{"S2C_UPDATE_QUEUE_STATUS;pending_rolecheck", "S2C_UPDATE_QUEUE_STATUS", []string{"pending_rolecheck"}},
		{"S2C_ROLECHECK_INFO;Tacmdaron;td", "S2C_ROLECHECK_INFO", []string{"Tacmdaron", "td"}},
		{"S2C_QUEUE_JOINED", "S2C_QUEUE_JOINED", nil},
		{"S2C_UPDATE_QUEUE_STATUS;queued;ragefirechasm:wc;1793209831;0;1:1:3", "S2C_UPDATE_QUEUE_STATUS",
			[]string{"queued", "ragefirechasm:wc", "1793209831", "0", "1:1:3"}},
		{"S2C_UPDATE_QUEUE_STATUS;pending_offer", "S2C_UPDATE_QUEUE_STATUS", []string{"pending_offer"}},
		{"S2C_OFFER_NEW;ragefirechasm;t", "S2C_OFFER_NEW", []string{"ragefirechasm", "t"}},
		{"S2C_OFFER_UPDATE_COUNT;1:0:0", "S2C_OFFER_UPDATE_COUNT", []string{"1:0:0"}},
		{"S2C_OFFER_COMPLETE", "S2C_OFFER_COMPLETE", nil},
		{"S2C_QUEUE_LEFT;Tacmdaron", "S2C_QUEUE_LEFT", []string{"Tacmdaron"}},
	}
	for _, c := range cases {
		m := Parse(c.line)
		if m.Kind != c.kind {
			t.Errorf("Parse(%q).Kind = %q, want %q", c.line, m.Kind, c.kind)
		}
		if !reflect.DeepEqual(m.Fields, c.fields) {
			t.Errorf("Parse(%q).Fields = %#v, want %#v", c.line, m.Fields, c.fields)
		}
		if m.JSON != nil {
			t.Errorf("Parse(%q).JSON = %s, want none", c.line, m.JSON)
		}
		if m.Raw != c.line {
			t.Errorf("Parse(%q).Raw = %q", c.line, m.Raw)
		}
	}
}

// TestParseListingLines uses lines copied from ~/TortoiseBots-audit/tests/lftapi/after.txt
// (a live capture against feat/lft-listing-api @ 6c5c7a2). Listing JSON is read from
// LFTUserGroups.cpp BuildListingJson (901-911) and BuildSignupJson (916-924); a description
// may hold ';' because it is JSON-escaped, not field-escaped, so only the leading fields are
// split (LFTUserGroups.cpp 616-632).
func TestParseListingLines(t *testing.T) {
	data, err := os.ReadFile("testdata/listing-lines.txt")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("testdata has %d lines, want 4", len(lines))
	}

	start := Parse(lines[0])
	if start.Kind != "S2C_GROUPS_LIST_UPDATE" || len(start.Fields) != 1 || start.Fields[0] != "start" {
		t.Fatalf("start line = %#v", start)
	}

	listingMsg := Parse(lines[1])
	if listingMsg.Kind != "S2C_GROUPS_LIST_UPDATE" || len(listingMsg.Fields) != 1 || listingMsg.JSON == nil {
		t.Fatalf("listing line = %#v", listingMsg)
	}
	var listing Listing
	if err := json.Unmarshal(listingMsg.JSON, &listing); err != nil {
		t.Fatalf("listing JSON: %v", err)
	}
	want := Listing{ID: 1, Creator: "Tacmdaron", Class: "MAGE", Level: 60, Title: "T1", Description: "D1",
		Category: 2, Limit: [3]int{1, 1, 3}}
	if !reflect.DeepEqual(listing, want) {
		t.Errorf("listing = %#v, want %#v", listing, want)
	}

	end := Parse(lines[2])
	if len(end.Fields) != 1 || end.Fields[0] != "end" {
		t.Fatalf("end line = %#v", end)
	}

	row := Parse(lines[3])
	if row.Kind != "S2C_UPDATE_GROUP" || !reflect.DeepEqual(row.Fields, []string{"2", "1"}) || row.JSON == nil {
		t.Fatalf("role row = %#v", row)
	}
	var signup Signup
	if err := json.Unmarshal(row.JSON, &signup); err != nil {
		t.Fatalf("signup JSON: %v", err)
	}
	if signup.Name != "Tacmdaron" || signup.Class != "MAGE" || signup.Level != 60 || !signup.Confirmed {
		t.Errorf("signup = %#v", signup)
	}

	// Folding start + listing + end commits the listing.
	var st State
	at := time.Unix(1793209831, 0)
	for _, line := range lines[:3] {
		st.Fold(Parse(line), at)
	}
	if got := st.Listings[1]; got.Description != "D1" || got.ID != 1 {
		t.Errorf("folded listing = %#v", got)
	}

	// A details row for listing 1 arrives with its role index; the role is the second field,
	// not part of the signup JSON (LFTUserGroups.cpp:625-631).
	detail := Parse(`S2C_UPDATE_GROUP;1;1;{"name":"Tacmdbilo","class":"MAGE","level":60,"confirmed":true}`)
	st.Fold(detail, at)
	got := st.Listings[1]
	if len(got.Signups) != 1 || got.Signups[0].Name != "Tacmdbilo" || got.Signups[0].Role != "t" {
		t.Errorf("signups = %#v, want Tacmdbilo as t", got.Signups)
	}

	// A description holding ';' survives because only the leading fields are split.
	constructed := `S2C_GROUPS_LIST_UPDATE;{"id":9,"creator":"X","class":"WARRIOR","level":60,"title":"T","description":"a;b","category":1,"limit":[0,0,0],"numConfirmed":[0,0,0]}`
	m := Parse(constructed)
	if len(m.Fields) != 1 || m.JSON == nil {
		t.Fatalf("constructed listing = %#v", m)
	}
	var withSemi Listing
	if err := json.Unmarshal(m.JSON, &withSemi); err != nil {
		t.Fatalf("constructed JSON: %v", err)
	}
	if withSemi.Description != "a;b" || withSemi.ID != 9 {
		t.Errorf("constructed listing = %#v", withSemi)
	}
	var st2 State
	st2.Fold(Parse("S2C_GROUPS_LIST_UPDATE;start"), at)
	st2.Fold(m, at)
	st2.Fold(Parse("S2C_GROUPS_LIST_UPDATE;end"), at)
	if st2.Listings[9].Description != "a;b" {
		t.Errorf("folded description = %q, want a;b", st2.Listings[9].Description)
	}
}

// TestFoldFullQueueFlow walks the state through a successful queue: role check, info rows,
// queue, offer, counts, formed group; then the error and leave paths (LFTQeueue.cpp).
func TestFoldFullQueueFlow(t *testing.T) {
	var s State
	t0 := time.Unix(1793209831, 0)

	s.Fold(Parse("S2C_ROLECHECK_START;ragefirechasm:wc"), t0)
	if s.Phase != PhaseRolecheck {
		t.Fatalf("phase after rolecheck start = %q", s.Phase)
	}
	if !s.RolecheckSince.Equal(t0) {
		t.Errorf("rolecheck since = %v, want %v", s.RolecheckSince, t0)
	}
	if !s.RolecheckDeadline.Equal(t0.Add(90 * time.Second)) {
		t.Errorf("rolecheck deadline = %v, want +90s", s.RolecheckDeadline)
	}
	if !reflect.DeepEqual(s.Instances, []string{"ragefirechasm", "wc"}) {
		t.Errorf("instances = %#v", s.Instances)
	}

	s.Fold(Parse("S2C_ROLECHECK_INFO;Tacmdaron;td"), t0)
	if s.Responses["Tacmdaron"] != "td" {
		t.Errorf("responses = %#v", s.Responses)
	}

	s.Fold(Parse("S2C_QUEUE_JOINED"), t0)
	if s.Phase != PhaseQueued {
		t.Errorf("phase after QUEUE_JOINED = %q", s.Phase)
	}
	s.Fold(Parse("S2C_UPDATE_QUEUE_STATUS;queued;ragefirechasm:wc;1793209999;0;1:1:3"), t0)
	if s.Phase != PhaseQueued || s.QueuedSince != 1793209999 || s.Counts != [3]int{1, 1, 3} {
		t.Errorf("queued state = phase %q since %d counts %v", s.Phase, s.QueuedSince, s.Counts)
	}

	t1 := t0.Add(20 * time.Second)
	s.Fold(Parse("S2C_OFFER_NEW;ragefirechasm;t"), t1)
	if s.Phase != PhaseOffer {
		t.Fatalf("phase after OFFER_NEW = %q", s.Phase)
	}
	if s.Offer == nil || s.Offer.Instance != "ragefirechasm" || s.Offer.Role != "t" {
		t.Errorf("offer = %#v", s.Offer)
	}
	if !s.OfferSince.Equal(t1) || !s.OfferDeadline.Equal(t1.Add(90*time.Second)) {
		t.Errorf("offer since %v deadline %v, want %v and +90s", s.OfferSince, s.OfferDeadline, t1)
	}
	if s.Counts != [3]int{0, 0, 0} {
		t.Errorf("counts at offer = %v, want zero", s.Counts)
	}

	s.Fold(Parse("S2C_OFFER_UPDATE_COUNT;1:0:0"), t1)
	if s.Counts != [3]int{1, 0, 0} || s.Offer.Counts != [3]int{1, 0, 0} {
		t.Errorf("counts = %v, offer counts = %v", s.Counts, s.Offer.Counts)
	}

	s.Fold(Parse("S2C_OFFER_COMPLETE"), t1)
	if s.Phase != PhaseGrouped {
		t.Errorf("phase after OFFER_COMPLETE = %q", s.Phase)
	}

	var e State
	e.Fold(Parse("S2C_QUEUE_ERROR;not_leader"), t0)
	if e.Phase != PhaseError || e.LastError != "not_leader" {
		t.Errorf("error state = %q %q", e.Phase, e.LastError)
	}
	e.Fold(Parse("S2C_QUEUE_LEFT;Tacmdaron"), t0)
	if e.Phase != PhaseLeft {
		t.Errorf("phase after QUEUE_LEFT = %q", e.Phase)
	}

	if len(e.History) != 2 || e.History[1].Text != "S2C_QUEUE_LEFT;Tacmdaron" || !e.History[1].At.Equal(t0) {
		t.Errorf("history = %#v", e.History)
	}
}

// TestAllowedRoles pins the core's class mask (LFTQeueue.cpp AllowedRoleMask, lines 717-736):
// druid/paladin/shaman t h d, warrior t d, priest h d, the rest d.
func TestAllowedRoles(t *testing.T) {
	cases := []struct {
		class uint8
		want  string
	}{
		{1, "td"},   // warrior
		{2, "thd"},  // paladin
		{3, "d"},    // hunter
		{4, "d"},    // rogue
		{5, "hd"},   // priest
		{7, "thd"},  // shaman
		{8, "d"},    // mage
		{9, "d"},    // warlock
		{11, "thd"}, // druid
		{6, ""},     // no such class
		{10, ""},
		{0, ""},
	}
	for _, c := range cases {
		if got := AllowedRoles(c.class); got != c.want {
			t.Errorf("AllowedRoles(%d) = %q, want %q", c.class, got, c.want)
		}
	}
	if Mask(AllowedRoles(5))&Mask("t") != 0 {
		t.Errorf("priest may not tank, but its mask %q allows t", AllowedRoles(5))
	}
	if got := Mask("THD"); got != 0x07 {
		t.Errorf("Mask(THD) = %d, want 7", got)
	}
	if got := Mask("tx"); got != 0x01 {
		t.Errorf("Mask(tx) = %d, want 1", got)
	}
}
