"use strict";
// Run by view_test.go under node: what a character is doing, the hover box, status icons, threat colours, the event feed and the run progress.
const assert = require("assert");
const U = require(process.argv[2]);
const names = { "7001": "Stone Steward", "6910": "Revelosh" };

// ---- what a bot is doing
const bot = (o) => Object.assign({ name: "Puvur", alive: true, hp_pct: 80, power_pct: 50, role: "dps", state: "n" }, o);
assert.strictEqual(U.activity(bot({ alive: false })).word, "dead");
assert.deepStrictEqual(U.activity(bot({ alive: false })).kinds, ["dead"]);
assert.strictEqual(U.activity(bot({ state: "c", target: "Stone Steward" })).word, "fighting Stone Steward");
assert.strictEqual(U.activity(bot({ state: "c" })).word, "fighting");
assert.strictEqual(U.activity(bot({ cast: "Fireball", target: "Stone Steward", state: "c" })).word, "casting Fireball on Stone Steward");
assert.deepStrictEqual(U.activity(bot({ cast: "Fireball" })).kinds, ["cast"]);
assert.strictEqual(U.activity(bot({ action: "drink", result: "O" })).word, "drinking");
assert.deepStrictEqual(U.activity(bot({ action: "drink", result: "O" })).kinds, ["rest"]);
assert.strictEqual(U.activity(bot({ action: "drink", result: "F", reason: "no-item" })).word, "out of drink");
assert.deepStrictEqual(U.activity(bot({ action: "drink", result: "F", reason: "no-item" })).kinds, []);
assert.strictEqual(U.activity(bot({ action: "drink", state: "c", result: "O", target: "X" })).word, "fighting X", "no drinking in a fight");
assert.strictEqual(U.activity(bot({ action: "flee" })).word, "fleeing");
assert.deepStrictEqual(U.activity(bot({ action: "flee" })).kinds, ["flee"]);
assert.strictEqual(U.activity(bot({ action: "follow" })).word, "moving");
assert.strictEqual(U.activity(bot({ action: "stay" })).word, "holding position");
assert.strictEqual(U.activity(bot({})).word, "idle");
assert.strictEqual(U.activity(bot({ state: undefined })).word, "idle");
assert.ok(U.activity(bot({ action: "drink", result: "O" })).raw.indexOf("drink") >= 0);

// ---- what a creature is doing
const mob = (o) => Object.assign({ guid: "0xF130001C4E00AC91", entry: 7001, kind: "hostile", hp_pct: 80, x: 0, y: 0, group: -1 }, o);
assert.strictEqual(U.mobActivity(mob({ target: "Forthur" })).word, "attacking Forthur");
assert.strictEqual(U.mobActivity(mob({ cc: true })).word, "crowd controlled");
assert.deepStrictEqual(U.mobActivity(mob({ cc: true, cast: "Frostbolt" })).kinds, ["cast", "cc"]);
assert.strictEqual(U.mobActivity(mob({ evade: true })).word, "walking back to its spot");
assert.strictEqual(U.mobActivity(mob({})).word, "idle");

