"use strict";
// Live dashboard page. Everything from the agents is put in the page with textContent, never as markup; the only markup
// inserted is the plan drawing, which the server builds with escaped text.
const SVGNS = "http://www.w3.org/2000/svg";
const MV = window.MapView, PT = window.PlainText, ML = window.MapLayers, UI = window.UnitInfo, MP = window.MapPaths, PD = window.PathDraw, PV = window.PlanView, RE = window.RouteEdit, RR = window.RealRoute; // planview.js (the plan view and its leg switches), view.js (zoom and pan math), text.js (plain wording), layers.js (what a map can draw), unitinfo.js (what a unit is doing, the hover box, events, progress)
const POLL_MS = 1500;
const $ = (id) => document.getElementById(id);
const cards = new Map(); // id -> {el, parts, geo}
const plans = new Map(); // name -> geometry | "loading" | null
const minimaps = new Map(); // map id -> [layer] | "loading" | null (no image for that map; looked for again after a while)
const minimapMiss = new Map(); // map id -> when it was found to have no image
let view = "grid", activeTab = null, last = null;
let dim = 30, followPref = true; // dim: percent the game map is darkened under the drawing; followPref: follow the party (per browser)
try { view = localStorage.getItem("view") === "tabs" ? "tabs" : "grid"; } catch (e) { /* storage may be blocked */ }
try { const d = parseInt(localStorage.getItem("mapdim"), 10); if (d >= 0 && d <= 80) dim = d; followPref = localStorage.getItem("mapfollow") !== "off"; } catch (e) { /* ignore */ }
const GROUP_COLORS = ["#ff6ec7", "#7dd3fc", "#a3e635", "#f0abfc", "#fdba74", "#5eead4"]; // a trail and dots colour per group (per agent)
let hiddenGroups = new Set();
try { hiddenGroups = new Set(JSON.parse(localStorage.getItem("grouphide") || "[]")); } catch (e) { /* ignore */ }
// What can be drawn on a map is listed in layers.js. The choice is one setting for every map and every card, saved once; a layer is a class
// "hide-<key>" on the svg when it is off (the stylesheet), and greyed out in a card's panel when that card's map has no data for it.
let layerState = ML.defaults();
try { layerState = ML.load(window.localStorage); } catch (e) { /* storage may be blocked */ }
// The sliders of the layers (minutes shown, opacity, thickness) are saved once for all maps, like the choice of layers.
let styleState = ML.styleDefaults();
try { styleState = ML.loadStyle(window.localStorage); } catch (e) { /* storage may be blocked */ }
const ghosts = new Map(), heats = new Map(); // "<plan>|<run>" / plan -> {at, loading, data}: the best earlier run and the death places, from the server
const GHOST_TTL_MS = 300000, LIVE_EVERY_MS = 3000;
const store = (k, v) => { try { localStorage.setItem(k, v); } catch (e) { /* ignore */ } };
const namesOf = (c) => { const g = plans.get(planKeyOf(c)); return g && g.names ? g.names : {}; };
// The plan view (#/plan/<name>): the plan with no run. route says which page is shown; planList is /api/plans (the plan files the dashboard can draw);
// copyRows is /api/plan-copies/<plan> (which copy each slot's runs read); legSel holds which legs of a plan are switched on.
let route = PV.parseRoute(location.hash);
let planList = new Map(), copyRows = [], copyAt = 0, planListAt = 0;
const legSel = new Map(), refetching = new Set();
function legSelFor(name, n) {
  if (!legSel.has(name)) { let raw = null; try { raw = JSON.parse(localStorage.getItem("legsel:" + name) || "null"); } catch (e) { /* ignore */ } legSel.set(name, PV.normalizeSel(raw, n)); }
  return PV.normalizeSel(legSel.get(name), n);
}
function setLegSel(name, sel) { legSel.set(name, sel); store("legsel:" + name, JSON.stringify(sel)); }
// The route editor (routeeditui.js): the panel of the plan view and the handles on its map. It is there only when the dashboard was started with --edit.
const EU = window.RouteEditUI.create({ h, s, MV, rerender: () => scheduleRender(), applyViewBox: (rec) => applyViewBox(rec), geoOf: (name) => { const g = plans.get(name); return g && g.legs ? g : null; }, currentPlan: () => (route.kind === "plan" ? route.plan : ""), routeDoc: (name) => { const r = routes.get(name); return r && r.doc ? r.doc : null; } });
// The real route (realroute.js): the navmesh walk of a plan, traced by the dashboard (GET /api/plan/<name>/route). plan name -> {key, doc, state, msg, at}; state is
// loading, refreshing (the drawing is the last route, the new one is on its way), ok, off (the dashboard has no --mmaps-dir) or error. While the editor is
// on, the route is asked with the edits made so far (the editor's route), so it follows each drop.
const routes = new Map();
const ROUTE_RETRY_MS = 20000;
function routeFor(name, geo) {
  const ops = EU.ops(name), opsKey = ops.length ? JSON.stringify(ops) : "";
  const key = (geo.source ? geo.source.sha256 : "") + "|" + opsKey;
  let st = routes.get(name);
  if (!st) { st = { key: "", doc: null, state: "loading", msg: "", at: 0 }; routes.set(name, st); }
  if (st.key === key && !(st.state === "error" && Date.now() - st.at > ROUTE_RETRY_MS)) return st;
  st.key = key; st.at = Date.now(); st.state = st.doc ? "refreshing" : "loading";
  fetch(RR.routeUrl(name, { ops }), { cache: "no-store" })
    .then(async (r) => { let j = null; try { j = await r.json(); } catch (e) { /* not JSON */ } return { r, j }; })
    .then(({ r, j }) => {
      if (st.key !== key) return; // a newer route was asked for in the meantime
      const doc = r.ok ? RR.parseDoc(j) : null;
      if (doc) { st.doc = doc; st.state = "ok"; st.msg = ""; }
      else { st.state = r.status === 503 ? "off" : "error"; st.msg = (j && j.message) || "HTTP " + r.status; if (!opsKey) st.doc = null; }
      scheduleRender();
    })
    .catch((e) => { if (st.key === key) { st.state = "error"; st.msg = String(e); st.at = Date.now(); scheduleRender(); } });
  return st;
}
// The real route of the plan view into the svg: lines and numbers, redrawn only when the route, the edits or the legs shown change.
function drawRealRoute(rec, svg, name, geo, vis) {
  const st = routeFor(name, geo), g = svg.querySelector("g.realg");
  svg.classList.toggle("hasreal", !!st.doc);
  svg.classList.toggle("stale", st.state === "refreshing");
  if (!g) return st;
  const key = (st.doc ? st.doc.sha256 + (st.doc.edited ? "e" : "") : "none") + "|" + vis.join(",");
  if (g.dataset.k !== key) {
    g.dataset.k = key;
    if (st.doc) { const b = RR.build(s, st.doc, vis); g.replaceChildren(...b.lines, ...b.marks); } else g.replaceChildren();
  }
  g.style.opacity = String(styleState.realpath.opacity / 100);
  g.style.setProperty("--rw", String(styleState.realpath.width));
  return st;
}
// The legend box of the plan view: the four toggles of the route (the same choices as the Layers panel), what the colours mean, and the route's size.
function renderRouteLegend(rec, c, st) {
  const el = rec.parts.rlegend;
  el.hidden = !c.synthetic;
  if (!c.synthetic) return;
  const have = !!(st && st.doc);
  const key = JSON.stringify([layerState.realpath, layerState.route, layerState.packs, layerState.runorder, have, st && st.state, st && st.msg, have ? st.doc.sha256 : ""]);
  if (el.dataset.k === key) return;
  el.dataset.k = key;
  const row = (k, label, sample, needsRoute) => {
    const cb = h("input", { type: "checkbox", "aria-label": label });
    cb.checked = !!layerState[k]; cb.disabled = needsRoute && !have;
    cb.addEventListener("change", () => { layerState = ML.set(layerState, k, cb.checked); ML.save(window.localStorage, layerState); syncLayers(); });
    return h("label", { class: "rlrow" }, cb, sample, h("span", { text: label }));
  };
  const status = !st || st.state === "loading" ? "Tracing the route on the navmesh..."
    : st.state === "off" ? "No navmesh: start the dashboard with --mmaps-dir to draw the real path."
    : st.state === "error" ? "The route could not be traced: " + st.msg
    : RR.summaryText(st.doc) + (st.state === "refreshing" ? " · updating..." : "");
  const red = have && st.doc.summary.failed > 0;
  el.replaceChildren(h("details", { open: "open" }, h("summary", { text: "Route legend" }),
    row("realpath", "Real path", h("i", { class: "rs real" }), true),
    row("route", "Straight lines (old)", h("i", { class: "rs old" }), false),
    row("packs", "Packs", h("i", { class: "rs pk" }), false),
    row("runorder", "Numbers (run order)", h("i", { class: "rs nm", text: "1" }), true),
    h("div", { class: "rlrow" }, h("i", { class: "rs bad" }), h("span", { text: "Dashed red: the planner finds no path (hover for why)" })),
    h("div", { class: "rlstatus" + (red || (st && st.state === "error") ? " bad" : ""), text: status })));
}

