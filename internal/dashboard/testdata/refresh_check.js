"use strict";
// Run by view_test.go under node: the refresh-rate picker (refresh.js): its options, labels, normalization, and the choice
// saved per browser (also when the storage throws or holds junk).
const assert = require("assert");
const R = require(process.argv[2]);

// ---- the options
assert.deepStrictEqual(R.OPTIONS, [1, 2, 5, 10, 30, 0], "1 / 2 / 5 / 10 / 30 s / paused");
assert.strictEqual(R.DEFAULT_SEC, 2, "the default is the poll interval: nothing is held back until the viewer picks");
assert.strictEqual(R.STORAGE_KEY, "tadash.refresh.v1", "per-dashboard key");
assert.strictEqual(R.label(0), "paused");
assert.strictEqual(R.label(1), "1 s");
assert.strictEqual(R.label(30), "30 s");
[["5", 5], [5, 5], ["0", 0], [0, 0], ["30", 30], ["7", 2], [7, 2], ["abc", 2], [null, 2], [undefined, 2], [NaN, 2], ["", 2], ["-1", 2], ["1e1", 2], [{}, 2]]
  .forEach(([v, want]) => assert.strictEqual(R.normalize(v), want, String(v)));

// ---- saved per browser, every read and write in try/catch
const mem = () => { const m = {}; return { getItem: (k) => (k in m ? m[k] : null), setItem: (k, v) => { m[k] = String(v); }, m }; };
let st = mem();
assert.strictEqual(R.load(st), 2, "nothing saved: the default");
assert.strictEqual(R.save(st, 10), true);
assert.strictEqual(st.m[R.STORAGE_KEY], "10");
assert.strictEqual(R.load(st), 10, "the choice survives a reload");
R.save(st, 0); assert.strictEqual(R.load(st), 0, "paused is saved too");
st.m[R.STORAGE_KEY] = "junk"; assert.strictEqual(R.load(st), 2, "a damaged value falls back");
R.save(st, 7); assert.strictEqual(st.m[R.STORAGE_KEY], "2", "a value that is no option is never stored");
const throwing = { getItem() { throw new Error("SecurityError"); }, setItem() { throw new Error("QuotaExceededError"); } };
assert.strictEqual(R.load(throwing), 2, "storage that throws on read: the default, no exception");
assert.strictEqual(R.save(throwing, 5), false, "storage that throws on write: reported, no exception");
assert.strictEqual(R.load(null), 2, "no storage at all (even asking for it threw)");
assert.strictEqual(R.save(null, 5), false);
assert.strictEqual(R.load({}), 2, "an object that is no storage");

// ---- the poll timer, on fake timers
function env() {
  const e = { polls: 0, forces: [], timers: [], nextId: 1, cleared: [] };
  e.setTimer = (fn, ms) => { const h = e.nextId++; e.timers.push({ h, ms, fn }); return h; };
  e.clearTimer = (h) => { e.cleared.push(h); e.timers = e.timers.filter((x) => x.h !== h); };
  e.fire = () => { const ts = e.timers.splice(0); ts.forEach((x) => x.fn()); };
  e.poll = (force) => { e.polls++; e.forces.push(force); };
  return e;
}
{
  // boot at a rate: one fetch at once, then the timer polls
  const e = env(), r = R.createRefresh(e.poll, e);
  assert.strictEqual(r.boot("5"), 5, "saved rate applies");
  assert.strictEqual(e.polls, 1, "boot fetches once");
  assert.deepStrictEqual(e.forces, [true], "boot fetch is unconditional");
  assert.strictEqual(e.timers.length, 1, "one timer stands");
  assert.strictEqual(e.timers[0].ms, 5000, "at the chosen rate");
  e.fire();
  assert.strictEqual(e.polls, 2, "the timer polls");
  assert.deepStrictEqual(e.forces, [true, false], "timer polls are guarded");
}
{
  // boot paused: one fetch (the page fills), then no timer
  const e = env(), r = R.createRefresh(e.poll, e);
  assert.strictEqual(r.boot("0"), 0, "paused applies");
  assert.strictEqual(e.polls, 1, "boot-paused still fetches once");
  assert.deepStrictEqual(e.forces, [true], "boot-paused fetch is unconditional");
  assert.strictEqual(e.timers.length, 0, "paused stands no timer");
  assert.ok(r.paused(), "paused reads back");
}
{
  // pause, then resume: resume re-polls once and re-arms; no two timers
  const e = env(), r = R.createRefresh(e.poll, e);
  r.boot("2");
  assert.strictEqual(e.timers.length, 1, "one timer at boot");
  r.set("0");
  assert.ok(r.paused(), "paused after set 0");
  assert.strictEqual(e.timers.length, 0, "pausing clears the timer");
  assert.strictEqual(e.polls, 1, "pausing fetches nothing");
  r.set("10");
  assert.strictEqual(e.polls, 2, "resume re-polls once at once");
  assert.deepStrictEqual(e.forces[1], false, "resume uses the guarded poll");
  assert.strictEqual(e.timers.length, 1, "resume re-arms one timer");
  assert.strictEqual(e.timers[0].ms, 10000, "at the new rate");
  r.set("1");
  assert.strictEqual(e.polls, 3, "a rate change re-polls");
  assert.strictEqual(e.timers.length, 1, "a rate change leaves one timer, not two");
  assert.deepStrictEqual(e.cleared.length >= 2, true, "old timers were cleared");
  r.stop();
  assert.strictEqual(e.timers.length, 0, "stop clears");
}

console.log("ok");
