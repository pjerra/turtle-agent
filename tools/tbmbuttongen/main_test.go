package main

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/tbm"
)

func TestCamel(t *testing.T) {
	for in, want := range map[string]string{"AoE": "AoE", "Come & Hold": "ComeHold", "Sell junk": "SellJunk", "Auto CC": "AutoCC", "Gear & bags": "GearBags"} {
		if got := camel(in); got != want {
			t.Errorf("camel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSection(t *testing.T) {
	src := "a = {\n x\n}\nb = {\n y\n}\n"
	if got := section(src, "b = {", "\n}"); !strings.Contains(got, "y") || strings.Contains(got, "x") {
		t.Errorf("%q", got)
	}
	if section(src, "c = {", "\n}") != "" {
		t.Error("missing section is not empty")
	}
}

// A changed addon must fail loudly instead of producing a table that no longer matches it.
func TestGenerateFailsWhenTheAddonMovedOn(t *testing.T) {
	if _, err := Generate(Sources{"Constants.lua": "", "UI.lua": "", "MiniBar.lua": "", "Comms.lua": "", "Core.lua": "", "BotPanel.lua": ""}, "abc"); err == nil {
		t.Fatal("an empty addon generated a table")
	}
	if err := check(Sources{"UI.lua": "x"}, proof{"UI.lua", `TB.SendActionIntent("cc clear")`}); err == nil ||
		!strings.Contains(err.Error(), "update the generator") {
		t.Fatalf("%v", err)
	}
}

// A section whose entries the expressions only partly read (a field reordered in the addon)
// fails instead of dropping buttons.
func TestEntriesCountsWhatTheRegexpsMissed(t *testing.T) {
	sec := "TB.C.X = {\n  { id = \"a\", label = \"A\" },\n  { label = \"B\", id = \"b\" },\n"
	if err := entries("X", sec, 2); err != nil {
		t.Fatal(err)
	}
	if err := entries("X", sec, 1); err == nil || !strings.Contains(err.Error(), "update the generator") {
		t.Fatalf("%v", err)
	}
	if entries("X", "", 0) == nil {
		t.Fatal("a missing section passed")
	}
}

// The committed table is what the generator writes for the pinned addon commit. The addon is
// fetched by tools/tbmcontract/fetch.sh (cached in .cache/, never committed); the test skips
// only when it cannot be had (offline) and TBM_ADDON_DIR is not set.
func TestCommittedTableMatchesTheAddon(t *testing.T) {
	fetch, err := os.ReadFile("../tbmcontract/fetch.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ADDON_COMMIT=([0-9a-f]{40})$`).FindSubmatch(fetch)
	if m == nil || string(m[1]) != tbm.AddonCommit {
		t.Fatalf("tools/tbmcontract/fetch.sh pins %q, internal/tbm/buttons_gen.go was generated from %s", m, tbm.AddonCommit)
	}
	out, err := exec.Command("sh", "../tbmcontract/fetch.sh").Output()
	if err != nil {
		t.Skipf("the pinned addon is not available (offline?): %v", err)
	}
	src, err := Load(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Generate(src, tbm.AddonCommit)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../internal/tbm/buttons_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("internal/tbm/buttons_gen.go differs from the generator output for the pinned addon: run go run ./tools/tbmbuttongen")
	}
}