// The leg a live run on this plan is in (-1 when none runs it).
function liveLegOf(name) {
  const a = ((last && last.agents) || []).find((x) => x.online && x.label.plan === name && x.run.state === "running");
  return a ? a.run.leg : -1;
}

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v == null) continue;
    if (k === "class") el.className = v; else if (k === "text") el.textContent = v; else el.setAttribute(k, v);
  }
  for (const kid of kids.flat()) if (kid != null) el.append(kid);
  return el;
}
function s(tag, attrs, text) {
  const el = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) el.setAttribute(k, v);
  if (text != null) el.textContent = text;
  return el;
}
const ago = (iso) => { const t = Date.parse(iso); return isNaN(t) ? "" : new Date(t).toLocaleTimeString(); };

function bar(pct, cls) {
  return h("span", { class: "bar " + (cls || "") + (pct < 30 && !cls ? " low" : "") }, h("i", { style: "width:" + Math.max(0, Math.min(100, pct)) + "%" }));
}

// A sample for one line of the legend: a dark chip with the glyph, a ring in the colour, or the health bar's colours.
function legendSample(l) {
  const ring = { "lg-thr-tank": "#2f80ff", "lg-thr-other": "#ff3b3b", "lg-thr-target": "#ffffff", "lg-pull": "#ffd400" }[l.sample];
  if (ring) return h("i", { class: "lgs ring" + (l.sample === "lg-pull" ? " dash" : ""), style: "--c:" + ring });
  if (l.sample === "lg-hp") return h("i", { class: "lgs hps" });
  const colour = { "lg-boss": "#ffd400", "lg-ps-cleared": "#22c55e", "lg-ps-next": "#ffd400", "lg-ps-skipped": "#9aa3b2", "lg-ps-failed": "#ff3b3b", "lg-wp": "#ffb020", "lg-rest": "#7aa7ff",
    "lg-obj": "#22c55e", "lg-stall": "#ffb020", "lg-ref": "#ff3b3b", "lg-aggro": "#ff3b3b", "lg-patrol": "#c084fc", "lg-combat": "#67e8f9", "lg-ghost": "#e9d5ff", "lg-heat": "#ff2d2d" }[l.sample];
  return h("i", { class: "lgs", style: colour ? "color:" + colour : null, text: l.glyph });
}

// The sliders under a layer's row: one per property it has (minutes shown, opacity, thickness). They show while the layer is on.
function styleSliders(parts, key) {
  const box = h("div", { class: "lstyle", hidden: true }), inputs = {};
  for (const prop of Object.keys(ML.STYLES[key])) {
    const sl = ML.SLIDERS[prop], val = h("span", { class: "sv" });
    const input = h("input", { type: "range", min: sl.min, max: sl.max, step: sl.step, "aria-label": sl.label + " of " + key });
    input.addEventListener("input", () => {
      styleState = ML.setStyle(styleState, key, prop, +input.value);
      ML.saveStyle(window.localStorage, styleState);
      for (const r of cards.values()) paintStyleUi(r);
      scheduleRender();
    });
    inputs[prop] = { input, val };
    box.append(h("label", { class: "srow" }, h("span", { class: "sl", text: sl.label }), input, val));
  }
  parts.sliders[key] = { box, inputs };
  return box;
}
const styleValueText = (prop, v) => (prop === "minutes" ? (v >= ML.WHOLE_RUN_MIN ? "whole run" : v + " min") : prop === "opacity" ? v + " %" : v + " px");
function paintStyleUi(rec) {
  for (const [key, sl] of Object.entries(rec.parts.sliders)) {
    const b = rec.parts.boxes[key], shown = !!layerState[key] && !(b && b.cb.disabled);
    sl.box.hidden = !shown;
    for (const [prop, io] of Object.entries(sl.inputs)) {
      const v = styleState[key][prop];
      if (document.activeElement !== io.input) io.input.value = String(v);
      io.val.textContent = styleValueText(prop, v);
    }
  }
}
let renderQueued = 0;
function scheduleRender() { if (renderQueued) return; renderQueued = requestAnimationFrame(() => { renderQueued = 0; if (last) render(last); }); }

function makeCard(c) {
  const parts = {}, rec = { geo: null, card: c, svg: null, layerName: null, layerApplied: null, pick: null,
    v: null, goal: null, fit: null, mode: "fit", followOn: followPref, paused: false, following: false, followScale: null, raf: 0, geoName: null, viewLoaded: null, avail: null };
  parts.head = h("div", { class: "chead" });
  parts.stage = h("div", { class: "stage" });
  parts.map = h("div", { class: "map" });
  parts.info = h("div", { class: "info" });
  // map controls: zoom, fit, follow, dim
  const btn = (text, title, fn, cls) => { const b = h("button", { type: "button", class: "mb " + (cls || ""), title, "aria-label": title, text }); b.addEventListener("click", fn); return b; };
  parts.bIn = btn("+", "Zoom in", () => zoomCenter(rec, 1.6));
  parts.bOut = btn("\u2212", "Zoom out", () => zoomCenter(rec, 1 / 1.6));
  parts.bFit = btn("Fit", "Show the whole map and the plan", () => fitView(rec));
  parts.bFollow = btn("Follow party", "Keep the party in the middle of the map while a run is live. Dragging the map pauses this.", () => toggleFollow(rec), "follow");
  parts.chip = btn("Following paused, click to resume", "Follow the party again", () => toggleFollow(rec), "chip");
  parts.chip.hidden = true;
  parts.dim = h("input", { type: "range", min: 0, max: 80, value: dim, title: "Darken the game map under the drawing", "aria-label": "dim the background" });
  parts.dim.addEventListener("input", () => { dim = +parts.dim.value; store("mapdim", String(dim)); for (const r of cards.values()) { r.parts.dim.value = String(dim); applyDim(r); } });
  parts.boxes = {};
  parts.sliders = {}; // layer key -> {box, inputs: {prop: {input, val}}}
  const dimRow = h("label", { class: "dimlbl" }, "Dim the picture ", parts.dim);
  const lbody = [];
  for (const group of ML.groups()) {
    lbody.push(h("div", { class: "lgroup" }, group));
    for (const d of ML.DEFS.filter((x) => x.group === group)) {
      const cb = h("input", { type: "checkbox" }), why = h("em", { class: "why" });
      cb.addEventListener("change", () => { layerState = ML.set(layerState, d.key, cb.checked); ML.save(window.localStorage, layerState); syncLayers(); });
      parts.boxes[d.key] = { cb, why, row: null };
      parts.boxes[d.key].row = h("label", { class: "lrow" }, cb, h("span", { text: " " + d.label }), why);
      lbody.push(parts.boxes[d.key].row);
      if (ML.STYLES[d.key]) lbody.push(styleSliders(parts, d.key));
    }
  }
  const lgRows = UI.LEGEND.map((l) => h("div", { class: "lgrow" }, legendSample(l), h("span", { text: l.text })));
  const legendBox = h("details", { class: "lgd" }, h("summary", { text: "Legend: what the marks mean" }), ...lgRows);
  const resetBtn = btn("Reset layers", "Back to the layers and sliders shown by default", () => { layerState = ML.defaults(); ML.save(window.localStorage, layerState); styleState = ML.styleDefaults(); ML.saveStyle(window.localStorage, styleState); syncLayers(); });
  parts.layersBox = h("details", { class: "layers" }, h("summary", { class: "mb", title: "Choose what is drawn on the map (one choice for all maps)", text: "Layers" }),
    h("div", { class: "lpanel" }, h("p", { class: "lnote", text: "One choice for all maps. A greyed layer has no data on this map now." }), ...lbody, legendBox, dimRow, resetBtn));
  parts.groups = h("span", { class: "groups", title: "Show or hide the walked path and the dots of each group on this map" });
  parts.bar = h("div", { class: "mapbar" }, parts.bIn, parts.bOut, parts.bFit, parts.bFollow, parts.chip, parts.groups, h("span", { class: "spacer" }), parts.layersBox);
  parts.scale = h("div", { class: "scalebar", hidden: true }, h("i"), h("span"));
  parts.runinfo = h("div", { class: "runinfo", hidden: true });
  parts.feed = h("div", { class: "evfeed", hidden: true, "aria-label": "events of the run" });
  parts.tip = h("div", { class: "unittip", hidden: true, role: "tooltip" });
  parts.rlegend = h("div", { class: "rlegend", hidden: true });
  parts.map.append(parts.bar, parts.runinfo, parts.stage, parts.scale, parts.feed, parts.tip, parts.rlegend);
  const swatches = [["dot", "#2f80ff", "tank"], ["dot", "#22c55e", "healer"], ["dot", "#ff9f1c", "damage"], ["dot", "#c084fc", "commander"], ["dot", "#ff3b3b", "hostile creature"],
    ["dot", "#9aa3b2", "neutral"], ["ring", "#ffd400", "pack to pull"], ["dia", "#ff2fd6", "boss"], ["sq", "#ffffff", "door or object"], ["line", "#00e5ff", "planned route"], ["dash", "#00e5ff", "route still to do"],
    ["trail", "#ff6ec7", "path walked (fades with age)"], ["arrow", "#ffffff", "next stop"]];
  parts.legend = h("div", { class: "legend" },
    ...swatches.map(([cls, col, t]) => h("span", {}, h("i", { class: cls, style: "--c:" + col }), t)),
    h("span", {}, "faded = already done; white ring = in a fight"));
  parts.layer = h("select", { hidden: true, title: "background image of the map (extracted from your own client by turtle-agent minimap-extract)", "aria-label": "map background" });
  parts.layer.addEventListener("change", () => { rec.layerName = parts.layer.value; rec.layerApplied = null; if (last) render(last); });
  parts.legend.append(parts.layer);
  parts.debug = h("details", {}, h("summary", { text: "Debug: raw status and files" }), h("div", { class: "files" }), h("pre", { text: "(open to load)" }));
  parts.debug.addEventListener("toggle", () => { if (parts.debug.open) loadRaw(c.id, parts); });
  const el = h("section", { class: "card", "data-id": c.id }, parts.head, h("div", { class: "cbody" }, parts.map, parts.info, parts.legend), parts.debug);
  rec.el = el; rec.parts = parts;
  if (window.ResizeObserver) new ResizeObserver(() => { if (rec.svg && computeView(rec)) applyViewBox(rec); }).observe(parts.stage);
  updateFollowUi(rec);
  paintLayerUi(rec);
  return rec;
}

