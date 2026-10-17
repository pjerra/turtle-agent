"use strict";
// The plan view (#/plan/<name>): a plan drawn with no run, its legs switched on and off, and the words that say which copy of the plan is
// drawn and which copy a run reads. Kept free of the page so a test can run it under node.
(function (root) {
  // The page address: "#/plan/<name>" is the plan view of a plan, anything else the run cards.
  const NAME_OK = /^[a-z0-9][a-z0-9_-]{0,39}$/;
  function parseRoute(hash) {
    const m = /^#\/plan\/([^/?#]+)$/.exec(hash || "");
    if (m) {
      let name = "";
      try { name = decodeURIComponent(m[1]); } catch (e) { name = ""; }
      if (NAME_OK.test(name)) return { kind: "plan", plan: name };
    }
    return { kind: "cards", plan: "" };
  }
  const planHash = (name) => (NAME_OK.test(name || "") ? "#/plan/" + encodeURIComponent(name) : "#/");

  // ---- which legs are shown ----
  // A selection is {mode, only, off}: "all" (every leg), "only" (the leg `only`), "custom" (every leg not in `off`) or "follow" (the leg a live run
  // is in, and the next one).
  const allLegs = () => ({ mode: "all", only: -1, off: [] });
  function normalizeSel(sel, n) {
    const s = sel && typeof sel === "object" ? sel : {};
    const mode = ["all", "only", "custom", "follow"].includes(s.mode) ? s.mode : "all";
    const off = Array.isArray(s.off) ? [...new Set(s.off.filter((i) => Number.isInteger(i) && i >= 0 && i < n))].sort((a, b) => a - b) : [];
    const only = Number.isInteger(s.only) && s.only >= 0 && s.only < n ? s.only : -1;
    return { mode: mode === "only" && only < 0 ? "all" : mode, only, off };
  }
  // The legs shown, as a sorted array of indexes. runLeg is the live run's leg (-1 for none): "follow" shows it and the next leg, and every leg without a run.
  function visibleLegs(sel, n, runLeg) {
    const s = normalizeSel(sel, n), out = [];
    for (let i = 0; i < n; i++) {
      const on = s.mode === "all" ? true : s.mode === "only" ? i === s.only : s.mode === "follow" ? (runLeg == null || runLeg < 0 ? true : i === runLeg || i === runLeg + 1) : !s.off.includes(i);
      if (on) out.push(i);
    }
    return out;
  }
  function toggleLeg(sel, i, n, runLeg) {
    const shown = new Set(visibleLegs(sel, n, runLeg));
    if (shown.has(i)) shown.delete(i); else shown.add(i);
    const off = [];
    for (let k = 0; k < n; k++) if (!shown.has(k)) off.push(k);
    return normalizeSel({ mode: "custom", only: -1, off }, n);
  }
  const onlyLeg = (i, n) => normalizeSel({ mode: "only", only: i, off: [] }, n);
  const hideAll = (n) => normalizeSel({ mode: "custom", only: -1, off: Array.from({ length: n }, (_, i) => i) }, n);

  // The plan with the legs that are not shown emptied (their route, packs, objects, marks), the array keeping its length so that every leg number stays.
  function filterGeo(geo, shown) {
    if (!geo || !geo.legs) return geo;
    const on = new Set(shown);
    if (on.size === geo.legs.length) return geo;
    return Object.assign({}, geo, { legs: geo.legs.map((l, i) => (on.has(i) ? l : Object.assign({}, l, { path: [], stops: [], packs: [], objects: [], marks: [], looks: [], boss: 0, boss_at: null }))) });
  }

  // The tight box {x, y, w, h}... as [x, y, w, h] of what the shown legs draw, or null when nothing is shown.
  function boundsOf(geo, shown) {
    if (!geo || !geo.legs) return null;
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    const add = (x, y, r) => { if (isFinite(x) && isFinite(y)) { x0 = Math.min(x0, x - r); y0 = Math.min(y0, y - r); x1 = Math.max(x1, x + r); y1 = Math.max(y1, y + r); } };
    for (const i of shown) {
      const l = geo.legs[i];
      if (!l) continue;
      (l.path || []).forEach((p) => add(p.x, p.y, 0));
      (l.packs || []).forEach((p) => (p.points && p.points.length ? p.points : [{ x: p.x, y: p.y }]).forEach((q) => add(q.x, q.y, p.r || 0)));
      (l.objects || []).forEach((o) => add(o.x, o.y, 2));
      (l.marks || []).forEach((m) => add(m.x, m.y, m.r || 0));
      if (l.boss_at) add(l.boss_at.x, l.boss_at.y, 6);
    }
    return x0 === Infinity ? null : [x0, y0, x1 - x0, y1 - y0];
  }

  // ---- which copy ----
  const short = (src) => (src && src.sha256 ? src.sha256.slice(0, 8) : "?");

  // The badge of a run card: how the copy the run reads compares with the one the dashboard keeps. level is "ok", "warn" or "info".
  function copyBadge(pc) {
    if (!pc) return null;
    if (pc.run && pc.changed) return { level: "warn", text: "the run's file changed after the run read it (it read " + (pc.run_sha256 || "?").slice(0, 8) + ", now " + short(pc.run) + ")" };
    if (pc.run && pc.same) return { level: "ok", text: "run reads the same file as the dashboard's copy" };
    if (pc.run) return { level: "warn", text: "run's copy " + short(pc.run) + " differs from the dashboard's" + (pc.dash ? " " + short(pc.dash) : "") };
    return { level: "warn", text: "run's copy not seen: the map shows the dashboard's" };
  }

  // The badge for a run started with a route edit (trace_meta.route_edit, "<plan>@v0007 sha256=<hex>", as `turtle-agent route-edits export` prints it):
  // ok when the file the run last read is that edit, warn when it is another (the run reloaded another version, or the tag was not for this file).
  function routeEditBadge(tag, pc) {
    const m = /^([a-z0-9][a-z0-9_-]{0,39})@(v\d{4}) sha256=([0-9a-f]{64})$/.exec(tag || "");
    if (!m) return null;
    const read = pc ? pc.run_sha256 || (pc.run && pc.run.sha256) || (pc.drawn && pc.drawn.sha256) : "";
    if (!read) return { level: "info", version: m[2], text: "run started with route edit " + m[2] + " (the file it read is not known)" };
    if (read === m[3]) return { level: "ok", version: m[2], text: "run uses route edit " + m[2] };
    return { level: "warn", version: m[2], text: "run was started with route edit " + m[2] + ", but the file it read is another version (" + read.slice(0, 8) + ")" };
  }

  // Whether the plan drawn can be the plan the run walks: the leg the run says it is in (its number and name) must be in the drawing under that
  // number with that name. "" when it fits (or there is nothing to compare), else the reason, for the card to show instead of a drawing that
  // would put the party on the wrong leg.
  function planMismatch(geo, run) {
    if (!geo || !geo.legs || !run || !(run.leg >= 0)) return "";
    const l = geo.legs[run.leg];
    if (!l) return "the run is in leg " + (run.leg + 1) + ", the drawn plan has only " + geo.legs.length + " legs";
    if (run.leg_name && l.name && l.name !== run.leg_name) return "the run is in leg " + (run.leg + 1) + " (" + run.leg_name + "), the drawn plan's leg " + (run.leg + 1) + " is " + l.name;
    return "";
  }

  // A line for the plan view about one slot's copy of the plan (a row of /api/plan-copies).
  function copyRowText(r) {
    if (!r) return "";
    if (r.run) return (r.same ? "same as" : "differs from") + " the dashboard's copy; reads " + r.run.path + " (" + short(r.run) + ")";
    if (!r.dungeons_dir) return "settings name no dungeons_dir: reads dungeons/ under its own working folder (not seen)";
    return "has no copy of this plan in " + r.dungeons_dir;
  }

  // The card the plan view draws with: a plan with no run, shaped like a run card so that the map code takes it as it is.
  function planCard(name, geo) {
    return {
      id: "plan:" + name, online: true, synthetic: true, character: "", level: 0, map: geo && geo.map ? geo.map : 0,
      label: { plan: name, map: geo && geo.map ? geo.map : 0, folder: "", run_id: "" },
      run: { state: "idle", phase: "", leg: -1, group: 0, groups: 0, minutes: 0, leg_count: geo && geo.legs ? geo.legs.length : 0, leg_name: "" },
      stall: { level: "none" }, counters: {}, orders: [], changes: [], tunables: [], links: [], feed: { have: false }, bots: [], mobs: [], commander: null,
      trail: [], trail_t: [], events: [],
    };
  }

  const api = { parseRoute, planHash, allLegs, normalizeSel, visibleLegs, toggleLeg, onlyLeg, hideAll, filterGeo, boundsOf, copyBadge, routeEditBadge, copyRowText, planMismatch, planCard, short };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.PlanView = api;
})(typeof self !== "undefined" ? self : this);
