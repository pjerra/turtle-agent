"use strict";
// Run by view_test.go under node: the map layers (defaults, the one global saved choice, availability, geometry of the newer layers).
const assert = require("assert");
const L = require(process.argv[2]);
const close = (a, b, eps = 1e-9) => assert.ok(Math.abs(a - b) <= eps, a + " != " + b);
const mem = () => { const d = {}; return { d, getItem: (k) => (k in d ? d[k] : null), setItem: (k, v) => { d[k] = String(v); } }; };

// ---- defaults are exactly what the maps drew before layers could be chosen; everything new is off
const def = L.defaults();
assert.deepStrictEqual(Object.keys(def).sort(), L.keys().slice().sort());
assert.strictEqual(new Set(L.keys()).size, L.keys().length);
const wasOn = ["bg", "route", "trail", "next", "packs", "hostile", "cleared", "bosses", "doors", "bots", "botnames", "commander", "sections"];
const wasOff = ["neutral", "steps"];
wasOn.forEach((k) => assert.strictEqual(def[k], true, k));
wasOff.forEach((k) => assert.strictEqual(def[k], false, k));
const newOn = ["bossmark", "hpbars"]; // the owner's upgrade of 2026-10-15: the boss mark and the health bars start on, everything else new is off
newOn.forEach((k) => assert.strictEqual(def[k], true, k + " starts on"));
const added = L.keys().filter((k) => wasOn.indexOf(k) < 0 && wasOff.indexOf(k) < 0 && newOn.indexOf(k) < 0);
assert.deepStrictEqual(added.sort(), ["aggro", "bottarget", "cmdline", "critter", "events", "feed", "grid", "healring", "heading", "hostilenames", "runinfo", "scale", "statusicons", "threat", "threatcolors", "trailtime", "zonenames"].sort());
added.forEach((k) => assert.strictEqual(def[k], false, k + " starts off"));
L.DEFS.forEach((d) => { assert.ok(d.label && d.group, d.key); assert.ok(!d.needs || d.why, d.key + " needs a reason"); });

// ---- one choice for every map: a single key, read back identically by any card
const st = mem();
assert.deepStrictEqual(L.load(st), def);
let s = L.set(L.set(L.load(st), "route", false), "threat", true);
assert.strictEqual(L.save(st, s), true);
assert.deepStrictEqual(Object.keys(st.d), [L.STORAGE_KEY]);
assert.ok(!/:/.test(L.STORAGE_KEY), "the key has no map id in it");
const back = L.load(st);
assert.strictEqual(back.route, false); assert.strictEqual(back.threat, true); assert.strictEqual(back.bots, true);
assert.deepStrictEqual(L.load(st), back);
assert.strictEqual(L.set(def, "nope", true).nope, undefined);
// the per-map keys of the older page are not read
const old = mem(); old.d["maplayers:209"] = JSON.stringify({ route: false });
assert.deepStrictEqual(L.load(old), def);

// ---- storage that fails or holds junk never breaks the page
assert.deepStrictEqual(L.load(null), def);
assert.deepStrictEqual(L.load({ getItem() { throw new Error("blocked"); } }), def);
assert.strictEqual(L.save({ setItem() { throw new Error("full"); } }, def), false);
assert.strictEqual(L.save(null, def), false);
const junk = mem();
junk.d[L.STORAGE_KEY] = "{nope"; assert.deepStrictEqual(L.load(junk), def);
junk.d[L.STORAGE_KEY] = "[1]"; assert.deepStrictEqual(L.load(junk), def);
junk.d[L.STORAGE_KEY] = JSON.stringify({ route: "no", bots: false, zzz: 1 });
assert.strictEqual(L.load(junk).route, true); assert.strictEqual(L.load(junk).bots, false); assert.strictEqual(L.load(junk).zzz, undefined);
junk.d[L.STORAGE_KEY] = JSON.stringify({ route: false }); // saved before a layer was added: the new one takes its default
assert.strictEqual(L.load(junk).route, false); assert.strictEqual(L.load(junk).threat, false); assert.strictEqual(L.load(junk).trail, true);

// ---- availability
let av = L.availability({});
assert.strictEqual(av.grid.ok, true); assert.strictEqual(av.scale.ok, true);
assert.strictEqual(av.threat.ok, false); assert.ok(av.threat.why.length > 5);
assert.strictEqual(av.zonenames.ok, false);
av = L.availability({ trail: true, trailtime: true, threat: true, healer: true });
assert.strictEqual(av.threat.ok, true); assert.strictEqual(av.healring.ok, true); assert.strictEqual(av.trailtime.ok, true);
assert.strictEqual(av.heading.ok, false); assert.strictEqual(av.aggro.ok, false);
assert.deepStrictEqual(L.groups(), ["Map", "Plan", "Walk", "Party", "Mobs", "Marks", "Run"]);

// ---- guids: the feed prints them in different widths
assert.strictEqual(L.guidKey("0x00000000000002DC"), "2dc");
assert.strictEqual(L.guidKey("0x2dc"), "2dc");
assert.strictEqual(L.guidKey("0xF130001C4E00AC91"), "f130001c4e00ac91");
assert.strictEqual(L.guidKey("0x0000000000000000"), "");
["", null, undefined, 5, "zz", "0x"].forEach((g) => assert.strictEqual(L.guidKey(g), "", String(g)));

