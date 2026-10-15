package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func eventCard(t *testing.T, dungeon string, view string, orders []Order) Card {
	t.Helper()
	return BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(dungeon), View: json.RawMessage(view),
		Plan: tinyPlan(t), Trace: &TraceSnapshot{Orders: orders}})
}

func kinds(ev []Event) string {
	var k []string
	for _, e := range ev {
		k = append(k, e.Kind)
	}
	return strings.Join(k, ",")
}

// A death in the report is a pin at the bot's place and a feed line with the report's own time; the same report twice adds nothing.
func TestEventsDeathOnce(t *testing.T) {
	var l EventLog
	c := eventCard(t, dungeonJSON("leg 0 (one): pull group 2 of 3", 0), viewJSON, nil)
	ev := l.Observe(&c, t0)
	if kinds(ev) != "death" || ev[0].Text != "Puvur died" || !ev[0].Pos || ev[0].X != -840 || ev[0].Y != -1030 {
		t.Fatalf("events: %+v", ev)
	}
	if !ev[0].At.Equal(t0) {
		t.Errorf("the death keeps the report's time: %v", ev[0].At)
	}
	if again := l.Observe(&c, t0.Add(2*time.Second)); len(again) != 1 {
		t.Errorf("a second look adds nothing: %+v", again)
	}
}

// A wipe that happens while the dashboard watches is an event; one already in the first report is history and is not.
func TestEventsWipeOnlyWhenSeen(t *testing.T) {
	var l EventLog
	c := eventCard(t, dungeonJSON("x", 0), viewJSON, nil)
	l.Observe(&c, t0)
	c.Counters.Wipes++
	ev := l.Observe(&c, t0.Add(3*time.Second))
	if kinds(ev) != "death,wipe" || ev[1].Text != "The whole party died" || !ev[1].At.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("events: %+v", ev)
	}
	var fresh EventLog
	c2 := eventCard(t, dungeonJSON("x", 0), viewJSON, nil)
	if got := kinds(fresh.Observe(&c2, t0)); got != "death" {
		t.Errorf("the wipe already counted at the start is history: %s", got)
	}
}

// A finished leg with a boss is a boss kill (named from the plan) at the boss's place; a leg without one is a section line with no pin.
func TestEventsBossAndSection(t *testing.T) {
	done := strings.Replace(dungeonJSON("x", 0), `"result":"running"`, `"result":"done"`, 1)
	var l EventLog
	c := eventCard(t, done, viewJSON, nil)
	ev := l.Observe(&c, t0)
	var boss *Event
	for i := range ev {
		if ev[i].Kind == "boss" {
			boss = &ev[i]
		}
	}
	if boss == nil || boss.Text != "boss 6001 killed" || !boss.Pos || boss.X != -900 || boss.Y != -1100 || !boss.At.IsZero() {
		t.Fatalf("boss event: %+v", ev)
	}
	// seen while it happens, the kill carries the time of the poll
	var live EventLog
	running := eventCard(t, dungeonJSON("x", 0), viewJSON, nil)
	live.Observe(&running, t0)
	d := eventCard(t, done, viewJSON, nil)
	ev = live.Observe(&d, t0.Add(4*time.Second))
	if last := ev[len(ev)-1]; last.Kind != "boss" || !last.At.Equal(t0.Add(4*time.Second)) {
		t.Fatalf("live boss kill: %+v", ev)
	}
	if n := len(live.Observe(&d, t0.Add(6*time.Second))); n != len(ev) {
		t.Errorf("the kill is counted once: %d then %d", len(ev), n)
	}
}

// Accepted open, loot and use orders are events once each; refused ones and other verbs are not.
func TestEventsFromOrders(t *testing.T) {
	at := t0.Add(-10 * time.Second)
	orders := []Order{
		{At: at, Verb: "open", Text: "cmd open entry:555", Reply: "ACK"},
		{At: at.Add(time.Second), Verb: "loot", Text: "cmd loot", Reply: "ACK"},
		{At: at.Add(2 * time.Second), Text: "cmd use entry:7", Reply: "ACK"},
		{At: at.Add(3 * time.Second), Verb: "open", Text: "cmd open entry:9", Reply: "ERR"},
		{At: at.Add(4 * time.Second), Verb: "move", Text: "cmd move 1 2 3", Reply: "ACK"},
	}
	var l EventLog
	c := eventCard(t, dungeonJSON("x", 0), viewJSON, orders)
	ev := l.Observe(&c, t0)
	if got := kinds(ev); got != "open,loot,use,death" {
		t.Fatalf("kinds %s: %+v", got, ev)
	}
	if !ev[0].Pos || len(l.Observe(&c, t0)) != 4 {
		t.Errorf("a pin at the party, once each: %+v", ev[0])
	}
}

// A new run starts a new list, and the list is capped.
func TestEventsNewRunAndCap(t *testing.T) {
	var l EventLog
	c := eventCard(t, dungeonJSON("x", 0), viewJSON, nil)
	l.Observe(&c, t0)
	c.Label.RunID = "r-2"
	c.ev.deaths = nil
	c.Counters.Wipes = 0
	if ev := l.Observe(&c, t0); len(ev) != 0 {
		t.Errorf("a new run starts empty: %+v", ev)
	}
	for i := 0; i < EventCap+20; i++ {
		c.ev.deaths = append(c.ev.deaths, evDeath{Name: "B", At: t0.Add(time.Duration(i) * time.Second)})
	}
	if ev := l.Observe(&c, t0); len(ev) != EventCap {
		t.Errorf("capped at %d: %d", EventCap, len(ev))
	}
}

// The cast of a bot and of a creature is the spell's name; an id the table lacks is "spell <id>".
func TestCardCastNames(t *testing.T) {
	view := strings.Replace(viewJSON, `"target":"0xF130001C4E00AC91","dead":false,"state":"c"`, `"target":"0xF130001C4E00AC91","spell":133,"dead":false,"state":"c"`, 1)
	view = strings.Replace(view, `"entry":7001,"x":1002`, `"entry":7001,"spell":4294967,"x":1002`, 1)
	c := eventCard(t, dungeonJSON("x", 0), view, nil)
	if c.Bots[0].Cast != "Fireball" || c.Mobs[0].Cast != "spell 4294967" || c.Bots[1].Cast != "" || c.Mobs[1].Cast != "" {
		t.Fatalf("cast: %q %q", c.Bots[0].Cast, c.Mobs[0].Cast)
	}
	body, _ := json.Marshal(c)
	if !strings.Contains(string(body), `"events":[]`) || !strings.Contains(string(body), `"cast":"Fireball"`) {
		t.Errorf("card json: events list and cast: %s", body)
	}
}
