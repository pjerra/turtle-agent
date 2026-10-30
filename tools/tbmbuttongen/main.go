// Command tbmbuttongen writes internal/tbm/buttons_gen.go from the TortoiseBotsManager addon
// source: every button, the command it sends, how it is aimed and what reply the addon
// waits for. It reads the addon's Lua (Constants.lua, UI.lua, MiniBar.lua, Comms.lua, Core.lua,
// BotPanel.lua), takes labels, intents, toggles, behaviours, marks and formations from it, and
// fails when a command it lists is no longer in the source (Proof), so an addon change cannot
// pass unnoticed. Run it on a clean checkout and give the commit:
//
//	git clone https://github.com/Sagiroth/TortoiseBotsManager /tmp/tbm && git -C /tmp/tbm checkout <sha>
//	go run ./tools/tbmbuttongen -in /tmp/tbm -out internal/tbm/buttons_gen.go
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

func main() {
	in := flag.String("in", "", "TortoiseBotsManager checkout")
	out := flag.String("out", "", "buttons_gen.go to write")
	commit := flag.String("commit", "", "addon commit (default: git rev-parse HEAD in -in)")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: tbmbuttongen -in <addon checkout> -out internal/tbm/buttons_gen.go [-commit sha]")
		os.Exit(2)
	}
	sha := *commit
	if sha == "" {
		b, err := exec.Command("git", "-C", *in, "rev-parse", "HEAD").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "tbmbuttongen: no -commit and git rev-parse failed:", err)
			os.Exit(1)
		}
		sha = strings.TrimSpace(string(b))
	}
	src, err := Load(*in)
	if err == nil {
		var b []byte
		if b, err = Generate(src, sha); err == nil {
			err = os.WriteFile(*out, b, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tbmbuttongen:", err)
		os.Exit(1)
	}
}

// Sources are the addon files the generator reads, by base name.
type Sources map[string]string

var files = []string{"Constants.lua", "UI.lua", "MiniBar.lua", "Comms.lua", "Core.lua", "BotPanel.lua"}

// Load reads the addon files from a checkout.
func Load(dir string) (Sources, error) {
	s := Sources{}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		s[f] = string(b)
	}
	return s, nil
}

// button mirrors tbm.Button for the generated literal.
type button struct {
	Name, Label, Tab, Command, BotCommand string
	Args, Optional                        []string
	Scope, Toggle                         string
	NeedsEnemy                            bool
	RequiresCap                           string
	Multi                                 bool
	PullSeconds                           int
	Reply, Intent                         string
	Ok, Fail                              []string
	FollowUp, MiniBar                     bool
	Source                                string
}

var (
	reLabel      = regexp.MustCompile(`(?m)^\s*(?:\["([^"]+)"\]|(\w+))\s*=\s*"([^"]*)",`)
	reToggle     = regexp.MustCompile(`\{\s*key\s*=\s*"(\w+)",\s*intent\s*=\s*"([^"]+)",\s*label\s*=\s*"([^"]+)"`)
	reBehave     = regexp.MustCompile(`\{\s*key\s*=\s*"(\w+)",\s*label\s*=\s*"([^"]+)"(?:[^}]*mana\s*=\s*(true))?`)
	reID         = regexp.MustCompile(`\{\s*id\s*=\s*"(\w+)",\s*label\s*=\s*"([^"]+)"`)
	reAction     = regexp.MustCompile(`buttons\.\w+\s*=\s*makeActionButton\((\w+),\s*"([^"]+)"`)
	reRaidBtn    = regexp.MustCompile(`button\((\w+),\s*"([^"]+)",\s*\d+,\s*-?\d+,\s*-?\d+,\s*(?:"([^"]+)"|nil)`)
	reLife       = regexp.MustCompile(`button\("(\w+)",\s*\d+,\s*"(\w+)",\s*"(\w+)"`)
	reMini       = regexp.MustCompile(`\{\s*intent\s*=\s*"([^"]+)"`)
	reMiniCap    = regexp.MustCompile(`\{\s*intent\s*=\s*"([^"]+)"[^}]*\bcap\s*=\s*"([^"]+)"`)
	reTargetOnly = regexp.MustCompile(`local targetOnly = \{([^}]*)\}`)
	reQuoted     = regexp.MustCompile(`"([^"]+)"`)
	reRaidWord   = regexp.MustCompile(`\bbutton\(`)
	reMakeAction = regexp.MustCompile(`makeActionButton\(`)
	reNumber     = regexp.MustCompile(`TB\.C\.(\w+)\s*=\s*([0-9.]+)`)
)

