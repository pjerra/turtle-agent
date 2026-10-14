"use strict";
// Live dashboard page. Everything from the agents is put in the page with textContent, never as markup; the only markup
// inserted is the plan drawing, which the server builds with escaped text.
const SVGNS = "http://www.w3.org/2000/svg";
const POLL_MS = 1500;
const $ = (id) => document.getElementById(id);
const cards = new Map(); // id -> {el, parts, geo}
const plans = new Map(); // name -> geometry | "loading" | null
let view = "grid", activeTab = null, last = null;
try { view = localStorage.getItem("view") === "tabs" ? "tabs" : "grid"; } catch (e) { /* storage may be blocked */ }

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
const fmtMin = (m) => (m < 1 ? Math.round(m * 60) + " s" : m.toFixed(1) + " min");
const fmtDur = (sec) => (sec >= 60 ? Math.floor(sec / 60) + " m " + Math.round(sec % 60) + " s" : Math.round(sec) + " s");
const ago = (iso) => { const t = Date.parse(iso); return isNaN(t) ? "" : new Date(t).toLocaleTimeString(); };

function bar(pct, cls) {
  return h("span", { class: "bar " + (cls || "") + (pct < 30 && !cls ? " low" : "") }, h("i", { style: "width:" + Math.max(0, Math.min(100, pct)) + "%" }));
}

