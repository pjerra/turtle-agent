"use strict";
// Run by view_test.go under node: the route editor's model (the ops, the route with ops applied, the handles, undo and redo, the words).
const assert = require("assert");
const E = require(process.argv[2]);
const close = (a, b, eps = 1e-6) => assert.ok(Math.abs(a - b) <= eps, a + " != " + b);

// a plan in the drawing's plane: leg 0 has a start, three via points (the second is a direct step, the third check_skip) and `to`; leg 1 has a boss;
// leg 2 has neither a boss nor a pack, so it ends at its `to`; leg 3 starts from it
const stop = (kind, x, y, extra) => Object.assign({ kind, x, y }, extra || {});
const geo = { legs: [
  { index: 0, label: "One", name: "one", packs: [{}], stops: [stop("start", 0, 0), stop("via", -20, -10), stop("via", -40, -10, { direct: true }), stop("via", -60, -10, { check_skip: "pit" }), stop("stage", -100, -10), stop("pull", -120, -10)] },
  { index: 1, label: "Two", name: "two", boss_at: { x: -220, y: 0 }, stops: [stop("start", -120, -10), stop("stage", -200, 0), stop("boss", -220, 0)] },
  { index: 2, label: "Three", name: "three", stops: [stop("start", -220, 0), stop("stage", -300, 0)] },
  { index: 3, label: "Four", name: "four", packs: [{}], stops: [stop("start", -300, 0), stop("stage", -400, 0)] },
] };
const model = E.buildModel(geo);
assert.strictEqual(model.legs.length, 4);
assert.deepStrictEqual(model.legs[0].via.map((v) => v.locked), [false, true, true]);
assert.ok(model.legs[0].via[1].why.includes("direct") && model.legs[0].via[2].why.includes("check_skip"));
assert.deepStrictEqual(model.legs[0].to, { x: -100, y: -10 });
assert.deepStrictEqual([0, 1, 2, 3].map((i) => model.legs[i].exitIsTo), [false, false, true, false]);
assert.deepStrictEqual(E.buildModel(null).legs, []);

// ---- game and drawing coordinates are each other's inverse: north (+x) is up (-y), west (+y) is left (-x)
const g = E.toGame(-30, -50);
assert.deepStrictEqual(g, { x: 50, y: 30 });
assert.deepStrictEqual(E.toDrawing(g.x, g.y), { x: -30, y: -50 });
for (const [x, y] of [[0, 0], [1234.5, -987.25], [-3, 7]]) { const d = E.toDrawing(x, y), b = E.toGame(d.x, d.y); close(b.x, x); close(b.y, y); }

// ---- ops applied: moves, an insert, a delete, in order
const ops = [
  { leg: 0, kind: "move", index: 0, x: 11, y: 22 },       // via 0 -> drawing (-22, -11)
  { leg: 0, kind: "insert", index: 1, x: 15, y: 30 },     // a new via point before the direct one
  { leg: 0, kind: "move", index: 4, x: 99, y: 98 },       // `to` is now index 4 (3 vias + 1 new)
  { leg: 0, kind: "delete", index: 0 },                   // the first via point
];
const w = E.apply(model, ops);
assert.deepStrictEqual(w.errors, []);
assert.deepStrictEqual(w.legs[0].via.map((v) => [v.x, v.y]), [[-30, -15], [-40, -10], [-60, -10]]);
assert.strictEqual(w.legs[0].via[0].inserted, true);
assert.deepStrictEqual(w.legs[0].to, { x: -98, y: -99 });
assert.deepStrictEqual(model.legs[0].via[0], { x: -20, y: -10, locked: false, why: "" }, "the model itself is not changed");
// a leg with no boss and no pack follows its `to`: the next leg starts there
const w2 = E.apply(model, [{ leg: 2, kind: "move", index: 0, x: 301, y: 5 }]);
assert.deepStrictEqual(w2.legs[3].start, { x: -5, y: -301 });
assert.deepStrictEqual(w2.legs[1].start, model.legs[1].start, "a leg after one with a boss starts where it did");
const w3 = E.apply(model, [{ leg: 0, kind: "move", index: 3, x: 1, y: 1 }]);
assert.deepStrictEqual(w3.legs[1].start, model.legs[1].start, "a leg whose exit is a pack does not move with `to`");

