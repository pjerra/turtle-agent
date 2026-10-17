"use strict";
// Run by view_test.go under node: the dashboard page's polling behaviour with the real refresh.js guard.
// It loads app.js in a stubbed browser environment and checks the boot-paused fetch, the stale-response guard on pause,
// that the in-flight flag clears after a failed fetch, and that a resume or rate change during a slow request is not lost
// (the turned-away poll runs as soon as the stale request settles), also when the change lands while the body decodes.
const assert = require("assert");
const fs = require("fs");
const path = require("path");

const appPath = process.argv[2];
const refreshPath = process.argv[3];
if (!appPath || !refreshPath) { console.error("usage: app_poll_check.js <app.js> <refresh.js>"); process.exit(1); }

// ---- stubbed browser environment ----
globalThis.window = globalThis;
// a working storage that already holds "paused" from an earlier visit, so app.js's own boot is a reload of saved settings
const stored = new Map([["tadash.refresh.v1", "0"]]);
globalThis.localStorage = { getItem(k) { return stored.has(k) ? stored.get(k) : null; }, setItem(k, v) { stored.set(k, String(v)); } };
globalThis.location = { hash: "" };

const elements = new Map();
function makeEl(tag) {
  const kids = [];
  const listeners = {};
  return {
    tag,
    textContent: "",
    hidden: false,
    value: "",
    className: "",
    classList: {
      toggle(k, v) { if (v === undefined) { /* no-op */ } else if (v) { /* on */ } else { /* off */ } },
      add() {},
      remove() {}
    },
    style: { setProperty() {} },
    dataset: {},
    setAttribute() {},
    append(...ks) { for (const k of ks) if (k != null) kids.push(k); },
    appendChild(k) { kids.push(k); return k; },
    replaceChildren(...ks) { kids.length = 0; for (const k of ks) if (k != null) kids.push(k); },
    addEventListener(ev, fn) { (listeners[ev] = listeners[ev] || []).push(fn); },
    querySelector() { return makeEl("null"); }
  };
}

globalThis.document = {
  getElementById(id) {
    if (!elements.has(id)) elements.set(id, makeEl(id));
    return elements.get(id);
  },
  createElement(tag) { return makeEl(tag); },
  createElementNS(ns, tag) { return makeEl(tag); }
};

const timers = [];
let timerId = 1;
globalThis.setInterval = (fn) => { const id = timerId++; timers.push({ id, fn }); return id; };
globalThis.clearInterval = (id) => { for (let i = timers.length - 1; i >= 0; i--) if (timers[i].id === id) timers.splice(i, 1); };
globalThis.requestAnimationFrame = (cb) => setImmediate(cb);
globalThis.addEventListener = () => {};
globalThis.removeEventListener = () => {};

// module stubs used by app.js initialisation
const RF = require(refreshPath);
globalThis.RefreshRate = RF;
globalThis.MapView = {};
globalThis.PlainText = {};
globalThis.MapLayers = {
  defaults() { return {}; },
  styleDefaults() { return {}; },
  load() { return {}; },
  loadStyle() { return {}; },
  DEFS: [],
  groups() { return []; },
  STYLES: {},
  set(s, k, v) { const n = { ...s }; n[k] = v; return n; },
  save() {}
};
globalThis.UnitInfo = { LEGEND: [] };
globalThis.MapPaths = {};
globalThis.PathDraw = {};
globalThis.PlanView = {
  parseRoute() { return { kind: "runs" }; },
  planHash() { return ""; },
  planCard(name) { return { id: "plan:" + name, label: { plan: name } }; },
  normalizeSel() { return []; }
};
globalThis.RouteEdit = {};
globalThis.RealRoute = { routeUrl() { return ""; }, parseDoc() { return {}; } };
globalThis.RouteEditUI = { create() { return { ops() { return []; } }; } };

// ---- load app.js as the page does, its own boot included (the boot fetch is settled below, before the tests) ----
const appSrc = fs.readFileSync(appPath, "utf8");

// fetch stub: records /api/state and other requests so tests can resolve them by hand.
let fetches = [];
function ok(body) {
  return { ok: true, json() { return Promise.resolve(body); } };
}
globalThis.fetch = (url) => {
  let resolve, reject;
  const p = new Promise((res, rej) => { resolve = res; reject = rej; });
  fetches.push({ url, resolve, reject });
  return p;
};

