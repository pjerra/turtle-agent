package main

// The practice run's GM hands are a closed set (owner-approved 2026-11-07): every line is built by
// session.PracticeGMLine, and any other kind is refused before it can reach the session. The RPCs themselves are
// covered by internal/session's practice tests (the selection, the max-health and the alive checks).

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/slots"
)

// TestPracticeGMLinesClosedSet pins every kind's exact text and refuses everything else.
func TestPracticeGMLinesClosedSet(t *testing.T) {
	lines := map[string]string{
		session.PracticeGMKill:    ".die",
		session.PracticeGMEvade:   ".npc evade",
		session.PracticeGMSetHP:   ".modify hp 42000 42000",
		session.PracticeGMRespawn: ".respawn",
		session.PracticeGMRevive:  ".revive Bottle",
	}
	args := map[string]string{
		session.PracticeGMKill: "", session.PracticeGMEvade: "", session.PracticeGMSetHP: "42000",
		session.PracticeGMRespawn: "", session.PracticeGMRevive: "Bottle",
	}
	for kind, want := range lines {
		got, err := session.PracticeGMLine(kind, args[kind])
		if err != nil || got != want {
			t.Fatalf("%s: %q, %v; want %q", kind, got, err, want)
		}
	}
	for _, bad := range []struct{ kind, arg string }{
		{"die", ""}, {"", ""}, {"config", "reload"}, {"set_hp", "0"}, {"set_hp", "-5"}, {"set_hp", "12x"},
		{"revive", "Bot 09"}, {"revive", "../x"}, {"evade", "extra"}, {"respawn", "extra"}, {"kill", "extra"},
	} {
		if line, err := session.PracticeGMLine(bad.kind, bad.arg); err == nil {
			t.Fatalf("%q %q built %q, want a refusal", bad.kind, bad.arg, line)
		}
	}
}

// TestPracticeGMRefusedWithoutAuthorize: an agent that dialled anything but a test server's configured world_addr has
// AllowGMKill false (login.go sets it from gmKillAuthorize), and every practice hand is refused before it sends.
func TestPracticeGMRefusedWithoutAuthorize(t *testing.T) {
	servers := []slots.Server{{Name: "m910q", WorldAddr: "10.1.1.1:8085"}}
	st := config.Settings{ServerName: "m910q"}
	// The authorized address passes, so the refusal below is the address rule and not a blanket false.
	if ok, why := gmKillAllowed(st, "10.1.1.1:8085", servers); !ok {
		t.Fatalf("the configured world_addr was refused: %s", why)
	}
	for _, dialed := range []string{"10.1.1.1:3724", "10.2.2.3:8085", "10.1.1.1:9999", "10.1.1.1"} {
		ok, why := gmKillAllowed(st, dialed, servers)
		if ok {
			t.Fatalf("dialling %s authorized the practice hands", dialed)
		}
		if !strings.Contains(why, "gm_kill refused") {
			t.Fatalf("the refusal does not say why: %q", why)
		}
	}
}
