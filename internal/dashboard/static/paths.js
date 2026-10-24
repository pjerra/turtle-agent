"use strict";
// The geometry and the words behind the path layers of the map (walk trails, waypoints, pull-group states, objects, the ghost, stalls,
// aggro rings, patrols, combat lines and the death heat map), kept free of the page so a test can run it under node. Everything is in the
// drawing's plane (north up, one unit a yard, the plane Project makes). Inputs are the card, the plan's drawing (geo) and the digests the
// server builds from run traces (see digest.go); a thing the data lacks is left out, never invented.
(function (root) {
  const ROLE_COLOR = { tank: "#2f80ff", healer: "#22c55e", dps: "#ff9f1c" };
  const STATE_COLOR = { cleared: "#22c55e", next: "#ffd400", skipped: "#9aa3b2", failed: "#ff3b3b", pending: "#1f2937" };
  const OBJECT_COLOR = { pending: "#ffffff", open: "#22c55e", looted: "#22c55e", used: "#22c55e", passed: "#9aa3b2", failed: "#ff3b3b" };
  const AGGRO_BASE_YD = 20, AGGRO_MIN_YD = 5, AGGRO_MAX_YD = 45;
  const OFF_ROUTE_YD = 15;
  const guidKey = (g) => (typeof g === "string" ? g.trim().toLowerCase().replace(/^0x/, "").replace(/^0+/, "") : "");
  const num = (v) => typeof v === "number" && isFinite(v);

  const roleOf = (b) => (b && (b.role === "tank" || b.role === "healer") ? b.role : "dps");

  // "#rrggbb" moved k (0..1) of the way to white.
  function brighten(hex, k) {
    const m = /^#([0-9a-f]{6})$/i.exec(hex || "");
    if (!m) return hex;
    const n = parseInt(m[1], 16), mix = (c) => Math.round(c + (255 - c) * k);
    return "#" + [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((c) => mix(c).toString(16).padStart(2, "0")).join("");
  }
  // The colour of a character's trail: its role's colour, the lead's brighter.
  function trailColor(role, lead) { const c = ROLE_COLOR[role === "tank" || role === "healer" ? role : "dps"]; return lead ? brighten(c, 0.45) : c; }

  // ---- trails ----

  // The part of a walk that is at most `minutes` old (null: all of it): times are in the unit of nowS (unix seconds for a card's trail, seconds
  // since the run began for a digest). The point before the cut stays so that the line starts at the edge. Without times the walk is whole.
  function recent(points, times, nowS, minutes) {
    if (!points || !points.length) return { points: [], times: [] };
    if (minutes == null || !times || times.length !== points.length || !num(nowS)) return { points: points.slice(), times: times ? times.slice() : [] };
    const cut = nowS - minutes * 60;
    let i = 0;
    while (i < points.length && times[i] < cut) i++;
    const from = Math.max(0, i - 1);
    return { points: points.slice(from), times: times.slice(from) };
  }

  // Distance from (x, y) to the polyline `line` ([[x, y], ...] or [{x, y}, ...]).
  function pt(p) { return Array.isArray(p) ? { x: p[0], y: p[1] } : p; }
  function segDist(x, y, a, b) {
    const dx = b.x - a.x, dy = b.y - a.y, l2 = dx * dx + dy * dy;
    const t = l2 === 0 ? 0 : Math.max(0, Math.min(1, ((x - a.x) * dx + (y - a.y) * dy) / l2));
    return Math.hypot(x - (a.x + t * dx), y - (a.y + t * dy));
  }
  function distToLine(x, y, line, closed) {
    let best = Infinity;
    const n = line.length;
    if (n === 1) { const a = pt(line[0]); return Math.hypot(x - a.x, y - a.y); }
    for (let i = 1; i < n; i++) best = Math.min(best, segDist(x, y, pt(line[i - 1]), pt(line[i])));
    if (closed && n > 2) best = Math.min(best, segDist(x, y, pt(line[n - 1]), pt(line[0])));
    return best;
  }

  // The pieces of a walk that lie farther than thr yards from every planned line (planned vs walked). The walk is sampled every ~4 yd, since
  // the server keeps a straight stretch as two points. Returns polylines [[x, y], ...] of at least two samples.
  function offRoute(trail, lines, thr) {
    const lim = thr == null ? OFF_ROUTE_YD : thr, out = [];
    if (!trail || trail.length < 2 || !lines || !lines.length) return out;
    const off = (x, y) => lines.every((l) => l.length > 0 && distToLine(x, y, l) > lim);
    let cur = [];
    const flush = () => { if (cur.length >= 2) out.push(cur); cur = []; };
    for (let i = 1; i < trail.length; i++) {
      const a = trail[i - 1], b = trail[i], d = Math.hypot(b[0] - a[0], b[1] - a[1]), n = Math.max(1, Math.ceil(d / 4));
      for (let k = (i === 1 ? 0 : 1); k <= n; k++) {
        const x = a[0] + (b[0] - a[0]) * k / n, y = a[1] + (b[1] - a[1]) * k / n;
        if (off(x, y)) cur.push([Math.round(x * 10) / 10, Math.round(y * 10) / 10]); else flush();
      }
    }
    flush();
    return out;
  }

  // The planned lines of the legs up to and including `leg` ([[x, y]] each); the first starts at the entrance.
  function plannedLines(geo, leg) {
    const out = [];
    ((geo && geo.legs) || []).forEach((l, i) => { if (i <= leg && l.path && l.path.length) out.push(l.path.map((p) => [p.x, p.y])); });
    return out;
  }

  // ---- pull groups ----

  const OUTCOME_WORDS = {
    won: "won", evaded: "the creatures evaded", no_engage: "the pull did not engage", error: "the pull errored", refused: "the module refused the pull", retarget: "had to pick another target",
  };

  // The state of every pull group of the plan: key "leg:index" -> {state: cleared | next | skipped | failed | pending, text}. pulls are the digest's
  // {leg, x, y, outcome}; haveTrace says the digest holds this run (without it a group behind the run counts as cleared, since nothing says otherwise).
  function groupStates(geo, run, pulls, haveTrace) {
    const out = {}, legs = (geo && geo.legs) || [], rl = run && run.leg != null ? run.leg : -1;
    const running = !!run && run.state === "running", finished = !!run && (run.state === "done" || run.state === "failed" || run.state === "stopped");
    const byGroup = {};
    for (const p of pulls || []) {
      let best = null, bd = Infinity;
      for (const g of (legs[p.leg] && legs[p.leg].packs) || []) {
        for (const q of g.points && g.points.length ? g.points : [{ x: g.x, y: g.y }]) {
          const d = Math.hypot(p.x - q.x, p.y - q.y);
          if (d <= g.r + 6 && d < bd) { best = g; bd = d; }
        }
      }
      if (best) (byGroup[p.leg + ":" + best.index] = byGroup[p.leg + ":" + best.index] || []).push(p);
    }
    const curG = running && run.group > 0 ? run.group - 1 : -1;
    let nextSet = false;
    legs.forEach((l, li) => {
      for (const g of l.packs || []) {
        const key = li + ":" + g.index, ps = byGroup[key] || [];
        const behind = rl >= 0 && (li < rl || (li === rl && curG >= 0 && g.index < curG));
        let st, text;
        if (ps.some((p) => p.outcome === "won")) { st = "cleared"; text = "cleared"; }
        else if (ps.length && ps[ps.length - 1].outcome === "") { st = "next"; text = "being pulled now"; nextSet = true; }
        else if (ps.length) {
          const last = ps[ps.length - 1];
          st = "failed"; text = "failed: " + (OUTCOME_WORDS[last.outcome] || last.outcome) + (ps.length > 1 ? " (" + ps.length + " tries)" : "");
        } else if (behind || finished) {
          st = haveTrace ? "skipped" : "cleared";
          text = haveTrace ? "skipped: no pull of it in the trace" : "cleared (the run is past it)";
        } else if (!nextSet && running && rl >= 0 && (li > rl || (li === rl && g.index >= Math.max(curG, 0)))) {
          st = "next"; text = "next to pull"; nextSet = true;
        } else { st = "pending"; text = "not reached yet"; }
        out[key] = { state: st, text, leg: li, index: g.index, color: STATE_COLOR[st] };
      }
    });
    return out;
  }

  // ---- objects ----

  // The state of every object step of the plan: [{leg, obj, state: pending | open | looted | used | passed | failed, text}]. objs are the digest's step records
  // {leg, action, entry, x, y, ok, step}, legs its legs ({leg, result}).
  function objectStates(geo, objs, legs, run) {
    const out = [], rl = run && run.leg != null ? run.leg : -1;
    ((geo && geo.legs) || []).forEach((l, li) => {
      for (const o of l.objects || []) {
        const m = (objs || []).filter((r) => r.leg === li && Math.hypot(r.x - o.x, r.y - o.y) <= 8);
        const done = m.find((r) => r.ok), bad = m.find((r) => !r.ok && r.action !== "wait" && r.action !== "wait_object");
        const legDone = (legs || []).some((g) => g.leg === li && g.result === "done");
        const laterOk = (objs || []).some((r) => r.leg === li && r.ok && r.step > o.step);
        let state, text;
        if (done) {
          state = o.kind === "loot" ? "looted" : o.kind === "door" ? "open" : "used";
          text = state;
        } else if (o.kind === "door" && (legDone || laterOk)) { state = "open"; text = "open (the run went on past it)"; }
        else if (bad) { state = "failed"; text = "failed: " + (bad.text || bad.action); }
        else if (legDone || (rl > li)) { state = "passed"; text = "passed (the trace has no record of it)"; }
        else { state = "pending"; text = "not done yet"; }
        out.push({ leg: li, obj: o, state, text, color: OBJECT_COLOR[state] });
      }
    });
    return out;
  }

  // ---- aggro rings ----

  // How far a creature of mobLevel notices a party of partyLevel: 20 yd at equal level, one yard more per level above, one less per level below, 5 to 45.
  // Without both levels, 20.
  function aggroRadius(mobLevel, partyLevel) {
    if (!(mobLevel > 0) || !(partyLevel > 0)) return AGGRO_BASE_YD;
    return Math.max(AGGRO_MIN_YD, Math.min(AGGRO_MAX_YD, AGGRO_BASE_YD + (mobLevel - partyLevel)));
  }

  // One ring per hostile pack that is still to do: the creatures of a pull group share a ring around their middle that reaches each one's aggro range;
  // a creature of no group has its own. [{x, y, r, n, label}], at most cap.
  function packRings(mobs, partyLevel, cap) {
    const groups = new Map();
    for (const m of mobs || []) {
      if (m.kind !== "hostile" || m.cleared || m.hp_pct === 0 || !num(m.x) || !num(m.y)) continue;
      const k = m.group >= 0 && m.leg >= 0 ? m.leg + ":" + m.group : "m:" + m.guid;
      if (!groups.has(k)) groups.set(k, []);
      groups.get(k).push(m);
    }
    const out = [];
    for (const list of groups.values()) {
      const cx = list.reduce((a, m) => a + m.x, 0) / list.length, cy = list.reduce((a, m) => a + m.y, 0) / list.length;
      let r = 0;
      for (const m of list) r = Math.max(r, Math.hypot(m.x - cx, m.y - cy) + aggroRadius(m.level, partyLevel));
      out.push({ x: cx, y: cy, r, n: list.length, level: list.some((m) => m.level > 0) });
    }
    out.sort((a, b) => b.n - a.n);
    return out.slice(0, cap == null ? 80 : cap);
  }

  // ---- patrols ----

  // Where a patrol is now: the feed's creature of the patrol's entries that stands nearest to its route (within maxD yards of it), else null.
  function patrolLive(patrol, mobs, maxD) {
    if (!patrol || !patrol.path || patrol.path.length < 2) return null;
    const ent = new Set(patrol.entries || []), lim = maxD == null ? 25 : maxD;
    let best = null, bd = Infinity;
    for (const m of mobs || []) {
      if (!ent.has(m.entry) || !num(m.x) || !num(m.y)) continue;
      const d = distToLine(m.x, m.y, patrol.path, true);
      if (d <= lim && d < bd) { best = m; bd = d; }
    }
    return best ? { x: best.x, y: best.y, mob: best } : null;
  }

  // ---- combat lines ----

  // Healer to the party member it targets (the feed gives a healer's target, not the spell's: a healer that targets a friend is taken to heal it).
  function healLines(bots) {
    const byKey = new Map();
    for (const b of bots || []) { const k = guidKey(b.guid); if (k) byKey.set(k, b); }
    const out = [];
    for (const b of bots || []) {
      if (!b.known || b.alive === false || b.role !== "healer") continue;
      const t = byKey.get(guidKey(b.target_guid));
      if (t && t !== b && t.known && t.alive !== false) out.push({ from: { x: b.x, y: b.y }, to: { x: t.x, y: t.y }, bot: b, target: t });
    }
    return out;
  }

  // Crowd control: the commander's `cmd cc <kind> <creature guid> <caster>` orders the module answered, drawn from the caster to the creature while the feed
  // says the creature is crowd controlled. The feed does not name who put a crowd control on, so an order is the only source.
  function ccLines(orders, bots, mobs) {
    const out = [], seen = new Set();
    for (const o of (orders || []).slice().reverse()) {
      const m = /^cmd cc (\S+) (0x[0-9a-fA-F]+) (.+)$/.exec(o.text || "");
      if (!m || String(o.reply || "").toUpperCase() !== "ACK") continue;
      const mob = (mobs || []).find((x) => guidKey(x.guid) === guidKey(m[2]));
      const caster = (bots || []).find((b) => b.name === m[3].trim());
      if (!mob || !caster || !caster.known || !mob.cc || seen.has(guidKey(m[2]))) continue;
      seen.add(guidKey(m[2]));
      out.push({ from: { x: caster.x, y: caster.y }, to: { x: mob.x, y: mob.y }, caster, mob, kind: m[1] });
    }
    return out;
  }

  // ---- the ghost ----

  const clockMin = (sec) => { const s = Math.max(0, Math.round(sec)); return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0"); };

  // Where the ghost was `t` seconds into its run (linear between its points; the first or last point outside them), or null.
  function ghostAt(path, t) {
    if (!path || !path.length || !num(t)) return null;
    if (t <= path[0][2]) return { x: path[0][0], y: path[0][1] };
    for (let i = 1; i < path.length; i++) {
      if (t <= path[i][2]) {
        const a = path[i - 1], b = path[i], span = b[2] - a[2], f = span > 0 ? (t - a[2]) / span : 1;
        return { x: a[0] + (b[0] - a[0]) * f, y: a[1] + (b[1] - a[1]) * f };
      }
    }
    const l = path[path.length - 1];
    return { x: l[0], y: l[1] };
  }

  // How the run stands against the ghost: the ghost's own time at the spot the lead stands on (the nearest point of its walk through the same section, within
  // maxD yards) against the run's elapsed seconds. delta > 0: the ghost was there first (it is ahead); delta < 0: the run is ahead.
  // Returns {delta, ghostT} or {note} when no comparison can be made, or null without a ghost.
  function ghostCompare(ghost, run, lead, elapsedS, maxD) {
    if (!ghost || !ghost.found || !ghost.path || !ghost.path.length) return null;
    if (!run || run.leg == null || run.leg < 0 || !lead || !num(elapsedS)) return null;
    const gl = (ghost.legs || []).find((l) => l.leg === run.leg);
    if (!gl) return { note: "the ghost never reached this section" };
    const lo = gl.start, hi = gl.end > 0 ? gl.end : Infinity, lim = maxD == null ? 60 : maxD;
    let best = null, bd = Infinity;
    for (const p of ghost.path) {
      if (p[2] < lo - 1 || p[2] > hi + 1) continue;
      const d = Math.hypot(p[0] - lead.x, p[1] - lead.y);
      if (d < bd) { best = p; bd = d; }
    }
    if (!best || bd > lim) return { note: "the lead is off the ghost's route here" };
    return { delta: elapsedS - best[2], ghostT: best[2] };
  }

  // "ghost is 1.3 min ahead", "ghost is 0.5 min behind", "level with the ghost"; "" for none.
  function ghostText(cmp) {
    if (!cmp) return "";
    if (cmp.note) return cmp.note;
    if (Math.abs(cmp.delta) < 5) return "level with the ghost";
    return "ghost is " + (Math.abs(cmp.delta) / 60).toFixed(1) + " min " + (cmp.delta > 0 ? "ahead" : "behind");
  }

  // The words about the ghost for the hover box / legend: "best earlier run: cleared in 41:07 (folder)".
  function ghostDescribe(g) {
    if (!g || !g.found) return "";
    const left = g.left > 0 ? ", " + g.left + " pack" + (g.left === 1 ? "" : "s") + " left" : "";
    return (g.complete ? "fastest clear: " : "furthest earlier run (" + g.legs_done + (g.plan_legs ? " of " + g.plan_legs : "") + " sections): ") + clockMin(g.seconds) + left + (g.folder ? " (" + g.folder + ")" : "");
  }

  // ---- stalls and refusals ----

  const stallText = (s) => "stood still " + Math.round(s.secs) + " s" + (s.open ? " (still there)" : "") + ": " + s.reason;
  const refusalText = (r) => (r.kind === "stuck" ? "stuck: " : r.kind === "unreachable" ? "unreachable: " : "walk refused: ") + (r.text || "");

  // ---- death heat ----

  // Death points [[x, y, leg], ...] counted into cells of `cell` yards: [{x, y, n}] (the cell's middle), and the largest count.
  function heatBins(points, cell) {
    const c = cell || 8, m = new Map();
    for (const p of points || []) {
      if (!num(p[0]) || !num(p[1])) continue;
      const ix = Math.floor(p[0] / c), iy = Math.floor(p[1] / c), k = ix + "," + iy;
      const e = m.get(k) || { x: (ix + 0.5) * c, y: (iy + 0.5) * c, n: 0 };
      e.n++;
      m.set(k, e);
    }
    const bins = [...m.values()];
    return { bins, max: bins.reduce((a, b) => Math.max(a, b.n), 0) };
  }

  const api = {
    ROLE_COLOR, STATE_COLOR, OBJECT_COLOR, OFF_ROUTE_YD, roleOf, brighten, trailColor, recent, distToLine, offRoute, plannedLines, groupStates, objectStates, aggroRadius,
    packRings, patrolLive, healLines, ccLines, ghostAt, ghostCompare, ghostText, ghostDescribe, stallText, refusalText, heatBins, clockMin,
  };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.MapPaths = api;
})(typeof self !== "undefined" ? self : this);