// eval app.js in this context, then expose the page objects the harness needs to drive.
eval(appSrc + "\nglobalThis.__dashboard = { poll, refresh, setRefresh, render, pollPlans, cards, polling, pollGen, resetPlanClock() { planListAt = 0; } };\n");
const { poll, refresh, setRefresh, cards, resetPlanClock } = globalThis.__dashboard;

const summary = document.getElementById("summary");
const updated = document.getElementById("updated");
const routeSel = document.getElementById("route");

function flush() { return new Promise((r) => setImmediate(r)); }
function fetchCount(url) { return fetches.filter((x) => x.url === url).length; }
const picker = document.getElementById("refresh");

(async function run() {
  // Test 0: the page's own boot reloads the saved "paused": the select shows it, nothing is armed, one state fetch.
  {
    assert.strictEqual(picker.value, "0", "after a reload with paused saved, the select shows paused");
    assert.strictEqual(refresh.sec(), 0, "the saved paused is in force after boot");
    assert.strictEqual(timers.length, 0, "a reload while paused arms no timer");
    assert.strictEqual(fetchCount("/api/state"), 1, "a reload while paused fetches state once");
    fetches[0].resolve(ok({ agents: [], offline: [], time: "2026-10-18T00:00:00.000Z" }));
    await flush();
  }

  // Test 1: boot while paused fetches /api/state exactly once and shows the paused message.
  {
    fetches = [];
    timers.length = 0;
    refresh.boot("0");
    assert.strictEqual(fetches.length, 1, "boot-paused fetches /api/state once");
    assert.strictEqual(fetches[0].url, "/api/state", "the only boot fetch is state");
    assert.strictEqual(timers.length, 0, "boot-paused arms no timer");
    fetches[0].resolve(ok({ agents: [], offline: [], time: "2026-10-18T00:00:00.000Z" }));
    await flush();
    assert.strictEqual(updated.textContent, "paused: the map is frozen; pick a rate to resume", "paused message shown after boot fetch");
    assert.strictEqual(fetchCount("/api/state"), 1, "no second state fetch after boot");
  }

  // Test 2: an in-flight poll is ignored after pause (generation guard).
  {
    fetches = [];
    timers.length = 0;
    refresh.boot("2");
    assert.strictEqual(fetches.length, 1, "boot fetches once at 2 s");
    fetches[0].resolve(ok({ agents: [], offline: [], time: "2026-10-18T00:00:01.000Z" }));
    await flush();

    fetches = [];
    poll(false); // start a new interval poll by hand
    assert.strictEqual(fetches.length, 1, "manual poll starts one fetch");
    const p = fetches[0];

    // pause while the response is still in flight
    setRefresh("0");
    const pausedMessage = "paused: the map is frozen; pick a rate to resume";
    assert.strictEqual(updated.textContent, pausedMessage, "pause updates the message immediately");

    // now the old response arrives
    p.resolve(ok({ agents: [{ id: "a", online: true, character: "A", label: {} }], offline: [], time: "2026-10-18T00:00:02.000Z" }));
    await flush();
    assert.strictEqual(updated.textContent, pausedMessage, "stale response does not redraw updated");
    assert.strictEqual(summary.textContent.indexOf("1 agent"), -1, "stale response does not redraw summary");
    assert.strictEqual(fetchCount("/api/plans"), 0, "stale response does not call pollPlans");
    assert.strictEqual(fetchCount("/api/state"), 1, "pause alone starts no replacement fetch once the stale one settles");
  }

  // Test 3: the in-flight guard clears after a failed fetch (a new poll can start afterwards).
  {
    fetches = [];
    timers.length = 0;
    refresh.boot("2");
    fetches[0].resolve(ok({ agents: [], offline: [], time: "2026-10-18T00:00:03.000Z" }));
    await flush();

    fetches = [];
    poll(false); // start a poll that will fail
    assert.strictEqual(fetches.length, 1, "first failing poll starts");

    // a second poll must bail because the first is still in flight
    poll(false);
    assert.strictEqual(fetches.length, 1, "second poll blocked while in flight");

    fetches[0].reject(new Error("network"));
    await flush();

    // after the failure the guard is clear, so a third poll can start
    poll(false);
    assert.strictEqual(fetches.length, 2, "third poll starts after failure cleared the guard");
    fetches[1].resolve(ok({ agents: [], offline: [], time: "2026-10-18T00:00:04.000Z" }));
    await flush();
  }

  const stateFetches = () => fetches.filter((x) => x.url === "/api/state");
  const doc = (tag, t) => ({ agents: [], offline: [tag], time: "2026-10-18T00:00:" + t + ".000Z" });

  // Test 4: pause, then resume while the pre-pause fetch is still unresolved. The resume poll meets the in-flight guard;
  // it must not be lost: the stale answer does not render, and a fresh /api/state request starts as soon as it settles.
  {
    fetches = [];
    timers.length = 0;
    refresh.boot("2");
    fetches[0].resolve(ok(doc("BOOT", "05")));
    await flush();

    fetches = [];
    poll(false); // the slow pre-pause request
    assert.strictEqual(stateFetches().length, 1, "slow poll starts");
    setRefresh("0");
    setRefresh("2"); // resume while the old request is still out
    assert.strictEqual(stateFetches().length, 1, "resume does not overlap the in-flight request");

    stateFetches()[0].resolve(ok(doc("STALE", "06")));
    await flush();
    assert.strictEqual(summary.textContent.indexOf("STALE"), -1, "stale pre-pause response does not render after resume");
    assert.strictEqual(stateFetches().length, 2, "resume poll runs right after the stale request settles (no timer tick)");

    stateFetches()[1].resolve(ok(doc("FRESH", "07")));
    await flush();
    assert.ok(summary.textContent.indexOf("FRESH") >= 0, "fresh response after resume renders: " + summary.textContent);
    assert.ok(updated.textContent.startsWith("updated "), "fresh response sets the updated line: " + updated.textContent);
    assert.strictEqual(stateFetches().length, 2, "exactly one replacement fetch");
  }

  // Test 5: changing the rate during a slow request: the stale answer does not render, the replacement fetch follows it,
  // also when the stale request fails; a plain tick that meets a current request is still dropped.
  {
    fetches = [];
    timers.length = 0;
    refresh.boot("2");
    fetches[0].resolve(ok(doc("BOOT", "08")));
    await flush();

    fetches = [];
    poll(false);
    setRefresh("5");
    assert.strictEqual(stateFetches().length, 1, "rate change does not overlap the in-flight request");
    stateFetches()[0].resolve(ok(doc("STALE", "09")));
    await flush();
    assert.strictEqual(summary.textContent.indexOf("STALE"), -1, "stale response from before the rate change does not render");
    assert.strictEqual(stateFetches().length, 2, "rate-change poll runs right after the stale request settles");
    stateFetches()[1].resolve(ok(doc("FRESH5", "10")));
    await flush();
    assert.ok(summary.textContent.indexOf("FRESH5") >= 0, "fresh response at the new rate renders: " + summary.textContent);

    // the stale request fails instead: the replacement still starts and the guard is clear
    fetches = [];
    poll(false);
    setRefresh("10");
    assert.strictEqual(stateFetches().length, 1, "second rate change does not overlap");
    stateFetches()[0].reject(new Error("network"));
    await flush();
    assert.ok(updated.textContent.indexOf("not reachable") === -1, "stale failure does not show an error");
    assert.strictEqual(stateFetches().length, 2, "replacement starts after a failed stale request");
    stateFetches()[1].resolve(ok(doc("FRESH10", "11")));
    await flush();
    assert.ok(summary.textContent.indexOf("FRESH10") >= 0, "replacement after failure renders");

    // a plain tick during a current (not stale) request is dropped, not queued
    fetches = [];
    poll(false);
    poll(false);
    assert.strictEqual(stateFetches().length, 1, "tick blocked while in flight");
    stateFetches()[0].resolve(ok(doc("CUR", "12")));
    await flush();
    assert.strictEqual(stateFetches().length, 1, "a tick that met a current request is not re-run");
    assert.ok(summary.textContent.indexOf("CUR") >= 0, "current response renders");
  }

  // A response whose body is still decoding (json() pending) when the page pauses or changes rate: once it decodes it is
  // stale, so nothing may follow from it: no render, no plan fetch, no debug-panel fetch.
  const slowBody = (body) => {
    let resolve;
    const p = new Promise((res) => { resolve = res; });
    return { r: { ok: true, json() { return p; } }, resolve: () => resolve(body) };
  };
  const fakeCard = () => ({ el: { remove() {} }, parts: { debug: { open: true, querySelector() { return makeEl("pre"); } } } });
  const otherFetches = () => fetches.filter((x) => x.url !== "/api/state");

  // Test 6: pause while the body decodes.
  {
    fetches = [];
    timers.length = 0;
    refresh.boot("2");
    fetches[0].resolve(ok(doc("BOOT6", "13")));
    await flush();

    fetches = [];
    cards.set("dbg", fakeCard()); // an open debug panel; a render of an agent-less doc would drop it
    resetPlanClock();            // so a pollPlans call would fetch /api/plans
    const before = summary.textContent;
    poll(false);
    const sb = slowBody(doc("STALEBODY", "14"));
    stateFetches()[0].resolve(sb.r);
    await flush(); // fetch resolved, json() still pending
    setRefresh("0");
    const pausedText = updated.textContent;
    sb.resolve();
    await flush();
    assert.strictEqual(summary.textContent, before, "body decoded after pause does not render");
    assert.strictEqual(updated.textContent, pausedText, "body decoded after pause leaves the paused line");
    assert.ok(cards.has("dbg"), "body decoded after pause does not redraw the cards");
    assert.strictEqual(otherFetches().length, 0, "body decoded after pause fetches no plans and no debug panel: " + otherFetches().map((x) => x.url));
    assert.strictEqual(stateFetches().length, 1, "pause starts no replacement fetch");
    cards.delete("dbg");
  }

  // Test 7: pause, then resume, while the body decodes: the stale body is not rendered and a fresh poll starts.
  {
    fetches = [];
    timers.length = 0;
    refresh.boot("2");
    fetches[0].resolve(ok(doc("BOOT7", "15")));
    await flush();

    fetches = [];
    cards.set("dbg", fakeCard());
    resetPlanClock();
    poll(false);
    const sb = slowBody(doc("STALEBODY7", "16"));
    stateFetches()[0].resolve(sb.r);
    await flush();
    setRefresh("0");
    setRefresh("2"); // resume while the old body is still decoding: turned away by the in-flight guard
    assert.strictEqual(stateFetches().length, 1, "resume does not overlap the decoding request");
    sb.resolve();
    await flush();
    assert.strictEqual(summary.textContent.indexOf("STALEBODY7"), -1, "stale body is not rendered after resume");
    assert.ok(cards.has("dbg"), "stale body does not redraw the cards");
    assert.strictEqual(otherFetches().length, 0, "stale body fetches no plans and no debug panel");
    assert.strictEqual(stateFetches().length, 2, "fresh poll starts right after the stale body settles");
    cards.delete("dbg");
    stateFetches()[1].resolve(ok(doc("FRESH7", "17")));
    await flush();
    assert.ok(summary.textContent.indexOf("FRESH7") >= 0, "fresh poll after resume renders: " + summary.textContent);
  }

  // Test 8: the select shows the rate in force after pause, resume and a rate change, and the choice is saved for the
  // next reload.
  {
    fetches = [];
    timers.length = 0;
    refresh.boot("2");
    fetches[0].resolve(ok(doc("BOOT8", "18")));
    await flush();
    const settle = async () => { for (const f of stateFetches()) f.resolve(ok(doc("S8", "19"))); await flush(); };

    setRefresh("0");
    assert.strictEqual(picker.value, "0", "after pause the select shows paused");
    assert.strictEqual(stored.get("tadash.refresh.v1"), "0", "pause is saved");
    setRefresh("2");
    assert.strictEqual(picker.value, "2", "after resume the select shows the resumed rate");
    await settle();
    setRefresh("10");
    assert.strictEqual(picker.value, "10", "after a rate change the select shows the new rate");
    assert.strictEqual(stored.get("tadash.refresh.v1"), "10", "the rate change is saved");
    assert.strictEqual(RF.load(localStorage), 10, "a reload reads the saved rate back");
    await settle();
  }

  console.log("ok");
})().catch((e) => { console.error(e); process.exit(1); });
