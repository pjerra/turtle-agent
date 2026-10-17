"use strict";
// Run by view_test.go under node: the route editor's words (labels, hover text, help) and the unsaved draft kept in the session storage.
const assert = require("assert");
const E = require(process.argv[2]);

const stop = (kind, x, y, extra) => Object.assign({ kind, x, y }, extra || {});
const geo = { legs: [
  { index: 0, label: "Baelog", name: "baelog", boss_at: { x: -300, y: 0 },
    packs: [{ index: 0, x: -120, y: -10, words: "2 Stonevault Oracles, Geologist", entries: [1, 1, 2], label: "pack 1: x" }, { index: 1, x: -140, y: -10, entries: [7, 7, 8], label: "pack 2" }],
    stops: [stop("start", 0, 0), stop("via", -20, -10), stop("via", -40, -10, { direct: true }), stop("via", -60, -10), stop("stage", -100, -10)] },
  { index: 1, label: "", name: "", stops: [stop("start", -100, -10), stop("stage", -200, -10)] },
] };
const model = E.buildModel(geo), w = E.apply(model, []), leg = w.legs[0], pts = E.routePoints(leg);

// ---- labels ----
assert.strictEqual(E.pointWords(leg, pts[3]), "Walk point 3 of leg Baelog");
assert.strictEqual(E.pointWords(leg, pts[1]), "Walk point 1 of leg Baelog");
assert.strictEqual(E.pointWords(leg, pts[4]), "Where the party gathers before Baelog");
assert.strictEqual(E.pointWords(leg, pts[0]), "Start of leg Baelog");
assert.strictEqual(E.pointWords(w.legs[1], E.routePoints(w.legs[1])[1]), "End of leg Section 2", "a leg with no name or pack ends, it does not gather");
assert.strictEqual(E.packWords(leg.packs[0]), "Pack: 2 Stonevault Oracles, Geologist");
assert.strictEqual(E.packWords(leg.packs[1]), "Pack: 2 x creature 7, creature 8", "creatures the plan does not name");
assert.strictEqual(E.packWords({}), "Pack: unknown creatures");
assert.ok(E.pointDoes(leg, pts[4]).includes("gathers here") && E.pointDoes(leg, pts[4]).includes("2 Stonevault Oracles"));
assert.ok(E.pointDoes(leg, pts[2]).includes("locked: a direct"), "a locked point says why");

// ---- hover text: what the bots do, and the reason when it is red ----
const doc = {
  stops: [{ seq: 1, leg: 0, kind: "via", x: -20, y: -10, failed: false }, { seq: 3, leg: 0, kind: "via", x: -60, y: -10, failed: true, issues: ["off the mesh"] }, { seq: 4, leg: 0, kind: "stage", x: -100, y: -10, failed: false }],
  segments: [{ stop: 1, leg: 0, ok: true, len: 20.4 }, { stop: 3, leg: 0, ok: false, reason: "no_path", detail: "the planner found no way across the pit" }, { stop: 4, leg: 0, ok: true, len: 41 }],
};
let tip = E.pointTip(leg, pts[1], doc);
assert.ok(tip.startsWith("Walk point 1 of leg Baelog\n") && tip.includes("20 yd") && !tip.includes("RED"), tip);
tip = E.pointTip(leg, pts[3], doc);
assert.ok(tip.includes("RED: the planner finds no path to this point (no_path): the planner found no way across the pit.") && tip.includes("PLAN CHECK FAILS: off the mesh"), tip);
tip = E.pointTip(leg, pts[2], null);
assert.ok(tip.includes("locked: a direct"), tip);
tip = E.segmentTip(leg, pts, 2, doc);
assert.ok(tip.includes("From: Walk point 2 of leg Baelog") && tip.includes("To: Walk point 3 of leg Baelog") && tip.includes("RED:") && tip.endsWith("Click to add a walk point here."), tip);
assert.ok(E.segmentTip(leg, pts, 0, null).includes("The bots walk this stretch."));
assert.ok(E.packTip(leg, leg.packs[0]).startsWith("Pack: 2 Stonevault Oracles, Geologist\n"));