// ---- the hover box
let d = U.describeUnit({ type: "mob", data: mob({ boss: true, cast: "Slam", target: "entry 7001", cc: false, level: 47 }) }, { names, lead: { x: 30, y: 40 } });
assert.strictEqual(d.title, "Stone Steward");
assert.strictEqual(d.badge, "boss");
const row = (dd, k) => (dd.rows.find((r) => r[0] === k) || [])[1];
assert.strictEqual(row(d, "Kind"), "Boss, hostile");
assert.strictEqual(row(d, "Level"), "47");
assert.strictEqual(row(d, "Health"), "80 %");
assert.strictEqual(row(d, "Distance to lead"), "50 yd");
assert.strictEqual(row(d, "Casting"), "Slam");
assert.strictEqual(row(d, "Target"), "Stone Steward", "an entry in a target is named from the plan");
assert.strictEqual(row(d, "Entry"), "7001");
d = U.describeUnit({ type: "mob", data: mob({ cc: true, entry: 999 }) }, { names, lead: null });
assert.strictEqual(d.title, "creature 999");
assert.strictEqual(row(d, "Level"), undefined, "no level, no row");
assert.strictEqual(row(d, "Distance to lead"), undefined);
assert.ok(row(d, "Crowd control"));
d = U.describeUnit({ type: "bot", data: bot({ lead: true, class: "priest", role: "healer", power_type: "mana", cast: "Heal", target: "Forthur" }) }, { names, lead: { x: 0, y: 0 } });
assert.strictEqual(d.title, "★ Puvur");
assert.strictEqual(row(d, "Class"), "Priest · healer");
assert.strictEqual(row(d, "Mana"), "50 %");
assert.strictEqual(row(d, "Doing"), "casting Heal on Forthur");
assert.strictEqual(row(d, "Distance to lead"), undefined, "the lead is not far from itself");
d = U.describeUnit({ type: "other", group: "Group B", data: bot({ x: 3, y: 4 }) }, { names, lead: { x: 0, y: 0 } });
assert.strictEqual(row(d, "Group"), "Group B"); assert.strictEqual(row(d, "Distance to lead"), "5 yd");
d = U.describeUnit({ type: "commander", data: { name: "Zfmagea", x: 0, y: 6 } }, { lead: { x: 0, y: 0 } });
assert.strictEqual(d.badge, "commander"); assert.strictEqual(row(d, "Distance to lead"), "6 yd");
d = U.describeUnit({ type: "event", data: { kind: "death", text: "Puvur died", at: "2026-10-14T16:40:00Z" } }, {});
assert.strictEqual(d.title, "Puvur died");
// markup in a name stays text: the box is built with textContent, and the strings are passed through untouched
d = U.describeUnit({ type: "bot", data: bot({ name: "<img src=x onerror=alert(1)>" }) }, {});
assert.strictEqual(d.title, "<img src=x onerror=alert(1)>");

// ---- status icons
assert.deepStrictEqual(U.statuses({ type: "mob", data: mob({ cc: true, cast: "x", hp_pct: 0 }) }), ["cast", "cc", "dead"]);
assert.deepStrictEqual(U.statuses({ type: "bot", data: bot({ alive: false }) }), ["dead"]);
assert.deepStrictEqual(U.statuses({ type: "commander", data: {} }), []);
["dead", "cast", "cc", "rest", "flee"].forEach((k) => assert.ok(U.STATUS[k].glyph && U.STATUS[k].text, k));

// ---- threat colours: a creature's ring is whom it attacks
const bots = [bot({ name: "Forthur", role: "tank", guid: "0x00000000000002DC", target_guid: "0xF130001C4E00AC91" }), bot({ name: "Puvur", role: "healer", guid: "0x00000000000002DD" })];
assert.strictEqual(U.threatOf(mob({ target_guid: "0x00000000000002dc" }), bots), "tank", "guids compare without case or leading zeros");
assert.strictEqual(U.threatOf(mob({ target_guid: "0x2DD" }), bots), "other");
assert.strictEqual(U.threatOf(mob({ target_guid: "0xDEAD" }), bots), "");
assert.strictEqual(U.threatOf(mob({}), bots), "");
assert.ok(U.tankTargets(bots).has(U.guidKey("0xF130001C4E00AC91")));
assert.strictEqual(U.tankTargets([bot({ role: "tank", alive: false, target_guid: "0x1" })]).size, 0, "a dead tank targets nothing");
assert.strictEqual(U.hpColor(90), "#3ddc84"); assert.strictEqual(U.hpColor(50), "#ffb020"); assert.strictEqual(U.hpColor(10), "#ff4d4d");

