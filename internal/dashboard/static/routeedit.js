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
      const m = { index: l.index, label: l.label || "", name: l.name || "", start: null, via: [], to: null, exitIsTo: !l.boss_at && !(l.packs && l.packs.length) };
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

  const api = { toGame, toDrawing, buildModel, checkOp, apply, routePoints, handles, midHandles, hitTest, moveOp, insertOp, deleteOp, history, push, undo, redo, compass, describeChanges, issueMarks, snapQuery };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.RouteEdit = api;
})(typeof self !== "undefined" ? self : this);
