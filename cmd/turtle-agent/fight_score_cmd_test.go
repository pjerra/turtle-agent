package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

func scoreLines(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") }

// scoreLine finds the one output line that holds every part.
func scoreLine(t *testing.T, out string, parts ...string) string {
	t.Helper()
	for _, l := range scoreLines(out) {
		ok := true
		for _, p := range parts {
			if !strings.Contains(l, p) {
				ok = false
				break
			}
		}
		if ok {
			return l
		}
	}
	t.Fatalf("no line with %v in\n%s", parts, out)
	return ""
}

// TestFightScoreWalk2Parity reproduces the fight debugger's pinned walk-2 numbers (TortoiseBots commander-fight-debugger.md §2):
// 5-4 main tank Ragemeister cast Sunder Armor 8 times in 39 s (2.0 per 10 s, ok), off-tanks Brun 2x and Irelanthe 3x taunted
// (ok); 5-3 Sunder is 7 in 52 s (1.3 per 10 s, warn) and Irelanthe never taunted (warn). The files are copies of
// ~/TortoiseBots-audit/tests/raid-ony-walk2/combat-fight-5-4.json and -5-3.json.
func TestFightScoreWalk2Parity(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"fight-score", "--mt", "Ragemeister", "--ot", "Brun,Irelanthe", "testdata/fights/combat-fight-5-4.json"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if l := scoreLine(t, out.String(), "mt_sunder", "2.0/10 s"); !strings.Contains(l, "ok") || !strings.Contains(l, "Sunder Armor 8x") {
		t.Fatalf("mt_sunder: %q", l)
	}
	if l := scoreLine(t, out.String(), "ot_taunt", "Brun"); !strings.Contains(l, "ok") || !strings.Contains(l, "2x") || !strings.Contains(l, "Hand of Reckoning 2x") {
		t.Fatalf("ot_taunt Brun: %q", l)
	}
	if l := scoreLine(t, out.String(), "ot_taunt", "Irelanthe"); !strings.Contains(l, "ok") || !strings.Contains(l, "3x") || !strings.Contains(l, "Growl 3x") {
		t.Fatalf("ot_taunt Irelanthe: %q", l)
	}

	out.Reset()
	errOut.Reset()
	code = run(context.Background(), []string{"fight-score", "--mt", "Ragemeister", "--ot", "Brun,Irelanthe", "testdata/fights/combat-fight-5-3.json"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if l := scoreLine(t, out.String(), "mt_sunder", "1.3/10 s"); !strings.Contains(l, "warn") {
		t.Fatalf("mt_sunder: %q", l)
	}
	if l := scoreLine(t, out.String(), "ot_taunt", "Irelanthe"); !strings.Contains(l, "warn") || !strings.Contains(l, "0x") || !strings.Contains(l, "no taunt") {
		t.Fatalf("ot_taunt Irelanthe: %q", l)
	}

	// No argument is a usage error.
	if code := run(context.Background(), []string{"fight-score"}, &out, &errOut); code != exitUsage {
		t.Fatalf("no arguments: exit %d", code)
	}
}

// TestFightScoreWalk2Oracle pins the oracle lines of walk-2 fight 5-4 as the generated per-class oracle produces them (O2; the
// lead read the lines before this pin). The default oracle is the generated KB data, so these lines change only when a recipe
// changes the spec lists. Note the `class-wide` rogue lines: a rogue whose casts name no dps signature is judged against the
// union of every rogue dps spec's lists, so Rupture and Sinister Strike (never for assassination and subtlety) warn for it.
func TestFightScoreWalk2Oracle(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"fight-score", "--mt", "Ragemeister", "--ot", "Brun,Irelanthe", "testdata/fights/combat-fight-5-4.json"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	want := []string{
		"oracle     warn -         Frostbringer (rogue dps): spec assassination (signature Cold Blood 1); unknown (list capped at 8): Envenom, Noxious Assault; never: Sinister Strike; off-list: Cheap Shot",
		"oracle     warn -         Varric (warlock dps): spec affliction (signature Siphon Life 1); missing core: Corruption, Drain Soul; off-list: Health Funnel",
		"oracle     warn -         Plainie (rogue dps): class-wide (no signature cast); never: Rupture, Sinister Strike; off-list: Cheap Shot",
		"oracle     warn -         Runeward (warlock dps): class-wide (no signature cast); never: Immolate",
		"oracle     warn -         Clunk (rogue dps): class-wide (no signature cast); never: Rupture, Backstab, Sinister Strike; off-list: Feint",
		"oracle     warn -         Hela (warrior dps): spec fury (signature Bloodthirst 1); missing core: Heroic Strike",
		"oracle     warn -         Durim (hunter dps): spec beast_mastery (signature Bestial Wrath 1); missing core: Kill Command; off-list: Feign Death, Intimidation",
		"oracle     warn -         Frosthammer (rogue dps): class-wide (no signature cast); never: Rupture, Backstab, Sinister Strike; off-list: Feint",
		"oracle     warn -         Leafinator (druid heal): spec restoration (role heal); missing core: Healing Touch; off-list: Faerie Fire, Rebirth",
		"oracle     warn -         Nathor (paladin heal): spec holy (role heal); missing core: Holy Shock; off-list: Hand of Freedom",
		"oracle     warn -         Sera (paladin heal): spec holy (role heal); missing core: Holy Shock; off-list: Greater Blessing of Light, Greater Blessing of Wisdom",
		"oracle     warn -         Dawnfist (rogue dps): spec assassination (signature Cold Blood 1); unknown (list capped at 8): Noxious Assault; never: Rupture, Backstab, Sinister Strike; off-list: Feint",
		"oracle     warn -         Kitetastic (hunter dps): spec beast_mastery (signature Bestial Wrath 1); missing core: Kill Command; off-list: Intimidation",
		"oracle     warn -         Dawnwalker (warlock dps): spec destruction_shadow (signature Shadowburn 1); missing core: Corruption; off-list: Life Tap",
		"oracle     warn -         Shanky (rogue dps): spec assassination (signature Cold Blood 1); unknown (list capped at 8): Envenom, Slice and Dice, Noxious Assault; never: Rupture, Backstab, Sinister Strike; off-list: Feint, Whipper Root Tuber",
		"oracle     ok   -         15 bot(s) judged ok, 9 not judged",
	}
	var got []string
	for _, l := range scoreLines(out.String()) {
		if strings.HasPrefix(l, "oracle") {
			got = append(got, l)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("oracle lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
