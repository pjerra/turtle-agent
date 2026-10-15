package dungeon

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The listing and the ACK of `commander status` are read as the module writes them (BotCommands.cpp, HandleCommander status).
func TestReadBoundParty(t *testing.T) {
	reply := tbm.Reply{Kind: tbm.KindAck, OK: true, Ack: &tbm.Ack{Intent: "commander status", Scope: "party", Count: 3, Executor: "Lcwarra"},
		Lines: []string{"You command Lcwarra's group: Lcwarra, Lchunter (dead), Lcpriest.", "TBM:ACTION_ACK|commander status|party|3|Lcwarra"}}
	b, err := readBoundParty(context.Background(), func(context.Context, string) (tbm.Reply, error) { return reply, nil })
	if err != nil || !b.Answered || b.Lead != "Lcwarra" || b.Count != 3 || strings.Join(b.Names, ",") != "Lcwarra,Lchunter,Lcpriest" {
		t.Fatalf("%+v %v", b, err)
	}
	b, _ = readBoundParty(context.Background(), func(context.Context, string) (tbm.Reply, error) { return tbm.Reply{Kind: tbm.KindTimeout}, nil })
	if b.Answered {
		t.Fatalf("a timeout counted as an answer: %+v", b)
	}
}

func TestPartyProblems(t *testing.T) {
	wanted := []string{"Lcwarra", "Lcpriest", "Lchunter"}
	full := boundParty{Answered: true, Lead: "Lcwarra", Count: 3, Names: []string{"Lcwarra", "Lchunter", "Lcpriest"}}
	for _, c := range []struct {
		name         string
		st           boundParty
		feed         []string
		haveFeed     bool
		missing, ext string
	}{
		{"whole", full, []string{"lcwarra", "Lcpriest", "Lchunter"}, true, "", ""},
		{"status only", full, nil, false, "", ""},
		{"left the listing", boundParty{Answered: true, Lead: "Lcwarra", Count: 2, Names: []string{"Lcwarra", "Lcpriest"}}, nil, false, "Lchunter", ""},
		{"left the feed", full, []string{"Lcwarra", "Lcpriest"}, true, "Lchunter", ""},
		{"an unnamed bot", boundParty{Answered: true, Lead: "Lcwarra", Count: 4, Names: []string{"Lcwarra", "Lcpriest", "Lchunter", "Lcrogue"}}, nil, false, "", "Lcrogue"},
		{"count only", boundParty{Answered: true, Lead: "Lcwarra", Count: 2}, nil, false, "(2 of 3 bots)", ""},
		{"another lead", boundParty{Answered: true, Lead: "Lcpriest", Count: 3, Names: []string{"Lcwarra", "Lcpriest", "Lchunter"}}, nil, false, "Lcwarra", ""},
	} {
		missing, extra, _ := partyProblems("Lcwarra", wanted, c.st, c.feed, c.haveFeed)
		if strings.Join(missing, ",") != c.missing || strings.Join(extra, ",") != c.ext {
			t.Errorf("%s: missing %v extra %v", c.name, missing, extra)
		}
	}
}
