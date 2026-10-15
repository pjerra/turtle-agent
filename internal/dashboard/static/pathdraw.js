"use strict";
// Draws the path layers (see paths.js for the geometry) as SVG elements for one card's map. Nothing here makes markup from a string: every element is
// created with createElementNS and every text goes in with textContent. The page passes in what it already has (the svg helpers, the layer choice, the
// styles, the card, the plan's drawing and the server's digests), and gets back the elements to put under and over the units, the availability of
// each layer (a layer with no data is greyed out in the panel) and the ghost line for the run line.
(function (root) {
  const MP = root.MapPaths || (typeof require === "function" ? require("./paths.js") : null);
  const MV = root.MapView || (typeof require === "function" ? require("./view.js") : null);

  const css = (o) => Object.entries(o).map(([k, v]) => k + ":" + v).join(";");

  // build(ctx): ctx = {s, plate, at, unitG, on (layer choice), style, card, geo, svg, live, ghost, heat, nowS, lead, scale, groups (the card's own group colour)}.
  function build(ctx) {
    const { s, plate, at, unitG, on, style, card: c, geo, live, ghost, heat } = ctx;
    const under = [], over = [], avail = {};
    const run = c.run || {}, legIdx = run.leg >= 0 ? run.leg : 0;
    const gleg = geo && geo.legs ? geo.legs[legIdx] : null;
    const st = (k, p, d) => (style && style[k] && style[k][p] != null ? style[k][p] : d);
    const opac = (k) => (st(k, "opacity", 100) / 100).toFixed(2);
    const lv = live || {};
    const elapsed = lv.seconds != null && !lv.ended ? lv.seconds : (run.minutes || 0) * 60;
    const mark = (g, key, title, badge, rows) => unitG(g, key, { type: "mark", data: { title, badge, rows } });
    const poly = (pts, attrs) => s("polyline", Object.assign({ points: pts.map((p) => (Array.isArray(p) ? p[0] + "," + p[1] : p.x + "," + p.y)).join(" "), fill: "none", "stroke-linecap": "round", "stroke-linejoin": "round", "vector-effect": "non-scaling-stroke" }, attrs));
    const halo = (pts, w, o) => poly(pts, { stroke: "#000", "stroke-width": w + 3, opacity: o });

    // ---- waypoints: the leg's dashed walk line, its via points, the target point, and where the lead left the plan ----
    const hasRoute = !!(gleg && gleg.path && gleg.path.length >= 2);
    avail.waypoints = hasRoute;
    if (on.waypoints && hasRoute) {
      const w = st("waypoints", "width", 2.5), o = opac("waypoints");
      const line = gleg.path;
      under.push(halo(line, w, (o * 0.6).toFixed(2)), poly(line, { stroke: "#ffb020", "stroke-width": w, "stroke-dasharray": "9 6", "stroke-linecap": "butt", opacity: o }));
      for (const p of gleg.stops || []) {
        if (p.kind !== "via") continue;
        const g = at(p.x, p.y, "wpv");
        g.append(s("polygon", { points: "0,-6 6,0 0,6 -6,0", fill: p.direct ? "#9df7ff" : "#ffb020", stroke: "#000", "stroke-width": 1.5, opacity: o }));
        mark(g, "wp:" + legIdx + ":" + p.n, "Waypoint " + p.n + (p.direct ? " (straight-line step)" : ""), "waypoint", [["Section", gleg.label], ["Step", p.n + " of " + (gleg.stops.length - 1)]]);
        over.push(g);
      }
      const nt = ctx.nextTarget;
      if (nt) {
        const g = at(nt.stop.x, nt.stop.y, "wpt");
        g.append(s("circle", { r: 13, fill: "none", stroke: "#fff", "stroke-width": 3, opacity: o }), s("circle", { r: 5, fill: "#ffb020", stroke: "#000", "stroke-width": 1.5 }), plate("target: " + nt.stop.label, 16, -10, ""));
        mark(g, "wptarget:" + c.id, "Target point: " + nt.stop.label, "target", [["Distance to lead", Math.round(nt.dist) + " yd"], ["Step", nt.index + " of " + (gleg.stops.length - 1)]]);
        over.push(g);
      }
      const off = MP.offRoute(c.trail || [], MP.plannedLines(geo, legIdx), MP.OFF_ROUTE_YD);
      avail.offroute = off.length > 0;
      off.forEach((seg, i) => {
        under.push(halo(seg, w + 1, "0.8"), poly(seg, { stroke: "#ff5a36", "stroke-width": w + 1, opacity: o }));
        if (i === 0) {
          const mid = seg[Math.floor(seg.length / 2)], g = at(mid[0], mid[1], "offr");
          g.append(plate("off the planned route", 8, -8, ""));
          over.push(g);
        }
      });
    }

    // ---- member trails (the lead's own trail is drawn by the page from the card) ----
    const members = (lv.paths || []).filter((p) => !p.lead && p.p && p.p.length >= 2);
    avail.membertrails = members.length > 0;
    if (on.trail_members && members.length) {
      const mins = ctx.layers.trailMinutes(style, "trail_members"), w = st("trail_members", "width", 2), o = opac("trail_members");
      for (const p of members) {
        const bot = (c.bots || []).find((b) => b.name === p.name) || { role: p.role };
        const col = MP.trailColor(MP.roleOf(bot), false);
        const r = MP.recent(p.p, p.t, lv.seconds, mins);
        for (const seg of MV.trailSegments(r.points, 16, 0.2)) {
          under.push(halo(seg.points, w, (seg.alpha * 0.5 * o).toFixed(2)), poly(seg.points, { stroke: col, "stroke-width": w, opacity: (seg.alpha * o).toFixed(2) }));
        }
      }
    }

    // ---- the ghost ----
    const hasGhost = !!(ghost && ghost.found && ghost.path && ghost.path.length > 1);
    avail.ghost = hasGhost;
    let ghostLine = "";
    if (hasGhost) {
      const cmp = MP.ghostCompare(ghost, run, ctx.lead, elapsed);
      ghostLine = MP.ghostText(cmp);
      if (on.ghost) {
        const w = st("ghost", "width", 3), o = opac("ghost");
        const pts = ghost.path.map((p) => [p[0], p[1]]);
        under.push(poly(pts, { stroke: "#e9d5ff", "stroke-width": w, "stroke-dasharray": "2 7", opacity: o }));
        const gp = MP.ghostAt(ghost.path, run.state === "running" ? elapsed : ghost.seconds);
        if (gp) {
          const g = at(gp.x, gp.y, "ghostm");
          g.append(s("circle", { r: 9, fill: "#e9d5ff", stroke: "#6b21a8", "stroke-width": 2, opacity: Math.min(1, +o + 0.3) }), plate("ghost" + (cmp && cmp.delta != null ? " " + MP.ghostText(cmp).replace(/^ghost /, "") : ""), 13, 4, ""));
          mark(g, "ghost:" + c.id, "Ghost: " + MP.ghostDescribe(ghost), "ghost", [["Run", ghost.run], ["Time here", MP.clockMin(Math.min(elapsed, ghost.seconds))], ["Compared with this run", ghostLine || "no comparison yet"]]);
          over.push(g);
        }
      }
    }

    // ---- pull groups by state, and the boss marks ----
    const states = MP.groupStates(geo, run, lv.pulls, !!(lv.paths && lv.paths.length));
    if (ctx.svg) {
      ctx.svg.querySelectorAll(".pull, .pin").forEach((e) => {
        for (const k of ["cleared", "next", "skipped", "failed", "pending"]) e.classList.remove("ps-" + k);
        const g = states[e.dataset.leg + ":" + e.dataset.group];
        if (g && on.pullstate) e.classList.add("ps-" + g.state);
      });
      ctx.svg.classList.toggle("ps-on", !!on.pullstate);
    }
    if (on.pullstate) {
      const o = opac("pullstate");
      for (const g of Object.values(states)) {
        const pk = geo.legs[g.leg].packs.find((x) => x.index === g.index);
        if (!pk) continue;
        const m = at(pk.x, pk.y, "psm");
        if (g.state === "next") m.append(s("circle", { r: 15, fill: "none", stroke: g.color, "stroke-width": 3, opacity: o }));
        mark(m, "ps:" + g.leg + ":" + g.index, pk.label, g.state, [["Section", geo.legs[g.leg].label], ["State", g.text], ["Creatures", pk.n + (pk.ambush ? " (ambush: stealthed)" : "")], ["Radius", Math.round(pk.r) + " yd"]]);
        over.push(m);
      }
      const killed = new Set((c.events || []).filter((e) => e.kind === "boss").map((e) => e.text));
      const doneLegs = new Set((lv.legs || []).filter((l) => l.result === "done").map((l) => l.leg));
      (geo.legs || []).forEach((l, li) => {
        if (!l.boss_at) return;
        const name = (geo.names && geo.names[String(l.boss)]) || "boss " + l.boss;
        const dead = killed.has(name + " killed") || doneLegs.has(li) || (run.leg > li);
        const g = at(l.boss_at.x, l.boss_at.y, "bossst");
        g.append(s("circle", { cx: 16, cy: -16, r: 8, fill: dead ? "#22c55e" : "#1f2937", stroke: "#000", "stroke-width": 1.5, opacity: o }), s("text", { x: 16, y: -12.5, class: "g", fill: "#fff", "text-anchor": "middle", "font-size": "11" }, dead ? "✓" : "□"));
        over.push(g);
      });
    }

    // ---- rest and gather spots ----
    const rests = lv.rests || [], gathers = lv.gathers || [];
    avail.restgather = rests.length + gathers.length > 0;
    if (on.restgather) {
      const o = opac("restgather");
      rests.forEach((r, i) => {
        const g = at(r.x, r.y, "rg rest");
        g.append(s("circle", { r: 9, fill: "#1e3a8a", stroke: "#fff", "stroke-width": 1.8, opacity: o }), s("text", { class: "g", y: 4, fill: "#fff", "text-anchor": "middle", "font-size": "12" }, "☕"));
        mark(g, "rest:" + c.id + ":" + i, "Rest spot", "rest", [["Rested", Math.round(r.secs) + " s"], ["Section", (r.leg >= 0 && geo.legs[r.leg] ? geo.legs[r.leg].label : "")]]);
        over.push(g);
      });
      gathers.forEach((r, i) => {
        const g = at(r.x, r.y, "rg gather");
        g.append(s("circle", { r: 8, fill: "#065f46", stroke: "#fff", "stroke-width": 1.8, opacity: o }), s("text", { class: "g", y: 4, fill: "#fff", "text-anchor": "middle", "font-size": "11" }, "◎"));
        mark(g, "gather:" + c.id + ":" + i, "Gather spot", "gather", [["Why", r.text || "the party gathers before the pull"], ["Section", (r.leg >= 0 && geo.legs[r.leg] ? geo.legs[r.leg].label : "")]]);
        over.push(g);
      });
    }

    // ---- objects ----
    const objs = MP.objectStates(geo, lv.objects, lv.legs, run);
    avail.objects = objs.length > 0;
    if (on.objects) {
      const o = opac("objects");
      objs.forEach((x, i) => {
        const g = at(x.obj.x, x.obj.y, "objst");
        const glyph = x.state === "failed" ? "✕" : x.state === "pending" ? "?" : "✓";
        g.append(s("rect", { x: -8, y: -8, width: 16, height: 16, rx: 3, fill: "#0b0e13", stroke: x.color, "stroke-width": 2.5, opacity: o }), s("text", { class: "g", y: 4, fill: x.color, "text-anchor": "middle", "font-size": "12" }, glyph), plate(x.obj.label + ": " + x.state, 12, 4, ""));
        mark(g, "obj:" + c.id + ":" + i, x.obj.label, x.obj.kind, [["State", x.text], ["Section", geo.legs[x.leg].label], ["Step", (x.obj.after ? "after the boss, " : "") + "step " + (x.obj.step + 1)]]);
        over.push(g);
      });
    }

    // ---- patrols ----
    const patrols = [];
    (geo && geo.legs ? geo.legs : []).forEach((l, li) => (l.packs || []).forEach((p) => { if (p.patrol && p.patrol.path && p.patrol.path.length >= 2) patrols.push({ leg: li, pack: p, patrol: p.patrol }); }));
    avail.patrols = patrols.length > 0;
    if (on.patrols) {
      const w = st("patrols", "width", 2), o = opac("patrols");
      patrols.forEach((pt, i) => {
        const path = pt.patrol.path.map((p) => [p.x, p.y]), loop = path.concat([path[0]]);
        under.push(halo(loop, w, (o * 0.5).toFixed(2)), poly(loop, { stroke: "#c084fc", "stroke-width": w, "stroke-dasharray": "3 6", opacity: o }));
        const lp = MP.patrolLive(pt.patrol, c.mobs || []);
        const where = lp || { x: path[0][0], y: path[0][1] };
        const g = at(where.x, where.y, "patm");
        g.append(s("polygon", { points: "0,-9 9,0 0,9 -9,0", fill: lp ? "#c084fc" : "#1f2937", stroke: "#fff", "stroke-width": 2, opacity: o }), plate(lp ? "patrol" : "patrol route", 12, 4, ""));
        mark(g, "pat:" + c.id + ":" + i, "Patrol near " + pt.pack.label, "patrol", [["Section", geo.legs[pt.leg].label], ["Round", pt.patrol.period_s ? Math.round(pt.patrol.period_s) + " s" : ""], ["Seen now", lp ? "yes, in the feed" : "no: not in the feed"]]);
        over.push(g);
      });
    }

    // ---- stalls and refused walks ----
    const stalls = lv.stalls || [], refs = lv.refusals || [];
    avail.stalls = stalls.length > 0;
    avail.refusals = refs.length > 0;
    if (on.stalls) {
      const o = opac("stalls");
      stalls.forEach((x, i) => {
        const g = at(x.x, x.y, "stallm");
        g.append(s("circle", { r: 12, fill: "rgba(255,176,32,.25)", stroke: "#ffb020", "stroke-width": 2.5, opacity: o }), s("text", { class: "g", y: 4, fill: "#fff", "text-anchor": "middle", "font-size": "10" }, Math.round(x.secs) + "s"));
        mark(g, "stall:" + c.id + ":" + i, "Party stood still", "stall", [["For", Math.round(x.secs) + " s" + (x.open ? " (still there)" : "")], ["Why", x.reason], ["Section", x.leg >= 0 && geo.legs[x.leg] ? geo.legs[x.leg].label : ""], ["At", MP.clockMin(x.from) + " into the run"]]);
        over.push(g);
      });
    }
    if (on.refusals) {
      const o = opac("refusals");
      refs.forEach((x, i) => {
        const g = at(x.x, x.y, "refm");
        g.append(s("path", { d: "M-6,-6 L6,6 M6,-6 L-6,6", fill: "none", stroke: "#000", "stroke-width": 6, "stroke-linecap": "round", opacity: o }), s("path", { d: "M-6,-6 L6,6 M6,-6 L-6,6", fill: "none", stroke: "#ff3b3b", "stroke-width": 3, "stroke-linecap": "round", opacity: o }));
        g.append(s("title", {}, MP.refusalText(x)));
        mark(g, "ref:" + c.id + ":" + i, x.kind === "stuck" ? "Bot stuck" : x.kind === "unreachable" ? "Unreachable" : "Walk refused", x.kind, [["Reason", x.text], ["At", MP.clockMin(x.t) + " into the run"], ["Section", x.leg >= 0 && geo.legs[x.leg] ? geo.legs[x.leg].label : ""]]);
        over.push(g);
      });
    }

    // ---- aggro rings around hostile packs ----
    const rings = MP.packRings(c.mobs || [], c.level, 80);
    avail.packrings = rings.length > 0;
    if (on.packrings) {
      const w = st("packrings", "width", 1.5), o = opac("packrings");
      for (const r of rings) under.push(s("circle", { cx: r.x, cy: r.y, r: r.r, fill: "rgba(255,59,59,.05)", stroke: "#ff3b3b", "stroke-width": w, "stroke-dasharray": "6 5", "vector-effect": "non-scaling-stroke", opacity: o }));
    }

    // ---- combat lines ----
    const threat = ctx.threat || [], heal = MP.healLines(c.bots || []), cc = MP.ccLines(c.orders || [], c.bots || [], c.mobs || []);
    avail.combat = threat.length + heal.length + cc.length > 0;
    if (on.combat) {
      const w = st("combat", "width", 2), o = opac("combat");
      const line = (l, col, dash) => {
        under.push(s("line", { x1: l.from.x, y1: l.from.y, x2: l.to.x, y2: l.to.y, stroke: "#000", "stroke-width": w + 2, opacity: (o * 0.5).toFixed(2), "vector-effect": "non-scaling-stroke", "stroke-linecap": "round" }),
          s("line", { x1: l.from.x, y1: l.from.y, x2: l.to.x, y2: l.to.y, stroke: col, "stroke-width": w, "stroke-dasharray": dash || "", opacity: o, "vector-effect": "non-scaling-stroke", "stroke-linecap": "round" }));
        const e = at(l.to.x, l.to.y, "cend");
        e.append(s("circle", { r: 3.5, fill: col, stroke: "#000", "stroke-width": 1 }));
        under.push(e);
      };
      threat.forEach((l) => line(l, "#ff3b3b", ""));
      heal.forEach((l) => line(l, "#22c55e", "6 4"));
      cc.forEach((l) => line(l, "#67e8f9", "2 5"));
    }

    // ---- death heat map ----
    const hb = MP.heatBins(heat && heat.points, 8);
    avail.deathheat = hb.max > 0;
    if (on.deathheat && hb.max > 0) {
      const o = +opac("deathheat");
      for (const b of hb.bins) under.unshift(s("circle", { cx: b.x, cy: b.y, r: 7, fill: "#ff2d2d", opacity: Math.min(1, o * (0.2 + 0.8 * b.n / hb.max)).toFixed(2) }, null));
    }
    return { under, over, avail, ghostLine, states };
  }

  const api = { build };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.PathDraw = api;
})(typeof self !== "undefined" ? self : this);
