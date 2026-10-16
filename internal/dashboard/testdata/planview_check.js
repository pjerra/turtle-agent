"use strict";
// Run by view_test.go under node: the plan view's address, its leg switches and the words about which copy of a plan is drawn.
const assert = require("assert");
const V = require(process.argv[2]);

// ---- the address: only a plausible plan name opens the plan view
assert.deepStrictEqual(V.parseRoute("#/plan/zulfarrak"), { kind: "plan", plan: "zulfarrak" });
assert.deepStrictEqual(V.parseRoute("#/plan/black_morass"), { kind: "plan", plan: "black_morass" });
for (const bad of ["", "#", "#/", "#/plan/", "#/plan/../etc", "#/plan/A", "#/plan/a/b", "#/plan/%2e%2e", "#/plan/a%20b", "#/plan/%E0%A4%A", "#/plan/" + "a".repeat(41), "#/runs", null, undefined]) {
  assert.strictEqual(V.parseRoute(bad).kind, "cards", String(bad));
}
assert.strictEqual(V.planHash("uldaman"), "#/plan/uldaman");
assert.strictEqual(V.planHash(""), "#/");
assert.strictEqual(V.planHash("../x"), "#/");

// ---- which legs are shown
const n = 5;
assert.deepStrictEqual(V.visibleLegs(V.allLegs(), n, -1), [0, 1, 2, 3, 4]);
assert.deepStrictEqual(V.visibleLegs(V.onlyLeg(2, n), n, -1), [2]);
assert.deepStrictEqual(V.visibleLegs(V.hideAll(n), n, -1), []);
// toggling a leg of "all" turns the selection into a custom one
let sel = V.toggleLeg(V.allLegs(), 1, n, -1);
assert.strictEqual(sel.mode, "custom");
assert.deepStrictEqual(V.visibleLegs(sel, n, -1), [0, 2, 3, 4]);
sel = V.toggleLeg(sel, 1, n, -1);
assert.deepStrictEqual(V.visibleLegs(sel, n, -1), [0, 1, 2, 3, 4]);
// "current leg + next" follows the run; with no run every leg is shown
const follow = { mode: "follow", only: -1, off: [] };
assert.deepStrictEqual(V.visibleLegs(follow, n, 2), [2, 3]);
assert.deepStrictEqual(V.visibleLegs(follow, n, 4), [4]);
assert.deepStrictEqual(V.visibleLegs(follow, n, -1), [0, 1, 2, 3, 4]);
// toggling while following starts from the legs shown then
assert.deepStrictEqual(V.visibleLegs(V.toggleLeg(follow, 3, n, 2), n, 2), [2]);
// junk from storage is brought back: unknown mode, legs out of range, a leg only that does not exist
assert.deepStrictEqual(V.normalizeSel({ mode: "weird", off: [9, -1, "a", 1, 1] }, n), { mode: "all", only: -1, off: [1] });
assert.deepStrictEqual(V.normalizeSel({ mode: "only", only: 17 }, n), { mode: "all", only: -1, off: [] });
assert.deepStrictEqual(V.normalizeSel(null, n), { mode: "all", only: -1, off: [] });
assert.deepStrictEqual(V.visibleLegs({ mode: "custom", off: [0, 1, 2, 3, 4] }, n, -1), []);

// ---- the plan with the hidden legs emptied, the array kept whole so that leg numbers hold
const leg = (i, x) => ({ index: i, label: "L" + i, path: [{ x, y: 0 }, { x: x + 10, y: 5 }], stops: [{ x, y: 0 }], packs: [{ x: x + 20, y: 3, r: 10, points: [{ x: x + 20, y: 3 }] }], objects: [{ x: x + 2, y: 2 }], marks: [{ x: x + 3, y: 3, r: 4 }], looks: [{ x, y: 1 }], boss: 7, boss_at: { x: x + 30, y: 0 } });
const geo = { plan: "p", map: 1, legs: [leg(0, 0), leg(1, 100), leg(2, 200)] };
assert.strictEqual(V.filterGeo(geo, [0, 1, 2]), geo, "everything shown: the same object");
const f = V.filterGeo(geo, [1]);
assert.strictEqual(f.legs.length, 3);
assert.deepStrictEqual(f.legs[0].packs, []); assert.deepStrictEqual(f.legs[0].path, []); assert.strictEqual(f.legs[0].boss, 0); assert.strictEqual(f.legs[0].boss_at, null);
assert.strictEqual(f.legs[0].label, "L0", "a hidden leg keeps its name");
assert.strictEqual(f.legs[1], geo.legs[1]);
assert.strictEqual(geo.legs[0].packs.length, 1, "the plan itself is not changed");

