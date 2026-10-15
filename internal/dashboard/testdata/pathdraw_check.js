"use strict";
// Run by view_test.go under node: the path layers drawn on a fake SVG (no browser): what each layer puts on the map, what is left out when a layer is off or
// its data is missing, what the hover boxes say, and that a text from the server only ever goes in as text.
const assert = require("assert");
const PD = require(process.argv[2]);
const ML = require("../static/layers.js");

function el(tag, attrs, text) {
  const o = { tag, attrs: Object.assign({}, attrs), kids: [], text: text == null ? null : String(text), dataset: {} };
  o.append = (...k) => { o.kids.push(...k); };
  return o;
}
const s = (tag, attrs, text) => el(tag, attrs, text);
const plate = (text, x, y) => el("g", { class: "plate", x, y }, text);
const at = (x, y, cls) => { const g = el("g", { class: "mk " + cls }); g.dataset.x = x; g.dataset.y = y; return g; };
const flat = (e) => [e].concat(...e.kids.map((k) => (typeof k === "object" ? flat(k) : [])));
const all = (list) => [].concat(...list.map(flat));
const texts = (list) => all(list).map((e) => e.text).filter((t) => t != null);

function mkCtx(over) {
  const units = new Map();
  const classes = new Map();
  const staticEls = ["0:0", "0:1", "1:0"].map((k) => { const [l, g] = k.split(":"); const cl = new Set(["pull"]); return { dataset: { leg: l, group: g }, classList: { add: (c) => cl.add(c), remove: (c) => cl.delete(c) }, cl }; });
  const svg = { querySelectorAll: () => staticEls, classList: { toggle: (c, on) => classes.set(c, on) } };
  const geo = {
    names: { "900": "Bossy" },
    legs: [
      { label: "Hall", boss: 900, boss_at: { x: 90, y: 0 }, path: [{ x: 0, y: 0 }, { x: 50, y: 0 }, { x: 100, y: 0 }],
        stops: [{ x: 0, y: 0, kind: "start", n: 0 }, { x: 50, y: 0, kind: "via", n: 1, label: "waypoint" }, { x: 100, y: 0, kind: "end", n: 2, label: "end of Hall" }],
        packs: [{ index: 0, x: 20, y: 0, r: 20, n: 3, label: "pack 1: Rat", points: [{ x: 20, y: 0 }], patrol: { path: [{ x: 20, y: 30 }, { x: 60, y: 30 }], entries: [7] } },
          { index: 1, x: 60, y: 0, r: 20, n: 2, label: "pack 2: Bat <b>", points: [{ x: 60, y: 0 }] }],
        objects: [{ kind: "use", x: 40, y: 10, step: 0, label: "use 12" }, { kind: "door", x: 80, y: 10, step: 1, label: "wait 13 active" }] },
      { label: "Cave", path: [{ x: 100, y: 0 }, { x: 150, y: 0 }], stops: [], packs: [{ index: 0, x: 120, y: 0, r: 20, n: 1, label: "pack 1: Bear", points: [{ x: 120, y: 0 }] }], objects: [] },
    ],
  };
  const live = {
    seconds: 400, ended: false, legs: [{ leg: 0, result: "" }],
    paths: [
      { name: "Tank", role: "tank", lead: true, p: [[0, 0], [30, 0]], t: [0, 60] },
      { name: "Heal", role: "healer", p: [[0, 5], [20, 5], [40, 5]], t: [0, 50, 100] },
      { name: "Mage", role: "dps", p: [[0, -5], [40, -5]], t: [0, 100] },
    ],
    stalls: [{ x: 30, y: 2, from: 100, to: 160, secs: 60, leg: 0, reason: "resting before the next pull" }],
    refusals: [{ kind: "refused", x: 70, y: 70, t: 120, leg: 0, text: "unreachable: The lead bot has no walkable path" }, { kind: "stuck", x: 5, y: 5, t: 10, leg: 0, text: "Mage stuck", secs: 6 }],
    rests: [{ kind: "rest", x: 10, y: 0, t: 60, leg: 0, secs: 25 }], gathers: [{ kind: "gather", x: 18, y: 2, t: 80, leg: 0, text: "the party gathers" }],
    pulls: [{ leg: 0, entry: 1, x: 20, y: 1, t: 70, outcome: "won" }, { leg: 0, entry: 2, x: 60, y: 0, t: 200, outcome: "refused" }],
    objects: [{ leg: 0, action: "use_object", x: 40, y: 10, ok: true, step: 0, t: 90 }],
  };
  const card = {
    id: "8765", level: 40, run: { leg: 0, group: 2, state: "running", minutes: 6.7 }, trail: [[0, 0], [30, 0], [30, 60]], trail_t: [1, 2, 3], events: [],
    bots: [
      { name: "Tank", guid: "0xB", role: "tank", lead: true, known: true, alive: true, x: 30, y: 60 },
      { name: "Heal", guid: "0xA", role: "healer", known: true, alive: true, x: 25, y: 55, target_guid: "0xB" },
      { name: "Mage", guid: "0xC", role: "dps", known: true, alive: true, x: 20, y: 50 },
    ],
    mobs: [
      { guid: "0x100", entry: 7, kind: "hostile", x: 40, y: 30, leg: 0, group: 0, hp_pct: 100, target_guid: "0xB", cc: false },
      { guid: "0x101", entry: 2, kind: "hostile", x: 45, y: 33, leg: 0, group: 0, hp_pct: 100, cc: true },
    ],
    orders: [{ text: "cmd cc moon 0x101 Mage", reply: "ACK" }],
  };
  const ctx = Object.assign({ s, plate, at, unitG: (g, key, u) => { units.set(key, u); g.dataset.uid = key; return g; }, on: ML.defaults(), style: ML.styleDefaults(), layers: ML, card, geo, svg,
    live, ghost: { found: true, run: "r-1", seconds: 300, complete: true, legs: [{ leg: 0, start: 0, end: 300 }], path: [[0, 0, 0], [50, 0, 100], [100, 0, 300]] },
    heat: { points: [[10, 10, 0], [11, 11, 0], [80, 80, 1]] }, lead: { x: 50, y: 10 }, nextTarget: { stop: { x: 50, y: 0, label: "waypoint" }, index: 1, dist: 63 },
    threat: [{ from: { x: 40, y: 30 }, to: { x: 30, y: 60 } }] }, over);
  return { ctx, units, staticEls, classes };
}
const allOn = () => { const o = {}; ML.keys().forEach((k) => { o[k] = true; }); return o; };
const allOff = () => { const o = {}; ML.keys().forEach((k) => { o[k] = false; }); return o; };

