"use strict";
// Live dashboard page. Everything from the agents is put in the page with textContent, never as markup; the only markup
// inserted is the plan drawing, which the server builds with escaped text.
const SVGNS = "http://www.w3.org/2000/svg";
const MV = window.MapView, PT = window.PlainText; // view.js (zoom and pan math) and text.js (plain wording)
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
// What can be drawn on a map: [key, label, on by default]. Each layer is a class "hide-<key>" on the svg that the stylesheet uses.
const LAYERS = [["bg", "Game map picture", true], ["route", "Planned route", true], ["trail", "Walked path", true], ["next", "Next-stop arrow", true],
  ["packs", "Pull-group circles", true], ["hostile", "Hostile mobs", true], ["neutral", "Neutral and friendly mobs", false], ["cleared", "Cleared (dimmed) groups", true],
  ["bosses", "Bosses", true], ["doors", "Doors and objects", true], ["bots", "Bot dots", true], ["botnames", "Bot names", true], ["commander", "Commander", true],
  ["sections", "Section labels", true], ["steps", "Step numbers", false]];
const store = (k, v) => { try { localStorage.setItem(k, v); } catch (e) { /* ignore */ } };
const namesOf = (c) => { const g = plans.get(c.label.plan); return g && g.names ? g.names : {}; };

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

function makeCard(c) {
  const parts = {}, rec = { geo: null, card: c, svg: null, layerName: null, layerApplied: null, pick: null,
    v: null, goal: null, fit: null, mode: "fit", followOn: followPref, paused: false, following: false, followScale: null, raf: 0, geoName: null, viewLoaded: null, layers: Object.fromEntries(LAYERS.map(([k, , d]) => [k, d])) };
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
  const dimRow = h("label", { class: "dimlbl" }, "Dim the picture ", parts.dim);
  parts.layersBox = h("details", { class: "layers" }, h("summary", { class: "mb", title: "Choose what is drawn on the map", text: "Layers" }),
    h("div", { class: "lpanel" }, ...LAYERS.map(([key, text]) => {
      const cb = h("input", { type: "checkbox" });
      cb.checked = rec.layers[key];
      cb.addEventListener("change", () => { rec.layers[key] = cb.checked; saveLayers(rec); applyLayers(rec); });
      parts.boxes[key] = cb;
      return h("label", { class: "lrow" }, cb, " " + text);
    }), dimRow));
  parts.groups = h("span", { class: "groups", title: "Show or hide the walked path and the dots of each group on this map" });
  parts.bar = h("div", { class: "mapbar" }, parts.bIn, parts.bOut, parts.bFit, parts.bFollow, parts.chip, parts.groups, h("span", { class: "spacer" }), parts.layersBox);
  parts.map.append(parts.bar, parts.stage);
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
  return rec;
}

async function loadRaw(id, parts) {
  try {
    const r = await fetch("/api/raw/" + encodeURIComponent(id), { cache: "no-store" });
    parts.debug.querySelector("pre").textContent = r.ok ? JSON.stringify(await r.json(), null, 1) : "(no status yet)";
  } catch (e) { parts.debug.querySelector("pre").textContent = String(e); }
}

function ensurePlan(name) {
  if (!name || plans.has(name)) return plans.get(name);
  plans.set(name, "loading");
  fetch("/api/plan/" + encodeURIComponent(name), { cache: "no-store" })
    .then((r) => (r.ok ? r.json() : r.text().then((t) => ({ error: t.trim() || "plan not found" }))))
    .then((g) => { plans.set(name, g); if (last) render(last); })
    .catch(() => plans.set(name, null));
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

function renderHead(c, rec) {
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

function kv(item) { return h("div", { class: item.hot ? "hot" : "", title: item.tip }, h("b", { text: item.value }), h("span", { text: item.label })); }

function renderInfo(c, rec) {
  const r = c.run, names = namesOf(c);
  const blocks = [];
  if (!c.online) { rec.parts.info.replaceChildren(h("p", { class: "err-line", text: c.error || "not reachable" })); return; }
  const raw = [r.text, r.waiting ? "waiting on: " + r.waiting : "", r.mode ? "mode: " + r.mode : ""].filter(Boolean).join("\n");
  blocks.push(h("div", {}, h("h3", { text: "Now" }), h("div", { class: "now", title: raw, text: PT.stateLine(c, names) }),
    r.state === "running" ? h("div", { class: "muted " + c.stall.level, text: PT.stallText(c.stall) }) : null));
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
      h("td", { text: w.target, title: [b.action, b.result, b.reason].filter(Boolean).join(" ") }),
      h("td", { class: "num", text: w.dist }));
  });
  if (rows.length) {
    const feed = c.feed.have ? "updated " + (c.feed.age_ms / 1000).toFixed(1) + " s ago" + (c.feed.stale ? " (stale)" : "") : "no data from the bots yet";
    blocks.push(h("div", {}, h("h3", { text: "Party \u00b7 " + feed }),
      h("table", {}, h("tr", {}, ...["Name", "Class", "Health", "Mana / power", "Target", "Distance"].map((t) => h("th", { text: t }))), ...rows)));
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
  const b = geo.bounds || geo.view_box;
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
  rec.svg.querySelectorAll(".mk").forEach((g) => { g.setAttribute("transform", "translate(" + g.dataset.x + " " + g.dataset.y + ") scale(" + k + ")"); });
}

