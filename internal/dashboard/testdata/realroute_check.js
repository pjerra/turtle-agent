"use strict";
// Run by view_test.go under node: the real route of the plan view (addresses, the legs shown, the lines, the numbers and what they say).
const assert = require("assert");
const R = require(process.argv[2]);

// a minimal svg element maker, to look at what the page would draw
const s = (tag, attrs, text) => {
  const el = { tag, attrs: attrs || {}, kids: [], text: text == null ? "" : text, append(...k) { this.kids.push(...k); } };
  return el;
};
const find = (el, tag, out = []) => { if (el.tag === tag) out.push(el); el.kids.forEach((k) => find(k, tag, out)); return out; };

// ---- addresses
assert.strictEqual(R.routeUrl("uldaman", {}), "/api/plan/uldaman/route");
assert.strictEqual(R.routeUrl("uldaman", { agent: "3" }), "/api/plan/uldaman/route?agent=3");
assert.strictEqual(R.routeUrl("uldaman", { ops: [] }), "/api/plan/uldaman/route");
const ops = [{ leg: 0, kind: "move", index: 1, x: 12.5, y: -3 }];
const u = R.routeUrl("uldaman", { ops });
assert.ok(u.startsWith("/api/edit/uldaman/route?ops="), u);
assert.deepStrictEqual(JSON.parse(new URLSearchParams(u.split("?")[1]).get("ops")), ops);
for (const bad of ["", "../x", "A", "a/b", null, undefined]) assert.strictEqual(R.routeUrl(bad, {}), "", String(bad));

// ---- a route: two legs, the second walked to from the end of the first; one segment the planner cannot path
const doc = {
  plan: "t", map: 1, sha256: "ab", mmaps: "/m", summary: { stops: 5, segments: 5, length: 412.4, failed: 1, links: 2, warnings: 0 },
  stops: [
    { seq: 1, leg: 0, kind: "via", x: 0, y: -20, label: "walk to waypoint 1 of One" },
    { seq: 2, leg: 0, kind: "stage", x: 0, y: -20.4, label: "walk to the stage point of One" }, // on the spot of stop 1
    { seq: 3, leg: 0, kind: "pack", group: 1, x: 5, y: -90, label: "pull pack 1: Rats", warnings: ["detour: long way"] },
    { seq: 4, leg: 1, kind: "stage", x: 10, y: -150, label: "walk to the stage point of Two", failed: true, issues: ["unreachable: path_to is no_path"] },
    { seq: 5, leg: 1, kind: "boss", x: 12, y: -190, label: "boss Revelosh of Two", notes: ["in view already"] },
  ],
  segments: [
    { stop: 1, leg: 0, kind: "via", part: "via 0", link: true, a: { x: 0, y: 0 }, b: { x: 0, y: -20 }, pts: [[0, 0], [2, -10], [0, -20]], len: 21, ok: true, text: "stop 1: walk to waypoint 1" },
    { stop: 2, leg: 0, kind: "stage", part: "to", a: { x: 0, y: -20 }, b: { x: 0, y: -20.4 }, pts: [[0, -20], [0, -20.4]], len: 0.4, ok: true, text: "tiny" },
    { stop: 3, leg: 0, kind: "pack", part: "pull group look", a: { x: 0, y: -20.4 }, b: { x: 5, y: -70 }, pts: [[0, -20.4], [5, -70]], len: 50, ok: true, text: "stop 3" },
    { stop: 4, leg: 1, kind: "stage", part: "to", link: true, a: { x: 5, y: -70 }, b: { x: 10, y: -150 }, pts: [], len: 0, ok: false, reason: "unreachable", detail: "path_to is no_path", text: "stop 4: walk\nCANNOT BE PATHED (unreachable): path_to is no_path" },
    { stop: 5, leg: 1, kind: "direct", part: "direct via 0", a: { x: 10, y: -150 }, b: { x: 12, y: -190 }, pts: [[10, -150], [12, -190]], len: 40, ok: true, text: "direct" },
    { stop: 5, leg: 1, kind: "skip", part: "via 1", a: { x: 1, y: 1 }, b: { x: 1, y: 1 }, pts: [[1, 1], [1, 1]], len: 0, ok: true, exempt: true, text: "nowhere" },
  ],
};
assert.strictEqual(R.parseDoc(doc), doc);
for (const bad of [null, {}, { stops: [] }, "x", { stops: 1, segments: [] }]) assert.strictEqual(R.parseDoc(bad), null);

