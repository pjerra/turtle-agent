"use strict";
// Run by view_test.go under node: the live-tuning panel's order, steps, clamp and queued text (ctltune.js).
const assert = require("assert");
const T = require(process.argv[2]);

assert.deepStrictEqual(T.FEATURED, ["rest_health_pct", "rest_mana_pct", "rest_healer_mana_pct", "big_pack", "patrol_wait_s", "pull_range", "pack_radius", "gather_wait_s"]);

const row = (key, unit, min, max, o) => Object.assign({ key, unit, min, max, default: min, base: min, value: min, source: "base", doc: "" }, o || {});

// rows: the featured ones first in FEATURED order, a missing key skipped, then the rest in tune_list's order; no card, no rows.
const card = { tune_all: [row("pull_range", "yd", 5, 40), row("zznote", "s", 0, 5), row("rest_health_pct", "pct", 0, 100), row("big_pack", "count", 0, 1)] };
assert.deepStrictEqual(T.rows(card).map((r) => r.key), ["rest_health_pct", "big_pack", "pull_range", "zznote"]);
assert.deepStrictEqual(T.rows({}).map((r) => r.key), []);
assert.deepStrictEqual(T.rows(null), []);
assert.deepStrictEqual(T.rows({ tune_all: [null, { min: 1 }, row("a", "yd", 0, 1)] }).map((r) => r.key), ["a"]);

// stepOf: whole pct and count, half yd and s.
assert.strictEqual(T.stepOf("pct"), 1);
assert.strictEqual(T.stepOf("count"), 1);
assert.strictEqual(T.stepOf("yd"), 0.5);
assert.strictEqual(T.stepOf("s"), 0.5);

// clamp: inside the limits, whole for pct/count, on the unit's step.
assert.strictEqual(T.clamp(row("rest_health_pct", "pct", 0, 100), 70.5), 71);
assert.strictEqual(T.clamp(row("rest_health_pct", "pct", 0, 100), 101), 100);
assert.strictEqual(T.clamp(row("rest_health_pct", "pct", 0, 100), -3), 0);
assert.strictEqual(T.clamp(row("pull_range", "yd", 5, 40), 100), 40);
assert.strictEqual(T.clamp(row("pull_range", "yd", 5, 40), 1), 5);
assert.strictEqual(T.clamp(row("pull_range", "yd", 5, 40), 21.3), 21.5);
assert.strictEqual(T.clamp(row("big_pack", "count", 0, 1), 1.4), 1);

// queuedText: only while something waits for the next boundary.
assert.strictEqual(T.queuedText(row("rest_health_pct", "pct", 0, 100, { queued: 70 })), "queued -> 70 (applies at the next leg or pull-group start)");
assert.strictEqual(T.queuedText(row("rest_health_pct", "pct", 0, 100)), "");
assert.strictEqual(T.queuedText(null), "");

// canTune: only a running remote/commander card with tunables.
const base = { online: true, run: { state: "running", mode: "remote" }, tune_all: [row("a", "yd", 0, 1)] };
assert.strictEqual(T.canTune(base), true);
assert.strictEqual(T.canTune(Object.assign({}, base, { run: { state: "running", mode: "observe" } })), false);
assert.strictEqual(T.canTune(Object.assign({}, base, { run: { state: "stopped", mode: "remote" } })), false);
assert.strictEqual(T.canTune(Object.assign({}, base, { tune_all: [] })), false);
assert.strictEqual(T.canTune(Object.assign({}, base, { online: false })), false);

console.log("ok");