async function loadRaw(id, parts) {
  try {
    const r = await fetch("/api/raw/" + encodeURIComponent(id), { cache: "no-store" });
    parts.debug.querySelector("pre").textContent = r.ok ? JSON.stringify(await r.json(), null, 1) : "(no status yet)";
  } catch (e) { parts.debug.querySelector("pre").textContent = String(e); }
}

// A card's plan is the file its slot reads when the server found that file (plan_copy.run), else the dashboard's own copy. Each is kept apart.
function planKeyOf(c) { return c.label.plan ? c.label.plan + (c.plan_copy && c.plan_copy.run ? "@" + c.id : "") : ""; }
const fetchPlan = (c) => fetch("/api/plan/" + encodeURIComponent(c.label.plan) + (c.plan_copy && c.plan_copy.run ? "?agent=" + encodeURIComponent(c.id) : ""), { cache: "no-store" })
  .then((r) => (r.ok ? r.json() : r.text().then((t) => ({ error: t.trim() || "plan not found" }))));
function ensurePlan(c) {
  const key = planKeyOf(c);
  if (!key) return undefined;
  const have = plans.get(key), want = c.plan_copy ? c.plan_copy.drawn.sha256 : c.want_sha;
  // the file changed under the page (an edit, a copy over it): fetch it again and keep drawing the old one until it is there
  const stale = have && have !== "loading" && have.source && want && have.source.sha256 !== want && !refetching.has(key);
  if (stale) {
    refetching.add(key);
    fetchPlan(c).then((g) => { plans.set(key, g); refetching.delete(key); if (last) render(last); }).catch(() => refetching.delete(key));
    return have;
  }
  if (plans.has(key)) return have;
  plans.set(key, "loading");
  fetchPlan(c).then((g) => { plans.set(key, g); if (last) render(last); }).catch(() => plans.set(key, null));
  return "loading";
}

function ensureMinimap(id) {
  if (id == null) return undefined;
  if (minimaps.has(id) && !(minimaps.get(id) === null && Date.now() - (minimapMiss.get(id) || 0) > 30000)) return minimaps.get(id);
  minimaps.set(id, "loading");
  fetch("/api/minimap/" + encodeURIComponent(id), { cache: "no-store" })
    .then((r) => (r.ok ? r.json() : null))
    .then((ls) => { minimaps.set(id, ls && ls.length ? ls : null); minimapMiss.set(id, Date.now()); if (last) render(last); })
    .catch(() => { minimaps.set(id, null); minimapMiss.set(id, Date.now()); });
  return "loading";
}

const getJSON = (url) => fetch(url, { cache: "no-store" }).then((r) => (r.ok ? r.json() : null)).catch(() => null);

// The digest of the agent's run (member walks, stalls, refused walks, rests, pulls, objects), from the server's follow of the run's trace: asked again every few
// seconds, whatever the layers, so that a layer's availability is known before it is switched on. null when the run has no readable trace.
function ensureLive(rec, c) {
  if (!c.online || !c.label.plan) { rec.live = null; return; }
  const now = Date.now();
  if (rec.liveBusy || now - (rec.liveAt || 0) < LIVE_EVERY_MS) return;
  rec.liveBusy = true; rec.liveAt = now;
  const mins = ML.trailMinutes(styleState, "trail_members") || 0;
  getJSON("/api/live/" + encodeURIComponent(c.id) + "?min=" + mins).then((d) => {
    const first = !rec.live && !!d;
    rec.live = d; rec.liveBusy = false;
    if (first) scheduleRender();
  });
}

// The best earlier run of the plan (not this run) and the places parties died in the latest runs: asked once per plan, again after a few minutes.
function cachedFetch(map, key, url) {
  const e = map.get(key);
  if (e && (e.loading || Date.now() - e.at < GHOST_TTL_MS)) return e.data;
  map.set(key, { at: Date.now(), loading: true, data: e ? e.data : null });
  getJSON(url).then((d) => { map.set(key, { at: Date.now(), loading: false, data: d }); scheduleRender(); });
  return e ? e.data : null;
}
const RUN_ID_OK = /^[A-Za-z0-9._-]{1,64}$/;
function ensureGhost(c) {
  const plan = c.label.plan, run = RUN_ID_OK.test(c.label.run_id || "") ? c.label.run_id : "";
  const g = plan ? plans.get(planKeyOf(c)) : null, legs = g && g.legs ? g.legs.length : 0; // the plan's sections tell the server what a clear of the whole dungeon is
  return plan ? cachedFetch(ghosts, plan + "|" + run + "|" + legs, "/api/ghost/" + encodeURIComponent(plan) + "?legs=" + legs + (run ? "&exclude=" + encodeURIComponent(run) : "")) : null;
}
function ensureHeat(c) { return c.label.plan ? cachedFetch(heats, c.label.plan, "/api/heat/" + encodeURIComponent(c.label.plan)) : null; }

// The image goes under the plan drawing; its rectangle is in the drawing's plane (yards), so the plan lines up with it.
function applyLayer(rec, svg, geo) {
  const ls = ensureMinimap(geo.map), sel = rec.parts.layer;
  const have = Array.isArray(ls) ? ls : [];
  sel.hidden = have.length < 2;
  const names = have.map((l) => l.name).join("|");
  if (have.length >= 2 && sel.dataset.names !== names) { sel.dataset.names = names; sel.replaceChildren(...have.map((l) => h("option", { value: l.name, text: l.name }))); }
  const pick = have.find((l) => l.name === rec.layerName) || have[0];
  rec.pick = pick || null;
  const key = pick ? pick.url : "";
  if (rec.layerApplied === key) return;
  rec.layerApplied = key;
  svg.querySelectorAll("image.bg").forEach((e) => e.remove());
  if (!pick) return;
  sel.value = pick.name;
  const img = s("image", { class: "bg", href: pick.url, x: pick.x, y: pick.y, width: pick.w, height: pick.h, preserveAspectRatio: "none" });
  svg.insertBefore(img, svg.firstChild);
}

// The head and the side panel of the plan view: the plan, the file it is drawn from, the copy each slot reads, and the leg switches.
function renderPlanHead(c, rec) {
  const geo = plans.get(c.label.plan), pi = planList.get(c.label.plan);
  const kids = [h("h2", { text: "Plan " + c.label.plan })];
  if (geo && geo.legs) kids.push(h("span", { class: "sum", text: geo.legs.length + " legs, map " + geo.map + (pi && pi.status ? " \u00b7 " + pi.status : "") }));
  kids.push(h("span", { class: "pill", text: "no run: the plan only" }));
  rec.parts.head.replaceChildren(...kids);
}

function renderPlanInfo(c, rec) {
  const name = c.label.plan, geo = plans.get(name), n = geo && geo.legs ? geo.legs.length : 0;
  const editPanel = EU.panel(name, geo && geo.legs ? geo : null); // one element that stays, so what is typed in it stays
  const sel = legSelFor(name, n), runLeg = liveLegOf(name), vis = PV.visibleLegs(sel, n, runLeg);
  const key = JSON.stringify([sel, runLeg, geo && geo.source, copyRows]);
  if (rec.planInfoKey === key) return;
  rec.planInfoKey = key;
  const blocks = [editPanel];
  const src = geo && geo.source;
  const where = !src ? "loading..." : src.role === "build" ? src.path + " (" + PV.short(src) + "): no plans folder of the dashboard has this plan, so it draws the copy built into its binary"
    : (src.role === "slot" ? "a slot's copy " : "the dashboard's plans folder, ") + src.path + " (" + PV.short(src) + (src.modified ? ", changed " + new Date(src.modified).toLocaleString() : "") + ")";
  blocks.push(h("div", { class: "plancopy" }, h("h3", { text: "Drawn from" }), h("div", { text: where }),
    h("div", { class: "muted", text: "A run reads the copy in its own slot folder (dungeons_dir), which can be another file: the table shows whether it is the same one." })));
  const rows = copyRows.map((r) => h("tr", { class: r.run && !r.same ? "diff" : "" }, h("td", { text: "agent " + r.agent }), h("td", { text: PV.copyRowText(r) })));
  blocks.push(h("div", {}, h("h3", { text: "Which copy each run reads" }),
    rows.length ? h("table", {}, ...rows) : h("div", { class: "muted", text: "no agent found" })));
  if (n) {
    const toolbar = h("div", { class: "legtools" });
    const tb = (text, title, sels) => { const b = h("button", { type: "button", class: "mb", title, text }); b.addEventListener("click", () => { setLegSel(name, sels()); if (last) render(last); }); return b; };
    toolbar.append(tb("All legs", "Show every leg", () => PV.allLegs()), tb("No legs", "Hide every leg", () => PV.hideAll(n)));
    if (runLeg >= 0) toolbar.append(tb("Current leg + next", "Show the leg the live run is in and the one after it (follows the run)", () => ({ mode: "follow", only: -1, off: [] })));
    const list = (geo.legs || []).map((l, i) => {
      const cb = h("input", { type: "checkbox", "aria-label": "show " + l.label }), only = h("button", { type: "button", class: "mb mini", title: "Show only this leg", text: "only" });
      cb.checked = vis.includes(i);
      cb.addEventListener("change", () => { setLegSel(name, PV.toggleLeg(sel, i, n, runLeg)); if (last) render(last); });
      only.addEventListener("click", () => { setLegSel(name, PV.onlyLeg(i, n)); if (last) render(last); });
      return h("label", { class: "legrow" + (i === runLeg ? " now" : "") }, cb, h("span", { text: " " + (i + 1) + ". " + l.label + " \u00b7 " + l.groups + " pack" + (l.groups === 1 ? "" : "s") + (l.boss ? " \u00b7 boss" : "") + (i === runLeg ? " (run is here)" : "") }), only);
    });
    blocks.push(h("div", {}, h("h3", { text: "Legs (" + vis.length + " of " + n + " shown)" }), toolbar, h("div", { class: "legs" }, ...list)));
  }
  rec.parts.info.replaceChildren(...blocks);
}