// ---- everything off: nothing is drawn, but every layer still knows whether it has data (so the panel can grey it out)
let t = mkCtx({ on: allOff() });
let r = PD.build(t.ctx);
assert.strictEqual(r.under.length, 0); assert.strictEqual(r.over.length, 0);
["waypoints", "membertrails", "ghost", "restgather", "objects", "patrols", "stalls", "refusals", "packrings", "combat", "deathheat"].forEach((k) => assert.strictEqual(r.avail[k], true, k + " has data " + JSON.stringify(r.avail)));
assert.ok(/ghost is/.test(r.ghostLine), r.ghostLine);

// ---- no data: nothing available, nothing thrown
t = mkCtx({ on: allOn(), live: null, ghost: null, heat: null, card: Object.assign({}, mkCtx().ctx.card, { mobs: [], orders: [], trail: [], bots: [] }), threat: [], nextTarget: null, lead: null,
  geo: { legs: [{ label: "A", path: [], stops: [], packs: [], objects: [] }] } });
r = PD.build(t.ctx);
["waypoints", "membertrails", "ghost", "restgather", "objects", "patrols", "stalls", "refusals", "packrings", "combat", "deathheat"].forEach((k) => assert.strictEqual(r.avail[k], false, k + " has no data"));
assert.strictEqual(r.ghostLine, "");

