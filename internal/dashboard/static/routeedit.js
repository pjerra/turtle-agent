"use strict";
// The route editor's model, kept free of the page so a test can run it under node: the edits are a list of ops against the plan file
// (the same ops the server takes), the route you see while editing is the plan's route with the ops applied, and every gesture (drag a
// point, click a segment, delete a point) is turned into an op. Drawing coordinates are the dashboard's plane (north up, one unit a yard);
// ops are in game coordinates, the plan file's: x north, y west.
(function (root) {
  const toGame = (sx, sy) => ({ x: -sy, y: -sx });       // the inverse of the server's Project
  const toDrawing = (x, y) => ({ x: -y, y: -x });

  // ---- the model: the route points of each leg, as the plan draws them ----
  // A point is {x, y} in the drawing plane; a via point may be locked (a direct or check_skip hop: the server will not edit it).
  function buildModel(geo) {
    const legs = (geo && geo.legs ? geo.legs : []).map((l) => {
      const m = { index: l.index, label: l.label || "", name: l.name || "", start: null, via: [], to: null, exitIsTo: !l.boss_at && !(l.packs && l.packs.length),
        packs: (l.packs || []).map((p) => ({ x: p.x, y: p.y, index: p.index, words: p.words || "", entries: p.entries || [], label: p.label || "" })), bossAt: l.boss_at ? { x: l.boss_at.x, y: l.boss_at.y } : null };
      for (const st of l.stops || []) {
        if (st.kind === "start") m.start = { x: st.x, y: st.y };
        else if (st.kind === "via") m.via.push({ x: st.x, y: st.y, locked: !!(st.direct || st.check_skip || st.gm_cross), why: st.gm_cross ? "a gm_cross point (a GM moves the party there)" : st.direct ? "a direct (straight-line) step" : st.check_skip ? "a hop the plan check skips (check_skip)" : "" });
        else if (st.kind === "stage") m.to = { x: st.x, y: st.y };
      }
      return m;
    });
    return { legs };
  }

  // Why an op cannot be applied to the legs, or "".
  function checkOp(legs, op) {
    const l = legs[op.leg];
    if (!l) return "no such leg";
    const n = l.via.length;
    switch (op.kind) {
      case "move":
        if (!(op.index >= 0 && op.index <= n)) return "no such point";
        if (op.index < n && l.via[op.index].locked) return "locked: " + l.via[op.index].why;
        return "";
      case "insert": return op.index >= 0 && op.index <= n ? "" : "no such place";
      case "delete":
        if (!(op.index >= 0 && op.index < n)) return "a leg needs its `to`: only a via point can be deleted";
        return l.via[op.index].locked ? "locked: " + l.via[op.index].why : "";
      default: return "unknown kind";
    }
  }

  // The legs with ops applied, in order: {legs, errors}. An op that cannot apply is skipped and named in errors. The start of a leg that follows a
  // leg with no boss and no pack is that leg's `to`, so it moves with it.
  function apply(model, ops) {
    const legs = model.legs.map((l) => ({ ...l, start: l.start && { ...l.start }, via: l.via.map((v) => ({ ...v })), to: l.to && { ...l.to } }));
    const errors = [];
    (ops || []).forEach((op, i) => {
      const why = checkOp(legs, op);
      if (why) { errors.push("op " + (i + 1) + ": " + why); return; }
      const l = legs[op.leg], p = toDrawing(op.x, op.y);
      if (op.kind === "move") {
        if (op.index < l.via.length) { l.via[op.index].x = p.x; l.via[op.index].y = p.y; } else { l.to.x = p.x; l.to.y = p.y; }
      } else if (op.kind === "insert") l.via.splice(op.index, 0, { x: p.x, y: p.y, locked: false, why: "", inserted: true });
      else l.via.splice(op.index, 1);
    });
    for (let i = 1; i < legs.length; i++) if (legs[i - 1].exitIsTo && legs[i - 1].to && legs[i].start) legs[i].start = { ...legs[i - 1].to };
    return { legs, errors };
  }

  // The points of a leg's route in order: the start, the via points, `to`. kind "start", "via" (with its index) or "to".
  function routePoints(leg) {
    const out = [];
    if (leg.start) out.push({ kind: "start", x: leg.start.x, y: leg.start.y });
    leg.via.forEach((v, i) => out.push({ kind: "via", index: i, x: v.x, y: v.y, locked: v.locked, why: v.why }));
    if (leg.to) out.push({ kind: "to", index: leg.via.length, x: leg.to.x, y: leg.to.y });
    return out;
  }

  // The handles of one leg to drag: every via point and `to` (the start belongs to the leg before).
  function handles(leg, legIndex) {
    return routePoints(leg).filter((p) => p.kind !== "start").map((p) => ({ leg: legIndex, kind: p.kind, index: p.index, x: p.x, y: p.y, locked: !!p.locked, why: p.why || "" }));
  }

  // The handles in the middle of each segment of a leg's route: click one to put a point there, or drag it to put the point where you drop it.
  // Inserting at `index` puts the new via point before the via point of that number (the number of via points: before `to`). A segment shorter than
  // minLen yards (drawing units) has none.
  function midHandles(leg, legIndex, minLen) {
    const pts = routePoints(leg), out = [], lo = minLen == null ? 4 : minLen;
    const first = pts.length && pts[0].kind === "start" ? 0 : -1; // without a start, the first segment is between the first two via points: no
    for (let k = 0; k + 1 < pts.length; k++) {
      const a = pts[k], b = pts[k + 1];
      if (Math.hypot(b.x - a.x, b.y - a.y) < lo) continue;
      const index = first === 0 ? k : k + 1; // the segment from point k to k+1: a new via point goes before the via point of number k
      out.push({ leg: legIndex, index, x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 });
    }
    return out;
  }

  // The nearest handle within radiusPx pixels of a drawing point (scale: pixels per yard), or null. Locked handles are found too: the page says why
  // they do not move.
  function hitTest(hs, x, y, scale, radiusPx) {
    let best = null, bestD = Infinity;
    for (const h of hs) {
      const d = Math.hypot(h.x - x, h.y - y) * scale;
      if (d <= radiusPx && d < bestD) { best = h; bestD = d; }
    }
    return best;
  }

  // ---- gestures to ops ----
  function moveOp(h, x, y) { const g = toGame(x, y); return { leg: h.leg, kind: "move", index: h.index, x: round(g.x), y: round(g.y) }; }
  function insertOp(leg, index, x, y) { const g = toGame(x, y); return { leg, kind: "insert", index, x: round(g.x), y: round(g.y) }; }
  function deleteOp(leg, index) { return { leg, kind: "delete", index }; }
  function round(v) { return Math.round(v * 100) / 100; }

  // ---- undo and redo over the op list ----
  function history(ops) { return { ops: (ops || []).slice(), redo: [] }; }
  function push(h, op) { return { ops: h.ops.concat([op]), redo: [] }; }
  function undo(h) { return h.ops.length ? { ops: h.ops.slice(0, -1), redo: h.redo.concat([h.ops[h.ops.length - 1]]) } : h; }
  function redo(h) { return h.redo.length ? { ops: h.ops.concat([h.redo[h.redo.length - 1]]), redo: h.redo.slice(0, -1) } : h; }

  // ---- words ----
  // north is +x and west is +y in the game; the compass word for a step of (dx, dy) game yards.
  function compass(dx, dy) {
    const d = Math.hypot(dx, dy);
    if (d < 0.05) return "";
    const a = (Math.atan2(dy, dx) * 180) / Math.PI; // 0 = north, 90 = west
    const names = ["north", "north-west", "west", "south-west", "south", "south-east", "east", "north-east"];
    return names[((Math.round(a / 45) % 8) + 8) % 8];
  }
  // The server's changes (a reply's `changes`) in words, one line each: "leg 3 (theka): via 2 moved 6.1 yd east".
  function describeChanges(changes) {
    return (changes || []).map((c) => {
      const where = "leg " + (c.leg + 1) + (c.leg_name ? " (" + c.leg_name + ")" : "") + ": " + c.part;
      if (c.op === "move" && c.from && c.to) {
        const dir = compass(c.to.x - c.from.x, c.to.y - c.from.y), yd = Math.hypot(c.to.x - c.from.x, c.to.y - c.from.y);
        return where + " moved " + yd.toFixed(1) + " yd" + (dir ? " " + dir : "") + (c.floor_picked ? " (floor picked: " + c.floors + " floors in range)" : "");
      }
      if (c.op === "insert") return where + " added" + (c.floor_picked ? " (floor picked: " + c.floors + " floors in range)" : "");
      if (c.op === "delete") return where + " removed";
      return where + " " + c.op;
    });
  }

  // The failing points of a check in the drawing's plane, for the red marks: {x, y, text, isNew}.
  function issueMarks(check) {
    return ((check && check.issues) || []).filter((i) => i.point).map((i) => { const p = toDrawing(i.point.x, i.point.y); return { x: p.x, y: p.y, text: i.text || (i.reason || "issue"), isNew: !!i.new }; });
  }

  // The query for a snap check of one op after the ops before it: GET /api/edit/<plan>/snap?...
  function snapQuery(prior, op) {
    const q = new URLSearchParams({ kind: op.kind, leg: String(op.leg), index: String(op.index), x: String(op.x), y: String(op.y) });
    if (prior && prior.length) q.set("prior", JSON.stringify(prior));
    return q.toString();
  }

  // ---- plain-language labels and hover text ----
  // Every point says what it is in words. A leg is called by its label ("Baelog"); a pack by the creatures in it ("Pack: 2 Stonevault Oracles"), a
  // creature the plan does not name as "creature <entry>".
  function legName(leg) { return (leg && leg.label) || "Section " + ((leg && leg.index) + 1); }
  function packWords(pack) {
    let w = pack && pack.words;
    if (!w && pack && pack.entries && pack.entries.length) {
      const order = [], n = new Map();
      for (const e of pack.entries) { if (!n.has(e)) order.push(e); n.set(e, (n.get(e) || 0) + 1); }
      w = order.map((e) => (n.get(e) > 1 ? n.get(e) + " x " : "") + "creature " + e).join(", ");
    }
    return "Pack: " + (w || (pack && pack.label) || "unknown creatures");
  }
  function pointWords(leg, p) {
    const L = legName(leg);
    if (p.kind === "start") return "Start of leg " + L;
    if (p.kind === "via") return "Walk point " + (p.index + 1) + " of leg " + L;
    return leg.exitIsTo ? "End of leg " + L : "Where the party gathers before " + L;
  }
  // What the bots do at a point, in a sentence.
  function pointDoes(leg, p) {
    if (p.kind === "start") return "The bots arrive here from the leg before.";
    if (p.kind === "via") return "The bots walk to this point on the navmesh, then on to the next one." + (p.locked ? " It is locked: " + p.why + "; edit it in the plan file." : "");
    if (leg.exitIsTo) return "The party walks on to the next leg from here.";
    const next = leg.packs && leg.packs.length ? "the lead looks at the first pack (" + packWords(leg.packs[0]).replace(/^Pack: /, "") + ") and pulls it" : "the party goes for the boss";
    return "The party gathers here, then " + next + ".";
  }
  // The route document's stop and walk at a spot of a leg (the doc is GET /api/plan/<name>/route, with the edits made so far while editing).
  function docAt(doc, legIndex, x, y, tol) {
    const t = tol == null ? 0.6 : tol, out = { stop: null, seg: null };
    if (!doc || !Array.isArray(doc.stops)) return out;
    out.stop = doc.stops.find((s) => s.leg === legIndex && (s.kind === "via" || s.kind === "stage") && Math.hypot(s.x - x, s.y - y) <= t) || null;
    if (out.stop && Array.isArray(doc.segments)) out.seg = doc.segments.find((g) => g.stop === out.stop.seq) || null;
    return out;
  }
  function redLines(info) {
    const out = [];
    if (info.seg && !info.seg.ok) out.push("RED: the planner finds no path to this point (" + (info.seg.reason || "unreachable") + ")" + (info.seg.detail ? ": " + info.seg.detail : "") + ".");
    if (info.stop) for (const t of info.stop.issues || []) out.push("PLAN CHECK FAILS: " + t);
    if (info.seg && info.seg.ok && info.seg.exempt) out.push("Not walked on the navmesh: the plan check skips this hop" + (info.seg.detail ? " (" + info.seg.detail + ")" : "") + ".");
    if (info.stop) for (const t of info.stop.warnings || []) out.push("warning: " + t);
    return out;
  }
  // The hover text of a point.
  function pointTip(leg, p, doc) {
    const info = docAt(doc, leg.index, p.x, p.y), lines = [pointWords(leg, p), pointDoes(leg, p)];
    if (info.seg && info.seg.ok && !info.seg.exempt && info.seg.len) lines.push("The walk to it is " + Math.round(info.seg.len) + " yd.");
    lines.push(...redLines(info));
    if (p.kind !== "start" && !p.locked) lines.push("Drag to move" + (p.kind === "via" ? ", Delete to remove." : "."));
    return lines.join("\n");
  }
  // The hover text of the stretch from point k to point k+1 of a leg's route points.
  function segmentTip(leg, pts, k, doc) {
    const a = pts[k], b = pts[k + 1], info = docAt(doc, leg.index, b.x, b.y);
    const lines = ["From: " + pointWords(leg, a), "To: " + pointWords(leg, b)];
    if (info.seg && info.seg.ok && !info.seg.exempt) lines.push("The bots walk this stretch on the navmesh (" + Math.round(info.seg.len) + " yd).");
    else if (!info.seg) lines.push("The bots walk this stretch.");
    lines.push(...redLines(info));
    lines.push("Click to add a walk point here.");
    return lines.join("\n");
  }
  function packTip(leg, pack) { return packWords(pack) + "\nThe lead looks at this pack from a computed spot, then the bots pull it. It is not moved on the map; edit it in the plan file."; }

  // The segment of a leg's route nearest to a drawing point, within radiusPx pixels (scale: pixels per yard): {leg, index, k, x, y} with (x, y) the
  // nearest point on it and index where a new via point goes (as midHandles), or null.
  function segmentAt(leg, legIndex, x, y, scale, radiusPx) {
    const pts = routePoints(leg), first = pts.length && pts[0].kind === "start" ? 0 : -1;
    let best = null, bestD = Infinity;
    for (let k = 0; k + 1 < pts.length; k++) {
      const a = pts[k], b = pts[k + 1], dx = b.x - a.x, dy = b.y - a.y, len2 = dx * dx + dy * dy;
      if (len2 < 1e-9) continue;
      const t = Math.max(0, Math.min(1, ((x - a.x) * dx + (y - a.y) * dy) / len2)), px = a.x + t * dx, py = a.y + t * dy;
      const d = Math.hypot(px - x, py - y) * scale;
      if (d <= radiusPx && d < bestD) { bestD = d; best = { leg: legIndex, index: first === 0 ? k : k + 1, k, x: px, y: py }; }
    }
    return best;
  }

  // The help box: what the mouse and keys do, and what the colours mean.
  const HELP = [
    "Drag a point to move it.",
    "Click a line to add a point there (or drag from the line to put it where you drop it).",
    "Click a point, then press Delete to remove it.",
    "Ctrl+Z undoes, Ctrl+Y redoes, Esc cancels a drag.",
    "Hover a point or a line to see what the bots do there.",
  ];
  const KEY = [
    ["yellow", "A walk point: the bots walk through it."],
    ["orange", "Where the party gathers before the leg's packs or boss."],
    ["grey", "Locked (a direct, gm_cross or skipped hop): edit it in the plan file."],
    ["red dashed", "The planner finds no path: hover for the reason."],
  ];

  // ---- the unsaved draft ----
  // The edits not yet saved are kept in the tab's session storage so a reload does not lose them. The storage is passed in (and every use is in a
  // try/catch: a private window or blocked storage must not break the editor), and a draft is only taken back for the plan file it was made on.
  const DRAFT_PREFIX = "routeedit.draft:", MAX_DRAFT_OPS = 500;
  const draftKey = (plan) => DRAFT_PREFIX + String(plan);
  const isInt = (v) => Number.isInteger(v) && v >= 0 && v < 1e6;
  function validOp(op) {
    if (!op || typeof op !== "object" || !isInt(op.leg) || !isInt(op.index)) return false;
    if (op.kind === "delete") return true;
    return (op.kind === "move" || op.kind === "insert") && Number.isFinite(op.x) && Number.isFinite(op.y);
  }
  function draftSave(storage, plan, base, ops, note) {
    try {
      if (!ops || !ops.length) { storage.removeItem(draftKey(plan)); return true; }
      storage.setItem(draftKey(plan), JSON.stringify({ v: 1, base: base || "", ops, note: String(note || "").slice(0, 500), at: Date.now() }));
      return true;
    } catch (e) { return false; }
  }
  function draftLoad(storage, plan, base) {
    try {
      const raw = storage.getItem(draftKey(plan));
      if (!raw) return null;
      const d = JSON.parse(raw);
      if (!d || d.v !== 1 || !base || d.base !== base || !Array.isArray(d.ops) || !d.ops.length || d.ops.length > MAX_DRAFT_OPS || !d.ops.every(validOp)) return null;
      return { ops: d.ops, note: typeof d.note === "string" ? d.note.slice(0, 500) : "", at: Number(d.at) || 0 };
    } catch (e) { return null; }
  }
  function draftClear(storage, plan) { try { storage.removeItem(draftKey(plan)); } catch (e) { /* ignore */ } }

  const api = { legName, packWords, pointWords, pointDoes, docAt, pointTip, segmentTip, packTip, segmentAt, HELP, KEY, draftKey, draftSave, draftLoad, draftClear, toGame, toDrawing, buildModel, checkOp, apply, routePoints, handles, midHandles, hitTest, moveOp, insertOp, deleteOp, history, push, undo, redo, compass, describeChanges, issueMarks, snapQuery };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.RouteEdit = api;
})(typeof self !== "undefined" ? self : this);