// section returns the text of src from the first occurrence of from to the next occurrence of
// to (or the end).
func section(src, from, to string) string {
	i := strings.Index(src, from)
	if i < 0 {
		return ""
	}
	src = src[i:]
	if j := strings.Index(src[len(from):], to); to != "" && j >= 0 {
		return src[:len(from)+j]
	}
	return src
}

// entries are the table entries of a Lua section (its text starts at the opening brace); the
// generator fails when a regular expression found fewer than the section holds, so a field
// reordered in the addon cannot drop a button without notice.
func entries(name, sec string, found int) error {
	if sec == "" {
		return fmt.Errorf("%s: section not found", name)
	}
	if n := strings.Count(sec, "{") - 1; n != found {
		return fmt.Errorf("%s: %d entries in the addon source, the generator read %d: update the generator for the addon change", name, n, found)
	}
	return nil
}

// camel names a button part from its label: "Come & Hold" -> "ComeHold", "AoE" -> "AoE".
func camel(label string) string {
	var b strings.Builder
	up := true
	for _, r := range label {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			up = true
			continue
		}
		if up {
			r = unicode.ToUpper(r)
			up = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// proofs are the lines of addon source each hand-listed command comes from; Generate fails
// when one is gone.
type proof struct{ file, text string }

func check(src Sources, ps ...proof) error {
	for _, p := range ps {
		if !strings.Contains(src[p.file], p.text) {
			return fmt.Errorf("%s no longer contains %q: update the generator for the addon change", p.file, p.text)
		}
	}
	return nil
}

// Generate builds the table from the addon sources and returns the Go file.
func Generate(src Sources, commit string) ([]byte, error) {
	k := src["Constants.lua"]
	labels := map[string]string{}
	for _, m := range reLabel.FindAllStringSubmatch(section(k, "TB.C.ACTION_LABELS = {", "\n}"), -1) {
		key := m[1]
		if key == "" {
			key = m[2]
		}
		labels[key] = m[3]
	}
	nums := map[string]float64{}
	for _, m := range reNumber.FindAllStringSubmatch(k, -1) {
		nums[m[1]], _ = strconv.ParseFloat(m[2], 64)
	}
	need := func(name string) (float64, error) {
		v, ok := nums[name]
		if !ok {
			return 0, fmt.Errorf("the Constants.lua has no TB.C.%s", name)
		}
		return v, nil
	}
	pullDelay, err := need("PULL_DELAY_DEFAULT")
	if err != nil {
		return nil, err
	}
	pullbackDelay, err := need("PULLBACK_DELAY_DEFAULT")
	if err != nil {
		return nil, err
	}
	sendDelay, err := need("SEND_DELAY")
	if err != nil {
		return nil, err
	}
	pollAfter, err := need("POLL_AFTER_CMD")
	if err != nil {
		return nil, err
	}
	actionTimeout, err := need("ACTION_TIMEOUT")
	if err != nil {
		return nil, err
	}
	panelPoll, err := need("POLL_PANEL_IV")
	if err != nil {
		return nil, err
	}
	rosterThrottle, err := need("LIST_THROTTLE")
	if err != nil {
		return nil, err
	}
	if err := check(src,
		proof{"Constants.lua", `TB.C.ADDON_PREFIX    = "TBM"`},
		proof{"Constants.lua", `TB.C.ADDON_TRANSPORT = "PARTY"`},
		proof{"Core.lua", `SendAddonMessage((C and C.ADDON_PREFIX) or "TBM", cmd, channel)`},
		proof{"Core.lua", `SendChatMessage(full)`},
		proof{"Core.lua", `local full = ".bot " .. cmd`},
		proof{"Comms.lua", `if not addonTransportKnown or addonTransport ~= "party" then return nil end`},
	); err != nil {
		return nil, err
	}

	ui := src["UI.lua"]
	cardTab := map[string]string{"cardEngage": "Fight", "cardPull": "Fight", "cardMove": "Fight", "cardSwitch": "Fight", "cardRest": "Camp"}
	mini := map[string]bool{}
	miniSec := section(src["MiniBar.lua"], "local ITEMS = {", "\n}")
	miniItems := reMini.FindAllStringSubmatch(miniSec, -1)
	for _, m := range miniItems {
		mini[m[1]] = true
	}
	if err := entries("MiniBar.lua ITEMS", miniSec, len(miniItems)); err != nil {
		return nil, err
	}
	// Capabilities a button needs (MiniBar cap =, UI.lua RefreshActionControls serverHasCap).
	needCap := map[string]string{}
	for _, m := range reMiniCap.FindAllStringSubmatch(miniSec, -1) {
		needCap[m[1]] = m[2]
	}
	if needCap["flee"] != "flee" {
		return nil, errors.New("MiniBar.lua: the flee item no longer names a capability: update the generator for the addon change")
	}
	if err := check(src, proof{"UI.lua", `if serverHasCap("flee") then TB.actionButtons.flee:Enable() else TB.actionButtons.flee:Disable() end`}); err != nil {
		return nil, err
	}
	// The buttons the addon greys out without an enemy target.
	tm := reTargetOnly.FindStringSubmatch(ui)
	if tm == nil {
		return nil, errors.New("UI.lua: no local targetOnly list: update the generator for the addon change")
	}
	targetOnly := map[string]bool{}
	for _, q := range reQuoted.FindAllStringSubmatch(tm[1], -1) {
		targetOnly[q[1]] = true
	}
	if err := check(src, proof{"UI.lua", `if hasEnemyTarget then TB.actionButtons[key]:Enable() else TB.actionButtons[key]:Disable() end`}); err != nil {
		return nil, err
	}

	var out []button
	add := func(b button) { out = append(out, b) }

	// Fight and Camp action buttons: makeActionButton(card, intent, ...).
	toggles := map[string]bool{}
	var toggleDefs [][]string
	toggleSec := section(k, "TB.C.PARTY_TOGGLES = {", "\n}")
	for _, m := range reToggle.FindAllStringSubmatch(toggleSec, -1) {
		toggles[m[2]] = true
		toggleDefs = append(toggleDefs, m)
	}
	if err := entries("Constants.lua PARTY_TOGGLES", toggleSec, len(toggleDefs)); err != nil {
		return nil, err
	}
	actionBtns := reAction.FindAllStringSubmatch(ui, -1)
	// Every makeActionButton call is read, but the function itself and the PARTY_TOGGLES loop.
	if n := len(reMakeAction.FindAllString(ui, -1)); n != len(actionBtns)+2 {
		return nil, fmt.Errorf("UI.lua: %d makeActionButton calls, the generator read %d: update the generator for the addon change", n-2, len(actionBtns))
	}
	for _, m := range actionBtns {
		tab, ok := cardTab[m[1]]
		intent := m[2]
		if !ok {
			continue
		}
		if intent == "cc moon" { // the CC Mark button opens the marks panel; it sends nothing
			continue
		}
		label := labels[intent]
		if label == "" {
			return nil, fmt.Errorf("the Constants.lua has no ACTION_LABELS entry for %q", intent)
		}
		if intent == "hold" {
			continue
		}
		b := button{Name: tab + "." + camel(label), Label: label, Tab: tab, Command: "action " + intent, Scope: "target",
			Reply: "ack", Intent: intent, MiniBar: mini[intent], Source: "UI.lua makeActionButton"}
		b.NeedsEnemy = targetOnly[intent]
		b.RequiresCap = needCap[intent]
		switch intent {
		case "pull", "pullback":
			b.PullSeconds = int(pullDelay)
			if intent == "pullback" {
				b.PullSeconds = int(pullbackDelay)
			}
			b.Command = "action " + intent + " {seconds?}"
			b.Optional = []string{"seconds"}
		}
		add(b)
	}
	// Party switches are made in a loop over PARTY_TOGGLES.
	if err := check(src,
		proof{"UI.lua", `makeActionButton(cardSwitch, toggle.intent,`},
		proof{"BotPanel.lua", `return TB.SendActionIntent(toggle.intent .. " " .. nextState)`},
		proof{"BotPanel.lua", `local nextState = TB.GetPartyToggleState(key) == "on" and "off" or "on"`},
	); err != nil {
		return nil, err
	}
	for _, t := range toggleDefs {
		key, intent, label := t[1], t[2], t[3]
		add(button{Name: "Fight." + camel(label), Label: label, Tab: "Fight", Command: "action " + intent + " {state}",
			Optional: []string{"state"}, Scope: "target", Toggle: key, Reply: "ack", Intent: intent,
			MiniBar: mini[intent], Source: "BotPanel.lua SendPartyToggle"})
	}
	for _, want := range []string{"attack", "stop", "interrupt", "flee", "pull", "pullback", "focus skull", "follow", "stay", "come", "rest", "ready", "repair", "sell", "learn", "release"} {
		if !hasButton(out, "action "+want) {
			return nil, fmt.Errorf("UI.lua: no makeActionButton for %q", want)
		}
	}
	for it := range targetOnly {
		if !hasButton(out, "action "+it) {
			return nil, fmt.Errorf("UI.lua: targetOnly names %q, which is not a Fight button", it)
		}
	}
	for it := range mini {
		if !hasButton(out, "action "+it) {
			return nil, fmt.Errorf("MiniBar.lua: %q is not a Fight button", it)
		}
	}
	// Release turns into Corpse run while party members are ghosts.
	if err := check(src, proof{"UI.lua", `if ghost then return "corpse run" end`}); err != nil {
		return nil, err
	}
	add(button{Name: "Camp.CorpseRun", Label: labels["corpse run"], Tab: "Camp", Command: "action corpse run", Scope: "target",
		Reply: "ack", Intent: "corpse run", Source: "UI.lua DeathIntent (the Release button while ghosts are in the party)"})

	// Marks panel.
	if err := check(src,
		proof{"UI.lua", `TB.SendActionIntent("cc " .. markId .. " " .. cands[idx])`},
		proof{"UI.lua", `TB.SendActionIntent("cc clear " .. owner)`},
		proof{"UI.lua", `TB.SendActionIntent("cc clear")`},
	); err != nil {
		return nil, err
	}
	var marks []string
	markSec := section(k, "TB.C.CC_MARKS = {", "\n}")
	for _, m := range reID.FindAllStringSubmatch(markSec, -1) {
		marks = append(marks, m[1])
	}
	if err := entries("Constants.lua CC_MARKS", markSec, len(marks)); err != nil {
		return nil, err
	}
	add(button{Name: "Fight.CCAssign", Label: "Next (CC marks panel)", Tab: "Fight", Command: "action cc {mark} {bot}",
		Args: []string{"mark", "bot"}, Scope: "bot", Reply: "ack", Intent: "cc {mark} {bot}", FollowUp: true,
		Source: "UI.lua marks panel cycler; mark is one of " + strings.Join(marks, ", ")})
	add(button{Name: "Fight.CCClear", Label: "X (CC marks panel)", Tab: "Fight", Command: "action cc clear {bot}",
		Args: []string{"bot"}, Scope: "bot", Reply: "ack", Intent: "cc clear {bot}", FollowUp: true, Source: "UI.lua marks panel clear"})
	add(button{Name: "Fight.CCClearAll", Label: "Clear all", Tab: "Fight", Command: "action cc clear", Scope: "target",
		Reply: "ack", Intent: "cc clear", FollowUp: true, Source: "UI.lua marks panel clear all"})

	// Formation pills.
	if err := check(src,
		proof{"Core.lua", `TB.SendBotCommand("formation " .. botName .. " " .. formationId)`},
		proof{"Core.lua", `TB.SendBotCommand("formation " .. formationId)`},
		proof{"UI.lua", `TB.SetFormation(id)`},
		proof{"Comms.lua", `formationSet   = "formation set to"`},
	); err != nil {
		return nil, err
	}
	formSec := section(k, "TB.C.FORMATIONS = {", "\n}")
	forms := reID.FindAllStringSubmatch(formSec, -1)
	if err := entries("Constants.lua FORMATIONS", formSec, len(forms)); err != nil {
		return nil, err
	}
	for _, m := range forms {
		add(button{Name: "Camp.Formation." + camel(m[2]), Label: m[2], Tab: "Camp", Command: "formation " + m[1],
			BotCommand: "formation {bot} " + m[1], Scope: "formation", Reply: "text",
			Ok: []string{`^(Bot \S+|Party) formation set to `}, Fail: failGeneric, FollowUp: true, Source: "Core.lua SetFormation"})
	}

	// Raid tab.
	raid := section(ui, "ENCOUNTER HELPERS", "")
	raidBtns := reRaidBtn.FindAllStringSubmatch(raid, -1)
	if n := len(reRaidWord.FindAllString(raid, -1)); n != len(raidBtns) {
		return nil, fmt.Errorf("UI.lua raid tab: %d button calls, the generator read %d: update the generator for the addon change", n, len(raidBtns))
	}
	for _, m := range raidBtns {
		if m[3] == "" {
			continue
		}
		label := m[2]
		name := camel(label)
		if label == "Check" { // next to the Custom tactics switch
			name = "CustomCheck"
		}
		add(button{Name: "Raid." + name, Label: label, Tab: "Raid", Command: "action " + m[3], Scope: "target",
			Reply: "ack", Intent: m[3], Multi: m[3] == "raid custom status", Source: "UI.lua raid tab"})
	}
	// "raid status" and "raid custom status" are answered per bot (the addon keeps one entry per
	// bot) and nothing marks the last one.
	if err := check(src,
		proof{"UI.lua", `local raidStatus = {}     -- [bot] = "molten core,onyxia" | "outdoor"`},
		proof{"UI.lua", `local raidCustom = {}     -- [bot] = "on" | "off"`},
	); err != nil {
		return nil, err
	}
	if err := check(src,
		proof{"UI.lua", `TB.SendActionIntent(state == "on" and "raid custom off" or "raid custom on")`},
		proof{"UI.lua", `TB.SendActionIntent("raid status")`},
	); err != nil {
		return nil, err
	}
	add(button{Name: "Raid.CustomToggle", Label: "Custom tactics", Tab: "Raid", Command: "action raid custom {state}",
		Args: []string{"state"}, Scope: "target", Reply: "ack", Intent: "raid custom {state}",
		Source: "UI.lua raid tab (state: the addon flips the last custom-tactics ACK)"})
	add(button{Name: "Raid.CheckBots", Label: "Check bots", Tab: "Raid", Command: "action raid status", Scope: "target",
		Reply: "ack", Intent: "raid status", Multi: true, Source: "UI.lua raid tab"})
	for _, name := range []string{"Raid.TankFaceAway", "Raid.DouseRunes", "Raid.CustomCheck"} {
		if !hasName(out, name) {
			return nil, fmt.Errorf("UI.lua: raid button %s not found", name)
		}
	}

	// Roster and Guild lifecycle buttons.
	// Patterns are anchored to the start of the line and name the bot ({name}), so another
	// request's reply does not settle this one. The wording is the module's (BotCommands.cpp at
	// TortoiseBots 18d4d14), the pieces the addon matches (Comms.lua PAT) are proven below.
	texts := map[string][2][]string{
		"add":      {{`^Bot {name} queued for login`}, {`^Failed to add bot {name}`, `^Character '{name}' not found`, `^Character '{name}' is already online and cannot be claimed`, `^You may only control characters on your account`}},
		"logout":   {{`^Bot {name} logout requested`, `^Hire ended for {name}`}, {`^Bot {name} could not be logged out`, `^Character '{name}' not found`, `^Usage: \.bot logout`, `^You may only control characters on your account`}},
		"invite":   {{`^Invitation sent to bot {name}`}, {`^The group invitation for {name} was rejected by the native group handler`}},
		"uninvite": {{`^Uninvite sent for bot {name}`}, nil},
		"summon":   {{`^Summoning {name} to a safe position near you`}, {`^Failed to summon bot '{name}'`, `^Bot '{name}' (is not in world|is dead and cannot be summoned|is already teleporting|is on a taxi|is in combat and cannot be summoned|could not be assigned to you for summon)`, `already being summoned`, `restricted to GameMasters`}},
	}
	for _, p := range []proof{
		{"Comms.lua", `queued        = "queued for login"`}, {"Comms.lua", `inviteSent    = "Invitation sent to bot"`},
		{"Comms.lua", `uninviteSent  = "Uninvite sent for bot"`}, {"Comms.lua", `summoning     = "Summoning (.+) to a safe position near you"`},
		{"Comms.lua", `addFailed     = "Failed to add bot"`}, {"Comms.lua", `summonFailed  = "Failed to summon bot"`},
		{"Core.lua", `batch.command = TB.BuildCommand(batch.verb, nextName)`}, {"UI.lua", `TB.SendBotCommand(TB.BuildCommand(verb, name))`},
		{"UI.lua", `for _, n in ipairs(names) do TB.SendBotCommand("invite " .. n) end`},
		{"UI.lua", `for _, n in ipairs(names) do TB.SendBotCommand("summon " .. n) end`},
	} {
		if err := check(src, p); err != nil {
			return nil, err
		}
	}
	seenLife := 0
	for _, m := range reLife.FindAllStringSubmatch(section(ui, "CreateRosterBar = function", "TB.lifecycleButtons"), -1) {
		label, verb := m[1], m[3]
		t, ok := texts[verb]
		if !ok {
			return nil, fmt.Errorf("UI.lua: roster button %s sends unknown verb %q", label, verb)
		}
		add(button{Name: "Roster." + camel(label), Label: label, Tab: "Roster", Command: verb + " {name}", Args: []string{"name"},
			Scope: "none", Reply: "text", Ok: t[0], Fail: append(append([]string{}, t[1]...), failGeneric...), FollowUp: true,
			Source: "UI.lua roster bar"})
		seenLife++
	}
	if seenLife != 5 {
		return nil, fmt.Errorf("UI.lua: found %d roster lifecycle buttons, want 5", seenLife)
	}
	for _, g := range [][2]string{{"Invite", "invite"}, {"Summon", "summon"}} {
		t := texts[g[1]]
		add(button{Name: "Guild." + g[0], Label: g[0] + " to Raid", Tab: "Guild", Command: g[1] + " {name}", Args: []string{"name"},
			Scope: "none", Reply: "text", Ok: t[0], Fail: append(append([]string{}, t[1]...), failGeneric...), FollowUp: true,
			Source: "UI.lua guild bar"})
	}
	if err := check(src, proof{"Core.lua", `local sent = TB.SendBotCommand("roster")`}, proof{"UI.lua", `TB.PollList(true)`}); err != nil {
		return nil, err
	}
	add(button{Name: "Roster.Refresh", Label: "Refresh", Tab: "Roster", Command: "roster", Scope: "none", Reply: "snapshot",
		Source: "Core.lua PollList (roster tab, tab switches and the 8 s poll)"})

	// Party tab roles.
	if err := check(src,
		proof{"UI.lua", `TB.SendBotCommand("role " .. capturedName .. " tank")`},
		proof{"UI.lua", `TB.SendBotCommand("role " .. capturedName .. " clear")`},
		proof{"UI.lua", `TB.SendBotCommand("command " .. capturedName .. " " .. capturedRole.strat)`},
		proof{"UI.lua", `TB.SendBotCommand("role self " .. playerRoleToken)`},
	); err != nil {
		return nil, err
	}
	add(button{Name: "Party.RoleTank", Label: "Tank", Tab: "Party", Command: "role {bot} tank", Args: []string{"bot"}, Scope: "bot",
		Reply: "text", Ok: []string{`^Bot {bot} role set to `}, Fail: roleFail, FollowUp: true, Source: "UI.lua party tab role cycler"})
	add(button{Name: "Party.RoleClear", Label: "Clear tank role", Tab: "Party", Command: "role {bot} clear", Args: []string{"bot"}, Scope: "bot",
		Reply: "text", Ok: []string{`^Bot {bot} role set to `}, Fail: roleFail, FollowUp: true, Source: "UI.lua party tab role cycler (leaving Tank)"})
	add(button{Name: "Party.RoleSpec", Label: "Spec role", Tab: "Party", Command: "command {bot} {strategy}", Args: []string{"bot", "strategy"},
		Scope: "bot", Reply: "text", Ok: []string{`^Forwarded command for {bot}: `}, Fail: append([]string{`^You may only command an online bot on your account`, `^Usage: \.bot command`}, roleFail...), FollowUp: true,
		Source: "UI.lua party tab role cycler; strategy is a CLASS_ROLES strat such as +arms,-protection"})
	add(button{Name: "Party.RoleSelf", Label: "Your role", Tab: "Party", Command: "role self {role}", Args: []string{"role"}, Scope: "none",
		Reply: "text", Ok: []string{`^Your role set to `}, Fail: append([]string{`^Usage: \.bot role`}, failGeneric...), FollowUp: true, Source: "UI.lua party tab (role: tank, healer or dps)"})

	// Bot panel.
	if err := check(src,
		proof{"BotPanel.lua", `return TB.SendBotCommand("inv " .. name)`},
		proof{"BotPanel.lua", `return TB.SendBotCommand("behavior " .. name .. " " .. key .. " " .. nextState)`},
		proof{"BotPanel.lua", `local nextState = st[key] == "on" and "off" or "on"`},
		proof{"BotPanel.lua", `local cmd = "item " .. currentBot .. " " .. op`},
		proof{"BotPanel.lua", `cmd = cmd .. " " .. bag .. " " .. slot`},
		proof{"Comms.lua", `return verb == "inv" or verb == "item" or verb == "behavior"`},
		proof{"BotPanel.lua", `TB.SendItemOrder("trade")`},
		proof{"BotPanel.lua", `TB.SendItemOrder("give", entry.bag, entry.slot)`},
		proof{"BotPanel.lua", `self.first.op = "unequip"`},
		proof{"BotPanel.lua", `self.first.op = "equip"`},
	); err != nil {
		return nil, err
	}
	add(button{Name: "BotPanel.Inventory", Label: "Gear & bags", Tab: "BotPanel", Command: "inv {bot}", Args: []string{"bot"}, Scope: "bot",
		Reply: "inventory", Intent: "inv", Source: "BotPanel.lua RequestInventory"})
	behaveSec := section(k, "TB.C.BEHAVIORS = {", "\n}")
	behaviors := reBehave.FindAllStringSubmatch(behaveSec, -1)
	if err := entries("Constants.lua BEHAVIORS", behaveSec, len(behaviors)); err != nil {
		return nil, err
	}
	for _, m := range behaviors {
		note := "BotPanel.lua SendBehavior"
		if m[3] == "true" {
			note += " (mana classes only)"
		}
		add(button{Name: "BotPanel.Behavior." + camel(m[2]), Label: m[2], Tab: "BotPanel",
			Command: "behavior {bot} " + m[1] + " {state}", Args: []string{"bot"}, Optional: []string{"state"}, Scope: "bot",
			Toggle: m[1], Reply: "ack", Intent: "behavior " + m[1], Source: note})
	}
	for _, op := range []struct {
		name, op, cmd string
		args          []string
	}{
		{"Equip", "equip", "item {bot} equip {bag} {slot}", []string{"bot", "bag", "slot"}},
		{"Unequip", "unequip", "item {bot} unequip {bag} {slot}", []string{"bot", "bag", "slot"}},
		{"Give", "give", "item {bot} give {bag} {slot}", []string{"bot", "bag", "slot"}},
		{"Trade", "trade", "item {bot} trade", []string{"bot"}},
	} {
		add(button{Name: "BotPanel.Item." + op.name, Label: labels["item "+op.op], Tab: "BotPanel", Command: op.cmd, Args: op.args, Scope: "bot",
			Reply: "ack", Intent: "item " + op.op, Source: "BotPanel.lua SendItemOrder"})
	}

	seen := map[string]bool{}
	for _, b := range out {
		if seen[b.Name] {
			return nil, fmt.Errorf("duplicate button name %s", b.Name)
		}
		seen[b.Name] = true
	}
	return render(out, commit, sendDelay, pollAfter, actionTimeout, panelPoll, rosterThrottle)
}

// failGeneric are the replies that fail any command: PAT.botNotFound, noAI, unknownCommand.
var failGeneric = []string{`^Bot '[^']+' not found`, `^Bot \S+ has no AI yet`, `^Unknown bot command`, `^Unknown command`}

var roleFail = append([]string{`^Bot {bot} has no AI yet`, `^You may only control an online bot on your account`, `^Usage: \.bot role`}, failGeneric...)

func hasButton(bs []button, command string) bool {
	for _, b := range bs {
		if b.Command == command || strings.HasPrefix(b.Command, command+" ") {
			return true
		}
	}
	return false
}

func hasName(bs []button, name string) bool {
	for _, b := range bs {
		if b.Name == name {
			return true
		}
	}
	return false
}

func strs(v []string) string {
	if len(v) == 0 {
		return "nil"
	}
	q := make([]string, len(v))
	for i, s := range v {
		q[i] = strconv.Quote(s)
	}
	return "[]string{" + strings.Join(q, ", ") + "}"
}

func render(bs []button, commit string, sendDelay, pollAfter, actionTimeout, panelPoll, rosterThrottle float64) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by tools/tbmbuttongen from TortoiseBotsManager %s; DO NOT EDIT.\n\n", commit)
	b.WriteString("package tbm\n\n")
	fmt.Fprintf(&b, "// AddonCommit is the TortoiseBotsManager commit the table was generated from.\nconst AddonCommit = %q\n\n", commit)
	fmt.Fprintf(&b, "// The addon's timings (Constants.lua SEND_DELAY, POLL_AFTER_CMD, ACTION_TIMEOUT, POLL_PANEL_IV, LIST_THROTTLE), in milliseconds.\n")
	fmt.Fprintf(&b, "const (\n\tSendDelayMS = %d\n\tPollAfterCmdMS = %d\n\tActionTimeoutMS = %d\n\tPanelPollMS = %d\n\tRosterThrottleMS = %d\n)\n\n", int(sendDelay*1000), int(pollAfter*1000), int(actionTimeout*1000), int(panelPoll*1000), int(rosterThrottle*1000))
	b.WriteString("// Buttons lists every addon button that sends a command, in UI order.\nvar Buttons = []Button{\n")
	for _, x := range bs {
		fmt.Fprintf(&b, "\t{Name: %q, Label: %q, Tab: %q, Command: %q", x.Name, x.Label, x.Tab, x.Command)
		if x.BotCommand != "" {
			fmt.Fprintf(&b, ", BotCommand: %q", x.BotCommand)
		}
		if len(x.Args) > 0 {
			fmt.Fprintf(&b, ", Args: %s", strs(x.Args))
		}
		if len(x.Optional) > 0 {
			fmt.Fprintf(&b, ", Optional: %s", strs(x.Optional))
		}
		fmt.Fprintf(&b, ", Scope: %q", x.Scope)
		if x.Toggle != "" {
			fmt.Fprintf(&b, ", Toggle: %q", x.Toggle)
		}
		if x.NeedsEnemy {
			b.WriteString(", NeedsEnemy: true")
		}
		if x.Multi {
			b.WriteString(", Multi: true")
		}
		if x.RequiresCap != "" {
			fmt.Fprintf(&b, ", RequiresCap: %q", x.RequiresCap)
		}
		if x.PullSeconds > 0 {
			fmt.Fprintf(&b, ", PullSeconds: %d", x.PullSeconds)
		}
		fmt.Fprintf(&b, ", Reply: %q", x.Reply)
		if x.Intent != "" {
			fmt.Fprintf(&b, ", Intent: %q", x.Intent)
		}
		if len(x.Ok) > 0 {
			fmt.Fprintf(&b, ", Ok: %s", strs(x.Ok))
		}
		if len(x.Fail) > 0 {
			fmt.Fprintf(&b, ", Fail: %s", strs(x.Fail))
		}
		if x.FollowUp {
			b.WriteString(", FollowUp: true")
		}
		if x.MiniBar {
			b.WriteString(", MiniBar: true")
		}
		fmt.Fprintf(&b, ", Source: %q},\n", x.Source)
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}
