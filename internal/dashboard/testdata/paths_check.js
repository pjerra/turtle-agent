"use strict";
// Run by view_test.go under node: the geometry and wording of the path layers (trails, off-route pieces, pull-group states, objects, aggro rings,
// patrols, combat lines, the ghost and the death heat map).
const assert = require("assert");
const P = require(process.argv[2]);
const close = (a, b, eps = 1e-6) => assert.ok(Math.abs(a - b) <= eps, a + " != " + b);

// ---- colours: role colours, the lead brighter
assert.strictEqual(P.trailColor("tank", false), P.ROLE_COLOR.tank);
assert.strictEqual(P.trailColor("healer", false), P.ROLE_COLOR.healer);
assert.strictEqual(P.trailColor("dps", false), P.ROLE_COLOR.dps);
assert.strictEqual(P.trailColor("", false), P.ROLE_COLOR.dps);
const lum = (hex) => { const n = parseInt(hex.slice(1), 16); return ((n >> 16) & 255) + ((n >> 8) & 255) + (n & 255); };
["tank", "healer", "dps"].forEach((r) => assert.ok(lum(P.trailColor(r, true)) > lum(P.trailColor(r, false)), r + " lead is brighter"));
assert.strictEqual(P.brighten("#000000", 1), "#ffffff");
assert.strictEqual(P.brighten("nope", 0.5), "nope");
assert.strictEqual(P.roleOf({ role: "healer" }), "healer"); assert.strictEqual(P.roleOf({ role: "mage" }), "dps"); assert.strictEqual(P.roleOf(null), "dps");

// ---- the last N minutes of a walk
const pts = [[0, 0], [1, 0], [2, 0], [3, 0], [4, 0]], ts = [0, 60, 120, 180, 240];
let r = P.recent(pts, ts, 240, 2);          // from 120 s on, plus the point before the cut
assert.deepStrictEqual(r.points, [[1, 0], [2, 0], [3, 0], [4, 0]]); assert.deepStrictEqual(r.times, [60, 120, 180, 240]);
assert.deepStrictEqual(P.recent(pts, ts, 240, null).points, pts);        // whole run
assert.deepStrictEqual(P.recent(pts, ts, 240, 100).points, pts);         // longer than the walk
assert.deepStrictEqual(P.recent(pts, [1], 240, 2).points, pts);          // times out of step: whole
assert.deepStrictEqual(P.recent(pts, null, 240, 2).points, pts);
assert.deepStrictEqual(P.recent([], [], 1, 1), { points: [], times: [] });
assert.deepStrictEqual(P.recent(pts, ts, 1000, 1).points, [[4, 0]]);     // everything older: the newest point stays

// ---- planned vs walked
const planned = [[[0, 0], [100, 0]]];
assert.deepStrictEqual(P.offRoute([[0, 2], [100, 3]], planned, 15), []);                     // on the line
let off = P.offRoute([[0, 0], [50, 0], [50, 60], [100, 60], [100, 0]], planned, 15);        // a detour 60 yd north
assert.strictEqual(off.length, 1);
assert.ok(off[0].every((p) => Math.abs(p[1]) > 15));
assert.ok(off[0][0][1] <= 60 && off[0].length >= 2);
assert.deepStrictEqual(P.offRoute([[0, 100], [100, 100]], planned, 15).length, 1);          // far off the whole way
assert.deepStrictEqual(P.offRoute([[0, 0]], planned, 15), []);
assert.deepStrictEqual(P.offRoute([[0, 0], [9, 9]], [], 15), []);                          // nothing planned: nothing to be off
assert.deepStrictEqual(P.offRoute([[0, 100], [100, 100]], [[[0, 0], [100, 0]], [[0, 100], [100, 100]]], 15), []); // near another leg's line
const geo = { legs: [{ path: [{ x: 0, y: 0 }, { x: 10, y: 0 }] }, { path: [{ x: 10, y: 0 }, { x: 10, y: 10 }] }, { path: [{ x: 10, y: 10 }, { x: 99, y: 10 }] }] };
assert.strictEqual(P.plannedLines(geo, 1).length, 2); assert.strictEqual(P.plannedLines(geo, -1).length, 0); assert.deepStrictEqual(P.plannedLines(geo, 0)[0], [[0, 0], [10, 0]]);
close(P.distToLine(5, 3, [[0, 0], [10, 0]]), 3); close(P.distToLine(15, 0, [{ x: 0, y: 0 }, { x: 10, y: 0 }]), 5);
close(P.distToLine(-2, 0, [[0, 0], [10, 0], [10, 10]], true), 2);

