"use strict";
// What the map says about one unit and about the run, free of the page so a test can run it under node: the words for what a
// character is doing, the hover box of a unit, status icons, threat colours, the event feed and the run progress. Pure functions of the
// card (the JSON the server builds); nothing here makes markup, the page puts every string in with textContent.
(function (root) {
  const cap = (s) => (s ? s[0].toUpperCase() + s.slice(1) : "");
  const pretty = (s) => cap(String(s || "").replace(/[_\-]+/g, " ").trim());
  const guidKey = (g) => (typeof g === "string" ? g.trim().toLowerCase().replace(/^0x/, "").replace(/^0+/, "") : "");

  // The feed knows a creature by its entry id; the plan's notes name some of them.
  function mobName(names, entry) { return (names && names[String(entry)]) || "creature " + entry; }
  const entryText = (names, s) => String(s || "").replace(/\bentry (\d+)/g, (_, e) => mobName(names, e));

  // ---- what a character is doing ----

  // Words for the last action a bot's AI ran, tested in this order. kind is what the status icon and the grouping use.
  const ACTIONS = [
    { re: /drink/, word: "drinking", kind: "rest" },
    { re: /\b(food|eat)/, word: "eating", kind: "rest" },
    { re: /bandage/, word: "bandaging", kind: "rest" },
    { re: /flee|retreat|run away|escape/, word: "fleeing", kind: "flee" },
    { re: /\brest|sit/, word: "resting", kind: "rest" },
    { re: /revive|release|corpse|spirit/, word: "recovering from death", kind: "" },
    { re: /loot/, word: "looting", kind: "" },
    { re: /follow|move|travel|reach|go to|walk|mount/, word: "moving", kind: "" },
    { re: /stay|guard|hold/, word: "holding position", kind: "" },
  ];

  function actionOf(b) {
    const a = String((b && b.action) || "").toLowerCase();
    if (!a) return null;
    return ACTIONS.find((x) => x.re.test(a)) || null;
  }

  // What a bot is doing, in words, and the icons that go with it: {word, kinds: ["dead" | "cast" | "rest" | "flee"], raw}.
  // A drink or food that failed is not "drinking" (reason no-item: the bot has none left). The word names the target when it fights.
  function activity(b) {
    const raw = [b.state && "state " + b.state, b.action, b.result, b.reason].filter(Boolean).join(" ");
    const kinds = [];
    if (b.alive === false) return { word: "dead", kinds: ["dead"], raw };
    const target = b.target ? " " + b.target : "";
    const inCombat = b.state === "c" || b.state === "r";
    if (b.cast) kinds.push("cast");
    const act = actionOf(b);
    const failed = b.result === "F" || b.result === "I";
    if (b.cast) return { word: "casting " + b.cast + (b.target ? " on " + b.target : ""), kinds, raw };
    if (act && /drink|food|eat/.test(b.action.toLowerCase()) && failed) {
      return { word: b.reason === "no-item" ? "out of " + (/drink/i.test(b.action) ? "drink" : "food") : "idle", kinds, raw };
    }
    if (act && act.kind === "flee") { kinds.push("flee"); return { word: "fleeing", kinds, raw }; }
    if (act && act.kind === "rest" && !inCombat && !failed) { kinds.push("rest"); return { word: act.word, kinds, raw }; }
    if (inCombat) return { word: "fighting" + target, kinds, raw };
    if (act && !failed) return { word: act.word, kinds, raw };
    if (b.state === "n" || !b.state) return { word: "idle", kinds, raw };
    return { word: "idle", kinds, raw };
  }

  // The same for a creature.
  function mobActivity(m) {
    const kinds = [];
    if (m.cast) kinds.push("cast");
    if (m.cc) kinds.push("cc");
    const raw = [m.cast && "casting " + m.cast, m.cc && "crowd controlled", m.evade && "evading", m.stealth && "stealthed"].filter(Boolean).join(", ");
    if (m.cast) return { word: "casting " + m.cast + (m.target ? " on " + m.target : ""), kinds, raw };
    if (m.cc) return { word: "crowd controlled", kinds, raw };
    if (m.evade) return { word: "walking back to its spot", kinds, raw };
    if (m.target) return { word: "attacking " + m.target, kinds, raw };
    return { word: "idle", kinds, raw };
  }

  // ---- the hover box ----

  const REACTION = { hostile: "Hostile", neutral: "Neutral", friendly: "Friendly", critter: "Critter" };

  function dist(a, b) { return a && b && isFinite(a.x) && isFinite(a.y) && isFinite(b.x) && isFinite(b.y) ? Math.hypot(a.x - b.x, a.y - b.y) : null; }
  const yd = (d) => (d == null ? "" : Math.round(d) + " yd");

  // The box for one unit. u = {type: "bot" | "mob" | "commander" | "other" | "event", data: the card's object, group?: name of another group}.
  // ctx = {names, lead: {x, y} | null}. Returns {title, badge, rows: [[label, value]]}; a row whose value the feed lacks is left out.
  function describeUnit(u, ctx) {
    const c = ctx || {}, d = u.data || {}, rows = [];
    const add = (k, v) => { if (v != null && v !== "") rows.push([k, String(v)]); };
    const toLead = dist(d, c.lead);
    if (u.type === "mob") {
      const name = mobName(c.names, d.entry), a = mobActivity(d);
      const kind = (d.boss ? "Boss, " : "") + (REACTION[d.kind] || pretty(d.kind));
      add("Kind", kind.toLowerCase().replace(/^./, (x) => x.toUpperCase()));
      add("Level", d.level > 0 ? d.level : "");
      add("Health", d.hp_pct != null ? d.hp_pct + " %" : "");
      add("Doing", a.word);
      add("Target", d.target ? entryText(c.names, d.target) : "");
      add("Casting", d.cast);
      add("Crowd control", d.cc ? "crowd controlled (stunned, rooted, feared or asleep)" : "");
      add("Distance to lead", yd(toLead));
      add("Pack", d.group >= 0 ? "pack " + (d.group + 1) + " of this section" : d.boss ? "boss fight" : "");
      add("Entry", d.entry);
      return { title: name, badge: d.boss ? "boss" : d.kind, rows };
    }
    if (u.type === "bot" || u.type === "other") {
      const a = activity(d);
      add("Group", u.group);
      add("Class", [pretty(d.class), d.role === "dps" ? "DPS" : d.role].filter(Boolean).join(" · "));
      add("Level", d.level > 0 ? d.level : "");
      add("Health", (d.alive === false ? 0 : d.hp_pct) + " %");
      if (d.power_type) add(pretty(d.power_type), d.power_pct + " %");
      add("Doing", a.word);
      add("Target", d.target ? entryText(c.names, d.target) : "");
      add("Casting", d.cast);
      add("Distance to lead", d.lead ? "" : yd(toLead));
      return { title: (d.lead ? "★ " : "") + d.name, badge: d.lead ? "lead" : "friendly", rows };
    }
    if (u.type === "event") {
      add("When", clockOf(d.at));
      add("What", d.text);
      return { title: d.text, badge: d.kind, rows };
    }
    add("Distance to lead", yd(toLead));
    return { title: d.name || "commander", badge: "commander", rows: rows.concat([["What", "the agent's own character"]]) };
  }

  // ---- status icons ----

  // The glyph and the words of each status. Symbols from the basic Unicode blocks, so they show without an emoji font.
  const STATUS = {
    dead: { glyph: "☠", text: "dead" },
    cast: { glyph: "✦", text: "casting" },
    cc: { glyph: "🐑", text: "crowd controlled" },
    rest: { glyph: "☕", text: "resting, drinking or eating" },
    flee: { glyph: "➜", text: "fleeing" },
  };

  // The statuses to draw over a unit, in drawing order.
  function statuses(u) {
    if (u.type === "mob") return mobActivity(u.data).kinds.concat(u.data.hp_pct === 0 ? ["dead"] : []);
    if (u.type === "bot" || u.type === "other") return activity(u.data).kinds;
    return [];
  }

  // ---- threat colours ----

  // Whom a creature attacks: "tank" (a bot whose role is tank), "other" (any other bot) or "" (nobody, or somebody off the list).
  function threatOf(mob, bots) {
    const k = guidKey(mob.target_guid);
    if (!k) return "";
    const b = (bots || []).find((x) => guidKey(x.guid) === k);
    if (!b) return "";
    return b.role === "tank" ? "tank" : "other";
  }

  // The guids (as keys) of the creatures the tank has targeted; the lead is the fallback for a party without a tank role.
  function tankTargets(bots) {
    const tanks = (bots || []).filter((b) => b.role === "tank" && b.alive !== false);
    return new Set(tanks.map((b) => guidKey(b.target_guid)).filter(Boolean));
  }

  // Health bar colour: green above 60 %, amber above 30 %, else red.
  const hpColor = (p) => (p > 60 ? "#3ddc84" : p > 30 ? "#ffb020" : "#ff4d4d");

  // ---- events ----

  const EVENT_GLYPH = { death: "☠", wipe: "☠", boss: "♛", section: "⚑", open: "⌂", loot: "◆", use: "⌂" };
  const eventGlyph = (kind) => EVENT_GLYPH[kind] || "•";
  const pad2 = (n) => String(n).padStart(2, "0");
  // "14:05:09" of an event's ISO time, "" for a zero or bad time. tz "utc" is for tests.
  function clockOf(iso, tz) {
    const t = Date.parse(iso);
    if (!isFinite(t) || t <= Date.parse("0002-01-01T00:00:00Z")) return "";
    const d = new Date(t);
    return tz === "utc" ? pad2(d.getUTCHours()) + ":" + pad2(d.getUTCMinutes()) + ":" + pad2(d.getUTCSeconds())
      : pad2(d.getHours()) + ":" + pad2(d.getMinutes()) + ":" + pad2(d.getSeconds());
  }
  // A feed line: {glyph, time, text, kind}.
  function feedLine(e, tz) { return { glyph: eventGlyph(e.kind), time: clockOf(e.at, tz), text: e.text, kind: e.kind }; }
  // The newest n events, newest first.
  function feedLines(events, n, tz) { return (events || []).slice(-(n == null ? 8 : n)).reverse().map((e) => feedLine(e, tz)); }
  // The events that get a pin on the map.
  const pinnedEvents = (events) => (events || []).filter((e) => e.pos && isFinite(e.x) && isFinite(e.y));

  // ---- the run: timer, section, bosses ----

  const clock = (sec) => { const s = Math.max(0, Math.round(sec)); const h = Math.floor(s / 3600); return (h ? h + ":" + pad2(Math.floor(s / 60) % 60) : Math.floor(s / 60)) + ":" + pad2(s % 60); };

  // How far the run has come. geo is the plan's drawing (legs with their boss entry, names), events the card's list.
  // {elapsed, section: {n, total, label}, pack: {n, total}|null, bosses: [{name, done}], done, total, next}; null when there is no run.
  function progress(c, geo, events) {
    const r = c && c.run;
    if (!r || r.state === "idle" || !(r.leg >= 0 || r.minutes > 0)) return null;
    const legs = (geo && geo.legs) || [], names = (geo && geo.names) || {};
    const total = r.leg_count || legs.length || 0;
    const label = r.leg >= 0 && legs[r.leg] ? legs[r.leg].label : r.leg_name ? pretty(r.leg_name) : "";
    const killed = new Set((events || []).filter((e) => e.kind === "boss").map((e) => e.text));
    const bosses = legs.filter((l) => l.boss).map((l) => {
      const name = names[String(l.boss)] || "boss " + l.boss;
      const done = killed.has(name + " killed") || (r.leg >= 0 && l.index < r.leg) || (r.state === "done");
      return { name, done };
    });
    const doneN = bosses.filter((b) => b.done).length, next = bosses.find((b) => !b.done);
    return {
      elapsed: clock((r.minutes || 0) * 60),
      section: r.leg >= 0 ? { n: r.leg + 1, total, label } : null,
      pack: r.group > 0 ? { n: r.group, total: r.groups || 0 } : null,
      bosses, done: doneN, total: bosses.length, next: next ? next.name : "",
    };
  }

  // The progress as one line: "18:42 · section 3 of 7, Sentinel room · bosses 1 of 3 · next: Revelosh".
  function progressLine(p) {
    if (!p) return "";
    const parts = [p.elapsed];
    if (p.section) parts.push("section " + p.section.n + (p.section.total ? " of " + p.section.total : "") + (p.section.label ? ", " + p.section.label : ""));
    if (p.pack) parts.push("pack " + p.pack.n + (p.pack.total ? " of " + p.pack.total : ""));
    if (p.total) parts.push("bosses " + p.done + " of " + p.total + (p.next ? " · next: " + p.next : p.done === p.total ? " · all down" : ""));
    return parts.join(" · ");
  }

  // ---- the legend ----

  // What the marks mean; the page draws a sample from `sample` (a css class of the sample) next to the words.
  const LEGEND = [
    { sample: "lg-boss", glyph: "♛", text: "boss: bigger ring, crown and its name always shown" },
    { sample: "lg-hp", glyph: "", text: "health bar over a unit (green, amber under 60 %, red under 30 %)" },
    { sample: "lg-dead", glyph: STATUS.dead.glyph, text: "dead" },
    { sample: "lg-cast", glyph: STATUS.cast.glyph, text: "casting a spell" },
    { sample: "lg-cc", glyph: STATUS.cc.glyph, text: "crowd controlled (sheep)" },
    { sample: "lg-rest", glyph: STATUS.rest.glyph, text: "resting, drinking or eating" },
    { sample: "lg-flee", glyph: STATUS.flee.glyph, text: "fleeing" },
    { sample: "lg-thr-tank", glyph: "", text: "blue ring: the creature attacks the tank" },
    { sample: "lg-thr-other", glyph: "", text: "red ring: the creature attacks someone else" },
    { sample: "lg-thr-target", glyph: "", text: "white outline: the tank's current target" },
    { sample: "lg-pull", glyph: "", text: "dashed yellow ring: the pull target" },
    { sample: "lg-event", glyph: EVENT_GLYPH.death + EVENT_GLYPH.boss + EVENT_GLYPH.open, text: "pins: death or wipe, boss killed, door or chest opened or looted" },
  ];

  const api = { guidKey, mobName, entryText, activity, mobActivity, describeUnit, statuses, STATUS, threatOf, tankTargets, hpColor, eventGlyph, clockOf, feedLine,
    feedLines, pinnedEvents, clock, progress, progressLine, LEGEND, dist };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.UnitInfo = api;
})(typeof self !== "undefined" ? self : this);
