"use strict";
// Run by view_test.go under node: the map view math.
const assert = require("assert");
const V = require(process.argv[2]);
const close = (a, b, eps = 1e-6) => assert.ok(Math.abs(a - b) <= eps, a + " != " + b);

// bounds union: the plan and the picture, and rectangles that are not usable are skipped
let u = V.union([{ x: 0, y: 0, w: 10, h: 10 }, { x: -5, y: 2, w: 8, h: 30 }, null, { x: NaN, y: 0, w: 1, h: 1 }]);
assert.deepStrictEqual(u, { x: -5, y: 0, w: 15, h: 32 });
assert.strictEqual(V.union([null]), null);
assert.deepStrictEqual(V.union([{ x: 1, y: 2, w: 3, h: 4 }]), { x: 1, y: 2, w: 3, h: 4 });

// fit keeps the aspect ratio: a wide box in a wide panel, the same box in a tall panel
const wide = { x: 100, y: 200, w: 400, h: 100 };
let f = V.fit(wide, 1000, 500, 0);
close(f.scale, 2.5); close(f.cx, 300); close(f.cy, 250);
f = V.fit(wide, 500, 1000, 0);
close(f.scale, 1.25);
f = V.fit(wide, 1000, 500, 50); // the padding is pixels on every side
close(f.scale, 900 / 400);
// the whole box is inside the viewBox, the viewBox has the panel's aspect, and nothing is stretched
for (const [pw, ph] of [[1000, 500], [500, 1000], [320, 568], [1920, 700]]) {
  const v = V.fit(wide, pw, ph, 16), vb = V.viewBox(v, pw, ph);
  close(vb[2] / vb[3], pw / ph, 1e-9);
  assert.ok(vb[0] <= wide.x && vb[1] <= wide.y && vb[0] + vb[2] >= wide.x + wide.w && vb[1] + vb[3] >= wide.y + wide.h, "box outside the view " + pw + "x" + ph);
}

// the transform: panel pixel <-> drawing point round trip, and the panel centre is the view centre
const v0 = { cx: 50, cy: -20, scale: 3 };
const c = V.toWorld(v0, 800, 600, 400, 300);
close(c.x, 50); close(c.y, -20);
const w = V.toWorld(v0, 800, 600, 123, 456), p = V.toPanel(v0, 800, 600, w.x, w.y);
close(p.x, 123); close(p.y, 456);

// zoom around the cursor keeps the point under the cursor, and is limited around the fit scale
const fs = 2;
let z = V.zoomAt({ cx: 0, cy: 0, scale: 4 }, 1.5, 800, 600, 700, 100, fs);
const before = V.toWorld({ cx: 0, cy: 0, scale: 4 }, 800, 600, 700, 100), after = V.toWorld(z, 800, 600, 700, 100);
close(before.x, after.x); close(before.y, after.y); close(z.scale, 6);
z = V.zoomAt({ cx: 0, cy: 0, scale: 4 }, 1e9, 800, 600, 0, 0, fs);
close(z.scale, fs * V.MAX_REL);
z = V.zoomAt({ cx: 0, cy: 0, scale: 4 }, 1e-9, 800, 600, 0, 0, fs);
close(z.scale, fs * V.MIN_REL);

// dragging right by 30 px moves the picture right: the view centre goes left by 30 px of yards
const d = V.panBy({ cx: 10, cy: 10, scale: 3 }, 30, -15);
close(d.cx, 0); close(d.cy, 15); close(d.scale, 3);

// pinch: spreading the fingers zooms in around their middle, moving them together pans
const a0 = { x: 300, y: 300 }, b0 = { x: 500, y: 300 };
let pz = V.pinch({ cx: 0, cy: 0, scale: 4 }, a0, b0, { x: 200, y: 300 }, { x: 600, y: 300 }, 800, 600, fs);
close(pz.scale, 8);
close(V.toWorld(pz, 800, 600, 400, 300).x, V.toWorld({ cx: 0, cy: 0, scale: 4 }, 800, 600, 400, 300).x);
pz = V.pinch({ cx: 0, cy: 0, scale: 4 }, a0, b0, { x: 340, y: 330 }, { x: 540, y: 330 }, 800, 600, fs);
close(pz.scale, 4); close(pz.cx, -10); close(pz.cy, -7.5);

// follow: the middle of the party and the zoom that shows the party plus 60 yards around it
const ce = V.centroid([{ x: 0, y: 0 }, { x: 40, y: 10 }, { x: 20, y: 20 }]);
close(ce.x, 20); close(ce.y, 10); close(ce.extent, 40);
assert.strictEqual(V.centroid([]), null);
close(V.followScale(40, 1000, 600, 60), 600 / 160);
close(V.followScale(0, 1000, 600), 600 / 120);

// easing reaches the goal and stops
let cur = { cx: 0, cy: 0, scale: 1 }, goal = { cx: 100, cy: 50, scale: 4 }, n = 0, r;
do { r = V.ease(cur, goal); cur = r.view; n++; } while (!r.done && n < 200);
assert.ok(r.done && n < 60, "ease took " + n);
assert.deepStrictEqual(cur, goal);
// the trail fades with age: the oldest chunk is the faintest, the newest is solid, chunks join end to end, and a long trail is capped
const line = Array.from({ length: 101 }, (_, i) => [i, 0]);
let segs = V.trailSegments(line, 10);
assert.strictEqual(segs.length, 10);
close(segs[0].alpha, 0.25); close(segs[9].alpha, 1);
for (let i = 1; i < segs.length; i++) { assert.ok(segs[i].alpha > segs[i - 1].alpha); assert.deepStrictEqual(segs[i].points[0], segs[i - 1].points[segs[i - 1].points.length - 1]); }
assert.strictEqual(segs.reduce((n, g) => n + g.points.length - 1, 0), 100);
assert.strictEqual(V.trailSegments(line, 24).length <= 24, true);
assert.strictEqual(V.trailSegments([[0, 0]], 5).length, 0);
assert.strictEqual(V.trailSegments([[0, 0], [5, 5]], 5)[0].alpha, 1);

// the next target: the stop after the last one the party came near
const stops = [{ x: 0, y: 0, label: "start" }, { x: 100, y: 0, label: "a" }, { x: 200, y: 0, label: "b" }, { x: 300, y: 0, label: "c" }];
let nt = V.nextTarget(stops, [[0, 0], [60, 0]], { x: 60, y: 0 });
assert.strictEqual(nt.index, 1); close(nt.dist, 40);
nt = V.nextTarget(stops, [[0, 0], [100, 0], [190, 0]], { x: 190, y: 0 }); // near b: the next is c
assert.strictEqual(nt.stop.label, "c"); close(nt.dist, 110);
nt = V.nextTarget(stops, [[0, 0], [250, 0]], { x: 250, y: 0 }); // skipped a, passed b: c
assert.strictEqual(nt.stop.label, "c");
assert.strictEqual(V.nextTarget(stops, [[300, 0]], { x: 300, y: 0 }), null); // at the leg's end
assert.strictEqual(V.nextTarget(stops, [], null), null);
assert.strictEqual(V.nextTarget([stops[0]], [], { x: 0, y: 0 }), null);
console.log("ok");
