"use strict";
// The real route on the plan view: the path the bots walk on the navmesh (GET /api/plan/<name>/route, or the editor's route with the ops made so
// far), the stops numbered in run order, and the segments the planner could not path drawn dashed red. Kept free of the page so a test can run it
// under node: the drawing is built with the `s` function the page gives (an svg element maker).
(function (root) {
  const NAME_OK = /^[a-z0-9][a-z0-9_-]{0,39}$/;

  // The address of the route of a plan. With ops (the editor's edits so far) it is the editor's route, which traces the plan with the ops applied;
  // without, the plan file's own route. agent names the slot whose copy of the plan is drawn ("" for the dashboard's).
  function routeUrl(plan, opts) {
    const o = opts || {};
    if (!NAME_OK.test(plan || "")) return "";
    if (o.ops && o.ops.length) return "/api/edit/" + encodeURIComponent(plan) + "/route?" + new URLSearchParams({ ops: JSON.stringify(o.ops) }).toString();
    return "/api/plan/" + encodeURIComponent(plan) + "/route" + (o.agent ? "?" + new URLSearchParams({ agent: String(o.agent) }).toString() : "");
  }

  // What a fetched answer is worth: the doc when it is a route, else null.
  function parseDoc(json) {
    if (!json || typeof json !== "object" || !Array.isArray(json.stops) || !Array.isArray(json.segments)) return null;
    return json;
  }

  // The stops and segments of the legs shown (shown: array of leg indexes).
  function visible(doc, shown) {
    if (!doc) return { stops: [], segs: [] };
    const on = new Set(shown);
    return { stops: doc.stops.filter((s) => on.has(s.leg)), segs: doc.segments.filter((g) => on.has(g.leg)) };
  }

  // The points of a segment's line: the planner's path, else the chord from its start to its end (a segment the planner could not path, a
  // direct step). A segment that goes nowhere has none.
  function linePoints(seg) {
    let pts = seg.pts && seg.pts.length >= 2 ? seg.pts : [[seg.a.x, seg.a.y], [seg.b.x, seg.b.y]];
    if (pts.length === 2 && Math.hypot(pts[0][0] - pts[1][0], pts[0][1] - pts[1][1]) < 0.05) return [];
    return pts;
  }
  const pointsAttr = (pts) => pts.map((p) => p[0] + "," + p[1]).join(" ");

  // The classes of a segment's line: its kind decides the style, a failure is red whatever the kind.
  function segClass(seg) {
    const c = ["rseg", "k-" + seg.kind];
    if (!seg.ok) c.push("bad");
    else if (seg.exempt) c.push("exempt");
    if (seg.link) c.push("link");
    return c.join(" ");
  }

  // The classes of a stop's number.
  function stopClass(st) {
    return "mk rnum k-" + st.kind + (st.failed ? " bad" : "") + (st.warnings && st.warnings.length ? " warn" : "");
  }

  // The hover text of a stop: its number, what happens there, and what the plan check said of it.
  function stopTip(st) {
    const lines = [st.seq + ". " + st.label];
    for (const t of st.issues || []) lines.push("PLAN CHECK FAILS: " + t);
    for (const t of st.warnings || []) lines.push("warning: " + t);
    for (const t of st.notes || []) lines.push(t);
    return lines.join("\n");
  }

  // Stops that stand on (nearly) the same spot, such as a leg's last via point and its stage point, would print their numbers on top of each other.
  // The offset of each number in pixels (to the right, in the order of the run), 0 for a stop that has the spot to itself.
  function fan(stops, cellYd, stepPx) {
    const cell = cellYd == null ? 2 : cellYd, step = stepPx == null ? 15 : stepPx;
    const groups = new Map();
    stops.forEach((st, i) => {
      const k = Math.round(st.x / cell) + "," + Math.round(st.y / cell);
      if (!groups.has(k)) groups.set(k, []);
      groups.get(k).push(i);
    });
    const out = stops.map(() => 0);
    for (const idx of groups.values()) if (idx.length > 1) idx.forEach((i, j) => { out[i] = Math.round((j - (idx.length - 1) / 2) * step * 10) / 10; });
    return out;
  }

  // One line for the legend: what the route holds.
  function summaryText(doc) {
    if (!doc || !doc.summary) return "";
    const s = doc.summary;
    let t = s.stops + " stops, " + s.segments + " walks, " + Math.round(s.length) + " yd";
    if (s.failed) t += ", " + s.failed + " cannot be pathed";
    return t + (doc.edited ? " (with your edits, not saved)" : "");
  }

  // The drawing of the route: {lines: [svg element], marks: [svg element]}. The lines go under the units and the marks over them. Lines are drawn
  // in the plane, marks are `mk` groups the page scales to a pixel size; each carries its text in a <title>.
  function build(s, doc, shown) {
    const v = visible(doc, shown), lines = [], marks = [];
    for (const seg of v.segs) {
      const pts = linePoints(seg);
      if (!pts.length) continue;
      const cls = segClass(seg), pa = pointsAttr(pts);
      const g = s("g", { class: cls, "data-leg": seg.leg, "data-stop": seg.stop });
      g.append(s("polyline", { class: "rhalo", points: pa }), s("polyline", { class: "rline", points: pa }));
      const hit = s("polyline", { class: "rhit", points: pa });
      hit.append(s("title", {}, seg.text || ""));
      g.append(hit);
      lines.push(g);
    }
    const dx = fan(v.stops);
    v.stops.forEach((st, i) => {
      const m = s("g", { class: stopClass(st), "data-x": st.x, "data-y": st.y, "data-leg": st.leg, "data-stop": st.seq });
      const inner = s("g", { transform: "translate(" + dx[i] + " 0)" });
      const label = String(st.seq), w = Math.max(9, 4 + label.length * 4.2);
      inner.append(s("circle", { class: "rc", r: w }), s("text", { class: "rt", y: 3.5 }, label), s("title", {}, stopTip(st)));
      m.append(inner);
      marks.push(m);
    });
    return { lines, marks };
  }

  const api = { routeUrl, parseDoc, visible, linePoints, pointsAttr, segClass, stopClass, stopTip, fan, summaryText, build };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.RealRoute = api;
})(typeof self !== "undefined" ? self : this);