// ---- click a line to add a point: the nearest segment and where the new point goes ----
let m = E.segmentAt(leg, 0, -10, -10.5, 1, 6);
assert.ok(m && m.index === 0 && Math.abs(m.y - m.x / 2) < 1e-9, "the new point is on the line: " + JSON.stringify(m));
m = E.segmentAt(leg, 0, -80, -12, 1, 6);
assert.ok(m && m.index === 3, "between via 2 and `to`: before the 4th route point -> index " + JSON.stringify(m));
assert.strictEqual(E.segmentAt(leg, 0, -10, 30, 1, 6), null, "too far from every line");
assert.strictEqual(E.segmentAt(leg, 0, -10, 30, 0.1, 40).index, 0, "a coarser zoom reaches further");

// ---- the help box ----
const help = E.HELP.join(" ");
for (const w of ["Drag a point", "Click a line", "Delete", "Ctrl+Z", "Esc"]) assert.ok(help.includes(w), w);
assert.ok(E.KEY.length >= 4 && E.KEY.every((k) => k.length === 2 && k[1].length > 10));

// ---- the unsaved draft ----
const mem = () => { const d = new Map(); return { d, getItem: (k) => (d.has(k) ? d.get(k) : null), setItem: (k, v) => { d.set(k, String(v)); }, removeItem: (k) => { d.delete(k); } }; };
const ops = [{ leg: 0, kind: "move", index: 1, x: 12.5, y: -3 }, { leg: 0, kind: "insert", index: 0, x: 4, y: 5 }, { leg: 1, kind: "delete", index: 0 }];
let ss = mem();
assert.ok(E.draftSave(ss, "uldaman", "abc123", ops, "my note"));
let d = E.draftLoad(ss, "uldaman", "abc123");
assert.deepStrictEqual(d.ops, ops);
assert.strictEqual(d.note, "my note");
assert.strictEqual(E.draftLoad(ss, "uldaman", "other-sha"), null, "a draft made on another version of the plan file is not taken back");
assert.strictEqual(E.draftLoad(ss, "zf", "abc123"), null, "another plan has no draft");
assert.strictEqual(E.draftLoad(ss, "uldaman", ""), null);
E.draftSave(ss, "uldaman", "abc123", [], "");
assert.strictEqual(ss.d.size, 0, "no edits left: the draft is removed");
E.draftSave(ss, "uldaman", "abc123", ops, "n");
E.draftClear(ss, "uldaman");
assert.strictEqual(E.draftLoad(ss, "uldaman", "abc123"), null);
// junk in the storage is ignored, never trusted
for (const raw of ["not json", "null", "[]", '{"v":2,"base":"b","ops":[{"leg":0,"kind":"delete","index":0}]}', '{"v":1,"base":"b","ops":[]}',
  '{"v":1,"base":"b","ops":[{"leg":0,"kind":"explode","index":0}]}', '{"v":1,"base":"b","ops":[{"leg":-1,"kind":"delete","index":0}]}',
  '{"v":1,"base":"b","ops":[{"leg":0,"kind":"move","index":0,"x":"1","y":2}]}', '{"v":1,"base":"b","ops":[{"leg":0,"kind":"move","index":0,"x":null,"y":2}]}',
  JSON.stringify({ v: 1, base: "b", ops: new Array(501).fill({ leg: 0, kind: "delete", index: 0 }) })]) {
  ss = mem(); ss.setItem(E.draftKey("p"), raw);
  assert.strictEqual(E.draftLoad(ss, "p", "b"), null, raw.slice(0, 60));
}
// a storage that throws (a private window, blocked site data) never breaks the editor
const boom = { getItem() { throw new Error("denied"); }, setItem() { throw new Error("quota"); }, removeItem() { throw new Error("denied"); } };
assert.strictEqual(E.draftSave(boom, "p", "b", ops, ""), false);
assert.strictEqual(E.draftLoad(boom, "p", "b"), null);
E.draftClear(boom, "p");
assert.strictEqual(E.draftLoad(null, "p", "b"), null);
console.log("ok");
