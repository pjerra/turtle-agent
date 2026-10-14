package dungeon

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The cadence of the party's attack order (attackAllowed). Group B's tank wipes: after a pull the driver sent `cmd attack <same guid>`
// every second (the threat gate kept a second mob "fresh", so every look sent the order again at the mob it already had), and the tank
// flip-flopped between the mob and "return to pull position".

func partyAttacks(g *fakeRemote, guid string) []time.Time {
	var at []time.Time
	for i, o := range g.orders {
		f := strings.Fields(o)
		if len(f) == 3 && f[0] == "cmd" && f[1] == "attack" && f[2] == guid {
			at = append(at, g.orderAt[i])
		}
	}
	return at
}

// B is held by the tank; A sits on the mage, so its threat gate stays shut for a while and A stays "not attacked yet" on every look.
func TestRemoteAttackOrderIsNotRepeatedEverySecond(t *testing.T) {
	g, a, b, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		a.hold, a.tankPct = gMage, 20
		g.ignorePull = true
		g.killRate = 4 // the fight lasts a while
	}, opts())
	bat := partyAttacks(g, tbm.GUIDArg(b.guid))
	if len(bat) != 1 {
		t.Fatalf("%d attack orders at the mob the tank holds, want 1: %v\n%s", len(bat), g.orders, notesOf(rep))
	}
	// The other mob is a new target: it gets its own order once the gate opens, and then only every AssistEvery (the tank does not hold it).
	aat := partyAttacks(g, tbm.GUIDArg(a.guid))
	if len(aat) < 1 {
		t.Fatalf("no attack order at the second mob: %v", g.orders)
	}
	for i := 1; i < len(aat); i++ {
		if gap := aat[i].Sub(aat[i-1]); gap < 10*time.Second {
			t.Fatalf("attack orders at the second mob %v apart: %v", gap, aat)
		}
	}
	if int(rep.Remote.AttackResends) != len(aat)-1 {
		t.Fatalf("resends %d for %d orders at the second mob", rep.Remote.AttackResends, len(aat))
	}
}

// A mob the tank does not hold (it stays on the mage) is ordered again after AssistEvery, never before, and each repeat is counted and noted.
func TestRemoteAttackOrderIsRepeatedOnlyWhenTheTankDoesNotHoldTheMob(t *testing.T) {
	g, _, b, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		b.hold = gMage // the tank never takes it
		g.ignorePull = true
		g.killRate = 2
	}, opts())
	bat := partyAttacks(g, tbm.GUIDArg(b.guid))
	if len(bat) < 2 {
		t.Fatalf("no repeat for a mob the tank does not hold: %d orders", len(bat))
	}
	for i := 1; i < len(bat); i++ {
		if gap := bat[i].Sub(bat[i-1]); gap < 10*time.Second {
			t.Fatalf("attack orders %v apart (want >= AssistEvery, 10 s): %v", gap, bat)
		}
	}
	if rep.Remote.AttackResends != len(bat)-1 || !strings.Contains(notesOf(rep), "sent again") {
		t.Fatalf("resends %d for %d orders\n%s", rep.Remote.AttackResends, len(bat), notesOf(rep))
	}
}

// A refused attack order is not remembered: the next look sends it again.
func TestRemoteAttackOrderRefusedIsSentAgain(t *testing.T) {
	g, _, _, _ := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		g.cmdErr["attack"] = []string{"busy"}
	}, opts())
	var first []string
	for _, o := range g.ordersOf("cmd attack") {
		if f := strings.Fields(o); len(f) == 3 {
			first = append(first, f[2])
		}
	}
	if len(first) < 2 || first[0] != first[1] {
		t.Fatalf("a refused order was not sent again at the same mob: %v", first)
	}
}