function makeCard(c) {
  const parts = {};
  parts.head = h("div", { class: "chead" });
  parts.map = h("div", { class: "map" });
  parts.info = h("div", { class: "info" });
  parts.legend = h("div", { class: "legend" },
    ...[["var(--bot)", "bot"], ["#d49a1f", "tank"], ["var(--cmd)", "commander"], ["var(--hostile)", "hostile"], ["var(--neutral)", "neutral"], ["var(--pull)", "pull group"]]
      .map(([col, t]) => h("span", {}, h("i", { style: "background:" + col }), t)),
    h("span", {}, "dimmed = cleared; ring = in a fight"));
  parts.debug = h("details", {}, h("summary", { text: "Debug: raw status and files" }), h("div", { class: "files" }), h("pre", { text: "(open to load)" }));
  parts.debug.addEventListener("toggle", () => { if (parts.debug.open) loadRaw(c.id, parts); });
  const el = h("section", { class: "card", "data-id": c.id }, parts.head, h("div", { class: "cbody" }, parts.map, parts.info, parts.legend), parts.debug);
  return { el, parts, geo: null, planName: null, svg: null };
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

function renderHead(c, rec) {
  const p = rec.parts.head, r = c.run;
  const label = [c.label.plan, c.label.folder].filter(Boolean).join(" · ");
  const kids = [
    h("h2", { text: c.character ? c.character + (c.level ? " (" + c.level + ")" : "") : "agent " + c.id }),
    h("span", { class: "label", text: label || "no run" }),
  ];
  if (!c.online) kids.push(h("span", { class: "pill failed", text: "offline" }), h("span", { class: "muted", text: c.error || "" }));
  else {
    kids.push(h("span", { class: "pill " + r.state, text: r.state === "running" ? stateText(c) : r.state }));
    if (r.state === "running" || r.minutes) kids.push(h("span", { class: "muted", text: fmtMin(r.minutes) + " in" }));
    if (r.state === "running") kids.push(h("span", { class: "pill " + c.stall.level, title: "time since the run last made progress (yellow 2 min, red 5 min)", text: "stall " + fmtDur(c.stall.seconds) }));
    kids.push(h("span", { class: "muted", text: "rpc " + c.id }));
  }
  p.replaceChildren(...kids);
}

function stateText(c) {
  const r = c.run, parts = [r.phase];
  if (r.leg >= 0) parts.push("leg " + r.leg + (r.leg_name ? " " + r.leg_name : ""));
  if (r.group > 0) parts.push("pull group " + r.group + " of " + r.groups);
  return parts.join(" · ");
}

function kv(label, value, hot) { return h("div", { class: hot ? "hot" : "" }, h("b", { text: String(value) }), h("span", { text: label })); }

function renderInfo(c, rec) {
  const n = c.counters, r = c.run;
  const blocks = [];
  if (!c.online) { rec.parts.info.replaceChildren(h("p", { class: "err-line", text: c.error || "not reachable" })); return; }
  if (r.text || r.waiting) blocks.push(h("div", {}, h("h3", { text: "Now" }), h("div", { text: r.text || "" }), h("div", { class: "muted", text: r.waiting || "" })));
  blocks.push(h("div", { class: "kv" },
    kv("deaths", n.deaths, n.deaths > 0), kv("wipes", n.wipes, n.wipes > 0), kv("party downs", n.party_downs, n.party_downs > 0),
    kv("threat overrides", n.threat_overrides), kv("rest share", n.rest_pct + " %"), kv("rest gates", n.rest_gates),
    kv("gathers", n.gathers), kv("walk stops", n.walk_stops), kv("pulls", n.pulls),
    kv("GM commands", n.gm_commands, n.gm_commands > 1), kv("GM assists", n.gm_assists, n.gm_assists > 0), kv("agent walks", n.agent_walks),
    kv("orders", n.orders_sent + " (" + n.orders_refused + " refused)"), kv("binary", c.sha || "?")));
  if (c.last_error) blocks.push(h("div", {}, h("h3", { text: "Last error" }), h("div", { class: "err-line", text: c.last_error })));
  if (c.last_refusal) blocks.push(h("div", {}, h("h3", { text: "Last refused order" }), h("div", { class: "err-line", text: c.last_refusal })));
  // bots
  const rows = c.bots.map((b) => h("tr", { class: b.alive ? "" : "dead" },
    h("td", { text: (b.lead ? "★ " : "") + b.name }),
    h("td", { text: [b.class, b.role].filter(Boolean).join(" / ") }),
    h("td", { class: "num" }, bar(b.hp_pct), String(b.hp_pct)),
    h("td", { class: "num", title: b.power_type || "" }, bar(b.power_pct, "p"), String(b.power_pct)),
    h("td", { class: "num", text: b.dist_tank == null ? "" : b.dist_tank.toFixed(0) + " yd" }),
    h("td", { text: b.target || "", title: [b.action, b.result, b.reason].filter(Boolean).join(" ") })));
  if (rows.length) {
    const feed = c.feed.have ? "feed " + (c.feed.age_ms / 1000).toFixed(1) + " s old" + (c.feed.stale ? " (stale)" : "") : "no feed yet";
    blocks.push(h("div", {}, h("h3", { text: "Party · " + feed }),
      h("table", {}, h("tr", {}, ...["bot", "class / role", "HP %", "power %", "to tank", "target"].map((t) => h("th", { text: t }))), ...rows)));
  }
  // orders
  const orders = c.orders.slice().reverse().map((o) => h("div", { class: (o.reply || "").toLowerCase() },
    ago(o.at) + " " + (o.reply || "sent").padEnd(4) + " " + o.text + (o.message ? " -> " + (o.code ? o.code + ": " : "") + o.message : "")));
  blocks.push(h("div", {}, h("h3", { text: "Last orders" }), h("div", { class: "orders" }, ...(orders.length ? orders : [h("span", { class: "muted", text: "none yet" })]))));
  const ch = c.changes.slice().reverse().map((x) => h("div", { class: "orders" }, ago(x.at) + " " + x.text));
  if (ch.length || (c.tunables || []).length) {
    blocks.push(h("div", {}, h("h3", { text: "On-the-spot changes" }), ...ch,
      ...(c.tunables || []).map((t) => h("div", { class: "muted", text: t.key + " = " + t.value + " (base " + t.base + ", " + t.source + ")" }))));
  }
  rec.parts.info.replaceChildren(...blocks);
  const files = rec.parts.debug.querySelector(".files");
  files.replaceChildren(...(c.links || []).map((l) => h("a", { href: "/files/" + l.path.split("/").map(encodeURIComponent).join("/"), target: "_blank", rel: "noopener", text: l.name + " " })));
}

function ensureSvg(rec, geo) {
  if (rec.svg && rec.geoName === geo.plan) return rec.svg;
  const svg = s("svg", { viewBox: geo.view_box.join(" "), preserveAspectRatio: "xMidYMid meet" });
  const stat = s("g", { class: "static" });
  stat.innerHTML = geo.svg; // built by the server from the plan file, text escaped
  svg.append(stat, s("g", { class: "dyn" }));
  rec.parts.map.replaceChildren(svg);
  rec.svg = svg; rec.geoName = geo.plan;
  return svg;
}

function renderMap(c, rec) {
  const m = rec.parts.map, name = c.label.plan;
  const geo = name ? ensurePlan(name) : null;
  if (geo && geo.error) { rec.svg = null; m.replaceChildren(h("div", { class: "nomap", text: "plan " + name + ": " + geo.error })); return; }
  if (!geo || geo === "loading") { rec.svg = null; m.replaceChildren(h("div", { class: "nomap", text: name ? "loading the plan " + name + "..." : "no run, no plan to draw" })); return; }
  const svg = ensureSvg(rec, geo);
  const vb = geo.view_box, w = svg.clientWidth || 400;
  const k = vb[2] / w; // yards per pixel
  svg.style.setProperty("--k", k.toFixed(4));
  const run = c.run;
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
  const dyn = svg.querySelector(".dyn"), kids = [], r = 3.2 * k;
  for (const mob of c.mobs) {
    const dot = s("circle", { cx: mob.x, cy: mob.y, r: r * (mob.kind === "hostile" ? 1 : 0.8),
      class: "mob " + mob.kind + (mob.cleared ? " cleared" : "") + (mob.engaged ? " engaged" : "") });
    dot.append(s("title", {}, [mob.kind + " entry " + mob.entry, mob.hp_pct + " % hp", mob.target ? "on " + mob.target : "", mob.cc ? "crowd controlled" : "", mob.evade ? "evading" : "",
      mob.group >= 0 ? "pull group " + (mob.group + 1) + " of leg " + mob.leg : mob.boss ? "boss fight" : ""].filter(Boolean).join(", ")));
    kids.push(dot);
  }
  for (const b of c.bots) {
    if (!b.known) continue;
    const dot = s("circle", { cx: b.x, cy: b.y, r: r * 1.5, class: "bot " + (b.role || "") + (b.alive ? "" : " dead") });
    dot.append(s("title", {}, b.name + " " + (b.class || "") + " " + (b.role || "") + ": " + b.hp_pct + " % hp, " + b.power_pct + " % " + (b.power_type || "power") +
      (b.dist_tank != null ? ", " + b.dist_tank.toFixed(0) + " yd from the tank" : "") + (b.target ? ", on " + b.target : "")));
    kids.push(dot, s("text", { x: b.x + r * 2, y: b.y - r * 1.5 }, b.name));
  }
  if (c.commander) {
    const d = s("circle", { cx: c.commander.x, cy: c.commander.y, r: r * 1.4, class: "cmd" });
    d.append(s("title", {}, "commander " + c.commander.name));
    kids.push(d);
  }
  dyn.replaceChildren(...kids);
  const note = !c.bots.some((b) => b.known) ? (c.feed.have ? "no bot is inside this map" : "waiting for the bots' feed") : "";
  let n = m.querySelector(".nomap");
  if (note) { if (!n) { n = h("div", { class: "nomap", style: "inset:auto 0 0 0;padding:4px" }); m.append(n); } n.textContent = note; } else if (n) n.remove();
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
