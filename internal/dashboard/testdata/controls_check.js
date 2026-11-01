"use strict";
// Run by view_test.go under node: the run controls' button matrix, the confirm texts and the log lines.
const assert = require("assert");
const C = require(process.argv[2]);

const card = (o) => Object.assign({ id: "8765", online: true, character: "Tanky", label: { plan: "zulfarrak" }, run: { state: "running", mode: "" } }, o);
const verbs = (c) => C.runButtons(c).map((b) => b.verb);
const btn = (c, v) => C.runButtons(c).find((b) => b.verb === v);

// idle: only unbind, with a confirm
assert.deepStrictEqual(verbs(card({ run: { state: "stopped" } })), ["stop"]);
assert.strictEqual(btn(card({ run: { state: "stopped" } }), "stop").label, "Unbind kept party");
assert.strictEqual(btn(card({ run: { state: "stopped" } }), "stop").confirm, true);

// a group/leader run (mode ""): stop run only, confirm
assert.deepStrictEqual(verbs(card({ run: { state: "running", mode: "" } })), ["stop"]);
assert.strictEqual(btn(card({ run: { state: "running", mode: "" } }), "stop").label, "Stop run");
assert.strictEqual(btn(card({ run: { state: "running", mode: "" } }), "stop").confirm, true);

// an observe run: pause, resume, skip, stop
const obs = card({ run: { state: "running", mode: "observe", leader: { state: "running" } } });
assert.deepStrictEqual(verbs(obs), ["pause", "resume", "skip", "stop"]);
assert.strictEqual(btn(obs, "pause").enabled, true);
assert.strictEqual(btn(obs, "pause").confirm, false);
assert.strictEqual(btn(obs, "resume").enabled, false, "resume is off while the run leader is running");
assert.strictEqual(btn(obs, "skip").enabled, true);
assert.strictEqual(btn(obs, "stop").confirm, true);

// an observe run paused: resume on, pause off
const obsPaused = card({ run: { state: "running", mode: "observe", leader: { state: "paused" } } });
assert.strictEqual(btn(obsPaused, "pause").enabled, false);
assert.strictEqual(btn(obsPaused, "resume").enabled, true);

// a commander (remote) run: stop-keep-party (pause) and stop-and-unbind (stop), both with a confirm
for (const mode of ["remote", "commander"]) {
  const c = card({ run: { state: "running", mode } });
  assert.deepStrictEqual(verbs(c), ["pause", "stop"], mode);
  assert.strictEqual(btn(c, "pause").label, "Stop, keep party");
  assert.strictEqual(btn(c, "pause").confirm, true);
  assert.strictEqual(btn(c, "stop").label, "Stop and unbind");
  assert.strictEqual(btn(c, "stop").confirm, true);
}

// offline and synthetic cards have no controls
assert.deepStrictEqual(C.runButtons(card({ online: false })), []);
assert.deepStrictEqual(C.runButtons(null), []);
assert.deepStrictEqual(C.runButtons({ id: "1", online: true, synthetic: true }), []);

// the confirm texts name the character and the plan
assert.ok(C.confirmText(card({ run: { state: "running", mode: "remote" } }), "pause").indexOf("Tanky") >= 0);
assert.ok(C.confirmText(card({ run: { state: "running", mode: "remote" } }), "pause").indexOf("zulfarrak") >= 0);
assert.ok(C.confirmText(card({ run: { state: "running", mode: "remote" } }), "stop").indexOf("zulfarrak") >= 0);
assert.ok(C.confirmText(card({ run: { state: "stopped" } }), "stop").indexOf("zulfarrak") >= 0);
assert.ok(/resume/.test(C.confirmText(card({ run: { state: "running", mode: "remote" } }), "pause")));

// the log lines: time, who, what
const lines = C.logLines({ lines: [{ at: "2026-11-01T12:34:56Z", who: "owner", via: "trusted", verb: "pause", ok: true },
  { at: "2026-11-01T12:35:00Z", who: "owner", verb: "stop", ok: false, message: "boo" }] });
assert.strictEqual(lines.length, 2);
assert.ok(lines[0].indexOf("12:34:56") >= 0 && lines[0].indexOf("owner") >= 0 && lines[0].indexOf("pause ok") >= 0 && lines[0].indexOf("trusted") >= 0);
assert.ok(lines[1].indexOf("stop failed") >= 0 && lines[1].indexOf("boo") >= 0);
assert.deepStrictEqual(C.logLines(null), []);

console.log("ok");
