"use strict";
// The map view math, kept free of the page so a test can run it under node. A view is {cx, cy, scale}: the drawing point at
// the middle of the panel and the pixels per yard. The drawing's plane is the one Project makes (north up, one unit a yard).
(function (root) {
  const MIN_REL = 0.5, MAX_REL = 60; // zoom limits as a multiple of the fit scale

  // A rectangle is {x, y, w, h}.
  function union(rects) {
    let o = null;
    for (const r of rects) {
      if (!r || !isFinite(r.x) || !isFinite(r.y) || !(r.w >= 0) || !(r.h >= 0)) continue;
      if (!o) { o = { x: r.x, y: r.y, w: r.w, h: r.h }; continue; }
      const x2 = Math.max(o.x + o.w, r.x + r.w), y2 = Math.max(o.y + o.h, r.y + r.h);
      o.x = Math.min(o.x, r.x); o.y = Math.min(o.y, r.y); o.w = x2 - o.x; o.h = y2 - o.y;
    }
    return o;
  }

  // The view that shows the whole rectangle in a panel of pw x ph pixels with padPx around it, keeping the aspect ratio.
  function fit(rect, pw, ph, padPx) {
    const pad = padPx == null ? 16 : padPx;
    const aw = Math.max(1, pw - 2 * pad), ah = Math.max(1, ph - 2 * pad);
    const scale = Math.min(aw / Math.max(rect.w, 1e-6), ah / Math.max(rect.h, 1e-6));
    return { cx: rect.x + rect.w / 2, cy: rect.y + rect.h / 2, scale };
  }

  // The viewBox [x, y, w, h] that makes the panel show view. It has the panel's aspect, so nothing is stretched.
  function viewBox(v, pw, ph) {
    const w = pw / v.scale, h = ph / v.scale;
    return [v.cx - w / 2, v.cy - h / 2, w, h];
  }

  // Panel pixel (px, py) to the drawing point under it, and back.
  function toWorld(v, pw, ph, px, py) { return { x: v.cx + (px - pw / 2) / v.scale, y: v.cy + (py - ph / 2) / v.scale }; }
  function toPanel(v, pw, ph, x, y) { return { x: (x - v.cx) * v.scale + pw / 2, y: (y - v.cy) * v.scale + ph / 2 }; }

  function clampScale(scale, fitScale) { return Math.min(Math.max(scale, fitScale * MIN_REL), fitScale * MAX_REL); }

  // Zoom by factor keeping the drawing point under panel pixel (px, py) where it is.
  function zoomAt(v, factor, pw, ph, px, py, fitScale) {
    const scale = clampScale(v.scale * factor, fitScale);
    const p = toWorld(v, pw, ph, px, py);
    return { cx: p.x - (px - pw / 2) / scale, cy: p.y - (py - ph / 2) / scale, scale };
  }

  // Drag the picture by (dx, dy) panel pixels.
  function panBy(v, dx, dy) { return { cx: v.cx - dx / v.scale, cy: v.cy - dy / v.scale, scale: v.scale }; }

  // Pinch: the two touches moved from (a0, b0) to (a1, b1), each {x, y} in panel pixels. The point between the fingers stays
  // between them and the distance between the fingers sets the zoom.
  function pinch(v, a0, b0, a1, b1, pw, ph, fitScale) {
    const d0 = Math.hypot(a0.x - b0.x, a0.y - b0.y), d1 = Math.hypot(a1.x - b1.x, a1.y - b1.y);
    const m0 = { x: (a0.x + b0.x) / 2, y: (a0.y + b0.y) / 2 }, m1 = { x: (a1.x + b1.x) / 2, y: (a1.y + b1.y) / 2 };
    const z = d0 > 1 ? zoomAt(v, d1 / d0, pw, ph, m0.x, m0.y, fitScale) : v;
    return panBy(z, m1.x - m0.x, m1.y - m0.y);
  }

  // The zoom factor of a pinch alone (the fingers' distance now over before), for a map that follows the party: the party stays in the
  // middle, so the fingers' own movement is not applied as a pan. 1 when the fingers started on one point.
  function pinchFactor(a0, b0, a1, b1) {
    const d0 = Math.hypot(a0.x - b0.x, a0.y - b0.y), d1 = Math.hypot(a1.x - b1.x, a1.y - b1.y);
    return d0 > 1 ? d1 / d0 : 1;
  }

  // The middle of a set of points [{x, y}] and the larger side of their box.
  function centroid(pts) {
    if (!pts.length) return null;
    let sx = 0, sy = 0, x0 = Infinity, x1 = -Infinity, y0 = Infinity, y1 = -Infinity;
    for (const p of pts) { sx += p.x; sy += p.y; x0 = Math.min(x0, p.x); x1 = Math.max(x1, p.x); y0 = Math.min(y0, p.y); y1 = Math.max(y1, p.y); }
    return { x: sx / pts.length, y: sy / pts.length, extent: Math.max(x1 - x0, y1 - y0) };
  }

  // The scale that shows the party and margin yards around it in the smaller side of the panel.
  function followScale(extent, pw, ph, margin) { return Math.min(pw, ph) / Math.max(extent + 2 * (margin == null ? 60 : margin), 1); }

  // One easing step of the view toward goal; done when it has arrived (less than half a pixel and a tenth of a percent away).
  function ease(v, goal, t) {
    const k = t == null ? 0.25 : t;
    const n = { cx: v.cx + (goal.cx - v.cx) * k, cy: v.cy + (goal.cy - v.cy) * k, scale: v.scale * Math.pow(goal.scale / v.scale, k) };
    const done = Math.hypot(goal.cx - n.cx, goal.cy - n.cy) * n.scale < 0.5 && Math.abs(goal.scale / n.scale - 1) < 0.001;
    return done ? { view: { cx: goal.cx, cy: goal.cy, scale: goal.scale }, done: true } : { view: n, done: false };
  }

  // The trail as chunks that fade with age: up to maxSeg polylines (a chunk shares its end point with the next one), alpha from
  // minAlpha for the oldest to 1 for the newest. pts is [[x, y], ...] oldest first.
  function trailSegments(pts, maxSeg, minAlpha) {
    const out = [];
    if (!pts || pts.length < 2) return out;
    const lo = minAlpha == null ? 0.25 : minAlpha, n = pts.length - 1;
    const per = Math.ceil(n / Math.max(1, maxSeg || 24)), count = Math.ceil(n / per);
    for (let i = 0, k = 0; i < n; i += per, k++) {
      out.push({ points: pts.slice(i, Math.min(pts.length, i + per + 1)), alpha: count === 1 ? 1 : lo + (1 - lo) * (k / (count - 1)) });
    }
    return out;
  }

  // The next stop of a leg's planned route. stops are the leg's {x, y, ...} in plan order (the first is the leg's start), trail the
  // walked path [[x, y], ...] and lead the party's {x, y}. A stop is reached when the trail or the lead came within radius of it; the
  // next one is the stop after the last one reached. Returns {index, stop, dist} (dist in yards), or null at the end of the leg.
  function nextTarget(stops, trail, lead, radius) {
    if (!stops || stops.length < 2 || !lead) return null;
    const r = radius == null ? 14 : radius;
    // the trail is simplified (a straight walk is two points), so the distance is to its segments
    const segDist = (st, a, b) => {
      const dx = b[0] - a[0], dy = b[1] - a[1], l2 = dx * dx + dy * dy;
      const t = l2 === 0 ? 0 : Math.max(0, Math.min(1, ((st.x - a[0]) * dx + (st.y - a[1]) * dy) / l2));
      return Math.hypot(st.x - (a[0] + t * dx), st.y - (a[1] + t * dy));
    };
    const tr = trail || [];
    const near = (st) => Math.hypot(st.x - lead.x, st.y - lead.y) <= r || tr.some((p, i) => (i === 0 ? Math.hypot(st.x - p[0], st.y - p[1]) : segDist(st, tr[i - 1], p)) <= r);
    let reached = 0;
    for (let i = 1; i < stops.length; i++) if (near(stops[i])) reached = i;
    if (reached + 1 >= stops.length) return null;
    const st = stops[reached + 1];
    return { index: reached + 1, stop: st, dist: Math.hypot(st.x - lead.x, st.y - lead.y) };
  }

  const api = { trailSegments, nextTarget, union, fit, viewBox, toWorld, toPanel, clampScale, zoomAt, panBy, pinch, pinchFactor, centroid, followScale, ease, MIN_REL, MAX_REL };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.MapView = api;
})(typeof self !== "undefined" ? self : this);