// ---- one layer at a time draws what it should
function only(key, over) { const on = allOff(); on[key] = true; return PD.build(mkCtx(Object.assign({ on }, over || {})).ctx); }
// waypoints: dashed line + halo, a via diamond, the target ring, the off-route piece with its label
r = only("waypoints");
assert.ok(all(r.under).some((e) => e.tag === "polyline" && e.attrs["stroke-dasharray"] === "9 6"));
assert.ok(all(r.over).some((e) => e.tag === "polygon"), "via point");
assert.ok(texts(r.over).some((x) => x === "target: waypoint"));
assert.ok(texts(r.over).some((x) => x === "off the planned route"));
assert.ok(all(r.under).some((e) => e.attrs.stroke === "#ff5a36"), "the lead left the plan");
// the style sliders reach the drawing
const wide = ML.setStyle(ML.setStyle(ML.styleDefaults(), "waypoints", "width", 6), "waypoints", "opacity", 50);
r = only("waypoints", { style: wide });
const dash = all(r.under).find((e) => e.tag === "polyline" && e.attrs["stroke-dasharray"] === "9 6");
assert.strictEqual(dash.attrs["stroke-width"], 6); assert.strictEqual(dash.attrs.opacity, "0.50");
// member trails: the lead is the page's own; the others are drawn in role colours, none for a lead
r = only("trail_members");
const cols = new Set(all(r.under).filter((e) => e.tag === "polyline" && e.attrs.stroke !== "#000").map((e) => e.attrs.stroke));
assert.deepStrictEqual([...cols].sort(), [ML && require("../static/paths.js").ROLE_COLOR.dps, require("../static/paths.js").ROLE_COLOR.healer].sort());
// only the last minutes: 1 minute of a run at 400 s keeps nothing but the newest point of each walk, which draws no line
r = only("trail_members", { style: ML.setStyle(ML.styleDefaults(), "trail_members", "minutes", 1) });
assert.strictEqual(all(r.under).filter((e) => e.tag === "polyline").length, 0);
// ghost: a dotted line and the ghost marker with its words; the hover box says what it is
r = only("ghost");
assert.ok(all(r.under).some((e) => e.tag === "polyline" && e.attrs["stroke-dasharray"] === "2 7"));
assert.ok(texts(r.over).some((x) => /^ghost /.test(x)));
t = mkCtx({ on: Object.assign(allOff(), { ghost: true }) }); PD.build(t.ctx);
assert.ok([...t.units.values()].some((u) => u.type === "mark" && u.data.badge === "ghost" && /fastest clear/.test(u.data.rows.map((x) => x.join(" ")).join(" ") + u.data.title)));
// pull groups: the static circles take the state class, boss status shows, hover boxes carry the words
t = mkCtx({ on: Object.assign(allOff(), { pullstate: true }) }); r = PD.build(t.ctx);
assert.ok(t.staticEls[0].cl.has("ps-cleared")); assert.ok(t.staticEls[1].cl.has("ps-failed")); assert.ok(t.staticEls[2].cl.has("ps-next"));
assert.strictEqual(t.classes.get("ps-on"), true);
assert.ok(texts(r.over).some((x) => x === "□"), "boss not dead yet");
const pk = [...t.units.values()].find((u) => u.data.title === "pack 2: Bat <b>");
assert.ok(pk && pk.data.badge === "failed" && pk.data.rows.some((row) => /refused/.test(row[1])));
t = mkCtx({ on: allOff() }); PD.build(t.ctx);
assert.ok(!t.staticEls[0].cl.has("ps-cleared"), "layer off: the plan's circles keep their own colour"); assert.strictEqual(t.classes.get("ps-on"), false);
// the boss is marked killed when the events or the leg say so
r = only("pullstate", { live: Object.assign({}, mkCtx().ctx.live, { legs: [{ leg: 0, result: "done" }] }) });
assert.ok(texts(r.over).some((x) => x === "✓"));
// rest and gather spots
t = mkCtx({ on: Object.assign(allOff(), { restgather: true }) }); r = PD.build(t.ctx);
assert.strictEqual(r.over.length, 2);
assert.ok([...t.units.values()].some((u) => u.data.title === "Rest spot" && u.data.rows.some((x) => x[1] === "25 s")));
// objects with their state
t = mkCtx({ on: Object.assign(allOff(), { objects: true }) }); r = PD.build(t.ctx);
assert.deepStrictEqual(texts(r.over).filter((x) => /:/.test(x)), ["use 12: used", "wait 13 active: pending"]);
// patrols: the loop and the diamond, filled when the feed sees the patrol
t = mkCtx({ on: Object.assign(allOff(), { patrols: true }) }); r = PD.build(t.ctx);
assert.ok(all(r.under).some((e) => e.tag === "polyline" && e.attrs["stroke-dasharray"] === "3 6"));
assert.ok(texts(r.over).includes("patrol")); assert.ok([...t.units.values()].some((u) => u.data.badge === "patrol" && u.data.rows.some((x) => x[0] === "Seen now" && /yes/.test(x[1]))));
// stalls and refusals (red X + the reason on hover)
t = mkCtx({ on: Object.assign(allOff(), { stalls: true, refusals: true }) }); r = PD.build(t.ctx);
assert.ok(texts(r.over).includes("60s"));
const x = all(r.over).filter((e) => e.tag === "path" && e.attrs.stroke === "#ff3b3b");
assert.strictEqual(x.length, 2);
assert.ok(all(r.over).some((e) => e.tag === "title" && /no walkable path/.test(e.text)));
assert.ok([...t.units.values()].some((u) => u.data.rows.some((row) => row[0] === "Reason" && /no walkable path/.test(row[1]))));
assert.ok([...t.units.values()].some((u) => u.data.title === "Party stood still" && u.data.rows.some((row) => /resting/.test(row[1]))));
// aggro rings: 20 yd (the feed gives no level) around the pack, scaled by the slider
r = only("packrings", { style: ML.setStyle(ML.styleDefaults(), "packrings", "width", 4) });
const ring = all(r.under).find((e) => e.tag === "circle"); assert.ok(ring.attrs.r >= 20); assert.strictEqual(ring.attrs["stroke-width"], 4);
// combat lines: mob to victim, healer to friend, caster to the creature it holds
r = only("combat");
const strokes = all(r.under).filter((e) => e.tag === "line" && e.attrs.stroke !== "#000").map((e) => e.attrs.stroke);
assert.deepStrictEqual(strokes.sort(), ["#22c55e", "#67e8f9", "#ff3b3b"].sort());
// the death heat map: darker where more died
r = only("deathheat");
const hc = all(r.under).filter((e) => e.tag === "circle");
assert.strictEqual(hc.length, 2); assert.ok(Math.max(...hc.map((e) => +e.attrs.opacity)) > Math.min(...hc.map((e) => +e.attrs.opacity)));

// ---- a text from the server is only ever a text node: nothing carries markup
t = mkCtx({ on: allOn() }); r = PD.build(t.ctx);
all(r.under.concat(r.over)).forEach((e) => { assert.ok(["g", "circle", "path", "polyline", "polygon", "line", "rect", "text", "title"].includes(e.tag), e.tag); assert.ok(!("innerHTML" in e), "no innerHTML"); });
assert.ok([...t.units.values()].some((u) => u.data.title === "pack 2: Bat <b>"), "the label is kept as it is: the page puts it in with textContent");
console.log("pathdraw.js ok");