function saveLayers(rec) { if (rec.geo) store("maplayers:" + rec.geo.map, JSON.stringify(rec.layers)); }
function loadLayers(rec) {
  try {
    const o = JSON.parse(localStorage.getItem("maplayers:" + rec.geo.map) || "null");
    if (o && typeof o === "object") for (const [k] of LAYERS) if (typeof o[k] === "boolean") rec.layers[k] = o[k];
  } catch (e) { /* defaults */ }
  for (const [k] of LAYERS) rec.parts.boxes[k].checked = rec.layers[k];
}
function applyLayers(rec) { if (rec.svg) for (const [k] of LAYERS) rec.svg.classList.toggle("hide-" + k, !rec.layers[k]); }

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
      if (rec.following) { rec.paused = true; rec.following = false; updateFollowUi(rec); }
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
  if (rec.svg && rec.geoName === geo.plan) return rec.svg;
  const svg = s("svg", { preserveAspectRatio: "xMidYMid meet", role: "img", "aria-label": "map of the run: route, packs, bosses and the party" });
  const stat = s("g", { class: "static" });
  stat.innerHTML = geo.svg; // built by the server from the plan file, text escaped
  svg.append(stat, s("g", { class: "dyn" }));
  rec.parts.stage.replaceChildren(svg);
  rec.svg = svg; rec.geoName = geo.plan; rec.layerApplied = null; rec.geo = geo;
  if (!rec.viewLoaded) { rec.viewLoaded = true; loadView(rec); loadLayers(rec); }
  applyLayers(rec);
  attachControls(rec, svg);
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

function trailEls(pts, color) {
  const out = [];
  for (const seg of MV.trailSegments(pts, 24, 0.25)) {
    const points = seg.points.map((p) => p[0] + "," + p[1]).join(" ");
    out.push(s("polyline", { class: "trail-halo", points, opacity: (seg.alpha * 0.7).toFixed(2) }), s("polyline", { class: "trail", points, stroke: color, opacity: seg.alpha.toFixed(2) }));
  }
  return out;
}