// ---- pull-group states
const g2 = { legs: [
  { packs: [{ index: 0, x: 0, y: 0, r: 20, points: [{ x: 0, y: 0 }] }, { index: 1, x: 100, y: 0, r: 20, points: [{ x: 100, y: 0 }] }, { index: 2, x: 200, y: 0, r: 20, points: [{ x: 200, y: 0 }] }] },
  { packs: [{ index: 0, x: 0, y: 500, r: 20, points: [{ x: 0, y: 500 }] }, { index: 1, x: 100, y: 500, r: 20, points: [{ x: 100, y: 500 }] }] },
] };
const stOf = (o, k) => o[k].state;
// no trace: the groups behind the run are cleared, the one in hand is next
let gs = P.groupStates(g2, { leg: 0, group: 2, state: "running" }, [], false);
assert.deepStrictEqual([stOf(gs, "0:0"), stOf(gs, "0:1"), stOf(gs, "0:2"), stOf(gs, "1:0")], ["cleared", "next", "pending", "pending"]);
// with a trace: a group the run passed without a pull is skipped, a won pull is cleared, a failed one is failed with its reason
gs = P.groupStates(g2, { leg: 1, group: 1, state: "running" }, [
  { leg: 0, x: 3, y: 1, outcome: "won" }, { leg: 0, x: 101, y: 0, outcome: "refused" }, { leg: 0, x: 99, y: 2, outcome: "no_engage" }], true);
assert.deepStrictEqual([stOf(gs, "0:0"), stOf(gs, "0:1"), stOf(gs, "0:2"), stOf(gs, "1:0"), stOf(gs, "1:1")], ["cleared", "failed", "skipped", "next", "pending"]);
assert.ok(/no_engage|did not engage/.test(gs["0:1"].text) && /2 tries/.test(gs["0:1"].text), gs["0:1"].text);
assert.ok(/skipped/.test(gs["0:2"].text));
// a pull still going is the next group; a later won pull of a failed group makes it cleared
gs = P.groupStates(g2, { leg: 0, group: 1, state: "running" }, [{ leg: 0, x: 0, y: 0, outcome: "" }], true);
assert.strictEqual(stOf(gs, "0:0"), "next");
gs = P.groupStates(g2, { leg: 0, group: 1, state: "running" }, [{ leg: 0, x: 0, y: 0, outcome: "error" }, { leg: 0, x: 1, y: 0, outcome: "won" }], true);
assert.strictEqual(stOf(gs, "0:0"), "cleared");
// no run: nothing is next; a finished run has nothing pending or next
gs = P.groupStates(g2, { leg: -1, state: "idle" }, [], false);
Object.keys(gs).forEach((k) => assert.strictEqual(gs[k].state, "pending", k));
gs = P.groupStates(g2, { leg: 1, group: 0, state: "done" }, [{ leg: 0, x: 0, y: 0, outcome: "won" }], true);
assert.ok(Object.values(gs).every((x) => x.state !== "next" && x.state !== "pending"));
assert.strictEqual(gs["0:0"].color, P.STATE_COLOR.cleared);
// a pull far from every group is no pull of a group
gs = P.groupStates(g2, { leg: 0, group: 1, state: "running" }, [{ leg: 0, x: 600, y: 600, outcome: "won" }], true);
assert.notStrictEqual(stOf(gs, "0:0"), "cleared");
assert.deepStrictEqual(P.groupStates(null, null, null, false), {});
// exactly one group is next
gs = P.groupStates(g2, { leg: 0, group: 0, state: "running" }, [], true);
assert.strictEqual(Object.values(gs).filter((x) => x.state === "next").length, 1);