// ---- what cannot be done is named, and skipped
assert.ok(E.checkOp(model.legs, { leg: 0, kind: "move", index: 1, x: 0, y: 0 }).startsWith("locked"));
assert.ok(E.checkOp(model.legs, { leg: 0, kind: "move", index: 2, x: 0, y: 0 }).includes("check_skip"));
assert.ok(E.checkOp(model.legs, { leg: 0, kind: "delete", index: 1, x: 0, y: 0 }).startsWith("locked"));
assert.ok(E.checkOp(model.legs, { leg: 0, kind: "delete", index: 3 }).includes("`to`"), "a leg needs its to");
assert.strictEqual(E.checkOp(model.legs, { leg: 0, kind: "move", index: 3, x: 0, y: 0 }), "");
assert.strictEqual(E.checkOp(model.legs, { leg: 0, kind: "insert", index: 3, x: 0, y: 0 }), "");
assert.ok(E.checkOp(model.legs, { leg: 0, kind: "insert", index: 4 }));
assert.ok(E.checkOp(model.legs, { leg: 9, kind: "move", index: 0 }));
assert.ok(E.checkOp(model.legs, { leg: 0, kind: "teleport", index: 0 }));
const bad = E.apply(model, [{ leg: 0, kind: "move", index: 1, x: 0, y: 0 }, { leg: 0, kind: "move", index: 0, x: 11, y: 22 }]);
assert.strictEqual(bad.errors.length, 1); assert.ok(bad.errors[0].startsWith("op 1"));
assert.deepStrictEqual(bad.legs[0].via[0], { x: -22, y: -11, locked: false, why: "" });

// ---- the route points and the handles
const pts = E.routePoints(model.legs[0]);
assert.deepStrictEqual(pts.map((p) => p.kind), ["start", "via", "via", "via", "to"]);
assert.strictEqual(pts[4].index, 3);
const hs = E.handles(model.legs[0], 0);
assert.deepStrictEqual(hs.map((h) => [h.kind, h.index, h.locked]), [["via", 0, false], ["via", 1, true], ["via", 2, true], ["to", 3, false]]);
// the middle of each segment, and the index a new via point takes there
const mids = E.midHandles(model.legs[0], 0, 4);
assert.deepStrictEqual(mids.map((m) => m.index), [0, 1, 2, 3]);
assert.deepStrictEqual([mids[0].x, mids[0].y], [-10, -5]);
assert.strictEqual(E.midHandles({ start: { x: 0, y: 0 }, via: [], to: { x: 3, y: 0 } }, 0, 4).length, 0, "a segment shorter than the minimum has no handle");
assert.deepStrictEqual(E.midHandles({ start: { x: 0, y: 0 }, via: [], to: { x: 30, y: 0 } }, 2).map((m) => [m.leg, m.index]), [[2, 0]]);
// a new point put at a middle handle's index lands between the two points of its segment
for (const m of mids) {
  const after = E.apply(model, [E.insertOp(0, m.index, m.x, m.y)]);
  const route = E.routePoints(after.legs[0]);
  const k = route.findIndex((p) => p.kind === "via" && p.index === m.index);
  assert.ok(k > 0 && route[k].x === m.x && route[k].y === m.y, "the new point is the via point of that number");
  const a = route[k - 1], b = route[k + 1];
  close(Math.hypot(a.x - m.x, a.y - m.y) + Math.hypot(b.x - m.x, b.y - m.y), Math.hypot(a.x - b.x, a.y - b.y), 1e-6); // between its two neighbours, on their line
  assert.strictEqual(route.length, pts.length + 1);
}

// ---- the nearest handle, in pixels
assert.strictEqual(E.hitTest(hs, -20.5, -10, 10, 12).index, 0);
assert.strictEqual(E.hitTest(hs, -26, -10, 10, 12), null, "6 yd at 10 px per yard is 60 px away");
assert.strictEqual(E.hitTest(hs, -26, -10, 1, 12).index, 0, "the same 6 yd at 1 px per yard is near");
const two = [{ leg: 0, kind: "via", index: 0, x: 0, y: 0 }, { leg: 0, kind: "via", index: 1, x: 1, y: 0 }];
assert.strictEqual(E.hitTest(two, 0.8, 0, 10, 20).index, 1, "the nearer of two");
assert.strictEqual(E.hitTest([], 0, 0, 1, 10), null);
assert.strictEqual(E.hitTest(hs, -40, -10, 10, 12).locked, true, "a locked handle is found too: the page says why it does not move");

