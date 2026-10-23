package session

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// selRig drives the selection log of a bare session: sends and echoes, no connection.
type selRig struct{ s *Session }

func newSelRig() selRig {
	s := &Session{}
	s.self.GUID = 0x2A
	return selRig{s}
}

func (r selRig) sent(gs ...packet.GUID) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, g := range gs {
		r.s.selSentLocked(g)
	}
}

func (r selRig) echo(v packet.GUID) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.selObservedLocked(v)
}

func (r selRig) neutral() (packet.GUID, bool) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return r.s.selNeutralLocked()
}

const (
	selLead     = packet.GUID(0x23A)
	selStranger = packet.GUID(0x4D2)
	selMob      = packet.GUID(0xF130000000000077)
)

// own makes g the confirmed selection by an echo of the session's own send.
func (r selRig) own(t *testing.T, g packet.GUID) {
	t.Helper()
	r.echo(0x999) // the create block: says nothing about the selection
	r.sent(g)
	r.echo(g)
	if !r.s.SelectionConfirmed(g) {
		t.Fatalf("%v not confirmed by the echo of its own send", g)
	}
}

func TestSelectionLogFreshEchoConfirms(t *testing.T) {
	r := newSelRig()
	r.echo(0) // the create block
	if r.s.SelectionConfirmed(0) {
		t.Fatal("a create block confirmed a selection")
	}
	r.sent(selLead)
	if r.s.SelectionConfirmed(selLead) {
		t.Fatal("confirmed before an echo")
	}
	r.echo(selLead)
	if !r.s.SelectionConfirmed(selLead) {
		t.Fatal("the fresh echo did not confirm")
	}
}

func TestSelectionLogStaleEchoDoesNotConfirm(t *testing.T) {
	r := newSelRig()
	r.own(t, selLead)
	r.sent(selStranger, selLead)
	r.echo(selLead) // may answer the first select, with the stranger's pending
	if r.s.SelectionConfirmed(selLead) {
		t.Fatal("a stale echo confirmed")
	}
	if !r.s.SelectionAmbiguous(selLead) {
		t.Fatal("not ambiguous")
	}
	r.echo(selStranger)
	if r.s.SelectionConfirmed(selLead) || r.s.SelectionConfirmed(selStranger) {
		t.Fatal("the stranger's echo, with the lead sent after it, confirmed")
	}
	r.echo(selLead)
	if !r.s.SelectionConfirmed(selLead) {
		t.Fatal("the lead's own echo did not confirm")
	}
}

// The core sends no update for a select that does not change the field: reselecting the confirmed unit is confirmed with no echo.
func TestSelectionLogUnchangedSelectionNeedsNoEcho(t *testing.T) {
	r := newSelRig()
	r.own(t, selLead)
	for i := 0; i < 5; i++ {
		r.sent(selLead)
	}
	if !r.s.SelectionConfirmed(selLead) || r.s.SelectionAmbiguous(selLead) {
		t.Fatal("the unchanged selection is not confirmed at once")
	}
	if n := len(r.s.selLog); n != 1 {
		t.Fatalf("log grew to %d", n)
	}
}

// Unit::AttackStop writes 0 into the target field (the victim died) without touching the selection the GM commands read: that is not a
// cleared selection, and clearing it now gets no echo (the field does not change), so it must go through a neutral selection.
func TestSelectionLogAttackStopZeroIsNotAClearedSelection(t *testing.T) {
	r := newSelRig()
	r.own(t, selMob)
	r.echo(0) // AttackStop
	if r.s.SelectionConfirmed(0) || r.s.SelectionConfirmed(selMob) {
		t.Fatal("the combat rewrite confirmed a selection")
	}
	if !r.s.SelectionAmbiguous(0) {
		t.Fatal("clearing would wait for an echo that never comes")
	}
	if n, ok := r.neutral(); !ok || n != r.s.self.GUID {
		t.Fatalf("neutral %v %v: 0 is what the field shows, the player itself must be used", n, ok)
	}
	r.sent(0) // without the neutral: the clear is recorded, not deduped into a confirmation
	if r.s.SelectionConfirmed(0) {
		t.Fatal("the clear was confirmed with no echo")
	}
	// The neutral round trip.
	r.sent(r.s.self.GUID)
	r.echo(r.s.self.GUID)
	if !r.s.SelectionConfirmed(r.s.self.GUID) {
		t.Fatal("neutral not confirmed")
	}
	r.sent(0)
	r.echo(0)
	if !r.s.SelectionConfirmed(0) {
		t.Fatal("the clear after the neutral was not confirmed")
	}
}

// Unit::Attack writes the victim into the field: with nothing selected, an attack on a player shows that player without selecting it.
func TestSelectionLogAttackOnAPlayerIsNotItsSelection(t *testing.T) {
	r := newSelRig()
	r.own(t, 0)
	r.echo(selLead) // Attack
	if r.s.SelectionConfirmed(selLead) {
		t.Fatal("the attack confirmed the victim as the selection")
	}
	if !r.s.SelectionAmbiguous(selLead) {
		t.Fatal("selecting the victim would wait for an echo that never comes")
	}
	r.sent(selLead)
	if r.s.SelectionConfirmed(selLead) {
		t.Fatal("confirmed with no echo")
	}
	// A stun end restoring the victim, after a stun cleared the field, is no echo of the select either... unless it is: that is the one
	// case the log cannot tell apart (a rewrite to exactly the unit of a pending select).
	r.echo(0) // stun
	if r.s.SelectionConfirmed(0) {
		t.Fatal("the stun's 0 confirmed a clear")
	}
}

// The log is capped; an echo of a unit whose send was dropped confirms nothing, and the neutral avoids dropped units.
func TestSelectionLogCap(t *testing.T) {
	r := newSelRig()
	r.own(t, selLead)
	r.sent(0) // will be dropped
	for i := 0; i < 2*selCap; i++ {
		r.sent(selStranger+packet.GUID(i%7), selLead)
	}
	if n := len(r.s.selLog); n > selCap {
		t.Fatalf("log at %d", n)
	}
	if r.s.selBaseKnown {
		t.Fatal("the base is known after a drop")
	}
	r.echo(selLead) // may answer a dropped select
	if r.s.SelectionConfirmed(selLead) {
		t.Fatal("an echo of a dropped unit confirmed")
	}
	if n, ok := r.neutral(); !ok || n != r.s.self.GUID {
		t.Fatalf("neutral %v %v: 0 was dropped", n, ok)
	}
	// The neutral round trip resets the log.
	r.sent(r.s.self.GUID)
	r.echo(r.s.self.GUID)
	if !r.s.SelectionConfirmed(r.s.self.GUID) || len(r.s.selLog) != 1 || r.s.selDropped != nil {
		t.Fatalf("log %v dropped %v", r.s.selLog, r.s.selDropped)
	}
}

// A far teleport forgets the selection in force.
func TestSelectionLogNewWorldForgets(t *testing.T) {
	r := newSelRig()
	r.own(t, selLead)
	r.s.mu.Lock()
	r.s.selUnknownLocked()
	r.s.mu.Unlock()
	if r.s.SelectionConfirmed(selLead) || !r.s.SelectionAmbiguous(selLead) {
		t.Fatal("still confirmed after the teleport")
	}
}