// ---- objects
const og = { legs: [{ objects: [{ kind: "use", x: 10, y: 10, step: 0, label: "use 1" }, { kind: "door", x: 50, y: 50, step: 1, label: "wait 2 active" }, { kind: "loot", x: 90, y: 0, step: 0, after: true, label: "loot 3" }] }, { objects: [{ kind: "use", x: 5, y: 5, step: 0, label: "use 4" }] }] };
let os = P.objectStates(og, [{ leg: 0, action: "use_object", x: 11, y: 9, ok: true, step: 0 }], [], { leg: 0, state: "running" });
assert.deepStrictEqual(os.map((o) => o.state), ["used", "pending", "pending", "pending"]);
os = P.objectStates(og, [{ leg: 0, action: "use_object", x: 11, y: 9, ok: true, step: 0 }, { leg: 0, action: "loot_object", x: 90, y: 1, ok: false, step: 0, text: "cmd open refused: in-combat" }], [{ leg: 0, result: "done" }], { leg: 1, state: "running" });
assert.deepStrictEqual(os.map((o) => o.state), ["used", "open", "failed", "pending"]);   // the door opened because the leg is done
assert.ok(/in-combat/.test(os[2].text));
os = P.objectStates(og, [{ leg: 0, action: "loot_object", x: 90, y: 0, ok: true, step: 0 }, { leg: 0, action: "use_object", x: 10, y: 10, ok: true, step: 2 }], [], { leg: 0, state: "running" });
assert.strictEqual(os[2].state, "looted"); assert.strictEqual(os[1].state, "open");        // a later step done: the door must be open
os = P.objectStates(og, [], [], { leg: 1, state: "running" });
assert.deepStrictEqual(os.map((o) => o.state), ["passed", "passed", "passed", "pending"]);
assert.strictEqual(P.objectStates(null, [], [], null).length, 0);

// ---- aggro: 20 yd at equal level, one per level, 5 to 45, 20 without levels
assert.strictEqual(P.aggroRadius(0, 0), 20); assert.strictEqual(P.aggroRadius(40, 0), 20); assert.strictEqual(P.aggroRadius(0, 40), 20);
assert.strictEqual(P.aggroRadius(40, 40), 20); assert.strictEqual(P.aggroRadius(43, 40), 23); assert.strictEqual(P.aggroRadius(38, 40), 18);
assert.strictEqual(P.aggroRadius(70, 40), 45); assert.strictEqual(P.aggroRadius(1, 60), 5);
const mobs = [
  { kind: "hostile", guid: "0x1", x: 0, y: 0, leg: 0, group: 0, hp_pct: 100 }, { kind: "hostile", guid: "0x2", x: 10, y: 0, leg: 0, group: 0, hp_pct: 100 },
  { kind: "hostile", guid: "0x3", x: 100, y: 100, leg: -1, group: -1, hp_pct: 100, level: 45 }, { kind: "neutral", guid: "0x4", x: 1, y: 1 },
  { kind: "hostile", guid: "0x5", x: 5, y: 5, cleared: true }, { kind: "hostile", guid: "0x6", x: 7, y: 7, hp_pct: 0 },
];
let rings = P.packRings(mobs, 40);
assert.strictEqual(rings.length, 2);
assert.strictEqual(rings[0].n, 2); close(rings[0].x, 5); close(rings[0].r, 5 + 20);          // reaches both mobs' 20 yd
assert.strictEqual(rings[1].n, 1); assert.strictEqual(rings[1].r, 25);                       // level 45 against 40
assert.strictEqual(P.packRings(mobs, 0)[1].r, 20);                                           // party level unknown
assert.strictEqual(P.packRings(mobs, 40, 1).length, 1);
assert.deepStrictEqual(P.packRings(null, 40), []);