// ---- gestures
assert.deepStrictEqual(E.moveOp(hs[0], -22, -11), { leg: 0, kind: "move", index: 0, x: 11, y: 22 });
assert.deepStrictEqual(E.insertOp(2, 1, -30, -50), { leg: 2, kind: "insert", index: 1, x: 50, y: 30 });
assert.deepStrictEqual(E.deleteOp(1, 0), { leg: 1, kind: "delete", index: 0 });
assert.deepStrictEqual(E.moveOp(hs[0], -22.12345, -11.98765), { leg: 0, kind: "move", index: 0, x: 11.99, y: 22.12 }, "rounded to a centimetre");
// an op list the page builds from gestures is one the model applies without errors
const gestured = [E.moveOp(hs[0], -25, -12), E.insertOp(0, 3, -80, -10), E.deleteOp(0, 0)];
assert.deepStrictEqual(E.apply(model, gestured).errors, []);

// ---- undo and redo
let hist = E.history([]);
assert.strictEqual(E.undo(hist), hist, "nothing to undo");
for (const op of ops) hist = E.push(hist, op);
assert.strictEqual(hist.ops.length, 4);
hist = E.undo(E.undo(hist));
assert.deepStrictEqual(hist.ops, ops.slice(0, 2)); assert.strictEqual(hist.redo.length, 2);
hist = E.redo(hist);
assert.deepStrictEqual(hist.ops, ops.slice(0, 3));
hist = E.push(hist, E.deleteOp(0, 0));
assert.strictEqual(hist.redo.length, 0, "a new edit ends the redo list");
assert.strictEqual(E.redo(hist), hist);
const loaded = E.history(ops);
loaded.ops.push({ leg: 0, kind: "delete", index: 0 });
assert.strictEqual(ops.length, 4, "a history keeps its own copy");

// ---- words
assert.strictEqual(E.compass(10, 0), "north"); assert.strictEqual(E.compass(-10, 0), "south"); assert.strictEqual(E.compass(0, 10), "west"); assert.strictEqual(E.compass(0, -10), "east");
assert.strictEqual(E.compass(5, 5), "north-west"); assert.strictEqual(E.compass(-5, -5), "south-east"); assert.strictEqual(E.compass(0, 0), "");
const words = E.describeChanges([
  { leg: 2, leg_name: "theka", op: "move", part: "via 2", from: { x: 0, y: 0, z: 0 }, to: { x: 0, y: -6.1, z: 0 } },
  { leg: 2, leg_name: "theka", op: "insert", part: "via 4", to: { x: 1, y: 1, z: 0 }, floors: 2, floor_picked: true },
  { leg: 0, op: "delete", part: "via 1", from: { x: 1, y: 1, z: 0 } },
]);
assert.strictEqual(words[0], "leg 3 (theka): via 2 moved 6.1 yd east");
assert.strictEqual(words[1], "leg 3 (theka): via 4 added (floor picked: 2 floors in range)");
assert.strictEqual(words[2], "leg 1: via 1 removed");
assert.deepStrictEqual(E.describeChanges(null), []);
// the failing points of a check, in the drawing's plane
const marks = E.issueMarks({ issues: [{ point: { x: 50, y: 30, z: 1 }, new: true, text: "leg 1 via 0: off the mesh" }, { point: { x: 1, y: 2 }, new: false, reason: "unreachable" }, { text: "no point" }] });
assert.deepStrictEqual(marks, [{ x: -30, y: -50, text: "leg 1 via 0: off the mesh", isNew: true }, { x: -2, y: -1, text: "unreachable", isNew: false }]);
assert.deepStrictEqual(E.issueMarks(null), []);

// ---- the snap query carries the ops before the tested one
const q = new URLSearchParams(E.snapQuery(ops.slice(0, 2), { leg: 0, kind: "move", index: 3, x: 1.5, y: -2 }));
assert.strictEqual(q.get("kind"), "move"); assert.strictEqual(q.get("leg"), "0"); assert.strictEqual(q.get("index"), "3"); assert.strictEqual(q.get("x"), "1.5"); assert.strictEqual(q.get("y"), "-2");
assert.deepStrictEqual(JSON.parse(q.get("prior")), ops.slice(0, 2));
assert.strictEqual(new URLSearchParams(E.snapQuery([], { leg: 0, kind: "insert", index: 0, x: 0, y: 0 })).has("prior"), false);
console.log("routeedit ok");