// ---- threat and target lines
const bots = [{ guid: "0x2DC", x: 0, y: 0, known: true }, { guid: "0x2DD", x: 10, y: 0, known: true, target_guid: "0xF130001C4E00AC91" }, { guid: "0x2DE", x: 5, y: 5, known: false }];
const mobs = [{ guid: "0xF130001C4E00AC91", x: 20, y: 0, target_guid: "0x00000000000002DC" }, { guid: "0xF130001C4E00AC92", x: 30, y: 0 },
  { guid: "0xF130001C4E00AC93", x: 40, y: 0, target_guid: "0x2DE" }, { guid: "0xF130001C4E00AC94", x: 50, y: 0, target_guid: "0xDEAD" }];
let tl = L.threatLines(mobs, bots);
assert.strictEqual(tl.length, 1);                 // the mob aimed at the unplaced bot, the one at nobody, and the unknown target draw nothing
assert.deepStrictEqual(tl[0].from, { x: 20, y: 0 }); assert.deepStrictEqual(tl[0].to, { x: 0, y: 0 });
tl = L.botTargetLines(bots, mobs);
assert.strictEqual(tl.length, 1);
assert.deepStrictEqual(tl[0].from, { x: 10, y: 0 }); assert.deepStrictEqual(tl[0].to, { x: 20, y: 0 });
assert.deepStrictEqual(L.threatLines([], bots), []);
assert.deepStrictEqual(L.botTargetLines([], mobs), []);

// ---- pull targets: the pack of the run, else the fighting hostiles
const m = (o) => Object.assign({ kind: "hostile", leg: -1, group: -1, engaged: false, cleared: false }, o);
const pm = [m({ id: 1, leg: 0, group: 1 }), m({ id: 2, leg: 0, group: 1 }), m({ id: 3, leg: 0, group: 0, cleared: true }), m({ id: 4, leg: 1, group: 1 }), m({ id: 5, kind: "neutral", leg: 0, group: 1 }), m({ id: 6, engaged: true })];
assert.deepStrictEqual(L.pullTargets(pm, { state: "running", leg: 0, group: 2 }).map((x) => x.id), [1, 2]);
assert.deepStrictEqual(L.pullTargets(pm, { state: "running", leg: 0, group: 0 }).map((x) => x.id), [6]);
assert.deepStrictEqual(L.pullTargets(pm, { state: "idle", leg: 0, group: 2 }).map((x) => x.id), [6]);
assert.deepStrictEqual(L.pullTargets([m({})], null), []);

// ---- heading
const trail = [[0, 0], [0, 10], [0, 20]];
let hd = L.heading(trail, [100, 105, 110], 112, 4);
close(hd.x, 0); close(hd.y, 1);
assert.strictEqual(L.heading(trail, [100, 105, 110], 200, 4), null);        // too old
assert.deepStrictEqual(Object.keys(L.heading(trail, null, 0, 4)), ["x", "y"]); // without times: no age check
assert.strictEqual(L.heading([[0, 0], [1, 1]], null, 0, 4), null);          // too short a walk
assert.strictEqual(L.heading([[0, 0]], null, 0), null);
assert.strictEqual(L.heading(null), null);
hd = L.heading([[0, 0], [3, 0], [6, 0], [9, 0]], null, 0, 4); close(hd.x, 1); close(hd.y, 0); // finds a point far enough back

// ---- time labels on the walked path
const tr = Array.from({ length: 101 }, (_, i) => [i, 0]), tt = Array.from({ length: 101 }, (_, i) => 1000 + i);
const stp = L.trailStamps(tr, tt, 2, 80, 8);
assert.strictEqual(stp[stp.length - 1].t, 1100);
for (let i = 1; i < stp.length; i++) assert.ok(Math.hypot(stp[i].x - stp[i - 1].x, stp[i].y - stp[i - 1].y) * 2 >= 80 - 1e-9);
assert.ok(stp.length >= 3 && stp.length <= 8);
assert.strictEqual(L.trailStamps(tr, tt, 2, 80, 2).length, 2);
assert.deepStrictEqual(L.trailStamps(tr, null, 2), []);
assert.deepStrictEqual(L.trailStamps(tr, [1, 2], 2), []);                   // out of step
assert.deepStrictEqual(L.trailStamps([[0, 0], [9, 9]], [0, 0], 2), []);     // no times

// ---- scale bar and grid
assert.deepStrictEqual(L.scaleBar(1, 110), { yd: 100, px: 100 });
assert.deepStrictEqual(L.scaleBar(4, 110), { yd: 20, px: 80 });
assert.strictEqual(L.scaleBar(900, 110).yd, 1);
assert.strictEqual(L.gridStep(1), 100); assert.strictEqual(L.gridStep(4), 20);
assert.deepStrictEqual(L.gridLines([-95, -30, 200, 100], 50).xs, [-50, 0, 50, 100]);
console.log("layers.js ok");