function renderHead(c, rec) {
  if (c.synthetic) { renderPlanHead(c, rec); return; }
  const p = rec.parts.head, r = c.run;
  const kids = [h("h2", { text: c.character ? c.character + (c.level ? " (level " + c.level + ")" : "") : "agent " + c.id })];
  if (!c.online) kids.push(h("span", { class: "pill failed", text: "offline" }), h("span", { class: "muted", text: c.error || "" }));
  else {
    kids.push(h("span", { class: "sum", title: "dungeon \u00b7 group (character) \u00b7 time \u00b7 deaths \u00b7 where", text: PT.summaryLine(c) || "no run yet" }));
    kids.push(h("span", { class: "pill " + r.state, text: PT.phaseWord(c) }));
    if (r.state === "running") kids.push(h("span", { class: "pill " + c.stall.level, title: "Time since the run last made progress. Yellow after 2 minutes, red after 5.", text: PT.stallText(c.stall) }));
  }
  p.replaceChildren(...kids);
}

// Which plan file the map draws and which one the run reads, in words, with the way into the plan view.
function planCopyBlock(c) {
  const pc = c.plan_copy, b = PV.copyBadge(pc), re = PV.routeEditBadge(c.route_edit, pc);
  return h("div", { class: "plancopy " + (re && re.level === "warn" ? "warn" : b ? b.level : "") },
    h("h3", { text: "Plan file" }),
    h("div", { text: pc.text }),
    re ? h("div", { class: re.level === "ok" ? "ok" : "", text: re.text + "." }) : null,
    h("div", { class: "muted" }, b ? b.text + ". " : "", h("a", { href: PV.planHash(c.label.plan), text: "Open this plan in the plan view" })));
}

function kv(item) { return h("div", { class: item.hot ? "hot" : "", title: item.tip }, h("b", { text: item.value }), h("span", { text: item.label })); }

function renderInfo(c, rec) {
  if (c.synthetic) { renderPlanInfo(c, rec); return; }
  const r = c.run, names = namesOf(c);
  const blocks = [];
  if (!c.online) { rec.parts.info.replaceChildren(h("p", { class: "err-line", text: c.error || "not reachable" })); return; }
  const raw = [r.text, r.waiting ? "waiting on: " + r.waiting : "", r.mode ? "mode: " + r.mode : ""].filter(Boolean).join("\n");
  blocks.push(h("div", {}, h("h3", { text: "Now" }), h("div", { class: "now", title: raw, text: PT.stateLine(c, names) }),
    r.state === "running" ? h("div", { class: "muted " + c.stall.level, text: PT.stallText(c.stall) }) : null));
  if (c.plan_copy) blocks.push(planCopyBlock(c));
  blocks.push(h("div", { class: "kv" }, ...PT.counterList(c).map(kv), kv({ label: "Build", value: c.sha ? c.sha.slice(0, 8) : "?", tip: "The turtle-agent build this run uses." })));
  if (c.last_error) blocks.push(h("div", {}, h("h3", { text: "Last problem" }), h("div", { class: "err-line", text: c.last_error })));
  if (c.last_refusal) blocks.push(h("div", {}, h("h3", { text: "Last order the bots refused" }), h("div", { class: "err-line", text: c.last_refusal })));
  // party
  const rows = c.bots.map((b) => {
    const w = PT.botRow(b, names);
    const nm = h("td", { class: "bn", title: "Show " + b.name + " on the map", text: w.name });
    if (b.known) nm.addEventListener("click", () => centerOn(rec, b.x, b.y));
    else nm.classList.add("off");
    return h("tr", { class: b.alive ? "" : "dead" }, nm, h("td", { text: w.kind }),
      h("td", { class: "num", title: "Health " + w.hp + " %" }, bar(w.hp), String(w.hp)),
      h("td", { class: "num", title: w.powerLabel + " " + w.power + " %" }, bar(w.power, "p"), String(w.power)),
      h("td", { class: "td-doing", text: UI.activity(b).word, title: UI.activity(b).raw }),
      h("td", { text: w.target, title: [b.action, b.result, b.reason].filter(Boolean).join(" ") }),
      h("td", { class: "num", text: w.dist }));
  });
  if (rows.length) {
    const feed = c.feed.have ? "updated " + (c.feed.age_ms / 1000).toFixed(1) + " s ago" + (c.feed.stale ? " (stale)" : "") : "no data from the bots yet";
    blocks.push(h("div", {}, h("h3", { text: "Party \u00b7 " + feed }),
      h("table", {}, h("tr", {}, ...["Name", "Class", "Health", "Mana / power", "Doing", "Target", "Distance"].map((t) => h("th", { text: t }))), ...rows)));
  }
  // orders
  const orders = c.orders.slice().reverse().map((o) => {
    const t = PT.orderText(o, c, names);
    return h("div", { class: t.ok ? "ack" : "err", title: t.raw + (o.ms ? "  (" + o.ms + " ms)" : "") }, ago(o.at) + " " + t.text);
  });
  blocks.push(h("div", {}, h("h3", { text: "Orders to the bots" }), h("div", { class: "orders" }, ...(orders.length ? orders : [h("span", { class: "muted", text: "none yet" })]))));
  const ch = c.changes.slice().reverse().map((x) => h("div", { class: "orders" }, ago(x.at) + " " + x.text));
  if (ch.length || (c.tunables || []).length) {
    blocks.push(h("div", {}, h("h3", { text: "Changes made during the run" }), ...ch,
      ...(c.tunables || []).map((t) => h("div", { class: "muted", text: "Setting " + t.key + " = " + t.value + " (normally " + t.base + ", set by " + t.source + ")" }))));
  }
  rec.parts.info.replaceChildren(...blocks);
  const files = rec.parts.debug.querySelector(".files");
  files.replaceChildren(...(c.links || []).map((l) => h("a", { href: "/files/" + l.path.split("/").map(encodeURIComponent).join("/"), target: "_blank", rel: "noopener", text: l.name + " " })));
}

// ---- the map: one viewBox for the picture and the drawing, so they always line up ----
// rec.v is the view {cx, cy, scale} (scale: pixels per yard). rec.mode "fit" shows everything; "free" is where the user put it.
// While the party is followed, the view eases toward the party's centre each update and keeps the zoom.
const MARKER_PX = 1; // markers are drawn in pixels around their own origin and scaled by 1/scale, so they stay one size

function panelSize(rec) { return rec.svg ? { pw: rec.svg.clientWidth, ph: rec.svg.clientHeight } : { pw: 0, ph: 0 }; }

function partyPoints(c) {
  const pts = c.bots.filter((b) => b.known).map((b) => ({ x: b.x, y: b.y }));
  if (c.commander) pts.push({ x: c.commander.x, y: c.commander.y });
  return pts;
}

function saveView(rec) {
  if (!rec.geo || !rec.v) return;
  store("mapview:" + rec.geo.map, JSON.stringify({ mode: rec.mode, cx: rec.v.cx, cy: rec.v.cy, scale: rec.v.scale, fs: rec.followScale }));
}
function loadView(rec) {
  try {
    const o = JSON.parse(localStorage.getItem("mapview:" + rec.geo.map) || "null");
    if (!o) return;
    if (o.mode === "free" && [o.cx, o.cy, o.scale].every(Number.isFinite) && o.scale > 0) { rec.v = { cx: o.cx, cy: o.cy, scale: o.scale }; rec.mode = "free"; }
    if (Number.isFinite(o.fs) && o.fs > 0) rec.followScale = o.fs;
  } catch (e) { /* no stored view */ }
}

