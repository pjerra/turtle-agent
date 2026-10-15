"use strict";
// Run by view_test.go under node: the plain-language formatting.
const assert = require("assert");
const T = require(process.argv[2]);
const names = { "7011": "Stone Steward", "4850": "Cave Lurker" };
const base = () => ({
  character: "Glacienne", label: { plan: "uldaman", folder: "grp-a" },
  run: { state: "running", phase: "pull", text: "leg 5 (sentinel): pull group 4 of 5", leg: 5, leg_name: "sentinel", group: 4, groups: 5, minutes: 18.3 },
  counters: { deaths: 0, wipes: 0, party_downs: 0, threat_overrides: 5, rest_pct: 12.5, rest_gates: 2, pulls: 9, gathers: 0, walk_stops: 1, gm_commands: 2, gm_assists: 1, agent_walks: 0, orders_sent: 30, orders_refused: 2 },
  stall: { seconds: 133, level: "yellow" },
  mobs: [{ guid: "0xF130001B5300AC91", entry: 7011, kind: "hostile", engaged: true, leg: 5, group: 3 }, { guid: "0xF130001B5300AC92", entry: 7011, kind: "hostile", engaged: true, leg: 5, group: 3 },
         { guid: "0x1", entry: 4850, kind: "hostile", engaged: false, leg: 5, group: 2, cleared: true }],
  bots: [],
});

assert.strictEqual(T.pretty("sentinel_room"), "Sentinel room");
assert.strictEqual(T.groupName("grp-a"), "Group A");
assert.strictEqual(T.groupName("night run"), "Night run");
assert.strictEqual(T.clock(133), "2:13");
assert.strictEqual(T.clock(5), "0:05");
assert.strictEqual(T.fmtMin(0.5), "30 s");
assert.strictEqual(T.fmtMin(18.3), "18 min");
assert.strictEqual(T.fmtMin(4.25), "4.3 min");
assert.strictEqual(T.mobName(names, 7011), "Stone Steward");
assert.strictEqual(T.mobName(names, 1234), "creature 1234");
assert.strictEqual(T.entryText(names, "entry 7011"), "Stone Steward");

// the state line, from a fight to a walk
let c = base();
assert.strictEqual(T.stateLine(c, names), "Sentinel · pack 4 of 5 · fighting Stone Steward ×2");
c.mobs.forEach((m) => (m.engaged = false));
assert.strictEqual(T.phaseWord(c), "Pulling");
c = base(); c.run.phase = "walk"; c.run.group = 0; c.run.groups = 0;
assert.strictEqual(T.stateLine(c, names), "Sentinel · walking");
c = base(); c.run.leg = -1; c.run.leg_name = ""; c.run.group = 0; c.run.phase = "prepare"; c.run.text = "prepare hire: Puvur";
assert.strictEqual(T.stateLine(c, names), "Hiring the party");
for (const [ph, text, want] of [["enter", "enter", "Entering the dungeon"], ["rest", "", "Resting"], ["step", "ritual 12", "Ritual at the altar"], ["wait", "wait 1 door", "Waiting for the door"], ["boss", "kill boss 6910", "Pulling the boss"]]) {
  c = base(); c.mobs = []; c.run.phase = ph; c.run.text = text;
  assert.strictEqual(T.phaseWord(c), want, ph);
}
c = base(); c.run.state = "failed"; assert.strictEqual(T.phaseWord(c), "Failed");

// an observed run: the run leader's own words
c = base(); c.run.phase = "leader"; c.run.leader = { state: "running", text: "Run leader: Sentinel · pull 7/41 · pulling" };
assert.strictEqual(T.stateLine(c, names), "Run leader: Sentinel · pull 7/41 · pulling");
assert.strictEqual(T.phaseWord(c), "Run leader");
c.run.leader = { state: "paused", text: "Run leader paused: Sentinel · boss 12/41 · 3 dead" };
assert.strictEqual(T.phaseWord(c), "Run leader paused");
c.run.state = "done"; assert.strictEqual(T.phaseWord(c), "Finished");

// the summary line
c = base(); c.counters.deaths = 1;
assert.strictEqual(T.summaryLine(c), "Uldaman · Group A (Glacienne) · 18 min · 1 death · Sentinel");
c = base(); c.run = { state: "idle", phase: "idle", leg: -1, minutes: 0 }; c.label = {}; 
assert.strictEqual(T.summaryLine(c), "Glacienne");

// the stall timer
assert.strictEqual(T.stallText({ seconds: 133, level: "yellow" }), "Stuck: no progress for 2:13");
assert.strictEqual(T.stallText({ seconds: 12, level: "none" }), "Last progress 0:12 ago");

// counters carry a tooltip each and flag the bad ones
const cl = T.counterList(base());
assert.ok(cl.every((x) => x.label && x.tip));
assert.strictEqual(cl.find((x) => x.label === "Tank lost a mob").value, "5");
assert.strictEqual(cl.find((x) => x.label === "Orders").value, "30 (2 refused)");
assert.ok(cl.find((x) => x.label === "GM help (this run)").hot);

// orders
c = base();
let o = T.orderText({ verb: "attack", text: "cmd attack 0xF130001B5300AC91", reply: "ACK" }, c, names);
assert.strictEqual(o.text, "Attack Stone Steward → OK"); assert.ok(o.ok); assert.strictEqual(o.raw, "cmd attack 0xF130001B5300AC91");
o = T.orderText({ verb: "move", text: "cmd move 1 2 3", reply: "ERR", code: "no-path", message: "no walkable path" }, c, names);
assert.strictEqual(o.text, "Move → refused: no walkable path"); assert.ok(!o.ok);
o = T.orderText({ text: ".bot action pullback" }, c, names);
assert.strictEqual(o.text, "Pull back → sent");
o = T.orderText({ verb: "attack", text: "cmd attack 0xdead", reply: "ACK" }, c, names);
assert.strictEqual(o.text, "Attack a target → OK");

// party rows
const row = T.botRow({ name: "Puvur", class: "priest", role: "healer", lead: false, hp_pct: 30, power_pct: 80, power_type: "mana", target: "entry 7011", dist_tank: 12.4 }, names);
assert.deepStrictEqual(row, { name: "Puvur", kind: "Priest · healer", hp: 30, power: 80, powerLabel: "Mana", target: "Stone Steward", dist: "12 yd from tank" });
assert.strictEqual(T.botRow({ name: "T", class: "warrior", role: "tank", lead: true, hp_pct: 1, power_pct: 1, dist_tank: 0 }, names).dist, "");
console.log("ok");
