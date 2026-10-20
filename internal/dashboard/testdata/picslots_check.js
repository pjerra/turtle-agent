"use strict";
// Run by view_test.go under node: the map picture slots (ZF's three, a missing kind, floors, the saved choice, the picker).
const assert = require("assert");
const P = require(process.argv[2]);

const zf = [
  { name: "World map", source: "worldmap", kind: "worldmap", floor: "", label: "World map", available: true, url: "/minimap/209.worldmap.png", x: -1625, y: -2052.08, w: 1383.33, h: 922.91 },
  { name: "Minimap", source: "minimap", kind: "minimap", floor: "", label: "Minimap", available: true, url: "/minimap/209.minimap.png", x: -1600, y: -2666.67, w: 1600, h: 3733.33 },
  { name: "Navmesh", source: "navmesh", kind: "navmesh", floor: "", label: "Navmesh", available: true, url: "/minimap/209.navmesh.png", x: -1612, y: -2678.67, w: 1624, h: 3756.53 },
];
const WHY = "No minimap picture: indoor dungeon (the client draws it from WMO tiles, which minimap-extract does not compose yet)";
const uldaman = [
  zf[0],
  { name: "Minimap", source: "minimap", kind: "minimap", floor: "", label: "Minimap", available: false, reason: WHY },
  { name: "Navmesh", source: "navmesh", kind: "navmesh", floor: "", label: "Navmesh", available: true, url: "/minimap/70.navmesh.png", x: -645, y: -204, w: 893, h: 595 },
];

// ---- what is drawn: an available entry needs a picture URL and a finite, positive rectangle, as before
const bad = [
  { kind: "worldmap", label: "World map", available: true, url: "javascript:alert(1)", x: 0, y: 0, w: 1, h: 1 },
  { kind: "worldmap", label: "World map", available: true, url: "/other/70.png", x: 0, y: 0, w: 1, h: 1 },
  { kind: "worldmap", label: "World map", available: true, url: "/minimap/70.png", x: NaN, y: 0, w: 1, h: 1 },
  { kind: "worldmap", label: "World map", available: true, url: "/minimap/70.png", x: 0, y: 0, w: 0, h: 1 },
  { kind: "worldmap", label: "World map", available: true, url: "/minimap/70.png", x: "0", y: 0, w: 1, h: 1 },
  { kind: "worldmap", label: "World map", available: true, url: "/minimap/70.png", x: 0, y: 0, h: 1 },
  null, 5, "x", {},
];
assert.deepStrictEqual(P.slots(bad), [], "no drawable picture survives a bad url or rectangle");
assert.deepStrictEqual(P.slots(null), []);
assert.deepStrictEqual(P.slots("nope"), []);
// a missing entry keeps its label and reason, whatever its picture fields look like
const kept = P.slots([
  { kind: "minimap", label: "Minimap", floor: "", available: false, reason: WHY, url: "javascript:x" },
  { kind: "navmesh", available: false },
])[0];
assert.strictEqual(kept.label, "Minimap"); assert.strictEqual(kept.reason, WHY); assert.strictEqual(kept.available, false);
assert.strictEqual(P.slots([{ kind: "navmesh", available: false }])[0].label, "Navmesh", "a missing slot without a label gets its kind's name");
assert.deepStrictEqual(P.slots([{}]), [], "an entry with no kind and no label is nothing");

// ---- the three kinds, ZF's order, the pickup of the floors and the default choice
let p = P.plan(zf);
assert.strictEqual(p.shown, true);
assert.deepStrictEqual(p.options.map((o) => o.value), ["World map", "Minimap", "Navmesh"]);
assert.deepStrictEqual(p.options.map((o) => o.disabled), [false, false, false]);
assert.strictEqual(p.pick.label, "World map", "the main picture is the default");
assert.strictEqual(p.reason, "");
assert.strictEqual(P.plan(zf, "Minimap").pick.label, "Minimap");
assert.strictEqual(P.plan(zf, "Navmesh").pick.label, "Navmesh");
assert.strictEqual(P.plan(zf, "nonsense").pick.label, "World map", "an unknown saved choice falls back");

// ---- a missing kind: still an option, disabled, with "(none)" and the reason; a saved choice of it falls back
p = P.plan(uldaman, "Minimap");
assert.strictEqual(p.shown, true, "the picker shows with one or two available slots");
assert.strictEqual(p.options.length, 3);
assert.deepStrictEqual(p.options[1], { value: "Minimap", text: "Minimap (none)", disabled: true, title: WHY });
assert.strictEqual(p.pick.label, "World map", "a saved choice of the missing kind falls back to the default");
assert.strictEqual(p.reason, WHY, "and its reason is shown");
assert.strictEqual(P.plan(uldaman).pick.label, "World map");
assert.strictEqual(P.plan(uldaman).reason, "");
assert.strictEqual(P.plan(uldaman, "Navmesh").pick.label, "Navmesh");

// ---- one available slot still shows the picker; none available hides it and carries the minimap's reason
const one = P.plan([zf[0], uldaman[1], uldaman[2]].map((s) => (s.kind === "navmesh" ? Object.assign({}, s, { available: false, reason: "no mmaps" }) : s)));
assert.strictEqual(one.shown, true);
assert.strictEqual(one.pick.label, "World map");
const none = P.plan([Object.assign({}, zf[0], { available: false, reason: "no world map" }), uldaman[1], Object.assign({}, zf[2], { available: false, reason: "no mmaps" })]);
assert.strictEqual(none.shown, false);
assert.strictEqual(none.pick, null);
assert.strictEqual(none.reason, WHY, "the greyed map picture explains the minimap gap");
assert.strictEqual(none.options.length, 3);
assert.deepStrictEqual(none.options.map((o) => o.text), ["World map (none)", "Minimap (none)", "Navmesh (none)"]);

// ---- floors stay with the world map, in the server's order, and a saved floor is drawn
const sm = [
  zf[0],
  Object.assign({}, zf[0], { floor: "2f", label: "World map 2f", url: "/minimap/189.worldmap-2f.png" }),
  Object.assign({}, zf[0], { floor: "3f", label: "World map 3f", url: "/minimap/189.worldmap-3f.png" }),
  Object.assign({}, zf[0], { floor: "4f", label: "World map 4f", url: "/minimap/189.worldmap-4f.png" }),
  uldaman[1],
  zf[2],
];
p = P.plan(sm, "World map 3f");
assert.deepStrictEqual(p.options.map((o) => o.value), ["World map", "World map 2f", "World map 3f", "World map 4f", "Minimap", "Navmesh"]);
assert.strictEqual(p.pick.label, "World map 3f");
assert.strictEqual(p.pick.url, "/minimap/189.worldmap-3f.png");
// a missing kind is put in its place even when the server sends only the others (an older server)
p = P.plan([zf[0]]);
assert.deepStrictEqual(p.options.map((o) => o.value), ["World map", "Minimap", "Navmesh"]);
assert.deepStrictEqual(p.options.map((o) => o.disabled), [false, true, true]);
// the floor is read off a label when the entry has no floor field
assert.strictEqual(P.slots([{ kind: "worldmap", label: "World map 5f", available: true, url: "/minimap/189.worldmap-5f.png", x: 0, y: 0, w: 1, h: 1 }])[0].floor, "5f");

console.log("picslots.js ok");