// ---- patrols
const pat = { path: [{ x: 0, y: 0 }, { x: 100, y: 0 }, { x: 100, y: 100 }], entries: [7] };
let pl = P.patrolLive(pat, [{ entry: 7, x: 50, y: 4 }, { entry: 8, x: 50, y: 0 }, { entry: 7, x: 500, y: 500 }]);
assert.deepStrictEqual([pl.x, pl.y], [50, 4]);
assert.strictEqual(P.patrolLive(pat, [{ entry: 8, x: 50, y: 0 }]), null);                    // other creatures are not the patrol
assert.strictEqual(P.patrolLive(pat, [{ entry: 7, x: 500, y: 500 }]), null);                  // too far from the route
assert.ok(P.patrolLive(pat, [{ entry: 7, x: 40, y: 60 }]) !== null);                          // the way back from the last point to the first counts
assert.strictEqual(P.patrolLive({ path: [{ x: 0, y: 0 }] }, []), null); assert.strictEqual(P.patrolLive(null, []), null);

// ---- combat lines
const bots = [
  { name: "Heal", guid: "0xA", role: "healer", known: true, alive: true, x: 0, y: 0, target_guid: "0x0000000B" },
  { name: "Tank", guid: "0xB", role: "tank", known: true, alive: true, x: 10, y: 0 },
  { name: "Mage", guid: "0xC", role: "dps", known: true, alive: true, x: -5, y: 5 },
  { name: "Dead", guid: "0xD", role: "healer", known: true, alive: false, x: 1, y: 1, target_guid: "0xB" },
  { name: "Self", guid: "0xE", role: "healer", known: true, alive: true, x: 2, y: 2, target_guid: "0xE" },
];
let hl = P.healLines(bots);
assert.strictEqual(hl.length, 1); assert.deepStrictEqual([hl[0].from, hl[0].to], [{ x: 0, y: 0 }, { x: 10, y: 0 }]);   // guids compare without case and zeros; the dead and the self-healer give none
const cmobs = [{ guid: "0xF130001C4E025FEA", x: 30, y: 30, cc: true }, { guid: "0xF1300000000000AA", x: 40, y: 40, cc: false }];
let cc = P.ccLines([{ text: "cmd cc moon 0xF130001C4E025FEA Mage", reply: "ACK" }, { text: "cmd cc moon 0xF1300000000000AA Mage", reply: "ACK" }, { text: "cmd cc moon 0xF130001C4E025FEA Mage", reply: "ERR" }], bots, cmobs);
assert.strictEqual(cc.length, 1); assert.deepStrictEqual([cc[0].from, cc[0].to], [{ x: -5, y: 5 }, { x: 30, y: 30 }]); assert.strictEqual(cc[0].kind, "moon");
assert.deepStrictEqual(P.ccLines([{ text: "cmd cc moon 0xF130001C4E025FEA Nobody", reply: "ACK" }], bots, cmobs), []);   // an unknown caster
assert.deepStrictEqual(P.ccLines([{ text: "cmd attack 0x1", reply: "ACK" }], bots, cmobs), []);
assert.deepStrictEqual(P.ccLines(null, bots, cmobs), []);