// ---- the box of the legs shown
assert.strictEqual(V.boundsOf(geo, []), null);
const b = V.boundsOf(geo, [1]);

assert.ok(b[0] <= 100 && b[0] + b[2] >= 100 + 30 + 6, "holds the leg's boss and path: " + b);
assert.ok(b[0] > 50, "and nothing of leg 0: " + b);
const both = V.boundsOf(geo, [0, 2]);
assert.ok(both[0] <= -1 && both[0] + both[2] >= 236, String(both));

// ---- which copy
const src = (sha, path) => ({ role: "slot", path: path || "/s/p.json", sha256: sha });
assert.strictEqual(V.copyBadge(null), null);
assert.strictEqual(V.copyBadge({ run: src("a".repeat(64)), dash: src("a".repeat(64)), same: true }).level, "ok");
const diff = V.copyBadge({ run: src("1234abcd" + "0".repeat(56)), dash: src("ffff0000" + "0".repeat(56)), same: false });
assert.strictEqual(diff.level, "warn"); assert.ok(diff.text.includes("1234abcd") && diff.text.includes("ffff0000"), diff.text);
assert.strictEqual(V.copyBadge({ dash: src("ffff0000") }).level, "warn");
const chg = V.copyBadge({ run: src("2222cccc" + "0".repeat(56)), run_sha256: "1111bbbb" + "0".repeat(56), changed: true, same: true });
assert.strictEqual(chg.level, "warn"); assert.ok(chg.text.includes("1111bbbb") && chg.text.includes("2222cccc"), chg.text);

// ---- the drawing must be the run's plan: the leg number and the leg name the run is in have to agree with it
const named = { legs: [{ name: "entrance" }, { name: "hall" }, { name: "" }] };
assert.strictEqual(V.planMismatch(named, { leg: 1, leg_name: "hall" }), "");
assert.strictEqual(V.planMismatch(named, { leg: -1, leg_name: "zzz" }), "", "no leg yet: nothing to compare");
assert.strictEqual(V.planMismatch(named, { leg: 2, leg_name: "anything" }), "", "a leg without a name in the drawing cannot be compared");
assert.strictEqual(V.planMismatch(named, { leg: 1 }), "");
assert.ok(V.planMismatch(named, { leg: 1, leg_name: "temple" }).includes("hall"));
assert.ok(V.planMismatch(named, { leg: 5, leg_name: "x" }).includes("3 legs"));
assert.strictEqual(V.planMismatch(null, { leg: 1, leg_name: "x" }), "");
assert.ok(V.copyRowText({ run: src("1234abcd" + "0".repeat(56), "/x/y.json"), same: false }).startsWith("differs from the dashboard's copy; reads /x/y.json (1234abcd)"));
assert.ok(V.copyRowText({ run: src("1234abcd"), same: true }).startsWith("same as"));
assert.ok(V.copyRowText({ dungeons_dir: "" }).includes("no dungeons_dir"));
assert.ok(V.copyRowText({ dungeons_dir: "/d" }).includes("/d"));
assert.strictEqual(V.copyRowText(null), "");

// ---- the card of the plan view is shaped like a run card, with nothing running
const c = V.planCard("p", geo);
assert.strictEqual(c.synthetic, true); assert.strictEqual(c.run.state, "idle"); assert.strictEqual(c.run.leg, -1); assert.strictEqual(c.label.plan, "p");
assert.deepStrictEqual([c.bots, c.mobs, c.events, c.trail], [[], [], [], []]);
assert.strictEqual(c.map, 1); assert.strictEqual(c.run.leg_count, 3);
assert.strictEqual(V.planCard("p", "loading").map, 0);
assert.strictEqual(V.planCard("p", null).map, 0);
console.log("planview ok");