// ---- the legs shown
let v = R.visible(doc, [0, 1]);
assert.strictEqual(v.stops.length, 5); assert.strictEqual(v.segs.length, 6);
v = R.visible(doc, [1]);
assert.deepStrictEqual(v.stops.map((x) => x.seq), [4, 5]);
assert.deepStrictEqual(R.visible(doc, []), { stops: [], segs: [] });
assert.deepStrictEqual(R.visible(null, [0]), { stops: [], segs: [] });

// ---- the line of a segment: the planner's path, else the chord; a segment that goes nowhere has none
assert.deepStrictEqual(R.linePoints(doc.segments[0]), [[0, 0], [2, -10], [0, -20]]);
assert.deepStrictEqual(R.linePoints(doc.segments[3]), [[5, -70], [10, -150]]);
assert.deepStrictEqual(R.linePoints(doc.segments[5]), []);
assert.strictEqual(R.pointsAttr([[0, 0], [1.5, -2]]), "0,0 1.5,-2");

// ---- classes: a failure is red whatever the kind
assert.ok(R.segClass(doc.segments[3]).split(" ").includes("bad"));
assert.ok(!R.segClass(doc.segments[0]).split(" ").includes("bad"));
assert.ok(R.segClass(doc.segments[0]).split(" ").includes("link"));
assert.ok(R.segClass(doc.segments[4]).split(" ").includes("k-direct"));
assert.ok(R.segClass(doc.segments[5]).split(" ").includes("exempt"));
assert.ok(R.segClass(Object.assign({}, doc.segments[4], { ok: false, reason: "too far" })).split(" ").includes("bad"));
assert.ok(R.stopClass(doc.stops[3]).split(" ").includes("bad"));
assert.ok(R.stopClass(doc.stops[2]).split(" ").includes("warn"));
assert.ok(R.stopClass(doc.stops[0]).split(" ").includes("mk"));

// ---- the numbers: in the order of the run, the two on one spot side by side, the rest alone
const fx = R.fan(doc.stops);
assert.strictEqual(fx.length, 5);
assert.ok(fx[0] < 0 && fx[1] > 0 && Math.abs(fx[0] + fx[1]) < 1e-9, JSON.stringify(fx));
assert.deepStrictEqual(fx.slice(2), [0, 0, 0]);
assert.deepStrictEqual(R.fan([{ x: 0, y: 0 }, { x: 0.2, y: 0 }, { x: 0.1, y: 0.1 }]).map((x) => Math.round(x)), [-15, 0, 15]);

// ---- what the hover says
assert.strictEqual(R.stopTip(doc.stops[0]), "1. walk to waypoint 1 of One");
const tip = R.stopTip(doc.stops[3]);
assert.ok(tip.startsWith("4. walk to the stage point of Two") && tip.includes("PLAN CHECK FAILS: unreachable"), tip);
assert.ok(R.stopTip(doc.stops[2]).includes("warning: detour"));
assert.ok(R.stopTip(doc.stops[4]).includes("in view already"));
assert.strictEqual(R.summaryText(doc), "5 stops, 5 walks, 412 yd, 1 cannot be pathed");
assert.ok(R.summaryText(Object.assign({}, doc, { edited: true })).endsWith("(with your edits, not saved)"));
assert.strictEqual(R.summaryText(null), "");

// ---- the drawing
let d = R.build(s, doc, [0, 1]);
assert.strictEqual(d.lines.length, 5);        // the segment that goes nowhere is not drawn
assert.strictEqual(d.marks.length, 5);
assert.deepStrictEqual(d.marks.map((m) => m.attrs["data-stop"]), [1, 2, 3, 4, 5]);
assert.ok(d.marks.every((m) => m.attrs.class.startsWith("mk ") && m.attrs["data-x"] !== undefined));
const red = d.lines.filter((l) => l.attrs.class.split(" ").includes("bad"));
assert.strictEqual(red.length, 1);
assert.strictEqual(find(red[0], "polyline").find((p) => p.attrs.class === "rline").attrs.points, "5,-70 10,-150"); // the chord of the failed walk
assert.ok(find(red[0], "title")[0].text.includes("CANNOT BE PATHED"));   // the reason is on the hover
assert.strictEqual(find(d.marks[2], "text")[0].text, "3");
assert.ok(find(d.marks[3], "title")[0].text.includes("PLAN CHECK FAILS"));
d = R.build(s, doc, [1]);
assert.strictEqual(d.lines.length, 2); assert.strictEqual(d.marks.length, 2);
d = R.build(s, doc, []);
assert.strictEqual(d.lines.length + d.marks.length, 0);
d = R.build(s, null, [0]);
assert.strictEqual(d.lines.length + d.marks.length, 0);

console.log("ok");