function renderMap(c, rec) {
  const stage = rec.parts.stage, m = rec.parts.map, name = c.label.plan;
  rec.card = c;
  const geo = name ? ensurePlan(name) : null;
  const msg = (t) => { rec.svg = null; stage.replaceChildren(h("div", { class: "nomap", text: t })); rec.parts.bar.hidden = true; };
  if (geo && geo.error) { msg("Plan " + name + ": " + geo.error); return; }
  if (!geo || geo === "loading") { msg(name ? "Loading the plan " + name + "..." : "No run, so there is no plan to draw."); return; }
  rec.parts.bar.hidden = false;
  const svg = ensureSvg(rec, geo);
  rec.geo = geo;
  applyLayer(rec, svg, geo);
  computeView(rec);
  const run = c.run, names = namesOf(c);
  svg.querySelectorAll(".leg").forEach((g) => {
    const i = +g.dataset.leg;
    g.classList.toggle("current", i === run.leg && run.state === "running");
    g.classList.toggle("done", run.leg >= 0 && i < run.leg);
  });
  svg.querySelectorAll(".pull, .pin").forEach((e) => {
    const i = +e.dataset.leg, gi = +e.dataset.group;
    const cleared = run.leg >= 0 && i === run.leg && run.group > 0 && gi < run.group - 1;
    e.classList.toggle("cleared", cleared);
    e.classList.toggle("active", i === run.leg && run.group > 0 && gi === run.group - 1 && run.state === "running");
  });
  const dyn = svg.querySelector(".dyn"), kids = [];
  const at = (x, y, cls) => s("g", { class: "mk " + cls, "data-x": x, "data-y": y });
  // the walked path of every group on this map, then the other groups' dots
  const groups = groupsOf(c);
  renderGroupChips(rec, groups);
  for (const g of groups) if (!hiddenGroups.has(g.card.id)) kids.push(...trailEls(g.card.trail || [], g.color));
  for (const g of groups) {
    if (g.own || hiddenGroups.has(g.card.id)) continue;
    for (const b of g.card.bots) {
      if (!b.known) continue;
      const m = at(b.x, b.y, "o");
      const d = s("circle", { r: 7, class: "other", fill: g.color });
      d.append(s("title", {}, g.name + ": " + b.name));
      m.append(d);
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
  for (const mob of c.mobs) {
    const g = at(mob.x, mob.y, "m " + mob.kind + (mob.cleared ? " cleared" : "") + (mob.boss ? " boss" : ""));
    const dot = s("circle", { r: mob.kind === "hostile" ? 6.5 : 5, class: "mob " + mob.kind + (mob.cleared ? " cleared" : "") + (mob.engaged ? " engaged" : "") + (mob.boss ? " boss" : "") });
    dot.append(s("title", {}, [PT.mobName(names, mob.entry) + " (" + mob.kind + ")", mob.hp_pct + " % health", mob.target ? "fighting " + mob.target : "", mob.cc ? "crowd controlled" : "", mob.evade ? "walking back to its spot" : "",
      mob.group >= 0 ? "pack " + (mob.group + 1) + " of this section" : mob.boss ? "boss fight" : "", "id " + mob.entry].filter(Boolean).join(", ")));
    g.append(dot);
    kids.push(g);
  }
  for (const b of c.bots) {
    if (!b.known) continue;
    const g = at(b.x, b.y, "b");
    const role = b.role === "tank" ? "tank" : b.role === "healer" ? "healer" : "dps";
    const dot = s("circle", { r: 9, class: "bot " + role + (b.alive ? "" : " dead") });
    const w = PT.botRow(b, names);
    dot.append(s("title", {}, b.name + " (" + w.kind + "): " + b.hp_pct + " % health, " + b.power_pct + " % " + w.powerLabel.toLowerCase() + (w.dist ? ", " + w.dist : "") + (w.target ? ", fighting " + w.target : "")));
    g.append(dot, plate(b.name, 13, 4, "botname" + (b.alive ? "" : " dead")));
    kids.push(g);
  }
  if (c.commander) {
    const g = at(c.commander.x, c.commander.y, "c");
    const d = s("circle", { r: 10, class: "cmd" });
    d.append(s("title", {}, "commander " + c.commander.name));
    g.append(d, plate(c.commander.name, 14, 4, "cmdl"));
    kids.push(g);
  }
  dyn.replaceChildren(...kids);
  applyViewBox(rec);
  const note = !c.bots.some((b) => b.known) ? (c.feed.have ? "No bot is inside this map." : "Waiting for the bots' position feed.") : "";
  let n = m.querySelector(":scope > .nomap");
  if (note) { if (!n) { n = h("div", { class: "nomap note" }); m.append(n); } n.textContent = note; } else if (n) n.remove();
}

function render(state) {
  last = state;
  const list = state.agents || [];
  $("summary").textContent = list.length + " agent" + (list.length === 1 ? "" : "s") + (state.offline.length ? ", not running: " + state.offline.join(", ") : "");
  $("updated").textContent = "updated " + new Date(state.time).toLocaleTimeString();
  $("empty").hidden = list.length > 0;
  const ids = new Set(list.map((c) => c.id));
  for (const [id, rec] of cards) if (!ids.has(id)) { rec.el.remove(); cards.delete(id); }
  for (const c of list) {
    let rec = cards.get(c.id);
    if (!rec) { rec = makeCard(c); cards.set(c.id, rec); $("cards").append(rec.el); }
    rec.el.classList.toggle("offline", !c.online);
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

async function poll() {
  try {
    const r = await fetch("/api/state", { cache: "no-store" });
    if (r.ok) render(await r.json());
    // an open debug panel follows its agent
    for (const [id, rec] of cards) if (rec.parts.debug.open) loadRaw(id, rec.parts);
  } catch (e) { $("updated").textContent = "dashboard not reachable: " + e; }
}
poll();
setInterval(poll, POLL_MS);