// Work out what the view should be now. false while the panel has no size (a card in a hidden tab).
function computeView(rec) {
  const geo = rec.geo, c = rec.card, { pw, ph } = panelSize(rec);
  if (!geo || !c || pw < 20 || ph < 20) return false;
  const b = rec.boundsOv || geo.bounds || geo.view_box;
  const rects = [{ x: b[0], y: b[1], w: b[2], h: b[3] }];
  if (rec.pick) rects.push({ x: rec.pick.x, y: rec.pick.y, w: rec.pick.w, h: rec.pick.h });
  rec.fit = MV.fit(MV.union(rects), pw, ph, 16);
  const pts = partyPoints(c);
  rec.following = !!(rec.followOn && !rec.paused && pts.length && c.run.state === "running");
  if (rec.following) {
    const ce = MV.centroid(pts);
    if (rec.followScale == null) rec.followScale = MV.followScale(ce.extent, pw, ph, 60);
    rec.followScale = MV.clampScale(rec.followScale, rec.fit.scale);
    const goal = { cx: ce.x, cy: ce.y, scale: rec.followScale };
    if (!rec.v) rec.v = goal; else { rec.goal = goal; startAnim(rec); }
  } else if (rec.mode === "fit" || !rec.v) {
    rec.v = rec.fit; rec.goal = null;
  } else {
    rec.v = { cx: rec.v.cx, cy: rec.v.cy, scale: MV.clampScale(rec.v.scale, rec.fit.scale) };
  }
  updateFollowUi(rec);
  return true;
}

function startAnim(rec) {
  if (rec.raf) return;
  const step = () => {
    rec.raf = 0;
    if (!rec.goal || !rec.v || !rec.svg) return;
    const r = MV.ease(rec.v, rec.goal, 0.2);
    rec.v = r.view;
    applyViewBox(rec);
    if (!r.done) rec.raf = requestAnimationFrame(step);
  };
  rec.raf = requestAnimationFrame(step);
}

// Put the view on the svg and size the markers: every .mk group sits at its drawing point at 1/scale yards per pixel.
function applyViewBox(rec) {
  const { pw, ph } = panelSize(rec);
  if (!rec.svg || !rec.v || pw < 20) return;
  rec.svg.setAttribute("viewBox", MV.viewBox(rec.v, pw, ph).map((n) => n.toFixed(3)).join(" "));
  rec.svg.classList.toggle("zoomed", !!rec.fit && rec.v.scale >= rec.fit.scale * 2.5);
  const k = (MARKER_PX / rec.v.scale).toFixed(5);
  rec.svg.querySelectorAll(".mk").forEach((g) => { g.setAttribute("transform", "translate(" + g.dataset.x + " " + g.dataset.y + ") scale(" + k + ")" + (g.dataset.hd ? " rotate(" + g.dataset.hd + ")" : "")); });
  drawGrid(rec, pw, ph);
  drawScale(rec);
}

// The grid layer: lines about 80 px apart across the view, rebuilt as the view moves (only while the layer is on).
function drawGrid(rec, pw, ph) {
  const g = rec.svg.querySelector("g.gridg");
  if (!g) return;
  if (!layerState.grid) { if (g.firstChild) g.replaceChildren(); g.dataset.k = ""; return; }
  const box = MV.viewBox(rec.v, pw, ph), step = ML.gridStep(rec.v.scale), gl = ML.gridLines(box, step);
  const key = [step, gl.xs[0], gl.xs.length, gl.ys[0], gl.ys.length].join("|");
  if (g.dataset.k === key) return;
  g.dataset.k = key;
  const lines = [];
  for (const x of gl.xs) lines.push(s("line", { class: "gridl", x1: x, y1: box[1], x2: x, y2: box[1] + box[3] }));
  for (const y of gl.ys) lines.push(s("line", { class: "gridl", x1: box[0], y1: y, x2: box[0] + box[2], y2: y }));
  g.replaceChildren(...lines);
}

// The distance scale bar (a piece of the card, not of the svg): the longest round number of yards under 110 px.
function drawScale(rec) {
  const sc = rec.parts.scale;
  sc.hidden = !layerState.scale || !rec.v;
  if (sc.hidden) return;
  const sb = ML.scaleBar(rec.v.scale, 110);
  sc.firstChild.style.width = sb.px.toFixed(0) + "px";
  sc.lastChild.textContent = sb.yd >= 1000 ? sb.yd / 1000 + " km" : sb.yd + " yd";
}

// The svg classes of the layers that are off, for one card.
function applyLayers(rec) { if (rec.svg) for (const d of ML.DEFS) rec.svg.classList.toggle("hide-" + d.key, !layerState[d.key]); }

// Every card shows the same choice: tick the boxes, grey out what the card's map has no data for, set the classes and redraw the scale bar and grid.
function paintLayerUi(rec) {
  for (const d of ML.DEFS) {
    const b = rec.parts.boxes[d.key], a = rec.avail ? rec.avail[d.key] : { ok: true, why: "" };
    b.cb.checked = !!layerState[d.key];
    b.cb.disabled = !a.ok;
    b.row.classList.toggle("off", !a.ok);
    b.row.title = a.ok ? "" : "Greyed out: " + a.why;
    b.why.textContent = a.ok ? "" : a.why;
  }
  paintStyleUi(rec);
}
function syncLayers() { for (const rec of cards.values()) { paintLayerUi(rec); applyLayers(rec); if (rec.svg) applyViewBox(rec); } if (last) render(last); }

function applyDim(rec) { if (rec.svg) rec.svg.style.setProperty("--bright", String(1 - dim / 100)); }

function updateFollowUi(rec) {
  const p = rec.parts;
  p.bFollow.classList.toggle("on", rec.followOn && !rec.paused);
  p.bFollow.setAttribute("aria-pressed", String(rec.followOn && !rec.paused));
  p.chip.hidden = !(rec.followOn && rec.paused);
}

function toggleFollow(rec) {
  if (rec.followOn && rec.paused) rec.paused = false;
  else { rec.followOn = !rec.followOn; rec.paused = false; followPref = rec.followOn; store("mapfollow", followPref ? "on" : "off"); }
  if (rec.followOn) { rec.mode = "free"; }
  if (computeView(rec)) applyViewBox(rec);
}

function fitView(rec) {
  rec.mode = "fit"; rec.goal = null;
  if (rec.followOn) rec.paused = true;
  saveView(rec);
  if (computeView(rec)) applyViewBox(rec);
}

function centerOn(rec, x, y) {
  if (!rec.v || !rec.fit) return;
  rec.mode = "free";
  if (rec.followOn) { rec.paused = true; rec.following = false; }
  rec.goal = { cx: x, cy: y, scale: Math.max(rec.v.scale, rec.fit.scale * 2) };
  updateFollowUi(rec); startAnim(rec);
  rec.parts.map.scrollIntoView({ block: "nearest", behavior: "smooth" });
}

function zoomAround(rec, factor, px, py) {
  const { pw, ph } = panelSize(rec);
  if (!rec.v || !rec.fit || pw < 20) return;
  if (rec.following) {
    // the party stays in the middle: only the zoom changes
    rec.followScale = MV.clampScale(rec.v.scale * factor, rec.fit.scale);
    computeView(rec);
    saveView(rec);
    return;
  }
  rec.goal = null; rec.mode = "free";
  rec.v = MV.zoomAt(rec.v, factor, pw, ph, px, py, rec.fit.scale);
  applyViewBox(rec); saveView(rec);
}
function zoomCenter(rec, factor) { const { pw, ph } = panelSize(rec); zoomAround(rec, factor, pw / 2, ph / 2); }

function dragBy(rec, dx, dy) {
  if (!rec.v) return;
  if (rec.following) { rec.paused = true; rec.following = false; updateFollowUi(rec); }
  rec.goal = null; rec.mode = "free";
  rec.v = MV.panBy(rec.v, dx, dy);
  applyViewBox(rec);
}

// Mouse wheel zooms around the cursor, a drag pans, two fingers pinch and pan, a double click zooms in.
function attachControls(rec, svg) {
  const ptrs = new Map();
  const rel = (e) => { const r = svg.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top }; };
  svg.addEventListener("wheel", (e) => {
    e.preventDefault();
    const px = e.deltaMode === 1 ? e.deltaY * 30 : e.deltaY, p = rel(e);
    zoomAround(rec, Math.exp(-px * (e.ctrlKey ? 0.01 : 0.0015)), p.x, p.y);
  }, { passive: false });
  svg.addEventListener("pointerdown", (e) => { if (e.button > 0) return; try { svg.setPointerCapture(e.pointerId); } catch (x) { /* ignore */ } ptrs.set(e.pointerId, rel(e)); });
  svg.addEventListener("pointermove", (e) => {
    const prev = ptrs.get(e.pointerId);
    if (!prev) return;
    const cur = rel(e);
    if (ptrs.size === 1) dragBy(rec, cur.x - prev.x, cur.y - prev.y);
    else if (ptrs.size === 2 && rec.v) {
      const other = [...ptrs].find(([id]) => id !== e.pointerId)[1], { pw, ph } = panelSize(rec);
      if (rec.following) {
        // Only a drag pauses the following: a pinch zooms around the party, which stays in the middle.
        zoomAround(rec, MV.pinchFactor(prev, other, cur, other), pw / 2, ph / 2);
        ptrs.set(e.pointerId, cur);
        return;
      }
      rec.goal = null; rec.mode = "free";
      rec.v = MV.pinch(rec.v, prev, other, cur, other, pw, ph, rec.fit.scale);
      applyViewBox(rec);
    }
    ptrs.set(e.pointerId, cur);
  });
  const up = (e) => { if (ptrs.delete(e.pointerId) && rec.mode === "free") saveView(rec); };
  svg.addEventListener("pointerup", up);
  svg.addEventListener("pointercancel", up);
  svg.addEventListener("dblclick", (e) => { const p = rel(e); zoomAround(rec, 2, p.x, p.y); });
}