// ---- the ghost
const gpath = [[0, 0, 0], [100, 0, 100], [200, 0, 200], [300, 0, 300]];
let at = P.ghostAt(gpath, 150); close(at.x, 150); close(at.y, 0);
assert.deepStrictEqual(P.ghostAt(gpath, -5), { x: 0, y: 0 }); assert.deepStrictEqual(P.ghostAt(gpath, 999), { x: 300, y: 0 });
assert.strictEqual(P.ghostAt([], 5), null); assert.strictEqual(P.ghostAt(gpath, NaN), null);
const ghost = { found: true, path: gpath, legs: [{ leg: 0, start: 0, end: 150 }, { leg: 1, start: 150, end: 300 }], seconds: 300 };
// the lead stands where the ghost was at 200 s; the run's clock says 260 s: the ghost is 60 s ahead
let cmp = P.ghostCompare(ghost, { leg: 1 }, { x: 200, y: 3 }, 260);
close(cmp.delta, 60); close(cmp.ghostT, 200);
assert.strictEqual(P.ghostText(cmp), "ghost is 1.0 min ahead");
cmp = P.ghostCompare(ghost, { leg: 1 }, { x: 200, y: 3 }, 170);
assert.strictEqual(P.ghostText(cmp), "ghost is 0.5 min behind");
assert.strictEqual(P.ghostText(P.ghostCompare(ghost, { leg: 1 }, { x: 200, y: 3 }, 203)), "level with the ghost");
// only the ghost's own pass through the same section counts, and only near its route
cmp = P.ghostCompare(ghost, { leg: 0 }, { x: 200, y: 3 }, 50); assert.ok(cmp.note, "the ghost was not at x=200 during section 0"); assert.strictEqual(P.ghostText(cmp), cmp.note);
assert.ok(P.ghostCompare(ghost, { leg: 5 }, { x: 0, y: 0 }, 50).note);                         // a section the ghost never reached
assert.ok(P.ghostCompare(ghost, { leg: 1 }, { x: 0, y: 900 }, 50).note);                       // off its route
assert.strictEqual(P.ghostCompare(null, { leg: 1 }, { x: 0, y: 0 }, 5), null);
assert.strictEqual(P.ghostCompare({ found: false }, { leg: 1 }, { x: 0, y: 0 }, 5), null);
assert.strictEqual(P.ghostCompare(ghost, { leg: -1 }, { x: 0, y: 0 }, 5), null);
assert.strictEqual(P.ghostCompare(ghost, { leg: 1 }, null, 5), null);
assert.strictEqual(P.ghostText(null), "");
assert.strictEqual(P.clockMin(0), "0:00"); assert.strictEqual(P.clockMin(2467), "41:07");
assert.ok(/fastest clear: 41:07 \(f1\)/.test(P.ghostDescribe({ found: true, complete: true, seconds: 2467, folder: "f1" })));
assert.ok(/furthest earlier run \(3 sections\)/.test(P.ghostDescribe({ found: true, complete: false, legs_done: 3, seconds: 90 })));
assert.ok(/furthest earlier run \(3 of 14 sections\): 1:30/.test(P.ghostDescribe({ found: true, complete: false, legs_done: 3, plan_legs: 14, seconds: 90 })));
assert.ok(/furthest earlier run \(4 sections\): 10:00, 2 packs left \(g\)/.test(P.ghostDescribe({ found: true, complete: false, legs_done: 4, seconds: 600, left: 2, folder: "g" })));
assert.strictEqual(P.ghostDescribe({ found: false }), "");

// ---- stalls, refusals, heat
assert.strictEqual(P.stallText({ secs: 51.5, reason: "in a fight" }), "stood still 52 s: in a fight");
assert.ok(/still there/.test(P.stallText({ secs: 40, reason: "x", open: true })));
assert.ok(/^walk refused: unreachable: no path/.test(P.refusalText({ kind: "refused", text: "unreachable: no path" })));
assert.ok(/^stuck: /.test(P.refusalText({ kind: "stuck", text: "Bob stuck" }))); assert.ok(/^unreachable: /.test(P.refusalText({ kind: "unreachable", text: "pull" })));
const hb = P.heatBins([[1, 1, 0], [2, 3, 0], [7.9, 7.9, 1], [9, 1, 0], [NaN, 1, 0]], 8);
assert.strictEqual(hb.max, 3); assert.strictEqual(hb.bins.length, 2);
assert.deepStrictEqual(hb.bins.find((b) => b.n === 3), { x: 4, y: 4, n: 3 });
assert.deepStrictEqual(P.heatBins([], 8), { bins: [], max: 0 }); assert.deepStrictEqual(P.heatBins(null), { bins: [], max: 0 });
const neg = P.heatBins([[-1, -1, 0], [-7, -7, 0]], 8); assert.strictEqual(neg.bins.length, 1); assert.strictEqual(neg.bins[0].n, 2); // negative coordinates share the cell below zero
console.log("paths.js ok");
