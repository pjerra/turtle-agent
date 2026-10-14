"use strict";
// What a map can draw (the Layers panel), kept free of the page so a test can run it under node. One choice for every map and every card,
// saved once in the browser; defaults are what the maps drew before layers could be chosen, anything added later starts off. A layer
// names the data it needs: with none on a card's map it is greyed out in the panel with the reason and nothing is drawn. The helpers at
// the end work out the geometry of the newer layers in the drawing's plane (north up, one unit a yard, the plane Project makes).
(function (root) {
  const STORAGE_KEY = "maplayers.v2"; // one key for all maps (the older per-map keys, "maplayers:<map id>", are no longer read)
  const HEAL_RANGE_YD = 40;           // a healer's spells reach this far
  const AGGRO_YD = 20;                // a mob this close to the party notices it
  const HEADING_MIN_YD = 4;           // the lead must have moved this far for a heading
  const HEADING_MAX_AGE_S = 20;       // and the last point must be this recent

  // needs: "" (always) or a key of availability()'s context. Each layer is a class "hide-<key>" on the svg when off (the stylesheet).
  const DEFS = [
    { key: "bg", label: "Game map picture", group: "Map", def: true, needs: "picture", why: "no map picture for this map (turtle-agent minimap-extract)" },
    { key: "grid", label: "Grid", group: "Map", def: false, needs: "" },
    { key: "scale", label: "Distance scale bar", group: "Map", def: false, needs: "" },
    { key: "zonenames", label: "Zone and sub-zone names", group: "Map", def: false, needs: "zones", why: "this dashboard has no zone outlines for dungeon maps" },
    { key: "route", label: "Planned route", group: "Plan", def: true, needs: "route", why: "the plan has no route here" },
    { key: "packs", label: "Pull-group circles", group: "Plan", def: true, needs: "packs", why: "the plan has no pull groups here" },
    { key: "cleared", label: "Cleared (dimmed) groups", group: "Plan", def: true, needs: "cleared", why: "nothing is cleared yet" },
    { key: "bosses", label: "Bosses", group: "Plan", def: true, needs: "bosses", why: "the plan has no boss here" },
    { key: "doors", label: "Doors and objects", group: "Plan", def: true, needs: "doors", why: "the plan has no door or object here" },
    { key: "sections", label: "Section labels", group: "Plan", def: true, needs: "sections", why: "the plan has no sections here" },
    { key: "steps", label: "Step numbers", group: "Plan", def: false, needs: "steps", why: "the plan has no steps here" },
    { key: "trail", label: "Walked path", group: "Walk", def: true, needs: "trail", why: "the lead has not moved yet" },
    { key: "trailtime", label: "Times on the walked path", group: "Walk", def: false, needs: "trailtime", why: "the walked path has no times yet" },
    { key: "heading", label: "Lead's heading arrow (from its movement)", group: "Walk", def: false, needs: "heading", why: "the lead has not moved enough to tell" },
    { key: "next", label: "Next-stop arrow", group: "Walk", def: true, needs: "next", why: "no leg is being walked" },
    { key: "bots", label: "Bot dots", group: "Party", def: true, needs: "bots", why: "no bot position yet" },
    { key: "botnames", label: "Bot names", group: "Party", def: true, needs: "bots", why: "no bot position yet" },
    { key: "bottarget", label: "Bot target lines", group: "Party", def: false, needs: "botTargets", why: "no bot has a target I can place" },
    { key: "healring", label: "Healer range (40 yd)", group: "Party", def: false, needs: "healer", why: "no living healer on the map" },
    { key: "commander", label: "Commander", group: "Party", def: true, needs: "commander", why: "the commander is not on this map" },
    { key: "cmdline", label: "Line from the commander to the lead", group: "Party", def: false, needs: "cmdline", why: "needs the commander and a lead on this map" },
    { key: "hostile", label: "Hostile mobs", group: "Mobs", def: true, needs: "hostile", why: "no hostile mob in the feed" },
    { key: "neutral", label: "Neutral and friendly mobs", group: "Mobs", def: false, needs: "neutral", why: "no neutral or friendly mob in the feed" },
    { key: "critter", label: "Critters", group: "Mobs", def: false, needs: "critter", why: "no critter in the feed" },
    { key: "threat", label: "Threat lines (mob to whom it targets)", group: "Mobs", def: false, needs: "threat", why: "no mob has a target I can place" },
    { key: "aggro", label: "Aggro range (20 yd) around pull targets", group: "Mobs", def: false, needs: "pulltargets", why: "no pull target now" },
  ];

  const keys = () => DEFS.map((d) => d.key);
  function defaults() { const o = {}; DEFS.forEach((d) => { o[d.key] = d.def; }); return o; }

  // Whatever was stored, as a full set: unknown keys dropped, non-booleans back to the default.
  function normalize(raw) {
    const out = defaults();
    if (raw && typeof raw === "object" && !Array.isArray(raw)) DEFS.forEach((d) => { if (typeof raw[d.key] === "boolean") out[d.key] = raw[d.key]; });
    return out;
  }
  function load(storage) {
    try { const s = storage && storage.getItem(STORAGE_KEY); return normalize(s ? JSON.parse(s) : null); } catch (e) { return defaults(); }
  }
  function save(storage, state) {
    try { if (!storage) return false; storage.setItem(STORAGE_KEY, JSON.stringify(normalize(state))); return true; } catch (e) { return false; }
  }
  function set(state, key, on) { const out = normalize(state); if (key in out) out[key] = !!on; return out; }

  // {key: {ok, why}} for one card's map. ctx has the booleans named by `needs`; a missing one is false.
  function availability(ctx) {
    const c = ctx || {}, out = {};
    DEFS.forEach((d) => { out[d.key] = !d.needs || c[d.needs] ? { ok: true, why: "" } : { ok: false, why: d.why || "no data" }; });
    return out;
  }
  const groups = () => DEFS.reduce((g, d) => (g.indexOf(d.group) < 0 ? g.concat(d.group) : g), []);

  // ---- geometry of the newer layers ----

  // A guid as the feed prints it ("0xF130001C4E00AC91", "0x00000000000002DC"), compared without case and leading zeros; "" for none.
  function guidKey(g) {
    if (typeof g !== "string") return "";
    const m = /^(?:0x)?([0-9a-fA-F]+)$/.exec(g.trim());
    if (!m) return "";
    const k = m[1].toLowerCase().replace(/^0+/, "");
    return k;
  }

  // Lines from each unit to the unit it targets: [{from: {x, y}, to: {x, y}}]. units are {guid, target_guid, x, y} (bots need known).
  function targetLines(units, targets) {
    const at = new Map();
    for (const u of targets) { const k = guidKey(u.guid); if (k) at.set(k, u); }
    const out = [];
    for (const u of units) {
      const k = guidKey(u.target_guid);
      const t = k ? at.get(k) : null;
      if (t && isFinite(u.x) && isFinite(u.y) && isFinite(t.x) && isFinite(t.y)) out.push({ from: { x: u.x, y: u.y }, to: { x: t.x, y: t.y }, unit: u, target: t });
    }
    return out;
  }

  // Mob to whom it targets: the mobs' targets among the bots.
  function threatLines(mobs, bots) { return targetLines(mobs, bots.filter((b) => b.known)); }
  // Bot to its target: a mob (or another bot).
  function botTargetLines(bots, mobs) { return targetLines(bots.filter((b) => b.known), mobs.concat(bots.filter((b) => b.known))); }

  // The mobs the party is about to pull or is pulling: the hostile ones of the current pack (leg and group of the run), else the hostile
  // ones that are fighting. run is {leg, group, state}; a mob has leg, group, kind, engaged, cleared.
  function pullTargets(mobs, run) {
    const hostile = mobs.filter((m) => m.kind === "hostile" && !m.cleared);
    if (run && run.state === "running" && run.leg >= 0 && run.group > 0) {
      const pack = hostile.filter((m) => m.leg === run.leg && m.group === run.group - 1);
      if (pack.length) return pack;
    }
    return hostile.filter((m) => m.engaged);
  }

  // Unit vector (plane) of the lead's walk: from the last trail point at least minYd behind the newest, or null when the lead has not
  // moved that far or the newest point is older than maxAgeS (times: unix seconds per point, nowS: unix seconds now; without times no age check).
  function heading(trail, times, nowS, minYd, maxAgeS) {
    if (!trail || trail.length < 2) return null;
    const last = trail[trail.length - 1], min = minYd == null ? HEADING_MIN_YD : minYd;
    if (times && times.length === trail.length && nowS && nowS - times[times.length - 1] > (maxAgeS == null ? HEADING_MAX_AGE_S : maxAgeS)) return null;
    for (let i = trail.length - 2; i >= 0; i--) {
      const d = Math.hypot(last[0] - trail[i][0], last[1] - trail[i][1]);
      if (d >= min) return { x: (last[0] - trail[i][0]) / d, y: (last[1] - trail[i][1]) / d };
    }
    return null;
  }

  // Which trail points get a time label: from the newest back, one each time the picture has moved minPx since the last labelled, at most
  // maxN. trail [[x, y]], times unix seconds in step. Returns [{x, y, t}] oldest first.
  function trailStamps(trail, times, scale, minPx, maxN) {
    const out = [];
    if (!trail || !times || trail.length !== times.length) return out;
    let prev = null;
    for (let i = trail.length - 1; i >= 0 && out.length < (maxN == null ? 8 : maxN); i--) {
      if (!times[i]) continue;
      const p = { x: trail[i][0], y: trail[i][1] };
      if (prev && Math.hypot(p.x - prev.x, p.y - prev.y) * scale < (minPx == null ? 80 : minPx)) continue;
      out.push({ x: p.x, y: p.y, t: times[i] });
      prev = p;
    }
    return out.reverse();
  }

  // The longest round number of yards that is at most maxPx pixels wide at this scale (pixels per yard): {yd, px}.
  function scaleBar(scale, maxPx) {
    const steps = [1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000];
    let yd = steps[0];
    steps.forEach((s) => { if (s * scale <= (maxPx == null ? 110 : maxPx)) yd = s; });
    return { yd, px: yd * scale };
  }

  // Grid lines about 80 px apart: the step in yards for a scale, and the line positions inside a plane box [x, y, w, h].
  function gridStep(scale) {
    const raw = 80 / scale, steps = [5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000];
    return steps.find((s) => s >= raw) || 10000;
  }
  function gridLines(box, step) {
    const xs = [], ys = [];
    for (let x = Math.ceil(box[0] / step) * step; x <= box[0] + box[2]; x += step) xs.push(x);
    for (let y = Math.ceil(box[1] / step) * step; y <= box[1] + box[3]; y += step) ys.push(y);
    return { xs, ys };
  }

  const api = {
    STORAGE_KEY, HEAL_RANGE_YD, AGGRO_YD, DEFS, keys, defaults, normalize, load, save, set, availability, groups,
    guidKey, threatLines, botTargetLines, pullTargets, heading, trailStamps, scaleBar, gridStep, gridLines,
  };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.MapLayers = api;
})(typeof self !== "undefined" ? self : this);