function ensureSvg(rec, geo) {
  const sha = geo.source ? geo.source.sha256 : "";
  if (rec.svg && rec.geoName === geo.plan && rec.geoSha === sha) return rec.svg;
  const svg = s("svg", { preserveAspectRatio: "xMidYMid meet", role: "img", "aria-label": "map of the run: route, packs, bosses and the party" });
  const stat = s("g", { class: "static" });
  stat.innerHTML = geo.svg; // built by the server from the plan file, text escaped
  svg.append(s("g", { class: "gridg" }), stat, s("g", { class: "realg" }), s("g", { class: "dyn" }), s("g", { class: "editg" }));
  rec.parts.stage.replaceChildren(svg);
  rec.svg = svg; rec.geoName = geo.plan; rec.geoSha = sha; rec.layerApplied = null; rec.geo = geo;
  if (!rec.viewLoaded) { rec.viewLoaded = true; loadView(rec); }
  applyLayers(rec);
  attachControls(rec, svg);
  attachHover(rec, svg);
  applyDim(rec);
  return svg;
}

// A label on a dark plate, in pixels around the origin.
function plate(text, x, y, cls) {
  const g = s("g", { class: cls || "" });
  g.append(s("rect", { class: "lbg", x: x - 3, y: y - 11.5, width: (text.length * 6.6 + 6).toFixed(1), height: 15, rx: 3 }), s("text", { class: "lt", x, y }, text));
  return g;
}

// The groups on this card's map: this one and every other online agent on the same map, each with its colour and a show/hide chip.
function groupsOf(c) {
  const all = (last && last.agents) || [];
  const color = (a) => GROUP_COLORS[Math.max(0, all.findIndex((x) => x.id === a.id)) % GROUP_COLORS.length];
  const name = (a) => PT.groupName(a.label.folder) || a.character || "agent " + a.id;
  const list = [{ card: c, own: true, color: color(c), name: name(c) }];
  for (const a of all) if (a.online && a.id !== c.id && c.label.map && a.label.map === c.label.map) list.push({ card: a, own: false, color: color(a), name: name(a) });
  return list;
}

function renderGroupChips(rec, groups) {
  const key = groups.map((g) => g.card.id + g.name + g.color + hiddenGroups.has(g.card.id)).join("|");
  if (rec.groupKey === key) return;
  rec.groupKey = key;
  rec.parts.groups.replaceChildren(...groups.map((g) => {
    const on = !hiddenGroups.has(g.card.id);
    const b = h("button", { type: "button", class: "mb grp" + (on ? " on" : ""), title: (on ? "Hide " : "Show ") + g.name + " (walked path" + (g.own ? "" : " and dots") + ")", "aria-pressed": String(on) },
      h("i", { class: "sw", style: "background:" + g.color }), g.name + (g.own ? "" : " "));
    b.addEventListener("click", () => {
      if (hiddenGroups.has(g.card.id)) hiddenGroups.delete(g.card.id); else hiddenGroups.add(g.card.id);
      store("grouphide", JSON.stringify([...hiddenGroups]));
      for (const r of cards.values()) r.groupKey = null;
      if (last) render(last);
    });
    return b;
  }));
}

// The walked path of a group's lead: its last N minutes (the Layers slider; the top is the whole run), fading with age, in the thickness and opacity the
// sliders say. The own group's lead is drawn in its role colour, brighter than the other trails; another group's in the group's colour.
function trailEls(card, groupColor, own) {
  const sty = styleState.trail, nowS = Date.parse(last && last.time) / 1000 || Date.now() / 1000;
  const lead = (card.bots || []).find((b) => b.lead), color = own && lead ? MP.trailColor(MP.roleOf(lead), true) : groupColor;
  const r = MP.recent(card.trail || [], card.trail_t, nowS, ML.trailMinutes(styleState, "trail"));
  const w = sty.width, o = sty.opacity / 100, out = [];
  for (const seg of MV.trailSegments(r.points, 24, 0.25)) {
    const points = seg.points.map((p) => p[0] + "," + p[1]).join(" ");
    out.push(s("polyline", { class: "trail-halo", points, opacity: (seg.alpha * 0.7 * o).toFixed(2), style: "stroke-width:" + (w + 2.5) }),
      s("polyline", { class: "trail", points, stroke: color, opacity: (seg.alpha * o).toFixed(2), style: "stroke-width:" + w }));
  }
  return out;
}

// ---- marks over the units, the hover box, the run line and the event feed ----

// A thin health bar centred over a marker, in pixels around its origin (y is its top edge).
function hpBarEl(pct, y, w) {
  const p = Math.max(0, Math.min(100, pct == null ? 0 : pct)), g = s("g", { class: "hpb" });
  g.append(s("rect", { class: "bg", x: -w / 2 - 1, y: y - 1, width: w + 2, height: 5, rx: 1.5 }), s("rect", { class: "fg", x: -w / 2, y, width: (w * p / 100).toFixed(1), height: 3, fill: UI.hpColor(p) }));
  return g;
}

// The status glyphs of a unit, in one text element beside its marker.
function statusEl(kinds, x, y) {
  const t = s("text", { class: "sti", x, y }, kinds.map((k) => UI.STATUS[k].glyph).join(""));
  t.append(s("title", {}, kinds.map((k) => UI.STATUS[k].text).join(", ")));
  return t;
}

function renderRunInfo(rec, prog, ghostLine) {
  const el = rec.parts.runinfo, showRun = !!(layerState.runinfo && prog), showGhost = !!(layerState.ghost && ghostLine);
  el.hidden = !(showRun || showGhost);
  if (el.hidden) return;
  const kids = showRun ? [h("span", { text: UI.progressLine({ ...prog, bosses: [], total: 0 }) })] : [];
  if (showRun && prog.bosses.length) {
    kids.push(h("span", { class: "boxes" }, " · bosses: ", ...prog.bosses.map((b, i) => h("span", { class: b.done ? "ok" : "", text: (i ? ", " : "") + (b.done ? "\u2713 " : "\u25A1 ") + b.name })),
      prog.next ? " · next: " + prog.next : prog.done === prog.total ? " · all down" : ""));
  }
  if (showGhost) kids.push(h("span", { class: "ghostline", title: "The best earlier run of this dungeon in the tests folder", text: (kids.length ? " \u00b7 " : "") + ghostLine }));
  el.replaceChildren(...kids);
}

function renderFeed(rec, c) {
  const el = rec.parts.feed, lines = UI.feedLines(c.events, 8);
  el.hidden = !(layerState.feed && lines.length);
  if (el.hidden) return;
  el.replaceChildren(...lines.map((l) => h("div", { class: l.kind }, h("span", { class: "t", text: l.time || "--:--:--" }), h("span", { class: "g", text: l.glyph }), l.text)));
}

function hideTip(rec) { rec.tipKey = null; rec.parts.tip.hidden = true; }

// Fill the box with the unit (textContent only) and put it near the pointer inside the map panel.
function showTip(rec, key, clientX, clientY) {
  const u = rec.units && rec.units.get(key);
  if (!u) { hideTip(rec); return; }
  const d = UI.describeUnit(u, rec.tipCtx || {}), tip = rec.parts.tip;
  tip.replaceChildren(h("div", {}, h("b", { text: d.title }), h("span", { class: "badge", text: d.badge })), h("dl", {}, ...d.rows.flatMap(([k, v]) => [h("dt", { text: k }), h("dd", { text: v })])));
  rec.tipKey = key;
  tip.hidden = false;
  if (clientX != null) {
    const r = rec.parts.map.getBoundingClientRect(), w = tip.offsetWidth, hh = tip.offsetHeight;
    let x = clientX - r.left + 14, y = clientY - r.top + 14;
    if (x + w > r.width - 6) x = Math.max(6, clientX - r.left - w - 14);
    if (y + hh > r.height - 6) y = Math.max(6, clientY - r.top - hh - 14);
    tip.style.left = x + "px"; tip.style.top = y + "px";
  }
}

// After a redraw the box shows the same unit again with fresh numbers (or goes when the unit is gone).
function refreshTip(rec) { if (rec.tipKey) showTip(rec, rec.tipKey, null, null); }

function unitKeyAt(e) {
  for (const el of document.elementsFromPoint(e.clientX, e.clientY)) {
    const g = el.closest && el.closest("[data-uid]");
    if (g && g.dataset.uid) return g.dataset.uid;
  }
  return null;
}

// A mouse shows the box while it is over a unit; a finger opens it with a tap on the unit and closes it with a tap elsewhere.
function attachHover(rec, svg) {
  let down = null, raf = 0;
  svg.addEventListener("pointerdown", (e) => { down = { x: e.clientX, y: e.clientY, id: e.pointerId }; });
  svg.addEventListener("pointermove", (e) => {
    if (e.pointerType !== "mouse" || e.buttons || raf) return;
    raf = requestAnimationFrame(() => { raf = 0; const k = unitKeyAt(e); if (k) showTip(rec, k, e.clientX, e.clientY); else hideTip(rec); });
  });
  svg.addEventListener("pointerleave", (e) => { if (e.pointerType === "mouse") hideTip(rec); });
  svg.addEventListener("pointerup", (e) => {
    if (e.pointerType === "mouse" || !down || down.id !== e.pointerId) return;
    const moved = Math.hypot(e.clientX - down.x, e.clientY - down.y);
    down = null;
    if (moved > 8) return;
    const k = unitKeyAt(e);
    if (k && k !== rec.tipKey) showTip(rec, k, e.clientX, e.clientY); else hideTip(rec);
  });
}

