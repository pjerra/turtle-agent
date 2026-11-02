"use strict";
// The live-tuning panel of the run controls (README "Controls (--control)", issue #183 slice 2, D13/D14). The rows, the step of a unit, the
// clamp and the queued text are pure functions of the card (tested under node); the panel is a <details> in the Controls box that sends one
// tune per slider release (change, never input) or per Default press.
(function (root) {
  // The eight sliders every remote run starts with, in this order (spec section 4). A key this run does not have is skipped.
  const FEATURED = ["rest_health_pct", "rest_mana_pct", "rest_healer_mana_pct", "big_pack", "patrol_wait_s", "pull_range", "pack_radius", "gather_wait_s"];

  // stepOf is the slider step of a tunable's unit: whole pct and count, half yards and seconds.
  function stepOf(unit) {
    if (unit === "pct" || unit === "count") return 1;
    if (unit === "yd" || unit === "s") return 0.5;
    return 0.1;
  }

  // rows lists a card's tunables: the featured ones first, in FEATURED order, then the rest in the order tune_list gave.
  function rows(card) {
    const all = card && Array.isArray(card.tune_all) ? card.tune_all.filter((r) => r && typeof r.key === "string") : [];
    const byKey = new Map(all.map((r) => [r.key, r]));
    const out = [];
    for (const k of FEATURED) { const r = byKey.get(k); if (r) { out.push(r); byKey.delete(k); } }
    for (const r of all) if (byKey.has(r.key)) { out.push(r); byKey.delete(r.key); }
    return out;
  }

  // clamp puts a value inside the row's limits and on its unit's step (pct and count stay whole).
  function clamp(row, v) {
    let n = Number(v);
    if (!Number.isFinite(n)) n = row.min;
    n = Math.min(Math.max(n, row.min), row.max);
    if (row.unit === "pct" || row.unit === "count") return Math.round(n);
    const step = stepOf(row.unit);
    n = Math.round(n / step) * step;
    return Math.min(Math.max(n, row.min), row.max);
  }

  // queuedText says what waits for the next boundary, and nothing when nothing does.
  function queuedText(row) {
    if (!row || row.queued === null || row.queued === undefined) return "";
    return "queued -> " + row.queued + " (applies at the next leg or pull-group start)";
  }

  // canTune: the panel exists only while a run goes in remote/commander mode and the card carries tunables.
  function canTune(card) {
    return !!(card && card.online && card.run && card.run.state === "running" &&
      (card.run.mode === "remote" || card.run.mode === "commander") && rows(card).length > 0);
  }

  function create(env) {
    const el = env.h("details", { class: "ctltune", hidden: true });
    const body = env.h("div", { class: "ctltune-body" });
    el.append(env.h("summary", { text: "Live tuning" }), body);
    let builtKey = "";
    let entries = [];

    function send(row, value) {
      env.post({ key: row.key, value: value }).then((r) => {
        if (r.status === 401) env.say("Wrong or missing control token.");
        // The command ran; only its log line failed. Said once, with no retry offered: a retry would send a second tune_set.
        else if (r.json && r.json.executed === true && r.json.log_failed === true) env.say(row.key + " set (control log write failed).");
        else if (r.json && r.json.status === "ok") env.say(row.key + " -> " + (value === "default" ? "default" : r.json.result && r.json.result.new !== undefined ? r.json.result.new : value) + ": " + ((r.json.result && r.json.result.applies) || "queued"));
        else if (r.json && typeof r.json.message === "string" && r.json.message.indexOf("unknown:") === 0) env.say(r.json.message);
        else env.say("Refused: " + ((r.json && r.json.message) || ("HTTP " + r.status)));
        env.after();
      });
    }

    function sliderRow(row) {
      const input = env.h("input", { type: "range", min: String(row.min), max: String(row.max), step: String(stepOf(row.unit)), value: String(row.value), "aria-label": row.key });
      const value = env.h("span", { class: "tune-val" });
      const queued = env.h("span", { class: "tune-queued" });
      const def = env.h("button", { type: "button", class: "mb tunebtn", text: "Default", title: "Put " + row.key + " back to its file or driver value." });
      const line = env.h("label", { class: "tune-row", title: row.doc || "" }, env.h("span", { class: "tune-key", text: row.key }), input, value, def, queued);
      const e = { row, line, input, value, queued };
      input.addEventListener("change", () => { input.value = String(clamp(e.row, input.value)); send(e.row, Number(input.value)); });
      def.addEventListener("click", () => send(e.row, "default"));
      return e;
    }

    function numberRow(row) {
      const input = env.h("input", { type: "number", min: String(row.min), max: String(row.max), step: String(stepOf(row.unit)), value: String(row.value), "aria-label": row.key });
      const set = env.h("button", { type: "button", class: "mb tunebtn", text: "Set" });
      const def = env.h("button", { type: "button", class: "mb tunebtn", text: "Default" });
      const queued = env.h("span", { class: "tune-queued" });
      const line = env.h("label", { class: "tune-row", title: row.doc || "" }, env.h("span", { class: "tune-key", text: row.key }), input, set, def, queued);
      const e = { row, line, input, value: null, queued };
      set.addEventListener("click", () => { const v = clamp(e.row, input.value); input.value = String(v); send(e.row, v); });
      def.addEventListener("click", () => send(e.row, "default"));
      return e;
    }

    function build(list) {
      const featured = list.filter((r) => FEATURED.indexOf(r.key) >= 0);
      const sliders = featured.map((r) => sliderRow(r));
      const allEntries = list.map((r) => numberRow(r));
      const allBody = env.h("div", { class: "ctltune-rows" });
      for (const e of allEntries) allBody.append(e.line);
      const all = env.h("details", { class: "ctltune-all" }, env.h("summary", { text: "All tunables" }), allBody);
      entries = sliders.concat(allEntries);
      body.replaceChildren(...sliders.map((e) => e.line), all);
      builtKey = list.map((r) => r.key + ":" + r.unit).join("|");
      update(list);
    }

    function update(list) {
      const byKey = new Map(list.map((r) => [r.key, r]));
      for (const e of entries) {
        const row = byKey.get(e.row.key);
        if (!row) continue;
        e.row = row;
        e.queued.textContent = queuedText(row);
        if (e.value) e.value.textContent = row.value + (row.unit ? " " + row.unit : "");
        if (document.activeElement !== e.input) e.input.value = String(row.value);
      }
    }

    return {
      el,
      render(card) {
        if (!canTune(card)) { el.hidden = true; return; }
        el.hidden = false;
        const list = rows(card);
        const key = list.map((r) => r.key + ":" + r.unit).join("|");
        if (key !== builtKey) build(list); else update(list);
      },
    };
  }

  const api = { FEATURED, stepOf, rows, clamp, queuedText, canTune, create };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.CTLT = api;
})(typeof self !== "undefined" ? self : this);