// ---- the event feed
const ev = [
  { at: "2026-10-14T16:40:00Z", kind: "death", text: "Puvur died", pos: true, x: 1, y: 2 },
  { kind: "section", text: "Sentinel room done" },
  { at: "2026-10-14T16:45:30Z", kind: "boss", text: "Revelosh killed", pos: true, x: 5, y: 6 },
  { at: "2026-10-14T16:46:00Z", kind: "open", text: "Door or chest opened" },
];
const lines = U.feedLines(ev, 3, "utc");
assert.deepStrictEqual(lines.map((l) => l.text), ["Door or chest opened", "Revelosh killed", "Sentinel room done"], "newest first, at most n");
assert.strictEqual(lines[1].time, "16:45:30");
assert.strictEqual(lines[2].time, "", "an event with no time prints none");
assert.strictEqual(U.pinnedEvents(ev).length, 2);
assert.strictEqual(U.clockOf("0001-01-01T00:00:00Z"), "");
assert.strictEqual(U.clockOf("junk"), "");
assert.deepStrictEqual(U.feedLines(null, 5), []);
["death", "wipe", "boss", "section", "open", "loot", "use", "zzz"].forEach((k) => assert.ok(U.eventGlyph(k)));

// ---- the run: timer, section, bosses
const geo = { legs: [{ index: 0, label: "Entry", boss: 0 }, { index: 1, label: "Sentinel room", boss: 7001 }, { index: 2, label: "Vault", boss: 6910 }], names };
const card = (run) => ({ run: Object.assign({ state: "running", leg: 1, leg_count: 3, minutes: 18.7, group: 2, groups: 5 }, run) });
let p = U.progress(card(), geo, []);
assert.strictEqual(p.elapsed, "18:42");
assert.deepStrictEqual(p.section, { n: 2, total: 3, label: "Sentinel room" });
assert.deepStrictEqual(p.pack, { n: 2, total: 5 });
assert.strictEqual(p.done, 0); assert.strictEqual(p.total, 2); assert.strictEqual(p.next, "Stone Steward");
assert.strictEqual(U.progressLine(p), "18:42 · section 2 of 3, Sentinel room · pack 2 of 5 · bosses 0 of 2 · next: Stone Steward");
p = U.progress(card({ leg: 2 }), geo, []);
assert.strictEqual(p.done, 1, "a boss in an earlier section is done"); assert.strictEqual(p.next, "Revelosh");
p = U.progress(card({ leg: 1 }), geo, [{ kind: "boss", text: "Stone Steward killed" }]);
assert.strictEqual(p.done, 1); assert.strictEqual(p.next, "Revelosh");
p = U.progress(card({ state: "done", leg: 2, group: 0 }), geo, []);
assert.strictEqual(p.done, 2); assert.ok(/all down/.test(U.progressLine(p)));
assert.strictEqual(U.progress({ run: { state: "idle", leg: -1, minutes: 0 } }, geo, []), null);
assert.strictEqual(U.progress({}, geo, []), null);
assert.strictEqual(U.progress(card(), null, []).total, 0, "without a plan drawing there is no boss list");
assert.strictEqual(U.clock(3725), "1:02:05");

// ---- the legend explains every mark
assert.ok(U.LEGEND.length >= 10);
U.LEGEND.forEach((l) => assert.ok(l.sample && l.text, l.sample));
// ---- a mark of the path layers: its rows are shown, an empty one is left out
const mk = U.describeUnit({ type: "mark", data: { title: "Party stood still", badge: "stall", rows: [["For", "60 s"], ["Why", ""], ["At", "1:40 into the run"]] } }, {});
assert.strictEqual(mk.title, "Party stood still"); assert.strictEqual(mk.badge, "stall");
assert.deepStrictEqual(mk.rows, [["For", "60 s"], ["At", "1:40 into the run"]]);
assert.deepStrictEqual(U.describeUnit({ type: "mark", data: {} }, {}).rows, []);
assert.ok(U.LEGEND.some((l) => /refused|stuck/.test(l.text)) && U.LEGEND.some((l) => /ghost/.test(l.text)) && U.LEGEND.some((l) => /patrol/.test(l.text)));
assert.ok(U.LEGEND.some((l) => /does not say who put a crowd control on/.test(l.text)), "the legend says what the feed lacks");
console.log("unitinfo.js ok");
