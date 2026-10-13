package dungeon

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// Items implements ItemSource.
func (g *fakeGame) Items() []objects.Item { return append([]objects.Item(nil), g.bag...) }

// runPlanKey is runPlan with the keystone step asking for the Staff of Prehistoria.
var runPlanKey = strings.Replace(runPlan, `{"use_object":{"entry":124371}}`,
	`{"use_object":{"entry":124371,"requires_item":{"entry":7733,"name":"Staff of Prehistoria"}}}`, 1)

func staff() objects.Item { return objects.Item{Bag: 255, Slot: 23, Entry: 7733, Count: 1} }

func TestPlanRequiresItemParses(t *testing.T) {
	p := mustPlan(t, runPlanKey)
	r := p.Legs[0].Steps[0].UseObject.RequiresItem
	if r == nil || r.Entry != 7733 || r.Name != "Staff of Prehistoria" || r.String() != "Staff of Prehistoria (item 7733)" {
		t.Fatalf("%+v", r)
	}
	if (ItemRef{Entry: 5}).String() != "item 5" {
		t.Fatal("no name")
	}
	bad := strings.Replace(runPlan, `{"use_object":{"entry":124371}}`, `{"use_object":{"entry":124371,"requires_item":{"name":"x"}}}`, 1)
	if _, err := Parse("t.json", []byte(bad)); err == nil || !strings.Contains(err.Error(), "legs[0].steps[0].use_object.requires_item.entry") {
		t.Fatalf("%v", err)
	}
	// It reads back from Marshal on one line.
	b, err := Marshal(p)
	if err != nil || !strings.Contains(string(b), `"requires_item": {"entry": 7733, "name": "Staff of Prehistoria"}`) {
		t.Fatalf("%v\n%s", err, b)
	}
	if _, err := Parse("t.json", b); err != nil {
		t.Fatal(err)
	}
}

// A non-GM leader without the key item: the run fails before it moves or says anything, naming the item.
func TestDriverFailsEarlyWithoutTheRequiredItem(t *testing.T) {
	g, _ := runFixture(t)
	p := mustPlan(t, runPlanKey)
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "Staff of Prehistoria (item 7733)") || !strings.Contains(err.Error(), `leg 0 "a" step 0`) {
		t.Fatalf("%v", err)
	}
	if len(g.walks) != 0 || len(g.chats) != 0 || len(g.used) != 0 || d.Report().State != "failed" {
		t.Fatalf("walks %v chats %q used %v state %s", g.walks, g.chats, g.used, d.Report().State)
	}
}

func TestDriverRunsWithTheRequiredItem(t *testing.T) {
	g, _ := runFixture(t)
	g.bag = []objects.Item{{Entry: 6948, Count: 1}, staff()}
	d := NewDriver(mustPlan(t, runPlanKey), g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.used) != 1 {
		t.Fatalf("used %v", g.used)
	}
}

// A leg that starts after the keystone's does not ask for the staff.
func TestDriverChecksOnlyTheLegsItRuns(t *testing.T) {
	g, _ := runFixture(t)
	d := NewDriver(mustPlan(t, runPlanKey), g, Config{GM: GMNone}, g.clock())
	if err := d.checkRequiredItems(1); err != nil {
		t.Fatal(err)
	}
	if err := d.checkRequiredItems(0); err == nil {
		t.Fatal("leg 0 needs the staff")
	}
}

// A GM passes the lock, so a GM leader is not failed (and the default mode takes the leader for a GM).
func TestDriverLetsAGMOpenWithoutTheItem(t *testing.T) {
	g, _ := runFixture(t)
	d := NewDriver(mustPlan(t, runPlanKey), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
}

// Bags whose items have not all arrived (entry 0) are no proof that the item is missing; a Game without an ItemSource is not asked.
func TestDriverDoesNotGuessWhenTheBagsAreNotKnown(t *testing.T) {
	g, _ := runFixture(t)
	g.bag = []objects.Item{{Slot: 23}}
	d := NewDriver(mustPlan(t, runPlanKey), g, Config{GM: GMNone}, g.clock())
	if err := d.checkRequiredItems(0); err != nil {
		t.Fatal(err)
	}
	d2 := NewDriver(mustPlan(t, runPlanKey), noItems{g}, Config{GM: GMNone}, g.clock())
	if err := d2.checkRequiredItems(0); err != nil {
		t.Fatal(err)
	}
}

// noItems hides fakeGame's ItemSource.
type noItems struct{ Game }

// The staff goes missing on the way (sold, destroyed): the check right before the use fails the step.
func TestDriverChecksTheItemAgainBeforeTheUse(t *testing.T) {
	g, _ := runFixture(t)
	g.bag = []objects.Item{staff()}
	g.onWalk = func(g *fakeGame, to Point) { g.bag = nil }
	d := NewDriver(mustPlan(t, runPlanKey), g, Config{GM: GMNone}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "does not carry Staff of Prehistoria") || len(g.used) != 0 {
		t.Fatalf("%v used %v", err, g.used)
	}
}