function renderMap(c, rec) {
  const stage = rec.parts.stage, m = rec.parts.map, name = c.label.plan;
  rec.card = c;
  if (c.synthetic) { const pi = planList.get(name); c.want_sha = pi && pi.source ? pi.source.sha256 : ""; }
  const geo = name ? ensurePlan(c) : null;
  const msg = (t) => { rec.svg = null; stage.replaceChildren(h("div", { class: "nomap", text: t })); rec.parts.bar.hidden = true; rec.parts.runinfo.hidden = true; rec.parts.feed.hidden = true; rec.parts.rlegend.hidden = true; hideTip(rec); };
  if (geo && geo.error) { msg("Plan " + name + ": " + geo.error); return; }
  if (!geo || geo === "loading") { msg(name ? "Loading the plan " + name + "..." : "No run, so there is no plan to draw."); return; }
  const mismatch = c.synthetic ? "" : PV.planMismatch(geo, c.run);
  if (mismatch) { msg("Plan " + name + ": dashboard plan does not match the run's plan (" + mismatch + "). " + (c.plan_copy ? c.plan_copy.text : "")); return; }
  rec.parts.bar.hidden = false;
  const svg = ensureSvg(rec, geo);
  rec.geo = geo;
  applyLayer(rec, svg, geo);
  computeView(rec);
  const run = c.run, names = namesOf(c);
  // the plan view switches legs on and off: a leg that is off is not drawn, and the fit follows the legs that are on
  let vis = null, vgeo = geo;
  rec.boundsOv = null;
  if (c.synthetic && geo.legs) {
    const n = geo.legs.length;
    vis = PV.visibleLegs(legSelFor(name, n), n, liveLegOf(name));
    vgeo = PV.filterGeo(geo, vis);
    if (vis.length < n) rec.boundsOv = PV.boundsOf(geo, vis);
    computeView(rec);
  }
  svg.querySelectorAll(".leg").forEach((g) => {
    const i = +g.dataset.leg;
    g.classList.toggle("current", i === run.leg && run.state === "running");
    g.classList.toggle("done", run.leg >= 0 && i < run.leg);
    g.classList.toggle("legoff", !!vis && !vis.includes(i));
  });
  let rst = null;
  if (c.synthetic) { rst = drawRealRoute(rec, svg, name, geo, vis || []); EU.overlay(rec, name, geo, vis || []); }
  renderRouteLegend(rec, c, rst);
  svg.querySelectorAll(".pull, .pin").forEach((e) => {
    const i = +e.dataset.leg, gi = +e.dataset.group;
    const cleared = run.leg >= 0 && i === run.leg && run.group > 0 && gi < run.group - 1;
    e.classList.toggle("cleared", cleared);
    e.classList.toggle("active", i === run.leg && run.group > 0 && gi === run.group - 1 && run.state === "running");
  });
  const dyn = svg.querySelector(".dyn"), kids = [];
  const at = (x, y, cls) => s("g", { class: "mk " + cls, "data-x": x, "data-y": y });
  // Every unit that can be hovered gets an id on its marker; the hover box looks the unit up in rec.units.
  const units = new Map();
  const unitG = (g, key, u) => { u.key = key; units.set(key, u); g.dataset.uid = key; g.append(s("circle", { class: "hit", r: 13 })); return g; };
  // the walked path of every group on this map, then the other groups' dots
  const groups = groupsOf(c);
  renderGroupChips(rec, groups);
  for (const g of groups) if (!hiddenGroups.has(g.card.id)) kids.push(...trailEls(g.card, g.color, g.own));
  for (const g of groups) {
    if (g.own || hiddenGroups.has(g.card.id)) continue;
    for (const b of g.card.bots) {
      if (!b.known) continue;
      const m = at(b.x, b.y, "o");
      const d = s("circle", { r: 7, class: "other", fill: g.color });
      m.append(d);
      unitG(m, "o:" + g.card.id + ":" + b.name, { type: "other", data: b, group: g.name });
      m.append(hpBarEl(b.alive ? b.hp_pct : 0, -15, 18));
      kids.push(m);
    }
  }
  // the next stop of the leg in hand: an arrow from the party to it
  const stops = geo.legs && run.leg >= 0 && geo.legs[run.leg] ? geo.legs[run.leg].stops : null;
  const lead = c.bots.find((b) => b.lead && b.known) || c.bots.find((b) => b.known) || c.commander;
  const nt = run.state === "running" && stops && lead ? MV.nextTarget(stops, c.trail || [], { x: lead.x, y: lead.y }) : null;
  if (nt) {
    const pts = lead.x + "," + lead.y + " " + nt.stop.x + "," + nt.stop.y;
    kids.push(s("polyline", { class: "next-halo", points: pts }), s("polyline", { class: "next", points: pts }));
    const head = at(nt.stop.x, nt.stop.y, "arrow");
    head.dataset.rot = (Math.atan2(nt.stop.y - lead.y, nt.stop.x - lead.x) * 180 / Math.PI).toFixed(1);
    head.append(s("polygon", { class: "arrow", points: "0,0 -14,-6 -14,6" }));
    kids.push(head);
    const mid = at((lead.x + nt.stop.x) / 2, (lead.y + nt.stop.y) / 2, "nl");
    mid.append(plate("\u2192 " + nt.stop.label + ", " + Math.round(nt.dist) + " yd", 6, -6, "nextlbl"));
    kids.push(mid);
  }
  // the newer layers (off by default, hidden by their hide-<key> class): ranges, target lines, the lead's heading, times on the walked path
  const nowS = Date.parse(last && last.time) / 1000 || Date.now() / 1000;
  const healers = c.bots.filter((b) => b.known && b.alive && b.role === "healer");
  for (const b of healers) kids.push(s("circle", { class: "range heal", cx: b.x, cy: b.y, r: ML.HEAL_RANGE_YD }, null));
  const pulls = ML.pullTargets(c.mobs, run);
  for (const mob of pulls) kids.push(s("circle", { class: "range aggro", cx: mob.x, cy: mob.y, r: ML.AGGRO_YD }, null));
  const threat = ML.threatLines(c.mobs, c.bots), botLines = ML.botTargetLines(c.bots, c.mobs);
  for (const l of threat) kids.push(s("line", { class: "tline", x1: l.from.x, y1: l.from.y, x2: l.to.x, y2: l.to.y }));
  for (const l of botLines) kids.push(s("line", { class: "btline", x1: l.from.x, y1: l.from.y, x2: l.to.x, y2: l.to.y }));
  const leadPos = c.bots.find((b) => b.lead && b.known) || null;
  if (c.commander && leadPos) kids.push(s("line", { class: "cmdline", x1: c.commander.x, y1: c.commander.y, x2: leadPos.x, y2: leadPos.y }));
  const hd = ML.heading(c.trail || [], c.trail_t, nowS);
  if (hd && lead) {
    const g = at(lead.x, lead.y, "hd");
    g.dataset.hd = (Math.atan2(hd.y, hd.x) * 180 / Math.PI).toFixed(1);
    const poly = s("polygon", { class: "heading", points: "34,0 14,-8 14,8" });
    poly.append(s("title", {}, "the lead is heading this way (from its movement)"));
    g.append(poly);
    kids.push(g);
  }
  const stamps = ML.trailStamps(c.trail || [], c.trail_t, rec.v ? rec.v.scale : 1, 80, 8);
  for (const st of stamps) {
    const g = at(st.x, st.y, "ts");
    g.append(s("circle", { class: "tsdot", r: 3 }), plate(new Date(st.t * 1000).toLocaleTimeString(), 6, -6, "tslbl"));
    kids.push(g);
  }
  const tankT = UI.tankTargets(c.bots), pullKeys = new Set(pulls.map((m) => UI.guidKey(m.guid)));
  const leadXY = lead ? { x: lead.x, y: lead.y } : null;
  const ctx = { names, lead: leadXY };
  let anyThreat = false, anyStatus = false;
  for (const mob of c.mobs) {
    const g = at(mob.x, mob.y, "m " + mob.kind + (mob.cleared ? " cleared" : "") + (mob.boss ? " boss" : ""));
    const dot = s("circle", { r: mob.kind === "hostile" ? 6.5 : 5, class: "mob " + mob.kind + (mob.cleared ? " cleared" : "") + (mob.engaged ? " engaged" : "") + (mob.boss ? " boss" : "") });
    g.append(dot);
    const u = { type: "mob", data: mob };
    unitG(g, "m:" + mob.guid, u);
    // threat colours: the ring is whom the creature attacks, the tank's target is outlined, the pull target marked
    const thr = UI.threatOf(mob, c.bots), key = UI.guidKey(mob.guid);
    if (thr) { g.append(s("circle", { class: "thr " + thr, r: 10 })); anyThreat = true; }
    if (key && tankT.has(key)) { g.append(s("circle", { class: "thr tgt", r: 13.5 })); anyThreat = true; }
    if (pullKeys.has(key)) { g.append(s("circle", { class: "thr pull", r: 17 })); anyThreat = true; }
    const top = mob.boss ? -20 : -(dot.getAttribute("r") * 1 + 8);
    g.append(hpBarEl(mob.hp_pct, top, mob.boss ? 26 : 18));
    const st = UI.statuses(u);
    if (st.length) { anyStatus = true; g.append(statusEl(st, mob.boss ? 15 : 10, mob.boss ? -12 : -9)); }
    if (mob.kind === "hostile") g.append(plate(UI.mobName(names, mob.entry), 10, 16, "hname"));
    if (mob.boss) {
      const bm = s("g", { class: "bossmk" });
      bm.append(s("circle", { class: "ring", r: 13 }), s("path", { class: "crown", d: "M-8,-26 L-5,-34 L0,-28 L5,-34 L8,-26 Z" }), plate(UI.mobName(names, mob.entry), 16, 5, ""));
      g.append(bm);
    }
    kids.push(g);
  }
  for (const b of c.bots) {
    if (!b.known) continue;
    const g = at(b.x, b.y, "b");
    const role = b.role === "tank" ? "tank" : b.role === "healer" ? "healer" : "dps";
    const dot = s("circle", { r: 9, class: "bot " + role + (b.alive ? "" : " dead") });
    g.append(dot, plate(b.name, 13, 4, "botname" + (b.alive ? "" : " dead")));
    const u = { type: "bot", data: b };
    unitG(g, "b:" + b.name, u);
    g.append(hpBarEl(b.alive ? b.hp_pct : 0, -17, 20));
    const st = UI.statuses(u);
    if (st.length) { anyStatus = true; g.append(statusEl(st, 11, -11)); }
    kids.push(g);
  }
  if (c.commander) {
    const g = at(c.commander.x, c.commander.y, "c");
    const d = s("circle", { r: 10, class: "cmd" });
    g.append(d, plate(c.commander.name, 14, 4, "cmdl"));
    unitG(g, "c:" + c.id, { type: "commander", data: c.commander });
    kids.push(g);
  }
  // the events of the run as pins
  const pins = UI.pinnedEvents(c.events);
  pins.forEach((e, i) => {
    const g = at(e.x, e.y, "evpin " + e.kind);
    g.append(s("circle", { r: 9 }), s("text", { class: "g", y: 4 }, UI.eventGlyph(e.kind)));
    unitG(g, "e:" + c.id + ":" + i, { type: "event", data: e });
    kids.push(g);
  });
  // the path layers: what lies under the units (heat, ghost, routes, rings, lines) and what marks them (spots, X marks, targets)
  if (!c.synthetic) ensureLive(rec, c);
  const pd = PD.build({ s, plate, at, unitG, on: layerState, style: styleState, layers: ML, card: c, geo: vgeo, svg, live: rec.live, ghost: ensureGhost(c), heat: ensureHeat(c), lead: leadXY, nextTarget: nt, threat });
  kids.unshift(...pd.under);
  kids.push(...pd.over);
  rec.units = units; rec.tipCtx = ctx;
  dyn.replaceChildren(...kids);
  const prog = UI.progress(c, geo, c.events);
  renderRunInfo(rec, prog, pd.ghostLine);
  renderFeed(rec, c);
  const has = (sel) => !!svg.querySelector(sel), kind = (...ks) => c.mobs.some((m) => ks.includes(m.kind));
  rec.avail = ML.availability({
    picture: !!rec.pick, route: has(".route, .direct"), packs: has(".pull, .pin"), cleared: c.mobs.some((m) => m.cleared) || has(".pull.cleared, .pin.cleared, .leg.done"),
    bosses: has(".boss"), doors: has(".obj"), sections: has(".legname"), steps: has(".stepn"), trail: (c.trail || []).length >= 2, trailtime: stamps.length > 0, heading: !!(hd && lead),
    next: !!nt, bots: c.bots.some((b) => b.known), botTargets: botLines.length > 0, healer: healers.length > 0, commander: !!c.commander, cmdline: !!(c.commander && leadPos),
    hostile: kind("hostile"), neutral: kind("neutral", "friendly"), critter: kind("critter"), threat: threat.length > 0, pulltargets: pulls.length > 0, zones: false,
    bossmob: c.mobs.some((m) => m.boss), hpunits: c.bots.some((b) => b.known) || c.mobs.length > 0, status: anyStatus, threatcolor: anyThreat,
    events: (c.events || []).length > 0, runinfo: !!prog,
    looks: has(".look"), standmarks: has(".trg, .gossip, .stand"), flags: has(".nw, .cskip"), realpath: !!(rst && rst.doc), runorder: !!(rst && rst.doc),
    ...pd.avail,
  });
  paintLayerUi(rec);
  applyViewBox(rec);
  refreshTip(rec);
  const note = !c.bots.some((b) => b.known) ? (c.feed.have ? "No bot is inside this map." : "Waiting for the bots' position feed.") : "";
  let n = m.querySelector(":scope > .nomap");
  if (note) { if (!n) { n = h("div", { class: "nomap note" }); m.append(n); } n.textContent = note; } else if (n) n.remove();
}

function render(state) {
  last = state;
  const agents = state.agents || [];
  $("summary").textContent = agents.length + " agent" + (agents.length === 1 ? "" : "s") + (state.offline.length ? ", not running: " + state.offline.join(", ") : "");
  $("updated").textContent = "updated " + new Date(state.time).toLocaleTimeString();
  const list = route.kind === "plan" ? [PV.planCard(route.plan, plans.get(route.plan))] : agents;
  $("empty").hidden = list.length > 0;
  const ids = new Set(list.map((c) => c.id));
  for (const [id, rec] of cards) if (!ids.has(id)) { rec.el.remove(); cards.delete(id); }
  for (const c of list) {
    let rec = cards.get(c.id);
    if (!rec) { rec = makeCard(c); if (c.synthetic) rec.parts.debug.hidden = true; cards.set(c.id, rec); $("cards").append(rec.el); }
    rec.el.classList.toggle("offline", !c.online);
    rec.card = c;
    renderHead(c, rec); renderInfo(c, rec); renderMap(c, rec);
  }
  if (!activeTab || !ids.has(activeTab)) activeTab = list.length ? list[0].id : null;
  applyView(list);
}

function applyView(list) {
  $("cards").classList.toggle("tabs", view === "tabs");
  $("v-grid").classList.toggle("on", view === "grid");
  $("v-tabs").classList.toggle("on", view === "tabs");
  const nav = $("tabs");
  nav.hidden = view !== "tabs";
  nav.replaceChildren(...list.map((c) => { const b = h("button", { type: "button", class: c.id === activeTab ? "on" : "", text: (c.label.plan || "-") + " · " + (c.character || c.id) }); b.onclick = () => { activeTab = c.id; if (last) render(last); }; return b; }));
  for (const [id, rec] of cards) rec.el.hidden = view === "tabs" && id !== activeTab;
}

function setView(v) { view = v; try { localStorage.setItem("view", v); } catch (e) { /* ignore */ } if (last) render(last); }
$("v-grid").onclick = () => setView("grid");
$("v-tabs").onclick = () => setView("tabs");

// ---- the page address and the plan list ----
// The select at the top names the pages: the run cards, or the plan view of any plan the dashboard can draw. The list is asked again every few seconds so that a
// plan file that changed (and so its drawing) is fetched again.
function paintRouteSelect() {
  const sel = $("route"), want = route.kind === "plan" ? route.plan : "";
  const names = [...planList.keys()];
  if (want && !planList.has(want)) names.push(want);
  const key = names.join("|");
  if (sel.dataset.names !== key) {
    sel.dataset.names = key;
    sel.replaceChildren(h("option", { value: "", text: "Runs (live)" }), ...names.map((n) => h("option", { value: n, text: "Plan: " + n })));
  }
  sel.value = want;
}
$("route").addEventListener("change", () => { location.hash = PV.planHash($("route").value); });
function onRoute() {
  const next = PV.parseRoute(location.hash);
  if (next.kind === route.kind && next.plan === route.plan) return;
  route = next;
  copyRows = []; copyAt = 0;
  for (const rec of cards.values()) rec.el.remove();
  cards.clear();
  paintRouteSelect();
  if (last) render(last);
  pollPlans();
}
window.addEventListener("hashchange", onRoute);
async function pollPlans() {
  const now = Date.now();
  if (now - planListAt < 3500) return;
  planListAt = now;
  const list = await getJSON("/api/plans");
  if (Array.isArray(list)) { planList = new Map(list.map((p) => [p.name, p])); paintRouteSelect(); }
  if (route.kind === "plan") {
    const rows = await getJSON("/api/plan-copies/" + encodeURIComponent(route.plan));
    if (Array.isArray(rows)) copyRows = rows;
    if (last) render(last);
  }
}

async function poll() {
  try {
    const r = await fetch("/api/state", { cache: "no-store" });
    if (r.ok) render(await r.json());
    pollPlans();
    // an open debug panel follows its agent
    for (const [id, rec] of cards) if (rec.parts.debug.open) loadRaw(id, rec.parts);
  } catch (e) { $("updated").textContent = "dashboard not reachable: " + e; }
}
paintRouteSelect();
poll();
setInterval(poll, POLL_MS);
